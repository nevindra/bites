# Middleware

**The Middleware type, the read-only Request view and the Response value it works with, the Use option, and the first-party middleware CORS, RequestID and Timeout.**

**Guide:** [middleware](../guide/middleware.md) · **Design:** [middleware and Providers](../design/middleware-and-providers.md)

A Middleware wraps the rest of the chain. It sees a read-only view of the request head, calls `next` to run what is inside it, and returns the Response it wants sent. It short-circuits by returning without calling `next`, and passes a value inward by calling `next` with a wrapped context ([ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)). It holds no writer, so it cannot write twice ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)).

The order a request goes through is fixed ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)): app middleware ([`App.Use`](./app.md#appuse)) runs before the route is matched, so it sees every request, a 404, a 405 and a CORS preflight included; the route is matched; group and route middleware run, outermost group first; then the Providers the Input needs ([providers](./providers.md)); then the Input is decoded and `Validate` runs; then the handler.

An error from a handler, a Provider or an inner middleware is already a problem+json Response at the `Next` boundary, so the middleware outside always sees the final status and can decorate it ([errors](./errors.md)). An error a middleware itself returns is answered the same way and the middleware outside it then sees that answer as a Response. A Stream or SSE is seen as a Response with `Streaming()` true and `Size()` 0.

Allocations: a typed POST behind any number of layers of middleware, at app, group or route level, is 2, the same as without. Each of the first-party middleware adds none on its own ([ADR 0003](../adr/0003-the-trade-budget.md), [ADR 0032](../adr/0032-four-first-party-middleware.md)). The fourth, `AccessLog`, is an Observer given with `WithObserver`, not a middleware; it is on the [observability](./observability.md#accesslog) page.

What Registration refuses: a nil middleware, wherever it is given (`app: Use: middleware 2 is nil`, `group "/g": Use: middleware 1 is nil`, `GET /x: Use: middleware 1 is nil`), a group `Use` after the group has a route or a child group, and `App.Use` after the app started serving. The constructors of the first-party middleware refuse a configuration that cannot work with a `RegistrationError` naming the constructor, the field and the line it was called from (below).

<!-- compiles: decls -->

```go
func serverHeader(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	if req.Path() == "/teapot" {
		return bites.Problem(418, "short and stout"), nil
	}
	res := next.Call(ctx)
	res.AddHeader("X-Served-By", "a")
	return res, nil
}

func wire(app *bites.App) {
	app.Use(bites.RequestID(), serverHeader)
	app.Get("/slow", func(ctx context.Context, in struct{}) (bites.Text, error) {
		return bites.Text{Body: "ok"}, nil
	}, bites.Use(bites.Timeout(2*time.Second)))
}
```

Guide: [middleware](../guide/middleware.md) and [CORS, request IDs, access logs and timeouts](../guide/first-party-middleware.md).

## The Middleware type

### `Middleware`

```go
type Middleware func(ctx context.Context, req Request, next Next) (Response, error)
```

Middleware wraps the rest of the chain. `ctx` is the request's context, cancelled when the client leaves. `req` is a view of the head; the body is not part of it. `next` runs what is inside. The Response it returns is what is sent. A middleware that returns an error makes that error the answer, as a handler's would.

Order inside a scope: the first middleware given to `Use` is outermost. Options apply from the outermost group inward, so a group's middleware wraps the route's. `RequestID` goes first so every answer carries the id, and `CORS` after it so a preflight's answer carries it too; `Timeout` goes on a route or a group. `AccessLog`, an Observer, sees every answer whatever the order.

## The request view

### `Request`

```go
type Request struct {
	// Has unexported fields.
}
```

Request is a read-only view of the request head. Every string in it is a copy made for this request, never the engine's read buffer, so a middleware that keeps one keeps that request's own bytes and no other request's ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). The methods that reach the request itself (`RemoteAddr`, `RequestID`, `SetRequestID`, `AddLogAttrs`, `AppendLogAttrs`) check that the request is still the one running and do nothing, or answer with a zero value, after it has been answered, so a Request kept past the call holds no pooled object it can read. The time on the app's clock is read from the context beside it, with [`Now(ctx)`](./app.md#now).

### `Request.Method`

```go
func (r Request) Method() string
```

Method is the request method, an interned constant for the nine standard ones.

### `Request.Path`

```go
func (r Request) Path() string
```

Path is the raw request path, still percent-encoded.

### `Request.RawQuery`

```go
func (r Request) RawQuery() string
```

RawQuery is the query after the `?`, still percent-encoded; empty when there is none.

### `Request.Host`

```go
func (r Request) Host() string
```

Host is the `Host` header, what this hop was sent. Behind a proxy that rewrites it, the host the client asked for is [`Request.PublicHost`](#requestpublichost).

### `Request.Header`

```go
func (r Request) Header(name string) string
```

Header returns the first value of the header called `name` (matched without regard to case), or the empty string when the request has none. It scans the copy of the head line by line without allocating: quick for one or two lookups.

### `Request.Cookie`

```go
func (r Request) Cookie(name string) (value string, ok bool)
```

Cookie returns the value of the first cookie called `name` (matched exactly: cookie names are case-sensitive) across the request's `Cookie` header fields, in the order the client sent them, and whether there was one. One pair of double quotes around the value is removed and the value is not percent-decoded. A pair with no `=` or an empty name is skipped, and an empty `name` returns the empty string and false. The parsing is the `Cookie` section's own, so the two read the same header the same way ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)). The value is a substring of the request's copy of the head and nothing is allocated. Like [`Request.Header`](#requestheader) it keeps working on a `Request` kept past the call, and returns what that request sent. A handler reads its cookies through the `Cookie` section of its Input instead.

### `Request.Headers`

```go
func (r Request) Headers() iter.Seq2[string, string]
```

Headers yields every header field of the request as received, in order: the name with the capitalisation the client used, and the value with the whitespace around it trimmed. A field sent twice is yielded twice. The strings are the copies the view holds, so keeping one is safe, and the iteration allocates nothing. It exists for code that must hand the whole head to something else, such as [`stdhttp.Mount`](./stdhttp.md#stdhttpmount) building an `http.Request`; a middleware that wants one field uses [`Request.Header`](#requestheader).

### `Request.RemoteAddr`

```go
func (r Request) RemoteAddr() netip.AddrPort
```

RemoteAddr is the client's address as the connection reports it (the address of a proxy, when there is one in front; no proxy is trusted by default, [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md)). The zero `AddrPort` after the request was answered, and on a Unix socket, whose peer has no address ([ADR 0068](../adr/0068-one-app-serves-several-listeners-and-a-unix-socket-is-a-socket-like-tcp.md)).

### `Request.VerifiedChains`

```go
func (r Request) VerifiedChains() [][]*x509.Certificate
```

VerifiedChains is the client certificate of an mTLS connection as `crypto/tls` verified it against the `ClientCAs` of the app's `*tls.Config`: one chain or more, each starting with the client's own certificate. It is nil on a plain connection, when the client sent no certificate, when the config asks for one without verifying it (`RequestClientCert`, `RequireAnyClientCert`), and after the request was answered, so only a certificate a CA vouched for is ever returned. Every request on one connection sees the same chains, which belong to the connection and are not pooled: read them, do not change them. A Provider is the usual reader ([Providers](./providers.md)). Decision: [ADR 0066](../adr/0066-client-certificates-are-verified-by-the-tls-config-and-read-as-verified-chains.md).

### `Request.ClientAddr`

```go
func (r Request) ClientAddr() netip.Addr
```

ClientAddr is the client's address as far as the app can trust it: the peer of the connection, unless the peer is one of the trusted proxies of [`WithTrustedProxies`](./app.md#withtrustedproxies), in which case it is read from their header. From `X-Forwarded-For` it is the rightmost address that is not a trusted proxy: each line of a repeated header is read in turn, the last line rightmost; a port written after an address is dropped; an element that does not parse, or is `unknown`, stops the walk and answers the nearest trusted address to its right; when every element is trusted it is the leftmost one. From another header it is the one address of its last line, or the peer when that does not parse. An IPv4 address mapped into IPv6 is answered as IPv4. It allocates nothing, and answers the zero `Addr` after the request was answered. It is the default key of a [`RateLimiter`](./security.md#ratelimiter).

Guide: [deploying](../guide/deploying.md#behind-a-load-balancer). Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

### `Request.Scheme`

```go
func (r Request) Scheme() string
```

Scheme is `https` when the request came over TLS the app terminated, the value of `X-Forwarded-Proto` when the peer is a trusted proxy and the header's rightmost element says `http` or `https` (in any case), and `http` otherwise. It is what a redirect, a `Secure` cookie and HSTS need ([`SecurityHeaders`](./security.md#securityheaders) sends HSTS only when it is `https`). It allocates nothing, and answers the empty string after the request was answered.

Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

### `Request.PublicHost`

```go
func (r Request) PublicHost() string
```

PublicHost is the host, with its port when one was given, that the client asked for: the `X-Forwarded-Host` a trusted proxy wrote, when [`TrustedProxies.ForwardedHost`](./app.md#trustedproxies) is set, and the `Host` header otherwise. Of `X-Forwarded-Host` the rightmost element of the last line is read, the one the nearest proxy wrote. It must be a host name or IPv4 address of letters, digits, `-`, `.` and `_`, or an IPv6 address in brackets without a zone, with an optional port of at most 65535; anything else (a path, a userinfo, a space, an IPv6 address without brackets) answers the `Host` header. It allocates nothing, and answers the empty string after the request was answered and for an HTTP/1.0 request with no `Host`.

Guide: [deploying](../guide/deploying.md#behind-a-load-balancer). Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

### `Request.PublicPrefix`

```go
func (r Request) PublicPrefix() string
```

PublicPrefix is the path prefix a trusted proxy strips before forwarding: the `X-Forwarded-Prefix` it wrote, when [`TrustedProxies.ForwardedPrefix`](./app.md#trustedproxies) is set, and the empty string otherwise. The rightmost element of the last line is read, and its trailing slash dropped (`/api/` is `/api`, `/` is empty). It must be an absolute path of RFC 3986 path characters with no empty, `.` or `..` segment (a dot written as `%2e` counts), so `//evil.example`, `/\evil.example` and `/a/../b` are refused; a refused value answers the empty string. A link is the prefix followed by the route's path: `req.PublicPrefix() + "/rooms/7"`. It allocates nothing, and answers the empty string after the request was answered.

Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

### `Request.PublicURL`

```go
func (r Request) PublicURL(path string) string
```

PublicURL is the absolute URL the client would use for `path`, a path of the app's own routes that starts with a slash and is already percent-encoded: [`Scheme`](#requestscheme), `://`, [`PublicHost`](#requestpublichost), [`PublicPrefix`](#requestpublicprefix) and `path`. Behind a proxy that strips `/api` for `https://api.example.com`, `PublicURL("/rooms/7")` is `https://api.example.com/api/rooms/7`. With no host it is the prefix and the path alone. It allocates the string it returns, one allocation, and answers the empty string after the request was answered.

Guide: [deploying](../guide/deploying.md#behind-a-load-balancer). Decision: [ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md).

### `Request.RequestID`

```go
func (r Request) RequestID() string
```

RequestID is the id a middleware gave this request with `SetRequestID`, or the empty string.

### `Request.SetRequestID`

```go
func (r Request) SetRequestID(id string)
```

SetRequestID gives the request an id. Every problem+json answer written afterwards, by the framework or the engine, carries it as `request_id`. The `RequestID` middleware is its caller; it does not touch the response headers, which are the middleware's.

### `RequestIDFrom`

```go
func RequestIDFrom(ctx context.Context) string
```

RequestIDFrom returns the id a middleware gave the request `ctx` belongs to with `Request.SetRequestID` (the `RequestID` middleware does), or the empty string when there is none, when no middleware ran, and when `ctx` is not a request's context or derived from one. It is how a handler, which holds only its context, reads the id that the access log and every problem+json answer carry; `Request.RequestID` is for a middleware or an Observer, which hold a Request. It works on a context the handler derived (`WithTimeout`, `WithValue`), allocates nothing, and answers with its own request's id on a context kept after the request ended, because the id is text in the request's own memory ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)).

### `Request.AddLogAttrs`

```go
func (r Request) AddLogAttrs(attrs ...slog.Attr)
```

AddLogAttrs adds attributes to this request's log record. A Provider calls it with what it found, the user's ID for one, and the access log, an Observer, writes them when the request has ended. The attributes are copied; nothing happens after the request was answered.

### `Request.AppendLogAttrs`

```go
func (r Request) AppendLogAttrs(dst []slog.Attr) []slog.Attr
```

AppendLogAttrs appends the attributes added to this request so far to `dst` and returns the result, as `append` does. It copies them, so the slice it returns is the caller's own.

## The response value

### `Response`

```go
type Response struct {
	// Has unexported fields.
}
```

Response is the answer as middleware sees it: a status, a content type, the headers middleware added, and the size of the body. The body bytes are not part of it: they are written once into the engine's own buffer by the innermost step, so no pooled buffer reaches a middleware and nothing here can be used to read or rewrite them. The value is 80 bytes and copied at each layer; the headers middleware added to an answer from `next` are kept in the chain's own list.

Methods that change it take a pointer, so a middleware writes `res := next.Call(ctx)`, `res.AddHeader("X-Served-By", "a")`, `return res, nil`. A header the handler itself set (a wrapper's `Header`, `Location`, `Set-Cookie`) is not visible here and cannot be replaced; a middleware can add headers and change the status. To replace more, return a fresh Response (`NewResponse`, `Problem`) and the inner answer is dropped. A Response used after its request has been answered has no headers and ignores `AddHeader`. A Response returned from a different request than the one finishing is a 500.

### `Response.Status`

```go
func (r *Response) Status() int
```

Status is the status code. It is 0 for the zero Response and when nothing was answered.

### `Response.SetStatus`

```go
func (r *Response) SetStatus(status int)
```

SetStatus changes the status; the body and headers stay.

### `Response.ContentType`

```go
func (r *Response) ContentType() string
```

ContentType is the `Content-Type` the answer was given, or the empty string when it has none.

### `Response.Header`

```go
func (r *Response) Header(name string) (string, bool)
```

Header returns the first value of a header a middleware set on this Response, matched without regard to case, and whether there is one. It does not see headers the handler set.

### `Response.SetHeader`

```go
func (r *Response) SetHeader(name, value string)
```

SetHeader sets a header: it replaces the value of one this Response already carries from a middleware, and adds it otherwise.

### `Response.AddHeader`

```go
func (r *Response) AddHeader(name, value string)
```

AddHeader adds a header line, after any with the same name.

### `Response.SetBody`

```go
func (r *Response) SetBody(contentType, body string)
```

SetBody gives a fresh Response a body and its content type. It is for a short answer a middleware writes itself; the text is copied when the chain returns. On an answer from `next` it replaces the answer: the inner status, headers and body are dropped.

### `Response.Size`

```go
func (r *Response) Size() int
```

Size is the number of body bytes the answer has. It is 0 for a stream or a `File`, whose body is written after every middleware has returned and whose size nobody knows yet: ask `Streaming`.

### `Response.Streaming`

```go
func (r *Response) Streaming() bool
```

Streaming reports whether the answer is a `Stream`, an `SSE` or a `File`. A middleware sees its status, its `Content-Type` and the headers it adds, as for any answer; it can change the status and add headers, which go out with the head, before the first byte. A stream answered by a fresh Response is dropped without its function running.

### `NewResponse`

```go
func NewResponse(status int) Response
```

NewResponse returns a fresh Response with `status` and no body, to return instead of calling `next`. The status must be 100 to 599; any other is answered as a 500 and logged. A fresh Response holds its headers on the heap, which is the short-circuit path and not budgeted.

### `Problem`

```go
func Problem(status int, detail string) Response
```

Problem returns a fresh problem+json Response with `detail`, carrying the request id like every other error answer. It is how a middleware refuses a request without an error value.

### `Next`

```go
type Next struct {
	// Has unexported fields.
}
```

Next runs the rest of the chain. It is a small value, not a function, because a function value made for each request and layer is an allocation the budget does not have ([ADR 0003](../adr/0003-the-trade-budget.md)). It is valid for the length of the call that received it: `Call` after the request has been answered returns a 500 Response and logs, and never runs another request's handler.

### `Next.Call`

```go
func (n Next) Call(ctx context.Context) Response
```

Call runs the rest of the chain with `ctx` and returns what it produced. It never returns an error: an error from the handler, a Provider or an inner middleware is already a problem+json Response here. Pass a wrapped context to hand a value inward.

## Adding middleware

### `Use`

```go
func Use(mw ...Middleware) RouteOption
```

Use adds middleware to a route, or to every route of a group when it is given to `Group`. It is the second spelling of [`Group.Use`](./app.md#groupuse), for a module that receives a group already holding routes: `g.Group("/orders", bites.Use(audit))`. Options apply from the outermost group inward, so the group's middleware wraps the route's. A nil middleware is refused, naming the route or group and its position in the call. The option cannot be late, unlike the method.

### `RouteInfo`

```go
type RouteInfo struct {
	Method      string
	Pattern     string
	OperationID string
	Tags        []string
	Deprecated  bool
	WebSocket   bool
	Static      bool
	Builtin     string
}
```

RouteInfo is what the function given to [`UseEach`](#useeach) is told about one route at Registration: a value of copies, with nothing of the framework's inside it. `Method` is the route's method (`GET` for a WebSocket and a Static route). `Pattern` is the pattern with its groups' prefixes as it was registered (`/orders/{id}`); a Static route's ends in `/{path...}`. `OperationID` is the operationId the API document gives the route, given with `OperationID` or made from the method and path; it is empty for a route the document leaves out (Static, `Undocumented`, so every `stdhttp.Mount` route) and for a method the document has no name for; a WebSocket route has the id of its handshake's `get`. `Tags` are the route's tags, the outermost group's first. `Deprecated` is true for a route given [`Deprecated`](./openapi.md#deprecated), itself or through a group, including a route the document leaves out; it is the zero value, false, otherwise. `WebSocket` and `Static` say which kind of route it is. `Builtin` is [`BuiltinOpenAPI`](#builtinopenapi) for the document [`OpenAPI`](./openapi.md) serves and [`BuiltinMetrics`](#builtinmetrics) for the page [`ServeMetrics`](./observability.md) serves, and empty for every route the application wrote; for these two `Method` is `GET`, `Pattern` is the path given and `OperationID` is empty. A function that refuses what it does not know refuses them too, so a guard that serves them openly returns nil for their `Builtin`. Only the app-level `UseEach` reaches them. The paths of `WithHealth` are not passed.

Guide: [middleware](../guide/middleware.md#default-deny-a-middleware-built-for-each-route). Decision: [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md).

### `BuiltinOpenAPI`

```go
const BuiltinOpenAPI = "openapi"
```

BuiltinOpenAPI is the [`RouteInfo`](#routeinfo) `Builtin` of the route [`OpenAPI`](./openapi.md) registers for the API document. A guard compares with it rather than with the literal, so a misspelling is a compile error instead of a refusal at start.

### `BuiltinMetrics`

```go
const BuiltinMetrics = "metrics"
```

BuiltinMetrics is the [`RouteInfo`](#routeinfo) `Builtin` of the route [`ServeMetrics`](./observability.md) registers for the metrics page.

### `UseEach`

```go
func UseEach(build func(r RouteInfo) (Middleware, error)) RouteOption
```

UseEach is the option spelling of [`App.UseEach`](./app.md#appuseeach) and [`Group.UseEach`](./app.md#groupuseeach): given to a route it builds that route's middleware, given to `Group` it builds one for every route of the group. `build` is called once for each route it reaches, at Registration, with the route's [`RouteInfo`](#routeinfo), with the Registration lock held (it must not register anything). A `Middleware` it returns runs inside every `Use` middleware of the route, next to the handler; several apply outermost scope first. A nil `Middleware` means none for this route. An error refuses the route: the program panics naming the route, the line of the `UseEach` and the line of the route, with the error's text (``GET /x: the function given to UseEach (at main.go:9) refused the route: no permission for getX``, then `registered at main.go:12`). A nil function is refused (`GET /x: UseEach: the function is nil`). The option cannot be late, unlike the methods.

Allocations: a route with a built middleware costs what the same route with `Use` costs, 2 for a typed JSON request; a route whose `build` returned nil costs what a route with no middleware costs ([ADR 0003](../adr/0003-the-trade-budget.md)); `build` runs at Registration only.

## First-party middleware

### `CORS`

```go
func CORS(o CORSOptions) Middleware
```

CORS is the cross-origin middleware. It is app middleware (`app.Use`), because a preflight is an OPTIONS request that no route has and app middleware runs before routing. Given to a group or a route (`bites.Use`, `Group.Use`, or returned by a `UseEach` function) it would never see a preflight, which would be a 405, so that is refused at Registration; `CORSOptions.Paths` limits it to part of the app.

- A preflight (OPTIONS with `Origin` and `Access-Control-Request-Method`) is answered here with a 204 and never reaches the router. It is allowed when the origin is listed, the requested method is in `Methods` and every requested header is in `Headers`; then the answer carries the `Access-Control-Allow-*` headers. A preflight that is not allowed gets the same 204 with none of them, which is what makes the browser refuse the real request.
- Every other request is passed on, and the headers of an allowed origin are added to the answer whatever it is: a 200, a 404, a 405, a 400 or any other problem+json, so the browser lets the page read the error. A request from an origin that is not listed is served normally, without the headers.
- The allowed origin is echoed back exactly, and every answer from a configuration that decides by origin (a list, a wildcard subdomain, `AllowOrigin`) carries `Vary: Origin`, also for a request with no `Origin` or an unlisted one. With `"*"` the answer is the same for everyone and has no `Vary`.

Origins are matched exactly and case-sensitively against the lower-case form browsers send, and the configured origins are lower-cased when the middleware is made. A wildcard subdomain entry (`https://*.example.com`) matches an origin with its scheme, ending in its `.example.com` and port, whose remaining head is one or more non-empty labels of lower-case letters, digits, `-` and `_`; `https://example.com`, `https://evilexample.com`, `https://example.com.evil.com`, `https://a.example.com.`, `https://a..example.com`, an origin with a path or user, upper case, another scheme and another port do not match. `AllowOrigin` is asked last, for an origin nothing else matched. The origin `null` cannot be listed and is never passed to `AllowOrigin`. A panic recovered by the engine is a 500 written after this middleware was left and carries no CORS headers.

Allocations: a simple request from an allowed origin costs none, listed or matched by a wildcard subdomain; `AllowOrigin`'s own allocations are the application's ([ADR 0003](../adr/0003-the-trade-budget.md)).

It panics with a `RegistrationError` when the options cannot work. The rows of the refusal table, after `CORS: `:

| Mistake | Message |
|---|---|
| no origin and no `AllowOrigin` | `Origins is empty and AllowOrigin is nil: list the full origins that may call this API ("https://app.example.com") or a wildcard subdomain ("https://*.example.com"), set AllowOrigin to decide at run time, or use "*" for any origin` |
| `"*"` with another origin | `Origins has "*" together with 1 other origins: "*" already allows every origin, so list it alone` |
| `"*"` with `Credentials` | `Origins is "*" with Credentials: a browser refuses a wildcard answer to a request with credentials, so list the full origins (https://app.example.com) or drop Credentials` |
| an origin with no scheme | `Origins[0] "app.example.com" is not a full origin: it must start with http:// or https://` |
| a path, trailing slash, query or user | `Origins[1] "https://app.example.com/" is not a full origin: it must be scheme://host[:port], with no path, no trailing slash, no query and no user` |
| `"*"` with a wildcard subdomain | `Origins has "*" together with the wildcard subdomain "https://*.example.com": "*" already allows every origin, so list it alone` |
| `"*"` with `AllowOrigin` | `Origins is "*" with AllowOrigin: "*" already allows every origin, so AllowOrigin would never be asked; drop one of them` |
| a `*` that is not the whole leftmost label | `Origins[0] "https://app-*.example.com" has a * that is not the whole leftmost label: write a wildcard subdomain as https://*.example.com, and list any other host in full` |
| a wildcard over fewer than two labels | `Origins[0] "https://*.com" is a wildcard over fewer than two labels: a wildcard such as https://*.com would allow a whole top-level domain, so name a domain you control (https://*.example.com)` |
| a wildcard with no scheme or with a path | `Origins[0] "*.example.com" is not a full origin: it must start with http:// or https://` |
| `null` | `Origins[0] "null" is not a full origin: "null" is what any sandboxed page sends, so it cannot be listed` |
| a port that is not a number | `Origins[0] "http://localhost:http" is not a full origin: the port is not a number from 1 to 65535` |
| a method or header that is not a token | `Methods[1] "PU T" is not an HTTP method`, `Headers[0] "Content Type" is not an HTTP header name`, `ExposeHeaders[0] "X:A" is not an HTTP header name` |
| `"*"` as a method or header with `Credentials` | `Methods[0] is "*" with Credentials: a browser reads "*" literally when credentials are sent, so name the methods` |
| `"*"` beside other headers | `Headers has "*" together with 1 other headers: "*" already allows every header, so list it alone` |
| a negative or sub-second `MaxAge` | `MaxAge is 500ms, which is not a whole number of seconds: give zero for the browser's default, or at least a second` |
| `Paths` of `/` | `Paths[0] is "/", which is every path: leave Paths empty for that` |
| a path in `Paths` that is not a prefix | `Paths[1] "api/" is not a path prefix: it starts with a slash, does not end with one and has no wildcard, query or fragment ("/api")` |
| a path in `Paths` twice | `Paths[1] "/api" is given twice` |
| `CORS` given to a group or a route (after the route or group label) | `Use: middleware 1 is CORS, which answers a preflight, an OPTIONS request no route has, so on a group or a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api` |
| `CORS` returned by a `UseEach` function | `GET /x: the function given to UseEach (at file.go:N) returned CORS, which answers a preflight, an OPTIONS request no route has, so on a route it is never reached and every preflight is a 405: give it to App.Use, with CORSOptions.Paths to limit it to a path such as /api` |

Guide: [CORS, request IDs, access logs and timeouts](../guide/first-party-middleware.md). Decision: [ADR 0032](../adr/0032-four-first-party-middleware.md).

### `CORSOptions`

```go
type CORSOptions struct {
	Origins       []string
	AllowOrigin   func(origin string) bool
	Methods       []string
	Headers       []string
	ExposeHeaders []string
	Credentials   bool
	MaxAge        time.Duration
	Paths         []string
}
```

CORSOptions configures `CORS`.

- `Origins` are the origins allowed to call the API, each in full: scheme, host and, when it is not the default, port (`https://app.example.com`, `http://localhost:5173`), with no trailing slash and no path. An entry may be a wildcard subdomain, `https://*.example.com` (`https://*.example.com:8443` with a port): the `*.` is the whole leftmost label and stands for one or more labels (`a.example.com`, `a.b.example.com`), never for none, so the apex `https://example.com` is listed separately; the scheme and the port are matched as written, and what follows `*.` has at least two labels (`*.com` and `*.localhost` are refused). The check does not know public suffixes: `https://*.co.uk` is accepted and allows every site under `.co.uk`, so name a domain you control. There is no other pattern. The single entry `"*"` allows any origin and is refused together with a wildcard entry, with `AllowOrigin` and with `Credentials`. `Origins` empty with `AllowOrigin` nil is refused.
- `AllowOrigin` decides an origin that `Origins` does not list, for origins known only at run time, such as a tenant's own domain looked up in a database. It is called only for an origin no entry of `Origins` matched, and never for `null` or for an origin that is not `scheme://host[:port]` in lower case; true allows it exactly as a listed origin is allowed: the origin is echoed, never `*`, and `Credentials` applies. It must be safe to call concurrently and should be fast. Compare the whole origin against what you hold: a suffix check written by hand lets `https://evilexample.com` in. A panic in it is a request panic like any middleware's. Nil adds nothing to `Origins`.
- `Methods` are the methods a preflight may ask for; GET, HEAD and POST when empty. Naming a method here allows the browser to send it; whether the route exists is the router's.
- `Headers` are the request headers beyond the safelisted ones that a preflight may ask for (a JSON body needs `Content-Type`, a token `Authorization`). None when empty. The single entry `"*"` allows any header and is refused together with `Credentials`.
- `ExposeHeaders` are the response headers a script may read beyond the safelisted ones.
- `Credentials` allows cookies and authorization headers on cross-origin requests (`Access-Control-Allow-Credentials: true`).
- `MaxAge` is how long a browser may cache a preflight answer, in whole seconds; the browser's default when zero. Browsers cap it (2 h in Chrome).
- `Paths` limits CORS to the requests whose path is one of them or below one (`/api` covers `/api` and `/api/files`, not `/apis`); every path when empty. A request outside them is passed on untouched: a preflight there reaches the router, and an answer gets no CORS header, not even `Vary: Origin`. Each entry starts with a slash and does not end with one, and `/` is refused (leave `Paths` empty).

<!-- compiles: body -->

```go
app.Use(
	bites.RequestID(),
	bites.CORS(bites.CORSOptions{
		Origins:     []string{"https://app.example.com"},
		Methods:     []string{"GET", "POST", "PATCH"},
		Headers:     []string{"Content-Type", "Authorization"},
		Credentials: true,
		MaxAge:      time.Hour,
	}),
)
```

### `RequestID`

```go
func RequestID(opts ...RequestIDOptions) Middleware
```

RequestID is the request-ID middleware. It gives the request an id with `Request.SetRequestID`, so every problem+json answer carries it as `request_id`, and sets it as a header of the response.

The id is the one the client sent in the header when it is well formed: 1 to 64 bytes, each a letter, a digit or one of `-_.:`. That covers a UUID, a ULID, a hex trace id and the ids gateways add, and keeps out whatever could split a header, break a log line or make an answer large. Anything else is not rejected: it is replaced. Without a usable incoming id it generates 16 lower-case hex digits of a random 64-bit value from `math/rand/v2`. That is unique enough to find one request in a log and is not a secret or a token; do not use it as one.

Put it first in the chain so that every answer written inside it, a preflight or a 404 included, carries the id. A panic recovered by the engine after the middleware set the id carries it in the 500's body and in the same response header: the middleware tells the engine its header name when it sets the id, since the chain never returns to add it.

Allocations: none, with the id generated or taken ([ADR 0003](../adr/0003-the-trade-budget.md)). Registration sees `RequestID` in a route's or the app's chain and sizes that chain's slab with 16 bytes for a generated id; called from inside another middleware it is not seen, and a generated id then costs one allocation.

It panics when more than one options value is given (`RequestID: 2 options values were given; give one RequestIDOptions`) or the header is not a token (`RequestID: Header "X Request" is not an HTTP header name: use letters, digits and the symbols of RFC 9110 token, without spaces or colons`).

### `RequestIDOptions`

```go
type RequestIDOptions struct {
	// Header is the name of the request and response header that carries the id; "X-Request-ID"
	// when empty. It must be an HTTP token.
	Header string
}
```

RequestIDOptions configures `RequestID`.

### `Timeout`

```go
func Timeout(d time.Duration) Middleware
```

Timeout is the per-route timeout middleware. It is meant for a route or a group (`bites.Use(bites.Timeout(2*time.Second))`); as app middleware it covers every request, 404 included.

When the rest of the chain has not returned after `d`, measured on the app's clock (so a test moves `bitetest.Clock` instead of waiting), the request's context is cancelled with `context.DeadlineExceeded` as its cause, and the middleware keeps waiting. The handler is never abandoned: Go cannot stop a goroutine, and answering while it still runs would put a second goroutine on the request ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md), [ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). When the handler returns, the answer is a 503 problem+json whatever it returned (a value, an error or a Stream), and its own answer is dropped; a handler that ignores its context is therefore late, visibly. One that ran more than twice the deadline is logged at Warn with how long it ran. A handler that returns before the deadline is not affected and the timer is stopped.

The context is the request's own, so cancelling it also reaches a Provider's work and any middleware inside this one. An earlier cancellation (the client left) wins and stays the cause; then the 503 is written to nobody. A Stream or SSE is not under the deadline once the handler has returned it: the timeout bounds only the time to produce the stream. If the deadline passes before the handler returns the stream, the 503 replaces it and its function never runs.

Allocations: none for a request that finishes in time, after the first ([ADR 0003](../adr/0003-the-trade-budget.md)).

It panics when `d` is zero or negative: `Timeout: the duration is 0s, which is not a deadline: give a positive duration such as 2*time.Second`.

<!-- compiles: body -->

```go
app.Get("/report", func(ctx context.Context, in struct{}) (bites.Text, error) {
	select {
	case <-ctx.Done():
		return bites.Text{}, context.Cause(ctx)
	case <-time.After(time.Millisecond):
		return bites.Text{Body: "done"}, nil
	}
}, bites.Use(bites.Timeout(2*time.Second)))
```
