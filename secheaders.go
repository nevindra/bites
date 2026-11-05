package bites

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// DefaultCSP is the Content-Security-Policy SecurityHeaders sends when it is given none: nothing
// may load and no page may frame the answer. It is right for a JSON API, whose answers are never
// meant to run as a page; an app that serves HTML gives its pages their own policy.
const DefaultCSP = "default-src 'none'; frame-ancestors 'none'"

// DefaultPermissionsPolicy is the Permissions-Policy SecurityHeaders sends when it is given none:
// the three features whose prompts an injected script could raise, off for this document and every
// frame in it. It is the value the OWASP Secure Headers Project recommends (ADR 0053).
const DefaultPermissionsPolicy = "geolocation=(), camera=(), microphone=()"

// SecurityHeadersOptions configures SecurityHeaders (ADR 0053). The zero value is the safe one.
type SecurityHeadersOptions struct {
	// CSP is the Content-Security-Policy; DefaultCSP when empty. It must have a frame-ancestors
	// directive, which also decides X-Frame-Options: 'none' sends DENY, 'self' sends SAMEORIGIN,
	// and a list of origins sends none, since X-Frame-Options cannot name them.
	CSP string

	// ReferrerPolicy is the Referrer-Policy; "no-referrer" when empty. It must be one of the
	// policies of the W3C Referrer Policy specification.
	ReferrerPolicy string

	// HSTSMaxAge is the max-age of Strict-Transport-Security, in whole seconds; 365 days when
	// zero. HSTS is sent only on an answer whose Request.Scheme is https.
	HSTSMaxAge time.Duration

	// HSTSIncludeSubdomains adds includeSubDomains, which pins every subdomain of the host to
	// HTTPS too. Off by default: it reaches hosts this app does not serve.
	HSTSIncludeSubdomains bool

	// HSTSPreload adds preload, the request to be put in the browsers' built-in list. It is
	// refused without HSTSIncludeSubdomains and a max-age of at least a year, which the list asks.
	HSTSPreload bool

	// NoHSTS leaves Strict-Transport-Security out, for an app whose balancer already sends it.
	NoHSTS bool

	// PermissionsPolicy is the Permissions-Policy, a Structured Fields dictionary of
	// feature=allowlist members; DefaultPermissionsPolicy when empty. It is checked for the shape
	// (members of the form name=value, parentheses and quotes closed), not for feature names:
	// a browser skips the names it does not know.
	PermissionsPolicy string

	// NoPermissionsPolicy leaves Permissions-Policy out, for a page group that needs the camera,
	// the microphone or the location and sets its own. It is refused with PermissionsPolicy.
	NoPermissionsPolicy bool

	// CrossOriginOpenerPolicy is the Cross-Origin-Opener-Policy: "same-origin",
	// "same-origin-allow-popups" or "unsafe-none". Not sent when empty. "same-origin" cuts the
	// opener's reference to every cross-origin popup, which breaks an OAuth or payment popup;
	// "same-origin-allow-popups" keeps it.
	CrossOriginOpenerPolicy string

	// CrossOriginEmbedderPolicy is the Cross-Origin-Embedder-Policy: "require-corp",
	// "credentialless" or "unsafe-none". Not sent when empty. With a "same-origin" opener policy
	// it makes the page cross-origin isolated; "require-corp" then blocks every cross-origin
	// image, script or frame that does not send Cross-Origin-Resource-Policy: cross-origin.
	CrossOriginEmbedderPolicy string

	// CrossOriginResourcePolicy is the Cross-Origin-Resource-Policy: "same-site" when empty,
	// "same-origin" or "cross-origin". It decides who may load an answer as an image, script or
	// frame (a no-cors request); a fetch under CORS is decided by CORS alone. "cross-origin" is
	// for answers meant to be embedded by other sites, and for a page that sends "require-corp".
	CrossOriginResourcePolicy string
}

var referrerPolicies = []string{
	"no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin",
	"same-origin", "strict-origin", "strict-origin-when-cross-origin", "unsafe-url",
}

