package bites_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// trail records the order middleware ran in, safe across the connections' goroutines.
type trail struct {
	mu sync.Mutex
	s  []string
}

func (t *trail) add(s string) {
	t.mu.Lock()
	t.s = append(t.s, s)
	t.mu.Unlock()
}

func (t *trail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.s, " ")
}

// tag is a middleware that records "name>" going in and "<name" coming out.
func (t *trail) tag(name string) bites.Middleware {
	return func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
		t.add(name + ">")
		res := next.Call(ctx)
		t.add("<" + name)
		return res, nil
	}
}

func TestAppMiddlewareSees404And405AndAPreflightBeforeRouting(t *testing.T) {
	var tr trail
	seen := func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		res := next.Call(ctx)
		tr.add(fmt.Sprintf("%s %s %d", req.Method(), req.Path(), res.Status()))
		return res, nil
	}
	preflight := func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		if req.Method() == "OPTIONS" && req.Header("Access-Control-Request-Method") != "" {
			res := bites.NewResponse(204)
			res.AddHeader("Access-Control-Allow-Origin", "*")
			return res, nil
		}
		return next.Call(ctx), nil
	}
	srv := start(t, func(app *bites.App) {
		app.Get("/users/{id}", getUser)
		app.Use(seen, preflight) // after the route on purpose: app-level Use is exempt (ADR 0048)
	})
	wantProblem(t, srv.Do(req("GET", "/nope")), 404, `{"title":"Not Found","status":404}`)
	wantProblem(t, srv.Do(req("POST", "/users/1")), 405, `{"title":"Method Not Allowed","status":405}`)
	r := srv.Do(bitetest.Request{Method: "OPTIONS", Target: "/users/1", Header: map[string][]string{"Access-Control-Request-Method": {"GET"}, "Origin": {"https://a.example"}}})
	if r.Status != 204 || r.Get("access-control-allow-origin") != "*" {
		t.Fatalf("preflight: %d %v", r.Status, r.Header)
	}
	if r := srv.Do(req("GET", "/users/7")); r.Status != 200 {
		t.Fatalf("a matched route still works: %d %s", r.Status, r.Text())
	}
	want := "GET /nope 404 POST /users/1 405 OPTIONS /users/1 204 GET /users/7 200"
	if got := tr.String(); got != want {
		t.Fatalf("seen:\n got  %s\n want %s", got, want)
	}
}

func TestMiddlewareRunsAppThenOuterGroupThenInnerGroupThenRouteAndBackOut(t *testing.T) {
	var tr trail
	srv := start(t, func(app *bites.App) {
		app.Use(tr.tag("app1"), tr.tag("app2"))
		outer := app.Group("/api")
		outer.Use(tr.tag("outer1"), tr.tag("outer2"))
		inner := outer.Group("/v1", bites.Use(tr.tag("inner")))
		inner.Get("/users/{id}", getUser, bites.Use(tr.tag("route")))
		inner.Get("/plain/{id}", getUser)
	})
	if r := srv.Do(req("GET", "/api/v1/users/1")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	want := "app1> app2> outer1> outer2> inner> route> <route <inner <outer2 <outer1 <app2 <app1"
	if got := tr.String(); got != want {
		t.Fatalf("order:\n got  %s\n want %s", got, want)
	}
	tr = trail{}
	srv.Do(req("GET", "/api/v1/plain/1"))
	want = "app1> app2> outer1> outer2> inner> <inner <outer2 <outer1 <app2 <app1"
	if got := tr.String(); got != want {
		t.Fatalf("a route with no middleware of its own:\n got  %s\n want %s", got, want)
	}
	tr = trail{}
	srv.Do(req("GET", "/api/v1/nope"))
	if got := tr.String(); got != "app1> app2> <app2 <app1" {
		t.Fatalf("group middleware does not run for a path no route matched: %s", got)
	}
}

func TestAMiddlewareThatDoesNotCallNextAnswersAndTheHandlerNeverRuns(t *testing.T) {
	ran := false
	deny := func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
		res := bites.NewResponse(401)
		res.SetBody("text/plain", "no")
		res.AddHeader("WWW-Authenticate", "Bearer")
		return res, nil
	}
	srv := start(t, func(app *bites.App) {
		app.Get("/x/{id}", func(ctx context.Context, in GetUser) (User, error) { ran = true; return getUser(ctx, in) }, bites.Use(deny))
	})
	r := srv.Do(req("GET", "/x/1"))
	if r.Status != 401 || r.Text() != "no" || r.Get("content-type") != "text/plain" || r.Get("www-authenticate") != "Bearer" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Text())
	}
	if ran {
		t.Fatal("the handler ran")
	}
}

