# A handler's context is cancelled when the client goes away and when the handler returns

The `ctx` a typed handler receives is cancelled when the connection closes while the handler runs and when the handler returns, as net/http does, so a Postgres query started with it stops when nobody is waiting for its answer. Gin's context does nothing on disconnect unless `ContextWithFallback` is set, and fib's `Request.Context()` is never cancelled ([`docs/research/`](../research/README.md)); both mean a database keeps working for a client that has left.

## How a disconnect is noticed

Noticing a disconnect while the handler runs needs something watching the socket. net/http pays for that with a goroutine for every request in flight (2,003 goroutines for 1,000 blocked requests, [`docs/research/engines/`](../research/engines/report.md)). Here, on Linux:

- **One watcher goroutine per engine** owns an epoll instance of its own. A connection's descriptor is added to it with `EPOLLRDHUP` while its handler runs, and removed when the handler returns. The watcher never reads, so the bytes of a pipelined next request stay where they are.
- **It is armed lazily.** The descriptor is added only when someone first calls the context's `Done()` or `AfterFunc`. A handler that never looks at its context pays nothing; one that queries Postgres pays two `epoll_ctl` calls against a round trip of hundreds of microseconds. Arming allocates nothing only because the hangup callback is made once per connection and reads the current context from an atomic pointer; a closure made per arming would cost an allocation a request. Off Linux, and for a wrapped connection, a goroutine reads one byte instead; a byte that arrives is kept for the next request, as net/http does.
- **Elsewhere** (macOS, Windows) a background reader goroutine per request in flight does the same job, as net/http's does, and stores what it reads for the next request.

**Telling a disconnect from other cancellation.** The cause of a disconnect is `ErrClientGone`, read with the context's own `Cause()` method. `context.Cause(ctx)` cannot see it and returns `context.Canceled`, because the standard library finds a cause only through a key it does not export; contexts derived with `WithCancel` or `WithTimeout` inherit that limit.

**The cause is exported, with a function that reads it** (built 2026-11-01, milestone 3 wave 0). `bites.ErrClientGone` is the cause, the same value as the engine's and not a copy, so `==` holds, beside `ErrServerClosed`, which already is. `bites.Cause(ctx) error` asks the context for its own `Cause()` when it has one and falls back to `context.Cause` otherwise, so a handler writes `if bites.Cause(ctx) == bites.ErrClientGone` with no type assertion. It finds the request's context through any context derived from it, because the request's context answers `Value` with itself for a key of the framework's own, so `Cause` of a `WithValue` or `WithCancel` child is still `ErrClientGone`; a cause of the child's own (its deadline, the cause given to its cancel) is reported as it is (`cause_test.go`). It allocates nothing. Exporting the error alone was rejected: every application would write the same type assertion, and one that used `context.Cause` would compare against `context.Canceled` and never match, silently.

**The context's parent is never cancellable.** Arming a context whose parent can be cancelled costs three more allocations a request (measured: 5 instead of 2), so server shutdown does not reach requests through a parent context; the engine cancels the contexts of the requests it is closing itself.

**Requests on one connection are served in order.** A pipelined request is read after the response to the one before it is written; bytes already received for it wait in the connection's buffer, which is then kept rather than returned to the pool.

**What it cannot catch:** a client that half-closes its side after sending the request looks like a disconnect and is cancelled, as with net/http. Behind a load balancer that reuses its connections to the service, a client that leaves is never seen here at all, by this design or any other; the balancer's own timeout is what bounds that request.

**What it costs:** Linux-specific code through `syscall`, and two paths that must both be tested. The cost a request is one allocation for the context and one for its done channel when used ([ADR 0003](0003-the-trade-budget.md)), and a watch and unwatch pair when armed, measured at about 480 ns on a Ryzen 7 9700X.

**Considered:** a background reader on every platform (simplest, about 2 KB of stack a request in flight); no detection at all (breaks the promise above); arming on every request (two system calls on every request, about a tenth of a loopback request's whole cost).
