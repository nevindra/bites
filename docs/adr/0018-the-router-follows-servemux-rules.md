# The router uses ServeMux's pattern syntax, with its own stricter matching

Route patterns are written as `http.ServeMux` writes them (`/users/{id}`, `/files/{path...}`, `/{$}`), so a Go programmer already knows them. Two patterns that conflict panic at Registration naming both and where each was registered, as ServeMux does. A path that exists for other methods answers 405 with an `Allow` header, and a GET route also answers HEAD.

Where it departs from ServeMux, deliberately (ServeMux's behaviour probed on Go 1.27.2, [`docs/research/facts/`](../research/facts/report.md)):

- **A typed route always has a method.** ServeMux lets a method-less pattern catch every other method, so that path never answers 405; here a method-less pattern exists only for mounting a Slow path ([ADR 0019](0019-plain-http-handlers-are-a-slow-path.md)).
- **Matching is on raw segments, decoding after.** `/files/a%2Fb` matches `/files/{name}` with `name` = `a/b`, and an encoded slash never splits a segment. A decoded `..` is just text in a parameter; code that turns a parameter into a file path goes through `os.Root`, which refuses to leave its directory.
- **No automatic redirects.** ServeMux redirects `/static` to `/static/` and cleans `//` with 307s; here `/users` and `/users/` are two paths, and a path with `//`, `/./` or `/../` is refused with 400. A load balancer that normalises paths differently from us is how routing gets bypassed, so the path is never second-guessed.
- **No host patterns** in the first versions; ServeMux's are case-sensitive, which is a surprise in itself.

Matching is ours, on method and path together, and allocates nothing once the app has started (the `Allow` value for every set of methods is built then).

**Edges, as built:** a pattern ending in `/` is an anonymous rest wildcard, as in ServeMux, so `GET /` catches every GET path no other pattern takes; write `/{$}` for the root alone. `{name...}` matches an empty remainder (`/files/` matches `/files/{p...}`), while `/files` without the slash does not. `{name}` never matches an empty segment. Patterns are matched as raw text and are not unescaped, unlike ServeMux, so a pattern never needs `%2F`. A method-less mount conflicts with any method pattern that is more specific on the path but less on the method, as in ServeMux: a pprof mount under `/debug/` beside `GET /` is refused, and the root route is written `GET /{$}`. One app may use at most 12 distinct methods.

**Why not ServeMux itself:** it needs an `*http.Request`, which our engine does not build ([ADR 0005](0005-our-own-http1-engine-with-a-goroutine-per-connection.md)). **Why not chi's syntax:** chi now fills `r.PathValue` itself, a sign the standard syntax has become the common one ([`docs/research/std-huma/`](../research/std-huma/report.md)).
