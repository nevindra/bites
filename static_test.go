package bites_test

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// Static (ADR 0065): a directory served under a prefix, with twins, cache headers, an index and a
// single-page-app fallback.

//go:embed testdata/static
var staticEmbed embed.FS

func staticFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(staticEmbed, "testdata/static")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func embedTag(t *testing.T, name string) string {
	t.Helper()
	b, err := staticEmbed.ReadFile("testdata/static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
}

func doStatic(t *testing.T, srv *bitetest.Server, target string, h map[string]string) bitetest.Response {
	t.Helper()
	hdr := map[string][]string{}
	for k, v := range h {
		hdr[k] = []string{v}
	}
	return srv.Do(bitetest.Request{Method: "GET", Target: target, Header: hdr})
}

func TestAStaticEmbedFSIsServedWithAContentHashETagAndItsIndexForTheBarePrefix(t *testing.T) {
	// fs.Sub of an embed.FS is not an embed.FS, but its files have no times, so both are indexed at
	// Registration.
	srv := start(t, func(a *bites.App) {
		a.Static("/assets", bites.Static{FS: staticEmbed})
		a.Static("/sub", bites.Static{FS: staticFS(t)})
	})
	r := doStatic(t, srv, "/assets/testdata/static/style.css", nil)
	if r.Status != 200 || r.Text() != "body{}" || r.Get("etag") != embedTag(t, "style.css") || r.Get("last-modified") != "" ||
		r.Get("cache-control") != "no-cache" || !strings.HasPrefix(r.Get("content-type"), "text/css") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := doStatic(t, srv, "/assets/testdata/static/style.css", map[string]string{"If-None-Match": embedTag(t, "style.css")}); r.Status != 304 {
		t.Fatalf("304: %d", r.Status)
	}
	if r := doStatic(t, srv, "/assets/testdata/static/", nil); r.Status != 200 || r.Text() != "<!doctype html><title>app</title>" || r.Get("cache-control") != "no-cache" {
		t.Fatalf("index: %d %v %q", r.Status, r.Header, r.Text())
	}
	if r := doStatic(t, srv, "/sub/docs/", nil); r.Status != 200 || r.Text() != "<p>docs</p>" {
		t.Fatalf("a nested index through fs.Sub: %d %q", r.Status, r.Text())
	}
	if r := doStatic(t, srv, "/sub/docs", nil); r.Status != 404 {
		t.Fatalf("a directory without its slash is a 404, never a redirect: %d %v", r.Status, r.Header)
	}
	if r := doStatic(t, srv, "/sub/", nil); r.Status != 200 || r.Get("etag") != embedTag(t, "index.html") {
		t.Fatalf("an fs.FS with no times (fs.Sub of an embed.FS) is indexed like one: %d %v", r.Status, r.Header)
	}
}

func TestATwinIsChosenByTheWeightsOfAcceptEncodingAndCarriesTheOriginalsType(t *testing.T) {
	sub := staticFS(t)
	for _, tc := range []struct {
		name string
		fsys fs.FS
	}{{"embed", staticEmbed}, {"fs.Sub", sub}} {
		prefix := "/s/"
		if tc.name == "embed" {
			prefix = "/s/testdata/static/"
		}
		srv := start(t, func(a *bites.App) { a.Static("/s", bites.Static{FS: tc.fsys}) })
		for _, c := range []struct{ ae, enc, body string }{
			{"", "", `console.log("hashed")`},
			{"br, gzip", "br", "BROTLI-BYTES"},
			{"gzip, deflate", "gzip", "GZIP-BYTES"},
			{"br;q=0.5, gzip", "gzip", "GZIP-BYTES"},
			{"zstd", "", `console.log("hashed")`},
			{"*", "br", "BROTLI-BYTES"},
			{"*;q=0.5, br;q=0", "gzip", "GZIP-BYTES"},
			{"identity, br;q=0.5", "", `console.log("hashed")`},
			{"br;q=0, gzip;q=0", "", `console.log("hashed")`},
			{"gzip;q=0.001, br;q=0.0005", "gzip", "GZIP-BYTES"},
		} {
			r := doStatic(t, srv, prefix+"app-3f2a1b9c.js", map[string]string{"Accept-Encoding": c.ae})
			if c.ae == "" {
				r = doStatic(t, srv, prefix+"app-3f2a1b9c.js", nil)
			}
			if r.Status != 200 || r.Get("content-encoding") != c.enc || r.Text() != c.body || r.Get("vary") != "Accept-Encoding" ||
				!strings.HasPrefix(r.Get("content-type"), "text/javascript") || r.Get("cache-control") != "max-age=31536000, immutable" {
				t.Errorf("%s %q: %d %v %q", tc.name, c.ae, r.Status, r.Header, r.Text())
			}
		}
	}
}

func TestATwinIsRangedAndValidatedAsItsOwnRepresentation(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Static("/s", bites.Static{FS: staticEmbed}) })
	br := map[string]string{"Accept-Encoding": "br", "Range": "bytes=0-5"}
	r := doStatic(t, srv, "/s/testdata/static/app-3f2a1b9c.js", br)
	if r.Status != 206 || r.Text() != "BROTLI" || r.Get("content-encoding") != "br" || r.Get("etag") != embedTag(t, "app-3f2a1b9c.js.br") {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Text())
	}
	// The identity tag does not validate the br representation.
	r = doStatic(t, srv, "/s/testdata/static/app-3f2a1b9c.js", map[string]string{"Accept-Encoding": "br", "If-None-Match": embedTag(t, "app-3f2a1b9c.js")})
	if r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
}

