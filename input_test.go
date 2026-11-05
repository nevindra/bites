package bites_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/testuuid"
)

func req(method, target string) bitetest.Request {
	return bitetest.Request{Method: method, Target: target}
}

func jsonReq(method, target, body string) bitetest.Request {
	return bitetest.Request{Method: method, Target: target, Body: []byte(body), Header: map[string][]string{"Content-Type": {"application/json"}}}
}

func wantProblem(t *testing.T, r bitetest.Response, status int, body string) {
	t.Helper()
	if r.Status != status || r.Get("content-type") != "application/problem+json" || r.Text() != body {
		t.Fatalf("got %d %s %s\nwant %d %s", r.Status, r.Get("content-type"), r.Text(), status, body)
	}
}

// echo returns its whole Input as JSON, so a test sees what was decoded.
func echo[In any](_ context.Context, in In) (In, error) { return in, nil }

type Search struct {
	Query struct {
		Q        string
		Page     int
		PageSize bites.Opt[int]
		Tags     []string
		Ids      bites.Opt[[]int]
		Since    bites.Opt[time.Time]
		Ref      bites.Opt[uuid.UUID]
		Sort     bites.Opt[Order]
	}
}

type Order string

func (Order) Values() []Order { return []Order{"asc", "desc"} }

func TestQueryValuesAreDecodedByTheTypeOfTheirField(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/search", echo[Search]) })
	r := srv.Do(req("GET", "/search?q=caf%C3%A9+au+lait&page=2&tags=a&tags=b%20c&ids=1&ids=2&since=2026-01-02T03:04:05%2B07:00&ref=6ba7b810-9dad-11d1-80b4-00c04fd430c8&sort=desc&page_size=10"))
	want := `{"query":{"q":"café au lait","page":2,"page_size":10,"tags":["a","b c"],"ids":[1,2],"since":"2026-01-02T03:04:05+07:00","ref":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","sort":"desc"}}`
	if r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s\nwant %s", r.Status, r.Text(), want)
	}
}

