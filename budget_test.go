package bites

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

// The allocation budget of ADR 0003, one test a row, through the engine path of ADR 0027 (the
// reader, the parser, the router, the decoders, the writer) and, for the row that needs the
// kernel, over loopback TCP. A count that rises fails the test (ADR 0033).

type budgetIn struct {
	Path  struct{ ID int64 }
	Query struct {
		Expand Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

type budgetOut struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Page  int    `json:"page"`
}

func budgetHandler(_ context.Context, in budgetIn) (budgetOut, error) {
	return budgetOut{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
}

type budgetFormIn struct {
	Path  struct{ ID int64 }
	Query struct {
		Expand Opt[bool]
		Page   int
	}
	Body Form[struct {
		Name  string
		Email string
		Age   int
		Tags  Opt[[]string]
	}]
}

func budgetFormHandler(_ context.Context, in budgetFormIn) (budgetOut, error) {
	f := in.Body.Value
	return budgetOut{ID: in.Path.ID, Name: f.Name, Email: f.Email, Age: f.Age + len(f.Tags.Or(nil)), Page: in.Query.Page}, nil
}

func formPost(target, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func TestAFormPOSTCostsWhatTheJSONPOSTCostsAndOneMorePerListItFills(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetFormHandler) })
	for _, tc := range []struct {
		name, body string
		want       float64
	}{
		{"plain fields", "name=Ada&email=ada@example.com&age=36", 2},
		{"percent escapes and plus signs", "name=Ada+Lovelace&email=ada%40example.com&age=36", 2},
		{"a list", "name=Ada&email=ada@example.com&age=36&tags=a&tags=b", 3},
	} {
		got := keepAlive(t, c, formPost("/users/42?expand=true&page=3", tc.body), "HTTP/1.1 200 OK")
		t.Logf("form POST, %s: %v allocations", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type budgetSubmittedIn struct {
	Path struct{ ID int64 }
	Body Submitted[struct {
		Name  string
		Email string
		Age   int
	}]
}

var budgetPage = []byte("<p>ok</p>")

func budgetSubmittedHandler(_ context.Context, in budgetSubmittedIn) (Page, error) {
	if _, ok := in.Body.Valid(); !ok {
		return Page{Status: 422, Body: budgetPage}, nil
	}
	return Page{Body: budgetPage}, nil
}

// A Submitted form keeps a copy of the form as sent, for the page that shows it again (ADR 0078):
// the copy, the submission and its index of fields are three allocations a Form does not make.
func TestASubmittedFormCostsAFormsAllocationsAndThreeForTheCopyItKeeps(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetSubmittedHandler) })
	got := keepAlive(t, c, formPost("/users/42", "name=Ada&email=ada%40example.com&age=36"), "HTTP/1.1 200 OK")
	t.Logf("Submitted POST, valid: %v allocations", got)
	if got != 5 {
		t.Errorf("a valid Submitted form: allocs/request = %v, want 5", got)
	}
	bad := keepAlive(t, c, formPost("/users/42", "name=Ada&email=ada%40example.com&age=x"), "HTTP/1.1 422")
	t.Logf("Submitted POST, one mistake: %v allocations", bad)
}

type line struct {
	Sku string
	Qty int
}

type budgetListIn struct {
	Path  struct{ ID int64 }
	Query struct{ Tags Opt[[]string] }
	Body  struct {
		Name  string
		Lines Opt[[]line]
		Notes Opt[[]string]
	}
}

func budgetListHandler(_ context.Context, in budgetListIn) (budgetOut, error) {
	return budgetOut{ID: in.Path.ID, Name: in.Body.Name, Age: len(in.Body.Lines.Or(nil)) + len(in.Body.Notes.Or(nil)) + len(in.Query.Tags.Or(nil))}, nil
}

func post(target, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// keepAlive drives one request over and over on one connection and returns the mallocs per
// request. The response must be the same length every time so the reader can take it exactly.
func keepAlive(t *testing.T, c net.Conn, request []byte, wantStatus string) float64 {
	t.Helper()
	buf := make([]byte, 16<<10)
	want := 0
	do := func() {
		c.Write(request)
		if want == 0 {
			want, _ = c.Read(buf)
			return
		}
		io.ReadFull(c, buf[:want])
	}
	do()
	if got := string(buf[:len(wantStatus)]); got != wantStatus {
		t.Fatalf("the first response is %q, want it to start with %q\n%s", got, wantStatus, buf[:want])
	}
	for range 100 {
		do()
	}
	return testing.AllocsPerRun(2000, do)
}

func memRig(t *testing.T, register func(*App)) net.Conn {
	t.Helper()
	app := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()))
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tcpRig(t *testing.T, register func(*App)) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	app := New(WithLogger(quiet()))
	register(app)
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

const typedBody = `{"name":"Ada Lovelace","email":"ada@example.com","age":36}`

func TestATypedJSONPOSTWithPathQueryAndBodyAndAJSONAnswerAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetHandler) })
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, typed JSON POST, Done not called: %v (the slab, the context)", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: the slab and the context)", got)
	}
}

func TestATypedGETWithNoBodyAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Get("/users/{id}", func(_ context.Context, in GetPlain) (budgetOut, error) { return budgetOut{ID: in.Path.ID}, nil })
	})
	got := keepAlive(t, c, []byte("GET /users/42 HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 200 OK")
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
}

type GetPlain struct{ Path struct{ ID int64 } }

func TestTheSameRequestWhoseHandlerCallsDoneAllocatesThriceOverLoopbackTCP(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the row of ADR 0003 is the epoll watcher's; elsewhere a reader goroutine watches the connection and costs its own")
	}
	c := tcpRig(t, func(a *App) {
		a.Post("/users/{id}", func(ctx context.Context, in budgetIn) (budgetOut, error) {
			ctx.Done()
			return budgetHandler(ctx, in)
		})
	})
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request over TCP, Done called: %v (the slab, the context, its done channel)", got)
	if got != 3 {
		t.Fatalf("allocs/request = %v, want exactly 3 (ADR 0003: plus the done channel)", got)
	}
}

func TestEachListARequestFillsCostsOneAllocationAndAnEmptyOrAbsentListCostsNone(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/orders/{id}", budgetListHandler) })
	for _, tc := range []struct {
		name, target, body string
		want               float64
	}{
		{"no list sent", "/orders/1", `{"name":"a"}`, 2},
		{"empty lists", "/orders/1", `{"name":"a","lines":[],"notes":[]}`, 2},
		{"one list in the body", "/orders/1", `{"name":"a","lines":[{"sku":"x","qty":1},{"sku":"y","qty":2}]}`, 3},
		{"two lists in the body", "/orders/1", `{"name":"a","lines":[{"sku":"x","qty":1}],"notes":["n","m","o"]}`, 4},
		{"a list in the query and one in the body", "/orders/1?tags=a&tags=b", `{"name":"a","lines":[{"sku":"x","qty":1}]}`, 4},
	} {
		got := keepAlive(t, c, post(tc.target, tc.body), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type budgetEmail string

func (budgetEmail) Length() (int, int) { return 3, 64 }

// budgetValidIn is budgetIn with a constraint type in every section that has one and a Validate
// that passes (ADR 0021).
type budgetValidIn struct {
	Path  struct{ ID PositiveInt64 }
	Query struct {
		Expand Opt[bool]
		Page   PositiveInt
	}
	Body struct {
		Name  NonEmpty
		Email budgetEmail
		Age   int
	}
}

func (in budgetValidIn) Validate() error {
	if in.Body.Age < 0 {
		return Invalid("body.age", "must not be negative")
	}
	return nil
}

func budgetValidHandler(_ context.Context, in budgetValidIn) (budgetOut, error) {
	return budgetOut{ID: int64(in.Path.ID), Name: string(in.Body.Name), Email: string(in.Body.Email), Age: in.Body.Age, Page: int(in.Query.Page)}, nil
}

func TestATypedJSONPOSTWithConstraintTypesAndAPassingValidateStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetValidHandler) })
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, constraint types and a Validate that returns nil: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: Validate and the constraint types add none)", got)
	}
}

// The same request with the rules carried by the application's own named types: a Range on the
// path id and the page, a Length on the name, a Check on the email.
type (
	ownID    int64
	ownPage  int
	ownName  string
	ownEmail string
)

func (ownID) Range() (min, max int64)   { return 1, 1 << 40 }
func (ownPage) Range() (min, max int64) { return 1, 100 }
func (ownName) Length() (min, max int)  { return 1, 64 }

var errOwnEmail = errors.New("must be an email address")

func (e ownEmail) Check() error {
	if strings.IndexByte(string(e), '@') < 1 {
		return errOwnEmail
	}
	return nil
}

type budgetOwnIn struct {
	Path  struct{ ID ownID }
	Query struct {
		Expand Opt[bool]
		Page   ownPage
	}
	Body struct {
		Name  ownName
		Email ownEmail
		Age   int
	}
}

func budgetOwnHandler(_ context.Context, in budgetOwnIn) (budgetOut, error) {
	return budgetOut{ID: int64(in.Path.ID), Name: string(in.Body.Name), Email: string(in.Body.Email), Age: in.Body.Age, Page: int(in.Query.Page)}, nil
}

