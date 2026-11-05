# What a connection holds while it declares a large body and sends nothing

Question: does the engine reserve memory for a declared `Content-Length` before the body arrives, and what does the fix change? Answer: it did (about 1 MiB a connection, held for the body timeout); it now holds about 72 KB.

## Method

`main.go` (own module) starts the server in-process, warms it with one request, opens `-n` loopback connections (source addresses 127.0.0.2 to 127.0.0.5) that each send a head declaring `Content-Length: 1048576` and then nothing, waits 2 s, and reports live heap (`HeapAlloc` after two GCs) and RSS per connection against the baseline. Two routes: `/read` (a typed JSON body, so the handler is blocked in `ReadBody`) and `/noread` (no Body section). The before binary was built from a copy of the working tree taken before the change (the tree held uncommitted milestone 2 work, so HEAD was not the right before), the after binary from the tree after. Three interleaved runs, `-n 200` (a first run at 1,000 connections gave the same per-connection heap, 1,063,050 to 1,063,051 B before and 71,876 to 71,883 B after, and was then stopped: 1,000 connections crashed the machine through a kernel reclaim bug, so every figure quoted below is at 200). Machine: Ryzen 7 9700X, Linux 7.2.8, Go 1.27.3. The probe was not pinned to cores: it measures bytes, not time.

## Results (`/read`, `-n 200`)

| | heap per connection | RSS per connection |
|---|---|---|
| before | 1,063,257 / 1,063,177 / 1,063,201 B | 58,245 / 68,259 / 57,917 B |
| after | 71,977 / 72,057 / 72,115 B | 87,203 / 77,189 / 76,718 B |

The after figure is the 64 KiB region (65,536 B) plus about 6.4 KB of the connection's own. `/noread` held 1,097 B (before) and 917 B (after): that route's handler returns at once, so the slab, 1 MiB before, was made and dropped and never held; the fix removes a 1 MiB zeroed allocation per such request, not a hold.

Caveat on RSS: it did not fall. Before, a 1 MiB `make` came fresh from the OS and was never touched, so few pages were resident; a 64 KiB region is zeroed in reused spans and is touched. Where the runtime reuses spans (after earlier large requests) the before case is resident in full, which is the case `decisions-bodies` measured (R4). Heap in use, which is what the collector and the memory limit see, falls 14.8 times. The 64 KiB first step is the `firstBody` constant in `internal/engine/exchange.go`; a smaller one lowers the hold in proportion at the cost of more doublings.

## Tests (internal/engine)

A silent 1 MiB declaration held 1,064,486 B a connection in the test before, 72,933 B after; a route that does not read its body had 1,048,630 B free in its slab before, none after; a 400 KB chunked body allocated 5.91 times its size per request before, 2.66 after; a body that arrives with its head allocates 2 per request, as before. All measured by the tests named in `engine_test.go` and `budget_test.go`, run against the before tree to confirm they fail there.

## Not measured

The POST throughput and CPU comparison asked for (bench `allocs post`, three interleaved rounds) was started and then cut short by the machine crash, and the harness was not rerun by instruction. The path it exercises (body arriving with its head) is unchanged apart from one added comparison in `handle`, and its allocation count is held at 2 by `TestABodyThatArrivesWithItsHeadIsReadWithOneAllocation`; a throughput number is therefore a claim, not a measurement.
