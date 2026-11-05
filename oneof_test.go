package bites_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nevindra/bites"
)

// itemAnswer is the return type of a handler with four answers.
type itemAnswer = bites.OneOf4[bites.Created[Item], bites.NoContent, Item, bites.Text]

func TestAOneOfAnswersWithTheVariantTheHandlerChoseAsIfItWereReturnedAlone(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Post("/v", func(_ context.Context, in struct{ Query struct{ Kind string } }) (itemAnswer, error) {
			switch in.Query.Kind {
			case "created":
				return itemAnswer{}.A(bites.Created[Item]{Body: Item{ID: 1}, Location: "/v/1", Header: bites.Header{"X-A": {"1"}}}), nil
			case "none":
				return itemAnswer{}.B(bites.NoContent{}), nil
			case "plain":
				return itemAnswer{}.C(Item{ID: 2}), nil
			}
			return itemAnswer{}.D(bites.Text{Body: "hi"}), nil
		})
	})
	cases := map[string]struct {
		status      int
		body, ctype string
	}{
		"created": {201, `{"id":1,"name":""}`, "application/json"},
		"none":    {204, "", ""},
		"plain":   {200, `{"id":2,"name":""}`, "application/json"},
		"text":    {200, "hi", "text/plain; charset=utf-8"},
	}
	for kind, want := range cases {
		r := srv.Do(req("POST", "/v?kind="+kind))
		if r.Status != want.status || r.Text() != want.body || r.Get("content-type") != want.ctype {
			t.Errorf("%s: %d %q %s, want %d %q %s", kind, r.Status, r.Get("content-type"), r.Text(), want.status, want.ctype, want.body)
		}
	}
	if r := srv.Do(req("POST", "/v?kind=created")); r.Get("location") != "/v/1" || r.Get("x-a") != "1" {
		t.Errorf("the wrapper's Location and Header: %v", r.Header)
	}
}

func TestAStreamVariantOfAOneOfIsStreamedAsAStreamReturnedAlone(t *testing.T) {
	type answer = bites.OneOf2[bites.NotModified, bites.Stream]
	type fresh struct {
		Query struct{ Fresh bites.Opt[bool] }
	}
	srv := start(t, func(app *bites.App) {
		app.Get("/s", func(_ context.Context, in fresh) (answer, error) {
			if in.Query.Fresh.Or(false) {
				return answer{}.A(bites.NotModified{Header: bites.Header{"ETag": {`"v1"`}}}), nil
			}
			return answer{}.B(bites.Stream{Header: bites.Header{"Content-Type": {"text/plain"}}, Func: func(_ context.Context, w *bites.StreamWriter) error {
				_, err := w.Write([]byte("chunk"))
				return err
			}}), nil
		})
	})
	if r := srv.Do(req("GET", "/s")); r.Status != 200 || r.Text() != "chunk" || r.Get("content-type") != "text/plain" {
		t.Errorf("stream: %d %v %s", r.Status, r.Header, r.Text())
	}
	if r := srv.Do(req("GET", "/s?fresh=true")); r.Status != 304 || len(r.Body) != 0 || r.Get("etag") != `"v1"` {
		t.Errorf("not modified: %d %v %s", r.Status, r.Header, r.Text())
	}
}

func TestAOneOfWithNoVariantChosenAndNoErrorIsALogged500NamingTheOneOf(t *testing.T) {
	srv, logs := startLogged(t, func(app *bites.App) {
		app.Get("/v", func(context.Context, none) (bites.OneOf2[Item, bites.NoContent], error) {
			return bites.OneOf2[Item, bites.NoContent]{}, nil
		})
	})
	wantProblem(t, srv.Do(req("GET", "/v")), 500, `{"title":"Internal Server Error","status":500}`)
	if !strings.Contains(logs.String(), "the handler returned a OneOf2 with no variant chosen and no error: choose one with the method of its letter") ||
		!strings.Contains(logs.String(), "path=/v") {
		t.Fatalf("log: %s", logs)
	}
}

func TestAZeroOneOfReturnedWithAnErrorIsAnsweredByTheError(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/v", func(context.Context, none) (bites.OneOf2[Item, bites.NoContent], error) {
			return bites.OneOf2[Item, bites.NoContent]{}, bites.NewError(404, "no such item")
		})
	})
	if r := srv.Do(req("GET", "/v")); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestValueReturnsTheVariantChosenAndNilForAZeroOneOf(t *testing.T) {
	var two bites.OneOf2[Item, bites.NoContent]
	var three bites.OneOf3[Item, bites.NoContent, bites.Text]
	var four itemAnswer
	for _, c := range []struct {
		got, want any
	}{
		{two.Value(), nil}, {two.A(Item{ID: 1}).Value(), Item{ID: 1}}, {two.B(bites.NoContent{}).Value(), bites.NoContent{}},
		{three.Value(), nil}, {three.C(bites.Text{Body: "c"}).Value(), bites.Text{Body: "c"}},
		{four.Value(), nil}, {four.D(bites.Text{Body: "d"}).Value(), bites.Text{Body: "d"}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("Value() = %#v, want %#v", c.got, c.want)
		}
	}
}

// The OneOf and the interface with Returns of the document test: the same answers, said two ways.
type docAnswer = bites.OneOf3[bites.OK[docUser], bites.Created[docUser], docUser]

func TestAOneOfIsDescribedInTheDocumentExactlyAsReturnsDescribesTheSameTypes(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Put("/a/{id}", func(context.Context, GetUser) (docAnswer, error) { return docAnswer{}, nil })
		app.Put("/b/{id}", func(context.Context, GetUser) (any, error) { return nil, nil },
			bites.Returns[bites.OK[docUser]](), bites.Returns[bites.Created[docUser]](), bites.Returns[docUser]())
	})
	a, b := dig(doc, "paths", "/a/{id}", "put", "responses"), dig(doc, "paths", "/b/{id}", "put", "responses")
	if a == nil || jsonOf(a) != jsonOf(b) {
		t.Fatalf("OneOf: %s\nReturns: %s", jsonOf(a), jsonOf(b))
	}
	if dig(a, "201", "headers") != nil || dig(a, "200", "content", "application/json") == nil {
		t.Errorf("%s", jsonOf(a))
	}
}

func TestTwoVariantsOfOneStatusWithDifferentSchemasAreAOneOfInTheDocument(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/v", func(context.Context, none) (bites.OneOf2[docUser, bites.Text], error) {
			return bites.OneOf2[docUser, bites.Text]{}, errors.New("unused")
		})
		app.Get("/w", func(context.Context, none) (bites.OneOf2[docUser, bites.OK[Item]], error) {
			return bites.OneOf2[docUser, bites.OK[Item]]{}, errors.New("unused")
		})
	})
	if c := dig(doc, "paths", "/v", "get", "responses", "200", "content").(map[string]any); c["application/json"] == nil || c["text/plain"] == nil {
		t.Errorf("two media types of one status: %s", jsonOf(c))
	}
	if s := dig(doc, "paths", "/w", "get", "responses", "200", "content", "application/json", "schema", "oneOf"); s == nil {
		t.Errorf("two schemas of one status: %s", jsonOf(dig(doc, "paths", "/w", "get", "responses")))
	}
}
