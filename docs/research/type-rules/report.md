# Rules a type carries: what Length, Range and Check cost

Taken on 2026-11-02, Go 1.27.0 darwin/arm64, Apple M1 Pro (8 performance and efficiency cores), macOS. The code measured is `3460aab` plus the uncommitted tree that adds `Length`, `Range` and `Check` (ADR 0021) and removes `Bounded`. This is a laptop, not the reference machine of ADR 0002: the allocation counts carry over, the nanoseconds are a ratio within one run and nothing more. No throughput or p99 over TCP was measured.

## What was run

- **Allocations per request** are the root package's budget tests over the in-memory connection, `go test -count=1 -run 'OwnTypesCarry|ValidateFails|ConstraintTypes' -v .`: `TestATypedJSONPOSTWhoseOwnTypesCarryLengthRangeAndCheckStillAllocatesTwice` sends the typed POST of the 2-allocation row with a `Range` on the path id and the page, a `Length` on the name and a `Check` on the email, once with a body the fast path decodes and once with an escape that sends it to the tokenizer.
- **Nanoseconds per body** are `BenchmarkAFlatBodyWithAPlainField` and `BenchmarkAFlatBodyWithACheckedField` in `internal/jsoncodec/fast_test.go`: one two-member body (`{"name":"Ada Lovelace","mail":"ada@example.com"}`) decoded on the fast path into a struct whose second field is a `string`, and into one whose second field is a named string with a `Check` that looks for an `@`. Run five times, the two in turn: `go test -run '^$' -bench 'AFlatBodyWithAPlainField$' -count=1 ./internal/jsoncodec/`, then the same for the checked one.

## Allocations

| Request | allocs/request |
|---|---|
| typed POST, constraint types and a passing `Validate` (the existing row) | 2 |
| typed POST, `Range`, `Length` and `Check`, fast path | 2 |
| the same body with an escape, through the tokenizer | 2 |
| a `Check` that fails in the body | 4 |
| a wrong type in the same field (the baseline for a decoding error) | 4 |

## Nanoseconds per body (five interleaved runs)

| Body | ns/op | allocs |
|---|---|---|
| plain `string` field | 50.30, 50.81, 51.05, 51.52, 52.69 | 0 |
| field with a `Check` | 63.09, 64.12, 64.23, 64.52, 65.42 | 0 |

The `Check` adds 12.6 to 13.1 ns a value, the work of the check itself (an `IndexByte` over 15 bytes) included. A first session before the pointer was hidden from escape analysis gave 62.3 to 63.4 ns; the `noescape` call costs no measurable time.

## What the first build got wrong

The first build called the type's `Check` through a `func(unsafe.Pointer) error` with the field's pointer. An indirect call makes its argument escape, so a caller that decodes into a value on its stack had that value moved to the heap: `TestAFreshStackValueCostsOneAllocationForItsSlice` went from 1 to 2 allocations and `TestTheFastPathAllocatesNothing` from 0 to 1. The engine's request did not show it (its Input is reached through the stack device of ADR 0023), which is why the package tests and not the budget test caught it. The pointer is now passed through `noescape`, as the decoder already does for `reflect`, and that is true only if `Check` cannot keep the pointer, so a `Check` with a pointer receiver is refused at Registration.

## The probe before the build

Before anything was built, a standalone program checked that calling a value's method through `reflect.NewAt(t, p).Interface().(interface{ Check() error })` allocates nothing when `p` points into a struct: `testing.AllocsPerRun` gave 0. That probe is not kept; the benchmark above measures the same call inside the decoder.
