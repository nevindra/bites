# Where fib's lower CPU per request comes from: CPU profiles of nilo-go and fib under the same load

A measurement with no code change, taken on 2026-10-31 as the "profile before the request-path steps" of [`docs/roadmap.md`](../../roadmap.md) (Milestone 2, and the first look that step 17 will repeat). It answers the question left open by [`bench/result/fib.md`](../../../bench/result/fib.md): fib serves GET at 2,429 CPU ns a request against nilo-go's 3,506, and POST at 3,084 against 4,430. Where do those ~1,000 and ~1,350 ns go, and how much of it is the engine model and how much is nilo-go doing typed decoding and problem+json where fib's route is hand-written?

## What was run

- **Machine:** AMD Ryzen 7 9700X (8 cores, 16 threads), Linux 7.2.8-9-omarchy, governor `performance`. Server pinned to CPUs 2,3,10,11 and the client to 4-7,12-15, as `bench/run.sh` does.
- **Toolchain:** go1.27.3 (the installed 1.27.2 switched to it through `GOTOOLCHAIN=auto`, because this module, like `bench/go.mod`, requires `go 1.27.3` for fib v1.6.0).
- **Tree:** the uncommitted tree of 2026-10-31 (no commit exists yet; 33 paths untracked before this folder was added), the same tree as `bench/result/fib.md`.
- **Servers:** `servers/nilogo/main.go` and `servers/fib/main.go` are copies of `bench/servers/*/main.go` that differ by two lines (the import path of `common`, and a `prof.Start()` call); `common/common.go` is byte-identical. `prof.Start()` arms a handler: on SIGUSR2 the process writes a CPU profile of 10 s, sampled at 500 Hz (`PROF_HZ`, instead of the default 100, so a 10 s run holds about 31,000 samples a profile). Built with `-trimpath` and symbols (the bench strips them; stripping does not change the code).
- **Client:** `bench/client` unchanged, built from the `bench` module, 256 keep-alive connections over source addresses 127.0.0.2 to .5 (the client does that), 3 s warm-up, then 11 s measured; the signal for the profile is sent 3.2 s after the client starts, so the 10 s profile lies inside the measured window.
- **Runs:** `run.sh`, three rounds, interleaved (round: GET nilogo, GET fib, POST nilogo, POST fib), a fresh server for each. 12 profiles. For the same 10 s window the script also reads the server's `utime`/`stime`, `/proc/<pid>/io` (`syscr`, `syscw`) and context switches from `/proc`, before and after.
- **Attribution:** `analyze.py` reads each profile through `go tool pprof -traces` and gives every sample to one bucket: first the syscall it is in (write, read, epoll, futex), then GC, then deadline code, then the engine clock, then the leaf-most frame that a regex rule names (list in the file). Nanoseconds a request are the bucket's CPU time divided by the requests of the window (the client's rate times 10 s). The totals match the process's own `utime+stime` per request to within 0.1%. Less than 0.3% of the samples fell in no bucket.
- **Syscall counts:** `strace` and tracepoints are not available (`perf_event_paranoid=2`, no sudo, no strace), so `syscount/` is a small ptrace counter (the equivalent of `strace -c -f`), driven by `syscalls.sh`. It slows the server a lot, so it is used for counts only. With 256 connections that slowdown makes every wake-up find more requests ready, so epoll_wait and futex counts at 256 connections are meaningless and are not quoted; the counts at one connection show a request with nothing to batch. The reads per request under real load come instead from `syscr` in `/proc/<pid>/io` over the window (which counts `read(2)` only, so it is blank for fib, whose sockets use `recvfrom`/`sendto`).
- **The profiler's own cost:** at 500 Hz the figures are 1 to 2% worse than `bench/result/fib.md` (nilo-go GET 3,572 CPU ns a request against 3,506, fib 2,441 against 2,429; throughput 876k against 887k and 1,070k against 1,090k). Both servers pay it, and the comparison inside this run is what is quoted.

