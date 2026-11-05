package bites_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// bitetestWith is a GET to target with the headers given as name, value pairs.
func bitetestWith(target string, h ...string) bitetest.Request {
	r := bitetest.Request{Method: "GET", Target: target, Header: map[string][]string{}}
	for i := 0; i+1 < len(h); i += 2 {
		r.Header[h[i]] = append(r.Header[h[i]], h[i+1])
	}
	return r
}

var hexID = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestARequestWithNoIDGetsAGeneratedOneInTheHeaderAndInItsProblemBody(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", getUser)
	})
	r := srv.Do(req("GET", "/nope"))
	id := r.Get("x-request-id")
	if r.Status != 404 || !hexID.MatchString(id) {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	wantProblem(t, r, 404, `{"title":"Not Found","status":404,"request_id":"`+id+`"}`)
	if again := srv.Do(req("GET", "/users/1")).Get("x-request-id"); !hexID.MatchString(again) || again == id {
		t.Fatalf("a second request got %q after %q: want a different generated id", again, id)
	}
}

func TestAWellFormedIncomingIDIsKeptAndEchoed(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", getUser)
	})
	for _, id := range []string{"abc123", "3f2b8a4e-9c1d-4e8a-b0a1-5d6c7e8f9a0b", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "a.b_c-d:e", strings.Repeat("x", 64)} {
		r := srv.Do(bitetestWith("/users/abc", "X-Request-ID", id))
		if r.Get("x-request-id") != id || !strings.HasSuffix(r.Text(), `,"request_id":"`+id+`"}`) {
			t.Errorf("%q: %v %s", id, r.Header, r.Text())
		}
	}
}

func TestAnIncomingIDThatIsTooLongOrHasOtherBytesIsReplacedNotRejected(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", getUser)
	})
	for name, id := range map[string]string{"too long": strings.Repeat("x", 65), "a space": "a b", "a quote": `a"b`, "a slash": "a/b", "non ASCII": "idé"} {
		r := srv.Do(bitetestWith("/users/1", "X-Request-ID", id))
		if r.Status != 200 || !hexID.MatchString(r.Get("x-request-id")) {
			t.Errorf("%s: %d %v", name, r.Status, r.Header)
		}
	}
}

func TestTheRequestIDHeaderNameIsConfigurable(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID(bites.RequestIDOptions{Header: "X-Correlation-ID"}))
		app.Get("/users/{id}", getUser)
	})
	r := srv.Do(bitetestWith("/users/1", "X-Correlation-ID", "c-1", "X-Request-ID", "ignored"))
	if r.Get("x-correlation-id") != "c-1" || r.Get("x-request-id") != "" {
		t.Fatalf("%v", r.Header)
	}
}

func TestTheIDIsVisibleToMiddlewareInsideItAndToThePanicAnswer(t *testing.T) {
	var seen string
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID(), func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			seen = req.RequestID()
			return next.Call(ctx), nil
		})
		app.Get("/boom", func(context.Context, Empty) (Empty, error) { panic("boom") })
	})
	r := srv.Do(bitetestWith("/boom", "X-Request-ID", "p-1"))
	wantProblem(t, r, 500, `{"title":"Internal Server Error","status":500,"request_id":"p-1"}`)
	if got := r.Get("X-Request-ID"); got != "p-1" {
		t.Fatalf("the 500 after a panic has X-Request-ID %q, want p-1", got)
	}
	if seen != "p-1" {
		t.Fatalf("seen %q", seen)
	}
}

func TestThe500AfterAPanicCarriesTheIDInTheConfiguredHeaderAndAPlainOneCarriesNone(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/with", bites.Use(bites.RequestID(bites.RequestIDOptions{Header: "Trace-Id"})))
		g.Get("/boom", func(context.Context, Empty) (Empty, error) { panic("boom") })
		app.Get("/without", func(context.Context, Empty) (Empty, error) { panic("boom") })
	})
	r := srv.Do(bitetestWith("/with/boom", "Trace-Id", "t-7"))
	if r.Status != 500 || r.Get("Trace-Id") != "t-7" {
		t.Fatalf("status %d, Trace-Id %q", r.Status, r.Get("Trace-Id"))
	}
	r = srv.Do(bitetestWith("/without", "Trace-Id", "t-8"))
	if r.Status != 500 || r.Get("Trace-Id") != "" || r.Get("X-Request-ID") != "" {
		t.Fatalf("a route with no request-ID middleware got an id header: %+v", r)
	}
}

func TestTheHandlerReadsTheRequestIDFromItsContextAndItIsTheOneInTheProblemAndTheAccessRecord(t *testing.T) {
	var mu sync.Mutex
	var seen string
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			mu.Lock()
			seen = bites.RequestIDFrom(ctx)
			mu.Unlock()
			return User{}, bites.BadRequest("no")
		})
	})
	r := srv.Do(bitetestWith("/users/1", "X-Request-ID", "req-ctx-1"))
	rec := waitRecords(t, srv, lb, 1)[0]
	mu.Lock()
	defer mu.Unlock()
	if seen != "req-ctx-1" || rec["request_id"] != seen || !strings.HasSuffix(r.Text(), `"request_id":"req-ctx-1"}`) {
		t.Fatalf("handler saw %q, record %v, body %s", seen, rec["request_id"], r.Text())
	}
}

func TestRequestIDFromWorksThroughContextsTheHandlerDerived(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			tctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			vctx := context.WithValue(tctx, struct{}{}, 1)
			mu.Lock()
			got["timeout"], got["value"] = bites.RequestIDFrom(tctx), bites.RequestIDFrom(vctx)
			mu.Unlock()
			return User{}, nil
		})
	})
	srv.Do(bitetestWith("/users/1", "X-Request-ID", "derived-1"))
	mu.Lock()
	defer mu.Unlock()
	if got["timeout"] != "derived-1" || got["value"] != "derived-1" {
		t.Fatalf("%v", got)
	}
}

func TestRequestIDFromIsEmptyWithoutTheMiddlewareAndOnContextsThatAreNotARequests(t *testing.T) {
	var mu sync.Mutex
	seen := "unset"
	srv := start(t, func(app *bites.App) {
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			mu.Lock()
			seen = bites.RequestIDFrom(ctx)
			mu.Unlock()
			return User{}, nil
		})
	})
	srv.Do(req("GET", "/users/1"))
	mu.Lock()
	defer mu.Unlock()
	if seen != "" || bites.RequestIDFrom(context.Background()) != "" || bites.RequestIDFrom(nil) != "" { //nolint:staticcheck
		t.Fatalf("handler saw %q", seen)
	}
}

func TestAContextKeptPastTheRequestKeepsItsOwnRequestIDWhileOthersRun(t *testing.T) {
	var mu sync.Mutex
	kept := map[string]context.Context{}
	srv := start(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			mu.Lock()
			kept[bites.RequestIDFrom(ctx)] = ctx
			mu.Unlock()
			return User{}, nil
		})
	})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				srv.Do(bitetestWith("/users/1", "X-Request-ID", fmt.Sprintf("kept-%d-%d", g, i)))
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(kept) != 400 {
		t.Fatalf("%d ids kept, want 400", len(kept))
	}
	for id, ctx := range kept {
		if got := bites.RequestIDFrom(ctx); got != id {
			t.Fatalf("a context kept for %q answers %q", id, got)
		}
	}
}
