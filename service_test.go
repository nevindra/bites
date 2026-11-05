package bites_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// svcEvents records what services did, in order, from any goroutine.
type svcEvents struct {
	mu sync.Mutex
	ev []string
}

func (e *svcEvents) add(s string) {
	e.mu.Lock()
	e.ev = append(e.ev, s)
	e.mu.Unlock()
}

func (e *svcEvents) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.ev)
}

func (e *svcEvents) has(s string) bool { return slices.Contains(e.list(), s) }

// recording is a Service that records each of its three functions as "name start" and so on.
func (e *svcEvents) recording(name string) bites.Service {
	f := func(what string) func(context.Context) error {
		return func(context.Context) error { e.add(name + " " + what); return nil }
	}
	return bites.Service{Name: name, Start: f("start"), BeforeShutdown: f("before"), Stop: f("stop")}
}

// svcLogBuf is a log destination that is safe to read while the app logs.
type svcLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *svcLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *svcLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newSvcLogBuf() (*svcLogBuf, *slog.Logger) {
	lb := &svcLogBuf{}
	return lb, slog.New(slog.NewTextHandler(lb, nil))
}

func svcNoop(context.Context) error { return nil }

func svcWithin(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish in 5 seconds", what)
	}
}

func svcEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not happen in 5 seconds", what)
}

func shutdownCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// svcServeTCP serves app on a loopback port and returns the address and what Serve returns.
func svcServeTCP(t *testing.T, app *bites.App) (string, chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Serve(ln) }()
	t.Cleanup(func() { app.Close() })
	return ln.Addr().String(), done
}

func TestServicesStartInRegistrationOrderAndStopInReverse(t *testing.T) {
	var e svcEvents
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	for _, n := range []string{"a", "b", "c"} {
		app.Service(e.recording(n))
	}
	bitetest.Start(t, app)
	if err := app.Shutdown(shutdownCtx(t)); err != nil {
		t.Fatal(err)
	}
	want := []string{"a start", "b start", "c start", "c before", "b before", "a before", "c stop", "b stop", "a stop"}
	if got := e.list(); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAServiceStartRunsAfterTheListenerIsBoundAndBeforeTheFirstConnectionIsAccepted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	var e svcEvents
	app := bites.New()
	app.Get("/ping", func(context.Context, Empty) (struct{}, error) { e.add("handled"); return struct{}{}, nil })
	app.Service(bites.Service{Name: "probe", Start: func(context.Context) error {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			return fmt.Errorf("the listener was not bound: %w", err)
		}
		defer c.Close()
		fmt.Fprint(c, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, err := c.Read(make([]byte, 1)); err == nil || n != 0 {
			return errors.New("a connection was answered during Start")
		}
		e.add("probe start")
		return nil
	}})
	done := make(chan error, 1)
	go func() { done <- app.Serve(ln) }()
	t.Cleanup(func() { app.Close() })
	svcEventually(t, "the probe's Start", func() bool { return e.has("probe start") })
	r, err := http.Get("http://" + ln.Addr().String() + "/ping")
	if err != nil || r.StatusCode != 204 && r.StatusCode != 200 {
		t.Fatalf("after Start the app serves: %v %v", r, err)
	}
	r.Body.Close()
}

