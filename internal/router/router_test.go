package router

import (
	"fmt"
	"strings"
	"testing"
)

func build(t testing.TB, routes ...string) *Router[string] {
	t.Helper()
	r := New[string]()
	for i, s := range routes {
		method, pattern, _ := strings.Cut(s, " ")
		if strings.HasPrefix(s, "/") {
			method, pattern = "", s
		}
		if err := r.Add(method, pattern, s, fmt.Sprintf("r.go:%d", i+1)); err != nil {
			t.Fatalf("Add(%q): %v", s, err)
		}
	}
	r.Freeze()
	return r
}

func look(r *Router[string], method, path string) (string, Result, string, map[string]string) {
	var p Params
	h, res, allow := r.Lookup([]byte(method), []byte(path), &p)
	vals := map[string]string{}
	if res == Found {
		for i := 0; i < p.N; i++ {
			vals[p.Names[i]] = string(p.Values[i])
		}
	}
	return h, res, allow, vals
}

func TestALiteralBeatsAWildcardBeatsARest(t *testing.T) {
	r := build(t, "GET /a/{x}", "GET /a/b", "GET /a/{x...}", "GET /a/{x}/c")
	for path, want := range map[string]string{
		"/a/b":     "GET /a/b",
		"/a/q":     "GET /a/{x}",
		"/a/q/c":   "GET /a/{x}/c",
		"/a/q/d":   "GET /a/{x...}",
		"/a/b/d/e": "GET /a/{x...}",
		"/a/":      "GET /a/{x...}",
	} {
		if h, res, _, _ := look(r, "GET", path); res != Found || h != want {
			t.Errorf("GET %s = %q (%v), want %q", path, h, res, want)
		}
	}
}

func TestALiteralThatDeadEndsFallsBackToTheWildcard(t *testing.T) {
	r := build(t, "GET /a/b/c", "GET /a/{x}/d")
	if h, res, _, v := look(r, "GET", "/a/b/d"); res != Found || h != "GET /a/{x}/d" || v["x"] != "b" {
		t.Fatalf("got %q %v %v", h, res, v)
	}
}

func TestAMethodThatOnlyAWiderPatternHasFallsBackToIt(t *testing.T) {
	r := build(t, "GET /a/{x}", "POST /a/b")
	if h, res, _, _ := look(r, "GET", "/a/b"); res != Found || h != "GET /a/{x}" {
		t.Fatalf("got %q %v", h, res)
	}
	if h, _, _, _ := look(r, "POST", "/a/b"); h != "POST /a/b" {
		t.Fatalf("got %q", h)
	}
}

func TestParamsAreRawSegmentsInPatternOrder(t *testing.T) {
	r := build(t, "GET /orgs/{org}/repos/{repo}/files/{path...}")
	_, res, _, v := look(r, "GET", "/orgs/a%2Fb/repos/r/files/x/y%20z")
	if res != Found || v["org"] != "a%2Fb" || v["repo"] != "r" || v["path"] != "x/y%20z" || len(v) != 3 {
		t.Fatalf("%v %v", res, v)
	}
}

func TestAPercentEncodedSlashStaysInsideOneSegment(t *testing.T) {
	r := build(t, "GET /files/{name}")
	if _, res, _, v := look(r, "GET", "/files/a%2Fb"); res != Found || v["name"] != "a%2Fb" {
		t.Fatalf("%v %v", res, v)
	}
	if _, res, _, _ := look(r, "GET", "/files/a/b"); res != NotFound {
		t.Fatalf("a real slash split the segment: %v", res)
	}
}

func TestAWildcardDoesNotMatchAnEmptySegment(t *testing.T) {
	r := build(t, "GET /files/{name}")
	if _, res, _, _ := look(r, "GET", "/files/"); res != NotFound {
		t.Fatalf("got %v", res)
	}
}

func TestEndAnchorMatchesOnlyTheTrailingSlashPath(t *testing.T) {
	r := build(t, "GET /{$}", "GET /docs/{$}", "GET /docs/{page}")
	for path, want := range map[string]Result{"/": Found, "/x": NotFound, "/docs/": Found, "/docs": NotFound, "/docs/a": Found, "/docs/a/": NotFound} {
		if _, res, _, _ := look(r, "GET", path); res != want {
			t.Errorf("%s = %v, want %v", path, res, want)
		}
	}
	if h, _, _, _ := look(r, "GET", "/docs/"); h != "GET /docs/{$}" {
		t.Errorf("got %q", h)
	}
}

