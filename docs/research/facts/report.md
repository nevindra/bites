# Go 1.27.2 facts for the framework ADRs

Toolchain: go1.27.2 linux/amd64 (the installed 1.27.x). Each folder is a runnable probe (`go run .`, or `go test -bench` in q3). Build cache was kept in the scratchpad `facts-bin`. "probe" = ran it; "src" = read in `$GOROOT/src`.

Note on json/v2: `encoding/json/v2` needs no GOEXPERIMENT in 1.27.2. The files carry `//go:build goexperiment.jsonv2`, but `JSONv2: true` is in the baseline (src/internal/buildcfg/exp.go:87). Setting `GOEXPERIMENT=nojsonv2` would remove it. Under this baseline, v1 `encoding/json` is implemented on top of v2 (src/encoding/json/v2_decode.go, v2_encode.go).

## 1. Naming policy in json/v2 (q1-naming)
No option applies a naming policy. The exported options (src/encoding/json/v2/options.go) are StringifyNumbers, Deterministic, FormatNilSliceAsNull, FormatNilMapAsNull, OmitZeroStructFields, MatchCaseInsensitiveNames, RejectUnknownMembers, WithMarshalers, WithUnmarshalers (plus format options in other files). Grep for snake/naming/namer finds nothing. Default marshal of `PageSize` gives `"PageSize"` (probe).
- Decode-only partial help: `MatchCaseInsensitiveNames(true)` folds case and ignores `_` and `-` (fold.go:14-33), so `page_size` fills `PageSize` on input (probe). It does nothing on output.
- `WithMarshalers(MarshalToFunc[any](...))` is called for every value as a pointer (`*In`, `*int`, `*string`, `*[]Item`...) (probe). Returning `errors.ErrUnsupported` falls through to the default (arshal_funcs.go:233-244). There is no `SkipFunc` in 1.27. A hook can only rewrite a whole value itself; it cannot rename members of the default struct encoding.
- Working mechanism (probe): build once per type, at registration, a wire type with `reflect.StructOf`, recursing into struct, slice and pointer element types, setting `json:"snake"` tags. The layout is identical, so `reflect.NewAt(wireType, unsafe.Pointer(&in))` gives a pointer json can use directly. Marshal, Unmarshal and `RejectUnknownMembers` all worked, with nested slices and pointers. Cost: rebuilding is one-off; the unsafe cast is sound only because field types and order match. Limits seen or expected: types with their own methods (time.Time, MarshalJSON types) must not be rebuilt; embedded fields and unexported fields need care in `StructOf`; a user's explicit `json` tag must be kept (ADR 0011 says it wins); error text then prints the wire Go type with tags (`Go struct { ItemID int "json:\"item_i_d\"" }`), so errors must be built from the JSON Pointer, not from `err.Error()`. A naive snake function turned `ItemID` into `item_i_d`; the policy needs an acronym rule.
- ADR 0011: the policy cannot be an option. It has to be a registration-time generated wire type (or our own encoder); say that and say that the policy must handle acronyms.

## 2. Error reporting (q2-errors)
- Stops at the first error. Input `{"name":5,"age":"x","tags":[1,"b"],"extra":1}` returned one error, at `/name`, and `Age` and `Tags` stayed zero. With a valid `name`, then bad `age`, the result was one error at `/age`, `Name` filled, nothing after it. Cause: semantic errors are fatal unless the legacy flag is set (src/encoding/json/v2/errors.go:62-66).
- Type: `*json.SemanticError` with `ByteOffset`, `JSONPointer` (RFC 6901 string such as `/age`, `/extra`), `JSONKind`, `JSONValue`, `GoType`, `Err` (errors.go:76-99). Pointer segments are JSON member names (wire names, taken from tags), not Go field paths. A syntax error is `*jsontext.SyntacticError` (`jsontext: unexpected EOF after offset 12`).
- `RejectUnknownMembers(true)`: error `json: cannot unmarshal JSON string into Go main.In: unknown object member name "extra"`, `JSONPointer="/extra"`, `Err=json.ErrUnknownName`-like value ("unknown object member name"). Without the option unknown members are silently skipped (probe). Only the first unknown member is reported.
- Needed for "every error at once" (ADR 0012): json can only give the first error, so the framework must either decode with `jsontext.Decoder` tokens and its own value conversion, or retry and skip the failed member. Neither is free. Also note the `RejectUnknownMembers` error is for the first unknown only.

