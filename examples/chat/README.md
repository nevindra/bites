# chat: the reference workload as a program

A JSON API over Postgres with long-lived WebSockets, the workload of [ADR 0002](../../docs/adr/0002-the-reference-workload.md). It is its own Go module (its `go.mod` replaces the framework with `../..`, [ADR 0070](../../docs/adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)) so the framework's root module keeps the standard library as its only dependency; the one third-party package here is `github.com/jackc/pgx/v5`, pinned at v5.10.0.

The auth is a **toy**: a token is `toy-` and a name, so `Bearer toy-ada` is the user `ada`, and anyone can mint one. It exists to show a Provider. Do not copy `auth.go`'s check; the shape around it (`BearerAuth` giving a `User` to every Input that asks) is what to keep.

## What it does

- `POST /rooms` creates a room (`{"name": "general"}`, 3 to 40 characters), `GET /rooms?after=&limit=` lists rooms a page at a time, with a `next` cursor.
- `POST /rooms/{id}/messages` posts a message (`{"text": "hello"}`, 1 to 1000 characters, not blank), `GET /rooms/{id}/messages?before=&limit=` reads the history newest first.
- `GET /rooms/{id}/ws` upgrades to a WebSocket. The client receives `{"kind":"message","message":{...}}` for each message of the room as it is committed, and sends `{"text":"..."}` to post one. A message posted by a socket is inserted, published to the room's Topic and comes back to every socket in the room on every instance, the sender's included, through the Postgres broker: there is one path from a message to a socket. A `{"kind":"resync"}` frame says notifications may have been missed (the `LISTEN` connection dropped and came back): read the history again.
- `/openapi.json`, `/metrics` (Prometheus), `/livez` and `/readyz`.

Creating, posting and the socket need `Authorization: Bearer toy-<name>`. Listing and history do not.

## Run it with Docker

```sh
docker run -d --name chat-pg -e POSTGRES_PASSWORD=chat -p 127.0.0.1:5432:5432 postgres:18
cd examples/chat
go run .
```

The defaults match that container. In another terminal:

```sh
curl -s -H 'Authorization: Bearer toy-ada' -d '{"name":"general"}' localhost:8080/rooms
curl -s -H 'Authorization: Bearer toy-ada' -d '{"text":"hello"}' localhost:8080/rooms/1/messages
curl -s localhost:8080/rooms/1/messages
websocat -H 'Authorization: Bearer toy-grace' ws://localhost:8080/rooms/1/ws   # any WebSocket client that sets a header
```

Post from `curl` while `websocat` is connected and the message appears there. `SIGTERM` makes `/readyz` answer 503, serves for `CHAT_SHUTDOWN_DELAY` more, then drains, and the sockets get a close with status 1001. Remove the database with `docker rm -f chat-pg`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `CHAT_ADDR` | `:8080` | the address to listen on |
| `CHAT_DATABASE_URL` | `postgres://postgres:chat@127.0.0.1:5432/postgres` | the Postgres to use; the schema is applied at start, and safe to apply again |
| `CHAT_TRUSTED_PROXIES` | none | comma separated CIDRs of the load balancers; without them the client address is the socket's |
| `CHAT_PUBLIC_URL` | none | where clients reach the API (`https://example.com/chat`) when a proxy in front strips a prefix: the API document's `servers`, and the prefix of every `Location` |
| `CHAT_RATE_LIMIT` | `600` | requests a minute for each client address |
| `CHAT_SHUTDOWN_DELAY` | `2s` | how long to keep serving with `/readyz` failing after SIGTERM |
| `CHAT_SHUTDOWN_TIMEOUT` | `25s` | how long to wait for requests to finish |

## Tests

```sh
go test ./... && go test -race ./...
```

The unit tests drive the whole app over memory with `bitetest` and a fake `Store`, and need no database. `integration_test.go` runs only when `CHAT_TEST_DATABASE_URL` names a Postgres, and is skipped otherwise:

```sh
docker run -d --name chat-pg-test -e POSTGRES_PASSWORD=chat -p 127.0.0.1:55432:5432 postgres:18
CHAT_TEST_DATABASE_URL=postgres://postgres:chat@127.0.0.1:55432/postgres go test -race ./...
docker rm -f chat-pg-test
```

## Which file shows what

| File | Framework feature |
| --- | --- |
| `app.go` | Registration in one place: `WithTrustedProxies` from the environment, the document's `Servers` from `CHAT_PUBLIC_URL`, `RequestID`, `SecurityHeaders`, `RateLimiter`, the `AccessLog` and `Metrics` Observers with `ServeMetrics`, `WithTraceContext`, `WithHealth`, `WithShutdownDelay`, `OpenAPI`, `Summary` and `Tag`; `newApp` builds without serving, so a test builds the router ([guide: testing](../../docs/guide/testing.md)) |
| `auth.go` | a Provider from `BearerAuth` and `ErrInvalidCredentials` ([guide: security](../../docs/guide/security.md), [providers](../../docs/guide/providers.md)) |
| `chat.go` | typed Inputs with Path, Query and Body sections, `Opt`, `PositiveInt`, string types that carry their own `Length`, `Validate` with `Invalid`, status errors (`Conflict`, `NotFound`), `Created`, and the WebSocket route with an `Inbox` and a `CloseError` ([guide: requests](../../docs/guide/requests.md), [validation](../../docs/guide/validation.md), [websockets](../../docs/guide/websockets.md)) |
| `hub.go` | the rooms as one `Topics` over a Broker: a Topic per room, made when its first socket joins, dropped when empty, framed once for every socket, with `OnGap` telling the sockets of a room to resync ([guide: websockets](../../docs/guide/websockets.md#across-several-instances)) |
| `pgstore.go`, `store.go`, `schema.sql` | a struct that owns the database handle behind an interface, so the handlers never see pgx ([ADR 0014](../../docs/adr/0014-services-are-plain-go-values.md)); the `INSERT` and nothing else; the handler publishes once it is stored |
| `config.go` | configuration from environment variables with defaults, each bad one named |
| `main.go` | the `pgnotify` Broker (`Run`, `Close`, `OnState` into `SetReady`, sharing the store's pool), and graceful shutdown: the signal handling the framework leaves to the program ([guide: deploying](../../docs/guide/deploying.md#shutting-down-without-dropping-requests)) |

## Decisions and limits

- **One `LISTEN` connection for the process**, not a connection a socket, owned by [`modules/pgnotify`](../../docs/reference/pgnotify.md) ([ADR 0081](../../docs/adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)). Idle sockets cost no database connection, and the pool serves requests only.
- **A message is delivered by the broker only.** The HTTP route and the socket both insert and then publish to the room's Topic; neither sends to a socket. The publisher's own sockets hear it through the broker like every other instance's, so it is never delivered twice and every instance hears one order. A failed publish is logged, not the poster's failure: the message is stored.
- **A gap is told, not hidden.** Postgres keeps no notification for a listener that is down. When the connection returns, each room with sockets sends `{"kind":"resync"}` and `/readyz` was 503 meanwhile; the client reads the history route and keys on the message id. The module reconnects with a growing pause and logs each attempt.
- **A text is at most 1000 characters** so the notification (the topic name, a newline and the JSON frame) stays under Postgres's limit of 7999 bytes; a `Publish` over it would be an error, never a cut message.
- **Fan-out is across instances.** Several replicas each hold a `LISTEN`, and the integration test runs two instances over one database. The in-memory `bitetest.NewBroker` does the same in the unit tests.
- `/metrics` and `/openapi.json` are public here and go through the same rate limit. Put them on an internal listener or behind an auth check in a real deployment ([observability](../../docs/guide/observability.md#counting-requests-for-prometheus)).
- The WebSocket route authenticates by the `Authorization` header, which browsers cannot set on a WebSocket. A browser-facing chat would authenticate with a cookie or a query token ([websockets](../../docs/guide/websockets.md#providers-and-middleware-on-a-socket-route)).