func TestAMiddlewareCanChangeTheStatusAndAddHeadersToTheInnerAnswer(t *testing.T) {
	var size int
	decorate := func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
		res := next.Call(ctx)
		size = res.Size()
		if res.ContentType() != "application/json" || res.Status() != 200 {
			return res, errors.New("the inner answer is not what the handler wrote")
		}
		res.SetStatus(203)
		res.AddHeader("X-One", "1")
		res.SetHeader("X-Two", "a")
		res.SetHeader("x-two", "b")
		for i := range 8 { // past the six inline pairs
			res.AddHeader(fmt.Sprintf("X-Extra-%d", i), "e")
		}
		if v, ok := res.Header("X-TWO"); !ok || v != "b" {
			return res, errors.New("SetHeader did not replace")
		}
		return res, nil
	}
	srv := start(t, func(app *bites.App) { app.Use(decorate); app.Get("/users/{id}", getUser) })
	r := srv.Do(req("GET", "/users/9"))
	if r.Status != 203 || r.Get("x-one") != "1" || r.Get("x-two") != "b" || len(r.Header["x-two"]) != 1 || r.Get("x-extra-7") != "e" || r.Get("content-type") != "application/json" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	if size != len(r.Body) || size == 0 {
		t.Fatalf("Size() = %d, the body has %d bytes", size, len(r.Body))
	}
}

func TestTheRequestIDIsInEveryProblemJSONAnswer(t *testing.T) {
	setID := func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		req.SetRequestID("req-" + req.Header("x-n"))
		return next.Call(ctx), nil
	}
	type Body struct {
		Body struct{ Name string }
	}
	boom := func(context.Context, GetUser) (User, error) { return User{}, errors.New("secret") }
	gone := func(context.Context, GetUser) (User, error) {
		return User{}, bites.NotFound("no such user").With("code", "u1")
	}
	srv := start(t, func(app *bites.App) {
		app.Use(setID)
		app.Get("/users/{id}", getUser)
		app.Get("/boom/{id}", boom)
		app.Get("/gone/{id}", gone)
		app.Post("/body", func(context.Context, Body) (User, error) { return User{}, nil })
		app.Get("/mw", getUserMw(), bites.Use(func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
			return bites.Problem(403, "not you"), nil
		}))
		app.Get("/mwerr/{id}", getUser, bites.Use(func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
			return bites.Response{}, bites.Unauthorized("sign in")
		}))
	})
	for _, tc := range []struct {
		name string
		r    bitetest.Request
		want string
	}{
		{"404", req("GET", "/nope"), `{"title":"Not Found","status":404,"request_id":"req-1"}`},
		{"405", req("POST", "/users/1"), `{"title":"Method Not Allowed","status":405,"request_id":"req-1"}`},
		{"the 400 listing errors", req("GET", "/users/x"), `{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer","rule":"type"}],"request_id":"req-1"}`},
		{"a 400 for a path that is not normalised", req("GET", "//x"), `{"title":"Bad Request","status":400,"detail":"the path is not normalised","request_id":"req-1"}`},
		{"a handler's plain error", req("GET", "/boom/1"), `{"title":"Internal Server Error","status":500,"request_id":"req-1"}`},
		{"a StatusError with a member", req("GET", "/gone/1"), `{"title":"Not Found","status":404,"detail":"no such user","code":"u1","request_id":"req-1"}`},
		{"a Problem from a middleware", req("GET", "/mw"), `{"title":"Forbidden","status":403,"detail":"not you","request_id":"req-1"}`},
		{"an error from a middleware", req("GET", "/mwerr/1"), `{"title":"Unauthorized","status":401,"detail":"sign in","request_id":"req-1"}`},
	} {
		tc.r.Header = map[string][]string{"X-N": {"1"}}
		r := srv.Do(tc.r)
		if r.Get("content-type") != "application/problem+json" || r.Text() != tc.want {
			t.Errorf("%s: got %d %s %s\n want %s", tc.name, r.Status, r.Get("content-type"), r.Text(), tc.want)
		}
	}
	tr := srv.Do(bitetest.Request{Method: "POST", Target: "/body", Body: []byte(`{"name":`), Header: map[string][]string{"X-N": {"2"}, "Content-Type": {"application/json"}}})
	if !strings.HasSuffix(tr.Text(), `,"request_id":"req-2"}`) {
		t.Errorf("a malformed body: %s", tr.Text())
	}
}