func TestEveryInputMistakeIsListedInOneAnswerWithWireNames(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/search", echo[Search]) })
	r := srv.Do(req("GET", "/search?page=x&page_size=&sort=up&since=2026-01-02&tags=a&ids=1&ids=z&q=a&q=b"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.page","message":"expected an integer","rule":"type"},`+
		`{"location":"query.page_size","message":"expected an integer","rule":"type"},`+
		`{"location":"query.sort","message":"expected one of: asc, desc","rule":"values"},`+
		`{"location":"query.since","message":"expected an RFC 3339 time with an offset","rule":"type"},`+
		`{"location":"query.ids[1]","message":"expected an integer","rule":"type"},`+
		`{"location":"query.q","message":"sent more than once"}]}`)
}

func TestAMissingRequiredQueryFieldIsRequiredAndAMissingOptionalOneIsNot(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/search", echo[Search]) })
	r := srv.Do(req("GET", "/search"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.q","message":"required","rule":"required"},{"location":"query.page","message":"required","rule":"required"},{"location":"query.tags","message":"required","rule":"required"}]}`)
}

func TestAListOfInputErrorsStopsAtTwentyAndSaysItWasTruncated(t *testing.T) {
	type many struct {
		Body []struct{ N int }
	}
	srv := start(t, func(app *bites.App) { app.Post("/many", echo[many]) })
	items := strings.Repeat(`{"N":"x"},`, 30)
	r := srv.Do(jsonReq("POST", "/many", "["+strings.TrimSuffix(items, ",")+"]"))
	if r.Status != 400 || !strings.HasSuffix(r.Text(), `],"truncated":true}`) || strings.Count(r.Text(), `"location"`) != 20 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestPathQueryAndBodyErrorsShareTheCapAndComeInSectionOrder(t *testing.T) {
	type in struct {
		Path  struct{ ID int64 }
		Query struct{ Page int }
		Body  struct{ Items []struct{ Qty int } }
	}
	srv := start(t, func(app *bites.App) { app.Post("/o/{id}", echo[in]) })
	r := srv.Do(jsonReq("POST", "/o/z?page=q", `{"items":[{"qty":1},{"qty":"x"}]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"path.id","message":"expected an integer","rule":"type"},`+
		`{"location":"query.page","message":"expected an integer","rule":"type"},`+
		`{"location":"body.items[1].qty","message":"expected an integer","rule":"type"}]}`)
}

func TestAPathParamIsPercentDecodedAndABadEscapeIsA400(t *testing.T) {
	type in struct{ Path struct{ Name string } }
	srv := start(t, func(app *bites.App) { app.Get("/f/{name}", echo[in]) })
	r := srv.Do(req("GET", "/f/a%2Fb+c"))
	if r.Text() != `{"path":{"name":"a/b+c"}}` {
		t.Fatalf("%s", r.Text())
	}
	wantProblem(t, srv.Do(req("GET", "/f/a%zz")), 400, `{"title":"Bad Request","status":400,"errors":[{"location":"path.name","message":"expected a valid percent-encoded value"}]}`)
}

func TestARestWildcardIsOneStringWithItsSlashes(t *testing.T) {
	type in struct{ Path struct{ Rest string } }
	srv := start(t, func(app *bites.App) { app.Get("/static/{rest...}", echo[in]) })
	if r := srv.Do(req("GET", "/static/a/b/c.txt")); r.Text() != `{"path":{"rest":"a/b/c.txt"}}` {
		t.Fatalf("%s", r.Text())
	}
}

func TestHeadersAreMatchedByTheirCanonicalNameInAnyCase(t *testing.T) {
	type in struct {
		Header struct {
			RequestID   string
			IfNoneMatch bites.Opt[string]
			Accept      []string
		}
	}
	srv := start(t, func(app *bites.App) { app.Get("/h", echo[in]) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/h", Header: map[string][]string{"x-ignored": {"1"}, "REQUEST-id": {" abc "}}})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"header.Accept","message":"required","rule":"required"}]}`)
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/h", Header: map[string][]string{"Request-Id": {" abc "}, "if-none-match": {`"v1"`}, "Accept": {"text/html"}}})
	if want := `{"header":{"request_id":"abc","if_none_match":"\"v1\"","accept":["text/html"]}}`; r.Text() != want {
		t.Fatalf("%s\nwant %s", r.Text(), want)
	}
}

func TestAHeaderFieldXAPIKeyIsReadFromXAPIKeyAndItsErrorIsLocatedThere(t *testing.T) {
	type in struct {
		Header struct{ XAPIKey string }
	}
	srv := start(t, func(app *bites.App) { app.Get("/k", echo[in]) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/k"})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"header.X-Api-Key","message":"required","rule":"required"}]}`)
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/k", Header: map[string][]string{"X-API-Key": {"k1"}}})
	if want := `{"header":{"xapi_key":"k1"}}`; r.Text() != want {
		t.Fatalf("%s\nwant %s", r.Text(), want)
	}
}

func TestACookieIsReadByNameAndARepeatedOneIsA400(t *testing.T) {
	type in struct {
		Cookie struct {
			SessionID string
			Theme     bites.Opt[string]
		}
	}
	srv := start(t, func(app *bites.App) { app.Get("/c", echo[in]) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/c", Header: map[string][]string{"Cookie": {`theme="dark"; session_id=s1; other=1`}}})
	if want := `{"cookie":{"session_id":"s1","theme":"dark"}}`; r.Text() != want {
		t.Fatalf("%s", r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/c", Header: map[string][]string{"Cookie": {`session_id=a; session_id=b`}}})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"cookie.session_id","message":"sent more than once"}]}`)
}

func TestTheNamingPolicyOfTheAppRenamesPathQueryCookiesAndBody(t *testing.T) {
	type in struct {
		Query struct{ PageSize int }
		Body  struct{ FirstName string }
	}
	srv := startWith(t, func(app *bites.App) { app.Post("/n", echo[in]) }, bites.WithNaming(bites.CamelCase))
	r := srv.Do(jsonReq("POST", "/n?pageSize=3", `{"firstName":"Ada"}`))
	if want := `{"query":{"pageSize":3},"body":{"firstName":"Ada"}}`; r.Text() != want {
		t.Fatalf("%s", r.Text())
	}
}

type named struct{ Page int }

func (named) WireNames() map[string]string { return map[string]string{"Page": "$page"} }

func TestWireNamesGivesANameNoRuleProduces(t *testing.T) {
	type in struct{ Query named }
	srv := start(t, func(app *bites.App) { app.Get("/w", echo[in]) })
	if r := srv.Do(req("GET", "/w?%24page=4")); r.Text() != `{"query":{"page":4}}` {
		t.Fatalf("%s", r.Text())
	}
}

type Embedded struct{ Page int }

func TestAnEmbeddedStructInASectionIsInlined(t *testing.T) {
	type in struct {
		Query struct {
			Embedded
			Q string
		}
	}
	srv := start(t, func(app *bites.App) { app.Get("/e", echo[in]) })
	if r := srv.Do(req("GET", "/e?page=2&q=x")); r.Status != 200 || !strings.Contains(r.Text(), `"page":2`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

type filterNames struct{ Kind string }

func (filterNames) WireNames() map[string]string { return map[string]string{"Kind": "k"} }

type pagingNames struct{ PerPage int }

func (pagingNames) WireNames() map[string]string { return map[string]string{"PerPage": "pp"} }

type bothNamed struct {
	filterNames
	pagingNames
}

type outerNamed struct {
	filterNames
	pagingNames
}

func (outerNamed) WireNames() map[string]string { return map[string]string{"PerPage": "size"} }

type oneNamed struct {
	pagingNames
	Q string
}

func TestEachEmbeddedStructInASectionNamesItsOwnFieldsWhenBothHaveWireNames(t *testing.T) {
	type in struct{ Query bothNamed }
	srv := start(t, func(app *bites.App) { app.Get("/b", echo[in]) })
	if r := srv.Do(req("GET", "/b?k=a&pp=5")); r.Text() != `{"query":{"kind":"a","per_page":5}}` {
		t.Fatalf("%s", r.Text())
	}
}

func TestTheSectionsOwnWireNamesWinOverAnEmbeddedStructs(t *testing.T) {
	type in struct{ Query outerNamed }
	srv := start(t, func(app *bites.App) { app.Get("/o", echo[in]) })
	if r := srv.Do(req("GET", "/o?k=a&size=7")); r.Text() != `{"query":{"kind":"a","per_page":7}}` {
		t.Fatalf("%s", r.Text())
	}
	if r := srv.Do(req("GET", "/o?k=a&pp=7")); r.Status != 400 {
		t.Fatalf("the embedded name was still read: %d %s", r.Status, r.Text())
	}
}

func TestAWireNamesPromotedFromTheOnlyEmbeddedStructNamesThatStructsFields(t *testing.T) {
	type in struct{ Query oneNamed }
	srv := start(t, func(app *bites.App) { app.Get("/p", echo[in]) })
	if r := srv.Do(req("GET", "/p?pp=2&q=x")); r.Text() != `{"query":{"per_page":2,"q":"x"}}` {
		t.Fatalf("%s", r.Text())
	}
}

type Create struct {
	Body struct {
		Name  string
		Email bites.Opt[string]
		Bio   bites.Null[string]
		Items []struct{ Sku string }
	}
}

func TestAJSONBodyIsDecodedWithWireNamesAndPresence(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create]) })
	r := srv.Do(jsonReq("POST", "/c", `{"name":"Ada","bio":null,"items":[{"sku":"a"},{"sku":"b"}]}`))
	if want := `{"body":{"name":"Ada","bio":null,"items":[{"sku":"a"},{"sku":"b"}]}}`; r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s\nwant %s", r.Status, r.Text(), want)
	}
	r = srv.Do(jsonReq("POST", "/c", `{"name":"Ada","email":"e","bio":"x","items":[],"extra":1}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.extra","message":"unknown field"}]}`)
	r = srv.Do(jsonReq("POST", "/c", `{"name":"Ada"`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"unexpected end of JSON at byte 13"}]}`)
	r = srv.Do(jsonReq("POST", "/c", `{"name":null,"bio":null,"items":[]}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.name","message":"must not be null"}]}`)
}

func TestARouteMayAcceptUnknownMembers(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create], bites.AcceptUnknownMembers()) })
	if r := srv.Do(jsonReq("POST", "/c", `{"name":"Ada","bio":null,"items":[],"extra":1}`)); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestABodyWithoutAJSONContentTypeIsA415EvenWhenItIsMissing(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create]) })
	for name, hdr := range map[string]map[string][]string{"none": nil, "text": {"Content-Type": {"text/plain"}}, "form": {"Content-Type": {"application/x-www-form-urlencoded"}}} {
		r := srv.Do(bitetest.Request{Method: "POST", Target: "/c", Body: []byte(`{}`), Header: hdr})
		wantProblem(t, r, 415, `{"title":"Unsupported Media Type","status":415,"detail":"the body must be sent as application/json"}`)
		_ = name
	}
	for _, ct := range []string{"application/json", "Application/JSON; charset=utf-8", "application/vnd.api+json"} {
		r := srv.Do(bitetest.Request{Method: "POST", Target: "/c", Body: []byte(`{"name":"a","bio":null,"items":[]}`), Header: map[string][]string{"Content-Type": {ct}}})
		if r.Status != 200 {
			t.Errorf("%s: %d %s", ct, r.Status, r.Text())
		}
	}
}

func TestARequiredBodyThatIsNotSentIsA400NamingBody(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create]) })
	wantProblem(t, srv.Do(req("POST", "/c")), 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body","message":"required","rule":"required"}]}`)
}

