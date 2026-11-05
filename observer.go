package bites

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/nevindra/bites/internal/engine"
)

// Observer is told when a request starts and when it has really ended (ADR 0051). It is what the
// access log, the metrics and tracing are built on, in the core or in a module, and it is given to
// the app with WithObserver. An Observer watches; it never answers.
//
// Start runs once the head is parsed, before app middleware. The context it returns is the one
// the middleware, the Providers and the handler get, so a tracer puts its span there; an
// Observer that has nothing to add returns ctx as it is.
//
// End runs once, after the last byte of the answer was written (a stream's last chunk, the
// compressed body, the 500 after a panic, the problem+json a middleware returned), at the 101 of
// a WebSocket upgrade, or once the connection was given up. It gets the context its own Start
// returned and the same Request. It runs on the connection's goroutine, before the connection
// reads its next request, so an End that blocks holds the connection.
//
// Observers run in the order given for Start and in reverse for End, so the first one given wraps
// the others. A panic in Start is the request's panic: recovered, answered 500 and logged
// (ADR 0028), and End still runs for every Observer whose Start had returned. A panic in End is
// recovered and logged, the other Observers' End still run, and the connection is closed.
//
// A request the engine refuses before its head is parsed (a request line that is not HTTP, a
// head over its limit), an OPTIONS * and the health and readiness answers of WithHealth are not
// observed.
type Observer interface {
	Start(ctx context.Context, req Request) context.Context
	End(ctx context.Context, req Request, out Outcome)
}

// SocketObserver is implemented, besides Observer, by an Observer that wants to know of the
// WebSockets of the app (ADR 0063); WithObserver finds it, so there is nothing more to register.
// It is told what a chat watches first: sockets open, why each closed, and pongs missed. The
// callbacks run once the facts are known, on the connection's goroutine (SocketOpened and
// SocketClosed) or the heartbeat's (SocketPongMissed), so they must not block; a panic in one is
// recovered and logged and the socket is unaffected. Nothing is passed from a pooled object
// (ADR 0008). An app with no SocketObserver pays nothing per socket.
//
// route is the pattern of the WebSocket route, "/rooms/{id}", with its group's prefix: bounded by
// the app, safe as a metric label.
type SocketObserver interface {
	// SocketOpened is called once the 101 is written. A handshake that is refused (a 403 for its
	// Origin, a 400, a handler error) never opens a socket and is seen only by Observer.End.
	SocketOpened(route string)
	// SocketClosed is called once for every socket SocketOpened was called for, after OnClose
	// has returned. info.Cause says why, info.Status and info.Reason what was sent or received.
	SocketClosed(route string, info CloseInfo)
	// SocketPongMissed is called each time a heartbeat tick finds the last ping unanswered. A
	// socket that misses Limits.MissedPongs in a row is then closed with CausePongTimeout.
	SocketPongMissed(route string)
}

// socketWatch returns what a WebSocket route at pattern tells the app's SocketObservers, or nil
// when none of the app's Observers is one.
func (a *App) socketWatch(pattern string) *socketWatch {
	var obs []SocketObserver
	for _, o := range a.obs {
		if so, ok := o.(SocketObserver); ok {
			obs = append(obs, so)
		}
	}
	if obs == nil {
		return nil
	}
	return &socketWatch{obs: obs, route: pattern, log: a.log}
}

// Outcome is what an Observer's End is told (ADR 0051). It is passed by value and holds no
// pointer to pooled memory.
type Outcome struct {
	// Route is the pattern of the route that matched, "/users/{id}", with its group's prefix; ""
	// for a 404, a 405, a path that is not normalised, and an answer a middleware gave before the
	// route was matched (a CORS preflight).
	Route string
	// Status is what was sent: 500 after a panic, the 503 of Timeout, 101 for an upgrade. For an
	// abandoned request nothing was sent, and it is the status the chain left, 0 when none.
	Status int
	// Read is the number of body bytes read from the client.
	Read int64
	// Written is the number of body bytes written, after gzip; 0 for a HEAD, and 0 when the
	// request was abandoned. A stream's is the bytes its function sent, without the chunk framing.
	Written int64
	// Duration is the time from Start to End, on the app's clock.
	Duration time.Duration
	// Streamed: the answer was a Stream or an SSE (ADR 0030).
	Streamed bool
	// Upgraded: the answer was the 101 of a WebSocket upgrade; the socket's life is not in it, an
	// Observer that implements SocketObserver is told it (ADR 0051).
	Upgraded bool
	// Panicked: the handler, a middleware, a Provider, an Observer's Start or a stream's function
	// panicked.
	Panicked bool
	// Panic is the value recovered when Panicked is true, as recover returned it: a panic(nil)
	// arrives as a *runtime.PanicNilError. It is nil when nothing panicked. It is the
	// application's own value, not a copy, and not pooled (ADR 0008).
	Panic any
	// Stack is the stack of the goroutine that panicked, as runtime/debug.Stack returned it inside
	// the recovery, so it names the panicking function; nil when nothing panicked. It is a fresh
	// slice, not pooled; the Observers of one request share it, so copy it before changing it.
	Stack []byte
	// Err is the error the application returned that the framework answered: from a handler, a
	// middleware, a Provider or a stream's function before its first byte, whatever its status
	// (a returned HasStatus 404 and an unmapped error answered 500 both), and a stream
	// function's error after its first byte (the cut stream). Nil when no error was returned. It
	// is the value returned, unwrapped by nothing, so errors.As finds the application's types in
	// it. The framework's own answers are not in it: a 400 for a body that does not decode, a
	// 404, a 405, and a panic (that is Panic). The last error answered stands; and when a
	// middleware replaces the answer to an error with a status below 400, the error was handled
	// and Err is nil. A client that left is not an error: nothing is answered, and Err is nil.
	// The value is the application's own (ADR 0008).
	Err error
	// Abandoned: the client left, or stopped reading, before the answer reached it whole
	// (ADR 0049).
	Abandoned bool
}