func TestATypedJSONPOSTWhoseOwnTypesCarryLengthRangeAndCheckStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetOwnHandler) })
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, own types with Length, Range and Check, fast path: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: the rules add none)", got)
	}
	// An escape keeps the body off the fast path, so the tokenizer runs the Check.
	got = keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":"Ada\u0020Lovelace","email":"ada@example.com","age":36}`), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, the same through the tokenizer: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v through the tokenizer, want exactly 2", got)
	}
	wrong := keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":"Ada","email":5,"age":36}`), "HTTP/1.1 400 Bad Request")
	got = keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":"Ada","email":"nobody","age":36}`), "HTTP/1.1 400 Bad Request")
	t.Logf("allocations per request, a Check fails in the body: %v (a wrong type in that field: %v)", got, wrong)
	if got != wrong {
		t.Fatalf("a failed Check costs %v allocations, a wrong type %v: they must cost the same", got, wrong)
	}
}

func TestARequestWhoseValidateFailsCostsTheErrorsItBuildsAndNothingElse(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetValidHandler) })
	// Validate returns one Invalid: one FieldError. The answer is built into the response buffer.
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":"Ada Lovelace","email":"ada@example.com","age":-1}`), "HTTP/1.1 400 Bad Request")
	t.Logf("allocations per request, Validate fails with one Invalid: %v", got)
	if got != 3 {
		t.Fatalf("allocs/request = %v, want 3 (the slab, the context, the FieldError Validate builds)", got)
	}
	// A constraint type that fails is a decoding error, and costs what any other decoding error
	// does (its location string): a wrong type in the same field is the baseline.
	wrong := keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":5,"email":"ada@example.com","age":36}`), "HTTP/1.1 400 Bad Request")
	got = keepAlive(t, c, post("/users/42?expand=true&page=3", `{"name":"","email":"ada@example.com","age":36}`), "HTTP/1.1 400 Bad Request")
	t.Logf("allocations per request, a constraint type fails in the body: %v (a wrong type in that field: %v)", got, wrong)
	if got != wrong {
		t.Fatalf("a failed constraint costs %v allocations, a wrong type %v: they must cost the same", got, wrong)
	}
}

func passThrough(ctx context.Context, _ Request, next Next) (Response, error) {
	return next.Call(ctx), nil
}

// readsHeader is a middleware that reads a header and a path, as most real ones do, and decorates.
func readsHeader(ctx context.Context, req Request, next Next) (Response, error) {
	_ = req.Header("authorization")
	_ = req.Path()
	res := next.Call(ctx)
	res.AddHeader("X-Served-By", "a")
	return res, nil
}

func TestATypedJSONPOSTBehindMiddlewareStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name string
		reg  func(a *App)
	}{
		{"one route middleware", func(a *App) { a.Post("/users/{id}", budgetHandler, Use(passThrough)) }},
		{"three route middleware", func(a *App) {
			a.Post("/users/{id}", budgetHandler, Use(readsHeader, passThrough, readsHeader))
		}},
		{"one app middleware", func(a *App) { a.Use(readsHeader); a.Post("/users/{id}", budgetHandler) }},
		{"app, group and route middleware", func(a *App) {
			a.Use(readsHeader)
			g := a.Group("/users")
			g.Use(readsHeader)
			g.Post("/{id}", budgetHandler, Use(readsHeader))
		}},
		{"a request ID set and a header read", func(a *App) {
			a.Use(func(ctx context.Context, req Request, next Next) (Response, error) {
				req.SetRequestID("fixed-id")
				_ = req.Header("authorization")
				return next.Call(ctx), nil
			})
			a.Post("/users/{id}", budgetHandler)
		}},
	} {
		target := "/users/42?expand=true&page=3"
		c := memRig(t, tc.reg)
		got := keepAlive(t, c, post(target, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0003: a route with middleware costs the slab and the context)", tc.name, got)
		}
	}
}

func TestATypedJSONPOSTBehindAUseEachMiddlewareCostsWhatTheSameUseCostsAndANilOneCostsNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	built := func(r RouteInfo) (Middleware, error) { return readsHeader, nil }
	none := func(r RouteInfo) (Middleware, error) { return nil, nil }
	for _, tc := range []struct {
		name string
		reg  func(a *App)
	}{
		{"Use, for the comparison", func(a *App) { a.Post("/users/{id}", budgetHandler, Use(readsHeader)) }},
		{"app UseEach", func(a *App) { a.UseEach(built); a.Post("/users/{id}", budgetHandler) }},
		{"group UseEach and a route's own Use", func(a *App) {
			g := a.Group("/users")
			g.UseEach(built)
			g.Post("/{id}", budgetHandler, Use(passThrough))
		}},
		{"UseEach as a route option", func(a *App) { a.Post("/users/{id}", budgetHandler, UseEach(built)) }},
		{"UseEach that returns nil, as a route with no middleware", func(a *App) { a.UseEach(none); a.Post("/users/{id}", budgetHandler) }},
	} {
		c := memRig(t, tc.reg)
		got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0003, ADR 0048)", tc.name, got)
		}
	}
}

func TestA404ThroughAppMiddlewareAllocatesOnlyTheContextAndTheSlab(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Use(passThrough)
		a.Get("/x", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	})
	got := keepAlive(t, c, []byte("GET /nope HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 404 Not Found")
	t.Logf("a 404 behind app middleware: %v allocations", got)
	if got > 2 {
		t.Fatalf("allocs/request = %v, want at most 2", got)
	}
}

// keepAliveBytes drives one request over and over on one connection, as keepAlive does, and
// returns the bytes the process allocated per request.
func keepAliveBytes(t *testing.T, c net.Conn, request []byte) float64 {
	t.Helper()
	buf := make([]byte, 16<<10)
	c.Write(request)
	want, _ := c.Read(buf)
	do := func() {
		c.Write(request)
		io.ReadFull(c, buf[:want])
	}
	for range 200 {
		do()
	}
	const n = 5000
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	for range n {
		do()
	}
	runtime.ReadMemStats(&b)
	return float64(b.TotalAlloc-a.TotalAlloc) / n
}

func TestAMiddlewareThatWritesNothingInTheSlabAddsNoByteToARequest(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	// Every byte a request allocates brings the next collection nearer, and with a live heap of a
	// megabyte the collector's frequency follows the bytes a request allocates; that is what the
	// p99 of a route behind middleware paid for the 80 bytes it reserved for RequestID and a
	// RateLimiter whether they were there or not (docs/research/middleware-p99/).
	target := "/users/42?expand=true&page=3"
	base := keepAliveBytes(t, memRig(t, func(a *App) { a.Post("/users/{id}", budgetHandler) }), post(target, typedBody))
	for _, tc := range []struct {
		name string
		reg  func(a *App)
	}{
		{"one pass-through route middleware", func(a *App) { a.Post("/users/{id}", budgetHandler, Use(passThrough)) }},
		{"three route middleware that read a header", func(a *App) {
			a.Post("/users/{id}", budgetHandler, Use(readsHeader, readsHeader, readsHeader))
		}},
		{"one pass-through app middleware", func(a *App) { a.Use(passThrough); a.Post("/users/{id}", budgetHandler) }},
		{"an Observer and no middleware", func(a *App) { WithObserver(&noopObserver{})(a); a.Post("/users/{id}", budgetHandler) }},
	} {
		got := keepAliveBytes(t, memRig(t, tc.reg), post(target, typedBody))
		t.Logf("%s: %.0f bytes a request, %.0f with no middleware", tc.name, got, base)
		if got > base+8 {
			t.Errorf("%s: %.0f bytes a request, %.0f without middleware: a middleware that writes nothing in the slab must reserve nothing in it (slabRoom)", tc.name, got, base)
		}
	}
}

func TestRequestIDAndARateLimiterKeepTheirRoomOnEveryPathThroughTheChain(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name string
		reg  func(a *App, l *RateLimiter)
	}{
		{"RequestID on the route", func(a *App, _ *RateLimiter) { a.Post("/users/{id}", budgetHandler, Use(RequestID())) }},
		{"RequestID on a group", func(a *App, _ *RateLimiter) {
			g := a.Group("/users")
			g.Use(RequestID())
			g.Post("/{id}", budgetHandler)
		}},
		{"RequestID on the route of an app with an Observer", func(a *App, _ *RateLimiter) {
			WithObserver(&noopObserver{})(a)
			a.Post("/users/{id}", budgetHandler, Use(RequestID()))
		}},
		{"RequestID on the route behind a pass-through app middleware", func(a *App, _ *RateLimiter) {
			a.Use(passThrough)
			a.Post("/users/{id}", budgetHandler, Use(RequestID()))
		}},
		{"a RateLimiter on the route", func(a *App, l *RateLimiter) { a.Post("/users/{id}", budgetHandler, Use(l.Middleware())) }},
		{"RequestID on the app and a RateLimiter on the route", func(a *App, l *RateLimiter) {
			a.Use(RequestID())
			a.Post("/users/{id}", budgetHandler, Use(l.Middleware()))
		}},
		{"two RateLimiters and RequestID on the route", func(a *App, l *RateLimiter) {
			l2 := NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second, Name: "second"})
			a.Post("/users/{id}", budgetHandler, Use(RequestID(), l.Middleware(), l2.Middleware()))
		}},
	} {
		l := NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second})
		c := memRig(t, func(a *App) { tc.reg(a, l) })
		got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2: the generated id and each RateLimit header are written in room the slab was sized with (slabRoom)", tc.name, got)
		}
	}
}

