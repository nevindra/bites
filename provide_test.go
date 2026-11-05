package bites_test

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// The types a Provider fills in these tests. Session comes from a header; Caller and Org are built
// from the Session, so a route that asks for both runs the Session Provider once.
type (
	Session struct{ Token string }
	Caller  struct{ Name string }
	Org     struct{ Slug string }
)

func sessionFromHeader(n *atomic.Int32) func(context.Context, bites.Request, struct{}) (Session, error) {
	return func(_ context.Context, req bites.Request, _ struct{}) (Session, error) {
		if n != nil {
			n.Add(1)
		}
		tok := req.Header("authorization")
		if tok == "" {
			return Session{}, bites.Unauthorized("no session")
		}
		return Session{Token: tok}, nil
	}
}

func callerFromSession(_ context.Context, req bites.Request, deps struct{ Session Session }) (Caller, error) {
	req.AddLogAttrs(slog.String("caller", "user-"+deps.Session.Token))
	return Caller{Name: "user-" + deps.Session.Token}, nil
}

func orgFromSession(_ context.Context, _ bites.Request, deps struct{ S Session }) (Org, error) {
	return Org{Slug: "org-" + deps.S.Token}, nil
}

type Me struct {
	Caller Caller
	Query  struct{ Verbose bites.Opt[bool] }
}

type meOut struct {
	Name    string `json:"name"`
	Verbose bool   `json:"verbose"`
}

func me(_ context.Context, in Me) (meOut, error) {
	return meOut{Name: in.Caller.Name, Verbose: in.Query.Verbose.Or(false)}, nil
}

func withAuth(tok string) bitetest.Request {
	return bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Authorization": {tok}}}
}

func TestAFieldOfAProvidedTypeIsFilledByItsProviderAlongsideTheSections(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", me)
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/me?verbose=true", Header: map[string][]string{"Authorization": {"ada"}}})
	if r.Status != 200 || r.Text() != `{"name":"user-ada","verbose":true}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAProviderThatReturnsAnErrorAnswersWithItAndTheHandlerNeverRuns(t *testing.T) {
	var ran atomic.Bool
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", func(ctx context.Context, in Me) (meOut, error) { ran.Store(true); return me(ctx, in) })
	})
	wantProblem(t, srv.Do(req("GET", "/me")), 401, `{"title":"Unauthorized","status":401,"detail":"no session"}`)
	if ran.Load() {
		t.Fatal("the handler ran after its Provider refused the request")
	}
}

func TestAProviderRunsBeforeTheInputIsDecodedSoARejectedCallerIsNotToldTheBodyIsWrong(t *testing.T) {
	type Rename struct {
		Caller Caller
		Body   struct{ Name string }
	}
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Post("/me", echo[Rename])
	})
	r := srv.Do(jsonReq("POST", "/me", `{"name":`))
	if r.Status != 401 {
		t.Fatalf("a malformed body from a caller with no session: %d %s, want the Provider's 401", r.Status, r.Text())
	}
}

func TestEachProviderRunsOnceARequestEvenWhenTwoProvidersNeedItsType(t *testing.T) {
	type Both struct {
		Caller Caller
		Org    Org
	}
	var sessions atomic.Int32
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(&sessions))
		app.Provide(callerFromSession)
		app.Provide(orgFromSession)
		app.Get("/me", echo[Both])
	})
	r := srv.Do(withAuth("ada"))
	if r.Status != 200 || r.Text() != `{"caller":{"name":"user-ada"},"org":{"slug":"org-ada"}}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if n := sessions.Load(); n != 1 {
		t.Fatalf("the Session Provider ran %d times for one request, want once", n)
	}
}

func TestAProviderNoFieldOfTheRouteNeedsDoesNotRun(t *testing.T) {
	var sessions atomic.Int32
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(&sessions))
		app.Get("/open", ok[Empty])
	})
	if r := srv.Do(req("GET", "/open")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if n := sessions.Load(); n != 0 {
		t.Fatalf("the Session Provider ran %d times for a route that does not ask for it", n)
	}
}

