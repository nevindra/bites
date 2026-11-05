# Documentation tooling

**How the documentation is organised in three layers and held true by tests: guide, reference and design, and what `go test` checks about them.**

**Guide:** none · **Reference:** none

## Overview

Each topic is written up to three times for three questions: how do I do this (the guide, one page per task), what exactly is this name (the reference, one page per area with a heading for every exported name) and why does it work this way (the design page, one page per topic, joining its ADRs). `docs/README.md` is the map, and the ADRs stay the rule in force.

```text
docs/README.md          the map: one row per guide page, with its reference and design pages
docs/guide/*.md         how                       worked examples, in reading order
docs/reference/*.md     what                      a heading is the exported name: ### `App.Get` -> #appget
docs/design/*.md        why                       Overview, Rules, Decisions, Open questions; links every ADR of the topic
docs/adr/*.md           the evidence              edited in place by the change that alters the rule

docs_test.go (every go test):
  five-line head | one paragraph a line, no em dash | links and anchors resolve | map links every page
  every ADR on a design page | every exported name has its heading | every `compiles` Go block compiles, and an example test runs
```

The checks see structure, links, names and compilation, not truth: a change that alters behaviour still edits its guide and reference pages by hand, as it edits its ADR.

## Rules

1. **The documentation is three layers in three folders, and the map joins them.** Guide, reference and design answer different questions, so each page is written for one; a design page is where a reader starts and an ADR is where the evidence is. [ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)

2. **Every page opens with the same five lines.** The title, one bold sentence saying what the page is, and on line 5 the same topic in the other two layers (a link or `none`). A reference heading is the name itself in code, so its anchor is predictable and a guide page can link a name without opening the reference. [ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)
3. **A design page has the sections Overview, Rules, Decisions and Open questions, and every ADR is linked from one.** No decision is left out of the topic pages, and a new ADR fails the build until a page joins it. [ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)

4. **Tests hold the rules on every `go test`:** the head, one paragraph a line with no em dash, every relative link and anchor, the map, every ADR on a design page, every exported name of `bites`, `bitetest` and `stdhttp` with its reference heading, the reference's list of headings, and every Go block marked `compiles` compiled and vetted against the module. `-short` skips the compile check because it runs the `go` command. [ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)
5. **Documentation is a build step like the other rules.** It sits beside the allocation budgets, the refusal table, the fuzzers and `go vet` as one of the checks that fail from the first commit of code. [ADR 0033](../adr/0033-the-rules-are-build-steps.md)

6. **Doc comments stay and the reference links the code.** `go doc` and pkg.go.dev order names alphabetically and cannot group a name with its task or name its ADR, but they are what an editor shows, so the reference does not replace them. A type alias shows only its own comment there, so that comment names each method and field of the type it stands for, held by a test. A published site waits for the first release. [ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)

7. **The three layers are rendered to a static site by `cmd/bites-site`, with the standard library only.** Links between pages, layers and ADRs are rewritten and every anchor checked, a broken one fails the build, and nothing is published by the tool. [ADR 0073](../adr/0073-the-documentation-is-rendered-to-a-static-site-by-a-program-in-the-repository.md)

8. **Prose for the API document is written once, in the doc comment of a field, and `bites-doc` generates the `Descriptions` method.** The Registration check holds the result against the wire fields. [ADR 0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md) | the three layers, the five-line head, the design page sections, and the tests that hold them |
| [0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md) | the doc-comment generator `bites-doc` |
| [0073](../adr/0073-the-documentation-is-rendered-to-a-static-site-by-a-program-in-the-repository.md) | the static site generator `bites-site` |

## Open questions

- **The checks cannot see truth.** A page can describe a behaviour wrongly and still pass; the em dash and paragraph checks are lexical and can be fooled by an unusual line; a snippet in a body block has to use what it declares ([ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)).
- **Where the site is published is not decided.** `bites-site` builds it ([ADR 0073](../adr/0073-the-documentation-is-rendered-to-a-static-site-by-a-program-in-the-repository.md)); the host, the domain and a copy per release wait for the first release, and the site has no search and no highlighting.
- **The doc-comment generator** is built ([ADR 0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md)) and describes fields only.
- **A habit for guide and reference** equal to the one ADRs have, that every change edits them, has tests only for their structure ([ADR 0033](../adr/0033-the-rules-are-build-steps.md)).
