# The milestone 1 benchmark

bites beside net/http, Huma and nilo (the Zig framework), on one machine, one pair of routes and one client. It is the "Done when" item 5 of milestone 1 in [the roadmap](../docs/roadmap.md), built on the reference workload of [ADR 0002](../docs/adr/0002-the-reference-workload.md) and the axes of [ADR 0003](../docs/adr/0003-the-trade-budget.md). This directory is its own module (`bench/go.mod`, which pulls Huma), so the core stays standard-library only and `go test ./...` at the root never builds it.

Nothing here records a result. The numbers of a run go to `bench/result/milestone1.md` with the machine, the commit and the spread, as [CLAUDE.md](../CLAUDE.md) asks.

## What is measured

| metric | how | servers |
|---|---|---|
| allocations per request | the server's `runtime.MemStats.Mallocs` (and `TotalAlloc`) delta over `ALLOCS_N` (50,000) POSTs on one keep-alive connection, after 2,000 warm-up requests, divided by `ALLOCS_N` | the three Go servers |
| CPU per request | `utime + stime` of the server process from `/proc/<pid>/stat` over the measured window, divided by the requests completed in it (resolution 10 ms ticks, so only a window of seconds is meaningful) | all four |
| throughput | requests completed per second in the measured window | all four |
| p50, p99, p999 | client-side latency of every request in the window, microseconds, 256 keep-alive connections | all four |
| idle memory per connection | marginal cost from 10,000 to 20,000 connections, each served one GET before it idles, after a 5 s settle: `VmRSS` difference, and (Go) heap plus stack difference from `runtime/metrics` (`/memory/classes/heap/objects:bytes` plus `/memory/classes/heap/stacks:bytes`, after two `runtime.GC()`, the measure of `idle_test.go`), divided by 10,000 | RSS: all four; heap plus stack: the three Go servers |
| block: throughput, p50, p99, peak RSS | the optional `block` experiment (see below): `GET /block`, a handler that sleeps 10 ms, driven by 1,000 keep-alive connections for 15 s after a 2 s warm-up; requests a second, client-side latency, peak RSS sampled every 100 ms and `VmHWM` read at the end | all four (nilo: see below), and fib |
| binary size | bytes of the stripped binary (`-trimpath -ldflags "-s -w"`; Zig `ReleaseFast` with strip) | all four |

The two routes are the same on every server. The wire shape matches `budgetIn` and `budgetOut` of [budget_test.go](../budget_test.go).

- `POST /users/{id}?page=2&expand=true` with body `{"name":"ada","email":"ada@example.dev","age":36}` answers `{"id":7,"name":"ada","email":"ada@example.dev","age":36,"page":2}`: a path param, a query param (and an optional one), a JSON body, a JSON answer.
- `GET /users/{id}` answers `{"id":7,"name":"ada","email":"ada@example.dev","age":36}`.

The settle is longer than 100 ms on purpose: bites moves an idle connection's wait to a fresh goroutine after 100 ms ([ADR 0005](../docs/adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)), so a snapshot taken sooner would measure the state before that move. Per-connection memory is the marginal cost, not the average, so the fixed cost of the process and of the first connections drops out.

## fib and the block experiment

Neither is part of the default run, which stays the milestone 1 comparison of bites, nethttp, huma and nilo.

