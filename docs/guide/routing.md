# Routing

**A route is a method and a pattern in the syntax of `http.ServeMux`, and the pattern's wildcards must match fields of the handler's Path section.**

**Reference:** [`App`, `Group`](../reference/app.md) · **Design:** [Routing](../design/routing.md)

## Writing a pattern

**Patterns are written as `http.ServeMux` writes them, with the method given by the route method you call.** `app.Get("/users/{id}", h)` registers `GET /users/{id}`. A segment `{name}` matches one non-empty segment, `{name...}` matches the rest of the path (and may match nothing), and `{$}` anchors the end so that `/{$}` is the root alone ([ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)).

<!-- compiles: decls -->
```go
type UserPath struct {
	Path struct{ ID int64 }
}

type FilePath struct {
	Path struct{ Rest string } // {rest...}: everything after /files/
}

type Page struct{}

func user(ctx context.Context, in UserPath) (string, error)   { return "user", nil }
func me(ctx context.Context, in struct{}) (string, error)     { return "me", nil }
func file(ctx context.Context, in FilePath) (string, error)   { return in.Path.Rest, nil }
func root(ctx context.Context, in struct{}) (string, error)   { return "root", nil }
func purge(ctx context.Context, in struct{}) (bites.NoContent, error) { return bites.NoContent{}, nil }
```

<!-- compiles: body -->
```go
app.Get("/{$}", root)            // the root only
app.Get("/users/{id}", user)     // one segment
app.Get("/users/me", me)         // more specific: wins over {id} for /users/me
app.Get("/files/{rest...}", file) // the rest of the path, possibly empty
app.Handle("PURGE", "/cache", purge) // any other method token
```

The methods are [`App.Get`](../reference/app.md#appget), [`App.Post`](../reference/app.md#apppost), [`App.Put`](../reference/app.md#appput), [`App.Patch`](../reference/app.md#apppatch), [`App.Delete`](../reference/app.md#appdelete), [`App.Head`](../reference/app.md#apphead) and [`App.Options`](../reference/app.md#appoptions). [`App.Handle`](../reference/app.md#apphandle) takes any method token as its first argument, for `PURGE`, `PROPFIND` and the like, and it registers a typed handler like the others. A typed route always has a method, so an empty one panics. A plain `http.Handler` is mounted with [`stdhttp.Mount`](./mounting-net-http.md), a [slow path](../adr/0019-plain-http-handlers-are-a-slow-path.md) that registers it once for each method it serves.

## Matching a wildcard to a field

**Each `{name}` in a pattern needs a Path field of the same wire name, and each Path field needs a wildcard.** The field `ID` has the wire name `id` and `ItemID` has `item_id`, so `/items/{item_id}` fits a field named `ItemID` ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md), [Requests](./requests.md#wire-names)). A mismatch in either direction is refused at Registration, naming both:

```text
GET /users/{id}: the pattern has {id}, but UserPath.Path has no field for it
	registered at main.go:21
```

A path value is decoded by its type, so a Path field that is an `int64` answers a 400 naming `path.id` when the segment is not a number. Matching is on raw segments and decoding comes after, so `/files/a%2Fb` matches `/files/{name}` with `name` equal to `a/b`, and an encoded slash never splits a segment. A decoded `..` is only text: code that turns a parameter into a file path should go through an `os.Root` ([Files](./files.md)).

## What wins when two patterns match

**The more specific pattern wins, as in `ServeMux`.** `/users/me` beats `/users/{id}` for the path `/users/me`, and a literal segment beats a wildcard. Two patterns that match exactly the same requests are a conflict, and a conflict panics naming both patterns and where each was registered:

```text
pattern "GET /a/{y}" (registered at main.go:18) conflicts with pattern "GET /a/{x}" (registered at main.go:17): they match the same requests
```

A pattern ending in `/` is an anonymous rest wildcard, as in `ServeMux`, so `GET /` catches every GET path nothing else takes. Write `/{$}` when you mean the root alone.

## Slashes, redirects and normalisation

**There are no automatic redirects.** `/users` and `/users/` are two paths. A request for a path that is not registered is a 404, and a path with `//`, `/./` or `/../` is refused with a 400, because a proxy that normalises differently from the app is how routing gets bypassed ([ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)).

```http
GET //users/7 HTTP/1.1

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"detail":"the path is not normalised"}
```

## 404, 405 and HEAD

**An unknown path is a 404, a known path with another method is a 405 with an `Allow` header, and a GET route also answers HEAD.** Both errors are problem+json ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md)):

```http
POST /users/7 HTTP/1.1

HTTP/1.1 405 Method Not Allowed
Allow: GET, HEAD
Content-Type: application/problem+json

{"title":"Method Not Allowed","status":405}
```

