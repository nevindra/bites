# The bites guide

The guide teaches bites by tasks: each page is a question a person asks while building an API, answered with an example that compiles and the exchange a client sees.

It is one of three layers ([ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)). A guide page answers "how do I do this?". The [reference](../reference/README.md) answers "what exactly is this name, and what does it take?", and the [design pages](../design/README.md) answer "why does it work this way?". Every page opens with a line that links the same topic in the other two layers, and every Go block marked to compile is compiled by `go test`, so an example here is an example that builds. The map of all three layers is [the documentation README](../README.md), and the words the project uses are in [the glossary](../../GLOSSARY.md).

Read the pages in order the first time; afterwards each stands alone. `go doc` shows the same doc comments as the reference; for a module under `modules/`, which has its own `go.mod`, run it inside that module's directory or from an application that requires it ([reference](../reference/README.md)).

For the pieces together in one program, [`examples/chat`](../../examples/chat/README.md) is the reference workload: typed routes over Postgres, a Provider, and a WebSocket per room fed by `LISTEN`/`NOTIFY`, with the observability, security and shutdown settings of this guide wired as an application would wire them. It is its own Go module with its own tests. [`examples/shop`](../../examples/shop/README.md) is two services that talk over HTTP: a catalog behind JWT and an orders service that asks it, with a located 400 from a handler, a WebSocket that only sends, a stream of events, an upload, and a test for each.

## Reading order

1. [Getting started](./getting-started.md): install the module, write a first app, see what a client gets for a good and a bad request, and see a Registration refusal.
2. [Handlers](./handlers.md): the shape of a handler, its Input and output, errors, services as plain Go values, and the context.
3. [Routing](./routing.md): patterns, methods, 404 and 405, groups, and the order a request goes through.
4. [Requests](./requests.md): the Path, Query, Header, Cookie and Body sections, optional and nullable fields, wire names and JSON bodies.
5. [Validation](./validation.md): the rules a type carries (`Length`, `Range`, `Format`, `Check`), the constraint types, the `Validate` method and how the 400 lists every mistake.
6. [Forms and uploads](./forms-and-uploads.md): `Form[T]` and the `Upload` read part by part, with the limits an upload requires.
7. [Errors](./errors.md): problem+json for every error, status errors, unknown errors, panics and the request ID.
8. [Responses](./responses.md): status wrappers, headers, cookies, text and bytes, and handlers that return an interface.
9. [Files](./files.md): sending a file from an `os.Root` or an `fs.FS`, and what it answers for a missing one.
10. [Streaming and SSE](./streaming.md): responses written over time, and Server-Sent Events.
11. [Middleware](./middleware.md): writing a middleware, where it runs and what it can see.
12. [CORS, request IDs, access logs and timeouts](./first-party-middleware.md): the three middleware the framework ships, and its access log.
13. [Providers](./providers.md): filling Input fields from the request, such as the signed-in user, and replacing them in a test.
14. [Security](./security.md): security headers, authenticating with Bearer tokens, Basic auth and API keys, rate limiting, sessions and CSRF.
15. [WebSockets](./websockets.md): typed WebSocket routes, sending, topics and closing.
16. [The OpenAPI document](./openapi.md): serving the API description the types already carry, and enriching it.
17. [Limits and timeouts](./limits.md): the limits that are on by default, and how to change them for the app, a group or a route.
18. [Deploying](./deploying.md): listening, TLS, graceful shutdown, health and readiness, gzip and running behind a proxy.
19. [Observability](./observability.md): Observers, the access log, Prometheus metrics and following a request across services.
20. [Testing](./testing.md): testing an app over memory with `bitetest`, with a clock you move yourself and a WebSocket client.
21. [Mounting an http.Handler](./mounting-net-http.md): serving pprof, a file server or any `net/http` handler on a route, as a slow path.
22. [The API documentation UI](./api-docs-ui.md): Swagger UI or Scalar for your OpenAPI document, from the `docsui` module.
23. [Writing descriptions as doc comments](./doc-comments.md): generating the `Descriptions` methods from the comments of your fields.
24. [Building the documentation site](./documentation-site.md): rendering these pages to static HTML.