## 3. JSON allocations (q3-json-allocs, incl. retained/)
Struct of 3 strings and 2 ints, `[]byte` input.
- Repeated identical document: v2 Unmarshal 0 allocs, v1 0 allocs. This is misleading: the decoder interns strings of 2 to 256 bytes in a 256-entry cache (src/encoding/json/v2/intern.go:12-52), so identical values are shared.
- Distinct values in each document (retained/ probe, 10,000 documents): v2 3.01 allocs and 49 B per document, v1 3.00 and 49 B. So one allocation per decoded string, none for ints, none for the struct if the caller owns it. Strings do not alias the input (checked by pointer range).
- `go test -bench` with `var s S` escaping per iteration: 1 alloc, 64 B for both (the struct itself, as `&s` goes to `any`).
- Marshal to reused `bytes.Buffer`: `MarshalWrite(&buf, &v)` 0 allocs; `MarshalWrite(&buf, v)` by value 2 allocs (boxing). v1 `Marshal(&v)` 1 alloc (the result slice); v1 Encoder to reused buffer 0.
- ADR 0008, 0003: copying the body once per request and pointing strings into it is cheaper in allocations than json's per-string copy (3 vs 1) only if we decode ourselves. json/v2 gives 1 per string. The string cache also shares equal strings across requests: harmless for immutable strings, but it means json-decoded strings are not "request-private".

## 4. Context allocations (q4-context-allocs)
`testing.AllocsPerRun`, including cancel:
- `WithCancel(Background)`: 2 (cancelCtx plus the cancel closure). Same with a cancelCtx parent: 2. If `Done()` is called: 3 (the channel is lazy, cancelCtx.Done at context.go:449).
- `WithCancelCause`: 2. `AfterFunc` with either parent and then stop: 2.
- Custom ctx with lazy Done channel: 1 (itself), 2 once `Done()` is called.
- `WithCancel(customParent)`: 4. A parent that is not a std cancelCtx and has no `AfterFunc` method starts a goroutine (context.go:508-523). Give a custom ctx an `AfterFunc(func()) func() bool` method (the `afterFuncer` interface, context.go:343) to avoid that.
- ADR 0016: a custom ctx with a lazily created Done and a cancel method costs 1 allocation per request, 2 if the handler touches `Done()`, against 2 to 3 for `WithCancel`. Anything derived with `WithTimeout`/`WithCancel` from it must implement `afterFuncer` or each derivation spawns a goroutine.

## 5. Typed handler and escaping (q5-handler-escape)
Generic registration returns a closure `func(ctx, body) error`; `in` is a local `var in I`. AllocsPerRun and `-gcflags=-m`:
- E1 `var in I; json.Unmarshal(body, &in)`: 2 allocs; `-m` says `moved to heap: in` (main.go:22). One alloc is the struct, one the Name string.
- E2 `var in I; reflect.ValueOf(&in).Elem().Field(1).SetInt(...)`: 0 allocs, `in` stays on the stack (no `moved to heap` for it).
- E3 `reflect.New(t)` plus json: 2 allocs. E4 `reflect.New(t)` plus reflect set: 1 alloc. `reflect.New` always heap-allocates.
- So: the escape comes from passing `&in` as `any` to a non-inlined function (json), not from reflect itself. Filling a stack `in` through `reflect.ValueOf(&in).Elem()` does not escape in this probe, provided no callee in the chain leaks the pointer. A decoder of our own that works through reflect.Value (not `any`) can keep `in` on the stack; json/v2 Unmarshal cannot.
- ADR 0003, 0008: with json/v2, expect 1 heap alloc for the input struct plus one per string; with an own decoder, per-string only (and zero if the strings point into the request buffer).