func TestProvidersRunAfterTheGroupAndRouteMiddlewareAndBeforeTheHandler(t *testing.T) {
	var tr trail
	srv := start(t, func(app *bites.App) {
		app.Use(tr.tag("app"))
		g := app.Group("/api", bites.Use(tr.tag("group")))
		g.Provide(func(_ context.Context, _ bites.Request, _ struct{}) (Session, error) {
			tr.add("session")
			return Session{Token: "t"}, nil
		})
		g.Get("/me", func(_ context.Context, in struct{ S Session }) (struct{}, error) {
			tr.add("handler")
			return struct{}{}, nil
		}, bites.Use(tr.tag("route")))
	})
	if r := srv.Do(req("GET", "/api/me")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	want := "app> group> route> session handler <route <group <app"
	if got := tr.String(); got != want {
		t.Fatalf("order:\n got  %s\n want %s", got, want)
	}
}

func TestAProviderOfAGroupCoversItsRoutesAndNotTheRoutesOutsideIt(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/in")
		g.Provide(func(context.Context, bites.Request, struct{}) (Session, error) { return Session{Token: "g"}, nil })
		g.Get("/s", echo[struct{ Session Session }])
	})
	if r := srv.Do(req("GET", "/in/s")); r.Status != 200 || r.Text() != `{"session":{"token":"g"}}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	got := recoverMessage(func() {
		a := bites.New(bites.WithClock(bitetest.NewClock()))
		a.Group("/in").Provide(sessionFromHeader(nil))
		a.Get("/out", echo[struct{ Session Session }])
	})
	if !strings.Contains(got, "GET /out: struct { Session bites_test.Session }.Session is not a section (Path, Query, Header, Cookie or Body), and no Provider of Session covers this route") {
		t.Fatalf("a route outside the group: %q", got)
	}
}

func TestOverrideInANestedGroupReplacesTheProviderThereAndTheProvidersAboveThatNeedItSeeTheReplacement(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", me)
		test := app.Group("/test")
		test.Override(func(context.Context, bites.Request, struct{}) (Session, error) { return Session{Token: "fake"}, nil })
		test.Get("/me", me)
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/test/me"}); r.Status != 200 || r.Text() != `{"name":"user-fake","verbose":false}` {
		t.Fatalf("under the override: %d %s", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(req("GET", "/me")), 401, `{"title":"Unauthorized","status":401,"detail":"no session"}`)
}

func TestAProviderAsARouteOptionCoversOnlyThatRoute(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/s", echo[struct{ Session Session }], bites.Provide(func(context.Context, bites.Request, struct{}) (Session, error) {
			return Session{Token: "r"}, nil
		}))
	})
	if r := srv.Do(req("GET", "/s")); r.Status != 200 || r.Text() != `{"session":{"token":"r"}}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestWhatAProviderAddsToTheLogRecordIsReadByAMiddlewareThatRanBeforeIt(t *testing.T) {
	var seen atomic.Value
	srv := start(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			res := next.Call(ctx)
			var buf [4]slog.Attr
			attrs := req.AppendLogAttrs(buf[:0])
			var b strings.Builder
			for _, a := range attrs {
				b.WriteString(a.String() + ";")
			}
			seen.Store(b.String())
			return res, nil
		})
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", me)
	})
	if r := srv.Do(withAuth("ada")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if got := seen.Load(); got != "caller=user-ada;" {
		t.Fatalf("attributes seen by the access-log position: %q", got)
	}
	srv.Do(withAuth("bob"))
	if got := seen.Load(); got != "caller=user-bob;" {
		t.Fatalf("the next request starts with no attributes of the last one: %q", got)
	}
}

