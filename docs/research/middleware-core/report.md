# The middleware core (milestone 2, step 5): what it costs

Taken on 2026-10-31, Go 1.27.3, AMD Ryzen 7 9700X, kernel 7.2.8-9-omarchy. The code measured is `a66baa8` plus the uncommitted step 5 tree (the "after"), against `git archive a66baa8` in `~/.cache/nilo-go-step5-before` (the "before"), both built in the same session with the same flags.

## What was run

`run.sh` runs `bench/run.sh` with `SERVERS=nilogo EXPS="get post allocs" ROUNDS=1` three times, the four variants in turn each time (interleaved): `before`, `after` (no middleware, the route of milestone 1), `mw1` and `mw3` (the same server started with `NILOGO_MW=1` or `3`: that many route middleware on both routes, each reading the `Authorization` header and the path and adding one response header, `bench/servers/nilogo/main.go`). Server on cores 2,3,10,11 and client on 4-7,12-15, 256 connections, 3 s warm-up and 10 s measured, loopback. `summary.py` prints the tables below; the raw runs are `raw-final.jsonl` and `raw-first.jsonl`.

Two complete sessions were taken. `summary-first.txt` is the first, before two small changes (the head copy no longer trims values the parser trimmed already, and the engine records the Content-Type it was given instead of scanning the head for it at every `Next` boundary); `summary-final.txt` is the tree as delivered.

## Allocations: the hard axis holds

| | allocs/request | bytes/request |
|---|---|---|
| before | 2.006 to 2.007 | 321 |
| after, no middleware | 2.005 to 2.006 | 321 |
| one route middleware | 2.008 to 2.009 | 450 |
| three route middleware | 2.006 to 2.007 | 449 to 450 |

The tests of `budget_test.go` are exact: 2 for one and for three route middleware, one app middleware, app plus group plus route middleware, a request ID set and a header read, and a 380 byte `Authorization` read by both the Input and a middleware (the case that made a lazy per-lookup copy overflow into a third allocation, `decisions-middleware` R10). A 404 behind app middleware is at most 2. The 129 extra bytes are the slab of twice the head.

## Throughput: the soft axis, final session (mean of three, spread under 1% within a variant)

| GET /users/7 | req/s | CPU ns/req | p99 |
|---|---|---|---|
| before | 894.1k | 3,460 | 739 us |
| after, no middleware | 883.3k (-1.2%) | 3,515 | 748 us |
| one route middleware | 824.0k (-7.8% from before, -6.7% from after) | 3,851 | 893 us (+21%) |
| three route middleware | 788.3k (-11.8%, -10.8%) | 4,057 | 979 us (+32%) |

| POST /users/7 | req/s | CPU ns/req | p99 |
|---|---|---|---|
| before | 743.1k | 4,373 | 895 us |
| after, no middleware | 732.0k (-1.5%) | 4,447 | 911 us |
| one route middleware | 669.3k (-9.9%, -8.6%) | 4,950 | 1,162 us (+30%) |
| three route middleware | 637.4k (-14.2%, -12.9%) | 5,258 | 1,266 us (+41%) |

Reading it:

- **A route with no middleware** moved from +0.5% (first session) to -1.2% and -1.5% (final session) against the before: the same code path in both sessions, so that is the size of the difference between two binaries (code layout), not a cost; within one binary the three rounds agree to 0.2%. Call it unchanged within 2%.
- **One middleware costs about 350 ns of CPU a request** (GET 3,515 to 3,851, POST 4,447 to 4,950): about 80 ns is the middleware's own work in this harness (a header scan and an added header), the rest is the chain: a pooled state, the head copy into the slab (about 85 ns in the profile, before the trim was dropped), a 272 byte `Response` copied across each boundary, and the extra 129 bytes of slab and one more header line written. **Each further layer costs about 100 to 125 ns.**
- **p99 rises 21% to 41%**, past the 10% a soft axis may give ([ADR 0003](../../adr/0003-the-trade-budget.md)). This is recorded in the row of ADR 0003 and is the first thing the optimisation pass (step 17) looks at; this change does not claim it is within the budget.

