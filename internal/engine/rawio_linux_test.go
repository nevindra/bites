//go:build linux

package engine

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

// smallSendBuffer hands the engine each accepted *net.TCPConn itself, so its answers are written
// raw, with a send buffer of a few KiB, so an answer of tens of KiB cannot fit in it at once.
type smallSendBuffer struct{ net.Listener }

func (l smallSendBuffer) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetWriteBuffer(4096)
	}
	return c, err
}

// dialSmall dials addr with a receive buffer of a few KiB, set before the connection is made so
// the window it advertises stays that small.
func dialSmall(t *testing.T, addr string) net.Conn {
	t.Helper()
	d := net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		return rc.Control(func(fd uintptr) { syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 4096) })
	}}
	nc, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return nc
}

func rawServer(t *testing.T, h handlerFunc, mod func(*Options)) (*Server, string) {
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
	go srv.Serve(smallSendBuffer{ln})
	t.Cleanup(func() { srv.Close() })
	return srv, ln.Addr().String()
}

func TestABareTCPSocketHasARawConnAndAWrappedOneHasNone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if rawConnOf(c) == nil {
		t.Error("a bare *net.TCPConn has no RawConn: its answers would not be written raw")
	}
	if rawConnOf(&proxyConn{Conn: c}) != nil {
		t.Error("a PROXY protocol connection has a RawConn: a raw read would skip the bytes it holds")
	}
}

func TestAKeepAliveTCPConnectionServesEachRequestAndAPipelinedBatchThroughTheRawPath(t *testing.T) {
	_, addr := rawServer(t, okHandler("hello"), nil)
	cl := dialTCP(t, addr)
	for i := range 200 {
		cl.send(get)
		if rp := cl.read(); rp.status != 200 || rp.body != "hello" {
			t.Fatalf("request %d: %+v", i, rp)
		}
	}
	cl.send(strings.Repeat(get, 16))
	for i := range 16 {
		if rp := cl.read(); rp.status != 200 || rp.body != "hello" {
			t.Fatalf("pipelined request %d: %+v", i, rp)
		}
	}
}

func TestARawAnswerThatFindsTheSendBufferFullWaitsForRoomAndArrivesWhole(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 2<<10) // 32 KiB: one raw write, eight send buffers
	_, addr := rawServer(t, okHandler(string(body)), nil)
	nc := dialSmall(t, addr)
	for i := range 3 {
		nc.Write([]byte(get))
		time.Sleep(20 * time.Millisecond) // the server's write fills both buffers and waits
		nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(nc)
		cl := &client{t: t, c: nc, br: br}
		rp := cl.read()
		if rp.status != 200 || rp.body != string(body) {
			t.Fatalf("answer %d: status %d, %d of %d body bytes, intact %v", i, rp.status, len(rp.body), len(body), rp.body == string(body))
		}
		if br.Buffered() != 0 {
			t.Fatalf("answer %d: %d bytes past the body", i, br.Buffered())
		}
	}
}

func TestARawAnswerToAClientThatStopsReadingEndsAtTheWriteDeadline(t *testing.T) {
	body := strings.Repeat("x", 32<<10)
	srv, addr := rawServer(t, okHandler(body), func(o *Options) { o.WriteTimeout = 150 * time.Millisecond })
	nc := dialSmall(t, addr)
	// Ten pipelined requests and nothing read: ten answers of 32 KiB are more than both socket
	// buffers hold, so a write waits for room until its deadline, and the connection is closed.
	const n = 10
	nc.Write([]byte(strings.Repeat(get, n)))
	conns := func() int { srv.mu.Lock(); defer srv.mu.Unlock(); return srv.nconns }
	waitFor(t, func() bool { return conns() == 1 }, "the server to accept the connection")
	waitFor(t, func() bool { return conns() == 0 }, "the server to close the connection a write could not finish")
	nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, _ := io.ReadAll(nc)
	if len(got) >= n*len(body) {
		t.Fatalf("the client got %d bytes, all %d answers, from writes that timed out", len(got), n)
	}
}
