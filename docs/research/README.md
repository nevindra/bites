# Research behind the first decisions

Taken on 2026-10-30, Go 1.27.2 and 1.27.3, on an AMD Ryzen 7 9700X (8 cores, 16 threads, 30 GB). Every claim in a report is tagged with where it came from: `[code]` read at a release tag, `[probe]` a program run against a released version, `[issue]`, `[docs]`, `[release-notes]` or `[bench]`. A claim with none of those is not in here.

**The name.** The framework was called nilo-go until 2026-11-04 ([ADR 0001](../adr/0001-independent-of-nilo.md)). The reports, scripts and recorded output here keep the name they were written with, since each was run at its own commit; only the Go code and `go.mod` of a probe that imports the framework were moved to `github.com/nevindra/bites`, keeping the old package names as import aliases, so the probes still build.

| folder | what it covers |
|---|---|
| [`engines/`](engines/report.md) | Idle memory per connection, plain throughput, 1,000 blocking handlers, and allocations per request, for net/http, fasthttp, fib, nbio, Hertz and two hand-written goroutine servers. The programs and raw JSON are kept so the run can be repeated. |
| [`fiber/`](fiber/report.md) | Fiber v3.6.1, fasthttp v1.77.0, Hertz v0.10.6. |
| [`gin-echo/`](gin-echo/report.md) | Gin v1.12.0, Echo v5.4.4. |
| [`std-huma/`](std-huma/report.md) | net/http and ServeMux in Go 1.27, encoding/json/v2, chi v5.5.0, Huma v2.40.0, Fuego v0.21.0, with an in-process benchmark of the three. |
| [`rust/`](rust/report.md) | Axum 0.8.9, Actix Web 4.18.1, Poem 3.1.12 with poem-openapi, utoipa and aide. |
| [`ws-idle/`](ws-idle/report.md) | Memory per idle WebSocket for a hand-written goroutine server, gws, gorilla, coder/websocket, fib and nbio, and a broadcast to 20,000 sockets. |
| [`pattern-dialect/`](pattern-dialect/report.md) | Which RE2 constructs ECMA-262 refuses or reads differently, compiled in Go 1.27 and node 26, for the subset `Pattern` allows (ADR 0021). |
| [`framework-gap/`](framework-gap/report.md) | Every feature of Fiber v3, Gin 1.12, Echo v4 and v5, chi v5 with net/http, Huma v2.40, Hertz and Fuego, read from their documentation with a URL each, as the evidence for the features milestone 4 adds. Taken 2026-11-03. |
| [`stack-handoff/`](stack-handoff/report.md) | The stack a request leaves on an idle connection: a move to a fresh goroutine after every request against one after 100 ms idle, on idle memory, throughput, p99 and CPU a request. Taken 2026-10-31. |
| [`facts/`](facts/report.md) | Probes of what the design relies on: encoding/json/v2's options, errors and allocations, context allocations, escape of a handler's input, `unsafe.String` liveness, `uuid` in the standard library, and ServeMux's exact behaviour. |
| [`fib.md`](fib.md) | fib at `7dcb41d`, read from its README and hack guide. |
| [`defaults-on-route/`](defaults-on-route/report.md) | A prototype of defaults stated on the route (run on a zero and a poisoned Input), what it handles, where it fails open, and why it is not built (ADR 0041). |
| [`middleware-p99/`](middleware-p99/report.md) | Why the p99 of a route rose past 10% behind any middleware: the collector on and off, its cycles a second, the 80 B of slab room every chain reserved, and the fix measured against the commit before it; Providers, constraint types and `RequestID` alone. Taken 2026-11-05. |
| [`stack-shrink/`](stack-shrink/report.md) | The stack the collector halves under a busy connection's wait: stack bytes under load with and without `gcshrinkstackoff`, the request path's deepest frames, the hot wait under padding measured against the tree without it, and the heap goal it raises. Taken 2026-11-05. |

The engine numbers have known weaknesses, stated in their report: the hand-written servers answer GET only with no body and no pipelining, so their throughput is a ceiling; runs above about 1.1M req/s may be client-bound; and two other agents were compiling on the same machine during part of the run, on cores the server was not pinned to.
