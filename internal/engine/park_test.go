package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/wire"
	"github.com/nevindra/bites/internal/ws"
)

// Parking (park.go, ADR 0005). Parking needs a real socket and epoll, so these tests run over
// loopback TCP on the real clock. A test that is about behaviour every idle connection must keep
// runs twice: "parked" with parking on, "moved" with NoPark, the path of a platform with no poller.

type parkPath struct {
	name   string
	noPark bool
}

var parkPaths = []parkPath{{"parked", false}, {"moved", true}}

func needPoller(t *testing.T, p parkPath) {
	t.Helper()
	if !p.noPark && runtime.GOOS != "linux" {
		t.Skip("parking needs Linux")
	}
}

// parkServer serves h on a loopback listener with the real clock.
func parkServer(t *testing.T, h handlerFunc, mod func(*Options)) (*Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	o := Options{Logger: quietLogger()}
	if mod != nil {
		mod(&o)
	}
	srv := New(h, o)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, ln.Addr().String()
}

func dialTCP(t *testing.T, addr string) *client {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return &client{t: t, c: nc, br: bufio.NewReader(nc)}
}

// theConn is the server's one connection.
func theConn(t *testing.T, s *Server) *conn {
	t.Helper()
	var c *conn
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		c = s.conns
		return c != nil
	}, "a connection")
	return c
}

func waitParked(t *testing.T, c *conn) {
	t.Helper()
	waitFor(t, func() bool { return c.pk.Load() == pkParked }, "the connection to park")
}

func waitNoConns(t *testing.T, s *Server) {
	t.Helper()
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.nconns == 0
	}, "every connection to end")
}

// stillOpen reports whether the server has neither closed nor written to the connection within d.
func (c *client) stillOpen(d time.Duration) bool {
	c.c.SetReadDeadline(time.Now().Add(d))
	defer c.c.SetReadDeadline(time.Time{})
	_, err := c.br.Peek(1)
	return errors.Is(err, os.ErrDeadlineExceeded)
}

func TestAnIdleConnectionParksAfterMoveAfterAndItsNextRequestsAreServed(t *testing.T) {
	needPoller(t, parkPaths[0])
	srv, addr := parkServer(t, okHandler("hello"), nil)
	cl := dialTCP(t, addr)
	for i := 0; i < 3; i++ {
		cl.send(get)
		if rp := cl.read(); rp.status != 200 || rp.body != "hello" {
			t.Fatalf("request %d: %d %q", i, rp.status, rp.body)
		}
		waitParked(t, theConn(t, srv))
		if n := srv.wheel.Load().Len(); n != 1 {
			t.Fatalf("%d nodes on the wheel, want the parked connection's idle timeout", n)
		}
	}
}

func TestAConnectionWithParkingOffNeverParksAndStillServesAfterAnIdleWait(t *testing.T) {
	srv, addr := parkServer(t, okHandler("hello"), func(o *Options) { o.NoPark = true })
	cl := dialTCP(t, addr)
	cl.send(get)
	cl.read()
	c := theConn(t, srv)
	time.Sleep(3 * moveAfter)
	if c.pk.Load() != pkOwned || c.ps != nil {
		t.Fatal("a connection parked with NoPark set")
	}
	cl.send(get)
	if rp := cl.read(); rp.status != 200 {
		t.Fatalf("status %d", rp.status)
	}
}

// The window between a moveAfter wait that read nothing and the registration is narrow, so it is
// hit by many requests sent at about moveAfter apart: a byte lost there is a request never
// answered, and read fails the test after 5 s.
func TestRequestsSentAroundTheMomentConnectionsParkAreAllAnswered(t *testing.T) {
	needPoller(t, parkPaths[0])
	_, addr := parkServer(t, okHandler("hello"), nil)
	var wg sync.WaitGroup
	for k := 0; k < 8; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			cl := dialTCP(t, addr)
			for i := 0; i < 20; i++ {
				time.Sleep(moveAfter - 10*time.Millisecond + time.Duration((i*7+k*3)%20)*time.Millisecond)
				cl.send(get)
				if rp := cl.readNow(); rp.err != nil || rp.status != 200 || rp.body != "hello" {
					t.Errorf("connection %d request %d: %+v", k, i, rp)
					return
				}
			}
		}(k)
	}
	wg.Wait()
}

