package engine

import (
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

// countingConn counts what the server does to its side of a connection: reads begun, deadline
// changes, and the last write deadline set, and whether a read is in progress.
type countingConn struct {
	net.Conn
	mu        sync.Mutex
	reads     int
	inRead    bool
	deadlines int
	lastWrite time.Time
}

func (c *countingConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.inRead = true
	c.mu.Unlock()
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.inRead = false
	c.mu.Unlock()
	return n, err
}

func (c *countingConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadlines++
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

func (c *countingConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadlines++
	c.lastWrite = t
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

func (c *countingConn) snapshot() (reads, deadlines int, lastWrite time.Time, inRead bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.deadlines, c.lastWrite, c.inRead
}

type countingListener struct {
	net.Listener
	got chan *countingConn
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	cc := &countingConn{Conn: c}
	l.got <- cc
	return cc, nil
}

// countingRig is newRig whose Server sees every connection through a countingConn.
func countingRig(t *testing.T) (*rig, *countingListener) {
	t.Helper()
	clk := clock.NewFake(epoch)
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	cl := &countingListener{Listener: r.ln, got: make(chan *countingConn, 1)}
	r.srv = New(okHandler("x"), Options{Clock: clk, Logger: quietLogger(), HeadTimeout: 7 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second})
	go func() { r.done <- r.srv.Serve(cl) }()
	t.Cleanup(func() { r.srv.Close() })
	return r, cl
}

func TestABusyKeepAliveConnectionReadsEachLaterRequestWithOneReadAndChangesNoDeadline(t *testing.T) {
	r, cl := countingRig(t)
	c := r.dial()
	sc := <-cl.got
	idle := func() bool { _, _, _, in := sc.snapshot(); return in }
	// The first two requests arm what a busy connection keeps: the write deadline, and the read
	// deadline of a hot wait (the first wait held no scratch, so it was not hot).
	for range 2 {
		c.send(get)
		c.read()
		waitFor(t, idle, "the server to wait for the next request")
	}
	reads0, dl0, _, _ := sc.snapshot()
	c.send(get)
	c.read()
	waitFor(t, idle, "the server to wait again")
	reads1, dl1, _, _ := sc.snapshot()
	// Between the two waits: the request came whole with the wait's own read, so the only read
	// begun is the next wait's. No deadline changed: the head needed no HeadTimeout, the answer
	// found its write deadline still armed, and the wait found its read deadline still armed.
	if got := reads1 - reads0; got != 1 {
		t.Errorf("reads begun for one request on a busy connection: %d, want 1", got)
	}
	if got := dl1 - dl0; got != 0 {
		t.Errorf("deadline changes for one request on a busy connection: %d, want 0", got)
	}
}

func TestAHotWaitArmsItsReadDeadlineAgainOnceHalfOfMoveAfterHasPassed(t *testing.T) {
	r, cl := countingRig(t)
	c := r.dial()
	sc := <-cl.got
	idle := func() bool { _, _, _, in := sc.snapshot(); return in }
	for range 2 {
		c.send(get)
		c.read()
		waitFor(t, idle, "the server to wait for the next request")
	}
	r.clk.Advance(moveAfter/2 + time.Millisecond)
	_, dl0, _, _ := sc.snapshot()
	c.send(get)
	c.read()
	waitFor(t, idle, "the server to wait again")
	_, dl1, _, _ := sc.snapshot()
	if got := dl1 - dl0; got != 1 {
		t.Errorf("deadline changes once less than half of moveAfter was left: %d, want 1 (the read deadline)", got)
	}
	// The wait re-armed from the request's time: it still moves the connection only after a
	// whole moveAfter of quiet, not at the deadline the earlier waits armed.
	waitNext(t, r.clk, r.clk.Now().Add(moveAfter))
}

func TestAConnectionQuietForMoveAfterGivesItsScratchBackAndClearsTheWriteDeadline(t *testing.T) {
	r, cl := countingRig(t)
	c := r.dial()
	sc := <-cl.got
	c.send(get)
	c.read()
	if _, _, w, _ := sc.snapshot(); w.IsZero() {
		t.Fatal("the answer was written with no write deadline armed")
	}
	waitNext(t, r.clk, epoch.Add(moveAfter))
	r.clk.Advance(moveAfter)
	waitFor(t, func() bool { _, _, w, _ := sc.snapshot(); return w.IsZero() }, "the write deadline to be cleared")
	c.send(get) // the moved wait still serves
	if rp := c.read(); rp.status != 200 {
		t.Fatalf("after the move: %+v", rp)
	}
}

// deepen grows the goroutine's stack by about n times 600 B.
//
//go:noinline
func deepen(n int) byte {
	var b [512]byte
	b[n%len(b)] = byte(n)
	if n == 0 {
		return b[0]
	}
	return deepen(n-1) + b[n%len(b)]
}

// padAfterCollections serves a request with h, then, n times, runs a full collection while the
// connection waits hot and serves the next request; it returns the connection's padding tier
// after each collection.
func padAfterCollections(t *testing.T, h handlerFunc, n int) []uint8 {
	t.Helper()
	clk := clock.NewFake(epoch)
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	cl := &countingListener{Listener: r.ln, got: make(chan *countingConn, 1)}
	r.srv = New(h, Options{Clock: clk, Logger: quietLogger(), HeadTimeout: 7 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second})
	go func() { r.done <- r.srv.Serve(cl) }()
	t.Cleanup(func() { r.srv.Close() })
	c := r.dial()
	sc := <-cl.got
	sv := theConn(t, r.srv)
	idle := func() bool { _, _, _, in := sc.snapshot(); return in }
	var tiers []uint8
	for i := 0; i <= n; i++ {
		c.send(get)
		c.read()
		if i > 0 {
			tiers = append(tiers, sv.pad) // written before the answer, read after it
		}
		waitFor(t, idle, "the server to wait for the next request")
		runtime.GC()
	}
	return tiers
}

func TestAHotWaitStopsTheCollectorShrinkingAStackItsRequestsGrewTo16KiB(t *testing.T) {
	tiers := padAfterCollections(t, func(ex *Exchange) {
		deepen(20)
		okHandler("x")(ex)
	}, 8)
	// Each collection that shrank the stack under a wait raised the tier; once the padding holds
	// the wait above a quarter of the stack, the tier stops rising below its cap, which it would
	// pass if a later collection still moved the stack.
	t.Logf("the tier after each collection: %v", tiers)
	last := tiers[len(tiers)-1]
	if last == 0 {
		t.Fatalf("tiers %v: the collector never shrank the stack of a 16 KiB request, so the test proves nothing", tiers)
	}
	if last >= maxPad {
		t.Fatalf("tiers %v: the padding reached its cap, so a later shrink would go unseen", tiers)
	}
	for _, p := range tiers[len(tiers)-4:] {
		if p != last {
			t.Fatalf("tiers %v: the collector still shrank the stack under a padded hot wait", tiers)
		}
	}
}

func TestAConnectionWhoseRequestsStaySmallWaitsUnderNoPadding(t *testing.T) {
	tiers := padAfterCollections(t, okHandler("x"), 4)
	for _, p := range tiers {
		if p != 0 {
			t.Fatalf("tiers %v: a request that grows no stack still got padding", tiers)
		}
	}
}
