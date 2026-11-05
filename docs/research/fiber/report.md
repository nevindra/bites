# Fiber v3 / fasthttp / Hertz: state on 2026-10-30

Tags: [code] read at the tag, [docs] doc text at the tag, [issue] GitHub issue, [probe] ran it, [release-notes] gh release/API. Clones: gofiber/fiber v3.6.1, valyala/fasthttp v1.77.0, cloudwego/hertz v0.10.6 under src/. Probes: probe/fiber1, probe/fiber2 (fiber v3.6.1, Go 1.27.2).

## 1. Release and activity
- Fiber latest v3.6.1, 2026-10-14; v3.6.0 10-12; v3.5.0 08-13; v3.4.0 07-02; v3.3.0 05-22; v3.2.0 04-25; v3.1.0 02-24; v3.0.0 GA 2026-02-02 [release-notes: gh release list]. v3 spent 2022 to 2026 in beta/rc (beta.2 2024-04, rc.1 2025-08, rc.3 2025-11). v2 still released: v2.52.16 on 10-23. Cadence since GA: a minor every 4-8 weeks.
- Last 90 days (since 07-31): 1222 commits on gofiber/fiber [GitHub API]. By login: ReneWerner87 518, "claude" 414 (an AI-agent account; commit subjects like "chore: trim the comments this branch adds"), dependabot 64, gaby 50, VedantMadane 20, RubenPari 14. So humans doing real work: about 2 (ReneWerner87, gaby) plus occasional contributors. The repo has an AGENTS.md instructing AI agents [code: AGENTS.md]. I did not verify who reviews the "claude" commits.
- Open: 27 issues+PRs per repo field, of which 19 issues and 8 PRs [API search].
- fasthttp: v1.77.0 on 10-26, v1.76.0 10-19, v1.75.0 10-05, v1.74.0 09-07 [release-notes]; about weekly-to-monthly. 69 open issues+PRs. Active: 耗子 25, Luis Figueroa 21, Erik Dubbelboer 16 commits in 90 days.
- Hertz: v0.10.6 2026-08-06, v0.10.5 06-11, v0.10.4 2026-01-26 [release-notes]. Only 8 commits in 90 days [API]. ROADMAP.md: "From 2025, instead of developing new features we will focus on optimizing core functionalities" [code: ROADMAP.md:3].

## 2. Foundation
- v3 still sits on fasthttp: go.mod `github.com/valyala/fasthttp v1.75.0` (latest is 1.77) [code: go.mod:15]. Go floor is 1.26.
- HTTP/2: fasthttp has none. Its README FAQ says "HTTP/2.0 support is in progress" via a separate repo [docs: fasthttp/README.md:655-657] and lists "net/http supports HTTP/2.0" as a net/http advantage (:665). Issue #144 "HTTP/2 support" is open since 2016-ish; in 2026-08 a contributor opened PR #2347 with a native h2 implementation (h2spec strict 146/147) [issue: valyala/fasthttp#144 comment 2026-08-06]. Not merged as far as I saw (v1.77.0 tree has no h2 files). Fiber docs say to terminate h2/h3 at nginx/Traefik [docs: docs/guide/reverse-proxy.md:196-229].
- Hertz: core is HTTP/1.1 over netpoll; h2 is an add-on `hertz-contrib/http2` [docs: README.md:64]; ALPN constants for h2/h3 exist in pkg/protocol/suite/server.go:34-38. h3 not verified beyond constants.
- net/http bridge: v3 accepts net/http handlers directly in `app.Get(path, handler any, ...)`. `toFiberHandler` type-switches 17 shapes, including http.Handler, http.HandlerFunc, func(w,r), fasthttp handlers, and Express-style func(Req,Res,next) [code: adapter.go:13-30]. Anything else panics at registration (collectHandlers).
- Cost: it wraps `fasthttpadaptor.NewFastHTTPHandler` [code: adapter.go:252-265]. That builds an http.Request per call, buffers the whole body (`PostBody`) even with StreamRequestBody, and starts a goroutine per request to run ServeHTTP so Flush/Hijack can be noticed [code: fasthttp/fasthttpadaptor/adaptor.go:56-58, 74-108]. The fasthttp doc says the result "will be always slower" [adaptor.go:50-55]. [probe fiber2] allocs/request through app.Handler(): native handler 0.0, `http.HandlerFunc` handler 16.0.
- Standing quirk: the middleware ecosystem is fasthttp-only; net/http middleware gets the 16-alloc + goroutine bridge.

