# Building the documentation site

**`go run ./cmd/bites-site -out DIR` renders the guide, the reference, the design pages, the ADRs and the roadmap to static HTML that works from any path, checks every link, and publishes nothing.**

**Reference:** [`bites-site`](../reference/tools.md#bites-site) · **Design:** [Documentation tooling](../design/docs-tooling.md)

The documentation is Markdown in three layers ([ADR 0050](../adr/0050-the-documentation-is-three-layers-held-by-tests.md)). The site is the same files, rendered, with the layers linked as they are on disk ([ADR 0073](../adr/0073-the-documentation-is-rendered-to-a-static-site-by-a-program-in-the-repository.md)). It uses the standard library only, so there is nothing to install.

## Build it

From the repository root:

```sh
go run ./cmd/bites-site -out /tmp/bites-site
```

It writes one page for every Markdown file of `docs/`, plus `README.md` as `index.html` and `GLOSSARY.md`, and a stylesheet in `assets/`. Open `/tmp/bites-site/index.html` in a browser, or serve the folder with any static file server. Links are relative, so the folder can be moved or put under a path.

## What it checks

Every link between pages, layers and ADRs is resolved to the page it names, and every `#anchor` is compared with the headings of that page, with the slug rule the documentation tests use. A link that does not resolve is listed and the build exits 1, so a moved page fails here as it fails `go test`.

A link to a file that is not documentation (a Go source file, a benchmark result) cannot be a page. Give the site the repository's address and those links point there:

```sh
go run ./cmd/bites-site -out site -repo-url https://example.com/owner/bites -branch main
```

Without `-repo-url` the text stays and the target is in its `title`; the build says how many.

## What it leaves out

There is no search, no syntax highlighting and no script, because each is a dependency or code to ship. Where to publish the site, and whether to keep a copy for each release, is not decided; the tool writes a folder and stops there.