type budgetAuthIn struct {
	Header struct{ Authorization string }
}

func TestAHeaderInTheInputAndInAMiddlewareShareOneSlabOfTwiceTheHead(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	// The Input copies the 380 byte value and the head copy holds it too: a slab of the head's
	// size alone would overflow into a third allocation (ADR 0022, docs/research/decisions-middleware R10).
	c := memRig(t, func(a *App) {
		a.Get("/a", func(context.Context, budgetAuthIn) (budgetOut, error) { return budgetOut{}, nil }, Use(readsHeader))
	})
	auth := make([]byte, 380)
	for i := range auth {
		auth[i] = 'a' + byte(i%26)
	}
	got := keepAlive(t, c, []byte("GET /a HTTP/1.1\r\nHost: x\r\nAuthorization: "+string(auth)+"\r\n\r\n"), "HTTP/1.1 200 OK")
	t.Logf("a 380 byte Authorization read by the Input and by a middleware: %v allocations", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
}

// BenchmarkTypedPOSTBehindMiddleware is the ns/op beside the allocation rows above: the typed POST
// of ADR 0003 over memory with 0, 1 and 3 route middleware (docs/research/middleware-core).
func BenchmarkTypedPOSTBehindMiddleware(b *testing.B) {
	for _, v := range []struct {
		n    int
		name string
		mw   Middleware
	}{{0, "0_middleware", readsHeader}, {1, "1_middleware", readsHeader}, {3, "3_middleware", readsHeader}, {1, "1_bare", passThrough}, {3, "3_bare", passThrough}} {
		n := v.n
		b.Run(v.name, func(b *testing.B) {
			ms := make([]Middleware, n)
			for i := range ms {
				ms[i] = v.mw
			}
			app := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()))
			app.Post("/users/{id}", budgetHandler, Use(ms...))
			ln := memconn.Listen(app.Clock())
			go app.Serve(ln)
			defer app.Close()
			c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
			if err != nil {
				b.Fatal(err)
			}
			request := post("/users/42?expand=true&page=3", typedBody)
			buf := make([]byte, 2048)
			c.Write(request)
			want, _ := c.Read(buf)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				c.Write(request)
				io.ReadFull(c, buf[:want])
			}
		})
	}
}

type budgetWorstIn struct {
	Path   struct{ Name string }
	Query  struct{ Q, R string }
	Header struct{ Authorization, XTrace string }
	Cookie struct{ Session string }
}

func TestTheLongestDecodedPathQueryAndHeadersBehindMiddlewareStillFitTheSlabInTwoAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	// Everything percent-encoded, so nothing can be answered with a substring of the head copy:
	// the slab is the head plus the target, and this is the case that fills the target part.
	c := memRig(t, func(a *App) {
		a.Get("/files/{name}", func(context.Context, budgetWorstIn) (budgetOut, error) { return budgetOut{}, nil }, Use(readsHeader))
	})
	enc := strings.Repeat("%41", 40)
	req := "GET /files/" + enc + "?q=" + enc + "&r=" + enc + " HTTP/1.1\r\nHost: x\r\nAuthorization: " + strings.Repeat("a", 300) +
		"\r\nX-Trace: " + strings.Repeat("t", 90) + "\r\nCookie: session=" + strings.Repeat("c", 50) + "\r\n\r\n"
	got := keepAlive(t, c, []byte(req), "HTTP/1.1 200 OK")
	t.Logf("fully encoded target with long headers: %v allocations", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
	// Plus signs alone are decoded too, with no percent escape in the target.
	plus := strings.Repeat("a+b+", 20)
	req = "GET /files/x?q=" + plus + "&r=" + plus + " HTTP/1.1\r\nHost: x\r\nAuthorization: " + strings.Repeat("a", 300) +
		"\r\nX-Trace: t\r\nCookie: session=c\r\n\r\n"
	c2 := memRig(t, func(a *App) {
		a.Get("/files/{name}", func(context.Context, budgetWorstIn) (budgetOut, error) { return budgetOut{}, nil }, Use(readsHeader))
	})
	if got := keepAlive(t, c2, []byte(req), "HTTP/1.1 200 OK"); got != 2 {
		t.Fatalf("plus signs: allocs/request = %v, want exactly 2", got)
	}
}

// The Provider rows of ADR 0003: a route whose Input asks for provided types costs what the same
// route without them costs, whatever the depth of the graph, because each Provider's value lives
// on the stack of its step (provide.go).

type (
	budgetSession struct{ Token string }
	budgetCaller  struct {
		ID   int64
		Name string
	}
	// budgetProvidedIn is budgetIn with a provided field: same sections, same request.
	budgetProvidedIn struct {
		Caller budgetCaller
		S      budgetSession
		Path   struct{ ID int64 }
		Query  struct {
			Expand Opt[bool]
			Page   int
		}
		Body struct {
			Name  string
			Email string
			Age   int
		}
	}
)

func (in budgetProvidedIn) plain() budgetIn {
	var b budgetIn
	b.Path.ID, b.Query.Page, b.Query.Expand = in.Path.ID, in.Query.Page, in.Query.Expand
	b.Body.Name, b.Body.Email, b.Body.Age = in.Body.Name, in.Body.Email, in.Body.Age
	return b
}

func budgetSessionOf(_ context.Context, req Request, _ struct{}) (budgetSession, error) {
	return budgetSession{Token: req.Header("authorization")}, nil
}

func budgetCallerOf(_ context.Context, req Request, deps struct{ S budgetSession }) (budgetCaller, error) {
	req.AddLogAttrs(slog.Int64("caller", 7))
	return budgetCaller{ID: 7, Name: deps.S.Token}, nil
}

func postAuth(target, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer abc\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func TestATypedJSONPOSTWhoseInputAsksForProvidedTypesStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	callerHandler := func(ctx context.Context, in budgetProvidedIn) (budgetOut, error) {
		out, err := budgetHandler(ctx, in.plain())
		out.Name, out.Email = in.Caller.Name, in.S.Token // no concatenation: it would allocate
		return out, err
	}
	for _, tc := range []struct {
		name string
		reg  func(a *App)
	}{
		{"two Providers with no dependencies, one of them a route option", func(a *App) {
			a.Provide(budgetSessionOf)
			a.Post("/users/{id}", callerHandler, Provide(func(_ context.Context, _ Request, _ struct{}) (budgetCaller, error) {
				return budgetCaller{ID: 7, Name: "x"}, nil
			}))
		}},
		{"a Provider that needs another, and adds a log attribute", func(a *App) {
			a.Provide(budgetSessionOf)
			a.Provide(budgetCallerOf)
			a.Post("/users/{id}", callerHandler)
		}},
		{"a Provider that reads its own Header section", func(a *App) {
			a.Provide(func(_ context.Context, _ Request, deps struct {
				Header struct{ Authorization string }
			}) (budgetSession, error) {
				return budgetSession{Token: deps.Header.Authorization}, nil
			})
			a.Provide(budgetCallerOf)
			a.Post("/users/{id}", callerHandler)
		}},
		{"the same behind app and route middleware, with the request's attributes read", func(a *App) {
			a.Use(func(ctx context.Context, req Request, next Next) (Response, error) {
				res := next.Call(ctx)
				var buf [4]slog.Attr
				_ = req.AppendLogAttrs(buf[:0])
				return res, nil
			})
			a.Provide(budgetSessionOf)
			a.Provide(budgetCallerOf)
			a.Post("/users/{id}", callerHandler, Use(readsHeader))
		}},
	} {
		c := memRig(t, tc.reg)
		got := keepAlive(t, c, postAuth("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0003: Providers cost no allocation of their own)", tc.name, got)
		}
	}
}

type feedIn struct{}

var feedEvents atomic.Int64

// feedFunc captures nothing, so the handler's own closure is not counted in the stream's numbers.
func feedFunc(_ context.Context, e EventWriter) error {
	for i := int64(0); i < feedEvents.Load(); i++ {
		if err := e.Send(Event{ID: "42", Name: "tick", Data: "a line of 30 bytes of event data"}); err != nil {
			return err
		}
	}
	return nil
}

// streamAllocs drives one SSE request over and over on one connection and returns the mallocs
// per request, reading each response up to its terminating chunk.
func streamAllocs(c net.Conn) float64 {
	req := []byte("GET /feed HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := make([]byte, 16<<10)
	do := func() {
		c.Write(req)
		got := 0
		for {
			m, err := c.Read(buf[got:])
			got += m
			if err != nil || (got >= 5 && string(buf[got-5:got]) == "0\r\n\r\n") {
				return
			}
		}
	}
	for range 100 {
		do()
	}
	return testing.AllocsPerRun(1000, do)
}

func TestAStreamAllocatesTwiceMoreThanARequestAndAnEventAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	events := &feedEvents
	c := memRig(t, func(a *App) {
		a.Get("/feed", func(_ context.Context, _ feedIn) (SSE, error) { return SSE{Func: feedFunc}, nil })
	})
	var per [3]float64
	for i, n := range []int64{0, 1, 100} {
		events.Store(n)
		per[i] = streamAllocs(c)
	}
	t.Logf("allocations per SSE request with 0, 1 and 100 events: %v, %v, %v", per[0], per[1], per[2])
	if per[0] != 4 {
		t.Errorf("an SSE stream of no events allocates %v a request, want 4 (the slab, the context, the writer, the runner)", per[0])
	}
	if per[1] != per[0] || per[2] != per[0] {
		t.Errorf("an event allocates: %v, %v, %v for 0, 1 and 100 events; want the same", per[0], per[1], per[2])
	}
}

