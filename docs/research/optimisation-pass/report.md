# The optimisation pass, and bites beside the Go frameworks most services use

Run on 2026-11-04 on the bench machine, the first pass of roadmap item 38: every part of the framework measured, the parts that cost most changed, and the result put beside Gin, Fiber, Echo, chi, Hertz, Huma, net/http, fib and nilo for HTTP, and beside gws, gorilla/websocket and coder/websocket for WebSockets.

## What was run

- **Machine:** AMD Ryzen 7 9700X (8 cores, 16 threads), Linux 7.2.8, go1.27.3 linux/amd64, Zig 0.17.0 for nilo.
- **Tree:** before is commit 2dd6c35; after is that commit with the changes below, uncommitted at the time of the run.
- **Over TCP:** `bench/run.sh` and `bench/ws-run.sh` ([`bench/README.md`](../../../bench/README.md#the-framework-comparison)), the server pinned to cores 2,3,10,11 and the client to 4-7,12-15, rounds interleaved, a fresh server process a run, three rounds, 256 keep-alive connections, 3 s warm-up and 10 s measured. Every number is the mean of the rounds with min to max beside it. The before run is every server; the after run is bites interleaved with Fiber and fib (HTTP) and with gws (WebSocket), whose binaries are the same files as in the before run, so a change in theirs between the runs is the machine's drift: Fiber moved -3% and fib -1% in CPU a request.
- **Over memory:** the benchmarks of `hotpath_test.go` and of `paths_resp_bench_test.go`, `paths_stream_bench_test.go` and `paths_sec_bench_test.go` (each path of the framework as a sub-benchmark, one memconn connection a goroutine, the real clock), one P, pinned to cores 0 and 8, the before and after test binaries interleaved five times, summarised with benchstat. These rank work; the TCP runs are the numbers.
- **Profiles:** CPU profiles of each path over memory, and of the servers under the harness's load over TCP (`CPUPROFILE`, `bench/common`).
- **Spread:** a margin inside the min to max of both runs is "unchanged".

The HTTP after run used the bites binary built before the last two changes (the WebSocket read buffer that grows, and the pooled frame); neither touches a plain request's path. The WebSocket after run used the final tree.

## Where the time went

What the paths cost over memory before the pass, and what a profile found in each. Paths where nothing worth changing was found are listed so they are not measured again for nothing.

| path | cost before | what the profile showed |
|---|---|---|
| a keep-alive request over TCP | GET 3.7 µs, POST 4.4 µs of server CPU | two reads a request (the idle wait's one byte, then the rest) and five deadline changes, each a runtime-timer change of about 80 ns; 30% behind Fiber |
| a JSON answer over 64 KiB | 100 KiB: 237 µs, 29 allocations, 515 KB of garbage | the body buffer grew by doubling from 4 KiB on every request, since one over 64 KiB was dropped, not pooled; a third of the time was the collector |
| a string in a JSON answer or body | 25-30% of a list answer, 19% of a 64 KiB message | `jsontext.AppendQuote` and the decoder's plain-string scan, one byte at a time |
| a WebSocket text message | 64 KiB echo 46 µs over memory, 47% in UTF-8 validation | the validation looked at one byte at a time |
| a WebSocket message of 4 KiB over TCP | 10.3 µs of server CPU, gws 2.8 µs | a 4,104-byte frame does not fit the 4,096-byte read buffer, so every message was read in pieces and assembled; the one-byte idle read again; a frame allocated on every Send |
| SSE and `Events[T]` | 90 to 130 ns an event | the clock read on every Flush, 15-30%; not changed |
| Registration | 27 µs and about 270 allocations a route | the encoder, decoder and schema of a shared type compiled again for each route; start-up only, not changed |
| a metrics scrape of 50 routes | 95 µs, 390 KB | every label escaped again on every line; once a scrape interval, not changed |
| JWT HS256 verify (`modules/jwt`) | 2.1 µs, 23 allocations | the payload parsed twice and the header into a map; not changed |
| error answers, files, static, uploads, forms, sessions, CSRF, auth, the rate limiter, CORS, PROXY protocol, health, the API document | 1 to 4 µs, or memory- and syscall-bound | nothing that would give more than a few percent |
| TLS | 5.3 µs a request established, 380 µs a handshake | the write syscall and crypto/tls; session resumption works |

## What was changed

1. **A busy connection waits holding its buffer** (`internal/engine/conn.go`, [ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)). The first 100 ms of an idle wait read straight into the scratch the last request used, which the connection keeps: one read a request instead of a byte and then the rest. A connection quiet for 100 ms gives it back before it moves or parks.
2. **The head's deadline only when needed, and the write deadline left armed** ([ADR 0024](../../adr/0024-limits-are-on-by-default.md)). `HeadTimeout` is armed only for a head that did not come whole; the write deadline is moved only when less than half of it is left and cleared when the connection goes quiet. Five deadline changes a keep-alive request became one (`TestABusyKeepAliveConnectionReadsEachLaterRequestWithOneReadAndChangesOneDeadline`, which fails on the before tree with 2 reads and 5 changes).
3. **Large bodies from pools by size class** (`internal/engine/bigbuf.go`, `outplan.go`). A route remembers its last large JSON answer's length and reserves it at once, from `sync.Pool`s of 128 KiB to 8 MiB; the scratch keeps its own 4 KiB buffer aside. The writev of a large answer keeps its two-slice vector in the scratch, where a local one escaped (two allocations).
4. **Strings scanned eight bytes at a time** (`internal/jsoncodec/plain.go`): the decoder's fast path and the encoder copy a string with nothing to escape as it is; anything else goes to `jsontext` as before, so the output and the errors are the same (`FuzzTheStringEncoderAgreesWithJsontext`, 30 s, and the existing fast-path fuzzer, 30 s).
5. **WebSocket UTF-8 by `utf8.Valid`** (`internal/ws/utf8.go`), holding back only a rune a chunk ends inside; the split-at-every-point test and both frame fuzzers pass.
6. **A hot WebSocket's read buffer grows** to 64 KiB when its messages fill it, so a message is one read, and goes back to 4 KiB with the scratch.
7. **A Send written inline builds its frame in a pooled buffer** (`websocket.go`, `engine.WS.SendFrame`): 0 allocations instead of 1 ([ADR 0003](../../adr/0003-the-trade-budget.md)).

## Over memory, before and after

`BenchmarkHotPaths`, five interleaved runs each, one P; every row p=0.008, allocations unchanged (2, and 3 for all four middleware).

| path | before | after | change |
|---|---|---|---|
| GET | 1.485 µs | 1.103 µs | -25.7% |
| POST | 1.845 µs | 1.404 µs | -23.9% |
| POST, `Validate` | 1.850 µs | 1.396 µs | -24.5% |
| POST, `Form[T]` | 1.929 µs | 1.528 µs | -20.8% |
| POST, 3 middleware | 2.135 µs | 1.675 µs | -21.6% |
| POST, two Providers | 1.981 µs | 1.580 µs | -20.2% |
| POST, `CORS` | 2.092 µs | 1.625 µs | -22.3% |
| POST, `AccessLog` JSON | 2.477 µs | 1.965 µs | -20.7% |
| POST, all four | 2.887 µs | 2.487 µs | -13.9% |

Large answers and messages (`paths_*_bench_test.go`), five interleaved runs, p=0.008:

| path | before | after | change | allocations |
|---|---|---|---|---|
| a list of 1,000 structs (100 KiB) | 237.5 µs | 102.7 µs | -56.8% | 29 to 2 |
| a 64 KiB answer | 168.9 µs | 72.6 µs | -57.0% | 27 to 2 |
| the same, gzip | 220.1 µs | 141.9 µs | -35.5% | 25 to 2 |
| a list of 100 structs (10 KiB) | 12.52 µs | 11.98 µs | -4.3% | 2 |
| WebSocket echo, bytes, 64 KiB | 45.80 µs | 27.28 µs | -40.4% | |
| WebSocket echo, JSON, 64 KiB | 108.7 µs | 67.9 µs | -37.5% | |
| WebSocket echo, JSON, 1 KiB | 2.648 µs | 2.046 µs | -22.7% | |
| UTF-8 check of 4 KiB of ASCII | 1,050 ns | 53 ns | 20x | |
| UTF-8 check of 4 KiB of mostly multi-byte text | 2,230 ns | 2,710 ns | +21% | |

## Over TCP: HTTP, before

Every server, three rounds. Server CPU is nanoseconds of the server process's CPU a request; allocations are the server's mallocs a POST over one connection.

| server | GET req/s | GET CPU ns | GET p99 µs | POST req/s | POST CPU ns | POST p99 µs | allocs | idle heap+stack B | idle RSS B |
|---|---|---|---|---|---|---|---|---|---|
| bites | 836,749 [829k-841k] | 3,716 | 985 | 677,465 [573k-747k] | 4,420 | 1,841 | **2.01** | 920 | 7,143 |
| net/http | 513,421 | 6,694 | 1,503 | 392,813 | 8,965 | 2,063 | 35.0 | 18,180 | 19,195 |
| chi | 484,616 | 7,102 | 1,641 | 377,389 | 9,334 | 2,283 | 38.0 | 18,185 | 19,629 |
| Gin | 529,281 | 6,442 | 1,653 | 362,088 | 9,500 | 2,655 | 43.0 | 18,181 | 18,914 |
| Echo | 531,843 | 6,408 | 1,511 | 414,292 | 8,457 | 1,964 | 34.0 | 18,181 | 19,050 |
| Fiber | 1,003,549 | 2,817 | 665 | 814,766 | 3,800 | 806 | 6.01 | 27,653 | 28,880 |
| Hertz | 860,973 | 3,576 | 712 | 705,269 | 4,651 | 999 | 14.0 | 22,608 | 23,404 |
| Huma | 444,507 | 7,877 | 1,736 | 302,505 | 12,015 | 2,459 | 46.0 | 18,182 | 19,698 |
| nilo (Zig) | 1,314,949 | 2,091 | 445 | 1,146,520 | 2,558 | 430 | n/a | n/a | 4,928 |
| fib | 1,043,032 | 2,502 | 583 | 881,452 | 3,196 | 704 | 6.03 | 983 | 1,077 |

The bites POST of the third before round overlapped a profile taken on cores 0 and 8, hence its spread (573k to 747k); the after run had nothing beside it.

## Over TCP: HTTP, after

| server | GET req/s | GET CPU ns | GET p99 µs | POST req/s | POST CPU ns | POST p99 µs | allocs |
|---|---|---|---|---|---|---|---|
| bites | 968,093 [957,948-974,826] | 3,016 [2,992-3,048] | 938 | 833,895 [822,264-853,984] | 3,624 [3,577-3,701] | 1,118 | 2.00 |
| Fiber | 1,033,722 | 2,729 | 641 | 814,272 | 3,763 | 830 | 6.01 |
| fib | 1,056,849 | 2,474 | 568 | 893,001 | 3,192 | 677 | 6.03 |

bites against itself: GET +15.7% a second and -18.8% CPU a request, POST +23.1% and -18.0%. A POST now costs less server CPU than Fiber's (3,624 against 3,763 ns) and is served faster (834k against 814k a second), with 2 allocations to Fiber's 6; a GET is 10% behind Fiber's CPU (3,016 against 2,729) and 6% behind its rate. fib, an engine with hand-written decoding and no typed contract, is still 12 to 18% ahead in CPU; nilo (Zig) 30% ahead.

What did not move: **p99**. bites' p99 is 938 µs on a GET and 1,118 on a POST against Fiber's 641 and 830 and fib's 568 and 677, with p50s alike (237 against 231 and 249). The cause is not in this pass; it is the p99 question the first tag already waits for (roadmap, "The first tag").

Idle memory, the guarded axis: heap plus stack per idle connection 944 B [937-956] after against 920 [910-927] before, unchanged within the spread of 10,000 to 20,000 connections, and the connection struct keeps its 416 B size class. The RSS while 10,000 connections each send one request is higher, 14,240 B a connection on average against 7,841: each holds its scratch for the 100 ms after its request, and the runtime has not returned that memory to the system by the snapshot. It is memory the process held at the peak, not memory an idle connection holds; the heap-plus-stack figure is the one ADR 0005 guards.

## Over TCP: WebSocket, before

| server | 32 B raw msg/s | CPU ns | 4 KiB raw msg/s | CPU ns | 32 B JSON CPU ns | 4 KiB JSON CPU ns | allocs a message | idle heap+stack B |
|---|---|---|---|---|---|---|---|---|
| bites | 1,056,026 | 2,622 | 326,080 | 10,302 | 2,760 | 15,802 | 2.00 | **1,665** |
| gws | 1,263,161 | 1,995 | 998,046 | 2,770 | 2,387 | 9,617 | 1.00 | 9,699 |
| gorilla | 1,186,168 | 2,183 | 636,819 | 4,222 | 3,385 | 16,016 | 3.00 | 16,008 |
| coder | 341,075 | 10,012 | 252,956 | 12,482 | 3,972 | 13,739 | 1.00 | 22,011 |

## Over TCP: WebSocket, after

bites interleaved with gws, three rounds (idle: two); gws's binary is the before run's, and moved under 1% between the runs.

| server | 32 B raw msg/s | CPU ns | 4 KiB raw msg/s | CPU ns | 32 B JSON CPU ns | 4 KiB JSON msg/s | CPU ns | allocs a message | idle heap+stack B |
|---|---|---|---|---|---|---|---|---|---|
| bites | 1,132,498 [1.085M-1.167M] | 2,276 | 673,209 [667,856-677,139] | 4,493 | 2,387 | 503,569 [502,225-505,710] | 6,610 | 1.00 | 1,678 |
| gws | 1,263,005 | 1,989 | 1,004,103 | 2,763 | 2,414 | 341,580 | 9,860 | 1.00 | (9,699 before) |

bites against itself: a 4 KiB message 2.06 times the rate at 44% of the CPU, a 4 KiB JSON message 2.22 times the rate at 42% of the CPU, a 32 B message +7% and -13% CPU, one allocation a message instead of two; idle memory unchanged (1,678 against 1,665 B). Against gws: the typed JSON echo is now ahead, 1.47 times its rate at 4 KiB and the same CPU at 32 B, with gws decoding through goccy/go-json; the raw byte echo is still behind, 14% in CPU at 32 B and 63% at 4 KiB, where bites copies each message into a slice the handler owns (ADR 0008) and gws lends its pooled buffer until `message.Close`. p99 of the 4 KiB echo fell from 4,201 to 2,160 µs and is still above gws's 664. An idle socket holds 5.8 times less than gws's.

## What was not done, and why

- **Registration** compiling a shared type once: about 27 µs a route is start-up, 14 ms for 500 routes.
- **The metrics scrape**: about 50 µs to gain once a scrape interval.
- **JWT HS256**: about 1.5x on a 2 µs verify in a module.
- **The clock read on every SSE flush**: 15-30% of an event that costs 90 to 130 ns.
- **A 10x on a request**: there is none to have. Over TCP the kernel's share of a request is about half (`docs/research/profile-fib`), and after this pass bites is within 10% of Fiber and 12 to 18% of fib in CPU a request; the 2x and 10x gains were on the paths that did avoidable work (large answers, UTF-8, large WebSocket messages), and are in the tables above.