type Restore struct {
	Path struct{ ID int64 }
	Body bites.Opt[struct {
		Name bites.Opt[string]
		Note string
	}]
}

func restore(_ context.Context, in Restore) (string, error) {
	b, ok := in.Body.Get()
	if !ok {
		return fmt.Sprintf("%d: no body", in.Path.ID), nil
	}
	return fmt.Sprintf("%d: %s %q", in.Path.ID, b.Name.Or("-"), b.Note), nil
}

func TestAnOptBodyThatIsNotSentIsUnsetAndOneThatIsSentIsReadAsAnyBody(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/spaces/{id}/restore", restore) })
	if r := srv.Do(req("POST", "/spaces/7/restore")); r.Status != 200 || r.Text() != `"7: no body"` {
		t.Fatalf("no body: %d %s", r.Status, r.Text())
	}
	if r := srv.Do(jsonReq("POST", "/spaces/7/restore", `{"name":"ops","note":"back"}`)); r.Status != 200 || r.Text() != `"7: ops \"back\""` {
		t.Fatalf("a body: %d %s", r.Status, r.Text())
	}
	// A body that is sent is the contract of a write: its required fields are still required.
	wantProblem(t, srv.Do(jsonReq("POST", "/spaces/7/restore", `{}`)), 400, `{"title":"Bad Request","status":400,"errors":[{"location":"body.note","message":"required","rule":"required"}]}`)
	wantProblem(t, srv.Do(bitetest.Request{Method: "POST", Target: "/spaces/7/restore", Body: []byte("x")}), 415, `{"title":"Unsupported Media Type","status":415,"detail":"the body must be sent as application/json"}`)
}

func TestTheAPIDocumentSaysAnOptBodyIsNotRequired(t *testing.T) {
	app := bites.New()
	app.Post("/spaces/{id}/restore", restore)
	app.Post("/c", echo[Create])
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	doc, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Required bool
				Content  map[string]struct{ Schema map[string]any }
			}
		}
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	rb := d.Paths["/spaces/{id}/restore"]["post"].RequestBody
	if rb.Required || rb.Content["application/json"].Schema == nil {
		t.Fatalf("an Opt body: %+v", rb)
	}
	if !d.Paths["/c"]["post"].RequestBody.Required {
		t.Fatal("a plain body is no longer required")
	}
}

func TestABodyOverTheRoutesLimitIsA413AndTheConnectionCloses(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create], bites.MaxBody(10)) })
	r := srv.Do(jsonReq("POST", "/c", `{"name":"Ada","bio":null,"items":[]}`))
	wantProblem(t, r, 413, `{"title":"Content Too Large","status":413,"detail":"request body too large: this route reads at most 10 bytes"}`)
}

func TestTheBodyLimitOfTheAppAppliesToEveryRoute(t *testing.T) {
	srv := startWith(t, func(app *bites.App) { app.Post("/c", echo[Create]) }, bites.WithLimits(bites.Limits{MaxBody: 10}))
	if r := srv.Do(jsonReq("POST", "/c", `{"name":"Ada","bio":null,"items":[]}`)); r.Status != 413 {
		t.Fatalf("%d", r.Status)
	}
}

