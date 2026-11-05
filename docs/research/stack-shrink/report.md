# The stack the collector shrinks under a busy connection's wait

Taken on 2026-11-05, Go 1.27.3, AMD Ryzen 7 9700X, kernel 7.2.8-9-omarchy, at `db35bea` plus the uncommitted slab change of [`docs/research/middleware-p99/`](../middleware-p99/report.md). The before is that tree with `internal/engine/conn.go` taken from `db35bea`, the after the tree with `waitHot`. Both were built in the same session with the same flags. The server ran on cores 2,3,10,11 and the client on 4-7,12-15, with 256 connections, 3 s of warm-up and 10 s measured, over loopback, through `bench/run.sh`. [`docs/research/middleware-p99/`](../middleware-p99/report.md) left this open: with `GODEBUG=gcshrinkstackoff=1` the plain route's p99 fell 18% and three middleware cost +6% of p99 instead of +17%.

## What the collector does (`probe.sh`, `probe-stacks.txt`, `depth.txt`, `gctrace.txt`)

At each cycle Go's collector halves the stack of a goroutine that is using less than a quarter of it. "Using" means from the top down to its stack pointer, plus `stackNosplit` (800 B on amd64). The rule is `runtime/stack.go`, `shrinkstack`, in Go 1.27.3. A connection waiting hot for its next request blocks in `readHot`. Its frames from `serve` to `gopark` are about 650 B of locals by the compiler's `-S` (`serve` 16, `await` 232, `readHot` 56, `net.(*rawConn).Read` 64, `poll.(*FD).RawRead` 88, then the poller's frames down to `gopark`), about 1.65 KiB with return addresses and the guard. A stack of 4 KiB is not halved, since its quarter is 1 KiB, which is what [ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md) had measured. A stack of 8 KiB or more is halved, every cycle.

The request path grows it past 4 KiB. The deepest call chains sampled in a CPU profile of the before (`depth.txt`, frame sizes from `-S`) reach about 4.5 KiB of frames on the plain typed GET, deepest at `textRun.offer` (824 B of locals) under `decodePath` (440) and `handler.Serve` (760). With the runtime's 928 B of guard that needs an 8 KiB stack. Each middleware adds about 1.1 KiB (`chainRun.step` 208, `chainRun.call` 368, the middleware's closure 312, `Next.Call` 200), so three take the chain past 7.7 KiB and the stack to 16 KiB.

Stack bytes a goroutine under load, from `/memory/classes/heap/stacks:bytes` over `/sched/goroutines` every 10 ms with no collection forced ($STACKS, `bench/common`), the median of the middle 60% of the run and its range, single runs:

| GET | before | before, `gcshrinkstackoff=1` | after |
|---|---|---|---|
| none | 12,047 B [6,385 to 13,854] | 9,999 B [9,879 to 9,999] | 10,120 B [10,120 to 10,360] |
| three middleware | 19,516 B [10,722 to 25,660] | 17,709 B [17,589 to 17,709] | 17,709 B [17,468 to 17,950] |

The metric counts the stack memory reserved, including stacks freed and kept by the runtime, so a stack shrunk and grown again shows as more and as a wider range, not less. With shrinking off, or with the change, the figure holds still at the size the requests grew: 8 KiB a connection with no middleware and 16 KiB with three, plus the few other goroutines.

## The change

A hot wait blocks under a frame of padding: `waitHot` in `internal/engine/conn.go` calls `readHot` from one of three functions whose frames hold 1, 3 or 7 KiB. The array that holds the room is declared inside a branch that never runs (`padNever`), so the frame reserves the room and nothing zeroes it. The assembly of `readHotPad1` shows the zeroing loop only behind the `padNever` test. Each wait starts at no padding and reads an address in its own frame before and after the read. A stack can only have been copied while the goroutine was blocked if the collector shrank it, and the new stack is allocated before the old one is freed, so the two addresses differ. A wait that finds them different takes the next tier for the waits after it:

- 1 KiB keeps a stack of 8 KiB, since 2.65 KiB is at least a quarter of it.
- 3 KiB keeps 16 KiB.
- 7 KiB keeps 32 KiB.

A tier is only reached once the stack is large enough to have been shrunk at the tier below, so the padding never grows the stack itself. `serve` resets the tier, since every goroutine (a new connection, a resumed park, a moved wait) starts on a fresh stack. A stack beyond 32 KiB is still halved.

Two tests in `internal/engine/hotwait_test.go` hold this. In the first, a request that grows the stack to 16 KiB is followed by eight full collections, each while the connection waits hot. The tier rose to 2 and stayed there below its cap: `[1 2 2 2 2 2 2 2]`, and `[1 1 ...]` under `-race`, whose larger guard keeps a stack of 8 KiB from being shrunk at all. With the padding taken out, so that every tier calls `readHot` directly, the test fails with `[1 2 3 3 3 ...]`. In the second, a request that grows no stack keeps the tier at 0 through four collections.

## Measured (`run.sh`, `summary.txt`, `raw.jsonl`)

The runs were six variants, three rounds interleaved, each experiment on a fresh server. `mw1` is one middleware that reads a header and adds one, and `mw3` is three of it. Medians are given with the three rounds' range.

