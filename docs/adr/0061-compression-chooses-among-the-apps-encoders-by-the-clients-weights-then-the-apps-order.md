# Compression chooses among the app's encoders by the client's weights, then the app's order

The engine's response compression ([ADR 0031](0031-gzip-only-and-off-by-default.md)) compresses with whichever of the encoders the app registered the request's `Accept-Encoding` weighs highest, and on a tie with the one the app listed first. gzip is the core's encoder; zstd and brotli are the modules `github.com/nevindra/bites/modules/zstd` and `github.com/nevindra/bites/modules/brotli` ([ADR 0070](0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core keeps no third-party dependency ([ADR 0006](0006-no-third-party-dependency-in-the-core.md)). Decided and built on 2026-11-01, milestone 3, wave 2, track E (`internal/engine/encode.go`, `compress.go`, `modules/zstd`, `modules/brotli`).

```go
app := bites.New(bites.WithCompression(bites.Compression{
	Encoders: []bites.Encoder{brotli.Encoder(), zstd.Encoder(), bites.GzipEncoder()},
}))
```

## The rule

1. **The seam is an interface of two methods.** `Encoder` has `Coding() string`, the token of `Accept-Encoding` and `Content-Encoding`, read once when `New` runs, and `Encode(dst, src []byte) []byte`, which appends the compressed form of the finished body to `dst`. `Encode` is called from many connections at once and keeps neither slice past its return: both are the engine's pooled buffers ([ADR 0008](0008-no-pooled-object-reaches-the-user.md)), which is the contract `io.Writer` states for its argument. An encoder pools its own state; the engine pools the output buffer (dropped past `maxPooled`, as the response buffer is). A panic in `Encode` is recovered by the engine, logged at Error with its stack, and the answer is sent uncompressed ([ADR 0028](0028-panics-are-recovered-by-the-engine.md)).
2. **The client's weights choose, the app's order breaks a tie.** RFC 9110 section 12.5.3, with weights read to the thousandth: a coding's weight is the one it is listed with, or that of `*` when it is not listed; 0 refuses it; the heaviest coding above 0 wins; on equal weights the one first in `Compression.Encoders` wins; no acceptable coding sends the body as it is. A coding listed twice is refused if either weight is 0 and otherwise weighs its first; several `Accept-Encoding` lines are one list; a weight that does not parse (above 1, more than three decimals, `.5`) is a refusal; `x-gzip` is gzip. The guide's order is brotli, zstd, gzip: brotli made the smallest answers at about gzip's CPU (below), and a browser sends `gzip, deflate, br, zstd`, all of weight 1, so the order decides for every browser.
3. **What is compressed is ADR 0031's.** A buffered answer of at least `MinSize` (1 KiB, the floor), not a 1xx, 204, 206 or 304, not a HEAD, without `Content-Encoding`, `Content-Range` or `Cache-Control: no-transform`, and not an already compressed type; a `Stream`, an SSE and a `File` are never compressed. `Vary: Accept-Encoding` is added to every answer once compression is on, a strong ETag is weakened when the body is compressed, and an encoder whose output is not smaller sends the body as it is.
4. **The options.** `WithCompression(Compression{MinSize, Encoders})` lists the encoders in the server's order; an empty list is gzip alone. `WithGzip(Gzip{MinSize})` stays, and is `WithCompression` with gzip alone. `GzipEncoder()` is the core's gzip, to list beside the modules. `MinCompressSize` replaces `MinGzipSize` as the floor's name.
5. **The modules' levels are fixed, as gzip's is.** zstd: `SpeedFastest`, a 1 MiB window, the lower-memory option, a content checksum, one frame per answer through `EncodeAll`, each state in a `sync.Pool` with a concurrency of one. brotli: level 1 of the matchfinder writer (`NewWriterV2`), one stream per answer, each writer in a `sync.Pool`. Each module exports `Encoder()` and its constant (`zstd.Window`, `brotli.Level`); neither has a knob. The window is within the 8 MiB RFC 9659 allows a zstd content coding.

## What it costs

**Allocations per request: 2 for a compressed typed JSON answer, with each encoder.** gzip's row of [ADR 0003](0003-the-trade-budget.md) holds as before (`budget_test.go`); the 4 KiB typed answer of that row is exactly 2 with zstd (`TestATypedJSONAnswerOf4KiBCompressedWithZstdAllocatesTwice`, `modules/zstd`) and with brotli (`TestATypedJSONAnswerOf4KiBCompressedWithBrotliAllocatesTwice`, `modules/brotli`). The choice reads the request's headers once per registered coding and allocates nothing.

**CPU per compressed answer** (the axis ADR 0031 already pays per answer, not hidden in the 10% of ADR 0003), measured on the JSON of the gzip probe with one reused state, median of three runs on the Ryzen 7 9700X, Go 1.27.3, unpinned beside other work, every row 0 allocations ([`docs/research/encoders/`](../research/encoders/bench-output.txt), the program `encoders_test.go` beside it):

| body | gzip BestSpeed | zstd (the module) | brotli 1 (the module) | brotli 4, classic |
|---|---|---|---|---|
| 1,087 B | 3.49 µs, 257 B | 2.84 µs, 237 B | 3.74 µs, 222 B | 15.2 µs, 205 B |
| 4,111 B | 5.47 µs, 613 B | 5.05 µs, 458 B | 5.73 µs, 452 B | 29.6 µs, 426 B |
| 16,468 B | 12.56 µs, 1,983 B | 12.95 µs, 1,516 B | 14.04 µs, 1,381 B | 79.7 µs, 1,286 B |
| 65,558 B | 39.0 µs, 6,839 B | 42.7 µs, 4,833 B | 44.3 µs, 3,959 B | 253 µs, 3,730 B |

