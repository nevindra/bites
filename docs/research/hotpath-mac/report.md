# The framework's hot paths over memory: a CPU profile on the Mac before step 17

A first look for step 17 of [`docs/roadmap.md`](../../roadmap.md), taken on 2026-11-02 on a laptop, where the Linux machine of [`profile-fib`](../profile-fib/report.md) was not reachable. It profiles the framework's own work (router, decoders, middleware, Providers, Observers, the response) over the in-memory connections of [ADR 0027](../../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md), so the kernel, deadlines on a real socket and parking are not in it. Its numbers rank candidates; they are not the numbers of ADR 0003, which are taken over TCP on Linux.

## What was run

- **Machine:** Apple M1 Pro (8 cores: 6 performance, 2 efficiency), macOS 26 (Darwin 25.6.0), on battery or power as found, nothing pinned (macOS cannot pin a thread to a core).
- **Toolchain:** go1.27.0 darwin/arm64.
- **Tree:** commit b2b8576 with `hotpath_test.go` added (uncommitted at the time of the run).
- **Load:** `BenchmarkHotPaths` in `hotpath_test.go`: each hot path as a sub-benchmark, the real clock, one memconn connection a goroutine, the routes and requests of `bench/` and of the allocation rows in `budget_test.go`.
- **Runs:** `-test.cpu 1 -test.count 6 -test.benchtime 1s` for the table (`out/bench-cpu1.txt`, summarised with benchstat). With 8 Ps (`out/bench-cpu8.txt`) the spread was ±27% to ±113%, since the client and server goroutines hand each request across cores; with one P they alternate on one thread and the spread is ±0% to ±7%. One CPU profile of 5 s per path at one P (`out/*.top.txt`; the binary profiles and test binary are in `~/.cache/nilo-go-profile/hotpath-mac/`).
- **Attribution:** `go tool pprof -peek` and `-diff_base` against the plain POST, normalised. ns a request are a function's share of the profile times the path's ns/op, so they are estimates.

## The paths, and what each adds to the plain POST

