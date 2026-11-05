package bites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/reqctx"
)

// Service is something that lives exactly as long as the app does: a database pool, a queue
// consumer, a scheduler, a registration in a service registry (ADR 0079). It is a value with a
// name and up to three functions, given to App.Service; a module returns one for the application
// to register. Every function is optional but at least one must be set.
//
// Start runs after the app has bound its listener and before it accepts the first connection,
// one service at a time in registration order. It returns when the service is running and does
// not block: a worker starts its goroutine and returns. Its ctx is the service's life, not the
// start's: it is cancelled just before Stop is called (or when the app closes), with the cause
// ErrServerClosed, and it carries no deadline, so "go run(ctx)" is the correct body of a worker.
// An error from Start (or a panic in it) refuses the start: the services already started are
// stopped in reverse and Listen returns the error.
//
// BeforeShutdown runs when Shutdown starts, in reverse registration order, after readiness has
// failed and before the shutdown delay and before the listener closes, while the app still
// serves. It is where a service leaves a registry or stops taking work. Close does not run it.
//
// Stop runs once the drain is over, in reverse registration order, with Shutdown's context. It
// is called for a service whose Start returned nil (or that has none), never for one whose Start
// failed, and it must work when BeforeShutdown never ran. Every Stop runs even when an earlier one
// failed.
//
// A function that does not return before its context ends is abandoned and logged, and the rest
// go on (see WithServiceTimeout). A panic in a function is recovered, logged and treated as an
// error.
type Service struct {
	// Name is the word the log and the errors call this service by. It is required, must be
	// unique in the app, and must have no space, quote or control character.
	Name string
	// Start brings the service up; nil means there is nothing to start.
	Start func(ctx context.Context) error
	// BeforeShutdown runs first when Shutdown starts; nil means nothing to do then.
	BeforeShutdown func(ctx context.Context) error
	// Stop releases the service once the drain is over; nil means nothing to release.
	Stop func(ctx context.Context) error
}

// defaultServiceTimeout is how long the app waits for one Start, and for a function called with
// a context that is already done, when WithServiceTimeout was not given.
const defaultServiceTimeout = 30 * time.Second

// WithServiceTimeout sets how long the app waits for one Service's Start to return, and for one
// BeforeShutdown or Stop that is called with a context already done (every one under Close, the
// rest after Shutdown's deadline has passed). The default is 30 seconds. A function that has not
// returned by then is abandoned: logged, its context cancelled, and the next one goes on. d must
// be positive; New panics for zero or a negative d and for the option given twice (ADR 0079).
func WithServiceTimeout(d time.Duration) Option {
	return func(a *App) {
		where := callerWhere()
		if a.custom["service timeout"] {
			panic(&RegistrationError{msg: "app: WithServiceTimeout is given twice\n\tregistered at " + where})
		}
		if d <= 0 {
			shown := "0"
			if d != 0 {
				shown = d.String()
			}
			panic(&RegistrationError{msg: "app: WithServiceTimeout(" + shown + ") is not a time to wait: give a positive duration, or leave it out for 30s\n\tregistered at " + where})
		}
		a.life.timeout = d
		a.custom["service timeout"] = true
	}
}

// Service registers s to start with the app and stop with it (ADR 0079). Services start in the
// order they are registered, after the listener is bound and before the first connection is
// accepted, and stop in the reverse order once the drain is over, so a service registered after
// another stops before it. It panics for a Service with no usable Name or with no function, for
// a Name used twice, and for a call after the app started serving.
func (a *App) Service(s Service) {
	where := callerWhere()
	fail := func(format string, args ...any) {
		panic(&RegistrationError{msg: fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
	}
	if badServiceName(s.Name) {
		fail("app: Service has no name: set Service.Name, the word the log and the errors call this service by")
	}
	if s.Start == nil && s.BeforeShutdown == nil && s.Stop == nil {
		fail("app: Service %q has no Start, BeforeShutdown or Stop: a service with nothing to run is a mistake; set at least one of them", s.Name)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: fmt.Sprintf("app: Service %q registered at %s after the app started serving: services can be added only before it does", s.Name, where)})
	}
	for _, e := range a.life.services {
		if e.Name == s.Name {
			fail("app: Service %q is registered twice, first at %s: a name is one service; give the second its own name", s.Name, e.where)
		}
	}
	if a.life.stopDone == nil {
		a.life.stopDone = make(chan struct{})
		a.life.startDone = make(chan struct{})
	}
	a.life.services = append(a.life.services, serviceEntry{Service: s, where: where})
}