func TestARestWildcardMatchesAnEmptyRemainderButNotAMissingSlash(t *testing.T) {
	r := build(t, "GET /files/{rest...}")
	if _, res, _, v := look(r, "GET", "/files/"); res != Found || v["rest"] != "" {
		t.Fatalf("empty remainder: %v %v", res, v)
	}
	if _, res, _, v := look(r, "GET", "/files/a/b/"); res != Found || v["rest"] != "a/b/" {
		t.Fatalf("remainder: %v %v", res, v)
	}
	if _, res, _, _ := look(r, "GET", "/files"); res != NotFound {
		t.Fatalf("no slash: %v", res)
	}
}

func TestATrailingSlashPatternMountsEverythingBelowIt(t *testing.T) {
	r := build(t, "/debug/pprof/")
	for _, m := range []string{"GET", "POST", "PURGE"} {
		if h, res, _, v := look(r, m, "/debug/pprof/heap"); res != Found || h != "/debug/pprof/" || len(v) != 0 {
			t.Errorf("%s: %q %v %v", m, h, res, v)
		}
	}
	if _, res, _, _ := look(r, "GET", "/debug/pprof"); res != NotFound {
		t.Errorf("no redirect expected, got %v", res)
	}
}

func TestSlashAndNoSlashAreDifferentPaths(t *testing.T) {
	r := build(t, "GET /users")
	if _, res, _, _ := look(r, "GET", "/users/"); res != NotFound {
		t.Fatalf("got %v", res)
	}
	r = build(t, "GET /users/{$}")
	if _, res, _, _ := look(r, "GET", "/users"); res != NotFound {
		t.Fatalf("got %v", res)
	}
}

func TestANotNormalisedPathIsBadPath(t *testing.T) {
	r := build(t, "GET /{x...}")
	for _, path := range []string{"//", "/a//b", "/a//", "//a", "/./a", "/a/./b", "/a/../b", "/..", "/a/.", "/a/..", "/."} {
		if _, res, _, _ := look(r, "GET", path); res != BadPath {
			t.Errorf("%s = %v, want BadPath", path, res)
		}
	}
	for _, path := range []string{"/", "/a/", "/.a", "/a./b", "/...", "/a/%2e%2e"} {
		if _, res, _, _ := look(r, "GET", path); res != Found {
			t.Errorf("%s = %v, want Found", path, res)
		}
	}
}

func TestAPathThatDoesNotStartWithASlashIsNotFound(t *testing.T) {
	r := build(t, "GET /{x...}")
	for _, path := range []string{"", "*", "a"} {
		if _, res, _, _ := look(r, "GET", path); res != NotFound {
			t.Errorf("%q = %v", path, res)
		}
	}
}

func TestAnUnknownPathIs404AndAKnownPathWithAnotherMethodIs405(t *testing.T) {
	r := build(t, "GET /q", "POST /q", "DELETE /q/{id}", "GET /g")
	if _, res, _, _ := look(r, "GET", "/nope"); res != NotFound {
		t.Errorf("got %v", res)
	}
	for _, c := range []struct{ method, path, allow string }{
		{"PUT", "/q", "GET, HEAD, POST"},
		{"OPTIONS", "/q", "GET, HEAD, POST"},
		{"GET", "/q/7", "DELETE"},
		{"POST", "/g", "GET, HEAD"},
		{"HEAD", "/q/7", "DELETE"},
	} {
		if _, res, allow, _ := look(r, c.method, c.path); res != MethodNotAllowed || allow != c.allow {
			t.Errorf("%s %s = %v %q, want 405 %q", c.method, c.path, res, allow, c.allow)
		}
	}
}

func TestTheAllowValueUnionsEveryPatternThatMatchesThePath(t *testing.T) {
	r := build(t, "GET /a/{x}", "POST /a/b", "PUT /a/{x...}")
	if _, res, allow, _ := look(r, "DELETE", "/a/b"); res != MethodNotAllowed || allow != "GET, HEAD, POST, PUT" {
		t.Fatalf("%v %q", res, allow)
	}
}

func TestGETAnswersHEADAndAnExplicitHEADRouteWins(t *testing.T) {
	r := build(t, "GET /a", "GET /b", "HEAD /b")
	if h, res, _, _ := look(r, "HEAD", "/a"); res != Found || h != "GET /a" {
		t.Errorf("got %q %v", h, res)
	}
	if h, res, _, _ := look(r, "HEAD", "/b"); res != Found || h != "HEAD /b" {
		t.Errorf("got %q %v", h, res)
	}
	if _, res, allow, _ := look(r, "POST", "/b"); res != MethodNotAllowed || allow != "GET, HEAD" {
		t.Errorf("got %v %q", res, allow)
	}
}

