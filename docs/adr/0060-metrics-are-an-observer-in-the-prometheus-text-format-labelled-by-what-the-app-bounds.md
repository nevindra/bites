# Metrics are an Observer in the Prometheus text format, labelled by what the app bounds

`NewMetrics(MetricsOptions{Buckets, MaxSeries})` returns a `*Metrics`, an Observer ([ADR 0051](0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)) given with `WithObserver`, and `app.ServeMetrics("/metrics", m)` serves what it counted in the Prometheus text exposition format, version 0.0.4, written by the core with the standard library alone. Decided and built on 2026-11-01 for milestone 3 (`metrics.go`, `metrics_test.go`).

```go
m := bites.NewMetrics()
app := bites.New(bites.WithObserver(bites.AccessLog(), m))
app.ServeMetrics("/metrics", m)
```

**The families.**

| family | type | labels |
|---|---|---|
| `http_server_request_duration_seconds` | histogram, from the parsed head to the last byte | method, route, code |
| `http_server_request_body_bytes_total` | counter, body bytes read | method, route, code |
| `http_server_response_body_bytes_total` | counter, body bytes written after gzip | method, route, code |
| `http_server_abandoned_requests_total` | counter, the client left or stopped reading | method, route |
| `http_server_requests_active` | gauge, Start run and End not yet | none |
| `bites_websocket_connections_open` | gauge, sockets open (revised 2026-11-04) | route |
| `bites_websocket_connections_opened_total` | counter | route |
| `bites_websocket_connections_closed_total` | counter, by why the socket closed | route, reason |
| `bites_websocket_pongs_missed_total` | counter | route |
| `bites_metrics_dropped_total` | counter, requests and WebSocket routes not counted past `MaxSeries` | none |

The four WebSocket families come from `Metrics` being a `SocketObserver` ([ADR 0051](0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)). `route` is a registered WebSocket pattern, `reason` one of the eight `CloseCause` strings, so both are bounded by the app; the routes share `MaxSeries` with the request series. A family with no sample is still announced with its `HELP` and `TYPE` lines, so a scrape of an app with no sockets differs from before by those eight lines.

The buckets are Prometheus' defaults (5 ms to 10 s) unless `Buckets` gives others. A request abandoned before anything was sent has no status, so it is counted in the abandoned counter only; one abandoned during a stream has its status and is counted in both.

**The labels, and why they are bounded.** `route` is `Outcome.Route`: a pattern the app registered, or `""` for a 404, a 405, a path that is not normalised and an answer a middleware gave before the match (a CORS preflight). `method` is one of the nine standard methods or `OTHER`. `code` is the status the app sent. None of the three is text the client chose: a scanner's ten thousand paths land in one series (`route=""`, `code="404"`), a fuzzer's invented methods in `OTHER`, and a client can make a route answer only the statuses the app answers (400, 404, 405, 413, 415, 503, ...). The bound is the app's: (routes + 1) × 10 × the statuses each route answers, a few hundred series for the reference workload. `MaxSeries` (10,000 by default) is the ceiling behind that bound, for an app whose own code makes the label set grow (a proxy route that passes on any upstream status): past it a request is counted in `bites_metrics_dropped_total` and nothing is added. **Status code rather than class:** 401, 403 and 429 are different alerts, a class is a PromQL expression away, and a route answers few codes, so the code costs little cardinality.

**Core, not a subpackage.** `ServeMetrics` registers a route the way `OpenAPI` registers the API document, a route that the document leaves out, which needs the app's route table; its refusals are `RegistrationError`s naming where they were made, which only the root package can make; and the text format, like the OpenAPI document, needs nothing beyond the standard library ([ADR 0006](0006-no-third-party-dependency-in-the-core.md)). That `Metrics` uses no internal of the request (only `Request.Method` and the `Outcome`) is the evidence that the Observer is enough for a module; the OpenTelemetry module will be the module that shows it.

