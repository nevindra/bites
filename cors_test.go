package bites_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func corsApp(o bites.CORSOptions) func(*bites.App) {
	return func(app *bites.App) {
		app.Use(bites.CORS(o))
		app.Get("/users/{id}", getUser)
	}
}

func from(origin, method, target string, h ...string) bitetest.Request {
	r := bitetest.Request{Method: method, Target: target, Header: map[string][]string{"Origin": {origin}}}
	for i := 0; i+1 < len(h); i += 2 {
		r.Header[h[i]] = append(r.Header[h[i]], h[i+1])
	}
	return r
}

func hasVary(r bitetest.Response, want string) bool {
	for _, v := range r.Header["vary"] {
		if v == want {
			return true
		}
	}
	return false
}

var appOrigin = bites.CORSOptions{Origins: []string{"https://app.example.com", "http://localhost:5173"}}

func TestACORSSimpleRequestFromAListedOriginGetsItsOriginBackAndVaryOrigin(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	r := srv.Do(from("http://localhost:5173", "GET", "/users/7"))
	if r.Status != 200 || r.Get("access-control-allow-origin") != "http://localhost:5173" || !hasVary(r, "Origin") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r.Get("access-control-allow-credentials") != "" || r.Get("access-control-expose-headers") != "" {
		t.Fatalf("headers that were not configured: %v", r.Header)
	}
}