func getUserMw() func(context.Context, struct{}) (User, error) {
	return func(context.Context, struct{}) (User, error) { return User{}, nil }
}

func TestAnAnswerWithoutARequestIDHasNoRequestIDMember(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			return next.Call(ctx), nil
		})
	})
	wantProblem(t, srv.Do(req("GET", "/nope")), 404, `{"title":"Not Found","status":404}`)
}

func TestAMiddlewarePanicIsA500ProblemJSONWithTheRequestIDAndTheServerKeepsServing(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/ok/{id}", getUser)
		app.Get("/panic/{id}", getUser, bites.Use(func(_ context.Context, req bites.Request, _ bites.Next) (bites.Response, error) {
			req.SetRequestID("abc")
			panic("middleware broke")
		}))
	})
	r := srv.Do(req("GET", "/panic/1"))
	wantProblem(t, r, 500, `{"title":"Internal Server Error","status":500,"request_id":"abc"}`)
	if !strings.Contains(logs.String(), "middleware broke") {
		t.Errorf("the panic is not logged: %s", logs.String())
	}
	if r := srv.Do(req("GET", "/ok/1")); r.Status != 200 {
		t.Fatalf("after a panic: %d", r.Status)
	}
	if r := srv.Do(req("GET", "/ok/1")); r.Status != 200 {
		t.Fatalf("the pooled state of the panicked request is not reused wrongly: %d", r.Status)
	}
}

func TestAnErrorFromAnInnerMiddlewareReachesTheOuterOneAsAResponse(t *testing.T) {
	var got int
	outer := func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
		res := next.Call(ctx)
		got = res.Status()
		res.AddHeader("X-Seen", "yes")
		return res, nil
	}
	inner := func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
		return bites.Response{}, bites.Forbidden("closed")
	}
	srv := start(t, func(app *bites.App) { app.Get("/x/{id}", getUser, bites.Use(outer, inner)) })
	r := srv.Do(req("GET", "/x/1"))
	wantProblem(t, r, 403, `{"title":"Forbidden","status":403,"detail":"closed"}`)
	if got != 403 || r.Get("x-seen") != "yes" {
		t.Fatalf("outer saw %d, headers %v", got, r.Header)
	}
}

func TestAMiddlewarePassesAValueInwardByWrappingTheContext(t *testing.T) {
	type key struct{}
	srv := start(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			return next.Call(context.WithValue(ctx, key{}, "tenant-7")), nil
		})
		app.Get("/t/{id}", func(ctx context.Context, in GetUser) (User, error) {
			return User{Name: ctx.Value(key{}).(string)}, nil
		})
	})
	if r := srv.Do(req("GET", "/t/1")); !strings.Contains(r.Text(), `"name":"tenant-7"`) {
		t.Fatalf("%s", r.Text())
	}
}

func TestTheRequestViewHasTheMethodPathQueryHostHeadersAndRemoteAddress(t *testing.T) {
	var got string
	srv := start(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, r bites.Request, next bites.Next) (bites.Response, error) {
			got = fmt.Sprintf("%s|%s|%s|%s|%s|%q|%v", r.Method(), r.Path(), r.RawQuery(), r.Host(), r.Header("X-Tok"), r.Header("absent"), r.RemoteAddr())
			return next.Call(ctx), nil
		})
		app.Post("/a%20b/{id}", ok[GetUser])
	})
	srv.Do(bitetest.Request{Method: "POST", Target: "/a%20b/5?x=%41&y", Header: map[string][]string{"x-tok": {"  tok en  "}}, Remote: "198.51.100.4:5555"})
	want := `POST|/a%20b/5|x=%41&y|test|tok en|""|198.51.100.4:5555`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestAMiddlewareThatReturnsTheZeroResponseAndNoErrorIsA500(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/x/{id}", getUser, bites.Use(func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
			return bites.Response{}, nil
		}))
	})
	wantProblem(t, srv.Do(req("GET", "/x/1")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "zero Response") {
		t.Errorf("not logged: %s", logs.String())
	}
}

