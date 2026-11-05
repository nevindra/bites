# Stress test of R1, R2, R10, R11, R12 and the Session rename (middleware and Providers, steps 5 to 7)

Probes in this folder (`r1_test.go`, `r10_test.go`, `r12_test.go`, `harness_test.go`, `syncmap_test.go`), run with `go test -race -count=1 .` and `go test -run XXX -bench . -count 3 .`, pinned `taskset -c 0,1,8,9`, on the Ryzen 7 9700X, Go 1.27.2, uncommitted tree of 2026-10-31. "Measured" is a number from these probes; "claim" is read from code or reasoned.


"Measured" means a number from these probes. "Claim" means read from code or reasoned, not run. The R1 chain is a model of the shape that decides allocations, not the framework, so step 5 must still measure the real row of ADR 0003.

## Facts from the code that the verdicts rest on

- **The head is in pooled memory.** It borrows `scratch.buf`, which is pooled (`getScratch` and `putScratch` in `internal/engine/conn.go`). The slab is a fresh `make` per request, sized `headLen` plus the declared body, and dropped after the handler returns. No copy of the head is in the slab today; it holds only the fields the Input copied.
- **Wrappers have no methods.** `OK[T]` and `Created[T]` carry no methods (`output.go`), so a user interface with a method cannot be implemented by them. A handler that mixes wrappers can only return `any`.
- **Interface returns cost 4 allocations, not 2.** Measured on the real framework over memconn with the same Input: plain Out 2.00, `OK[Out]` 2.00, `any` holding `OK[Out]` 4.00, `any` holding Out 4.00. No ADR 0003 row or test covers this.
- **A late `Use` or `Provide` silently skips.** Group options are copied at group creation and again at route registration (`register.go`), so a later call never reaches a child group or a route already made.
- **chi refuses the same thing.** `mux.go:102` of v5.5.0, read from the module cache, panics with "chi: all middlewares must be defined before routes on a mux".

## R1: the Response a middleware sees

**Verdict: MODIFY.** Keep the small value with an opaque body. Do not defer the encode. Encode in the innermost step into the engine's pooled response buffer, and let `Response` carry status, headers and the body size.

**Design:**
- `Response{Status int32; ctype; n uint8; size int32; h [6]pair; stream func}` is returned by value.
- The terminal step turns the handler's result into the value and appends the body to the engine buffer. The buffer never reaches user code.
- A middleware reads and sets status and headers, reads `Size()`, or returns a fresh `Response`, in which case the engine truncates the buffer.
- Errors become a `Response` at every `Next` boundary, so an outer middleware always sees the final status.

**Evidence:**
- **Cost of the three designs.** Three middleware layers each:

  | design | allocations | time |
  |---|---|---|
  | early encode, 4 pairs | 0 | 90 ns |
  | early encode, 6 pairs | 0 | 96 ns |
  | early encode, 8 pairs | 0 | 107 ns |
  | deferred, Output in a slot on the route's frame, 4 pairs | 0 | 88 ns |
  | deferred, Output in a slot on the route's frame, 8 pairs | 0 | 103 ns |
  | deferred, Output boxed in `any`, 8 pairs | 1 (64 B) | 117 ns |

  Boxing breaks the hard axis. The frame slot needs the `noescape` device through the chain, and a kept `Response` then points at a dead frame (claim).
- **Body size.** Early encode makes the size known, which the access log of ADR 0032 and the milestone 3 hook need. A deferred body hides it (claim).
- **Inline pairs.** Four is too few. CORS adds up to 4 headers, request ID 1, and a `Created` plus Cache-Control 2 more, about 7 (claim, counted from ADRs 0032 and 0013). Keep Content-Type out of the pairs and use 6 or 8. Sizes measured: 160 B for 4 pairs, 224 B for 6, 288 B for 8. Step 5 needs an allocation test with CORS, request ID and a `Created` with Location together.
- **Streaming (step 10).** A `Stream` is status, headers and a function value, already on the heap. `Response.stream` holds it and the engine runs it after the chain returns, so no slot is needed.
- **Short circuit.** `return Response{}, nilogo.Unauthorized(...)` or a `Problem(401, detail)` value. Error answers are not budgeted.
- **Errors must become `Response` values before the chain unwinds.** The 400s from decoding and Providers are written straight into `ex.Respond` today. CORS must add its headers to them or a browser hides the answer. This holds for either design.

**Best alternative:** deferred encode with the Output in the route's frame. It lost because it needs the unsafe pointer for no gain on the hot path (same allocations, same time) and it hides the size.

**Fails at:**
- A middleware cannot cache, hash (ETag) or compress a body, so gzip and ETag must be engine features and ADR 0031 should say so.
- A discarded inner response wastes its encode, a few hundred ns on a failure path (claim).
- `Response` is 224 B and is copied at each layer.

## R2: `Use` or `Provide` after a route or child group

