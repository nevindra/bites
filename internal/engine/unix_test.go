package engine

import (
	"bufio"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/reqctx"
)

// Unix socket listeners (ADR 0068): served, parked and watched for a hangup exactly as a TCP
// connection is, with no peer address.

func unixServer(t *testing.T, h handlerFunc, mod func(*Options)) (*Server, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the parking and watching paths of a Unix socket are Linux's")
	}
	path := filepath.Join(t.TempDir(), "app.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skip("no Unix socket:", err)
	}
	o := Options{Logger: quietLogger()}
	if mod != nil {
		mod(&o)
	}
	srv := New(h, o)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, path
}

func dialUnix(t *testing.T, path string) *client {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return &client{t: t, c: nc, br: bufio.NewReader(nc)}
}

func TestAUnixConnectionParksWhenIdleAndItsNextRequestsAreServed(t *testing.T) {
	srv, path := unixServer(t, okHandler("hello"), nil)
	cl := dialUnix(t, path)
	for i := 0; i < 3; i++ {
		cl.send(get)
		if rp := cl.read(); rp.status != 200 || rp.body != "hello" {
			t.Fatalf("request %d: %d %q", i, rp.status, rp.body)
		}
		waitParked(t, theConn(t, srv))
	}
	cl.c.Close()
	waitNoConns(t, srv)
}

func TestAParkedUnixConnectionIsClosedByShutdown(t *testing.T) {
	srv, path := unixServer(t, okHandler("hello"), nil)
	cl := dialUnix(t, path)
	cl.send(get)
	cl.read()
	waitParked(t, theConn(t, srv))
	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(t.Context()) }()
	cl.expectClosed()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return")
	}
}

func TestAUnixClientThatLeavesMidHandlerCancelsTheContextThroughTheEpollWatcher(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	srv, path := unixServer(t, causeHandler(started, cause), nil)
	cl := dialUnix(t, path)
	cl.send(get)
	<-started
	w := theConn(t, srv).w.Load()
	if w == nil {
		t.Fatal("the connection was not armed")
	}
	w.mu.Lock()
	mode := w.mode
	w.mu.Unlock()
	if mode != modeEpoll {
		t.Fatal("a Unix connection is watched by the epoll watcher, not a reader goroutine")
	}
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

func TestAUnixConnectionHasTheZeroPeerAddress(t *testing.T) {
	got := make(chan netip.AddrPort, 1)
	_, path := unixServer(t, func(ex *Exchange) {
		got <- ex.RemoteAddrPort()
		okHandler("x")(ex)
	}, nil)
	cl := dialUnix(t, path)
	cl.send(get)
	cl.read()
	if ap := <-got; ap.IsValid() {
		t.Fatalf("peer address %v", ap)
	}
}

func TestARefusedHeadOnAUnixSocketIsAnsweredBeforeTheClose(t *testing.T) {
	_, path := unixServer(t, okHandler("x"), nil)
	cl := dialUnix(t, path)
	cl.send("GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n" + string(make([]byte, 4096)))
	if rp := cl.read(); rp.status != 400 {
		t.Fatalf("status %d", rp.status)
	}
}
