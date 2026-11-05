//go:build linux

package reqctx

import (
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func pair(t testing.TB) (client *net.TCPConn, server *net.TCPConn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); ch <- c }()
	cc, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	sc := <-ch
	t.Cleanup(func() { cc.Close(); sc.Close() })
	return cc.(*net.TCPConn), sc.(*net.TCPConn)
}

func newWatcher(t testing.TB) *Watcher {
	t.Helper()
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func watch(t testing.TB, w *Watcher, c *net.TCPConn, f func()) uint64 {
	t.Helper()
	rc, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.Watch(rc, f)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWatcherReportsAClientCloseOnce(t *testing.T) {
	w := newWatcher(t)
	cl, sv := pair(t)
	var n atomic.Int32
	watch(t, w, sv, func() { n.Add(1) })
	cl.Close()
	waitFor(t, "hangup", func() bool { return n.Load() == 1 })
	time.Sleep(30 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("called %d times", n.Load())
	}
}

func TestWatcherIgnoresPipelinedBytesAndNeverReadsThem(t *testing.T) {
	w := newWatcher(t)
	cl, sv := pair(t)
	var n atomic.Int32
	id := watch(t, w, sv, func() { n.Add(1) })
	cl.Write([]byte("GET /next HTTP/1.1\r\n\r\n"))
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatal("data counted as a hangup")
	}
	w.Unwatch(id)
	buf := make([]byte, 64)
	sv.SetReadDeadline(time.Now().Add(time.Second))
	got, err := sv.Read(buf)
	if err != nil || string(buf[:got]) != "GET /next HTTP/1.1\r\n\r\n" {
		t.Fatalf("pipelined bytes were consumed: %q %v", buf[:got], err)
	}
}

func TestWatcherStaysQuietAfterUnwatchThenClose(t *testing.T) {
	w := newWatcher(t)
	cl, sv := pair(t)
	var n atomic.Int32
	id := watch(t, w, sv, func() { n.Add(1) })
	w.Unwatch(id)
	sv.Close()
	cl.Close()
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatal("callback after Unwatch")
	}
}

func TestWatcherStaleIdCannotUnwatchANewerRegistration(t *testing.T) {
	w := newWatcher(t)
	cl1, sv1 := pair(t)
	old := watch(t, w, sv1, func() {})
	w.Unwatch(old)
	cl2, sv2 := pair(t)
	var n atomic.Int32
	watch(t, w, sv2, func() { n.Add(1) }) // reuses the slot
	w.Unwatch(old)
	w.Unwatch(old)
	cl2.Close()
	cl1.Close()
	waitFor(t, "hangup on the newer registration", func() bool { return n.Load() == 1 })
}

func TestWatcherReportsAnAlreadyClosedPeer(t *testing.T) {
	w := newWatcher(t)
	cl, sv := pair(t)
	cl.Close()
	time.Sleep(20 * time.Millisecond)
	var n atomic.Int32
	watch(t, w, sv, func() { n.Add(1) })
	waitFor(t, "hangup", func() bool { return n.Load() == 1 })
}

func TestWatcherHandlesManyConnections(t *testing.T) {
	w := newWatcher(t)
	const total = 300
	var hung atomic.Int32
	var clients []*net.TCPConn
	for i := 0; i < total; i++ {
		cl, sv := pair(t)
		clients = append(clients, cl)
		watch(t, w, sv, func() { hung.Add(1) })
	}
	for i, cl := range clients {
		if i%2 == 0 {
			cl.Close()
		}
	}
	waitFor(t, "half hang up", func() bool { return hung.Load() == total/2 })
	time.Sleep(30 * time.Millisecond)
	if hung.Load() != total/2 {
		t.Fatalf("hung = %d", hung.Load())
	}
}

func TestWatcherWatchAfterCloseFailsAndCloseIsIdempotent(t *testing.T) {
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	_, sv := pair(t)
	w.Close()
	w.Close()
	rc, _ := sv.SyscallConn()
	if _, err := w.Watch(rc, func() {}); err == nil {
		t.Fatal("Watch on a closed watcher succeeded")
	}
	w.Unwatch(0)
}

func TestWatcherWorksAsACtxWatch(t *testing.T) {
	w := newWatcher(t)
	cl, sv := pair(t)
	rc, _ := sv.SyscallConn()
	cw := &connWatch{w: w, rc: rc}
	c := New(nil, cw)
	d := c.Done()
	cl.Close()
	select {
	case <-d:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctx not cancelled by the disconnect")
	}
	if c.Cause() != ErrClientGone {
		t.Fatal(c.Cause())
	}
	c.Finish()
}

type connWatch struct {
	w  *Watcher
	rc syscall.RawConn
	id uint64
}

func (cw *connWatch) Arm(c *Ctx) {
	cw.id, _ = cw.w.Watch(cw.rc, func() { c.Cancel(ErrClientGone) })
}
func (cw *connWatch) Disarm() { cw.w.Unwatch(cw.id) }

func TestWatchAndUnwatchAllocateNothingInSteadyState(t *testing.T) {
	w := newWatcher(t)
	_, sv := pair(t)
	rc, _ := sv.SyscallConn()
	f := func() {}
	if n := testing.AllocsPerRun(200, func() {
		id, _ := w.Watch(rc, f)
		w.Unwatch(id)
	}); n != 0 {
		t.Fatalf("allocs = %v", n)
	}
}

func BenchmarkWatchUnwatch(b *testing.B) {
	w := newWatcher(b)
	_, sv := pair(b)
	rc, _ := sv.SyscallConn()
	f := func() {}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id, _ := w.Watch(rc, f)
		w.Unwatch(id)
	}
}
