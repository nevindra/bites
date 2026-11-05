package bites_test

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type none struct{}

type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestStatusWrappersSetTheStatusTheLocationTheHeadersAndTheCookies(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/items", func(context.Context, none) (bites.Created[Item], error) {
			return bites.Created[Item]{
				Body:     Item{ID: 7, Name: "x"},
				Location: "/items/7",
				Header:   bites.Header{"Cache-Control": {"no-store"}, "X-Multi": {"a", "b"}},
				Cookies:  []bites.Cookie{{Name: "sid", Value: "abc", MaxAge: time.Hour}},
			}, nil
		})
		app.Get("/ok", func(context.Context, none) (bites.OK[Item], error) {
			return bites.OK[Item]{Body: Item{ID: 1}, Header: bites.Header{"ETag": {`"v1"`}}}, nil
		})
		app.Post("/accepted", func(context.Context, none) (bites.Accepted[Item], error) { return bites.Accepted[Item]{}, nil })
		app.Delete("/items/{id}", func(context.Context, GetUser) (bites.NoContent, error) { return bites.NoContent{}, nil })
		app.Get("/cached", func(context.Context, none) (bites.NotModified, error) {
			return bites.NotModified{Header: bites.Header{"ETag": {`"v1"`}}}, nil
		})
		app.Post("/login", func(context.Context, none) (bites.SeeOther, error) { return bites.SeeOther{Location: "/home"}, nil })
		app.Get("/old", func(context.Context, none) (bites.Redirect, error) {
			return bites.Redirect{Status: 308, Location: "/new"}, nil
		})
	})
	r := srv.Do(req("POST", "/items"))
	if r.Status != 201 || r.Text() != `{"id":7,"name":"x"}` || r.Get("location") != "/items/7" || r.Get("content-type") != "application/json" ||
		r.Get("cache-control") != "no-store" || strings.Join(r.Header["x-multi"], ",") != "a,b" ||
		r.Get("set-cookie") != "sid=abc; Path=/; Max-Age=3600; HttpOnly; Secure; SameSite=Lax" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	if r := srv.Do(req("GET", "/ok")); r.Status != 200 || r.Get("etag") != `"v1"` || r.Text() != `{"id":1,"name":""}` {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	if r := srv.Do(req("POST", "/accepted")); r.Status != 202 {
		t.Fatalf("%d", r.Status)
	}
	if r := srv.Do(req("DELETE", "/items/3")); r.Status != 204 || len(r.Body) != 0 || r.Get("content-type") != "" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("GET", "/cached")); r.Status != 304 || len(r.Body) != 0 || r.Get("etag") != `"v1"` {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("POST", "/login")); r.Status != 303 || r.Get("location") != "/home" || r.Get("content-type") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := srv.Do(req("GET", "/old")); r.Status != 308 || r.Get("location") != "/new" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestARedirectWithAStatusThatIsNotARedirectIsA500NotAnAnswerWithThatStatus(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/r", func(context.Context, none) (bites.Redirect, error) {
			return bites.Redirect{Status: 200, Location: "/x"}, nil
		})
	})
	wantProblem(t, srv.Do(req("GET", "/r")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "a Redirect has status 200") {
		t.Fatalf("log: %s", logs)
	}
}

func TestAHeaderTheHandlerSetsCannotBreakTheFramingAndContentTypeReplacesJSON(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/h", func(context.Context, none) (bites.OK[Item], error) {
			return bites.OK[Item]{Header: bites.Header{"Content-Length": {"1"}, "Connection": {"upgrade"}, "Content-Type": {"application/vnd.x+json"}, "X-Evil": {"a\r\nSet-Cookie: pwn=1"}}}, nil
		})
	})
	r := srv.Do(req("GET", "/h"))
	if r.Status != 200 || r.Get("content-type") != "application/vnd.x+json" || r.Get("connection") != "" || r.Get("set-cookie") != "" || len(r.Body) == 0 {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
}

