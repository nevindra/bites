# Mounting an http.Handler

**An existing `net/http` handler (pprof, expvar, a file server, a third-party handler) is served on a route with `stdhttp.Mount`, and the whole app is served from a `net/http` server with `stdhttp.Handler`, both as slow paths that sit outside the allocation budget.**

**Reference:** [`stdhttp`](../reference/stdhttp.md) · **Design:** [Routing](../design/routing.md)

The engine is not `net/http`, and the core never imports it ([ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md)). The `stdhttp` package is where the two meet: it builds an `*http.Request` and an `http.ResponseWriter` over the request and runs the handler. Reach for it for what already exists as an `http.Handler`; a route of your own belongs in a typed handler, which costs 2 allocations where a mounted GET costs 32.

## Mounting a handler

A pattern with a trailing slash covers a subtree, as in `http.ServeMux`, and the handler sees the full path.

<!-- compiles: body -->
```go
stdhttp.Mount(app, "/debug/pprof/", http.DefaultServeMux)
```

The `net/http/pprof` import registers its handlers on `http.DefaultServeMux`, so mounting that mux serves `/debug/pprof/` and the profiles under it. To hand a handler the path without its prefix, wrap it as you would in `net/http`:

<!-- compiles: body -->
```go
files := http.StripPrefix("/assets", http.FileServer(http.Dir("public")))
stdhttp.Mount(app, "/assets/", files)
```

`Mount` registers the handler for GET, POST, PUT, PATCH, DELETE and OPTIONS; a HEAD request is served by the GET route. Any other method is a 405 with an `Allow` header. [`stdhttp.MountMethods`](../reference/stdhttp.md#stdhttpmountmethods) names the methods when you want fewer (`"GET"` alone for a read-only mount) or more (PROPFIND for a WebDAV handler), and [`stdhttp.MountGroup`](../reference/stdhttp.md#stdhttpmountgroup) mounts inside a group, so the group's prefix, middleware and Providers apply. A mount is not in the [OpenAPI document](./openapi.md): its routes carry [`bites.Undocumented()`](../reference/openapi.md#undocumented), since a handler's types say nothing about what it serves.

## Protecting it

A mount is a set of ordinary typed routes, so the route options of [the other pages](./middleware.md) apply. A profile endpoint should not be public:

<!-- compiles: body -->
```go
stdhttp.Mount(app, "/debug/pprof/", http.DefaultServeMux,
	bites.Use(bites.Timeout(2*time.Minute)),
	bites.MaxBody(1<<20))
```

Put an authentication middleware in the same `Use` to keep it private. The `MaxBody` bounds the request body a handler may read, which is read whole before the handler runs, so a mount is the wrong place for an upload that should stream (use an [`Upload`](./forms-and-uploads.md) route).

## What the handler gets and what it cannot do

The handler sees the method, the decoded path, the query, every header, `Host`, `RemoteAddr`, the body for POST, PUT, PATCH and OPTIONS, and a context that is cancelled when the client leaves ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)). It can write, set headers, and flush; a handler that flushes streams, so a `pprof` profile of 30 seconds or an event stream reaches the client as it is written. A response the handler finishes before it flushes and before 32 KiB is sent with a `Content-Length`.

It cannot hijack the connection (the engine owns it), set per-request deadlines through `http.ResponseController` (the route's [limits](./limits.md) apply), send trailers or 1xx responses (dropped), or see `Request.TLS`. [The reference](../reference/stdhttp.md) lists them all with the measured cost.

## Serving the app from a net/http server

**[`stdhttp.Handler(app)`](../reference/stdhttp.md#stdhttphandler) is the other way round: it returns the whole app as an `http.Handler`.** Use it to move an existing `net/http` service to bites one route at a time, and to keep tests written with `httptest`. Call it after the last route, since it starts serving the app; `app.Shutdown` stops it.

<!-- compiles: body -->
```go
mux := http.NewServeMux()
mux.Handle("/v2/", http.StripPrefix("/v2", stdhttp.Handler(app))) // the routes moved so far
mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
	// everything not moved yet, as before
})
```

A test drives it as it would any handler, through a `ResponseRecorder` or an `httptest.Server`; a WebSocket route needs the server, since a recorder cannot hand the connection over.

<!-- compiles: body -->
```go
h := stdhttp.Handler(app)
rec := httptest.NewRecorder()
h.ServeHTTP(rec, httptest.NewRequest("GET", "/users/7", nil))
```

Every request is written to the app as HTTP/1.1 over a connection in memory and the answer read back, so the app behaves exactly as on its own listener, and a request costs about 64 allocations instead of 2. Serve the app with `app.Listen` when it carries the traffic; the app sees no TLS through `Handler`, so `Request.Scheme` is `"http"` even when the `net/http` server terminated TLS. Tests that need no `net/http` are faster with [`bitetest`](./testing.md).