func TestAMountAnswersAnyMethodAndASpecificMethodWinsOnTheSamePath(t *testing.T) {
	r := build(t, "/m/", "GET /m/")
	if h, res, _, _ := look(r, "GET", "/m/x"); res != Found || h != "GET /m/" {
		t.Errorf("got %q %v", h, res)
	}
	if h, res, _, _ := look(r, "PURGE", "/m/x"); res != Found || h != "/m/" {
		t.Errorf("got %q %v", h, res)
	}
}

func TestAMountNeverAnswers405(t *testing.T) {
	r := build(t, "/m/", "GET /other")
	if _, res, _, _ := look(r, "POST", "/m/x"); res != Found {
		t.Fatalf("got %v", res)
	}
}

func TestAddAfterFreezePanics(t *testing.T) {
	r := build(t, "GET /a")
	defer func() {
		got, _ := recover().(string)
		want := `route POST /b registered at x.go:9 after the app started serving: routes can be added only before it does`
		if got != want {
			t.Fatalf("panic %q, want %q", got, want)
		}
	}()
	r.Add("POST", "/b", "h", "x.go:9")
}

func TestFreezeTwiceIsHarmless(t *testing.T) {
	r := build(t, "GET /a")
	r.Freeze()
	if _, res, _, _ := look(r, "GET", "/a"); res != Found {
		t.Fatal(res)
	}
}

func TestAConflictNamesBothPatternsBothPlacesAndAPathBothMatch(t *testing.T) {
	for _, c := range []struct{ first, second, want string }{
		{"GET /a/{x}/b", "GET /a/b/{y}",
			`pattern "GET /a/b/{y}" (registered at new.go:2) conflicts with pattern "GET /a/{x}/b" (registered at old.go:1): both match "GET /a/b/b", and neither is more specific`},
		{"GET /a/{x}", "GET /a/{y}",
			`pattern "GET /a/{y}" (registered at new.go:2) conflicts with pattern "GET /a/{x}" (registered at old.go:1): they match the same requests`},
		{"GET /z", "GET /z",
			`pattern "GET /z" (registered at new.go:2) conflicts with pattern "GET /z" (registered at old.go:1): they match the same requests`},
		{"/a/", "/a/{rest...}",
			`pattern "/a/{rest...}" (registered at new.go:2) conflicts with pattern "/a/" (registered at old.go:1): they match the same requests`},
		{"GET /x/{id}", "/x/a",
			`pattern "/x/a" (registered at new.go:2) conflicts with pattern "GET /x/{id}" (registered at old.go:1): both match "GET /x/a", and neither is more specific`},
		{"/x/{id}", "GET /x/a", ""}, // more specific in both ways
		{"GET /a/{x}/c", "POST /a/b/c", ""},
		{"GET /a/{x}/c", "GET /a/b/{y...}",
			`pattern "GET /a/b/{y...}" (registered at new.go:2) conflicts with pattern "GET /a/{x}/c" (registered at old.go:1): both match "GET /a/b/c", and neither is more specific`},
		{"GET /a", "HEAD /a", ""},
	} {
		r := New[string]()
		add := func(s, where string) error {
			method, pattern, _ := strings.Cut(s, " ")
			if strings.HasPrefix(s, "/") {
				method, pattern = "", s
			}
			return r.Add(method, pattern, s, where)
		}
		if err := add(c.first, "old.go:1"); err != nil {
			t.Fatal(err)
		}
		err := add(c.second, "new.go:2")
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q then %q: unexpected %v", c.first, c.second, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%q then %q:\n got %v\nwant %s", c.first, c.second, err, c.want)
		}
	}
}

func TestAFailedAddLeavesTheTableAsItWas(t *testing.T) {
	r := New[string]()
	r.Add("GET", "/a/{x}", "one", "a:1")
	if r.Add("GET", "/a/{y}", "two", "a:2") == nil {
		t.Fatal("no conflict")
	}
	r.Freeze()
	if h, _, _, _ := look(r, "GET", "/a/1"); h != "one" {
		t.Fatalf("got %q", h)
	}
}

