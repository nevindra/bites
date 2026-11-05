# bites

A Go HTTP framework where your types are the contract.

Write a plain Go function. Its input struct becomes routing, decoding, validation, a 400 that names the field a client got wrong, and the OpenAPI 3.1 document. A mistake in those types is refused when the route is registered, before the first request.

> [!WARNING]
> bites is pre-1.0 and not tagged yet. The API may change before 1.0, and every breaking change says what to change ([ADR 0034](docs/adr/0034-versioning-before-1-0.md)).

## Features

- **Typed handlers**: `func(ctx, Input) (Output, error)`, with `Path`, `Query`, `Header`, `Cookie` and `Body` sections and no struct tags
- **Refused at start-up**: a route whose types cannot work panics at Registration, naming the field and the line
- **problem+json errors** that list every field a client got wrong, by its wire name
- **OpenAPI 3.1**, generated from the same types
- **Middleware, groups and Providers** for what a route needs from the request, checked at Registration
- **WebSockets, streaming and server-sent events**
- **Production pieces**: CORS, sessions, CSRF, rate limiting, security headers, auth Providers, access log, Prometheus metrics, trace context, health checks, TLS and mTLS, graceful shutdown
- **Testing without a port**: `bitetest` runs the whole app in memory
- **Standard library only** in the core, with its own HTTP/1.1 engine

## Install

```sh
go get github.com/nevindra/bites
```

Requires Go 1.27 or later.

## Quick start

<!-- compiles -->
```go
package main

type GetUser struct {
	Path struct{ ID int64 }
}

type User struct {
	ID   int64
	Name string
}

func getUser(ctx context.Context, in GetUser) (User, error) {
	if in.Path.ID != 1 {
		return User{}, bites.NotFound("no such user")
	}
	return User{ID: 1, Name: "Ada"}, nil
}

func main() {
	app := bites.New()
	app.Get("/users/{id}", getUser)
	log.Fatal(app.Listen(":8080"))
}
```

A bad request gets a 400 that names the field:

```http
GET /users/abc

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer"}]}
```

A route that cannot work never starts:

```text
panic: GET /users/{id}: the pattern has {id}, but GetUser.Path has no field for it
	registered at main.go:21
```

More in [Getting started](docs/guide/getting-started.md) and [`examples/`](examples/).

## Principles

Four, in the order they win when they pull apart ([ADR 0077](docs/adr/0077-four-principles-in-the-order-they-win.md)):

1. **AI agent first, human second.** One way to do each thing, a signature that is the whole contract, a doc comment on every exported name, and refusals that say what to change. What an agent gets right on the first try, a person does too.
2. **Safe to use and to debug.** Mistakes are refused at Registration with the route, the field and the line; a client error names the field; limits are on by default; nothing happens in silence, and a failed request says why in the log and the span.
3. **Covers a lot.** Breadth is a goal: what Go frameworks commonly ship goes into the core or into a module under `modules/`, so an application is built fast without a second framework beside it.
4. **Fast and low memory, without chasing the last percent.** Allocations per request and memory per idle connection are held by tests and never rise unstated ([ADR 0003](docs/adr/0003-the-trade-budget.md)); a workload that needs the limit is nilo's, in Zig.

## Benchmarks

`GET /users/{id}` and a typed JSON `POST`, on a Ryzen 7 9700X with server and client on separate cores, mean of three interleaved rounds:

| | bites | net/http | Huma |
|---|---|---|---|
| GET req/s | 894k | 527k | 454k |
| GET p99 | 737 µs | 1,464 µs | 1,702 µs |
| allocations per POST | 2 | 35 | 46 |
| memory per idle connection | 894 B | 18.2 KB | 18.2 KB |

Idle memory is heap plus stack per connection. Full runs, spreads and method: [`bench/result/milestone1.md`](bench/result/milestone1.md) and [`docs/research/parking/`](docs/research/parking/report.md).

## Modules

| package | what it is |
|---|---|
| [`bitetest`](bitetest/) | in-memory server and WebSocket client for tests |
| [`stdhttp`](stdhttp/) | mount a `net/http` handler inside an app, or serve the app as one |
| [`modules/jwt`](modules/jwt/) | JWT verification, with JWKS |
| [`modules/otel`](modules/otel/) | OpenTelemetry server spans |
| [`modules/zstd`](modules/zstd/), [`modules/brotli`](modules/brotli/) | extra response encoders |
| [`modules/docsui`](modules/docsui/) | an API documentation page |

Each module has its own `go.mod`, so the core stays dependency-free.

## Documentation

- [Guide](docs/guide/README.md): how to do each task
- [Reference](docs/reference/README.md): every exported name
- [Design](docs/design/README.md) and [ADRs](docs/adr/): why it works this way
- [Roadmap](docs/roadmap.md): what comes next
- [Glossary](GLOSSARY.md): the words the project uses

> [!NOTE]
> bites brings the idea of [nilo](https://github.com/nevindra/nilo) (Zig) to Go, and argues each decision again with its own evidence ([ADR 0001](docs/adr/0001-independent-of-nilo.md)). If it ever stops, an ADR says why and this note says so first.

## License

[MIT](LICENSE)