func TestConcurrentWakesOfAParkedConnectionStartOneGoroutine(t *testing.T) {
	var started atomic.Int32
	c := &conn{}
	c.ps = &parkState{c: c, resumeFn: func() { started.Add(1) }}
	c.pk.Store(pkParked)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.wake() }()
	}
	wg.Wait()
	waitFor(t, func() bool { return started.Load() >= 1 }, "the resumed goroutine")
	time.Sleep(10 * time.Millisecond)
	if n := started.Load(); n != 1 || c.pk.Load() != pkOwned {
		t.Fatalf("%d goroutines started, pk = %d; want 1 and owned", n, c.pk.Load())
	}
	c.wake() // owned: nothing happens
	if started.Load() != 1 {
		t.Fatal("a wake of an owned connection started a goroutine")
	}
}

func TestAWakeWhileAConnectionRegistersLeavesItWithTheRegisteringGoroutine(t *testing.T) {
	var started atomic.Int32
	c := &conn{}
	c.ps = &parkState{c: c, resumeFn: func() { started.Add(1) }}
	c.pk.Store(pkParking)
	c.wake()
	c.wake()
	if c.pk.Load() != pkWoken || started.Load() != 0 {
		t.Fatalf("pk = %d, %d started; want woken and none", c.pk.Load(), started.Load())
	}
	if c.pk.CompareAndSwap(pkParking, pkParked) {
		t.Fatal("the registering goroutine could still give the connection away")
	}
}

func TestAnIdleConnectionIsClosedAtItsIdleTimeoutAndNotBeforeParkedOrMoved(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr := parkServer(t, okHandler("x"), func(o *Options) { o.IdleTimeout = 1500 * time.Millisecond; o.NoPark = p.noPark })
			cl := dialTCP(t, addr)
			cl.send(get)
			cl.read()
			start := time.Now()
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			if !cl.stillOpen(1200*time.Millisecond - time.Since(start)) {
				t.Fatal("closed before the idle timeout")
			}
			cl.expectClosed() // the wheel's tick is 1 s, so by 2.5 s at the latest
			if d := time.Since(start); d > 4*time.Second {
				t.Fatalf("closed after %v", d)
			}
			waitNoConns(t, srv)
		})
	}
}

func TestShutdownClosesAnIdleConnectionAtOnceParkedOrMoved(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr := parkServer(t, okHandler("x"), func(o *Options) { o.NoPark = p.noPark })
			cl := dialTCP(t, addr)
			cl.send(get)
			cl.read()
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			} else {
				time.Sleep(2 * moveAfter)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			if err := srv.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("Shutdown took %v with only an idle connection", d)
			}
			cl.expectClosed()
		})
	}
}

func TestCloseEndsAnIdleConnectionAndGivesBackItsPlaceParkedOrMoved(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr := parkServer(t, okHandler("x"), func(o *Options) { o.NoPark = p.noPark; o.MaxConns = 1 })
			cl := dialTCP(t, addr)
			cl.send(get)
			cl.read()
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			srv.Close()
			cl.expectClosed()
			waitNoConns(t, srv)
			waitFor(t, func() bool { return len(srv.sem) == 0 }, "every MaxConns place to be given back")
		})
	}
}

func TestAClientThatLeavesWhileItsConnectionIsIdleEndsItParkedOrMoved(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr := parkServer(t, okHandler("x"), func(o *Options) { o.NoPark = p.noPark })
			cl := dialTCP(t, addr)
			cl.send(get)
			cl.read()
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			cl.c.Close()
			waitNoConns(t, srv)
		})
	}
}

func TestAConnectionThatParkedStillHasItsContextCancelledWhenTheClientLeavesMidHandler(t *testing.T) {
	needPoller(t, parkPaths[0])
	started, cause := make(chan struct{}), make(chan error, 1)
	first := true
	srv, addr := parkServer(t, func(ex *Exchange) {
		if first {
			first = false
			okHandler("x")(ex)
			return
		}
		causeHandler(started, cause)(ex)
	}, nil)
	cl := dialTCP(t, addr)
	cl.send(get)
	cl.read()
	waitParked(t, theConn(t, srv))
	cl.send(get)
	<-started
	cl.c.Close()
	select {
	case err := <-cause:
		if err != reqctx.ErrClientGone {
			t.Fatalf("cause = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not cancelled")
	}
}

// ---- TLS ----

func tlsParkServer(t *testing.T, h handlerFunc, mod func(*Options)) (*Server, string, *tls.Config) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	scfg, ccfg := testCerts(t)
	o := Options{Logger: quietLogger()}
	if mod != nil {
		mod(&o)
	}
	srv := New(h, o)
	go srv.ServeTLS(ln, scfg)
	t.Cleanup(func() { srv.Close() })
	return srv, ln.Addr().String(), ccfg
}

// splitConn writes the next Write in two parts, the second after a pause, when split is set.
type splitConn struct {
	net.Conn
	split atomic.Bool
	pause time.Duration
}

