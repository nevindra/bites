package bites

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/reqctx"
)

// Timeout is the per-route timeout middleware of ADR 0032 and ADR 0049. It is meant for a route
// or a group (bites.Use(bites.Timeout(2*time.Second))); as app middleware it covers every
// request, 404 included.
//
// When the rest of the chain has not returned after d, measured on the app's clock with
// AfterFunc (so a test moves bitetest.Clock instead of waiting), the request's context is
// cancelled with context.DeadlineExceeded as its cause, and the middleware keeps waiting. The
// handler is never abandoned: Go cannot stop a goroutine, and answering while it still runs
// would put a second goroutine on the request and write a response while the handler might
// still act (ADR 0009). When the handler returns, the answer is a 503 problem+json whatever it
// returned (a value, an error or a Stream), and its own answer is dropped; a handler that ignores its
// context is therefore late, visibly. One that ran more than twice the deadline is logged at
// Warn with how long it ran. A handler that returns before the deadline is not affected, and
// the timer is stopped.
//
// The context is the request's own: cancelling it also reaches a Provider's work, any
// middleware inside this one, and the middleware outside it if it keeps using ctx after Call,
// all of which is the intent of a deadline. An earlier cancellation (the client left) wins and
// stays the cause; then the 503 is written to nobody.
//
// A Stream or SSE (ADR 0030) is not under the deadline once the handler has returned it: its
// function runs after every middleware has returned, so the timeout bounds only the time to
// produce the stream. A stream that must end by a time ends itself on its context or its own
// timer. If the deadline passes before the handler returns the stream, the 503 replaces it and
// its function never runs.
//
// It allocates nothing for a request that finishes in time after the first: the timer, its
// callback and its state are reused. It panics when d is zero or negative.
func Timeout(d time.Duration) Middleware {
	if d <= 0 {
		refuseMiddleware("Timeout", "the duration is %v, which is not a deadline: give a positive duration such as 2*time.Second", d)
	}
	var pool sync.Pool
	return func(ctx context.Context, req Request, next Next) (Response, error) {
		a := req.run.a
		dl, _ := pool.Get().(*deadline)
		if dl == nil || dl.clk != a.clk {
			dl = newDeadline(a.clk)
		}
		dl.st.Store(0)
		dl.ctx = req.run.ex.Context()
		dl.arm(d)
		returned := false
		defer func() {
			if !returned { // a panic: nothing is waiting for the timer any more
				dl.timer.Stop()
			}
		}()
		res := next.Call(ctx)
		returned = true
		if dl.st.CompareAndSwap(deadlineArmed, deadlineDone) {
			if dl.timer.Stop() && dl.reusable {
				dl.ctx = nil
				pool.Put(dl)
			}
			return res, nil
		}
		// The deadline passed first. The handler has returned now; its answer is dropped.
		ran := d + time.Duration(a.clk.Now().UnixNano()-dl.at.Load())
		if ran > 2*d {
			a.log.Warn("bites: a handler returned long after its Timeout",
				"method", req.Method(), "path", req.Path(), "timeout", d, "ran", ran, "request_id", req.RequestID())
		}
		return Problem(503, "the request did not finish in time"), nil
	}
}

const (
	deadlineArmed = iota
	deadlineDone  // the chain returned first
	deadlineFired // the timer won
)

// deadline is one request's timer and its state. It is reused while a request ends in time and
// Stop reports the callback will never run; one whose callback might still be running is left to
// the garbage collector, so a late callback can never touch a later request's.
type deadline struct {
	clk      clock.Clock
	timer    clock.Timer
	reusable bool // the timer can be re-armed in place
	st       atomic.Uint32
	at       atomic.Int64 // clk.Now() in nanoseconds when the callback ran
	ctx      *reqctx.Ctx
	fire     func()
}

func newDeadline(clk clock.Clock) *deadline {
	dl := &deadline{clk: clk}
	dl.fire = dl.expire
	return dl
}

func (dl *deadline) arm(d time.Duration) {
	if dl.timer != nil {
		if r, ok := dl.timer.(clock.Resetter); ok {
			r.Reset(d)
			return
		}
	}
	dl.timer = dl.clk.AfterFunc(d, dl.fire)
	_, dl.reusable = dl.timer.(clock.Resetter)
}

// expire is the timer's callback. The time is stored before the race is decided, so a reader
// that lost the race sees it.
func (dl *deadline) expire() {
	dl.at.Store(dl.clk.Now().UnixNano())
	if dl.st.CompareAndSwap(deadlineArmed, deadlineFired) {
		dl.ctx.Cancel(context.DeadlineExceeded)
	}
}