func TestAChunkedBodyIsDecodedLikeAnyOther(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/c", echo[Create]) })
	c := srv.Dial("203.0.113.9:1")
	body := `{"name":"Ada","bio":null,"items":[]}`
	fmt.Fprintf(c, "POST /c HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n", len(body), body)
	if r := srv.Read(c, false); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestABytesBodyIsTheRequestAsSentWhateverItsContentType(t *testing.T) {
	type in struct{ Body []byte }
	srv := start(t, func(app *bites.App) {
		app.Post("/b", func(_ context.Context, in in) (bites.Created[int], error) {
			b := append(in.Body, "!!!!!!!!!!"...) // must not write into the text after it
			return bites.Created[int]{Body: len(b)}, nil
		})
	})
	r := srv.Do(nilNoCT("POST", "/b", "hello"))
	if r.Status != 201 || r.Text() != "15" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(req("POST", "/b")); r.Status != 201 || r.Text() != "10" {
		t.Fatalf("an empty []byte body is allowed: %d %s", r.Status, r.Text())
	}
}

func nilNoCT(method, target, body string) bitetest.Request {
	return bitetest.Request{Method: method, Target: target, Body: []byte(body)}
}

func TestRawKeepsTheExactBytesBesideTheDecodedValue(t *testing.T) {
	type payload struct{ Event string }
	type in struct{ Body bites.Raw[payload] }
	srv := start(t, func(app *bites.App) {
		app.Post("/hook", func(_ context.Context, in in) (map[string]string, error) {
			return map[string]string{"event": in.Body.Value.Event, "raw": string(in.Body.Bytes())}, nil
		})
	})
	raw := `{ "event" :"push" }`
	r := srv.Do(jsonReq("POST", "/hook", raw))
	if want := `{"event":"push","raw":"{ \"event\" :\"push\" }"}`; r.Text() != want {
		t.Fatalf("%s", r.Text())
	}
}

func TestPresenceInAPatchBodyTellsAbsentFromNullFromAValue(t *testing.T) {
	type in struct {
		Body struct {
			Nick bites.Opt[bites.Null[string]]
		}
	}
	srv := start(t, func(app *bites.App) {
		app.Patch("/p", func(_ context.Context, in in) (string, error) {
			v, set := in.Body.Nick.Get()
			switch {
			case !set:
				return "leave", nil
			case v.IsNull():
				return "clear", nil
			}
			s, _ := v.Get()
			return "set " + s, nil
		})
	})
	for body, want := range map[string]string{`{}`: `"leave"`, `{"nick":null}`: `"clear"`, `{"nick":"z"}`: `"set z"`} {
		if r := srv.Do(jsonReq("PATCH", "/p", body)); r.Text() != want {
			t.Errorf("%s: %s, want %s", body, r.Text(), want)
		}
	}
}

func TestAStringInAnInputOutlivesItsRequest(t *testing.T) {
	type in struct {
		Query struct{ Name string }
		Body  struct{ Note string }
	}
	var kept []string
	srv := start(t, func(app *bites.App) {
		app.Post("/k", func(_ context.Context, in in) (struct{}, error) {
			kept = append(kept, in.Query.Name, in.Body.Note)
			return struct{}{}, nil
		})
	})
	for i := range 50 {
		srv.Do(jsonReq("POST", fmt.Sprintf("/k?name=n%d", i), fmt.Sprintf(`{"note":"note %d é"}`, i)))
	}
	for i := range 50 {
		if kept[2*i] != fmt.Sprintf("n%d", i) || kept[2*i+1] != fmt.Sprintf("note %d é", i) {
			t.Fatalf("request %d: %q %q", i, kept[2*i], kept[2*i+1])
		}
	}
}

type nestedList struct {
	Body struct {
		A struct{ A struct{ Pals []string } }
	}
}

// The connection's goroutine starts with a small stack, which grows while the first requests are
// decoded; a pointer to the Input carried as a uintptr across that move named the old stack, and
// this list came back nil with a 500 for about six requests in nine (unsafe.go, noescape).
func TestAListNestedInTheBodyIsDecodedWhileTheConnectionsStackGrows(t *testing.T) {
	for range 5 {
		srv := start(t, func(app *bites.App) { app.Post("/p", echo[nestedList]) })
		for range 3 {
			r := srv.Do(jsonReq("POST", "/p", `{"a":{"a":{"pals":["x","y"]}}}`))
			if r.Status != 200 || r.Text() != `{"body":{"a":{"a":{"pals":["x","y"]}}}}` {
				t.Fatalf("%d %s", r.Status, r.Text())
			}
		}
	}
}

// thirdPartyID stands for another module's uuid type, which the core cannot import: sixteen bytes
// with a text form of 32 hex digits.
type thirdPartyID [16]byte

func (id thirdPartyID) MarshalText() ([]byte, error) { return []byte(hex.EncodeToString(id[:])), nil }
func (id *thirdPartyID) UnmarshalText(b []byte) error {
	if len(b) != 32 {
		return fmt.Errorf("secret %q is not an id", b) // would leak the value if it ever reached the client
	}
	_, err := hex.Decode(id[:], b)
	return err
}

type idIn struct {
	Path  struct{ ID thirdPartyID }
	Query struct{ Parent bites.Opt[thirdPartyID] }
	Body  struct{ Owner thirdPartyID }
}

type idOut struct {
	ID    thirdPartyID `json:"id"`
	Owner thirdPartyID `json:"owner"`
}

func idHandler(_ context.Context, in idIn) (idOut, error) {
	return idOut{ID: in.Path.ID, Owner: in.Body.Owner}, nil
}

const sampleHexID = "000102030405060708090a0b0c0d0e0f"

func TestATypeRegisteredWithATextFormatIsReadFromPathQueryAndBodyAndWrittenAsText(t *testing.T) {
	srv := startWith(t, func(app *bites.App) { app.Post("/things/{id}", idHandler) }, bites.WithTextFormat[thirdPartyID]("uuid"))
	r := srv.Do(jsonReq("POST", "/things/"+sampleHexID+"?parent="+sampleHexID, `{"owner":"`+sampleHexID+`"}`))
	if want := `{"id":"` + sampleHexID + `","owner":"` + sampleHexID + `"}`; r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s\nwant %s", r.Status, r.Text(), want)
	}
}

