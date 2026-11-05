# nilo-go beside fib

**Run:** `cd bench && ulimit -n 524288 && OUT=out/fib-compare SERVERS="nilogo fib" EXPS="get post idle block allocs" ./run.sh`, three interleaved rounds, on 2026-10-31 from 10:53 to 10:59 WIB. Same machine, pinning, client and routes as [`milestone1.md`](milestone1.md), but built with Go 1.27.3: `bench/go.mod` asks for it since fib v1.6.0 needs it, so the toolchain switched. The nilo-go figures here are compared with fib's from the same run, not with `milestone1.md`'s. Tree: the same uncommitted tree as `milestone1.md`, plus `/block` routes in the bench servers.

**What fib is here:** [fib](https://github.com/lesismal/fib) v1.6.0, an event-loop engine, in `fib.DefaultConfig()` with the synchronous handler (the mode `docs/research/engines/` measured). Its routing, JSON scanning and answers are written by hand, with the same validation rules as nilo-go's route and a plain 400 for any mistake. So it is compared as an engine with hand-written code, not as a typed framework: its CPU and allocation figures are its engine's and that code's together.

Every figure is the mean of three rounds with the min to max in brackets.

| | nilo-go | fib |
|---|---|---|
| allocations a POST | 2.01 [2.01-2.01] | 6.03 [6.03-6.03] |
| bytes allocated a POST | 321 | 280 [279-281] |
| GET req/s | 887k [883k-894k] | 1,090k [1,080k-1,099k] |
| GET CPU ns/req | 3,506 [3,471-3,527] | 2,429 [2,413-2,443] |
| GET p50 / p99 / p999 µs | 268 / 743 / 1,934 | 242 / 545 / 789 |
| POST req/s | 736k [734k-739k] | 936k [930k-943k] |
| POST CPU ns/req | 4,430 [4,405-4,442] | 3,084 [3,059-3,099] |
| POST p50 / p99 / p999 µs | 321 / 906 / 2,217 | 284 / 626 / 873 |
| peak RSS under load, MB | 20.2 | 27.6 |
| `block`: req/s (1,000 connections, 10 ms sleep; ideal 100k) | 92.4k [91.9k-93.3k] | 92.4k [91.8k-93.0k] |
| `block`: p99 µs | 12,182 [11,811-12,371] | 12,208 [12,118-12,306] |
| `block`: peak RSS (VmHWM), MB | 31.9 [31.2-33.3] | 76.5 [75.8-77.9] |
| idle a connection, RSS, B (10k to 20k) | 3,972 [3,971-3,973] | 1,072 [1,072-1,073] |
| idle a connection, heap plus stack, B | 3,162 [3,157-3,165] | 980 [971-987] |
| goroutines at 20k idle connections | 20,003 | 5 |
| binary, MB | 4.05 | 6.18 |

No run had an error.

## What it says

- **Idle connections:** fib holds 3.7 times less a connection, because an idle connection has no goroutine at all, only its registration on epoll and its buffers' bookkeeping. This is the gap [ADR 0005](../../docs/adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md) chose to accept, and it is the same as `docs/research/engines/` found.
- **CPU and throughput:** fib serves 23% more GETs and 27% more POSTs at 31% less CPU a request, with a lower tail (p999 about 0.8 ms against 1.9 to 2.2 ms). How much of that is the engine and how much is the typed decoding and problem+json answers nilo-go does and fib's hand-written route does not is not known from this run; a CPU profile of both is the next measurement.
- **Handlers that block:** both reach the same throughput and latency, close to the ideal, but fib's worker pool grows to a goroutine a blocked handler on top of its loops, and it peaks at 2.4 times nilo-go's memory. This is the case ADR 0005 was decided on, and it holds.
- **Allocations:** nilo-go makes 2 a request against fib's 6, though fib's are smaller.
