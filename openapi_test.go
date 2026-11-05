package bites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

var update = flag.Bool("update", false, "rewrite the golden API documents in testdata/openapi")

// The types of a representative application, one of each thing the document describes.
type (
	docRole string
	docName string

	docUser struct {
		ID      int64
		Name    string
		Email   bites.Opt[string]
		Role    docRole
		Created time.Time
	}
	docNewUser struct {
		Name  docName
		Email string
		Role  bites.Opt[docRole]
	}
	docPage struct {
		Items []docUser
		Total int
	}
	docEvent struct{ Kind string }
	docLogin struct {
		Name     string
		Password string
		Remember bites.Opt[bool]
	}
	docAdmin  struct{ Name string }
	docMsg    struct{ Text string }
	docResult interface{}
)

func (docRole) Values() []string   { return []string{"admin", "member"} }
func (docName) Length() (int, int) { return 1, 64 }

func (docUser) Descriptions() map[string]string {
	return map[string]string{"ID": "The user's id.", "Email": "Where mail goes; absent for a user without one."}
}

type docListQuery struct {
	Page bites.PositiveInt
	Tags []string
}

func (docListQuery) Descriptions() map[string]string {
	return map[string]string{"Page": "The page, from 1.", "Tags": "Only users with all of these tags."}
}

type docListIn2 struct {
	Query  docListQuery
	Header struct{ RequestToken string }
	Cookie struct{ Session bites.Opt[string] }
}

type docGetIn struct{ Path struct{ ID int64 } }

func docEcho[In, Out any](_ context.Context, _ In) (Out, error) {
	var o Out
	return o, nil
}

// docApp registers one route of every kind the document describes.
func docApp(app *bites.App) {
	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1.4.0", Description: "The users API."})
	app.Provide(func(context.Context, bites.Request, struct{}) (docAdmin, error) { return docAdmin{}, nil })
	app.Get("/users", docEcho[docListIn2, docPage], bites.Summary("List users"), bites.Tag("users"))
	app.Get("/users/{id}", docEcho[docGetIn, docUser], bites.Summary("Get a user"), bites.Description("Finds a user by id.\n\nA missing user is a 404."), bites.Tag("users"))
	app.Post("/users", docEcho[struct{ Body docNewUser }, bites.Created[docUser]], bites.Tag("users"))
	app.Put("/users/{id}", docEcho[struct {
		Path struct{ ID int64 }
		Body docNewUser
	}, docResult], bites.Returns[bites.OK[docUser]](), bites.Returns[bites.Created[docUser]](), bites.Returns[docUser](), bites.Tag("users"))
	app.Delete("/users/{id}", docEcho[docGetIn, bites.NoContent], bites.Tag("users"))
	app.Post("/webhooks", docEcho[struct{ Body bites.Raw[docEvent] }, bites.Accepted[docEvent]])
	app.Post("/blobs", docEcho[struct{ Body []byte }, bites.Bytes])
	app.Post("/login", docEcho[struct{ Body bites.Form[docLogin] }, bites.SeeOther])
	app.Post("/signup", docEcho[struct{ Body bites.Submitted[docLogin] }, bites.OneOf2[bites.SeeOther, bites.Page]])
	app.Post("/avatars", docEcho[struct{ Body bites.Upload }, bites.Text], bites.MaxBody(1<<20))
	app.Get("/export", docEcho[struct{}, bites.File])
	app.Get("/events", docEcho[struct{}, bites.SSE])
	app.Get("/feed", docEcho[struct{}, bites.Stream])
	app.Get("/old", docEcho[struct{}, bites.Redirect])
	app.Get("/me", docEcho[struct{ Admin docAdmin }, docAdmin])
	app.WebSocket("/chat", docEcho3)
	app.Handle("PURGE", "/cache", docEcho[struct{}, bites.NoContent])
	admin := app.Group("/admin", bites.Tag("admin"))
	admin.Get("/stats", docEcho[struct{}, map[string]int], bites.Tag("stats"))
	legacy := app.Group("/legacy", bites.Deprecated())
	legacy.Get("/users/{id}", docEcho[docGetIn, docUser])
	legacy.Group("/admin").Get("/stats", docEcho[struct{}, map[string]int])
}

func docEcho3(context.Context, struct{}, *bites.Socket[docMsg]) (bites.Inbox[docMsg], error) {
	return bites.Inbox[docMsg]{}, nil
}

func docBytes(t *testing.T, register func(*bites.App)) (*bitetest.Server, []byte) {
	t.Helper()
	srv := start(t, register)
	r := srv.Do(req("GET", "/openapi.json"))
	if r.Status != 200 || r.Get("content-type") != "application/json" {
		t.Fatalf("%d %s %s", r.Status, r.Get("content-type"), r.Text())
	}
	return srv, r.Body
}

func docOf(t *testing.T, register func(*bites.App)) map[string]any {
	t.Helper()
	_, b := docBytes(t, register)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("the document is not JSON: %v\n%s", err, b)
	}
	return doc
}

// dig follows keys through nested objects and returns nil when one is missing.
func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func jsonOf(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return strings.TrimSpace(buf.String())
}

func TestTheDocumentOfARepresentativeAppMatchesItsGolden(t *testing.T) {
	_, b := docBytes(t, docApp)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "openapi", "app.json")
	if *update {
		os.MkdirAll(filepath.Dir(golden), 0o755)
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("the document differs from %s (run with -update to rewrite it)\n got:\n%s", golden, pretty.Bytes())
	}
}

func TestTheDocumentIsBuiltOnceAndServedAsTheSameBytesForGETAndHEAD(t *testing.T) {
	srv, first := docBytes(t, docApp)
	second := srv.Do(req("GET", "/openapi.json")).Body
	if !bytes.Equal(first, second) {
		t.Fatal("two requests got different bytes")
	}
	h := srv.Do(req("HEAD", "/openapi.json"))
	if h.Status != 200 || len(h.Body) != 0 || h.Get("content-type") != "application/json" {
		t.Fatalf("HEAD: %d %q %q", h.Status, h.Get("content-type"), h.Body)
	}
}

func TestTheDocumentNamesTheAPIAndItsTagsAndLeavesOutWhatOpenAPICannotDescribe(t *testing.T) {
	doc := docOf(t, docApp)
	if doc["openapi"] != "3.1.0" || jsonOf(doc["info"]) != `{"description":"The users API.","title":"Users","version":"1.4.0"}` {
		t.Fatalf("%v %v", doc["openapi"], doc["info"])
	}
	if got := jsonOf(doc["tags"]); got != `[{"name":"admin"},{"name":"stats"},{"name":"users"}]` {
		t.Errorf("tags: %s", got)
	}
	paths := doc["paths"].(map[string]any)
	for _, absent := range []string{"/cache", "/openapi.json"} {
		if _, ok := paths[absent]; ok {
			t.Errorf("%s is in the document", absent)
		}
	}
	if dig(paths, "/chat", "get", "responses", "101") == nil {
		t.Errorf("the WebSocket route is a GET that answers 101: %v", paths["/chat"])
	}
	if got := jsonOf(dig(paths, "/admin/stats", "get", "tags")); got != `["admin","stats"]` {
		t.Errorf("a group's tag comes first: %s", got)
	}
	if _, ok := paths["/users/{id}"].(map[string]any)["get"]; !ok {
		t.Error("GET /users/{id} is missing")
	}
}

func TestSummaryDescriptionAndTagLandOnTheOperation(t *testing.T) {
	doc := docOf(t, docApp)
	op := dig(doc, "paths", "/users/{id}", "get")
	if dig(op, "summary") != "Get a user" || dig(op, "description") != "Finds a user by id.\n\nA missing user is a 404." || jsonOf(dig(op, "tags")) != `["users"]` {
		t.Fatalf("%s", jsonOf(op))
	}
}

