# OpenAPI

**The OpenAPI 3.1 document of an app's typed routes: how to serve it, the options that give an operation its prose, and the Descriptions method that describes fields.**

**Guide:** [the OpenAPI document](../guide/openapi.md) · **Design:** [OpenAPI](../design/openapi.md)

The API document is described at Registration, from the types, and written once when the app starts serving ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)). Every typed route leaves a record when it is registered: its parameters, request body, responses and tags, built from the Input and Output types and the options below. A type the document cannot describe, and a description that names a missing field, are Registration panics naming the route and the line. The names of the component schemas can be decided only once every type is known, so the document is put together and written as JSON when the app starts serving, and a request for it copies those bytes into the response.

What is described: the fields of `Path`, `Query`, `Header` and `Cookie` as parameters (a list is an array, an `Opt` is not required, a constraint type states its bounds); the Body by the way its type is read; the success responses by the Output type, including each type given to [`Returns`](./responses.md#returns), a `File` with the headers and statuses it sends itself, and a `Bytes`, `File` or `Stream` as any type (`*/*`) unless [`MediaType`](#mediatype) states it; and two shared error responses, `InvalidInput` (the 400 with its `errors` list) and `Error` (every other problem+json answer), which each operation refers to. A field a Provider fills is not a parameter. Prose cannot come from types, so it comes from the options below and from the `Descriptions` method.

Every operation has an `operationId`, made from its method and path (`GET /users/{id}` is `getUsersById`) unless [`OperationID`](#operationid) gives one; a client generator names the method that calls the operation by it ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)).

Security ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)): a route that runs a Provider made by [`BearerAuth`, `BasicAuth`, `APIKeyAuth`](./security.md#bearerauth) or [`CustomAuth`](./security.md#customauth) has the scheme under `components.securitySchemes` (`bearerAuth`, `basicAuth`, `apiKeyAuth` for `X-API-Key`, `apiKeyAuth.<header>` for another header, `cookieAuth.<name>` for a cookie, `<scheme>Auth` for another HTTP scheme, `mutualTLS`, `openIdConnect`) and in the operation's `security`, with a 401 that refers to `components.responses.Unauthorized` and a 400 that refers to `Error` when the route has none. The credentials of the Providers a route runs are required together, in one requirement; an [`AnyAuth`](./security.md#anyauth) offers each of its credentials as an alternative requirement, and an `Opt` field of an auth Provider's type offers also the empty requirement `{}`, since the credential may be left out. A route behind a [`RateLimiter`](./security.md#ratelimiter)'s middleware, its own, a group's or the app's, gets a 429 that refers to `TooManyRequests` (`Retry-After`, `RateLimit-Policy`, `RateLimit`); a route behind [`CSRF`](./security.md#csrf) gets a 403 that refers to `Error` for every method but GET, HEAD, OPTIONS and TRACE. A Provider or middleware the application wrote is not recognised unless `CustomAuth` gives it its scheme, and is never called at Registration.

A request `Body` that is an anonymous struct (`Body struct{ Name string }`) is a component named after its Input with `Body` appended: `CreateUser` has `CreateUserBody`, and a name given by [`Info.SchemaName`](#info) replaces it as it replaces any other. Two Inputs that write the same anonymous struct hold one Go type, so neither name fits the other's body: that body is written in place in each operation, and a type declared with a name (`type RoomFields struct{...}`) is the way to share one component; an anonymous Input, an anonymous response and an anonymous struct nested in another type stay inline. A `Created` states a `Location` header only on a route registered with [`SetsLocation`](#setslocation), as a required one.

What is left out: a route given [`Undocumented`](#undocumented) and routes on a method OpenAPI 3.1 has no name for. A WebSocket route is the `get` of its handshake: its parameters and `security`, a `101`, the handshake's `400`, `403` and `426`, and its messages under `x-websocket` as `clientMessage` (the Inbox's type) and `serverMessage` (the Socket's), since OpenAPI 3.1 has no keyword for them.

Allocations: none per request. The document is built once and served as the same bytes every time; the plain typed JSON request still costs 2 ([ADR 0003](../adr/0003-the-trade-budget.md)). Serving the document runs through the same engine as any GET and HEAD route.

<!-- compiles: body -->

```go
app.OpenAPI("/openapi.json", bites.Info{Title: "Users API", Version: "1.0.0", Description: "The users of the reference workload."})
app.Get("/users", func(ctx context.Context, in struct{}) (bites.Text, error) {
	return bites.Text{Body: "ada"}, nil
}, bites.Summary("List the users"), bites.Description("Every user, **newest first**."), bites.Tag("users"))
```

Guide: [the OpenAPI document](../guide/openapi.md). Decision: [ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md).

What Registration refuses, as rows of the refusal table (after the route label, then `registered at file.go:N`):

| Mistake | Message |
|---|---|
| `OpenAPI` called twice | `OpenAPI /e: the document is already served at /d (registered at file.go:N)` |
| a path that is not a plain absolute path | `OpenAPI d: the path must start with a slash and have no wildcard, query or fragment` |
| no `Title` | `OpenAPI /d: Info.Title is empty: OpenAPI requires a title` |
| no `Version` | `OpenAPI /d: Info.Version is empty: OpenAPI requires the version of the API (not of OpenAPI)` |
| a `Server` with no URL | `OpenAPI /d: Info.Servers[0].URL "" is empty: give the URL the API is served at, such as https://api.example.com or /api` |
| a `Server` URL of `/` | `OpenAPI /d: Info.Servers[0].URL "/" is "/", which OpenAPI reads when servers is absent: leave Servers out` |
| a `Server` URL with a trailing slash | `OpenAPI /d: Info.Servers[0].URL "https://api.example.com/" ends with a slash, and every path of the document begins with one, so a client would ask for //...: drop the trailing slash` |
| a `Server` URL with a variable | `OpenAPI /d: Info.Servers[0].URL "https://{region}.example.com" has a server variable, which is not supported: write the URL in full` |
| a `Server` URL with a query or fragment | `OpenAPI /d: Info.Servers[0].URL "https://api.example.com?v=1" has a query or a fragment: a server URL is scheme://host[:port][/path] or /path` |
| a `Server` URL that is neither `http(s)` nor a path | `OpenAPI /d: Info.Servers[0].URL "api.example.com/v1" is neither an http or https URL nor a path that starts with a slash` |
| a `Server` URL whose host is not one (a userinfo, a space) | `OpenAPI /d: Info.Servers[0].URL "https://ada:pw@api.example.com" has a host that is not a host name, an IPv4 address or a bracketed IPv6 address with an optional port (a userinfo or a space is refused)` |
| a `Server` URL whose path has an empty, `.` or `..` segment | `OpenAPI /d: Info.Servers[0].URL "/api/../admin" has a path that is not an absolute path of RFC 3986 path characters with no empty, "." or ".." segment` |
| a `Server` URL given twice | `OpenAPI /d: Info.Servers[1].URL "/api" is given twice (also Servers[0])` |
| two paths that differ only in wildcard names | `POST /u/{uid}: the API document would hold /u/{uid} and /u/{id} as two paths, but OpenAPI treats paths that differ only in the names of their wildcards as one: use the same wildcard names in both (the other is registered at file.go:N)` |
| two routes held as one operation | `GET /x/{$}: the API document would hold GET /x/ twice: the route registered at file.go:N already describes it` |
| two different types with one name and package path | at start: `OpenAPI /d: the API document cannot be built: openapi: two different types are both named ... so they cannot get distinct component names; give one of them another name` |
| `Info.SchemaName` gives two types one name | at start: `OpenAPI /d: the API document cannot be built: openapi: the schema namer gave pkg/a.User and pkg/b.User the same name "Same", but each component needs its own` |
| `Info.SchemaName` gives a name with a character a component name cannot have | at start: `OpenAPI /d: the API document cannot be built: openapi: the schema namer named pkg/a.User "Commitment List", but a component name may use only A-Z a-z 0-9 . - and _` |
| `Info.SchemaName` gives a name another type has by default | at start: `OpenAPI /d: the API document cannot be built: openapi: the schema namer named pkg/a.User "User", which is the name pkg/b.User has without it: name both or neither` |
| a description naming no field | see [Descriptions method](#descriptions-method) |
| `Summary` or `Description` empty | `GET /x: Summary is empty`, `GET /x: Description is empty` |
| `Summary` or `Description` on a group | `group "/g": Summary is given to a group, but one summary cannot describe every operation in it: give it to each route` |
| `Tag` with no name or an empty one | `GET /x: Tag is given no name`, `GET /x: Tag is given an empty name` |
| `Summary`, `Description` or `OperationID` on an `Undocumented` route | `GET /x: Summary, Description and OperationID describe an operation of the API document, which Undocumented leaves this route out of: give one or the other` |
| `SetsLocation` on a group | `group "/g": SetsLocation is given to a group, but only some of its routes may return a Created: give it to each route that does` |
| `SetsLocation` on a route that cannot return a `Created` | `GET /x: SetsLocation is given, but the handler returns struct {}, which cannot be a Created: only a Created has a Location the handler may leave empty` |
| `SetsLocation` on a WebSocket or a static route | `WebSocket /x: SetsLocation is given to a WebSocket route, which answers 101 and has no Created to carry a Location`, `Static /s: SetsLocation is given to a static route, which answers files and has no Created to carry a Location` |
| `ErrorStatuses` with no status | `GET /x: ErrorStatuses is given no status: name the error statuses the handler returns, as ErrorStatuses(404, 409)` |
| `ErrorStatuses` given a status outside 400 to 599 | `GET /x: ErrorStatuses is given 302, which is not an error status: give the statuses from 400 to 599 the handler returns` |
| `ErrorStatuses` given one status twice | `GET /x: ErrorStatuses is given 404 twice` |
| `ErrorStatuses` on an `Undocumented` route | `GET /x: ErrorStatuses states answers in the API document, which Undocumented leaves this route out of: give one or the other` |
| `ErrorStatuses` on a static route | `Static /s: ErrorStatuses states answers in the API document, which leaves static files out: give it to the typed routes instead` |
| `ErrorStatus` given a status outside 400 to 599 | `GET /x: ErrorStatus[StaleBlocks] is given 302, which is not an error status: give a status from 400 to 599 the handler returns` |
| `ErrorStatus` given 400, 401 or 429 | `GET /x: ErrorStatus[StaleBlocks] is given 429, which the framework answers in a shape of its own (400 lists the Input's mistakes, 401 and 429 carry headers): use a status the handler decides, such as 409 or 422` |
| `ErrorStatus` for a status the framework already answers on the route | `POST /x: ErrorStatus[StaleBlocks](413): the framework already answers 413 on this route with a shape of its own, so the document cannot state members for it: use a status the handler decides, such as 409 or 422` |
| `ErrorStatus` given a type that is not a struct | `GET /x: ErrorStatus[string](409): the members of an error are the fields of a struct, so T must be a struct type, as ErrorStatus[StaleBlocks](409)` |
| `ErrorStatus` given one status twice, on the route and its group | `GET /g/x: ErrorStatus is given 409 twice: state the members of a status once, on the route or on its group` |
| `ErrorStatus` with a field written as a member of the problem | `GET /x: ErrorStatus[Clashing](409): a field of Clashing is written as "title", a member the problem body already has (type, title, status, detail, instance, errors, truncated, request_id): give the field another wire name with a json tag` |
| `ErrorStatus` on an `Undocumented` route | `GET /x: ErrorStatus states answers in the API document, which Undocumented leaves this route out of: give one or the other` |
| `MediaType` on a group | `group "/g": MediaType is given to a group, but the routes of a group send different types: give it to each route` |
| a `MediaType` that is not a media type, or has a wildcard | `GET /x: MediaType "ndjson" is not a media type: give type/subtype, with no wildcard, as "application/x-ndjson" or "text/csv; charset=utf-8"` |
| `MediaType` on a route with no `Bytes`, `File` or `Stream` answer | `GET /x: MediaType is given, but every answer of the handler has a media type the API document already states (JSON, text or an event stream): MediaType is for a Bytes, File or Stream answer` |
| `SendsHeader` with a name that is not a header name | `GET /x: SendsHeader is given "E Tag", which is not a header name` |
| `SendsHeader` with `Content-Type` or `Location` | `GET /x: SendsHeader is given Content-Type, which the media type of the answer states: use MediaType`, `GET /x: SendsHeader is given Location, which SetsLocation states for a Created and a redirect states by itself: use SetsLocation` |
| `SendsHeader` with no description | `GET /x: SendsHeader X-Total-Count has no description: say what the header means to a client, as SendsHeader[int]("X-Total-Count", "...")` |
| `SendsHeader` given one header twice, also by a group and its route | `GET /x: SendsHeader is given etag twice: state a header once, on the route or on its group` |
| `SendsHeader` of a type that is not text | `GET /x: SendsHeader[[]string] is given for X-List, but a header value is text and []string cannot be read as text (...): use a string, a number, a bool or a named type of one` |
| an `Example` its own type refuses | `POST /x: the API description cannot describe the Body: exBadHandle.Example returns "ab", which its own type refuses: must be between 3 and 12 characters: change the value so the type accepts it` |
| an `Example` that panics, or a method named `Example` that returns another type | `GET /x: the API description cannot describe exPanicky: exPanicky.Example panicked: no example today`, `... exWrongSig.Example has the signature func() string, but an example is a method that returns the type's own value: write func (exWrongSig) Example() exWrongSig, or rename the method` |
| a `DeprecatedFields` name that is not a field | see [`DeprecatedFields` method](#deprecatedfields-method) |
| `MediaType` or `SendsHeader` on an `Undocumented` or static route | as `ErrorStatuses` above, with the option's name: `GET /x: MediaType states answers in the API document, which Undocumented leaves this route out of: give one or the other` |
| `MediaType` or `SendsHeader` on a WebSocket route | `WebSocket /x: SendsHeader states what a handler's answer sends, but a WebSocket route answers 101 with no body, and its messages are the Socket's type` |
| `OperationID` on a group | `group "/g": OperationID is given to a group, but an operationId names one operation: give it to each route` |
| an `OperationID` that is not a method name | `GET /x: OperationID "list users" is not an operationId a client generator can name a method by: start with a letter and use letters, digits, '_', '.' and '-'` |
| two `OperationID`s one method name | `GET /b: OperationID "list_users" names the same method of a generated client as the operationId "listUsers" of the route registered at file.go:N (given with OperationID): give each operation its own` |
| two routes whose method and path make one id | `GET /x/: the operationId made from the method and path, "getX", names the same method of a generated client as the operationId "getX" of the route registered at file.go:N (made from its method and path): give this route its own with OperationID` |

## Serving the document

### `App.OpenAPI`

```go
func (a *App) OpenAPI(path string, info Info)
```

OpenAPI serves the API document of the app's typed routes as `application/json` at `path`, for GET and HEAD. It is opt-in: nothing is served at a default path, because an API description is a decision about what to publish. The document is built once, when the app starts serving, and served as the same bytes every time; a route added after that is refused, as always. It is a method that registers a route, so a conflict with a route of the application is raised in order, at the line that registered the second.

It panics when called twice, when `path` does not start with a slash or has a wildcard, a query or a fragment, when `Info.Title` or `Info.Version` is empty, or when a [`Server`](#server) URL is refused (the table above). A mistake only the whole set of types shows panics when serving starts, naming the line that asked for the document.

The route is passed to the function of an app-level [`UseEach`](./middleware.md#useeach) with `RouteInfo.Builtin` set to `"openapi"`, so a default-deny guard sees it and must exempt it by that name or guard it (`GET /openapi.json` answers 401 behind a session guard); `UseEach` after `OpenAPI` panics, as after a route.

Guide: [the OpenAPI document](../guide/openapi.md).

### `App.OpenAPIDocument`

```go
func (a *App) OpenAPIDocument() ([]byte, error)
```

OpenAPIDocument returns the bytes of the API document, the JSON that [`OpenAPI`](#appopenapi) serves, without serving it: a test that compares the document with a file checked into the repository, or a `go generate` program that writes it, calls it once the routes are registered. It builds the document of the routes registered so far, so call it after the last one; it does not start the app or freeze the routes, and a route registered afterwards is not in bytes already returned. The slice is the caller's copy and equals what the document route answers. It returns an error when `OpenAPI` was never called, and when the document cannot be built (two types given one component name), where serving the same app would panic. It allocates and is for a build step or a test, never for a request. The document is JSON only: the core has no YAML encoder ([ADR 0006](../adr/0006-no-third-party-dependency-in-the-core.md)), so a project that keeps `openapi.yaml` converts these bytes.

Guide: [the OpenAPI document](../guide/openapi.md#getting-the-document-without-serving-it).

### `Info`

```go
type Info struct {
	Title       string
	Version     string
	Description string
	Servers     []Server
	SchemaName  func(pkgPath, name string) string
}
```

Info is the identity of the API in its document. `Title` and `Version` are required by OpenAPI, and `Version` is the version of the API, not of OpenAPI. `Description` is optional and may use CommonMark. `Servers` are where the API is served, written as the document's top-level `servers` in the order given; without them the document has none, which OpenAPI reads as `/` on the host the document was fetched from, wrong behind a proxy that strips a prefix. They are fixed at Registration and never read from a forwarding header, since the document is the same bytes for every request ([ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md)).

`SchemaName` names the component schemas. It is called once for each Go type that becomes a component, in a fixed order, with the type's package path and the name the framework would give it (the Go name, a generic instance's type arguments appended, anything outside `A-Z a-z 0-9 . - _` written `_`), and returns the component name, or `""` to keep the default rules for that type. A name it returns is used for that type whether or not the default would collide; the `.Request`, `.RequestOpen` and `.Response` suffixes of a type whose schemas differ by mode are added after it; the types it leaves alone are named by the default rules among themselves. It is refused when the document is built, with the line that called `OpenAPI` (the table above). Allocations: none per request, since the namer runs once, when the app starts serving ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)).

### `Server`

```go
type Server struct {
	URL         string
	Description string
}
```

Server is one entry of the document's `servers`: `URL` is an absolute `http` or `https` URL (`https://api.example.com/api`) or a path on the host the document was fetched from (`/api`), with no trailing slash, query, fragment, userinfo, server variable or `.`, `..` or empty segment, since the document's paths are appended to it; `Description` is optional. Each refusal is a row of the table above.

Guide: [the OpenAPI document](../guide/openapi.md#serving-the-document). Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

## Describing an operation

These are `RouteOption` values for a route. `Summary`, `Description` and `OperationID` are refused on a group, because one line or one id on every operation of a group would describe none of them, and so are `SetsLocation` and `MediaType`, because the routes of a group differ in what they return; `Tag`, `ErrorStatuses` and `SendsHeader` are accepted on a group. On a WebSocket route `Summary`, `Description`, `OperationID`, `Tag`, `Deprecated`, `ErrorStatuses` and `Undocumented` apply as on any route, and `SetsLocation`, `MediaType`, `SendsHeader` and `Returns` are refused, as the table above says.

### `Summary`

```go
func Summary(s string) RouteOption
```

Summary sets the one-line summary of the route's operation in the API document. It is for a route, not a group. An empty or blank summary is refused.

### `Description`

```go
func Description(s string) RouteOption
```

Description sets the longer description of the route's operation in the API document (CommonMark is allowed). Like `Summary` it is for a route, not a group.

### `Tag`

```go
func Tag(names ...string) RouteOption
```

Tag adds tags to the route's operation in the API document, or to every operation of a group when it is given to `Group`. A tag a group and its route both give is listed once, the group's first. A route's own tags follow the groups'.

### `Deprecated`

```go
func Deprecated() RouteOption
```

Deprecated marks the route's operation deprecated in the API document: the operation gets `"deprecated": true`, which a client generator and a documentation page show as a warning. Given to `Group` (or inside [`Options`](./app.md#options-1)) it deprecates every route of the group and of the groups nested in it, as `Tag` does; giving it to a route and to its group as well is harmless. A route that is not deprecated has no `deprecated` member. It also sets [`RouteInfo.Deprecated`](./middleware.md#routeinfo), which a `UseEach` function reads to add the `Deprecation` and `Sunset` headers itself; the framework sends no header. On a Static or `Undocumented` route, which the document has no operation for, it is accepted and still reaches `RouteInfo`. Only the operation is marked: a deprecated field or parameter is not part of it.

### `OperationID`

```go
func OperationID(id string) RouteOption
```

OperationID sets the `operationId` of the route's operation, which a client generator (openapi-generator, oapi-codegen) turns into the name of the method that calls it ([ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md)). Without it the id is made from the method and the path: the method in lower case, each literal segment as words with a capital each, and each wildcard as `By` and its name, so `GET /users/{id}/orders` is `getUsersByIdOrders`, `POST /api.v1/user-groups` is `postApiV1UserGroups` and `GET /` is `getRoot`. The id must start with a letter and hold only letters, digits, `_`, `.` and `-`. Two operations whose ids differ only in case or in those three marks are refused at the second route's Registration, given or made, since a generator makes one method name of both; renaming a handler never changes an id, and changing a path changes the made one. It is for a route, not a group; it is refused on an `Undocumented` route, a WebSocket route and `Static`.

<!-- compiles: body -->

```go
app.Get("/users/{id}", func(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.Text, error) {
	return bites.Text{Body: "ada"}, nil
}, bites.OperationID("getUser"))
```

### `SetsLocation`

```go
func SetsLocation() RouteOption
```

SetsLocation says that every [`Created`](./responses.md#created) the route answers carries a `Location`. The document then states the header on the 201 as required; without the option it states none, because a `Created` sends `Location` only when the handler fills the field and most handlers do not. A handler on such a route that returns a `Created` with an empty `Location` gets a logged 500 (`a Created has an empty Location, but its route is registered with SetsLocation and the API document promises the header: set Location, or drop SetsLocation`). A handler that sets `Location` on a route without the option still sends it. It covers a `Created` returned plainly, as a variant of a `OneOf` and as a type given to `Returns`. It is refused on a group and on a route that cannot return a `Created`, as the table above says. Decision: [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md).

### `ErrorStatuses`

```go
func ErrorStatuses(statuses ...int) RouteOption
```

ErrorStatuses states the error statuses the route's handler, or its Providers, may return, so the document lists each as a problem+json response (a reference to `components.responses.Error`) beside the ones the framework states itself: the 400 of the Input, the 401 of a credential, the 413 and 415 of a body, the 429 and 403 of the first-party middleware. Given to a group, it covers every route of the group, and a group's and a route's statuses add up. A status the framework already states keeps its own response: `ErrorStatuses(400)` on a route with an Input leaves its 400 as `InvalidInput`. A 429 is a reference to `components.responses.TooManyRequests`, the rate limit's answer with `Retry-After`, `RateLimit-Policy` and `RateLimit`: that is how a Provider that calls [`RateLimiter.Allow`](./security.md#ratelimiterallow) states its limit, since the document cannot see a call inside a Provider. It does not limit what the handler may return: an error status not named is still sent, and the operation's `default` response covers it. Refused, as the table above says: no status, a status outside 400 to 599, a status named twice in one call, and the option on an `Undocumented`, WebSocket or static route. Allocations: none per request; it is read once, when the document is built. Decision: [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md).

<!-- compiles: body -->

```go
app.Delete("/rooms/{id}", func(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.NoContent, error) {
	return bites.NoContent{}, bites.NotFound("no room with this id")
}, bites.ErrorStatuses(404, 409))
```

### `ErrorStatus`

```go
func ErrorStatus[T any](status int) RouteOption
```

ErrorStatus states an error status the route's handler may return together with the type of the members its answer carries, so the document, and the client types generated from it, say what the problem body holds beyond the framework's own members. The response for that status is `application/problem+json` whose schema is `allOf` the problem's members (`title`, `status`, `detail`, `request_id`) and the schema of `T`, a named component with the wire names of its fields. The handler sends the data with [`WithMembers`](./errors.md#withmembers), which writes `T` with the response codec. `T` must be a struct. Given to a group, it covers every route of the group. It is the typed form of [`ErrorStatuses`](#errorstatuses) for one status, and replaces it for that status when both name it. It states and does not check: an error that carries members of a type the route does not state is still sent, and logged at `WARN` with the option to add. Refused, as the table above says: a status outside 400 to 599; 400, 401 and 429, which the framework answers in shapes of its own; a status the framework already answers on the route (413 and 415 on a route with a body); a `T` that is not a struct or cannot be written as JSON; a field of `T` written as `type`, `title`, `status`, `detail`, `instance`, `errors`, `truncated` or `request_id`; a status named twice on a route and its groups; and the option on an `Undocumented` or static route. Allocations: none per request; it is read once, when the document is built. Decision: [ADR 0088](../adr/0088-an-error-that-carries-data-states-its-type-with-error-status-and-sends-it-with-members.md).

<!-- compiles: decls -->

```go
type StaleBlocks struct {
	Conflicts []string
}

func registerOps(app *bites.App) {
	app.Post("/docs/{id}/ops", func(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.NoContent, error) {
		return bites.NoContent{}, bites.WithMembers(bites.Conflict("stale batch"), StaleBlocks{Conflicts: []string{"b1"}})
	}, bites.ErrorStatus[StaleBlocks](409))
}
```

### `MediaType`

```go
func MediaType(media string) RouteOption
```

MediaType states the media type of what a `Bytes`, `File` or `Stream` answer of the route sends, so the document says `application/x-ndjson` where it would say any type (`*/*`). The handler sets the type at run time, where Registration cannot see it. It is for a route, not a group, and is refused on a route whose answers all have a media type the document already states (JSON, text, an event stream), and on a media type that is not `type/subtype` or has a wildcard. It states and does not check: an answer of another type is still sent as it is. Allocations: none per request. Decision: [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md).

<!-- compiles: body -->

```go
app.Get("/report.csv", func(ctx context.Context, in struct{}) (bites.Bytes, error) {
	return bites.Bytes{ContentType: "text/csv; charset=utf-8", Body: []byte("id,name\n1,ada\n")}, nil
}, bites.MediaType("text/csv; charset=utf-8"))
```

### `SendsHeader`

```go
func SendsHeader[T any](name, description string) RouteOption
```

SendsHeader states one response header the route's handler sets on a success answer, or the handler of every route of a group, so the document lists it on every success status beside the ones the framework states itself (a `Created`'s `Location`, a `File`'s `ETag`). `T` is the Go type of the value and gives the header's schema as it does for a `Query` field (a number is an `integer`, a named type states its `Length`, `Range`, `Pattern`, `Format` and its [`Example`](#example-method)); `description` says what the header means and may not be empty. The header is optional; a handler sends it only through the `Header` of the value it returns. A group's and a route's add up; where the framework already states the header, the route's description replaces it. Refused: a name that is not a header name, an empty description, a name given twice (a group and its route included), `Content-Type` (use `MediaType`) and `Location` (use `SetsLocation`), a `T` that is not text (a struct, a slice, a map), and the option on an `Undocumented`, WebSocket or static route. It states and does not check: a header not named is still sent. Allocations: none per request. Decision: [ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md), [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md).

<!-- compiles: body -->

```go
app.Get("/notes/{id}", func(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.OK[string], error) {
	return bites.OK[string]{Body: "a note", Header: bites.Header{"ETag": {`"4"`}, "X-Total-Count": {"12"}}}, nil
}, bites.SendsHeader[string]("ETag", "The version of the note."), bites.SendsHeader[int]("X-Total-Count", "How many notes match, before paging."))
```

### `Undocumented`

```go
func Undocumented() RouteOption
```

Undocumented leaves the route, or every route of a group when it is given to `Group`, out of the API document ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)): a mounted `http.Handler`, the documentation page, an internal or debugging route. The route is served as any other; it is not in `paths`, its types are not described (so a type the document could not describe is not refused there), and the check that the document can hold its path is not made. A `Tag` on it is ignored; a `Summary` or `Description` on the same route is refused. [`stdhttp.Mount`](./stdhttp.md#stdhttpmount) and [`docsui.Mount`](./docsui.md#docsuimount) give it to their routes. It hides the route from the document, not from a client.

<!-- compiles: body -->

```go
app.Get("/internal/state", func(ctx context.Context, in struct{}) (bites.Text, error) {
	return bites.Text{Body: "ok"}, nil
}, bites.Undocumented())
```

### Descriptions method

A struct type may have the optional method `Descriptions() map[string]string`, which returns its field descriptions by Go field name:

```go
func (UserBody) Descriptions() map[string]string
```

The description lands on the property (or the parameter); a property that is a `$ref` carries it beside the reference. It may be declared on a Body type, on a type in a response, on a type nested in either, and on the struct of a `Path`, `Query`, `Header` or `Cookie` section. Each name is checked at Registration against the fields the codec reads and writes for the type (so a field left out with `json:"-"`, or a typo, is a panic listing the fields that exist), and a nested type's method is checked only where the document reaches it. The checks are rows of the refusal table:

| Where | Message (after the route label) |
|---|---|
| a Body | `the API description cannot describe the Body: typoBody.Descriptions names Nmae, which is not a field of typoBody on the wire (its fields: Name)` |
| a response | `the API description cannot describe the response typoOut: typoOut.Descriptions names Totl, which is not a field of typoOut on the wire (its fields: Total)` |
| a Section | `TypoQueryIn.Query.Descriptions names Pag, which is not a field of typoQuery (its fields: Page)` |

<!-- compiles: decls -->

```go
type NewUser struct {
	Name  string
	Email string
}

func (NewUser) Descriptions() map[string]string {
	return map[string]string{
		"Name":  "The name shown to other users.",
		"Email": "Where the confirmation is sent.",
	}
}

type CreateUser struct{ Body NewUser }

func createUser(ctx context.Context, in CreateUser) (bites.Created[NewUser], error) {
	return bites.Created[NewUser]{Body: in.Body, Location: "/users/1"}, nil
}
```

**Embedded sections.** A `Query` (or `Path`, `Header`, `Cookie`) built from embedded structs, `Query struct{ FilterQuery; Paging }`, is described by each embedded struct's own `Descriptions`, which describes that struct's own fields; Go promotes neither method when both exist, so the framework reads each type's own. The section struct's own `Descriptions`, when it declares one, may describe any field of the section, embedded ones included, and wins over an embedded type's entry for the same field. A method that exists only because one embedded type has it, promoted, is not the section's own. Each method's names are checked against the fields of the type that declares it, so an embedded type that names a field it lacks is a panic naming that type:

| Where | Message (after the route label) |
|---|---|
| an embedded section | `typoQuery.Descriptions names Pag, which is not a field of typoQuery (its fields: Page)` |

Two embedded types cannot describe one field: a field of one name in both is two parameters with one wire name, which is already refused. An embedded struct in a JSON `Body` is not covered by this; its fields are read by the codec.

### Example method

A named type may have the optional method `Example() T`, with `T` the type itself, on `T` or on `*T`:

```go
func (Handle) Example() Handle
```

It is read once at Registration on a zero receiver, written with the codec the wire uses and read back through the decoder the type is read with, so every rule of the type applies to it (`Length`, `Range`, `Items`, `Pattern`, `Format`, `Check`, a required member that is missing). The document states the JSON beside the type's schema as `examples: [value]`: a type that is a component carries it once, and every property, parameter, body and header that refers to it shows it. A struct's `Example` states every field in it, `Opt` and plain `string` fields included. A response type is read back by a plan in which nothing is required, but every rule of a type still applies. Refused at Registration, naming the type: a value the type refuses, a panic, a value the codec cannot write (a type of a multipart form has no JSON example), and a method named `Example` that returns another type and is not promoted from an embedded struct. The zero value is no method: the document states no example. Allocations: none per request. Decision: [ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md).

### DeprecatedFields method

A struct type may have the optional method `DeprecatedFields() []string`, which returns the Go names of its deprecated fields:

```go
func (SearchQuery) DeprecatedFields() []string
```

A field of a `Path`, `Query`, `Header` or `Cookie` section is a deprecated parameter (`deprecated: true`); a field of a Body, a response and the types nested in them is a deprecated property. It is found where `Descriptions` is, through embedded structs, with the outer struct's own method winning. A name that is not a field of the struct is a panic at Registration listing the fields there are, as for `Descriptions`: `exStaleDeprecation.DeprecatedFields names Nmae, which is not a field of exStaleDeprecation on the wire (its fields: Name)`, and for a section `TypoQueryIn.Query.DeprecatedFields names Nmae, which is not a field of typoQuery (its fields: Name)`. A deprecated field is still accepted and still sent; the document states it. `Deprecated()` on a route or a group deprecates the whole operation. The zero value is no method: nothing is deprecated. Allocations: none per request. Decision: [ADR 0020](../adr/0020-descriptions-come-from-registration-and-later-from-doc-comments.md).

The method is the first of the two places prose comes from; a `go generate` tool that reads doc comments is milestone 3 ([roadmap](../roadmap.md)). The response types, `Returns` and the statuses of the wrappers are in [responses](./responses.md).