Wall ns/op at one P, client and server together (the client's share is the same in every row), median of six with benchstat's spread.

| path | ns/op | over POST | allocs | B/op |
|---|---|---|---|---|
| GET `/users/{id}` | 1,535 ± 3% | | 2 | 224 |
| POST (typed JSON, path, query, body) | 2,313 ± 6% | | 2 | 353 |
| POST, constraint types and `Validate` | 2,327 ± 1% | +14 (unchanged) | 2 | 353 |
| POST, `Form[T]` | 2,342 ± 1% | +29 (unchanged) | 2 | 353 |
| POST, 1 route middleware | 2,497 ± 2% | +184 | 2 | 433 |
| POST, 3 route middleware | 2,696 ± 0% | +383 | 2 | 434 |
| POST, two Providers | 2,574 ± 1% | +261 | 2 | 370 |
| POST, `RequestID` | 2,541 ± 0% | +228 | 2 | 434 |
| POST, `CORS` | 2,729 ± 1% | +416 | 2 | 466 |
| POST, `Timeout` | 2,593 ± 5% | +280 | 2 | 435 |
| POST, `AccessLog` text | 3,243 ± 1% | +930 | 2 | 355 |
| POST, `AccessLog` JSON | 3,259 ± 3% | +946 | 2 | 354 |
| POST, all four (JSON log) | 3,986 ± 7% | +1,673 | 3 | 549 |

## Where the time goes

- **The engine reads the clock five times a POST** (four a GET): `await`, `readHead`, `ReadBody`, `write` and `dateLine`, 7.5% of the POST profile, about 35 ns each, as on Linux (33 ns in `profile-fib`). `memconn`'s own deadlines read it as often again; that half is the harness, not the engine.
- **`AccessLog` is slog.** 93% of `accessLog.End` is `slog.Logger.LogAttrs`: the JSON handler's formatting 73%, `runtime.Callers` for the record's source 15%, and a second `time.Now` 6%. The record's source is always the same line of `accesslog.go`, and the time is already read by `chainRun.End`.
- **The body.** `bodyPlan.read` is 18% of the POST: the codec 61% of it (`state.value` 74% of the codec, `jsontext.Decoder.Reset` 11%, the pool 7%), `ReadBody` 29% and `isJSONContentType` 8% (lower-casing the value into a buffer before comparing it).
- **Headers a middleware adds.** `engine.appendClean` checks every byte of a name and value for CR, LF and NUL, constants such as `Vary: Origin` included: about 2.7% of the CORS path.
- **Header lookups in middleware.** `Request.Header` scans the head: three lookups are about 2% of the 3-middleware path, the "scan or index" question of ADR 0022.
- **`Timeout`** is mostly its timer: `time.(*Timer).Reset` 5.5% of its path.

## What it does not show

The syscalls (44% of the GET profile on Linux in `profile-fib`), deadlines on a real socket, the scheduler under 256 connections and parking. Those are measured over TCP on the Linux machine, with `profile-fib/run.sh`.

## Round 1: three changes, measured

Built and measured on 2026-11-02, same machine and toolchain, the before binary built from b2b8576 with `hotpath_test.go` in the same session. Six runs of each, interleaved (two before, two after, three times), one P; `out/round1-benchstat.txt`.

- **`AccessLog` builds its own `slog.Record`** (`accesslog.go`): its time is End on the app's clock, already read by `chainRun.End` (now kept as `chainRun.ended`), and its PC is 0, so slog takes no `runtime.Callers` and no second `time.Now`. A probe on go1.27.0 showed that a record with PC 0 has no `source` with `AddSource` on, in the text and JSON handlers.
- **`ReadBody` arms no deadline for a body already whole in the read buffer** (`internal/engine/exchange.go`): nothing waits, so nothing needs bounding; a chunked body or one still arriving keeps its deadline. The idle wait arms its own deadline next, and the reader-goroutine watcher clears it, so nothing depended on the deadline left behind.
- **`isJSONContentType` answers `application/json` exactly before lower-casing** (`decode.go`), and **`appendClean` looks for CR, LF and NUL with `strings.IndexByte`** instead of a loop a byte at a time (`internal/engine/exchange.go`).

| path | before | after | change |
|---|---|---|---|
| GET | 1,476 ± 4% | 1,523 ± 3% | unchanged (p=0.29) |
| POST | 2,206 ± 3% | 2,104 ± 3% | -4.6% |
| POST, `Validate` | 2,217 ± 3% | 2,114 ± 3% | -4.7% |
| POST, `Form[T]` | 2,223 ± 4% | 2,089 ± 2% | -6.0% |
| POST, 1 middleware | 2,424 ± 2% | 2,256 ± 2% | -6.9% |
| POST, 3 middleware | 2,569 ± 4% | 2,423 ± 2% | -5.7% |
| POST, two Providers | 2,480 ± 3% | 2,230 ± 3% | -10.1% |
| POST, `RequestID` | 2,412 ± 4% | 2,245 ± 3% | -6.9% |
| POST, `CORS` | 2,587 ± 4% | 2,391 ± 5% | -7.6% |
| POST, `Timeout` | 2,459 ± 3% | 2,256 ± 3% | -8.2% |
| POST, `AccessLog` text | 3,171 ± 2% | 2,775 ± 2% | -12.5% |
| POST, `AccessLog` JSON | 3,106 ± 3% | 2,765 ± 3% | -11.0% |
| POST, all four | 3,911 ± 3% | 3,377 ± 2% | -13.6% |

Every change is p=0.002 except GET, which none of the three touches. Allocations and bytes a request are unchanged (2, and 3 for all four). Over memory a deadline is `memconn`'s timer, not the runtime poller's, so the `ReadBody` share of these gains is not the Linux one; it is measured over TCP with `profile-fib/run.sh`.

The full test suite passes with and without `-race` except six tests that fail on macOS before and after these changes alike, all on behaviour only Linux has or the inline write only Linux takes: the epoll watcher over TLS, `TCP_NOTSENT_LOWAT`, the allocations of a `Publish` to 300 subscribers, two Unix-socket tests, and the 3 allocations of `Done` over loopback TCP.

## Round 2: a fast path for flat JSON bodies

Built and measured on 2026-11-02, same machine and toolchain; before is the round 1 binary, after adds `internal/jsoncodec/fast.go` (ADR 0015). Six runs each, interleaved, one P; `out/round2-benchstat.txt`.

| path | before | after | change |
|---|---|---|---|
| GET | 1,524 ± 2% | 1,541 ± 2% | unchanged (p=0.39) |
| POST | 2,119 ± 2% | 1,860 ± 1% | -12.2% |
| POST, `Validate` | 2,130 ± 2% | 1,911 ± 3% | -10.3% |
| POST, `Form[T]` | 2,120 ± 2% | 2,153 ± 2% | unchanged (p=0.13), not JSON |
| POST, 3 middleware | 2,464 ± 2% | 2,204 ± 2% | -10.6% |
| POST, all four | 3,477 ± 1% | 3,136 ± 3% | -9.8% |

`BenchmarkDecodeAFlatBody` (13 members, every kind the fast path takes): 395 ns and 0 allocations against 1,061 ns and 0 through the tokenizer, one run, `-benchmem`. `FuzzTheFastPathAgreesWithTheTokenizer` ran 120 s, 22,942,206 executions, no difference. Allocations a request are unchanged.

## Round 3: query lookups, unescaping, one clock read a response

Built and measured on 2026-11-02, same machine and toolchain; before is the round 2 binary. Six runs each, interleaved, one P; `out/round3-benchstat.txt`.

- **A text section of at most 8 fields is scanned instead of hashed** (`textSection.lookup` in `compile.go`), for the query, headers, cookies and `Form[T]`, as `jsoncodec` already does for a struct.
- **`Slab.Unescape` asks `bytes.IndexByte` whether there is a '%' or a '+'** before its loop a byte at a time (`internal/wire/slab.go`); a value with neither is a plain `Copy`.
- **A response reads the clock once**: `write` reads it for its write deadline and hands the same time to `dateLine`, whose line has a resolution of a second anyway (`internal/engine/conn.go`, `server.go`). The write deadline starts the few hundred nanoseconds earlier that the head takes to finish.

| path | before | after | change |
|---|---|---|---|
| GET | 1,507 ± 2% | 1,463 ± 1% | -2.9% |
| POST | 1,837 ± 1% | 1,792 ± 1% | -2.5% |
| POST, `Form[T]` | 2,104 ± 3% | 1,992 ± 0% | -5.4% |
| POST, 3 middleware | 2,178 ± 2% | 2,119 ± 2% | -2.7% |
| POST, `CORS` | 2,118 ± 1% | 2,091 ± 3% | unchanged (p=0.29) |
| POST, all four | 3,104 ± 1% | 3,103 ± 2% | unchanged (p=0.85) |

## The three rounds together

The binary of b2b8576 with `hotpath_test.go`, against the tree after round 3, six runs each, interleaved, one P; `out/cumulative-benchstat.txt`. Every row is p=0.002; allocations and bytes a request are unchanged.

| path | before | after | change |
|---|---|---|---|
| GET | 1,514 ± 2% | 1,468 ± 1% | -3.1% |
| POST | 2,246 ± 2% | 1,796 ± 2% | -20.0% |
| POST, `Validate` | 2,272 ± 1% | 1,806 ± 2% | -20.5% |
| POST, `Form[T]` | 2,272 ± 1% | 2,004 ± 3% | -11.8% |
| POST, 1 middleware | 2,443 ± 1% | 1,961 ± 2% | -19.7% |
| POST, 3 middleware | 2,631 ± 1% | 2,143 ± 4% | -18.5% |
| POST, two Providers | 2,515 ± 5% | 1,968 ± 6% | -21.8% |
| POST, `RequestID` | 2,469 ± 1% | 1,986 ± 3% | -19.6% |
| POST, `CORS` | 2,639 ± 1% | 2,085 ± 1% | -21.0% |
| POST, `Timeout` | 2,499 ± 1% | 1,980 ± 2% | -20.8% |
| POST, `AccessLog` text | 3,212 ± 1% | 2,490 ± 5% | -22.5% |
| POST, `AccessLog` JSON | 3,158 ± 0% | 2,476 ± 1% | -21.6% |
| POST, all four | 3,882 ± 1% | 3,134 ± 7% | -19.3% |

These are wall ns over memory with the client in them, on a laptop. What they come to over TCP on Linux, where the kernel's share is about half of a request (`profile-fib`), is measured with `profile-fib/run.sh` before any number goes into ADR 0003 or `bench/result/`.