func TestARegisteredTextTypeThatDoesNotDecodeIsToldTheFormatsSentenceAndNeverItsOwnErrorText(t *testing.T) {
	srv := startWith(t, func(app *bites.App) { app.Post("/things/{id}", idHandler) }, bites.WithTextFormat[thirdPartyID]("uuid"))
	r := srv.Do(jsonReq("POST", "/things/short?parent=also-short", `{"owner":"tiny"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"path.id","message":"expected a UUID","rule":"type"},`+
		`{"location":"query.parent","message":"expected a UUID","rule":"type"},`+
		`{"location":"body.owner","message":"expected a UUID","rule":"type"}]}`)
}

func TestWithoutARegistrationTheSameTypeSaysExpectedAValidValue(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/things/{id}", idHandler) })
	r := srv.Do(jsonReq("POST", "/things/short", `{"owner":"`+sampleHexID+`"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected a valid value","rule":"type"}]}`)
}

func TestARegisteredTextTypeIsDocumentedAsAStringWithItsFormatInRequestsAndResponses(t *testing.T) {
	srv := startWith(t, func(app *bites.App) {
		app.Post("/things/{id}", idHandler)
		app.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
	}, bites.WithTextFormat[thirdPartyID]("uuid"))
	var doc map[string]any
	if err := json.Unmarshal(srv.Do(req("GET", "/openapi.json")).Body, &doc); err != nil {
		t.Fatal(err)
	}
	want := `{"format":"uuid","type":"string"}`
	params, _ := dig(doc, "paths", "/things/{id}", "post", "parameters").([]any)
	for _, p := range params {
		if got := jsonOf(dig(p, "schema")); got != want {
			t.Errorf("%s: %s, want %s", jsonOf(p), got, want)
		}
	}
	if len(params) != 2 {
		t.Fatalf("%d parameters", len(params))
	}
	comps := dig(doc, "components", "schemas")
	if s := jsonOf(comps); !strings.Contains(s, `"owner":{"format":"uuid","type":"string"}`) || !strings.Contains(s, `"id":{"format":"uuid","type":"string"}`) {
		t.Errorf("components: %s", s)
	}
}

func TestADateIsReadFromEverySectionAndWrittenAsYYYYMMDD(t *testing.T) {
	type in struct {
		Path  struct{ Day bites.Date }
		Query struct{ From bites.Opt[bites.Date] }
		Body  struct{ Due bites.Date }
	}
	type out struct {
		Day  bites.Date `json:"day"`
		From string     `json:"from"`
		Due  bites.Date `json:"due"`
	}
	srv := start(t, func(app *bites.App) {
		app.Post("/d/{day}", func(_ context.Context, i in) (out, error) {
			return out{i.Path.Day, i.Query.From.Or(bites.Date{}).String(), i.Body.Due}, nil
		})
	})
	r := srv.Do(jsonReq("POST", "/d/2026-11-03?from=2024-02-29", `{"due":"2027-01-31"}`))
	if want := `{"day":"2026-11-03","from":"2024-02-29","due":"2027-01-31"}`; r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestADateThatIsATimestampOrImpossibleIsOneFixedSentenceInEverySectionAndQuotesNothing(t *testing.T) {
	type in struct {
		Path  struct{ Day bites.Date }
		Query struct{ From bites.Date }
		Body  struct{ Due bites.Date }
	}
	srv := start(t, func(app *bites.App) { app.Post("/d/{day}", echo[in]) })
	r := srv.Do(jsonReq("POST", "/d/2026-02-30?from=2026-11-03T00:00:00Z", `{"due":"hunter2"}`))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"path.day","message":"expected a date as YYYY-MM-DD","rule":"type"},`+
		`{"location":"query.from","message":"expected a date as YYYY-MM-DD","rule":"type"},`+
		`{"location":"body.due","message":"expected a date as YYYY-MM-DD","rule":"type"}]}`)
}

func TestADateIsDocumentedAsAStringWithTheDateFormat(t *testing.T) {
	type in struct {
		Query struct{ From bites.Opt[bites.Date] }
	}
	type out struct {
		Due bites.Date `json:"due"`
	}
	doc := docOf(t, func(app *bites.App) {
		app.Get("/d", func(context.Context, in) (out, error) { return out{}, nil })
		app.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
	})
	if got := jsonOf(dig(doc, "paths", "/d", "get", "parameters")); got != `[{"in":"query","name":"from","required":false,"schema":{"format":"date","type":"string"}}]` {
		t.Errorf("parameters %s", got)
	}
	if got := jsonOf(dig(doc, "components", "schemas")); !strings.Contains(got, `"due":{"format":"date","type":"string"}`) {
		t.Errorf("components %s", got)
	}
}

type filterIn struct {
	Query struct {
		Kind bites.Opt[string]
		Page bites.Opt[int]
		Tags bites.Opt[[]string]
	}
}

func rejectingQuery(opts ...bites.RouteOption) func(*bites.App) {
	return func(app *bites.App) { app.Get("/f", echo[filterIn], opts...) }
}

func TestAnUnknownQueryKeyIsIgnoredByDefault(t *testing.T) {
	srv := start(t, rejectingQuery())
	if r := srv.Do(req("GET", "/f?knid=government&tags=a")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAnUnknownQueryKeyIsA400NamingItWhenTheRouteRejectsThem(t *testing.T) {
	srv := start(t, rejectingQuery(bites.RejectUnknownQueryKeys()))
	r := srv.Do(req("GET", "/f?knid=government&tags=a&kind=x&utm=1&knid=again"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[`+
		`{"location":"query.knid","message":"unknown query parameter; did you mean \"kind\"?; this route takes kind, page, tags"},`+
		`{"location":"query.utm","message":"unknown query parameter; this route takes kind, page, tags"},`+
		`{"location":"query.knid","message":"unknown query parameter; did you mean \"kind\"?; this route takes kind, page, tags"}]}`)
	if r := srv.Do(req("GET", "/f?kind=x&page=2&tags=a&tags=b")); r.Status != 200 {
		t.Fatalf("known keys are refused: %d %s", r.Status, r.Text())
	}
}

