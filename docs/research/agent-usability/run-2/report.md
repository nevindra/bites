# Agent usability test, run 2: rooms and bookings

A two-service room booking application (`cmd/rooms`, `cmd/bookings`, module `example.com/bookings`) built by Claude Sonnet on 2026-11-04 against nilo-go commit `7450052`, with uncommitted changes in the working tree (about 77 modified or deleted files, mostly docs and tests, which is what the reader saw).

The code is in `app/`. The log below was kept while building, in the order the rows happened; the first row of a stumble is not always the first thing I did.

## Caveats about this run

I was allowed to read `examples/`, and `examples/shop` is almost exactly this assignment (two services, JWT, a WebSocket that only sends, an event stream, an upload and a download, a located 400 after asking another service). It began as the application of run 1, so it already holds what run 1 found missing. I copied its shape for the `newApp(Config)` split, the shutdown block in `main`, the `Events` loop with a subscriber channel, the `Upload.Each` handler, the `File` download, the `lookup` call over `net/http` that turns a 404 into `nilogo.Invalid("body.x", ...)`, and the `BearerAuth(verifier.Verify)` line with the claims type as a Provider output. That is why there were no compile errors: a fresh agent without `examples/shop` would very likely have stumbled more. Anything not in that example (a Provider that depends on `Claims` to check a scope, the tenant Provider, `Opt[Null[string]]`, embedded `Filter` and `Paging` sections with `Defaults`, `Form[T]`, `If-Match`, the send-only socket on a per-booking topic, the tests) I wrote from the guide, the reference and `go doc` alone.

I never opened a `.go` file of the framework outside `examples/`, nor anything under `docs/adr`, `docs/design` or `docs/research`. I did not feel the need to. Two things I would have liked to see in source: how `nilotest.WS.SendJSON` encodes (row 5) and in which order independent Providers run (row 3); I found both by writing probe tests instead.

## Log

