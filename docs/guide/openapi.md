# The OpenAPI document

**One call, `app.OpenAPI`, serves an OpenAPI 3.1 document built from the same types that route, decode and answer, so it cannot disagree with the code.**

**Reference:** [OpenAPI](../reference/openapi.md) · **Design:** [OpenAPI](../design/openapi.md)

The document is described at Registration, from the Input and output types, and written once when the app starts serving; a request for it copies those bytes, so it costs none of the four axes of [ADR 0003](../adr/0003-the-trade-budget.md) ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)). Types give the paths, parameters, schemas and statuses. Prose, which types cannot give, comes from route options and from a method on a type.

<!-- compiles: decls -->
```go
type GetUser struct {
	Path  struct{ ID int64 }
	Query struct{ Expand bites.Opt[bool] }
}

type User struct {
	ID   int64
	Name string
}

type CreateUser struct {
	Body struct{ Name string }
}

func getUser(ctx context.Context, in GetUser) (User, error) {
	return User{ID: in.Path.ID, Name: "Ada"}, nil
}

func createUser(ctx context.Context, in CreateUser) (bites.Created[User], error) {
	return bites.Created[User]{Body: User{ID: 1, Name: in.Body.Name}, Location: "/users/1"}, nil
}
```

## Serving the document

**[`app.OpenAPI(path, Info)`](../reference/openapi.md#appopenapi) registers `GET path` (and so HEAD) and serves the document as `application/json`.** It is opt-in, because describing an API is a decision about what to publish: nothing is served at a default path.

<!-- compiles: body -->
```go
app.OpenAPI("/openapi.json", bites.Info{
	Title:       "Users",
	Version:     "1.0.0",
	Description: "Who is who.",
})
app.Get("/users/{id}", getUser, bites.Summary("Get a user"), bites.Tag("users"))
app.Post("/users", createUser, bites.Summary("Create a user"), bites.Tag("users"))
```

[`Info`](../reference/openapi.md#info) needs a `Title` and a `Version`, which is the version of your API and not of OpenAPI. It is a method that registers a route, and not an option of `New`, so a conflict with one of your routes is raised in order, at the line that registered the second ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)). The call may come before or after the routes it describes; the document is built when the app starts serving.

**Name the component schemas with `Info.SchemaName` when a client already has names for them.** By default a schema is named by its Go type, `User`, and `users.User` and `billing.User` when two packages declare one; a frontend whose generated types are `OrderListItem` and `ProductListItem` wants those. `SchemaName` is called once for each Go type with its package path and the name the framework would use, and returns the name, or `""` to keep the default:

<!-- compiles: body -->
```go
app.OpenAPI("/openapi.json", bites.Info{Title: "Shop", Version: "1.0.0", SchemaName: func(pkgPath, name string) string {
	switch pkgPath {
	case "example.com/shop/order":
		return "Order" + name
	case "example.com/shop/product":
		return "Product" + name
	}
	return ""
}})
```

What it returns is the name whether or not the default would collide; `.Request`, `.RequestOpen` or `.Response` is still added after it for a type whose request and response schemas differ. A name with a character outside `A-Z a-z 0-9 . - _`, one name for two types, and a name that another type has by default are refused when the app starts serving, naming both types and the line that called `OpenAPI` ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)).

