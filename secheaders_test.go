package bites_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func headersApp(o bites.SecurityHeadersOptions, opts ...bites.Option) func(*testing.T) *bitetest.Server {
	return func(t *testing.T) *bitetest.Server {
		return startWith(t, func(app *bites.App) {
			app.Use(bites.SecurityHeaders(o))
			app.Get("/users/{id}", getUser)
		}, opts...)
	}
}

func TestSecurityHeadersWithNoOptionsAreTheSafeDefaultsOfAJSONAPI(t *testing.T) {
	srv := startWith(t, func(app *bites.App) {
		app.Use(bites.SecurityHeaders())
		app.Get("/users/{id}", getUser)
	})
	r := srv.Do(req("GET", "/users/7"))
	for k, v := range map[string]string{
		"content-security-policy": "default-src 'none'; frame-ancestors 'none'",
		"x-frame-options":         "DENY",
		"x-content-type-options":  "nosniff",
		"referrer-policy":         "no-referrer",
	} {
		if got := r.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if r.Get("strict-transport-security") != "" {
		t.Errorf("HSTS over plain HTTP: %v", r.Header)
	}
	if got := r.Get("permissions-policy"); got != "geolocation=(), camera=(), microphone=()" {
		t.Errorf("permissions-policy = %q", got)
	}
	if got := r.Get("cross-origin-resource-policy"); got != "same-site" {
		t.Errorf("cross-origin-resource-policy = %q", got)
	}
}

func TestTheDefaultsLeaveOutTheOpenerAndEmbedderPoliciesSoPopupsAndThirdPartyImagesKeepWorking(t *testing.T) {
	r := headersApp(bites.SecurityHeadersOptions{})(t).Do(req("GET", "/users/7"))
	for _, k := range []string{"cross-origin-opener-policy", "cross-origin-embedder-policy"} {
		if got := r.Get(k); got != "" {
			t.Errorf("%s = %q by default; it would break an OAuth popup or a cross-origin image", k, got)
		}
	}
}

func TestTheCrossOriginPoliciesAreSentAsGiven(t *testing.T) {
	r := headersApp(bites.SecurityHeadersOptions{
		PermissionsPolicy:         `camera=(self "https://meet.example"), fullscreen=*`,
		CrossOriginOpenerPolicy:   "same-origin-allow-popups",
		CrossOriginEmbedderPolicy: "credentialless",
		CrossOriginResourcePolicy: "cross-origin",
	})(t).Do(req("GET", "/users/7"))
	for k, v := range map[string]string{
		"permissions-policy":           `camera=(self "https://meet.example"), fullscreen=*`,
		"cross-origin-opener-policy":   "same-origin-allow-popups",
		"cross-origin-embedder-policy": "credentialless",
		"cross-origin-resource-policy": "cross-origin",
	} {
		if got := r.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestNoPermissionsPolicyLeavesItOut(t *testing.T) {
	r := headersApp(bites.SecurityHeadersOptions{NoPermissionsPolicy: true})(t).Do(req("GET", "/users/7"))
	if got := r.Get("permissions-policy"); got != "" {
		t.Fatalf("permissions-policy = %q", got)
	}
}

func TestACORSFetchFromAnotherOriginIsNotTheBusinessOfTheResourcePolicy(t *testing.T) {
	// The default same-site policy is on the answer a CORS preflight and a CORS fetch get too;
	// the browser checks it only for no-cors loads (Fetch, cross-origin resource policy check),
	// so a SPA on another origin that reads this API under CORS is not stopped by it.
	srv := startWith(t, func(app *bites.App) {
		app.Use(bites.CORS(bites.CORSOptions{Origins: []string{"https://spa.example"}}), bites.SecurityHeaders())
		app.Get("/users/{id}", getUser)
	})
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"Origin": {"https://spa.example"}}})
	if r.Get("access-control-allow-origin") != "https://spa.example" || r.Get("cross-origin-resource-policy") != "same-site" {
		t.Fatalf("%v", r.Header)
	}
}

func TestSecurityHeadersAreOnErrorAnswersToo(t *testing.T) {
	srv := headersApp(bites.SecurityHeadersOptions{})(t)
	for _, target := range []string{"/nope", "/users/abc"} {
		if r := srv.Do(req("GET", target)); r.Status < 400 || r.Get("x-content-type-options") != "nosniff" || r.Get("content-security-policy") == "" {
			t.Errorf("%s: %d %v", target, r.Status, r.Header)
		}
	}
}

func TestHSTSIsSentOnlyWhenTheSchemeIsHttpsFromATrustedProxy(t *testing.T) {
	srv := headersApp(bites.SecurityHeadersOptions{HSTSIncludeSubdomains: true},
		bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}))(t)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Remote: "10.0.0.5:4000", Header: map[string][]string{"X-Forwarded-Proto": {"https"}}})
	if got := r.Get("strict-transport-security"); got != "max-age=31536000; includeSubDomains" {
		t.Fatalf("HSTS = %q", got)
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"X-Forwarded-Proto": {"https"}}})
	if got := r.Get("strict-transport-security"); got != "" {
		t.Fatalf("an untrusted client earned HSTS by saying https: %q", got)
	}
}

func TestTheFrameOptionsFollowTheCSPsFrameAncestors(t *testing.T) {
	for csp, want := range map[string]string{
		"default-src 'self'; frame-ancestors 'self'":                  "SAMEORIGIN",
		"default-src 'self'; FRAME-ANCESTORS   'none'":                "DENY",
		"default-src 'self'; frame-ancestors https://partner.example": "",
	} {
		srv := headersApp(bites.SecurityHeadersOptions{CSP: csp, ReferrerPolicy: "strict-origin-when-cross-origin"})(t)
		r := srv.Do(req("GET", "/users/7"))
		if r.Get("x-frame-options") != want || r.Get("content-security-policy") != csp || r.Get("referrer-policy") != "strict-origin-when-cross-origin" {
			t.Errorf("%q: %v", csp, r.Header)
		}
	}
}

func TestNoHSTSLeavesItOutAndPreloadIsWrittenWhenAsked(t *testing.T) {
	trusted := bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	https := bitetest.Request{Method: "GET", Target: "/users/7", Remote: "10.0.0.5:4000", Header: map[string][]string{"X-Forwarded-Proto": {"https"}}}
	if r := headersApp(bites.SecurityHeadersOptions{NoHSTS: true}, trusted)(t).Do(https); r.Get("strict-transport-security") != "" {
		t.Fatalf("%v", r.Header)
	}
	r := headersApp(bites.SecurityHeadersOptions{HSTSMaxAge: 2 * 365 * 24 * time.Hour, HSTSIncludeSubdomains: true, HSTSPreload: true}, trusted)(t).Do(https)
	if got := r.Get("strict-transport-security"); got != "max-age=63072000; includeSubDomains; preload" {
		t.Fatalf("%q", got)
	}
}
