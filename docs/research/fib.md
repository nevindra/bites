# fib at 7dcb41d

Read on 2026-10-30 from its README and `docs/hacks.md`; measured in [`engines/`](engines/report.md) at tag v1.6.0.

fib (github.com/lesismal/fib, by the author of nbio) is an engine, not a framework: a few event loops wait on epoll, kqueue or IOCP, and a shared worker pool reads, parses, runs the handler and writes. An idle connection is a struct in an fd-indexed table, with no goroutine and no buffer. Handlers take the standard `*http.Request` and an `http.ResponseWriter`, so chi or ServeMux run on it.

What it trades, in its own words:

- **Object reuse is on by default** (`ReuseRequests`, `ReuseHeaders`, `ReuseURLs`, `ReuseContexts`), "under the same lifetime rule as fasthttp and Fiber". A goroutine that keeps `*http.Request` past the response reads another request's data, and the hack guide says "the bug is silently swallowed".
- **`c.Request.Context()` is not cancelled** when the client goes away; `c.Context()` is the one that is.
- **No `http.Hijacker`**, so gorilla/websocket does not run on it.
- **Blocking in a handler is anti-pattern number one**: it holds a worker. The way out is `c.Retain()` / `c.Release()` with the work on another goroutine, a callback style.

Repository facts on 2026-10-30 (GitHub API): created 2026-09-14, 664 commits, one human contributor, 36 stars, about 198,000 lines of Go including tests, its own TLS record layer and QUIC, Go 1.27.3 minimum.

What the measurement found ([`engines/`](engines/report.md)): 1,078 B per idle connection against 3,408 B for a hand-written goroutine server, but 77.6 MB against 19.3 MB at 1,000 handlers blocked for 10 ms, because its pool grew to about 3,960 goroutines. With the async style it was 28.4 MB and about 7% slower than its sync style.