func TestADeprecatedRouteIsDeprecatedInTheDocumentAndToldToUseEach(t *testing.T) {
	var seen []bites.RouteInfo
	doc := docOf(t, func(app *bites.App) {
		app.UseEach(func(r bites.RouteInfo) (bites.Middleware, error) { seen = append(seen, r); return nil, nil })
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/a", docEcho[struct{}, docMsg], bites.Deprecated())
		app.Get("/b", docEcho[struct{}, docMsg])
		app.Get("/c", docEcho[struct{}, docMsg], bites.Options(bites.Summary("c"), bites.Deprecated()))
		g := app.Group("/g", bites.Deprecated())
		g.Get("/d", docEcho[struct{}, docMsg])
		g.Group("/n").Get("/e", docEcho[struct{}, docMsg])
		g.Get("/f", docEcho[struct{}, docMsg], bites.Deprecated()) // both: harmless
		app.Group("/h", bites.Options(bites.Deprecated())).Get("/i", docEcho[struct{}, docMsg])
		g.WebSocket("/ws", docEcho3)
		g.Static("/files", bites.Static{FS: staticEmbed})
		g.Get("/hidden", docEcho[struct{}, docMsg], bites.Undocumented())
	})
	for path, want := range map[string]bool{"/a": true, "/b": false, "/c": true, "/g/d": true, "/g/n/e": true, "/g/f": true, "/h/i": true} {
		op := dig(doc, "paths", path, "get").(map[string]any)
		got, has := op["deprecated"]
		if want && got != true || !want && has {
			t.Errorf("GET %s: deprecated = %v (present %v), want deprecated %v", path, got, has, want)
		}
	}
	if got := jsonOf(dig(doc, "paths", "/g/hidden")); got != "null" {
		t.Errorf("an Undocumented route is in the document: %s", got)
	}
	deprecated := map[string]bool{}
	for _, r := range seen {
		deprecated[r.Pattern] = r.Deprecated
	}
	for _, p := range []string{"/a", "/c", "/g/d", "/g/n/e", "/g/f", "/h/i", "/g/ws", "/g/files/{path...}", "/g/hidden"} {
		if !deprecated[p] {
			t.Errorf("UseEach was not told %s is deprecated (saw %v)", p, deprecated)
		}
	}
	if deprecated["/b"] || deprecated["/openapi.json"] {
		t.Errorf("UseEach was told a route that is not deprecated is: %v", deprecated)
	}
}

func TestEverySectionIsDescribedAsParametersWithTheirDescriptionsAndRequiredness(t *testing.T) {
	doc := docOf(t, docApp)
	got := jsonOf(dig(doc, "paths", "/users", "get", "parameters"))
	want := `[{"description":"The page, from 1.","in":"query","name":"page","required":true,"schema":{"format":"int64","minimum":1,"type":"integer"}},` +
		`{"description":"Only users with all of these tags.","in":"query","name":"tags","required":true,"schema":{"items":{"type":"string"},"type":"array"}},` +
		`{"in":"header","name":"Request-Token","required":true,"schema":{"type":"string"}},` +
		`{"in":"cookie","name":"session","required":false,"schema":{"type":"string"}}]`
	if got != want {
		t.Errorf("\n got  %s\n want %s", got, want)
	}
	if got := jsonOf(dig(doc, "paths", "/users/{id}", "get", "parameters")); got != `[{"in":"path","name":"id","required":true,"schema":{"format":"int64","type":"integer"}}]` {
		t.Errorf("path: %s", got)
	}
}

func TestAProvidedFieldIsNotAParameterOrABody(t *testing.T) {
	doc := docOf(t, docApp)
	op := dig(doc, "paths", "/me", "get").(map[string]any)
	if _, ok := op["parameters"]; ok {
		t.Errorf("parameters: %s", jsonOf(op["parameters"]))
	}
	if _, ok := op["requestBody"]; ok {
		t.Error("a requestBody")
	}
	if _, ok := op["responses"].(map[string]any)["400"]; ok {
		t.Error("a route with only a Provider has no input to be a 400")
	}
}

func TestEachBodyTypeIsDescribedByTheWayItIsRead(t *testing.T) {
	doc := docOf(t, docApp)
	media := func(path string) string {
		c := dig(doc, "paths", path, "post", "requestBody", "content").(map[string]any)
		out := make([]string, 0, 1)
		for m, v := range c {
			out = append(out, m+"="+jsonOf(v))
		}
		return strings.Join(out, ";")
	}
	cases := map[string]string{
		"/users":    `application/json={"schema":{"$ref":"#/components/schemas/docNewUser"}}`,
		"/webhooks": `application/json={"schema":{"$ref":"#/components/schemas/docEvent.Request"}}`,
		"/blobs":    `*/*={}`,
		"/login":    `application/x-www-form-urlencoded={"schema":{"$ref":"#/components/schemas/docLogin"}}`,
		"/avatars":  `multipart/form-data={"schema":{"type":"object"}}`,
	}
	for path, want := range cases {
		if got := media(path); got != want {
			t.Errorf("%s:\n got  %s\n want %s", path, got, want)
		}
	}
	if dig(doc, "paths", "/users", "post", "requestBody", "required") != true {
		t.Error("a body is required")
	}
}

func TestEachWrapperIsDescribedByItsStatusAndMediaType(t *testing.T) {
	doc := docOf(t, docApp)
	status := func(path, method string) map[string]any {
		return dig(doc, "paths", path, method, "responses").(map[string]any)
	}
	if r := status("/users", "post"); dig(r, "201", "headers") != nil || dig(r, "201", "content", "application/json") == nil || r["200"] != nil {
		t.Errorf("Created: %s", jsonOf(r))
	}
	if r := status("/users/{id}", "delete"); dig(r, "204") == nil || dig(r, "204", "content") != nil {
		t.Errorf("NoContent: %s", jsonOf(r))
	}
	if r := status("/webhooks", "post"); dig(r, "202", "content", "application/json") == nil {
		t.Errorf("Accepted: %s", jsonOf(r))
	}
	if r := status("/login", "post"); dig(r, "303", "headers", "Location") == nil {
		t.Errorf("SeeOther: %s", jsonOf(r))
	}
	if r := status("/old", "get"); len(r) != 5 || dig(r, "301", "headers", "Location") == nil || dig(r, "308") == nil {
		t.Errorf("Redirect: %s", jsonOf(r))
	}
	for path, want := range map[string]string{"/avatars": "text/plain", "/blobs": "*/*", "/export": "*/*", "/feed": "*/*", "/events": "text/event-stream"} {
		method := "get"
		if path == "/avatars" || path == "/blobs" {
			method = "post"
		}
		if c, _ := dig(status(path, method), "200", "content").(map[string]any); c == nil || c[want] == nil {
			t.Errorf("%s: %s", path, jsonOf(status(path, method)))
		}
	}
}

type (
	docTick   struct{ N int }
	docNotice struct{ Text string }
)

// eventsApp registers the ways an Events reaches the document: alone, beside a JSON value in an
// interface return, two of them in one, and one beside an untyped SSE.
func eventsApp(app *bites.App) {
	app.OpenAPI("/openapi.json", bites.Info{Title: "Feeds", Version: "1"})
	app.Get("/ticks", docEcho[struct{}, bites.Events[docTick]])
	app.Get("/either", docEcho[struct{}, docResult], bites.Returns[docTick](), bites.Returns[bites.Events[docTick]]())
	app.Get("/two", docEcho[struct{}, docResult], bites.Returns[bites.Events[docTick]](), bites.Returns[bites.Events[docNotice]]())
	app.Get("/mixed", docEcho[struct{}, docResult], bites.Returns[bites.Events[docTick]](), bites.Returns[bites.SSE]())
}

func TestAnEventsStatesItsEventsAndTheSchemaOfTheirDataUnderTextEventStream(t *testing.T) {
	doc := docOf(t, eventsApp)
	mt := dig(doc, "paths", "/ticks", "get", "responses", "200", "content", "text/event-stream")
	if got := jsonOf(dig(mt, "schema")); got != `{"type":"string"}` {
		t.Errorf("the stream's schema: %s", got)
	}
	want := `{"properties":{"data":{"contentMediaType":"application/json","contentSchema":{"$ref":"#/components/schemas/docTick"},"type":"string"},"event":{"type":"string"},"id":{"type":"string"},"retry":{"minimum":0,"type":"integer"}},"required":["data"],"type":"object"}`
	if got := jsonOf(dig(mt, "x-itemSchema")); got != want {
		t.Errorf("x-itemSchema\n got %s\nwant %s", got, want)
	}
	if dig(doc, "components", "schemas", "docTick") == nil {
		t.Errorf("the event's type is not a component: %s", jsonOf(dig(doc, "components", "schemas")))
	}
	either := dig(doc, "paths", "/either", "get", "responses", "200", "content")
	if dig(either, "application/json", "schema") == nil || jsonOf(dig(either, "text/event-stream", "x-itemSchema")) != want {
		t.Errorf("an interface of a value and an Events: %s", jsonOf(either))
	}
	two := dig(doc, "paths", "/two", "get", "responses", "200", "content", "text/event-stream", "x-itemSchema", "oneOf")
	if l, _ := two.([]any); len(l) != 2 {
		t.Errorf("two Events in one interface: %s", jsonOf(two))
	}
	mixed := dig(doc, "paths", "/mixed", "get", "responses", "200", "content", "text/event-stream")
	if dig(mixed, "x-itemSchema") != nil || dig(mixed, "schema") == nil {
		t.Errorf("an Events beside an SSE states no item, since the SSE's data is any text: %s", jsonOf(mixed))
	}
}

func TestTheDocumentOfAnAppWithEventsMatchesItsGolden(t *testing.T) {
	_, b := docBytes(t, eventsApp)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "openapi", "events.json")
	if *update {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("the document differs from %s (run with -update to rewrite it)\n got:\n%s", golden, pretty.Bytes())
	}
}

