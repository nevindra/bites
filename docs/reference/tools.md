# Tools

**The two programs in the repository that build documentation, `bites-doc` for the API document's prose and `bites-site` for the static site, with their flags and what they refuse.**

**Guide:** [doc comments](../guide/doc-comments.md) and [the documentation site](../guide/documentation-site.md) · **Design:** [Documentation tooling](../design/docs-tooling.md)

Both use the standard library only and live in the core module, so `go run github.com/nevindra/bites/cmd/bites-doc` works from any project that requires it, and `go run ./cmd/bites-site` from the repository. They are programs, not packages: no Go API is promised.

### `bites-doc`

```text
bites-doc [-dir d] [-types A,B] [-check]
```

Writes `bites_descriptions_gen.go` in the package directory `-dir` (the current one): for each named, non-generic struct type, exported or not, with a documented, exported field, a `Descriptions() map[string]string` method keyed by Go field name ([ADR 0072](../adr/0072-descriptions-are-generated-from-doc-comments-by-bites-doc.md)). It is meant for a `//go:generate go run github.com/nevindra/bites/cmd/bites-doc` line.

- A description is the comment above a field, or after it on the same line; the lines of a paragraph are joined with one space and paragraphs with a blank line. The text is kept as written.
- Fields of a struct embedded by value from the same package are included in the outer type.
- `-types` limits the types written. `-check` writes nothing and exits 1 when the file on disk is not what would be written.
- A type with a `Descriptions` method written by hand is left alone. A field with `json:"-"`, a commented field of an anonymous struct and a generic type are named on standard error and skipped.
- It exits 1 for a directory with no Go files, with two packages, or that does not parse. When nothing is left to describe, a stale generated file is deleted.

### `bites-site`

```text
bites-site [-src .] [-out site] [-repo-url URL] [-branch main] [-title bites]
```

Renders `README.md`, `GLOSSARY.md` and everything under `docs/` to static HTML in `-out` ([ADR 0073](../adr/0073-the-documentation-is-rendered-to-a-static-site-by-a-program-in-the-repository.md)). Links between pages, layers and ADRs are rewritten and every anchor is checked; it exits 1 and lists each link or anchor that does not resolve. A link to a file that is not documentation points to `-repo-url` on `-branch` when given, and otherwise stays text with its target in a `title`. The site is relative-linked, has one stylesheet and no script, and nothing is published.
