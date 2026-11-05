# otel

**The module whose Observer records an OpenTelemetry server span for each request, child of the caller's span, and names it to the core so `TraceFrom` and the access log see the same ids.**

**Guide:** [observability](../guide/observability.md#opentelemetry) · **Design:** [observability](../design/observability.md)

`github.com/nevindra/bites/modules/otel` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of OpenTelemetry. It depends on `go.opentelemetry.io/otel` and `go.opentelemetry.io/otel/trace` at v1.47.0; the SDK that samples and exports is the application's. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/otel && go test ./...`. The decision is [ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md).

```go
app := bites.New(bites.WithObserver(otel.Tracing(otel.Config{TracerProvider: tp}), bites.AccessLog()))
```

### `otel.Tracing`

```go
func Tracing(cfg Config) bites.Observer
```

Tracing returns the [Observer](./observability.md#observer) that records one server span a request. Start takes the caller's trace from [`bites.TraceFrom`](./observability.md#tracefrom) when the app has `WithTraceContext`, and from the `traceparent` and `tracestate` headers otherwise; it starts a span of kind server whose remote parent is the caller's span, or the root of a new trace, with `http.request.method` (`_OTHER` and `http.request.method_original` for a method outside the nine the conventions name), `url.path`, `url.scheme`, `client.address`, `server.address` and `user_agent.original`; it returns the context holding the span, so the handler's spans are its children; and it calls [`Request.SetTrace`](./observability.md#requestsettrace) with the span's ids, so `TraceFrom`, an outgoing call's `traceparent` and `AccessLog`'s `trace_id` and `span_id` name it. With a no-op provider no span is recorded and the request keeps the trace the core read. End names the span `METHOD /route` (the bare method for a 404 or 405), sets `http.route`, `http.response.status_code`, `http.request.body.size` and `http.response.body.size`, sets the status to Error with `error.type` for a 5xx (the code) and a panic (`panic`), records the application's error of a 5xx with `RecordError` (its text as the status description), adds an `exception` event with the panic's value and the stack of the goroutine that panicked, records a stream cut by its function's error with `error.type` `stream`, adds `bites.abandoned` and an event `abandoned` for a client that left, without an error status, and `bites.streamed` or `bites.upgraded`, then ends the span. Give it before `AccessLog`. It refuses nothing.

Allocations, the module's and not the core's: 7 a request more than without it with the no-op provider, 11 with the SDK sampling nothing, 16 with the SDK sampling and recording ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md) has the run).

### `otel.Config`

```go
type Config struct {
	TracerProvider trace.TracerProvider
}
```

`TracerProvider` makes the tracer; `otel.GetTracerProvider()` of OpenTelemetry, the global one, when nil.

### `otel.ScopeName`

```go
const ScopeName = "github.com/nevindra/bites/modules/otel"
```

ScopeName is the instrumentation scope of the tracer the module asks the provider for.