func TestAnImageHasNoVaryAndNoTwinIsLookedFor(t *testing.T) {
	srv := start(t, func(a *bites.App) { a.Static("/s", bites.Static{FS: staticFS(t)}) })
	r := doStatic(t, srv, "/s/logo.png", map[string]string{"Accept-Encoding": "br"})
	if r.Status != 200 || r.Get("vary") != "" || r.Get("content-encoding") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func staticRootDir(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pub", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "index.html"), []byte("<p>home</p>"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "a.css"), []byte("a{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "a.css.gz"), []byte("GZ"), 0o644)
	os.WriteFile(filepath.Join(dir, "pub", "plain.txt"), []byte("plain"), 0o644)
	os.Chtimes(filepath.Join(dir, "pub", "plain.txt"), time.Time{}, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "pub", "link-out"))
	root, err := os.OpenRoot(filepath.Join(dir, "pub"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestAStaticOsRootServesFilesAsTheyAreNowAndEveryForbiddenNameIsTheSame404(t *testing.T) {
	root, dir := staticRootDir(t)
	srv := start(t, func(a *bites.App) { a.Static("/static", bites.Static{Root: root, MaxAge: time.Minute}) })
	r := doStatic(t, srv, "/static/plain.txt", nil)
	if r.Status != 200 || r.Text() != "plain" || r.Get("cache-control") != "max-age=60" || r.Get("etag") == "" || r.Get("last-modified") != "Sun, 01 Mar 2026 00:00:00 GMT" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := doStatic(t, srv, "/static/a.css", map[string]string{"Accept-Encoding": "gzip, br"}); r.Text() != "GZ" || r.Get("content-encoding") != "gzip" {
		t.Fatalf("a twin beside a file in an os.Root: %v %q", r.Header, r.Text())
	}
	os.WriteFile(filepath.Join(dir, "pub", "new.txt"), []byte("new"), 0o644)
	if r := doStatic(t, srv, "/static/new.txt", nil); r.Text() != "new" {
		t.Fatalf("a file added while serving: %d", r.Status)
	}
	want := `{"title":"Not Found","status":404}`
	for _, name := range []string{"nope.txt", "sub", "sub/", "%2e%2e/secret.txt", "..%2fsecret.txt", "link-out", "%2fetc%2fpasswd", "a.css%00", "%zz", "../secret.txt", "a//b"} {
		r := doStatic(t, srv, "/static/"+name, map[string]string{"Accept": "text/html"})
		if r.Status == 400 && strings.Contains(r.Text(), "the path is not normalised") {
			continue
		}
		if r.Status != 404 || r.Text() != want {
			t.Errorf("%q: %d %s", name, r.Status, r.Text())
		}
		if strings.Contains(r.Text(), "SECRET") {
			t.Fatalf("%q leaked", name)
		}
	}
}

func spaApp(t *testing.T) *bitetest.Server {
	mem := fstest.MapFS{
		"index.html":             {Data: []byte("<div id=app></div>"), ModTime: time.Unix(1700000000, 0)},
		"assets/app-3f2a1b9c.js": {Data: []byte("app()"), ModTime: time.Unix(1700000000, 0)},
	}
	return start(t, func(a *bites.App) {
		a.Get("/api/users/{id}", getUser)
		a.Get("/healthz", func(context.Context, Empty) (bites.Text, error) { return bites.Text{Body: "ok"}, nil })
		a.Static("/", bites.Static{FS: mem, Fallback: "index.html"})
	})
}

func TestTheFallbackAnswersANavigationWithTheIndexAndNeverHidesAnAPI404(t *testing.T) {
	srv := spaApp(t)
	html := map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"}
	for _, p := range []string{"/", "/dashboard", "/dashboard/settings/42", "/users/7"} {
		r := doStatic(t, srv, p, html)
		if r.Status != 200 || r.Text() != "<div id=app></div>" || r.Get("cache-control") != "no-cache" {
			t.Errorf("%s: %d %v %q", p, r.Status, r.Header, r.Text())
		}
	}
	for _, tc := range []struct {
		path   string
		accept string
	}{
		{"/api/nothing", "text/html"},       // the first segment is the API's
		{"/api/users/1/extra", "text/html"}, // under the API, even as a navigation
		{"/healthz/x", "text/html"},
		{"/dashboard", "application/json"},      // not a navigation
		{"/dashboard", "text/html;q=0"},         // refused explicitly
		{"/missing.js", "text/html"},            // a file with an extension
		{"/assets/app-00000000.js", "*/*"},      // a stale hashed asset
		{"/assets/missing", "application/json"}, // a fetch
	} {
		r := doStatic(t, srv, tc.path, map[string]string{"Accept": tc.accept})
		if r.Status != 404 || r.Get("content-type") != "application/problem+json" {
			t.Errorf("%s (%s): %d %q", tc.path, tc.accept, r.Status, r.Text())
		}
	}
	if r := doStatic(t, srv, "/api/users/7", html); r.Status != 200 || !strings.Contains(r.Text(), `"id":7`) {
		t.Fatalf("the API route wins: %d %q", r.Status, r.Text())
	}
	if r := doStatic(t, srv, "/assets/app-3f2a1b9c.js", nil); r.Get("cache-control") != "max-age=31536000, immutable" {
		t.Fatalf("%v", r.Header)
	}
}

func TestAStaticRouteOfAGroupRunsTheGroupsMiddleware(t *testing.T) {
	var seen string
	srv := start(t, func(a *bites.App) {
		g := a.Group("/app", bites.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			seen = req.Path()
			return next.Call(ctx), nil
		}))
		g.Static("/files", bites.Static{FS: staticFS(t)})
	})
	if r := doStatic(t, srv, "/app/files/style.css", nil); r.Status != 200 || r.Text() != "body{}" || seen != "/app/files/style.css" {
		t.Fatalf("%d %q %q", r.Status, r.Text(), seen)
	}
}