func TestARequestAMiddlewareKeptCannotSeeAnotherRequestsBytes(t *testing.T) {
	var mu sync.Mutex
	var kept []bites.Request
	var keptNext []bites.Next
	var wantAuth []string
	keep := func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		mu.Lock()
		kept = append(kept, req)
		keptNext = append(keptNext, next)
		wantAuth = append(wantAuth, req.Header("authorization"))
		mu.Unlock()
		return next.Call(ctx), nil
	}
	srv := start(t, func(app *bites.App) { app.Use(keep); app.Get("/users/{id}", getUser) })
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	for i := range 300 {
		auth := fmt.Sprintf("Bearer %s", strings.Repeat(string(rune('A'+i%26)), 20+i%7))
		c.Write([]byte(fmt.Sprintf("GET /users/%d?n=%d HTTP/1.1\r\nHost: h%d\r\nAuthorization: %s\r\n\r\n", i, i, i, auth)))
		if r := srv.Read(c, false); r.Status != 200 {
			t.Fatalf("request %d: %d", i, r.Status)
		}
		runtime.GC()
	}
	mu.Lock()
	mu.Unlock()
	if len(kept) != 300 {
		t.Fatalf("kept %d", len(kept))
	}
	for i, r := range kept {
		auth := fmt.Sprintf("Bearer %s", strings.Repeat(string(rune('A'+i%26)), 20+i%7))
		if r.Header("authorization") != auth || wantAuth[i] != auth || r.Path() != fmt.Sprintf("/users/%d", i) || r.RawQuery() != fmt.Sprintf("n=%d", i) || r.Host() != fmt.Sprintf("h%d", i) {
			t.Fatalf("request %d, read later: %q %q %q %q", i, r.Header("authorization"), r.Path(), r.RawQuery(), r.Host())
		}
		// The parts that reach the live request are inert once it is answered.
		r.SetRequestID("late")
		if r.RequestID() != "" || r.RemoteAddr() != (netip.AddrPort{}) {
			t.Fatalf("request %d: a Request kept past its request reads %q %v", i, r.RequestID(), r.RemoteAddr())
		}
	}
	// A kept Next does not run another request's chain: it answers a 500 Response and nothing else.
	for _, n := range keptNext[:50] {
		if res := n.Call(context.Background()); res.Status() != 500 {
			t.Fatalf("a kept Next ran: %d", res.Status())
		}
	}
	if r := srv.Do(req("GET", "/users/1")); r.Status != 200 {
		t.Fatalf("after stale calls: %d", r.Status)
	}
	var zero bites.Next
	if res := zero.Call(context.Background()); res.Status() != 500 {
		t.Fatalf("the zero Next: %d", res.Status())
	}
}

func TestAMiddlewareThatReturnsAResponseKeptFromAnotherRequestGetsA500(t *testing.T) {
	var mu sync.Mutex
	var first bites.Response
	have := false
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, _ bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			mu.Lock()
			defer mu.Unlock()
			if !have {
				first, have = res, true
				return res, nil
			}
			return first, nil
		})
		app.Get("/users/{id}", getUser)
	})
	if r := srv.Do(req("GET", "/users/1")); r.Status != 200 {
		t.Fatal(r.Status)
	}
	wantProblem(t, srv.Do(req("GET", "/users/2")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "kept from another request") {
		t.Errorf("not logged: %s", logs.String())
	}
}

func TestAMiddlewareOnOneRouteDoesNotTouchTheOthers(t *testing.T) {
	var tr trail
	srv := start(t, func(app *bites.App) {
		app.Get("/a/{id}", getUser, bites.Use(tr.tag("a")))
		app.Get("/b/{id}", getUser)
	})
	srv.Do(req("GET", "/b/1"))
	if tr.String() != "" {
		t.Fatalf("ran for the other route: %s", tr.String())
	}
	srv.Do(req("GET", "/a/1"))
	if tr.String() != "a> <a" {
		t.Fatalf("%s", tr.String())
	}
}

func TestMiddlewareSeesABodyRouteAndTheHandlerStillReadsItsBody(t *testing.T) {
	var tr trail
	srv := start(t, func(app *bites.App) {
		app.Use(tr.tag("m"))
		app.Post("/echo", echo[struct{ Body struct{ Name string } }])
	})
	r := srv.Do(jsonReq("POST", "/echo", `{"name":"Ada"}`))
	if r.Status != 200 || !strings.Contains(r.Text(), `"Ada"`) || tr.String() != "m> <m" {
		t.Fatalf("%d %s %s", r.Status, r.Text(), tr.String())
	}
}

func TestAppUseAfterTheAppStartedServingPanicsNamingWhereItWasCalled(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/a/{id}", getUser)
	srv := bitetest.Start(t, app)
	srv.Do(req("GET", "/a/1"))
	msg := recoverMessage(func() { app.Use(passthru) })
	if !strings.HasPrefix(msg, "app: Use registered at middleware_test.go:") || !strings.HasSuffix(msg, "after the app started serving: middleware can be added only before it does") {
		t.Fatalf("%q", msg)
	}
}