// WithObserver gives the app its Observers (ADR 0051), in order: Start runs in the order given and
// End in reverse. Giving it twice, or a nil Observer, makes New panic.
//
// With no Observer a request pays one length check. With Observers it takes the chain's path, as
// a route with middleware does (the head is copied once into the request's slab, which costs no
// allocation), and pays two calls per Observer and two reads of the app's clock; an Observer that
// allocates nothing itself costs no allocation (ADR 0003).
func WithObserver(obs ...Observer) Option {
	return func(a *App) {
		where := callerWhere()
		if a.obsWhere != "" {
			panic(&RegistrationError{msg: "WithObserver: given twice (first at " + a.obsWhere + ", again at " + where + "): give every Observer in one WithObserver, in the order they should run"})
		}
		for i, o := range obs {
			if o == nil {
				panic(&RegistrationError{msg: fmt.Sprintf("WithObserver: Observer %d is nil\n\tregistered at %s", i+1, where)})
			}
		}
		a.obs, a.obsWhere = append([]Observer(nil), obs...), where
		a.custom["observers"] = true
	}
}

// observe begins observing the request: the engine is asked to call End, then every Observer's
// Start runs in order, each given the context the one before returned. It returns the context the
// chain gets.
func (r *chainRun) observe(ctx context.Context) context.Context {
	r.began = r.a.clk.Now()
	for _, o := range r.a.obs {
		ctx = o.Start(ctx, r.req)
		r.ctxs = append(r.ctxs, ctx)
	}
	return ctx
}

// NoteError is the engine's word that the framework answered err (engine.ErrorNoter): the last
// one stands, and End tells it as Outcome.Err.
func (r *chainRun) NoteError(err error) { r.err = err }

// End is the engine's call when the observed request has ended (engine.Ender). Every Observer
// whose Start returned has its End run, in reverse order, and then the run is released, so the
// Request the Observers were given answers zero values from here on.
func (r *chainRun) End(_ *engine.Exchange, e engine.Ended) bool {
	defer r.release()
	r.ended = r.a.clk.Now()
	out := Outcome{
		Status: e.Status, Read: e.Read, Written: e.Written,
		Duration: r.ended.Sub(r.began),
		Streamed: e.Streamed, Upgraded: e.Upgraded, Panicked: e.Panicked, Abandoned: e.Abandoned,
		Panic: e.Panic, Stack: e.Stack, Err: e.Err,
	}
	if out.Err == nil {
		out.Err = r.err
	}
	if out.Err != nil && e.Status < 400 && !e.Streamed {
		out.Err = nil // a middleware replaced the answer with a success: the error was handled
	}
	if r.rt != nil {
		out.Route = r.rt.pattern
	}
	ok := true
	for i := len(r.ctxs) - 1; i >= 0; i-- {
		if !r.endOne(i, &out) {
			ok = false
		}
	}
	return ok
}

// endOne runs one Observer's End, with its own recovery, so a panic in one leaves the others to
// run and only closes the connection.
func (r *chainRun) endOne(i int, out *Outcome) (ok bool) {
	defer func() {
		if v := recover(); v != nil {
			ok = false
			r.a.log.Error("bites: panic in an Observer's End; the connection is closed", "panic", v, "stack", string(debug.Stack()),
				"observer", fmt.Sprintf("%T", r.a.obs[i]), "method", r.req.method, "path", r.req.path)
		}
	}()
	r.a.obs[i].End(r.ctxs[i], r.req, *out)
	return true
}
