# Observability

**The Observer and its Outcome, the AccessLog Observer, Metrics in the Prometheus text format, W3C trace context, and the health and readiness answers with the shutdown delay.**

**Guide:** [observability](../guide/observability.md) · **Design:** [observability](../design/observability.md)

An Observer is told when a request starts, before app middleware, and when it has really ended, after the last byte was written or the connection was given up ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)). It watches and never answers. `AccessLog` and `Metrics` are Observers; `WithTraceContext` reads the trace before any Observer runs; `WithHealth` answers its probes before all of it, and they are not observed.

Allocations: an Observer that allocates nothing costs none, so the typed JSON POST stays at 2 with any number of them; `Metrics` costs none once a label set has its series; `WithTraceContext` costs none; `AccessLog` costs what its slog record costs (2 up to five attributes, 3 beyond); a health probe is 1, the request's context ([ADR 0003](../adr/0003-the-trade-budget.md)).

What Registration refuses: `WithObserver`, `WithHealth`, `WithShutdownDelay` or `WithTraceContext` given twice; a nil Observer; a health path that does not start with a slash or has a wildcard, query or fragment, the same path twice, or neither; a route at a health path; a negative delay; `NewMetrics` with two options values, empty or unordered or non-positive buckets, or a negative `MaxSeries`; `ServeMetrics` with a nil `Metrics` or a bad path. Each panics with a `RegistrationError` naming where it was given.

<!-- compiles: decls -->

```go
func observedApp() *bites.App {
	m := bites.NewMetrics()
	app := bites.New(
		bites.WithObserver(bites.AccessLog(), m),
		bites.WithTraceContext(bites.TraceContext{}),
		bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}),
		bites.WithShutdownDelay(5*time.Second),
	)
	app.ServeMetrics("/metrics", m)
	return app
}
```

## Observers

### `Observer`

```go
type Observer interface {
	Start(ctx context.Context, req Request) context.Context
	End(ctx context.Context, req Request, out Outcome)
}
```