func TestACookieIsRefusedAs500WhenItsNameOrValueCouldSplitTheHeader(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/c", func(context.Context, none) (bites.NoContent, error) {
			return bites.NoContent{Cookies: []bites.Cookie{{Name: "a", Value: "b;c"}}}, nil
		})
	})
	wantProblem(t, srv.Do(req("GET", "/c")), 500, `{"title":"Internal Server Error","status":500}`)
}

func TestACookieLoosensItsDefaultsOnlyWhenTheCodeSaysSo(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/c", func(context.Context, none) (bites.NoContent, error) {
			return bites.NoContent{Cookies: []bites.Cookie{
				{Name: "a", Value: "1"},
				{Name: "b", Value: "2", Path: "/app", Domain: "example.com", SameSite: bites.Strict, NotHttpOnly: true, NotSecure: true},
				{Name: "gone", MaxAge: -1, Expires: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			}}, nil
		})
	})
	r := srv.Do(req("GET", "/c"))
	want := []string{
		"a=1; Path=/; HttpOnly; Secure; SameSite=Lax",
		"b=2; Path=/app; Domain=example.com; SameSite=Strict",
		"gone=; Path=/; Expires=Fri, 02 Jan 2026 03:04:05 GMT; Max-Age=0; HttpOnly; Secure; SameSite=Lax",
	}
	if strings.Join(r.Header["set-cookie"], "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", r.Header["set-cookie"])
	}
}

func TestTextIsPlainUTF8TextAndBytesIsAnyContentTypeWrittenAsTheyAre(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/t", func(context.Context, none) (bites.Text, error) {
			return bites.Text{Body: "café \"quoted\"\n", Header: bites.Header{"X-A": {"1"}}, Cookies: []bites.Cookie{{Name: "s", Value: "1"}}}, nil
		})
		app.Get("/b", func(context.Context, none) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "text/csv; charset=utf-8", Body: []byte("a,b\n1,2\n")}, nil
		})
		app.Get("/e", func(context.Context, none) (bites.Text, error) { return bites.Text{}, nil })
		app.Get("/o", func(context.Context, none) (bites.Text, error) {
			return bites.Text{Body: "x", Header: bites.Header{"Content-Type": {"text/markdown"}}}, nil
		})
		app.Get("/d", func(context.Context, none) (any, error) {
			return bites.Bytes{ContentType: "image/png", Body: []byte{0x89, 'P'}}, nil
		}, bites.Returns[bites.Bytes]())
	})
	r := srv.Do(req("GET", "/t"))
	if r.Status != 200 || r.Get("content-type") != "text/plain; charset=utf-8" || r.Text() != "café \"quoted\"\n" ||
		r.Get("x-a") != "1" || r.Get("set-cookie") == "" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("GET", "/b")); r.Status != 200 || r.Get("content-type") != "text/csv; charset=utf-8" || r.Text() != "a,b\n1,2\n" {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("GET", "/e")); r.Status != 200 || r.Get("content-type") != "text/plain; charset=utf-8" || len(r.Body) != 0 {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("GET", "/o")); r.Get("content-type") != "text/markdown" || r.Text() != "x" {
		t.Fatalf("%v", r.Header)
	}
	if r := srv.Do(req("GET", "/d")); r.Get("content-type") != "image/png" || string(r.Body) != "\x89P" {
		t.Fatalf("%v %q", r.Header, r.Body)
	}
}

func TestBytesWithAContentTypeThatIsNotATypeAndSubtypeIsA500AndLogged(t *testing.T) {
	for _, ct := range []string{"", "text", "/csv", "text/", "te xt/csv", "text/csv\r\nSet-Cookie: x=1"} {
		srv, logs := startLogged(t, func(app *bites.App) {
			app.Get("/b", func(context.Context, none) (bites.Bytes, error) {
				return bites.Bytes{ContentType: ct, Body: []byte("x")}, nil
			})
		})
		r := srv.Do(req("GET", "/b"))
		wantProblem(t, r, 500, `{"title":"Internal Server Error","status":500}`)
		if !strings.Contains(logs.String(), "a Bytes has the content type") {
			t.Fatalf("%q: log: %s", ct, logs)
		}
	}
}