## 3. Handler shape
- `type Handler = func(Ctx) error` [code: app.go:40]. `fiber.Ctx` is now an interface (large; ctx_interface_gen.go is 43 KB) so custom Ctx via `NewWithCustomCtx` [docs: whats_new.md:141-160]. Also reachable: `c.Req()`, `c.Res()` views.
- Generics: `fiber.Params[V](c, key, default...)`, `fiber.Query[V]`, `GetReqHeader[V]`, `Locals[V]` [code: req.go:650,1200,1386,1520]. They are only string-to-scalar parsing helpers: on parse failure they silently return the default or zero value (comment at req.go:1380-1383 "returns 0 because can't parse 'john'"). There is no typed-handler mechanism: the handler still pulls values by string name.
- Hertz: `type HandlerFunc func(c context.Context, ctx *RequestContext)`, no error return [code: pkg/app/context.go:343].

## 4. Lifetime
- Ctx is pooled (`app.AcquireCtx(fctx)` from a pool) [code: app.go:80,744,1738-1739]. README "Zero Allocation": values from Ctx "are not immutable by default and will be reused across requests... must not keep any references" [docs: README.md:123-125]. `Immutable` default false [code: app.go:264; docs/api/fiber.md:67]. fasthttp: "VERY IMPORTANT! Fasthttp disallows holding references to RequestCtx or its members" [docs: fasthttp/README.md:409-414].
- [probe fiber1, real loopback, keep-alive, two requests on one connection, string captured in request 1 read after request 2]:
  - Immutable=false: `c.Params("x")` captured as "AAAAAAAA" read later as **"BBBBBBBB"**; `c.Query("q")` "QQQQQQQQ" read later as **"ZZZZZZZZ"**. The first request's value is silently replaced by the next request's data.
  - Immutable=true: strings stayed "AAAAAAAA"/"QQQQQQQQ".
  - `c.Body()` slice kept from POST 1 read after POST 2: "BBBBBBBB" **in both modes** (Immutable=true did not protect the raw []byte; docs only promise `GetBytes` copies "when Immutable is enabled and data still references buffers" [docs: whats_new.md:92]).
- Fiber has its own corruption bug of this kind: #4534 "Bind().Query() intermittently binds empty or cross-contaminated fields since v3.4.0": query keys passed zero-copy (UnsafeString) were cached across requests in a path cache; hit in production auth service; closed 2026-07-13 [issue].
- #3718: panic after migrating because v3 `fiber.Ctx` implements `context.Context` and users pass it to code running after the handler returns [issue]. #4563 (open) proposes v4 "stop implementing context.Context on Ctx" because a pooled mutable object cannot be a context [issue]. #185 "document clearly that Ctx properties are invalid after handler returns" is a 2020 issue still cited as the doc for the config option [docs: fiber.md:67].
- Hertz: RequestContext also pooled (engine.ctxPool [code: pkg/route/engine.go:169,662]); `ctx.Copy()` "can be safely used outside the request's scope... If you want to pass requestContext to a goroutine, call this method" [code: pkg/app/context.go:792-796]. ROADMAP lists "Optimize RequestContext issues under concurrency" as an open item [code: ROADMAP.md:6].

