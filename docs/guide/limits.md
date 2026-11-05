# Limits and timeouts

**Every limit is on by default, none can be switched off, and a limit that is passed has its own answer.**

**Reference:** [`Limits`, route options](../reference/app.md#limits) · **Design:** [Limits](../design/limits.md)

A server that has no limit has one anyway: its memory. bites chooses the limits for you, writes each one in the start-up log marked `default` or `custom`, and lets you change them on the app, a group or a route ([ADR 0024](../adr/0024-limits-are-on-by-default.md), [ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md)). A zero field of [`Limits`](../reference/app.md#limits) takes its default; a negative one makes `New` panic, because a `-1` read from a config as "unlimited" is a mistake to stop at boot and not to replace silently.

<!-- compiles: decls -->
```go
func newApp() *bites.App {
	return bites.New(bites.WithLimits(bites.Limits{
		MaxBody:      4 << 20,
		IdleTimeout:  2 * time.Minute,
		WriteTimeout: 10 * time.Second,
	}))
}
```

## The limits and their defaults

**Each field of [`Limits`](../reference/app.md#limits) is given to [`WithLimits`](../reference/app.md#withlimits); the fields you leave at zero keep these defaults.**

| Field | Default | When it is passed |
|---|---|---|
| `MaxHead` | 8 KiB | the request head is answered 431 |
| `MaxFields` | 100 | more header fields are a 431; more fields of a `Form` body are a 413 |
| `MaxBody` | 1 MiB, per route | a 413 |
| `HeadTimeout` | 10 s | the connection is closed |
| `BodyTimeout` | 30 s, per route | a 408 |
| `IdleTimeout` | 60 s | the idle keep-alive connection is closed |
| `WriteTimeout` | 30 s | the connection is closed |
| `MaxConns` | 90% of the file descriptor limit | the connection is not accepted |
| `MaxDepth` | 64 levels of JSON nesting | a 400 |
| `HandshakeTimeout` | 10 s | the TLS handshake fails and the connection is closed |
| `MaxMessage` | 64 KiB | the WebSocket is closed with 1009 |
| `MessageTimeout` | 30 s | the WebSocket is closed with 1008 |
| `ReassemblyBudget` | 64 MiB | the WebSocket that would pass it is closed with 1013 |
| `SendQueue` | 1 MiB | the WebSocket is closed with 1008 |
| `SendBudget` | 64 MiB | the WebSocket that would pass it is closed with 1013 |
| `HeartbeatInterval` | 30 s | (how often the server pings) |
| `MissedPongs` | 2 | the WebSocket is closed with 1008 |
| `MaxFieldBytes` | 64 KiB, one text field of a `Multipart` form | a 413 located at the field |
| `MaxFormBytes` | 1 MiB, all the text of a `Multipart` form | a 413 located at the field that passed it |
| `UploadMemory` | 32 MiB of `Multipart` file bytes in memory, all requests together | the file goes to a temporary file, and the client is told nothing |
| `UploadDir` | `os.TempDir()` | (where those temporary files go; `New` panics when it does not exist or is not writable) |

The values are in `Limits.withDefaults` and in the table of [ADR 0024](../adr/0024-limits-are-on-by-default.md), which also says why each was chosen. The errors listed in one 400 are capped at 20 and not configurable.

The start-up log marks each limit on its own line, so a limit you set is `custom` even when its neighbours are `default`:

```text
level=INFO msg="bites: settings" setting="max body bytes" value=4194304 source=custom
level=INFO msg="bites: settings" setting="idle timeout" value=2m0s source=custom
level=INFO msg="bites: settings" setting="write timeout" value=10s source=custom
```

## Raising the body limit for one route

**`MaxBody` and `BodyTimeout` are also options of a route or a group, and the nearest one wins.** A group's options apply to every route in it and in the groups nested in it, and a route's own beats its group's ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)).

<!-- compiles: decls -->
```go
type Import struct {
	Body []struct{ Name string }
}

func importUsers(ctx context.Context, in Import) (bites.NoContent, error) {
	return bites.NoContent{}, nil
}
```

<!-- compiles: body -->
```go
bulk := app.Group("/bulk", bites.MaxBody(16<<20), bites.BodyTimeout(time.Minute))
bulk.Post("/users", importUsers)
bulk.Post("/small", importUsers, bites.MaxBody(1<<10))
```

[`MaxBody(n)`](../reference/input.md#maxbody) is the most bytes of request body the route reads, and [`BodyTimeout(d)`](../reference/input.md#bodytimeout) is how long it waits for the body. Both must be positive; `MaxBody(0)` or a nil option is refused when the group or route is made, naming where, even if the group has no route yet ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

**An upload route sets its own limits.** A route whose Body is an `Upload` has no default `MaxBody`: how much it may receive is the application's decision, so a route without one is refused at Registration. Its body timeout is derived from the size, `max(30 s, MaxBody / 128 KiB/s)`, and logged as `default`. Two more options bound it, and both are explained in [forms and uploads](./forms-and-uploads.md):

- [`MaxParts(n)`](../reference/input.md#maxparts) is the most parts of one upload, 128 by default; more is a 413.
- [`MaxInFlight(n)`](../reference/input.md#maxinflight) bounds the uploads of the route running at once. It has no default, because one would turn away real traffic, and the start-up log says `unlimited` for a route without it.

**A `Multipart` route sets `MaxBody` the same way** and adds bounds of its own: one file is bounded by the `MaxSize` of its type (a 413 at the file's field), a request keeps at most 256 KiB of files in memory (fixed in code), and the app keeps `UploadMemory` in all; past either a file waits on disk, so the disk a route may use is `MaxBody` a request and `MaxInFlight` requests. The 256 KiB and the 32 MiB are judgements, not measurements of a workload. The start-up log states the worst case of each such route and warns when `UploadDir` is on `tmpfs`, where a file written is memory. See [forms and uploads](./forms-and-uploads.md#receiving-a-form-with-its-files).

[`MaxMessage(n)`](../reference/websocket.md#maxmessage) is the same kind of option for a [WebSocket](./websockets.md) route or group.

## What a client sees when a limit is passed

**Most limits are an answer in problem+json; the ones that protect the connection close it.** The exact body is the `detail` shown.

```http
POST /bulk/small HTTP/1.1
Host: api.example.com
Content-Type: application/json
Content-Length: 4000

HTTP/1.1 413 Content Too Large
Content-Type: application/problem+json
Connection: close

{"title":"Content Too Large","status":413,"detail":"request body too large: this route reads at most 1024 bytes"}
```

- **413, `request body too large: this route reads at most N bytes`.** The body is over the route's `MaxBody`, which the detail names. A declared `Content-Length` over it is answered before `100 Continue`, so a client that waits for it never sends the body; a chunked body over it is answered as it arrives. The connection closes. A `Form` with more fields than `MaxFields`, or an upload with more parts than `MaxParts`, is also a 413 with its own detail (`request body has too many fields`, `the upload has more parts than this route allows`).
- **408, `request body too slow`.** The body did not arrive within the route's `BodyTimeout`; a client that sends a byte a second is caught here. The connection closes.
- **431, `request head too large` or `too many header fields`.** The head is over `MaxHead`, or has more than `MaxFields` fields. The connection closes.
- **503 with `Retry-After: 5`.** The upload route already has `MaxInFlight` uploads running, answered after the head and before `100 Continue`, so the client never sends the body. The connection closes.
- **400.** JSON nested deeper than `MaxDepth`.
- **Nothing.** A head that does not arrive within `HeadTimeout`, a TLS handshake over `HandshakeTimeout`, an idle connection over `IdleTimeout`, and a write that takes longer than `WriteTimeout` close the connection with no answer, because there is nobody to answer or nothing the client would read. A connection over `MaxConns` is not accepted: the engine stops calling `accept` and the kernel's backlog holds the rest.

## Timeouts

**Four timeouts bound a connection, and none of them is the time a handler may take.** A handler's own deadline is [`Timeout`](./first-party-middleware.md#cutting-off-a-slow-handler), a middleware you add to the routes that need it.

- `HeadTimeout` is the time to read a request head. It bounds a client that opens a connection and trickles bytes.
- `BodyTimeout` is the time to read a body, per route.
- `IdleTimeout` is how long a keep-alive connection waits for its next request. It is 60 seconds, above common load balancer idle timeouts, so the balancer closes first. Idle connections are [parked](./deploying.md#idle-connections-and-memory) and cost little.
- `WriteTimeout` is the time one write may take. A [stream](./streaming.md#slow-clients-and-write-deadlines) applies it to every chunk of at most 64 KiB and lifts only the total duration, so a long download is not cut and a client that never reads is.

A test moves these on a `bitetest.Clock` and never waits: see [testing](./testing.md#moving-time).

## WebSocket limits

**The WebSocket limits are the last group of `Limits`, and each closes the socket with the status RFC 6455 gives.** `MaxMessage` closes with 1009; a message not finished in `MessageTimeout` with 1008; `ReassemblyBudget` and `SendBudget` with 1013 for the socket that would pass them; `SendQueue` with 1008; and a ping unanswered `MissedPongs` times in a row, one every `HeartbeatInterval`, with 1008 ([ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md), [ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md), [ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)). A route whose `MaxMessage` is above the app's `ReassemblyBudget` is refused at Registration, because its message could never be assembled.
