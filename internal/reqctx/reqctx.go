// Package reqctx is the context a handler receives, and the Linux watcher that tells it the
// client has gone.
//
// ADR 0016 says the handler's context is cancelled when the connection closes while the handler
// runs and when the handler returns. ADR 0003 prices that at one allocation for the context and
// one for its done channel, made only when someone asks for it. context.WithCancel costs two
// allocations before a handler has done anything, so Ctx is a context of its own.
//
// Ctx implements AfterFunc, the method the context package looks for (context.go afterFuncer).
// Without it every context.WithCancel or WithTimeout a handler derives from Ctx starts a goroutine
// that waits on Done; with it the derived context registers a callback here and nothing runs.
//
// The first Done or AfterFunc call arms the connection's Watch (the epoll registration in
// watcher_linux.go); a handler that never looks at its context pays nothing. Finish, called when
// the handler returns, cancels, disarms, and lets go of the Watch, so a Ctx a handler kept after
// its request (ADR 0008 allows that) can never reach the connection or a later request.
//
// context.Cause: the context package finds a cause through an unexported key that only its own
// types answer, so context.Cause(c) on a Ctx returns c.Err() (context.Canceled), not
// ErrClientGone. Use (*Ctx).Cause for the cause.
package reqctx

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/clock"
)

// ErrClientGone is the cancel cause when the client disconnects.
var ErrClientGone = errors.New("bites: client disconnected")

// Watch is implemented by the engine's per-connection state. Arm is called at most once per Ctx,
// the first time anyone calls Done() or AfterFunc on it; from then until Disarm, the engine must
// call c.Cancel(ErrClientGone) if the client disconnects. Disarm is called when the Ctx finishes.
type Watch interface {
	Arm(c *Ctx)
	Disarm()
}

var closedchan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// Ctx is one request's context. See the package comment.
type Ctx struct {
	parent    context.Context
	parentRun bool // parent can be cancelled (parent.Done() != nil), fixed at New

	// state is 0 while live, and once cancelled 1 (Err is context.Canceled) or 2 (Err is
	// context.DeadlineExceeded). cause is written before it is stored and never again.
	state atomic.Uint32
	cause error
	done  unsafe.Pointer // the chan struct{} made on first Done, read and written atomically (loadDone, storeDone)

	mu  sync.Mutex // guards ext and the transition to cancelled
	ext *ctxExt    // what AfterFunc and a cancellable parent need, made by the first of them

	// wmu serialises Arm, Disarm and Finish so Disarm never races an Arm in flight.
	wmu      sync.Mutex
	w        Watch
	armedReq atomic.Bool // someone asked for arming
	armedW   bool        // w.Arm ran (under wmu)
	finished bool        // under wmu

	// trace is the request's W3C trace context as the root package encodes it (trace.go), "" when
	// it has none. It is the request's own memory (its slab, or a string of its own), never the
	// engine's buffer, so a Ctx kept after its request keeps it (ADR 0008). It is set before the
	// handler runs, by the framework or an Observer's Start, and read afterwards.
	trace string

	// reqID, app and origin are what RequestIDFrom, Logger and Now answer from. They are set
	// before the handler runs (by SetRequestID, serveChain and the route match); reqID is a string
	// of the request's own slab, app is the app's and origin is the program's or the request's own,
	// so a Ctx kept after its request answers with its own request and never another's (ADR 0008).
	reqID  string
	app    *App
	origin *Origin
}

// App is what a request's context knows of its app: the logger and the clock. The framework keeps
// one for each app and a request points at it, so the Ctx holds one pointer for both.
type App struct {
	Log   *slog.Logger
	Clock clock.Clock
}

// Origin is the method a request came with and the pattern of the route it matched ("" before
// the match or when none did), as a request's logger names them. The framework keeps one for each
// method of each route, made at Registration, so a request points at it instead of holding two
// strings of its own: the Ctx stays in a smaller size class. It is never changed once a Ctx
// points at it.
type Origin struct {
	Method, Route string
}

// SetRequestID records the id a middleware gave the request.
func (c *Ctx) SetRequestID(id string) { c.reqID = id }

// RequestID is what SetRequestID recorded, or "".
func (c *Ctx) RequestID() string { return c.reqID }

// SetRequest records the request's app and origin.
func (c *Ctx) SetRequest(app *App, o *Origin) { c.app, c.origin = app, o }