## 6. unsafe.String into a per-request []byte (q6-unsafe-string)
- Probe: 100 buffers, each with a finalizer on `&buf[0]`, only a string from `unsafe.String(&buf[0], n)` kept. After 5 rounds of GC and 200k allocations: 0 of 100 strings corrupted, 0 of 100 buffers finalized. Control: after dropping the strings, 100 of 100 finalized (so the probe can detect collection).
- Why: the string header holds an ordinary pointer, and the GC treats it as a reference into the object. Real use in std: `strings.Builder.String` returns `unsafe.String(unsafe.SliceData(b.buf), len(b.buf))` (src/strings/builder.go:47).
- Documentation (src/unsafe/unsafe.go:253-264): `unsafe.String` documents only the panic conditions and that "the bytes passed to String must not be modified as long as the returned string value exists". It does not say in words that the GC keeps the array alive; that follows from the pointer being a normal one. The condition for ADR 0008 is therefore: never write to the buffer after creating strings from it, never put it back in a pool.

## 7. UUID (src/uuid)
Yes: new package `uuid` in 1.27 (src/uuid/uuid.go, "Copyright 2026"; api/go1.27.txt lines 305-314, issue #62026). `type UUID [16]byte`; `New` (v4 random), `NewV4`, `NewV7`, `Parse`, `MustParse`, `Nil`, `Max`, `String`, `MarshalText`, `AppendText`, `UnmarshalText`, `Compare`. It is a comparable array, and has text marshalling, so json/v2 writes it as a string with no extra code. The release-notes fragment dir (doc/initial/6-stdlib/99-minor) was empty in this install, so the API file and source are the evidence.

## 8. ServeMux (q8-servemux, httptest, status codes)
(a) Pattern `/static/` matches `/static/` and everything under it: 200. `/static` gives 307 to `/static/`. `/static/../x` gives 307 to `/x` (cleaned). `/statics`: 404. With `/{$}` plus `/api/`: `/` 200 root, `/api/x` 200, `/api` 307, `/other` 404.
(b) `/files/{name}`: `/files/a` 200 `name=a`. `/files/a%2Fb` matches, 200, `name="a/b"` (value decoded, `r.URL.RawPath="/files/a%2Fb"`). `/files/a/b` 404. `/files/a%20b` 200 `name="a b"`. `/files/` 404. `/files/%2e%2e` 200 with `name=".."` (no traversal protection). With `/files/{name...}`: `/files/a%2Fb/c` gives `name="a/b/c"`, so an encoded slash is indistinguishable from a real one there.
(c) `example.com/x` plus `/x`: Host `example.com` and `example.com:8080` hit the host pattern; `other.com` and `EXAMPLE.com` (uppercase) hit `/x`, so host comparison is case-sensitive. A host-only pattern with no generic fallback gives 404 for other hosts.
(d) `/p` plus `GET /p`: GET and HEAD run the GET handler; POST and DELETE run the method-less one (200). Only method patterns (`GET /q`, `POST /q`): GET/HEAD/POST 200; PUT and OPTIONS give 405 with `Allow: GET, HEAD, POST`. Unknown path: 404. Conflicts panic at registration naming both patterns and the file:line (`/a/{x}/b` vs `/a/b/{y}`; duplicate `GET /z`).
- ADR 0018: all four rules match the ADR text. Additions to specify: a method-less pattern is a fallback for methods not otherwise registered (no 405 on that path); `{name}` matches the decoded segment, so `%2F` yields a value containing `/` and `%2e%2e` yields `..`; host matching is case-sensitive.
