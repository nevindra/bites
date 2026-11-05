package bites_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// recorder is an Observer that writes what it is told into a shared list and hands each Outcome
// to the test.
type recorder struct {
	name string
	mu   *sync.Mutex
	log  *[]string
	outs chan bites.Outcome
	reqs chan bites.Request

	panicStart, panicEnd bool
}

type obsKey string

func newRecorder(name string, mu *sync.Mutex, log *[]string) *recorder {
	return &recorder{name: name, mu: mu, log: log, outs: make(chan bites.Outcome, 16), reqs: make(chan bites.Request, 16)}
}

func (r *recorder) note(s string) {
	r.mu.Lock()
	*r.log = append(*r.log, s)
	r.mu.Unlock()
}

func (r *recorder) Start(ctx context.Context, req bites.Request) context.Context {
	r.note("start " + r.name)
	if r.panicStart {
		panic("start " + r.name)
	}
	return context.WithValue(ctx, obsKey(r.name), r.name)
}

func (r *recorder) End(ctx context.Context, req bites.Request, out bites.Outcome) {
	r.note(fmt.Sprintf("end %s ctx=%v", r.name, ctx.Value(obsKey(r.name))))
	r.reqs <- req
	r.outs <- out
	if r.panicEnd {
		panic("end " + r.name)
	}
}

func (r *recorder) outcome(t *testing.T) bites.Outcome {
	t.Helper()
	select {
	case o := <-r.outs:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("End was not called in 5 s")
		return bites.Outcome{}
	}
}

func (r *recorder) none(t *testing.T) {
	t.Helper()
	select {
	case o := <-r.outs:
		t.Fatalf("End was called: %+v", o)
	case <-time.After(20 * time.Millisecond):
	}
}

// observed starts an app with the recorder as its only Observer and a log the test can read.
func observed(t *testing.T, rec bites.Observer, register func(*bites.App), opts ...bites.Option) (*bitetest.Server, *bitetest.Clock, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	clk := bitetest.NewClock()
	all := append([]bites.Option{bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))), bites.WithObserver(rec)}, opts...)
	app := bites.New(all...)
	register(app)
	return bitetest.Start(t, app), clk, lb
}

func TestObserversStartInTheOrderGivenAndEndInReverseEachWithTheContextItsStartReturned(t *testing.T) {
	var mu sync.Mutex
	var log []string
	a, b := newRecorder("a", &mu, &log), newRecorder("b", &mu, &log)
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithObserver(a, b))
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		mu.Lock()
		log = append(log, fmt.Sprintf("middleware sees a=%v b=%v", ctx.Value(obsKey("a")), ctx.Value(obsKey("b"))))
		mu.Unlock()
		return next.Call(ctx), nil
	})
	app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
		mu.Lock()
		log = append(log, fmt.Sprintf("handler sees b=%v", ctx.Value(obsKey("b"))))
		mu.Unlock()
		return getUser(ctx, in)
	})
	srv := bitetest.Start(t, app)
	srv.Do(req("GET", "/users/1"))
	a.outcome(t)
	b.outcome(t)
	mu.Lock()
	got := strings.Join(log, "; ")
	mu.Unlock()
	want := "start a; start b; middleware sees a=a b=b; handler sees b=b; end b ctx=b; end a ctx=a"
	if got != want {
		t.Fatalf("\n got  %s\n want %s", got, want)
	}
}

func TestTheOutcomeHasTheRoutePatternTheStatusTheBytesAndTheDurationOnTheAppsClock(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	var clk *bitetest.Clock
	srv, clk, _ := observed(t, rec, func(app *bites.App) {
		g := app.Group("/v1")
		g.Post("/users/{id}", func(ctx context.Context, in struct {
			Path struct{ ID int64 }
			Body struct{ Name string }
		}) (User, error) {
			clk.Advance(250 * time.Millisecond)
			return User{ID: in.Path.ID, Name: in.Body.Name}, nil
		})
	})
	body := `{"name":"ada"}`
	r := srv.Do(jsonReq("POST", "/v1/users/7", body))
	o := rec.outcome(t)
	want := bites.Outcome{Route: "/v1/users/{id}", Status: 200, Read: int64(len(body)), Written: int64(len(r.Body)), Duration: 250 * time.Millisecond}
	if !reflect.DeepEqual(o, want) {
		t.Fatalf("\n got  %+v\n want %+v", o, want)
	}
}