func TestEveryMalformedPatternIsRefusedWithItsReason(t *testing.T) {
	long := ""
	for i := 0; i <= MaxParams; i++ {
		long += fmt.Sprintf("/{p%d}", i)
	}
	for _, c := range []struct{ method, pattern, want string }{
		{"GET", "", `invalid pattern "" registered at p.go:1: must start with '/'`},
		{"GET", "users", `invalid pattern "users" registered at p.go:1: must start with '/'`},
		{"GET", "/a/{rest...}/b", `invalid pattern "/a/{rest...}/b" registered at p.go:1: "{rest...}" must be the last segment`},
		{"GET", "/a/{$}/b", `invalid pattern "/a/{$}/b" registered at p.go:1: "{$}" must be the last segment`},
		{"GET", "/a/{}", `invalid pattern "/a/{}" registered at p.go:1: empty wildcard name in "{}"`},
		{"GET", "/a/{...}", `invalid pattern "/a/{...}" registered at p.go:1: empty wildcard name in "{...}"`},
		{"GET", "/{x}/{x}", `invalid pattern "/{x}/{x}" registered at p.go:1: duplicate wildcard name "x"`},
		{"GET", "/{x}/{x...}", `invalid pattern "/{x}/{x...}" registered at p.go:1: duplicate wildcard name "x"`},
		{"GET", "/{1x}", `invalid pattern "/{1x}" registered at p.go:1: invalid wildcard name "1x" in "{1x}": a name is a Go identifier`},
		{"GET", "/{a-b}", `invalid pattern "/{a-b}" registered at p.go:1: invalid wildcard name "a-b" in "{a-b}": a name is a Go identifier`},
		{"GET", "/a/{x", `invalid pattern "/a/{x" registered at p.go:1: unbalanced braces in segment "{x"`},
		{"GET", "/a/x}", `invalid pattern "/a/x}" registered at p.go:1: unbalanced braces in segment "x}"`},
		{"GET", "/a/{{x}}", `invalid pattern "/a/{{x}}" registered at p.go:1: unbalanced braces in segment "{{x}}"`},
		{"GET", "/a/pre{x}", `invalid pattern "/a/pre{x}" registered at p.go:1: a wildcard must be a whole segment, got "pre{x}"`},
		{"GET", "/a/{x}post", `invalid pattern "/a/{x}post" registered at p.go:1: a wildcard must be a whole segment, got "{x}post"`},
		{"GET", "/a//b", `invalid pattern "/a//b" registered at p.go:1: empty segment ("//") never matches: such paths are refused as bad`},
		{"GET", "/a/../b", `invalid pattern "/a/../b" registered at p.go:1: segment ".." never matches: such paths are refused as bad`},
		{"GET", long, `invalid pattern "` + long + `" registered at p.go:1: more than 16 wildcards`},
		{"G ET", "/a", `invalid method "G ET" registered at p.go:1: a method is a token`},
	} {
		err := New[string]().Add(c.method, c.pattern, "h", "p.go:1")
		if err == nil || err.Error() != c.want {
			t.Errorf("%q:\n got %v\nwant %s", c.pattern, err, c.want)
		}
	}
}

func TestSixteenWildcardsAreAccepted(t *testing.T) {
	p := ""
	for i := 0; i < MaxParams; i++ {
		p += fmt.Sprintf("/{p%d}", i)
	}
	r := build(t, "GET "+p)
	path := strings.Repeat("/v", MaxParams)
	if _, res, _, v := look(r, "GET", path); res != Found || len(v) != MaxParams {
		t.Fatalf("%v %v", res, v)
	}
	// An anonymous rest after the sixteenth must not overrun Params.
	r = build(t, "GET "+p+"/")
	if _, res, _, _ := look(r, "GET", path+"/z"); res != Found {
		t.Fatalf("%v", res)
	}
}

func TestMoreDistinctMethodsThanTheAllowTableHoldsIsRefused(t *testing.T) {
	r := New[string]()
	for i := 0; i < maxMethods; i++ {
		if err := r.Add(fmt.Sprintf("M%d", i), "/a", "h", "m.go:1"); err != nil {
			t.Fatal(err)
		}
	}
	err := r.Add("EXTRA", "/a", "h", "m.go:2")
	want := `pattern "EXTRA /a" registered at m.go:2: more than 12 distinct methods in one table`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
}

func TestLookupDoesNotAllocate(t *testing.T) {
	r := build(t, "GET /users/{id}", "GET /orgs/{org}/repos/{repo}", "POST /users", "GET /files/{p...}", "/m/", "GET /x/y/z")
	var p Params
	for _, c := range [][2]string{
		{"GET", "/users/42"}, {"GET", "/orgs/a/repos/b"}, {"HEAD", "/users/42"}, {"GET", "/files/a/b/c"},
		{"PURGE", "/m/q"}, {"DELETE", "/users"}, {"GET", "/nope"}, {"GET", "/a//b"}, {"GET", "/x/y/z"},
	} {
		m, path := []byte(c[0]), []byte(c[1])
		if n := testing.AllocsPerRun(100, func() { r.Lookup(m, path, &p) }); n != 0 {
			t.Errorf("%s %s allocates %v times", c[0], c[1], n)
		}
	}
}