func TestHeadersYieldsEveryFieldAsReceivedAndAFieldSentTwiceTwice(t *testing.T) {
	var got []string
	app := bites.New()
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		for k, v := range req.Headers() {
			got = append(got, k+"="+v)
		}
		return next.Call(ctx), nil
	})
	app.Get("/x", func(context.Context, Empty) (bites.NoContent, error) { return bites.NoContent{}, nil })
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	c.Write([]byte("GET /x HTTP/1.1\r\nHost: test\r\nX-Tag: a\r\nx-tag:   b \r\n\r\n"))
	srv.Read(c, false)
	want := "Host=test X-Tag=a x-tag=b"
	if s := strings.Join(got, " "); s != want {
		t.Fatalf("got %q, want %q", s, want)
	}
}

// permissions is the permission-table case of ADR 0048: default-deny from the route's identity.
func permissions(table map[string]string, exempt map[string]bool, audit *trail) func(bites.RouteInfo) (bites.Middleware, error) {
	return func(r bites.RouteInfo) (bites.Middleware, error) {
		if exempt[r.OperationID] {
			return nil, nil
		}
		perm, ok := table[r.OperationID]
		if !ok {
			return nil, fmt.Errorf("operation %q has no permission in the table", r.OperationID)
		}
		id := r.OperationID
		return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			if req.Header("X-Perm") != perm {
				audit.add(id + " denied")
				return bites.Response{}, bites.Forbidden("missing " + perm)
			}
			res := next.Call(ctx)
			audit.add(fmt.Sprintf("%s %d", id, res.Status()))
			return res, nil
		}, nil
	}
}

func TestUseEachBuildsADefaultDenyMiddlewareFromTheOperationIDAndAuditsTheStatus(t *testing.T) {
	var audit trail
	srv := start(t, func(app *bites.App) {
		app.UseEach(permissions(
			map[string]string{"deals.list": "deals:read", "getUsersById": "users:read"},
			map[string]bool{"health.check": true}, &audit))
		app.Get("/deals", getUserMw(), bites.OperationID("deals.list"))
		app.Get("/users/{id}", getUser)
		app.Get("/healthz", getUserMw(), bites.OperationID("health.check"))
	})
	if r := srv.Do(req("GET", "/deals")); r.Status != 403 {
		t.Fatalf("no permission header: %d %s", r.Status, r.Text())
	}
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/deals", Header: map[string][]string{"X-Perm": {"deals:read"}}})
	if r.Status != 200 {
		t.Fatalf("with the permission: %d %s", r.Status, r.Text())
	}
	srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"X-Perm": {"users:read"}}})
	if r := srv.Do(req("GET", "/healthz")); r.Status != 200 {
		t.Fatalf("an exempt operation is served with no header: %d", r.Status)
	}
	if got, want := audit.String(), "deals.list denied deals.list 200 getUsersById 200"; got != want {
		t.Fatalf("audit:\n got  %s\n want %s", got, want)
	}
}

func TestUseEachRefusesARouteTheFunctionRefusesAndNamesTheRouteAndBothLines(t *testing.T) {
	var audit trail
	got := recoverMessage(func() {
		app := bites.New(bites.WithClock(bitetest.NewClock()))
		app.UseEach(permissions(map[string]string{}, nil, &audit))
		app.Get("/orphan", getUserMw())
	})
	for _, w := range []string{`GET /orphan: the function given to UseEach (at middleware_test.go:`, `refused the route: operation "getOrphan" has no permission in the table`, "registered at middleware_test.go:"} {
		if !strings.Contains(got, w) {
			t.Errorf("message lacks %q:\n%s", w, got)
		}
	}
}

func TestUseEachIsToldTheEffectiveIdentityOfEveryKindOfRoute(t *testing.T) {
	var seen []bites.RouteInfo
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) { seen = append(seen, r); return nil, nil })
	g := app.Group("/api", bites.Tag("shop"))
	g.Get("/users/{id}", getUser, bites.Tag("people"))
	g.Post("/orders", getUserMw(), bites.OperationID("orders.create"))
	g.Get("/hidden", getUserMw(), bites.Undocumented())
	g.Static("/assets", bites.Static{FS: staticEmbed})
	g.WebSocket("/rooms", wsHandler[Empty, wsOut, wsMsg])
	want := []bites.RouteInfo{
		{Method: "GET", Pattern: "/api/users/{id}", OperationID: "getApiUsersById", Tags: []string{"shop", "people"}},
		{Method: "POST", Pattern: "/api/orders", OperationID: "orders.create", Tags: []string{"shop"}},
		{Method: "GET", Pattern: "/api/hidden", Tags: []string{"shop"}},
		{Method: "GET", Pattern: "/api/assets/{path...}", Tags: []string{"shop"}, Static: true},
		{Method: "GET", Pattern: "/api/rooms", OperationID: "getApiRooms", Tags: []string{"shop"}, WebSocket: true},
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("RouteInfo:\n got  %+v\n want %+v", seen, want)
	}
}

