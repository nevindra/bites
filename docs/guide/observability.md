# Observability

**To watch a running app you give it Observers: the access log, Prometheus metrics, or your own, each told when a request starts and when it has really ended, and you turn on W3C trace context to follow a request across services.**

**Reference:** [Observability](../reference/observability.md) · **Design:** [Observability](../design/observability.md)

Everything here is given to the app when it is made. Health and readiness probes, which an orchestrator needs, are on [the deploying page](./deploying.md#health-readiness-and-the-shutdown-delay).

## Watching every request with an Observer

**An [`Observer`](../reference/observability.md#observer) has two methods: Start, run once the head is parsed and before app middleware, and End, run once after the last byte of the answer was written.** It watches and never answers ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).

<!-- compiles: decls -->
```go
// slowLog warns about every request that took longer than a limit.
type slowLog struct {
	limit time.Duration
	log   *slog.Logger
}

func (s slowLog) Start(ctx context.Context, req bites.Request) context.Context { return ctx }

func (s slowLog) End(ctx context.Context, req bites.Request, out bites.Outcome) {
	if out.Duration > s.limit {
		s.log.Warn("slow request", "method", req.Method(), "route", out.Route, "status", out.Status, "duration", out.Duration)
	}
}

func newWatched() *bites.App {
	return bites.New(bites.WithObserver(bites.AccessLog(), slowLog{limit: time.Second, log: slog.Default()}))
}
```

End is given an [`Outcome`](../reference/observability.md#outcome): the route pattern that matched (`/users/{id}`, or `""` for a 404 and a 405), the status that was sent, the body bytes read and written (after gzip), the duration on the app's clock, and whether the request streamed, upgraded to a WebSocket, panicked or was abandoned by its client.

**Use an Observer to watch and a middleware to act.** A middleware sees the chain return, and the chain returns before four things an Observer sees: a stream's body, which is written after every middleware has returned; a panic, after which the chain does not return; the bytes gzip wrote; and the body of a `Problem` a middleware returned, rendered only when it is written. A middleware can refuse a request or add a header; an Observer cannot.

- `WithObserver` takes every Observer at once. Start runs in the order given and End in reverse, so the first one wraps the others. Each End gets the context its own Start returned, which is where a tracer keeps its span.
- A panic in Start is the request's panic: a 500, logged with its stack. A panic in End is logged and closes the connection, and the other Observers' End still run.
- End runs on the connection's goroutine after the client has its answer: keep it short, and in a [test](./testing.md), call [`srv.WaitEnded()`](../reference/bitetest.md#bitetestserverwaitended) before reading what it recorded, rather than reading it as soon as the request returns.
- A request the engine refuses before it could parse its head, and the health probes, are not observed.

An Observer that allocates nothing costs nothing: the typed JSON request stays at 2 allocations with any number of them ([ADR 0003](../adr/0003-the-trade-budget.md)).

## Reporting failures to a tracker

**An Observer is where a failure is sent to Sentry or a tracker like it**, because it sees the three things a middleware does not: the error the handler returned after the middleware, a panic, and a stream cut after its first byte. [`Outcome`](../reference/observability.md#outcome) carries them as `Err`, `Panic` and `Stack` ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).

<!-- compiles: decls -->
```go
// tracker is what the app needs of Sentry, Bugsnag or the like; the adapter is one method.
type tracker interface {
	report(err error, stack []byte)
}

// toTracker reports the errors worth waking someone for.
type toTracker struct{ t tracker }

func (toTracker) Start(ctx context.Context, req bites.Request) context.Context { return ctx }

func (o toTracker) End(ctx context.Context, req bites.Request, out bites.Outcome) {
	switch {
	case out.Panicked:
		err, ok := out.Panic.(error)
		if !ok {
			err = fmt.Errorf("panic: %v", out.Panic)
		}
		o.t.report(err, out.Stack)
	case out.Err != nil && out.Status >= 500:
		o.t.report(out.Err, nil)
	}
}

func newReported(t tracker) *bites.App {
	return bites.New(bites.WithObserver(bites.AccessLog(), toTracker{t: t}))
}
```

- `Err` is the value your code returned, so `errors.As(out.Err, &target)` finds your own types in it, and a `HasStatus` 404 is in it too: filter on `out.Status` as above to leave client mistakes out.
- The framework's own answers are not in `Err`: a 400 for a body that does not decode, a 404 and a 405 carry nothing a tracker needs. A client that left is not an error either.
- A middleware that answers an error with a status below 400 has handled it, and `Err` is nil.
- A stream that fails after its first byte has a status that was already sent: `Streamed` is true and `Err` is the function's error.
- `Stack` is the panicking goroutine's stack, a fresh slice that the Observers of the request share: copy it before changing it. A `panic(nil)` arrives as a `*runtime.PanicNilError`.
- A panic in a WebSocket's `OnMessage` or `OnClose` happens after the upgrade was observed and is not in any Outcome.

## Logging each request

**[`AccessLog()`](../reference/observability.md#accesslog) is the Observer that writes one `slog` record per request**, with its real status and size, after a stream has ended and after a panic too. Its attributes and options are on [the first-party page](./first-party-middleware.md#logging-each-request), which also shows how a handler's own lines carry `request_id`, `method` and `route`, and `request_id` joins them to the access line with [`Logger(ctx)`](../reference/observability.md#logger).

## Counting requests for Prometheus

**[`NewMetrics()`](../reference/observability.md#newmetrics) is an Observer that counts requests by method, route pattern and status code, and [`ServeMetrics`](../reference/observability.md#appservemetrics) writes the counts in the Prometheus text format** ([ADR 0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md)). Nothing outside the standard library is involved.

<!-- compiles: decls -->
```go
func newCounted() *bites.App {
	m := bites.NewMetrics()
	app := bites.New(bites.WithObserver(bites.AccessLog(), m))
	app.ServeMetrics("/metrics", m)
	app.Get("/ping", func(ctx context.Context, in struct{}) (bites.Text, error) { return bites.Text{Body: "pong"}, nil })
	return app
}
```

```text
# TYPE http_server_request_duration_seconds histogram
http_server_request_duration_seconds_bucket{method="GET",route="/ping",code="200",le="0.005"} 41
...
http_server_request_duration_seconds_count{method="GET",route="/ping",code="200"} 41
http_server_request_duration_seconds_count{method="GET",route="",code="404"} 7
http_server_response_body_bytes_total{method="GET",route="/ping",code="200"} 164
http_server_abandoned_requests_total{method="GET",route="/ping"} 0
http_server_requests_active 1
```

**The labels cannot be chosen by a client.** `route` is the pattern you registered, so `/users/1` and `/users/2` are one series, and every 404 a scanner sends is in `route=""`; `method` is one of the nine standard methods or `OTHER`; `code` is a status your app answered. The number of series is your routes times the statuses each answers, and [`MetricsOptions.MaxSeries`](../reference/observability.md#metricsoptions) (10,000 by default) is the ceiling behind that. `Buckets` changes the histogram's bounds, which are Prometheus' defaults, 5 ms to 10 s.

The metrics route is a route of the app: app middleware runs before it, so put your auth check there or serve it from a second `App` on an internal port, since one `Metrics` given to the public app can be served by another. It is observed like any route and is not in the [API document](./openapi.md). A request counts no allocation once its label set has a series.

## Watching WebSockets

**A WebSocket is not a request after its 101, so the Observer's `End` ends there; an Observer that also implements [`SocketObserver`](../reference/observability.md#socketobserver) is told of the socket's life** with no second registration, and `Metrics` is one ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)). The first numbers a chat watches are in the same scrape as the request counts:

```text
bites_websocket_connections_open{route="/rooms/{id}"} 1840
bites_websocket_connections_opened_total{route="/rooms/{id}"} 25311
bites_websocket_connections_closed_total{route="/rooms/{id}",reason="client"} 23102
bites_websocket_connections_closed_total{route="/rooms/{id}",reason="queue_full"} 9
bites_websocket_connections_closed_total{route="/rooms/{id}",reason="pong_timeout"} 360
bites_websocket_pongs_missed_total{route="/rooms/{id}"} 1112
```

**`reason` says why the socket ended, and is one of eight values:** `client`, `server` (you called `Socket.Close` or returned a `CloseError`), `handler_error` (`OnMessage` returned an error or panicked), `queue_full` (a send overflowed the socket's queue or the app's budget, `ErrSendQueueFull`: a reader too slow), `pong_timeout`, `shutdown`, `client_fault` (the client broke the protocol or a limit on what it may send) and `lost` (the connection ended with no close handshake). `queue_full` rising while the open gauge falls says your readers are slow, not that clients left. The same value is `CloseInfo.Cause` in `OnClose`, for a log line without an Observer.

To record sockets elsewhere, implement the three methods next to `Start` and `End`; they run on the connection's or the heartbeat's goroutine and must not block, a panic in one is logged and the socket is unaffected, and an app with no such Observer pays nothing per socket:

<!-- compiles: decls -->
```go
type socketLog struct{ bites.Observer }

func (socketLog) SocketOpened(route string)                      {}
func (socketLog) SocketClosed(route string, info bites.CloseInfo) { _ = info.Cause }
func (socketLog) SocketPongMissed(route string)                  {}
```

`modules/otel` records the request span only; it does not record sockets ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)).

## Following a request across services

**[`WithTraceContext`](../reference/observability.md#withtracecontext) reads the W3C `traceparent` and `tracestate` a caller sent, gives the request a span id of its own, and [`TraceFrom(ctx)`](../reference/observability.md#tracefrom) returns it to every Provider, middleware, handler and Observer** ([ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)). The access log then carries `trace_id` and `span_id`, so one search finds this service's lines among those of the ingress, the database and the services it calls.

<!-- compiles: decls -->
```go
type Order struct {
	ID string
}

// placeOrder calls another service, passing the trace on.
func placeOrder(ctx context.Context, in struct{ Body Order }) (bites.Text, error) {
	header := map[string]string{}
	if t, ok := bites.TraceFrom(ctx); ok {
		call := t.Child() // the outgoing call is a span of its own, under this request's
		header["traceparent"] = call.Traceparent()
		if t.State != "" {
			header["tracestate"] = t.State
		}
	}
	_ = header // set these on the request to the next service
	return bites.Text{Body: "ok"}, nil
}

func newTraced() *bites.App {
	app := bites.New(
		bites.WithTraceContext(bites.TraceContext{Begin: true}),
		bites.WithObserver(bites.AccessLog()),
	)
	app.Post("/orders", placeOrder)
	return app
}
```

- A request with one valid `traceparent` gets the caller's trace id, the caller's span as `Parent`, its flags and its `tracestate`. One with none, or an invalid one, has no trace, unless `TraceContext.Begin` is set: then it begins a trace with a new id and the sampled flag clear.
- The trace is the request's own memory: a goroutine that keeps the context after the request still finds it, and so does a context derived from it.
- Nothing is added to the answer. The core records no span: recording is the job of a tracer, which the [OpenTelemetry module](#opentelemetry) does as an Observer, setting its own span with [`Request.SetTrace`](../reference/observability.md#requestsettrace) so the log lines name the span it records.
- For work that is not a request, a job taken from a queue with a traceparent beside it, [`ParseTraceparent`](../reference/observability.md#parsetraceparent) reads the header and [`ContextWithTrace`](../reference/observability.md#contextwithtrace) puts the trace in a context.

Reading the trace costs no allocation.

## OpenTelemetry

The [`otel` module](../reference/otel.md) records one OpenTelemetry server span a request ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)). Give your SDK's tracer provider to `otel.Tracing`, and give the Observer before `AccessLog`:

```go
import "github.com/nevindra/bites/modules/otel"

tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
app := bites.New(
	bites.WithTraceContext(bites.TraceContext{}),
	bites.WithObserver(otel.Tracing(otel.Config{TracerProvider: tp}), bites.AccessLog()),
)
```

- **The span is the caller's child.** The `traceparent` the caller sent is its remote parent; without one the span begins a trace and your sampler decides. `WithTraceContext` is optional: without it the module reads the header itself.
- **Your spans are its children.** The context the handler gets holds the span, so a database driver that traces with `ctx` makes child spans.
- **One id everywhere.** The module gives the request the span's ids, so [`TraceFrom`](../reference/observability.md#tracefrom), an outgoing call's `TraceFrom(ctx).Child().Traceparent()` and the access log's `trace_id` and `span_id` name the span it records.
- **What is recorded.** The span is named `GET /users/{id}` from the route pattern, with the method, route, status and body sizes of the HTTP semantic conventions; a 5xx and a panic are errors, a 4xx is not, and a client that left is an `abandoned` event.

It costs 7 allocations a request with a no-op provider and 16 when sampled, the module's own; the core's two are unchanged.

