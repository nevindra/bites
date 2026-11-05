package bites_test

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type eventForm struct {
	Name     bites.NonEmpty
	Capacity bites.PositiveInt
	Tags     bites.Opt[[]string]
}

type eventPageIn struct {
	Query struct{ Draft bites.Opt[bool] }
	Body  bites.Submitted[eventForm]
}

func (in eventPageIn) Validate() error {
	if v, _ := in.Body.Valid(); v.Name == "closed" {
		return bites.Invalid("body.name", "is a name the venue does not take")
	}
	return nil
}

type eventAnswer = bites.OneOf2[bites.SeeOther, bites.Page]

var eventTmpl = template.Must(template.New("f").Parse(`<form>` +
	`<input name="name" value="{{.Sent "name"}}"><b>{{.Problem "name"}}</b>` +
	`<input name="capacity" value="{{.Sent "capacity"}}"><b>{{.Problem "capacity"}}</b>` +
	`{{range .SentAll "tags"}}<i>{{.}}</i>{{end}}</form>`))

func eventPage(_ context.Context, in eventPageIn) (eventAnswer, error) {
	f, ok := in.Body.Valid()
	if ok && f.Name == "booked" {
		in.Body.Add("name", "is booked on that date")
		ok = false
	}
	if !ok {
		var b bytes.Buffer
		if err := eventTmpl.Execute(&b, in.Body); err != nil {
			return eventAnswer{}, err
		}
		return eventAnswer{}.B(bites.Page{Status: 422, Body: b.Bytes()}), nil
	}
	return eventAnswer{}.A(bites.SeeOther{Location: "/events/1"}), nil
}

func formPages(t *testing.T) *bitetest.Server {
	app := bites.New()
	app.Post("/events", eventPage)
	return bitetest.Start(t, app)
}

func postForm(srv *bitetest.Server, target string, form url.Values) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "POST", Target: target, Body: []byte(form.Encode()),
		Header: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}}})
}