func TestA404AndA405AreObservedWithAnEmptyRoute(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	srv.Do(req("GET", "/nope"))
	if o := rec.outcome(t); o.Route != "" || o.Status != 404 {
		t.Fatalf("%+v", o)
	}
	srv.Do(req("DELETE", "/users/1"))
	if o := rec.outcome(t); o.Route != "" || o.Status != 405 {
		t.Fatalf("%+v", o)
	}
}

func TestAStreamIsObservedWhenItEndsWithTheBytesItSent(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				w.Write([]byte("hello "))
				w.Flush()
				w.Write([]byte("world"))
				return nil
			}}, nil
		})
	})
	r := srv.Do(req("GET", "/s"))
	o := rec.outcome(t)
	if r.Text() != "hello world" || o.Status != 200 || !o.Streamed || o.Written != 11 || o.Route != "/s" {
		t.Fatalf("body %q, outcome %+v", r.Text(), o)
	}
}

func TestAPanicInTheHandlerIsObservedAsPanickedWithThe500(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/boom", func(context.Context, Empty) (Empty, error) { panic("boom") })
	})
	r := srv.Do(req("GET", "/boom"))
	o := rec.outcome(t)
	if r.Status != 500 || o.Status != 500 || !o.Panicked || o.Written != int64(len(r.Body)) || o.Route != "/boom" {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestAPanicInAnObserversStartIsTheRequestsPanicAndTheObserversBeforeItStillEnd(t *testing.T) {
	var mu sync.Mutex
	var log []string
	a, b, c := newRecorder("a", &mu, &log), newRecorder("b", &mu, &log), newRecorder("c", &mu, &log)
	b.panicStart = true
	lb := &logBuf{}
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))), bites.WithObserver(a, b, c))
	app.Get("/users/{id}", getUser)
	srv := bitetest.Start(t, app)
	if r := srv.Do(req("GET", "/users/1")); r.Status != 500 {
		t.Fatalf("status %d", r.Status)
	}
	if o := a.outcome(t); !o.Panicked || o.Status != 500 {
		t.Fatalf("%+v", o)
	}
	b.none(t)
	c.none(t)
	if !strings.Contains(lb.String(), "panic serving request") {
		t.Fatalf("the panic was not logged: %s", lb)
	}
}

func TestAPanicInAnObserversEndIsLoggedTheOthersStillEndAndTheConnectionCloses(t *testing.T) {
	var mu sync.Mutex
	var log []string
	a, b := newRecorder("a", &mu, &log), newRecorder("b", &mu, &log)
	b.panicEnd = true
	lb := &logBuf{}
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))), bites.WithObserver(a, b))
	app.Get("/users/{id}", getUser)
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, "GET /users/1 HTTP/1.1\r\nHost: x\r\n\r\n")
	if r := srv.Read(c, false); r.Status != 200 {
		t.Fatalf("status %d", r.Status)
	}
	b.outcome(t)
	a.outcome(t)
	fmt.Fprint(c, "GET /users/2 HTTP/1.1\r\nHost: x\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var one [1]byte
	if n, err := c.Read(one[:]); n != 0 || err == nil {
		t.Fatalf("the connection carried on after a panic in End: %d %v", n, err)
	}
	if !strings.Contains(lb.String(), "panic in an Observer's End") {
		t.Fatalf("the panic was not logged: %s", lb)
	}
}

func TestAClientThatLeavesMidHandlerIsObservedAsAbandonedWithNothingWritten(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	started := make(chan struct{})
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			close(started)
			<-ctx.Done()
			return Empty{}, ctx.Err()
		})
	})
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	<-started
	c.Close()
	if o := rec.outcome(t); !o.Abandoned || o.Written != 0 || o.Status != 0 || o.Route != "/slow" {
		t.Fatalf("%+v", o)
	}
}

