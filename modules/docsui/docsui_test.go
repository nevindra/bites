package docsui_test

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/docsui"
)

type user struct {
	ID int64 `json:"id"`
}

func start(t *testing.T, cfg docsui.Config) *bitetest.Server {
	t.Helper()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/users", func(context.Context, struct{}) (user, error) { return user{1}, nil })
	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
	docsui.Mount(app, "/docs", cfg)
	return bitetest.Start(t, app)
}

func TestThePageUnderTheDefaultSecurityHeadersStillLoadsItsPinnedCDNAssets(t *testing.T) {
	// ADR 0053: the defaults send Cross-Origin-Resource-Policy: same-site and no opener or
	// embedder policy, so the page may load the CDN's scripts, styles and fonts and the images the
	// API document names. Under "Cross-Origin-Embedder-Policy: require-corp" it would need the
	// CDN's own Cross-Origin-Resource-Policy: cross-origin (jsdelivr sends it) and every https image.
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Use(bites.SecurityHeaders())
	docsui.Mount(app, "/docs", docsui.Config{SpecURL: "/openapi.json"})
	r := bitetest.Start(t, app).Do(bitetest.Request{Method: "GET", Target: "/docs"})
	for _, k := range []string{"cross-origin-embedder-policy", "cross-origin-opener-policy"} {
		if got := r.Get(k); got != "" {
			t.Errorf("%s = %q on the documentation page", k, got)
		}
	}
	if got := r.Get("cross-origin-resource-policy"); got != "same-site" {
		t.Errorf("cross-origin-resource-policy = %q", got)
	}
}

func TestSwaggerUIIsServedWithAPinnedCDNVersionAndNoInlineScript(t *testing.T) {
	srv := start(t, docsui.Config{SpecURL: "/openapi.json", Title: "Users <API>"})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/docs"})
	body := r.Text()
	if r.Status != 200 || !strings.HasPrefix(r.Get("content-type"), "text/html") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	for _, want := range []string{
		"https://cdn.jsdelivr.net/npm/swagger-ui-dist@" + docsui.SwaggerVersion + "/swagger-ui-bundle.js",
		"<title>Users &lt;API&gt;</title>", `src="/docs/init.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"@latest", "@5\"", "<script>", "onload=", "onclick="} {
		if strings.Contains(body, bad) {
			t.Errorf("the page has %q", bad)
		}
	}
	js := srv.Do(bitetest.Request{Method: "GET", Target: "/docs/init.js"})
	if js.Status != 200 || !strings.Contains(js.Text(), `url: "/openapi.json"`) || !strings.HasPrefix(js.Get("content-type"), "text/javascript") {
		t.Fatalf("%d %v %s", js.Status, js.Header, js.Body)
	}
	csp := r.Get("content-security-policy")
	if !strings.Contains(csp, "script-src 'self' https://cdn.jsdelivr.net;") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("the policy is %q", csp)
	}
	if r.Get("x-content-type-options") != "nosniff" {
		t.Fatalf("%v", r.Header)
	}
}

func TestScalarIsServedFromAttributesAloneAndTheVersionAndIntegrityAreWhatWasGiven(t *testing.T) {
	srv := start(t, docsui.Config{SpecURL: "/openapi.json", UI: docsui.Scalar, Version: "1.2.3", ScriptIntegrity: "sha384-abc", CDN: "https://cdn.example.com/npm/"})
	body := srv.Do(bitetest.Request{Method: "GET", Target: "/docs"}).Text()
	want := `<script id="api-reference" data-url="/openapi.json" src="https://cdn.example.com/npm/@scalar/api-reference@1.2.3" integrity="sha384-abc" crossorigin="anonymous"></script>`
	if !strings.Contains(body, want) {
		t.Fatalf("got:\n%s", body)
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/docs/init.js"}); r.Status != 404 {
		t.Fatalf("Scalar has no init script, got %d", r.Status)
	}
	if csp := srv.Do(bitetest.Request{Method: "GET", Target: "/docs"}).Get("content-security-policy"); !strings.Contains(csp, "script-src 'self' https://cdn.example.com;") {
		t.Fatalf("the policy is %q", csp)
	}
}

func TestTheDocumentTheUIPointsAtIsServedByTheSameApp(t *testing.T) {
	srv := start(t, docsui.Config{SpecURL: "/openapi.json"})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"})
	if r.Status != 200 || !strings.Contains(r.Text(), `"openapi"`) {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
}

func TestAMistakeInTheConfigurationIsAPanicNamingTheLine(t *testing.T) {
	for name, tc := range map[string]struct {
		path string
		cfg  docsui.Config
	}{
		"no spec":            {"/docs", docsui.Config{}},
		"relative path":      {"docs", docsui.Config{SpecURL: "/o"}},
		"a wildcard":         {"/d/{x}", docsui.Config{SpecURL: "/o"}},
		"a trailing /":       {"/docs/", docsui.Config{SpecURL: "/o"}},
		"a version break":    {"/docs", docsui.Config{SpecURL: "/o", Version: `1"></script><script>x`}},
		"an http CDN":        {"/docs", docsui.Config{SpecURL: "/o", CDN: "http://cdn.example.com"}},
		"a bad UI":           {"/docs", docsui.Config{SpecURL: "/o", UI: 9}},
		"a bad integrity":    {"/docs", docsui.Config{SpecURL: "/o", ScriptIntegrity: `x" onload="y`}},
		"a javascript: spec": {"/docs", docsui.Config{SpecURL: "javascript:alert(1)"}},
	} {
		func() {
			defer func() {
				v := recover()
				s, _ := v.(string)
				if !strings.Contains(s, "docsui.Mount") || !strings.Contains(s, "registered at") || !strings.Contains(s, "docsui_test.go") {
					t.Errorf("%s: %v", name, v)
				}
			}()
			docsui.Mount(bites.New(), tc.path, tc.cfg)
		}()
	}
}

func TestASpecURLWithQuotesIsEscapedInTheAttributeAndInTheScript(t *testing.T) {
	page, script := docsui.Page("/docs", docsui.Config{SpecURL: `/o?a="b"&c=<d>`})
	if strings.Contains(string(page), `"b"`) || strings.Contains(string(page), "<d>") {
		t.Fatalf("%s", page)
	}
	if !strings.Contains(string(script), `\"b\"`) {
		t.Fatalf("%s", script)
	}
	page, _ = docsui.Page("/docs", docsui.Config{SpecURL: `/o?a="b"&c=<d>`, UI: docsui.Scalar})
	if strings.Contains(string(page), `"b"`) || !strings.Contains(string(page), "&#34;b&#34;") {
		t.Fatalf("%s", page)
	}
}

func TestTheModuleImportsOnlyTheCoresExportedAPI(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, "bites/internal") {
			t.Errorf("the module imports %s", p)
		}
	}
}

func TestThePageAndItsScriptAreLeftOutOfTheAPIDocument(t *testing.T) {
	srv := start(t, docsui.Config{SpecURL: "/openapi.json"})
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	if !strings.Contains(doc, `"/users"`) || strings.Contains(doc, "/docs") {
		t.Fatalf("%s", doc)
	}
}
