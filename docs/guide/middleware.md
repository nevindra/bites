# Middleware

**Middleware wraps a route: it sees the request head, calls the rest of the chain, and returns the answer it wants sent.**

**Reference:** [Middleware](../reference/middleware.md) · **Design:** [Middleware and Providers](../design/middleware-and-providers.md)

This page writes two middleware, an authentication check that answers 401 and a header added on the way out, then says where each kind is registered, in which order a request meets them, and what a middleware cannot see. The four middleware the framework ships (CORS, request IDs, access logs, timeouts) are on [the next page](./first-party-middleware.md).

The examples share one handler and its types.

<!-- compiles: decls -->
```go
type Ping struct{}

type Pong struct {
	OK bool
}

func ping(ctx context.Context, in Ping) (Pong, error) {
	return Pong{OK: true}, nil
}
```

## Answering 401 before the handler runs

**A [`Middleware`](../reference/middleware.md#middleware) is a function that takes the context, a read-only [`Request`](../reference/middleware.md#request) and a [`Next`](../reference/middleware.md#next), and returns a [`Response`](../reference/middleware.md#response) and an error.** It short-circuits by returning without calling `next`, and passes the request on by calling [`next.Call`](../reference/middleware.md#nextcall) ([ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)).

<!-- compiles: decls -->
```go
func requireToken(token string) bites.Middleware {
	return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		if req.Header("Authorization") != "Bearer "+token {
			return bites.Problem(401, "missing or wrong token"), nil
		}
		return next.Call(ctx), nil
	}
}
```

[`Problem`](../reference/middleware.md#problem) builds a fresh problem+json answer with a status and a detail, and it carries the request ID when a middleware set one. A middleware can also return an error: `return bites.Response{}, bites.Unauthorized("missing token")` is answered exactly as a handler's error is ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)), and the middleware outside this one then sees that answer as a `Response`. Use `Problem` when you have a status and a sentence, and an error when the code that produced it already returns [`StatusError`](../reference/errors.md#statuserror) values.

**[`Next.Call`](../reference/middleware.md#nextcall) never returns an error.** An error from the handler, a Provider or an inner middleware is already a problem+json `Response` by the time `Call` returns, so the middleware outside always sees the final status.

The client sees:

```http
GET /ping HTTP/1.1
Host: example.com

HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json

{"title":"Unauthorized","status":401,"detail":"missing or wrong token"}
```

**A middleware reads one cookie with [`req.Cookie(name)`](../reference/middleware.md#requestcookie)**, which returns the value of the first cookie of that name and whether there was one, with no allocation: `if v, ok := req.Cookie("session"); !ok { return bites.Problem(401, "no session"), nil }`. It reads the `Cookie` header the way the `Cookie` section does (quotes removed, malformed pairs skipped). A handler does not use it: it names the cookie in the `Cookie` section of its Input and receives it decoded and checked ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

## Adding a header on the way out

**A middleware that calls `next.Call` gets the answer back and may add headers and change the status before returning it.** The methods that change a `Response` take a pointer, so the value is a variable.

<!-- compiles: decls -->
```go
func servedBy(name string) bites.Middleware {
	return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		res := next.Call(ctx)
		res.AddHeader("X-Served-By", name)
		return res, nil
	}
}
```

[`Response.AddHeader`](../reference/middleware.md#responseaddheader) adds a line after any with the same name, [`Response.SetHeader`](../reference/middleware.md#responsesetheader) replaces a value a middleware already set, [`Response.SetStatus`](../reference/middleware.md#responsesetstatus) changes the status and keeps the body, and [`Response.Status`](../reference/middleware.md#responsestatus), [`Response.ContentType`](../reference/middleware.md#responsecontenttype), [`Response.Size`](../reference/middleware.md#responsesize) and [`Response.Streaming`](../reference/middleware.md#responsestreaming) say what the answer is. To replace the answer, return a fresh one: [`NewResponse`](../reference/middleware.md#newresponse) makes one with a status, and [`Response.SetBody`](../reference/middleware.md#responsesetbody) gives it a short body.

<!-- compiles: decls -->
```go
func maintenance(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	res := bites.NewResponse(503)
	res.SetBody("text/plain; charset=utf-8", "back soon\n")
	res.AddHeader("Retry-After", "120")
	return res, nil
}
```

## Registering middleware on the app, a group or a route

**Where a middleware is registered decides which requests it sees.** There are three places, and a request meets them in a fixed order ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)).

[`app.Use`](../reference/app.md#appuse) adds app middleware, which runs before the route is matched. It therefore sees every request, including those that will be a 404 or a 405 and a CORS preflight, which no route has. The first one given is outermost, and `app.Use` may be called after routes are registered, because it is read when a request arrives.

[`Group.Use`](../reference/app.md#groupuse) adds middleware to every route of a group and of the groups made in it afterwards. The option [`bites.Use`](../reference/middleware.md#use) does the same on a route, and on a group when it is given to `Group`.

<!-- compiles: body -->
```go
app.Use(servedBy("a"))

admin := app.Group("/admin")
admin.Use(requireToken("s3cret"))
admin.Get("/ping", ping)

app.Get("/ping", ping, bites.Use(servedBy("route")))
app.Group("/orders", bites.Use(requireToken("orders"))).Get("/ping", ping)
```

Group and route middleware run after the match, so a request for `/admin/nothing` never reaches `requireToken`: the 404 is answered inside `servedBy("a")` and nowhere else. Use the app level for what every answer needs (an ID, a log line, CORS), and a group or a route for what only some routes need (authentication, a timeout).

## The order a request goes through

**A request meets app middleware, then the route match, then group and route middleware outermost first, then the Providers, the Input and the handler.** Within one list the first middleware given is outermost, and the answer comes back through them in the reverse order.

1. App middleware, in the order of the `app.Use` calls.
2. The route is matched; a 404 or a 405 is produced here.
3. Group middleware from the outermost group inward, then the route's own.
4. The [Providers](./providers.md) the Input needs.
5. The Input is decoded and its `Validate` method runs.
6. The handler.

Because Providers and decoding come after middleware, a middleware that refuses a request spares the cost of both, and a request with a malformed body and no token is told about the token, not the body.

## Default-deny: a middleware built for each route

**A middleware registered with `app.Use` runs before the route is matched, so it cannot know which route it is serving; [`UseEach`](../reference/middleware.md#useeach) builds a middleware for each route at Registration, from the route's identity, and can refuse the route.** That is the shape of a permission table keyed on the operation ID: every route needs an entry, a route without one must not be served, and a short exempt list skips the check ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). A `Use(perm("deals.list"))` on each route cannot do it, because a route registered without it is open and nothing says so.

The function given to `UseEach` is called once for each route, with a [`RouteInfo`](../reference/middleware.md#routeinfo): the method, the pattern with its groups' prefixes, the operation ID the API document gives the route, the tags, whether it is a WebSocket or a Static route, and `Builtin` (`"openapi"` or `"metrics"` for the two routes the app registers for itself, empty for every route you wrote). It returns a middleware for that route, `nil` for none (the exempt list), or an error, which refuses the route.

<!-- compiles: decls -->
```go
var permissions = map[string]string{
	"deals.list":   "deals:read",
	"deals.create": "deals:write",
}

var exempt = map[string]bool{"health.check": true}

// permissionFor is the function given to UseEach.
func permissionFor(audit func(op string, status int)) func(bites.RouteInfo) (bites.Middleware, error) {
	return func(r bites.RouteInfo) (bites.Middleware, error) {
		if exempt[r.OperationID] {
			return nil, nil // no check for this route
		}
		perm, ok := permissions[r.OperationID]
		if !ok {
			return nil, errors.New("operation " + strconv.Quote(r.OperationID) + " has no permission in the table")
		}
		op := r.OperationID // the middleware closes over it: no lookup at request time
		return func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			if !strings.Contains(req.Header("X-Permissions"), perm) {
				audit(op, 403)
				return bites.Response{}, bites.Forbidden("missing permission " + perm)
			}
			res := next.Call(ctx)
			audit(op, res.Status()) // the final status, as the client sees it
			return res, nil
		}, nil
	}
}
```

<!-- compiles: body -->
```go
app.UseEach(permissionFor(func(op string, status int) {
	slog.Info("audit", "operation", op, "status", status)
}))

app.Get("/deals", ping, bites.OperationID("deals.list"))
app.Post("/deals", ping, bites.OperationID("deals.create"))
app.Get("/healthz", ping, bites.OperationID("health.check"))
```

A route registered with an operation ID that is not in the table, or with none (the generated `getDealsById` is looked up like any other), stops the program at start:

```text
GET /deals/{id}: the function given to UseEach (at main.go:21) refused the route: operation "getDealsById" has no permission in the table
	registered at main.go:27
```

**The built middleware runs inside every `Use` middleware of the route, next to the handler.** A sign-in middleware on the app, a group or the route has run first, so the permission check can read the user it established; it does not see what that sign-in refused, so an audit of every 401 belongs to an [Observer](./observability.md). Several `UseEach` calls apply outermost scope first, the app's, a group's, then the route's own.

**`UseEach` must come before the first route of its scope**, like a group's `Use`: after one (or after `OpenAPI` or `ServeMetrics`), the call panics naming the line that registered the route, because it would silently skip it, which is what default-deny must not do. `g.UseEach(...)` and the option `bites.UseEach(...)` exist for a group and a route. Every route is passed to the function: a typed route (a [mounted `net/http` handler](./mounting-net-http.md) is one, with an empty operation ID because it is not documented, so key those on `Method` and `Pattern`), a WebSocket route and a Static route, and the two the app registers for itself, the API document from `OpenAPI` and the page from `ServeMetrics`, with `Builtin` set to `"openapi"` or `"metrics"`. **A built-in route is guarded like any other**, so a guard that checks a session answers 401 for `/openapi.json` unless it returns nil for `r.Builtin == "openapi"`, and a function that refuses what it does not know stops the program at start with `OpenAPI /openapi.json: the function given to UseEach ... refused the route`, which is the loud answer you want. `UseEach` must therefore come before `OpenAPI` and `ServeMetrics` too. The docs UI of the `docsui` module is an ordinary typed route. Not passed: the paths of `WithHealth`, which are answered before routing so that a load balancer's probe needs no credential, and a 404 or a preflight, which have no route; protect those with app middleware. The function runs at Registration and must not register routes. For a guard that authenticates every route and hands the user to the handlers that ask, see [Providers](./providers.md#a-session-on-every-route-and-the-user-in-the-handlers-that-ask).

## What a middleware cannot see

**A middleware sees the request head and the status, content type and size of the answer, and nothing of either body.** A [`Request`](../reference/middleware.md#request) has [`Method`](../reference/middleware.md#requestmethod), [`Path`](../reference/middleware.md#requestpath) (still percent-encoded), [`RawQuery`](../reference/middleware.md#requestrawquery), [`Host`](../reference/middleware.md#requesthost), [`Header`](../reference/middleware.md#requestheader), [`RemoteAddr`](../reference/middleware.md#requestremoteaddr) and [`RequestID`](../reference/middleware.md#requestrequestid). The body is not part of the view: the Input decodes it once, and no pooled buffer reaches a middleware ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)).

On the way out, a `Response` holds the headers middleware added. A header the handler itself set (the `Header` of a wrapper, a `Location`, a `Set-Cookie`) is not visible and cannot be replaced; a middleware can add headers and change the status. To replace more, return a fresh `Response`, and the inner answer is dropped. A stream is seen as `Streaming()` true with `Size()` 0, and a middleware that replaces it means its function never runs ([ADR 0030](../adr/0030-streaming-is-a-return-type.md)). Compression and ETags are engine features for the same reason ([ADR 0031](../adr/0031-gzip-only-and-off-by-default.md)).

**A `Request`, a `Next` or a `Response` kept past the call is harmless.** The strings in a `Request` are copies, and the state behind them is checked on every use: a kept `Next.Call` answers a 500 `Response` and logs, `SetRequestID` does nothing, and a kept `Response` has no headers and ignores `AddHeader`.

## Giving a request an ID and adding log attributes

**[`Request.SetRequestID`](../reference/middleware.md#requestsetrequestid) gives the request an ID that every problem+json answer then carries as `request_id`.** The [`RequestID`](./first-party-middleware.md#giving-every-request-an-id) middleware calls it for you; write your own only to take the ID from somewhere else.

**[`Request.AddLogAttrs`](../reference/middleware.md#requestaddlogattrs) adds attributes to the request's log record.** The access log is an [Observer](./observability.md) that writes when the request has ended, so what a Provider or any middleware adds shows up on its line ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)). [`Request.AppendLogAttrs`](../reference/middleware.md#requestappendlogattrs) reads them back, as `append` does.

<!-- compiles: decls -->
```go
func tenantAttr(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	if t := req.Header("X-Tenant"); t != "" {
		req.AddLogAttrs(slog.String("tenant", t))
	}
	return next.Call(ctx), nil
}
```

## What is refused at Registration

**A middleware that could not work is refused before the first request, naming where it was registered** ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)). These mistakes are caught:

- A `nil` middleware, in `app.Use`, `Group.Use` or the `Use` option, panics naming the position of the nil in the call.
- A `UseEach` function that is `nil`, or an error returned by one, panics naming the route (above); `app.UseEach` after the app's first route or group, and `Group.UseEach` after the group's, panic like a late `Use`.
- A `Group.Use` after the group's first route or first child group panics, because the middleware would silently skip what is already registered.

```text
group "/g": Use is called after GET /g/x (at main.go:12) was registered in this group, so it would not reach it: move Use above it
	registered at main.go:15
```

The option spelling cannot be late, which is why a module that receives a group already holding routes can still write `g.Group("/orders", bites.Use(audit))`. `app.Use` after the app has started serving also panics, as adding a route does. What cannot be caught is what a middleware does at run time, such as a [`Timeout`](./first-party-middleware.md#cutting-off-a-slow-handler) on a route that returns a stream, because a middleware is an opaque function.

## Cost

**A typed request behind middleware costs 2 allocations, the same as without, at any number of layers, and a middleware built by `UseEach` costs what the same `Use` costs.** `Next` is a small struct and the chain's state is pooled, so no layer allocates ([ADR 0003](../adr/0003-the-trade-budget.md)). A fresh `Response` built by a middleware (`Problem`, `NewResponse`) is the short-circuit path and is not budgeted.