func TestAnUnknownQueryKeyIsReportedOnceEvenWhenTheSectionHasAList(t *testing.T) {
	srv := start(t, rejectingQuery(bites.RejectUnknownQueryKeys()))
	r := srv.Do(req("GET", "/f?tags=a&tags=b&oops=1"))
	if n := strings.Count(r.Text(), "query.oops"); r.Status != 400 || n != 1 {
		t.Fatalf("%d, %d reports: %s", r.Status, n, r.Text())
	}
}

func TestAnUnknownQueryKeyIsEchoedOnlyAsTheKeyAndCutAtSixtyFourBytesAndAValueIsNeverEchoed(t *testing.T) {
	srv := start(t, rejectingQuery(bites.RejectUnknownQueryKeys()))
	r := srv.Do(req("GET", "/f?"+strings.Repeat("k", 200)+"=hunter2&caf%C3%A9=hunter2"))
	if strings.Contains(r.Text(), "hunter2") || strings.Contains(r.Text(), strings.Repeat("k", 65)) || !strings.Contains(r.Text(), "query.café") {
		t.Fatalf("%s", r.Text())
	}
}

func TestABrokenPercentEscapeInAQueryKeyIsA400OnlyWhenUnknownKeysAreRejected(t *testing.T) {
	if r := start(t, rejectingQuery()).Do(req("GET", "/f?%zz=1")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r := start(t, rejectingQuery(bites.RejectUnknownQueryKeys())).Do(req("GET", "/f?%zz=1"))
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"query","message":"expected a valid percent-encoded value"}]}`)
}

func TestAGroupRejectsUnknownQueryKeysAndARouteInsideItMayAcceptThemAgain(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.RejectUnknownQueryKeys())
		g.Get("/strict", echo[filterIn])
		g.Get("/loose", echo[filterIn], bites.AcceptUnknownQueryKeys())
	})
	if r := srv.Do(req("GET", "/g/strict?oops=1")); r.Status != 400 {
		t.Errorf("strict: %d", r.Status)
	}
	if r := srv.Do(req("GET", "/g/loose?oops=1")); r.Status != 200 {
		t.Errorf("loose: %d", r.Status)
	}
}

func TestTheAppWideSwitchRejectsUnknownKeysOnEveryRouteIncludingOnesWithNoQuerySectionAndARouteMayOptOut(t *testing.T) {
	srv := startWith(t, func(app *bites.App) {
		app.Get("/f", echo[filterIn])
		app.Get("/none", echo[Empty])
		app.Get("/loose", echo[Empty], bites.AcceptUnknownQueryKeys())
	}, bites.WithRejectUnknownQueryKeys())
	for target, want := range map[string]int{"/f?oops=1": 400, "/none?oops=1": 400, "/none": 200, "/loose?oops=1": 200, "/f?kind=a": 200} {
		if r := srv.Do(req("GET", target)); r.Status != want {
			t.Errorf("%s: %d %s, want %d", target, r.Status, r.Text(), want)
		}
	}
}

func TestARouteRejectingUnknownKeysOverridesNothingWhenTheAppAlreadyDoesAndTheOtherWayRound(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		g := app.Group("/g", bites.RejectUnknownQueryKeys())
		g.Group("/inner", bites.AcceptUnknownQueryKeys()).Get("/f", echo[filterIn])
	})
	if r := srv.Do(req("GET", "/g/inner/f?oops=1")); r.Status != 200 {
		t.Errorf("the innermost option did not win: %d", r.Status)
	}
}

type accountIn struct {
	Query struct {
		UserAccessID bites.Opt[string] `json:"user_access_id"`
		Limit        bites.Opt[int]
	}
}

func queryMessage(t *testing.T, srv *bitetest.Server, target string) string {
	t.Helper()
	r := srv.Do(req("GET", target))
	var p struct{ Errors []struct{ Message string } }
	if err := json.Unmarshal([]byte(r.Text()), &p); err != nil || r.Status != 400 || len(p.Errors) != 1 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	return p.Errors[0].Message
}

func TestAnUnknownQueryKeyThatDiffersOnlyInCaseOrSeparatorsNamesTheDeclaredKey(t *testing.T) {
	srv := start(t, rejectingQuery(bites.RejectUnknownQueryKeys()))
	srv2 := start(t, func(app *bites.App) { app.Get("/f", echo[accountIn], bites.RejectUnknownQueryKeys()) })
	for _, k := range []string{"userAccessId", "user-access-id", "USER_ACCESS_ID", "useraccessid"} {
		want := "unknown query parameter; did you mean \"user_access_id\"?; this route takes user_access_id, limit"
		if got := queryMessage(t, srv2, "/f?"+k+"=1"); got != want {
			t.Errorf("%s: %q", k, got)
		}
	}
	_ = srv
}

func TestAnUnknownQueryKeyFarFromEveryDeclaredOneOnlyListsWhatTheRouteTakes(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/f", echo[accountIn], bites.RejectUnknownQueryKeys()) })
	if got, want := queryMessage(t, srv, "/f?account_id=1"), "unknown query parameter; this route takes user_access_id, limit"; got != want {
		t.Fatalf("%q", got)
	}
	// One edit from a short key is near; a different short word is not.
	if got := queryMessage(t, srv, "/f?limt=1"); !strings.Contains(got, `did you mean "limit"?`) {
		t.Errorf("a missing letter: %q", got)
	}
	if got := queryMessage(t, srv, "/f?lamp=1"); strings.Contains(got, "did you mean") {
		t.Errorf("two edits from a five-letter key is another parameter: %q", got)
	}
}

func TestAnUnknownQueryKeyOnARouteWithoutAQuerySectionSaysItTakesNone(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/f", echo[Empty], bites.RejectUnknownQueryKeys()) })
	if got, want := queryMessage(t, srv, "/f?a=1"), "unknown query parameter; this route takes no query parameters"; got != want {
		t.Fatalf("%q", got)
	}
}

func TestTheListOfDeclaredQueryKeysInAnUnknownKeysMessageIsCapped(t *testing.T) {
	type wide struct {
		Query struct{ A1, A2, A3, A4, A5, A6, A7, A8, A9, A10 bites.Opt[string] }
	}
	srv := start(t, func(app *bites.App) { app.Get("/f", echo[wide], bites.RejectUnknownQueryKeys()) })
	want := "unknown query parameter; this route takes a1, a2, a3, a4, a5, a6, a7, a8, and 2 more"
	if got := queryMessage(t, srv, "/f?zzzzzzzzzz=1"); got != want {
		t.Fatalf("%q", got)
	}
}

func TestAnUnknownQueryKeyIsReportedOnlyAfterTheRoutesMiddlewareHasLetTheRequestIn(t *testing.T) {
	deny := func(ctx context.Context, r bites.Request, next bites.Next) (bites.Response, error) {
		if r.Header("Authorization") == "" {
			return bites.Problem(401, "sign in"), nil
		}
		return next.Call(ctx), nil
	}
	srv := start(t, func(app *bites.App) {
		app.Get("/f", echo[accountIn], bites.RejectUnknownQueryKeys(), bites.Use(deny))
	})
	r := srv.Do(req("GET", "/f?account_id=1"))
	if r.Status != 401 || strings.Contains(r.Text(), "user_access_id") {
		t.Fatalf("an anonymous client learns no parameter name: %d %s", r.Status, r.Text())
	}
}

// The Inputs of ADR 0041's defaults: a Section's Defaults method states what an omitted field means.
type (
	dfltPage  int
	dfltOrder string
	dfltMode  string
)

func (dfltPage) Range() (int64, int64) { return 1, 100 }
func (dfltOrder) Values() []dfltOrder  { return []dfltOrder{"risk", "due", "name"} }
func (dfltMode) Length() (int, int)    { return 3, 8 }

type dfltQuery struct {
	Limit    dfltPage
	Sort     dfltOrder
	Archived bool
	Q        bites.Opt[string]
	Owner    uuid.UUID
}

func (dfltQuery) Defaults() map[string]string {
	return map[string]string{"Limit": "50", "Sort": "risk", "Archived": "false"}
}

type dfltIn struct {
	Query  dfltQuery
	Header struct{ AcceptLanguage string }
	Cookie struct{ Theme string }
}

func (dfltIn) Validate() error { return nil }

func TestAnOmittedFieldTakesTheDefaultItsSectionStatesAndAFieldSentKeepsTheClientsValue(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/x", echo[dfltIn]) })
	owner := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	r := srv.Do(req("GET", "/x?owner="+owner+"&header=x"))
	if r.Status != 400 {
		t.Fatalf("the header and cookie have no default, so they are still required: %d %s", r.Status, r.Text())
	}
	hdr := map[string][]string{"Accept-Language": {"en"}, "Cookie": {"theme=dark"}}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/x?owner=" + owner, Header: hdr})
	want := `{"query":{"limit":50,"sort":"risk","archived":false,"owner":"` + owner + `"},"header":{"accept_language":"en"},"cookie":{"theme":"dark"}}`
	if r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s\nwant %s", r.Status, r.Text(), want)
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/x?owner=" + owner + "&limit=7&sort=due&archived=true&q=a", Header: hdr})
	want = `{"query":{"limit":7,"sort":"due","archived":true,"q":"a","owner":"` + owner + `"},"header":{"accept_language":"en"},"cookie":{"theme":"dark"}}`
	if r.Status != 200 || r.Text() != want {
		t.Fatalf("%d %s\nwant %s", r.Status, r.Text(), want)
	}
}

func TestAFieldSentWithABadValueIsA400AndNeverFallsBackToItsDefault(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/x", echo[dfltIn]) })
	hdr := map[string][]string{"Accept-Language": {"en"}, "Cookie": {"theme=dark"}}
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/x?owner=6ba7b810-9dad-11d1-80b4-00c04fd430c8&limit=&sort=up&limit=3", Header: hdr})
	if r.Status != 400 || !strings.Contains(r.Text(), `"location":"query.sort"`) || !strings.Contains(r.Text(), `"location":"query.limit","message":"sent more than once"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/x?owner=6ba7b810-9dad-11d1-80b4-00c04fd430c8&limit=", Header: hdr})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"query.limit","message":"expected an integer","rule":"type"}]}`)
}