// SecurityHeaders is the middleware of ADR 0053. It adds to every answer, errors included:
//
//   - Content-Security-Policy, DefaultCSP unless CSP is given;
//   - X-Frame-Options, DENY or SAMEORIGIN as the CSP's frame-ancestors says, for browsers that
//     predate frame-ancestors;
//   - X-Content-Type-Options: nosniff, so a browser never runs a JSON answer as a script;
//   - Referrer-Policy, no-referrer unless another is given;
//   - Permissions-Policy, DefaultPermissionsPolicy unless given or NoPermissionsPolicy;
//   - Cross-Origin-Resource-Policy, same-site unless another is given;
//   - Cross-Origin-Opener-Policy and Cross-Origin-Embedder-Policy, only when given: both change
//     what a page may open and load, so a default would break pages the middleware cannot see;
//   - Strict-Transport-Security, max-age one year, when Request.Scheme is https: over TLS the
//     app terminated, or from a trusted proxy that says so (ADR 0025). A browser ignores it over
//     plain HTTP, and sending it there would only tell a reader the app means HTTPS.
//
// It is app middleware for an API, or a group's for a set of pages with a policy of their own: a
// header a handler sets is invisible to middleware (ADR 0022), so a route that sends its own CSP
// next to this middleware gets both, and a browser enforces both.
//
// It allocates nothing: every value is built when the middleware is made. It panics when more
// than one options value is given, the CSP has no frame-ancestors or a byte a header cannot carry,
// the referrer, opener, embedder or resource policy is not one of its values, the
// Permissions-Policy is not a dictionary of name=value members or is given with
// NoPermissionsPolicy, the HSTS max-age is negative or not whole seconds, preload is
// asked without includeSubDomains and a year, or NoHSTS is given with an HSTS field.
func SecurityHeaders(opts ...SecurityHeadersOptions) Middleware {
	var o SecurityHeadersOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseMiddleware("SecurityHeaders", "%d options values were given; give one SecurityHeadersOptions", len(opts))
	}
	csp := o.CSP
	if csp == "" {
		csp = DefaultCSP
	}
	for i := 0; i < len(csp); i++ {
		if c := csp[i]; c < 0x20 || c > 0x7e {
			refuseMiddleware("SecurityHeaders", "CSP has the byte %#x at %d, which a header value cannot carry", c, i)
		}
	}
	ancestors, ok := frameAncestors(csp)
	if !ok {
		refuseMiddleware("SecurityHeaders", "CSP %q has no frame-ancestors directive, so any site could frame these answers: add frame-ancestors 'none', 'self', or the origins that may frame them", csp)
	}
	frame := ""
	switch ancestors {
	case "'none'":
		frame = "DENY"
	case "'self'":
		frame = "SAMEORIGIN"
	}
	ref := o.ReferrerPolicy
	if ref == "" {
		ref = "no-referrer"
	}
	known := false
	for _, p := range referrerPolicies {
		known = known || p == ref
	}
	if !known {
		refuseMiddleware("SecurityHeaders", "ReferrerPolicy %q is not a referrer policy: use one of %s", o.ReferrerPolicy, strings.Join(referrerPolicies, ", "))
	}
	pp := o.PermissionsPolicy
	switch {
	case o.NoPermissionsPolicy:
		if pp != "" {
			refuseMiddleware("SecurityHeaders", "NoPermissionsPolicy is given together with PermissionsPolicy: keep one of the two")
		}
	case pp == "":
		pp = DefaultPermissionsPolicy
	default:
		if why := badPermissionsPolicy(pp); why != "" {
			refuseMiddleware("SecurityHeaders", "PermissionsPolicy %q %s: write feature=(allowlist) members separated by commas, as in %q", pp, why, DefaultPermissionsPolicy)
		}
	}
	coop := checkPolicy("CrossOriginOpenerPolicy", o.CrossOriginOpenerPolicy, "", "same-origin", "same-origin-allow-popups", "unsafe-none")
	coep := checkPolicy("CrossOriginEmbedderPolicy", o.CrossOriginEmbedderPolicy, "", "require-corp", "credentialless", "unsafe-none")
	corp := checkPolicy("CrossOriginResourcePolicy", o.CrossOriginResourcePolicy, "same-site", "same-site", "same-origin", "cross-origin")
	hsts := ""
	if o.NoHSTS {
		if o.HSTSMaxAge != 0 || o.HSTSIncludeSubdomains || o.HSTSPreload {
			refuseMiddleware("SecurityHeaders", "NoHSTS is given together with an HSTS field: keep one of the two")
		}
	} else {
		age := o.HSTSMaxAge
		if age < 0 || age%time.Second != 0 {
			refuseMiddleware("SecurityHeaders", "HSTSMaxAge is %v, which is not a whole number of seconds: give zero for a year, or whole seconds", age)
		}
		if age == 0 {
			age = 365 * 24 * time.Hour
		}
		if o.HSTSPreload && (!o.HSTSIncludeSubdomains || age < 365*24*time.Hour) {
			refuseMiddleware("SecurityHeaders", "HSTSPreload needs HSTSIncludeSubdomains and an HSTSMaxAge of at least a year, or the preload list refuses the host")
		}
		hsts = "max-age=" + strconv.FormatInt(int64(age/time.Second), 10)
		if o.HSTSIncludeSubdomains {
			hsts += "; includeSubDomains"
		}
		if o.HSTSPreload {
			hsts += "; preload"
		}
	}
	return func(ctx context.Context, req Request, next Next) (Response, error) {
		res := next.Call(ctx)
		res.AddHeader("Content-Security-Policy", csp)
		if frame != "" {
			res.AddHeader("X-Frame-Options", frame)
		}
		res.AddHeader("X-Content-Type-Options", "nosniff")
		res.AddHeader("Referrer-Policy", ref)
		if pp != "" {
			res.AddHeader("Permissions-Policy", pp)
		}
		res.AddHeader("Cross-Origin-Resource-Policy", corp)
		if coop != "" {
			res.AddHeader("Cross-Origin-Opener-Policy", coop)
		}
		if coep != "" {
			res.AddHeader("Cross-Origin-Embedder-Policy", coep)
		}
		if hsts != "" && req.Scheme() == "https" {
			res.AddHeader("Strict-Transport-Security", hsts)
		}
		return res, nil
	}
}

