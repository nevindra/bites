package bites_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/stdhttp"
)

func TestAnUnknownPathIsA404AsProblemJSON(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	wantProblem(t, srv.Do(req("GET", "/nope")), 404, `{"title":"Not Found","status":404}`)
}

func TestAPathThatExistsForOtherMethodsIsA405WithAllow(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/users/{id}", getUser)
		app.Delete("/users/{id}", func(context.Context, GetUser) (bites.NoContent, error) { return bites.NoContent{}, nil })
	})
	r := srv.Do(req("POST", "/users/1"))
	wantProblem(t, r, 405, `{"title":"Method Not Allowed","status":405}`)
	if got := r.Get("allow"); got != "DELETE, GET, HEAD" {
		t.Fatalf("Allow: %q", got)
	}
}

func TestAPathThatIsNotNormalisedIsA400AndIsNeverRedirected(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	wantProblem(t, srv.Do(req("GET", "/users//1")), 400, `{"title":"Bad Request","status":400,"detail":"the path is not normalised"}`)
	wantProblem(t, srv.Do(req("GET", "/users/1/")), 404, `{"title":"Not Found","status":404}`)
}

func TestAGETRouteAnswersHEADWithTheHeadersAndNoBody(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	r := srv.Do(req("HEAD", "/users/1"))
	if r.Status != 200 || len(r.Body) != 0 || r.Get("content-length") == "" || r.Get("content-length") == "0" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
}

func TestOPTIONSStarListsTheMethodsTheAppServes(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/a/{id}", getUser)
		app.Post("/b", ok[none])
	})
	c := srv.Dial("203.0.113.9:1")
	c.Write([]byte("OPTIONS * HTTP/1.1\r\nHost: x\r\n\r\n"))
	r := srv.Read(c, false)
	if r.Status != 204 || r.Get("allow") != "GET, HEAD, OPTIONS, POST" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestAGroupPrefixesItsRoutesAndNests(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		admin := app.Group("/admin")
		admin.Get("/users/{id}", getUser)
		admin.Group("/v2").Get("/users/{id}", getUser)
	})
	for _, p := range []string{"/admin/users/1", "/admin/v2/users/1"} {
		if r := srv.Do(req("GET", p)); r.Status != 200 {
			t.Errorf("%s: %d %s", p, r.Status, r.Text())
		}
	}
	if r := srv.Do(req("GET", "/users/1")); r.Status != 404 {
		t.Errorf("the unprefixed path: %d", r.Status)
	}
}

func TestAMethodValueIsARoute(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", (&usersService{prefix: "svc-"}).Get) })
	if r := srv.Do(req("GET", "/users/9")); r.Text() != `"svc-9"` {
		t.Fatalf("%s", r.Text())
	}
}

type usersService struct{ prefix string }

func (u *usersService) Get(_ context.Context, in GetUser) (string, error) {
	return u.prefix + strings.TrimSpace(itoa(in.Path.ID)), nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestRegisteringARouteAfterTheAppStartedServingPanics(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/a/{id}", getUser)
	srv := bitetest.Start(t, app)
	srv.Do(req("GET", "/a/1"))
	msg := recoverMessage(func() { app.Get("/b", ok[none]) })
	if !strings.HasPrefix(msg, "route GET /b registered at routing_test.go:") || !strings.HasSuffix(msg, "after the app started serving: routes can be added only before it does") {
		t.Fatalf("%q", msg)
	}
}

func TestNewReadsNoGlobalStateTwoAppsAreIndependent(t *testing.T) {
	a := start(t, func(app *bites.App) { app.Get("/only-a", ok[none]) })
	b := start(t, func(app *bites.App) { app.Get("/only-b", ok[none]) })
	if a.Do(req("GET", "/only-b")).Status != 404 || b.Do(req("GET", "/only-a")).Status != 404 {
		t.Fatal("routes leaked between apps")
	}
}

func TestABodyThatIsSentTooSlowlyIsA408WhenTheFakeClockPassesTheRoutesDeadline(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Post("/c", echo[Create], bites.BodyTimeout(2*time.Second))
	srv := bitetest.Start(t, app)
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "POST /c HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 40\r\n\r\n{")
	deadline := clk.Now().Add(2 * time.Second)
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if n, ok := clk.Next(); ok && n.Equal(deadline) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the route never armed its body deadline")
		}
	}
	clk.Advance(3 * time.Second)
	r := srv.Read(c, false)
	wantProblem(t, r, 408, `{"title":"Request Timeout","status":408,"detail":"request body too slow"}`)
}

