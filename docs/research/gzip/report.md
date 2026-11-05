# gzip in the engine (step 15)

What was run: `gzip_test.go` in this folder (its own `go.mod`, `replace` to `../../..`), `BenchmarkCompress` (a pooled BestSpeed writer appending to a reused buffer, the engine's own loop) and `BenchmarkRequest` (a whole GET on a kept-alive in-memory connection served by the built engine, the client reading the whole answer; `plain` sends no `Accept-Encoding`, `gzip` sends `gzip`, the same app with `WithGzip(Gzip{})`). The body is the JSON list of `docs/research/decisions-bodies/gzip_test.go`, more repetitive than real data, so ratios are optimistic. Machine: AMD Ryzen 7 9700X, Go 1.27.3, pinned with `taskset -c 2,3,10,11`, `-count 5`, 1 s each. Commit under test: working tree on top of 6b086e4 (step 15 uncommitted). Raw output: `bench-output.txt`. Run: `cd docs/research/gzip && taskset -c 2,3,10,11 go test -run XXX -bench . -count 5`.

The runs of the two modes are sequential benchmarks, not interleaved with each other; the margins below are far outside the spread.

## Compression alone (ns per response, median and range of 5)

| body | ADR 0031 (earlier probe) | built loop | spread | output | allocs |
|---|---|---|---|---|---|
| 1,087 B | 3.5 µs | 3.66 µs | 3.66 to 3.78 | 257 B | 0 |
| 4,111 B | 5.5 µs | 5.80 µs | 5.73 to 5.82 | 613 B | 0 |
| 16,468 B | 13.4 µs | 13.4 µs | 13.2 to 13.6 | 1,983 B | 0 |
| 65,558 B | 38.3 µs | 40.8 µs | 40.3 to 41.7 | 6,839 B | 0 |

The B/op column of the raw output (2 to 29 B) is the pool's refill after a GC spread over the runs, not an allocation per call.

## A whole request through the engine

| body | plain µs | gzip µs | extra | wire plain | wire gzip | allocs plain / gzip |
|---|---|---|---|---|---|---|
| 1,087 B | 1.94 (1.93 to 1.98) | 6.38 (6.28 to 6.42) | 4.4 | 1,220 B | 413 B | 2 / 2 |
| 4,111 B | 2.03 (2.03 to 2.08) | 8.59 (8.50 to 8.73) | 6.6 | 4,244 B | 769 B | 2 / 2 |
| 16,468 B | 2.18 (2.16 to 2.23) | 16.16 (16.03 to 16.56) | 14.0 | 16,602 B | 2,140 B | 2 / 2 |
| 65,558 B | 11.4 (11.0 to 11.7) | 49.2 (49.0 to 56.3, one slow run) | 37.8 | 65,692 B | 6,996 B | 5 / 3 |

Reading it: the extra is the compression (the table above) plus about 0.7 µs of negotiation, the pool and the smaller write; it is CPU on the server, paid per compressed response, as ADR 0031 says. The 64 KiB row is outside the 2-allocation row either way: the body is over `maxPooled` (64 KiB), so the response buffer is dropped from the pool after each request whatever gzip does (plain pays 5 allocations, one of them a 74 KB body region); not investigated further.

## Allocations on the budget row

`budget_test.go`, `TestATypedJSONAnswerOf4KiBAllocatesTwiceWithGzipOffAndWithItOnWhetherCompressedOrNot`: a typed GET with a 40-item JSON list (about 4 KiB) allocates 2 per request with gzip off, with gzip on and the client sending `identity`, with gzip on and a body under `MinSize`, and with gzip on and the body compressed (3 runs, same number each). `AllocsPerRun` over 2,000 requests.

## Not measured

Idle memory per connection with gzip on (the pooled writers and buffers hold no connection, so it should not move; a claim). Live writers about `GOMAXPROCS` (a claim, ADR 0031). Throughput and p99 over TCP with gzip on. The wire effect of compressing below 1.2 KiB.
