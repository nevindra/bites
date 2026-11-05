package engine

import (
	"bufio"
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

// The PROXY protocol (proxy.go, ADR 0058).

const pxV1 = "PROXY TCP4 192.0.2.7 198.51.100.1 56324 443\r\n"

var balancers = netip.MustParsePrefix("203.0.113.0/24")

func trustBalancers() *ProxyProtocol {
	return &ProxyProtocol{Trusts: balancers.Contains}
}

// peerHandler answers with the request's peer as the engine sees it.
func peerHandler() handlerFunc {
	return func(ex *Exchange) {
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		*r.Body() = append(*r.Body(), ex.RemoteAddrPort().String()+" "+ex.RemoteAddr().String()...)
	}
}

// newProxyRig is newRig serving a PROXY protocol listener, TLS too when tlsCfg is set.
func newProxyRig(t *testing.T, h handlerFunc, px *ProxyProtocol, tlsCfg *tls.Config, mod func(*Options)) *rig {
	t.Helper()
	clk := clock.NewFake(epoch)
	o := Options{Clock: clk, Logger: quietLogger(), HeadTimeout: 7 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second, HandshakeTimeout: 10 * time.Second}
	if mod != nil {
		mod(&o)
	}
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	r.srv = New(h, o)
	go func() { r.done <- r.srv.ServeWith(r.ln, tlsCfg, px) }()
	t.Cleanup(func() { r.srv.Close() })
	return r
}

func (r *rig) dialFrom(addr string) *client {
	r.t.Helper()
	c, err := r.ln.Dial(memconn.Addr{Name: addr})
	if err != nil {
		r.t.Fatal(err)
	}
	return &client{t: r.t, c: c, br: bufio.NewReader(c)}
}

func TestAPROXYHeaderFromATrustedBalancerMakesItsClientThePeerOfEveryRequestOnTheConnection(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	c := r.dial() // 203.0.113.9, a balancer
	c.send(pxV1 + get)
	for i := 0; i < 3; i++ {
		if i > 0 {
			c.send(get)
		}
		if rp := c.read(); rp.status != 200 || rp.body != "192.0.2.7:56324 192.0.2.7:56324" {
			t.Fatalf("request %d: %d %q", i, rp.status, rp.body)
		}
	}
}

func TestAVersion2HeaderIsReadAndALocalOneLeavesTheBalancerAsThePeer(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	proxied := string(v2Header(0x21, 0x11, []byte{192, 0, 2, 8, 10, 0, 0, 1, 0x1f, 0x90, 0x01, 0xbb}))
	c := r.dial()
	c.send(proxied + get)
	if rp := c.read(); rp.body != "192.0.2.8:8080 192.0.2.8:8080" {
		t.Fatalf("PROXY over TCP4: %q", rp.body)
	}
	local := string(v2Header(0x20, 0x00, nil))
	c2 := r.dial()
	c2.send(local + get)
	if rp := c2.read(); rp.body != "203.0.113.9:4000 203.0.113.9:4000" {
		t.Fatalf("LOCAL: %q, want the balancer itself", rp.body)
	}
}

func v2Header(verCmd, fam byte, addr []byte) []byte {
	b := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A, verCmd, fam, 0, byte(len(addr))}
	return append(b, addr...)
}

func TestAPeerThatIsNotATrustedProxyIsClosedAtAcceptAndGivesItsPlaceBack(t *testing.T) {
	var logs lockedBuf
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, func(o *Options) {
		o.MaxConns = 1
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	for i := 0; i < 3; i++ {
		c := r.dialFrom("198.51.100.20:5000") // a client that reached the port directly
		// Claiming to be anybody, and never read: the peer is closed at accept, so the write may
		// already find the connection closed, which is the refusal this test wants.
		c.c.Write([]byte(pxV1 + get))
		c.expectClosed()
	}
	if n := strings.Count(logs.String(), "not a trusted proxy"); n != 1 {
		t.Fatalf("%d Warn lines for three untrusted peers within a minute, want 1:\n%s", n, logs.String())
	}
	c := r.dial() // MaxConns is 1: the place came back each time
	c.send(pxV1 + get)
	if rp := c.read(); rp.status != 200 {
		t.Fatalf("a trusted balancer after three refused peers: %d", rp.status)
	}
}

func TestATrustedPeerThatSendsNoPROXYHeaderIsClosedWithNothingWrittenAndAWarn(t *testing.T) {
	var logs lockedBuf
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	})
	c := r.dial()
	c.send(get) // a balancer not configured to send the header
	c.expectClosed()
	if !strings.Contains(logs.String(), "did not start its connection with a PROXY header") {
		t.Fatalf("no Warn:\n%s", logs.String())
	}
}