// SetOrigin records the request's origin once its route matched.
func (c *Ctx) SetOrigin(o *Origin) { c.origin = o }

// Request returns what SetRequest and SetOrigin recorded: the logger (nil when none), the method
// and the route pattern ("" for either when none was recorded).
func (c *Ctx) Request() (log *slog.Logger, method, route string) {
	if c.app != nil {
		log = c.app.Log
	}
	if o := c.origin; o != nil {
		return log, o.Method, o.Route
	}
	return log, "", ""
}

// Clock is the clock of the request's app, or nil when SetRequest recorded none.
func (c *Ctx) Clock() clock.Clock {
	if c.app != nil {
		return c.app.Clock
	}
	return nil
}

// SetTrace records the request's encoded trace context.
func (c *Ctx) SetTrace(s string) { c.trace = s }

// Trace is what SetTrace recorded, or "".
func (c *Ctx) Trace() string { return c.trace }

// ctxExt is the part of a Ctx most requests never use: a handler that registers no AfterFunc (and
// derives no WithCancel or WithTimeout context from it) under a parent that cannot be cancelled
// never makes one, so every request's Ctx is smaller by its size (ADR 0003). Guarded by Ctx.mu.
type ctxExt struct {
	// first is the first AfterFunc's node, held here so the usual one (a derived context, a
	// mounted net/http handler) needs no node and no slice of its own; later ones go to callbacks.
	first      afterNode
	firstUsed  bool
	callbacks  []*afterNode
	stopParent func() bool
	linked     bool // the parent registration was attempted
}

// extLocked returns c.ext, making it first. c.mu is held.
func (c *Ctx) extLocked() *ctxExt {
	if c.ext == nil {
		c.ext = new(ctxExt)
	}
	return c.ext
}

func (c *Ctx) loadDone() chan struct{} {
	p := atomic.LoadPointer(&c.done)
	if p == nil {
		return nil
	}
	return *(*chan struct{})(unsafe.Pointer(&p))
}

func (c *Ctx) storeDone(d chan struct{}) {
	atomic.StorePointer(&c.done, *(*unsafe.Pointer)(unsafe.Pointer(&d)))
}

type afterNode struct {
	f       func()
	settled bool // run or stopped, under Ctx.mu
}

// New returns the context for one request: exactly one allocation. parent supplies Value lookups
// and its own cancellation (server shutdown); w may be nil.
func New(parent context.Context, w Watch) *Ctx {
	if parent == nil {
		parent = context.Background()
	}
	return &Ctx{parent: parent, parentRun: parent.Done() != nil, w: w}
}

func (c *Ctx) Deadline() (time.Time, bool) { return c.parent.Deadline() }

func (c *Ctx) Value(key any) any {
	if key == (selfKey{}) {
		return c
	}
	return c.parent.Value(key)
}

// selfKey makes Value answer with the Ctx itself, so that Of finds it through a context derived
// from it with WithValue or WithCancel, whose own Cause would hide the request's (ADR 0016).
type selfKey struct{}

// Of returns the request context ctx is or derives from, or nil.
func Of(ctx context.Context) *Ctx {
	c, _ := ctx.Value(selfKey{}).(*Ctx)
	return c
}

// Err returns context.Canceled after any cancel, and context.DeadlineExceeded when the cause is
// that. A cancelled parent is noticed here too, so Err and Done agree even before the parent link
// exists.
func (c *Ctx) Err() error {
	if err := c.stateErr(); err != nil || !c.parentRun {
		return err
	}
	if perr := c.parent.Err(); perr != nil {
		c.Cancel(context.Cause(c.parent))
		return c.stateErr()
	}
	return nil
}

func (c *Ctx) stateErr() error {
	switch c.state.Load() {
	case 0:
		return nil
	case 2:
		return context.DeadlineExceeded
	}
	return context.Canceled
}

// Cause returns why the context was cancelled: ErrClientGone, the parent's cause, the cause given
// to Cancel, or context.Canceled from Finish. It is nil while the context is live.
func (c *Ctx) Cause() error {
	if c.state.Load() != 0 {
		return c.cause
	}
	if c.Err() != nil {
		return c.cause
	}
	return nil
}