func TestAConnectionDuringStartIsAnsweredOnlyAfterEveryStartHasReturned(t *testing.T) {
	var e svcEvents
	release := make(chan struct{})
	entered := make(chan struct{})
	app := bites.New()
	app.Get("/ping", func(context.Context, Empty) (struct{}, error) { e.add("handled"); return struct{}{}, nil })
	app.Service(bites.Service{Name: "slow", Start: func(context.Context) error {
		close(entered)
		<-release
		e.add("slow start")
		return nil
	}})
	app.Service(e.recording("fast"))
	addr, _ := svcServeTCP(t, app)
	<-entered
	answered := make(chan string, 1)
	go func() {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			answered <- err.Error()
			return
		}
		defer c.Close()
		fmt.Fprint(c, "GET /ping HTTP/1.1\r\nHost: x\r\n\r\n")
		line, _ := bufio.NewReader(c).ReadString('\n')
		answered <- line
	}()
	select {
	case l := <-answered:
		t.Fatalf("answered during Start: %q", l)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case l := <-answered:
		if !strings.HasPrefix(l, "HTTP/1.1 2") {
			t.Fatalf("got %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never answered")
	}
	if got, want := e.list(), []string{"slow start", "fast start", "handled"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAListenOnAnAddressInUseReturnsItsErrorBeforeAnyServiceStarts(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer busy.Close()
	var e svcEvents
	app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
	app.Service(e.recording("a"))
	if err := app.Listen(busy.Addr().String()); err == nil || errors.Is(err, bites.ErrServerClosed) {
		t.Fatalf("Listen = %v, want the bind error", err)
	}
	if got := e.list(); len(got) != 0 {
		t.Fatalf("a service ran: %v", got)
	}
}

func svcFailing(e *svcEvents, name string, err error) bites.Service {
	s := e.recording(name)
	s.Start = func(context.Context) error { e.add(name + " start"); return err }
	return s
}

func TestAFailedStartStopsTheEarlierServicesInReverseAndListenReturnsTheErrorNamingTheService(t *testing.T) {
	var e svcEvents
	lb, log := newSvcLogBuf()
	app := bites.New(bites.WithLogger(log))
	app.Service(e.recording("a"))
	app.Service(e.recording("b"))
	app.Service(svcFailing(&e, "c", errors.New("boom")))
	app.Service(e.recording("d"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	addr := ln.Addr().String()
	var got error
	svcWithin(t, "Serve", func() { got = app.Serve(ln) })
	if got == nil || got.Error() != `bites: service "c": Start: boom` {
		t.Fatalf("Serve = %v", got)
	}
	want := []string{"a start", "b start", "c start", "b stop", "a stop"}
	if g := e.list(); !slices.Equal(g, want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("the listener was left open")
	}
	if !strings.Contains(lb.String(), `service=c`) || !strings.Contains(lb.String(), "boom") {
		t.Fatalf("the log does not say which service failed:\n%s", lb)
	}
}

type startErr struct{ code int }

func (e *startErr) Error() string { return fmt.Sprint("start failed with ", e.code) }

func TestAFailedStartIsSeenByErrorsIsAndErrorsAs(t *testing.T) {
	sentinel := errors.New("sentinel")
	for _, tc := range []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"errors.Is", fmt.Errorf("wrapped: %w", sentinel), func(e error) bool { return errors.Is(e, sentinel) }},
		{"errors.As", &startErr{7}, func(e error) bool { var s *startErr; return errors.As(e, &s) && s.code == 7 }},
	} {
		var e svcEvents
		app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
		app.Service(svcFailing(&e, "q", tc.err))
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip(err)
		}
		if got := app.Serve(ln); !tc.is(got) {
			t.Errorf("%s does not see the cause in %v", tc.name, got)
		}
	}
}

func TestAServiceWhoseStartFailedIsNotStopped(t *testing.T) {
	var e svcEvents
	app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
	app.Service(svcFailing(&e, "x", errors.New("no")))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	app.Serve(ln)
	app.Close()
	if got := e.list(); !slices.Equal(got, []string{"x start"}) {
		t.Fatalf("got %v", got)
	}
}

func TestAPanicInAServiceFunctionIsRecoveredLoggedAndTreatedAsItsError(t *testing.T) {
	lb, log := newSvcLogBuf()
	var e svcEvents
	app := bites.New(bites.WithLogger(log), bites.WithClock(bitetest.NewClock()))
	app.Service(e.recording("a"))
	app.Service(bites.Service{Name: "bad", Stop: func(context.Context) error { panic("kaboom") }})
	bitetest.Start(t, app)
	err := app.Shutdown(shutdownCtx(t))
	if err == nil || !strings.Contains(err.Error(), `bites: service "bad": Stop: panic: kaboom`) {
		t.Fatalf("Shutdown = %v", err)
	}
	if !e.has("a stop") {
		t.Fatal("the next Stop did not run")
	}
	if s := lb.String(); !strings.Contains(s, "kaboom") || !strings.Contains(s, "stack=") {
		t.Fatalf("the panic and its stack are not logged:\n%s", s)
	}
	// A panic in Start refuses the start.
	app2 := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
	app2.Service(bites.Service{Name: "p", Start: func(context.Context) error { panic("early") }})
	ln, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		t.Skip(err2)
	}
	if got := app2.Serve(ln); got == nil || got.Error() != `bites: service "p": Start: panic: early` {
		t.Fatalf("Serve = %v", got)
	}
}