// badServiceName is true for a name the log could not show whole: empty, or with a space, a
// quote or a control character.
func badServiceName(name string) bool {
	return name == "" || strings.ContainsFunc(name, func(r rune) bool { return r == ' ' || r == '"' || r == '\'' || r == '`' || r < 0x20 || r == 0x7f })
}

type serviceEntry struct {
	Service
	where string
}

// liveService is a service the start phase has begun: its life context, and whether Start returned
// nil (or there was none).
type liveService struct {
	serviceEntry
	cancel  context.CancelCauseFunc
	started bool
}

// lifecycle is the app's services and the one start and one stop they go through (ADR 0079).
type lifecycle struct {
	services []serviceEntry // App.Service, in order; guarded by App.mu, frozen once serving
	timeout  time.Duration  // WithServiceTimeout; 0 is the default

	mu        sync.Mutex
	begun     bool
	closed    bool // Shutdown or Close was called
	stopTaken bool // the Stop phase has begun
	live      []*liveService
	startDone chan struct{} // closed when the start phase ends
	startErr  error

	beforeOnce sync.Once
	stopOnce   sync.Once
	stopDone   chan struct{} // closed when the Stop phase has ended
	stopErr    error
}

func (lc *lifecycle) wait() time.Duration {
	if lc.timeout > 0 {
		return lc.timeout
	}
	return defaultServiceTimeout
}

// begin runs the start phase once, for the first Serve that has bound its listener; any other
// waits for it. A nil error means every Start returned nil. ErrServerClosed itself is returned
// when Shutdown or Close came first.
func (a *App) beginServices() error {
	lc := &a.life
	if len(lc.services) == 0 {
		return nil
	}
	lc.mu.Lock()
	if lc.begun {
		done := lc.startDone
		lc.mu.Unlock()
		<-done
		return lc.startErr
	}
	lc.begun = true
	if lc.closed {
		lc.startErr = ErrServerClosed
		close(lc.startDone)
		lc.mu.Unlock()
		return ErrServerClosed
	}
	lc.mu.Unlock()
	err, failed := a.runStarts()
	if failed {
		lc.beforeOnce.Do(func() {})
		ctx, cancel := context.WithTimeout(context.Background(), lc.wait())
		a.stopServices(ctx)
		cancel()
	}
	lc.startErr = err
	close(lc.startDone)
	return err
}

// runStarts calls every Start in order. failed is true when one failed, and err is then the
// wrapped error; an abort by Shutdown or Close is ErrServerClosed with failed false.
func (a *App) runStarts() (err error, failed bool) {
	lc := &a.life
	for _, e := range lc.services {
		lc.mu.Lock()
		if lc.closed {
			lc.mu.Unlock()
			return ErrServerClosed, false
		}
		ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), serviceAppKey{}, &a.info))
		ls := &liveService{serviceEntry: e, cancel: cancel}
		lc.live = append(lc.live, ls)
		lc.mu.Unlock()
		if e.Start != nil {
			if err := a.invoke(e.Name, "Start", e.Start, ctx, true); err != nil {
				cancel(err)
				return err, true
			}
		}
		lc.mu.Lock()
		ls.started = true
		late := lc.stopTaken
		lc.mu.Unlock()
		if late { // Shutdown ended its stop phase while this Start was still running
			cancel(ErrServerClosed)
			if e.Stop != nil {
				tctx, tcancel := context.WithTimeout(context.Background(), lc.wait())
				a.invoke(e.Name, "Stop", e.Stop, tctx, false)
				tcancel()
			}
			return ErrServerClosed, false
		}
	}
	return nil, false
}

// markClosed records that Shutdown or Close was called. A start phase that is running is told to
// stop: no further Start is called and the contexts of the services already live are cancelled.
func (lc *lifecycle) markClosed() {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.closed = true
	if lc.begun {
		select {
		case <-lc.startDone:
		default:
			for _, ls := range lc.live {
				ls.cancel(ErrServerClosed)
			}
		}
	}
}

