# A mistake in a handler's types is refused when the route is registered

Go keeps struct field names and types at run time, so registering a route reads its handler's input and output types once, checks them against the route pattern, and builds the decoder and encoder for that route. Anything that does not fit (a `{id}` with no field for it, a field the pattern does not have, a type that cannot be decoded from text or encoded to JSON) panics at registration, naming the route, the field and the file and line of the registration, in the way `http.ServeMux` already panics on two conflicting patterns. One test that builds the application's router turns every such mistake into a failing test.

**Why it matters:** every framework examined lets the mismatch through. Huma and Fuego run the handler with a zero; Axum answers 500 at request time, Actix 404, and Poem binds by position and documents a parameter name that does not exist ([`docs/research/std-huma/`](../research/std-huma/report.md), [`docs/research/rust/`](../research/rust/report.md)).

**Considered:** code generation with `go generate`, which would refuse the same mistakes at compile time but adds a build step and generated files to every project. Not now; the design must not close it off, so the checks live in one place a generator could later call.

**What this does not give:** nilo's guarantee. The check runs when the program starts, not when it compiles, so an application with no test that builds its router finds out on deploy.