func (s *splitConn) Write(b []byte) (int, error) {
	if !s.split.Swap(false) || len(b) < 4 {
		return s.Conn.Write(b)
	}
	n, err := s.Conn.Write(b[:3])
	if err != nil {
		return n, err
	}
	time.Sleep(s.pause)
	m, err := s.Conn.Write(b[3:])
	return n + m, err
}

func TestAParkedTLSConnectionServesItsNextRequestAndARecordSplitAcrossTheParkIsRead(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, ccfg := tlsParkServer(t, okHandler("hello"), func(o *Options) { o.NoPark = p.noPark })
			nc, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			sc := &splitConn{Conn: nc, pause: 3 * moveAfter}
			tc := tls.Client(sc, ccfg)
			t.Cleanup(func() { tc.Close() })
			cl := &client{t: t, c: tc, br: bufio.NewReader(tc)}
			cl.send(get)
			cl.read()
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			// Three bytes of a record, a pause past moveAfter (the connection waits holding a part
			// of a record, and parks again), then the rest.
			sc.split.Store(true)
			cl.send(get)
			if rp := cl.read(); rp.status != 200 || rp.body != "hello" {
				t.Fatalf("%d %q", rp.status, rp.body)
			}
		})
	}
}

func TestTwoPipelinedRequestsInTwoTLSRecordsAreBothAnsweredParkedOrMoved(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, ccfg := tlsParkServer(t, func(ex *Exchange) {
				time.Sleep(2 * moveAfter) // the second record waits inside crypto/tls meanwhile
				okHandler("hello")(ex)
			}, func(o *Options) { o.NoPark = p.noPark })
			tc, err := tls.Dial("tcp", addr, ccfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { tc.Close() })
			cl := &client{t: t, c: tc, br: bufio.NewReader(tc)}
			cl.send(get)
			cl.send(get) // a second record
			_ = srv
			for i := 0; i < 2; i++ {
				if rp := cl.read(); rp.status != 200 {
					t.Fatalf("answer %d: %d", i, rp.status)
				}
			}
		})
	}
}

// ---- WebSockets ----

// wsCatch is a WebSocket handler that hands the test its WS, echoes text and reports the close.
type wsCatch struct {
	got    chan *WS
	closed chan WSCloseInfo
}

func (h wsCatch) Message(w *WS, op ws.Opcode, msg []byte, _ *wire.Slab) error {
	return w.Send(ws.AppendFrame(nil, op, msg))
}
func (h wsCatch) Closed(_ *WS, info WSCloseInfo) { h.closed <- info }

func wsParkServer(t *testing.T, p parkPath, mod func(*Options)) (*Server, string, wsCatch) {
	t.Helper()
	h := wsCatch{got: make(chan *WS, 1), closed: make(chan WSCloseInfo, 1)}
	srv, addr := parkServer(t, func(ex *Exchange) {
		lim := ws.DefaultLimits()
		w := ex.NewWS(WSSpec{Limits: &lim})
		ex.Upgrade(w, []byte("dGhlIHNhbXBsZSBub25jZQ=="), h)
		h.got <- w
	}, func(o *Options) {
		o.NoPark = p.noPark
		if mod != nil {
			mod(o)
		}
	})
	return srv, addr, h
}

func dialWSTCP(t *testing.T, addr string) *client {
	t.Helper()
	cl := dialTCP(t, addr)
	cl.send(upgradeReq)
	if rp := cl.read(); rp.status != 101 {
		t.Fatalf("handshake answered %d", rp.status)
	}
	return cl
}

// clientFrame is a masked frame, as a client sends.
func clientFrame(fin bool, op ws.Opcode, payload string) string {
	b0 := byte(op)
	if fin {
		b0 |= 0x80
	}
	mask := [4]byte{1, 2, 3, 4}
	out := []byte{b0, 0x80 | byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	for i := 0; i < len(payload); i++ {
		out = append(out, payload[i]^mask[i&3])
	}
	return string(out)
}

// frame reads one server frame (unmasked, short).
func (c *client) frame(d time.Duration) (ws.Opcode, string, error) {
	c.c.SetReadDeadline(time.Now().Add(d))
	defer c.c.SetReadDeadline(time.Time{})
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return 0, "", err
	}
	p := make([]byte, h[1]&0x7f)
	if _, err := io.ReadFull(c.br, p); err != nil {
		return 0, "", err
	}
	return ws.Opcode(h[0] & 0x0f), string(p), nil
}

func waitClosed(t *testing.T, h wsCatch) WSCloseInfo {
	t.Helper()
	select {
	case info := <-h.closed:
		return info
	case <-time.After(10 * time.Second):
		t.Fatal("OnClose was not called")
		return WSCloseInfo{}
	}
}