func TestACORSRequestFromAnUnlistedOriginIsServedWithoutTheHeadersAndWithVaryOrigin(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	r := srv.Do(from("https://evil.example", "GET", "/users/7"))
	if r.Status != 200 || r.Get("access-control-allow-origin") != "" || !hasVary(r, "Origin") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestACORSRequestWithNoOriginIsUntouchedExceptForVaryOrigin(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	r := srv.Do(req("GET", "/users/7"))
	if r.Status != 200 || r.Get("access-control-allow-origin") != "" || !hasVary(r, "Origin") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestACORSOriginIsMatchedExactlyNotByPrefixOrSuffix(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	for _, o := range []string{"https://app.example.com.evil.example", "https://evil-app.example.com", "http://app.example.com", "https://app.example.com:8443", "null"} {
		if r := srv.Do(from(o, "GET", "/users/7")); r.Get("access-control-allow-origin") != "" {
			t.Errorf("%s was allowed: %v", o, r.Header)
		}
	}
}

func TestACORSPreflightIsAnsweredByTheMiddlewareAndNeverReachesTheRoute(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{
		Origins: []string{"https://app.example.com"}, Methods: []string{"GET", "PUT"}, Headers: []string{"Content-Type", "Authorization"},
		Credentials: true, MaxAge: 10 * time.Minute,
	}))
	// PUT /users/7 has no route: the preflight is answered before the router would say 405.
	r := srv.Do(from("https://app.example.com", "OPTIONS", "/users/7",
		"Access-Control-Request-Method", "PUT", "Access-Control-Request-Headers", "content-type, Authorization"))
	if r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	want := map[string]string{
		"access-control-allow-origin":      "https://app.example.com",
		"access-control-allow-credentials": "true",
		"access-control-allow-methods":     "GET, PUT",
		"access-control-allow-headers":     "Content-Type, Authorization",
		"access-control-max-age":           "600",
	}
	for k, v := range want {
		if got := r.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !hasVary(r, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers") {
		t.Errorf("vary: %v", r.Header["vary"])
	}
}

func TestACORSPreflightThatIsNotAllowedIsA204WithNoAllowHeaders(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://app.example.com"}, Headers: []string{"Content-Type"}}))
	for name, r := range map[string]bitetest.Request{
		"an unlisted origin":    from("https://evil.example", "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET"),
		"a method not allowed":  from("https://app.example.com", "OPTIONS", "/users/7", "Access-Control-Request-Method", "DELETE"),
		"a header not allowed":  from("https://app.example.com", "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "content-type, x-secret"),
		"a header not safelist": from("https://app.example.com", "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "authorization"),
	} {
		got := srv.Do(r)
		if got.Status != 204 || got.Get("access-control-allow-origin") != "" || got.Get("access-control-allow-methods") != "" || !hasVary(got, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers") {
			t.Errorf("%s: %d %v", name, got.Status, got.Header)
		}
	}
}

func TestAnOPTIONSRequestWithNoRequestMethodIsNotAPreflightAndGoesToTheRouter(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	r := srv.Do(from("https://app.example.com", "OPTIONS", "/users/7"))
	wantProblem(t, r, 405, `{"title":"Method Not Allowed","status":405}`)
	if r.Get("access-control-allow-origin") != "https://app.example.com" {
		t.Fatalf("%v", r.Header)
	}
}

func TestCORSHeadersAreOnEveryErrorAnswerSoABrowserCanReadIt(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	o := "https://app.example.com"
	for target, status := range map[string]int{"/nope": 404, "/users/abc": 400} {
		r := srv.Do(from(o, "GET", target))
		if r.Status != status || r.Get("content-type") != "application/problem+json" || r.Get("access-control-allow-origin") != o || !hasVary(r, "Origin") {
			t.Errorf("GET %s: %d %v", target, r.Status, r.Header)
		}
	}
	r := srv.Do(from(o, "POST", "/users/7"))
	if r.Status != 405 || r.Get("allow") == "" || r.Get("access-control-allow-origin") != o {
		t.Errorf("405: %d %v", r.Status, r.Header)
	}
}

func TestACORSWithCredentialsSaysSoAndExposesTheConfiguredHeaders(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://app.example.com"}, Credentials: true, ExposeHeaders: []string{"X-Request-ID", "ETag"}}))
	r := srv.Do(from("https://app.example.com", "GET", "/users/7"))
	if r.Get("access-control-allow-credentials") != "true" || r.Get("access-control-expose-headers") != "X-Request-ID, ETag" {
		t.Fatalf("%v", r.Header)
	}
}

func TestACORSForAnyOriginAnswersAStarWithoutVary(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"*"}, Headers: []string{"*"}}))
	r := srv.Do(from("https://anywhere.example", "GET", "/users/7"))
	if r.Get("access-control-allow-origin") != "*" || len(r.Header["vary"]) != 0 {
		t.Fatalf("%v", r.Header)
	}
	p := srv.Do(from("https://anywhere.example", "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "x-anything, x-else"))
	if p.Status != 204 || p.Get("access-control-allow-origin") != "*" || p.Get("access-control-allow-headers") != "*" {
		t.Fatalf("%d %v", p.Status, p.Header)
	}
}

func TestACORSMethodsDefaultToGetHeadAndPost(t *testing.T) {
	srv := start(t, corsApp(appOrigin))
	r := srv.Do(from("https://app.example.com", "OPTIONS", "/users/7", "Access-Control-Request-Method", "POST"))
	if r.Get("access-control-allow-methods") != "GET, HEAD, POST" || strings.Contains(r.Get("access-control-allow-headers"), "x") {
		t.Fatalf("%v", r.Header)
	}
	if r.Get("access-control-max-age") != "" {
		t.Fatalf("a max age nobody configured: %v", r.Header)
	}
}

func allowed(t *testing.T, srv *bitetest.Server, origin string) bool {
	t.Helper()
	r := srv.Do(from(origin, "GET", "/users/7"))
	if r.Status != 200 || !hasVary(r, "Origin") {
		t.Fatalf("%s: %d %v", origin, r.Status, r.Header)
	}
	got := r.Get("access-control-allow-origin")
	if got != "" && got != origin {
		t.Fatalf("%s: allowed as %q, want the origin echoed", origin, got)
	}
	return got != ""
}

func TestAWildcardOriginAllowsEverySubdomainButNeverTheApexOrALookalike(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://*.example.com", "https://*.shop.io:8443"}}))
	for _, o := range []string{"https://a.example.com", "https://a.b.example.com", "https://tenant-1.example.com", "https://a_b.example.com", "https://x.shop.io:8443"} {
		if !allowed(t, srv, o) {
			t.Errorf("%s was refused", o)
		}
	}
	for _, o := range []string{
		"https://example.com",               // the apex is listed on its own
		"https://evilexample.com",           // a suffix check without the dot
		"https://example.com.evil.com",      // the name only as a prefix
		"https://a.example.com.",            // a trailing dot
		"https://a..example.com",            // an empty label
		"https://.example.com",              // an empty leftmost label
		"https://a.example.com/",            // a path
		"https://a.example.com/x",           // a path
		"https://evil.com/.example.com",     // the suffix hidden in a path
		"https://evil.com#.example.com",     // the suffix hidden in a fragment
		"https://evil.com?.example.com",     // the suffix hidden in a query
		"https://user@a.example.com",        // userinfo
		"https://evil.com@a.example.com",    // userinfo
		"https://a.example.com@evil.com",    // userinfo
		"https://evil.com:80@a.example.com", // userinfo with a port
		"https://a b.example.com",           // a space
		"https://A.example.com",             // upper case is not what a browser sends
		"https://a.EXAMPLE.com",             // upper case
		"HTTPS://a.example.com",             // upper case scheme
		"null",                              // a sandboxed page
		"http://a.example.com",              // another scheme
		"https://a.example.com:8443",        // another port
		"https://a.example.com:443",         // another port
		"https://a.shop.io",                 // the port is part of the entry
		"https://x.shop.io:8444",            // another port
		"https://x.shop.io:8443.evil.com",   // the port as a label
		"ftp://a.example.com",               // another scheme
		"//a.example.com",                   // no scheme
		"a.example.com",                     // no scheme
		"https://a.example.com:",            // an empty port
		"https://é.example.com",             // not ASCII
	} {
		if allowed(t, srv, o) {
			t.Errorf("%q was allowed", o)
		}
	}
}

func TestAWildcardOriginIsMatchedOnTheSchemeItIsWrittenWith(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"http://*.example.com"}}))
	if !allowed(t, srv, "http://a.example.com") || allowed(t, srv, "https://a.example.com") {
		t.Fatal("the scheme was not matched exactly")
	}
}

