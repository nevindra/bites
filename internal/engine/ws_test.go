package engine

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
	"github.com/nevindra/bites/internal/ws"
)

// The window this covers (an inline write cut short, its rest queued, and the queue overflowing
// before the flusher takes it) is too narrow to hit reliably over a socket, so the drop is driven
// on a WS directly.
func TestAQueueDroppedForACloseKeepsTheRestOfAFrameAlreadyPartlyWritten(t *testing.T) {
	s := &Server{}
	w := &WS{c: &conn{srv: s}}
	rest, next := []byte("the rest of a frame"), []byte("a whole frame")
	w.q = [][]byte{rest, next}
	w.qbytes = int64(len(rest) + len(next))
	s.sendUsed.Add(w.qbytes)
	w.partial, w.flushing = true, true

	w.beginClose(ws.ClosePolicy, "", WSCloseInfo{}, true, false)

	if len(w.q) != 2 || string(w.q[0]) != string(rest) {
		t.Fatalf("queue after the drop = %q, want the rest of the cut frame and then the Close", w.q)
	}
	if w.q[1][0] != 0x88 {
		t.Fatalf("the frame after the rest is %#x, want a Close (0x88)", w.q[1][0])
	}
	want := int64(len(rest) + len(w.q[1]))
	if w.qbytes != want || s.sendUsed.Load() != want {
		t.Fatalf("qbytes = %d, sendUsed = %d, want both %d: the dropped frame is given back", w.qbytes, s.sendUsed.Load(), want)
	}
}

func TestAQueueDroppedForACloseWithNoCutFrameKeepsOnlyTheClose(t *testing.T) {
	s := &Server{}
	w := &WS{c: &conn{srv: s}}
	w.q = [][]byte{[]byte("one"), []byte("two")}
	w.qbytes = 6
	s.sendUsed.Add(6)
	w.flushing = true

	w.beginClose(ws.ClosePolicy, "", WSCloseInfo{}, true, false)

	if len(w.q) != 1 || w.q[0][0] != 0x88 {
		t.Fatalf("queue after the drop = %q, want only the Close", w.q)
	}
	if w.qbytes != int64(len(w.q[0])) || s.sendUsed.Load() != w.qbytes {
		t.Fatalf("qbytes = %d, sendUsed = %d, want both %d", w.qbytes, s.sendUsed.Load(), len(w.q[0]))
	}
}

// ---- Shutdown (ADR 0029) ----

type quietWS struct{ closed chan WSCloseInfo }

func (quietWS) Message(*WS, ws.Opcode, []byte, *wire.Slab) error { return nil }
func (h quietWS) Closed(_ *WS, info WSCloseInfo)                 { h.closed <- info }

// upgrading answers every request with a WebSocket handshake.
func upgrading(h WSHandler, hold <-chan struct{}, entered ...chan struct{}) handlerFunc {
	return func(ex *Exchange) {
		lim := ws.DefaultLimits()
		w := ex.NewWS(WSSpec{Limits: &lim})
		if hold != nil {
			entered[0] <- struct{}{}
			<-hold
		}
		ex.Upgrade(w, []byte("dGhlIHNhbXBsZSBub25jZQ=="), h)
	}
}

const upgradeReq = "GET /s HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"

// dialWS opens a WebSocket on the rig and reads the 101.
func (r *rig) dialWS() *client {
	r.t.Helper()
	c := r.dial()
	c.send(upgradeReq)
	if rp := c.read(); rp.status != 101 {
		r.t.Fatalf("handshake answered %d", rp.status)
	}
	return c
}

// closeStatus reads the server's frames until a Close and returns its status.
func (c *client) closeStatus() (uint16, error) {
	for {
		var h [2]byte
		if _, err := io.ReadFull(c.br, h[:]); err != nil {
			return 0, err
		}
		p := make([]byte, h[1]&0x7f)
		if _, err := io.ReadFull(c.br, p); err != nil {
			return 0, err
		}
		if h[0]&0x0f == byte(ws.OpClose) {
			return uint16(p[0])<<8 | uint16(p[1]), nil
		}
	}
}

func waitSockets(t *testing.T, r *rig, n int) {
	t.Helper()
	waitFor(t, func() bool {
		r.srv.mu.Lock()
		defer r.srv.mu.Unlock()
		k := 0
		for c := r.srv.conns; c != nil; c = c.next {
			if c.state.Load() == stWS {
				k++
			}
		}
		return k == n
	}, "the sockets to open")
}