// feedTick is the value of the typed feed: about the 30 bytes of feedFunc's data as JSON.
type feedTick struct {
	N    int64  `json:"n"`
	Room string `json:"room"`
	OK   bool   `json:"ok"`
}

// feedTypedFunc is feedFunc for an Events[feedTick]: the same ID and name, the data a value.
func feedTypedFunc(_ context.Context, e EventSender[feedTick]) error {
	for i := int64(0); i < feedEvents.Load(); i++ {
		if err := e.SendEvent(Event{ID: "42", Name: "tick"}, feedTick{N: i, Room: "lobby", OK: true}); err != nil {
			return err
		}
	}
	return nil
}

func TestAnEventsStreamAllocatesWhatAnSSEStreamDoesAndATypedEventAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Get("/feed", func(_ context.Context, _ feedIn) (Events[feedTick], error) {
			return Events[feedTick]{Func: feedTypedFunc}, nil
		})
	})
	var per [3]float64
	for i, n := range []int64{0, 1, 100} {
		feedEvents.Store(n)
		per[i] = streamAllocs(c)
	}
	t.Logf("allocations per Events request with 0, 1 and 100 events: %v, %v, %v", per[0], per[1], per[2])
	if per[0] != 4 {
		t.Errorf("an Events stream of no events allocates %v a request, want 4 (the slab, the context, the writer, the runner with its buffer)", per[0])
	}
	if per[1] != per[0] || per[2] != per[0] {
		t.Errorf("a typed event allocates: %v, %v, %v for 0, 1 and 100 events; want the same", per[0], per[1], per[2])
	}
}

// BenchmarkAnEvent compares one event sent by an SSE and by an Events, over the whole engine: a
// stream of 100 events a request, reported per event.
func BenchmarkAnEvent(b *testing.B) {
	for _, tc := range []struct {
		name string
		reg  func(a *App)
	}{
		{"SSE", func(a *App) {
			a.Get("/feed", func(_ context.Context, _ feedIn) (SSE, error) { return SSE{Func: feedFunc}, nil })
		}},
		{"SSE+json.Marshal", func(a *App) { // what a handler wrote before Events: its own encoding
			a.Get("/feed", func(_ context.Context, _ feedIn) (SSE, error) {
				return SSE{Func: func(_ context.Context, e EventWriter) error {
					for i := int64(0); i < feedEvents.Load(); i++ {
						body, _ := json.Marshal(feedTick{N: i, Room: "lobby", OK: true})
						if err := e.Send(Event{ID: "42", Name: "tick", Data: string(body)}); err != nil {
							return err
						}
					}
					return nil
				}}, nil
			})
		}},
		{"Events", func(a *App) {
			a.Get("/feed", func(_ context.Context, _ feedIn) (Events[feedTick], error) {
				return Events[feedTick]{Func: feedTypedFunc}, nil
			})
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			app := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()))
			tc.reg(app)
			ln := memconn.Listen(app.Clock())
			go app.Serve(ln)
			defer app.Close()
			c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
			if err != nil {
				b.Fatal(err)
			}
			feedEvents.Store(100)
			req := []byte("GET /feed HTTP/1.1\r\nHost: x\r\n\r\n")
			buf := make([]byte, 64<<10)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				c.Write(req)
				got := 0
				for {
					m, err := c.Read(buf[got:])
					got += m
					if err != nil || (got >= 5 && string(buf[got-5:got]) == "0\r\n\r\n") {
						break
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/100, "ns/event")
		})
	}
}

// postWith is post with extra header lines ("Name: value\r\n" each).
func postWith(target, extra, body string) []byte {
	return []byte("POST " + target + " HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\n" + extra + "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}

func TestATypedJSONPOSTBehindTheFirstPartyMiddlewareCostsWhatTheTableOfADR0032Says(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	jsonDiscard := slog.New(slog.NewJSONHandler(io.Discard, nil))
	const origin = "Origin: https://app.example.com\r\n"
	cors := CORS(CORSOptions{Origins: []string{"https://app.example.com"}, Credentials: true, ExposeHeaders: []string{"X-Request-ID"}})
	corsFunc := CORS(CORSOptions{AllowOrigin: func(o string) bool { return o == "https://tenant.example.net" }})
	corsWild := CORS(CORSOptions{Origins: []string{"https://*.example.com"}, Credentials: true, ExposeHeaders: []string{"X-Request-ID"}})
	for _, tc := range []struct {
		name  string
		extra string
		mw    []Middleware
		want  float64
		obs   []Observer
	}{
		{"RequestID, one generated", "", []Middleware{RequestID()}, 2, nil},
		{"RequestID, one taken from the request", "X-Request-ID: 3f2b8a4e-9c1d-4e8a-b0a1-5d6c7e8f9a0b\r\n", []Middleware{RequestID()}, 2, nil},
		{"CORS, a simple request from a listed origin", origin, []Middleware{cors}, 2, nil},
		{"CORS, a request with no Origin", "", []Middleware{cors}, 2, nil},
		{"CORS, a simple request matched by a wildcard subdomain", "Origin: https://a.b.example.com\r\n", []Middleware{corsWild}, 2, nil},
		{"CORS, a simple request allowed by an AllowOrigin that allocates nothing", "Origin: https://tenant.example.net\r\n", []Middleware{corsFunc}, 2, nil},
		{"CORS, a simple request from an origin no wildcard matches", "Origin: https://evilexample.com\r\n", []Middleware{corsWild}, 2, nil},
		{"Timeout, finished in time", "", []Middleware{Timeout(time.Second)}, 2, nil},
		{"all four, a JSON access log", origin + "X-Request-ID: abc\r\n", []Middleware{RequestID(), cors, Timeout(time.Second)}, 3, []Observer{AccessLog(AccessLogOptions{Logger: jsonDiscard})}},
		{"AccessLog, a text handler that discards", "", nil, 2, []Observer{AccessLog(AccessLogOptions{Logger: discard})}},
		{"AccessLog, a JSON handler that discards", "", nil, 2, []Observer{AccessLog(AccessLogOptions{Logger: jsonDiscard})}},
		{"RequestID and AccessLog, a text handler that discards", "", []Middleware{RequestID()}, 3, []Observer{AccessLog(AccessLogOptions{Logger: discard})}},
		{"RequestID and AccessLog, a JSON handler that discards", "", []Middleware{RequestID()}, 3, []Observer{AccessLog(AccessLogOptions{Logger: jsonDiscard})}},
		{"AccessLog, a level the logger does not log", "", nil, 2, []Observer{AccessLog(AccessLogOptions{Logger: discard, Level: slog.LevelDebug})}},
	} {
		c := memRig(t, func(a *App) {
			if len(tc.obs) > 0 {
				WithObserver(tc.obs...)(a)
			}
			if len(tc.mw) > 0 {
				a.Use(tc.mw...)
			}
			a.Post("/users/{id}", budgetHandler)
		})
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tc.extra, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if tc.want >= 0 && got != tc.want {
			t.Errorf("%s: allocs/request = %v, want exactly %v", tc.name, got, tc.want)
		}
	}
}

// ---- Observers, health and trace context (ADR 0051, ADR 0052, ADR 0059) ----

// noopObserver allocates nothing of its own, so what it measures is the hook.
type noopObserver struct{ n int }

func (o *noopObserver) Start(ctx context.Context, _ Request) context.Context { return ctx }
func (o *noopObserver) End(context.Context, Request, Outcome)                { o.n++ }

func TestATypedJSONPOSTWithANoOpObserverStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name string
		mw   []Middleware
		obs  []Observer
	}{
		{"one Observer, no middleware", nil, []Observer{&noopObserver{}}},
		{"three Observers, no middleware", nil, []Observer{&noopObserver{}, &noopObserver{}, &noopObserver{}}},
		{"one Observer behind RequestID", []Middleware{RequestID()}, []Observer{&noopObserver{}}},
	} {
		c := memRig(t, func(a *App) {
			WithObserver(tc.obs...)(a)
			if len(tc.mw) > 0 {
				a.Use(tc.mw...)
			}
			a.Post("/users/{id}", budgetHandler)
		})
		got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0051: the hook costs none)", tc.name, got)
		}
	}
}

