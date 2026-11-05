# Parking idle connections on epoll

Roadmap step 16: a connection idle past `moveAfter` (100 ms) is registered with the engine's poller for one event and its goroutine ends; bytes, a hangup, its idle timeout on the wheel, Shutdown or a WebSocket's own close start a goroutine for it again. This measures it against the same binary with `WithoutParking` ("off") and against the tree before the step ("base"), on idle memory, throughput, p99, allocations, and the case parking makes costlier: requests that each find their connection idle. The decision it fed is in [ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md).

## Setup

- **Machine:** AMD Ryzen 7 9700X (8 cores, 16 threads), Linux 7.2.8-9-omarchy, Go 1.27.3. Commit 6b086e4 with the uncommitted tree of milestone 2 (steps 3 to 15) and this step's changes; "base" is the same tree without this step (the staged index exported with `git checkout-index`), built with `-tags base` in the same session from the same flags (`-trimpath -ldflags "-s -w"`). Run on 2026-11-01 from 11:35 to 12:01 WIB.
- **Pinning:** server on CPUs 2, 3, 10, 11 (cores 2 and 3), client on 4 to 7 and 12 to 15 (cores 4 to 7), from `lscpu -e`. Client connections from 127.0.0.2 to 127.0.0.5.
- **Interleaving:** three rounds; each round runs every experiment on on, off and base in turn. Raw output: `out/<exp>-<variant>-<round>.json`; the load average before every run in `out/loadavg.txt`.
- **Load:** before the idle block a `zig` build and a process named `outpost` of another session each held one core (load average about 3, the `zig` build on CPU 2 for a while); the idle block was started once `zig` had ended and ran with `outpost` still on one core (load 1.8 to 3.6). Memory figures are not sensitive to it (spreads under 0.5%). The load and wake blocks ran with nothing else busy in `top`; their load average rises to about 10 because the benchmark itself keeps twelve threads busy, and falls back during the wake block. No block was repeated.
- **Server:** `server/` here: the routes of `bench/servers/nilogo` (GET and POST `/users/{id}`, the same Inputs and answers), the WebSocket echo route of `docs/research/ws-gate`, plain or TLS (self-signed ecdsa P-256), `-park on|off`. Its SIGUSR1 handler runs two collections and reports heap plus stack from `runtime/metrics`, as `bench/common` does.
- **Clients:** `bench/client` for the idle, load and allocation experiments of HTTP (the harness of milestone 1), `client/` here for the WebSocket idle and load experiments and the wake scenario.
- **Run:** `BASE_DIR=<export> BIN=<outside the repository> ./run.sh idle load wake`, then `python3 -I analyze.py` (its output is `summary.txt`).

## Idle memory, marginal from 10,000 to 20,000 connections

Each HTTP connection served one GET, each WebSocket exchanged one message; snapshots after a 5 s settle. Mean of three runs, min to max in brackets, bytes a connection.

| | parking on | off (`WithoutParking`) | base (before the step) |
|---|---|---|---|
| plain, heap plus stack | **894** [890-896] | 3,252 [3,251-3,254] | 3,227 [3,221-3,232] |
| plain, RSS 0 to 20k | **3,479** [3,216-3,958] | 5,184 [5,116-5,225] | 5,179 [5,105-5,222] |
| plain, RSS 10k to 20k | 768 [268-1,655] | 4,118 [3,974-4,191] | 4,259 [4,190-4,397] |
| TLS, heap plus stack | **7,581** [7,577-7,585] | 11,994 [11,990-11,999] | 11,966 [11,961-11,970] |
| TLS, RSS 0 to 20k | **17,440** [17,433-17,446] | 22,777 [22,639-22,951] | 22,568 [22,532-22,637] |
| TLS, RSS 10k to 20k | 16,568 [16,070-16,925] | 21,799 [20,312-22,861] | 21,584 [20,729-22,431] |
| WebSocket, heap plus stack | **1,642** [1,632-1,661] | 3,966 [3,964-3,969] | 3,934 [3,932-3,938] |
| WebSocket, RSS 0 to 20k | **4,706** [4,697-4,711] | 5,907 [5,871-5,975] | 5,867 [5,865-5,871] |
| WebSocket, RSS 10k to 20k | 2,584 [2,508-2,723] | 4,767 [4,627-4,838] | 4,835 [4,834-4,836] |
| goroutines at 20,000 | 4 | 20,003 | 20,003 |
| stacks a connection | 0 to 7 | 2,048 to 4,099 | 2,048 to 4,099 |

