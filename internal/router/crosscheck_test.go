package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// The cross-check registers one table in net/http's ServeMux and in Router
// and compares, for every method and path, which pattern answered and what
// it captured. The only differences allowed are the deliberate ones of
// ADR 0018, each named where it is allowed below.

var crossTable = []string{
	"GET /",
	"GET /{$}",
	"GET /users",
	"POST /users",
	"GET /users/{id}",
	"DELETE /users/{id}",
	"GET /users/me",
	"GET /users/{id}/posts",
	"GET /users/{id}/posts/{post}",
	"GET /users/{id}/posts/latest",
	"GET /files/{name}",
	"GET /static/",
	"POST /static/upload",
	"GET /docs/{$}",
	"GET /docs/{page}",
	"GET /blob/{path...}",
	"HEAD /blob/special",
	"PUT /blob/special/{x}",
	"GET /a/{x}/b/c",
	"GET /a/b/{y}/d",
	"POST /a/{x}",
}

// A mount (method-less) conflicts with GET / and the like, in ServeMux and in
// Router alike, so it gets its own table.
var crossMounts = []string{
	"/mount/",
	"GET /mount/exact",
	"/any/{id}",
	"POST /any/fixed",
	"GET /other",
}

var crossPaths = []string{
	"/", "/users", "/users/", "/users/42", "/users/me", "/users/42/posts", "/users/42/posts/", "/users/42/posts/7",
	"/users/42/posts/latest", "/users/a%2Fb", "/users/a%20b", "/users/%2e%2e", "/files/a", "/files/", "/files/a/b",
	"/files/a%2Fb", "/static", "/static/", "/static/x.css", "/static/upload", "/statics", "/docs", "/docs/", "/docs/intro",
	"/docs/a/b", "/blob/", "/blob", "/blob/a/b/c", "/blob/special", "/blob/special/x", "/blob/special/x/y", "/blob/a%2Fb/c",
	"/a/q/b/c", "/a/b/q/c", "/a/b/b/c", "/a/b", "/a/q", "/mount", "/mount/", "/mount/x/y", "/mount/exact", "/any/1", "/any/fixed",
	"/any", "/nope", "/nope/deeper",
	// not normalised: ServeMux answers a redirect, Router answers BadPath
	"//", "/users//42", "/users/./42", "/users/../users", "/users/42/.", "/users/42/..", "/static/../x", "//users",
}

var crossMethods = []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PURGE"}

func muxOf(t *testing.T, table []string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, s := range table {
		pat := s
		mux.HandleFunc(pat, func(w http.ResponseWriter, r *http.Request) {
			var names []string
			for _, seg := range strings.FieldsFunc(r.Pattern, func(c rune) bool { return c == '/' }) {
				if strings.HasPrefix(seg, "{") && !strings.HasPrefix(seg, "{$") {
					names = append(names, strings.TrimSuffix(strings.TrimSuffix(strings.Trim(seg, "{}"), "..."), "}"))
				}
			}
			out := r.Pattern
			for _, n := range names {
				out += fmt.Sprintf("|%s=%s", n, r.PathValue(n))
			}
			w.Write([]byte(out))
		})
	}
	return mux
}

func TestRouterPicksTheSamePatternServeMuxDoes(t *testing.T) {
	for _, table := range [][]string{crossTable, crossMounts} {
		crossCheck(t, table)
	}
}