func TestANoOpObserverOnTheRealClockCostsNothingOverLoopbackTCP(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := tcpRig(t, func(a *App) {
		WithObserver(&noopObserver{})(a)
		a.Post("/users/{id}", budgetHandler)
	})
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("a no-op Observer on the real clock over loopback: %v allocations per request", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
}

func TestAHealthProbeCostsOnlyTheContextAndARouteBesideItIsUnchanged(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		WithHealth(Health{Live: "/livez", Ready: "/readyz"})(a)
		a.Post("/users/{id}", budgetHandler)
	})
	for _, target := range []string{"/livez", "/readyz"} {
		got := keepAlive(t, c, []byte("GET "+target+" HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 200 OK")
		t.Logf("GET %s: %v allocations per request", target, got)
		if got != 1 {
			t.Errorf("GET %s: allocs/request = %v, want exactly 1 (the request's context, made by the engine before the app sees the request)", target, got)
		}
	}
	if got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK"); got != 2 {
		t.Errorf("a typed POST beside WithHealth: allocs/request = %v, want exactly 2", got)
	}
}

func TestATypedJSONPOSTWithATraceContextStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const tp = "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01\r\ntracestate: congo=t61rcWkgMzE,rojo=00f067aa0ba902b7\r\n"
	for _, tc := range []struct {
		name  string
		extra string
		begin bool
		mw    bool
	}{
		{"a valid traceparent and tracestate, no middleware", tp, false, false},
		{"a valid traceparent and tracestate, behind middleware", tp, false, true},
		{"no traceparent, Begin", "", true, false},
		{"no traceparent, not Begin", "", false, false},
	} {
		c := memRig(t, func(a *App) {
			WithTraceContext(TraceContext{Begin: tc.begin})(a)
			if tc.mw {
				a.Use(RequestID())
			}
			a.Post("/users/{id}", func(ctx context.Context, in budgetIn) (budgetOut, error) {
				if tr, ok := TraceFrom(ctx); ok && tr.ID == (TraceID{}) {
					return budgetOut{}, nil
				}
				return budgetHandler(ctx, in)
			})
		})
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tc.extra, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0059: the trace lives in the slab)", tc.name, got)
		}
	}
}

func TestATypedJSONPOSTCountedByMetricsStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	m := NewMetrics()
	c := memRig(t, func(a *App) {
		WithObserver(m)(a)
		a.Post("/users/{id}", budgetHandler)
	})
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("a typed POST counted by Metrics: %v allocations per request", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0060: a series that exists costs none)", got)
	}
}

func TestAnAccessLogWithATraceCostsWhatTheTableOfADR0059Says(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const tp = "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01\r\n"
	c := memRig(t, func(a *App) {
		WithTraceContext(TraceContext{})(a)
		WithObserver(AccessLog(AccessLogOptions{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}))(a)
		a.Post("/users/{id}", budgetHandler)
	})
	got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tp, typedBody), "HTTP/1.1 200 OK")
	t.Logf("AccessLog with trace_id and span_id: %v allocations per request", got)
	if got != 3 {
		t.Fatalf("allocs/request = %v, want exactly 3 (the record's attributes past five, as with a request id)", got)
	}
}

func TestATimeoutOnTheRealClockAlsoCostsNothingOverLoopbackTCP(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := tcpRig(t, func(a *App) { a.Post("/users/{id}", budgetHandler, Use(Timeout(time.Second))) })
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("Timeout on the real clock over loopback: %v allocations per request", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
}

// ---- Upload and File (ADR 0044, ADR 0043) ----

type budgetUpIn struct {
	Path struct{ ID int64 }
	Body Upload
}

func budgetUpHandler(_ context.Context, in budgetUpIn) (Text, error) {
	for {
		p, err := in.Body.Next()
		if err != nil {
			return Text{Body: "ok"}, nil
		}
		var buf [512]byte // a handler that reads a part itself boxes nothing
		for {
			if _, err := p.Read(buf[:]); err != nil {
				break
			}
		}
	}
}

func multipartPOST(parts int, chunkedBody bool) []byte {
	const b = "budget-boundary"
	var body strings.Builder
	for i := range parts {
		body.WriteString("--" + b + "\r\nContent-Disposition: form-data; name=\"f" + strconv.Itoa(i) + "\"; filename=\"a.txt\"\r\nContent-Type: text/plain\r\n\r\nsome part data\r\n")
	}
	body.WriteString("--" + b + "--\r\n")
	head := "POST /up/42 HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + b + "\r\n"
	if chunkedBody {
		return []byte(head + "Transfer-Encoding: chunked\r\n\r\n" + strconv.FormatInt(int64(body.Len()), 16) + "\r\n" + body.String() + "\r\n0\r\n\r\n")
	}
	return []byte(head + "Content-Length: " + strconv.Itoa(body.Len()) + "\r\n\r\n" + body.String())
}

func TestAnUploadRequestAllocatesTheMultipartAndOneStringBlockForEachPart(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/up/{id}", budgetUpHandler, MaxBody(1<<20)) })
	var prev float64
	for _, tc := range []struct {
		name    string
		parts   int
		chunked bool
		want    float64
	}{
		{"no part", 0, false, 3},
		{"one part", 1, false, 4},
		{"three parts", 3, false, 6},
		{"three parts, chunked", 3, true, 6},
	} {
		got := keepAlive(t, c, multipartPOST(tc.parts, tc.chunked), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want %v", tc.name, got, tc.want)
		}
		prev = got
	}
	_ = prev
}

// budgetUpEachHandler reads the parts as budgetUpHandler does, through Each, with a closure that
// captures the handler's own variables.
func budgetUpEachHandler(_ context.Context, in budgetUpIn) (Text, error) {
	n := 0
	err := in.Body.Each(func(p Part) error {
		n++
		var buf [512]byte
		for {
			if _, err := p.Read(buf[:]); err != nil {
				return nil
			}
		}
	})
	if err != nil || n < 0 {
		return Text{}, err
	}
	return Text{Body: "ok"}, nil
}

type budgetRawIn struct {
	Path struct{ ID int64 }
	Body BodyReader
}

// budgetRawBuf is the handler's read buffer, kept outside the handler: Read passes it to the
// connection, so a buffer declared in the handler is a heap allocation of the handler's own,
// which a handler pays whatever reads its body (io.Copy makes one of 32 KiB).
var budgetRawBuf [512]byte

func budgetRawHandler(_ context.Context, in budgetRawIn) (Text, error) {
	for {
		if _, err := in.Body.Read(budgetRawBuf[:]); err != nil {
			return Text{Body: "ok"}, nil
		}
	}
}

func TestABodyReaderRequestAllocatesOnlyItsHandle(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Put("/raw/{id}", budgetRawHandler, MaxBody(1<<20)) })
	body := strings.Repeat("some body bytes ", 100)
	plain := []byte("PUT /raw/42 HTTP/1.1\r\nHost: x\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
	chunkedReq := []byte("PUT /raw/42 HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n" + strconv.FormatInt(int64(len(body)), 16) + "\r\n" + body + "\r\n0\r\n\r\n")
	for _, tc := range []struct {
		name string
		req  []byte
		want float64
	}{
		{"a length body", plain, 3},
		{"a chunked body", chunkedReq, 3},
	} {
		got := keepAlive(t, c, tc.req, "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestReadingAnUploadWithEachAllocatesWhatTheLoopOverNextDoes(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	loop := memRig(t, func(a *App) { a.Post("/up/{id}", budgetUpHandler, MaxBody(1<<20)) })
	each := memRig(t, func(a *App) { a.Post("/up/{id}", budgetUpEachHandler, MaxBody(1<<20)) })
	for _, tc := range []struct {
		name    string
		parts   int
		chunked bool
		want    float64
	}{
		{"no part", 0, false, 3},
		{"one part", 1, false, 4},
		{"three parts", 3, false, 6},
		{"three parts, chunked", 3, true, 6},
	} {
		byNext := keepAlive(t, loop, multipartPOST(tc.parts, tc.chunked), "HTTP/1.1 200 OK")
		byEach := keepAlive(t, each, multipartPOST(tc.parts, tc.chunked), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request with Next, %v with Each", tc.name, byNext, byEach)
		if byEach != byNext || byEach != tc.want {
			t.Errorf("%s: allocs/request with Each = %v, with Next = %v, want %v for both", tc.name, byEach, byNext, tc.want)
		}
	}
}

func TestAFileAnswerAllocatesWhatTheStatTheOpenAndTheStreamNeed(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	os.WriteFile(filepath.Join(root.Name(), "f.txt"), []byte("0123456789"), 0o644)
	c := memRig(t, func(a *App) {
		a.Get("/f", func(context.Context, struct{}) (File, error) { return File{Root: root, Name: "f.txt"}, nil })
	})
	got := keepAlive(t, c, []byte("GET /f HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, a File of 10 bytes from an os.Root: %v", got)
	// A ceiling, not an exact count: the row is not a design target, and most of it is os.Root's
	// own walk and open, which measured 16 and later 14 on the same machine on the same day, a
	// difference not traced (ADR 0003). The stream's 4 are exact in their own test.
	if got > 16 {
		t.Errorf("allocs/request = %v, want at most 16", got)
	}
}

func TestAConditionalOrRangeFileAnswerCostsNoMoreThanTheWholeFile(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	os.WriteFile(filepath.Join(root.Name(), "f.txt"), []byte("0123456789"), 0o644)
	fi, _ := root.Stat("f.txt")
	c := memRig(t, func(a *App) {
		a.Get("/f", func(context.Context, struct{}) (File, error) { return File{Root: root, Name: "f.txt"}, nil })
	})
	tag := fmt.Sprintf(`"%x-%x"`, fi.ModTime().UnixNano(), fi.Size())
	whole := keepAlive(t, c, []byte("GET /f HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 200 OK")
	notMod := keepAlive(t, c, []byte("GET /f HTTP/1.1\r\nHost: x\r\nIf-None-Match: "+tag+"\r\n\r\n"), "HTTP/1.1 304 Not Modified")
	ranged := keepAlive(t, c, []byte("GET /f HTTP/1.1\r\nHost: x\r\nRange: bytes=2-5\r\n\r\n"), "HTTP/1.1 206 Partial Content")
	t.Logf("allocations per request: whole %v, 304 %v, 206 %v", whole, notMod, ranged)
	// A 304 opens nothing and has no stream: the stat, the context and the slab.
	if notMod > 8 {
		t.Errorf("304: allocs/request = %v, want at most 8", notMod)
	}
	if ranged > whole {
		t.Errorf("206: allocs/request = %v, more than the whole file's %v", ranged, whole)
	}
}

//go:embed testdata/static
var budgetStatic embed.FS

func TestAStaticAnswerCostsWhatTheFileAnswerCostsAndATwinLookupItsStats(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app-3f2a1b9c.js"), []byte("console.log(1)"), 0o644)
	os.WriteFile(filepath.Join(dir, "app-3f2a1b9c.js.br"), []byte("BR"), 0o644)
	os.WriteFile(filepath.Join(dir, "plain.js"), []byte("console.log(2)"), 0o644)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	c := memRig(t, func(a *App) {
		a.Static("/r", Static{Root: root})
		a.Static("/e", Static{FS: budgetStatic})
	})
	const browser = "Accept-Encoding: gzip, deflate, br, zstd\r\n"
	for _, tc := range []struct {
		name, req string
		max       float64
	}{
		{"os.Root, no Accept-Encoding", "GET /r/plain.js HTTP/1.1\r\nHost: x\r\n\r\n", 16},
		{"os.Root, a browser, no twin (remembered after the first request)", "GET /r/plain.js HTTP/1.1\r\nHost: x\r\n" + browser + "\r\n", 16},
		{"os.Root, a browser, the br twin", "GET /r/app-3f2a1b9c.js HTTP/1.1\r\nHost: x\r\n" + browser + "\r\n", 21},
		{"embed.FS, a browser, the br twin", "GET /e/testdata/static/app-3f2a1b9c.js HTTP/1.1\r\nHost: x\r\n" + browser + "\r\n", 7},
		{"embed.FS, no twin", "GET /e/testdata/static/style.css HTTP/1.1\r\nHost: x\r\n" + browser + "\r\n", 5},
	} {
		got := keepAlive(t, c, []byte(tc.req), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		// os.Root's walk is the standard library's and drifts by a few between runs (see the File
		// row of ADR 0003), so those rows are ceilings; the embed.FS rows make no stat and are exact.
		if got > tc.max {
			t.Errorf("%s: allocs/request = %v, want at most %v", tc.name, got, tc.max)
		}
	}
}

var gzipList = func() []budgetOut {
	l := make([]budgetOut, 40)
	for i := range l {
		l[i] = budgetOut{ID: int64(i), Name: "Ada Lovelace", Email: "ada@example.com", Age: 36, Page: i}
	}
	return l
}()

func TestATypedJSONAnswerOf4KiBAllocatesTwiceWithGzipOffAndWithItOnWhetherCompressedOrNot(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name   string
		gzip   bool
		header string
		small  bool
		gz     bool
	}{
		{"gzip off, client sends Accept-Encoding: gzip", false, "Accept-Encoding: gzip\r\n", false, false},
		{"gzip on, client accepts it, body compressed", true, "Accept-Encoding: gzip\r\n", false, true},
		{"gzip on, client accepts only identity", true, "Accept-Encoding: identity\r\n", false, false},
		{"gzip on, client accepts it, body under MinSize", true, "Accept-Encoding: gzip\r\n", true, false},
	} {
		app := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()))
		if tc.gzip {
			app = New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()), WithGzip(Gzip{}))
		}
		app.Get("/list/{n}", func(_ context.Context, in struct{ Path struct{ N int } }) ([]budgetOut, error) {
			return gzipList[:in.Path.N], nil
		})
		ln := memconn.Listen(app.Clock())
		go app.Serve(ln)
		c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		if err != nil {
			t.Fatal(err)
		}
		n := 40
		if tc.small {
			n = 2
		}
		req := []byte("GET /list/" + strconv.Itoa(n) + " HTTP/1.1\r\nHost: x\r\n" + tc.header + "\r\n")
		got := keepAlive(t, c, req, "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0003, ADR 0031)", tc.name, got)
		}
		c.Close()
		app.Close()
	}
}