func TestAnInterfaceReturnIsDescribedByTheStatusOfEveryTypeItDeclared(t *testing.T) {
	doc := docOf(t, docApp)
	r := dig(doc, "paths", "/users/{id}", "put", "responses").(map[string]any)
	if dig(r, "201", "headers") != nil {
		t.Errorf("201 states a Location the route never said it sets: %s", jsonOf(r))
	}
	// OK[docUser] and the plain docUser are both a 200 with the same schema: listed once.
	if got := jsonOf(dig(r, "200", "content", "application/json")); got != `{"schema":{"$ref":"#/components/schemas/docUser"}}` {
		t.Errorf("200: %s", got)
	}
}

func TestEveryOperationNamesTheProblemAnswersItCanGive(t *testing.T) {
	doc := docOf(t, docApp)
	r := dig(doc, "paths", "/users", "post", "responses").(map[string]any)
	for status, ref := range map[string]string{"400": "InvalidInput", "413": "Error", "415": "Error", "default": "Error"} {
		if got := jsonOf(r[status]); got != `{"$ref":"#/components/responses/`+ref+`"}` {
			t.Errorf("%s: %s", status, got)
		}
	}
	if _, ok := dig(doc, "paths", "/blobs", "post", "responses").(map[string]any)["415"]; ok {
		t.Error("a []byte body takes any media type, so it has no 415")
	}
	errs := dig(doc, "components", "responses", "InvalidInput", "content", "application/problem+json", "schema", "properties", "errors")
	if errs == nil || dig(doc, "components", "responses", "Error", "content", "application/problem+json") == nil {
		t.Errorf("components: %s", jsonOf(dig(doc, "components", "responses")))
	}
}

func TestAFieldDescriptionIsOnItsPropertyOfTheSchema(t *testing.T) {
	doc := docOf(t, docApp)
	props := dig(doc, "components", "schemas", "docUser", "properties")
	if dig(props, "id", "description") != "The user's id." || dig(props, "email", "description") != "Where mail goes; absent for a user without one." || dig(props, "name", "description") != nil {
		t.Errorf("%s", jsonOf(props))
	}
}

func TestAWebSocketRouteIsAGETThatAnswers101AndStatesItsMessagesItsTagAndItsSummary(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		g := app.Group("/g", bites.Tag("g"))
		g.WebSocket("/chat", docEcho3, bites.Summary("Chat"), bites.ErrorStatuses(404))
		app.WebSocket("/hidden", docEcho3, bites.Undocumented())
	})
	op := dig(doc, "paths", "/g/chat", "get")
	if op == nil {
		t.Fatalf("paths: %s", jsonOf(doc["paths"]))
	}
	if dig(op, "summary") != "Chat" || jsonOf(dig(op, "tags")) != `["g"]` {
		t.Errorf("summary and tag: %s", jsonOf(op))
	}
	rs := dig(op, "responses").(map[string]any)
	for _, st := range []string{"101", "400", "403", "404", "426", "default"} {
		if rs[st] == nil {
			t.Errorf("the WebSocket route answers %s: %s", st, jsonOf(rs))
		}
	}
	if dig(op, "x-websocket", "clientMessage", "$ref") != "#/components/schemas/docMsg.Request" || dig(op, "x-websocket", "serverMessage", "$ref") != "#/components/schemas/docMsg.Response" {
		t.Errorf("x-websocket: %s", jsonOf(dig(op, "x-websocket")))
	}
	if dig(doc, "paths", "/hidden") != nil {
		t.Error("Undocumented leaves a WebSocket route out")
	}
}

func TestADocumentOfAnAppWithNoOpenAPICallIsNeverBuiltAndHasNoRoute(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	if r := srv.Do(req("GET", "/openapi.json")); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestTheComponentsAreTheSameWhateverOrderTheRoutesWereRegisteredIn(t *testing.T) {
	a := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/a", docEcho[struct{}, docUser])
		app.Get("/b", docEcho[struct{}, docPage])
	})
	b := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/b", docEcho[struct{}, docPage])
		app.Get("/a", docEcho[struct{}, docUser])
	})
	if !reflect.DeepEqual(a["components"], b["components"]) {
		t.Errorf("components differ with the order routes were registered in")
	}
}

// The security of the document (ADR 0069): the schemes come from the Providers a route runs, and
// the 401, 403 and 429 answers from the auth Providers, CSRF and a RateLimiter on the route.
type (
	docCaller  struct{ ID string }
	docService struct{ Name string }
	docTenant  struct{ ID string }
	docStaff   struct{ Name string }
)

func docBearer(context.Context, string) (docCaller, error) { return docCaller{ID: "u1"}, nil }

func docAPIKey(context.Context, string) (docService, error) { return docService{Name: "s"}, nil }

func docTenantKey(context.Context, string) (docTenant, error) { return docTenant{ID: "t"}, nil }

func docBasic(context.Context, string, string) (docStaff, error) { return docStaff{Name: "ada"}, nil }

// securedApp is a representative app with every scheme, a RateLimiter, CSRF and routes left out
// of the document, served behind a proxy that strips /shop (its servers, ADR 0025).
func securedApp(app *bites.App) {
	app.OpenAPI("/openapi.json", bites.Info{Title: "Shop", Version: "2.0.0", Servers: []bites.Server{
		{URL: "https://shop.example.com/shop", Description: "production"}, {URL: "/shop"},
	}})
	app.Provide(bites.BearerAuth(docBearer))
	limit := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 100, Period: time.Minute})
	app.Get("/health/deep", docEcho[struct{}, bites.NoContent], bites.Undocumented())
	app.Get("/me", docEcho[struct{ Caller docCaller }, docCaller], bites.Summary("The signed-in caller"))
	app.Get("/products", docEcho[struct{}, []docEvent], bites.Use(limit.Middleware()))
	machines := app.Group("/v1", bites.Provide(bites.APIKeyAuth(docAPIKey)), bites.Tag("machines"))
	machines.Post("/orders", docEcho[struct {
		Service docService
		Body    docEvent
	}, bites.Created[docEvent]])
	machines.Post("/tenants/orders", docEcho[struct {
		Service docService
		Tenant  docTenant
	}, bites.NoContent], bites.Provide(bites.APIKeyAuth(docTenantKey, bites.AuthOptions{Header: "X-Tenant-Key"})))
	pages := app.Group("/admin", bites.Provide(bites.BasicAuth(docBasic)), bites.Use(bites.CSRF(bites.CSRFOptions{Origins: []string{"https://shop.example.com"}})))
	pages.Get("/stats", docEcho[struct{ Staff docStaff }, map[string]int])
	pages.Post("/reindex", docEcho[struct{ Staff docStaff }, bites.Accepted[docEvent]])
	internal := app.Group("/internal", bites.Undocumented(), bites.Tag("internal"))
	internal.Get("/debug", docEcho[struct{}, any], bites.Returns[docEvent]())
	// A product page for the signed-in caller and the anonymous one alike, named by hand.
	app.Get("/products/{id}", docEcho[struct {
		Path   struct{ ID int64 }
		Caller bites.Opt[docCaller]
	}, docEvent], bites.OperationID("getProduct"))
	// Partners reach /v2 with a token, a session cookie or a client certificate.
	partners := app.Group("/v2", bites.Provide(bites.AnyAuth(
		bites.BearerAuth(docPartnerToken),
		bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"}, docPartnerSession),
		bites.CustomAuth(bites.SecurityScheme{Type: "mutualTLS", Description: "A client certificate our CA issued."}, docPartnerSession),
	)))
	partners.Get("/orders", docEcho[struct{ Partner docPartner }, []docEvent])
	partners.Get("/reports", docEcho[struct{ Auditor docAuditor }, []docEvent], bites.Provide(bites.CustomAuth(bites.SecurityScheme{
		Type: "openIdConnect", OpenIDConnectURL: "https://id.example.com/.well-known/openid-configuration",
	}, docAuditorToken)))
}

type (
	docPartner struct{ ID string }
	docAuditor struct{ ID string }
)

func docPartnerToken(context.Context, string) (docPartner, error) { return docPartner{ID: "p"}, nil }

func docPartnerSession(context.Context, bites.Request, struct{}) (docPartner, error) {
	return docPartner{ID: "p"}, nil
}

func docAuditorToken(context.Context, bites.Request, struct{}) (docAuditor, error) {
	return docAuditor{ID: "a"}, nil
}