Everything under `out/` is kept: `out/run-*.txt` (the window counters and the client's JSON), `out/pprof/*.top.txt` and `*.cum.txt` (`-top -nodecount=60`, and with `-cum`, for each of the 12 profiles), `out/syscalls-*.json`, and `out/analysis.txt` (the tables below, all rounds). The binary profiles are under `~/.cache/nilo-go-profile/prof/`.

## The per-bucket table

CPU nanoseconds a request, mean of three rounds with min to max in brackets. "Gap" is nilo-go minus fib. The two servers do not have the same buckets, so a row that is zero for one of them means that server has no such code, not that it is free.

### GET /users/{id}

| bucket | nilo-go | fib | gap |
|---|---|---|---|
| **total** (profile) | **3,572** [3,560-3,581] | **2,441** [2,435-2,447] | **+1,131** |
| syscall write (the one `write`/`sendto`, kernel TCP and loopback delivery included) | 1,570 [1,549-1,586] | 1,312 [1,299-1,326] | +259 |
| syscall read (nilo-go: 3 `read`; fib: 1 `recvfrom`) | 475 [461-499] | 170 [167-175] | +305 |
| syscall epoll_wait / epoll_ctl | 57 [53-59] | 54 [53-55] | +3 |
| futex | 2 | 2 | 0 |
| deadlines (`SetReadDeadline`, `SetWriteDeadline`, runtime timers) | 388 [382-397] | 102 [100-104] | +286 |
| engine clock (`Server.now()` four times a request, and `dateLine`) | 142 [139-144] | 0 | +142 |
| other `time.Now` / nanotime | 22 | 38 | -17 |
| Go scheduler and netpoll (park, ready, `findRunnable`, `netpoll`) | 115 [104-128] | 19 [17-20] | +96 |
| GC (mark, assist, sweep) | 77 [72-80] | 23 [22-24] | +55 |
| allocation (`mallocgc`) | 61 [59-63] | 27 [26-28] | +34 |
| HTTP parse (`internal/h1`; fib's `http.Parser`) | 94 [89-99] | 170 [167-176] | -76 |
| response head and write path (nilo-go `outPlan` and `Response`; fib `Respond`, `sendResponse`, `writeHTTP1`) | 39 [36-42] | 116 [113-117] | -77 |
| engine and connection glue (nilo-go `internal/engine` 173 and `net`/`poll` wrappers 113; fib `Connection`, `Engine`, `taskpool` 388) | 286 | 388 [378-401] | -102 |
| router (`internal/router`) | 48 [47-48] | 0 | +48 |
| decode and JSON codec (root decode, `jsoncodec`, `wire`) | 127 [123-132] | 0 | +127 |
| request context and typed-route glue (`reqctx`, root `serve`) | 65 [58-73] | 0 | +65 |
| handler (user code; fib's hand-written route) | 0 | 19 [17-21] | -19 |
| unattributed | 6 | 2 | +3 |

Process view of the same window: user 1,612 ns and system 1,964 ns a request for nilo-go, user 953 and system 1,495 for fib. Throughput in the window 876k [873k-878k] against 1,070k [1,036k-1,089k] a second (one fib round was lower; the others are 1,085k to 1,089k).

### POST /users/{id}?page=2&expand=true

| bucket | nilo-go | fib | gap |
|---|---|---|---|
| **total** (profile) | **4,543** [4,540-4,547] | **3,112** [3,105-3,121] | **+1,431** |
| syscall write | 1,606 [1,593-1,631] | 1,451 [1,437-1,463] | +155 |
| syscall read | 501 [500-502] | 193 [186-201] | +308 |
| syscall epoll | 59 [58-60] | 53 [51-55] | +6 |
| futex | 3 | 2 | +2 |
| deadlines | 462 [456-466] | 103 [94-110] | +359 |
| engine clock (`Server.now()`, `dateLine`) | 185 [185-186] | 0 | +185 |
| other `time.Now` / nanotime | 28 | 37 | -9 |
| Go scheduler and netpoll | 141 [138-147] | 23 [17-28] | +118 |
| GC | 130 [120-138] | 46 [46-47] | +84 |
| allocation | 78 [75-82] | 87 [82-92] | -9 |
| HTTP parse | 180 [172-184] | 268 [266-269] | -89 |
| response head and write path | 38 [34-40] | 113 [108-117] | -75 |
| engine and connection glue (nilo-go engine 195 plus `net`/`poll` 136) | 330 | 565 [563-567] | -235 |
| router | 53 [51-57] | 0 | +53 |
| decode and JSON codec | 621 [606-628] | 0 | +621 |
| request context and typed-route glue | 119 [108-132] | 0 | +119 |
| handler (fib: scanner, `strconv`, answer by hand) | 1 | 169 [165-175] | -168 |
| unattributed | 8 | 3 | +5 |

User 2,493 and system 2,055 ns for nilo-go, user 1,461 and system 1,660 for fib. Throughput 722k [721k-722k] against 929k [926k-931k].

The spread of every bucket that matters here (all but futex, GC and the scheduler) is within a few percent of its mean, and the ranges of the two servers never meet, so the sign and rough size of each gap is stable across the three rounds. The scheduler (GET 104 to 128) and GC (POST 120 to 138) buckets are the noisy ones, as small shares of a 10 s window.

### Syscalls and switches a request

| | nilo-go | fib |
|---|---|---|
| `read`/`recvfrom` a request, under load (256 connections, `syscr` over the window) | **2.996** [2.995-2.998] (GET and POST) | 1 (ptrace count, 1.00) |
| `write`/`sendto` a request, under load | **0.999** (`syscw`) | 1 (ptrace count, 1.00) |
| reads, one connection, ptrace count | 2.00 to 2.01 | 1.00 |
| `epoll_wait`, one connection, ptrace count | 1.75 (GET), 1.87 (POST) | 1.27 (GET), 1.30 (POST), plus `epoll_ctl` 0.43 to 0.46 |
| `futex`, one connection, ptrace count | 0.00 | 1.44 (GET), 1.51 (POST) |
| `nanosleep`, one connection | 0.04 | 0.26 |
| `epoll_wait` and `futex` under 256-connection load | not measured (see above) | not measured |
| voluntary context switches a request, 256 connections, from `/proc` | 0.003 (GET), 0.004 (POST) | 0.008 (GET), 0.006 (POST) |
| threads in the process | 11 to 12 | 11 to 12 |

So ADR 0005's "a request is read in two calls" is two *useful* reads (the one-byte read that waits, then the rest of the head), and under load a third: the Go runtime always tries a read before it parks, so when the next request has not arrived yet the one-byte read costs an `EAGAIN` read and then a second one-byte read after the netpoller wakes the goroutine. At one connection with a slowed server the request is already there and the count is 2.0; at full speed it is 3.0, exactly (2.996). fib reads once, after epoll has said the socket is readable, and writes once. Both write exactly once a request. The epoll_wait cost per request is the same in the two profiles (57 and 54 ns); fib's one-connection futex count is its worker pool handing a connection to a worker, which at 256 connections shows up as 0.008 voluntary context switches a request, so it is small there.

## Where the ~1,100 ns goes

Grouping the rows above (GET first, POST in brackets), in CPU nanoseconds a request:

| group | gap, GET | gap, POST | share of the gap |
|---|---|---|---|
| syscalls (read 305, write 259, epoll 3; POST read 308, write 155) | +567 | +471 | 50% / 33% |
| deadlines, engine clock and other clock | +411 | +535 | 36% / 37% |
| scheduler and netpoll | +96 | +118 | 8% / 8% |
| GC and allocation | +88 | +75 | 8% / 5% |
| HTTP engine proper (parse, response head, glue; nilo-go is cheaper here) | -256 | -398 | -23% / -28% |
| framework work fib's route does not do (router, decode and codec, request ctx glue, handler) | +221 | +626 | 20% / 44% |
| unattributed | +3 | +5 | |
| **gap** | **+1,131** | **+1,431** | |

**Which buckets account for the gap.** On GET, half of it is syscalls (the extra reads 305 and a dearer write 259) and over a third is time-keeping: `SetReadDeadline` and `SetWriteDeadline` 286 and the four `Server.now()` calls 142. The scheduler's park and ready is 96, GC and allocation 88. Against these, nilo-go's own HTTP code is cheaper than fib's by 256 ns (its parser, its response head and its glue cost less than fib's equivalents), and the typed framework adds 221 on GET and 626 on POST.

**Engine model versus typed framework.** The framework part (router 48, decode and codec 127, request context glue 65, minus what fib's hand-written handler costs, 19) is **+221 ns of the 1,131 on GET (20%) and +626 of 1,431 on POST (44%)**. That is the cost of typed decoding, the Input plan and the codec, and it is the part that a hand-written route avoids; it is mostly the POST body: `jsoncodec.Decoder.Decode` alone is 2.6 s of a 32.8 s profile, 365 ns a request, against fib's whole scanner and answer at 169, and `decodeQuery` is 97 ns. The remaining 80% of the GET gap and 56% of the POST gap (about 910 and 805 ns) is not typing: it is how the engine waits, reads, arms timeouts and takes the time, and it is the same for a route with no decoding at all. Inside it, two things belong to the goroutine-per-connection model itself and one to how nilo-go's engine is written:

- **Model:** the scheduler bucket (park and ready of a goroutine, 96 to 118 ns) and the extra reads that come with a blocking read of one byte (a speculative `EAGAIN` read, the one-byte read after the wake-up, and the rest of the head: about 300 ns). fib's event loop learns from epoll that bytes are there and reads them all in one call. Both are what ADR 0005 chose (a pooled buffer held only while a request is in flight, and a blocking read that costs a goroutine). The `EAGAIN` read is Go's `net` always trying a read before it parks; the one-byte read is nilo-go's choice to hold no buffer while it waits.
- **Engine code, not model:** deadlines and clocks (GET 411, POST 535). By reading the source the engine sets about five deadlines on a plain request (`awaitRequest`, `readHead` and its clear after the head, and `write` with its clear; `exchange.go` adds more for a body) and calls `Server.now()` four times (`awaitRequest`, `readHead`, `write`, `dateLine`). fib arms one read deadline a request (102 ns) and takes the time almost never. Each `SetXDeadline` costs about 75 ns (`setDeadlineImpl` is 10.6% of the whole GET profile, 3.3 s of 31.2 s, `runtime_pollSetDeadline` 2.6 s of it, `time.Until` 0.47 s) and each `now()` about 33 ns (`clock.real.Now` 1.17 s). This is a design choice, not a property of goroutines, and it is the biggest single item in the profile after the syscalls.
- **Unexplained:** the write syscall is 259 ns dearer on GET and 155 dearer on POST, with the same one write a request and similar response sizes. It is kernel time for TCP send and loopback delivery plus waking the client, and it is the largest single bucket in both servers (44% of nilo-go's GET). **Claim, not measured:** a slower server leaves the client threads asleep more often, so each write pays a wake-up the faster server's writes do not; the evidence is only that the cost differs while the call does not, and `perf` could not show it.

GC and allocation (88 on GET) come from the two allocations a request that ADR 0003 budgets: the GC assist is charged to `wire.(*Slab).Init`'s `make` (550 ms of a 32.8 s POST profile, 76 ns a request), because the slab is a fresh buffer on purpose (ADR 0008: nothing pooled reaches the user). Scanning goroutine stacks is under 0.3% of the samples, so the 256 goroutines are not what makes GC dearer here; the cause of the rest (130 against 46 on POST) was not isolated (the GC cycle count was not recorded). It is a price of ADR 0008, not of the engine.

## Ranked candidates for nilo-go

Gains are read from this profile as the bucket that would shrink, in ns a request on GET (POST in brackets), and as a share of nilo-go's 3,572 (4,543). They are estimates from per-call costs, **claims until built and measured** against ADR 0003 (and the memory axis where marked).

| # | candidate | kind | expected gain | roadmap step |
|---|---|---|---|---|
| 1 | **Fewer deadline arms**: arm one read deadline for the request (head and idle merged) and one write deadline, and do not clear a deadline that nothing will wait on; fib's floor is one arm, 100 ns, so the reachable saving is the deadlines bucket minus about 100 to 150 | engine | about 240 to 290 (POST 310 to 360), 7 to 8% | 17, but design it with 16: parking moves the idle timeout to the wheel (ADR 0039) and the head and write timeouts must keep Limits' exact meaning, so this needs an ADR edit |
| 2 | **One clock read a request**: `Server.now()` once when the request's first byte arrives, handed to the deadline code and `dateLine` (the injected `Clock` of ADR 0049 stays, called once) | engine | about 100 (POST 140), 3% | 17 |
| 3 | **Read the head in one call, not two**: the idle wait reads into a small inline array in the connection instead of one byte, so the head is usually in hand after the wake-up and the second read goes away; a read costs about 160 ns here (475 / 3) | engine | about 160, 4 to 5% | 16: it is the code that step rewrites. **Costs the memory axis**, one array's size on every idle connection (3,157 B today), so it needs the idle-memory number in ADR 0005 and may fit only for connections inside `moveAfter` |
| 4 | **Avoid the speculative `EAGAIN` read** by parking before the first read, with `syscall.RawConn.Read` returning false first | engine | about 160 | not recommended: with edge-triggered epoll a readiness edge that arrived before `prepareRead` would be lost, so it can hang; listed only so the question "why not" has an answer. Test before any claim |
| 5 | **A cheaper body decode**: `Decoder.Decode` runs `jsontext.ReadValue` over the body (850 ms, 118 ns) and then walks it again in `state.object`; one pass would remove the first | framework | up to about 100 on POST, 2% | 17, after steps 8 and 9, which change the decode path |
| 6 | **Query and path decoding** (`decodeQuery` 97 ns on POST, `wire.EachQuery` 90 ns, router 48 to 53) | framework | 30 to 60 | 17; low priority, the typed contract needs most of it |
| 7 | **Slab right-sizing** so the per-request `make` stays at the smallest size the route needs (the GC assist is charged to it) | framework | up to about 50, unmeasured | 17; bound by ADR 0008 and the 2-allocation budget |
| 8 | **Scheduler and netpoll** (115 to 141) | engine, the model | none to take | 16: the parking prototype must not raise this bucket for busy connections; compare it against 115 on GET before and after |
| 9 | **The write syscall** (1,570, 44% of the profile) | kernel | none known | one write a request is already the minimum; do not spend time here until `perf` with kernel symbols can be run |

Candidates 1 to 3 together are about 500 ns on GET, which would put nilo-go at about 3,070 CPU ns a request against fib's 2,440: the engine can recover roughly 45% of the gap without becoming an event loop, and what is left is the scheduler, the write cost and the typed framework.

## What it decides for the milestone 2 steps

- **Steps 5 to 9 (middleware, Providers, first-party middleware, `Validate`, `Form`)** all land in the framework buckets that are +221 ns on GET and +626 on POST already. They cannot close the gap with fib and need only to stay inside ADR 0003; a middleware row in ADR 0003 (step 5) should be set against these figures. Step 8 (`Validate`) and step 9 (`Form`) change the decode path, so candidate 5 waits for them.
- **Steps 10, 12 and 16 (streaming, WebSockets, parking)** touch the one place where the engine waits, which is where 80% of the GET gap lives (reads, deadlines, clock, scheduler). Keep that to one place, as the roadmap already says, and do not add a deadline arm or a `now()` for each feature: a WebSocket's heartbeats on the wheel are fine, a per-feature `SetReadDeadline` would put back what candidate 1 takes out. Make candidates 1 to 3 part of the step 16 prototype's comparison, not a separate earlier change that step 16 would redo.
- **Step 17** repeats this measurement with the same `run.sh` against the tree after steps 5 to 16.

## What was not measured

- Syscall counts for `epoll_wait` and `futex` under real load (only one connection, with a slowed server); the epoll cost is known from the profile (57 and 54 ns) but not the count.
- The count of `SetXDeadline` and `now()` calls a request: five and four are read from the source, the per-call costs are taken from the profile assuming those counts.
- Why the write syscall is dearer for nilo-go (claim above), and why GC is dearer on POST.
- Anything about the block experiment, idle memory or p99; this run is CPU only.
- Kernel symbols: the profile attributes kernel time to the Go frame that made the syscall, so inside a syscall bucket there is no split between TCP, the loopback softirq and the wake-ups.

## To rerun

```
cd docs/research/profile-fib
./run.sh                 # builds into ~/.cache/nilo-go-profile, 3 rounds, about 7 minutes
./syscalls.sh            # ptrace counts, about 3 minutes
./pprof.sh               # -top and -cum text into out/pprof/
python3 -I analyze.py --unmatched > out/analysis.txt
```

Close other work first, as `bench/README.md` says: the pinned cores are not isolated.