func TestAFieldWithNoDefaultIsStillRequiredBesideOneThatHasOne(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/x", echo[dfltIn]) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/x", Header: map[string][]string{"Accept-Language": {"en"}, "Cookie": {"theme=dark"}}})
	wantProblem(t, r, 400, `{"title":"Bad Request","status":400,"errors":[{"location":"query.owner","message":"required","rule":"required"}]}`)
}

func TestTheInputsValidateSeesTheDefaultsAlreadyFilledIn(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/x", func(_ context.Context, in dfltValidated) (dfltValidated, error) { return in, nil })
	})
	r := srv.Do(req("GET", "/x"))
	if r.Status != 200 {
		t.Fatalf("Validate saw a zero Limit: %d %s", r.Status, r.Text())
	}
}

type dfltValidated struct {
	Query dfltLimitOnly
}

type dfltLimitOnly struct{ Limit dfltPage }

func (v dfltValidated) Validate() error {
	if v.Query.Limit == 0 {
		return fmt.Errorf("limit is zero")
	}
	return nil
}

func (dfltLimitOnly) Defaults() map[string]string { return map[string]string{"Limit": "25"} }

// A shared Paging section and two routes that want different defaults.
type (
	dfltPaging struct {
		Limit  dfltPage
		Offset int
	}
	dfltTwenty  struct{ dfltPaging }
	dfltHundred struct{ dfltPaging }
)

