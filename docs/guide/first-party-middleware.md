# CORS, request IDs, access logs and timeouts

**Four first-party pieces ship with bites: the middleware `CORS`, `RequestID` and `Timeout`, which you register like your own, and `AccessLog`, an Observer given to the app when it is made.**

**Reference:** [Middleware](../reference/middleware.md) · **Design:** [Middleware and Providers](../design/middleware-and-providers.md)

They are in the root package and not a subpackage because each reaches the app's logger, its clock and the request's memory through the `Request` it is given ([ADR 0032](../adr/0032-four-first-party-middleware.md)). Each is made by a constructor that refuses a configuration that cannot work at Registration, so a typo in an origin is a panic at start-up and not a missing header in production. Read [the middleware page](./middleware.md) first for what a `Middleware` is and where it is registered.

The examples use one handler.

<!-- compiles: decls -->
```go
type Ping struct{}

type Pong struct {
	OK bool
}

func ping(ctx context.Context, in Ping) (Pong, error) {
	return Pong{OK: true}, nil
}

// slow waits for its context, as a handler that calls a database would.
func slow(ctx context.Context, in Ping) (Pong, error) {
	select {
	case <-time.After(10 * time.Second):
		return Pong{OK: true}, nil
	case <-ctx.Done():
		return Pong{}, context.Cause(ctx)
	}
}
```

## Letting a browser page call the API