func TestShutdownStopsServeWithErrServerClosed(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/a/{id}", getUser)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Serve(ln) }()
	var conn net.Conn
	for range 100 {
		if conn, err = net.Dial("tcp", ln.Addr().String()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Fprint(conn, "GET /a/5 HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 200 OK") {
		t.Fatalf("%s", buf[:n])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, bites.ErrServerClosed) {
		t.Fatalf("Serve returned %v", err)
	}
}

const smallBody = `{"name":"Ada","bio":null,"items":[]}`

func postStatus(srv *bitetest.Server, path, body string) int {
	return srv.Do(jsonReq("POST", path, body)).Status
}

func TestAGroupOptionAppliesToEveryRouteInTheGroupAndItsNestedGroups(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.MaxBody(10), bites.AcceptUnknownMembers())
		g.Post("/a", echo[Create])
		g.Group("/n").Post("/b", echo[Create])
		app.Post("/outside", echo[Create])
	})
	for _, p := range []string{"/g/a", "/g/n/b"} {
		wantProblem(t, srv.Do(jsonReq("POST", p, smallBody)), 413, `{"title":"Content Too Large","status":413,"detail":"request body too large: this route reads at most 10 bytes"}`)
	}
	if got := postStatus(srv, "/outside", smallBody); got != 200 {
		t.Errorf("a route outside the group: %d", got)
	}
}

func TestABodyOverAGroupsMaxBodyIsA413AndAnAcceptUnknownMembersOptionReachesItsRoutes(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Group("/g", bites.MaxBody(100), bites.AcceptUnknownMembers()).Post("/a", echo[Create])
		app.Post("/strict", echo[Create])
	})
	extra := `{"name":"Ada","bio":null,"items":[],"zzz":1}`
	if got := postStatus(srv, "/g/a", extra); got != 200 {
		t.Errorf("an unknown member in the group: %d", got)
	}
	if got := postStatus(srv, "/strict", extra); got != 400 {
		t.Errorf("an unknown member outside the group: %d", got)
	}
	if got := postStatus(srv, "/g/a", strings.Repeat(" ", 200)+smallBody); got != 413 {
		t.Errorf("a body over the group's MaxBody: %d", got)
	}
}

func TestARoutesOwnOptionBeatsItsGroups(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.MaxBody(10))
		g.Post("/tight", echo[Create])
		g.Post("/wide", echo[Create], bites.MaxBody(1000))
	})
	if got := postStatus(srv, "/g/tight", smallBody); got != 413 {
		t.Errorf("the group's limit: %d", got)
	}
	if got := postStatus(srv, "/g/wide", smallBody); got != 200 {
		t.Errorf("the route's own limit: %d", got)
	}
}

func TestANestedGroupsOptionBeatsItsParents(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.MaxBody(10))
		g.Post("/a", echo[Create])
		g.Group("/wide", bites.MaxBody(1000)).Post("/b", echo[Create])
		g.Group("/inherit").Post("/c", echo[Create])
	})
	for path, want := range map[string]int{"/g/a": 413, "/g/wide/b": 200, "/g/inherit/c": 413} {
		if got := postStatus(srv, path, smallBody); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
}

func TestTheStartUpLogNamesEveryLimitAndMarksTheOnesTheUserSet(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) { app.Get("/a/{id}", getUser) },
		bites.WithLimits(bites.Limits{MaxBody: 2048, IdleTimeout: 5 * time.Second, MaxConns: 77, HandshakeTimeout: 3 * time.Second}))
	srv.Do(req("GET", "/a/1")) // Serve runs in a goroutine; an answered request means it has started
	log := lb.String()
	for _, want := range []string{
		`setting="max head bytes" value=8192 source=default`,
		`setting="max header fields" value=100 source=default`,
		`setting="max body bytes" value=2048 source=custom`,
		`setting="head timeout" value=10s source=default`,
		`setting="body timeout" value=30s source=default`,
		`setting="idle timeout" value=5s source=custom`,
		`setting="write timeout" value=30s source=default`,
		`setting="tls handshake timeout" value=3s source=custom`,
		`setting="max connections" value=77 source=custom`,
		`setting="json depth" value=64 source=default`,
		`setting="errors listed in a 400" value=20 source=default`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
}