func TestAPanicInAProviderIsA500AndTheConnectionServesTheNextRequest(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(func(context.Context, bites.Request, struct{}) (Session, error) { panic("boom") })
		app.Get("/s", echo[struct{ Session Session }])
		app.Get("/ok", ok[Empty])
	})
	if r := srv.Do(req("GET", "/s")); r.Status != 500 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(req("GET", "/ok")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

// Grow is provided by a Provider that grows the goroutine's stack and runs a collection, between
// the Provider of Session and the one of Caller that reads it.
type Grow struct{ Depth int }

//go:noinline
func deep(n int) int {
	var pad [256]byte
	pad[n%256] = byte(n)
	if n == 0 {
		runtime.GC()
		return int(pad[0])
	}
	return deep(n-1) + int(pad[n%256]&0)
}

func TestAProviderValueSurvivesTheStackGrowingAndACollectionWhileLaterProvidersRun(t *testing.T) {
	type Deep struct {
		S      Session
		G      Grow
		Caller Caller
	}
	srv := start(t, func(app *bites.App) {
		app.Provide(func(_ context.Context, req bites.Request, _ struct{}) (Session, error) {
			return Session{Token: strings.Repeat(req.Header("authorization"), 2)}, nil
		})
		app.Provide(func(context.Context, bites.Request, struct{}) (Grow, error) { return Grow{Depth: deep(2000)}, nil })
		app.Provide(callerFromSession)
		app.Get("/me", echo[Deep])
	})
	for range 3 {
		r := srv.Do(withAuth("ada"))
		if r.Status != 200 || r.Text() != `{"s":{"token":"adaada"},"g":{"depth":0},"caller":{"name":"user-adaada"}}` {
			t.Fatalf("%d %s", r.Status, r.Text())
		}
	}
}

func TestAStreamOfARouteWithProvidersSeesTheProvidedValuesAfterTheProvidersHaveReturned(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", func(_ context.Context, in Me) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				deep(500) // the stack of the Providers' steps is gone; grow it over where they were
				return e.Send(bites.Event{Data: in.Caller.Name})
			}}, nil
		})
	})
	r := srv.Do(withAuth("ada"))
	if r.Status != 200 || r.Text() != "data: user-ada\n\n" {
		t.Fatalf("%d %q", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/me"}), 401, `{"title":"Unauthorized","status":401,"detail":"no session"}`)
}

// sessionService is a service struct whose methods are Providers, the way a handler reaches a
// service through a method value (ADR 0014).
type sessionService struct{ prefix string }

func (s *sessionService) FromHeader(_ context.Context, req bites.Request, _ struct{}) (Session, error) {
	tok := req.Header("authorization")
	if tok == "" {
		return Session{}, bites.Unauthorized("no session")
	}
	return Session{Token: s.prefix + tok}, nil
}

func (s sessionService) Fake(context.Context, bites.Request, struct{}) (Session, error) {
	return Session{Token: s.prefix + "fake"}, nil
}

func (s *sessionService) Caller(_ context.Context, _ bites.Request, deps struct{ Session Session }) (Caller, error) {
	return Caller{Name: "user-" + deps.Session.Token}, nil
}

func (s *sessionService) Org(_ context.Context, _ bites.Request, deps struct{ Session Session }) (Org, error) {
	return Org{Slug: "org-" + deps.Session.Token}, nil
}

func TestAMethodValueIsAProviderOnTheAppAGroupARouteAndAsAnOverride(t *testing.T) {
	svc := &sessionService{prefix: "s:"}
	srv := start(t, func(app *bites.App) {
		app.Provide(svc.FromHeader)
		app.Provide(svc.Caller)
		app.Get("/me", me)
		app.Get("/route", me, bites.Override(sessionService{prefix: "r:"}.Fake))
		test := app.Group("/test")
		test.Override(sessionService{prefix: "g:"}.Fake)
		test.Get("/me", me)
		org := app.Group("/org")
		org.Provide(svc.Org)
		org.Get("/s", echo[struct{ Org Org }])
		app.Get("/one", echo[struct{ Org Org }], bites.Provide(svc.Org))
	})
	if r := srv.Do(withAuth("ada")); r.Status != 200 || r.Text() != `{"name":"user-s:ada","verbose":false}` {
		t.Fatalf("the app's Providers: %d %s", r.Status, r.Text())
	}
	wantProblem(t, srv.Do(req("GET", "/me")), 401, `{"title":"Unauthorized","status":401,"detail":"no session"}`)
	if r := srv.Do(req("GET", "/route")); r.Status != 200 || r.Text() != `{"name":"user-r:fake","verbose":false}` {
		t.Fatalf("a route's Override: %d %s", r.Status, r.Text())
	}
	if r := srv.Do(req("GET", "/test/me")); r.Status != 200 || r.Text() != `{"name":"user-g:fake","verbose":false}` {
		t.Fatalf("a group's Override: %d %s", r.Status, r.Text())
	}
	for _, target := range []string{"/org/s", "/one"} {
		if r := srv.Do(bitetest.Request{Method: "GET", Target: target, Header: map[string][]string{"Authorization": {"ada"}}}); r.Status != 200 || r.Text() != `{"org":{"slug":"org-s:ada"}}` {
			t.Fatalf("%s, a Provider of a group or a route: %d %s", target, r.Status, r.Text())
		}
	}
}

// ---- a Provider's own Header and Cookie sections (ADR 0023) ----

type Tenant struct{ ID string }

type tenantSlug string

func (tenantSlug) Length() (int, int) { return 2, 20 }

func tenantFromHeader(_ context.Context, _ bites.Request, deps struct {
	Header struct{ XTenant tenantSlug }
}) (Tenant, error) {
	return Tenant{ID: string(deps.Header.XTenant)}, nil
}

type InTenant struct{ T Tenant }

func TestAProvidersHeaderSectionIsDecodedAndCheckedAsTheInputsWouldBe(t *testing.T) {
	ran := false
	srv := start(t, func(app *bites.App) {
		app.Provide(tenantFromHeader)
		app.Get("/t", func(_ context.Context, in InTenant) (Tenant, error) { ran = true; return in.T, nil })
	})
	wantProblem(t, srv.Do(req("GET", "/t")), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"header.X-Tenant","message":"required","rule":"required"}]}`)
	wantProblem(t, srv.Do(bitetest.Request{Method: "GET", Target: "/t", Header: map[string][]string{"X-Tenant": {"a"}}}), 400,
		`{"title":"Bad Request","status":400,"errors":[{"location":"header.X-Tenant","message":"must be between 2 and 20 characters","rule":"length","min":2,"max":20}]}`)
	if ran {
		t.Fatal("the handler ran after its Provider's header failed")
	}
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/t", Header: map[string][]string{"X-Tenant": {"acme"}}})
	if r.Status != 200 || r.Text() != `{"id":"acme"}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestTheAPIDocumentStatesTheHeaderAProviderReadsOnceBesideTheInputs(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(tenantFromHeader)
		app.Get("/t", func(_ context.Context, in struct {
			T      Tenant
			Header struct{ XTenant bites.Opt[string] }
		}) (Tenant, error) {
			return in.T, nil
		})
	})
	op := doc["paths"].(map[string]any)["/t"].(map[string]any)["get"].(map[string]any)
	params := op["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("parameters: %v", params)
	}
	p := params[0].(map[string]any)
	if p["in"] != "header" || p["name"] != "X-Tenant" || p["required"] != true {
		t.Fatalf("%v", p)
	}
	if _, ok := op["responses"].(map[string]any)["400"]; !ok {
		t.Fatalf("no 400: %v", op["responses"])
	}
}

