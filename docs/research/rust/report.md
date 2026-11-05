# Rust typed-handler frameworks, state at 2026-10-30

Tags: [code] read at a tag, [docs], [issue], [probe] I ran it (probe crates under probe/, released crates.io versions, outputs below), [release-notes].

## Axum (tokio-rs/axum)
- Latest: axum 0.8.9, 2026-04-14 (gh release list). Still 0.x. main last commit 2026-10-30. [release-notes] 0.8.9 was small: websocket subprotocol selection, MSRV 1.80, two fixes (axum/CHANGELOG.md:9-20 at axum-v0.8.9).
- Last major (0.8.0, 2025-01-01): path syntax `/:id` and `/*rest` became `/{id}` and `/{*rest}`, old syntax panics (CHANGELOG #2645); handlers and services must be `Sync` (#2473); `Option<Path<T>>` no longer swallows all errors (#2475); tuple Path deserializers require an exact param count (#2931); Query/Form report the failing field via serde_path_to_error (#3081). [release-notes] The async_trait removal is also in the 0.8 cycle (axum-core 0.5.0 section): extractor traits now use return-position impl Trait in traits (axum-core/CHANGELOG.md:52,73, #2308). [release-notes]
- Handler shape: any async fn up to 16 args, all but the last implement FromRequestParts, the last FromRequest, returns IntoResponse, future is Send (axum/src/docs/debugging_handler_type_errors.md:5-14). [code/docs] So only the last argument can consume the body; order matters.
- PROBE, non-extractor argument `async fn bad(_x: NotExtractor)` passed to `get(bad)`, axum 0.8.9 plain:
  ```
  error[E0277]: the trait bound `fn(NotExtractor) -> impl Future<Output = ()> {bad}: Handler<_, _>` is not satisfied
    ... the trait `Handler<_, _>` is not implemented for fn item ...
    = note: Consider using `#[axum::debug_handler]` to improve the error message
  note: required by a bound in `axum::routing::get`  (+ a macro-expansion block)
  ```
  It never names the argument or says why. With `#[axum::debug_handler]` (needs the `macros` feature) [probe]: `error[E0277]: the trait bound NotExtractor: FromRequest<(), ViaParts> is not satisfied ... src/main.rs:4:18 ... "Function argument is not a valid axum extractor. See https://docs.rs/axum/0.8/axum/extract/index.html" ... the following other types implement FromRequestParts<S>: ConnectInfo<T>, Extension<T>, HeaderMap, ... and 6 others`. It points at the argument, but the original unreadable Handler<_,_> error is still emitted after it, and you must know to add the attribute. The docs themselves admit "Rust gives poor error messages" and "doesn't tell you why" (same docs file, lines 15-35). [docs]
- PROBE rejections (curl, all `content-type: text/plain; charset=utf-8`, never JSON):
  - Path<u32> "abc": 400 `Invalid URL: Cannot parse `abc` to a `u32`` (names the value, not the param).
  - Query missing: 400 `Failed to deserialize query string: missing field `page``; bad value: `page: invalid digit found in string`.
  - Json malformed: 400 `Failed to parse the request body as JSON: name: EOF while parsing a value at line 1 column 8`.
  - Json missing field: **422** `Failed to deserialize the JSON body into the target type: missing field `age` at line 1 column 12`; wrong type: 422 `age: invalid type: string "x", expected u32 at line 1 column 21`.
  - No content-type: 415. The field IS named for JSON and Query, plain text, status split 400/422.
- PROBE route `/m/{id}` with `Path<(u32,u32)>`: compiles; at request time 500 `Wrong number of path arguments for `Path`. Expected 2 but got 1`. So runtime, and a server error not a client error. [probe] Because axum matches by position, not name, nothing ties the route string to the extractor type.
- Customizing rejections is the top open ergonomics issue: #1116 "Improve the custom rejection experience" (28 reactions, open since 2022). [issue]
- OpenAPI: none first-party. #50 "Support generating OpenAPI/Swagger docs" has 500 reactions, closed 2022-06-25; the closing comment points to a utoipa example (gh api issues/50). [issue]
  - utoipa 6.0.0 (gen 6.0.1, 2026-09): annotation-based. README minimal example (utoipa/README.md:143-182): `#[derive(ToSchema)] struct Pet`, `#[utoipa::path(get, path = "/pets/{id}", responses((status = 200, body = Pet), (status = NOT_FOUND, ...)), params(("id" = u64, Path, ...)))]` on the fn, then `#[derive(OpenApi)] #[openapi(paths(pet_api::get_pet_by_id))]`. Note the README's own handler parameter is `pet_id` while the doc says `id`. [docs]
  - aide 0.15.1 (latest stable; 0.16 alpha exists; last commit 2026-09-21): derives from handler types via `ApiRouter::api_route` plus schemars JsonSchema. [docs/code]
  - Drift, PROBE (probe/oapi): utoipa: I annotated `path="/pets/{id}"`, `params(("id"=u64, Path))`, but wrote `Path<String>` and registered the handler at `/animals/{id}`; it compiled, and the emitted spec says integer at /pets/{id}. Nothing links annotation to code, so drift is possible by construction. [probe] aide: derived from the handler's types, so the schema cannot disagree with the extractor type, BUT: (a) a bare `Path<u64>` produced NO parameter at all in the spec (silent); (b) `Path<PP{id}>` registered at `/r/{wrong}` documented a path param named `id` under `/r/{wrong}` (silent, on_error callback printed nothing); (c) aide 0.15.1 needs schemars 0.9: with schemars 0.8 or 1 my handler failed with `OperationHandler<_, _> is not satisfied`, a version-skew error that says nothing about schemars. [probe] Types-derived beats annotation-derived on drift, but aide does not check the route string against the type either.
  - utoipa health: issue #1498 "Still maintained?" (23 reactions, 2025-11-17) after a quiet stretch; last commit 2026-10-20 so it recovered. [issue]
- IntoResponse: `fn into_response(self) -> Response` (axum-core/src/response/into_response.rs:112-116), Response is `http::Response<Body>` (response/mod.rs:21). A handler returns one complete value; headers and status exist before the body is sent, so a header cannot be set after the body is written; tuples `(StatusCode, HeaderMap, body)` assemble the value. [code] The 'late header' case is by construction impossible; streaming bodies carry trailers only through the body type.
- Compile cost: axum's own tracking issue #200 "Having compile time issues? Post here!" lists chained `.route(...)` plus `.boxed()` as slow, a user reports 3 s becoming ~1 minute with boxed (tokio-rs/axum#200 comments). [issue, old, 2021] Also #145, #392. I did not measure compile times myself.

## Actix Web
- Latest: actix-web 4.18.1, 2026-10-23, four minors in 12 days (4.16 on 10-12, 4.17 10-15, 4.18 10-23); still 4.x since 4.0.0. 4.18.0 raised MSRV to 1.89 and added a `#[query]` macro (CHANGES.md). Very active. [release-notes]
- Handler: `web::get().to(f)` with extractors implementing FromRequest as a tuple (tuple impls up to 16, src/extract.rs:426); handler futures have no Send bound (grep Send src/handler.rs = 0 matches). Routes can also be written with attribute macros `#[get("/x/{id}")]`. [code]
- PROBE bad arg: `error[E0277]: NotExtractor: FromRequest is not satisfied ... help: the trait FromRequest is not implemented for NotExtractor ... required for (NotExtractor,) to implement FromRequest ... required by a bound in Route::to`, plus a second error `(): Responder is not satisfied` for the empty return. Clearer than axum's un-annotated error because it names FromRequest and the argument, but it exposes the tuple encoding and lists "and 28 others". [probe] The Handler doc also warns: "If you get the error `the trait Handler<_> is not implemented`..." (src/handler.rs:27).
- PROBE rejections, all text/plain: Path<u32> "abc": **404** `can not parse "abc" to a u32`; Path<(u32,u32)> on /m/{id}: 404 `wrong number of parameters: 1 expected 2` (runtime, and 404 not 500). Query: 400 `Query deserialize error: missing field `page``. Json: 400 `Json deserialize error: missing field `age` at line 1 column 12`; wrong type 400. [probe]
- Runtime model: `HttpServer::new(factory)` where factory is `Fn() -> App + Send + Clone + 'static`; the factory is instantiated once per worker thread (and per bind address), default workers = available_parallelism, one blocking pool per worker (src/server.rs:45-150, 219-225, 467-474; app.rs:102). So application state is per worker unless you share an Arc, and handlers need not be Send. [code]
- Issues by reaction (open): #309 QUIC/HTTP3 (127), #4199 update h2 (16), #1604 consistent JSON output for all endpoints (15), #414 middleware on route groups without prefix (13), #2702 `get` macro lacks HEAD support (11). OpenAPI: #310 "Generate API documentation" (204, closed). [issue]

## Poem + poem-openapi
- Releases: crates.io shows poem 3.1.12 and poem-openapi 5.1.16, last published 2025-07-28 (crates.io API). GitHub's release list is stale (latest "Release" is v2.0.0 from 2022-05-31, ignore it). main last commit 2026-10-28 and carries unreleased poem 4.0.0 / poem-openapi 6.0.0 (Cargo.toml), whose changelog lists breaking changes (exact integer bounds, union schema names, GeoJSON 1.0, `explode` defaults, Rust 1.94). So the latest release is about 15 months old while main is active. [code/release-notes]
- Declaration: `#[OpenApi] impl Api { #[oai(path = "/u/:id", method = "get")] async fn one(&self, id: Path<u32>) -> PlainText<String> }`, then `OpenApiService::new(Api, "t", "1.0")`; `svc.spec()` gives the document. Payload and response types derive `Object`/`ApiResponse`. This is annotation on the route, but the types (Path<u32>, Json<T>, Object) feed the schema, so only the route string is annotation. [probe/code]
- PROBE bad arg `x: NotExtractor`: compiles with two E0277 errors, `NotExtractor: ApiExtractor<'_> is not satisfied` plus `FromRequest<'_> not implemented`, the second pointing at the `#[OpenApi]` attribute. Names the trait, readable enough, lists only 5 impls. [probe]
- PROBE mismatch: `#[oai(path="/m/:nope")]` with parameter `id: Path<u32>` compiles with no warning; runtime: request /m/5 returns 200 `5` (path params are bound by position), and the spec says path `/m/{nope}` with a path parameter named `id`. Silent drift in the generated document. [probe]
- PROBE rejections (text/plain, 400 for everything): Path "abc": `failed to parse path `param0`: failed to parse "integer(uint32)": invalid digit found in string` (the field is called `param0`, not the name); Query: `failed to parse parameter `page`: Type "integer(uint32)" expects an input value.`; Json missing field: `parse request payload error: Expected input type "integer(uint32)", found null. (occurred while parsing "T")` (names the type, not the field `age`). [probe]
- Open issues by reaction: #674 "Poem is awesome!" (12), #399 merging routes/endpoints (9), #903 AsyncAPI (7), #740 scalar docs (6), #914 OpenAPI 3.1 support (5). Others: #982 "Missing field `style` compilation error" (closed), #669 validator on NewType compiles but does nothing, #995 "implementation of `Send` is not general enough". [issue]

## Compile-time and error-quality evidence
Only found: axum#200/#145/#392 (boxed routers, 2021-22) and the axum docs admitting poor Handler errors. No measured numbers of my own; no recent benchmark found. Axum issue #1142 title "Missing extensions are runtime errors rather than compile errors" (closed) shows the same class of complaint (not read in depth). [issue]

## What Rust gets right that a Go port cannot copy
- Handler arguments are checked by a trait solver at compile time (the whole argument list becomes `Handler<T,S>`); Go has no per-signature trait dispatch, so a Go port can only reflect at startup (or generate code), and the failure moves from the compile to program start.
- Extractor ordering (only the last consumes the body) is enforced by two traits FromRequestParts/FromRequest. Go can only check it by reflection or convention.
- Serde-derived field-level errors with a path (`age: invalid type ...`) come from derive macros on the user's type. Go struct tags + reflection can reproduce this, at runtime cost.
- OpenAPI from types without annotations (aide) works because schemars derives a schema from the type; Go would need reflection or codegen and loses generics-driven schemas.
- Response is a single value: status, headers, body are fixed before send; Go's http.ResponseWriter allows WriteHeader after Write by design, which a Go port would have to wrap.

## Mistakes worth not repeating
1. Unreadable `Handler<_,_>` errors: axum needed a separate opt-in macro to say why; the unrelated bound error still comes out too. A refusal should name the argument and the reason, always.
2. Route string and extractor type not tied: axum fails with a 500 at request time for a Path arity mismatch, actix 404, poem silently binds by position and documents a name that does not exist. Check them against each other at startup or build.
3. Annotation-based OpenAPI (utoipa) drifts by construction; I produced an integer-typed doc for a String extractor on a different path. Types-derived aide still silently drops bare `Path<u64>` and misdocuments a renamed segment: deriving from types is not enough, the route string must be checked too.
4. Status/format inconsistency: 400 vs 422 vs 404 for the same class of client error across the three; all plain text, so clients cannot parse a field name. Customizing rejections is the top-reacted axum ergonomics issue (#1116).
5. Poem names path errors `param0`, and its JSON error names the type, not the field.
6. Version skew in the doc layer (aide needs schemars 0.9) turns into a trait error.
7. Release gaps: poem's last release is 15 months behind main; utoipa drew a "still maintained?" issue.

## Not verified
- No compile-time numbers measured by me; no head-to-head build timing.
- Poem 4.0 / poem-openapi 6 migration guide not read in full; main branch not built.
- Actix `!Send` consequence read in source/docs only, not probed under load; actix OpenAPI options (utoipa-actix-web, paperclip) not examined.
- Axum #1142 and #3229 read by title only. Reaction counts are from the GitHub search API on 2026-10-30.
