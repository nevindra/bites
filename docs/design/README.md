# Design

**Why bites works the way it does: one page a topic, each joining the ADRs that decided it.**

A design page is where a reader starts and an ADR is where the evidence is. Every page has the same four sections: Overview, Rules, Decisions and Open questions. Back to [the map](../README.md).

| Page | What it covers | ADRs |
|---|---|---|
| [Principles](principles.md) | the four principles and their order, independence from nilo, the reference workload, Go 1.27, the standard library only, no global state, versioning | 0077, 0001, 0002, 0004, 0006, 0026, 0034, 0070, 0080 |
| [The trade budget](trade-budget.md) | the four axes, allocation and idle-memory budgets, nothing pooled reaching the user, rules as tests | 0003, 0008, 0033 |
| [The engine](engine.md) | the HTTP/1.1 engine, parking, cancellation on disconnect, TLS, panics, shutdown and the shutdown delay, parser strictness, trusted proxies | 0005, 0016, 0017, 0025, 0028, 0029, 0045, 0052 |
| [Typed handlers](typed-handlers.md) | the handler signature, the Input and its Sections, refusal at Registration, services as plain values | 0007, 0009, 0010, 0014 |
| [Request input](request-input.md) | wire names, the JSON codec, presence, text decoding, body types, validation | 0011, 0015, 0021, 0041, 0042, 0044, 0076 |
| [Responses](responses.md) | status wrappers, streaming and SSE, gzip, cookies, files | 0013, 0030, 0031, 0043 |
| [Errors](errors.md) | problem+json, 400 for input mistakes, status errors, timeouts | 0012, 0049 |
| [Routing](routing.md) | patterns, groups, the order of steps, mounting a plain `http.Handler` | 0018, 0019, 0048 |
| [Middleware and Providers](middleware-and-providers.md) | the request view and response value, Providers, the four first-party middleware | 0022, 0023, 0032 |
| [Observability](observability.md) | the Observer, the access log, metrics, W3C trace context, health and readiness | 0051, 0059, 0060, 0032, 0052 |
| [Security](security.md) | trusted proxies, security headers, auth Providers, rate limiting, sessions, CSRF | 0053, 0054, 0055, 0056, 0057 |
| [Limits](limits.md) | every limit on by default, with its own status | 0024 |
| [WebSockets](websocket.md) | message handlers, typed routes, send queues, topics, heartbeats, origins, inbound limits | 0035, 0036, 0037, 0038, 0039, 0040, 0046, 0047 |
| [OpenAPI](openapi.md) | the API document, built at Registration and served as fixed bytes, its prose generated from comments, its documentation UI | 0020, 0071, 0072, 0076 |
| [Testing](testing.md) | in-memory connections, a manual clock and a WebSocket client, a snapshot of the API document | 0027, 0074, 0075 |
| [Documentation tooling](docs-tooling.md) | the three layers, the tests that hold them, the doc-comment generator and the static site | 0050, 0072, 0073 |

Every ADR is on one of these pages, and `go test` fails when one is not.