func TestTheContextOfStartIsTheServicesLifeAndIsCancelledJustBeforeItsStopWithCauseErrServerClosed(t *testing.T) {
	var life context.Context
	var duringBefore, duringStop error
	var cause error
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(bites.Service{
		Name:           "w",
		Start:          func(ctx context.Context) error { life = ctx; return nil },
		BeforeShutdown: func(context.Context) error { duringBefore = life.Err(); return nil },
		Stop:           func(context.Context) error { duringStop = life.Err(); cause = context.Cause(life); return nil },
	})
	bitetest.Start(t, app)
	if _, ok := life.Deadline(); ok {
		t.Fatal("the life context has a deadline")
	}
	if life.Err() != nil {
		t.Fatal("the life context ended when Start returned")
	}
	app.Shutdown(shutdownCtx(t))
	if duringBefore != nil || duringStop == nil || !errors.Is(cause, bites.ErrServerClosed) {
		t.Fatalf("before %v, stop %v, cause %v", duringBefore, duringStop, cause)
	}
}

func TestAWorkerStartedWithTheStartContextKeepsRunningAfterStartReturns(t *testing.T) {
	var running atomic.Bool
	stopped := make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(bites.Service{Name: "worker", Start: func(ctx context.Context) error {
		running.Store(true)
		go func() { <-ctx.Done(); running.Store(false); close(stopped) }()
		return nil
	}})
	bitetest.Start(t, app)
	time.Sleep(50 * time.Millisecond)
	if !running.Load() {
		t.Fatal("the worker ended after Start returned")
	}
	app.Shutdown(shutdownCtx(t))
	svcWithin(t, "the worker's end", func() { <-stopped })
}

func TestStopRunsAfterTheDrainSoARequestInFlightStillUsesTheService(t *testing.T) {
	var open atomic.Bool
	var stopped atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	var usedOpen atomic.Bool
	app.Get("/slow", func(context.Context, Empty) (struct{}, error) {
		close(entered)
		<-release
		usedOpen.Store(open.Load())
		return struct{}{}, nil
	})
	app.Service(bites.Service{Name: "pool", Start: func(context.Context) error { open.Store(true); return nil },
		Stop: func(context.Context) error { open.Store(false); stopped.Store(true); return nil }})
	srv := bitetest.Start(t, app)
	go srv.Do(req("GET", "/slow"))
	<-entered
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(shutdownCtx(t)) }()
	time.Sleep(100 * time.Millisecond)
	if stopped.Load() {
		t.Fatal("Stop ran while a request was in flight")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() || !usedOpen.Load() {
		t.Fatalf("stopped %v, request saw an open service %v", stopped.Load(), usedOpen.Load())
	}
}

func TestBeforeShutdownRunsAfterReadinessFailsAndBeforeTheShutdownDelayAndTheListenerClosing(t *testing.T) {
	clk := bitetest.NewClock()
	var ready, other, delayStarted int
	var srv *bitetest.Server
	app := bites.New(bites.WithClock(clk), bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}), bites.WithShutdownDelay(5*time.Second))
	app.Get("/orders", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
	app.Service(bites.Service{Name: "registry", BeforeShutdown: func(context.Context) error {
		delayStarted = clk.Pending()
		ready = srv.Do(req("GET", "/readyz")).Status
		other = srv.Do(req("GET", "/orders")).Status
		return nil
	}})
	srv = bitetest.Start(t, app)
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(shutdownCtx(t)) }()
	svcEventually(t, "the delay", func() bool { return clk.Pending() > 0 })
	clk.Advance(5 * time.Second)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ready != 503 || other != 200 || delayStarted != 0 {
		t.Fatalf("readiness %d, a request %d, timers pending when the hook ran %d", ready, other, delayStarted)
	}
}

