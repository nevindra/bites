# Go server foundations: goroutine-per-connection vs event loop (measured on this machine)

Dir: /tmp/claude-1000/-home-nezhifi-Development-nilo/c3529e15-d781-409c-ad5c-befd03675bc7/scratchpad/research/engines
Repeat: `go build` the servers into bin/ (see runall.sh), `./runall.sh` for idle (3 runs) and `./runall2.sh` for plain and block (2 runs), `python3 -I analyze.py`, `go test ./bench -bench . -benchtime 300000x -count 3`. Raw JSON in raw/, client in client/main.go, servers in servers/*/main.go, hand-written server in hand/hand.go.

## Setup
- AMD Ryzen 7 9700X, 8 cores/16 threads; SMT siblings are CPU n and n+8. Server: `taskset -c 2,3,10,11` (physical cores 2 and 3, both siblings). Client: `taskset -c 4-7,12-15` (cores 4-7). Cores 0,1 idle.
- go 1.27.3 (go.mod asks for it because fib needs >= 1.27.3; the toolchain auto-switched from the installed 1.27.2). GOMAXPROCS seen by every server: 4 (Go reads the affinity mask). runtime.NumCPU in the server is therefore 4 too, which sets pool caps below.
- Versions: fasthttp v1.77.0, fib v1.6.0, nbio v1.7.1, hertz v0.10.6 (netpoll v0.7.5), net/http = Go 1.27.3 std. Latest as of `go get @latest` today; tags, not commits.
- All servers default config; each answers GET / and GET /block with 200 "hello" (Content-Length: 5), keep-alive. nbio needed an explicit Content-Length header in the handler: without it nbhttp answers chunked. 
- Client (client/main.go, separate process): one goroutine per connection, raw net.Conn, sequential request/response, parses Content-Length. Source IPs 127.0.0.2-5 round robin, SO_REUSEADDR + IP_BIND_ADDRESS_NO_PORT, SO_LINGER 0 on close (my first attempt ran out of ports because of TIME_WAIT; those results were discarded, kept in raw_discarded_*).
- Server stats: SIGUSR1 writes runtime.MemStats (HeapInuse + StackInuse) to a file; RSS from /proc/pid/status VmRSS; peak = VmHWM; peak goroutines from a 25 ms sampler goroutine in the server (so the number is a lower bound).
- Latency: client-side, per request, microseconds, measured only after warm-up.
- Run counts reduced to fit 40 minutes: A = 3 runs, B and C = 2 runs (not 3), interleaved server by server; spread = [min-max]. C warm-up 2 s, measured 15 s. B warm-up 3 s, measured 10 s. Two runs is thin for a spread.
- Hand-written server: (a) 4 KiB buffer allocated per connection. (b) per connection a 1-byte slice; blocks on Read of 1 byte, then takes the 4 KiB buffer from a sync.Pool, copies the byte in, reads the rest of the head, answers, returns the buffer to the pool before blocking again. No pipelining, GET only, no body parsing (leftover bytes dropped).
- Caveat: the client may cap the fastest servers (about 1.4M req/s over 8 client threads); the ranking among the top three in B is partly the client. All traffic is loopback, a fixed 5-byte answer, so it measures the engine, not routing or a real handler.

## A, B, C results (mean [min-max]; bytes per connection unless stated)
## A idle (bytes per connection; mean [min-max] over runs)
| server | runs | RSS base | RSS/conn @10k | RSS marginal 10k->20k | heap+stack/conn @10k | heap+stack marginal | goroutines @20k |
| stdhttp | 3 | 11.2 [9.8-11.9] MB | 19978 [19767-20187] | 19926 [19780-20002] | 18845 [18792-18935] | 18652 [18055-18961] | 20003 [20003-20003] |
| fasthttp | 3 | 12.1 [10.0-14.2] MB | 16620 [16548-16760] | 16383 [16370-16394] | 15535 [15530-15541] | 15526 [15514-15534] | 20005 [20005-20005] |
| fib | 3 | 15.4 [14.0-16.1] MB | 1371 [1229-1649] | 1078 [867-1288] | 1117 [1114-1121] | 1063 [1051-1076] | 5 [5-5] |
| nbio | 3 | 15.6 [14.9-17.0] MB | 1896 [1821-2041] | 1151 [1078-1291] | 1256 [1243-1273] | 1556 [1535-1579] | 7 [7-7] |
| hertz | 3 | 14.6 [13.2-15.3] MB | 23687 [23560-23760] | 23514 [23375-23588] | 22968 [22888-23021] | 22994 [22872-23070] | 20006 [20006-20006] |
| hand-a | 3 | 9.4 [9.4-9.4] MB | 7835 [7762-7973] | 7713 [7644-7847] | 6985 [6966-6997] | 6964 [6948-6992] | 20003 [20003-20003] |
| hand-b | 3 | 9.4 [9.4-9.5] MB | 3668 [3667-3669] | 3408 [3337-3549] | 2905 [2905-2906] | 2904 [2901-2907] | 20003 [20003-20003] |

## B plain GET / 256 conns
| server | runs | req/s | p50 us | p99 us | errors | peak RSS MB (VmHWM) | peak goroutines | gomaxprocs |
| stdhttp | 2 | 619539 [614558-624520] | 364 [361-367] | 1258 [1250-1265] | 0 | 23.8 [22.7-24.9] | 293 [288-298] | 4 |
| fasthttp | 2 | 1191788 [1179890-1203686] | 199 [196-202] | 555 [551-559] | 0 | 17.4 [17.3-17.4] | 261 [261-261] | 4 |
| fib | 2 | 1137873 [1129474-1146272] | 234 [232-235] | 492 [491-493] | 0 | 25.0 [23.9-26.1] | 127 [122-132] | 4 |
| nbio | 2 | 536543 [534488-538598] | 440 [438-441] | 1129 [1110-1148] | 0 | 30.7 [30.7-30.8] | 263 [263-263] | 4 |
| hertz | 2 | 989612 [980585-998639] | 246 [245-246] | 612 [585-639] | 0 | 34.7 [33.7-35.8] | 263 [263-263] | 4 |
| hand-a | 2 | 1449279 [1446489-1452068] | 162 [161-162] | 496 [495-497] | 0 | 11.8 [11.8-11.8] | 259 [259-259] | 4 |
| hand-b | 2 | 1339537 [1330583-1348492] | 175 [173-177] | 526 [524-529] | 0 | 10.7 [9.7-11.8] | 259 [259-259] | 4 |

## C GET /block 1000 conns
| server | runs | req/s | p50 us | p99 us | errors | peak RSS MB (VmHWM) | peak goroutines | gomaxprocs |
| stdhttp | 2 | 92170 [91445-92895] | 10741 [10682-10800] | 12797 [12402-13192] | 0 | 59.2 [59.2-59.3] | 2003 [2003-2003] | 4 |
| fasthttp | 2 | 90159 [88006-92312] | 10920 [10528-11312] | 12632 [12611-12654] | 0 | 30.0 [29.0-31.1] | 1005 [1005-1005] | 4 |
| fib | 2 | 90898 [90164-91632] | 10936 [10837-11036] | 12700 [12576-12823] | 0 | 77.6 [77.6-77.6] | 3960 [3947-3972] | 4 |
| nbio | 2 | 88803 [88511-89094] | 11110 [11096-11125] | 13844 [13735-13953] | 0 | 34.9 [34.8-34.9] | 1007 [1007-1007] | 4 |
| hertz | 2 | 92266 [92017-92515] | 10797 [10733-10861] | 12146 [11999-12292] | 0 | 82.2 [81.3-83.1] | 1007 [1007-1007] | 4 |
| hand-a | 2 | 89944 [89471-90417] | 11075 [11005-11145] | 12368 [12247-12488] | 0 | 17.2 [17.2-17.2] | 1003 [1003-1003] | 4 |
| hand-b | 2 | 92263 [89681-94844] | 10742 [10396-11089] | 12205 [12120-12290] | 0 | 19.3 [19.3-19.3] | 1003 [1003-1003] | 4 |
| fib-async | 2 | 84935 [83028-86842] | 11779 [11493-12065] | 13421 [12968-13874] | 0 | 28.4 [28.3-28.4] | 1041 [1041-1041] | 4 |

## D Allocations per request (in-process, one keep-alive loopback connection, 300k requests x 3 runs, client is zero-alloc)
| server | allocs/op | B/op | ns/op (round trip incl. client) |
|---|---|---|---|
| net/http | 13 (all 3 runs) | 1382 | 6.2-6.3 us |
| fasthttp (bonus) | 0 | 0 | 3.15-3.17 us |
| hand-written (a) | 0 | 0 | 2.80-2.84 us |
| hand-written (b) pool | 0 | 0 | 2.93-2.94 us |
fib, nbio, hertz: not measured in-process. Claims (README, not verified by me): fib "routing allocates nothing", "size-classed buffer pool", "a tenth of net/http's memory". A rough external check from my 2 s smoke run of B (not the final runs; MemStats mallocs divided by requests sent, including warm-up and setup): net/http about 13, fib about 1, hertz about 1, nbio about 27. Order of magnitude only; I did not look for why nbio is high.

## Why the numbers look like this (code or docs read)
- Idle memory, goroutine models: net/http costs 19.9 KB per connection, fasthttp 16.4 KB, hertz 23.7 KB, hand (a) 7.8 KB, hand (b) 3.7 KB. 20,003 goroutines at 20k connections for all of them, including hertz: hertz's OnRequest callback (netpoll) runs http1.Server.Serve, a `for` loop that blocks in zr.Peek, so it is goroutine-per-connection with netpoll only as the wake-up path. Its default read buffer is 4 KiB (config defaultReadBufferSize) plus the stack. fasthttp and net/http hold per-connection reader/writer buffers (4 KiB each in net/http) for the life of the connection.
- Hand-written: stack_inuse is about 2 KB a goroutine; (a) adds the 4 KiB buffer, (b) frees it between requests via the pool. The pool helped by 4.1 KB per connection (7.8 KB -> 3.7 KB), and cost about 5 percent throughput in B (1.45M -> 1.34M req/s, two syscalls per request instead of one: the 1-byte read, then the rest) and nothing measurable in C. Note that RSS per connection (3.7 KB) is 0.8 KB above heap+stack (2.9 KB): kernel socket memory is not in either column of "RSS" here, but netFD/pollDesc, TCPConn objects and page rounding are.
- Event loops: fib 1.1-1.4 KB and nbio 1.2-1.9 KB per connection: 5 to 7 goroutines in total at 20k connections. fib's README says a connection costs no goroutine while idle and reads into a buffer borrowed from a pool (docs: "worker reads with a borrowed buffer until a short read"). The first 10k nbio figure (RSS 1.9 KB) is above its marginal (1.15 KB) because fixed structures grow in steps. Kernel socket buffers are not counted in anyone's RSS (they are not process memory), so on the box the real cost per connection is higher for everyone by the same kernel amount.
- B: net/http is slowest-but-one because of 13 allocs/request (D) at 4 cores; nbio is lowest, its allocs per request look high (smoke check) and it dispatches every read to a task pool (ServerExecutor, default size NumCPU*1024). fasthttp, fib, hertz and the hand server are 1.0-1.45M req/s, within the client's own limits, so treat their order as indicative only.
- C: every model is near the ideal ~100k/s (86-92k). Event-loop engines did not stall here because their default pool caps exceed 1000 on this machine: fib WorkerCount = max(GOMAXPROCS*1000, NumCPU*20) = 4000 (config.go), nbio MessageHandlerPoolSize = NumCPU*1024 = 4096 (engine.go), hertz/netpoll gopool is capped at MaxInt32 (gopkg gopool). So the stall case (blocking handlers more than the cap) was NOT exercised; I did not lower the caps (out of time). What did happen: fib's sync handler grew its pool to about 3,960 goroutines (RSS 77.6 MB, spread 77.6 on both runs) for 1,000 blocked handlers: the adaptive pool over-grows when its workers block, and its comment in taskpool/adaptive.go says a ceiling on tasks that block "may be [exceeded] until they are released". With the documented Retain/Release style and the sleep in a goroutine, fib held 1,041 goroutines and 28.4 MB, but throughput was 85k/s vs 91k/s (about 7 percent slower, 2 runs, ranges do not overlap: 83.0-86.8k vs 90.2-91.6k; cause not investigated). nbio: 1,007 goroutines, 34.9 MB, so it also spawned a goroutine per blocked handler. hertz: 82 MB peak, 1,007 goroutines (goroutine per connection plus per-connection 4 KiB buffers, plus netpoll buffers).
- net/http in C shows 2,003 goroutines for 1,000 connections: the connection goroutine plus net/http's background reader goroutine that watches for a closed client while a handler runs.

## What this means for the foundation choice
- Idle memory per connection: event loop wins by 10x-17x against net/http and fasthttp (1.1-1.9 KB vs 16-20 KB), by 3x-6x against the best simple goroutine server (hand (a) 7.8 KB; hand (b) 3.7 KB, so the gap falls to about 2.5-3x). A goroutine server with a tuned buffer discipline gets to 2.9 KB heap+stack per connection, 3.7 KB RSS; the floor is the goroutine stack (about 2 KB, measured as stack_inuse) plus Go netFD objects. At 1M idle connections that is 3.7 GB vs 1.1-1.9 GB, not 20 GB vs 1 GB: the 10x numbers are mostly a statement about net/http's and fasthttp's buffer defaults, not about goroutines.
- Memory while handlers block (C, the normal "database call" case): the advantage mostly disappears. fib (sync) peaked at 77.6 MB vs net/http 59 MB, hand-written 17-19 MB; nbio 35 MB, fasthttp 30 MB. An event loop whose handler blocks pays a goroutine (and stack) per blocked request on top of its pool, i.e. the goroutine-per-connection cost moves from connections to in-flight requests. It stays ahead only if handlers are non-blocking or use the async style (fib-async 28.4 MB), which is a worse developer experience (Retain/Release, callbacks).
- Throughput of plain requests (B): not decided by the model. hand-written goroutine server: 1.45M req/s, fasthttp 1.19M, fib 1.14M (event loop), hertz 0.99M, net/http 0.62M, nbio 0.54M. The fastest of all was a goroutine-per-connection server; the event-loop engines are not faster on 2 physical cores. Allocation per request is the larger factor (13 vs 0 per request in D). The 4 KiB pool variant (b) cost about 5 percent throughput.
- Tail latency in C was identical (p99 12.1-13.8 ms) across all models; none stalled.
- Not measured: pool exhaustion (handlers blocked beyond the cap of 4000/4096), TLS, large bodies/slow clients, pipelining, GC pauses with millions of goroutines, and real kernel memory per socket. A decision toward event loop + worker pool needs the stall case measured before it is trusted.
