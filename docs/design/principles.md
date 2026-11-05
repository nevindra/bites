# Principles

**The ground rules every other decision of bites stands on: the four principles and their order, independence from nilo, the workload it is judged against, Go 1.27, the standard library only, no global state and versioning before 1.0.**

**Guide:** [configuration](../guide/configuration.md) · **Reference:** [config](../reference/config.md)

## Overview

bites is a Go HTTP framework in which a handler's types are the contract. Seven decisions sit under every other page and are not about one feature: what the framework is for and which concern wins when two pull apart, where the project's ideas come from, what it is measured against, which Go it needs, what its core may import, what state it may keep and how it changes before 1.0.

```text
   AI agent first > safe > covers a lot > fast and low memory (0077)
                          |
                 independent of nilo (0001)
                          |
   reference workload (0002) ----> what is measured and what is built first
                          |
   Go 1.27 (0004)  +  standard library only (0006)  +  no global state (0026)
                          |
              versioning before 1.0 (0034)
```

bites is a framework offered to the public: a JSON API over Postgres with long-lived WebSockets is judged first, what most applications need is built first, and what fewer need follows in order, mostly as modules.

## Rules

1. **Four principles decide every design, and when two pull apart the earlier wins: an AI agent uses it first and a person second; it is safe, so an agent uses it without a hidden trap and debugs it from what it is told; it covers a lot, so many applications are built on it fast; and it is fast and low on memory without chasing the last percent.** Standard library idioms are used when they are what an agent gets right first, not for familiarity, and a departure needs no ADR for being one. Speed ranks last because nilo, in Zig, serves the workloads that need the limit; its numbers are guards held by tests, not goals ([trade budget](trade-budget.md)). Three principles with the hard axes winning, one of them familiarity to a Go developer, were replaced on 2026-11-04. [ADR 0077](../adr/0077-four-principles-in-the-order-they-win.md)

2. **An idea from nilo is argued again in this repository with its own evidence, and nilo's ADRs are never cited as the reason for anything.** Go and Zig force different trades (a garbage collector against an arena, reflection at Registration against the compiler), so bites may decide the opposite where Go makes it the better answer. The module is `github.com/nevindra/bites` and its package `bites`, the name its import path ends in, chosen on 2026-11-04 before the first tag. [ADR 0001](../adr/0001-independent-of-nilo.md)

3. **Every design is judged first against one application shape: a JSON API whose handlers call Postgres, beside WebSocket connections held open for a long time.** It decides what is measured (a handler blocked on a database call, many idle WebSockets) and what goes into the first two milestones. The Postgres driver is the application's choice, not the framework's. [ADR 0002](../adr/0002-the-reference-workload.md)
4. **bites is a framework offered to the public, breadth is a goal, and the work is ordered by how many applications need a feature.** What most need is built first and what fewer need follows in order on [the roadmap](../roadmap.md#the-definition-of-finished), each with a trigger that moves it forward, not one it waits for; a feature with a dependency or for fewer applications is a module. A design is argued from the general case, never from one application's port. Scope set by the maintainer's internal tools was rejected because the work began to fit one application; waiting for each feature's trigger and refusing parity outright was rejected on 2026-11-04, when breadth was ranked above speed. [ADR 0002](../adr/0002-the-reference-workload.md)

5. **Go 1.27 is the minimum.** It gives generic methods, so a route is registered as `app.Get("/users/{id}", users.Get)` with its Input and output types inferred, and a stable `encoding/json/v2`. An older Go would need a package-level function in place of the method and a JSON layer that behaves two ways. [ADR 0004](../adr/0004-go-1-27-minimum.md)
6. **The core imports the standard library and nothing else.** A dependency is a release the project does not control, and Gin's hello world links several modules it does not need ([`docs/research/gin-echo/`](../research/gin-echo/report.md)). A feature that needs a dependency lives in a separate module, or gets an ADR of its own saying what it costs. A test reads the core's imports ([ADR 0033](../adr/0033-the-rules-are-build-steps.md)). [ADR 0006](../adr/0006-no-third-party-dependency-in-the-core.md)

7. **There is no package-level mutable variable, no environment variable and no debug mode.** Everything is configured on an app instance, logging goes through the `*slog.Logger` it was given, and at start-up the app logs its effective settings, each marked `default` or `custom`. A warning that matters in development matters in production. The configuration module extends the rule: it reads the environment, the arguments and the files only as values the application passes in ([ADR 0080](../adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md)). [ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md)
8. **Before 1.0 a breaking change is made when it buys a cleaner design, and it says what a user has to change.** The module stays at v0.x with no promise of stability, there is no changelog before the first tag and from it each release page says what a user has to change, a version bump follows the size of the change and not the word used for it, and a tag is created only when the maintainer asks, with the exact version stated first. [ADR 0034](../adr/0034-versioning-before-1-0.md)

9. **A feature with a dependency, or that most applications do not need, is a module under `modules/` with its own `go.mod`.** It builds against the core beside it through a `replace` until a release, uses the core's exported API only and is tested with its own `go test`; the core never imports it. [ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0077](../adr/0077-four-principles-in-the-order-they-win.md) | four principles in the order they win: AI agent first, safe, covers a lot, fast and low memory without chasing the last percent |
| [0001](../adr/0001-independent-of-nilo.md) | bites borrows nilo's central idea and none of its decisions; the name is bites, and why the others lost |
| [0002](../adr/0002-the-reference-workload.md) | the reference workload, a JSON API over Postgres with WebSockets, decides order and measurement; bites is a framework offered to the public, breadth is a goal, and what most applications need is built first |
| [0004](../adr/0004-go-1-27-minimum.md) | Go 1.27 is the minimum, for generic methods and `encoding/json/v2` |
| [0006](../adr/0006-no-third-party-dependency-in-the-core.md) | the core imports the standard library only |
| [0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md) | optional features are modules under `modules/`, each with its own `go.mod` |
| [0080](../adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md) | configuration is the module `modules/config`, which fills a typed struct from flags, the environment and files, reads nothing by itself, and refuses every wrong setting at start-up |
| [0026](../adr/0026-no-global-state-and-one-behaviour.md) | no package-level state, no environment variables, one behaviour in every build |
| [0034](../adr/0034-versioning-before-1-0.md) | breaking changes are allowed before 1.0; no changelog before the first tag, and a release page says what to change |

## Open questions

- **When bites stops.** The README calls it a bridge; no measurable condition is fixed in advance (the maintainer's decision of 2026-11-02): if it stops, an ADR says so with the evidence of the time.
