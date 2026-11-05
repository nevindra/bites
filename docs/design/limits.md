# Limits

**Every limit is on by default, a limit that is exceeded has its own status and is never a 400, and no limit can be switched off.**

**Guide:** [limits](../guide/limits.md) · **Reference:** [app](../reference/app.md), [input](../reference/input.md)

## Overview

A server that waits for a client on a goroutine per connection needs a bound on everything the client controls: the size of the head, the number of fields, the size of the body, and the time each takes. The framework ships a default for each ([ADR 0024](../adr/0024-limits-are-on-by-default.md) has the table with the value and the answer for every one), so a hello world is already safe against a trickling client.

```text
size     head, header fields, form fields, body (per route), JSON nesting, errors listed
time     head, body (per route), TLS handshake, idle keep-alive, write (per chunk of a stream)
count    connections (a share of the file descriptor limit), upload parts, uploads in flight (per route)
WebSocket  message, message timeout, reassembly budget, send queue, send budget, heartbeat

set by:  WithLimits(Limits{...})  on the app      zero field = default, negative field = panic in New
         MaxBody, BodyTimeout, MaxParts, MaxInFlight  as options on a route or a group
```

The start-up log prints every limit, each marked `default` or `custom` on its own line, and the effective connection limit is the computed number, not the word "default".

## Rules

1. **Every limit is on by default and has the answer that says what went wrong.** A head over its limit is a 431, a body over its limit a 413, a body read too slowly a 408 with the connection closed, a connection past the limit is not accepted, and a WebSocket over a limit is closed with the code that names it. A limit is never the 400 of the input errors, because the request was not wrong in its content, it was too large or too slow. [ADR 0024](../adr/0024-limits-are-on-by-default.md)

2. **A limit cannot be switched off.** A zero field of `Limits` means the default and a negative one makes `New` panic naming the field, since a `-1` read from a configuration as "unlimited" is the mistake this catches; `MaxBody(0)` on a route or a group is refused at Registration. The check is in `New`, so a test app that never starts is told. [ADR 0024](../adr/0024-limits-are-on-by-default.md)
3. **The body limit and the body timeout are per route, and a group's option applies to its routes.** The app-level value is the default for every route that states none. [ADR 0024](../adr/0024-limits-are-on-by-default.md)

4. **An `Upload` or `Multipart[T]` route must state `MaxBody`, and only its time has a derived default.** One number about the application's storage is the author's decision and a default is wrong in both directions; the body timeout is derived from `MaxBody` at a rate floor and logged as `default`. A request in flight is bounded by connections generally, and only an upload route may bound them with `MaxInFlight(n)`, which has no default because a default would 503 legitimate traffic. [ADR 0024](../adr/0024-limits-are-on-by-default.md)
5. **A `Multipart[T]` also bounds the memory of its files, and what it cannot hold waits on disk.** A request keeps at most 256 KiB of files in memory and the app `Limits.UploadMemory` (32 MiB) in all, past which a file waits in an unlinked temporary file in `Limits.UploadDir`; one text field is bounded by `Limits.MaxFieldBytes` (64 KiB) and all the text of a request by `Limits.MaxFormBytes` (1 MiB), each a 413 located at the field. The 256 KiB and the 32 MiB are judgements, not measurements of a workload, and a spill directory on tmpfs is memory, which the start-up log warns about instead of refusing. [ADR 0024](../adr/0024-limits-are-on-by-default.md), [ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)
6. **A streaming response applies the write timeout to every chunk and lifts only its total.** A deadline over the whole of a large write would kill a slow but steady client, and a stream that never ends would otherwise have none. [ADR 0024](../adr/0024-limits-are-on-by-default.md)
7. **A timeout is armed only where something can wait on it.** The head's timeout is armed only for a head that did not arrive whole with the read that woke the connection, and the write deadline stays armed between the answers of a busy connection, moved forward only when less than half of it is left, so a write has between half and all of `WriteTimeout`; it is cleared when the connection goes quiet. Arming and clearing both on every request was four of a keep-alive request's five timer changes. [ADR 0024](../adr/0024-limits-are-on-by-default.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0024](../adr/0024-limits-are-on-by-default.md) | the table of limits and their defaults, the status each limit is answered with, negative values refused, `MaxInFlight` for uploads |

## Open questions

- **A concurrency limit per route**, with a backlog and load shedding, waits for a handler too costly to run unbounded; only `Upload` routes have it today ([ADR 0024](../adr/0024-limits-are-on-by-default.md), [roadmap](../roadmap.md#fairly-important-what-many-applications-need)).
- **The rate floor of an upload's derived timeout is a judgement, unmeasured**, and one deadline covers the whole body ([ADR 0024](../adr/0024-limits-are-on-by-default.md), [ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).
- **`MaxFields` is app-wide**, so a form with a long list raises it for every route ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).
- **The cost of the `MaxInFlight` counter** is a claim, not benchmarked ([ADR 0024](../adr/0024-limits-are-on-by-default.md)).
- **Which defaults a real workload would move** is not known; each value is a judgement stated in the ADR's table with its reason.