func TestTheDocumentOfASecuredAppMatchesItsGolden(t *testing.T) {
	_, b := docBytes(t, securedApp)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden := filepath.Join("testdata", "openapi", "secured.json")
	if *update {
		if err := os.WriteFile(golden, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("the document differs from %s (run with -update to rewrite it)\n got:\n%s", golden, pretty.Bytes())
	}
}

func TestARouteWhoseProviderReadsACredentialRequiresItsSchemeAndCanAnswer401(t *testing.T) {
	doc := docOf(t, securedApp)
	if got := jsonOf(dig(doc, "paths", "/me", "get", "security")); got != `[{"bearerAuth":[]}]` {
		t.Errorf("security: %s", got)
	}
	r := dig(doc, "paths", "/me", "get", "responses").(map[string]any)
	if jsonOf(r["401"]) != `{"$ref":"#/components/responses/Unauthorized"}` || jsonOf(r["400"]) != `{"$ref":"#/components/responses/Error"}` {
		t.Errorf("responses: %s", jsonOf(r))
	}
	if dig(doc, "components", "responses", "Unauthorized", "headers", "WWW-Authenticate") == nil {
		t.Errorf("components: %s", jsonOf(dig(doc, "components", "responses")))
	}
	want := `{"apiKeyAuth":{"in":"header","name":"X-API-Key","type":"apiKey"},"apiKeyAuth.X-Tenant-Key":{"in":"header","name":"X-Tenant-Key","type":"apiKey"},"basicAuth":{"scheme":"basic","type":"http"},"bearerAuth":{"scheme":"bearer","type":"http"},"cookieAuth.sid":{"in":"cookie","name":"sid","type":"apiKey"},"mutualTLS":{"description":"A client certificate our CA issued.","type":"mutualTLS"},"openIdConnect":{"openIdConnectUrl":"https://id.example.com/.well-known/openid-configuration","type":"openIdConnect"}}`
	if got := jsonOf(dig(doc, "components", "securitySchemes")); got != want {
		t.Errorf("securitySchemes:\n got  %s\n want %s", got, want)
	}
}

func TestARouteThatAsksForNoCredentialHasNoSecurityThoughAProviderCoversIt(t *testing.T) {
	doc := docOf(t, securedApp)
	op := dig(doc, "paths", "/products", "get").(map[string]any)
	if _, ok := op["security"]; ok {
		t.Errorf("an app Provider the route does not run is not a requirement: %s", jsonOf(op))
	}
	if _, ok := op["responses"].(map[string]any)["401"]; ok {
		t.Errorf("%s", jsonOf(op))
	}
}

func TestARouteThatRunsTwoCredentialProvidersRequiresBothTogether(t *testing.T) {
	doc := docOf(t, securedApp)
	if got := jsonOf(dig(doc, "paths", "/v1/tenants/orders", "post", "security")); got != `[{"apiKeyAuth":[],"apiKeyAuth.X-Tenant-Key":[]}]` {
		t.Errorf("security: %s", got)
	}
}

func TestAProviderThatNeedsTheCallerMakesItsRouteRequireTheCallersScheme(t *testing.T) {
	type admin struct{ Name string }
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(bites.BearerAuth(docBearer))
		app.Provide(func(_ context.Context, _ bites.Request, d struct{ Caller docCaller }) (admin, error) {
			return admin{d.Caller.ID}, nil
		})
		app.Get("/admin", docEcho[struct{ Admin admin }, bites.NoContent])
	})
	if got := jsonOf(dig(doc, "paths", "/admin", "get", "security")); got != `[{"bearerAuth":[]}]` {
		t.Errorf("security: %s", got)
	}
}

func TestAProviderOfTheSameShapeAsBearerAuthIsNeverCalledAtRegistrationAndAddsNoSecurity(t *testing.T) {
	calls := 0
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(func(context.Context, bites.Request, struct{}) (docCaller, error) { calls++; return docCaller{}, nil })
		app.Get("/me", docEcho[struct{ Caller docCaller }, bites.NoContent])
	})
	if calls != 0 {
		t.Errorf("the application's Provider was called %d times at Registration", calls)
	}
	if op := dig(doc, "paths", "/me", "get").(map[string]any); op["security"] != nil || dig(doc, "components", "securitySchemes") != nil || dig(doc, "components", "responses", "Unauthorized") != nil {
		t.Errorf("%s", jsonOf(doc))
	}
}

func TestARateLimiterGivesEveryOperationA429AndCSRFGivesStateChangingOnesA403(t *testing.T) {
	doc := docOf(t, securedApp)
	status := func(path, method, code string) string {
		return jsonOf(dig(doc, "paths", path, method, "responses", code))
	}
	if status("/products", "get", "429") != `{"$ref":"#/components/responses/TooManyRequests"}` || status("/me", "get", "429") != "null" {
		t.Errorf("429: %s %s", status("/products", "get", "429"), status("/me", "get", "429"))
	}
	if status("/admin/reindex", "post", "403") != `{"$ref":"#/components/responses/Error"}` || status("/admin/stats", "get", "403") != "null" {
		t.Errorf("403: %s %s", status("/admin/reindex", "post", "403"), status("/admin/stats", "get", "403"))
	}
	if dig(doc, "components", "responses", "TooManyRequests", "headers", "Retry-After") == nil {
		t.Errorf("components: %s", jsonOf(dig(doc, "components", "responses")))
	}
}

func TestAppMiddlewareGivenAfterTheRoutesStillAddsItsAnswersToEveryOperation(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/a", docEcho[struct{}, bites.NoContent])
		app.Post("/b", docEcho[struct{}, bites.NoContent])
		app.Use(bites.NewRateLimiter(bites.RateLimitOptions{Limit: 1, Period: time.Second}).Middleware(), bites.CSRF(bites.CSRFOptions{Origins: []string{"https://a.example"}}))
	})
	for _, c := range []struct{ path, method, code, want string }{
		{"/a", "get", "429", "TooManyRequests"}, {"/b", "post", "429", "TooManyRequests"}, {"/b", "post", "403", "Error"},
	} {
		if got := jsonOf(dig(doc, "paths", c.path, c.method, "responses", c.code)); got != `{"$ref":"#/components/responses/`+c.want+`"}` {
			t.Errorf("%s %s %s: %s", c.method, c.path, c.code, got)
		}
	}
	if dig(doc, "paths", "/a", "get", "responses", "403") != nil {
		t.Error("a GET is not refused by CSRF")
	}
}

func TestAnUndocumentedRouteIsServedButLeftOutOfTheDocumentWithItsTypes(t *testing.T) {
	srv, b := docBytes(t, securedApp)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/health/deep", "/internal/debug"} {
		if dig(doc, "paths", p) != nil {
			t.Errorf("%s is in the document", p)
		}
	}
	if strings.Contains(string(b), `"internal"`) {
		t.Errorf("the tag of an undocumented group is in the document: %s", b)
	}
	if r := srv.Do(req("GET", "/health/deep")); r.Status != 204 {
		t.Errorf("the undocumented route answered %d", r.Status)
	}
}

func TestAnUndocumentedRouteMayShareAShapeTheDocumentCouldNotHold(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/u/{id}", docEcho[docGetIn, bites.NoContent])
		app.Post("/u/{uid}", docEcho[struct{ Path struct{ UID int64 } }, bites.NoContent], bites.Undocumented())
	})
	if got := jsonOf(dig(doc, "paths", "/u/{id}")); !strings.Contains(got, `"get"`) || strings.Contains(got, `"post"`) {
		t.Errorf("%s", got)
	}
}

func TestEveryOperationHasAnOperationIdMadeFromItsMethodAndPath(t *testing.T) {
	doc := docOf(t, docApp)
	for _, c := range []struct{ path, method, want string }{
		{"/users", "get", "getUsers"}, {"/users", "post", "postUsers"}, {"/users/{id}", "get", "getUsersById"},
		{"/users/{id}", "delete", "deleteUsersById"}, {"/admin/stats", "get", "getAdminStats"},
	} {
		if got := dig(doc, "paths", c.path, c.method, "operationId"); got != c.want {
			t.Errorf("%s %s: %v, want %s", c.method, c.path, got, c.want)
		}
	}
	doc = docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/{$}", docEcho[struct{}, bites.NoContent])
		app.Get("/files/{path...}", docEcho[struct{ Path struct{ Path string } }, bites.NoContent])
		app.Get("/api.v1/user-groups/{group}", docEcho[struct{ Path struct{ Group string } }, bites.NoContent])
	})
	for path, want := range map[string]string{"/": "getRoot", "/files/{path}": "getFilesByPath", "/api.v1/user-groups/{group}": "getApiV1UserGroupsByGroup"} {
		if got := dig(doc, "paths", path, "get", "operationId"); got != want {
			t.Errorf("%s: %v, want %s", path, got, want)
		}
	}
}

func TestOperationIDGivesTheOperationTheIdTheApplicationChose(t *testing.T) {
	doc := docOf(t, securedApp)
	if got := dig(doc, "paths", "/products/{id}", "get", "operationId"); got != "getProduct" {
		t.Errorf("%v", got)
	}
}

func TestAnOptFieldMakesTheCredentialOptionalWithAnEmptyRequirement(t *testing.T) {
	doc := docOf(t, securedApp)
	if got := jsonOf(dig(doc, "paths", "/products/{id}", "get", "security")); got != `[{"bearerAuth":[]},{}]` {
		t.Errorf("security: %s", got)
	}
	if jsonOf(dig(doc, "paths", "/products/{id}", "get", "responses", "401")) != `{"$ref":"#/components/responses/Unauthorized"}` {
		t.Error("a credential that is not good is still a 401")
	}
}