**Verdict: KEEP for groups, with two changes.** App-level `Use` is exempt. Offer `Use` and `Provide` as options too, so the mistake cannot be written there.

**Design:** each scope keeps `sealedAt`, the site of its first route or child group. A later `Use` or `Provide` panics naming its own line, the sealing line, and "move it above". That is four refusal-table rows for a group and two for the app.

**Evidence:**
- **Why it is needed.** Without it the call silently skips (see the facts above), and chi refuses the same case.
- **Why app `Use` is exempt.** It runs before routing and is read at request time, so a late call cannot skip a route. The order among `Use` calls is call order, outermost first.
- **Why app `Provide` is not exempt.** The route's Registration check runs at the route's line, so a Provider registered later would give a false "no Provider for User" there.
- **Modular apps.** The normal order is `api.Use(auth); users.Register(api); orders.Register(api)`. The case that must be loud is a package calling `g.Use(audit)` inside `Register` after another package added routes. The way out is `g.Group("/orders", Use(audit))`.

**Best alternative:** options only, with no `Use` or `Provide` methods. Kept as the second spelling, not the only one, because every Go router user types `g.Use` and `RouteOption` already exists.

**Fails at:**
- It refuses harmless code, such as a late `Provide` of a type no earlier route asks for. A uniform rule is easier to teach than a conditional one (claim).
- App `Provide` still depends on call order.

## R10: `Request.Header(name)` aliasing the slab

**Verdict: MODIFY.** The premise is half wrong: the head is in pooled `scratch.buf`, not in the slab. Aliasing it would hand pooled memory to user code and break ADR 0008. Measured negative control: an alias of a reused buffer read "Bearer AAAA" and then "Bearer BBBB". The fix is one copy of the head into the slab.

**Design:**
- At request start, copy the request line and header block into the slab once.
- `Request` is a value holding that string and a `netip.AddrPort`.
- `Method` returns an interned constant for the nine standard methods.
- `Path`, the raw query and `Header(lowerName)` are substrings of the copy.
- Size the slab at `2*headLen` plus body for requests that have a view.

**Evidence:**
- **Kept string survives.** Through the real engine, 300 requests on one connection each kept `Header.Authorization`. Afterwards 0 strings had changed and 0 shared a start address with the previous request's string. This held with `runtime.GC()` in between and under `-race`. The same mechanism covers the head copy.
- **Lookup allocations.** Both a scan and an index lookup allocate nothing (measured).
- **Lookup time.**

  | | scan, 1 lookup | scan, 4 lookups | index built per request, then 4 lookups |
  |---|---|---|---|
  | browser head, 642 B | 46 ns | 279 ns | 137 ns (index build alone is about 100 ns) |
  | API head, 286 B | 36 ns | 144 ns | 72 ns |

  Scan wins for 1 or 2 lookups and an index for 4 or more. Which to pick depends on how many lookups real middleware makes, which step 5 must count.
- **Cost of the copy.** `Slab.Init(642)` takes 45 ns. `Init(1284)` plus a 642 B copy takes 72 ns, so the copy adds about 27 ns, or 0.8% of a request, with the same single allocation.
- **Why not a lazy copy per `Header` call.** The slab is sized to the head, and the same bytes can be copied twice, once by the Input's `Header` section and once by a middleware. A 432 B head with a 380 B Authorization value copied twice gave 1 fallback, which is +1 allocation and breaks the 2 row. One head copy in a 2x slab gave 0 fallbacks. This answers the roadmap's "cost of the `Request` view" item.
- **What `Request` can expose without more copies:** method, path, raw query, host, headers, and the remote address as `netip.AddrPort`. The `AddrPort` is a claim and was not measured; `RemoteAddr().String()` allocates, so no string. The body stays hidden.
- **Request ID.** A request ID middleware needs about 32 B of extra slab for the ID it generates.

**Best alternative:** a lazy per-lookup copy. It lost on the overflow above.

**Fails at:**
- A head near `MaxHead` (8 KiB) doubles its slab for that request (claim).
- Every request of an app with app-level middleware pays the copy.

## R11: a nested group overriding its parent's Provider

**Verdict: MODIFY.** Nearest scope wins, but replacing needs its own word.

**Design:**
- `Provide[T]` is refused when any enclosing scope or the same scope already has a Provider for T, naming both lines.
- `Override[T]` replaces an outer Provider for that scope and below, and is refused when there is nothing to override.
- Each route resolves one map from type to Provider, nearest scope first, built at Registration. A Provider's own dependencies resolve from that same map.

**Scenarios (reasoned, not run):**
- **Accident.** Under a parent that provides `User` by session, a package provides `User` by API key on a nested group, expecting to add a second way in. With silent nearest-wins, everything under that group, including a parent `Account(deps User)`, now authenticates by API key only, with no message.
- **Memo conflict.** If a Provider resolved its dependencies in its own declaring scope, one request could hold two different `User` values under one type key. The per-route map removes that.
- **Decorator.** A Provider for T that asks for T is a self-cycle, already refused by the cycle check. The idiom is a distinct type such as `Admin`.
- **R2 sealing** is what makes this deterministic, because the parent's Providers all come before any child exists.

