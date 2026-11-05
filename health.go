package bites

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/engine"
)

// Health names the two paths WithHealth answers (ADR 0052). "/livez" and "/readyz" are the
// documented choice; a field left empty is not answered.
type Health struct {
	// Live is answered 200 for as long as the app serves, Shutdown included: it says the process
	// is not wedged.
	Live string
	// Ready is answered 200 while the app serves and wants traffic, and 503 problem+json from the
	// moment Shutdown starts, or while the application has said it is not ready with SetReady.
	Ready string
}

// health is the app's state for WithHealth, WithShutdownDelay and SetReady.
type health struct {
	on          bool
	live, ready string
	where       string // where WithHealth was given

	delay      time.Duration
	delayWhere string // where WithShutdownDelay was given; "" when it was not

	notReady atomic.Bool // SetReady(false)
	draining atomic.Bool // Shutdown has started

	mu        sync.Mutex
	delayDone chan struct{} // closed when the delay is over, made by the first Shutdown (mu)
	delayOver bool          // delayDone is closed (mu)
	closed    bool          // Close was called (mu)
}

// WithHealth answers a liveness and a readiness path (ADR 0052). Both are answered before app
// middleware, so an auth middleware does not refuse a probe and a rate limiter does not count
// one; they are left out of the API document and are not observed (ADR 0051). GET and HEAD are
// answered; another method is a 405.
//
//   - Live: 200 text/plain "ok" for as long as the app serves, Shutdown included.
//   - Ready: the same 200 while the app wants traffic; 503 problem+json from the moment Shutdown
//     starts ("the app is shutting down") or while SetReady(false) holds ("the app is not ready").
//
// Readiness checks no dependency: a readiness that pings the database takes every replica out of
// the balancer at once when the database blips. An application that wants that anyway calls
// SetReady from its own check.
//
// New panics for a path that does not begin with a slash or has a wildcard, query or fragment,
// the same path for both, neither path, and WithHealth given twice; registering a route at
// either path panics at that route, naming both.
func WithHealth(h Health) Option {
	return func(a *App) {
		where := callerWhere()
		fail := func(format string, args ...any) {
			panic(&RegistrationError{msg: "WithHealth: " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
		}
		if a.health.on {
			panic(&RegistrationError{msg: "WithHealth: given twice (first at " + a.health.where + ", again at " + where + "): give one Health"})
		}
		if h.Live == "" && h.Ready == "" {
			fail(`Health has neither a Live nor a Ready path: give at least one, such as Health{Live: "/livez", Ready: "/readyz"}`)
		}
		for _, f := range []struct{ name, path string }{{"Live", h.Live}, {"Ready", h.Ready}} {
			if f.path != "" && (!strings.HasPrefix(f.path, "/") || strings.ContainsAny(f.path, "{}?#")) {
				fail("Health.%s is %q, which is not a path: it must start with a slash and have no wildcard, query or fragment", f.name, f.path)
			}
		}
		if h.Live == h.Ready {
			fail("Health.Live and Health.Ready are both %q: a probe could not tell liveness from readiness; give each its own path", h.Live)
		}
		a.health.on, a.health.live, a.health.ready, a.health.where = true, h.Live, h.Ready, where
		a.custom["health"] = true
	}
}

// WithShutdownDelay makes Shutdown fail readiness and keep serving, with the listener open, for d
// before it stops accepting (ADR 0052, ADR 0029). A balancer learns of a deploy late (kube-proxy
// and an ingress apply an endpoint's removal some seconds after it happens, an AWS target group
// after its interval times its threshold); a listener closed at once refuses the connections it
// still sends. Set it behind any balancer, to about the balancer's lag. The delay is bounded by
// half of what is left of Shutdown's context, so the drain still has time; a second Shutdown
// during it waits for the same delay and does not restart it. It is measured on the app's clock.
//
// The default is 0: no delay. New panics for a negative delay and for the option given twice.
func WithShutdownDelay(d time.Duration) Option {
	return func(a *App) {
		where := callerWhere()
		if a.health.delayWhere != "" {
			panic(&RegistrationError{msg: "WithShutdownDelay: given twice (first at " + a.health.delayWhere + ", again at " + where + "): give one delay"})
		}
		if d < 0 {
			panic(&RegistrationError{msg: fmt.Sprintf("WithShutdownDelay: the delay is %v, which is not a delay: give zero for none, or a positive duration such as 5*time.Second\n\tregistered at %s", d, where)})
		}
		a.health.delay, a.health.delayWhere = d, where
		a.custom["shutdown delay"] = true
	}
}

// SetReady says whether the app wants traffic: SetReady(false) makes the Ready path of WithHealth
// answer 503 until SetReady(true) (ADR 0052), for a cache still warming or a maintenance window.
// The app starts ready. It is one flag for the whole app: two parts of an application that each
// want it unready must agree between themselves. Once Shutdown has started, readiness fails
// whatever SetReady says. Without WithHealth it has no effect.
func (a *App) SetReady(ready bool) { a.health.notReady.Store(!ready) }

// answerHealth answers a request for one of the paths of WithHealth, and reports whether it did.
// It runs before app middleware, and writes fixed bytes into the engine's buffer.
func (a *App) answerHealth(ex *engine.Exchange, method, path []byte) bool {
	h := &a.health
	var ready bool
	switch {
	case h.live != "" && len(path) == len(h.live) && string(path) == h.live:
	case h.ready != "" && len(path) == len(h.ready) && string(path) == h.ready:
		ready = true
	default:
		return false
	}
	if m := string(method); m != "GET" && m != "HEAD" {
		problem(ex, 405, "").Header("Allow", "GET, HEAD")
		return true
	}
	if ready {
		switch {
		case h.draining.Load():
			problem(ex, 503, "the app is shutting down")
			return true
		case h.notReady.Load():
			problem(ex, 503, "the app is not ready")
			return true
		}
	}
	r := ex.Respond(200)
	r.Header("Content-Type", "text/plain; charset=utf-8")
	b := r.Body()
	*b = append(*b, "ok"...)
	return true
}

// healthConflict panics when a route is registered at a path WithHealth answers: the probe is
// answered before routing, so the route would never run (ADR 0052). Only the exact path is
// refused; a wildcard route that would also match it simply never sees it.
func (a *App) healthConflict(label, path, where string) {
	h := &a.health
	if !h.on {
		return
	}
	for _, f := range []struct{ name, path string }{{"Live", h.live}, {"Ready", h.ready}} {
		if f.path != "" && f.path == path {
			panic(&RegistrationError{msg: fmt.Sprintf("%s: %s is the %s path of WithHealth (given at %s), which is answered before any route, so this route would never run: give the route or Health.%s another path\n\tregistered at %s", label, path, f.name, h.where, f.name, where)})
		}
	}
}

// waitDelay is the shutdown delay (ADR 0052): the first Shutdown fixes its length, bounded by
// half of what is left of its context, and arms it on the app's clock; every Shutdown waits for
// it or for its own context. After Close there is nothing to wait for.
func (a *App) waitDelay(ctx context.Context) {
	h := &a.health
	h.mu.Lock()
	if h.delayDone == nil {
		h.delayDone = make(chan struct{})
		d := h.delay
		if dl, ok := ctx.Deadline(); ok {
			d = max(min(d, time.Until(dl)/2), 0)
		}
		if d == 0 || h.closed {
			h.endDelayLocked()
		} else {
			a.clk.AfterFunc(d, h.endDelay)
		}
	}
	done := h.delayDone
	h.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// endDelay ends the shutdown delay, once: when it has run its length, or when Close is called
// during it.
func (h *health) endDelay() {
	h.mu.Lock()
	h.endDelayLocked()
	h.mu.Unlock()
}

func (h *health) endDelayLocked() {
	if h.delayDone != nil && !h.delayOver {
		h.delayOver = true
		close(h.delayDone)
	}
}

// closeDelay is Close's part: a delay in progress ends, and a later Shutdown does not wait.
func (h *health) closeDelay() {
	h.mu.Lock()
	h.closed = true
	h.endDelayLocked()
	h.mu.Unlock()
}