type Result interface{ isResult() }

type okResult struct{ N int }

func (okResult) isResult() {}

func TestAHandlerThatAnswersSeveralStatusesReturnsAnInterfaceTheWrappersFill(t *testing.T) {
	type either interface{}
	srv := start(t, func(app *bites.App) {
		app.Post("/v", func(_ context.Context, in struct{ Query struct{ Kind string } }) (either, error) {
			switch in.Query.Kind {
			case "created":
				return bites.Created[Item]{Body: Item{ID: 1}, Location: "/v/1"}, nil
			case "none":
				return bites.NoContent{}, nil
			case "plain":
				return Item{ID: 2}, nil
			case "ptr":
				return &bites.Accepted[Item]{Body: Item{ID: 3}}, nil
			case "nil":
				return nil, nil
			}
			return make(chan int), nil
		}, bites.Returns[bites.Created[Item]](), bites.Returns[bites.NoContent](), bites.Returns[Item](), bites.Returns[bites.Accepted[Item]]())
	})
	cases := map[string]struct {
		status int
		body   string
	}{
		"created": {201, `{"id":1,"name":""}`}, "none": {204, ""}, "plain": {200, `{"id":2,"name":""}`}, "ptr": {202, `{"id":3,"name":""}`},
		"nil": {500, `{"title":"Internal Server Error","status":500}`}, "chan": {500, `{"title":"Internal Server Error","status":500}`},
	}
	for kind, want := range cases {
		r := srv.Do(req("POST", "/v?kind="+kind))
		if r.Status != want.status || r.Text() != want.body {
			t.Errorf("%s: %d %s, want %d %s", kind, r.Status, r.Text(), want.status, want.body)
		}
	}
}

func TestAStatusErrorIsFoundThroughWrappingAndOnlyItsMessageReachesTheClient(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/e", func(_ context.Context, in struct{ Query struct{ Kind string } }) (none, error) {
			switch in.Query.Kind {
			case "wrapped":
				return none{}, fmt.Errorf("load user 7 from db host-1: %w", bites.NotFound("no such user"))
			case "joined":
				return none{}, errors.Join(errors.New("log me"), bites.Conflict("name taken"))
			case "member":
				return none{}, bites.Forbidden("not yours").With("code", "owner_only").With("n", 3).With("title", "ignored")
			case "bad":
				return none{}, bites.NewError(200, "not an error status")
			case "empty":
				return none{}, bites.NewError(418, "")
			}
			return none{}, errors.New("connection to db-3 refused: password=hunter2")
		})
	})
	cases := map[string]struct {
		status int
		body   string
	}{
		"wrapped": {404, `{"title":"Not Found","status":404,"detail":"no such user"}`},
		"joined":  {409, `{"title":"Conflict","status":409,"detail":"name taken"}`},
		"member":  {403, `{"title":"Forbidden","status":403,"detail":"not yours","code":"owner_only","n":3}`},
		"bad":     {500, `{"title":"Internal Server Error","status":500}`},
		"empty":   {418, `{"title":"Status 418","status":418}`},
		"plain":   {500, `{"title":"Internal Server Error","status":500}`},
	}
	for kind, want := range cases {
		r := srv.Do(req("GET", "/e?kind="+kind))
		if r.Status != want.status || r.Text() != want.body || r.Get("content-type") != "application/problem+json" {
			t.Errorf("%s: %d %s, want %d %s", kind, r.Status, r.Text(), want.status, want.body)
		}
	}
}

