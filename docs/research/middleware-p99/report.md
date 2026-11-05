# The p99 of a route behind middleware: what raised it, and what is left

Taken on 2026-11-05, Go 1.27.3, AMD Ryzen 7 9700X, kernel 7.2.8-9-omarchy. The "before" is `db35bea`, built from `git archive` into `~/.cache`; the "after" is `db35bea` plus the uncommitted change that counts the slab room per chain (ADR 0003, ADR 0055). Both were built in the same session with the same flags. Server on cores 2,3,10,11 and client on 4-7,12-15, 256 connections, 3 s warm-up and 10 s measured, loopback, through `bench/run.sh`. The roadmap had this as the first thing the profile before the first tag takes: a p99 19% to 23% higher behind any middleware, whatever the number of layers, for a cause not found ([`docs/research/middleware-core/`](../middleware-core/report.md), [`docs/research/m2-request-path/`](../m2-request-path/report.md)).

## How the cause was found (`probes.sh`, on `db35bea`)

The variants are the route of milestone 1 with no middleware (`none`) and with one pass-through route middleware (`bare1`, `BITES_MW=1 BITES_MWKIND=bare`), GET `/users/7`.

**The collector, on and off** (`probes.sh gc`, `raw-gc.jsonl`, three rounds interleaved):

| GET | p50 | p99 | req/s |
|---|---|---|---|
| none, `GOGC=100` | 229 us | 814 to 817 us | 1,026k |
| bare1, `GOGC=100` | 240 to 243 us | 910 to 950 us (+14%) | 945k to 967k |
| none, `GOGC=off` | 223 to 225 us | 609 to 630 us | 1,050k to 1,064k |
| bare1, `GOGC=off` | 234 to 235 us | 628 to 638 us (+2.5%) | 1,014k to 1,021k |

With the collector off the middleware's p99 rise is the size of its p50 rise (+4.5%): the tail it adds is the collector's.

**How often it runs** (`probes.sh gctrace`, `gctrace.txt`, the last two thirds of each run): none 98 and 99 cycles a second, bare1 139 and 140. Each cycle costs the same in both (0.30 ms of mark, 0.73 ms of assist CPU), the live heap is 1 MB and the goal 4 MB, Go's floor for a small heap: a cycle starts every 3 MB allocated, so the number of cycles follows the bytes a request allocates.