**Behind a proxy, say where the API is served with `Info.Servers`.** Without it the document has no `servers`, which OpenAPI reads as the root of the host the document was fetched from: right for an app at the root of its host, wrong behind a proxy that strips a prefix, where a client generated from the document would call `https://api.example.com/users` instead of `https://api.example.com/api/users`. A [`Server`](../reference/openapi.md#server) URL is absolute or a path on the document's host, with no trailing slash:

<!-- compiles: body -->
```go
app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1.0.0", Servers: []bites.Server{
	{URL: "https://api.example.com/api", Description: "production"},
	{URL: "/api"},
}})
```

The servers are configuration, read from your environment at start, never from a request's `X-Forwarded-Host`: the document is the same bytes for every client, and a cache in front of the app would otherwise keep one client's host for all ([ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md)).

```http
GET /openapi.json HTTP/1.1
Host: api.example.com

HTTP/1.1 200 OK
Content-Type: application/json

{"components":{...},"info":{"title":"Users","version":"1.0.0"},"openapi":"3.1.0","paths":{"/users":{"post":{...}},"/users/{id}":{"get":{...}}},"tags":[{"name":"users"}]}
```

## Getting the document without serving it

**Take the bytes without serving them with [`app.OpenAPIDocument()`](../reference/openapi.md#appopenapidocument).** A project that checks its document into git, or generates a client in a build step, builds the app, registers its routes and asks for the JSON, with no listener and no request: the returned bytes are what the route serves.

<!-- compiles: body -->
```go
doc, err := app.OpenAPIDocument()
if err != nil {
	log.Fatal(err)
}
os.WriteFile("openapi.json", doc, 0o644)
```

It is JSON: the core has no YAML encoder, so a project that keeps `openapi.yaml` converts it.

## What the document contains

**Every typed route becomes an operation that lists its parameters, its request body and every status it can answer.**

- **Parameters.** The fields of `Path`, `Query`, `Header` and `Cookie` are parameters, with their wire names. A list is an array (a [`CSV`](../reference/input.md#csv) in a query is an array with `style: form` and `explode: false`, in a header `style: simple`), an `Opt` is not required, a path parameter always is, and a [constraint type or a type's `Length`, `Range`, `Items`, `Pattern` or `Format`](./validation.md) states its bounds (`PositiveInt` has `minimum: 1`, a `Range` of 1 to 6 has `minimum: 1` and `maximum: 6` (a float `Range` too), an `Items` of 1 to 8 has `minItems: 1` and `maxItems: 8`, a `Pattern` has `pattern`, a `Format` of `email` has `format: email`, in a response schema too, where the other rules are not stated because a response does not check them). A field a [Provider](./providers.md) fills is not a parameter and not a body.
- **The request body**, by the way its type is read ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)): a struct, slice or map is `application/json` (an anonymous `Body struct{...}` is a component named after its Input, `CreateUser` has `CreateUserBody`, so a client has a type to import; two Inputs that write the same anonymous struct share one Go type, so it is written in place in each operation instead, and a named type is the way to share a component); `Form[T]` is `application/x-www-form-urlencoded`; an `Upload` is `multipart/form-data`; `[]byte` is `*/*`.
- **Success statuses**, by the output type ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)): a plain value is a 200 with its JSON schema; `Created[T]` is a 201, with a required `Location` header on a route registered with [`SetsLocation()`](../reference/openapi.md#setslocation) and none stated otherwise; `NoContent`, `Accepted`, `NotModified`, `SeeOther` and the rest each carry their own status and media type. A `File` is a 200 and a 206 with the headers `File` sends (`ETag`, `Last-Modified`, `Accept-Ranges`, `Content-Disposition`, and `Content-Range` on the 206), a 304, and the 412 and 416 it refuses with. `Bytes`, `File` and `Stream` set their content type at run time, so they are `*/*` unless the route states it with `MediaType` (below).
- **Error statuses.** Every operation names the problem+json answers it can give: a 400 that lists every error found with its location when the route has an Input, a 413 and a 415 for a route with a body, and a `default` for any other error. They are shared components, so the page that reads the document sees one `Error` and one `InvalidInput` ([errors](./errors.md)).
- **Schemas**, in `components`, named once however many routes use a type, and the same whatever order the routes were registered in. A type that writes its own JSON (a database timestamp) is described by the schema the app gave it with [`WithJSONSchema`](../reference/app.md#withjsonschema), and a field of one with no schema is refused at Registration, so the document never says `{}` by accident ([ADR 0076](../adr/0076-a-type-that-writes-its-own-json-is-described-by-a-registration-or-refused.md)). `json.RawMessage` is `{}` on purpose, and a member whose shape is known but whose JSON the app does not decode is a [`RawJSON[T]`](../reference/input.md#rawjson), described as `T` and allowing `null` ([ADR 0087](../adr/0087-a-json-value-the-app-owns-is-raw-json-bytes-documented-as-a-type.md)).

## Describing operations and fields

**Three route options add prose to an operation.** [`Summary`](../reference/openapi.md#summary) is a line, [`Description`](../reference/openapi.md#description) is longer and may use CommonMark, and [`Tag`](../reference/openapi.md#tag) groups operations. `Summary` and `Description` are for a route and not a group, because the same line on every operation of a group would describe none of them. `Tag` may also be given to a group, and a tag a group and its route both give is listed once, the group's first.

<!-- compiles: body -->
```go
admin := app.Group("/admin", bites.Tag("admin"))
admin.Get("/users/{id}", getUser,
	bites.Summary("Get a user as an administrator"),
	bites.Description("Finds a user by id.\n\nA missing user is a 404."),
	bites.Tag("users"))
```

**Mark a route, or a whole group, deprecated with `Deprecated()`.** The operation gets `"deprecated": true` in the document, and a generated client warns its callers. Given to a group it reaches every route in it and in the groups nested in it, like `Tag`; given to a route and its group as well it is the same. A route the document leaves out (WebSocket, Static, `Undocumented`) accepts it too and is still told by `RouteInfo.Deprecated`.

<!-- compiles: body -->
```go
v1 := app.Group("/v1", bites.Deprecated())
v1.Get("/users/{id}", getUser) // deprecated
v1.Group("/admin").Get("/stats", getUser) // deprecated, through the nested group
app.Get("/users/{id}", getUser, bites.Tag("users"))
```

**The framework sends no `Deprecation` or `Sunset` header; one `UseEach` function adds them to every deprecated route.** `RouteInfo.Deprecated` is true for exactly the routes marked in the document, so the headers cannot disagree with it. The function returns nil for a route that is not deprecated, which costs that route nothing. The dates below are examples: `Deprecation` is a structured date ([RFC 9745](https://www.rfc-editor.org/rfc/rfc9745)) and `Sunset` an HTTP date ([RFC 8594](https://www.rfc-editor.org/rfc/rfc8594)).

<!-- compiles: decls -->
```go
func deprecationHeaders(r bites.RouteInfo) (bites.Middleware, error) {
	if !r.Deprecated {
		return nil, nil
	}
	return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		res := next.Call(ctx)
		res.SetHeader("Deprecation", "@1798761600")
		res.SetHeader("Sunset", "Thu, 01 Jul 2027 00:00:00 GMT")
		return res, nil
	}, nil
}
```

<!-- compiles: body -->
```go
app.UseEach(deprecationHeaders) // before the first route
```

**Tag a group once instead of every route.** An API usually has one tag per area of the product, and the area is a group already: `app.Group("/api/commitments", bites.Tag("commitments"))` tags every operation in it, and in the groups nested in it, with `commitments`. A route that belongs to a second area adds its own `Tag`, listed after the group's. A guard or a Provider given to the same group (`UseEach`, `Provide`) is inherited just as the tag is, so the group is the one place that says what an area is.

<!-- compiles: body -->
```go
commitments := app.Group("/api/commitments", bites.Tag("commitments"))
commitments.Get("/overdue", getUser)                       // tags: ["commitments"]
commitments.Get("/{id}", getUser)                         // tags: ["commitments"]
commitments.Post("/{id}/link", getUser, bites.Tag("work")) // tags: ["commitments", "work"]
```

Many client generators make one class or module per tag, so the tag is the name of the client's section; rename a group's tag and the generated section is renamed with it.

**Every operation has an `operationId`, which a client generator names its method by.** It is made from the method and the path, `GET /users/{id}` is `getUsersById`, so renaming a handler never renames a client's method. Give a shorter one with [`OperationID`](../reference/openapi.md#operationid):

<!-- compiles: body -->
```go
app.Get("/users/{id}/avatar", getUser, bites.OperationID("getAvatar"))
```

Two operations whose ids a generator would make one method of (`listUsers` and `list_users`, or `GET /x` and `GET /x/`, which both make `getX`) are refused at the second route's line, naming the first; give one of them an `OperationID`.

**A type describes its own fields with a `Descriptions() map[string]string` method.** The keys are Go field names. It may be defined on a section's struct, on a body type or on an output type ([`Descriptions`](../reference/openapi.md#descriptions-method)).

<!-- compiles: decls -->
```go
type ListQuery struct {
	Page bites.PositiveInt
	Tags []string
}

func (ListQuery) Descriptions() map[string]string {
	return map[string]string{
		"Page": "The page, from 1.",
		"Tags": "Only users with all of these tags.",
	}
}

type ListUsers struct {
	Query ListQuery
}

func (User) Descriptions() map[string]string {
	return map[string]string{"Name": "The display name."}
}
```

The description lands on the parameter or on the property of the schema. **A description cannot outlive its field**: a key that is not a field of the type is a panic at Registration, naming the type, the key and the fields it has.

```text
GET /x: TypoQueryIn.Query.Descriptions names Pag, which is not a field of typoQuery (its fields: Page)
	registered at main.go:30
```

**A section built from embedded structs is described by each embedded struct's own method.** The usual way to share a filter and a page between routes is `Query struct{ Filter; Paging }`; each embedded struct's `Descriptions` describes its own fields, and the section's own `Descriptions`, if it has one, is the route's view: it can describe any field of the section and wins over the embedded entry for the same field.

<!-- compiles: decls -->
```go
type Paging struct {
	Limit  bites.PositiveInt
	Offset int
}

func (Paging) Descriptions() map[string]string {
	return map[string]string{"Limit": "How many rows.", "Offset": "How many rows to skip."}
}

type Filter struct{ Status string }

func (Filter) Descriptions() map[string]string {
	return map[string]string{"Status": "Only rows in this status."}
}

type ListOrders struct {
	Query struct {
		Filter
		Paging
	}
}
```

The prose can live in the doc comment of the field instead: `bites-doc` writes the same method from it ([doc comments](./doc-comments.md), [ADR 0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md)). To browse the document, [mount a documentation UI](./api-docs-ui.md).

## Handlers with more than one answer

**A handler that returns a [`OneOf2`](../reference/responses.md#oneof2), [`OneOf3`](../reference/responses.md#oneof3) or [`OneOf4`](../reference/responses.md#oneof4) is described by every variant, with nothing more to write.** Each type argument is described as it would be if the handler returned it alone, so the document gets one status per variant; two variants with one status share a response, a `oneOf` when their schemas differ ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).

<!-- compiles: decls -->
```go
type UserAnswer = bites.OneOf2[bites.OK[User], bites.NoContent]

func lookUpUser(ctx context.Context, in GetUser) (UserAnswer, error) {
	if in.Path.ID == 0 {
		return UserAnswer{}.B(bites.NoContent{}), nil
	}
	return UserAnswer{}.A(bites.OK[User]{Body: User{ID: in.Path.ID, Name: "Ada"}}), nil
}
```

<!-- compiles: body -->
```go
app.Get("/look-up/{id}", lookUpUser) // 200 with a User, and 204
```

**A handler that returns an interface needs one [`Returns[T]`](../reference/responses.md#returns) for each type it may hold.** It is the fallback for more than four answers, or for an interface the handler already has. The framework cannot see the types behind an interface, so without `Returns` there would be no statuses to describe and a wrong type would be a 500 at run time instead of a refusal now.

<!-- compiles: decls -->
```go
func findUser(ctx context.Context, in GetUser) (any, error) {
	if in.Path.ID == 0 {
		return bites.NoContent{}, nil
	}
	return bites.OK[User]{Body: User{ID: in.Path.ID, Name: "Ada"}}, nil
}
```

<!-- compiles: body -->
```go
app.Get("/find/{id}", findUser,
	bites.Returns[bites.OK[User]](),
	bites.Returns[bites.NoContent](),
)
```

The answer of each type is compiled at Registration, and the document gets its status and schema. `T` is the type itself, and the handler may return it by value or by pointer. These are refused, each naming the route: an interface return with no `Returns`, a `T` that does not implement the interface, the same `T` twice, a pointer or an interface as `T`, `Returns` on a route whose output is not an interface, and `Returns` on a group. `Returns` on a route that returns a OneOf is refused too: its type arguments already say what it returns.

## Error answers a handler returns

**Name the error statuses a handler returns with [`ErrorStatuses`](../reference/openapi.md#errorstatuses), so a client generator knows them.** The document states the errors the framework answers by itself: a 400 for an Input, a 401 for a credential, 413 and 415 for a body, 429 and 403 for the first-party middleware. What the handler decides at run time (a `NotFound`, a `Conflict`, a `PreconditionFailed`) cannot be seen in its types, so without the option it is covered only by the operation's `default` response. Each status named is a problem+json response:

<!-- compiles: body -->
```go
rooms := app.Group("/rooms", bites.ErrorStatuses(404)) // every route of the group may answer 404
rooms.Put("/{id}", func(ctx context.Context, in struct {
	Path struct{ ID int64 }
	Body struct{ Name string }
}) (bites.NoContent, error) {
	return bites.NoContent{}, nil
}, bites.ErrorStatuses(409, 412)) // and this one 409 and 412 too
```

It states what the handler may return and does not check it: a status it returns and the option does not name is still sent, and still covered by `default`. A status whose answer carries data is stated with [`ErrorStatus[T]`](../reference/openapi.md#errorstatus) instead, and the document then lists the fields of `T` beside the problem's own members ([errors](./errors.md#data-the-client-reads)).

## Examples and deprecated fields

**A type gives its own example with an `Example() T` method.** A named type (a body, a field's type, a parameter's type) may declare `func (T) Example() T`; the document states its value, as the wire writes it, in the `examples` of the type's schema, once for a component and again nowhere (every use refers to it). The value is checked when the route is registered: written and read back through the same codec a request is, so an example that `Length`, `Range`, `Pattern`, `Format`, `Check` or a missing required member would refuse is a panic naming the rule, and not a document that shows a client a value the API answers 400 for ([`Example` method](../reference/openapi.md#example-method)).

<!-- compiles: decls -->
```go
type Handle string

func (Handle) Length() (min, max int) { return 3, 12 }
func (Handle) Example() Handle        { return "ayu_k" }

type Profile struct {
	Handle Handle
	City   string
}

func (Profile) Example() Profile { return Profile{Handle: "ayu_k", City: "Bandung"} }
```

A field of a plain `string` has no type of its own to carry an example: the example of the struct that holds it states every field.

**A struct names its deprecated fields with `DeprecatedFields() []string`.** The strings are Go field names, as the keys of `Descriptions` are, and it works on the struct of a `Query`, `Path`, `Header` or `Cookie` section (the parameters get `deprecated: true`), on a body and on a response; a name that is not a field is a panic at Registration, listing the fields there are ([`DeprecatedFields` method](../reference/openapi.md#deprecatedfields-method)). A deprecated field is still accepted and still sent: the document states it, and nothing is refused. `Deprecated()` on a route or a group deprecates the whole operation.

<!-- compiles: decls -->
```go
type SearchQuery struct {
	Term   string
	Sort   string
	Offset int
}

func (SearchQuery) DeprecatedFields() []string { return []string{"Offset"} }
```

## Media types and headers the handler sets

**State what a `Bytes`, `File` or `Stream` answer sends with [`MediaType`](../reference/openapi.md#mediatype), and each header a handler sets with [`SendsHeader`](../reference/openapi.md#sendsheader).** The handler decides both at run time, where Registration cannot see them, so without the options an NDJSON export is "any bytes" (`*/*`) to a client generator, and an `ETag` the handler puts in an `OK`'s `Header` is not in the document:

<!-- compiles: body -->
```go
app.Get("/export", func(ctx context.Context, in struct{}) (bites.Stream, error) {
	return bites.Stream{Header: bites.Header{"Content-Type": {"application/x-ndjson"}}, Func: func(ctx context.Context, w *bites.StreamWriter) error {
		_, err := w.WriteString("{}\n")
		return err
	}}, nil
}, bites.MediaType("application/x-ndjson"))

app.Put("/notes/{id}", func(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.OK[string], error) {
	return bites.OK[string]{Body: "saved", Header: bites.Header{"ETag": {`"4"`}}}, nil
}, bites.SendsHeader[string]("ETag", "The version of the note, for If-Match on the next write."))
```

Both state and do not check, like `ErrorStatuses`: an answer of another type, or a header not named, is still sent. `MediaType` is for a route, and is refused on a route whose answers already have a media type (JSON, text, an event stream); `SendsHeader` may be given to a group too: its type says the header's schema (`SendsHeader[int]("X-Total-Count", "How many records match, before paging.")` is an integer), and it needs a description, a name not given twice, and a text type; it refuses `Content-Type` and `Location`, which `MediaType` and `SetsLocation` state.

## Security

The document says which credential each route needs, from the Providers the route runs ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)). Nothing is declared twice: a route that resolves a Provider made by `BearerAuth`, `BasicAuth`, `APIKeyAuth` or `CustomAuth`, directly or through a Provider that needs its value, has the scheme in `components.securitySchemes` and in its operation's `security`, with a 401. An `AnyAuth` lists each of its credentials as an alternative requirement, and an `Opt` field of the user's type adds the empty requirement `{}`, since the route also serves a caller who sends none. A route that asks for no credential has no `security`, even when a Provider covers its group. A `RateLimiter`'s middleware on the route, its group or the app adds a 429 with `Retry-After`, and `CSRF` adds a 403 to the methods it checks. See [security](./security.md#what-the-api-document-says-about-it).

## What is left out

**A route you leave out.** Give it [`Undocumented()`](../reference/openapi.md#undocumented), or give it to a group for all its routes: the route is served as any other and is not in the document, nor are its types. Mounted `http.Handler`s ([mounting](./mounting-net-http.md)) and the documentation page ([docsui](./api-docs-ui.md)) are left out this way already. A `Summary`, `Description` or `OperationID` on an undocumented route is refused, since the document would drop it. It hides the route from the document, not from a client: protect an internal route with middleware.

<!-- compiles: body -->
```go
internal := app.Group("/internal", bites.Undocumented())
internal.Get("/state", func(ctx context.Context, in struct{}) (bites.Text, error) {
	return bites.Text{Body: "ok"}, nil
})
```

**A [WebSocket route](./websockets.md) is the `get` of its handshake.** Its parameters, its `security` and the refusals of a handshake (`400`, `403` for an Origin it does not allow, `426`) are stated as for any route, with `101` as its answer, and its messages under the extension `x-websocket`: `clientMessage` is the schema of what a client sends (the Inbox's type) and `serverMessage` of what the server sends (the Socket's), since OpenAPI 3.1 has no keyword for them. `Summary`, `Description`, `OperationID`, `Tag` and `ErrorStatuses` apply to it; `Returns`, `MediaType` and `SendsHeader` are refused, since it answers 101 with no body.

**Routes on a method OpenAPI 3.1 has no name for are not in the document.** A route on a method outside GET, PUT, POST, DELETE, OPTIONS, HEAD, PATCH and TRACE is served but not described.

**Two mistakes that only the document shows are refused too.** Two routes whose paths differ only in the names of their wildcards (`/u/{id}` and `/u/{uid}`) are one path to OpenAPI, so the second is refused naming the first; and a route twice for one method and path template. Calling `OpenAPI` twice, with a path that is not a plain absolute path, or with no `Title` or `Version`, panics ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

```text
OpenAPI d: the path must start with a slash and have no wildcard, query or fragment
	registered at main.go:8
```