## 5. Binding and validation
- `c.Bind().URI/Query/JSON/Form/Header/Cookie/XML/CBOR/MsgPack/Body/All(&dst)` [code: bind.go:237-502]. Tags: `uri`, `query`, `json`, `form`, `header`, `cookie`, `binding_source` for All [code/docs]. Binding goes through gofiber/schema (reflection), JSON via encoding/json by default [docs: faster-fiber.md].
- Validator: none by default. `Config.StructValidator` interface must be set by the user; it runs on structs only [code: bind.go:190-213]. Docs example uses go-playground/validator.
- Default error mode is "manual": `shouldSkipErrHandling: true` [code: bind.go:32,126-130]. You must return the error yourself and the default handler makes it **500**. [probe fiber1]:
  - GET /n/abc, `c.Bind().URI` into `ID int`: **500** `bind "id" from uri: schema: error converting value for "id"`.
  - POST JSON `{"age":"notint"}`: **500** `bind "age" from body: json: cannot unmarshal string into Go struct field Item.age of type int`.
  - malformed JSON: **500** `bind from body: unexpected end of JSON input`.
  - With opt-in `.WithAutoHandling()` [probe fiber2]: **400** `Bad request: schema: error converting value for "id"` and `Bad request: unexpected end of JSON input`. Plain text, not JSON. Validation errors become 400 only from `All`, other bind methods give 500 even in auto mode [docs: bind.md:1059].
  - So: the wrong default (500 for client mistakes), the message leaks Go internals ("Item.age", schema), field named only by tag.

## 6. Writing twice / late headers
[probe fiber1] `c.JSON(a)` then `return c.JSON(b)`: 200, body only `{"b":2}` (second silently replaces first, no error). `c.SendString("body")` then `c.Set("X-Late","1")` and `c.Status(201)`: header and status both took effect (response is buffered until handler returns). No misuse detection either way. Contrast: net/http (superfluous WriteHeader) and nilo's write-once. Handler that returns nil without writing: 200, empty body.

## 7. Error handling
Central `Config.ErrorHandler`, default `DefaultErrorHandler` [code: app.go:690-706]: plain `error` -> **500 with `err.Error()` as a text/plain body** (leaks internals); `fiber.NewError(418,"teapot")` -> 418 "teapot". [probe] `errors.New("boom secret")` returned 500 body "boom secret". 404 "Not Found", 405 with Allow header ("Method Not Allowed") exist. Errors are returned, not panicked.

## 8. OpenAPI
- No first-party generator. Open proposals: #2347 (generate OpenAPI from route details, open since 2023-02), #4163 "official Swagger support" (open; points to third-party TDiblik/gofiber-swagger and a PR #3702) [issue]. #3271 "API Schemas support" for v3 was closed [issue].
- gofiber/contrib v3 ships `swaggo` (swag annotations, serves generated docs) and `swaggerui` (serves a spec file) [code: contrib/v3 listing, READMEs]. Both are annotation/spec-file based, not derived from handler types. Third-party fiber-openapi repos found had 0 stars.
- Hertz: hz generator from Thrift/protobuf IDL (cmd/hz) and hertz-contrib/swagger (Swagger 2.0, README.md:91). I did not read hz's output.

## 9. Middleware model
`app.Use(func(c Ctx) error { ...; return c.Next() })`; Next returns the downstream error [docs: ctx.md:166-171]. Not calling Next ends the chain; there is no Abort (Gin-style) because the chain is error-returning. `c.Endpoint()` lets middleware look ahead at the target route; docs warn it costs a second router scan [docs: ctx.md:174-203]. Also Express-style `func(Req,Res,next)` shapes accepted (12 variants).

