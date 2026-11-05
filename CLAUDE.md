# CLAUDE.md

bites is a Go 1.27 HTTP framework in which a handler's types are the contract: one Input struct becomes routing, decoding, a 400 that names the field, and the OpenAPI document, and a mistake in those types is **refused at Registration**, before the first request. It is being built, with no milestones: what is left is built in the order `docs/roadmap.md` gives, Important first, then Fairly important, then Nice to have.

## The four principles

[ADR 0077](docs/adr/0077-four-principles-in-the-order-they-win.md) decides them and the README states them for users ([Principles](README.md#principles)); this is what each asks of a change. **They are ranked: when two pull apart, the earlier one wins.** Every agent working in this repository follows them.

1. **AI agent first, human DX second.** An agent that sees only the signatures, the doc comments and the refusals must use an API correctly on the first try. A new exported name or field gets a doc comment saying what it holds and what its zero value does. A misuse that can be caught is a refusal whose message says what to change, not only what is wrong. There is one way to do a thing: a second spelling of an existing feature needs an ADR saying why. Where what reads best to a person and what an agent gets right differ, the agent wins. Standard library types and idioms are used when they are what an agent gets right first, not because they are familiar; a new idiom that serves an agent better needs no ADR for being a departure.
2. **Safe: easy for an agent to use and to debug.** No hidden trap: a mistake is refused at Registration, a client error names the field, limits are on by default, nothing pooled reaches user code. Nothing is skipped, defaulted or ignored in silence; when the framework does something the code did not say, it says so where an agent will read it (the refusal, the problem+json, the log, the span). A failure at run time says what failed and why, so an agent debugs from the output without reading the framework's source.
3. **Cover a lot, so applications are built fast.** Breadth is a goal: what Go frameworks commonly ship belongs on the roadmap, built in order of how many applications need it, and no feature is refused for scope alone. That includes what microservices need (configuration, discovery, gRPC, message consumers), so bites is ready when an application is split into services. The core stays standard-library-only; a feature with a dependency or for fewer applications is a module under `modules/` ([ADR 0070](docs/adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)). Breadth never buys a second way to do a thing or a feature that cannot be made safe.
4. **Fast and low memory, not chasing the last percent.** nilo (Zig) serves the workloads that need the limit, so bites does not push past it. The numbers in [ADR 0003](docs/adr/0003-the-trade-budget.md) are guards held by tests: none rises by accident, and a change that raises one states the new number and why in its row. No work goes into going below them until an application's load asks. What is refused is bloat: a cost a feature does not need, or one that grows with something a client controls.

## Where the truth lives

- **`docs/README.md`**: the map of the documentation, three layers held by `docs_test.go` ([ADR 0050](docs/adr/0050-the-documentation-is-three-layers-held-by-tests.md)): `docs/guide/` (how to do a task), `docs/reference/` (every exported name, a heading each), `docs/design/` (one page a topic, joining its ADRs). Start a question about the API at the guide or reference, and a question about why at the design page.
- **`docs/adr/`**: every decision, and the rule in force. Read the ADRs a change touches before proposing it; "why not X?" usually has its answer under "Considered" or "What was rejected".
- **`GLOSSARY.md`**: the words this project uses, and under _Avoid_ the ones it refuses. Use its words in code, comments, docs and commit messages (Input, Section, Provider, Registration, Socket, Topic).
- **`docs/research/`**: the evidence behind the ADRs: probes, measurements and the programs that took them. Cite it; rerun a probe rather than recall a fact about Go or another framework.
- **`docs/roadmap.md`**: what is not built yet, in three tiers by importance, what the first tag waits for, the definition of finished (what must ship before bites is called finished, and the few optimisations that wait for a trigger), the not-goals (only those a principle still refuses), what was reopened and left unplaced, and the questions still open.

## The rules a change is held to

These are the ADRs most changes run into; each is enforced by a test or a refusal once code exists ([ADR 0033](docs/adr/0033-the-rules-are-build-steps.md)).

- **Two guarded axes**: allocations per request (2 for a typed JSON request, 3 when its context's done channel is used) and memory per idle connection, each held by a test. Either rises only when the change states the new number, measured, and why, in its row of ADR 0003. Throughput and p99 may give up to 10% for a feature or clearly better use; past that the row says so and why ([ADR 0003](docs/adr/0003-the-trade-budget.md)).
- **Nothing pooled reaches user code**: an Input, a string in it, a body slice, a context, a Socket ([ADR 0008](docs/adr/0008-no-pooled-object-reaches-the-user.md)). The engine pools only what the user never sees.
- **The response comes only from the return value**; a handler never holds a writer ([ADR 0009](docs/adr/0009-the-response-comes-only-from-the-return-value.md)).
- **The core imports the standard library only, and never `net/http`** ([ADR 0006](docs/adr/0006-no-third-party-dependency-in-the-core.md), [ADR 0019](docs/adr/0019-plain-http-handlers-are-a-slow-path.md)).
- **A refusal is output**: each Registration mistake panics with an exact message naming the route, the field and where it was registered, and has a row in the refusal table ([ADR 0007](docs/adr/0007-mistakes-are-refused-at-registration.md)).
- **Every error answer is problem+json** with wire names ([ADR 0012](docs/adr/0012-client-errors-are-400-problem-json.md)); the one exception is a `Page` a handler returns, HTML for a browser with a 4xx, and the `Submitted[T]` form whose mistakes it shows ([ADR 0078](docs/adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md)).

## How a change is shaped

A change is done when it carries all four:

1. **The axis it spends, and the number**: which of the four axes of ADR 0003 it moves, measured, or "none" with the reason.
2. **Its refusals**: every new way to misuse it is a Registration panic with a table row, or a stated reason it cannot be caught before the first request.
3. **Its tests**, in the file they test, named as sentences about behaviour (`"a path param that is not a number is a 400 naming path.id"`), run with and without `-race`, through the in-memory connections where possible ([ADR 0027](docs/adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)).
4. **Its documentation**: the ADR it changes, edited in place; the glossary, for any new word; the guide and reference pages the change touches (a new exported name fails `go test` until it has its reference heading), and the design page of its topic when an ADR is added or its rule changes.

## Decisions are recorded as they are made

- **A revision edits its ADR in place.** The Decision says the rule as it holds now; the position it replaced moves under "What was rejected" with the evidence that moved it. A new number is only for a decision that did not exist before.
- **An ADR is argued from this repository's evidence.** nilo is a source of ideas, never a reason: an idea taken from nilo is argued again here, and nilo's ADRs are not cited as authority ([ADR 0001](docs/adr/0001-independent-of-nilo.md)).
- **A design is stress-tested before it is recommended**: put it against concrete scenarios (the reference workload of [ADR 0002](docs/adr/0002-the-reference-workload.md), a hostile client, the ADRs it touches), and say what it fails at alongside what it solves.
- **Markdown is one paragraph per line**, never hard-wrapped.

## Measuring

A number in a document has a run behind it, written under `docs/research/` or `bench/result/`: what was run, on which machine, at which commit, with its spread. Without that it is written as a claim. The method that held up in `docs/research/engines/` and `ws-idle/`:

- **Pin the server and the client to separate physical cores** (`lscpu -e`; on the Ryzen 7 9700X the server took 2,3,10,11 and the client 4-7,12-15).
- **Interleave runs** of the things compared, at least three, and quote the spread; a margin inside the spread is "unchanged".
- **Per-connection memory is the marginal cost** from 10k to 20k connections, as RSS and as heap plus stack from `runtime/metrics`.
- **Spread client connections over source addresses** 127.0.0.2 to 127.0.0.5, or loopback runs out of ports in TIME_WAIT.
- **Build the before** from the same commit and flags in the same session, never quote it.

## Gotchas

- **`/tmp` is a tmpfs emptied on reboot.** A measurement or probe that should survive is written under `docs/research/`, with binaries and build caches kept outside the repository.
- **Every folder under `docs/research/` with Go code is its own module**, so `go test ./...` at the root never builds a probe. Keep it that way: a new probe gets its own `go.mod`.
- **The Go toolchain auto-switches** (`GOTOOLCHAIN=auto`): a dependency asking for a newer Go than the installed one downloads it, which is how a research run ended up on 1.27.3.