func TestAParkedWebSocketIsWokenByAMessageAndASendFromAnotherGoroutineReachesIt(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, h := wsParkServer(t, p, nil)
			cl := dialWSTCP(t, addr)
			w := <-h.got
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			if err := w.Send(ws.AppendFrame(nil, ws.OpText, []byte("from elsewhere"))); err != nil {
				t.Fatal(err)
			}
			if op, msg, err := cl.frame(5 * time.Second); err != nil || op != ws.OpText || msg != "from elsewhere" {
				t.Fatalf("%v %q %v", op, msg, err)
			}
			cl.send(clientFrame(true, ws.OpText, "echo me"))
			if op, msg, err := cl.frame(5 * time.Second); err != nil || op != ws.OpText || msg != "echo me" {
				t.Fatalf("%v %q %v", op, msg, err)
			}
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
		})
	}
}

func TestAParkedWebSocketIsPingedOnTheWheelAndClosedWith1008WhenItNeverAnswers(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, h := wsParkServer(t, p, func(o *Options) {
				o.WS.HeartbeatInterval, o.WS.MissedPongs, o.WS.CloseTimeout = time.Second, 1, time.Second
			})
			cl := dialWSTCP(t, addr)
			<-h.got
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			if op, _, err := cl.frame(5 * time.Second); err != nil || op != ws.OpPing {
				t.Fatalf("want a ping, got %v %v", op, err)
			}
			if st, err := cl.closeStatus(); err != nil || st != ws.ClosePolicy {
				t.Fatalf("close status %d, %v", st, err)
			}
			// The client never answers the Close: the close deadline on the wheel drops the socket,
			// and its goroutine (woken if parked) runs OnClose.
			if info := waitClosed(t, h); info.Status != ws.ClosePolicy {
				t.Fatalf("OnClose saw %d", info.Status)
			}
			waitNoConns(t, srv)
		})
	}
}

func TestAParkedWebSocketThatAnswersItsPingsStaysOpenAndParksAgainBetweenThem(t *testing.T) {
	needPoller(t, parkPaths[0])
	srv, addr, _ := wsParkServer(t, parkPaths[0], func(o *Options) { o.WS.HeartbeatInterval, o.WS.MissedPongs = time.Second, 1 })
	cl := dialWSTCP(t, addr)
	c := theConn(t, srv)
	for i := 0; i < 2; i++ {
		waitParked(t, c)
		if op, _, err := cl.frame(5 * time.Second); err != nil || op != ws.OpPing {
			t.Fatalf("ping %d: %v %v", i, op, err)
		}
		cl.send(clientFrame(true, ws.OpPong, ""))
	}
	waitParked(t, c)
	if c.state.Load() != stWS {
		t.Fatal("the socket did not stay open")
	}
}

func TestShutdownSendsAParkedWebSocketItsClose1001AndOnCloseSeesIt(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, h := wsParkServer(t, p, func(o *Options) { o.WS.CloseTimeout = time.Second })
			cl := dialWSTCP(t, addr)
			<-h.got
			if !p.noPark {
				waitParked(t, theConn(t, srv))
			}
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done <- srv.Shutdown(ctx)
			}()
			if st, err := cl.closeStatus(); err != nil || st != ws.CloseGoingAway {
				t.Fatalf("close status %d, %v", st, err)
			}
			cl.send(clientFrame(true, ws.OpClose, "\x03\xe9")) // the client answers 1001
			if info := waitClosed(t, h); info.Status != ws.CloseGoingAway || info.Remote {
				t.Fatalf("OnClose saw %+v", info)
			}
			if err := <-done; err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

func TestAWebSocketWithAMessageHalfReceivedIsNeverParkedAndItsMessageTimeoutHolds(t *testing.T) {
	for _, p := range parkPaths {
		t.Run(p.name, func(t *testing.T) {
			needPoller(t, p)
			t.Parallel()
			srv, addr, h := wsParkServer(t, p, func(o *Options) { o.WS.MessageTimeout = 1500 * time.Millisecond })
			cl := dialWSTCP(t, addr)
			<-h.got
			c := theConn(t, srv)
			if !p.noPark {
				waitParked(t, c)
			}
			cl.send(clientFrame(false, ws.OpText, "first half"))
			time.Sleep(4 * moveAfter)
			if c.pk.Load() == pkParked {
				t.Fatal("a socket assembling a message parked")
			}
			if st, err := cl.closeStatus(); err != nil || st != ws.ClosePolicy {
				t.Fatalf("close status %d, %v", st, err)
			}
			if info := waitClosed(t, h); info.Reason != "message timeout" {
				t.Fatalf("OnClose saw %+v", info)
			}
		})
	}
}