// checkPolicy returns v, or def when v is empty, and refuses a v that is not one of values.
func checkPolicy(field, v, def string, values ...string) string {
	if v == "" {
		return def
	}
	for _, w := range values {
		if v == w {
			return v
		}
	}
	refuseMiddleware("SecurityHeaders", "%s %q is not one of %s", field, v, strings.Join(values, ", "))
	return ""
}

// badPermissionsPolicy says what is wrong with a Permissions-Policy value, or "" when it is a
// dictionary of name=value members: printable ASCII, every top-level comma-separated member
// starting with a lower-case token and "=", parentheses and quotes closed.
func badPermissionsPolicy(v string) string {
	depth, quoted, start := 0, false, 0
	member := func(end int) string {
		m := strings.TrimSpace(v[start:end])
		eq := strings.IndexByte(m, '=')
		if eq <= 0 || eq == len(m)-1 {
			return "has a member that is not name=value"
		}
		for i := 0; i < eq; i++ {
			c := m[i]
			if !(c >= 'a' && c <= 'z' || i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '*')) {
				return "has a feature name that is not a lower-case token"
			}
		}
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c > 0x7e {
			return "has a byte a header value cannot carry"
		}
		switch {
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return "closes a parenthesis it did not open"
			}
		case c == ',' && depth == 0:
			if why := member(i); why != "" {
				return why
			}
			start = i + 1
		}
	}
	if quoted || depth != 0 {
		return "leaves a quote or parenthesis open"
	}
	return member(len(v))
}

// frameAncestors returns the source list of the policy's frame-ancestors directive, its
// whitespace collapsed, and whether there is one.
func frameAncestors(csp string) (string, bool) {
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) > 0 && strings.EqualFold(f[0], "frame-ancestors") {
			return strings.Join(f[1:], " "), true
		}
	}
	return "", false
}