func TestBeforeShutdownIsSkippedByCloseAndStopStillRuns(t *testing.T) {
	var e svcEvents
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(e.recording("a"))
	bitetest.Start(t, app)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := e.list(), []string{"a start", "a stop"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEveryStopRunsEvenWhenAnEarlierOneReturnedAnError(t *testing.T) {
	var e svcEvents
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(e.recording("a"))
	bad := e.recording("b")
	bad.Stop = func(context.Context) error { e.add("b stop"); return errors.New("flush failed") }
	app.Service(bad)
	app.Service(e.recording("c"))
	bitetest.Start(t, app)
	app.Shutdown(shutdownCtx(t))
	if got := e.list(); !slices.Equal(got[len(got)-3:], []string{"c stop", "b stop", "a stop"}) {
		t.Fatalf("got %v", got)
	}
}

func TestShutdownReturnsTheErrorsOfTheServicesJoinedWithTheEngines(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/slow", func(context.Context, Empty) (struct{}, error) { close(entered); <-release; return struct{}{}, nil })
	cause := errors.New("flush failed")
	app.Service(bites.Service{Name: "a", BeforeShutdown: func(context.Context) error { return errors.New("leave failed") }, Stop: func(context.Context) error { return cause }})
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:4000")
	c.Write([]byte("GET /slow HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := app.Shutdown(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cause) || !strings.Contains(err.Error(), "leave failed") {
		t.Fatalf("Shutdown = %v", err)
	}
}

func TestAStopThatIgnoresItsContextIsAbandonedLoggedAndTheNextStopStillRuns(t *testing.T) {
	lb, log := newSvcLogBuf()
	var e svcEvents
	block := make(chan struct{})
	defer close(block)
	app := bites.New(bites.WithLogger(log), bites.WithClock(bitetest.NewClock()))
	app.Service(e.recording("first"))
	app.Service(bites.Service{Name: "worker", Stop: func(context.Context) error { <-block; return nil }})
	bitetest.Start(t, app)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := app.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v", err)
	}
	if !e.has("first stop") {
		t.Fatal("the next Stop did not run")
	}
	if !strings.Contains(lb.String(), `bites: service \"worker\": Stop did not return before the shutdown context ended; it is abandoned`) {
		t.Fatalf("the log does not name the abandoned function:\n%s", lb)
	}
}

func TestBeforeShutdownHooksCannotSpendMoreThanHalfOfWhatIsLeftOfShutdownsContext(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var e svcEvents
	app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)), bites.WithClock(bitetest.NewClock()))
	app.Service(bites.Service{Name: "hung", BeforeShutdown: func(context.Context) error { <-block; return nil }})
	app.Service(bites.Service{Name: "after", Start: func(context.Context) error { return nil }, Stop: func(ctx context.Context) error {
		dl, _ := ctx.Deadline()
		e.add(fmt.Sprint("left ", time.Until(dl) > 300*time.Millisecond))
		return nil
	}})
	bitetest.Start(t, app)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	begin := time.Now()
	err := app.Shutdown(ctx)
	if took := time.Since(begin); took > 700*time.Millisecond {
		t.Fatalf("a hung hook spent %v of a 1s context", took)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v", err)
	}
	if got := e.list(); !slices.Equal(got, []string{"left true"}) {
		t.Fatalf("the drain was left too little: %v", got)
	}
}

func TestListenReturnsErrServerClosedOnlyAfterTheStopsHaveReturned(t *testing.T) {
	var stopped atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	app := bites.New()
	app.Service(bites.Service{Name: "slow", Stop: func(context.Context) error { <-release; stopped.Store(true); return nil }, Start: func(context.Context) error { close(started); return nil }})
	_, done := svcServeTCP(t, app)
	<-started
	go app.Shutdown(shutdownCtx(t))
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v before the Stop", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != bites.ErrServerClosed || !stopped.Load() {
			t.Fatalf("Serve = %v, stopped %v", err, stopped.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never returned")
	}
}

func TestTwoServesOnOneAppStartTheServicesOnceAndStopThemOnce(t *testing.T) {
	var starts, stops atomic.Int32
	app := bites.New()
	app.Service(bites.Service{Name: "s", Start: func(context.Context) error { starts.Add(1); return nil }, Stop: func(context.Context) error { stops.Add(1); return nil }})
	_, d1 := svcServeTCP(t, app)
	_, d2 := svcServeTCP(t, app)
	svcEventually(t, "the start", func() bool { return starts.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if err := app.Shutdown(shutdownCtx(t)); err != nil {
		t.Fatal(err)
	}
	for _, d := range []chan error{d1, d2} {
		if err := <-d; err != bites.ErrServerClosed {
			t.Fatalf("Serve = %v", err)
		}
	}
	if starts.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("starts %d, stops %d", starts.Load(), stops.Load())
	}
}

func TestAShutdownWhileStartIsRunningStopsWhatStartedAndEveryServeReturnsErrServerClosed(t *testing.T) {
	var e svcEvents
	entered := make(chan struct{})
	var sawCancel atomic.Bool
	app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
	app.Service(e.recording("a"))
	app.Service(bites.Service{Name: "slow", Start: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		sawCancel.Store(true)
		return nil
	}, Stop: func(context.Context) error { e.add("slow stop"); return nil }})
	app.Service(e.recording("never"))
	_, d1 := svcServeTCP(t, app)
	_, d2 := svcServeTCP(t, app)
	<-entered
	if err := app.Shutdown(shutdownCtx(t)); err != nil {
		t.Fatal(err)
	}
	for _, d := range []chan error{d1, d2} {
		if err := <-d; err != bites.ErrServerClosed {
			t.Fatalf("Serve = %v", err)
		}
	}
	if !sawCancel.Load() || e.has("never start") {
		t.Fatalf("cancelled %v, svcEvents %v", sawCancel.Load(), e.list())
	}
	got := e.list()
	if i, j := slices.Index(got, "a stop"), slices.Index(got, "slow stop"); i < 0 || j < 0 || j > i {
		t.Fatalf("svcEvents %v", got)
	}
}