func TestAMalformedPROXYHeaderClosesTheConnectionWithNothingWritten(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	for _, bad := range []string{
		"PROXY TCP4 192.0.2.7 198.51.100.1 99999 443\r\n" + get,
		"PROXY TCP4 192.0.2.7 198.51.100.1 1 2\n" + get,
		string(v2Header(0x21, 0x12, make([]byte, 12))) + get, // a UDP source
	} {
		c := r.dial()
		c.send(bad)
		c.expectClosed()
	}
}

func TestAPROXYHeaderThatStallsAfterItsFirstByteIsClosedAtTheHeadTimeout(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	c := r.dial()
	c.send("PROXY TCP4 192.0.2.7")
	waitFor(t, func() bool { return c.c.(*memconn.Conn).Unconsumed() == 0 }, "the server to read the partial header")
	waitNext(t, r.clk, epoch.Add(7*time.Second))
	r.clk.Advance(7 * time.Second)
	c.expectClosed()
}

func TestAConnectionWaitingForItsPROXYHeaderIsIdleUntilItsFirstByteAndClosedAtTheIdleTimeout(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	c := r.dial()
	waitNext(t, r.clk, epoch.Add(moveAfter))
	r.clk.Advance(moveAfter) // the in-memory connection cannot park: its wait moves
	waitNext(t, r.clk, epoch.Add(60*time.Second))
	r.clk.Advance(60*time.Second - moveAfter)
	c.expectClosed()
}

func TestAHeaderSentAloneIsFollowedByAnIdleWaitForTheFirstRequest(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	c := r.dial()
	c.send(pxV1) // HAProxy writes the header the moment it connects
	waitFor(t, func() bool { return c.c.(*memconn.Conn).Unconsumed() == 0 }, "the server to read the header")
	conn := theConn(t, r.srv)
	waitFor(t, func() bool { return conn.state.Load() == stIdle }, "the connection to wait idle")
	c.send(get)
	if rp := c.read(); rp.body != "192.0.2.7:56324 192.0.2.7:56324" {
		t.Fatalf("%q", rp.body)
	}
}

func TestShutdownClosesAConnectionStillWaitingForItsPROXYHeader(t *testing.T) {
	r := newProxyRig(t, peerHandler(), trustBalancers(), nil, nil)
	c := r.dial()
	conn := theConn(t, r.srv)
	waitFor(t, func() bool { return conn.state.Load() == stIdle }, "the connection to wait idle")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	c.expectClosed()
}

// prefixConn writes pre before the first Write, in the same call, so the server reads the PROXY
// header and the start of the ClientHello in one read.
type prefixConn struct {
	net.Conn
	pre []byte
}

func (p *prefixConn) Write(b []byte) (int, error) {
	if p.pre != nil {
		_, err := p.Conn.Write(append(p.pre, b...))
		p.pre = nil
		return len(b), err
	}
	return p.Conn.Write(b)
}

func TestOverTLSThePROXYHeaderComesBeforeTheHandshakeAndTheBytesReadPastItReachCryptoTLS(t *testing.T) {
	scfg, ccfg := testCerts(t)
	r := newProxyRig(t, func(ex *Exchange) {
		if !ex.IsTLS() {
			t.Error("the request is not seen as TLS")
		}
		peerHandler()(ex)
	}, trustBalancers(), scfg, nil)
	raw, err := r.ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(&prefixConn{Conn: raw, pre: []byte(pxV1)}, ccfg)
	c := &client{t: t, c: tc, br: bufio.NewReader(tc)}
	c.send(get)
	if rp := c.read(); rp.body != "192.0.2.7:56324 192.0.2.7:56324" {
		t.Fatalf("%q", rp.body)
	}
	c.send(get)
	if rp := c.read(); rp.status != 200 {
		t.Fatalf("second request: %d", rp.status)
	}
}