func TestAnAnyAuthRouteListsEachCredentialAsAnAlternative(t *testing.T) {
	doc := docOf(t, securedApp)
	if got := jsonOf(dig(doc, "paths", "/v2/orders", "get", "security")); got != `[{"bearerAuth":[]},{"cookieAuth.sid":[]},{"mutualTLS":[]}]` {
		t.Errorf("security: %s", got)
	}
}

func TestACustomAuthProviderStatesItsSchemeAndIsNeverCalledAtRegistration(t *testing.T) {
	calls := 0
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "http", Scheme: "Bearer", BearerFormat: "JWT", Description: "A token from our identity provider."},
			func(context.Context, bites.Request, struct{}) (docCaller, error) { calls++; return docCaller{}, nil }))
		app.Get("/me", docEcho[struct{ Caller docCaller }, bites.NoContent])
	})
	if calls != 0 {
		t.Errorf("the application's Provider was called %d times at Registration", calls)
	}
	if got := jsonOf(dig(doc, "paths", "/me", "get", "security")); got != `[{"bearerAuth":[]}]` {
		t.Errorf("security: %s", got)
	}
	if got := jsonOf(dig(doc, "components", "securitySchemes")); got != `{"bearerAuth":{"bearerFormat":"JWT","description":"A token from our identity provider.","scheme":"bearer","type":"http"}}` {
		t.Errorf("securitySchemes: %s", got)
	}
}

func TestEveryAlternativeIsRequiredTogetherWithACredentialTheRouteAlsoNeeds(t *testing.T) {
	type tenant struct{ ID string }
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(bites.AnyAuth(bites.BearerAuth(docBearer), bites.APIKeyAuth(func(context.Context, string) (docCaller, error) { return docCaller{}, nil })))
		app.Provide(bites.APIKeyAuth(func(context.Context, string) (tenant, error) { return tenant{}, nil }, bites.AuthOptions{Header: "X-Tenant-Key"}))
		app.Get("/orders", docEcho[struct {
			Caller docCaller
			Tenant bites.Opt[tenant]
		}, bites.NoContent])
	})
	want := `[{"apiKeyAuth.X-Tenant-Key":[],"bearerAuth":[]},{"bearerAuth":[]},{"apiKeyAuth":[],"apiKeyAuth.X-Tenant-Key":[]},{"apiKeyAuth":[]}]`
	if got := jsonOf(dig(doc, "paths", "/orders", "get", "security")); got != want {
		t.Errorf("security:\n got  %s\n want %s", got, want)
	}
}

type docEmailAddr string

func (docEmailAddr) Format() string { return "email" }

type docContact struct{ Email docEmailAddr }

func TestAFormatMethodIsStatedInAResponseSchemaAsInARequestSchema(t *testing.T) {
	doc := docOf(t, func(a *bites.App) {
		a.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		a.Get("/c", docEcho[Empty, docContact])
		a.Post("/c", docEcho[struct{ Body docContact }, Empty])
	})
	for _, name := range []string{"docContact.Request", "docContact.Response"} {
		if got := jsonOf(dig(doc, "components", "schemas", name, "properties", "email")); got != `{"format":"email","type":"string"}` {
			t.Errorf("%s states the format: %s", name, got)
		}
	}
}

func TestInfoSchemaNameChoosesTheComponentNames(t *testing.T) {
	var asked []string
	doc := docOf(t, func(a *bites.App) {
		a.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1", SchemaName: func(pkg, name string) string {
			asked = append(asked, pkg+"."+name)
			if name == "docUser" {
				return "AccountListItem"
			}
			return ""
		}})
		a.Get("/u", docEcho[Empty, docUser])
		a.Get("/a", docEcho[Empty, docAdmin])
	})
	schemas := dig(doc, "components", "schemas").(map[string]any)
	if schemas["AccountListItem"] == nil || schemas["docAdmin"] == nil || schemas["docUser"] != nil {
		t.Errorf("names: %s", jsonOf(schemas))
	}
	if got := jsonOf(dig(doc, "paths", "/u", "get", "responses", "200", "content", "application/json", "schema")); got != `{"$ref":"#/components/schemas/AccountListItem"}` {
		t.Errorf("the reference follows the chosen name: %s", got)
	}
	if len(asked) < 2 {
		t.Errorf("asked once per type: %v", asked)
	}
}

// ---- Descriptions on embedded sections (ADR 0020) ----

type (
	embFilter struct{ Status string }
	embPaging struct{ Limit, Offset int }
	embBoth   struct {
		Query struct {
			embFilter
			embPaging
		}
	}
	embOuter struct {
		Query struct {
			embFilter
			embPaging
			Sort string
		}
	}
	embPromoted struct {
		Query struct {
			embFilter
			Sort string
		}
	}
)

func (embFilter) Descriptions() map[string]string {
	return map[string]string{"Status": "Filter by status."}
}
func (embPaging) Descriptions() map[string]string {
	return map[string]string{"Limit": "How many rows.", "Offset": "How many to skip."}
}

// The Query struct of embOuter is anonymous, so its own Descriptions comes from a named wrapper.
type (
	embNamed struct {
		embFilter
		embPaging
		Sort string
	}
	embNamedIn   struct{ Query embNamed }
	embOnlyOne   struct{ Query embPromotedQ }
	embPromotedQ struct {
		embFilter
		Sort string
	}
)

func (embNamed) Descriptions() map[string]string {
	return map[string]string{"Limit": "Rows per page, at most 50.", "Sort": "Column to order by."}
}

