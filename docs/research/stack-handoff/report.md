# The stack a request leaves on an idle connection

**The question** ([ADR 0005](../../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md), [`docs/roadmap.md`](../../roadmap.md)): an idle connection that had served one typed request held 5.1 to 5.4 KB, not 3.1 to 3.5, because its goroutine's stack grew from 2 KiB to 4 KiB during the request and Go never shrinks the stack of a goroutine blocked in a read. Restate the figure, or move the waiting off the goroutine that ran the request?

**Machine and tree:** Ryzen 7 9700X, Go 1.27.2, on the uncommitted milestone 1 tree of 2026-10-31. The before is a copy of that tree taken in the same session, built with the same flags (`go.before.mod` points at it).

## What was run

- **Idle memory:** `idle_test.go` (through the whole app, a typed route) and `internal/engine/budget_test.go` (bare engine): marginal heap plus stack from `runtime/metrics`, client in a child process, each connection served one request and then left idle, three runs each.
- **Throughput and cost:** `main.go` here, built against both trees: a nilo-go app on cores 2,3,10,11, a keep-alive client of 256 connections on 4-7,12-15 spread over 127.0.0.2 to .5, 2 s warm and 8 s measured, CPU per request from the server's `/proc/<pid>/stat`. Two routes: `/ping/{id}`, a typed route, and `/deep/{id}`, the same with a handler that uses about 6 KiB of stack, standing in for a driver or `fmt`. `run.sh` interleaves before and after, three rounds; the raw lines are `every-request.jsonl` and `after-idle.jsonl`.

## Two designs measured

**A move on every request:** after each request with nothing pipelined, `go c.serveFn()` and return, so every wait is on a fresh 2 KiB stack. Idle memory came back to 3.1 to 3.5 KB through the whole app (stack 2,031 B a connection). It costs too much (`every-request.jsonl`, three rounds, spread in brackets):

| route | before req/s | after req/s | before CPU ns/req | after CPU ns/req | before p99 µs | after p99 µs |
|---|---|---|---|---|---|---|
| `/ping` | 840k to 844k | 663k to 666k (-21%) | 3,531 to 3,550 | 4,782 to 4,793 | 779 to 791 | 988 to 1,006 |
| `/deep` | 810k to 813k | 530k to 531k (-35%) | 3,736 to 3,748 | 6,301 to 6,326 | 851 to 867 | 1,242 to 1,243 |

The goroutine itself is cheap; the cost is that every request now grows a fresh stack again (2 to 4 KiB, and to 8 KiB for `/deep`, each a copy), plus a goroutine exit and start and a scheduler hop. That is outside the 10% of [ADR 0003](../../adr/0003-the-trade-budget.md).

**A move after 100 ms idle (kept):** the idle read first waits `moveAfter` (100 ms); a connection still idle then hands the rest of its wait to a fresh goroutine, once per idle period. A busy connection never moves, and the deadline it sets is the one it set before, with a different time. Measured (`after-idle.jsonl`):

| route | before req/s | after req/s | before CPU ns/req | after CPU ns/req | before p99 µs | after p99 µs |
|---|---|---|---|---|---|---|
| `/ping` | 840k to 841k | 855k to 860k | 3,553 to 3,561 | 3,448 to 3,470 | 782 to 789 | 767 to 779 |
| `/deep` | 796k to 803k | 813k to 824k | 3,766 to 3,797 | 3,651 to 3,700 | 893 to 930 | 880 to 919 |

The after is about 2% faster on every round. Nothing in the change does less work on a busy connection, so this is read as unchanged (code layout or run order), not as a gain. Allocations per request are unchanged: the budget tests pass, because `serveFn` is the method value made once a connection, so `go c.serveFn()` allocates nothing.

Idle memory, three runs each:

| | before | after |
|---|---|---|
| whole app, 1,200 to 2,200 connections | 5,116 to 5,125 B | 3,135 to 3,433 B |
| whole app, 200 to 1,200 connections | 5,404 to 5,410 B | 3,960 to 4,189 B |
| stack alone, 1,200 to 2,200 | 4,096 B | 2,031 to 2,326 B |
| bare engine, 1,200 to 2,200 | (not run) | 3,138 to 3,181 B |

The first step is higher after than before the second. The likely reason, not measured: a goroutine that moves starts its successor before it exits, so a thousand connections moving at once leave a thousand dead goroutine structs (about 400 B each) on the runtime's free list, which Go keeps and reuses for the next thousand. That is a cost of the most connections that ever moved at once, not of each idle connection.

## What it fails at

- **A client that paces requests just past 100 ms** pays a move on every request: about 1.3 µs of CPU for `/ping` and 2.6 µs for `/deep`, from the every-request run. At under ten requests a second a connection, that is under 0.003% of a core.
- **A connection idle for less than 100 ms** still holds its grown stack. Such a connection is busy, not idle; the idle-memory figure is about the many that wait for seconds.
- **The stack while a request runs** is not reduced: a handler that needs 8 KiB has 8 KiB while it runs, as before.
- **The 10k to 20k RSS run** of milestone 1's benchmark is still to replace these `runtime/metrics` figures.

## Not measured

- **A pool of workers** that runs request work while the connection's goroutine waits: each request would cost a hand-off to a worker and a wake of the waiter, the same kind of scheduler work that made the every-request move cost 21%, and the workers keep grown stacks. It was not built, so this is a claim.
- **Restating the figure** costs nothing to build and keeps 2 KiB more of every idle connection, which the after-idle move removes with no measured cost.