// readAnswer reads one whole answer that carries a Content-Length into body, grown when it is
// short, without allocating otherwise, so an allocation count around it is the server's.
func readAnswer(t *testing.T, br *bufio.Reader, body []byte) []byte {
	t.Helper()
	n := -1
	for first := true; ; first = false {
		l, err := br.ReadSlice('\n')
		if err != nil {
			t.Fatal(err)
		}
		if first && !bytes.HasPrefix(l, []byte("HTTP/1.1 200")) {
			t.Fatalf("the answer is not a 200: %q", l)
		}
		if len(l) == 2 {
			break
		}
		if k, v, ok := bytes.Cut(l, []byte(":")); ok && bytes.EqualFold(k, []byte("Content-Length")) {
			n = 0
			for _, c := range bytes.TrimSpace(v) {
				n = n*10 + int(c-'0')
			}
		}
	}
	if cap(body) < n {
		body = make([]byte, n)
	}
	body = body[:n]
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAJSONAnswerOf300KiBAllocatesTwiceOnceItsRouteHasSeenOne(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	big := make([]budgetOut, 4000)
	for i := range big {
		big[i] = budgetOut{ID: int64(i), Name: "Ada Lovelace", Email: "ada@example.com", Age: 36, Page: i}
	}
	c := memRig(t, func(a *App) {
		a.Get("/big", func(context.Context, struct{}) ([]budgetOut, error) { return big, nil })
	})
	req := []byte("GET /big HTTP/1.1\r\nHost: x\r\n\r\n")
	br := bufio.NewReaderSize(c, 64<<10)
	var body []byte
	do := func() {
		c.Write(req)
		body = readAnswer(t, br, body)
	}
	do()
	if len(body) < 300<<10 {
		t.Fatalf("the answer is %d bytes, want at least 300 KiB", len(body))
	}
	for range 20 {
		do()
	}
	got := testing.AllocsPerRun(200, do)
	t.Logf("%v allocations a request, %d-byte answer", got, len(body))
	if got != 2 {
		t.Errorf("allocs/request = %v, want exactly 2: a large answer reserves its route's last size from the engine's pool of large buffers rather than growing by doubling (ADR 0003)", got)
	}
}

type budgetResult interface{}

func TestAnInterfaceReturnWithReturnsAllocatesOnceMoreThanAPlainOneForTheBoxedValue(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	rows := []struct {
		name string
		h    func(context.Context, budgetIn) (budgetResult, error)
		opts []RouteOption
	}{
		{"a wrapper by value", func(_ context.Context, in budgetIn) (budgetResult, error) {
			o, _ := budgetHandler(nil, in)
			return OK[budgetOut]{Body: o}, nil
		}, []RouteOption{Returns[OK[budgetOut]](), Returns[NoContent]()}},
		{"a plain value", func(_ context.Context, in budgetIn) (budgetResult, error) {
			o, _ := budgetHandler(nil, in)
			return o, nil
		}, []RouteOption{Returns[budgetOut](), Returns[NoContent]()}},
		{"a pointer to a wrapper", func(_ context.Context, in budgetIn) (budgetResult, error) {
			o, _ := budgetHandler(nil, in)
			return &OK[budgetOut]{Body: o}, nil
		}, []RouteOption{Returns[OK[budgetOut]]()}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := memRig(t, func(a *App) { a.Post("/users/{id}", row.h, row.opts...) })
			got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
			t.Logf("allocations per request, interface return, %s: %v", row.name, got)
			if got != 3 {
				t.Fatalf("allocs/request = %v, want exactly 3 (ADR 0003: the slab, the context, and the handler boxing its value)", got)
			}
		})
	}
}

func TestAOneOfReturnAllocatesNoMoreThanAPlainOneBecauseNothingIsBoxed(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	type answer = OneOf3[OK[budgetOut], NoContent, budgetOut]
	rows := []struct {
		name string
		h    func(context.Context, budgetIn) (answer, error)
	}{
		{"a wrapper variant", func(_ context.Context, in budgetIn) (answer, error) {
			o, _ := budgetHandler(nil, in)
			return answer{}.A(OK[budgetOut]{Body: o}), nil
		}},
		{"a plain value variant", func(_ context.Context, in budgetIn) (answer, error) {
			o, _ := budgetHandler(nil, in)
			return answer{}.C(o), nil
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := memRig(t, func(a *App) { a.Post("/users/{id}", row.h) })
			got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
			t.Logf("allocations per request, OneOf return, %s: %v", row.name, got)
			if got != 2 {
				t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003, ADR 0013: the slab and the context; the variant is held in the OneOf, not boxed)", got)
			}
		})
	}
}