## Why `Next` is a struct (`next_test.go`)

A model of the chain with the same pooled state and three layers: `Next` as a struct with a `Call` method 13 ns and 0 allocations; `Next` as a `func` made for each layer 25 ns and 2 allocations (96 B). With one layer the compiler inlined the model and the function shape also allocated nothing, which a chain of unknown functions held in a slice cannot rely on. Run: `go test -run XXX -bench Next -count 3` in this folder.

## In-process cost (not the benchmark of record)

`BenchmarkTypedPOSTBehindMiddleware` in `budget_test.go`, over the in-memory connection, `-benchtime 100000x -count 5`: 0 middleware 1,655 to 1,703 ns/op, 1 middleware 1,885 to 2,055, 3 middleware 2,164 to 2,194; 338 B, 451 B and 451 B an operation, 2 allocations in every case. The goroutine handoff in the in-memory connection dominates and adds noise, so the loopback table above is the one to quote.

## Not measured

TLS, a WebSocket upgrade behind middleware (step 12), more than six headers added by middleware (spills to the heap by design), the index-or-scan choice for `Request.Header` (ADR 0022 still waits for the count real middleware makes).

## Second pass: where the cost went, and what was changed (2026-10-31)

The first version cost a route 350 ns of CPU for one middleware and 100 to 125 ns for each more, and raised p99 by 21% to 41% (tables above). The coordinator asked for the chain to be brought under the 10% of [ADR 0003](../../adr/0003-the-trade-budget.md) for three middleware before step 7 builds on it.

### Attribution (first version)

Taken with `BenchmarkTypedPOSTBehindMiddleware` at `-cpu 1` (one P, so the goroutine handoff of the in-memory connection stops adding noise: the spread is 2%, against 10% with all cores) and pprof of the 1-layer pass-through, 4,000,000 requests. ns over the route with no middleware (1,550 ns):