**[`CORS`](../reference/middleware.md#cors) answers a preflight itself and adds the allow headers to every answer for a listed origin.** It is app middleware, because a preflight is an `OPTIONS` request that no route has, and app middleware runs before routing ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)).

<!-- compiles: body -->
```go
app.Use(bites.CORS(bites.CORSOptions{
	Origins:       []string{"https://app.example.com", "http://localhost:5173"},
	Methods:       []string{"GET", "POST", "PUT", "DELETE"},
	Headers:       []string{"Content-Type", "Authorization"},
	ExposeHeaders: []string{"X-Request-ID"},
	Credentials:   true,
	MaxAge:        time.Hour,
}))
```

The fields of [`CORSOptions`](../reference/middleware.md#corsoptions):

- `Origins` lists the allowed origins in full (`scheme://host[:port]`), matched exactly, or as a wildcard subdomain (`https://*.example.com`, see below). The single entry `"*"` allows any origin.
- `AllowOrigin` decides, at run time, an origin that `Origins` does not list. `Origins` or `AllowOrigin` is required.
- `Methods` are the methods a preflight may ask for: GET, HEAD and POST when empty.
- `Headers` are the request headers beyond the safelisted ones that a preflight may ask for: none when empty, so a JSON API names `Content-Type`, and a token needs `Authorization`.
- `ExposeHeaders` are the response headers a script may read.
- `Credentials` allows cookies and authorization headers on cross-origin requests.
- `MaxAge` is how long a browser may cache a preflight answer, in whole seconds; zero leaves the browser's default.
- `Paths` limits CORS to part of the app, the path prefixes it covers; every path when empty.

**To give CORS to an API under `/api` and not to the pages beside it, set `Paths`; do not give it to the `/api` group.** A group's middleware runs after routing, and a preflight is an `OPTIONS` request no route has, so the router would answer it 405 before CORS saw it. `bites.Use(bites.CORS(...))` on a group or a route is refused at Registration for that reason, naming the route.

<!-- compiles: body -->
```go
app.Use(bites.CORS(bites.CORSOptions{
	Origins: []string{"https://app.example.com"},
	Methods: []string{"GET", "POST", "PUT"},
	Headers: []string{"Content-Type", "X-API-Key"},
	Paths:   []string{"/api"}, // /api and below; /login and /files get no CORS header
}))
```

A preflight from a listed origin is a 204 that never reaches the router:

```http
OPTIONS /ping HTTP/1.1
Host: api.example.com
Origin: https://app.example.com
Access-Control-Request-Method: GET
Access-Control-Request-Headers: authorization

HTTP/1.1 204 No Content
Access-Control-Allow-Origin: https://app.example.com
Access-Control-Allow-Methods: GET, POST, PUT, DELETE
Access-Control-Allow-Headers: Content-Type, Authorization
Access-Control-Max-Age: 3600
Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers
```

**A preflight that is not allowed gets the same 204 with none of the allow headers, which is what makes the browser refuse the real request.** The server has no other answer to give, since CORS is the browser's rule and not an access control: a request from an unlisted origin is served normally, without the headers. The headers of an allowed origin are added to every answer, a 404, a 405 and every problem+json included, so the page can read the error. The answer echoes the one origin (never a list) and carries `Vary: Origin`, so a shared cache does not hand one origin's answer to another; with `"*"` there is no `Vary`.

**The constructor refuses what a browser would ignore.** It panics with a [`RegistrationError`](../reference/app.md#registrationerror) for no origin, an origin that is not a full origin (a path, a trailing slash, no scheme, `"null"`), a `*` that is not the whole leftmost label or a wildcard over fewer than two labels (`https://*.com`), `"*"` beside other origins, a wildcard subdomain or `AllowOrigin`, or together with `Credentials`, a method or header that is not an HTTP token, and a negative or sub-second `MaxAge`.

## Letting every tenant's subdomain call the API

**A SaaS that gives each tenant a subdomain lists one wildcard instead of a hundred origins.** `https://*.example.com` allows `https://acme.example.com` and `https://a.b.example.com`, and never the apex, which you list beside it when the marketing site calls the API too. The scheme and the port are matched as written, so `http://*.example.com` does not allow `https://acme.example.com`, and `https://*.example.com:8443` allows only that port. The part after `*.` needs at least two labels: `https://*.com` and `*.localhost` are refused when `CORS` is made, because one would allow every site under a top-level domain.

<!-- compiles: body -->
```go
app.Use(bites.CORS(bites.CORSOptions{
	Origins:     []string{"https://example.com", "https://*.example.com"},
	Headers:     []string{"Content-Type", "Authorization"},
	Credentials: true,
}))
```

**A wildcard is matched label by label, not as a suffix.** `https://evilexample.com`, `https://example.com.evil.com`, `https://a.example.com.`, `https://a..example.com`, an origin with a path or a user, a different port and a different scheme are all unlisted and get no `Access-Control-Allow-Origin`. With `Credentials` the answer still echoes the one exact origin, never `*`.

**A tenant's own domain is decided by `AllowOrigin`, which sees the origin and nothing else.** It is asked only for an origin that `Origins` did not match, and never for `null` or an origin that is not a lower-case `scheme://host[:port]`. It runs on the request's goroutine, concurrently with itself, so read a cache or a database that is safe for that, and keep it fast: every preflight from an unlisted origin reaches it.

<!-- compiles: body -->
```go
domains := map[string]bool{"https://shop.customer.io": true} // loaded from your tenant table
var mu sync.RWMutex

app.Use(bites.CORS(bites.CORSOptions{
	Origins: []string{"https://*.example.com"},
	AllowOrigin: func(origin string) bool {
		mu.RLock()
		defer mu.RUnlock()
		return domains[origin]
	},
	Credentials: true,
}))
```

**Compare the whole origin against what you hold; a suffix check written by hand is how `evilexample.com` gets in.** `strings.HasSuffix(origin, "example.com")` allows `https://evilexample.com`, and `strings.Contains` allows `https://example.com.evil.com`. A lookup of the exact origin, as above, cannot be fooled; for a subdomain family use the wildcard entry, which has the dot and the label rules built in. `"*"` cannot be combined with a wildcard entry or with `AllowOrigin`, since it already allows every origin.

## Giving every request an ID

**[`RequestID`](../reference/middleware.md#requestid) gives each request an ID, sets it as a response header and makes every problem+json answer carry it as `request_id`.**

<!-- compiles: body -->
```go
app.Use(bites.RequestID(bites.RequestIDOptions{Header: "X-Correlation-ID"}))
```

[`RequestIDOptions`](../reference/middleware.md#requestidoptions) has one field, `Header`, the name of the request and response header; it is `X-Request-ID` when empty, and it must be an HTTP token. The ID is the one the client sent when it is well formed: 1 to 64 bytes, each a letter, a digit or one of `-_.:`, which covers a UUID, a ULID, a hex trace ID and the IDs that gateways add. Anything else is replaced and not rejected, because a client's formatting mistake is no reason to fail its request; the ID is echoed in a header, in every error body and in the log, so what could split a header or break a log line is kept out. Without a usable ID it generates 16 lower-case hex digits from a random 64-bit value. That finds one request in a log; it is not a secret or a token.

```http
GET /nope HTTP/1.1
Host: api.example.com

HTTP/1.1 404 Not Found
Content-Type: application/problem+json
X-Request-ID: 0f65dfe672f324af

{"title":"Not Found","status":404,"request_id":"0f65dfe672f324af"}
```

## Logging each request

**[`AccessLog`](../reference/observability.md#accesslog) is an [Observer](./observability.md): it writes one `slog` record, with the message `request`, when each request has really ended, after the last byte of its answer was written.** It is given to the app when it is made, with `WithObserver`, not with `app.Use`.

<!-- compiles: body -->
```go
logged := bites.New(bites.WithObserver(bites.AccessLog(bites.AccessLogOptions{
	Logger: slog.Default(),
	Level:  slog.LevelInfo,
})))
logged.Get("/ping", ping)
```

[`AccessLogOptions`](../reference/observability.md#accesslogoptions) has two fields: `Logger`, which is the app's own logger ([`WithLogger`](../reference/app.md#withlogger)) when nil, and `Level`, `slog.LevelInfo` by default. A logger that is not enabled at the level costs one `Enabled` call and nothing else. The record has these attributes:

- `method`, `path` (without the query, because a query is where secrets and personal data go), `status` (what was sent), `size` (the body bytes written, after gzip) and `duration` (from the parsed head to the last byte, on the app's [clock](../reference/app.md#appclock)).
- `request_id`, when a middleware set one, and `trace_id` and `span_id` when the request has a [trace](./observability.md#following-a-request-across-services).
- `streaming=true` for a [stream](./streaming.md), `abandoned=true` when the client left, which is how a vanished client differs from a failure of the server ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)), and `panicked=true` after a panic.
- `error`, the text of the error the handler or a middleware returned, when there was one, so the line of a failure says why on its own ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).
- Every attribute a Provider or middleware added with [`Request.AddLogAttrs`](../reference/middleware.md#requestaddlogattrs), such as the authenticated user ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)).

```text
level=INFO msg=request method=GET path=/ping status=200 size=11 duration=0s request_id=13f054ce1fa30bca
```

**Because the record is written at the real end, it tells the truth about the answers a middleware cannot see.** A stream's record has its real status, the bytes it sent and its whole life as the duration; a panic leaves a record with the 500 and `panicked=true`; a gzipped answer logs its compressed size; and a `Problem` a middleware returned, such as an auth middleware's 401 or the 503 of `Timeout`, logs the size of the body that was sent. Every request that reaches the app is logged, a 404, a 405 and a CORS preflight included; the probes of [`WithHealth`](./deploying.md#health-readiness-and-the-shutdown-delay) are not. The record is written after the client has its answer, so a [test](./testing.md) that reads it waits for it.

**Its cost is 2 allocations with up to five attributes, and 3 with a request id, a trace or any attribute beyond five**, because a `slog.Record` holds five attributes inline ([ADR 0003](../adr/0003-the-trade-budget.md), the row for `AccessLog`). It panics if more than one options value is given.

**A handler's own log lines carry `request_id` and `method` and `route`, and `request_id` joins them to the access line.** [`Logger(ctx)`](../reference/observability.md#logger) is the app's logger with those attributes added, and [`RequestIDFrom(ctx)`](../reference/middleware.md#requestidfrom) is the id alone, for a header you send on to another service or a field of your own. Both work on a context you derived and on one kept after the request ended.

<!-- compiles: body -->
```go
ping := func(ctx context.Context, _ struct{}) (string, error) {
	lg := bites.Logger(ctx) // request_id, method and route are on every line
	lg.Info("answering a ping")
	return "pong", nil
}
_ = ping
```

`Logger` allocates when it is called (the `With`), so call it once and not for each line; a request that does not call it pays nothing. `Request.RequestID` stays for a middleware or an Observer, which hold a `Request` and not a context.

## Cutting off a slow handler

**[`Timeout`](../reference/middleware.md#timeout) cancels the request's context after a duration, waits for the handler to return, and then answers 503.** It is meant for a route or a group; as app middleware it covers every request.

<!-- compiles: body -->
```go
app.Get("/slow", slow, bites.Use(bites.Timeout(2*time.Second)))
```

After the duration (measured on the app's clock, so a [test](./testing.md) moves a `bitetest.Clock` instead of waiting) the context is cancelled with `context.DeadlineExceeded` as its cause. The middleware keeps waiting: the handler is never abandoned and never answered over, because Go cannot stop a goroutine and a second goroutine writing a response while the handler might still act would break [ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md). When the handler returns, the answer is a 503 whatever it returned (a value, an error or a stream), and its own answer is dropped:

```http
GET /slow HTTP/1.1
Host: api.example.com

HTTP/1.1 503 Service Unavailable
Content-Type: application/problem+json
X-Request-ID: 73d00cb857a7ebf4

{"title":"Service Unavailable","status":503,"detail":"the request did not finish in time","request_id":"73d00cb857a7ebf4"}
```

A handler that ignores its context is therefore late, visibly. One that ran more than twice the deadline is logged at Warn with how long it ran ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). The same context reaches a Provider's work and the middleware inside, which is the intent of a deadline. If the client left first, that cancellation wins and the 503 is written to nobody.

**A stream is not under the deadline once the handler has returned it.** Its function runs after every middleware has returned, so the timeout bounds only the time to produce the stream; a stream that must end by a time ends itself on its context or its own timer. If the deadline passes before the handler returns the stream, the 503 replaces it and its function never runs. `Timeout` panics at Registration for a duration of zero or less. It allocates nothing for a request that finishes in time, after the first.

The other time limits, the ones the engine applies to every connection, are on [the limits page](./limits.md).

## Combining them, and in which order

**Give `AccessLog` to `New`, and put `RequestID` first, then `CORS`, then any app-wide `Timeout`.** The first `app.Use` argument is outermost, so each middleware sees the answers of everything after it; `AccessLog`, an Observer, sees every answer whatever the order.

<!-- compiles: decls -->
```go
func newApp(logger *slog.Logger) *bites.App {
	app := bites.New(bites.WithLogger(logger), bites.WithObserver(bites.AccessLog()))
	app.Use(
		bites.RequestID(),
		bites.CORS(bites.CORSOptions{
			Origins: []string{"https://app.example.com"},
			Headers: []string{"Content-Type", "Authorization"},
		}),
		bites.Timeout(5*time.Second),
	)
	app.Get("/ping", ping)
	app.Get("/slow", slow, bites.Use(bites.Timeout(2*time.Second)))
	return app
}
```

The reasons for each place:

- `RequestID` first, so every answer written inside it, a preflight and a 404 included, carries the ID. A panic recovered by the engine after the ID was set carries it in the 500's body and in the same header, although the chain never returned.
- `AccessLog` is not in the chain: it reads the ID that `RequestID` set and the answer that was sent, such as the CORS 204 and the timeout's 503, when the request has ended.
- `CORS` before `Timeout`, so the 503 of a timeout still carries the CORS headers and the page can read it.
- A route's own `Timeout` runs after routing, inside all of these, so a shorter limit on `/slow` wins over the app-wide five seconds.

A panic recovered by the engine is a 500 written after these middleware were left: it carries no CORS headers, and its access record says `status=500` and `panicked=true` ([ADR 0028](../adr/0028-panics-are-recovered-by-the-engine.md)).