// Items, Format and a uuid.UUID add nothing to a request that passes (ADR 0021): the same body is
// decoded into a list type with an Items bound and into a plain list, and costs the same.
type (
	budgetTags []string
	budgetMail string
)

func (budgetTags) Items() (min, max int) { return 1, 8 }
func (budgetMail) Format() string        { return "email" }

type budgetItemsIn struct {
	Body struct {
		Tags budgetTags
		Mail budgetMail
		Ref  uuid.UUID
	}
}

type budgetPlainIn struct {
	Body struct {
		Tags []string
		Mail string
		Ref  uuid.UUID
	}
}

func TestAListWithItemsAndAUUIDCostsWhatAPlainListAndAStringCost(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const body = `{"tags":["a","b","c"],"mail":"ada@example.com","ref":"123e4567-e89b-12d3-a456-426614174000"}`
	withItems := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetItemsIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	plain := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetPlainIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: with Items, Format and uuid.UUID %v, plain list, string and uuid.UUID %v", withItems, plain)
	if withItems != plain {
		t.Fatalf("Items and Format cost %v allocations a request, a plain list %v: they must cost none", withItems, plain)
	}
}

// A value that matches its Pattern adds nothing to a request (ADR 0021): the same body is decoded
// into a string type with a Pattern and into a plain string, and costs the same.
type budgetSlug string

func (budgetSlug) Pattern() string { return `^[a-z0-9-]+$` }

type budgetPatternIn struct {
	Body struct{ Slug budgetSlug }
}

type budgetPlainSlugIn struct {
	Body struct{ Slug string }
}

func TestAStringThatMatchesItsPatternCostsWhatAPlainStringCosts(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const body = `{"slug":"ada-lovelace-1815"}`
	withPattern := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetPatternIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	plain := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetPlainSlugIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: with a Pattern %v, plain string %v", withPattern, plain)
	if withPattern != plain {
		t.Fatalf("a Pattern costs %v allocations a request, a plain string %v: a match must cost none", withPattern, plain)
	}
}

// A value that passes a Range on a float and every checked Format adds nothing to a request
// (ADR 0021): the same body is decoded into these types and into plain ones, and costs what a
// typical typed JSON request costs, 2.
type (
	budgetAmount   float64
	budgetMailFmt  string
	budgetURI      string
	budgetHost     string
	budgetV4       string
	budgetV6       string
	budgetDay      string
	budgetStamp    string
	budgetIdentity string
)

func (budgetAmount) Range() (min, max float64) { return 0.01, 1000000 }
func (budgetMailFmt) Format() string           { return "email" }
func (budgetURI) Format() string               { return "uri" }
func (budgetHost) Format() string              { return "hostname" }
func (budgetV4) Format() string                { return "ipv4" }
func (budgetV6) Format() string                { return "ipv6" }
func (budgetDay) Format() string               { return "date" }
func (budgetStamp) Format() string             { return "date-time" }
func (budgetIdentity) Format() string          { return "uuid" }

type budgetBoundsIn struct {
	Body struct {
		Amount budgetAmount
		Email  budgetMailFmt
		URI    budgetURI
		Host   budgetHost
		V4     budgetV4
		V6     budgetV6
		Day    budgetDay
		Stamp  budgetStamp
		ID     budgetIdentity
	}
}

type budgetPlainBoundsIn struct {
	Body struct {
		Amount             float64
		Email, URI, Host   string
		V4, V6, Day, Stamp string
		ID                 string
	}
}

func TestAFloatRangeAndEveryCheckedFormatCostWhatPlainFieldsCost(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const body = `{"amount":12.5,"email":"ada@example.com","uri":"https://example.com/a%20b?q=1","host":"api.example.com","v4":"192.168.0.1","v6":"2001:db8::1","day":"2026-11-03","stamp":"2026-11-03T10:20:30.5+07:00","id":"123e4567-e89b-12d3-a456-426614174000"}`
	with := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetBoundsIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	plain := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetPlainBoundsIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: with a float Range and eight checked formats %v, plain fields %v", with, plain)
	if with != plain || with != 2 {
		t.Fatalf("a float Range and the checked formats cost %v allocations a request, plain fields %v, want both exactly 2 (ADR 0003, ADR 0021)", with, plain)
	}
}

type budgetCSVIn struct {
	Query struct{ Ids CSV[int] }
}

type budgetRepeatedIn struct {
	Query struct{ Ids []int }
}

func TestACSVOfThreeElementsCostsWhatThreeRepeatedKeysCost(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	get := func(target string) []byte { return []byte("GET " + target + " HTTP/1.1\r\nHost: x\r\n\r\n") }
	csv := keepAlive(t, memRig(t, func(a *App) {
		a.Get("/x", func(_ context.Context, in budgetCSVIn) (budgetOut, error) {
			return budgetOut{Age: len(in.Query.Ids)}, nil
		})
	}), get("/x?ids=1,2,3"), "HTTP/1.1 200 OK")
	repeated := keepAlive(t, memRig(t, func(a *App) {
		a.Get("/x", func(_ context.Context, in budgetRepeatedIn) (budgetOut, error) {
			return budgetOut{Age: len(in.Query.Ids)}, nil
		})
	}), get("/x?ids=1&ids=2&ids=3"), "HTTP/1.1 200 OK")
	empty := keepAlive(t, memRig(t, func(a *App) {
		a.Get("/x", func(_ context.Context, in budgetCSVIn) (budgetOut, error) {
			return budgetOut{Age: len(in.Query.Ids)}, nil
		})
	}), get("/x?ids="), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: CSV of 3 %v, 3 repeated keys %v, empty CSV %v", csv, repeated, empty)
	if csv != repeated || csv != 3 || empty != 2 {
		t.Fatalf("CSV of 3 costs %v, repeated keys %v, empty CSV %v: want 3, 3 and 2 (the slab, the context, one backing array)", csv, repeated, empty)
	}
}

type budgetDateIn struct {
	Path  struct{ Day Date }
	Query struct {
		From Opt[Date]
		Page int
	}
	Body struct {
		Name string
		Due  Date
	}
}

type budgetDateOut struct {
	Day  Date   `json:"day"`
	Due  Date   `json:"due"`
	Name string `json:"name"`
}

func budgetDateHandler(_ context.Context, in budgetDateIn) (budgetDateOut, error) {
	return budgetDateOut{Day: in.Path.Day, Due: in.Body.Due, Name: in.Body.Name}, nil
}

func TestATypedJSONPOSTWithADateInPathQueryBodyAndAnswerStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/days/{day}", budgetDateHandler) })
	got := keepAlive(t, c, post("/days/2026-11-03?from=2026-01-02&page=3", `{"name":"Ada","due":"2027-02-28"}`), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, a Date in every section: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: a Date is an integer, decoded where it lies)", got)
	}
}

func TestATypedJSONPOSTOnARouteThatRejectsUnknownQueryKeysStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) { a.Post("/users/{id}", budgetHandler, RejectUnknownQueryKeys()) })
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: the check is a failed lookup)", got)
	}
}

type budgetFailDB struct{}

func (budgetFailDB) Error() string { return "db: no rows" }

var budgetNoSuchRecord = NotFound("no such record")

func TestAnErrorMapperCostsTheSuccessPathNothingAndAMappedErrorCostsWhatTheSameStatusErrorCosts(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	mapper := WithErrorMapper(func(err error) *StatusError {
		if _, ok := err.(budgetFailDB); ok {
			return budgetNoSuchRecord
		}
		return nil
	})
	rig := func(opts ...Option) net.Conn {
		t.Helper()
		app := New(append([]Option{WithClock(clock.NewFake(time.Now())), WithLogger(quiet())}, opts...)...)
		app.Post("/users/{id}", budgetHandler)
		app.Post("/fail/{id}", func(context.Context, budgetIn) (budgetOut, error) { return budgetOut{}, budgetFailDB{} })
		app.Post("/own/{id}", func(context.Context, budgetIn) (budgetOut, error) { return budgetOut{}, budgetNoSuchRecord })
		ln := memconn.Listen(app.Clock())
		go app.Serve(ln)
		t.Cleanup(func() { app.Close() })
		c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	body := `{"name":"Ada Lovelace","email":"ada@example.com","age":36}`
	without, with := rig(), rig(mapper)
	ok0 := keepAlive(t, without, post("/users/42?expand=true&page=3", body), "HTTP/1.1 200 OK")
	ok1 := keepAlive(t, with, post("/users/42?expand=true&page=3", body), "HTTP/1.1 200 OK")
	own := keepAlive(t, with, post("/own/42?expand=true&page=3", body), "HTTP/1.1 404 Not Found")
	mapped := keepAlive(t, with, post("/fail/42?expand=true&page=3", body), "HTTP/1.1 404 Not Found")
	t.Logf("allocations per request: success without the option %v, with it %v; a StatusError the handler returned %v; the same answer from the mapper %v", ok0, ok1, own, mapped)
	if ok0 != 2 || ok1 != 2 {
		t.Fatalf("a success costs %v without the option and %v with it, want 2 both", ok0, ok1)
	}
	if mapped != own {
		t.Fatalf("a mapped error costs %v allocations, the same StatusError returned by the handler %v: they must cost the same", mapped, own)
	}
}

type budgetSort string

func (budgetSort) Values() []budgetSort { return []budgetSort{"risk", "due"} }

type budgetPaged struct {
	Limit    int
	Sort     budgetSort
	Archived bool
	Label    string
}

func (budgetPaged) Defaults() map[string]string {
	return map[string]string{"Limit": "50", "Sort": "risk", "Archived": "false", "Label": "all"}
}

func TestATypedGETThatTakesFourDefaultsAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Get("/items", func(_ context.Context, in struct{ Query budgetPaged }) (budgetOut, error) {
			return budgetOut{ID: int64(in.Query.Limit), Name: in.Query.Label, Page: len(in.Query.Sort)}, nil
		})
	})
	got := keepAlive(t, c, []byte("GET /items HTTP/1.1\r\nHost: x\r\n\r\n"), "HTTP/1.1 200 OK")
	t.Logf("allocs/request with four defaults taken = %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2: a default is decoded from text that already lives in the program (ADR 0041)", got)
	}
}