## 10. v2 to v3 migration
- Official guide: docs/whats_new.md + a migration section [docs: whats_new.md:2019+]. Headline breaks: `*fiber.Ctx` -> `fiber.Ctx` interface; `Bind()` now means binding (view binding is `ViewBind`); `BodyParser`->`Bind().Body`; `ParamsInt/QueryInt/QueryBool/QueryFloat` removed in favor of generics; Hooks, Listen, Router, Client, Redirect, Log, Storage interface, middleware context keys, Static/Filesystem, Monitor all changed; Go minimum raised; there is a CLI `fiber migrate --to v3` [docs: whats_new.md:14-24, 2285+].
- Complaints/evidence [issue]: #4381 the migrate tool turned `c.BodyParser(c)` into `c.Bind().Body(c)` and caused an OOM (closed 2026-05); #3375 static serving broken after upgrading; #3718 Ctx-as-context.Context panics; #4056 changes missing from the guide (logger.Output->Stream); #3455 "V3 Beta Feedback" thread; #4534 Bind().Query corruption in v3.4.0. Time cost: v3 beta from 2022 to GA 2026-02-02.
- I did not do a systematic count of migration-pain issues; the list above is a search sample.

## 11. Dependencies
Fiber v3.6.1 go.mod direct requires: schema, utils/v2, uuid, go-colorable, go-isatty, msgpack/v3, testify, msgp, bytebufferpool, fasthttp, x/crypto, cbor/v2, x/net, x/text = **14** (one of them, testify, is test-only) [code: go.mod]. fasthttp 6 direct (compress, go-brrr, bytebufferpool, x/crypto, x/net, x/sys) [code]. Hertz 10 direct including sonic, netpoll, protobuf, gjson, fsnotify [code].

## 12. Performance claims (their words)
fasthttp: "fasthttp server is up to 6 times faster than net/http", client "up to 4 times" [docs: fasthttp/README.md:45,114]; "Writing zero-allocation request handlers" section :470. Fiber: README section "Zero Allocation" (the price being reuse rules) [docs: README.md:123] and a benchmarks section :125. I ran no benchmark apart from the alloc probe in section 2 (native handler 0 allocs, net/http-wrapped 16).

## Mistakes a new framework should not repeat
1. Pooled, mutable request values handed to the user as ordinary strings. [probe] a captured Params/Query value silently becomes the next request's data; the opt-in fix (Immutable) is off by default and did not cover Body(). Fiber then shipped its own instance of the bug (#4534). Make a stale value fail, not change.
2. Making the pooled Ctx a context.Context (#3718, #4563 wants it undone in v4).
3. Bind errors default to 500 with raw Go decoder text; the 400 behavior is opt-in and per-method inconsistent [probe, docs bind.md:1059]. The validator is not on by default.
4. Plain returned error becomes a 500 whose body is the error text [probe].
5. Writing twice and setting headers after the body are silent [probe].
6. Generic helpers that swallow parse errors into zero/default (Params[int] returns 0) [code req.go:1380].
7. A mega-interface Ctx plus 17 accepted handler shapes: the surface and the 16-alloc/goroutine bridge for net/http make the "fast" path a special case.
8. No HTTP/2 because the foundation lacks it; 10 years open (#144).
9. Long beta (about 4 years) and a migration tool that can break code (#4381).
10. OpenAPI left to annotation tools; a request open since 2023 (#2347).
11. Review capacity: 414 of 1222 recent commits are from an AI account; 2 active human maintainers. I flag this as a fact, not a judgment of quality.

## Things worth copying
- Handler returns `error` and the central ErrorHandler decides the response; `NewError(code,msg)`.
- `Endpoint()` look-ahead in middleware; `Name()` on routes for URL building.
- `Bind().All` with a per-field source tag (`binding_source`) and a `BindError` carrying the source [docs bind.md:108,869].
- Zero allocs on the native path is real [probe: 0.0 allocs].
- Hertz `Copy()` as the explicit, named escape hatch; fasthttp's loud "VERY IMPORTANT" rule.
- Route constraints in the path (e.g. int); I did not probe their error status (unverified).

## Not verified
Whether fasthttp h2 PR #2347 has merged; Hertz h3 support; third-party validators' error shapes; benchmark numbers.
