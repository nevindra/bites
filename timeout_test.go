package bites_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// doAsync sends r and returns the channel its response arrives on.
func doAsync(srv *bitetest.Server, r bitetest.Request) <-chan bitetest.Response {
	ch := make(chan bitetest.Response, 1)
	go func() { ch <- srv.Do(r) }()
	return ch
}

func wait(t *testing.T, ch <-chan bitetest.Response) bitetest.Response {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no response")
		return bitetest.Response{}
	}
}

func TestAHandlerThatReturnsBeforeTheDeadlineIsUntouchedEvenWhenTheClockMovesLater(t *testing.T) {
	clk := bitetest.NewClock()
	srv := startWith(t, func(app *bites.App) {
		app.Get("/users/{id}", getUser, bites.Use(bites.Timeout(time.Second)))
	}, bites.WithClock(clk))
	for range 3 {
		if r := srv.Do(req("GET", "/users/7")); r.Status != 200 {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
	clk.Advance(time.Hour)
	if r := srv.Do(req("GET", "/users/7")); r.Status != 200 {
		t.Fatalf("after the clock moved: %d", r.Status)
	}
}

func TestAHandlerThatIgnoresItsContextIsWaitedForAndThenAnsweredWithA503NamingTheRequest(t *testing.T) {
	clk := bitetest.NewClock()
	entered, release := make(chan struct{}), make(chan struct{})
	var cause atomic.Value
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/slow", func(ctx context.Context, _ Empty) (User, error) {
			close(entered)
			<-release
			cause.Store(context.Cause(ctx))
			return User{ID: 1, Name: "a handler that finished too late"}, nil
		}, bites.Use(bites.Timeout(time.Second)))
	}, bites.WithClock(clk))
	ch := doAsync(srv, bitetestWith("/slow", "X-Request-ID", "slow-1"))
	<-entered
	clk.Advance(2500 * time.Millisecond) // 2.5 times the deadline
	select {
	case r := <-ch:
		t.Fatalf("answered while the handler still ran: %d", r.Status)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	r := wait(t, ch)
	wantProblem(t, r, 503, `{"title":"Service Unavailable","status":503,"detail":"the request did not finish in time","request_id":"slow-1"}`)
	if strings.Contains(r.Text(), "too late") {
		t.Fatalf("the handler's own answer reached the client: %s", r.Text())
	}
	if err, _ := cause.Load().(error); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the handler saw the cause %v, want context.DeadlineExceeded", err)
	}
	if l := logs.String(); !strings.Contains(l, "returned long after its Timeout") || !strings.Contains(l, "timeout=1s") || !strings.Contains(l, "ran=2.5s") {
		t.Fatalf("a handler that ran 2.5 deadlines was not logged with how long: %s", l)
	}
}

func TestAHandlerThatHonoursItsContextReturnsAtOnceAndWhateverItReturnsIsA503(t *testing.T) {
	clk := bitetest.NewClock()
	entered := make(chan struct{})
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			close(entered)
			<-ctx.Done()
			return Empty{}, ctx.Err() // a plain error, which would be a 500
		}, bites.Use(bites.Timeout(time.Second)))
	}, bites.WithClock(clk))
	ch := doAsync(srv, req("GET", "/slow"))
	<-entered
	clk.Advance(time.Second)
	wantProblem(t, wait(t, ch), 503, `{"title":"Service Unavailable","status":503,"detail":"the request did not finish in time"}`)
	if strings.Contains(logs.String(), "returned long after") {
		t.Fatalf("a handler that returned at its deadline was logged as late: %s", logs)
	}
}

func TestATimeoutOnAGroupCoversItsRoutesAndNotTheOnesOutsideIt(t *testing.T) {
	clk := bitetest.NewClock()
	entered := make(chan struct{}, 2)
	slow := func(ctx context.Context, _ Empty) (Empty, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return Empty{}, nil
	}
	srv := startWith(t, func(app *bites.App) {
		app.Group("/g", bites.Use(bites.Timeout(time.Second))).Get("/slow", slow)
		app.Get("/free", func(context.Context, Empty) (Empty, error) {
			entered <- struct{}{}
			time.Sleep(10 * time.Millisecond)
			return Empty{}, nil
		})
	}, bites.WithClock(clk))
	chG := doAsync(srv, req("GET", "/g/slow"))
	chF := doAsync(srv, req("GET", "/free"))
	<-entered
	<-entered
	clk.Advance(time.Minute)
	if r := wait(t, chG); r.Status != 503 {
		t.Fatalf("/g/slow: %d", r.Status)
	}
	if r := wait(t, chF); r.Status != 200 {
		t.Fatalf("/free: %d", r.Status)
	}
}

func TestAStreamReturnedBeforeTheDeadlineKeepsRunningAfterIt(t *testing.T) {
	clk := bitetest.NewClock()
	running, release := make(chan struct{}), make(chan struct{})
	srv := startWith(t, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				close(running)
				<-release
				w.Write([]byte("late data"))
				return nil
			}}, nil
		}, bites.Use(bites.Timeout(time.Second)))
	}, bites.WithClock(clk))
	ch := doAsync(srv, req("GET", "/s"))
	<-running
	clk.Advance(time.Hour) // far past the deadline: the chain has returned and the timer is gone
	close(release)
	r := wait(t, ch)
	if r.Status != 200 || r.Text() != "late data" {
		t.Fatalf("%d %q", r.Status, r.Text())
	}
}

func TestAStreamReturnedAfterTheDeadlineIsReplacedBy503AndItsFunctionNeverRuns(t *testing.T) {
	clk := bitetest.NewClock()
	entered, release := make(chan struct{}), make(chan struct{})
	var ran atomic.Bool
	srv := startWith(t, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			close(entered)
			<-release
			return bites.Stream{Func: func(context.Context, *bites.StreamWriter) error { ran.Store(true); return nil }}, nil
		}, bites.Use(bites.Timeout(time.Second)))
	}, bites.WithClock(clk))
	ch := doAsync(srv, req("GET", "/s"))
	<-entered
	clk.Advance(time.Second)
	close(release)
	if r := wait(t, ch); r.Status != 503 || ran.Load() {
		t.Fatalf("%d, stream function ran: %v", r.Status, ran.Load())
	}
}

func TestATimeoutRunsOnTheAppsClockSoTheRealOneNeverMatters(t *testing.T) {
	clk := bitetest.NewClock()
	entered := make(chan struct{})
	srv := startWith(t, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			close(entered)
			<-ctx.Done()
			return Empty{}, nil
		}, bites.Use(bites.Timeout(time.Hour)))
	}, bites.WithClock(clk))
	ch := doAsync(srv, req("GET", "/slow"))
	<-entered
	select {
	case <-ch:
		t.Fatal("answered before the fake clock moved")
	case <-time.After(30 * time.Millisecond):
	}
	clk.Advance(time.Hour)
	if r := wait(t, ch); r.Status != 503 {
		t.Fatal(r.Status)
	}
}
