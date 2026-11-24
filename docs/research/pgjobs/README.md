# What the pgjobs queue costs

The questions, from [ADR 0095](../../adr/0095-a-job-queue-in-postgres-is-a-module-whose-jobs-are-claimed-under-a-lease-and-delivered-at-least-once.md): how fast one instance enqueues and completes jobs, with one worker and with eight, and whether the claim stays an index scan when the table keeps a million completed jobs.

## The runs

Both are in `modules/pgjobs/bench_test.go` and need `BITES_PG_URL`. Machine: AMD Ryzen 7 9700X, Linux, Go 1.27.4, not pinned. Postgres 18.7 in Docker on the same machine, over loopback, started with `fsync=off` and `synchronous_commit=off`, so a commit is cheaper than on a durable server and the figures are the queue's own cost over a fast database, not what a production server gives. The working tree is the one of 2026-11-24 on commit 0bce130, two sessions of three runs each.

- `BenchmarkEnqueue` is one `Kind.Enqueue` through the pool at a time, without and with `Options.Notify` (a second statement, the `pg_notify`).
- `BenchmarkComplete` stores N jobs first, then starts the Service with one queue of 1 or 8 workers and times from the start to the last completion an Observer sees. The handler returns nil at once, so this is the claim, the UPDATE that records each completion and the heartbeat.

```sh
cd modules/pgjobs
BITES_PG_URL=postgres://... go test -run '^$' -bench . -benchtime 3000x -count 3 .
```

## The numbers

The second session, as printed:

```
cpu: AMD Ryzen 7 9700X 8-Core Processor
BenchmarkEnqueue/notify=false-16         	    3000	     28395 ns/op	     35218 jobs/s
BenchmarkEnqueue/notify=false-16         	    3000	     27463 ns/op	     36413 jobs/s
BenchmarkEnqueue/notify=false-16         	    3000	     27767 ns/op	     36014 jobs/s
BenchmarkEnqueue/notify=true-16          	    3000	     46658 ns/op	     21432 jobs/s
BenchmarkEnqueue/notify=true-16          	    3000	     46381 ns/op	     21561 jobs/s
BenchmarkEnqueue/notify=true-16          	    3000	     51263 ns/op	     19507 jobs/s
BenchmarkComplete/workers=1-16           	    3000	     83525 ns/op	     11972 jobs/s
BenchmarkComplete/workers=1-16           	    3000	     89704 ns/op	     11148 jobs/s
BenchmarkComplete/workers=1-16           	    3000	     91505 ns/op	     10928 jobs/s
BenchmarkComplete/workers=8-16           	    3000	     29294 ns/op	     34136 jobs/s
BenchmarkComplete/workers=8-16           	    3000	     26864 ns/op	     37224 jobs/s
BenchmarkComplete/workers=8-16           	    3000	     27969 ns/op	     35754 jobs/s
```

Over both sessions (six runs each):

| What | Per job | Jobs a second |
|---|---|---|
| Enqueue through the pool | 27 to 31 µs | 32,400 to 36,400 |
| Enqueue with the wake-up | 46 to 54 µs | 18,600 to 21,600 |
| Complete, 1 worker | 84 to 92 µs | 10,800 to 12,000 |
| Complete, 8 workers | 27 to 33 µs | 30,000 to 37,200 |

One worker waits for each round trip, so it is bound by latency; eight share a claim statement and overlap their completions, about three times as many. The wake-up costs a second statement, about 20 µs.

## The claim on a million completed jobs

`TestTheClaimQueryIsAnIndexScanOnATableOfAMillionCompletedJobs`, run with `PGJOBS_EXPLAIN=1`, fills a table with 1,000,000 completed and 1,000 available jobs (3.3 s), runs `ANALYZE`, and prints `EXPLAIN (ANALYZE, BUFFERS)` of the claim of ten, the rescue probe and a prune batch, failing when the claim is not an index scan of the claim index. The claim, from the 2026-11-24 run:

```
Update on t_jobs_1396587_1 (actual time=0.085..0.139 rows=10.00 loops=1)
  Buffers: shared hit=128 dirtied=2 written=1
  ->  Nested Loop (actual time=0.040..0.070 rows=10.00 loops=1)
        Buffers: shared hit=56
        ->  HashAggregate (actual time=0.035..0.037 rows=10.00 loops=1)
              Group Key: "ANY_subquery".id
              Buffers: shared hit=16
              ->  Subquery Scan on "ANY_subquery" (actual time=0.025..0.031 rows=10.00 loops=1)
                    ->  Limit (actual time=0.020..0.026 rows=10.00 loops=1)
                          ->  LockRows (actual time=0.020..0.024 rows=10.00 loops=1)
                                Buffers: shared hit=16
                                ->  Index Scan using t_jobs_1396587_1_claim_idx on t_jobs_1396587_1 t_jobs_1396587_1_1 (actual time=0.016..0.018 rows=10.00 loops=1)
                                      Index Cond: ((queue = 'default'::text) AND (run_at <= '2026-11-24 16:16:04.625098+00'::timestamp with time zone))
                                      Filter: ((kind = ANY ('{send}'::text[])) AND (state = 'available'::text))
                                      Buffers: shared hit=6
        ->  Index Scan using t_jobs_1396587_1_pkey on t_jobs_1396587_1 (actual time=0.003..0.003 rows=1.00 loops=10)
              Index Cond: (id = "ANY_subquery".id)
              Buffers: shared hit=40
Planning Time: 0.300 ms
Execution Time: 0.182 ms
```

The claim index is partial on `state = 'available'`, so the million completed rows are not in it: the claim reads 16 shared buffers to find and lock ten jobs, 0.18 ms here and 0.24 ms in the first session. The rescue probe is an index scan of the running index, 0.008 ms. A prune batch is a sequential scan in this table, because the planner sees that half the completed rows match and the first thousand are found in 52 pages, 0.49 ms. `Counts` reads the whole table, 41 to 45 ms over the 1,001,000 rows; `Work` ran the 1,000 available jobs one at a time in 252 to 277 ms.

## What this does not say

- A durable server (`fsync=on`, `synchronous_commit=on`) pays a WAL flush per commit, which on this kind of disk is the larger part of a round trip; the figures here are an upper bound for one instance.
- Several instances share the claim index and contend on the same rows only as `SKIP LOCKED` lets them; the throughput of several instances was not measured.
- A prune on a table where few rows match uses the finished index; only the case above was run.
