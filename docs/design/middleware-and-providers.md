# Middleware and Providers

**How code runs around a handler: middleware that sees a request view and returns a response value, Providers that fill Input fields and are checked at Registration, and the four first-party middleware.**

**Guide:** [middleware](../guide/middleware.md), [first-party middleware](../guide/first-party-middleware.md), [providers](../guide/providers.md) · **Reference:** [middleware](../reference/middleware.md), [providers](../reference/providers.md)

## Overview

Two mechanisms put code around a handler, and neither holds a writer. A Middleware wraps the rest of the chain: it sees a read-only Request view, calls `next.Call(ctx)` or does not, and returns a Response value. A Provider is a function registered for a type, which fills an Input field of that type from the request view (the authenticated user is the usual case), so a route that asks for a type nobody provides is refused at Registration.

```text
app.Use(...)                    before routing: 404, 405 and preflight pass through it
  route matched
group Use, route Use            outermost group first
UseEach (built per route)      app's, groups', route's; next to the handler
    Provide chain               each Provider once, after its dependencies
      decode Input, Validate
        handler                 the body is encoded here, in the innermost step
    <- Response (80 B value: status, content type, headers added, size, streaming)

first-party (root package):  CORS   RequestID   AccessLog   Timeout
```

The Response is returned by value and its body stays opaque, so a middleware can decorate headers and read the size, but gzip and ETag are engine features. Because every error becomes a Response at each `Next` boundary, an outer middleware such as CORS always sees the final status.

## Rules

1. **Middleware is `func(ctx, req Request, next Next) (Response, error)`, and short-circuits by returning without calling `next`.** `Next` is a small struct with one method and not a function, because a function value made for each layer is an allocation; it returns no response error because an error from inside has already become a problem+json Response at that boundary. A value passes inward by wrapping `ctx`. [ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)

2. **A Request, a Next and a Response that are kept after the request are harmless.** The strings of a Request are copies in the request's own slab, and what they also hold is a generation of the pooled chain state, so a kept `Next.Call` answers a 500 and logs instead of running another request's chain. The head is copied once into the slab and the Input reads out of that copy, so the allocations stay the same. [ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)
3. **A middleware that must know its route is built for it at Registration, not looked up at request time.** App middleware runs before the match, so the request view has no route; `UseEach` calls a function once for each route with its `RouteInfo` (method, pattern, operation ID, tags), and the middleware it returns closes over what it learned. A nil result is an exempt route and an error refuses it, so default-deny is a start-up panic. The API document and the metrics page are passed too, marked `Builtin`, so a guard cannot miss them; the health paths, answered before routing, are not. A late `UseEach` is refused like a late `Use`. [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)
4. **The body is encoded in the innermost step and is opaque to middleware.** A middleware reads and sets the status and headers, reads `Size()`, or returns a fresh Response; it never reads or rewrites body bytes, so it cannot cache, hash or compress, and the headers a handler set are invisible to it. [ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)

5. **An Input field that is not a Section is filled by a Provider, and a route whose Provider is missing is refused at Registration.** A Provider is `func(ctx, req Request, deps D) (T, error)` where `D` is a struct of the provided types it needs; the graph is checked at Registration, a cycle or a missing link is refused naming the chain, each Provider runs only when the route needs it and at most once a request, the authentication Providers before the others so a missing credential is the 401 whatever the field order, and a route runs at most 16. An `Opt[T]` field whose `T` has an auth Provider is the optional credential: the Provider runs only when its credential is sent. A `Header` or `Cookie` field of `D` is a section, decoded and checked before the Provider runs and stated in the API document, and so is a `Query` field on a WebSocket or event stream route, which a browser opens without a header; the path and the body are the Input's. The framework injects nothing else. [ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md), [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)
6. **`Provide[T]` for a type an enclosing scope already provides is refused, and `Override[T]` replaces it for its scope and below.** Silent nearest-scope-wins was rejected: a nested package could change the authentication of everything under its group with no message. An Override with nothing to replace is refused, and each route resolves one map, so one request never holds two `User` values. [ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)

7. **A Provider's values live on the stack of its own step, so Providers cost no allocation.** A Provider's error is the answer as a handler's is, a panic in one is the engine's 500, and what it found reaches the access log through `AddLogAttrs`, not through middleware reading it. [ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)
8. **Four middleware ship in the root package: `CORS`, `RequestID`, `AccessLog` and `Timeout`.** Each is made by a constructor that refuses a configuration that cannot work with a `RegistrationError`; recovery is the engine's and not middleware. CORS is app middleware matching full origins exactly or by a wildcard subdomain, and varies on `Origin`; `Paths` limits it to part of the app, and giving it to a group or a route, where a preflight never reaches it, is refused; `RequestID` keeps a client's id only when it is 1 to 64 safe bytes; `AccessLog` is an Observer that writes one `request` record when the request has really ended ([observability](observability.md)); `Timeout` cancels the request's own context, waits for the handler and answers 503. [ADR 0032](../adr/0032-four-first-party-middleware.md)

9. **What only watches a request is an Observer, not a middleware.** A middleware cannot see a stream's end, a panic, gzip or a rendered `Problem`; an Observer, given with `WithObserver`, is told at the request's start and at its real end, and only watches. Its page is [observability](observability.md). [ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)

10. **Everything else a production framework needs is scheduled, not left waiting to be asked for.** Rate limiting, CSRF, security headers, sessions, auth Providers and conditional requests are milestone 3, each built whole because a security middleware done halfway is worse than none; the security ones are built and have a page of their own, [security](security.md). [ADR 0032](../adr/0032-four-first-party-middleware.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md) | middleware takes a request view and returns a response value; `Next` is a struct |
| [0023](../adr/0023-provided-sections-are-checked-at-registration.md) | Providers fill Input fields, are checked at Registration, and `Override` is explicit |
| [0032](../adr/0032-four-first-party-middleware.md) | `CORS`, `RequestID`, `AccessLog` and `Timeout`, and the rest scheduled |

## Open questions

- **`AccessLog` and throughput.** `AccessLog` costs throughput past 10% because slog formats a line a request ([ADR 0032](../adr/0032-four-first-party-middleware.md)).
- **`Request.Header` is a scan**, quickest for one or two lookups; an index wins from four, and the choice waits for the count real middleware makes ([ADR 0022](../adr/0022-middleware-sees-a-request-view-and-returns-a-response-value.md)).
- **A faster access record** would bypass the app's logger and its handler, which is left open ([ADR 0032](../adr/0032-four-first-party-middleware.md)).
- **What registration cannot catch:** a `Timeout` on a route that returns a Stream, `CORS` placed before `RequestID`, and an origin list that does not match what the browser sends ([ADR 0032](../adr/0032-four-first-party-middleware.md)).
- **A generated request id is not a trace id**; the trace id is [ADR 0059](../adr/0059-the-core-carries-w3c-trace-context-and-records-no-span.md)'s ([ADR 0032](../adr/0032-four-first-party-middleware.md)).
- **Sixteen Providers a route** is a constant chosen without a workload that needs more, and the stack device depends on the runtime moving pointers between frames, which a future runtime could change ([ADR 0023](../adr/0023-provided-sections-are-checked-at-registration.md)).
- **A panic skips the CORS headers**, since the chain does not return; the access record, written by an Observer, is not skipped ([ADR 0032](../adr/0032-four-first-party-middleware.md)).
