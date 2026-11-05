# WebSockets

**How real-time connections work: message handlers and no user read loop, routes typed like HTTP routes, bounded queues, shared broadcast frames, one timer wheel, Origin checks and inbound limits.**

**Guide:** [websockets](../guide/websockets.md) · **Reference:** [websocket](../reference/websocket.md)

## Overview

A WebSocket is a route whose handler receives a typed Input and a `Socket` to send on, and returns an `Inbox` of functions the engine calls with the socket's messages and its close. User code never reads from the socket: the engine owns the read loop, so an idle socket can hold no goroutine ([ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md)).

```text
GET upgrade --> Origin check (403) --> Providers, Input, Validate, middleware --> handler(ctx, in, *Socket[Out])
                                                                                        |
                                                                                        v returns Inbox[Msg]
                                                                             101 Switching Protocols
   client frames --> engine read loop --> OnMessage(ctx, msg)   one at a time, in order
                                          OnMalformed / OnClose
   Socket.Send --> queue (1 MiB, then 1008; app budget, then 1013) --> inline write or flusher
   Topic.Publish --> one frame, shared by every subscriber's queue
   heartbeat: one timer wheel per app, ping every interval, close at the missed pongs
```

A message that arrives whole is copied once into a slab of its own and decoded from the copy, so an `OnMessage` costs one allocation ([ADR 0003](../adr/0003-the-trade-budget.md)). Idle sockets park on the engine's poller like any idle connection ([ADR 0005](../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md)), which the handler API never knew about.

## Rules

1. **A WebSocket is served by message handlers, and user code never reads from the socket.** `OnMessage` and `OnClose` are called by the engine, and a `Send` never blocks. A `Read`/`Write` connection like gorilla's would have fixed a goroutine per socket into the API for good. A blocking `OnMessage` pauses only that socket, which is the inbound backpressure. An Inbox with no `OnMessage` only sends, and a message from its client closes it with 1003. [ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md)

2. **A WebSocket route is typed like an HTTP route.** Its Input is decoded and checked exactly as an HTTP route's, with its Providers, its refusals and its 400, before the upgrade, so a request that would be refused is never upgraded; a message type is a struct, slice, map, `[]byte` or `Raw[T]`; a message that does not decode closes with 1007 unless `OnMalformed` takes it; and a binary frame to any message type but `[]byte` closes with 1003. A second, weaker pipeline for the upgrade is how authentication gets skipped. [ADR 0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md) A protocol of several kinds of frame is a tagged union in each direction, a struct of `Opt` variants with a `Discriminator` method, read by its `type` member and written with it, and described as `oneOf` with a discriminator; a variant only servers carry (a Topic's reference) is named by a `ServerOnly` method, left out of the document, never sent to a client (`Send` refuses it, a Topic drops it for its Sockets with a Warn) and still carried by the broker and heard by `Listen`. [ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)
3. **The value a route returns is called `Inbox`, not Session or Handlers.** `Session` is what a web developer expects for a login session and `Handlers` collides with the struct of route methods and with `http.Handler`. [ADR 0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md)

4. **A Socket is a handle that is safe to keep and never reused for another connection.** `Send`, `Close` and `Context` work from any goroutine, a handle kept after the socket closed only returns errors (`ErrSocketClosed`), and a parked socket is the same one woken. `OnMessage` runs one message at a time; an error closes with 1011 unless a `CloseError` in its chain chooses the status, and a panic closes with 1011. [ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)
5. **A Topic is a set of Sockets that one published message reaches.** A publish encodes and frames once, a socket leaves every topic when it closes, server push from outside a request is the application's goroutine calling `Publish`, and a Topic made with `NewTopic` reaches the sockets of every instance through a `Broker`, as does a `SocketOwners` made with `NewSocketOwners`, whose sign-out order reaches every instance and whose gap closes every socket with 1012 unless the application says otherwise, one path for the publisher's sockets too, with a gap callback when the broker may have lost messages, and a message over a limit, MaxPayload's or the broker's, is a `*PayloadError` whose `Limit` is what is left to that name, so a program publishes a reference and gives the Topic a `Resolve` (a family's is given the key), and the server's own code hears a Topic with `Listen`, a function subscriber with the same gap callback (a cache dropped on every instance); Postgres `LISTEN`/`NOTIFY` is the module `pgnotify`. `SubscribeUnless` leaves one Socket out of the messages its function skips (a User's mutes), failing closed, and a name made from data is a key of a `Topics` family, a Topic per key made with its first Socket and dropped with its last, whose `Publish` reaches a key this instance has no Socket for. [ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md), [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md), [ADR 0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md) A `SocketOwners` (one the application makes) closes every Socket of one owner, the User, when they are signed out: a handler `Add`s its Socket under the authenticated User, `Close(owner, status, reason)` returns how many it closed and logs it, and an owner is forgotten with its last Socket; there is no index in the app or the Socket.


6. **Each socket's send queue is bounded, and a reader that falls behind is closed.** A frame over `SendQueue` closes with 1008 and the app-wide `SendBudget` closes with 1013, both returning `ErrSendQueueFull`. There is no writer goroutine per socket: a socket on plain TCP with an empty queue is written inline by one non-blocking syscall, otherwise a flusher goroutine runs while the queue is non-empty. [ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md)
7. **A message sent to many sockets is framed once, and every queue shares that frame.** There is no reference count: the collector frees the frame when the last queue has written it, and all subscribers of a Topic belong to one app. [ADR 0038](../adr/0038-a-broadcast-is-framed-once-and-shared.md)

8. **Heartbeats are pings on one timer wheel for the whole app.** A timer per socket would add its memory to every idle connection; a pong only sets a flag the next tick reads and never touches the wheel; a socket that misses the configured number of pongs is closed with 1008. The idle gate is a test. [ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md)
9. **Every WebSocket route checks `Origin` before any Provider runs.** By default the Origin's authority must equal `Host`, or the `X-Forwarded-Host` a trusted proxy wrote when the app says its proxies write it, and the scheme is never inferred, because behind a TLS-terminating proxy the engine sees plain HTTP. Other origins are listed in full with `AllowOrigins`, and the dangerous choice has its own greppable name, `AllowAnyOrigin`; a refused origin is a 403, and a handshake with no `Origin` is accepted. [ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)

9a. **The app's origins are one list, shared with CSRF, and a nearer socket list must be within it.** `WithAllowOrigins` is the origins of the app's own pages; a `CSRF` with no origins of its own reads it, and a route's or a group's own list narrows it and is refused at Registration when it names an origin outside ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md), [ADR 0057](../adr/0057-csrf-trusts-the-browsers-origin-and-falls-back-to-a-double-submit-token.md)). `CORS` does not take it. A socket's end carries a fixed cause that an Observer is told ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)).