func TestAnUnknownErrorIsLoggedWithTheRequestAndNotShown(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/e", func(context.Context, none) (none, error) { return none{}, errors.New("db password=hunter2") })
	})
	r := srv.Do(req("GET", "/e"))
	if strings.Contains(r.Text(), "hunter2") || !strings.Contains(logs.String(), "hunter2") || !strings.Contains(logs.String(), "path=/e") {
		t.Fatalf("body %s\nlog %s", r.Text(), logs)
	}
}

func TestAPanicInAHandlerIsA500AndTheNextRequestIsServed(t *testing.T) {
	srv, _ := startLogged(t, func(app *bites.App) {
		app.Get("/p", func(context.Context, none) (none, error) { panic("boom") })
		app.Get("/ok", func(context.Context, none) (string, error) { return "fine", nil })
	})
	wantProblem(t, srv.Do(req("GET", "/p")), 500, `{"title":"Internal Server Error","status":500}`)
	if r := srv.Do(req("GET", "/ok")); r.Text() != `"fine"` {
		t.Fatalf("%s", r.Text())
	}
}

func TestAClientThatLeavesCancelsTheHandlersContextAndNothingIsLoggedAsAnError(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan error, 1)
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ none) (none, error) {
			close(started)
			<-ctx.Done()
			finished <- ctx.Err()
			return none{}, ctx.Err()
		})
	})
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	<-started
	c.Close()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx.Err() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context was not cancelled")
	}
	time.Sleep(20 * time.Millisecond)
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("a departed client was logged as an error: %s", logs)
	}
}

var _ = bitetest.NewClock

// held is a pointer-shaped type: an interface holds its map directly, with no box to read it from.
type held struct{ M map[string]int }

func TestAnInterfaceReturnIsAnsweredByTheTypeTheRouteDeclaredWhetherReturnedByValueOrPointer(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/v", func(_ context.Context, in struct{ Query struct{ Kind string } }) (Result, error) {
			switch in.Query.Kind {
			case "value":
				return okResult{N: 1}, nil
			case "ptr":
				return &okResult{N: 2}, nil
			}
			return (*okResult)(nil), nil
		}, bites.Returns[okResult]())
		app.Get("/h", func(context.Context, none) (any, error) {
			return held{M: map[string]int{"a": 1}}, nil
		}, bites.Returns[held]())
	})
	for kind, want := range map[string]string{"value": `{"n":1}`, "ptr": `{"n":2}`} {
		if r := srv.Do(req("GET", "/v?kind="+kind)); r.Status != 200 || r.Text() != want {
			t.Errorf("%s: %d %s", kind, r.Status, r.Text())
		}
	}
	wantProblem(t, srv.Do(req("GET", "/v?kind=nil")), 500, `{"title":"Internal Server Error","status":500}`)
	if r := srv.Do(req("GET", "/h")); r.Status != 200 || r.Text() != `{"m":{"a":1}}` {
		t.Errorf("a type an interface holds directly: %d %s", r.Status, r.Text())
	}
}

func TestAnInterfaceReturnOfATypeTheRouteDidNotDeclareIsALoggedA500NamingTheDeclaration(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/v", func(context.Context, none) (any, error) { return bites.NoContent{}, nil }, bites.Returns[bites.Created[Item]]())
	})
	wantProblem(t, srv.Do(req("GET", "/v")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "which its route does not declare: add Returns[bites.NoContent]()") {
		t.Fatalf("log: %s", logs)
	}
}

// ---- SetsLocation (ADR 0013) ----