func TestASubmittedFormWithMistakesReachesTheHandlerWhichShowsItAgainAsA422PageWithWhatWasTyped(t *testing.T) {
	srv := formPages(t)
	resp := postForm(srv, "/events", url.Values{"name": {""}, "capacity": {"abc"}, "tags": {"a", "<b>"}})
	if resp.Status != 422 || resp.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("%d %s %s", resp.Status, resp.Get("Content-Type"), resp.Text())
	}
	body := resp.Text()
	for _, want := range []string{`value="abc"`, `<i>a</i>`, `<i>&lt;b&gt;</i>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page keeps what was typed (%s): %s", want, body)
		}
	}
	if strings.Count(body, "<b></b>") != 0 {
		t.Errorf("both fields name their mistake: %s", body)
	}
}

func TestAValidSubmittedFormRunsTheHandlerWhichRedirects(t *testing.T) {
	srv := formPages(t)
	if resp := postForm(srv, "/events", url.Values{"name": {"Gala"}, "capacity": {"10"}}); resp.Status != 303 || resp.Get("Location") != "/events/1" {
		t.Fatalf("%d %v %s", resp.Status, resp.Header, resp.Text())
	}
}

func TestValidateAndTheHandlerAddTheirOwnMistakesToTheSubmittedForm(t *testing.T) {
	srv := formPages(t)
	for name, want := range map[string]string{"closed": "is a name the venue does not take", "booked": "is booked on that date"} {
		resp := postForm(srv, "/events", url.Values{"name": {name}, "capacity": {"10"}})
		if resp.Status != 422 || !strings.Contains(resp.Text(), "<b>"+want+"</b>") {
			t.Errorf("%s: %d %s", name, resp.Status, resp.Text())
		}
	}
}

func TestAMistakeOutsideTheSubmittedFormIsStillThe400ListingTheFormsToo(t *testing.T) {
	srv := formPages(t)
	resp := postForm(srv, "/events?draft=maybe", url.Values{"name": {""}, "capacity": {"10"}})
	if resp.Status != 400 || resp.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
	for _, loc := range []string{"query.draft", "body.name"} {
		if !strings.Contains(resp.Text(), `"location":"`+loc+`"`) {
			t.Errorf("the 400 lists %s: %s", loc, resp.Text())
		}
	}
}

func TestAPageIsSentWithItsStatusAndARefusedStatusOrContentTypeIsALogged500(t *testing.T) {
	logs := &logBuf{}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	app.Get("/p/{status}", func(_ context.Context, in struct{ Path struct{ Status int } }) (bites.Page, error) {
		return bites.Page{Status: in.Path.Status, Body: []byte("<p>hi</p>")}, nil
	})
	app.Get("/typed", func(context.Context, struct{}) (bites.Page, error) {
		return bites.Page{Header: bites.Header{"content-type": {"text/plain"}}}, nil
	})
	srv := bitetest.Start(t, app)
	for status, want := range map[string]int{"0": 200, "200": 200, "404": 404, "422": 422, "401": 500, "407": 500, "302": 500, "500": 500, "201": 500} {
		resp := srv.Do(bitetest.Request{Method: "GET", Target: "/p/" + status})
		if resp.Status != want {
			t.Errorf("a Page of status %s: %d %s", status, resp.Status, resp.Text())
		}
		if want != 500 && (resp.Text() != "<p>hi</p>" || resp.Get("Content-Type") != "text/html; charset=utf-8") {
			t.Errorf("a Page of status %s: %s %s", status, resp.Get("Content-Type"), resp.Text())
		}
	}
	if resp := srv.Do(bitetest.Request{Method: "GET", Target: "/typed"}); resp.Status != 500 {
		t.Errorf("a Page with its own Content-Type: %d", resp.Status)
	}
	for _, want := range []string{"which must name a challenge", "a Page has a Content-Type in its Header"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log names the mistake %q: %s", want, logs.String())
		}
	}
}

func TestTheDocumentStatesAPageAs200And4XXHtmlAndASubmittedFormWithNo400(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/events", func(_ context.Context, in struct {
			Body bites.Submitted[eventForm]
		}) (eventAnswer, error) {
			return eventAnswer{}, nil
		})
	})
	rs := dig(doc, "paths", "/events", "post", "responses").(map[string]any)
	for _, st := range []string{"200", "4XX"} {
		if dig(rs, st, "content", "text/html", "schema", "type") != "string" {
			t.Errorf("%s is an HTML page: %v", st, rs[st])
		}
	}
	if rs["303"] == nil {
		t.Errorf("the redirect is stated: %v", rs)
	}
	if rs["400"] != nil {
		t.Errorf("a form whose mistakes the handler shows has no 400: %v", rs["400"])
	}
	if dig(doc, "paths", "/events", "post", "requestBody", "content", "application/x-www-form-urlencoded", "schema") == nil {
		t.Errorf("the form is the request body: %v", dig(doc, "paths", "/events", "post", "requestBody"))
	}
}

func TestAFormOverTheFieldLimitOnASubmittedRouteIsStillThe413ProblemNotAPage(t *testing.T) {
	app := bites.New(bites.WithLimits(bites.Limits{MaxFields: 4}))
	app.Post("/events", eventPage)
	srv := bitetest.Start(t, app)
	resp := postForm(srv, "/events", url.Values{"name": {"a"}, "capacity": {"1"}, "tags": {"1", "2", "3", "4"}})
	if resp.Status != 413 || resp.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s %s", resp.Status, resp.Get("Content-Type"), resp.Text())
	}
}

func TestABadEscapeAndAnUnknownFieldAreProblemsOfTheFormAndTheRawTextIsKept(t *testing.T) {
	got := make(chan bites.Submitted[eventForm], 1)
	app := bites.New()
	app.Post("/events", func(_ context.Context, in struct{ Body bites.Submitted[eventForm] }) (bites.Page, error) {
		got <- in.Body
		return bites.Page{Status: 422}, nil
	})
	srv := bitetest.Start(t, app)
	srv.Do(bitetest.Request{Method: "POST", Target: "/events", Body: []byte("name=%zz&capacity=3&submit=Save"),
		Header: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}}})
	f := <-got
	if _, ok := f.Valid(); ok {
		t.Fatal("a form with a bad escape is not valid")
	}
	if f.Sent("name") != "%zz" || f.Sent("capacity") != "3" || f.Sent("submit") != "Save" {
		t.Errorf("the text as sent: %q %q %q", f.Sent("name"), f.Sent("capacity"), f.Sent("submit"))
	}
	if f.Problem("submit") != "unknown field" {
		t.Errorf("an unknown field is a problem a page must list: %+v", f.Problems())
	}
}

func TestASubmittedFormKeptPastItsRequestIsNotChangedByTheNextRequests(t *testing.T) {
	kept := make(chan bites.Submitted[eventForm], 10)
	app := bites.New()
	app.Post("/events", func(_ context.Context, in struct{ Body bites.Submitted[eventForm] }) (bites.Page, error) {
		kept <- in.Body
		return bites.Page{Status: 422}, nil
	})
	srv := bitetest.Start(t, app)
	postForm(srv, "/events", url.Values{"name": {"first+%2F"}, "capacity": {"zero"}})
	first := <-kept
	for i := range 5 {
		postForm(srv, "/events", url.Values{"name": {strings.Repeat("x", 40+i)}, "capacity": {"nope" + strings.Repeat("y", i)}})
		<-kept
	}
	if first.Sent("name") != "first+%2F" || first.Sent("capacity") != "zero" || !strings.HasPrefix(first.Problem("capacity"), "expected") {
		t.Errorf("the first form changed: %q %q %q", first.Sent("name"), first.Sent("capacity"), first.Problem("capacity"))
	}
}

type pageUser struct{ Name string }

func TestASubmittedFormBehindAProviderKeepsItsMistakesForTheHandler(t *testing.T) {
	app := bites.New()
	app.Provide(func(context.Context, bites.Request, struct{}) (pageUser, error) { return pageUser{"ada"}, nil })
	app.Post("/events", func(_ context.Context, in struct {
		U    pageUser
		Body bites.Submitted[eventForm]
	}) (bites.Page, error) {
		return bites.Page{Status: 422, Body: []byte(in.U.Name + ":" + in.Body.Problem("capacity"))}, nil
	})
	srv := bitetest.Start(t, app)
	resp := postForm(srv, "/events", url.Values{"name": {"a"}, "capacity": {"-1"}})
	if resp.Status != 422 || !strings.HasPrefix(resp.Text(), "ada:") || resp.Text() == "ada:" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestTheZeroSubmittedHoldsNoFormAndAddMakesOne(t *testing.T) {
	var s bites.Submitted[eventForm]
	if _, ok := s.Valid(); ok || s.Problems() != nil || s.Sent("name") != "" || s.SentAll("tags") != nil {
		t.Fatal("the zero Submitted holds no form")
	}
	s.Add("name", "is taken")
	if s.Problem("name") != "is taken" || len(s.Problems()) != 1 || s.Problems()[0].Location != "body.name" {
		t.Fatalf("%+v", s.Problems())
	}
}