func parameterDescriptions(t *testing.T, register func(*bites.App)) map[string]string {
	t.Helper()
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		register(app)
	})
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct{ Name, Description string }
		}
	}
	if err := json.Unmarshal([]byte(srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range doc.Paths["/x"]["get"].Parameters {
		out[p.Name] = p.Description
	}
	return out
}

func TestEachEmbeddedSectionDescribesItsOwnFieldsWhenBothHaveTheMethod(t *testing.T) {
	got := parameterDescriptions(t, func(a *bites.App) { a.Get("/x", ok[embBoth]) })
	want := map[string]string{"status": "Filter by status.", "limit": "How many rows.", "offset": "How many to skip."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTheSectionsOwnDescriptionsWinOverAnEmbeddedOnesAndAddItsOwnFields(t *testing.T) {
	got := parameterDescriptions(t, func(a *bites.App) { a.Get("/x", ok[embNamedIn]) })
	want := map[string]string{"status": "Filter by status.", "limit": "Rows per page, at most 50.", "offset": "How many to skip.", "sort": "Column to order by."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestADescriptionsMethodPromotedFromTheOnlyEmbeddedSectionIsNotTakenForTheSectionsOwn(t *testing.T) {
	// embPromotedQ has no Descriptions of its own: the method is embFilter's, promoted. Taken as
	// the section's own it would name Status (a field it has) and leave Sort undescribed either
	// way, so the check is that the same answer arrives with no refusal for the embedded name.
	got := parameterDescriptions(t, func(a *bites.App) { a.Get("/x", ok[embOnlyOne]) })
	want := map[string]string{"status": "Filter by status.", "sort": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A JSON body is described the same way: each struct the codec inlines describes its own fields.
type (
	embBody struct {
		embFilter
		embPaging
		Sort string
	}
	embBodyOwn struct {
		embFilter
		embPaging
		Sort string
	}
	embBodyIn    struct{ Body embBody }
	embBodyOwnIn struct{ Body embBodyOwn }
)

func (embBodyOwn) Descriptions() map[string]string {
	return map[string]string{"Limit": "Rows per page, at most 50.", "Sort": "Column to order by."}
}

// bodyDescriptions is the description of each property of the JSON body of POST /x.
func bodyDescriptions(t *testing.T, register func(*bites.App)) map[string]string {
	t.Helper()
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		register(app)
	})
	ref, _ := dig(doc, "paths", "/x", "post", "requestBody", "content", "application/json", "schema", "$ref").(string)
	name, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok {
		t.Fatalf("the body is not a component: %q", ref)
	}
	props, _ := dig(doc, "components", "schemas", name, "properties").(map[string]any)
	out := map[string]string{}
	for k, v := range props {
		d, _ := dig(v, "description").(string)
		out[k] = d
	}
	return out
}

func TestEachStructInlinedIntoAJSONBodyDescribesItsOwnFieldsWhenBothHaveTheMethod(t *testing.T) {
	got := bodyDescriptions(t, func(a *bites.App) { a.Post("/x", ok[embBodyIn]) })
	want := map[string]string{"status": "Filter by status.", "limit": "How many rows.", "offset": "How many to skip.", "sort": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestABodyStructsOwnDescriptionsWinOverThoseOfTheStructsItInlines(t *testing.T) {
	got := bodyDescriptions(t, func(a *bites.App) { a.Post("/x", ok[embBodyOwnIn]) })
	want := map[string]string{"status": "Filter by status.", "limit": "Rows per page, at most 50.", "offset": "How many to skip.", "sort": "Column to order by."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// ---- An anonymous Body struct is a component named after its Input (ADR 0020) ----

type (
	CreateThing struct {
		Body struct {
			Name string
			Qty  int
		}
	}
	UpdateThing struct {
		Body struct {
			Name string
			Qty  int
		}
	}
)

func TestAnAnonymousBodyStructIsAComponentNamedAfterItsInput(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1", SchemaName: func(_, name string) string {
			if name == "CreateThingBody" {
				return "NewThing"
			}
			return ""
		}})
		app.Post("/things", ok[CreateThing])
	})
	var doc struct {
		Paths struct {
			Things struct {
				Post struct {
					RequestBody struct {
						Content map[string]struct{ Schema map[string]any }
					}
				}
			} `json:"/things"`
		}
		Components struct{ Schemas map[string]map[string]any }
	}
	if err := json.Unmarshal([]byte(srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()), &doc); err != nil {
		t.Fatal(err)
	}
	ref := doc.Paths.Things.Post.RequestBody.Content["application/json"].Schema["$ref"]
	if ref != "#/components/schemas/NewThing" {
		t.Fatalf("the Body is a $ref named through Info.SchemaName, got %v", doc.Paths.Things.Post.RequestBody)
	}
	if _, ok := doc.Components.Schemas["NewThing"]["properties"]; !ok {
		t.Fatalf("the component holds the properties: %v", doc.Components.Schemas)
	}
}

func TestTwoInputsWithTheSameAnonymousBodyHaveItWrittenInPlaceInEachOperation(t *testing.T) {
	var docs []string
	for _, order := range [][2]string{{"c", "u"}, {"u", "c"}} {
		srv := start(t, func(app *bites.App) {
			app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
			for _, o := range order {
				if o == "c" {
					app.Post("/c", ok[CreateThing])
				} else {
					app.Post("/u", ok[UpdateThing])
				}
			}
		})
		doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
		if strings.Contains(doc, "CreateThingBody") || strings.Contains(doc, "UpdateThingBody") {
			t.Errorf("registration order %v: a body named after one of two Inputs: %s", order, doc)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"/c", "/u"} {
			sch := dig(d, "paths", p, "post", "requestBody", "content", "application/json", "schema")
			if dig(sch, "properties", "name") == nil || dig(sch, "$ref") != nil {
				t.Errorf("%s: the body is written in place: %v", p, sch)
			}
		}
		docs = append(docs, doc)
	}
	if docs[0] != docs[1] {
		t.Errorf("the order of Registration changed the document")
	}
}

// ---- The document's bytes without serving it ----

func TestOpenAPIDocumentReturnsTheBytesTheAppServes(t *testing.T) {
	app := bites.New()
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	app.Post("/things", ok[CreateThing])
	before, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(before) || !strings.Contains(string(before), `"CreateThingBody"`) {
		t.Fatalf("not the document: %s", before)
	}
	before[0] = 'X' // the slice is the caller's
	again, _ := app.OpenAPIDocument()
	if again[0] != '{' {
		t.Fatal("a caller changed the app's document")
	}
	srv := bitetest.Start(t, app)
	if served := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text(); served != string(again) {
		t.Fatalf("the returned bytes and the served ones differ:\n%s\n%s", again, served)
	}
	after, _ := app.OpenAPIDocument()
	if string(after) != string(again) {
		t.Fatal("asking after serving gives another document")
	}
}

func TestOpenAPIDocumentWithoutOpenAPIIsAnError(t *testing.T) {
	if _, err := bites.New().OpenAPIDocument(); err == nil || !strings.Contains(err.Error(), "OpenAPI was never called") {
		t.Fatalf("got %v", err)
	}
}

// ---- Created's Location is documented only for a route that says it sets one ----

func locationDocOf(t *testing.T, opts ...bites.RouteOption) map[string]any {
	t.Helper()
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/things", func(context.Context, Empty) (bites.Created[User], error) {
			return bites.Created[User]{Location: "/things/1"}, nil
		}, opts...)
	})
	var doc map[string]any
	if err := json.Unmarshal([]byte(srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()), &doc); err != nil {
		t.Fatal(err)
	}
	return dig(doc, "paths", "/things", "post", "responses", "201").(map[string]any)
}

func TestACreatedRouteStatesNoLocationHeaderUnlessItSaysItSetsOne(t *testing.T) {
	if r := locationDocOf(t); r["headers"] != nil {
		t.Fatalf("a route without SetsLocation documents no header: %s", jsonOf(r))
	}
}

func TestARouteThatSetsLocationDocumentsItAsARequiredHeader(t *testing.T) {
	h := dig(locationDocOf(t, bites.SetsLocation()), "headers", "Location")
	if h == nil || dig(h, "required") != true || dig(h, "schema", "type") != "string" {
		t.Fatalf("Location is a required string header: %v", h)
	}
}

func TestSetsLocationDocumentsTheHeaderOnACreatedInsideAOneOfAndAReturns(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/a", func(context.Context, Empty) (docAnswer, error) { return docAnswer{}, nil }, bites.SetsLocation())
		app.Post("/b", func(context.Context, Empty) (docResult, error) { return nil, nil }, bites.Returns[bites.Created[docUser]](), bites.Returns[docUser](), bites.SetsLocation())
	})
	for _, p := range []string{"/a", "/b"} {
		if dig(doc, "paths", p, "post", "responses", "201", "headers", "Location", "required") != true {
			t.Errorf("%s: %s", p, jsonOf(dig(doc, "paths", p, "post", "responses")))
		}
	}
}

// parameterSchemas is the schema of each parameter of GET /x by name.
func parameterSchemas(t *testing.T, register func(*bites.App)) map[string]any {
	t.Helper()
	doc := docOf(t, func(a *bites.App) {
		a.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		register(a)
	})
	out := map[string]any{}
	for _, p := range dig(doc, "paths", "/x", "get", "parameters").([]any) {
		out[dig(p, "name").(string)] = p
	}
	return out
}

func TestTheDocumentStatesADefaultInTheParametersSchemaAndMarksTheParameterOptional(t *testing.T) {
	ps := parameterSchemas(t, func(a *bites.App) { a.Get("/x", ok[dfltIn]) })
	for name, want := range map[string]string{
		"limit":    `{"default":50,"format":"int64","maximum":100,"minimum":1,"type":"integer"}`,
		"sort":     `{"$ref":"#/components/schemas/dfltOrder","default":"risk"}`,
		"archived": `{"default":false,"type":"boolean"}`,
	} {
		if got := jsonOf(dig(ps[name], "schema")); got != want {
			t.Errorf("%s: schema %s, want %s", name, got, want)
		}
		if dig(ps[name], "required") != false {
			t.Errorf("%s: a parameter with a default is not required", name)
		}
	}
	if dig(ps["owner"], "required") != true || dig(ps["owner"], "schema", "default") != nil {
		t.Errorf("owner has no default and stays required: %v", ps["owner"])
	}
}

func TestADefaultIsStatedInTheDocumentAsTheTypeOfItsSchema(t *testing.T) {
	ps := parameterSchemas(t, func(a *bites.App) { a.Get("/x", ok[dfltKindsIn]) })
	for name, want := range map[string]string{"since": `"2026-01-01"`, "ratio": `0.5`, "id": `"6ba7b810-9dad-11d1-80b4-00c04fd430c8"`} {
		if got := jsonOf(dig(ps[name], "schema", "default")); got != want {
			t.Errorf("%s: default %s, want %s", name, got, want)
		}
	}
}

type dfltKinds struct {
	Since bites.Date
	Ratio float64
	ID    uuid.UUID
}

func (dfltKinds) Defaults() map[string]string {
	return map[string]string{"Since": "2026-01-01", "Ratio": "0.5", "ID": "6ba7b810-9dad-11d1-80b4-00c04fd430c8"}
}

type dfltKindsIn struct{ Query dfltKinds }

// A type that writes its own JSON is described by what its app declared for it (ADR 0076).
type (
	stampBody struct {
		At      stamp
		Maybe   bites.Opt[stamp]
		Nothing bites.Null[stamp]
		Many    []stamp
		ByName  map[string]stamp
		Free    json.RawMessage
		Config  stampFree
	}
	stampFree struct{ raw []byte }
)

func (s stampFree) MarshalJSON() ([]byte, error) { return s.raw, nil }

func (s *stampFree) UnmarshalJSON(b []byte) error { s.raw = append(s.raw[:0], b...); return nil }

