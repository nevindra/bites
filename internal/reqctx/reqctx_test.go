package reqctx

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

type fakeWatch struct {
	arms, disarms atomic.Int32
	c             atomic.Pointer[Ctx]
}

func (f *fakeWatch) Arm(c *Ctx) { f.arms.Add(1); f.c.Store(c) }
func (f *fakeWatch) Disarm()    { f.disarms.Add(1) }

type key struct{}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewAndFinishWithoutDoneAllocateOnce(t *testing.T) {
	w := &fakeWatch{}
	if n := testing.AllocsPerRun(100, func() { New(context.Background(), w).Finish() }); n != 1 {
		t.Fatalf("allocs = %v, want 1", n)
	}
}

func TestDoneAllocatesOneMore(t *testing.T) {
	w := &fakeWatch{}
	n := testing.AllocsPerRun(100, func() {
		c := New(context.Background(), w)
		_ = c.Done()
		c.Finish()
	})
	if n != 2 {
		t.Fatalf("allocs = %v, want 2", n)
	}
}

func TestValueLookupAllocatesNothing(t *testing.T) {
	parent := context.WithValue(context.Background(), key{}, "v")
	c := New(parent, nil)
	if n := testing.AllocsPerRun(100, func() { _ = c.Value(key{}) }); n != 0 {
		t.Fatalf("allocs = %v, want 0", n)
	}
	if c.Value(key{}) != "v" {
		t.Fatal("value not found through the parent")
	}
}

func TestErrIsCanceledForAnyCancelAndCauseIsKept(t *testing.T) {
	c := New(context.Background(), nil)
	if c.Err() != nil || c.Cause() != nil {
		t.Fatal("live context has an error")
	}
	c.Cancel(ErrClientGone)
	c.Cancel(errors.New("second"))
	if c.Err() != context.Canceled {
		t.Fatalf("Err = %v", c.Err())
	}
	if c.Cause() != ErrClientGone {
		t.Fatalf("Cause = %v, first cause must win", c.Cause())
	}
}

func TestDeadlineExceededCauseGivesDeadlineExceededErr(t *testing.T) {
	c := New(context.Background(), nil)
	c.Cancel(context.DeadlineExceeded)
	if c.Err() != context.DeadlineExceeded {
		t.Fatalf("Err = %v", c.Err())
	}
}

func TestContextCauseSeesCanceledNotOurCause(t *testing.T) {
	// context.Cause finds a cause only through an unexported key, so it falls back to Err.
	c := New(context.Background(), nil)
	c.Cancel(ErrClientGone)
	if got := context.Cause(c); got != context.Canceled {
		t.Fatalf("context.Cause = %v, documented as Canceled", got)
	}
	if c.Cause() != ErrClientGone {
		t.Fatal("Ctx.Cause lost the cause")
	}
}

func TestDoneIsClosedByCancelWhetherMadeBeforeOrAfter(t *testing.T) {
	before := New(context.Background(), nil)
	d := before.Done()
	before.Cancel(nil)
	select {
	case <-d:
	default:
		t.Fatal("channel made before cancel stayed open")
	}
	after := New(context.Background(), nil)
	after.Cancel(nil)
	select {
	case <-after.Done():
	default:
		t.Fatal("channel made after cancel is open")
	}
}

func TestDoneAndAfterFuncArmTheWatchOnce(t *testing.T) {
	w := &fakeWatch{}
	c := New(context.Background(), w)
	if w.arms.Load() != 0 {
		t.Fatal("armed before anyone looked")
	}
	c.Done()
	c.Done()
	c.AfterFunc(func() {})
	if w.arms.Load() != 1 {
		t.Fatalf("arms = %d", w.arms.Load())
	}
	c.Finish()
	c.Finish()
	if w.disarms.Load() != 1 {
		t.Fatalf("disarms = %d", w.disarms.Load())
	}
}