func TestTheStartUpLogShowsTheConnectionLimitComputedFromTheFileDescriptorLimit(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) { app.Get("/a/{id}", getUser) })
	srv.Do(req("GET", "/a/1"))
	if log := lb.String(); !strings.Contains(log, `setting="max connections" value=`) || !strings.Contains(log, "source=default") ||
		strings.Contains(log, `setting="max connections" value=0 `) {
		t.Errorf("no effective connection limit in the log:\n%s", log)
	}
}

func TestTheStartUpLogShowsTheTLSHandshakeTimeoutAsADefaultOf10s(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) { app.Get("/a/{id}", getUser) })
	srv.Do(req("GET", "/a/1"))
	if log := lb.String(); !strings.Contains(log, `setting="tls handshake timeout" value=10s source=default`) {
		t.Errorf("no handshake timeout in the log:\n%s", log)
	}
}

func TestAZeroLimitTakesItsDefault(t *testing.T) {
	srv, lb := startLogged(t, func(app *bites.App) { app.Get("/a/{id}", getUser) },
		bites.WithLimits(bites.Limits{MaxBody: 0, IdleTimeout: 0, MaxConns: 0, HandshakeTimeout: 0}))
	srv.Do(req("GET", "/a/1"))
	log := lb.String()
	for _, want := range []string{
		`setting="max body bytes" value=1048576 source=default`,
		`setting="idle timeout" value=1m0s source=default`,
		`setting="tls handshake timeout" value=10s source=default`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, `setting="max connections" value=0 `) {
		t.Errorf("a zero MaxConns stayed zero:\n%s", log)
	}
}

// ---- Options bundles route options (ADR 0024) ----

func op(id, summary string) bites.RouteOption {
	return bites.Options(bites.OperationID(id), bites.Summary(summary))
}

func TestAnApplicationCanBundleRouteOptionsAndKeepGivingOthersAfterThem(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/a", ok[Empty], op("listA", "List the As"), bites.Tag("as"), bites.Description("d"))
	})
	got := dig(doc, "paths", "/a", "get")
	if dig(got, "operationId") != "listA" || dig(got, "summary") != "List the As" || dig(got, "description") != "d" || jsonOf(dig(got, "tags")) != `["as"]` {
		t.Fatalf("%s", jsonOf(got))
	}
}

func TestOptionsGivenToAGroupApplyToEveryRouteInTheOrderTheyAreListed(t *testing.T) {
	seen := ""
	mark := func(s string) bites.Middleware {
		return func(ctx context.Context, r bites.Request, next bites.Next) (bites.Response, error) {
			seen += s
			return next.Call(ctx), nil
		}
	}
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.Options(bites.Use(mark("1")), bites.Options(bites.Use(mark("2")), bites.Use(mark("3")))))
		g.Get("/x", ok[Empty], bites.Use(mark("4")))
		g.Get("/y", ok[Empty])
	})
	srv.Do(bitetest.Request{Method: "GET", Target: "/g/x"})
	if seen != "1234" {
		t.Fatalf("middleware ran %q, want 1234", seen)
	}
	seen = ""
	srv.Do(bitetest.Request{Method: "GET", Target: "/g/y"})
	if seen != "123" {
		t.Fatalf("middleware ran %q, want 123", seen)
	}
}