// waitStart waits for the start phase to end, within ctx.
func (lc *lifecycle) waitStart(ctx context.Context) {
	lc.mu.Lock()
	begun, done := lc.begun, lc.startDone
	lc.mu.Unlock()
	if !begun {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// beforeShutdown runs the BeforeShutdown functions once, in reverse order, within the first half of
// what is left of ctx.
func (a *App) beforeShutdown(ctx context.Context) error {
	lc := &a.life
	if len(lc.services) == 0 {
		return nil
	}
	var errs []error
	lc.beforeOnce.Do(func() {
		lc.markClosed()
		lc.waitStart(ctx)
		lc.mu.Lock()
		live := append([]*liveService(nil), lc.live...)
		skip := lc.stopTaken
		lc.mu.Unlock()
		if skip {
			return
		}
		bctx := ctx
		if dl, ok := ctx.Deadline(); ok {
			var cancel context.CancelFunc
			bctx, cancel = context.WithTimeout(ctx, time.Until(dl)/2)
			defer cancel()
		}
		for i := len(live) - 1; i >= 0; i-- {
			ls := live[i]
			lc.mu.Lock()
			ok := ls.started
			lc.mu.Unlock()
			if ok && ls.BeforeShutdown != nil {
				if err := a.invoke(ls.Name, "BeforeShutdown", ls.BeforeShutdown, bctx, false); err != nil {
					errs = append(errs, err)
				}
			}
		}
	})
	return joinErrors(errs...)
}

// stopServices runs the Stop functions once, in reverse order, and ends the stop phase. Every
// caller gets the joined errors.
func (a *App) stopServices(ctx context.Context) error {
	lc := &a.life
	if len(lc.services) == 0 {
		return nil
	}
	lc.stopOnce.Do(func() {
		lc.mu.Lock()
		lc.stopTaken = true
		live := append([]*liveService(nil), lc.live...)
		lc.mu.Unlock()
		var errs []error
		for i := len(live) - 1; i >= 0; i-- {
			ls := live[i]
			lc.mu.Lock()
			ok := ls.started
			lc.mu.Unlock()
			ls.cancel(ErrServerClosed)
			if ok && ls.Stop != nil {
				if err := a.invoke(ls.Name, "Stop", ls.Stop, ctx, false); err != nil {
					errs = append(errs, err)
				}
			}
		}
		lc.stopErr = joinErrors(errs...)
		close(lc.stopDone)
	})
	return lc.stopErr
}

// waitStopped is what Listen and Serve do after the server has closed: wait for the services to
// stop, so main returning does not end the process in the middle of a Stop (ADR 0079).
func (a *App) waitStopped() {
	if len(a.life.services) > 0 {
		<-a.life.stopDone
	}
}

// invoke calls one service function, recovering a panic, and waits for it. With timed (Start, or a
// context already done) it waits at most WithServiceTimeout; otherwise until ctx ends. A function
// that has not returned by then is abandoned. Every outcome is logged on one line.
func (a *App) invoke(name, phase string, fn func(context.Context) error, ctx context.Context, timed bool) error {
	res := make(chan error, 1)
	began := time.Now()
	go func() {
		defer func() {
			if v := recover(); v != nil {
				a.log.Error("bites: panic in a service function", "service", name, "function", phase, "panic", v, "stack", string(debug.Stack()))
				res <- fmt.Errorf("panic: %v", v)
			}
		}()
		res <- fn(ctx)
	}()
	var timeout <-chan time.Time
	var done <-chan struct{}
	if timed || ctx.Err() != nil {
		t := time.NewTimer(a.life.wait())
		defer t.Stop()
		timeout = t.C
	} else {
		done = ctx.Done()
	}
	var err error
	abandoned := ""
	select {
	case err = <-res:
	case <-timeout:
		abandoned = fmt.Sprintf("%s did not return within the service timeout (%v); it is abandoned", phase, a.life.wait())
	case <-done:
		select {
		case err = <-res:
		default:
			abandoned = phase + " did not return before the shutdown context ended; it is abandoned"
		}
	}
	if abandoned != "" {
		a.log.Warn(fmt.Sprintf("bites: service %q: %s", name, abandoned), "service", name, "function", phase, "took", time.Since(began))
		return fmt.Errorf("bites: service %q: %s: %w", name, phase, context.DeadlineExceeded)
	}
	msg := "bites: service stopped"
	switch phase {
	case "Start":
		msg = "bites: service started"
	case "BeforeShutdown":
		msg = "bites: service left"
	}
	if err != nil {
		a.log.Error(msg, "service", name, "took", time.Since(began), "err", err)
		return fmt.Errorf("bites: service %q: %s: %w", name, phase, err)
	}
	a.log.Log(context.Background(), slog.LevelInfo, msg, "service", name, "took", time.Since(began))
	return nil
}

// serviceAppKey is the context key under which a Service's life carries its app's logger and
// clock, so Logger and Now work on it and Every runs on the app's clock (ADR 0079).
type serviceAppKey struct{}

// serviceApp is what ctx, a Service's life or derived from it, knows of its app, or nil.
func serviceApp(ctx context.Context) *reqctx.App {
	a, _ := ctx.Value(serviceAppKey{}).(*reqctx.App)
	return a
}

// refuseService panics at the constructor of a Service with the line that called it.
func refuseService(ctor, name, where, format string, args ...any) {
	label := ctor
	if name != "" {
		label += fmt.Sprintf("(%q)", name)
	}
	panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
}

// Worker returns a Service that runs run on its own goroutine for the life of the app (ADR 0079).
// Its Start calls "go run(ctx)" with the Service's life as ctx and returns at once, so a slow
// run never delays the app's start. Its Stop waits for run to return: the app cancels ctx (cause
// ErrServerClosed) just before calling Stop, so run must watch ctx.Done and return, and one that
// does not within Shutdown's context (or WithServiceTimeout under Close) is abandoned and logged,
// as every Service function is. Logger(ctx) is the app's logger and Now(ctx) its clock.
//
// A run that returns before Stop, with an error or nil, or that panics, is logged at Error with
// the name of the worker and the error, and is not restarted: a worker that must survive its own
// failure loops inside run. An error from run after ctx was cancelled is logged at Warn, except
// context.Canceled, which is the expected way to stop. Worker panics at the call (a
// RegistrationError naming the line) for a name that is empty or has a space, a quote or a control
// character, and for a nil run. Register the result with App.Service.
func Worker(name string, run func(ctx context.Context) error) Service {
	where := callerWhere()
	if badServiceName(name) {
		refuseService("bites.Worker", "", where, "the name is empty or has a space, a quote or a control character: give the word the log and the errors call this worker by, such as \"mail-sender\"")
	}
	if run == nil {
		refuseService("bites.Worker", name, where, "run is nil: give the function the worker runs until its ctx is cancelled")
	}
	done := make(chan struct{})
	return Service{
		Name: name,
		Start: func(ctx context.Context) error {
			go func() {
				defer close(done)
				err := guard(ctx, name, "worker", run)
				switch {
				case ctx.Err() == nil && err != nil:
					Logger(ctx).Error("bites: worker returned before Stop and is not restarted", "worker", name, "err", err)
				case ctx.Err() == nil:
					Logger(ctx).Error("bites: worker returned before Stop and is not restarted: it returned nil", "worker", name)
				case err != nil && !errors.Is(err, context.Canceled):
					Logger(ctx).Warn("bites: worker returned an error while stopping", "worker", name, "err", err)
				}
			}()
			return nil
		},
		Stop: func(context.Context) error {
			<-done
			return nil
		},
	}
}

// guard calls fn(ctx), recovering a panic into an error and logging it at Error with its stack.
func guard(ctx context.Context, name, kind string, fn func(context.Context) error) (err error) {
	defer func() {
		if v := recover(); v != nil {
			Logger(ctx).Error("bites: panic in a "+kind, kind, name, "panic", v, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return fn(ctx)
}

// EveryOptions are the settings of an Every. The zero value is valid: the first call is one
// interval after the app started serving.
type EveryOptions struct {
	// AtStart, when true, makes the first call of fn right after the Service started, on the
	// Service's own goroutine, before the first interval has passed: warm a cache, or reconcile
	// what a crash left behind. The call does not delay the start: Start returns first and the
	// app accepts connections while fn runs. It is the same call as every other one: the app's
	// clock, logger and ctx, serial with the interval calls, an error logged at Warn and a panic
	// recovered, and Stop cancels its ctx and waits for it. The intervals count from the start,
	// so when the start call takes longer than interval the ticks it missed are dropped and
	// logged as for any call. False (the zero value) is no call at start.
	AtStart bool
}

// Every returns a Service that calls fn once every interval for the life of the app, on the app's
// clock (WithClock, bitetest.Clock), so a test moves time instead of waiting (ADR 0079). By
// default the first call is one interval after the app started serving, not at start: a start
// never waits for the work, and a deploy does not run every sweep at once. For a first call right
// after the start, give EveryOptions{AtStart: true}.
//
// Calls are serial: fn is never running twice. A call that takes longer than interval delays the
// next one until it returns and the ticks it missed are dropped, not run in a burst: the next call
// is at the next multiple of interval after the first call's due time that is still ahead, and
// the log says at Warn how many were skipped. fn's ctx is the Service's life, cancelled before
// Stop, which waits for a call in progress to return (bounded as every Service function is).
//
// An error from fn is logged at Warn with the name, and the loop goes on. A panic in fn is
// recovered, logged at Error with its stack, and the loop goes on. Every panics at the call (a
// RegistrationError naming the line) for a name that is empty or has a space, a quote or a
// control character, for a nil fn, for an interval that is zero or negative, and when more than
// one EveryOptions is given. Cron expressions and jitter are not offered. Register the result
// with App.Service.
func Every(name string, interval time.Duration, fn func(ctx context.Context) error, opts ...EveryOptions) Service {
	where := callerWhere()
	if badServiceName(name) {
		refuseService("bites.Every", "", where, "the name is empty or has a space, a quote or a control character: give the word the log and the errors call this job by, such as \"purge-deleted\"")
	}
	if fn == nil {
		refuseService("bites.Every", name, where, "fn is nil: give the function called once every interval")
	}
	if interval <= 0 {
		shown := "0"
		if interval != 0 {
			shown = interval.String()
		}
		refuseService("bites.Every", name, where, "the interval %s is not a time between calls: give a positive duration, such as time.Minute", shown)
	}
	var o EveryOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseService("bites.Every", name, where, "%d options values were given; give one EveryOptions", len(opts))
	}
	done := make(chan struct{})
	return Service{
		Name: name,
		Start: func(ctx context.Context) error {
			var clk clock.Clock = clock.Real()
			if a := serviceApp(ctx); a != nil && a.Clock != nil {
				clk = a.Clock
			}
			// The first timer is armed before Start returns, so the interval counts from the start
			// and a test that advances the clock right after Start cannot miss it. Each timer
			// carries its generation, so a tick of a timer that was replaced is ignored.
			tick := make(chan int64, 1)
			var gen atomic.Int64
			arm := func(d time.Duration) clock.Timer {
				g := gen.Add(1)
				return clk.AfterFunc(d, func() {
					select {
					case tick <- g:
					default:
					}
				})
			}
			due := clk.Now().Add(interval)
			first := arm(interval)
			go func() {
				defer close(done)
				runEvery(ctx, clk, name, interval, fn, o.AtStart, tick, &gen, arm, due, first)
			}()
			return nil
		},
		Stop: func(context.Context) error {
			<-done
			return nil
		},
	}
}

func runEvery(ctx context.Context, clk clock.Clock, name string, interval time.Duration, fn func(context.Context) error, atStart bool, tick <-chan int64, gen *atomic.Int64, arm func(time.Duration) clock.Timer, due time.Time, t clock.Timer) {
	defer func() { t.Stop() }()
	for startRun := atStart; ; {
		if !startRun {
			select {
			case <-ctx.Done():
				return
			case g := <-tick:
				if g != gen.Load() {
					continue
				}
			}
		} else if ctx.Err() != nil {
			return
		}
		if err := guard(ctx, name, "job", fn); err != nil && ctx.Err() == nil {
			Logger(ctx).Warn("bites: job failed, trying again at the next tick", "job", name, "err", err)
		}
		if ctx.Err() != nil {
			return
		}
		now := clk.Now()
		if startRun {
			startRun = false
			if now.Before(due) {
				continue // the timer armed in Start is still the next call
			}
			// The call at start overran the first interval: it stands in for the call due at the
			// start, and the timer armed in Start is replaced below.
			due = due.Add(-interval)
		}
		due = due.Add(interval)
		skipped := 0
		for !due.After(now) {
			due = due.Add(interval)
			skipped++
		}
		if skipped > 0 {
			Logger(ctx).Warn("bites: job took longer than its interval, the missed ticks are dropped", "job", name, "interval", interval, "skipped", skipped)
		}
		t.Stop()
		t = arm(due.Sub(now))
	}
}