type viewer struct{ Name string }

// viewerFromTicket reads a ticket from the query, as a browser's EventSource or WebSocket sends it.
func viewerFromTicket(_ context.Context, _ bites.Request, deps struct {
	Query struct{ Ticket bites.Opt[string] }
}) (viewer, error) {
	t, ok := deps.Query.Ticket.Get()
	if !ok || t != "good" {
		return viewer{}, bites.Unauthorized("open the stream with ?ticket=")
	}
	return viewer{Name: "ada"}, nil
}

func TestAProviderReadsATicketFromTheQueryOfAWebSocketAndAnEventStreamRoute(t *testing.T) {
	scheme := bites.SecurityScheme{Type: "apiKey", In: "query", Name: "ticket"}
	var doc string
	srv := start(t, func(app *bites.App) {
		app.Provide(bites.CustomAuth(scheme, viewerFromTicket))
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/feed", func(_ context.Context, in struct {
			V     viewer
			Query struct{ Since bites.Opt[int64] }
		}) (bites.SSE, error) {
			return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
				return e.Send(bites.Event{Data: in.V.Name})
			}}, nil
		}, bites.RejectUnknownQueryKeys())
		app.WebSocket("/ws", func(_ context.Context, in struct{ V viewer }, _ *bites.Socket[string]) (bites.Inbox[struct{}], error) {
			return bites.Inbox[struct{}]{}, nil
		})
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/feed?since=3"}); r.Status != 401 {
		t.Fatalf("no ticket: %d %s", r.Status, r.Text())
	}
	ev := srv.OpenEvents(bitetest.Request{Target: "/feed?ticket=good&since=3"}).Next()
	if ev.Data != "ada" {
		t.Fatalf("a ticket, and a strict query that lets the Provider's key through: %+v", ev)
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/feed?ticket=good&other=1"}); r.Status != 400 || !strings.Contains(r.Text(), "query.other") {
		t.Fatalf("a key neither reads is still refused: %d %s", r.Status, r.Text())
	}
	if _, r := srv.TryDialWS(bitetest.WSRequest{Target: "/ws"}); r.Status != 401 {
		t.Fatalf("a WebSocket with no ticket: %d %s", r.Status, r.Text())
	}
	srv.DialWS(bitetest.WSRequest{Target: "/ws?ticket=good"}).Leave()
	doc = srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	if !strings.Contains(doc, `"in":"query","name":"ticket"`) || strings.Count(doc, `"security":[{"apiKeyAuth.ticket":[]}]`) != 2 {
		t.Fatalf("the document states the ticket and its scheme on the stream and the WebSocket: %s", doc)
	}
}
