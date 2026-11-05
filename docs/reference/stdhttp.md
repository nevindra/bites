# stdhttp

**The package that mounts a plain `net/http` handler on a route and serves an app as an `http.Handler`, both as slow paths outside the allocation budget, and the only package of the module that imports `net/http`.**

**Guide:** [mounting an http.Handler](../guide/mounting-net-http.md) · **Design:** [Routing](../design/routing.md)

`github.com/nevindra/bites/stdhttp` serves an `http.Handler` (pprof, expvar, `http.FileServer`, a third-party handler) on a bites route ([ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md)). It is separate so the core never imports `net/http` and an application that mounts nothing links none of it. Each request builds an `*http.Request` and a `ResponseWriter` and runs the handler on a goroutine of its own; a mounted GET allocates 32 times, a POST with a 1 KiB body 35 and a GET whose handler flushes three times 31 to 39, against 2 for a typed JSON request. The names here carry the `stdhttp.` prefix in their headings because they live in another package.

## What a mounted handler sees and can do

The handler sees the method, the decoded path (`RawPath` when it differs), the query, `RequestURI`, every header, `Host`, `RemoteAddr`, `ContentLength`, the engine's context (cancelled when the client leaves) and, for POST, PUT, PATCH and OPTIONS, the body, read whole up to the route's `MaxBody`. `Request.TLS` is nil, and a body on GET, HEAD and DELETE is not read.

It can `WriteHeader`, set `Header`, `Write` and `Flush` (also through `http.NewResponseController(w).Flush()`). The response is buffered and sent with a `Content-Length` when the handler returns before it flushes and before it has written 32 KiB; otherwise it is streamed, chunked. A 204 and a 304 carry no body and a `Write` on them returns `http.ErrBodyNotAllowed`. A panic is a 500 before the first flush and a cut connection after it.

Not supported: `Hijack` and the deadline setters (the `ResponseController` returns an error), trailers and 1xx responses (dropped), HTTP/2 push, and a `Content-Length` set by the handler (the engine's replaces it). The request is built by a Provider that `Mount` registers for the route ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)), so a mounted route also takes a `Provide` of its own. A mounted route shows in the OpenAPI document as operations answering `Bytes`, `Stream`, `NoContent` and `NotModified`.

### `stdhttp.Mount`

```go
func Mount(app *bites.App, pattern string, h http.Handler, opts ...bites.RouteOption)
```

Mount serves `h` for every method of `Methods` on `pattern`, a path with no method in it and, to cover a subtree, a trailing slash (`"/debug/pprof/"`). The handler sees the full path; wrap it in `http.StripPrefix` to hand it the rest. `opts` are the route options of the typed routes (`MaxBody`, `Use`, `Provide`). The routes are left out of the app's API document: each carries [`bites.Undocumented`](./openapi.md#undocumented), so a `Summary` or `Description` in `opts` is refused. It panics at the line that called it for a nil handler, and with the usual Registration message for a pattern with a method, a conflict with another route or an option that cannot work.

### `stdhttp.MountGroup`

```go
func MountGroup(g *bites.Group, pattern string, h http.Handler, opts ...bites.RouteOption)
```

MountGroup is `Mount` on a group; the pattern is relative to the group's prefix, and the group's middleware, Providers and limits apply.

### `stdhttp.MountMethods`

```go
func MountMethods(app *bites.App, methods []string, pattern string, h http.Handler, opts ...bites.RouteOption)
```

MountMethods is `Mount` for the methods given. A method that is not a standard one is allowed (PROPFIND for a WebDAV handler), and so is a subset: `"GET"` alone makes a read-only mount whose other methods are a 405. `HEAD` is not listed: a GET route answers HEAD.

### `stdhttp.Methods`

```go
var Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
```

Methods is the set `Mount` registers. A HEAD request is served by the GET route, with the handler's body dropped and its `Content-Length` kept.

### `stdhttp.Handler`

```go
func Handler(app *bites.App) http.Handler
```

Handler returns `app` as an `http.Handler`, for a `net/http` server that serves it beside other handlers (routes moved to bites one at a time behind an existing mux, with `http.StripPrefix` in front when it sits under a prefix) and for a test that drives it with `httptest` ([ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md)). The app is served on an in-memory listener as `bitetest` serves it, and each request is written to it as HTTP/1.1 and its answer read back, so every behaviour of the app is the one it has on its own listener: limits, middleware, refusals, gzip, problem+json, streams and SSE, which are flushed as they arrive.

- It starts serving the app, as `Listen` does: a route registered after it panics, so call it after the last route. `Shutdown` and `Close` stop it with the app's other listeners; a request after that is a 503 problem+json. It panics for a nil app, and with the error of a Service whose Start failed (ADR 0079).
- The app sees the method, the path and query of the request's URL, its `Host`, every header but the hop-by-hop ones, the body (streamed, chunked when its length is unknown) and `RemoteAddr` as the peer, so `TrustedProxies` decide on the real client. When the client leaves, the app's context is cancelled with `ErrClientGone`.
- The answer's status, headers and body reach the `ResponseWriter` as the app wrote them. A body the app cut (a stream that failed) aborts the response with `http.ErrAbortHandler`, so the client sees a cut answer.
- A WebSocket upgrade takes the connection over with `Hijack` and joins it to the app's, so a WebSocket route works under an HTTP/1.1 server (`httptest.NewServer` among them); a `ResponseWriter` that cannot hijack (HTTP/2, `httptest.ResponseRecorder`) gets a 501 problem+json.
- A header whose name or value would split the head (a test can build one) is a 400 problem+json, and the app never sees it.
- The app sees no TLS: `Request.Scheme` is `"http"` unless a trusted proxy says otherwise, even when the `net/http` server terminated TLS itself. Trailers are dropped.
- A typed JSON GET through it allocates 64 times (with the `ResponseRecorder`'s own) and takes about 5 µs on a Ryzen 7 9700X, against 2 allocations for the same request on the app's own listener.
