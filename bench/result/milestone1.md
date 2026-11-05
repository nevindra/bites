# Milestone 1 beside net/http, Huma and nilo

**Run:** `cd bench && ulimit -n 524288 && ./run.sh && python3 -I analyze.py`, three interleaved rounds, on 2026-10-31 from 10:33 to 10:42 WIB. Ryzen 7 9700X, kernel 7.2.8-9-omarchy, Go 1.27.2, Zig 0.17.0. Server on cores 2,3,10,11, client on 4-7,12-15, client connections spread over 127.0.0.2 to 127.0.0.5. Load: 256 keep-alive connections, 3 s warm, 10 s measured. Idle: 10,000 then 20,000 connections, each served one GET, 5 s settle. Method, routes and per-server differences are in [`bench/README.md`](../README.md); raw JSON of the run is in `bench/out/` (not kept in the repository).

**Tree:** nilo-go uncommitted (the repository has no commit yet; 33 paths dirty), which is milestone 1 plus milestone 2's step 1 (group options and the start-up log, Registration only, nothing on the request path) and the not-yet-linked `internal/ws` and `internal/wheel`. nilo at `v0.8.0-29-gbdb943b` with 3 paths dirty, Huma v2.40.0.

Every figure is the mean over three rounds with the min to max in brackets. A margin inside the spread is "unchanged".

## Allocations per request

`POST /users/{id}` on one keep-alive connection, the server's `Mallocs` delta over 50,000 requests after 2,000 warm-up requests. The three Go servers only.

| server | allocations | bytes |
|---|---|---|
| nilo-go | 2.01 [2.01-2.01] | 321 [321-321] |
| net/http | 35.02 [35.02-35.03] | 3,555 [3,554-3,557] |
| Huma | 46.04 [46.03-46.05] | 3,454 [3,452-3,455] |

The 2 of [ADR 0003](../../docs/adr/0003-the-trade-budget.md) holds over a real socket, with the standard library's and the runtime's share included; the 0.01 is the stats request itself (README, "Known weaknesses").

## Load

`GET /users/{id}`:

| server | req/s | CPU ns/req | p50 µs | p99 µs | p999 µs | peak RSS MB |
|---|---|---|---|---|---|---|
| nilo-go | 894k [889k-898k] | 3,469 [3,453-3,490] | 265 [264-266] | 737 [733-744] | 2,182 [2,094-2,296] | 18.1 |
| net/http | 527k [524k-529k] | 6,561 [6,537-6,591] | 422 [421-423] | 1,464 [1,443-1,493] | 2,128 [2,101-2,176] | 24.0 |
| Huma | 454k [452k-455k] | 7,758 [7,738-7,794] | 488 [486-490] | 1,702 [1,694-1,707] | 2,474 [2,461-2,481] | 25.4 |
| nilo | 1,406k [1,391k-1,416k] | 2,005 [1,996-2,015] | 178 [177-180] | 381 [362-407] | 639 [618-670] | 9.6 |

`POST /users/{id}?page=2&expand=true` with a JSON body:

| server | req/s | CPU ns/req | p50 µs | p99 µs | p999 µs | peak RSS MB |
|---|---|---|---|---|---|---|
| nilo-go | 737k [732k-740k] | 4,424 [4,407-4,449] | 320 [319-322] | 905 [895-914] | 2,174 [2,102-2,288] | 20.1 |
| net/http | 407k [406k-408k] | 8,696 [8,665-8,718] | 540 [538-541] | 1,969 [1,949-1,990] | 3,133 [3,105-3,164] | 24.8 |
| Huma | 306k [305k-307k] | 11,921 [11,882-11,974] | 729 [727-733] | 2,434 [2,416-2,456] | 3,603 [3,588-3,620] | 25.6 |
| nilo | 1,199k [1,192k-1,203k] | 2,500 [2,494-2,510] | 211 [210-212] | 381 [372-389] | 635 [628-641] | 10.9 |

No run had an error.

## Idle memory per connection

Marginal from 10,000 to 20,000 connections, bytes.

| server | RSS | heap plus stack |
|---|---|---|
| nilo-go | 4,115 [3,974-4,187] | 3,161 [3,157-3,166] |
| net/http | 19,551 [19,123-19,766] | 18,188 [18,183-18,190] |
| Huma | 19,344 [18,914-19,562] | 18,186 [18,183-18,191] |
| nilo | 4,928 [4,927-4,928] | not applicable (no Go runtime) |

## Binary size

Stripped: nilo-go 4.05 MB, net/http 6.74 MB, Huma 7.80 MB, nilo 1.37 MB.

## What it says

- **Against net/http and Huma**, nilo-go serves 1.7 to 2.4 times the requests at half or less of the CPU a request, with p99 about half, 2 allocations against 35 and 46, and a fifth of the idle memory a connection, in a binary 40 to 48% smaller. net/http does the least validation of the three (README), so it is the cheapest by construction and still the slower.
- **Against nilo**, nilo-go is 1.6 times slower (GET 894k against 1,406k) and spends 1.7 to 1.8 times the CPU a request, with about twice the p99. nilo's figures sit above the roughly 1.1M req/s where this client was found to limit itself ([`docs/research/README.md`](../../docs/research/README.md)), so the true gap may be larger. nilo-go holds less memory an idle connection (4.1 KB against 4.9 KB RSS); nilo's binary is a third of the size.
- **The idle figure of [ADR 0005](../../docs/adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md) holds at scale**: heap plus stack is 3,157 to 3,166 B a connection at 10k to 20k, the 3.1 to 3.4 KB that `idle_test.go` measures at 1.2k to 2.2k. RSS is about 950 B above it: runtime and allocator overhead that `runtime/metrics`' heap objects and stacks do not count.

## Known limits of this run

- Loopback and one Go client; read the comparison, not the absolute figures.
- A first run of the same command, from 09:51 to 09:59, completed but ran after a kernel oops had killed `kswapd0` (the machine rebooted at 10:06). It is not used. For the record it read 2 to 4% lower throughput for nilo-go and nilo and the same idle and allocation figures; its raw files are kept outside the repository.
- No commit to tie the tree to; the figures should be retaken from the first tagged commit if the tree changes on the request path before then.
