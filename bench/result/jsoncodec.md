# jsoncodec against encoding/json/v2

**Run:** `go test -bench . -benchmem ./internal/jsoncodec/`, three runs, on a Ryzen 7 9700X with the machine otherwise quiet, Go 1.27, on the uncommitted milestone 1 tree of 2026-10-30. v2 decodes into the `reflect.StructOf` wire type of [ADR 0015](../../docs/adr/0015-json-is-our-own-codec-on-jsontext.md), so both sides produce the same wire names. The body is about 90 bytes: three strings, two integers, a nested struct and a list of three items.

| operation | ns/op | B/op | allocs/op |
|---|---|---|---|
| decode, ours, into a value whose list already has capacity | 845 to 875 | 0 | 0 |
| decode, ours, into a fresh value | 895 to 940 | 128 | 1 |
| decode, v2 (includes the `reflect.New` of the wire value) | 1120 to 1180 | 378 | 7 |
| encode, ours, into a reused `[]byte` | 205 to 213 | 0 | 0 |
| encode, v2 `MarshalWrite` | 566 to 577 | 0 | 0 |

**What it decided:** the codec stays on jsontext. Decoding is only about 25% faster than v2, because jsontext's tokenizer is about 700 ns of the 90-byte body; the gain is the allocations (0 or 1 against 7), which is the hard axis. Encoding is about 2.7 times faster. The one allocation of a fresh value is the list's backing array, which is why [ADR 0003](../../docs/adr/0003-the-trade-budget.md) budgets a list.

**Can it go further:** the tokenizer is the floor while the codec reads through jsontext. A scanner of our own would trade the standard library's grammar and UTF-8 checks for speed, which ADR 0015 chose not to do; it is reopened only if a profile of the reference workload shows decoding mattering.

**Differential runs:** encoding was byte-identical to v2 over 8,000 generated values of two types; `FuzzDecode` ran about 150M executions over three runs with no difference left outside three allowed messages (`required`, `must not be null`, `expected one of`), each a case where nilo-go refuses what v2 accepts. The one real difference it found, `time.Time.UnmarshalText` accepting a one-digit hour, is kept as `internal/jsoncodec/testdata/fuzz/FuzzDecode/dbf628ddeece2231`.