Observer is told when a request starts and when it has really ended. Start runs once the head is parsed, before app middleware; the context it returns is the one the middleware, the Providers and the handler get, so a tracer puts its span there, and an Observer with nothing to add returns `ctx`. End runs once, after the last byte of the answer was written (a stream's last chunk, the compressed body, the 500 after a panic, the problem+json a middleware returned), at the 101 of a WebSocket upgrade, or once the connection was given up; it gets the context its own Start returned and the same `Request`, whose live methods answer zero values once End has returned.

- Observers run in the order given for Start and in reverse for End.
- A panic in Start is the request's panic: answered 500 and logged, and End still runs for every Observer whose Start had returned. A panic in End is logged, the other Observers' End still run, and the connection is closed.
- End runs on the connection's goroutine after the client has its answer: an End that blocks holds the connection, and a test that reads what an Observer recorded calls [`bitetest.Server.WaitEnded`](./bitetest.md#bitetestserverwaitended) first.
- Not observed: a head the engine refuses before parsing it, an `OPTIONS *`, and the probes of `WithHealth`.

### `SocketObserver`

```go
type SocketObserver interface {
	SocketOpened(route string)
	SocketClosed(route string, info CloseInfo)
	SocketPongMissed(route string)
}
```

SocketObserver is implemented, besides `Observer`, by an Observer that wants to know of the app's WebSockets; `WithObserver` finds it, so there is nothing more to register ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)). `route` is the pattern of the WebSocket route with its group's prefix, bounded by the app, safe as a metric label.

- `SocketOpened` is called once the 101 is written. A refused handshake never opens a socket and is seen only by `End`.
- `SocketClosed` is called once for every socket `SocketOpened` was called for, after `OnClose` has returned; `info.Cause` says why ([`CloseCause`](./websocket.md#closecause)).
- `SocketPongMissed` is called each time a heartbeat tick finds the last ping unanswered; a socket that misses `Limits.MissedPongs` in a row is then closed with `CausePongTimeout`.
- The callbacks run on the connection's goroutine (opened, closed) or the heartbeat's (pong), so they must not block. A panic in one is recovered and logged and the socket is unaffected. An app with no SocketObserver pays nothing per socket.

### `Outcome`

```go
type Outcome struct {
	Route     string        // the pattern that matched, with its group's prefix; "" for a 404, a 405 and an answer before the match
	Status    int           // what was sent: 500 after a panic, the 503 of Timeout, 101 for an upgrade; for an abandoned request the status the chain left, 0 for none
	Read      int64         // body bytes read from the client
	Written   int64         // body bytes written, after gzip; 0 for a HEAD and when abandoned
	Duration  time.Duration // from Start to End, on the app's clock
	Streamed  bool          // the answer was a Stream or an SSE
	Upgraded  bool          // the answer was the 101 of a WebSocket upgrade
	Panicked  bool          // the handler, a middleware, a Provider, a Start or a stream's function panicked
	Abandoned bool          // the client left, or stopped reading, before the answer reached it whole
	Panic     any           // the value recovered when Panicked; nil otherwise
	Stack     []byte        // the stack of the panicking goroutine, naming the panicking function; nil otherwise
	Err       error         // the error the application returned that the framework answered; nil for none
}
```

Outcome is what End is told. It is a value and holds no pointer to pooled memory. It holds a slice, so it cannot be compared with `==`.

`Err` is the error a handler, a middleware, a Provider or a stream's function returned that the framework answered, at any status, and a stream function's error after its first byte. It is the value returned, so `errors.As` finds the application's types in it. The framework's own answers (a 400 for a body that does not decode, a 404, a 405) and a panic are not in it, a client that left is not an error, and an error whose answer a middleware replaced with a status below 400 is not reported. The last error answered stands. Nil when none was returned.

`Panic` is the value `recover` returned, with a `panic(nil)` arriving as a `*runtime.PanicNilError`; `Stack` is `runtime/debug.Stack()` taken in the recovery. Both are nil when `Panicked` is false. `Stack` is a fresh slice shared by the Observers of the request: copy it before changing it. All three are the application's own values and not pooled ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)).

### `WithObserver`

```go
func WithObserver(obs ...Observer) Option
```

WithObserver gives the app its Observers, in order. With Observers every request takes the chain's path, as a route with middleware does, which copies the head into the request's slab without an allocation. Giving it twice (`WithObserver: given twice (first at main.go:12, again at main.go:14): give every Observer in one WithObserver, in the order they should run`) or a nil Observer (`WithObserver: Observer 2 is nil`) makes `New` panic.

### `AccessLog`

```go
func AccessLog(opts ...AccessLogOptions) Observer
```

AccessLog is the access log, an Observer: one `slog` record, `request`, for each observed request, written at End with the attributes `method`, `path` (without the query), `status` (what was sent), `size` (body bytes written, after gzip), `duration` (from the parsed head to the last byte), `request_id` (when a middleware set one), `trace_id` and `span_id` (when the request has a `Trace`), `streaming=true`, `abandoned=true`, `panicked=true`, `error` (the text of `Outcome.Err`, when the application returned an error the framework answered), then every attribute a Provider or middleware added with `Request.AddLogAttrs`.

- A stream's record has its real status, the bytes it sent and its whole life as the duration.
- A panic leaves a record with the 500 and `panicked=true`.
- A `Problem` a middleware returned (a 401, the 503 of `Timeout`) has the size of the body sent.
- When the client left, `size` is 0, `status` is what the chain left, and the record has `abandoned=true`.
- The record's time is End on the app's clock (`WithClock`), and it has no source even when the handler has `AddSource`, since the source would be the same line of bites for every request.

Allocations: 2 with up to five attributes, 3 with a request id, a trace or any attribute beyond five, because a `slog.Record` holds five inline ([ADR 0032](../adr/0032-four-first-party-middleware.md)). It panics when more than one options value is given (`AccessLog: 2 options values were given; give one AccessLogOptions`).

### `AccessLogOptions`

```go
type AccessLogOptions struct {
	// Logger receives the records; the app's own logger (WithLogger) when nil.
	Logger *slog.Logger

	// Level is the level of every record; slog.LevelInfo, the zero value, by default. When the
	// logger is not enabled at this level the Observer does nothing at End, and allocates nothing.
	Level slog.Level
}
```

AccessLogOptions configures `AccessLog`.

## Metrics

### `Metrics`

```go
type Metrics struct{ /* unexported fields */ }
```

Metrics is an Observer that counts requests by method, route pattern and status code and writes them in the Prometheus text exposition format, version 0.0.4 ([ADR 0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md)). Its families:

| family | type | labels |
|---|---|---|
| `http_server_request_duration_seconds` | histogram, from the parsed head to the last byte | method, route, code |
| `http_server_request_body_bytes_total` | counter | method, route, code |
| `http_server_response_body_bytes_total` | counter, after gzip | method, route, code |
| `http_server_abandoned_requests_total` | counter | method, route |
| `http_server_requests_active` | gauge | none |
| `bites_websocket_connections_open` | gauge, sockets open | route |
| `bites_websocket_connections_opened_total` | counter | route |
| `bites_websocket_connections_closed_total` | counter | route, reason |
| `bites_websocket_pongs_missed_total` | counter | route |
| `bites_metrics_dropped_total` | counter, requests and socket routes past `MaxSeries` | none |

`method` is one of the nine standard methods or `OTHER`; `route` is a registered pattern or `""` (a 404, a 405, an answer before the match); `code` is the status sent. A request abandoned before anything was sent is counted only as abandoned. The WebSocket families come from Metrics being a [`SocketObserver`](#socketobserver): `reason` is a [`CloseCause`](./websocket.md#closecause) label, always one of eight, and `route` a registered WebSocket pattern; they cost nothing on the HTTP request path. One Metrics may observe several apps; their counts add up.

### `NewMetrics`

```go
func NewMetrics(opts ...MetricsOptions) *Metrics
```

NewMetrics returns a Metrics with no series. It panics when more than one options value is given (`NewMetrics: 2 options values were given; give one MetricsOptions`), `Buckets` is empty but not nil, a bucket is not positive (`NewMetrics: Buckets[0] is 0s, which is not an upper bound: give positive durations in increasing order`) or not above the one before, or `MaxSeries` is negative.

### `MetricsOptions`

```go
type MetricsOptions struct {
	Buckets   []time.Duration // the histogram's upper bounds, increasing; nil is 5 ms to 10 s
	MaxSeries int             // the most label sets kept; 0 is 10,000
}
```

MetricsOptions configures `NewMetrics`. Past `MaxSeries` a request that would add a label set is counted in `bites_metrics_dropped_total` instead.

### `MetricsContentType`

```go
const MetricsContentType = "text/plain; version=0.0.4; charset=utf-8"
```

MetricsContentType is the Content-Type `ServeMetrics` answers with.

### `Metrics.Start`

```go
func (m *Metrics) Start(ctx context.Context, _ Request) context.Context
```

Start counts the request as active and returns `ctx`.

### `Metrics.End`

```go
func (m *Metrics) End(_ context.Context, req Request, out Outcome)
```

End counts the request in its series. It allocates nothing once the series exists; the first request of a label set copies the map of series once.

### `Metrics.SocketOpened`

```go
func (m *Metrics) SocketOpened(route string)
```

SocketOpened counts the socket in `bites_websocket_connections_open` and `..._opened_total`. It allocates nothing once the route has its series.

### `Metrics.SocketClosed`

```go
func (m *Metrics) SocketClosed(route string, info CloseInfo)
```

SocketClosed takes the socket out of the open gauge and counts it in `bites_websocket_connections_closed_total` under `info.Cause`.

### `Metrics.SocketPongMissed`

```go
func (m *Metrics) SocketPongMissed(route string)
```

SocketPongMissed counts a ping the client did not answer in `bites_websocket_pongs_missed_total`.

### `Metrics.WriteTo`

```go
func (m *Metrics) WriteTo(w io.Writer) (int64, error)
```

WriteTo writes the text format to `w`, the series sorted by method, route and code.

### `App.ServeMetrics`

```go
func (a *App) ServeMetrics(path string, m *Metrics)
```

ServeMetrics answers `GET path` with what `m` counted. Like the API document of `OpenAPI` it is a route: app middleware runs before it, it is observed, and it is not in the API document. To serve it on an internal listener, give `m` to the public app with `WithObserver` and call `ServeMetrics` on a second `App`. It panics when `m` is nil (`ServeMetrics /metrics: the Metrics is nil: make one with NewMetrics and give the same one to WithObserver`), the path does not start with a slash or has a wildcard, query or fragment, a route is already at the path, or `WithHealth` answers it.

The route is passed to the function of an app-level [`UseEach`](./middleware.md#useeach) with `RouteInfo.Builtin` set to `"metrics"`, so a default-deny guard sees it and must exempt it by that name or guard it; `UseEach` after `ServeMetrics` panics, as after a route. The paths of `WithHealth` are not passed, because they are answered before routing.

## Trace context

### `WithTraceContext`

```go
func WithTraceContext(tc TraceContext) Option
```

WithTraceContext makes the app take part in W3C Trace Context ([ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)). Each request's `traceparent` and `tracestate` are read before any Observer's Start and any middleware; a request with one valid `traceparent` gets a `Trace` with the caller's trace and a span id of its own. Nothing is added to the answer. It costs no allocation. Giving it twice makes `New` panic.

### `TraceContext`

```go
type TraceContext struct {
	Begin bool
}
```

TraceContext configures `WithTraceContext`. With `Begin`, a request with no valid `traceparent` begins a trace here: a new trace id, a span id, no parent and the flags 0 (not sampled). Without it such a request has no `Trace`.

### `Trace`

```go
type Trace struct {
	ID     TraceID // the trace, as the caller sent it, or new when it began here
	Span   SpanID  // this request's span, generated for each request
	Parent SpanID  // the caller's span; zero when the trace began here
	Flags  byte    // trace-flags as received; bit 0 is sampled
	State  string  // tracestate as received, several lines joined with ","
}
```

Trace is a request's W3C trace context.

### `TraceID`

```go
type TraceID [16]byte
```

TraceID is the 16-byte id of a trace. The zero value is not valid.

### `TraceID.String`

```go
func (id TraceID) String() string
```

String is the id as 32 lower-case hex digits. It allocates.

### `SpanID`

```go
type SpanID [8]byte
```

SpanID is the 8-byte id of a span. The zero value is not valid.

### `SpanID.String`

```go
func (id SpanID) String() string
```

String is the id as 16 lower-case hex digits. It allocates.

### `Trace.Sampled`

```go
func (t Trace) Sampled() bool
```

Sampled reports whether the sampled flag (0x01) is set.

### `Trace.Child`

```go
func (t Trace) Child() Trace
```

Child returns the trace context of an outgoing call made from this request: the same trace, flags and state, a new span id, and this request's span as its parent.

### `Trace.Traceparent`

```go
func (t Trace) Traceparent() string
```

Traceparent is the header that names this span as the parent of the next one, `00-<ID>-<Span>-<Flags>`, 55 bytes. Send `t.Child().Traceparent()` with an outgoing call, and `t.State` as its `tracestate`.

### `Trace.AppendTraceparent`

```go
func (t Trace) AppendTraceparent(dst []byte) []byte
```

AppendTraceparent appends what `Traceparent` returns to `dst`, without allocating.

### `ParseTraceparent`

```go
func ParseTraceparent(value string) (Trace, bool)
```

ParseTraceparent reads a `traceparent` value: the trace id, the caller's span as `Parent`, and the flags, with `Span` zero. It reports false for a value W3C Trace Context does not allow: a version of `ff`, upper-case hex, a zero trace or parent id, a version 00 value that is not 55 bytes.

### `TraceFrom`

```go
func TraceFrom(ctx context.Context) (Trace, bool)
```

TraceFrom returns the trace of `ctx`: the one `ContextWithTrace` put there, or that of the request `ctx` belongs to, through any context derived from it and after the request has ended. It reports false when there is none.

### `Logger`

```go
func Logger(ctx context.Context) *slog.Logger
```

Logger returns the app's logger (the one `WithLogger` gave, else `slog.Default()` as it was when the app was made) with the attributes of the request `ctx` belongs to: `request_id` when a middleware gave the request an id, `method`, and `route`, the pattern that matched, when one did. `request_id` and `method` are the names of the access log, so a line a handler writes and the access line of the same request are joined on `request_id`. On a Service's life (the `ctx` its `Start` receives, and what derives from it) it returns the app's logger without request attributes. On any other context that is not a request's it returns `slog.Default()`. It works on a context the handler derived and on one kept after the request ended, which still names its own request.

Allocations: it allocates when it is called, the `With` of the attributes (12 on a text handler, measured with a request id, a method and a route), and a request that does not call it pays nothing. Call it once in the handler and keep the logger for its lines.

### `ContextWithTrace`

```go
func ContextWithTrace(ctx context.Context, t Trace) context.Context
```

ContextWithTrace returns a copy of `ctx` that `TraceFrom` answers with `t`, for work that is not a request. It allocates.

### `Request.SetTrace`

```go
func (r Request) SetTrace(t Trace)
```

SetTrace replaces the request's trace, from an Observer's Start or a middleware before it calls `next`: a tracer sets the span it began, so `TraceFrom` and the access log name it. A `Trace` whose `ID` or `Span` is zero removes the request's trace. It allocates the encoding, and does nothing once the handler has returned.

## Health, readiness and the shutdown delay

### `Health`

```go
type Health struct {
	Live  string // 200 for as long as the app serves, Shutdown included
	Ready string // 200 while the app wants traffic; 503 from Shutdown's start or while SetReady(false) holds
}
```

Health names the paths `WithHealth` answers; `/livez` and `/readyz` are the documented choice, and a field left empty is not answered ([ADR 0052](../adr/0052-health-and-readiness-follow-shutdown.md)).

### `WithHealth`

```go
func WithHealth(h Health) Option
```

WithHealth answers the two paths before app middleware, with `ok` as text/plain, or with a 503 problem+json (`the app is shutting down`, `the app is not ready`) on the Ready path. GET and HEAD are answered and another method is a 405. The probes are not in the API document, not observed and not logged, and readiness checks no dependency. It panics for a path that does not start with a slash or has a wildcard, query or fragment, the same path for both, neither path, and when given twice; a route registered at either path panics naming both (`GET /livez: /livez is the Live path of WithHealth (given at main.go:12), which is answered before any route, so this route would never run: give the route or Health.Live another path`).

### `WithShutdownDelay`

```go
func WithShutdownDelay(d time.Duration) Option
```

WithShutdownDelay makes `Shutdown` fail readiness and keep serving, with the listener open, for `d` before it stops accepting, so a balancer that learns of a deploy late stops sending first. The delay is bounded by half of what is left of Shutdown's context, runs on the app's clock, is not restarted by a second Shutdown, and ends at `Close`. The default is 0. It panics for a negative delay (`WithShutdownDelay: the delay is -1s, which is not a delay: give zero for none, or a positive duration such as 5*time.Second`) and when given twice.

### `App.SetReady`

```go
func (a *App) SetReady(ready bool)
```

SetReady says whether the app wants traffic: `SetReady(false)` makes the Ready path answer 503 until `SetReady(true)`. The app starts ready, and once Shutdown has started readiness fails whatever SetReady says.