**fib** ([github.com/lesismal/fib](https://github.com/lesismal/fib), v1.6.0, the version of [docs/research/engines](../docs/research/engines/report.md); background in [docs/research/fib.md](../docs/research/fib.md)) is an event-loop HTTP engine: a few loops wait on epoll and a shared worker pool reads, parses, runs the handler and writes. It is added as `SERVERS="... fib"` and is compared as an engine, not as a typed framework: it has no routing by pattern, no typed decoding and no schema, so `servers/fib/main.go` routes on the path and reads and writes the JSON by hand (a small scanner for the body, `strconv.Append*` for the answer). The validation is what a hand-written server reasonably does and matches bites' rules: an integer path id, a required integer `page`, an optional boolean `expand`, a body with all three members and no unknown one; a mistake is a plain-text 400, not problem+json. Its mode is `fib.DefaultConfig()` with the synchronous handler, the one the research measured as "fib" (a blocking handler holds a worker of fib's adaptive pool); it is not the `fib-async` mode (`c.Retain`, the work on another goroutine). Compare its numbers with bites' as "an engine with hand-written code against a typed framework", and expect the allocation and CPU figures to reflect that hand-written code as much as the engine.

**block** is an experiment, `EXPS="block"`, as in `docs/research/engines/run1.sh`: `GET /block` on every server is a handler that sleeps 10 ms (`time.Sleep`, the stand-in for a database call) and answers `{"ok":true}`; the client holds 1,000 keep-alive connections (`BLOCK_CONNS`, `BLOCK_WARM` 2 s, `BLOCK_DUR` 15 s). With 1,000 connections and a 10 ms handler the ideal is 100,000 requests a second and a p50 just over 10 ms; the interesting column is peak RSS, what a server pays for 1,000 handlers blocked at once (an event loop whose handler blocks pays a goroutine per blocked request, the research found fib at 77.6 MB against 19.3 MB for a hand-written goroutine server). It is not in the default `EXPS`. The pool-exhaustion case (blocked handlers beyond fib's cap of 4,000) is not exercised by 1,000 connections.

- bites, net/http, Huma and fib sleep with `time.Sleep`. nilo (Zig) sleeps with `nilo.sleep(10)`, its own fiber-parking sleep, since a thread sleep would stop every request on the thread; that is the same wait, not the same mechanism, so read the nilo row as "the framework's own way of waiting".
- The `block` row of bites uses an empty Input; if a Registration refusal ever rejects that, this route is where to look first.

## The framework comparison

`SERVERS="bites nethttp chi gin echo fiber hertz huma nilo fib"` adds five Go frameworks beside the default four, the ones most Go services are built on, each at its latest release on 2026-11-04 (the versions go to `out/versions.json`). Each is configured the way its own documentation says to for speed, and each decodes the request with its own binder, so what is compared is the framework as it is used, not hand-written code on top of it:

| | configuration | decoding | JSON |
|---|---|---|---|
| Gin 1.12 | release mode, `gin.New()` with no Logger or Recovery | `c.Param`, `ShouldBindQuery`, `ShouldBindJSON` with unknown members refused | goccy/go-json, through the `go_json` build tag `run.sh` passes |
| Fiber 3.6 (fasthttp) | `fiber.New`, no middleware, one process (no prefork, as every other server) | `c.Params`, `c.Bind().Query`, `c.Bind().Body` | goccy/go-json as `JSONEncoder` and `JSONDecoder` |
| Echo 5.4 | `echo.New()`, no middleware | `c.Param`, `echo.BindQueryParams`, `echo.BindBody` | goccy/go-json through a `JSONSerializer` |
| chi 5.5 | the router on net/http, no middleware | the nethttp server's code | `encoding/json` v1 |
| Hertz 0.10 (netpoll) | `server.New`, no middleware | `c.Param`, `c.BindQuery`, `c.BindJSON` | sonic, its default |

The required checks are the same hand-written line in every one of the five (`common.NewUser.Complete`, a zero member taken as missing) and no validator library runs, which is cheaper than the presence bites tracks and favours them; pointer members would cost three allocations a request, so they are not used. Their errors are a plain 400, not problem+json. Every server was checked by hand for the 200 answers and for a 400 on a path id that is not a number, a missing `page` and a body without its members.

## The WebSocket comparison

`ws-run.sh` puts the bites Socket beside [gws](https://github.com/lxzan/gws) 1.10, gorilla/websocket 1.5 and coder/websocket 1.8, with the method of `run.sh` (pinned cores, interleaved rounds, a fresh process a run) and its own client, `wsclient/`: raw TCP, the upgrade written by hand, every message one masked text frame built once at start, and each message waiting for its echo (all frames up to FIN, since gorilla's and coder's streaming writers may end with an empty continuation frame). Two routes on every server: `GET /ws` echoes the bytes (bites: an Inbox of `[]byte`, answered as a binary frame; gws: `WriteMessage` with the message's opcode and `message.Close`, as gws documents; gorilla and coder: `NextReader` into `NextWriter`, so nothing is copied into a fresh slice), and `GET /wsjson` decodes each message into a struct and sends the struct back (bites: its own codec through the typed Inbox; the others: goccy/go-json). gorilla uses `WriteBufferPool`, its documented setting for many connections; everything else is each library's default. Experiments: `wsallocs` (mallocs per 32 B raw echo on one socket), `wsraw32`, `wsraw4k`, `wsjson32`, `wsjson4k` (256 sockets, messages a second, server CPU a message, latency) and `wsidle` (marginal memory per socket from 10,000 to 20,000, each having echoed one message).

```
cd bench
ulimit -n 524288
OUT=out/ws ./ws-run.sh
python3 -I analyze.py out/ws
```

With `CPUPROFILE=<file>` in its environment any Go server of the harness writes a CPU profile of its whole run, stopped by the SIGTERM `run.sh` sends. With `STACKS=<file>` it appends, every 10 ms, the bytes of goroutine stacks, the goroutines and the collector's cycles so far, without forcing a collection: the stack a connection holds under load and how often the collector runs ([`docs/research/stack-shrink/`](../docs/research/stack-shrink/report.md)).

## Method

- The server is pinned to cores 2,3,10,11 and the client to 4-7,12-15 with `taskset` (two physical cores for the server, four for the client, on the Ryzen 7 9700X; check `lscpu -e` on another machine and set `SRV_CPUS` and `CLI_CPUS`).
- Every connection of the client binds to 127.0.0.2 to 127.0.0.5 in turn, so loopback does not run out of ports in TIME_WAIT.
- Rounds are interleaved: round 1 runs every experiment on every server before round 2 starts, and at least three rounds are run. A fresh server process is started for every run.
- `analyze.py` prints mean and min to max over the rounds for every metric. A margin between servers inside the spread is "unchanged".
- The client is plain: raw TCP, one goroutine a connection, a hand-written response reader that wants a `200` and a `Content-Length`. Any other answer is counted as an error.
- `run.sh` writes `out/versions.json` (date, commits of bites and nilo with the dirty path count, Go, Zig and Huma versions, kernel, CPU, the knobs) and `out/sizes.json` next to the raw runs, one JSON object a run in `out/<experiment>-<server>-<round>.json`. `out/` is gitignored.
- Build the before from the same commit and flags in the same session: run the whole harness once for each tree, never quote an older run.

## How to run

```
cd bench
ulimit -n 524288        # the idle run holds 20,000 connections in the client and 20,000 in the server; run.sh needs 2 * N2 + 2000 and refuses to start below that
./run.sh                # builds into ~/.cache/bites-bench, runs 3 rounds
python3 -I analyze.py   # tables from out/
```

Expected duration for the defaults: about 25 to 30 minutes plus the Zig build (one to two minutes the first time, seconds after). Per round: the allocation run takes about 10 s a server (3 servers), each load run 3 s warm-up plus 10 s measured plus start, stop and a 1 s pause (2 routes, 4 servers), each idle run about 1 to 2 minutes (opening 20,000 connections and two 5 s settles). Three rounds of that is the figure; `ROUNDS=5` for a tighter spread.

Knobs (environment variables): `SERVERS` (any of `bites nethttp huma nilo fib`; the default leaves out fib), `EXPS` (any of `allocs get post idle block`; the default leaves out block), `ROUNDS`, `WARM`, `DUR`, `CONNS`, `BLOCK_CONNS`, `BLOCK_WARM`, `BLOCK_DUR`, `N1`, `N2`, `SETTLE`, `ALLOCS_N`, `SRV_CPUS`, `CLI_CPUS`, `PORT`, `OUT`, `BENCH_CACHE` (default `~/.cache/bites-bench`), `NILO_DIR` (default `~/Development/nilo`), `SKIP_BUILD=1`. A smoke run:

```
OUT=/tmp/smoke ROUNDS=1 WARM=1s DUR=2s CONNS=32 N1=100 N2=200 SETTLE=1s ALLOCS_N=2000 ./run.sh
python3 -I analyze.py /tmp/smoke
```

The follow-up comparison of bites with fib (every experiment, and the blocking one) is its own run in its own output directory, so the main run's files are not overwritten:

```
cd bench
ulimit -n 524288
OUT=out/fib-compare SERVERS="bites fib" EXPS="get post idle block allocs" ./run.sh
python3 -I analyze.py out/fib-compare
```

Expected duration, an estimate from the per-run costs above and not a measurement: about 15 to 20 minutes for three rounds (per round: the allocation runs about 15 s for both servers, the four load runs about 70 s, the two idle runs 2 to 4 minutes, the two block runs about 45 s; builds take seconds).

Binaries, the Go and Zig build caches and the Zig package cache live under `~/.cache/bites-bench/` and never in the repository. Zig always fetches packages into a `zig-pkg` directory beside the build root, so `run.sh` builds the Zig server from a copy of its sources in `~/.cache/bites-bench/zig-src` (with a symlink to the nilo checkout, which is only read).

Close other work first, and do not run a fuzzer or a build beside it: the pinned cores are not isolated from the scheduler (`isolcpus` is not set), so another process on them shows up as spread.

## The release run

`release.sh` is the release run, one command on the bench machine; v0.1.0 was released without it, the machine out of reach ([roadmap](../docs/roadmap.md), milestone 3), and the next release with the machine makes it. It refuses to start off Linux or on a tree with uncommitted changes, and writes everything under `out/release/` (gitignored), with the commit in `commit.txt`:

| phase | what it runs | what it answers |
|---|---|---|
| `step17` | `run.sh` on `b2b8576` and on this checkout, `bites` only, `get post allocs`, for the milestone 1 routes and the `prov`, `valid`, `mw4` and `log` variants, interleaved | what step 17's pass on the Mac ([`docs/research/hotpath-mac`](../docs/research/hotpath-mac/report.md)) is worth over a real socket |
| `profile` | `docs/research/profile-fib/run.sh`, `pprof.sh` and `analyze.py` on this checkout | the CPU profile of bites and fib that step 17 repeats |
| `compare` | `run.sh` with `bites nethttp huma nilo fib`, `allocs get post idle` | milestone 2's "done when" 7, and the release's table |
| `tls` | `run.sh` with `TLS=1`, `bites` only, `allocs get post idle` | TLS throughput and allocations a request, left unmeasured by milestone 2 |

```
cd bench
ulimit -n 524288
./release.sh                       # about 90 minutes; PHASES="step17 compare" runs some
```

`run.sh` takes `TLS=1` for the `tls` phase: the bites server serves its self-signed certificate (`-tls`) and the client speaks TLS without verifying it; the other servers have no such flag. The script has not yet been run on the bench machine, so its first run is also its test.

## What differs between the servers

| | bites | net/http | Huma | nilo |
|---|---|---|---|---|
| language, runtime | Go | Go | Go, on net/http through humago | Zig, fibers on OS threads (`threads` = one per core the process may use), no garbage collector |
| JSON codec | its own, on `encoding/json/jsontext` ([ADR 0015](../docs/adr/0015-json-is-our-own-codec-on-jsontext.md)) | `encoding/json` v1 | `encoding/json` v1 | its own, in Zig |
| path id type | `int64` | `int64` | `int64` | `u32` |
| validation | from the Input types: integer path id, required integer `page`, optional bool `expand`, body members all required, unknown ones refused; a mistake is a 400 problem+json naming the field | by hand: path id, page and expand parsed, body must be well-formed JSON with no unknown member; a missing body member is NOT noticed; errors are plain text | JSON Schema from the types: body members required, unknown ones refused, `page` required; a type mismatch is a 422 problem+json | from the handler's types: `:id` a `u32`, `page` required, `expand` defaulted, the body must carry the three members; errors are a small JSON object |
| extra work per answer | none | none | `DefaultConfig` adds a `$schema` member to the body and a `Link` header, so its answers are larger | a `Date` header |
| allocations per request | measured | measured | measured | not measured: Zig's allocator is not Go's, a mallocs count is not comparable |
| heap plus stack | `runtime/metrics` | `runtime/metrics` | `runtime/metrics` | not applicable: `runtime/metrics` is Go only, only RSS is recorded |

Settings that were changed from a server's default so the comparison is of the same thing:

- nilo ends a connection after 1,000 requests by default and the client does not reconnect (net/http and bites keep one for ever), so the Zig server sets `max_requests_per_connection = 0`. It also raises `max_connections` to 40,000 (default 10,000) for the idle run.
- Every server logs nothing (bites gets a discard logger, net/http and Huma do not log requests, nilo logs only at start and stop).
- bites, net/http and Huma are each run with their defaults otherwise (timeouts, buffer sizes). Huma's `DefaultConfig` is used as it ships.

## Known weaknesses

- **Heap plus stack is read inside the server**, through a SIGUSR1 handler (`common.StartStats`) that runs two collections first and then reads `runtime/metrics`, the same measure as `idle_test.go` and ADR 0005. The handler's goroutine and the collections are part of the process while it is read. The RSS number needs no cooperation from the server and is the one to trust across languages.
- **The allocation count includes the stats request itself.** Both reads of `Mallocs` come from the same handler, and the marshal after the first read is counted in the delta: tens of allocations over 50,000 requests, under 0.01 a request. The count is also of the whole process, so a background allocation (a timer, the signal goroutine) is in it. Run the allocation experiment alone if a figure of one or two allocations matters ([ADR 0003](../docs/adr/0003-the-trade-budget.md) counts exactly).
- **It is not the allocation test of the repository.** `budget_test.go` counts allocations of the typed request through the in-memory entry point; this counts them over a real socket, and includes what the Go runtime and the standard library's server allocate for the connection. Expect the two to differ.
- **CPU per request is 10 ms ticks** of `/proc/<pid>/stat` over the window. It includes kernel time for the socket and the garbage collector's background work, which is the point, and it also includes the server's idle threads waking.
- **Peak RSS in a load run is sampled** every 100 ms, with `VmHWM` read at the end; it measures the 256-connection load, not the idle cost.
- **One client for everything.** It is a Go program with one goroutine a connection. At about a million requests a second it is itself a limit, and it shares loopback with the server, so absolute numbers are loopback numbers; read the comparison, not the figure. Keep-alive only: connection setup is not measured.
- **Idle memory counts the connections each served one request**, with a 8 KB read buffer in the client; the server's buffers for a connection that has served a request and gone idle are what is compared. The settle is 5 s, so a server that releases buffers later than that looks bigger than it is. A second hazard is the garbage collector: Go's RSS includes memory the runtime has not returned to the system, which is why the heap-plus-stack figure is quoted beside it.
- **The servers are not equally strict**, see the table: net/http does the least validation and so is the cheapest by construction; Huma validates against a schema and answers larger bodies. Comparing bites with net/http is comparing a typed contract against hand-written parsing.
- **Different ids.** nilo's `u32` against the Go servers' `int64` changes nothing on these inputs.
- **Zig version and optimisation.** nilo is built `ReleaseFast` with strip by Zig 0.17; the Go servers with `-trimpath -ldflags "-s -w"` and no profile-guided optimisation.
- **One machine, one kernel.** Frequency scaling and the SMT sibling sharing (the server's cores 2,3 share silicon with 10,11) are not controlled beyond pinning; the interleaved rounds and the spread are the guard.