func TestTheDefaultImmutableRuleFindsContentHashesAndNotOrdinaryNames(t *testing.T) {
	yes := []string{"app-3f2a1b9c.js", "assets/index-BxR3k9aQ.js", "main.3f2a1b9c.chunk.css", "chunk-ABCD1234.js", "3f2a1b9cdeadbeef.woff2"}
	no := []string{"index.html", "app.js", "jquery-3.7.1.min.js", "favicon.ico", "fontawesome6.css", "20240101.json", "my-component.js", "deadbeefcafe.js"}
	srv := start(t, func(a *bites.App) {
		mem := fstest.MapFS{}
		for _, n := range append(append([]string{}, yes...), no...) {
			mem[n] = &fstest.MapFile{Data: []byte("x")}
		}
		a.Static("/", bites.Static{FS: mem})
	})
	for _, n := range yes {
		if r := doStatic(t, srv, "/"+n, nil); r.Get("cache-control") != "max-age=31536000, immutable" {
			t.Errorf("%s: %q, want immutable", n, r.Get("cache-control"))
		}
	}
	for _, n := range no {
		if r := doStatic(t, srv, "/"+n, nil); r.Get("cache-control") != "no-cache" {
			t.Errorf("%s: %q, want no-cache", n, r.Get("cache-control"))
		}
	}
}

func TestAnImmutableRuleOfTheApplicationReplacesTheDefault(t *testing.T) {
	srv := start(t, func(a *bites.App) {
		a.Static("/", bites.Static{FS: staticFS(t), Immutable: func(name string) bool { return strings.HasSuffix(name, ".css") }})
	})
	if r := doStatic(t, srv, "/style.css", nil); r.Get("cache-control") != "max-age=31536000, immutable" {
		t.Fatalf("%v", r.Header)
	}
	if r := doStatic(t, srv, "/app-3f2a1b9c.js", nil); r.Get("cache-control") != "no-cache" {
		t.Fatalf("%v", r.Header)
	}
}

func TestATwinThatGoesAwayIsNotServedAndATwinAddedIsFoundWhenItsFileChanges(t *testing.T) {
	root, dir := staticRootDir(t)
	srv := start(t, func(a *bites.App) { a.Static("/s", bites.Static{Root: root}) })
	gz := map[string]string{"Accept-Encoding": "gzip"}
	if r := doStatic(t, srv, "/s/a.css", gz); r.Text() != "GZ" {
		t.Fatalf("%q", r.Text())
	}
	os.Remove(filepath.Join(dir, "pub", "a.css.gz"))
	if r := doStatic(t, srv, "/s/a.css", gz); r.Status != 200 || r.Text() != "a{}" || r.Get("content-encoding") != "" {
		t.Fatalf("a removed twin: %d %v %q", r.Status, r.Header, r.Text())
	}
	if r := doStatic(t, srv, "/s/plain.txt", gz); r.Text() != "plain" {
		t.Fatalf("%q", r.Text())
	}
	// Which twins a file has is remembered per version of the file: a twin added beside an
	// unchanged file is not seen (ADR 0065 says so), and is once the file changes.
	os.WriteFile(filepath.Join(dir, "pub", "plain.txt.gz"), []byte("PGZ"), 0o644)
	if r := doStatic(t, srv, "/s/plain.txt", gz); r.Text() != "plain" {
		t.Fatalf("%q", r.Text())
	}
	os.WriteFile(filepath.Join(dir, "pub", "plain.txt"), []byte("plain, v2"), 0o644) // a new version of the file
	if r := doStatic(t, srv, "/s/plain.txt", gz); r.Text() != "PGZ" {
		t.Fatalf("a twin added with a new version of its file: %q", r.Text())
	}
}