**Best alternative:** silent nearest-wins as recommended. It lost because a silent authentication change is the mistake class ADR 0007 exists to refuse. Banning overrides entirely loses the subtree case for the price of one word.

**Fails at:** one more name in the API, and an override changes the dependencies of Providers declared above it.

## R12: `Returns[T]()` for interface-typed returns

**Verdict: KEEP, with details.**

**Design:**
- `Returns[T]()` is a `RouteOption` that appends `reflect.TypeFor[T]()` to the route's config.
- For an interface `Out` at least one is required.
- Each type is compiled at Registration, must implement `Out` when `Out` has methods, and may not repeat.
- `Returns` on a route whose `Out` is not an interface is refused, and so is `Returns` given to `Group`.
- Plans live in a small per-route slice found by comparing `reflect.TypeOf(v)`, which replaces the global `sync.Map` and `planFor`.
- A handler returning an undeclared type gets a logged 500 and is never compiled on the fly.

**Evidence:**
- **Technique cost.** Reading the value from the interface's data word and finding the plan by scan takes 15 ns and 1 allocation. Today's `reflect.ValueOf`, `reflect.New`, `Set` takes 36 ns and 2 allocations. The `sync.Map` lookup is 18 ns. These are microbenchmarks of the technique.
- **Framework effect.** An interface route could drop from 4 allocations to 3 (claim for the framework). That is still above 2, so ADR 0003 needs a row that says 3.
- **`Implements` has no teeth for `any`.** Because wrappers have no methods, the `Implements` check says nothing for `(any, error)` returning `OK[T]`. `Returns` is then the only declaration, which makes the Registration check stronger than the compiler's and makes the runtime 500 for an undeclared type necessary.
- **OpenAPI.** Step 14 reads statuses and schemas from the per-route list, as ADR 0013 already says.

**Best alternative:** `Either[A, B]` in the return type, with `First(v)` and `Second(v)` constructors. It matches the "types are the contract" thesis, adds no allocation or reflection, and the compiler checks it. An `Either` of two `Created`-shaped wrappers is 216 B on the stack (measured). It lost because Go has no variadic generics, so every arity must be hand-written. It is worth adding later if the 3 allocations matter.

**Fails at:** a handler can still return an undeclared type, which is a 500 at request time and not a compile error. `Returns` and the real returns can drift, and only a test that runs the handler catches it.

## Session rename

**Verdict: MODIFY.** Not `Handlers[Msg]`; use `Inbox[Msg]`.

**Every mention to edit** (grep over `*.md` and `*.go`, outside `docs/research`, which has none):
- `GLOSSARY.md` line 47.
- ADR 0036 line 3.
- ADR 0047 line 3 and the paragraph "The Session" on line 11.
- ADR 0035 line 7 ("the session value").
- `docs/roadmap.md` line 26 and line 97, which this settles.
- ADR 0023 line 5 uses `Session` in the cookie sense and stays as written. The "sessions" in the README and in ADRs 0002, 0017 and 0032 are also the cookie sense and stay.
- No Go code uses the name.

**Collisions of `Handlers`:**
- The glossary already uses "handler" for the typed handler and for `http.Handler` (ADR 0019).
- ADR 0014 calls the user's struct of route methods "Handlers", and `type Handlers struct{ db }` is a common Go habit. A route function returning `Handlers[Msg]` reads as handlers returning handlers.
- ADR 0035's title already uses "message handlers" for the parts (`OnMessage`, `OnClose`), so the word means both the whole and the parts.
- `gorilla/handlers` is a well-known middleware package.

**Why `Inbox`:**
- Nothing in the standard library or this repository uses the word (grep: no match).
- It is where one socket's messages arrive one at a time, in order, with blocking as backpressure (ADR 0047).
- It reads well beside the others: `Join(ctx, in JoinRoom, s *Socket[Out]) (Inbox[In], error)`.
- `OnClose` fits slightly less well; accepted.
- Runner-up: `Receiver`, rejected because it collides with Go's method receiver and ADR 0014's method values.

**Proposed glossary entry:** **Inbox**: the value a WebSocket route returns on upgrade, holding that socket's state and the functions called with its messages and its close. _Avoid_: session, handlers, connection, client.

With `Inbox`, `Session` is free for milestone 3's cookie session, and ADR 0023's Provider example needs no change.

## Not measured

- The real allocation row of a route with middleware, which step 5 measures.
- The lookup count of real middleware.
- `netip.AddrPort` from a TCP connection.
- How Gin and Echo behave on a late `Use`.
- An 8 KiB head with a 2x slab.

Files are in `/home/nezhifi/Development/nilo-go/docs/research/decisions-middleware/`: `go.mod`, `harness_test.go`, `r1_test.go`, `r10_test.go`, `r12_test.go`, `syncmap_test.go`.