func TestEachServiceFunctionLogsWhenItEndsAndAFailureLogsTheError(t *testing.T) {
	lb, log := newSvcLogBuf()
	var e svcEvents
	app := bites.New(bites.WithLogger(log), bites.WithClock(bitetest.NewClock()))
	app.Service(e.recording("ok"))
	bad := e.recording("bad")
	bad.Stop = func(context.Context) error { return errors.New("not flushed") }
	app.Service(bad)
	bitetest.Start(t, app)
	app.Shutdown(shutdownCtx(t))
	s := lb.String()
	for _, want := range []string{
		"level=INFO msg=\"bites: service started\" service=ok took=",
		"level=INFO msg=\"bites: service left\" service=bad took=",
		"level=INFO msg=\"bites: service stopped\" service=ok took=",
		"level=ERROR msg=\"bites: service stopped\" service=bad took=",
		"err=\"not flushed\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the log lacks %s:\n%s", want, s)
		}
	}
}

func TestTheStartUpSettingsListTheServicesAndTheServiceTimeoutAsDefaultOrCustom(t *testing.T) {
	for _, tc := range []struct {
		opts   []bites.Option
		source string
		value  string
	}{
		{nil, "default", "30s"},
		{[]bites.Option{bites.WithServiceTimeout(time.Minute)}, "custom", "1m0s"},
	} {
		lb, log := newSvcLogBuf()
		app := bites.New(append(tc.opts, bites.WithLogger(log))...)
		app.Service(bites.Service{Name: "db", Stop: func(context.Context) error { return nil }})
		app.Service(bites.Service{Name: "queue", Stop: func(context.Context) error { return nil }})
		bites.StartForTest(app)
		s := lb.String()
		for _, want := range []string{
			`setting=services value="db, queue" source=custom`,
			"setting=\"service timeout\" value=" + tc.value + " source=" + tc.source,
		} {
			if !strings.Contains(s, want) {
				t.Errorf("the settings lack %s:\n%s", want, s)
			}
		}
	}
}

// fakeTB is a testing.TB whose Fatalf records the message and ends the goroutine, and whose
// Cleanup functions are dropped, so a test can watch another test fail.
type fakeTB struct {
	testing.TB
	fatal atomic.Pointer[string]
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	m := fmt.Sprintf(format, args...)
	f.fatal.Store(&m)
	runtime.Goexit()
}
func (f *fakeTB) Cleanup(func()) {}

func TestBitetestStartRunsTheStartFunctionsBeforeItReturnsAndFailsTheTestWhenOneFails(t *testing.T) {
	var started atomic.Bool
	app := bites.New()
	app.Service(bites.Service{Name: "s", Start: func(context.Context) error { started.Store(true); return nil }})
	bitetest.Start(t, app)
	if !started.Load() {
		t.Fatal("Start returned before the Start function")
	}
	// A svcFailing Start fails the test; run it in a goroutine so Fatalf's Goexit ends only it.
	ft := &fakeTB{TB: t}
	failed := make(chan struct{})
	go func() {
		defer close(failed)
		bad := bites.New()
		bad.Service(bites.Service{Name: "bad", Start: func(context.Context) error { return errors.New("no database") }})
		bitetest.Start(ft, bad)
		t.Error("Start returned for a failed Start function")
	}()
	svcWithin(t, "the svcFailing Start", func() { <-failed })
	if msg := ft.fatal.Load(); msg == nil || !strings.Contains(*msg, "no database") {
		t.Fatalf("the test did not fail with the error: %v", msg)
	}
}

func TestBitetestShutdownAndCleanupStopTheServicesSoATestLeaksNoWorker(t *testing.T) {
	var e svcEvents
	t.Run("cleanup", func(t *testing.T) {
		app := bites.New()
		app.Service(e.recording("a"))
		bitetest.Start(t, app)
	})
	if got, want := e.list(), []string{"a start", "a stop"}; !slices.Equal(got, want) {
		t.Fatalf("after the cleanup: %v", got)
	}
	var f svcEvents
	app := bites.New()
	app.Service(f.recording("b"))
	bitetest.Start(t, app)
	app.Shutdown(shutdownCtx(t))
	if got := f.list(); !slices.Equal(got, []string{"b start", "b before", "b stop"}) {
		t.Fatalf("after Shutdown: %v", got)
	}
}

