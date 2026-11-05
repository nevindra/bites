// Package docsui serves an interactive page for an app's OpenAPI document, Scalar or Swagger UI,
// as a module apart from the core (ADR 0070, ADR 0071). The page is a few hundred bytes of HTML
// built once when the route is registered; the user interface itself is loaded by the browser
// from a CDN at a pinned version, so nothing is vendored and nothing is added to the core's
// binary.
//
//	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
//	docsui.Mount(app, "/docs", docsui.Config{SpecURL: "/openapi.json", Title: "Users API"})
//
// The page has no inline script: Scalar reads its settings from data attributes and Swagger UI
// from a small script the module serves beside the page, so a Content-Security-Policy that names
// the CDN and 'self' is enough, and the page sets that policy itself.
package docsui

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"runtime"
	"strings"

	"github.com/nevindra/bites"
)

// UI is the interface that renders the document.
type UI uint8

const (
	// Swagger is Swagger UI. It is the zero value.
	Swagger UI = iota
	// Scalar is Scalar's API reference.
	Scalar
)

// The versions loaded when Config.Version is empty. They are exact, never a range, so the page a
// browser gets is the page that was tested; pin another with Config.Version. Neither was
// fetched when this module was written (its tests run offline): check the version on the CDN
// before relying on a default.
const (
	SwaggerVersion = "5.34.1"
	ScalarVersion  = "1.76.4"
)

// Config says what the page shows and where it loads the interface from.
type Config struct {
	// SpecURL is where the OpenAPI document is served, the path given to App.OpenAPI. Required.
	SpecURL string
	// Title is the page title; "API reference" if empty.
	Title string
	// UI is Swagger (the default) or Scalar.
	UI UI
	// Version is the exact CDN version of the interface; the package's default for the UI if empty.
	Version string
	// CDN is the base of the CDN, "https://cdn.jsdelivr.net/npm" if empty. Set it to a mirror you
	// control to keep the page working with no third party.
	CDN string
	// ScriptIntegrity and StyleIntegrity are Subresource Integrity values ("sha384-...") of the
	// script and the stylesheet at that version, added to the tags when set. Swagger UI has both;
	// Scalar has the script only.
	ScriptIntegrity, StyleIntegrity string
}

// Mount serves the page at GET path and, for Swagger UI, its script at GET path+"/init.js". It
// panics at the line that called it for a path that is not a plain absolute path, a missing or
// invalid SpecURL, a version or CDN that could break out of the page, and, like any route, for a
// path another route already has. Options are the route options of the typed routes (Use for
// an authentication middleware that keeps the page private).
func Mount(app *bites.App, path string, cfg Config, opts ...bites.RouteOption) {
	page, script := build(path, cfg, caller())
	headers := bites.Header{
		"Content-Security-Policy": {csp(cfg)},
		"X-Content-Type-Options":  {"nosniff"},
		"Referrer-Policy":         {"no-referrer"},
		"Cache-Control":           {"no-cache"},
	}
	// The page and its script are not operations of the API they document (ADR 0069).
	opts = append(opts[:len(opts):len(opts)], bites.Undocumented())
	app.Get(path, func(context.Context, struct{}) (bites.Bytes, error) {
		return bites.Bytes{ContentType: "text/html; charset=utf-8", Body: page, Header: headers}, nil
	}, opts...)
	if script != nil {
		app.Get(strings.TrimSuffix(path, "/")+"/init.js", func(context.Context, struct{}) (bites.Bytes, error) {
			return bites.Bytes{ContentType: "text/javascript; charset=utf-8", Body: script, Header: headers}, nil
		}, opts...)
	}
}

func caller() string {
	_, file, line, _ := runtime.Caller(2)
	return fmt.Sprintf("%s:%d", file, line)
}

// Page returns the HTML of the page and, for Swagger UI, the script it loads, for the
// configuration; Mount serves exactly these. It is exported for a test or a static build of the
// documentation, and panics like Mount on a bad configuration.
func Page(path string, cfg Config) (page, script []byte) { return build(path, cfg, caller()) }