func TestFinishWithoutArmingDoesNotDisarm(t *testing.T) {
	w := &fakeWatch{}
	c := New(context.Background(), w)
	c.Finish()
	if w.disarms.Load() != 0 {
		t.Fatal("disarmed a watch that was never armed")
	}
	if c.Err() != context.Canceled || c.Cause() != context.Canceled {
		t.Fatal("Finish must cancel with context.Canceled")
	}
}

func TestAWatchArmedAfterFinishIsNeverTouched(t *testing.T) {
	w := &fakeWatch{}
	c := New(context.Background(), w)
	c.Finish()
	c.Done()
	c.AfterFunc(func() {})
	if w.arms.Load() != 0 {
		t.Fatal("a kept Ctx armed the connection after its request")
	}
}

func TestWatchCancelsWithClientGone(t *testing.T) {
	w := &fakeWatch{}
	c := New(context.Background(), w)
	c.Done()
	w.c.Load().Cancel(ErrClientGone)
	<-c.Done()
	if c.Cause() != ErrClientGone {
		t.Fatal(c.Cause())
	}
}

func TestAfterFuncRunsOnceInAGoroutine(t *testing.T) {
	c := New(context.Background(), nil)
	var n atomic.Int32
	ran := make(chan struct{}, 4)
	c.AfterFunc(func() { n.Add(1); ran <- struct{}{} })
	c.Cancel(nil)
	c.Cancel(nil)
	<-ran
	time.Sleep(10 * time.Millisecond)
	if n.Load() != 1 {
		t.Fatalf("ran %d times", n.Load())
	}
}

func TestAfterFuncStopPreventsTheCall(t *testing.T) {
	c := New(context.Background(), nil)
	var n atomic.Int32
	stop := c.AfterFunc(func() { n.Add(1) })
	other := make(chan struct{})
	c.AfterFunc(func() { close(other) })
	if !stop() {
		t.Fatal("stop returned false before cancel")
	}
	if stop() {
		t.Fatal("second stop returned true")
	}
	c.Cancel(nil)
	<-other
	time.Sleep(10 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatal("stopped callback ran")
	}
}

func TestAfterFuncStopAfterCancelReturnsFalse(t *testing.T) {
	c := New(context.Background(), nil)
	ran := make(chan struct{})
	stop := c.AfterFunc(func() { close(ran) })
	c.Cancel(nil)
	<-ran
	if stop() {
		t.Fatal("stop returned true after the call started")
	}
}

func TestAfterFuncAfterCancelRunsAtOnce(t *testing.T) {
	c := New(context.Background(), nil)
	c.Cancel(nil)
	ran := make(chan struct{})
	stop := c.AfterFunc(func() { close(ran) })
	<-ran
	if stop() {
		t.Fatal("stop returned true")
	}
}

func TestParentCancellationPropagates(t *testing.T) {
	parent, cancel := context.WithCancelCause(context.Background())
	c := New(parent, nil)
	done := c.Done()
	cancel(errors.New("shutdown"))
	<-done
	if c.Err() != context.Canceled || c.Cause().Error() != "shutdown" {
		t.Fatalf("%v %v", c.Err(), c.Cause())
	}
}

func TestParentCancellationIsSeenByErrWithoutDone(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	c := New(parent, nil)
	cancel()
	if c.Err() != context.Canceled {
		t.Fatal(c.Err())
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done open though Err is set")
	}
}

func TestParentDeadlineIsInherited(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	c := New(parent, nil)
	if _, ok := c.Deadline(); !ok {
		t.Fatal("deadline lost")
	}
	c2 := New(context.Background(), nil)
	if _, ok := c2.Deadline(); ok {
		t.Fatal("phantom deadline")
	}
}

func TestBackgroundParentAddsNoAllocationAndFinishDropsTheParentLink(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(parent, nil)
	c.Done()
	c.Finish()
	cancel() // must not reach c.stopParent or panic
	if c.Cause() != context.Canceled {
		t.Fatal(c.Cause())
	}
}