10. **Everything a client sends on a socket is bounded, each answered by the close status RFC 6455 gives.** Message size, the time to finish a message, the memory held for messages being assembled across all sockets, fragments, control frames, a ping and pong bucket, UTF-8 and unmasked frames each have a limit, and a message is assembled in size classes charged to an app-wide budget by the capacity held. [ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)
11. **permessage-deflate is not supported yet.** With context takeover each socket would keep a window about ten times an idle socket's memory; it ships in milestone 4 in the no-context-takeover mode only. [ADR 0040](../adr/0040-no-permessage-deflate-for-now.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md) | message handlers and no user read loop |
| [0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md) | the route and its messages are typed like HTTP, and `Inbox` is the name |
| [0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md) | a bounded send queue, an app-wide budget, no writer goroutine per socket |
| [0038](../adr/0038-a-broadcast-is-framed-once-and-shared.md) | a broadcast is framed once and shared |
| [0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md) | heartbeats on one timer wheel, and the idle gate |
| [0040](../adr/0040-no-permessage-deflate-for-now.md) | no compression until milestone 4, and never with context takeover |
| [0046](../adr/0046-websocket-origins-and-inbound-limits.md) | Origin check and the limits on what a client sends |
| [0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md) | `Socket`, `Inbox` and `Topic` as built |
| [0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md) | a Topic across instances through a `Broker`, and Postgres as a module |
| [0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md) | a frame of several kinds is a tagged union of `Opt` variants with a `Discriminator` method, and a variant only servers carry is named by `ServerOnly` |
| [0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md) | a name made from data is a key of a `Topics` family, and `SubscribeUnless` leaves one Socket out of a message |

## Open questions

- **An attacker who reconnects keeps the reassembly budget full**, so honest large messages get 1013 while the attack lasts; evicting the oldest assembly instead is not built. A budget too small for an app with large legitimate messages is a setting ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)).
- **The scheme of the Origin is not compared**, so an `http://` page on the same host passes; a proxy that rewrites `Host` without writing `X-Forwarded-Host` is the case for `AllowOrigins`. A client that insists on a subprotocol closes the socket itself ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)).
- **Two write paths.** The inline write and the flusher are both tested, but coalescing queued frames into one `writev` is not measured ([ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md)).
- **Throughput and Autobahn conformance are not run**, and the 6,700 closes a second at Shutdown is arithmetic ([roadmap](../roadmap.md#nice-to-have-what-few-applications-need), Autobahn under [Fairly important](../roadmap.md#fairly-important-what-many-applications-need), [ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)).
- **A parked socket is woken by each heartbeat's pong**, about 670 wakes a second at 20,000 sockets with the default interval, and each holds a goroutine for 100 ms ([ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md)).
- **A WebSocket route is in the API document as its handshake, its messages under `x-websocket`**, not as an AsyncAPI document, and a WebSocket over HTTP/2 (RFC 8441) is not planned ([ADR 0017](../adr/0017-tls-now-http2-later.md), [openapi](openapi.md)).
- **The cost of compression**, in the no-context-takeover mode, is measured before it ships, which waits for an application that asks for it ([ADR 0040](../adr/0040-no-permessage-deflate-for-now.md), [roadmap](../roadmap.md#nice-to-have-what-few-applications-need)).
- **`MessageTimeout` close status.** 1008 has no existing row to follow; 1002 and 1009 were the other candidates ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)).