func TestAServiceRegisteredFromInsideAStartFailsTheStartWithTheRefusalNamingWhereItWasRegistered(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.DiscardHandler)))
	app.Service(bites.Service{Name: "outer", Start: func(context.Context) error {
		app.Service(bites.Service{Name: "inner", Stop: svcNoop})
		return nil
	}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	got := app.Serve(ln)
	if got == nil || !strings.Contains(got.Error(), `bites: service "outer": Start: panic: app: Service "inner" registered at service_test.go:`) ||
		!strings.Contains(got.Error(), "after the app started serving: services can be added only before it does") {
		t.Fatalf("Serve = %v", got)
	}
}

// everyApp serves one Every job on a fake clock and calls fn's start on each run.
func everyApp(t *testing.T, interval time.Duration, fn func(context.Context) error, opts ...bites.Option) (*bitetest.Clock, *bites.App) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(append([]bites.Option{bites.WithClock(clk)}, opts...)...)
	app.Service(bites.Every("job", interval, fn))
	bitetest.Start(t, app)
	return clk, app
}

// tick moves the clock by d once the job has armed its next timer, which it does after a run.
func tick(t *testing.T, clk *bitetest.Clock, d time.Duration) {
	t.Helper()
	svcEventually(t, "the job to arm its timer", func() bool { _, ok := clk.Next(); return ok })
	clk.Advance(d)
}

func TestEveryRunsFnOncePerIntervalOnTheAppsClockAndNotAtStart(t *testing.T) {
	var runs atomic.Int32
	clk, _ := everyApp(t, time.Minute, func(context.Context) error { runs.Add(1); return nil })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("fn ran %d times at start; want 0, the first run is one interval later", n)
	}
	for i := int32(1); i <= 3; i++ {
		tick(t, clk, time.Minute)
		svcEventually(t, "the run", func() bool { return runs.Load() == i })
	}
	tick(t, clk, 30*time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 3 {
		t.Fatalf("fn ran %d times after half an interval more; want 3", n)
	}
}

func TestAnEveryThatWorksOnAServiceContextSeesTheAppsClockAndLogger(t *testing.T) {
	lb, log := newSvcLogBuf()
	var seen atomic.Value
	clk, _ := everyApp(t, time.Hour, func(ctx context.Context) error {
		seen.Store(bites.Now(ctx))
		bites.Logger(ctx).Info("from the job")
		return nil
	}, bites.WithLogger(log))
	clk.Advance(time.Hour)
	svcEventually(t, "the run", func() bool { return seen.Load() != nil })
	if got, want := seen.Load().(time.Time), clk.Now(); !got.Equal(want) {
		t.Fatalf("bites.Now(ctx) in the job is %v; want the app's clock %v", got, want)
	}
	if !strings.Contains(lb.String(), "from the job") {
		t.Fatalf("bites.Logger(ctx) in the job is not the app's logger; log:\n%s", lb.String())
	}
}

func TestARunLongerThanTheIntervalDelaysTheNextAndNeverOverlaps(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	entered, release := make(chan struct{}, 8), make(chan struct{})
	lb, log := newSvcLogBuf()
	clk, _ := everyApp(t, time.Minute, func(context.Context) error {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		runs.Add(1)
		entered <- struct{}{}
		<-release
		running.Add(-1)
		return nil
	}, bites.WithLogger(log))
	clk.Advance(time.Minute)
	<-entered
	clk.Advance(2*time.Minute + 10*time.Second) // two more ticks fall due while the run is in progress
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("a second run started while the first was in progress (%d runs)", n)
	}
	release <- struct{}{}
	svcEventually(t, "the skipped ticks to be logged", func() bool { return strings.Contains(lb.String(), "skipped=2") })
	tick(t, clk, time.Minute)
	svcWithin(t, "the next run", func() { <-entered })
	close(release)
	if m := maxRunning.Load(); m != 1 {
		t.Fatalf("%d runs overlapped", m)
	}
	if !strings.Contains(lb.String(), "the missed ticks are dropped") {
		t.Fatalf("the dropped ticks were not logged:\n%s", lb.String())
	}
}