**The bytes under load.** `bench/run.sh`'s `allocs` experiment drives one connection with the POST; here the server's `MemStats` were read twice during a 256-connection run (a SIGUSR1 at 4 s and at 11 s, the requests between estimated from the client's rate, so a few percent rough). GET: 2.00 allocations and 192 B a request with no middleware, 2.00 and 272 B with one pass-through. POST: 304 B and 384 B. On one connection the GET was 192.3 and 272.4 B. The 80 B is exactly `generatedIDLen + rateLimitRoom`, the room `serveChain` reserved in the slab of every request with any middleware, for `RequestID`'s generated id and a `RateLimiter`'s header, whether either was on the chain or not. The allocation count, the guarded axis, did not move, which is why no test saw it.

**The room removed** (`probes.sh room`, `raw-room.jsonl`, the same tree with the `AddSlab` line taken out, three rounds):

| | p99 GET | p99 POST | req/s GET |
|---|---|---|---|
| none | 821 to 827 us | 949 to 973 us | 1,019k to 1,023k |
| bare1 | 901 to 906 us (+10%) | 1,014 to 1,025 us (+6%) | 963k to 965k |
| bare1 without the room | 852 to 866 us (+4%) | 983 to 1,031 us (+5%) | 982k to 987k |

Without the room the bytes are 192 and 304 again, the same as with no middleware, and what is left of the GET p99 rise is the size of the CPU the chain adds (about 150 ns, +5%).

## The change, measured (`run.sh`, `summary.txt`, `raw.jsonl`)

`serveChain` now adds the room the chain's first-party middleware write into, counted at Registration by `slabRoom`: 16 B for each `RequestID`, 64 B for each `RateLimiter`, nothing for any other middleware. `RequestID` became a method value so that Registration recognises it, as a `RateLimiter`'s middleware already was for the API document (ADR 0069). A request matched inside the app's chain is sized for the app's middleware plus the largest room of any route, since its route is not known when the slab is made.

Three rounds interleaved, medians, each tree against its own route with no middleware; `mw1` is one middleware that reads a header and adds one, `bare1` one pass-through, `mw3` three of `mw1`. The third round of `before-none` was disturbed (991k, p99 900 us), which is why the medians are quoted.

| GET | req/s | CPU ns/req | p50 | p99 |
|---|---|---|---|---|
| before, none | 1,023.1k | 2,790 | 230 us | 816 us |
| before, mw1 | 952.8k (-6.9%) | 3,087 (+297) | 244 us | 919 us (+13%) |
| after, none | 1,017.4k | 2,792 | 230 us | 831 us |
| after, mw1 | 974.7k (-4.2%) | 2,981 (+189) | 241 us | 852 us (+3%) |
| after, bare1 | 988.9k (-2.8%) | 2,926 (+134) | 237 us | 853 us (+3%) |
| after, mw3 | 933.3k (-8.3%) | 3,155 (+363) | 249 us | 973 us (+17%) |

| POST | req/s | CPU ns/req | p50 | p99 |
|---|---|---|---|---|
| before, none | 900.4k | 3,331 | 259 us | 951 us |
| before, mw1 | 843.4k (-6.3%) | 3,637 (+306) | 272 us | 1,021 us (+7%) |
| after, none | 903.2k | 3,320 | 257 us | 956 us |
| after, mw1 | 861.8k (-4.6%) | 3,546 (+226) | 270 us | 990 us (+4%) |
| after, bare1 | 874.3k (-3.2%) | 3,471 (+151) | 266 us | 980 us (+3%) |
| after, mw3 | 825.0k (-8.7%) | 3,758 (+437) | 279 us | 1,115 us (+17%) |

Bytes a request (the harness's POST on one connection): 304 with no middleware in both trees, 385 behind one middleware before, 304 to 305 after with one and with three. Allocations 2.003 to 2.004 in every variant. The route with no middleware is unchanged between the trees (inside the spread).

One middleware is now inside the 10% of ADR 0003 on throughput and on p99. Three are inside it on throughput and not on p99, which the next section is about.

## What was left: the stack the collector shrinks (`probes.sh shrink`, `raw-shrink.jsonl`)

With the bytes equal, three middleware still cost +17% of p99 against +8% of p50. Go's collector, at each cycle, halves the stack of a goroutine that uses less than a quarter of it (plus about 800 B of guard); a connection's goroutine waiting for its next request uses little, so it is shrunk, and the request then grows it back with a copy (`runtime.newstack`, `copystack`). [ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md) says a goroutine blocked in a read is never shrunk because its frames are more than that quarter; that was measured for a stack of 4 KiB, and these results fit a request that now grows the stack past 4 KiB, to a size whose quarter the blocked read is under (an inference: the stack sizes were not read). With `GODEBUG=gcshrinkstackoff=1`, GET, two rounds:

| | p99, default | p99, no shrinking | req/s, default | req/s, no shrinking |
|---|---|---|---|---|
| none | 818 to 823 us | 677 to 679 us (-18%) | 1,023k to 1,024k | 1,037k to 1,038k |
| mw3 | 979 to 1,012 us | 710 to 724 us (-28%) | 917k to 927k | 952k to 959k |

Three middleware against none: +21% of p99 by default, +6% without shrinking. In a CPU profile of each (`CPUPROFILE`, one run), `newstack` is 1.1% of the samples with no middleware and 1.7% with three, `copystack` 1.4% and 2.1%; the frame whose prologue grows the stack is the deepest of the request, `textRun.offer` with none and `wire.(*Slab).Unescape` with three, in the Input's decoding. So it is a cost of every route that the depth of a chain makes larger, and the route with no middleware pays it too (18% of its p99).

A library cannot set `gcshrinkstackoff` for the process: a `//go:debug` line is honoured only in the main package, and `GODEBUG` belongs to whoever runs the binary. What could be done in the framework: a request path that needs less stack at its deepest, so a shrunk stack still holds it; or a connection's goroutine that waits in a frame large enough to stay above a quarter of its stack, which costs nothing on the idle-connection axis since an idle connection parks and gives up its goroutine after 100 ms (ADR 0005). The second was built and measured the same day: a busy connection's wait under padding sized to keep it above that quarter, [`docs/research/stack-shrink/`](../stack-shrink/report.md).

## Providers, constraint types, and `RequestID` alone (`run-rest.sh`, `summary-rest.txt`, `raw-rest.jsonl`)

Three rounds interleaved on the fixed tree, medians, against the route with no middleware: `prov` is an Input that asks for a `Caller` whose Provider needs a `Session` whose Provider reads a header, `valid` constraint types in every field and a `Validate` that returns nil, `rid` `RequestID` alone with a generated id (`bench/servers/bites/main.go`, `BITES_VARIANT`).

| | GET req/s | GET p99 | POST req/s | POST p99 | CPU ns/req GET, POST | bytes a request |
|---|---|---|---|---|---|---|
| none | 1,025.4k | 821 us | 903.9k | 949 us | 2,775, 3,315 | 304 |
| prov | -4.8% | 864 us (+5%) | -4.7% | 984 us (+4%) | +199, +233 | 304 to 305 |
| valid | -0.3% | 817 us (-0%) | -0.6% | 950 us (+0%) | +15, +33 | 304 |
| rid | -6.0% | 870 us (+6%) | -5.3% | 1,008 us (+6%) | +267, +266 | 321 |

All three are inside the 10% of ADR 0003 on both axes. The Providers' +21% to +22% of 2026-11-01 was the chain's rise then, and is gone; the constraint types' POST +19% of that day is not reproduced (the same POST, three rounds, p99 945 to 956 us against 945 to 951). `RequestID` pays its 16 B of room, as it should, since it writes there.

## Not measured

TLS; a chain with app middleware and a `RateLimiter` on one route only (the room is the app's plus the largest of any route, an upper bound by design); `RequestID` wrapped inside another middleware (one allocation for a generated id, by design, not measured under load).