// Done returns the channel closed on cancellation. It is made on first use (one allocation) and
// arms the Watch.
func (c *Ctx) Done() <-chan struct{} {
	if d := c.loadDone(); d != nil {
		return d
	}
	c.mu.Lock()
	d := c.loadDone()
	if d == nil {
		if c.state.Load() != 0 {
			d = closedchan
		} else {
			d = make(chan struct{})
		}
		c.storeDone(d)
	}
	c.mu.Unlock()
	c.arm()
	return d
}

// AfterFunc arranges for f to run in its own goroutine once c is cancelled, as context.AfterFunc
// does; if c is already cancelled f runs at once. stop reports whether it prevented f from running.
func (c *Ctx) AfterFunc(f func()) (stop func() bool) {
	c.mu.Lock()
	if c.state.Load() != 0 {
		c.mu.Unlock()
		go f()
		return func() bool { return false }
	}
	e := c.extLocked()
	var n *afterNode
	if !e.firstUsed {
		e.firstUsed = true
		n = &e.first
		n.f = f
	} else {
		n = &afterNode{f: f}
		e.callbacks = append(e.callbacks, n)
	}
	c.mu.Unlock()
	c.arm()
	return func() bool { return c.stopNode(n) }
}

func (c *Ctx) stopNode(n *afterNode) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n.settled {
		return false
	}
	n.settled = true
	e := c.ext // not nil: n is in it
	if n == &e.first {
		return true
	}
	for i, x := range e.callbacks {
		if x == n {
			last := len(e.callbacks) - 1
			e.callbacks[i] = e.callbacks[last]
			e.callbacks[last] = nil
			e.callbacks = e.callbacks[:last]
			break
		}
	}
	return true
}

// arm links the parent's cancellation and arms the Watch, each at most once, and only now that
// somebody is looking at the context.
func (c *Ctx) arm() {
	if c.parentRun {
		c.linkParent()
	}
	if c.state.Load() != 0 || c.armedReq.Swap(true) {
		return
	}
	c.wmu.Lock()
	if !c.finished && c.w != nil && c.state.Load() == 0 {
		c.w.Arm(c)
		c.armedW = true
	}
	c.wmu.Unlock()
}

func (c *Ctx) linkParent() {
	c.mu.Lock()
	if c.state.Load() != 0 || (c.ext != nil && c.ext.linked) {
		c.mu.Unlock()
		return
	}
	c.extLocked().linked = true
	c.mu.Unlock()
	// context.AfterFunc takes the parent's lock, so it runs outside c.mu.
	stop := context.AfterFunc(c.parent, func() { c.Cancel(context.Cause(c.parent)) })
	c.mu.Lock()
	if c.state.Load() != 0 {
		c.mu.Unlock()
		stop()
		return
	}
	c.ext.stopParent = stop
	c.mu.Unlock()
}

// Cancel cancels with cause; the first cause wins. A nil cause means context.Canceled. Safe from
// any goroutine and idempotent.
func (c *Ctx) Cancel(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	c.mu.Lock()
	if c.state.Load() != 0 {
		c.mu.Unlock()
		return
	}
	c.cause = cause
	st := uint32(1)
	if cause != context.Canceled && errors.Is(cause, context.DeadlineExceeded) {
		st = 2
	}
	c.state.Store(st)
	if d := c.loadDone(); d != nil {
		close(d)
	}
	var cbs []*afterNode
	var first func()
	var stop func() bool
	if e := c.ext; e != nil {
		if e.firstUsed && !e.first.settled {
			e.first.settled = true
			first = e.first.f
		}
		cbs, e.callbacks = e.callbacks, nil
		for _, n := range cbs {
			n.settled = true
		}
		stop, e.stopParent = e.stopParent, nil
	}
	c.mu.Unlock()

	if first != nil {
		go first()
	}
	for _, n := range cbs {
		go n.f()
	}
	if stop != nil {
		stop()
	}
}

// Finish is called by the engine when the handler has returned. It cancels with context.Canceled
// if not yet cancelled, disarms the Watch if armed, and drops the Watch and the parent link, so a
// kept Ctx can never touch the connection or a later request (ADR 0008).
func (c *Ctx) Finish() {
	c.Cancel(context.Canceled)
	c.wmu.Lock()
	c.finished = true
	w := c.w
	armed := c.armedW
	c.w = nil
	c.armedW = false
	c.wmu.Unlock()
	if armed {
		w.Disarm()
	}
}