func TestTimeoutIsObservedWithThe503ItAnswered(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	var clk *bitetest.Clock
	srv, clk, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			clk.Advance(2 * time.Second)
			<-ctx.Done()
			return Empty{}, nil
		}, bites.Use(bites.Timeout(time.Second)))
	})
	r := srv.Do(req("GET", "/slow"))
	if o := rec.outcome(t); r.Status != 503 || o.Status != 503 || o.Written != int64(len(r.Body)) {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestAWebSocketUpgradeIsObservedAtThe101(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	e := newRoom()
	srv, _, _ := observed(t, rec, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	o := rec.outcome(t)
	if o.Status != 101 || !o.Upgraded || o.Route != "/rooms/{id}" {
		t.Fatalf("%+v", o)
	}
	w.text(`{"text":"hi"}`)
	w.expectText(`{"text":"hi"}`)
	rec.none(t) // a socket's messages are not requests
}

func TestAHeadTheEngineRefusesBeforeParsingIsNotObserved(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, "NOT HTTP AT ALL\r\n\r\n")
	if r := srv.Read(c, false); r.Status != 400 {
		t.Fatalf("status %d", r.Status)
	}
	rec.none(t)
}

func TestTheRequestAnObserverKeptAnswersZeroValuesOnceEndHasReturned(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", getUser)
	})
	srv.Do(bitetestWith("/users/1", "X-Request-ID", "req-1"))
	rec.outcome(t)
	kept := <-rec.reqs
	waitFor(t, "the run to be released", func() bool { return kept.RequestID() == "" })
	if kept.Method() != "GET" || kept.Path() != "/users/1" {
		t.Fatalf("the request's own copies are gone: %q %q", kept.Method(), kept.Path())
	}
}

func TestAnObserverReadsTheRequestIDAMiddlewareSetAtEnd(t *testing.T) {
	var got = make(chan string, 1)
	obs := endFunc(func(_ context.Context, req bites.Request, _ bites.Outcome) { got <- req.RequestID() })
	srv, _, _ := observed(t, obs, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", getUser)
	})
	srv.Do(bitetestWith("/users/1", "X-Request-ID", "req-7"))
	select {
	case id := <-got:
		if id != "req-7" {
			t.Fatalf("End read %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("End was not called")
	}
}

// endFunc is an Observer whose Start adds nothing.
type endFunc func(context.Context, bites.Request, bites.Outcome)

func (endFunc) Start(ctx context.Context, _ bites.Request) context.Context { return ctx }
func (f endFunc) End(ctx context.Context, req bites.Request, out bites.Outcome) {
	f(ctx, req, out)
}

// ---- the error and the panic a tracker needs (ADR 0051) ----

type ledgerError struct{ Account string }

func (e *ledgerError) Error() string { return "ledger " + e.Account + " is closed" }

type notFoundError struct{}

func (notFoundError) Error() string             { return "no such invoice" }
func (notFoundError) HTTPStatus() (int, string) { return 404, "no such invoice" }

func TestAReturnedErrorReachesEndAsErrWithErrorsAsIntact(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (Empty, error) {
			return Empty{}, fmt.Errorf("closing: %w", &ledgerError{Account: "42"})
		})
	})
	r := srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	var le *ledgerError
	if r.Status != 500 || o.Status != 500 || !errors.As(o.Err, &le) || le.Account != "42" || o.Panicked || o.Panic != nil || o.Stack != nil {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestAHasStatus4xxErrorReachesEndAsErr(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (Empty, error) { return Empty{}, notFoundError{} })
	})
	r := srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	if r.Status != 404 || o.Status != 404 || !errors.As(o.Err, new(notFoundError)) {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestAMiddlewaresErrorReachesEndAsErr(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			return bites.Response{}, &ledgerError{Account: "m"}
		})
		app.Get("/x", func(context.Context, Empty) (Empty, error) { return Empty{}, nil })
	})
	srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	if !errors.As(o.Err, new(*ledgerError)) {
		t.Fatalf("outcome %+v", o)
	}
}

