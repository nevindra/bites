# Routing

**How a route is matched, grouped and ordered: ServeMux's pattern syntax with stricter matching, groups with options, one fixed order of steps and a slow path for plain handlers.**

**Guide:** [routing](../guide/routing.md) · **Reference:** [app](../reference/app.md)

## Overview

Route patterns are written as `http.ServeMux` writes them (`/users/{id}`, `/files/{path...}`, `/{$}`), but matching is the framework's own, on method and path together, because the engine does not build an `*http.Request`. Routes are grouped by prefix, and a request goes through a fixed order of steps.

```text
request
  1. app middleware            sees every request: 404, 405, a CORS preflight
  2. route matching            404, 405 + Allow   (problem+json)
  3. group and route middleware, outermost group first
  4. Providers the Input needs
  5. Input decoding, then Validate
  6. the handler

app.Group("/admin", opts...)   prefix, middleware, Providers, limits, tag; nests; innermost option wins
```

A typed route always has a method. A plain `http.Handler` is mounted by `stdhttp.Mount` as one typed route a method, as a slow path outside the allocation budget.

## Rules

1. **Patterns follow `http.ServeMux`, and two that conflict panic at Registration naming both and where each was registered.** A path that exists for other methods answers 405 with `Allow`, a GET route also answers HEAD, and matching allocates nothing once the app has started. ServeMux itself was not used because it needs an `*http.Request`. [ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)

2. **Matching is on raw segments, there are no automatic redirects and no host patterns.** `/files/a%2Fb` matches `/files/{name}` with `a/b`; `/users` and `/users/` are two paths; a path containing `//`, `/./` or `/../` is a 400, so a load balancer that normalises differently cannot bypass routing. Code that turns a parameter into a file path goes through `os.Root`. [ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)
3. **A pattern ending in `/` is a rest wildcard and the root alone is `/{$}`.** `{name...}` matches an empty remainder, `{name}` never matches an empty segment, one app uses at most 12 distinct methods, and the router also has method-less patterns, which a more specific method pattern beats; `stdhttp` mounts register one route a method instead, so the root of an app that mounts something needs no special spelling. [ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)

4. **Routes are grouped by prefix, and a group's options apply to every route in it, innermost winning.** `Group(prefix, opts...)` takes the options a route takes, groups nest, an option that cannot work is refused when the group is made, and versioned APIs that differ in naming are separate apps. [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)
5. **The order a request goes through is fixed:** app middleware, route matching, group and route middleware, Providers, decoding and `Validate`, the handler. This is why CORS is app middleware, and why a Provider's value reaches the access log through log attributes rather than middleware. [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)

6. **A `Use` or `Provide` that would not reach an existing route or child group is refused at Registration.** A group is sealed by its first route or child group, and a late call panics naming its own line, the line that sealed the scope and "move it above"; app-level `Use` is exempt because it is read at request time, app-level `Provide` is not. The options spelling cannot be late. Routes, groups and middleware cannot be added once the app serves. [ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)
7. **A plain `http.Handler` is served by `stdhttp.Mount` as a slow path outside the allocation budget, from a package of its own.** Each such request builds an `*http.Request` and a `ResponseWriter` and runs the handler on a goroutine, so it can stream and flush; a mounted GET allocates 32 times against the typed request's 2. The other way, `stdhttp.Handler(app)` serves the whole app as an `http.Handler` by writing each request to it over a connection in memory (64 allocations a typed GET), for a `net/http` service moving route by route and for tests written with `httptest`. The core never imports `net/http` (a test checks it), and `stdhttp` is the only package that does. [ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0018](../adr/0018-the-router-follows-servemux-rules.md) | ServeMux's pattern syntax, matched by our own stricter router |
| [0019](../adr/0019-plain-http-handlers-are-a-slow-path.md) | a plain `http.Handler` is a slow path in its own package, and so is the app served as one |
| [0048](../adr/0048-groups-and-the-order-things-run-in.md) | groups with options, the fixed order of steps and sealed scopes |

## Open questions

- **A mounted route appears in the API document and cannot be left out**, because no route option does that; and `Hijack`, trailers, 1xx responses and per-request deadlines are not supported over the engine ([ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md), [reference](../reference/stdhttp.md)).
- **The rule refuses harmless code**, such as a late `Provide` of a type no earlier route asks for, and app-level `Provide` still depends on call order ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)).
- **Host patterns** are absent from the first versions ([ADR 0018](../adr/0018-the-router-follows-servemux-rules.md)).
- **Net/http's size.** Importing it adds a few megabytes to a stripped binary, which only an app that makes no HTTP calls saves ([ADR 0019](../adr/0019-plain-http-handlers-are-a-slow-path.md)).
