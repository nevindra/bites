# Agent usability study: a two-service shop on nilo-go

Built: a catalog service and an orders service in one module (`example.com/shop`), by Claude Sonnet, on 2026-11-04, against commit 7450052.

## Log

Attempts counts compile or run attempts at that one problem, the first included. "Source" is where the answer came from.

| # | Trying to do | Tried first (and why) | What happened (exact) | Resolved how / source | Attempts | Verdict |
|---|---|---|---|---|---|---|
| 1 | Send a request with an `Authorization` header from a test | `nilotest.Request{Header: map[string][]string{...}}`, because `net/http.Header` and the framework's own `nilogo.Header` are both `map[string][]string` | `cannot use map[string][]string{…} (value of type map[string][]string) as map[string]string` | Read `go doc ./nilotest Request`: `Header map[string]string`, one value each; the compiler error pointed straight at it | 2 | API trap |
| 2 | Mint a token in a test for the `jwt` module | Looked for a signing function in `go doc -all modules/jwt` before writing code | The module only verifies (`Verify`, `Keys`, `HS256` keys); no `Sign` or `Mint`, and neither the guide nor the reference says how a test should get a token | Wrote a 10-line HS256 signer in the test (`crypto/hmac`, `encoding/base64`). Source: `go doc` | 1 | missing feature |
| 3 | Answer 400 naming a body field when the failure is only known in the handler (orders: catalog says product 42 does not exist, so `body.items[1].product_id` is wrong) | `return nilogo.Invalid("body.items[1].product_id", "no such product")` from the handler, because the 400 page says a 400 lists located errors and `Invalid`'s doc shows the 400 listing them | Compiled, registered, ran. Test got `500 {"title":"Internal Server Error","status":500,"request_id":"..."}` and the log said `handler error err="body.items[1].product_id: no such product"`. `go doc Invalid` says it is "the error a Validate method returns"; nothing says a handler may not, nor what a handler should do instead | Used `nilogo.BadRequest("an item names a product catalog does not have").With("location", "body.items[1].product_id")`, a non-standard member, so the shape differs from the framework's own `errors` list. Source: `errors.md` ("Adding members") plus trial and error | 2 | missing feature |
| 4 | Test that an upload over `MaxBody` is a 413 | `srv.Do` with a 6 MiB multipart body, as for any other request | `nilotest: writing the request: write memconn 203.0.113.9:4000->memconn: io: read/write on closed pipe` (the test fails; the 413 the server did send is never returned, because the server answers before the body and closes) | `srv.Dial`, write only the head with `Content-Length: 6291456`, then `srv.Read(c, false)`. Source: the "Writing the bytes yourself" section of the testing guide, adapted by trial and error; the guide does not say `Do` cannot see an early refusal | 2 | doc gap |
| 5 | Test a server-sent events route | Looked for an SSE reader in `go doc -all ./nilotest` (there is `DialWS` for sockets, none for SSE) and in the testing guide (no SSE section) | No helper. `Do` would wait for a response that never ends, so it was not tried | `srv.Dial`, write the GET by hand, read lines with `bufio` and a deadline. To know the handler had subscribed before publishing, the stream sends an SSE comment first (`e.Comment("open")`) and the test waits for it. Source: reasoning from `streaming.md` and `Dial`; worked on the first run | 1 | missing feature |
| 6 | A WebSocket route where the client only listens | `nilogo.Inbox[Nothing]{}` with `type Nothing struct{}`, returning an empty Inbox, because the guide's only example has an `OnMessage` and the signature needs a `Msg` type | Compiled and registered. The docs never show a send-only socket or say what `Msg` should be. At run time a client that sends `not json` gets silence: no close. With an `OnMessage`, the same frame closes `1007 "malformed message: message: invalid JSON at byte 0"` as the guide says. The guide's rule "without an OnMalformed, a malformed message closes the socket with 1007" does not hold when `OnMessage` is nil | Left it as is; found by a probe test, not by a doc. Source: trial and error | 2 | doc gap |
| 7 | Deliberate probes of seven wrong registrations I did not hit by accident (provider missing, `\d` pattern, wildcard with no Path section, `Upload` with no `MaxBody`, `*int` field, `validate` tag, `chan` in a socket's message type) | Wrote each wrong on purpose, to see the refusal text | Six refused with the route, the field and `registered at probe_test.go:N`, each saying what to write instead; e.g. `Ptr.Body.N: a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]` and `Up.Body is an Upload, but the route has no MaxBody: ... give MaxBody(n) to the route or to a group above it`. The seventh (`^\d+$`) was accepted, correctly: `\d` is allowed, I had misremembered the list | Read the messages. Source: the refusals | 7 | good refusal |

Rows 1 to 6 are stumbles that happened while building. Row 7 is a set of refusals I provoked on purpose after the app was done; none was met by accident, so it shows the refusals are good, not that they saved me. Nothing in the log was reconstructed afterwards: each row was written when it happened. I did not open any forbidden file; the places where I wanted one are listed under "Needed source" below.

## First-try successes

These are not stumbles, so they have no row, but they are the evidence for the last section.

- S1. The catalog, with typed paging and filter, a SKU pattern, a name length, a price range, a JWT guard, request id, CORS, rate limiter, access log, health, OpenAPI and the docs page, compiled and registered on the first run, and every route test passed on the first run after two test-file typing errors (row 1).
- S2. `jwt.New` plus `nilogo.BearerAuth(v.Verify)`: no token is a 401 with `WWW-Authenticate: Bearer realm="api"`, an expired token is `invalid_token`, and the API document shows `bearerAuth` on the POST, PUT and DELETE only. Source: security guide.
- S3. Wire names: `page`, `per_page`, `product_id` and `body.items[0].qty` were guessed from the naming rule in the requests guide and were right.
- S4. Several validation errors in one 400 in a fixed order (`body.name,body.sku,body.price`), and a bad enum, id or page naming `body.status`, `path.id` and `query.page`.
- S5. A service struct whose methods are the handlers (store behind an interface, catalog client in a config), from `handlers.md`, with no container.
- S6. WebSocket: a `Topic` per order held in a map of pointers (the guide says the zero value must not be copied), `Subscribe` in the handler, `Publish` from another handler, and a test that reads the message with `DialWS` and `ReadJSON`. A missing order is refused before the upgrade with a 404 (`TryDialWS`).
- S7. File upload and download through `Upload.Each`, `MaxBody`, an `os.Root` and `nilogo.File{Root, Name, ContentType, Attachment}`: no problem, including the 413 and the 400 for a missing part.
- S8. Both binaries were started for real: a token minted outside Go created a product, an order was created through the real catalog over HTTP, `/docs` answered, and SIGTERM stopped both with no error.

## Needed source

None. The one time I wanted to read the code was row 3 (is a located error from a handler meant to work?), and the `go doc` text answered it by omission only. A user cannot tell "not supported" from "not documented here" without source or an ADR.

## Counts

Verdicts across the six stumbles: `API trap` 1 (row 1), `missing feature` 3 (rows 2, 3, 5), `doc gap` 2 (rows 4, 6), `good refusal` 1 (row 7, provoked). `doc wrong`, `unclear refusal`, `silent trap` and `needed source`: 0.

Compile and run attempts: 12 invocations of `go vet`, `go build` or `go test` that compiled my code, of which 2 failed to compile (both in a test file, both `nilotest.Request.Header`, row 1) and 2 compiled and failed a test (rows 3 and 4). The two `main` packages and the first registration of each service passed on the first attempt.

What works, is partial, or was left out:

| Part | State |
|---|---|
| catalog: list with paging and name filter, get, create, update, delete | works, tested |
| catalog: 400 naming the field, 404, 409, 401 on a missing and an expired token | works, tested |
| catalog: request id, CORS (including a preflight), rate limit 429, access log | works, tested (the access log is only seen in the test output and the real run) |
| catalog: health and readiness, graceful shutdown, OpenAPI, docs page | works; shutdown checked by SIGTERM on the real binary, not by a test |
| orders: create with a check against catalog | works, tested with a fake catalog over `httptest`; also run against the real catalog |
| orders: WebSocket status subscription and broadcast | works, tested |
| orders: SSE of order events | works, tested by hand-reading the stream |
| orders: invoice upload and download | works, tested |
| a located 400 from the handler for a product catalog does not know | partial: a 400 with a non-standard `location` member (row 3) |
| a test that the 5 MiB limit is a 413 | works through `Dial` (row 4) |
| `stdhttp`, `modules/otel`, compression modules | not used, not asked for |

`go vet ./...`, `go test ./...` and `go test -race ./...` pass in the app folder (25 tests), and both binaries build.

## The five changes that would have helped most

1. **Let a handler return a located 400, and say so in the docs (row 3).** `Invalid` is documented only for `Validate`, and returning it from a handler is a bare 500 with the field name in the log and nothing to the client. A handler that finds, after a lookup, that a field is wrong is common (a foreign key, a duplicate, a stock check). Either accept `*FieldError` from a handler as a 400 in the same list, or put the supported way on the `Invalid` doc and in `errors.md`. Today the only route is a `With` member that no client library knows.
2. **Make `nilotest` handle a server that answers before it has read the body (row 4).** `Do` fails the test with `io: read/write on closed pipe` when the server refuses a large body early, which is exactly what `MaxBody` is for. Return the response (the 413) when one arrived, or document `Dial` plus `Read` as the way to test it in `testing.md` and `forms-and-uploads.md`.
3. **Add a way to read an SSE stream in `nilotest`, or a section in the testing guide that shows one (row 5).** Sockets have `DialWS`; streams and events have nothing, and `Do` cannot return for a stream. A `srv.OpenStream` that returns events as they come, and a note on how to know that a handler has subscribed before the test publishes (I used a first comment), would have saved the design work.
4. **Give the `jwt` module a signer for tests, or say in its reference how a test gets a token (row 2).** Every app that uses `BearerAuth` with `jwt` has to test a 401 and a 200; today each one writes its own HS256 signer. A `jwt.HS256Token(key, claims)` (clearly marked for tests), or a ten-line recipe in `security.md`, removes it.
5. **Document the send-only WebSocket and what a nil `OnMessage` does (row 6).** The guide's one example always has an `OnMessage`. A server-push route needs a `Msg` type it never uses, and with a nil `OnMessage` a malformed frame is silently ignored although the guide says it closes with 1007. Add a send-only example, name the type to use, and state the nil-`OnMessage` behaviour (or make it match the rule). A smaller version of the first change in this list also applies to `nilotest.Request.Header` being `map[string]string` while `Response.Header` is `map[string][]string` (row 1): the docs are right, the shape is the surprise, so a one-line note beside the type, or a helper such as `Bearer(token)`, would remove it.

## What worked well

- Registration refusals read like instructions: they name the route, the type and field, the line, and the fix (row 7; the `MaxBody` and pointer messages are the best of them). I met none by accident because the guides taught the rules before I broke them (S1, S5).
- The type is the contract held: validation types with `Length`, `Range` and `Pattern` produced a located, ordered 400 with no code of mine (S4), and the OpenAPI document picked up the Bearer scheme per route (S2).
- The guide examples are close to complete programs. Authentication, CORS, rate limiting, shutdown, the upload, the WebSocket and the events were each copied from a page and worked (S1, S2, S6, S7, S8).
- The error vocabulary (`NotFound`, `Conflict`, `NewError(502, ...)`, `Created` with `Location`) and the `errors.Is` mapping in a service method need no explanation (S5).
- `nilotest.Start` runs everything, middleware and rate limiter included, with no port, and the test output shows the app's log only when a test fails, which made the two real failures easy to read (rows 3, 4).

## Review against the source

Read by the session that ran the study, on 2026-11-04, after the report was written; the subject did not see the source, the reviewer did. Two verdicts are wrong in the subject's favour, and both are reclassified here:

- **Row 3 is a `silent trap`, not a `missing feature`.** `Invalid` is an exported function that returns an `error` any code can return, and from a handler it compiles, registers and answers 500 with nothing for the client; `collectFieldErrors` (`validate.go:51`) is reached only from the `Validate` path, so a `*FieldError` from a handler is an ordinary handler error.
- **Row 6 is `doc wrong` and a `silent trap`, not a `doc gap`.** The doc comment of `Inbox` (`websocket.go:95` and the `OnMalformed` field) says a malformed message with no `OnMalformed` closes the socket with 1007, and `wsHandler.Message` (`websocket.go:315`) returns before decoding when `OnMessage` is nil, so on a send-only socket every message, malformed or not, is dropped in silence.

Corrected counts over the six stumbles: `silent trap` 2 (rows 3 and 6), `doc wrong` 1 (row 6), `API trap` 1 (row 1), `missing feature` 2 (rows 2 and 5), `doc gap` 1 (row 4), with row 6 counted under two verdicts.

## Where the application is

The application was removed from the tree on 2026-11-04 to keep the repository small; it is in the history at commit 981ee3e (`git show 981ee3e:docs/research/agent-usability/app`).
