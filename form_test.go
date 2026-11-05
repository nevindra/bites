package bites_test

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type formFields struct {
	Name bites.NonEmpty
	Tags bites.Opt[[]string]
	Page bites.Opt[int]
	Age  bites.PositiveInt
}

type formIn struct{ Body bites.Form[formFields] }

type formOut struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
	Page int      `json:"page"`
	Set  bool     `json:"set"`
	Age  int      `json:"age"`
}

func formHandler(_ context.Context, in formIn) (formOut, error) {
	p, set := in.Body.Value.Page.Get()
	return formOut{string(in.Body.Value.Name), in.Body.Value.Tags.Or(nil), p, set, int(in.Body.Value.Age)}, nil
}

func formReq(body string) bitetest.Request {
	return bitetest.Request{Method: "POST", Target: "/f", Body: []byte(body),
		Header: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}}}
}

func formApp(opts ...bites.RouteOption) func(*bites.App) {
	return func(app *bites.App) { app.Post("/f", formHandler, opts...) }
}

const formOK = `{"name":"Ada Lovelace","tags":["a","b c"],"page":2,"set":true,"age":36}`

func TestAFormBodyIsDecodedLikeAQueryWithListsOptAndConstraintTypes(t *testing.T) {
	srv := start(t, formApp())
	r := srv.Do(formReq("name=Ada+Lovelace&tags=a&tags=b%20c&page=2&age=36"))
	if r.Status != 200 || r.Text() != formOK {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(formReq("name=Ada&age=1"))
	if want := `{"name":"Ada","tags":[],"page":0,"set":false,"age":1}`; r.Status != 200 || r.Text() != want {
		t.Fatalf("an absent list and an absent Opt are allowed: %d %s", r.Status, r.Text())
	}
}

func TestAFormBodyMayCarryACharsetAndAnyCaseInItsContentType(t *testing.T) {
	srv := start(t, formApp())
	q := formReq("name=Ada&age=1")
	q.Header["Content-Type"] = []string{"Application/X-WWW-Form-Urlencoded; charset=UTF-8"}
	if r := srv.Do(q); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAFormFieldThatIsWrongIsA400NamingBodyDotTheField(t *testing.T) {
	srv := start(t, formApp())
	r := srv.Do(formReq("name=&page=x&age=0&tags=ok"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.name","message":"must not be empty","rule":"nonempty"},`+
		`{"location":"body.page","message":"expected an integer","rule":"type"},`+
		`{"location":"body.age","message":"expected a positive integer","rule":"positive"}]}`)
	r = srv.Do(formReq("age=3"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.name","message":"required","rule":"required"}]}`)
	r = srv.Do(formReq("name=a&age=3&age=4"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.age","message":"sent more than once"}]}`)
}

func TestAnEmptyFormBodyIsAFormWithNoFieldsSoRequiredFieldsAreMissing(t *testing.T) {
	srv := start(t, formApp())
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/f"})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.name","message":"required","rule":"required"},{"location":"body.age","message":"required","rule":"required"}]}`)
}

func TestAFormListElementThatIsWrongNamesItsIndex(t *testing.T) {
	type in struct {
		Body bites.Form[struct{ N []int }]
	}
	srv := start(t, func(app *bites.App) {
		app.Post("/f", func(_ context.Context, in in) (int, error) { return len(in.Body.Value.N), nil })
	})
	r := srv.Do(formReq("n=1&n=x&n=3"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.n[1]","message":"expected an integer","rule":"type"}]}`)
}

func TestAFormBodyWithAFieldTheTypeDoesNotHaveIsRefusedUnlessTheRouteAcceptsUnknownMembers(t *testing.T) {
	r := start(t, formApp()).Do(formReq("name=a&age=1&extra=1&x%20y=2"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.extra","message":"unknown field"},{"location":"body.x y","message":"unknown field"}]}`)
	r = start(t, formApp(bites.AcceptUnknownMembers())).Do(formReq("name=a&age=1&extra=1"))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	long := strings.Repeat("k", 100)
	r = start(t, formApp()).Do(formReq("name=a&age=1&" + long + "=1"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.`+long[:64]+`","message":"unknown field"}]}`)
}

func TestAFormRouteGivenAnythingButAFormIsA415AndAJSONRouteGivenAFormIsA415(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		formApp()(app)
		app.Post("/j", func(_ context.Context, in struct{ Body struct{ A string } }) (string, error) { return in.Body.A, nil })
	})
	const want = `{"title":"Unsupported Media Type","status":415,"detail":"the body must be sent as application/x-www-form-urlencoded"}`
	for _, ct := range []string{"application/json", "multipart/form-data; boundary=x", "text/plain", ""} {
		q := formReq("name=a&age=1")
		delete(q.Header, "Content-Type")
		if ct != "" {
			q.Header["Content-Type"] = []string{ct}
		}
		wantProblem(t, srv.Do(q), 415, want)
	}
	q := formReq("a=b")
	q.Target = "/j"
	r := srv.Do(q)
	if r.Status != 415 || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAFormValueWithABadPercentEscapeIsA400NamingTheField(t *testing.T) {
	srv := start(t, formApp())
	r := srv.Do(formReq("name=%zz&age=1&tags=%4"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"body.name","message":"expected a valid percent-encoded value"},`+
		`{"location":"body.tags","message":"expected a valid percent-encoded value"},`+
		`{"location":"body.name","message":"required","rule":"required"}]}`)
	r = srv.Do(formReq("name=a&age=1&%zz=1"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"expected a valid percent-encoded value"}]}`)
}

func TestAFormBodyOfAMillionAmpersandsIsA413AndCostsOneScan(t *testing.T) {
	srv := start(t, formApp())
	r := srv.Do(formReq(strings.Repeat("&", 1_000_000)))
	wantProblem(t, r, 413, `{"title":"Content Too Large","status":413,"detail":"request body has too many fields"}`)
}

func TestAFormFieldRepeatedPastMaxFieldsIsA413AndWithinItIsAList(t *testing.T) {
	srv := startWith(t, formApp(), bites.WithLimits(bites.Limits{MaxFields: 5}))
	r := srv.Do(formReq("name=a&age=1&tags=1&tags=2&tags=3"))
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(formReq("name=a&age=1&tags=1&tags=2&tags=3&tags=4"))
	if r.Status != 413 || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAFormBodyOverMaxBodyIsA413(t *testing.T) {
	srv := start(t, formApp(bites.MaxBody(16)))
	r := srv.Do(formReq("name=" + strings.Repeat("a", 100) + "&age=1"))
	if r.Status != 413 || r.Get("content-type") != "application/problem+json" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAFormFieldUsesTheNamingPolicyAndWireNames(t *testing.T) {
	type in struct {
		Body bites.Form[formNamed]
	}
	srv := start(t, func(app *bites.App) {
		app.Post("/f", func(_ context.Context, in in) (string, error) {
			return in.Body.Value.PageSize + "/" + in.Body.Value.Odd, nil
		})
	})
	r := srv.Do(formReq("page_size=10&x-odd=7"))
	if r.Status != 200 || r.Text() != `"10/7"` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

type formNamed struct {
	PageSize string
	Odd      string
}

func (formNamed) WireNames() map[string]string { return map[string]string{"Odd": "x-odd"} }

func TestAStringFromAFormBodyOutlivesItsRequest(t *testing.T) {
	var kept []string
	srv := start(t, func(app *bites.App) {
		app.Post("/f", func(_ context.Context, in formIn) (struct{}, error) {
			kept = append(kept, string(in.Body.Value.Name), in.Body.Value.Tags.Or(nil)[0])
			return struct{}{}, nil
		})
	})
	srv.Do(formReq("name=Ada+Lovelace&age=1&tags=x%40y"))
	for i := 0; i < 50; i++ {
		srv.Do(formReq("name=ZZZZZZZZZZZ&age=1&tags=QQQQ"))
	}
	if kept[0] != "Ada Lovelace" || kept[1] != "x@y" {
		t.Fatalf("%q", kept)
	}
}

type listingForm struct {
	Title bites.NonEmpty
	Cover mpPhoto
}

type listingPageIn struct {
	Body bites.Submitted[listingForm]
}

var listingTmpl = template.Must(template.New("l").Parse(`<form enctype="multipart/form-data">` +
	`<input name="title" value="{{.Sent "title"}}"><b>{{.Problem "title"}}</b>` +
	`<input type="file" name="cover" value="[{{.Sent "cover"}}]"><b>{{.Problem "cover"}}</b></form>`))

func listingPage(_ context.Context, in listingPageIn) (bites.OneOf2[bites.SeeOther, bites.Page], error) {
	type answer = bites.OneOf2[bites.SeeOther, bites.Page]
	f, ok := in.Body.Valid()
	if ok {
		r, err := f.Cover.Open()
		if err != nil {
			return answer{}, err
		}
		defer r.Close()
		if b, _ := io.ReadAll(r); string(b) == "REFUSED" {
			in.Body.Add("cover", "is a picture the site does not take")
			ok = false
		}
	}
	if !ok {
		var b bytes.Buffer
		if err := listingTmpl.Execute(&b, in.Body); err != nil {
			return answer{}, err
		}
		return answer{}.B(bites.Page{Status: 422, Body: b.Bytes()}), nil
	}
	return answer{}.A(bites.SeeOther{Location: "/listings/1"}), nil
}

func listingServer(t *testing.T) *bitetest.Server {
	return startMP(t, func(a *bites.App) { a.Post("/listings", listingPage, bites.MaxBody(4<<20)) }, bites.Limits{})
}

func TestASubmittedWithAFileFieldShowsTheFormAgainWithTheTextAsTypedAndProblemNamingTheFile(t *testing.T) {
	srv := listingServer(t)
	r := doRaw(t, srv, mpRequest("/listings", field("title", "A <flat>")+fileField("cover", "c.gif", "image/gif", "GIF")+closing()))
	body := r.Text()
	if r.Status != 422 || !strings.Contains(r.Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s %s", r.Status, r.Get("Content-Type"), body)
	}
	for _, want := range []string{`value="A &lt;flat&gt;"`, `<b>the file must be sent as image/png or image/jpeg</b>`, `value="[]"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s: %s", want, body)
		}
	}
	// The handler runs whatever the form held, and sees a good file.
	if r := doRaw(t, srv, mpRequest("/listings", field("title", "A flat")+png("cover", "FINE")+closing())); r.Status != 303 || r.Get("Location") != "/listings/1" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = doRaw(t, srv, mpRequest("/listings", field("title", "A flat")+png("cover", "REFUSED")+closing()))
	if r.Status != 422 || !strings.Contains(r.Text(), "<b>is a picture the site does not take</b>") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	// A body over the limits is the 413 problem, since no form exists yet to show.
	r = doRaw(t, srv, mpRequest("/listings", field("title", "A flat")+png("cover", filler(4<<20+1))+closing()))
	if r.Status != 413 || r.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s", r.Status, r.Get("Content-Type"))
	}
}

func TestASubmittedWithAFileFieldRefusesAnUrlencodedBodyWithA415(t *testing.T) {
	srv := listingServer(t)
	r2 := srv.Do(bitetest.Request{Method: "POST", Target: "/listings", Body: []byte("title=x"),
		Header: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}}})
	if r2.Status != 415 || !strings.Contains(r2.Text(), "the body must be sent as multipart/form-data") {
		t.Fatalf("%d %s", r2.Status, r2.Text())
	}
}