| | first version | what it was made of |
|---|---|---|
| 1 pass-through middleware | +150 | the Request view 105 ns in the profile, of which about 50 is the slab's `make` that moved here from before `Serve` (so about 55 new): two passes over the headers with a lower-casing copy and two `TrimSpace`s each; the pooled state 36 (`sync.Pool` Get and Put 18 each); `release` 20; copying `router.Params` (648 B) into the state 12; a 272 B `Response` made and copied across each boundary 45 (measured by shrinking the inline pairs from 6 to 1, Response 112 B: 1,660 to 1,616 ns); the rest is the calls, `finish` and `capture` |
| each further pass-through layer | +25 | the `Request` (80 B) and `Next` passed in, a `Response` returned twice |
| a middleware that reads one header and adds one | +80 per layer | `Request.Header` for an absent header 36 ns (a byte loop over every line); `AddHeader` and the header line written 30 to 40 (the engine's `appendClean` appended byte by byte) |
| GC | p99 | the slab of twice the head: 129 B more a request. With `GOGC=400` the p99 rise of one middleware fell from +21% to +6% (GET 712 to 754 us), so the tail is the collector's, not the chain's |

Nothing in the list is the generation checks (a load and a compare), the interface call of a `Next`, or scheduling.

### Changes

1. **The head is copied as received, in one `memmove`**, and the path, the query and the header block are substrings of that copy (`Request.Header` scans the lines case-insensitively without allocating). The view cost 105 ns; it is about 40 now, the slab's `make` included.
2. **The Input reads out of that copy.** `wire.Slab.Alias` tells the slab that a string is a copy of the read buffer's head, so `Copy` of bytes inside the head, which is what the Header and Cookie sections and every path or query value with nothing to decode do, returns a substring and copies nothing. The slab therefore no longer needs twice the head: it needs the head and, only when the target holds a `%` or a `+`, the target again for what is decoded. 450 B a request became 321 B, the same as without middleware (the allocation experiment, all three variants), and the p99 rise of a plain request fell with it. The worst case (a fully percent-encoded path and query of 40 escapes each, a 300 B Authorization, a 90 B header and a cookie) is a test and stays at 2 allocations.
3. **`Response` is 80 B, not 272.** The headers middleware added to an answer from `next` live in a list in the chain's pooled state, and the Response holds the range that is its own (a generation check guards a kept one); a fresh Response, on the short-circuit path that is not budgeted, holds its headers on the heap. The API a middleware sees is unchanged (`res.AddHeader`, `res.SetHeader`, `res.Header`).
4. **Smaller things:** only the wildcards a match filled are copied into the chain's state, not the 648 B array; `Request.Header` skips a line on its first mismatch, with no scan for the name's end; the engine's `appendClean` looks for a CR, LF or NUL and then appends the value in one go, which every response header of every route pays (this is also in the no-middleware path).

### Rejected, with the number

- **Lazy head copy on the first `Request` call.** The view is passed by value, so the lazy state would live in the pooled chain state and a `Request` kept past the call would read nothing; the tested property, that a kept `Request` keeps its own request's bytes, is gone. It would save the 40 ns of the copy only for a middleware that never reads the request, and most first-party middleware read it (CORS the Origin, the request ID a header, the access log the path).
- **A `*Response` owned by the chain** passed to each middleware. A middleware that keeps the pointer holds the next request's answer, so every method would carry the generation check anyway, and what it saves is the copy of a value that is now 80 B (the 272 B to 112 B step, which was the whole of that copy, was worth 45 ns; 80 B is below what the remaining copies can show against the 2% spread).
- **Flattening the app, group and route chains into one slice at Registration.** The app's middleware runs before the match, so the route is not known and the chain cannot be composed ahead; the walk is already one indexed loop whose layer `len(app)` is the match, and its cost is the 25 ns of a pass-through layer.
- **Skipping the Response for a middleware that does not use it.** Whether it does is not known before it returns; the Response is built once at each boundary, 80 B.
- **A chain state kept on the connection instead of a pool.** It would save the 36 ns of Get and Put and cost about 1 KB for every idle connection, a hard axis ([ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)).

### Result (`summary-step5c.txt`, `raw-step5c.jsonl`; three interleaved rounds, spread under 1% within a variant)

In-memory, `-cpu 1`, over the route with no middleware (1,475 ns): one pass-through +100, three pass-through +135, one middleware that reads and adds a header +130, three +225; the first version +150, +200, +230 and +365. 336 B and 2 allocations in every case.

| req/s, mean of 3 | GET | POST |
|---|---|---|
| a66baa8 | 893.2k | 741.4k |
| no middleware | 890.4k (-0.3%) | 736.9k (-0.6%) |
| 1 middleware (reads a header, adds one) | 847.7k (-4.8% from none) | 694.8k (-5.7%) |
| 3 middleware | 817.5k (-8.2%) | 675.7k (-8.3%) |
| 3 pass-through (the chain alone) | 837.0k (-6.0%) | 691.4k (-6.2%) |
| 3 middleware, first version | 785.8k (-11.7%) | 633.5k (-14.0%) |

| p99 | GET | POST |
|---|---|---|
| no middleware | 739 us | 907 us |
| 1 middleware | 882 us (+19%) | 1,120 us (+23%) |
| 3 middleware | 881 us (+19%) | 1,111 us (+22%) |
| 3 pass-through | 874 us (+18%) | 1,096 us (+21%) |
| 3 middleware, first version | 960 us (+30%) | 1,275 us (+41%) |

Bytes a request: 321 in every variant (first version 450). Throughput for three middleware is now inside the 10%; the p99 is not, and it does not grow with the number of layers (one middleware and three give the same), so what is left is a fixed cost of having any middleware, not the chain's length. The cause is not found: with `GOGC=400` it was +6% before the slab change, which points at the collector, and the allocations and bytes are now equal to the route without middleware, so what remains is the pooled state (it holds pointers, so it is scanned and takes write barriers) or the extra response header the harness's middleware adds. This is open; it is not claimed to be within the budget.
