# bites documentation

**Start here: every topic of bites, with its guide page, its reference page and its design page side by side.**

**Guide:** [the guide](./guide/README.md) · **Reference:** [the reference](./reference/README.md) · **Design:** [design, one page a topic](./design/README.md)

## How the documentation is organised

Each topic is written up to three times, for three different questions ([ADR 0050](./adr/0050-the-documentation-is-three-layers-held-by-tests.md)):

| Layer | Answers | One page per | Where |
|---|---|---|---|
| **Guide** | How do I do this? Worked examples, in reading order. | task | [`docs/guide/`](./guide/README.md) |
| **Reference** | What exactly is this name, and what does it take? | area of the API | [`docs/reference/`](./reference/README.md) |
| **Design** | Why does it work this way, and what was rejected? | topic | [`docs/design/`](./design/README.md) |

Behind the design pages are the ADRs in [`docs/adr/`](./adr/), one decision each with the alternative it rejected and its evidence. A design page links every ADR of its topic, so read the design page first and an ADR when you need the evidence. The measurements behind the ADRs are in [`docs/research/`](./research/README.md), what is not built yet is in [the roadmap](./roadmap.md), and the words the project uses, and the ones it avoids, are in [`GLOSSARY.md`](../GLOSSARY.md).

## How to read a page

Every page in the three folders starts the same way:

- **Line 1** is the title.
- **Line 3** is one bold sentence saying what the page is about.
- **Line 5** links the same topic in the other two layers.

A reference heading is the exported name itself (`` `App.Get` ``, `` `Opt` ``, `` `bitetest.Start` ``), so its anchor is the name in lower case without the dot (`reference/app.md#appget`), and [every heading of the reference](./reference/README.md#every-heading) is listed on one page. A design page has the same four sections: Overview, Rules, Decisions and Open questions.

**For an agent**, the fastest way in is the outline, then a search:

```sh
head -5 docs/guide/*.md                 # what each page is, and where its other layers are
grep -n '^#' docs/reference/app.md      # a page's outline, with line numbers
grep -rn '`Returns`' docs/              # every place a name is written
```

Prose is one paragraph a line, so a phrase is never split across two lines, and `go test` keeps all of the above true (`docs_test.go`): the five-line head, links and anchors, this map, every ADR on a design page, every exported name in the reference, and every Go block marked `<!-- compiles -->` compiled.

## Topics

In the order the guide teaches them. Each row is one guide page and the reference and design pages that go with it.

### Start here

| Topic | Reference | Design |
|---|---|---|
| [Getting started](./guide/getting-started.md) | [`New`, `App`, options](./reference/app.md) | [Principles](./design/principles.md) |
| [Handlers](./guide/handlers.md) | [`App`](./reference/app.md) | [Typed handlers](./design/typed-handlers.md) |
| [Routing](./guide/routing.md) | [`App`, `Group`](./reference/app.md) | [Routing](./design/routing.md) |

### Reading a request

| Topic | Reference | Design |
|---|---|---|
| [Requests](./guide/requests.md) | [Input types](./reference/input.md) | [Request input](./design/request-input.md) |
| [Validation](./guide/validation.md) | [Constraint types](./reference/input.md) | [Request input](./design/request-input.md) |
| [Forms and uploads](./guide/forms-and-uploads.md) | [`Form`, `Upload`, `Part`](./reference/input.md) | [Request input](./design/request-input.md) |
| [Errors](./guide/errors.md) | [Errors](./reference/errors.md) | [Errors](./design/errors.md) |

### Answering

| Topic | Reference | Design |
|---|---|---|
| [Responses](./guide/responses.md) | [Responses](./reference/responses.md), [`zstd`](./reference/zstd.md), [`brotli`](./reference/brotli.md) | [Responses](./design/responses.md) |
| [Files](./guide/files.md) | [`File`, `Static`](./reference/responses.md) | [Responses](./design/responses.md) |
| [Streaming and SSE](./guide/streaming.md) | [`Stream`, `SSE`, `Events`](./reference/responses.md) | [Responses](./design/responses.md) |

### Around the handler

| Topic | Reference | Design |
|---|---|---|
| [Middleware](./guide/middleware.md) | [Middleware](./reference/middleware.md) | [Middleware and Providers](./design/middleware-and-providers.md) |
| [CORS, request IDs, access logs and timeouts](./guide/first-party-middleware.md) | [First-party middleware](./reference/middleware.md), [`AccessLog`](./reference/observability.md#accesslog) | [Middleware and Providers](./design/middleware-and-providers.md), [Observability](./design/observability.md) |
| [Providers](./guide/providers.md) | [Providers](./reference/providers.md) | [Middleware and Providers](./design/middleware-and-providers.md) |
| [Security](./guide/security.md) | [Security](./reference/security.md), [`jwt`](./reference/jwt.md), [`pgsession`](./reference/pgsession.md), [`pgratelimit`](./reference/pgratelimit.md) | [Security](./design/security.md) |

### Real time

| Topic | Reference | Design |
|---|---|---|
| [WebSockets](./guide/websockets.md) | [WebSockets](./reference/websocket.md), [`pgnotify`](./reference/pgnotify.md) | [WebSockets](./design/websocket.md) |

### Describing the API

| Topic | Reference | Design |
|---|---|---|
| [The OpenAPI document](./guide/openapi.md) | [OpenAPI](./reference/openapi.md) | [OpenAPI](./design/openapi.md) |

### Running it

| Topic | Reference | Design |
|---|---|---|
| [Limits and timeouts](./guide/limits.md) | [`Limits`, route options](./reference/app.md) | [Limits](./design/limits.md) |
| [Deploying](./guide/deploying.md) | [`App.Listen`, `App.Shutdown`, options](./reference/app.md) | [The engine](./design/engine.md) |
| [Configuration](./guide/configuration.md) | [`config`](./reference/config.md) | [Principles](./design/principles.md) |
| [Observability](./guide/observability.md) | [Observers, metrics, traces, health](./reference/observability.md), [`otel`](./reference/otel.md) | [Observability](./design/observability.md) |
| [Testing](./guide/testing.md) | [`bitetest`](./reference/bitetest.md) | [Testing](./design/testing.md) |
| [Mounting an http.Handler](./guide/mounting-net-http.md) | [`stdhttp`](./reference/stdhttp.md) | [Routing](./design/routing.md) |
| [The API documentation UI](./guide/api-docs-ui.md) | [`docsui`](./reference/docsui.md) | [OpenAPI](./design/openapi.md) |
| [Writing descriptions as doc comments](./guide/doc-comments.md) | [`bites-doc`](./reference/tools.md) | [OpenAPI](./design/openapi.md) |
| [Building the documentation site](./guide/documentation-site.md) | [`bites-site`](./reference/tools.md) | [Documentation tooling](./design/docs-tooling.md) |

### Design pages with no guide page of their own

| Design | What it covers |
|---|---|
| [Principles](./design/principles.md) | the four principles and their order, independence from nilo, the reference workload, Go 1.27, the standard library only, no global state, versioning |
| [The trade budget](./design/trade-budget.md) | the four axes, allocations per request and memory per idle connection, nothing pooled reaching the user, rules as tests |
| [The engine](./design/engine.md) | the HTTP/1.1 engine, a goroutine per connection, parking, cancellation, TLS, mTLS and certificate reload, several listeners and Unix sockets, panics, shutdown, HTTP/1 strictness, proxies |
| [Documentation tooling](./design/docs-tooling.md) | the three layers and the tests that hold them |