At 0.8 to 1.1 times gzip's CPU zstd's answers are 8 to 29% smaller, and at 1.05 to 1.14 times brotli 1's are 14 to 42% smaller; brotli's classic level 4, a CDN's usual level for dynamic answers, costs 4 to 7 times gzip's CPU for 6 to 8% fewer bytes than brotli 1. The spread of the three runs is within 2% on these rows (gzip 3.47 to 3.49 µs at 1 KiB, 38.5 to 39.1 at 64 KiB; zstd 2.81 to 2.86 and 42.7 to 42.7; brotli 1 3.73 to 3.83 and 44.0 to 44.7).

**Memory: none per idle connection** (the pools hold no connection) **and about GOMAXPROCS live states per coding in use.** One state after a 64 KiB answer holds 815 KB for gzip, 1.56 MB for zstd as the module sets it (9.0 MB with the library's defaults, which `EncodeAll` would make GOMAXPROCS of at first use and keep for ever) and 2.58 MB for brotli 1. With all three in use at once on 16 threads that is about 80 MB at the peak, a claim from the argument of ADR 0031 that compression never blocks, not measured under load; the collector drops idle states as it drops gzip's.

**Throughput and p99 through the engine: not measured yet; they wait for the serial run.** **Binary size:** the core is unchanged; an app that imports a module carries its library.

## Refusals

Rows of `refusal_test.go`, each a panic in `New` naming the file and line of the option: `WithCompression` given twice, or after `WithGzip` (and `WithGzip` after either, as before), naming both places; a `MinSize` below 1 KiB; a nil `Encoder`, by its index; a `Coding` that is not a lowercase token, or is `identity` or `*`; a coding listed twice, naming both indexes. What cannot be caught before the first request: an encoder whose output a client cannot decode, or that keeps a buffer it was given. The module tests decode every answer they compress, from sixteen goroutines at once under `-race`.

## Scenarios it was put against

- _The reference workload_ ([ADR 0002](0002-the-reference-workload.md)) behind a balancer that compresses: compression stays off, the default, and nothing here runs. Without one, or behind an L4 balancer ([ADR 0058](0058-the-proxy-protocol-is-turned-on-per-listener-and-accepted-only-from-a-trusted-proxy.md)), a JSON list of 16 KiB to a browser costs 14 µs of CPU and leaves as 1,381 B of brotli instead of 1,983 of gzip.
- _A browser_ (`gzip, deflate, br, zstd`): every coding weighs 1, so the app's order gives brotli (`TestTheHeaviestAcceptableCodingWinsAndATieGoesToTheServersOrder`, `TestWithCompressionTheClientsWeightsChooseAndATieGoesToTheAppsOrder`).
- _curl or a client library_ (`gzip` only, or nothing): gzip, or the body as it is.
- _A hostile client_ that sends a long `Accept-Encoding`: its cost is one scan of the header per registered coding, bounded by `MaxHead`; it cannot raise the CPU of an answer beyond the most expensive encoder the app chose to offer. The decompression-ratio concern of a compressed request does not apply: the engine compresses only what it sends and decompresses nothing. What a client can do is make every answer pay compression, about 3 to 45 µs of CPU per answer of 1 to 64 KiB, which is why compression is off by default and the floor is 1 KiB. BREACH (an answer that reflects a secret beside attacker input) applies to every coding alike; the guide says so.
- _A module with a bug_ that panics: the answer goes out uncompressed and the log has the stack (`TestAnEncoderThatPanicsSendsTheAnswerUncompressedAndIsLogged`).
- _A precompressed static file_ ([ADR 0065](0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md)): `Static` picks a twin by the same weights in its own fixed order (br, zstd, gzip) and serves it as a `File`, which the engine never compresses again.

## What it fails at

The app's order is one order for every route: an app that wants brotli for its JSON and zstd for a large export cannot say so per route. The levels are fixed: an app that wants brotli 5 for bodies a CDN will cache writes its own `Encoder`. Streams, SSE and files are still not compressed, and Range never is. Peak memory grows with the number of codings in use, up to about 5 MB per thread with all three. A third-party `Encoder` that keeps `src` or `dst` corrupts later answers, and nothing but its own tests can find it. The static twins keep their own order instead of the app's.

## What was rejected

- **Writer-shaped encoders** (`NewWriter(io.Writer)` and `Reset`, pooled by the engine): the engine would pool a different type per coding and call four methods where one does; zstd's `EncodeAll` is already the one-call shape, and an encoder that streams can wrap itself, as gzip and brotli do here.
- **The client's order breaking a tie**: RFC 9110 gives order no meaning, and browsers list `gzip` first for history, so it would pick the largest coding for every browser.
- **A level knob per module**: the measured levels sit at gzip's CPU; a knob would let one setting quadruple the CPU of every answer, the trade ADR 0031 refused for gzip. An app that wants another level has the interface.
- **zstd's defaults through one shared `EncodeAll` encoder**: 9 MB a state, GOMAXPROCS of them made at first use and never released.
- **Brotli's classic writer at level 4 or 5**: 4 to 8 times gzip's CPU for 6 to 26% fewer bytes than level 1 of the matchfinder writer, on a path that compresses every answer.
- **Making brotli or zstd part of the core with a copied implementation**: the core takes no third-party code ([ADR 0006](0006-no-third-party-dependency-in-the-core.md)), and a copy is a dependency without updates.
- **Implicitly adding gzip after the modules**: an app could not offer zstd alone, and a list that says what is offered is the one place to read it.
