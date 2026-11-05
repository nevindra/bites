# The API documentation UI

**The `docsui` module serves Swagger UI or Scalar for your OpenAPI document: a small page from your app, the interface loaded from a CDN at a pinned version.**

**Reference:** [`docsui`](../reference/docsui.md) · **Design:** [OpenAPI](../design/openapi.md)

[The OpenAPI document](./openapi.md) says what the API is; this page is the browsable version of it. It is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so an app that does not want it neither downloads nor links it:

```sh
go get github.com/nevindra/bites/modules/docsui
```

Until the first release tags the modules, the `go.mod` of a module in this repository points at the core beside it, so use it from inside the repository, or with a `replace` of your own.

## Mounting it

Serve the document, then point the page at it:

```go
app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
docsui.Mount(app, "/docs", docsui.Config{SpecURL: "/openapi.json", Title: "Users API"})
```

`GET /docs` answers an HTML page, and Swagger UI's start script is at `/docs/init.js`. Both are fixed bytes built when `Mount` runs, so a request costs what a [`Bytes`](./responses.md) answer costs. Choose Scalar with `UI: docsui.Scalar`.

## The version and the CDN

The browser loads the interface from `https://cdn.jsdelivr.net/npm` at an exact version, so the page you tested is the page it gets. The defaults are in `docsui.SwaggerVersion` and `docsui.ScalarVersion`; they were not fetched when the module was written, so confirm one on the CDN or set your own:

```go
docsui.Mount(app, "/docs", docsui.Config{
	SpecURL:         "/openapi.json",
	Version:         "5.34.1",
	ScriptIntegrity: "sha384-...", // from the CDN, once
})
```

To use no third party at all, mirror the package to a host you control and set `CDN: "https://assets.example.com/npm"`.

## The policy

The page has no inline script, so its `Content-Security-Policy` names only `'self'` and the CDN's origin for scripts, forbids frames and bases, and allows connections to your own origin (and the origin of `SpecURL` if it has one). Styles allow `'unsafe-inline'` because both interfaces draw with inline styles. If your site sets its own policy in a middleware, give it the same origins or let this one stand.

## Keeping it private

`Mount` takes the route options of any route, so a middleware that answers 401 keeps the page (and its script) behind a login: `docsui.Mount(app, "/docs", cfg, bites.Use(requireStaff))`. The document is a separate route; protect it the same way if the API's shape is not public.

The page and the script are not in the API document: `Mount` gives both routes [`bites.Undocumented()`](../reference/openapi.md#undocumented) ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)), since they are not operations of the API they show.