func TestAnObserverToldTheErrorOfAFailedRequestAddsNoAllocationToTheFailure(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	failing := func(context.Context, struct{ Path struct{ ID int64 } }) (budgetOut, error) {
		return budgetOut{}, errBudget
	}
	bare := memRig(t, func(a *App) { a.Get("/users/{id}", failing) })
	watched := memRig(t, func(a *App) {
		WithObserver(&noopObserver{})(a)
		a.Get("/users/{id}", failing)
	})
	req := []byte("GET /users/42 HTTP/1.1\r\nHost: x\r\n\r\n")
	without := keepAlive(t, bare, req, "HTTP/1.1 500")
	with := keepAlive(t, watched, req, "HTTP/1.1 500")
	t.Logf("a handler error answered 500: %v allocations per request, %v with an Observer told Err", without, with)
	if with > without {
		t.Errorf("an Observer adds %v allocations to a failed request, want 0 (ADR 0051)", with-without)
	}
}

var errBudget = errors.New("budget: the handler failed")

func TestReadingTheRequestIDFromTheContextCostsNothingAndLoggerCostsItsWith(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name string
		h    func(context.Context, budgetIn) (budgetOut, error)
		want float64
	}{
		{"RequestIDFrom in the handler", func(ctx context.Context, in budgetIn) (budgetOut, error) {
			_ = RequestIDFrom(ctx)
			return budgetHandler(ctx, in)
		}, 2},
		{"Logger in the handler, with the request id", func(ctx context.Context, in budgetIn) (budgetOut, error) {
			_ = Logger(ctx)
			return budgetHandler(ctx, in)
		}, -1},
	} {
		c := memRig(t, func(a *App) {
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))(a)
			a.Use(RequestID())
			a.Post("/users/{id}", tc.h)
		})
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", "", typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if tc.want >= 0 && got != tc.want {
			t.Errorf("%s: allocs/request = %v, want exactly %v", tc.name, got, tc.want)
		}
	}
}

func TestARouteBesideAServiceKeepsTheRequestAllocationsAtTwo(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Service(Service{
			Name:           "pool",
			Start:          func(context.Context) error { return nil },
			BeforeShutdown: func(context.Context) error { return nil },
			Stop:           func(context.Context) error { return nil },
		})
		a.Post("/users/{id}", budgetHandler)
	})
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, typed JSON POST beside a Service: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0079: a Service adds nothing to the request path)", got)
	}
}

type budgetMPIn struct {
	Path struct{ ID int64 }
	Body Multipart[struct {
		Title   string
		Price   int64
		Cover   Opt[FormFile]
		Gallery []FormFile
	}]
}

func budgetMPHandler(_ context.Context, in budgetMPIn) (Text, error) {
	f := in.Body.Value
	if f.Price < 0 || f.Title == "" {
		return Text{}, nil
	}
	return Text{Body: "ok"}, nil
}

func multipartForm(files int, fileBytes int) []byte {
	const b = "budget-boundary"
	var body strings.Builder
	body.WriteString("--" + b + "\r\nContent-Disposition: form-data; name=\"title\"\r\n\r\nA flat\r\n")
	body.WriteString("--" + b + "\r\nContent-Disposition: form-data; name=\"price\"\r\n\r\n1200\r\n")
	data := strings.Repeat("x", fileBytes)
	for i := range files {
		name := "gallery"
		if i == 0 {
			name = "cover"
		}
		body.WriteString("--" + b + "\r\nContent-Disposition: form-data; name=\"" + name + "\"; filename=\"a.png\"\r\nContent-Type: image/png\r\n\r\n" + data + "\r\n")
	}
	body.WriteString("--" + b + "--\r\n")
	return []byte("POST /users/42?expand=true HTTP/1.1\r\nHost: x\r\nContent-Type: multipart/form-data; boundary=" + b +
		"\r\nContent-Length: " + strconv.Itoa(body.Len()) + "\r\n\r\n" + body.String())
}

func TestAMultipartRequestCostsTheTextBlockAndOneBlockForEachFileKeptInMemoryAndOneForEachSpilled(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	dir := t.TempDir()
	app := func(a *App) { a.Post("/users/{id}", budgetMPHandler, MaxBody(8<<20)) }
	c := func() net.Conn {
		a := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()), WithLimits(Limits{UploadDir: dir}))
		app(a)
		ln := memconn.Listen(a.Clock())
		go a.Serve(ln)
		t.Cleanup(func() { a.Close() })
		conn, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}()
	for _, tc := range []struct {
		name string
		req  []byte
		want float64
	}{
		{"text fields only", multipartForm(0, 0), 3},
		{"one file of 1 KiB", multipartForm(1, 1<<10), 5},
		{"two files of 1 KiB", multipartForm(2, 1<<10), 8},
		{"one file of 100 KiB", multipartForm(1, 100<<10), 5},
		{"one file of 300 KiB, spilled", multipartForm(1, 300<<10), 13},
	} {
		got := keepAlive(t, c, tc.req, "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		// os.CreateTemp draws a random name, and a draw that meets an existing name costs one more.
		if got != tc.want && !(strings.Contains(tc.name, "spilled") && got == tc.want-1) {
			t.Errorf("%s: allocs/request = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestATypedJSONRequestStillAllocatesTwiceWithTheMultipartPlanCompiledIntoTheApp(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	c := memRig(t, func(a *App) {
		a.Post("/users/{id}", budgetHandler)
		a.Post("/listings/{id}", budgetMPHandler, MaxBody(8<<20))
	})
	got := keepAlive(t, c, post("/users/42?expand=true&page=3", typedBody), "HTTP/1.1 200 OK")
	t.Logf("allocations per request, typed JSON POST beside a Multipart route: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (ADR 0003: the slab and the context)", got)
	}
}

// Exclusive bounds, MultipleOf, Unique and Members add nothing to a request that passes (ADR 0021):
// the same body is decoded into these types and into plain ones, and costs the same.
type (
	budgetPrice  float64
	budgetCents  float64
	budgetStep   int
	budgetUnique []string
	budgetNums   []int64
	budgetBag    map[string]int
)

func (budgetPrice) ExclusiveMin() float64 { return 0 }
func (budgetPrice) ExclusiveMax() float64 { return 1e6 }
func (budgetCents) MultipleOf() float64   { return 0.01 }
func (budgetStep) MultipleOf() int64      { return 5 }
func (budgetUnique) Unique() bool         { return true }
func (budgetNums) Unique() bool           { return true }
func (budgetBag) Members() (min, max int) { return 1, 8 }

type budgetStrictIn struct {
	Body struct {
		Price budgetPrice
		Cents budgetCents
		Step  budgetStep
		Tags  budgetUnique
		Nums  budgetNums
		Bag   budgetBag
	}
}

type budgetLooseIn struct {
	Body struct {
		Price float64
		Cents float64
		Step  int
		Tags  []string
		Nums  []int64
		Bag   map[string]int
	}
}

func TestExclusiveBoundsAMultipleAUniqueListAndAMemberCountCostWhatPlainFieldsCost(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const body = `{"price":12.5,"cents":19.99,"step":15,"tags":["a","b","c"],"nums":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16],"bag":{"x":1,"y":2}}`
	with := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetStrictIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	plain := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetLooseIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", body), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: with the new rules %v, plain fields %v", with, plain)
	if with != plain {
		t.Fatalf("the new rules cost %v allocations a request, plain fields %v: a request that passes must pay none", with, plain)
	}
}

type budgetScalarStrictIn struct {
	Body struct {
		Price budgetPrice
		Cents budgetCents
		Step  budgetStep
	}
}

func TestAnExclusiveBoundAndAMultipleOnScalarFieldsKeepATypedRequestAtTwoAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	got := keepAlive(t, memRig(t, func(a *App) {
		a.Post("/x", func(_ context.Context, in budgetScalarStrictIn) (budgetOut, error) { return budgetOut{}, nil })
	}), post("/x", `{"price":12.5,"cents":19.99,"step":15}`), "HTTP/1.1 200 OK")
	t.Logf("allocations per request: %v", got)
	if got != 2 {
		t.Fatalf("a request with an exclusive bound, a float multiple and an integer multiple costs %v allocations, want 2 (ADR 0003, ADR 0021)", got)
	}
}