func TestACreatedOnARouteThatSetsLocationSendsItAndAnEmptyOneIsALogged500(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Post("/a", func(_ context.Context, in struct {
			Query struct{ Empty bites.Opt[bool] }
		}) (bites.Created[User], error) {
			if in.Query.Empty.Or(false) {
				return bites.Created[User]{}, nil
			}
			return bites.Created[User]{Location: "/a/1"}, nil
		}, bites.SetsLocation())
	})
	if r := srv.Do(bitetest.Request{Method: "POST", Target: "/a"}); r.Status != 201 || r.Get("location") != "/a/1" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := srv.Do(bitetest.Request{Method: "POST", Target: "/a?empty=true"}); r.Status != 500 {
		t.Fatalf("an empty Location on a route that said it sets one is %d, not 500", r.Status)
	}
	if !strings.Contains(logs.String(), "a Created has an empty Location, but its route is registered with SetsLocation") {
		t.Fatalf("the 500 is logged with its cause: %s", logs.String())
	}
}

func TestACreatedOnARouteWithoutSetsLocationSendsItsLocationAndMayLeaveItEmpty(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/a", func(context.Context, Empty) (bites.Created[User], error) {
			return bites.Created[User]{Location: "/a/1"}, nil
		})
		app.Post("/b", func(context.Context, Empty) (bites.Created[User], error) { return bites.Created[User]{}, nil })
	})
	if r := srv.Do(bitetest.Request{Method: "POST", Target: "/a"}); r.Status != 201 || r.Get("location") != "/a/1" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := srv.Do(bitetest.Request{Method: "POST", Target: "/b"}); r.Status != 201 || r.Get("location") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

// attachmentCases is a download name and the exact Content-Disposition that
// mime.FormatMediaType("attachment", {"filename": name}) writes for it, recorded from the Go
// version this repository builds with. ADR 0043: a download's header is written by the standard
// library, and these are its outputs.
var attachmentCases = []struct{ name, want string }{
	{"report.pdf", `attachment; filename=report.pdf`},
	{"my report.pdf", `attachment; filename="my report.pdf"`},
	{`a"b.pdf`, `attachment; filename="a\"b.pdf"`},
	{`a\b.pdf`, `attachment; filename="a\\b.pdf"`},
	{"a/b.pdf", `attachment; filename="a/b.pdf"`}, // a separator is kept: strip it with path.Base first
	{"laporan-\u00e9.pdf", `attachment; filename*=utf-8''laporan-%C3%A9.pdf`},
	{"\u62a5\u544a.pdf", `attachment; filename*=utf-8''%E6%8A%A5%E5%91%8A.pdf`},
	{"", `attachment; filename=""`},
	{"a\nb.pdf", `attachment; filename*=utf-8''a%0Ab.pdf`}, // a line break is percent-encoded, never raw
	{"a\xffb.pdf", `attachment; filename*=utf-8''a%FFb.pdf`},
}

func TestTheStandardLibraryWritesTheContentDispositionOfADownloadAndParsesItBack(t *testing.T) {
	for _, c := range attachmentCases {
		got := mime.FormatMediaType("attachment", map[string]string{"filename": c.name})
		if got != c.want {
			t.Errorf("FormatMediaType(%q) = %q, want %q", c.name, got, c.want)
		}
		typ, params, err := mime.ParseMediaType(got)
		if err != nil || typ != "attachment" || params["filename"] != c.name {
			t.Errorf("ParseMediaType(%q) = %q %q %v, want attachment %q", got, typ, params["filename"], err, c.name)
		}
	}
	// Format returns "" only for a media type or a parameter name that is not a token, never
	// for a filename value, so with the two constant words of a download it cannot be "".
	for _, c := range []struct {
		typ    string
		params map[string]string
	}{
		{"", map[string]string{"filename": "a"}},
		{"attach ment", map[string]string{"filename": "a"}},
		{"attachment", map[string]string{"file name": "a"}},
	} {
		if got := mime.FormatMediaType(c.typ, c.params); got != "" {
			t.Errorf("FormatMediaType(%q, %v) = %q, want empty", c.typ, c.params, got)
		}
	}
}