func TestAWildcardOriginAnswersAPreflightAndASimpleRequestAndSaysVaryOrigin(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://*.example.com"}, Headers: []string{"Content-Type"}, Credentials: true, ExposeHeaders: []string{"ETag"}}))
	o := "https://acme.example.com"
	r := srv.Do(from(o, "GET", "/users/7"))
	if r.Get("access-control-allow-origin") != o || r.Get("access-control-allow-credentials") != "true" || r.Get("access-control-expose-headers") != "ETag" || !hasVary(r, "Origin") {
		t.Fatalf("simple: %v", r.Header)
	}
	p := srv.Do(from(o, "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "content-type"))
	if p.Status != 204 || p.Get("access-control-allow-origin") != o || p.Get("access-control-allow-credentials") != "true" || p.Get("access-control-allow-headers") != "Content-Type" ||
		!hasVary(p, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers") {
		t.Fatalf("preflight: %d %v", p.Status, p.Header)
	}
	bad := srv.Do(from("https://example.com", "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET"))
	if bad.Status != 204 || bad.Get("access-control-allow-origin") != "" || !hasVary(bad, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers") {
		t.Fatalf("apex preflight: %d %v", bad.Status, bad.Header)
	}
	if none := srv.Do(req("GET", "/users/7")); !hasVary(none, "Origin") || none.Get("access-control-allow-origin") != "" {
		t.Fatalf("no origin: %v", none.Header)
	}
}

func TestAWildcardOriginAndAListedOriginWorkTogether(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://example.com", "https://*.example.com"}}))
	if !allowed(t, srv, "https://example.com") || !allowed(t, srv, "https://www.example.com") || allowed(t, srv, "https://example.org") {
		t.Fatal("apex and subdomains were not both allowed")
	}
}

func TestAllowOriginIsAskedOnlyForAnOriginNoEntryMatchedAndNeverForNullOrAMalformedOne(t *testing.T) {
	var asked []string
	var mu sync.Mutex
	srv := start(t, corsApp(bites.CORSOptions{
		Origins: []string{"https://app.example.com", "https://*.example.org"},
		AllowOrigin: func(o string) bool {
			mu.Lock()
			asked = append(asked, o)
			mu.Unlock()
			return o == "https://tenant.example.net"
		},
	}))
	for _, o := range []string{"https://app.example.com", "https://a.example.org", "null", "https://A.example.net", "https://tenant.example.net/x", "https://u@tenant.example.net", "tenant.example.net"} {
		allowed(t, srv, o)
	}
	if len(asked) != 0 {
		t.Fatalf("asked about %q", asked)
	}
	if !allowed(t, srv, "https://tenant.example.net") || allowed(t, srv, "https://other.example.net") {
		t.Fatal("AllowOrigin's answer was not followed")
	}
	if len(asked) != 2 || asked[0] != "https://tenant.example.net" || asked[1] != "https://other.example.net" {
		t.Fatalf("asked %q", asked)
	}
}

func TestAnOriginAllowedByAllowOriginIsEchoedWithCredentialsAndOnAPreflight(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{
		AllowOrigin: func(o string) bool { return o == "https://shop.customer.io" },
		Credentials: true, Headers: []string{"Authorization"},
	}))
	o := "https://shop.customer.io"
	r := srv.Do(from(o, "GET", "/users/7"))
	if r.Get("access-control-allow-origin") != o || r.Get("access-control-allow-credentials") != "true" || !hasVary(r, "Origin") {
		t.Fatalf("simple: %v", r.Header)
	}
	p := srv.Do(from(o, "OPTIONS", "/users/7", "Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "authorization"))
	if p.Status != 204 || p.Get("access-control-allow-origin") != o || p.Get("access-control-allow-credentials") != "true" || !hasVary(p, "Origin, Access-Control-Request-Method, Access-Control-Request-Headers") {
		t.Fatalf("preflight: %d %v", p.Status, p.Header)
	}
	n := srv.Do(from("https://shop.other.io", "GET", "/users/7"))
	if n.Get("access-control-allow-origin") != "" || n.Get("access-control-allow-credentials") != "" || !hasVary(n, "Origin") {
		t.Fatalf("refused: %v", n.Header)
	}
	if none := srv.Do(req("GET", "/users/7")); !hasVary(none, "Origin") {
		t.Fatalf("no origin: %v", none.Header)
	}
}

func TestAWildcardOriginWithCredentialsEchoesTheExactOriginNeverAStar(t *testing.T) {
	srv := start(t, corsApp(bites.CORSOptions{Origins: []string{"https://*.example.com"}, Credentials: true}))
	r := srv.Do(from("https://a.example.com", "GET", "/users/7"))
	if r.Get("access-control-allow-origin") != "https://a.example.com" || r.Get("access-control-allow-credentials") != "true" {
		t.Fatalf("%v", r.Header)
	}
}

func TestCORSWithPathsCoversOnlyThoseAndTheRestOfTheAppIsUntouched(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Use(bites.CORS(bites.CORSOptions{Origins: []string{"https://app.example.com"}, Methods: []string{"GET", "PUT"}, Paths: []string{"/api"}}))
		app.Get("/api/users/{id}", getUser)
		app.Get("/api", getUserRoot)
		app.Get("/apis", getUserRoot)
		app.Get("/page", getUserRoot)
	})
	pre := func(target string) bitetest.Response {
		return srv.Do(from("https://app.example.com", "OPTIONS", target, "Access-Control-Request-Method", "PUT"))
	}
	if r := pre("/api/users/7"); r.Status != 204 || r.Get("access-control-allow-methods") != "GET, PUT" {
		t.Fatalf("a preflight under /api: %d %v", r.Status, r.Header)
	}
	if r := srv.Do(from("https://app.example.com", "GET", "/api")); r.Get("access-control-allow-origin") != "https://app.example.com" {
		t.Fatalf("/api itself: %v", r.Header)
	}
	for _, p := range []string{"/apis", "/page"} {
		if r := pre(p); r.Status != 405 || r.Get("access-control-allow-origin") != "" {
			t.Errorf("a preflight to %s reaches the router: %d %v", p, r.Status, r.Header)
		}
		if r := srv.Do(from("https://app.example.com", "GET", p)); r.Status != 200 || r.Get("access-control-allow-origin") != "" || hasVary(r, "Origin") {
			t.Errorf("GET %s has no CORS header: %d %v", p, r.Status, r.Header)
		}
	}
}

func getUserRoot(context.Context, struct{}) (string, error) { return "ok", nil }