| GET | req/s | CPU ns/req | p50 | p99 |
|---|---|---|---|---|
| before, none | 1,009.1k [1,005.6 to 1,014.3] | 2,810 | 230 us | 861 us [850 to 875] |
| after, none | 1,031.7k [1,013.0 to 1,037.3] (+2.2%) | 2,751 | 231 us | **683 us [677 to 733] (-21%)** |
| before, mw1 | 957.1k [949.4 to 970.8] | 3,031 | 243 us | 908 us [876 to 928] |
| after, mw1 | 966.4k [961.4 to 973.0] (+1.0%) | 2,963 | 244 us | **754 us [733 to 760] (-17%)** |
| before, mw3 | 920.4k [914.7 to 924.8] | 3,205 | 251 us | 1,008 us [1,002 to 1,024] |
| after, mw3 | 940.8k [927.8 to 942.4] (+2.2%) | 3,088 | 252 us | **744 us [744 to 781] (-26%)** |

| POST | req/s | CPU ns/req | p50 | p99 |
|---|---|---|---|---|
| before, none | 882.2k [876.7 to 890.4] | 3,358 | 259 us | 1,011 us [993 to 1,026] |
| after, none | 908.9k [892.9 to 920.9] (+3.0%) | 3,284 | 261 us | **785 us [756 to 833] (-22%)** |
| before, mw1 | 847.9k [837.3 to 854.9] | 3,582 | 272 us | 1,030 us [1,006 to 1,066] |
| after, mw1 | 863.3k [860.2 to 868.3] (+1.8%) | 3,498 | 273 us | **825 us [813 to 829] (-20%)** |
| before, mw3 | 813.4k [793.9 to 814.1] | 3,806 | 281 us | 1,166 us [1,153 to 1,224] |
| after, mw3 | 838.8k [819.3 to 853.5] (+3.1%) | 3,641 | 282 us | **834 us [799 to 916] (-28%)** |

p50 is unchanged. The p99 the single runs of `gcshrinkstackoff=1` gave (674 us with no middleware, 727 us with three, [`probe-stacks.txt`](probe-stacks.txt) and [`raw-probe.jsonl`](raw-probe.jsonl)) is about what the change reaches. Allocations are 2.003 to 2.004 a request and 304 to 305 B in every variant, before and after.

Against the route with no middleware of the same tree, the after:

| | GET req/s | GET p99 | POST req/s | POST p99 |
|---|---|---|---|---|
| mw1 | -6.3% | +10% (+3%, +7%, +12% round by round) | -5.0% | +5% |
| mw3 | -8.8% | +9% | -7.7% | +6% |

Every figure is inside the 10% of [ADR 0003](../../adr/0003-the-trade-budget.md) except one middleware's GET p99, which sits at its edge. That figure varies with the round as the route with no middleware does (677 to 733 us), and the session of `docs/research/middleware-p99/` measured it at +3%. Three middleware were +17% of p99 before.

**The collector runs less often.** In GET cycles a second (from the $STACKS logs, median of the three rounds), none went from 97 to 82, mw1 from 97 to 80 and mw3 from 94 to 60. On POST, none went from 132 to 114 and mw3 from 126 to 80. Turning shrinking off does not do this (93 a second with three middleware), so it is the padding. `GODEBUG=gctrace=1` with three middleware (`gctrace.txt`, cycles after the 300th) shows that before, and before without shrinking, every cycle had a 4 MB goal and "0 MB stacks". After, every cycle had a 5 MB goal and "1 MB stacks". Go's heap goal is twice the live heap plus the stack and globals scanned at the last cycle, with a floor of 4 MB (`runtime/mgcpacer.go`, `commit`), and the padding is scanned stack: 256 connections waiting under 3 KiB is 768 KiB more. So the heap goal is the padding's size over the floor, and the collector runs less because the heap may grow that much more before it starts. This is the change's only new memory: about 1 MB of heap goal at 256 busy connections, which grows with the connections waiting hot.

**Idle connections** (`bench/run.sh idle`, one run a tree, heap plus stack marginal from 10,000 to 20,000 connections): 937 B before and 939 B after. A hot wait lasts at most `moveAfter` (100 ms); a quiet connection then parks or moves, and its goroutine ends with its stack and the padding.

## What it fails at

- A connection waiting hot keeps the stack its requests grew for up to 100 ms, where the collector would have halved it. That is no memory a parked connection holds. For a busy one, the stack its next request grows back to is the same size anyway.
- A stack beyond 32 KiB is still halved at each cycle and regrown.
- A goroutine pays one shrink per tier before it settles: at most two for a stack of 16 KiB, after each park or move.
- The heap goal rises by the padding's size, as above.

## Rejected

- **`gcshrinkstackoff`**: a `//go:debug` line is honoured only in the main package, and `GODEBUG` belongs to whoever runs the binary.
- **A fixed padding.** 1 KiB keeps nothing above 8 KiB. 7 KiB grows a stack that a small route would have kept at 4 KiB, and is that much more scanned stack at every cycle.
- **A shallower request path alone.** Trimming `handler.Serve` and `textRun.offer` might keep the plain typed route inside 4 KiB. Any handler that calls a driver or `fmt`, and any middleware, would still take it to 8 KiB or more. Not measured.
- **Running the request on another goroutine**, so that the waiting one stays small: [ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md) rejected a pool of workers for its hand-off and wake.

## Not measured

TLS, where the wait's frames are deeper and the tier may settle lower; a WebSocket's hot wait, which takes the same path; and handlers deeper than the bench's.