func TestALaterOptionBeatsTheOneBundledBeforeIt(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/x", func(context.Context, struct{ Body struct{ N string } }) (struct{}, error) { return struct{}{}, nil },
			bites.Options(bites.MaxBody(8)), bites.MaxBody(1<<20))
	})
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/x", Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"n":"0123456789"}`)})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestRoutesListsEveryRouteInTheOrderItWasRegisteredAsUseEachSeesIt(t *testing.T) {
	var seen []bites.RouteInfo
	var app *bites.App
	room := newRoom()
	start(t, func(a *bites.App) {
		app = a
		a.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) { seen = append(seen, r); return nil, nil })
		a.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
		a.ServeMetrics("/metrics", bites.NewMetrics())
		a.Get("/ping", ok[Empty], bites.OperationID("ping"))
		api := a.Group("/api", bites.Tag("api"))
		api.Get("/users", ok[Empty], bites.Tag("users"))
		a.WebSocket("/rooms/{id}", room.join)
		a.Static("/assets", bites.Static{FS: staticFS(t)})
		stdhttp.Mount(a, "/debug/", http.NotFoundHandler())
	})
	got := app.Routes()
	if len(got) != 6+len(stdhttp.Methods) || !reflect.DeepEqual(got, seen) {
		t.Fatalf("Routes() = %+v\nUseEach saw %+v", got, seen)
	}
	want := []string{"GET /openapi.json", "GET /metrics", "GET /ping", "GET /api/users", "GET /rooms/{id}", "GET /assets/{path...}"}
	for i, w := range want {
		if g := got[i].Method + " " + got[i].Pattern; g != w {
			t.Errorf("route %d is %q, want %q", i, g, w)
		}
	}
	if got[0].Builtin != bites.BuiltinOpenAPI || got[1].Builtin != bites.BuiltinMetrics || got[2].Builtin != "" {
		t.Errorf("Builtin of the first three: %q %q %q", got[0].Builtin, got[1].Builtin, got[2].Builtin)
	}
	if got[2].OperationID != "ping" || !slices.Equal(got[3].Tags, []string{"api", "users"}) || !got[4].WebSocket || got[4].OperationID != "getRoomsById" || !got[5].Static {
		t.Errorf("identity not carried: %+v %+v %+v %+v", got[2], got[3], got[4], got[5])
	}
	for _, r := range got[6:] {
		if r.Pattern != "/debug/" || r.OperationID != "" || r.Builtin != "" {
			t.Errorf("a stdhttp route is %+v", r)
		}
	}
}

func TestRoutesDoesNotListTheHealthPathsAndIsEmptyForAnAppWithNoRoute(t *testing.T) {
	if got := bites.New().Routes(); got == nil || len(got) != 0 {
		t.Errorf("an app with no route: %#v, want an empty slice", got)
	}
	app := bites.New(bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}))
	app.Get("/x", ok[Empty])
	if got := app.Routes(); len(got) != 1 || got[0].Pattern != "/x" {
		t.Errorf("Routes() = %+v, want only /x", got)
	}
}

func TestChangingWhatRoutesReturnsChangesNothingInTheAppOrASecondCall(t *testing.T) {
	app := bites.New()
	app.Get("/x", ok[Empty], bites.Tag("a", "b"))
	first := app.Routes()
	first[0].Tags[0] = "changed"
	first[0].Pattern = "/changed"
	first = append(first[:0], bites.RouteInfo{})
	second := app.Routes()
	if len(second) != 1 || second[0].Pattern != "/x" || !slices.Equal(second[0].Tags, []string{"a", "b"}) {
		t.Errorf("a second call returned %+v", second)
	}
	_ = first
}

func TestRoutesIsTheSameBeforeAndWhileServing(t *testing.T) {
	var app *bites.App
	var before []bites.RouteInfo
	start(t, func(a *bites.App) {
		app = a
		a.Get("/x", ok[Empty])
		before = a.Routes()
	})
	if after := app.Routes(); !reflect.DeepEqual(before, after) || len(after) != 1 {
		t.Errorf("before %+v, while serving %+v", before, after)
	}
}

func TestRoutesCalledFromAUseEachFunctionListsTheRoutesSoFarInsteadOfDeadlocking(t *testing.T) {
	app := bites.New()
	var seen [][]string
	app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) {
		var patterns []string
		for _, x := range app.Routes() {
			patterns = append(patterns, x.Pattern)
		}
		seen = append(seen, patterns)
		return nil, nil
	})
	app.Get("/a", ok[Empty])
	app.Get("/b", ok[Empty])
	want := [][]string{{"/a"}, {"/a", "/b"}}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("UseEach saw %q, want %q", seen, want)
	}
}

func TestRoutesMayBeCalledFromAnotherGoroutineWhileRoutesAreRegistered(t *testing.T) {
	app := bites.New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			for _, r := range app.Routes() {
				if r.Pattern == "" {
					t.Error("a route with no pattern")
					return
				}
			}
		}
	}()
	for i := range 200 {
		app.Get("/r"+strconv.Itoa(i), ok[Empty])
	}
	<-done
	if n := len(app.Routes()); n != 200 {
		t.Errorf("%d routes, want 200", n)
	}
}