func TestUseEachMiddlewareRunsInsideEveryUseAndAppThenGroupThenRouteInOrder(t *testing.T) {
	var tr trail
	each := func(name string) func(bites.RouteInfo) (bites.Middleware, error) {
		return func(bites.RouteInfo) (bites.Middleware, error) { return tr.tag(name), nil }
	}
	srv := start(t, func(app *bites.App) {
		app.Use(tr.tag("app"))
		app.UseEach(each("eachApp"))
		g := app.Group("/g")
		g.Use(tr.tag("use"))
		g.UseEach(each("eachGroup"))
		g.Get("/x", getUserMw(), bites.Use(tr.tag("route")), bites.UseEach(each("eachRoute")))
	})
	if r := srv.Do(req("GET", "/g/x")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	want := "app> use> route> eachApp> eachGroup> eachRoute> <eachRoute <eachGroup <eachApp <route <use <app"
	if got := tr.String(); got != want {
		t.Fatalf("order:\n got  %s\n want %s", got, want)
	}
}

func TestUseEachReturningNilLeavesARouteWithNoMiddlewareAndServedDirectly(t *testing.T) {
	var tr trail
	srv := start(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
			if r.Pattern == "/open" {
				return nil, nil
			}
			return tr.tag(r.Pattern), nil
		})
		app.Get("/open", getUserMw())
		app.Get("/closed", getUserMw())
	})
	srv.Do(req("GET", "/open"))
	if tr.String() != "" {
		t.Fatalf("a nil Middleware ran something: %s", tr.String())
	}
	srv.Do(req("GET", "/closed"))
	if tr.String() != "/closed> </closed" {
		t.Fatalf("got %q", tr.String())
	}
}

// guardBuiltins is the default-deny shape of ADR 0048 for the routes the app registers for
// itself: a route the application wrote needs the X-Perm header, and a built-in one is named.
func guardBuiltins(exempt ...string) func(bites.RouteInfo) (bites.Middleware, error) {
	return func(r bites.RouteInfo) (bites.Middleware, error) {
		if r.Builtin != "" {
			if !slices.Contains(exempt, r.Builtin) {
				return nil, fmt.Errorf("built-in route %q is not exempted", r.Builtin)
			}
			return nil, nil
		}
		return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			if req.Header("X-Perm") == "" {
				return bites.Response{}, bites.Unauthorized("sign in")
			}
			return next.Call(ctx), nil
		}, nil
	}
}

func TestUseEachIsToldAboutTheDocumentAndTheMetricsPageWithTheirBuiltinName(t *testing.T) {
	var seen []bites.RouteInfo
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) { seen = append(seen, r); return nil, nil })
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	app.ServeMetrics("/metrics", bites.NewMetrics())
	app.Get("/users/{id}", getUser)
	want := []bites.RouteInfo{
		{Method: "GET", Pattern: "/openapi.json", Builtin: "openapi"},
		{Method: "GET", Pattern: "/metrics", Builtin: "metrics"},
		{Method: "GET", Pattern: "/users/{id}", OperationID: "getUsersById"},
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("RouteInfo:\n got  %+v\n want %+v", seen, want)
	}
}

func TestADefaultDenyUseEachGuardsTheDocumentUntilTheGuardExemptsItByName(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.UseEach(guardBuiltins("metrics"))
		app.Get("/users/{id}", getUser)
		app.ServeMetrics("/metrics", bites.NewMetrics())
	})
	if r := srv.Do(req("GET", "/metrics")); r.Status != 200 {
		t.Fatalf("an exempted built-in route: %d", r.Status)
	}
	if r := srv.Do(req("GET", "/users/7")); r.Status != 401 {
		t.Fatalf("a route of the application with no credential: %d", r.Status)
	}

	srv = start(t, func(app *bites.App) {
		app.UseEach(guardBuiltins("openapi"))
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/users/{id}", getUser)
	})
	if r := srv.Do(req("GET", "/openapi.json")); r.Status != 200 {
		t.Fatalf("the document, exempted by name: %d", r.Status)
	}
}