| # | Trying to do | First attempt and why | What happened (exact) | Resolved by, source | Attempts | Verdict |
|---|---|---|---|---|---|---|
| 1 | Learn the methods of `nilogo.Date` and `Opt` from `go doc` (comparing dates in a store filter). | `go doc . Date` and `go doc -all . Date`, expecting the method list as for any type. | `go doc . Date.String` printed `doc: no method or field Date.String in package github.com/nevindra/nilo-go`; `go doc -all . Date` lists only `func DateOf(t time.Time) Date`. `Date`, `Opt`, `Null`, `CSV`, `NonEmpty` are aliases to an internal package, so `go doc` shows none of their methods. | `docs/reference/input.md#date` lists all methods (`Before`, `After`, `Compare`, `String`...). Without the reference an agent that only has signatures cannot see `Opt.Or/Get`, `Date.Before`. | 2 | doc gap |
| 2 | Give every handler the tenant id from `X-Tenant`, 400 when missing. | A Provider `tenantFromHeader(ctx, req, struct{}) (Tenant, error)` returning `nilogo.Invalid("header.X-Tenant", "required")`, because the Providers guide says "from a handler, a Provider or a middleware alike" and the docs call a Provider the way to hand a request-scoped value to handlers. | Worked first time: `400 {"errors":[{"location":"header.X-Tenant","message":"required"}]}`. But the API document does not mention `X-Tenant` at all (a Provider-filled field "is not a parameter"), so a generated client cannot know the header is required. | `CustomAuth(nilogo.SecurityScheme{Type: "apiKey", In: "header", Name: "X-Tenant"}, tenantFromHeader)` puts it in the document as a security scheme (`apiKeyAuth.X-Tenant`) and a 401 that the route never gives (the real answer is 400). Using auth machinery for a non-credential is a workaround; there is no way to declare a Provider's header or parameter. Source: guide/security.md "A Provider you wrote, in the API document", guide/openapi.md. | 2 | missing feature |
| 3 | Make a missing token a 401 even when the tenant header is also missing. | Wrote the Inputs as `Tenant Tenant` then `Writer Writer`, by habit of "tenant first". | A request with no token and no `X-Tenant` got `400 header.X-Tenant required`, not 401: an unauthenticated caller is told about validation before it is told to authenticate. Swapping the two field declarations made it `401 a Bearer token is required`. So independent Providers run in the field order of the Input. No page says so (`grep -i "field order"` over guide and reference finds nothing), and nothing warned at the time. | Found by a probe test printing the answers; swapped the fields in every write Input. | 2 | silent trap |
| 4 | Say in the OpenAPI document that a route answers 404, 409, 412 or 403. | Looked for a route option to declare error statuses (`Returns`, `Responds`, `Errors`) in `go doc .` and in reference/openapi.md. | None exists. The document lists `400 InvalidInput`, `401`, `413`, `415` and `default` only; the 403 of the scope Provider, the 404, 409 and 412 are not stated. | Left undocumented; recorded. `Returns` is only for success variants. | 1 | missing feature |
| 5 | Test a chat message in both directions with `ada.SendJSON(ChatIn{Text: "hello bob"})`. | `nilotest.WS.SendJSON(v)` "sends `v` encoded as JSON"; the response helpers (`Response.JSON`, `Message.JSON`) match names ignoring case and underscores, so I assumed the sending side also followed the app's naming policy (snake_case). | `go test` failed with `nilotest: reading a message: nilotest: no message in time` after 5 real seconds, and in the malformed-message test a later message was answered by my own `OnMalformed` instead of being broadcast. A probe showed the app received `{"Text":"hello bob"}` (encoding/json, Go field name) and treated it as malformed. Nothing at the time said which name was sent. | Sent `map[string]string{"text": ...}` instead. Source: a probe test printing the frames; no doc says `SendJSON` uses encoding/json names. | 3 | silent trap |
| 6 | Check the guide's own WebSocket test (`ws.SendJSON(Echo{Text: "hello"})` against an `Echo` route). | Copied the pattern from docs/guide/testing.md "Testing a WebSocket". | Run against a default app it closes the socket: `kind=close code=1007 reason="malformed message: message.Text: unknown field"`. The example is marked `compiles` only, so it was never run; as written it cannot pass. | Same fix as row 5. | 1 | doc wrong |
| 7 | Read the OpenAPI document of rooms to see the schemas of create and update. | Expected the PUT body to have a component of its own, as the guide says an anonymous `Body struct{...}` is "a component named after its Input". | `PUT /rooms/{id}` requestBody refers to `#/components/schemas/CreateRoomBody`. The two Inputs have identical anonymous body structs, which Go treats as one type, so the first registered name wins. A generated client gets an update that takes a `CreateRoomBody`. | Left as it is; recorded. | 1 | API trap |
| 8 | (Deliberate probe) Register an upload route with no `MaxBody`. | Wanted to see whether the refusal tells an agent what to change. | `POST /u: Up.Body is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it` plus `registered at probe_test.go:28`. | The message is the fix. | 1 | good refusal |
| 9 | (Deliberate probe) An Input field `Tenant Tenant` with no Provider. | The usual mistake when a Provider is forgotten. | `GET /n: Needs.Tenant is not a section (Path, Query, Header, Cookie or Body), and no Provider of Tenant covers this route` plus the line. | The message names the field, the type and the missing piece. | 1 | good refusal |
| 10 | (Deliberate probe) Call `app.Provide` after the first route. | A natural ordering slip in a `newApp`. | `app: Provide is called after GET /x (at probe_test.go:44) was registered, which was checked without it: move Provide above it` plus the line. | The message says what to move and where. | 1 | good refusal |
| 11 | (Deliberate probe) A `validate:"required"` tag, a habit from Gin. | Wanted to see whether a foreign tag is ignored or refused. | `POST /t: TagIn.Body.Name has a validate tag, which nilo-go does not read, so its rules would never run: give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in TagIn.Validate` plus the line. | The message names the three ways out. | 1 | good refusal |

Rows 8 to 11 are deliberate probes of refusals, not stumbles; they are recorded because a refusal that tells you exactly what to change is a result.

## Counts

| Verdict | Rows |
|---|---|
| doc gap | 1 |
| doc wrong | 1 |
| unclear refusal | 0 |
| good refusal | 4 (all deliberate probes) |
| silent trap | 2 (rows 3, 5) |
| API trap | 1 (row 7) |
| missing feature | 2 (rows 2, 4) |
| needed source | 0 |

