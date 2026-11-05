# The trade budget

**How bites spends performance: two guarded axes, two soft ones, the rule that nothing pooled reaches user code, and the tests that hold the rules.**

**Guide:** none · **Reference:** none

## Overview

Performance is four axes and they are not traded alike. Allocations per request and memory per idle connection are guarded: each number is held by a test, and a change that raises one states the new number and why in its row of ADR 0003. They are guards, not goals: speed ranks last of the four principles ([ADR 0077](../adr/0077-four-principles-in-the-order-they-win.md)), so no work goes into going below them until an application's load asks. Throughput and p99 latency are soft: a feature, or a change that is clearly easier or safer to use, may give up to 10% of either, measured. Binary size is recorded.

```text
guard  allocations per request        one row per shape of request, each a test
guard  memory per idle connection     a gate per kind of connection, each a test
soft   throughput                     may give up to 10% for a feature or better use
soft   p99 latency                    may give up to 10% for a feature or better use
noted  binary size                    a change says by how much it adds
```

The allocation budget is a table with a row for each shape of request, because each feature on the path has a floor that was measured. A number in a document has a run behind it in `bench/result/` or `docs/research/`, or it is written as a claim.

## Rules

1. **Allocations per request and memory per idle connection are guarded by tests: either rises only when the change states the new number, measured, and why it is worth it, in its row of ADR 0003; going below them waits for an application that needs it.** They compound with load and connection count, and in Go an allocation is paid twice, once to make it and once in the collector. The budget is stated per shape of request in the table of [ADR 0003](../adr/0003-the-trade-budget.md), and a feature that adds a row says what it costs there. [ADR 0003](../adr/0003-the-trade-budget.md)

2. **Throughput and p99 latency may give up to 10% for a feature or a change that makes the framework clearly easier or safer to use, and a number past that is written down as outside the budget.** Several rows of the table state a p99 rise past 10% with the cause, or "not found", rather than hide it. [ADR 0003](../adr/0003-the-trade-budget.md)
3. **A number in a document has a run behind it.** The run says what was run, on which machine, at which commit, with its spread; a number without one is a claim and is written as one. [ADR 0003](../adr/0003-the-trade-budget.md)

4. **Nothing a handler can hold is reused for another request.** The Input, a string in it, a body slice and the context are never pooled: text read from the request is copied once into a buffer allocated for that request, and the strings of the Input point into it, so the collector keeps it for as long as anything refers to it. Gin, Echo and Fiber hand a goroutine that outlives its handler the next request's data and `-race` does not report it ([`docs/research/gin-echo/`](../research/gin-echo/report.md)). [ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)
5. **The engine may pool what the user never sees.** Read buffers, parser state, the gzip writer and the chain's state are pooled; a convenience or a speed-up that needs a pooled object to reach the user does not ship. [ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)

6. **Every rule that matters is a test that fails, from the first commit of code.** The allocation budgets use `testing.AllocsPerRun` through the in-memory connections, the idle-memory gates read heap plus stack from `runtime/metrics`, every refusal has a table row, `-race` runs in CI, the HTTP/1.1 parser and the JSON codec are fuzzed against net/http and `encoding/json/v2`, the core never imports `net/http`, and the documentation is checked by `docs_test.go`. [ADR 0033](../adr/0033-the-rules-are-build-steps.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0003](../adr/0003-the-trade-budget.md) | two guarded axes, two soft ones within 10%, and the allocation budget row by row |
| [0008](../adr/0008-no-pooled-object-reaches-the-user.md) | nothing pooled or reused reaches the user's code |
| [0033](../adr/0033-the-rules-are-build-steps.md) | the rules are tests and build steps that fail from the first commit of code |

## Open questions

- **Soft axes not measured.** Throughput and p99 of an upload, of a stream, of TLS, and of gzip over TCP are not claimed yet ([ADR 0003](../adr/0003-the-trade-budget.md), [roadmap](../roadmap.md#nice-to-have-what-few-applications-need)).
- **The `File` answer's allocations.** It is measured, not designed, and its test is a ceiling because two runs differed for a cause not traced ([ADR 0003](../adr/0003-the-trade-budget.md)).
- **Parking costs a wake.** A request that finds its connection parked raises p99 past the 10%, accepted for the memory axis ([ADR 0003](../adr/0003-the-trade-budget.md), [`docs/research/parking/`](../research/parking/report.md)).
- **Binary size** is recorded but has no gate ([ADR 0003](../adr/0003-the-trade-budget.md)).