func crossCheck(t *testing.T, table []string) {
	mux := muxOf(t, table)
	r := build(t, table...)
	diffs := map[string]int{}
	for _, m := range crossMethods {
		for _, path := range crossPaths {
			req := httptest.NewRequest(m, "http://example.com"+path, nil)
			req.URL.Path, req.URL.RawPath = unescapedPath(path)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			h, res, allow, vals := look(r, m, path)

			switch {
			case rec.Code == 200:
				// The pattern and every captured value must agree. ServeMux
				// hands out decoded values, Router the raw ones, so decode
				// ours before comparing (ADR 0018, "decoding after").
				if res != Found {
					diffs[fmt.Sprintf("mux 200 %q, router %v", rec.Body.String(), res)]++
					t.Errorf("%s %s: ServeMux answered %q, Router %v", m, path, rec.Body.String(), res)
					continue
				}
				names := make([]string, 0, len(vals))
				for n := range vals {
					names = append(names, n)
				}
				sort.Strings(names)
				_, after, _ := strings.Cut(rec.Body.String(), "|")
				want := strings.Split(rec.Body.String(), "|")[0]
				if h != want {
					t.Errorf("%s %s: ServeMux %q, Router %q", m, path, want, h)
					continue
				}
				var gotVals []string
				for _, n := range names {
					dec, _ := url.PathUnescape(vals[n])
					gotVals = append(gotVals, n+"="+dec)
				}
				if g, w := strings.Join(gotVals, "|"), after; g != w {
					t.Errorf("%s %s: ServeMux values %q, Router %q", m, path, w, g)
				}
			case rec.Code == 405:
				if res != MethodNotAllowed || allow != rec.Header().Get("Allow") {
					t.Errorf("%s %s: ServeMux 405 Allow %q, Router %v %q", m, path, rec.Header().Get("Allow"), res, allow)
				}
			case rec.Code == 404:
				if res != NotFound {
					t.Errorf("%s %s: ServeMux 404, Router %v (%q)", m, path, res, h)
				}
			case rec.Code == 301 || rec.Code == 307 || rec.Code == 308:
				// ALLOWED, ADR 0018 "No automatic redirects": ServeMux answers a
				// redirect to the cleaned path or to the trailing-slash path.
				// Router never redirects: the path is NotFound (a missing or an
				// extra trailing slash) or BadPath (not normalised), or, where a
				// broader pattern such as GET / or a mount also matches the
				// path as given, Found by that one: ServeMux prefers the
				// redirect to the more specific trailing-slash pattern.
				diffs[fmt.Sprintf("redirect, router %v", res)]++
				if res == Found && strings.HasSuffix(path, "/") {
					t.Errorf("%s %s: ServeMux redirected, Router found %q", m, path, h)
				}
			default:
				t.Errorf("%s %s: ServeMux status %d", m, path, rec.Code)
			}
		}
	}
	t.Logf("deliberate differences seen (ADR 0018, no redirects): %v", diffs)
}

// unescapedPath splits a raw path into the Path and RawPath of a URL, as
// net/http's parser would.
func unescapedPath(raw string) (string, string) {
	u, err := url.Parse("http://example.com" + raw)
	if err != nil {
		return raw, ""
	}
	return u.Path, u.RawPath
}

// The patterns whose pairwise acceptance is compared. A pair ServeMux
// refuses at registration must be refused by Router, and the other way round.
var conflictPatterns = []string{
	"GET /a", "POST /a", "HEAD /a", "/a",
	"GET /a/{x}", "GET /a/{y}", "POST /a/{x}", "/a/{x}",
	"GET /a/b", "/a/b", "HEAD /a/b",
	"GET /a/", "/a/", "GET /a/{x...}", "GET /a/b/", "GET /a/b/{y}",
	"GET /a/{x}/b", "GET /a/b/{y}", "GET /a/{x}/{y}", "GET /a/b/c",
	"GET /{$}", "GET /", "/", "GET /a/{$}", "GET /{x}", "GET /{x...}",
	"GET /a/{x}/", "GET /a/b/{y...}", "GET /a/{x}/c/{y...}", "GET /a/b/c/",
}

func TestRouterRefusesExactlyTheConflictsServeMuxRefuses(t *testing.T) {
	asMux := func(a, b string) (conflict bool) {
		defer func() { conflict = recover() != nil }()
		mux := http.NewServeMux()
		h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
		mux.Handle(a, h)
		mux.Handle(b, h)
		return false
	}
	asRouter := func(a, b string) bool {
		r := New[string]()
		split := func(s string) (string, string) {
			if strings.HasPrefix(s, "/") {
				return "", s
			}
			m, p, _ := strings.Cut(s, " ")
			return m, p
		}
		am, ap := split(a)
		bm, bp := split(b)
		if err := r.Add(am, ap, a, "a"); err != nil {
			t.Fatalf("%q: %v", a, err)
		}
		return r.Add(bm, bp, b, "b") != nil
	}
	n := 0
	for _, a := range conflictPatterns {
		for _, b := range conflictPatterns {
			if got, want := asRouter(a, b), asMux(a, b); got != want {
				t.Errorf("%q then %q: Router conflict=%v, ServeMux conflict=%v", a, b, got, want)
			}
			n++
		}
	}
	t.Logf("%d ordered pairs compared", n)
}
