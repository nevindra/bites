# The rules that matter are tests that fail, from the first commit of code

These checks run on every change, so the rules in these ADRs do not depend on anyone remembering them:

1. **Allocation budgets**: `testing.AllocsPerRun` on each budgeted row of [ADR 0003](0003-the-trade-budget.md), through the in-memory connections of [ADR 0027](0027-an-in-memory-entry-point-runs-the-engine-path.md), failing when a count rises.
2. **Idle memory**: 2,000 loopback connections held idle, and the marginal heap plus stack per connection read from `runtime/metrics`, failing above its budget; once for a plain connection and once for a WebSocket. RSS is too noisy on a CI machine; heap plus stack was stable to the byte in the research runs. Kernel memory and RSS stay the job of a run in `bench/result/` before each release.
3. **A refusal table**: every mistake [ADR 0007](0007-mistakes-are-refused-at-registration.md) refuses has a case that must panic with exactly the message written for it, so an error message is tested like any other output.
4. **`go test -race`** in CI.
5. **Differential fuzzing of the HTTP/1.1 parser** against net/http's, plus a corpus of known smuggling inputs; the policy is [ADR 0045](0045-http1-strictness.md), and the fuzzer finds where we and net/http disagree, not what is right.
6. **Differential fuzzing of the JSON codec** against `encoding/json/v2` ([ADR 0015](0015-json-is-our-own-codec-on-jsontext.md)).
7. **The core never imports net/http** ([ADR 0019](0019-plain-http-handlers-are-a-slow-path.md)): a test reads the core package's dependencies.
8. **`go vet` and staticcheck**, staticcheck run as a tool and never imported.
9. **The documentation** ([ADR 0050](0050-the-documentation-is-three-layers-held-by-tests.md)): every page's five-line head, one paragraph a line with no em dash, every relative link and anchor resolved, every page on the map, every ADR on a design page, every exported name with its reference heading, and every Go block marked `compiles` compiled, in `docs_test.go`.