func build(path string, cfg Config, where string) (page, script []byte) {
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("docsui.Mount: %s\n\tregistered at %s", fmt.Sprintf(format, args...), where))
	}
	switch {
	case !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{}?#") || strings.HasSuffix(path, "/") && path != "/":
		fail("the path %q must start with a slash, end without one and have no wildcard, query or fragment", path)
	case cfg.SpecURL == "":
		fail("Config.SpecURL is empty: give the path App.OpenAPI serves the document at")
	case cfg.UI != Swagger && cfg.UI != Scalar:
		fail("Config.UI is %d, which is neither Swagger nor Scalar", cfg.UI)
	}
	if u, err := url.Parse(cfg.SpecURL); err != nil || u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		fail("Config.SpecURL %q is not a path or an http(s) URL", cfg.SpecURL)
	}
	version := cfg.Version
	if version == "" {
		version = map[UI]string{Swagger: SwaggerVersion, Scalar: ScalarVersion}[cfg.UI]
	}
	if !safeToken(version) {
		fail("Config.Version %q must be an exact version of letters, digits, dots, dashes and plus signs", version)
	}
	cdn := strings.TrimRight(cfg.CDN, "/")
	if cdn == "" {
		cdn = "https://cdn.jsdelivr.net/npm"
	}
	if u, err := url.Parse(cdn); err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(cdn, "\"'<> \t\r\n") {
		fail("Config.CDN %q must be an https URL", cfg.CDN)
	}
	for _, s := range []string{cfg.ScriptIntegrity, cfg.StyleIntegrity} {
		if s != "" && !strings.HasPrefix(s, "sha") || strings.ContainsAny(s, "\"'<> \t\r\n") {
			fail("an integrity value %q must look like sha384-...", s)
		}
	}
	title := cfg.Title
	if title == "" {
		title = "API reference"
	}
	integrity := func(v string) string {
		if v == "" {
			return ""
		}
		return ` integrity="` + html.EscapeString(v) + `" crossorigin="anonymous"`
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title>\n")
	switch cfg.UI {
	case Swagger:
		pkg := cdn + "/swagger-ui-dist@" + version
		fmt.Fprintf(&b, "<link rel=\"stylesheet\" href=\"%s/swagger-ui.css\"%s>\n</head>\n<body>\n<div id=\"swagger-ui\"></div>\n", html.EscapeString(pkg), integrity(cfg.StyleIntegrity))
		fmt.Fprintf(&b, "<script src=\"%s/swagger-ui-bundle.js\"%s></script>\n<script src=\"%s\"></script>\n", html.EscapeString(pkg), integrity(cfg.ScriptIntegrity), html.EscapeString(strings.TrimSuffix(path, "/")+"/init.js"))
		spec, _ := json.Marshal(cfg.SpecURL)
		script = fmt.Appendf(nil, "window.addEventListener('load', function () {\n  window.ui = SwaggerUIBundle({ url: %s, dom_id: '#swagger-ui', deepLinking: true });\n});\n", spec)
	case Scalar:
		fmt.Fprintf(&b, "</head>\n<body>\n<script id=\"api-reference\" data-url=\"%s\" src=\"%s/@scalar/api-reference@%s\"%s></script>\n", html.EscapeString(cfg.SpecURL), html.EscapeString(cdn), version, integrity(cfg.ScriptIntegrity))
	}
	b.WriteString("</body>\n</html>\n")
	return []byte(b.String()), script
}

func safeToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

// csp is the policy of the page: nothing but itself and the CDN, no frames, no plugins. Both
// interfaces draw with inline styles, so style-src allows them; script-src never does. The
// document is fetched from the page's own origin or the origin SpecURL names.
func csp(cfg Config) string {
	cdn := cfg.CDN
	if cdn == "" {
		cdn = "https://cdn.jsdelivr.net"
	} else if u, err := url.Parse(cdn); err == nil {
		cdn = u.Scheme + "://" + u.Host
	}
	connect := "'self'"
	if u, err := url.Parse(cfg.SpecURL); err == nil && u.Host != "" {
		connect += " " + u.Scheme + "://" + u.Host
	}
	return "default-src 'none'; script-src 'self' " + cdn + "; style-src 'self' 'unsafe-inline' " + cdn +
		"; img-src 'self' data: https:; font-src 'self' data: " + cdn + "; connect-src " + connect +
		"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
}