func TestShutdownSpreadsTheClose1001OfManyWebSocketsOverItsWindow(t *testing.T) {
	const n = 40
	h := quietWS{closed: make(chan WSCloseInfo, n)}
	r := newRig(t, upgrading(h, nil), func(o *Options) {
		o.WS.GoAwayPer = time.Second // 40 sockets: a window of 3 s, the spread's ceiling
		o.WS.GoAwaySpread = 3 * time.Second
	})
	var step atomic.Int32
	var got [8]atomic.Int32
	var seen sync.WaitGroup
	clients := make([]*client, n)
	for i := range clients {
		clients[i] = r.dialWS()
	}
	waitSockets(t, r, n)
	for _, c := range clients {
		seen.Add(1)
		go func() {
			defer seen.Done()
			if code, err := c.closeStatus(); err == nil && code == 1001 {
				got[step.Load()].Add(1)
				c.c.Write(ws.AppendClientFrame(nil, true, ws.OpClose, []byte{0x03, 0xe9}, [4]byte{1, 2, 3, 4}))
			}
		}()
	}
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	for i := range 5 {
		time.Sleep(60 * time.Millisecond)
		step.Store(int32(i + 1))
		r.clk.Advance(time.Second)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	seen.Wait()
	total, steps := 0, 0
	for i := range got {
		if v := got[i].Load(); v > 0 {
			total += int(v)
			steps++
		}
	}
	if total != n {
		t.Fatalf("%d of %d sockets were told 1001", total, n)
	}
	if steps < 2 {
		t.Fatalf("every Close 1001 went out in the same second: %d", steps)
	}
	for range n {
		if info := <-h.closed; info.Status != 1001 || info.Remote {
			t.Fatalf("OnClose saw %+v, want status 1001, not remote", info)
		}
	}
}

func TestShutdownSendsAFewWebSocketsTheirClose1001AtOnce(t *testing.T) {
	h := quietWS{closed: make(chan WSCloseInfo, 2)}
	r := newRig(t, upgrading(h, nil), nil)
	a, b := r.dialWS(), r.dialWS()
	waitSockets(t, r, 2)
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	for _, c := range []*client{a, b} {
		code, err := c.closeStatus()
		if err != nil || code != 1001 {
			t.Fatalf("status %d, %v, want 1001 with no clock moved", code, err)
		}
		c.c.Write(ws.AppendClientFrame(nil, true, ws.OpClose, []byte{0x03, 0xe9}, [4]byte{1, 2, 3, 4}))
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v: the sockets answered, so nothing was left", err)
	}
}

func TestAWebSocketThatNeverAnswersItsClose1001IsDroppedAfterCloseTimeoutAndSeesStatus1001(t *testing.T) {
	h := quietWS{closed: make(chan WSCloseInfo, 1)}
	r := newRig(t, upgrading(h, nil), nil)
	c := r.dialWS()
	waitSockets(t, r, 1)
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	if code, err := c.closeStatus(); err != nil || code != 1001 {
		t.Fatalf("status %d, %v", code, err)
	}
	waitFor(t, func() bool { return r.clk.Pending() > 0 }, "the close deadline")
	r.clk.Advance(6 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	if info := <-h.closed; info.Status != 1001 {
		t.Fatalf("OnClose saw %+v, want 1001", info)
	}
}

func TestAWebSocketWhoseHandshakeIsInFlightWhenShutdownStartsOpensAndIsToldToGo(t *testing.T) {
	h := quietWS{closed: make(chan WSCloseInfo, 1)}
	hold, entered := make(chan struct{}), make(chan struct{}, 1)
	r := newRig(t, upgrading(h, hold, entered), nil)
	c := r.dial()
	c.send(upgradeReq)
	<-entered
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	waitFor(t, func() bool { r.srv.mu.Lock(); defer r.srv.mu.Unlock(); return r.srv.leaveSet }, "Shutdown to walk")
	close(hold)
	if rp := c.read(); rp.status != 101 {
		t.Fatalf("handshake answered %d", rp.status)
	}
	if code, err := c.closeStatus(); err != nil || code != 1001 {
		t.Fatalf("status %d, %v, want 1001 straight after the 101", code, err)
	}
	c.c.Write(ws.AppendClientFrame(nil, true, ws.OpClose, []byte{0x03, 0xe9}, [4]byte{1, 2, 3, 4}))
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	if info := <-h.closed; info.Status != 1001 {
		t.Fatalf("OnClose saw %+v", info)
	}
}

func TestAWebSocketIsNotToldToGoTwiceWhenShutdownIsCalledTwice(t *testing.T) {
	h := quietWS{closed: make(chan WSCloseInfo, 1)}
	r := newRig(t, upgrading(h, nil), nil)
	c := r.dialWS()
	waitSockets(t, r, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	r.srv.Shutdown(ctx)
	r.srv.Shutdown(ctx)
	if code, err := c.closeStatus(); err != nil || code != 1001 {
		t.Fatalf("status %d, %v", code, err)
	}
	if code, err := c.closeStatus(); err == nil {
		t.Fatalf("a second Close %d followed the first", code)
	}
}

// A field added to a WS is a field of every idle WebSocket (ADR 0003): leaving is a bool that
// lands in the padding after the other flags, so the struct did not grow.
func TestTheLeavingFlagAddsNoBytesToAnIdleWebSocket(t *testing.T) {
	if got, want := unsafe.Sizeof(WS{}), uintptr(512); got != want {
		t.Fatalf("sizeof(WS) = %d, want %d: a new field costs every idle socket", got, want)
	}
}

// ---- the spread of Shutdown's Close 1001 (ADR 0029) ----

func windowFor(spread time.Duration) *Server {
	c := WSConfig{GoAwaySpread: spread}
	c.defaults()
	return &Server{o: Options{WS: c}}
}

func TestTheGoAwaySpreadWidensTheWindowOf20000SocketsFrom3sTo10sAndTheDefaultIsUnchanged(t *testing.T) {
	win, asked := windowFor(0).goAwayWindow(context.Background(), 20000)
	if win != 3*time.Second || asked != 3*time.Second {
		t.Fatalf("default window = %v (asked %v), want 3s: the default must not change", win, asked)
	}
	win, asked = windowFor(10*time.Second).goAwayWindow(context.Background(), 20000)
	if win != 10*time.Second || asked != 10*time.Second {
		t.Fatalf("window with a 10s spread = %v (asked %v), want 10s", win, asked)
	}
	win, _ = windowFor(10*time.Second).goAwayWindow(context.Background(), 5000)
	if win != 5*time.Second {
		t.Fatalf("5000 sockets under a 10s spread got %v, want 1 ms a socket, 5s", win)
	}
}

func TestHalfOfShutdownsContextStillBoundsTheGoAwayWindowAndReportsWhatWasAsked(t *testing.T) {
	s := windowFor(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	win, asked := s.goAwayWindow(ctx, 20000)
	if asked != 10*time.Second || win > 2*time.Second || win < 1900*time.Millisecond {
		t.Fatalf("window = %v, asked = %v, want about 2s of the 10s asked", win, asked)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if win, asked = s.goAwayWindow(ctx, 20000); win != asked || win != 10*time.Second {
		t.Fatalf("with 30s left the window = %v (asked %v), want the 10s asked", win, asked)
	}
}

type spreadLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *spreadLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *spreadLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func shutdownLog(t *testing.T, ctx context.Context) string {
	t.Helper()
	const n = 3
	lg := &spreadLog{}
	h := quietWS{closed: make(chan WSCloseInfo, n)}
	r := newRig(t, upgrading(h, nil), func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(lg, nil))
		o.WS.GoAwayPer = time.Second // 3 sockets: a window of 3s
		o.WS.GoAwaySpread = 10 * time.Second
	})
	for range n {
		r.dialWS()
	}
	waitSockets(t, r, n)
	go r.srv.Shutdown(ctx)
	waitFor(t, func() bool { return strings.Contains(lg.String(), "Close 1001 spread") }, "the spread to be logged")
	return lg.String()
}

func TestShutdownLogsTheWindowItSpreadTheClose1001Over(t *testing.T) {
	out := shutdownLog(t, context.Background())
	for _, want := range []string{"level=INFO", "sockets=3", "window=3s", "asked=3s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "cut short") {
		t.Fatalf("nothing was cut, yet the log says so:\n%s", out)
	}
}

func TestShutdownSaysInItsLogWhenHalfOfItsContextCutTheWindowShorterThanAsked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out := shutdownLog(t, ctx)
	for _, want := range []string{"level=WARN", "cut short by half of what is left of Shutdown's context", "asked=3s", "WithGoAwaySpread"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "window=3s") {
		t.Fatalf("the window was cut but the log shows the asked one:\n%s", out)
	}
}

func TestForgetClearsTheSlotItFreesSoTheSocketKeepsNoTopicItLeft(t *testing.T) {
	w := &WS{}
	a, b := new(int), new(int)
	w.Join(a, func(*WS) {})
	w.Join(b, func(*WS) {})
	w.Forget(a)
	if len(w.leave) != 1 || w.leave[0].key != b {
		t.Fatalf("leave %v", w.leave)
	}
	if rest := w.leave[1:cap(w.leave)]; rest[0].key != nil || rest[0].fn != nil {
		t.Fatal("the freed slot still holds the key it forgot, which keeps it alive")
	}
}