- **Heap plus stack is the figure to read.** Parking removes the goroutine (its 2 KiB stack, 4 KiB under TLS, and its `g`, which the next goroutine started reuses) and adds a `parkState` of about 180 B (the raw socket, two method values, the wheel node of the idle timeout, the registration's state) and one pointer in the poller's table. Plain: -2,358 B (-73%). TLS: -4,413 B (-37%; the rest is crypto/tls's cipher state and record buffers). WebSocket: -2,324 B (-59%). The spreads are under 1%.
- **RSS from 10k to 20k is not a sound measure here.** A parked connection is smaller than the memory the runtime already holds resident from the first 10k (garbage of the openings, collected but not returned to the kernel), so the second 10k partly fit in pages that were already counted: 268 to 1,655 B plain over three runs. RSS from 0 to 20k (the whole process over the connection count, fixed costs included) is steadier and is the one quoted; it falls by 1.7 KB plain, 5.3 KB TLS and 1.2 KB WebSocket.
- **Against fib** ([`bench/result/fib.md`](../../../bench/result/fib.md), another session, the same harness for plain): fib held 980 B of heap plus stack and 1,072 B of RSS (10k to 20k) an idle connection; parked nilo-go holds 894 B of heap plus stack. The goroutine was the gap, as ADR 0005 said.
- **Off against base:** +25 B plain, +28 B TLS, +32 B WebSocket of heap plus stack, outside the spread. The connection struct grew from 352 to 360 B (`pk` and `ps`) and stays in the 384 B size class, so its fields do not explain it; it was not traced. That is what `WithoutParking` costs over the tree before.

The gates in the tests measure 200, 1,200 and 2,200 connections in-process, through the whole app: parked 1,170 B plain, 7,606 B TLS, 1,466 B WebSocket from 1,200 to 2,200 (the first run with parking built). Their ceilings are set 20% above: 1,400, 9,100 and 1,800 B, beside the old ceilings (4,500, 14,500, 4,700 B), which now hold `WithoutParking`.

## Throughput, p99 and allocations: busy connections

256 keep-alive connections back to back for 10 s after 3 s of warm-up (WebSocket: 256 sockets, each sending a message and waiting for its echo). A busy connection never waits 100 ms, so it never parks: this is the path every request takes, and parking adds a flag and a branch at the end of an idle wait only.

| | parking on | off | base |
|---|---|---|---|
| GET req/s | 828,955 [822,816-832,078] | 830,756 [825,766-834,404] | 819,375 [812,883-823,519] |
| GET p50 / p99 / p999 µs | 279 / 914 / 2,512 | 279 / 900 / 2,498 | 283 / 921 / 2,598 |
| GET p99 µs, spread | 914 [899-930] | 900 [886-927] | 921 [913-932] |
| GET CPU ns/req | 3,668 [3,662-3,677] | 3,665 [3,657-3,681] | 3,725 [3,716-3,740] |
| POST req/s | 676,051 [669,005-680,336] | 677,026 [674,693-679,959] | 674,048 [672,342-676,671] |
| POST p50 / p99 / p999 µs | 338 / 1,267 / 2,669 | 337 / 1,261 / 2,656 | 338 / 1,265 / 2,792 |
| POST p99 µs, spread | 1,267 [1,236-1,319] | 1,261 [1,254-1,275] | 1,265 [1,241-1,279] |
| GET over TLS req/s | 767,712 [765,269-771,885] | 767,044 [766,758-767,232] | 757,444 [752,722-760,222] |
| GET over TLS p50 / p99 / p999 µs | 298 / 1,026 / 2,589 | 300 / 1,017 / 2,417 | 301 / 1,048 / 2,799 |
| WebSocket echo msg/s | 952,118 [950,015-954,197] | 953,854 [950,122-958,534] | 942,702 [940,938-945,165] |
| WebSocket p50 / p99 / p999 µs | 246 / 782 / 2,414 | 246 / 769 / 2,277 | 250 / 759 / 2,169 |
| allocations a POST | 2.007 [2.006-2.007] | 2.007 [2.006-2.007] | 2.006 [2.006-2.007] |
| bytes a POST | 321 | 321 | 321 |

On against off: every throughput figure is inside the spread of the other (GET -0.2%, POST -0.1%, TLS +0.1%, WebSocket -0.2%), and so is every p99 (+1.6%, +0.5%, +0.9%, +1.7%, inside run-to-run spreads of 30 to 80 µs). Unchanged. The p999 of WebSocket and TLS GET is 140 to 170 µs higher with parking on, inside or at the edge of spreads of 100 to 300 µs: not a finding either way at three runs. Against base this tree is 1.2% (GET), 0.4% (POST), 1.3% (TLS) and 1.0% (WebSocket) faster with parking on or off, inside the 2% two binaries of one source differ by ([`docs/research/m2-request-path/`](../m2-request-path/report.md)); it is not credited to this step. A typed POST stays at 2 allocations and 321 B.

## The wake scenario: every request finds its connection idle

2,000 connections, each sending one request (or one WebSocket message) every 300 ms, jittered by 10%, for 15 s after 3 s of warm-up: 6,657 requests a second, about 100,000 a run, every one arriving after its connection had waited past 100 ms. With parking on, each request wakes a parked connection (the poller's event, a goroutine started, the woken read); off, each is read by a goroutine that moved after 100 ms and is blocked in the runtime's own netpoller.

| | parking on | off | base |
|---|---|---|---|
| plain p50 / p90 / p99 µs | 32 / 76 / 101 | 25 / 42 / 72 | 25 / 42 / 71 |
| plain p99 µs, spread | 101 [99-103] | 72 [69-77] | 71 [68-76] |
| plain p999 µs, spread | 637 [541-711] | 367 [142-631] | 247 [133-379] |
| plain CPU ns/req | 14,287 [14,021-14,621] | 13,586 [13,418-13,921] | 13,586 [13,419-13,820] |
| TLS p50 / p90 / p99 µs | 35 / 78 / 106 | 27 / 44 / 74 | 27 / 44 / 74 |
| TLS CPU ns/req | 14,654 [14,422-15,021] | 14,253 [14,116-14,519] | 14,320 [14,219-14,521] |
| WebSocket p50 / p90 / p99 µs | 32 / 76 / 101 | 25 / 42 / 71 | 25 / 43 / 73 |
| WebSocket CPU ns/req | 13,887 [13,819-14,020] | 13,519 [13,419-13,619] | 13,486 [13,420-13,619] |
| allocations a request | 3.01 | 3.01 | 3.01 |
| goroutines at the end | 650 to 666 | 2,003 | 2,003 |

**What a wake costs:** about **7 µs at the median and 30 µs at p90 and p99** (plain 72 to 101 µs at p99, +40%; TLS 74 to 106; WebSocket 71 to 101), and **0.4 to 0.7 µs of server CPU** a wake (+3% to +5%; for plain the spreads just separate, for TLS and WebSocket they touch). The p999 is higher too (+270 µs plain) but its spread is wide on both sides. The extra time is the poller's thread returning from `epoll_wait` and handing the connection to a fresh goroutine, against the runtime's netpoller waking the goroutine already blocked in its read. **A wake allocates nothing:** 3.01 allocations a request on, off and base. The third, beside the slab and the context of every typed request, is the `*net.OpError` that the timed-out 100 ms read returns (found with an allocation profile of the server under this scenario): every request that follows an idle wait pays it, parked or moved, and it was paid before this step. The goroutines at the end are the connections woken in the last 100 ms (2,000 × 100 / 300 is about 667).

## What it fails at

- **A connection idle a little over 100 ms between requests pays a wake each time**, about 30 µs at p99 and 0.5 µs of CPU, where it paid a move before. A client polling every 150 ms to 1 s is the worst case; a busier one never parks, an idler one pays it rarely. Past ADR 0003's 10% for that p99 in relative terms. Not measured: a park threshold later than the move (park after 1 s, move after 100 ms), which would spare such clients the wake for the memory of their goroutines; it is the change to make if a workload shows the cost.
- **The idle timeout of a parked connection is on the one-second wheel**, so it closes up to one tick late (never early).
- **A WebSocket is woken by every heartbeat's pong**: 670 wakes a second at 20,000 sockets with the 30 s default (arithmetic), each holding a goroutine for 100 ms, about 67 goroutines at a time. Not measured at that scale.
- **One poller goroutine and one OS thread blocked in `epoll_wait` a Server**, made at the first park. A process with hundreds of Servers (a test suite) has hundreds of such threads; `ws_budget_test.go` counted their creation as allocations of a Publish until it waited for its sockets to park before counting.
- **Off Linux, and on in-memory connections, nothing parks**: the wait moves after 100 ms as before. `WithoutParking` gives the same path on Linux.
- **Not measured:** Shutdown's own time with 20,000 parked connections, the wake latency under a CPU-saturated server, and RSS after the runtime returns free pages (which would make RSS 10k to 20k usable).