func TestAnErrorFromEveryIsLoggedAtWarnWithTheNameAndTheLoopContinues(t *testing.T) {
	var runs atomic.Int32
	lb, log := newSvcLogBuf()
	clk, _ := everyApp(t, time.Minute, func(context.Context) error { runs.Add(1); return errors.New("db down") }, bites.WithLogger(log))
	for i := int32(1); i <= 2; i++ {
		tick(t, clk, time.Minute)
		svcEventually(t, "the run", func() bool { return runs.Load() == i })
	}
	svcEventually(t, "the second warning", func() bool { return strings.Count(lb.String(), "level=WARN") >= 2 })
	out := lb.String()
	if !strings.Contains(out, `job=job`) || !strings.Contains(out, `err="db down"`) {
		t.Fatalf("the Warn does not name the job and the error:\n%s", out)
	}
}

func TestAPanicInEveryIsRecoveredLoggedAtErrorAndTheLoopContinues(t *testing.T) {
	var runs atomic.Int32
	lb, log := newSvcLogBuf()
	clk, _ := everyApp(t, time.Minute, func(context.Context) error {
		if runs.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}, bites.WithLogger(log))
	clk.Advance(time.Minute)
	svcEventually(t, "the first run", func() bool { return runs.Load() == 1 })
	svcEventually(t, "the panic log", func() bool { return strings.Contains(lb.String(), "level=ERROR") })
	tick(t, clk, time.Minute)
	svcEventually(t, "the run after the panic", func() bool { return runs.Load() == 2 })
	if !strings.Contains(lb.String(), "boom") {
		t.Fatalf("the panic value is not in the log:\n%s", lb.String())
	}
}

func TestStopWaitsForARunInProgressAndEveryDoesNotRunAgain(t *testing.T) {
	var runs atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	clk, app := everyApp(t, time.Minute, func(ctx context.Context) error {
		runs.Add(1)
		entered <- struct{}{}
		<-release
		return nil
	})
	clk.Advance(time.Minute)
	<-entered
	stopped := make(chan struct{})
	go func() { app.Shutdown(shutdownCtx(t)); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned while a run was in progress")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	svcWithin(t, "Shutdown", func() { <-stopped })
	clk.Advance(time.Hour)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times; want 1, none after Stop", n)
	}
}

func TestAWorkerRunsWithTheServicesContextAndStopCancelsItAndWaits(t *testing.T) {
	var cause atomic.Value
	returned := make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(bites.Worker("w", func(ctx context.Context) error {
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		time.Sleep(30 * time.Millisecond)
		close(returned)
		return ctx.Err()
	}))
	bitetest.Start(t, app)
	svcWithin(t, "Shutdown", func() { app.Shutdown(shutdownCtx(t)) })
	select {
	case <-returned:
	default:
		t.Fatal("Shutdown returned before the worker's run did")
	}
	if c, _ := cause.Load().(error); !errors.Is(c, bites.ErrServerClosed) {
		t.Fatalf("the worker's context cause is %v; want ErrServerClosed", c)
	}
}