**How it counts.** The series are a map from (method, route, code) to a block of atomic counters, read without a lock through an atomic pointer and copied when a label set is first seen, which happens at most once per series. End finds its series with one map lookup (the key holds the route string the app registered and an interned method, so nothing is built) and makes four or five atomic additions; the bucket is found by a scan of the bounds. A scrape sorts the series and writes them, so two scrapes of the same counts are the same bytes; `_count` and `+Inf` are the same sum of the buckets, so a scrape is consistent with itself.

**What it costs.** End allocates nothing once its series exists: **measured, the typed JSON POST stays at 2** (`TestATypedJSONPOSTCountedByMetricsStillAllocatesTwice`). The first request of a label set copies the map, once. A scrape allocates (the sorted keys, the text) and is not budgeted. A series is about 250 bytes (twelve bucket counters, the sum and three counters, the key and the map entry; arithmetic, not measured), so the 10,000-series ceiling is about 2.5 MB. Memory per idle connection: none. Under load the counters of a hot series are atomics shared by every core, which costs throughput under contention; throughput and p99 are not measured yet and wait for the serial run of wave 2.

**Refusals** (rows of `refusal_test.go`): `NewMetrics` with two options values; an empty `Buckets` (nil is the default); a bucket that is not positive; a bucket not above the one before; a negative `MaxSeries`; `ServeMetrics` with a nil `Metrics`; a path that does not start with a slash or has a wildcard, query or fragment; a path a route already has (the router's conflict) or one `WithHealth` answers ([ADR 0052](0052-health-and-readiness-follow-shutdown.md)). **What cannot be caught before the first request:** a `Metrics` served by `ServeMetrics` and never given to `WithObserver` serves zeros; an app middleware that refuses the scrape (an auth middleware) is the application's choice.

**Scenarios it was put against.**

- _The reference workload_ ([ADR 0002](0002-the-reference-workload.md)): the JSON routes are counted by pattern; an SSE feed is counted when it ends, with the bytes it sent and its whole life as its duration, so long feeds land in `+Inf`; a WebSocket is counted at its 101, and its messages are not requests; Postgres errors show as `code="500"` on the routes that query it. Prometheus scraping every 15 s reads a few hundred series.
- _A hostile client_ that probes ten thousand paths and invents methods: two series (`TestA404AnyPathAndAnUnknownMethodLandInOneSeriesWhateverAHostileClientSends`); it cannot add a label value, so it cannot grow memory.
- _A client that leaves_: the abandoned counter, not the histogram (`TestAnAbandonedRequestIsCountedAsAbandonedAndNotInTheHistogram`).
- _An app that serves metrics on an internal listener_: one `Metrics` observes the public app and a second `App` on another port serves it with `ServeMetrics`; their series add up if both are observed by it.
- _MaxSeries reached_: `TestMaxSeriesBoundsTheSeriesAndCountsWhatItDropped`.

**What it fails at.** The family and label names are fixed: no namespace, no constant labels, no way to add an application's own metrics to the same registry, which an app that wants business metrics does with another library on another path. No process or Go runtime metrics (`go_*`, `process_*`). No OpenMetrics format, so no exemplars linking a bucket to a trace ([ADR 0059](0059-the-core-carries-w3c-trace-context-and-records-no-span.md)). `route=""` reads as an absent label in PromQL. A long stream's duration says little beyond `+Inf`. The scrape route is behind app middleware like any route, so an auth middleware must let Prometheus through. The `Accept` header is not read: the answer is always the text format.

**What was rejected.** The raw path as a label: a client would choose the label values and grow memory without end. A status class (`2xx`) instead of the code: it loses 401 against 403 against 429. A summary with quantiles: quantiles cannot be added across instances. `sync.Map` or a map behind a read lock: boxing the key into an interface allocates on every request, and a read lock is a shared counter every core writes. `prometheus/client_golang`: a third-party dependency in the core ([ADR 0006](0006-no-third-party-dependency-in-the-core.md)). A subpackage: its refusals could not be `RegistrationError`s and its route could not be one the API document leaves out, for no gain. Counting abandoned requests with no status under `code="0"`: a code that is not a status.
