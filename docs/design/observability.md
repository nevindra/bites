# Observability

**How a running app is watched: the Observer told when a request starts and when it has really ended, the access log, Prometheus metrics and W3C trace context built on it, and the health and readiness answers that follow Shutdown.**

**Guide:** [observability](../guide/observability.md), [deploying](../guide/deploying.md) · **Reference:** [observability](../reference/observability.md), [`otel`](../reference/otel.md)

## Overview

Everything that watches a request is an Observer: an interface with Start, run once the head is parsed and before app middleware, and End, run once after the last byte was written or the connection was given up. A middleware cannot do this job, because the chain returns before a stream's body, before gzip and before a `Problem` is rendered, and does not return at all after a panic. The core ships three things on it and leaves a fourth, OpenTelemetry, to a module, `modules/otel`.

```text
head parsed
  WithHealth?  /livez, /readyz answered here: not observed, not traced, not logged
  WithTraceContext: traceparent read, a span id given, stored in the request's slab
  Observer.Start (in order)       -> the context the chain gets
    app middleware, route, Providers, handler
  engine writes the answer        (gzip, a stream's chunks, the 500 after a panic, a 101)
  Observer.End (in reverse)       <- Outcome{Route, Status, Read, Written, Duration, Streamed, Upgraded, Panicked, Abandoned, Err, Panic, Stack}

AccessLog()   one slog record at End
NewMetrics()  counters and a histogram by method, route pattern and code; ServeMetrics writes them
otel.Tracing  (modules/otel) a server span from Start to End, named to the core with Request.SetTrace
```

The engine's part is one hook: the framework hands the request's chain state to the Exchange before anything can panic, the engine counts the bytes read and written and calls End from the place each request ends. Health and readiness are answered before all of it, and readiness turns to 503 the moment Shutdown starts, which with a shutdown delay is before the listener closes.

## Rules

1. **An Observer is told when a request starts and when it has really ended, and only watches.** `WithObserver(...Observer)`, given once; Start in the order given and End in reverse, each End with the context its own Start returned; a panic in Start is the request's 500, and a panic in End is logged and closes the connection. A head refused before parsing, an `OPTIONS *` and the health probes are not observed. With no Observer the cost is a length check; with Observers it is 0 allocations for the hook, measured. [ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)
2. **End is told the error and the panic, for a tracker.** `Outcome.Err` is the error the application returned that the framework answered (any status, and a stream's error after its first byte), unwrapped, so `errors.As` works; the framework's own 400s and 404s, a handled error a middleware answered with a success, and a client that left are not in it. `Panic` and `Stack` are the recovered value and `debug.Stack()` from the recover. Nothing on a request that neither fails nor panics, and no allocation on the failure. [ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)
3. **`AccessLog` is an Observer, and its record is written at End.** So a stream's record has its real status and size, a panic leaves a record with `panicked=true`, `size` is after gzip, and a `Problem` a middleware returned has its size. The record is written after the client has its answer. A handler's lines join it through `Logger(ctx)` and `RequestIDFrom(ctx)`, which carry `request_id`, `method` and `route`; `request_id` joins them to the access line. [ADR 0032](../adr/0032-four-first-party-middleware.md), [ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)
4. **The core carries W3C trace context and records no span.** `WithTraceContext` reads `traceparent` and `tracestate` before any Observer or middleware, gives each request a span id, and `TraceFrom(ctx)` returns it everywhere, a context kept after the request included; `Child()` is the context of an outgoing call. Nothing is answered; `AccessLog` writes `trace_id` and `span_id`. A tracer's Observer replaces the span with `Request.SetTrace`. [ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)
5. **Metrics are an Observer whose labels the app bounds.** `NewMetrics()` counts by method (nine or `OTHER`), route pattern (`""` for a 404 and a 405) and status code, with a duration histogram and byte counters, and `ServeMetrics` writes the Prometheus text format with no dependency. A client cannot add a label value; `MaxSeries` is the ceiling behind that. [ADR 0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md)
6. **Health and readiness are answered by the app before app middleware, and readiness fails when Shutdown starts.** `WithHealth(Health{Live, Ready})`; liveness stays 200 through Shutdown, readiness is 503 from Shutdown's start or while `SetReady(false)` holds, and checks no dependency. `WithShutdownDelay(d)` keeps serving with readiness failing for `d`, bounded by half of what is left of Shutdown's context, before the listener closes. [ADR 0052](../adr/0052-health-and-readiness-follow-shutdown.md), [ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)

7. **OpenTelemetry is a module whose Observer records the server span and names it to the core.** `otel.Tracing` takes the caller's trace from `TraceFrom` or the header, starts a server span in the context the handler gets, calls `SetTrace` so `TraceFrom` and `AccessLog` name that span, and at End names it `METHOD /route`, sets the HTTP semantic-convention attributes, marks a 5xx and a panic as errors and an abandoned request as an event. It costs the module 7 allocations a request with a no-op provider and 16 sampled, none of them the core's. [ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)

8. **A socket's life is told to an Observer that implements `SocketObserver`.** Open, close with a fixed cause (`client`, `server`, `handler_error`, `queue_full`, `pong_timeout`, `shutdown`, `client_fault`, `lost`) and a missed pong, by route, from the engine to the framework with no new field on a socket and nothing on the HTTP request path. `Metrics` records the gauge and the counters; `modules/otel` does not ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md), [ADR 0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md), [ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)).

## Decisions

| ADR | What it decides |
|---|---|
| [0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md) | the Observer: a request watched at its start and at its real end, the base of metrics, tracing and the access log |
| [0032](../adr/0032-four-first-party-middleware.md) | the access log's record, written by an Observer |
| [0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md) | W3C trace context read, carried in the request's slab, handed to everything through `TraceFrom`; no span recorded |
| [0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md) | metrics in the Prometheus text format, by method, route pattern and code, in the core |
| [0052](../adr/0052-health-and-readiness-follow-shutdown.md) | liveness and readiness answered by the app, readiness failing when Shutdown starts, the shutdown delay |
| [0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md) | `modules/otel`: the server span of OpenTelemetry v1.47.0 as an Observer, named to the core through `SetTrace` |

## Open questions

- **Throughput and p99 of the Observer, `AccessLog` as an Observer, `Metrics` and `WithTraceContext`** are not measured yet; they wait for the serial run of wave 2 ([ADR 0003](../adr/0003-the-trade-budget.md)).
- **End runs after the client has its answer**, so a test calls `bitetest.Server.WaitEnded` before it reads what an Observer recorded, which waits on the in-memory connections and not on a sleep ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md), [ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).
- **An observer of WebSockets.** A socket is observed at its 101 and its messages are not requests; whether its life and its messages get an Observer of their own, and whether a socket's context carries the handshake's trace, is a later decision ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md), [ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)).
- **Restarting a trace at an edge that faces the internet**, so clients cannot choose the trace id and the sampled flag, is not an option yet ([ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)).
- **Metrics have fixed names**: no namespace, no constant labels, no application metrics in the same registry, no runtime metrics and no OpenMetrics exemplars ([ADR 0060](../adr/0060-metrics-are-an-observer-in-the-prometheus-text-format-labelled-by-what-the-app-bounds.md)).
- **A metric of malformed traffic** (heads refused before parsing) is taken when a deployment asks for it ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).
- **The OpenTelemetry span does not carry the handler's error**, only its status, since an Observer is not given the error; its timestamps are the SDK's clock, and there are no OpenTelemetry metrics ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)).