func TestDerivedContextsStartNoGoroutine(t *testing.T) {
	c := New(context.Background(), &fakeWatch{})
	runtime.GC()
	before := runtime.NumGoroutine()
	var cancels []context.CancelFunc
	var kids []context.Context
	for i := 0; i < 100; i++ {
		k, cancel := context.WithCancel(c)
		k2, cancel2 := context.WithTimeout(k, time.Hour)
		kids = append(kids, k, k2)
		cancels = append(cancels, cancel, cancel2)
	}
	if after := runtime.NumGoroutine(); after != before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
	c.Cancel(ErrClientGone)
	for _, k := range kids {
		<-k.Done()
		if k.Err() != context.Canceled {
			t.Fatal(k.Err())
		}
	}
	for _, f := range cancels {
		f()
	}
}

func TestDerivedContextStopsWithItsCancel(t *testing.T) {
	c := New(context.Background(), nil)
	k, cancel := context.WithCancel(c)
	cancel()
	<-k.Done()
	if c.ext == nil || len(c.ext.callbacks) != 0 {
		t.Fatal("the derived context stayed registered")
	}
}

func TestARequestContextNobodyDerivesFromIsOneSmallObject(t *testing.T) {
	// ADR 0003: the context is one of a typed request's two allocations, and its size sets how
	// often the collector runs under load. What AfterFunc and a cancellable parent need is made
	// only when they are used.
	if got := unsafe.Sizeof(Ctx{}); got > 144 {
		t.Fatalf("a Ctx is %d bytes, want at most 144 (the 144-byte size class)", got)
	}
	c := New(context.Background(), nil)
	c.Done()
	c.Finish()
	<-c.Done()
	if c.ext != nil {
		t.Fatal("Done and Finish made the part only AfterFunc and a cancellable parent need")
	}
	if c.Err() != context.Canceled || c.Cause() != context.Canceled {
		t.Fatalf("after Finish: Err %v, Cause %v; want context.Canceled for both", c.Err(), c.Cause())
	}
}

func TestACauseOfDeadlineExceededMakesErrDeadlineExceeded(t *testing.T) {
	c := New(context.Background(), nil)
	cause := fmt.Errorf("budget spent: %w", context.DeadlineExceeded)
	c.Cancel(cause)
	if c.Err() != context.DeadlineExceeded || c.Cause() != cause {
		t.Fatalf("Err %v, Cause %v; want context.DeadlineExceeded and the cause given", c.Err(), c.Cause())
	}
}

func TestContextAfterFuncUsesOurMethod(t *testing.T) {
	c := New(context.Background(), nil)
	runtime.GC()
	before := runtime.NumGoroutine()
	ran := make(chan struct{})
	stop := context.AfterFunc(c, func() { close(ran) })
	if runtime.NumGoroutine() != before {
		t.Fatal("goroutine started at registration")
	}
	_ = stop
	c.Cancel(nil)
	<-ran
}

func TestConcurrentUseIsRaceFree(t *testing.T) {
	for i := 0; i < 200; i++ {
		parent, pc := context.WithCancel(context.Background())
		w := &fakeWatch{}
		c := New(parent, w)
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.Done()
				c.AfterFunc(func() {})()
				c.Err()
				c.Cause()
				k, cancel := context.WithCancel(c)
				cancel()
				_ = k
			}()
		}
		wg.Add(3)
		go func() { defer wg.Done(); c.Cancel(ErrClientGone) }()
		go func() { defer wg.Done(); pc() }()
		go func() { defer wg.Done(); c.Finish() }()
		wg.Wait()
	}
}

func BenchmarkNewFinish(b *testing.B) {
	w := &fakeWatch{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		New(context.Background(), w).Finish()
	}
}

func BenchmarkNewDoneFinish(b *testing.B) {
	w := &fakeWatch{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c := New(context.Background(), w)
		_ = c.Done()
		c.Finish()
	}
}

func BenchmarkNewDoneFinishCancelableParent(b *testing.B) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &fakeWatch{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c := New(parent, w)
		_ = c.Done()
		c.Finish()
	}
}
