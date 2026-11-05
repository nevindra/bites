# Idle memory of a TLS connection

Taken on 2026-10-31 for step 3 of Milestone 2 ([ADR 0017](../../adr/0017-tls-now-http2-later.md)).

## What was run

`run.sh` builds `bench/servers/nilogo` (new `-tls` flag, off by default: a self-signed ecdsa P-256 certificate made at start-up) and `bench/client` (new `-tls` flag, off by default: the certificate is not verified), then runs the harness's idle experiment: 10,000 then 20,000 connections, each handshaken and served one `GET /users/7`, 5 s settle before each snapshot, server RSS from `/proc` and heap plus stacks from the server's own SIGUSR1 stats (`runtime/metrics`). Rounds are interleaved (base, plain, tls), three of them. Server pinned to CPUs 2,3,10,11 and client to 4-7,12-15 (AMD Ryzen 7 9700X, Linux 7.2.8-9-omarchy, go1.27.2). `base` is the server built from `git archive HEAD` (commit 79d0e58, before this change) with the same flags in the same session; `plain` and `tls` are the working tree on top of that commit (`versions.json` records it as dirty). `analyze.py` computes the marginal bytes per connection from 10k to 20k; `out/` holds every raw run and `out/analysis.txt` the result.

## Result

Bytes per idle connection, marginal from 10,000 to 20,000, mean with min to max over three runs:

| server | heap + stack | RSS |
|---|---|---|
| base (before the change), plain | 3,161 [3,158-3,163] | 4,184 [4,184-4,185] |
| working tree, plain | 3,162 [3,160-3,165] | 4,112 [3,973-4,183] |
| working tree, TLS | 11,901 [11,900-11,903] | 21,589 [20,738-22,449] |

The plain connection did not move: the differences are inside the spread. Of the TLS connection's heap plus stack, 4,096 B is stack (2,048 B plain, deeper because the idle read is in `tls.Conn.Read`) and about 7,800 B is heap (1,100 B plain). RSS is about 10 KB over heap plus stack; the server's `sys` grew by 21.6 to 22.5 KB a connection, so it is memory the runtime holds from the roughly 57 KB a connection allocates in total (`total_alloc` 1.14 GB for 20,000 handshakes and requests), not a live object, still held after the 5 s settle (the run did 34 to 40 collections against 7 to 10 for plain).

The same measurement through the root package's gate (client in a child process, 200 warm, then 1,000 and 1,000 more, `go test -run Idle`) gave 10,639 to 11,553 B for TLS and 3,333 to 3,518 B for plain; it is the smaller sample, and the harness above is the figure quoted.

## Not measured

TLS throughput and allocations a request. The change adds no clock read, deadline or allocation to the request path, and the plain allocation tests are unchanged, but the cost of the record layer on a request is not a figure here.