func stampDoc(t *testing.T, s bites.JSONSchema, free bites.JSONSchema) map[string]any {
	t.Helper()
	srv := startWith(t, func(app *bites.App) {
		app.Post("/stamps", func(context.Context, struct{ Body stampBody }) (stampBody, error) { return stampBody{}, nil })
		app.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
	}, bites.WithJSONSchema[stamp](s), bites.WithJSONSchema[stampFree](free))
	var doc map[string]any
	if err := json.Unmarshal(srv.Do(req("GET", "/openapi.json")).Body, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestARegisteredJSONTypeIsDescribedWhereverItAppearsInARequestAndAResponse(t *testing.T) {
	doc := stampDoc(t, bites.JSONSchema{Type: "string", Format: "date-time", Nullable: true}, bites.JSONSchema{Type: "any"})
	// A request cannot carry the null: the decoder refuses it for a field that is not a Null.
	for comp, want := range map[string]string{
		"stampBody.Request":  `{"format":"date-time","type":"string"}`,
		"stampBody.Response": `{"format":"date-time","type":["string","null"]}`,
	} {
		props := dig(doc, "components", "schemas", comp, "properties")
		if props == nil {
			t.Fatalf("no component %s in %s", comp, jsonOf(dig(doc, "components", "schemas")))
		}
		for _, f := range []string{"at", "maybe", "nothing"} {
			if got := jsonOf(dig(props, f)); got != want && !(f == "nothing" && comp == "stampBody.Request") {
				t.Errorf("%s.%s: %s, want %s", comp, f, got, want)
			}
		}
		if got := jsonOf(dig(props, "nothing")); strings.Contains(got, "anyOf") {
			t.Errorf("%s.nothing: %s", comp, got)
		}
		if got := jsonOf(dig(props, "many")); got != `{"items":`+want+`,"type":"array"}` {
			t.Errorf("%s.many: %s", comp, got)
		}
		if got := jsonOf(dig(props, "by_name")); got != `{"additionalProperties":`+want+`,"type":"object"}` {
			t.Errorf("%s.by_name: %s", comp, got)
		}
		if jsonOf(dig(props, "config")) != "{}" || jsonOf(dig(props, "free")) != "{}" {
			t.Errorf("%s: a free-form type is not {}: %s", comp, jsonOf(props))
		}
	}
}

func TestARegisteredJSONTypeThatIsNotNullableIsDescribedWithoutNull(t *testing.T) {
	doc := stampDoc(t, bites.JSONSchema{Type: "string", Format: "date-time"}, bites.JSONSchema{Type: "object"})
	if s := jsonOf(dig(doc, "components", "schemas")); !strings.Contains(s, `"at":{"format":"date-time","type":"string"}`) || !strings.Contains(s, `"config":{"type":"object"}`) {
		t.Errorf("components: %s", s)
	}
}

type rawBody struct{ Payload json.RawMessage }

func TestAJSONRawMessageIsFreeFormByNameAndNeedsNoRegistration(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.Post("/raw", func(context.Context, struct{ Body rawBody }) (rawBody, error) { return rawBody{}, nil })
		app.OpenAPI("/openapi.json", bites.Info{Title: "T", Version: "1"})
	})
	if s := jsonOf(dig(doc, "components", "schemas")); !strings.Contains(s, `"payload":{}`) {
		t.Errorf("components: %s", s)
	}
}

func TestARegisteredJSONTypeIsStillReadAndWrittenByItsOwnMethods(t *testing.T) {
	srv := startWith(t, func(app *bites.App) {
		app.Post("/stamps", func(_ context.Context, in struct{ Body struct{ At stamp } }) (struct{ At stamp }, error) {
			return struct{ At stamp }(in.Body), nil
		})
		app.Get("/none", func(context.Context, Empty) (struct{ At stamp }, error) { return struct{ At stamp }{}, nil })
	}, bites.WithJSONSchema[stamp](bites.JSONSchema{Type: "string", Format: "date-time", Nullable: true}))
	if r := srv.Do(jsonReq("POST", "/stamps", `{"at":"2026-11-03T10:00:00Z"}`)); r.Status != 200 || r.Text() != `{"at":"2026-11-03T10:00:00Z"}` {
		t.Errorf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(req("GET", "/none")); r.Status != 200 || r.Text() != `{"at":null}` {
		t.Errorf("%d %s", r.Status, r.Text())
	}
}

func TestTheErrorStatusesARouteNamesAreProblemAnswersAddedToAGroupsAndTheFrameworksOwn(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		g := app.Group("/rooms", bites.ErrorStatuses(404))
		g.Put("/{id}", func(context.Context, struct {
			Path struct{ ID int64 }
		}) (bites.NoContent, error) {
			return bites.NoContent{}, nil
		}, bites.ErrorStatuses(409, 412, 400))
	})
	rs := dig(doc, "paths", "/rooms/{id}", "put", "responses").(map[string]any)
	for _, st := range []string{"404", "409", "412"} {
		if dig(rs, st, "$ref") != "#/components/responses/Error" {
			t.Errorf("%s is a problem answer: %v", st, rs[st])
		}
	}
	if dig(rs, "400", "$ref") != "#/components/responses/InvalidInput" {
		t.Errorf("the framework's own 400 stays as it was: %v", rs["400"])
	}
}

func TestErrorStatuses429IsTheRateLimitsAnswerWithItsHeaders(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		g := app.Group("/api", bites.ErrorStatuses(429))
		g.Get("/me", func(context.Context, struct{}) (bites.OK[docUser], error) { return bites.OK[docUser]{}, nil })
	})
	if got := dig(doc, "paths", "/api/me", "get", "responses", "429", "$ref"); got != "#/components/responses/TooManyRequests" {
		t.Errorf("a 429 a Provider sends through Allow is the rate limit's answer: %v", got)
	}
	if dig(doc, "components", "responses", "TooManyRequests", "headers", "Retry-After") == nil {
		t.Error("the rate limit's answer is in the components, with Retry-After")
	}
}

func TestAFileAnswerStatesItsHeadersRangesAndRefusalsAndMediaTypeAndSendsHeaderStateWhatTheHandlerSets(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/files/{id}", func(context.Context, struct{ Path struct{ ID int64 } }) (bites.File, error) {
			return bites.File{}, nil
		}, bites.MediaType("application/pdf"))
		app.Get("/export", func(context.Context, struct{}) (bites.Stream, error) { return bites.Stream{}, nil },
			bites.MediaType("application/x-ndjson"))
		app.Put("/notes/{id}", func(context.Context, struct{ Path struct{ ID int64 } }) (bites.OK[docUser], error) {
			return bites.OK[docUser]{}, nil
		}, bites.SendsHeader[string]("ETag", "The version of the note."))
	})
	file := dig(doc, "paths", "/files/{id}", "get", "responses").(map[string]any)
	for _, h := range []string{"ETag", "Last-Modified", "Accept-Ranges", "Content-Disposition"} {
		if dig(file, "200", "headers", h) == nil {
			t.Errorf("a File's 200 states %s: %v", h, file["200"])
		}
	}
	if dig(file, "206", "headers", "Content-Range") == nil || dig(file, "304") == nil || dig(file, "412") == nil || dig(file, "416") == nil {
		t.Errorf("a File states 206, 304, 412 and 416: %v", file)
	}
	if dig(file, "200", "content", "application/pdf") == nil || dig(file, "206", "content", "application/pdf") == nil || dig(file, "200", "content", "*/*") != nil {
		t.Errorf("MediaType replaces any type on the 200 and the 206: %v", file)
	}
	if dig(doc, "paths", "/export", "get", "responses", "200", "content", "application/x-ndjson") == nil {
		t.Errorf("a Stream's media type: %v", dig(doc, "paths", "/export", "get", "responses"))
	}
	if dig(doc, "paths", "/notes/{id}", "put", "responses", "200", "headers", "ETag") == nil {
		t.Errorf("SendsHeader states the ETag: %v", dig(doc, "paths", "/notes/{id}", "put", "responses"))
	}
}

func TestAMultipartBodyIsMultipartFormDataWithFormatBinaryForAFileAnArrayForAListOfFilesAndAnEncodingContentTypeFromAccept(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "Listings", Version: "1"})
		app.Post("/listings", mpHandler(nil), bites.MaxBody(8<<20))
	})
	content := dig(doc, "paths", "/listings", "post", "requestBody", "content").(map[string]any)
	if len(content) != 1 {
		t.Fatalf("one media type expected: %s", jsonOf(content))
	}
	entry := dig(content, "multipart/form-data").(map[string]any)
	schema := entry["schema"].(map[string]any)
	if r, ok := schema["$ref"].(string); ok {
		schema = dig(doc, append([]string{"components", "schemas"}, strings.TrimPrefix(r, "#/components/schemas/"))...).(map[string]any)
	}
	props := schema["properties"].(map[string]any)
	if got := jsonOf(props["cover"]); !strings.Contains(got, `"format":"binary"`) || !strings.Contains(got, `"type":"string"`) || !strings.Contains(got, "At most 3145728 bytes.") {
		t.Errorf("a file: %s", got)
	}
	if got := jsonOf(props["gallery"]); !strings.Contains(got, `"type":"array"`) || !strings.Contains(got, `"format":"binary"`) {
		t.Errorf("a list of files: %s", got)
	}
	if got := jsonOf(props["floor"]); !strings.Contains(got, `"format":"binary"`) {
		t.Errorf("an optional file: %s", got)
	}
	if got := jsonOf(props["title"]); strings.Contains(got, "binary") {
		t.Errorf("a text field: %s", got)
	}
	required := jsonOf(schema["required"])
	if !strings.Contains(required, `"cover"`) || !strings.Contains(required, `"title"`) || strings.Contains(required, `"floor"`) || strings.Contains(required, `"gallery"`) {
		t.Errorf("required: %s", required)
	}
	enc := jsonOf(entry["encoding"])
	if want := `{"cover":{"contentType":"image/png, image/jpeg"},"floor":{"contentType":"image/png, image/jpeg"},"gallery":{"contentType":"image/png, image/jpeg"}}`; enc != want {
		t.Errorf("encoding\n got  %s\n want %s", enc, want)
	}
	if dig(doc, "paths", "/listings", "post", "responses", "413") == nil || dig(doc, "paths", "/listings", "post", "responses", "400") == nil {
		t.Error("a Multipart route can answer 400 and 413")
	}
}