func TestAGuardThatDoesNotExemptTheDocumentIsAnsweredWith401BeforeTheDocumentIsSent(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
			return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
				if req.Header("X-Perm") == "" {
					return bites.Response{}, bites.Unauthorized("sign in")
				}
				return next.Call(ctx), nil
			}, nil
		})
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	})
	if r := srv.Do(req("GET", "/openapi.json")); r.Status != 401 {
		t.Fatalf("no credential: %d %s", r.Status, r.Text())
	}
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json", Header: map[string][]string{"X-Perm": {"x"}}})
	if r.Status != 200 || !strings.Contains(r.Text(), `"openapi"`) {
		t.Fatalf("with a credential: %d %s", r.Status, r.Text())
	}
}

func TestAnAppWithNoUseEachServesTheDocumentAndTheMetricsPageAsBefore(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.ServeMetrics("/metrics", bites.NewMetrics())
	})
	for _, p := range []string{"/openapi.json", "/metrics"} {
		if r := srv.Do(req("GET", p)); r.Status != 200 {
			t.Fatalf("%s: %d", p, r.Status)
		}
	}
}

func TestTheHealthPathsAreNotGuardedByUseEachSoALoadBalancerNeedsNoCredentials(t *testing.T) {
	var seen []string
	srv := startWith(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
			seen = append(seen, r.Pattern)
			return func(context.Context, bites.Request, bites.Next) (bites.Response, error) {
				return bites.Response{}, bites.Unauthorized("sign in")
			}, nil
		})
		app.Get("/users/{id}", getUser)
	}, bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}))
	for _, p := range []string{"/livez", "/readyz"} {
		if r := srv.Do(req("GET", p)); r.Status != 200 {
			t.Fatalf("%s: %d", p, r.Status)
		}
	}
	if fmt.Sprint(seen) != "[/users/{id}]" {
		t.Fatalf("UseEach was told about %v", seen)
	}
}

type ctxAccount struct{ Name string }
type ctxAccountKey struct{}

type MeInput struct{ Account ctxAccount }

func TestAGuardOnEveryRouteHandsTheUserItProvedToAProviderThroughTheContextItPassesOn(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
			if r.OperationID == "login" || r.Builtin == bites.BuiltinOpenAPI {
				return nil, nil
			}
			return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
				name := req.Header("X-User")
				if name == "" {
					return bites.Response{}, bites.Unauthorized("not signed in")
				}
				return next.Call(context.WithValue(ctx, ctxAccountKey{}, ctxAccount{Name: name})), nil
			}, nil
		})
		app.Provide(func(ctx context.Context, _ bites.Request, _ struct{}) (ctxAccount, error) {
			a, ok := ctx.Value(ctxAccountKey{}).(ctxAccount)
			if !ok {
				return ctxAccount{}, bites.Unauthorized("not signed in")
			}
			return a, nil
		})
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/login", func(context.Context, Empty) (bites.NoContent, error) { return bites.NoContent{}, nil }, bites.OperationID("login"))
		app.Get("/me", func(_ context.Context, in MeInput) (ctxAccount, error) { return in.Account, nil })
		app.Get("/ping", func(context.Context, Empty) (bites.NoContent, error) { return bites.NoContent{}, nil })
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"X-User": {"ada"}}}); r.Status != 200 || r.Text() != `{"name":"ada"}` {
		t.Fatalf("the Provider did not see the user the guard proved: %d %s", r.Status, r.Text())
	}
	for _, p := range []string{"/me", "/ping"} {
		if r := srv.Do(req("GET", p)); r.Status != 401 {
			t.Fatalf("%s with no session: %d", p, r.Status)
		}
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/ping", Header: map[string][]string{"X-User": {"ada"}}}); r.Status != 204 {
		t.Fatalf("a route that asks for no user: %d", r.Status)
	}
	for _, c := range []struct{ method, path string }{{"POST", "/login"}, {"GET", "/openapi.json"}} {
		if r := srv.Do(req(c.method, c.path)); r.Status >= 300 {
			t.Fatalf("public %s %s: %d", c.method, c.path, r.Status)
		}
	}
}

// cookieProbe sends a request whose head carries the given raw Cookie header lines (several
// fields when several lines) and returns the Request a middleware saw, kept past the call.
func cookieProbe(t *testing.T, lines ...string) bites.Request {
	t.Helper()
	var kept bites.Request
	srv := start(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, r bites.Request, next bites.Next) (bites.Response, error) {
			kept = r
			return next.Call(ctx), nil
		})
		app.Get("/c", ok[Empty])
	})
	head := "GET /c HTTP/1.1\r\nHost: test\r\n"
	for _, l := range lines {
		head += "Cookie: " + l + "\r\n"
	}
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	if _, err := c.Write([]byte(head + "\r\n")); err != nil {
		t.Fatal(err)
	}
	if r := srv.Read(c, false); r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	return kept
}