Compile attempts: every `go build` and `go vet` run was clean, so zero compile errors. The first build of `cmd/rooms` and the first vet of `cmd/bookings` both passed (two compile attempts before the tests, then clean vets throughout). Test runs: rooms passed on its first run; bookings failed on its first run (two tests, both caused by row 5) and passed on its second.

Final state: `go vet ./...`, `go test ./...` and `go test -race ./...` pass in `app/`; both binaries build; I also ran both binaries on real ports and drove them with curl (a booking over capacity is a 400 at `body.guests`, a good one is a 201, the same `request_id` appears in both services' access logs, and SIGTERM stops both).

## What works, what is partial, what was given up

Works, with tests for each: rooms list (capacity minimum, two dates with a cross-field check, paging with defaults), get, create, update, delete, 404, 409, 412 (as `NewError(412, ...)` with an `ETag` and `If-Match`; there is no 412 constructor and none was needed), `Opt[Null[string]]` description (absent keeps, null clears, a string sets), bearer JWT with a scope Provider (401 for a missing or wrong token, 403 for a token without `rooms:write`), the tenant Provider and its 400, request id, access log, security headers, Prometheus metrics, health, graceful shutdown, the OpenAPI document. Bookings: create with the call to rooms (400 naming `body.end`, `body.room_id` or `body.guests`, 502 when rooms fails), a chat WebSocket in both directions with a malformed message answered and the socket kept, a send-only WebSocket (and its 1003 for a client that speaks), the event stream of created bookings, an upload with a form field beside the file (413 over the limit, 400 with no file part), the download, and the form-encoded cancel (409 the second time, 400 for a missing reason or an extra field, 415 for JSON).

Partial: the OpenAPI document of both services does not say that `X-Tenant` is needed except through the `CustomAuth` workaround (row 2), and does not state the 403, 404, 409 or 412 answers (row 4). The stores are in memory.

Given up: nothing.

## The five changes that would have helped most

1. Make `nilotest.WS.SendJSON` encode with the app's naming policy, as the reading helpers match names, or say loudly in its doc comment that it uses `encoding/json` names; and run (not only compile) the guide's WebSocket example, which as written closes with 1007 (rows 5 and 6). This cost the most time: the failure was a five second timeout with `no message in time`, far from the cause.
2. Let a route or a Provider declare what the OpenAPI document cannot read from types: a required header a Provider reads, and the error statuses a route answers (404, 409, 412, 403). Today the tenant header needs a `CustomAuth` that calls it a credential and adds a 401 that never happens (rows 2 and 4).
3. State the order in which independent Providers run (it is the field order of the Input) and, better, run the authentication Providers first, so a request with no token is never told about a missing header before it is told to authenticate (row 3).
4. Make `go doc` useful for the wrapper types: `Opt`, `Null`, `Date`, `CSV` and `NonEmpty` are aliases to an internal package, so `go doc` shows none of their methods, and an agent that only has signatures cannot see `Or`, `Get`, `Before` (row 1). A method list in each alias's doc comment, or real types, would fix it.
5. Name an anonymous Body component after the Input that is least ambiguous, or give identical bodies a shared, neutral name; today an update's body is called `CreateRoomBody` in the document (row 7).

## What worked well

The shape of the API was guessable: a handler is a function of one Input, sections are named by field, and nothing needed a tag. Every one of the eight refusals I provoked on purpose named the route, the field and the line, and most said the fix. Typed `Opt[Null[T]]` made the absent versus null distinction a field type and not a convention. The 400 names every wrong field by its wire name, including for `Validate`, for a Provider's `Invalid` and for a handler's `Invalid` after a call to another service, so the located error the assignment asks of bookings was three lines. `nilotest` made every route, both sockets, the event stream and the upload testable with no port, and the clock made the JWT's `exp` follow the app's clock without extra work. The error answers all carried `request_id` once `RequestID` was on, and propagating it to the other service was one line (`RequestIDFrom`).

## Where the application is

The application was removed from the tree on 2026-11-04 to keep the repository small; it is in the history at commit 981ee3e (`git show 981ee3e:docs/research/agent-usability/run-2/app`).