A HEAD request to a GET route gets the same status and headers, including `Content-Length`, and no body. Register [`App.Head`](../reference/app.md#apphead) for the same path to answer HEAD yourself; it wins over the implicit one. `OPTIONS *` lists the methods the app serves.

## Grouping routes

**A group is a path prefix whose routes share options.** [`App.Group`](../reference/app.md#appgroup) takes a prefix that starts with a slash and does not end with one, and returns a [`Group`](../reference/app.md#group) with the same route methods. Groups nest, and their prefixes add up ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). A group also takes a [`Tag`](../reference/openapi.md#tag), so the API document tags every operation of an area once ([the OpenAPI document](./openapi.md#describing-operations-and-fields)).

<!-- compiles: decls -->
```go
func listUsers(ctx context.Context, in struct{}) ([]string, error) { return nil, nil }

func routes(app *bites.App, audit bites.Middleware) {
	api := app.Group("/api", bites.MaxBody(64<<10))
	api.Get("/ping", root) // GET /api/ping

	admin := api.Group("/admin", bites.Use(audit), bites.MaxBody(1<<20))
	admin.Get("/users", listUsers) // GET /api/admin/users, with audit and a 1 MiB limit
}
```

A group takes the same [`RouteOption`](../reference/app.md#routeoption) values as a route: [`MaxBody`](../reference/input.md#maxbody), [`BodyTimeout`](../reference/input.md#bodytimeout), [`AcceptUnknownMembers`](../reference/input.md#acceptunknownmembers), `Use` and `Provide`. A group's options apply to every route in it and in the groups nested in it, from the outermost group inward and then the route's own, so the innermost wins: a route's `MaxBody` beats its group's, and a nested group's beats its parent's. An option that cannot work, such as `MaxBody(0)`, panics when the group is made, even if the group has no route yet. Versioned APIs with different naming are separate apps; a `/v1` and `/v2` with one convention are two groups ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md)).

**Routes that share options but no prefix share an option made with [`Options`](../reference/app.md#options).** A group needs a prefix (`Group("")` and `Group("/")` are refused, since a group without one would be a second spelling of a bundle), so the pages of a site at `/`, `/login` and `/files` take one bundle instead, passed to each. The same holds for some routes of a group: the sign-in routes of `/api/v1` that alone need CSRF and a limiter are registered on the `/api/v1` group with one bundle passed to each:

<!-- compiles: decls -->
```go
func pages(app *bites.App, csrf, headers bites.Middleware) {
	page := bites.Options(bites.Use(headers, csrf), bites.Tag("pages"))
	app.Get("/", root, page)
	app.Post("/login", root, page)
}
```

A bundle is an option like any other: each member is applied in order, and a member's refusal names the route it was given to. `bites.Options` is this function, not [`App.Options`](../reference/app.md#appoptions), which registers a route for the `OPTIONS` method.

Middleware and Providers can also be added with [`Group.Use`](../reference/app.md#groupuse), which must come before the group's first route or child group; a late call panics, naming the line that sealed the group. Using the option spelling (`bites.Use(audit)` above) cannot be late ([Middleware](./middleware.md), [Providers](./providers.md)).

## Listing the routes

**`app.Routes()` returns every route registered so far, in the order it was registered**, as a `[]RouteInfo`: the same value [`UseEach`](./middleware.md#default-deny-a-middleware-built-for-each-route) is given for each route, so a startup check, a route table for the logs or a test that every route has a permission reads what the guard read. It includes routes mounted with `stdhttp.Mount`, WebSocket and Static routes, and the API document and the metrics page (marked `Builtin`); the paths of `WithHealth` are answered before routing and are not listed. The slice and each `Tags` are copies, so changing them changes nothing in the app. Call it after the last route is registered: called earlier, or from a `UseEach` function, it lists only the routes so far.

<!-- compiles: decls -->
```go
func printRoutes(app *bites.App) {
	for _, r := range app.Routes() {
		fmt.Println(r.Method, r.Pattern, r.OperationID)
	}
}
```

## The order a request goes through

**The order is fixed: app middleware, routing, group and route middleware, Providers, decoding with `Validate`, then your handler.** ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md))

1. App middleware (`app.Use`) runs before the route is matched, so it sees every request, including those that will be a 404 or 405 and a CORS preflight.
2. The route is matched; 404 and 405 are produced here.
3. Group and route middleware run, outermost group first.
4. Providers the Input needs run ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)).
5. The sections are decoded, then `Validate` runs ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)).
6. The handler runs.

So a request that fails decoding never reaches `Validate` or the handler, and a middleware that refuses a request never costs a decode. Routes, groups, middleware and Providers can be added only before the app starts serving; adding one afterwards panics.

## Where to go next

- [Requests](./requests.md): what a Path field can be.
- [Middleware](./middleware.md): what runs at steps 1 and 3.
- [Limits and timeouts](./limits.md): the options a group can set.
