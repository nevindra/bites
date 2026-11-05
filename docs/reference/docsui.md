# docsui

**The module that serves an interactive page for the app's OpenAPI document, Swagger UI or Scalar, loaded from a CDN at a pinned version, with a Content-Security-Policy that needs no inline script.**

**Guide:** [the API documentation UI](../guide/api-docs-ui.md) · **Design:** [OpenAPI](../design/openapi.md)

`github.com/nevindra/bites/modules/docsui` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/docsui && go test ./...`. The decision is [ADR 0071](../adr/0071-the-api-documentation-ui-is-a-module-that-loads-a-pinned-cdn-version.md).

### `docsui.Mount`

```go
func Mount(app *bites.App, path string, cfg Config, opts ...bites.RouteOption)
```

Mount serves the page at `GET path` and, for Swagger UI, its script at `GET path + "/init.js"`. Both answers are fixed bytes built when `Mount` runs, with `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `Cache-Control: no-cache`. `opts` are the route options of the typed routes; `bites.Use` with an authentication middleware keeps the page private. Both routes carry [`bites.Undocumented`](./openapi.md#undocumented), so they are not operations of the document they show. It panics, naming the file and line of the call, for a path that does not start with a slash, ends with one or holds a wildcard, query or fragment; an empty `SpecURL` or one that is neither a path nor an `http(s)` URL; a `UI` that is neither value; a `Version` that is not an exact version; a `CDN` that is not `https`; and an integrity value that is not `sha...`.

### `docsui.Config`

```go
type Config struct {
	SpecURL         string
	Title           string
	UI              UI
	Version         string
	CDN             string
	ScriptIntegrity string
	StyleIntegrity  string
}
```

`SpecURL` is where the document is served, the path given to `App.OpenAPI`; it is required, and behind a prefix it carries the prefix. `Title` is the page title (`API reference` when empty). `UI` is `Swagger` (the zero value) or `Scalar`. `Version` is the exact CDN version of the interface, the package's default for the UI when empty. `CDN` is the base of the CDN, `https://cdn.jsdelivr.net/npm` when empty; set it to a mirror to keep the page working with no third party. `ScriptIntegrity` and `StyleIntegrity` are Subresource Integrity values (`sha384-...`) of the script and the stylesheet at that version, added to the tags when set; Swagger UI has both, Scalar the script only.

### `docsui.UI`

```go
type UI uint8

const (
	Swagger UI = iota
	Scalar
)
```

UI is the interface that renders the document.

### `docsui.SwaggerVersion`

```go
const SwaggerVersion = "5.34.1"
```

The Swagger UI version loaded when `Config.Version` is empty. It was chosen from the CDN's catalogue as remembered and not fetched, because the module is built offline: check it before relying on it, or set `Version`.

### `docsui.ScalarVersion`

```go
const ScalarVersion = "1.76.4"
```

The Scalar version loaded when `Config.Version` is empty, with the same caveat as `SwaggerVersion`.

### `docsui.Page`

```go
func Page(path string, cfg Config) (page, script []byte)
```

Page returns the HTML of the page and, for Swagger UI, the script it loads, for the configuration; `Mount` serves exactly these. It is for a test or a static build of the documentation, and panics like `Mount` on a bad configuration.
