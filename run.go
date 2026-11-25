package bites

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Run serves the app until ctx is done, then shuts it down within grace, and returns once
// everything has stopped: the requests in flight have finished or been closed, the Services have
// stopped, and every serve function has returned (ADR 0029). It is how a main serves:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//	if err := app.Run(ctx, 25*time.Second, func() error { return app.Listen(":8080") }); err != nil {
//		log.Fatal(err)
//	}
//
// ctx is the application's: the framework installs no signal handler, so the context that ends
// on SIGTERM comes from signal.NotifyContext. grace is how long Shutdown may take once ctx is
// done, WithShutdownDelay and the Services' BeforeShutdown and Stop included; give a grace shorter
// than the orchestrator's kill timeout (Kubernetes waits 30 s).
//
// Each serve function serves one listener of this app and returns what it returns: a call of
// Listen, ListenTLS, ListenUnix, Serve or ServeTLS, so TLS, a Unix socket, the PROXY protocol and
// several listeners are said the one way they are said without Run. Each runs on its own
// goroutine.
//
// Run returns nil when ctx ended and everything stopped within grace, or when Shutdown or Close,
// called elsewhere, stopped the app (Run then returns once that call has returned). It returns
// the error of a serve function that failed (an address already in use, a Service whose Start
// failed), after stopping the other listeners within grace; and Shutdown's error when the grace
// ran out (errors.Is(err, context.DeadlineExceeded)) or a Service failed to stop. A serve function
// that returns nil, or that has not returned 5 s after the app stopped, is an error that names
// it, because it does not serve this app.
//
// Run panics with a RegistrationError for a nil ctx, a grace that is not positive or is less than
// twice WithShutdownDelay (the delay is bounded by half of what is left of Shutdown's context), no
// serve function or a nil one, and an app that is already serving: every listener is given to
// one Run, and nothing else that serves is called beside it.
func (a *App) Run(ctx context.Context, grace time.Duration, serve ...func() error) error {
	where := callerWhere()
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: "Run: " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	if ctx == nil {
		fail("the context is nil: give the context whose end stops the app, the one signal.NotifyContext returns for os.Interrupt and syscall.SIGTERM")
	}
	if grace <= 0 {
		fail("the grace period %v is not a time to shut down in: give a positive duration shorter than the orchestrator's kill timeout, such as 25*time.Second under Kubernetes' 30 s", grace)
	}
	if d := a.health.delay; d > 0 && grace < 2*d {
		fail("the grace period %v is less than twice WithShutdownDelay(%v): the delay is bounded by half of Shutdown's context, so it would be cut short; give a grace of at least %v plus the time a request needs to finish", grace, d, 2*d)
	}
	if len(serve) == 0 {
		fail(`no serve function: give one for each listener, such as func() error { return app.Listen(":8080") }`)
	}
	for i, s := range serve {
		if s == nil {
			fail("serve function %d is nil: give a function that returns what app.Listen, ListenTLS, ListenUnix, Serve or ServeTLS returns", i+1)
		}
	}
	a.mu.Lock()
	if a.serving {
		a.mu.Unlock()
		fail("the app is already serving (a Listen, ListenTLS, ListenUnix, Serve, ServeTLS, bitetest.Start or another Run came first): give every listener to one Run as a serve function, and call nothing else that serves")
	}
	a.serving = true // a second Run from now on is refused, though start has not run yet
	a.mu.Unlock()
	// The server is built before the first serve function runs, so a ctx that is already done, or
	// ends before a goroutine reaches Listen, is a Shutdown that reaches it.
	a.start()

	type result struct {
		i   int
		err error
	}
	done := make(chan result, len(serve))
	for i, s := range serve {
		go func() { done <- result{i, s()} }()
	}
	var (
		failed  []error // what a serve function returned other than ErrServerClosed
		pending = len(serve)
		out     = make([]bool, len(serve)) // the serve functions that have returned
	)
	note := func(r result) {
		out[r.i] = true
		switch {
		case r.err == nil:
			failed = append(failed, fmt.Errorf("bites: Run: serve function %d returned nil while the app was serving: a serve function returns what this app's Listen, ListenTLS, ListenUnix, Serve or ServeTLS returns", r.i+1))
		case errors.Is(r.err, ErrServerClosed):
		default:
			for _, e := range failed {
				if e == r.err {
					return // every serve function is told the same failed Start
				}
			}
			failed = append(failed, r.err)
		}
	}

	shutdown := true
	select {
	case <-ctx.Done():
		a.log.Info("bites: the context of Run ended: shutting down", "grace", grace, "cause", context.Cause(ctx))
	case r := <-done:
		pending--
		note(r)
		// ErrServerClosed first: Shutdown or Close was called elsewhere, and Run waits for it.
		shutdown = len(failed) > 0
		if shutdown {
			a.log.Error("bites: a serve function of Run failed: shutting down the rest", "serve", r.i+1, "grace", grace)
		}
	}
	var stopErr error
	if shutdown {
		gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		if err := a.Shutdown(gctx); err != nil {
			stopErr = fmt.Errorf("bites: Run: shutting down within the grace period of %v: %w", grace, err)
		}
		cancel()
	}

	// This app's serve functions return once Shutdown or Close has returned; one that does not
	// is serving something else, and is not waited for forever.
	var late <-chan time.Time
	stopped := a.stopped
	for pending > 0 {
		select {
		case r := <-done:
			pending--
			note(r)
		case <-stopped:
			stopped = nil
			late = time.After(runServeWait)
		case <-late:
			for i, ok := range out {
				if !ok {
					failed = append(failed, fmt.Errorf("bites: Run: serve function %d has not returned %v after the app stopped, so it does not serve this app: a serve function returns what this app's Listen, ListenTLS, ListenUnix, Serve or ServeTLS returns", i+1, runServeWait))
				}
			}
			pending = 0
		}
	}
	return joinErrors(append(failed, stopErr)...)
}

// runServeWait is how long Run waits, once the app has stopped, for a serve function that has not
// returned.
var runServeWait = 5 * time.Second

// markStopped closes a.stopped once, when Shutdown or Close has returned.
func (a *App) markStopped() { a.stopOnce.Do(func() { close(a.stopped) }) }