func (dfltPaging) Defaults() map[string]string {
	return map[string]string{"Limit": "20", "Offset": "0"}
}
func (dfltHundred) Defaults() map[string]string { return map[string]string{"Limit": "100"} }

func TestTwoRoutesSharingOnePagingSectionMayStateDifferentDefaults(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Get("/a", echo[struct{ Query dfltTwenty }])
		app.Get("/b", echo[struct{ Query dfltHundred }])
	})
	if got := srv.Do(req("GET", "/a")).Text(); got != `{"query":{"limit":20,"offset":0}}` {
		t.Fatalf("the section's own defaults: %s", got)
	}
	if got := srv.Do(req("GET", "/b")).Text(); got != `{"query":{"limit":100,"offset":0}}` {
		t.Fatalf("the outer section's Defaults wins for a field both state and keeps the embedded one's other entries: %s", got)
	}
}

type dfltForm struct {
	Visibility dfltMode
	Name       string
}

func (dfltForm) Defaults() map[string]string { return map[string]string{"Visibility": "private"} }

func TestAFormFieldTakesItsDefaultLikeAQueryField(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Post("/f", echo[struct{ Body bites.Form[dfltForm] }]) })
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/f", Body: []byte("name=a"), Header: map[string][]string{"Content-Type": {"application/x-www-form-urlencoded"}}})
	if r.Status != 200 || r.Text() != `{"body":{"value":{"visibility":"private","name":"a"}}}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

// spacePath is the Path section every route of a space shares, embedded instead of repeated.
type spacePath struct{ Path struct{ ID int } }

type renameSpace struct {
	spacePath
	Body struct{ Name string }
}

type SpaceCaller struct{ Name string }

type spaceAs struct {
	spacePath
	SpaceCaller
}

func TestAnInputThatEmbedsAStructOfSectionsReadsThemAsItsOwnAndAnEmbeddedProvidedTypeStillIsProvided(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(func(context.Context, bites.Request, struct{}) (SpaceCaller, error) { return SpaceCaller{"ada"}, nil })
		app.Get("/spaces/{id}", func(_ context.Context, in spaceAs) (string, error) {
			return fmt.Sprintf("%d %s", in.Path.ID, in.Name), nil
		})
		app.Put("/spaces/{id}", func(_ context.Context, in renameSpace) (string, error) {
			return fmt.Sprintf("%d %s", in.Path.ID, in.Body.Name), nil
		})
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/spaces/7"}); r.Status != 200 || r.Text() != `"7 ada"` {
		t.Fatalf("GET: %d %s", r.Status, r.Text())
	}
	r := srv.Do(bitetest.Request{Method: "PUT", Target: "/spaces/8", Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"name":"x"}`)})
	if r.Status != 200 || r.Text() != `"8 x"` {
		t.Fatalf("PUT: %d %s", r.Status, r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/spaces/nope"})
	if r.Status != 400 || !strings.Contains(r.Text(), `"location":"path.id"`) {
		t.Fatalf("a bad id in the shared section: %d %s", r.Status, r.Text())
	}
}

type foreignUUIDIn struct {
	Path struct{ ID testuuid.UUID }
}

func TestAUUIDOfAnotherPackageWithNoFormatIsNamedAtStartWithTheOptionThatGivesItOne(t *testing.T) {
	var logs bytes.Buffer
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	h := func(_ context.Context, in foreignUUIDIn) (bites.NoContent, error) { return bites.NoContent{}, nil }
	app.Get("/spaces/{id}", h)
	app.Delete("/spaces/{id}", h)
	srv := bitetest.Start(t, app)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/spaces/nope"}); r.Status != 400 || !strings.Contains(r.Text(), "expected a valid value") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	out := logs.String()
	if strings.Count(out, "a uuid type is read as text with no format") != 1 ||
		!strings.Contains(out, `bites.WithTextFormat[testuuid.UUID](\"uuid\")`) ||
		!strings.Contains(out, "type=github.com/nevindra/bites/internal/testuuid.UUID") ||
		!strings.Contains(out, `first="GET foreignUUIDIn.Path.ID"`) {
		t.Fatalf("the start-up log does not name the type once with its field and the option:\n%s", out)
	}

	logs.Reset()
	app = bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))), bites.WithTextFormat[testuuid.UUID]("uuid"))
	app.Get("/spaces/{id}", h)
	srv = bitetest.Start(t, app)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/spaces/nope"}); r.Status != 400 || !strings.Contains(r.Text(), "expected a UUID") {
		t.Fatalf("with the format: %d %s", r.Status, r.Text())
	}
	if strings.Contains(logs.String(), "no format") {
		t.Fatalf("a uuid type given a format is still named:\n%s", logs.String())
	}
}
