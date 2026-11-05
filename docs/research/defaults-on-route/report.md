# Defaults on the route: a prototype, and why it is not built

The question: a handler states `Opt.Or(50)` in code, so the API document loses `default: 50` and a client generator cannot show it. Can a default be stated once, on the route, and reach both the decoder and the document? Raised on 2026-11-03.

## What was run

A throwaway module (its own `go.mod`, as every folder here with Go code is), `go 1.27`, `go1.27.3 linux/amd64`, at nilo-go commit `fcde2af`, run on 2026-11-03 with `go test -v -count=1 ./...` (3 tests pass; the output is the finding). `engine.go` is a stand-in for Registration and decoding, not nilo-go's code. The design it tests: a route option `Defaults(func(in *In){ in.Query.Limit = 50 })`. Registration runs the function on a zero Input and on a poisoned Input (every field set to an improbable value) and takes a field as defaulted when the two runs leave the same value in it; a field the function sets differently, reads, or derives from another is refused. The decoder starts from the defaulted Input and overwrites what the request sent; the document states `default` for every defaulted field.

## What it does

- **Non-zero defaults** in a query (`PerPage = 30`, `Page = 1`), a header (`Accept-Language = en`) and a create body (`Visibility = "private"`), overridable by the request, and a request that omits a field without a default still gets `required: Name`.
- **Zero defaults**: `Archived = false` is detected as a default because the poisoned run leaves `false` where it would have left `true`.
- **One shared section, different defaults per route**: `Paging{Limit, Offset}` embedded by two Inputs, default limit 20 on one and 50 on the other. This is what a `Default()` method on the type cannot do, and the application it came from needs it: the same bounds with `limit` defaults of 20, 25, 50 and 100 on different routes, and on others absence meaning "every row".
- **Refusals**: a dynamic default (`now-24h`: the two runs differ), a conditional one (`if Sort == "" {...}` reads the Input), one derived from another field (`End = Start + 10`), and an `Opt` given a default (an `Opt` with a default is not optional, it is a plain field).

## What it fails at

- **(a) A static default that is stale.** `From = start of today` is accepted, because both runs agree, and is frozen at boot: the document and the decoder say 2026-11-03 07:00 on the 4th. The refusal catches `now-24h` (it moves between two calls) and not what changes once a day.
- **(b) A type the poison cannot reach.** A `[16]byte` UUID, or an application's own struct, is not set by the poison, so both runs leave the zero value and the field is reported as **defaulted to its zero value, though the function never mentioned it** (`edge_test.go`: `no defaults given -> defaulted=map[Query.OwnerID:[0 ...]]`). A required field has silently become optional with a zero value, which is the very failure [ADR 0041](../../adr/0041-presence-is-a-type.md) exists to prevent. The poison would need a case for every type the codec decodes, including every `TextUnmarshaler` an application owns, and a type it does not know fails open.
- **(c) A zero default that looks redundant and is load-bearing.** Deleting `in.Query.Archived = false` as a no-op turns the field from defaulted to required (`err=required: Archived`). Go's formatters and linters will suggest the deletion. The same line in `Opt.Or(false)` cannot be mistaken.
- **(d) A slice default costs, or aliases.** Copying the defaulted Input per request costs an allocation for the slice (and a deep copy for anything with a pointer inside); not copying shares the slice across requests: with a shallow copy a handler that rewrote `Fields[0]` changed the next request's default (`[password_hash name]` instead of `[id name]`). Either breaks [ADR 0003](../../adr/0003-the-trade-budget.md)'s allocation count for the typed JSON request or ADR 0008's rule that nothing shared reaches user code.
- **The structural cost.** Whether a field may be absent would be decided by a route option, not by the field's type. Two routes sharing one Input type would disagree on whether `limit` is required, and reading the type alone no longer says what a client must send; ADR 0041's principle is that the type says it.

## What it would gain

The document would state `default`. That matters for generators that use it (openapi-generator fills a missing parameter with the default) and is cosmetic for `openapi-typescript`, the client the first port uses, which only reads types. `Descriptions` can state the default in prose today, at the cost of two places that can disagree.

## A `Default()` method on the type, considered and rejected

`func (Paging) Default() Paging` is read by the decoder and the document without a new concept. Rejected: a default belongs to the route and not to the value (the shared `Paging` above has two defaults), the method on a shared type is a global answer to a local question, and a PATCH body has no defaults at all but shares its field types with the create body that does.

## Decision

Not built. The failures (b) and (c) are silent and invert ADR 0041's guarantee, and the gain is cosmetic for the client at hand. Recorded under "Considered" in ADR 0041 and as an open question in `docs/roadmap.md`. What would reopen it: a client generator that needs `default` in the document, and a way to make (b) a Registration refusal (for example, defaults restricted to the field types the codec has a closed list of, with a refusal for any other).

## Update, 2026-11-03

A different design was built the same day: a `Defaults() map[string]string` method on the Section's struct, whose entries are text decoded by the field's own decoder. It answers failures (a) to (d) above by never inferring a default, so a type the poison could not reach is not a case. See [ADR 0041](../../adr/0041-presence-is-a-type.md). The findings above stand for the route-option design they tested.