// downloadCases is an Attachment name and the exact Content-Disposition the answer carries: the
// last element of the name, written by mime.FormatMediaType as attachmentCases records.
var downloadCases = []struct{ name, want string }{
	{"report.pdf", `attachment; filename=report.pdf`},
	{"my report.pdf", `attachment; filename="my report.pdf"`},
	{`a"b.pdf`, `attachment; filename="a\"b.pdf"`},
	{"laporan-\u00e9.pdf", `attachment; filename*=utf-8''laporan-%C3%A9.pdf`},
	{"\u62a5\u544a.pdf", `attachment; filename*=utf-8''%E6%8A%A5%E5%91%8A.pdf`},
	{"a\nb.pdf", `attachment; filename*=utf-8''a%0Ab.pdf`},
	{"a\xffb.pdf", `attachment; filename*=utf-8''a%FFb.pdf`},
	{"reports/q3.pdf", `attachment; filename=q3.pdf`},
	{`C:\exports\q3.pdf`, `attachment; filename=q3.pdf`},
	{"../../etc/passwd", `attachment; filename=passwd`},
	{"dir/", `attachment`},
	{".", `attachment`},
	{"a/..", `attachment`},
}

// oneDisposition fails unless the response carries exactly one Content-Disposition and it is want.
func oneDisposition(t *testing.T, r bitetest.Response, want string) {
	t.Helper()
	if vs := headerValues(r, "Content-Disposition"); len(vs) != 1 || vs[0] != want {
		t.Errorf("Content-Disposition %q, want exactly one %q (status %d)", vs, want, r.Status)
	}
}

func TestBytesWithAnAttachmentNameIsADownloadOnGetHeadAndGzip(t *testing.T) {
	body := []byte(strings.Repeat("id,title\n42,Plan\n", 200)) // over the 1 KiB gzip floor
	srv := startWith(t, func(app *bites.App) {
		for i, c := range downloadCases {
			app.Get("/b/"+strconv.Itoa(i), func(context.Context, none) (bites.Bytes, error) {
				return bites.Bytes{ContentType: "text/csv; charset=utf-8", Body: body, Attachment: c.name}, nil
			})
		}
	}, bites.WithGzip(bites.Gzip{}))
	for i, c := range downloadCases {
		target := "/b/" + strconv.Itoa(i)
		r := srv.Do(bitetest.Request{Method: "GET", Target: target})
		oneDisposition(t, r, c.want)
		if r.Status != 200 || len(r.Body) != len(body) {
			t.Errorf("%s: %d, %d bytes", target, r.Status, len(r.Body))
		}
		oneDisposition(t, srv.Do(bitetest.Request{Method: "HEAD", Target: target}), c.want)
		z := srv.Do(bitetest.Request{Method: "GET", Target: target, Header: map[string][]string{"Accept-Encoding": {"gzip"}}})
		oneDisposition(t, z, c.want)
		if z.Get("content-encoding") != "gzip" {
			t.Errorf("%s: not compressed, so the gzip path was not the one tested: %v", target, z.Header)
		}
	}
}

func TestAContentDispositionInHeaderReplacesTheAttachmentAndAnEmptyAttachmentSendsNone(t *testing.T) {
	srv := startWith(t, func(app *bites.App) {
		app.Get("/inline", func(context.Context, none) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "application/pdf", Body: []byte("%PDF"), Attachment: "q3.pdf",
				Header: bites.Header{"content-disposition": {`inline; filename="q3.pdf"`}}}, nil
		})
		app.Get("/shown", func(context.Context, none) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "application/pdf", Body: []byte("%PDF")}, nil
		})
	})
	oneDisposition(t, srv.Do(bitetest.Request{Method: "GET", Target: "/inline"}), `inline; filename="q3.pdf"`)
	if vs := headerValues(srv.Do(bitetest.Request{Method: "GET", Target: "/shown"}), "Content-Disposition"); len(vs) != 0 {
		t.Errorf("an empty Attachment sent %q", vs)
	}
}