func TestOverTLSAPROXYHeaderThatNeverComesIsClosedAtTheHandshakeTimeout(t *testing.T) {
	scfg, _ := testCerts(t)
	r := newProxyRig(t, peerHandler(), trustBalancers(), scfg, nil)
	c := r.dial()
	waitNext(t, r.clk, epoch.Add(10*time.Second))
	r.clk.Advance(10 * time.Second)
	c.expectClosed()
}

func TestOverTLSAClientHelloWithNoPROXYHeaderIsClosed(t *testing.T) {
	scfg, ccfg := testCerts(t)
	r := newProxyRig(t, peerHandler(), trustBalancers(), scfg, nil)
	raw, err := r.ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tc := tls.Client(raw, ccfg)
	done := make(chan error, 1)
	go func() { done <- tc.Handshake() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the handshake succeeded without a PROXY header")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection was not closed")
	}
}

func TestAConnectionWaitingForItsPROXYHeaderParksAndIsServedWhenItComes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("parking needs Linux")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	srv := New(peerHandler(), Options{Logger: quietLogger()})
	go srv.ServeWith(ln, nil, &ProxyProtocol{Trusts: netip.MustParsePrefix("127.0.0.0/8").Contains})
	t.Cleanup(func() { srv.Close() })
	cl := dialTCP(t, ln.Addr().String())
	conn := theConn(t, srv)
	waitParked(t, conn) // nothing sent yet: the wait for the header is an idle wait
	cl.send(pxV1)
	waitParked(t, conn) // the header alone: the wait for the first request parks too
	for i := 0; i < 2; i++ {
		cl.send(get)
		if rp := cl.read(); rp.body != "192.0.2.7:56324 192.0.2.7:56324" {
			t.Fatalf("request %d: %q", i, rp.body)
		}
		waitParked(t, conn)
	}
	cl.c.Close()
	waitNoConns(t, srv)
}

func TestAUnixSocketPeerSendsAPROXYHeaderOnlyWhenUnixPeersAreTrusted(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Unix socket paths are Linux's")
	}
	for _, trusted := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "app.sock")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Skip("no Unix socket:", err)
		}
		srv := New(handlerFunc(func(ex *Exchange) {
			r := ex.Respond(200)
			r.Header("Content-Type", "text/plain")
			unix := "tcp"
			if ex.PeerIsUnix() {
				unix = "unix"
			}
			*r.Body() = append(*r.Body(), ex.RemoteAddrPort().String()+" "+unix...)
		}), Options{Logger: quietLogger()})
		go srv.ServeWith(ln, nil, &ProxyProtocol{Unix: trusted})
		cl := dialUnix(t, path)
		if !trusted {
			// Closed at accept, so the write may already find the connection closed.
			cl.c.Write([]byte(pxV1 + get))
			cl.expectGone() // closed unread, so the kernel may answer the bytes sent with a reset
			srv.Close()
			continue
		}
		cl.send(pxV1 + get)
		if rp := cl.read(); rp.body != "192.0.2.7:56324 tcp" {
			t.Fatalf("named: %q", rp.body)
		}
		cl2 := dialUnix(t, path)
		cl2.send(string(v2Header(0x20, 0x00, nil)) + get)
		if rp := cl2.read(); rp.body != "invalid AddrPort unix" {
			t.Fatalf("LOCAL over a Unix socket: %q", rp.body)
		}
		srv.Close()
	}
}

func TestTheConnectionStructKeepsItsSizeClassWithThePROXYProtocolState(t *testing.T) {
	// 416 bytes is a size class of Go's allocator: the state of the PROXY protocol took a byte of
	// padding, and any field that grows the struct moves every connection to the 448 class.
	if n := unsafe.Sizeof(conn{}); n > 416 {
		t.Fatalf("conn is %d bytes, past the 416-byte size class it was in before ADR 0058", n)
	}
	if n := unsafe.Sizeof(proxyConn{}); n > 80 {
		t.Fatalf("proxyConn is %d bytes, past the 80-byte size class ADR 0058 records", n)
	}
}

// expectGone fails unless the connection ends, by EOF or a reset, within five real seconds and
// with no byte of an answer.
func (c *client) expectGone() {
	c.t.Helper()
	ch := make(chan error, 1)
	go func() { _, err := c.br.ReadByte(); ch <- err }()
	select {
	case err := <-ch:
		if err == nil {
			c.t.Fatal("the server answered")
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("connection not closed in 5 s")
	}
}
