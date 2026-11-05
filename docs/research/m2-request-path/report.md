# Milestone 2 steps 6 to 11 on the request path: what they cost

Taken on 2026-11-01, Go 1.27.3, AMD Ryzen 7 9700X, kernel 7.2.8-9-omarchy. The code measured is `6b086e4` plus the uncommitted tree of steps 6 to 11 (the "after"), against `git archive 6b086e4` in `~/.cache/nilo-go-m2-before` (the "before"), both built in the same session with the same flags. No other work ran on the machine during either session.

## What was run

`run.sh` runs `bench/run.sh` with `SERVERS=nilogo EXPS="get post allocs" ROUNDS=1` three times, the variants in turn each time (interleaved): `before`, `after` (the routes of milestone 1), `prov` (the Input asks for a `Caller` whose Provider needs a `Session`, whose Provider reads a header), `valid` (constraint types in every field of both Inputs and a `Validate` that returns nil), and `mw4` (`CORS`, `RequestID` and `AccessLog` with a text handler writing to `io.Discard` on the app, `Timeout(5s)` on each route). `run-mw.sh` then ran each of the four alone (`cors`, `rid`, `log`, `timeout`) beside the plain route (`after2`), the same way. The server is `bench/servers/nilogo/main.go`, chosen by `NILOGO_VARIANT`; each variant was checked to answer 200 with the expected body before the run. Server on cores 2,3,10,11 and client on 4-7,12-15, 256 connections, 3 s warm-up and 10 s measured, loopback. `summary.py` prints `summary.txt` and `summary-mw.txt`; the raw runs are `raw.jsonl` and `raw-mw.jsonl`. `Form[T]`, `Upload` and `File` are not in the run: the client sends JSON only, and their allocation rows are exact in `budget_test.go`.

## Allocations: the hard axis holds

| | allocs/request | bytes/request |
|---|---|---|
| before | 2.006 to 2.007 | 321 |
| after | 2.005 to 2.006 | 321 |
| two Providers, one needing the other | 2.006 to 2.009 | 321 to 322 |
| constraint types and `Validate` | 2.006 to 2.007 | 321 |
| one first-party middleware, any of the four | 2.007 to 2.009 | 337 to 338 |
| all four | 3.010 to 3.011 | 386 |

The 16 bytes of every middleware are the slab room `RequestID` reserves on any request with middleware (ADR 0032). The third allocation of the four together is the `slog.Record`'s sixth attribute (ADR 0003).

## Throughput: the first session (mean of three, spread under 1% within a variant)

| GET /users/7 | req/s | CPU ns/req | p99 |
|---|---|---|---|
| before | 851.9k | 3,595 | 819 us |
| after | 844.7k (-0.8%) | 3,629 | 831 us (+1%) |
| two Providers | 798.6k (-5.5% from after) | 3,909 | 1,017 us (+22%) |
| constraints and `Validate` | 848.4k (+0.4%) | 3,621 | 832 us (+0%) |
| all four middleware | 609.9k (-27.8%) | 5,532 | 1,355 us (+63%) |

| POST /users/7 | req/s | CPU ns/req | p99 |
|---|---|---|---|
| before | 712.7k | 4,531 | 957 us |
| after | 703.3k (-1.3%) | 4,606 | 993 us (+4%) |
| two Providers | 663.1k (-5.7%) | 4,946 | 1,198 us (+21%) |
| constraints and `Validate` | 688.7k (-2.1%) | 4,726 | 1,188 us (+19%) |
| all four middleware | 524.9k (-25.4%) | 6,605 | 1,600 us (+61%) |

## The four middleware one at a time: the second session (spread 3% to 5%, noisier than the first)

| against `after2` | GET req/s | POST req/s | p99 GET, POST | CPU ns/req GET |
|---|---|---|---|---|
| `after2` | 828.1k | 691.3k | 897 us, 1,038 us | 3,676 |
| `CORS` | -6.2% | -6.5% | +23%, +23% | +296 |
| `RequestID` | -8.6% | -5.9% | +32%, +20% | +322 |
| `AccessLog` | -20.5% | -17.7% | +31%, +30% | +1,284 |
| `Timeout` | -9.0% | -7.9% | +27%, +25% | +407 |

## Reading it

- **What every route pays** for steps 6 to 11 (the `noescape` helpers now calls, the Provider branch in `serve`, the extra arguments of `Validate` and `Stream`) is -0.8% on GET and -1.3% on POST, inside the 2% that two binaries of the same code differed by in step 5 (`docs/research/middleware-core/`): unchanged.
- **Providers** cost -5.5% and -5.7%, inside the 10% of ADR 0003, and p99 +21% to +22%, outside it. That p99 is the one any middleware pays (+19% to +23% in step 5): a route with Providers is served through the chain, which builds the request view a Provider receives, so it is the chain's fixed cost, not the Providers'. Its cause is the open question step 17 takes first.
- **Constraint types and `Validate`** cost nothing on GET (one `PositiveInt64` path parameter) and -2.1% on POST, inside the budget. The POST p99 rose 19% with no matching rise in CPU (+120 ns); the cause is not found. It is recorded for step 17 with the chain's.
- **`CORS`, `RequestID` and `Timeout`** each cost 6% to 9% of throughput, most of which is the chain's fixed cost of about 5%; their own work is 300 to 400 ns. Inside the 10%.
- **`AccessLog`** costs 18% to 21% and 1.3 us of CPU a request, outside the 10%. The time is slog's text handler formatting one line of six attributes (time, level, message, method, path, status, size, duration) for every request, which is the work of an access log rather than overhead of the framework around it; the framework's own part (reading the response, the attributes of Providers) is the chain's. A faster record (a fixed encoder bypassing slog's handler) would trade the app's logger and its configuration for speed; that is a decision, not done here.
- **All four together** cost what their parts add up to (-26% to -28%), so they do not interfere.