// The types of the example, header and deprecated tests (ADR 0020).
type (
	exHandle       string
	exBadHandle    string
	exCheckedPlate string
	exPanicky      string
	exWrongSig     string
	exHeaderSeq    int
	exAddress      struct {
		City string
		Zip  exZip
	}
	exZip     string
	exAccount struct {
		Handle  exHandle
		Address exAddress
		Legacy  string
		Nick    bites.Opt[string]
	}
	exAccountOut struct {
		Handle exHandle
		Fax    string
		Notes  string `json:"notes,omitempty"`
	}
	exFilter struct {
		Handle exHandle
		Sort   string
		Order  string
	}
	exFilterQuery struct {
		exFilter
		Dir string
	}
	exFilterIn struct {
		Query  exFilterQuery
		Header exOldHeader
	}
	exOldHeader struct{ XOld string }
)

func (exHandle) Length() (int, int)      { return 3, 12 }
func (exHandle) Example() exHandle       { return "ayu_k" }
func (exBadHandle) Length() (int, int)   { return 3, 12 }
func (exBadHandle) Example() exBadHandle { return "ab" }
func (exCheckedPlate) Check() error {
	return nil
}
func (exZip) Pattern() string             { return `^[0-9]{5}$` }
func (exZip) Example() exZip              { return "40115" }
func (exPanicky) Example() exPanicky      { panic("no example today") }
func (exWrongSig) Example() string        { return "x" }
func (exHeaderSeq) Range() (int64, int64) { return 1, 99 }
func (exHeaderSeq) Example() exHeaderSeq  { return 7 }

func (exAddress) Example() exAddress { return exAddress{City: "Bandung", Zip: "40115"} }
func (exAccount) Example() exAccount {
	return exAccount{Handle: "ayu_k", Address: exAddress{City: "Bandung", Zip: "40115"}, Legacy: "old", Nick: bites.Some("ayu")}
}
func (exAccount) DeprecatedFields() []string     { return []string{"Legacy"} }
func (exAccountOut) DeprecatedFields() []string  { return []string{"Fax"} }
func (exFilter) DeprecatedFields() []string      { return []string{"Sort"} }
func (exFilterQuery) DeprecatedFields() []string { return []string{"Dir"} }
func (exOldHeader) DeprecatedFields() []string   { return []string{"XOld"} }

func TestATypesExampleMethodIsWrittenInItsSchemaAsAnExamplesArrayInTheCodecsForm(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/accounts", docEcho[struct{ Body exAccount }, bites.Created[exAccount]])
		app.Get("/accounts", docEcho[struct{ Query struct{ Handle exHandle } }, exAccountOut])
	})
	acc := dig(doc, "components", "schemas", "exAccount.Request")
	if acc == nil {
		acc = dig(doc, "components", "schemas", "exAccount")
	}
	ex, _ := dig(acc, "examples").([]any)
	if len(ex) != 1 || jsonOf(ex[0]) != `{"address":{"city":"Bandung","zip":"40115"},"handle":"ayu_k","legacy":"old","nick":"ayu"}` {
		t.Errorf("the body type's example is its value as the wire writes it: %v", dig(acc, "examples"))
	}
	addr := dig(doc, "components", "schemas", "exAddress.Request")
	if addr == nil {
		addr = dig(doc, "components", "schemas", "exAddress")
	}
	if got := jsonOf(dig(addr, "properties", "zip", "examples")); got != `["40115"]` {
		t.Errorf("a field's named type states its own example: %s", got)
	}
	if got := jsonOf(dig(doc, "paths", "/accounts", "get", "parameters")); !strings.Contains(got, `"examples":["ayu_k"]`) {
		t.Errorf("a parameter's named type states its example in the parameter's schema: %s", got)
	}
}

func TestAnExampleThatItsOwnTypeRefusesIsAPanicAtRegistrationNamingTheRule(t *testing.T) {
	for name, reg := range map[string]func(*bites.App){
		"a body field": func(app *bites.App) {
			app.Post("/x", docEcho[struct{ Body struct{ H exBadHandle } }, bites.NoContent])
		},
		"a query parameter": func(app *bites.App) {
			app.Get("/x", docEcho[struct{ Query struct{ H exBadHandle } }, bites.NoContent])
		},
	} {
		msg := recoverMessage(func() { reg(bites.New(bites.WithClock(bitetest.NewClock()))) })
		if !strings.Contains(msg, `exBadHandle.Example returns "ab", which its own type refuses: must be between 3 and 12 characters`) {
			t.Errorf("%s: %q", name, msg)
		}
	}
}

func TestAResponseHeaderStatesItsDescriptionAndTheSchemaOfItsType(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		g := app.Group("/v1", bites.SendsHeader[string]("X-Request-Cost", "Units of quota the call used."))
		g.Get("/accounts", docEcho[struct{}, bites.OK[exAccountOut]],
			bites.SendsHeader[int]("X-Total-Count", "How many accounts match, before paging."),
			bites.SendsHeader[exHeaderSeq]("X-Seq", "The sequence of the answer."))
	})
	h := dig(doc, "paths", "/v1/accounts", "get", "responses", "200", "headers")
	if got := jsonOf(dig(h, "X-Total-Count")); got != `{"description":"How many accounts match, before paging.","schema":{"format":"int64","type":"integer"}}` {
		t.Errorf("a header of an int: %s", got)
	}
	if got := jsonOf(dig(h, "X-Seq")); !strings.Contains(got, `"minimum":1`) || !strings.Contains(got, `"maximum":99`) || !strings.Contains(got, `"examples":[7]`) {
		t.Errorf("a header of a named type states its rules and its example: %s", got)
	}
	if got := jsonOf(dig(h, "X-Request-Cost", "description")); got != `"Units of quota the call used."` {
		t.Errorf("a group's header reaches its routes: %s", got)
	}
}

func TestASendsHeaderReplacesTheHeaderTheFrameworkStatesOnAFile(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/f", docEcho[struct{}, bites.File], bites.SendsHeader[string]("ETag", "The hash of the file."))
	})
	if got := dig(doc, "paths", "/f", "get", "responses", "200", "headers", "ETag", "description"); got != "The hash of the file." {
		t.Errorf("the route's description wins: %v", got)
	}
}

func TestDeprecatedFieldsMarksAParameterAndABodyFieldAndAResponseFieldDeprecated(t *testing.T) {
	doc := docOf(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Post("/accounts", docEcho[struct{ Body exAccount }, bites.OK[exAccountOut]])
		app.Get("/search", docEcho[exFilterIn, bites.NoContent])
	})
	props := func(name string) any {
		for _, k := range []string{name + ".Request", name} {
			if p := dig(doc, "components", "schemas", k, "properties"); p != nil {
				return p
			}
		}
		return nil
	}
	if dig(props("exAccount"), "legacy", "deprecated") != true || dig(props("exAccount"), "handle", "deprecated") != nil {
		t.Errorf("a body field named by DeprecatedFields is deprecated and its sibling is not: %v", props("exAccount"))
	}
	if dig(props("exAccountOut"), "fax", "deprecated") != true {
		t.Errorf("a response field can be deprecated: %v", props("exAccountOut"))
	}
	deprecated := map[string]bool{}
	for _, p := range dig(doc, "paths", "/search", "get", "parameters").([]any) {
		deprecated[dig(p, "in").(string)+"."+dig(p, "name").(string)] = dig(p, "deprecated") == true
	}
	if !deprecated["query.sort"] || !deprecated["query.dir"] || !deprecated["header.X-Old"] || deprecated["query.handle"] {
		t.Errorf("a parameter is deprecated when its section's struct or an embedded one names it: %v", deprecated)
	}
}

type (
	exStaleDeprecation struct{ Name string }
	exStaleSection     struct{ Name string }
)

func (exStaleDeprecation) DeprecatedFields() []string { return []string{"Nmae"} }
func (exStaleSection) DeprecatedFields() []string     { return []string{"Nmae"} }
