# What an idle WebSocket costs in Go, by server model (measured on this machine, 2026-10-30)

Repeat: build with `go build` into the scratchpad ws-bin (`servers/*`, `client`), then `./runall.sh` (idle, 3 interleaved runs, 7 servers), `./runall2.sh` (park ablation + broadcast), `./runall3.sh` (broadcast with ack), `python3 -I analyze.py`. Raw JSON in `raw/`, server logs in `raw/*.srvlog`, one discarded broadcast-with-ack set (client bug, infinite ack ping-pong) in `raw_discarded/`.

## Setup
- AMD Ryzen 7 9700X, 8 cores/16 threads, SMT siblings n and n+8 [probe: lscpu -e]. Server `taskset -c 2,3,10,11`; client `taskset -c 4-7,12-15` [probe]. GOMAXPROCS seen by the servers: 4 [probe].
- go 1.27.3. Versions (go get @latest today): gws v1.10.2, coder/websocket v1.8.15, gorilla/websocket v1.5.3, lesismal/fib v1.6.0, lesismal/nbio v1.7.1 [probe: go.mod]. Go downloaded the new modules into the default module cache (~/go/pkg/mod), the only write outside the research folder and the scratchpad.
- Method as in `../engines/report.md`: RSS from VmRSS, heap+stack = HeapInuse+StackInuse from MemStats on SIGUSR1, 5 s settle before each snapshot, marginal = (20k - 10k)/10k, client source IPs 127.0.0.2-5 with SO_REUSEADDR + IP_BIND_ADDRESS_NO_PORT + SO_LINGER 0 on close [code: client/main.go]. No dial failures in any run [probe: fails=0].
- Client per connection: upgrade GET /ws, one masked 6-byte text frame, read the 8-byte echo, then idle (no goroutine per connection on the client in idle runs) [code].
- Servers: all default settings. gws `ServerOption{}` (ParallelEnabled false), `go c.ReadLoop()` after `Upgrade`. coder `Accept(w, r, nil)` + Read/Write loop. gorilla `Upgrader{}` + ReadMessage/WriteMessage. fib `websocket.NewHandler` with `HandlerFuncs{Message}` under `fib.DefaultConfig()`. nbio `nbhttp.NewEngine` + `websocket.NewUpgrader` + `OnMessage` [code: servers/*/main.go].
- Added to the stats dump (not in the earlier research): `/gc/scan/stack:bytes` (stack actually in use, measured after two forced GCs via SIGHUP after the 20k snapshot, so the main numbers were taken without that GC) [code: common/common.go].
- Runs: 3 per server for idle, 3 for broadcast, 3 for broadcast with ack. Nothing was cut. The whole thing took about 30 min.

## Hand-written servers [code: hand/hand.go]
- (b): handshake by hand (Sec-WebSocket-Accept via sha1+base64, request read into a pooled 4 KiB buffer, returned straight after). Frame loop: `io.ReadFull(c, hdr[:2])` on a per-connection 2-byte array, so while idle the goroutine holds no pooled buffer; extension length and mask are read into a small array; then a pooled buffer takes the payload, it is unmasked, the reply header is written just before the payload in the same buffer and sent with one `Write`; buffer returned. Text/binary/ping/close, no fragmentation, no compression, frames up to 4096 bytes.
- (b2): the same loop, but the blocking read goes straight to the fd through `net.TCPConn.SyscallConn().Read(cb)` with `syscall.Read`, to take frames out of the stack at the park point (net.Conn.Read -> netFD.Read -> poll.FD.Read are gone). The callback is a method on a per-connection struct so it allocates once per connection.
- park (ablation, not asked for): same handshake and first echo, then the goroutine blocks on a nil channel instead of the socket. It shows what a goroutine plus its net.Conn costs with no read in flight.

## Idle results (mean [min-max] over 3 runs; bytes per connection)
| server | RSS @10k avg | RSS marginal 10k to 20k | heap+stack @10k avg | heap+stack marginal | StackInuse per conn @20k | in-use stack per conn (after GC) | goroutines @20k |
|---|---|---|---|---|---|---|---|
| hand (b) | 3678 [3675-3684] | 3615 [3544-3756] | 2924 [2914-2933] | 2947 [2945-2950] | 2059 | 913 | 20003 |
| hand (b2) shallow | 3694 [3686-3706] | 3692 [3556-3768] | 3044 [3040-3047] | 3022 [3008-3032] | 2061 | 793 | 20003 |
| hand park (ablation) | 3680 [3679-3683] | 3543 [3541-3544] | 2913 [2905-2919] | 2944 [2937-2952] | 2058 | 417 | 20003 |
| gws | 13498 [13209-14073] | 13058 [11919-14708] | 11693 [11380-12066] | 11581 [11095-12481] | 4104 | 993 | 20003 |
| coder/websocket | 23874 [23728-23951] | 23789 [23509-24131] | 22576 [22557-22594] | 22536 [22507-22553] | 8205 | 3137 | 20003 |
| gorilla | 17639 [17498-17710] | 17285 [17283-17286] | 16418 [16417-16420] | 16429 [16418-16442] | 4106 | 2545 | 20003 |
| fib (event loop) | 1540 [1470-1681] | 1292 [1077-1512] | 1107 [964-1371] | 1118 [1022-1265] | 5 | 1 | 5 |
| nbio (event loop) | 2181 [2045-2250] | 1452 [1305-1536] | 1668 [1176-1914] | 1060 [514-1402] | 2 | 1 | 9 |

All [probe]. Goroutine counts include 3 server housekeeping goroutines in the hand servers; the net/http-based ones show the same 20003 because hijacked connections keep their goroutine [probe]. The event loops are noisier (spread of about 400 B); the first 10k of nbio costs more than the marginal, as in the earlier research [probe].

## Broadcast, hand (b) only [probe; code: servers/hand/main.go]
20k idle sockets, then on SIGUSR2 the server writes one prebuilt 8-byte frame to every socket, 4 writer goroutines over the registry, once per second for 10 s. The registry (a map of conns) is on only for these runs (`-push`), so it is not in the idle numbers.
- Push only, 3 runs: all 200,000 messages received, a tick takes 25-37 ms for 20k sockets. RSS 82.0-84.3 MB before, peak 84.2-86.8 MB (+2.2 to +2.6 MB, +3%), after two forced GCs 82.5-84.9 MB. The pool is not touched by a write: Gets=Puts=40,001 (the 20k handshakes and 20k first frames) before and after, buffers ever allocated 26-37.
- Push plus a 6-byte ack from every client on every push (this exercises the read pool): 400,000 messages received (200k pushes + 200k echoes of the acks), peak RSS 88.4-89.0 MB (+4.7 to +6.9 MB over the 82.0-84.2 before), 83.9-85.2 MB after two GCs. Pool Gets and Puts both rise to 240,001, equal: every buffer came back. Buffers ever allocated stayed 26-28 (the pool does not grow with 20k sockets: concurrent holders are about the number of running Ps). HeapInuse went 19.0 to 25.6 MB (garbage) and returned to 18.6-18.8 MB after GC.
- The ack runs show about 1 malloc per ack (200,300 mallocs for 200k acks). Cause [code, `go build -gcflags=-m`]: `ext` (the extension/mask scratch array) is passed to an indirect call and moves to the heap on every frame; `hdr` moves to heap once per connection. Fixable with a per-connection struct holding both; not fixed here.

## Why the numbers look like this
- Stack floor [probe+code]: StackInuse is 2059 B per connection in every hand variant and 4104-4106 B in gws and gorilla, 8205 in coder. Go goroutine stacks are powers of two with a 2 KiB minimum; the idle hand goroutine uses only 913 B (793 for b2, 417 parked on a channel) of it. So shortening the stack at the park point lowers stack in use by 120 B but the allocated stack and the RSS do not move: b2 is 77 B worse in RSS marginal (within spread) because of its extra per-connection struct. The park ablation (no read in flight at all) is 3543 vs 3615 RSS, also within spread. What is left after the 2 KiB stack, about 0.9 KB heap+stack-bookkeeping and about 0.7 KB RSS beyond MemStats, is net.Conn objects, the g struct, runtime bookkeeping and page rounding, none of which our code decides [probe; the split of that 1.5 KB into parts was not measured].
- gws [code]: `Upgrade` takes a `*bufio.Reader` from `brPool` (4 KiB by default: `defaultReadBufferSize = 4 * 1024`, option.go) and keeps it for the life of the connection; it is returned only at close (conn.go). Its stack is 4 KiB [probe]; why (frame depth outgrowing 2 KiB) is my inference, not checked.
- gorilla [code, server.go]: with `ReadBufferSize == 0` the upgrader reuses the hijacked net/http `bufio.Reader` (4096) and the hijacked writer buffer as the connection's buffers, so 4 KiB read + 4 KiB write stay for life, plus a 4 KiB stack.
- coder/websocket [code, accept.go]: keeps the hijacked `brw.Reader` and `brw.Writer` (`br: brw.Reader, bw: brw.Writer`) and wraps the reader in an `io.MultiReader`; its stack is 8 KiB, the largest, and it also keeps a goroutine-per-connection net/http handler. I did not find which of its frames force 8 KiB.
- fib and nbio [probe, prior research docs]: 5 and 9 goroutines in total for 20k sockets, so no per-connection stack. nbio's Conn struct has a send queue, mutex and several fields [code: nbhttp/websocket/conn.go]; its default read buffer is 64 KiB per engine reader, not per connection [docs: engine.go comment "ReadBufferSize ... 64k by default"; the sharing was not verified in code].
- Kernel socket memory is not in RSS for any of these and was not measured here.

## What this means for a goroutine-per-connection engine serving idle WebSockets
- Gap to the event loops, marginal RSS [probe]: hand (b) 3615 B vs fib 1292 B (+2323 B, 2.8x) and nbio 1452 B (+2163 B, 2.5x).
  - At 100k sockets: hand 0.36 GB, fib 0.13 GB, nbio 0.15 GB, so the goroutine model costs about 0.22-0.23 GB more.
  - At 1M sockets: hand 3.6 GB, fib 1.3 GB, nbio 1.5 GB, so about 2.2-2.3 GB more (linear extrapolation of the 10k-20k marginal; I did not run 100k or 1M, and GC cost with 1M goroutines is not measured).
- Gap to the libraries people reach for [probe]: gws 13.1 KB, gorilla 17.3 KB, coder 23.8 KB. At 1M sockets that is 13, 17 and 24 GB against 3.6 GB for the hand server (a saving of 9.4, 13.7 and 20.2 GB). Most of that is buffers held per connection, not the goroutine.
- The goroutine model has a floor that code cannot lower: a 2 KiB stack is 57% of the 3.6 KB. Everything we controlled (no buffer while idle, shallower park point, no goroutine-held net.Conn read) moved the number by less than the run-to-run spread. The remaining lever is to stop having a goroutine per idle socket, which is the event loop.
- Pooled buffers hold up under broadcast: pushes do not touch the read pool, and when every client answers, buffers came back one for one (Gets=Puts) and the pool stayed at 26-28 buffers for 20k sockets. Peak RSS rose 3-8% and returned after GC.
- Not measured: 100k+ sockets, TLS (a per-connection tls.Conn adds its own buffers), permessage-deflate (gws/coder/gorilla/fib/nbio all keep compression state per connection when it is on), large or fragmented messages, slow readers during broadcast (a blocking Write on a stalled client holds a writer goroutine), and the cost of the heartbeat/ping timers a real deployment adds.
