# Testing

**How an application is tested with no port: the engine takes a listener of in-memory connections, the app takes a clock that moves only when told to, and the same path runs as in production.**

**Guide:** [testing](../guide/testing.md) · **Reference:** [bitetest](../reference/bitetest.md)

## Overview

The engine takes its listener as an interface. The `bitetest` package provides one whose connections live in memory: they honour deadlines, carry a remote address the test sets, and can be closed from the client's side at any moment. The app takes a `Clock`, and `bitetest.Clock` moves only when advanced, so a timeout or a heartbeat is tested without sleeping.

```text
test --> bitetest.Start(t, app) --> Server over in-memory connections
              Do(Request)   -> Response          a request and its answer
              Dial(remote)  -> net.Conn          the test writes its own bytes: pipelined, a client that leaves, a slow head
              Read(conn)    -> Response          reads one response from such a connection
              DialWS(req)   -> *WS               a WebSocket client: send, read, answer pings, close
         bitetest.Clock  -> WithClock(...)       advance it to fire a timeout or a missed heartbeat

what runs: the engine's own read, parse, route, chain, write path. what does not: the kernel.
```

Because nothing is skipped between the bytes and the handler, a disconnect, a timeout, a stream and a WebSocket are testable, which a function from request bytes to response bytes could not express. The framework's own tests use it for almost every behaviour and the allocation budgets are measured through it.

## Rules

1. **The engine takes its listener as an interface, and a test package provides one whose connections live in memory.** They honour deadlines, carry a remote address the test sets and can be closed from the client's side at any moment, so a request, its 400, a client leaving mid-handler and TLS over a pipe are testable with no port. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)

2. **The app takes a `Clock`, and the test package has one that moves only when told to.** A timeout, a missed heartbeat and a wheel tick are tested by advancing it, and nothing in the framework sleeps for a test. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)
3. **An entry point that takes bytes and returns bytes was rejected.** It could not express a WebSocket, a stream, a disconnect or a timeout, so half of the reference workload would have been untestable without a port; the in-memory connection runs the same engine path instead. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)

4. **A test waits for an Observer's End on the connections, not by sleeping.** The engine's connection reads its next request only after End, so `Server.WaitEnded` waits for every in-memory server end to be closed or blocked in `Read`; the memory connection is the only code that changed, and a real listener pays nothing. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)
5. **A smaller set of tests runs over loopback for what memory cannot simulate.** A reset connection, a partial write, a full socket buffer and real epoll readiness belong to the kernel; an in-memory connection also does not park or use the disconnect watcher's epoll path. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)

6. **The WebSocket client reads on its own goroutine, answers pings by default and times out in real time.** A test dials a route, sends text, binary and JSON, reads messages and pings, closes with a status and reads the server's close, and never hangs; a slow reader or a malformed frame is still written by hand over `Dial`. A stream of events is read the same way, with `OpenEvents`. `Quiet` asserts that nothing arrived and `Pending` returns what did, one wait under two names (a ping with a payload of its own, read up to its pong); a frame queued by a goroutine the ping does not wait for passes too early, and `bitetest.Broker.Async` makes the in-memory broker deliver on a goroutine for each subscription, as `modules/pgnotify` does, so a test can show it. [ADR 0074](../adr/0074-the-bitetest-websocket-client-reads-on-its-own-goroutine-and-times-out-in-real-time.md)
7. **JSON is read back as the app's clients read it.** `Response.JSON`, `Message.JSON`, `WS.ReadJSON` and `bitetest.JSON[T]` decode with the app's own codec and naming policy, so a handler's own type, an `Opt`, a `Null` and a tagged union fill; the read is lenient (an unknown member is skipped, a missing one leaves its field zero), and a type the codec has no plan for (`map[string]any`, `any`) or a value with no app behind it falls back to `encoding/json` with names matched without case, underscores and dashes. A test of the exact wire names compares the text. [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)
8. **The API document is held to a committed file, and a difference is a list of changes.** `bitetest.Document` canonicalizes `App.OpenAPIDocument` (never a request), fails with one structural line per change (`DiffDocuments` is the same comparison with no file) and rewrites the file under `BITETEST_UPDATE=1`; a missing file fails and is not written, so a first run in CI cannot record whatever it found. [ADR 0075](../adr/0075-the-api-document-is-held-to-a-committed-file-by-a-structural-diff.md)

9. **A store or a `Broker` is held to its interface by a suite the implementer calls.** `bitetest.TestRateLimitStore`, `TestSessionStore`, `TestOwnerSessionStore` and `TestBroker` run one subtest for each promise of the interface's doc comment (atomic per key under concurrent callers, an expired session never loaded, `DeleteOwner` and its `keep`, a payload over the limit an error and never cut), against a factory that returns a fresh store for each subtest (`TestOwnerSessionStore` takes an optional `OwnerSessionSuite` for a store whose owners must be real rows or that refuses an empty owner); the core runs them on its in-memory stores and `bitetest.Broker`, and each Postgres module on a real database. [ADR 0086](../adr/0086-a-store-is-held-to-its-interface-by-a-conformance-suite-in-bitetest.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md) | in-memory connections and a manual clock run the engine path, with a loopback set for the kernel |
| [0086](../adr/0086-a-store-is-held-to-its-interface-by-a-conformance-suite-in-bitetest.md) | a store or a `Broker` is held to its interface by a conformance suite in `bitetest` |
| [0074](../adr/0074-the-bitetest-websocket-client-reads-on-its-own-goroutine-and-times-out-in-real-time.md) | the WebSocket client of `bitetest` |
| [0075](../adr/0075-the-api-document-is-held-to-a-committed-file-by-a-structural-diff.md) | the API document held to a committed file by a structural diff |

## Open questions

- **The WebSocket client cannot show backpressure or send a broken frame**, because it reads eagerly and speaks well-formed frames; those tests use `Dial` and bytes ([ADR 0074](../adr/0074-the-bitetest-websocket-client-reads-on-its-own-goroutine-and-times-out-in-real-time.md), [ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md)).
- **A trusted proxy is tested through the remote address**: `bitetest.Request.Remote` set to an address inside the listed networks, as `proxies_test.go` does; a TLS connection, for `Request.Scheme`, needs a loopback listener ([ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md), [engine](engine.md)).
- **A `bitetest` helper that checks a response against an enum's `Values()`** is a claim, not built ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).
- **Over loopback, not memory:** TLS throughput, allocations a request over TLS and Autobahn conformance are not run ([roadmap](../roadmap.md#nice-to-have-what-few-applications-need), and Autobahn under [Fairly important](../roadmap.md#fairly-important-what-many-applications-need)).
- **The documentation tests** are the other half of how this repository is held true ([docs tooling](docs-tooling.md)).