func TestRequestCookieReturnsTheFirstCookieOfThatNameWithItsQuotesRemoved(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		cookie string
		want   string
		wantOK bool
	}{
		{"one cookie", []string{"id=7"}, "id", "7", true},
		{"several in one field", []string{"a=1; id=7; b=2"}, "id", "7", true},
		{"several fields, the second holds it", []string{"a=1", "id=7"}, "id", "7", true},
		{"a duplicate in one field, the first wins", []string{"id=1; id=2"}, "id", "1", true},
		{"a duplicate across fields, the first field wins", []string{"id=1", "id=2"}, "id", "1", true},
		{"a quoted value loses its quotes", []string{`id="a b"`}, "id", "a b", true},
		{"an empty quoted value", []string{`id=""`}, "id", "", true},
		{"a lone quote stays", []string{`id="`}, "id", `"`, true},
		{"an empty value is present", []string{"id=; b=1"}, "id", "", true},
		{"a value is not percent-decoded", []string{"id=a%20b"}, "id", "a%20b", true},
		{"a value may hold an equals sign", []string{"id=a=b"}, "id", "a=b", true},
		{"an absent name", []string{"a=1; b=2"}, "id", "", false},
		{"an absent name with no Cookie field at all", nil, "id", "", false},
		{"an empty name", []string{"a=1; =2"}, "", "", false},
		{"a name that is a prefix of another", []string{"idx=9; id=7"}, "id", "7", true},
		{"a name that is longer than the one sent", []string{"id=7"}, "idx", "", false},
		{"a name is case-sensitive", []string{"ID=1; Id=2; id=3"}, "id", "3", true},
		{"a malformed pair is skipped", []string{"junk; =x; id=7"}, "id", "7", true},
		{"a pair with an empty name is not a cookie", []string{"=x"}, "", "", false},
		{"white space around a pair is trimmed", []string{"  a=1 ;\tid=7  ;b=2"}, "id", "7", true},
		{"the field name is matched without regard to case", []string{"id=7"}, "id", "7", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := cookieProbe(t, tc.lines...)
			if got, ok := r.Cookie(tc.cookie); got != tc.want || ok != tc.wantOK {
				t.Fatalf("Cookie(%q) = %q, %v; want %q, %v", tc.cookie, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestRequestCookieAllocatesNothingAndWorksOnAKeptRequest(t *testing.T) {
	r := cookieProbe(t, `a=1; b="two"`, "session=abc123; c=3")
	var v string
	var found bool
	if n := testing.AllocsPerRun(1000, func() {
		v, found = r.Cookie("session")
		_, _ = r.Cookie("absent")
		_, _ = r.Cookie("b")
	}); n != 0 {
		t.Fatalf("Cookie allocated %v times", n)
	}
	if !found || v != "abc123" {
		t.Fatalf("a kept Request read %q, %v", v, found)
	}
	if b, _ := r.Cookie("b"); b != "two" {
		t.Fatalf("b = %q", b)
	}
}

func TestRequestCookieAgreesWithTheCookieSectionOnTheSameHeader(t *testing.T) {
	type in struct {
		Cookie struct {
			Id    bites.Opt[string]
			Theme bites.Opt[string]
			Q     bites.Opt[string]
			Empty bites.Opt[string]
		}
	}
	headers := []string{
		`theme="dark"; id=s1; other=1`,
		`junk; =x; id=7 ;  theme=light;q="a b"`,
		"id=; theme=\"\"; empty=",
		`q="; id=a=b`,
	}
	for _, h := range headers {
		var viaView = map[string]string{}
		srv := start(t, func(app *bites.App) {
			app.Use(func(ctx context.Context, r bites.Request, next bites.Next) (bites.Response, error) {
				for _, n := range []string{"id", "theme", "q", "empty"} {
					if v, ok := r.Cookie(n); ok {
						viaView[n] = v
					}
				}
				return next.Call(ctx), nil
			})
			app.Get("/c", echo[in])
		})
		resp := srv.Do(bitetest.Request{Method: "GET", Target: "/c", Header: map[string][]string{"Cookie": {h}}})
		var got struct {
			Cookie map[string]string `json:"cookie"`
		}
		if err := resp.JSON(&got); err != nil {
			t.Fatalf("%q: %v: %s", h, err, resp.Text())
		}
		if !maps.Equal(got.Cookie, viaView) {
			t.Errorf("%q: section %v, view %v", h, got.Cookie, viaView)
		}
	}
}