func TestAWorkerThatReturnsAnErrorIsLoggedAndNotRestarted(t *testing.T) {
	var runs atomic.Int32
	lb, log := newSvcLogBuf()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(log))
	app.Service(bites.Worker("mailer", func(context.Context) error { runs.Add(1); return errors.New("smtp refused") }))
	bitetest.Start(t, app)
	svcEventually(t, "the log line", func() bool { return strings.Contains(lb.String(), "not restarted") })
	out := lb.String()
	for _, want := range []string{"level=ERROR", "worker=mailer", `err="smtp refused"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("the worker ran %d times; want 1", n)
	}
	svcWithin(t, "Shutdown", func() { app.Shutdown(shutdownCtx(t)) })
}

func TestAWorkerThatReturnsNilBeforeStopIsLoggedAtErrorToo(t *testing.T) {
	lb, log := newSvcLogBuf()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(log))
	app.Service(bites.Worker("w", func(context.Context) error { return nil }))
	bitetest.Start(t, app)
	svcEventually(t, "the log line", func() bool { return strings.Contains(lb.String(), "it returned nil") })
	if !strings.Contains(lb.String(), "level=ERROR") {
		t.Fatalf("not at Error:\n%s", lb.String())
	}
}

func TestAPanicInAWorkerIsRecoveredLoggedNamingItAndNotRestarted(t *testing.T) {
	lb, log := newSvcLogBuf()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(log))
	app.Service(bites.Worker("w", func(context.Context) error { panic("kaput") }))
	bitetest.Start(t, app)
	svcEventually(t, "the log lines", func() bool { return strings.Contains(lb.String(), "not restarted") })
	out := lb.String()
	if !strings.Contains(out, "kaput") || !strings.Contains(out, "worker=w") {
		t.Fatalf("the panic is not logged with the worker's name:\n%s", out)
	}
	svcWithin(t, "Shutdown", func() { app.Shutdown(shutdownCtx(t)) })
}

// everyAtStartApp is everyApp with EveryOptions{AtStart: true}.
func everyAtStartApp(t *testing.T, interval time.Duration, fn func(context.Context) error, opts ...bites.Option) (*bitetest.Clock, *bites.App) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(append([]bites.Option{bites.WithClock(clk)}, opts...)...)
	app.Service(bites.Every("job", interval, fn, bites.EveryOptions{AtStart: true}))
	bitetest.Start(t, app)
	return clk, app
}

func TestAnEveryWithAtStartCallsFnRightAfterStartAndThenOncePerInterval(t *testing.T) {
	var runs atomic.Int32
	clk, _ := everyAtStartApp(t, time.Minute, func(context.Context) error { runs.Add(1); return nil })
	svcEventually(t, "the call at start", func() bool { return runs.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times before any interval passed; want 1", n)
	}
	for i := int32(2); i <= 4; i++ {
		tick(t, clk, time.Minute)
		svcEventually(t, "the interval call", func() bool { return runs.Load() == i })
	}
	tick(t, clk, 30*time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 4 {
		t.Fatalf("fn ran %d times after half an interval more; want 4", n)
	}
}

func TestAnEveryWithAtStartSeesTheAppsClockAndLoggerAndLogsItsErrorLikeAnIntervalCall(t *testing.T) {
	lb, log := newSvcLogBuf()
	var runs atomic.Int32
	var seen atomic.Value
	_, _ = everyAtStartApp(t, time.Hour, func(ctx context.Context) error {
		seen.Store(bites.Now(ctx))
		runs.Add(1)
		return errors.New("cache cold")
	}, bites.WithLogger(log))
	svcEventually(t, "the warning", func() bool { return strings.Contains(lb.String(), "level=WARN") })
	out := lb.String()
	if !strings.Contains(out, `job=job`) || !strings.Contains(out, `err="cache cold"`) {
		t.Fatalf("the Warn of the call at start does not name the job and the error:\n%s", out)
	}
	if seen.Load() == nil {
		t.Fatal("bites.Now(ctx) was not read in the call at start")
	}
}

func TestAPanicInTheCallAtStartOfAnEveryIsRecoveredAndTheLoopContinues(t *testing.T) {
	var runs atomic.Int32
	lb, log := newSvcLogBuf()
	clk, _ := everyAtStartApp(t, time.Minute, func(context.Context) error {
		if runs.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}, bites.WithLogger(log))
	svcEventually(t, "the Error", func() bool { return strings.Contains(lb.String(), "level=ERROR") })
	tick(t, clk, time.Minute)
	svcEventually(t, "the interval call", func() bool { return runs.Load() == 2 })
}

func TestStopDuringTheCallAtStartCancelsItsContextAndWaitsForIt(t *testing.T) {
	entered, returned := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	_, app := everyAtStartApp(t, time.Minute, func(ctx context.Context) error {
		runs.Add(1)
		close(entered)
		<-ctx.Done()
		time.Sleep(30 * time.Millisecond)
		close(returned)
		return ctx.Err()
	})
	<-entered
	svcWithin(t, "Shutdown", func() { app.Shutdown(shutdownCtx(t)) })
	select {
	case <-returned:
	default:
		t.Fatal("Shutdown returned before the call at start did")
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times; want 1, none after Stop", n)
	}
}

func TestACallAtStartThatOverrunsTheIntervalDropsTheMissedTicksAndLogsHowManyWereSkipped(t *testing.T) {
	var runs atomic.Int32
	entered, release := make(chan struct{}, 4), make(chan struct{})
	lb, log := newSvcLogBuf()
	clk, _ := everyAtStartApp(t, time.Minute, func(context.Context) error {
		if runs.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		return nil
	}, bites.WithLogger(log))
	<-entered
	clk.Advance(3*time.Minute + 30*time.Second)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times while the call at start was in progress; want 1, never overlapped", n)
	}
	close(release)
	svcEventually(t, "the warning", func() bool { return strings.Contains(lb.String(), "skipped=3") })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times right after the overrun; want 1, no burst", n)
	}
	tick(t, clk, 30*time.Second)
	svcEventually(t, "the next call", func() bool { return runs.Load() == 2 })
}