func TestADecodingErrorThe400IsNotAnErr(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/n/{id}", func(context.Context, struct{ Path struct{ ID int64 } }) (Empty, error) { return Empty{}, nil })
	})
	r := srv.Do(req("GET", "/n/abc"))
	o := rec.outcome(t)
	if r.Status != 400 || o.Err != nil {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestAMiddlewareThatReplacesAnErrorsAnswerWithASuccessLeavesErrNil(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			if res.Status() >= 400 {
				res.SetBody("text/plain", "fallback")
				res.SetStatus(200)
			}
			return res, nil
		})
		app.Get("/x", func(context.Context, Empty) (Empty, error) { return Empty{}, &ledgerError{Account: "s"} })
	})
	r := srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	if r.Status != 200 || o.Status != 200 || o.Err != nil {
		t.Fatalf("answer %d, outcome %+v", r.Status, o)
	}
}

func TestASuccessfulRequestLeavesErrPanicAndStackZero(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/ok", func(context.Context, Empty) (Empty, error) { return Empty{}, nil })
	})
	srv.Do(req("GET", "/ok"))
	o := rec.outcome(t)
	if o.Err != nil || o.Panic != nil || o.Stack != nil || o.Panicked {
		t.Fatalf("outcome %+v", o)
	}
}

func panickingHandler(context.Context, Empty) (Empty, error) { panic("boom") }

func TestAPanicInAHandlerGivesPanicAndAStackNamingTheFunction(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) { app.Get("/boom", panickingHandler) })
	srv.Do(req("GET", "/boom"))
	o := rec.outcome(t)
	if !o.Panicked || o.Panic != "boom" || o.Err != nil || !strings.Contains(string(o.Stack), "panickingHandler") {
		t.Fatalf("outcome %+v\n%s", o, o.Stack)
	}
}

func panickingMiddleware(context.Context, bites.Request, bites.Next) (bites.Response, error) {
	panic(&ledgerError{Account: "mw"})
}

func TestAPanicInAMiddlewareGivesPanicAndAStackNamingTheFunction(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Use(panickingMiddleware)
		app.Get("/x", func(context.Context, Empty) (Empty, error) { return Empty{}, nil })
	})
	srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	if le, ok := o.Panic.(*ledgerError); !o.Panicked || !ok || le.Account != "mw" || !strings.Contains(string(o.Stack), "panickingMiddleware") {
		t.Fatalf("outcome %+v\n%s", o, o.Stack)
	}
}

func panickingStream(_ context.Context, w *bites.StreamWriter) error {
	w.Write([]byte("partial"))
	w.Flush()
	panic("stream boom")
}

func TestAPanicInAStreamFunctionGivesPanicAndAStackNamingTheFunction(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) { return bites.Stream{Func: panickingStream}, nil })
	})
	cutGet(t, srv, "/s")
	o := rec.outcome(t)
	if !o.Panicked || !o.Streamed || o.Panic != "stream boom" || !strings.Contains(string(o.Stack), "panickingStream") {
		t.Fatalf("outcome %+v\n%s", o, o.Stack)
	}
}

func TestAPanicNilArrivesAsARuntimePanicNilError(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/x", func(context.Context, Empty) (Empty, error) { panic(nil) })
	})
	srv.Do(req("GET", "/x"))
	o := rec.outcome(t)
	if _, ok := o.Panic.(*runtime.PanicNilError); !ok || !o.Panicked {
		t.Fatalf("outcome %+v", o)
	}
}

func TestAStreamFunctionsErrorAfterTheFirstByteReachesEndAsErr(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	srv, _, _ := observed(t, rec, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				w.Write([]byte("partial"))
				w.Flush()
				return &ledgerError{Account: "cut"}
			}}, nil
		})
	})
	cutGet(t, srv, "/s")
	o := rec.outcome(t)
	if !o.Streamed || !errors.As(o.Err, new(*ledgerError)) || o.Panicked {
		t.Fatalf("outcome %+v", o)
	}
}

// cutGet sends a GET whose stream is cut, and so has no complete response to read.
func cutGet(t *testing.T, srv *bitetest.Server, target string) {
	t.Helper()
	c := srv.Dial("203.0.113.9:4000")
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte("GET " + target + " HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
}
