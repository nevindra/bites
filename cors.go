package bites

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CORSOptions configures CORS (ADR 0032).
type CORSOptions struct {
	// Origins are the origins allowed to call the API, each in full: scheme, host and, when it is
	// not the default, port ("https://app.example.com", "http://localhost:5173"), with no
	// trailing slash and no path. An entry may be a wildcard subdomain, "https://*.example.com"
	// (with a port when needed, "https://*.example.com:8443"): the "*." is the whole leftmost
	// label and stands for one or more labels (a.example.com, a.b.example.com), never for none,
	// so the apex "https://example.com" is listed separately; the scheme and the port are
	// matched as written, and what follows "*." must have at least two labels. The check does not
	// know public suffixes, so "https://*.co.uk" is accepted and allows every site under .co.uk:
	// name a domain you control. There is no other
	// pattern. The single entry "*" allows any origin and is refused together with a wildcard
	// entry, with AllowOrigin and with Credentials. Origins or AllowOrigin must decide at least
	// one origin: Origins empty and AllowOrigin nil is refused.
	Origins []string

	// AllowOrigin decides an origin that Origins does not list, for origins known only at run
	// time (a tenant's own domain looked up in a database). It is called only for an origin
	// that no entry of Origins matched, exact or wildcard, and never for "null" or for an
	// origin that is not scheme://host[:port] in lower case; true allows it exactly as a listed
	// origin is allowed: the origin is echoed back, never "*", and Credentials applies. Nil
	// asks nothing more than Origins. It is called on the request's goroutine and must be safe
	// to call concurrently; keep it fast, since a preflight and every simple request from an
	// unlisted origin reach it. Compare the whole origin against what you hold: a suffix check
	// written by hand ("ends with example.com") lets "https://evilexample.com" in. A panic in it
	// is a request panic like any middleware's. It is refused together with the entry "*".
	AllowOrigin func(origin string) bool

	// Methods are the methods a preflight may ask for; GET, HEAD and POST when empty. Naming a
	// method here allows the browser to send it; whether the route exists is the router's.
	Methods []string

	// Headers are the request headers beyond the safelisted ones that a preflight may ask for
	// (a JSON body needs "Content-Type", a token "Authorization"). None when empty. The single
	// entry "*" allows any header and is refused together with Credentials, because a browser
	// reads "*" literally when credentials are sent.
	Headers []string

	// ExposeHeaders are the response headers a script may read beyond the safelisted ones.
	ExposeHeaders []string

	// Credentials allows cookies and authorization headers on cross-origin requests
	// (Access-Control-Allow-Credentials: true).
	Credentials bool

	// MaxAge is how long a browser may cache a preflight answer, in whole seconds; the browser's
	// default (5 s in Chrome, 5 s in Firefox) when zero. Browsers cap it (2 h in Chrome).
	MaxAge time.Duration

	// Paths limits CORS to the requests whose path is one of them or below one ("/api" covers
	// /api and /api/files, not /apis); every path when empty. A request outside them is passed on
	// untouched, so a preflight there reaches the router (a 405 or a 404) and an answer gets no
	// CORS header: the pages of a site and the API it serves can share one app. Each entry starts
	// with a slash, does not end with one and has no wildcard, and "/" is refused, since it is
	// every path: leave Paths empty for that. It is how CORS is given to part of an app, since a
	// preflight never reaches a group's or a route's middleware.
	Paths []string
}

// CORS is the cross-origin middleware of ADR 0032. It is app middleware (app.Use), because a
// preflight is an OPTIONS request that no route has and ADR 0048 runs app middleware before
// routing; given to a group or a route it would never see one, so that is refused at
// Registration, and Paths is how it covers only part of the app.
//
//   - A preflight (OPTIONS with Origin and Access-Control-Request-Method) is answered here with a
//     204 and never reaches the router. It is allowed when the origin is listed, the requested
//     method is in Methods and every requested header is in Headers; then the answer carries the
//     Access-Control-Allow-* headers. A preflight that is not allowed gets the same 204 with none
//     of them, which is what makes the browser refuse the real request; the server needs no
//     other answer, since CORS is the browser's rule and not an access control.
//   - Every other request is passed on, and the headers of an allowed origin are added to the
//     answer whatever it is: a 200, a 404, a 405, a 400 or any other problem+json, so the browser
//     lets the page read the error. A request from an origin that is not listed is served
//     normally, without the headers.
//   - The allowed origin is echoed back exactly (Access-Control-Allow-Origin names one origin),
//     and every answer from a configuration that decides by origin (a list, a wildcard
//     subdomain, AllowOrigin) carries Vary: Origin, also for a request with no Origin header or
//     an unlisted one, so a shared cache never serves one origin's answer to another. With "*"
//     the answer is the same for everyone and has no Vary.
//
// Origins are matched exactly and case-sensitively against the lower-case form browsers send
// (RFC 6454), and the configured origins are lower-cased when the middleware is made. A wildcard
// subdomain entry matches an origin that has its scheme, ends with its ".suffix[:port]" and
// whose remaining head is one or more non-empty labels of lower-case letters, digits, "-" and
// "_"; so "https://evilexample.com", "https://example.com.evil.com", "https://a.example.com.",
// "https://a..example.com", an origin with a path or user, and a different scheme or port do not
// match. AllowOrigin is asked last, for an origin nothing else matched. The origin "null"
// (sandboxed frames and local files) cannot be listed and is never passed to AllowOrigin, since
// any page can make itself "null". A panic recovered by the engine is a 500 written after this
// middleware was left and carries no CORS headers.
//
// It panics (a RegistrationError) when the options cannot work: no origin and no AllowOrigin, an
// origin that is not a full origin, a "*" that is not the whole leftmost label or has fewer than
// two labels after it, "*" with other origins, a wildcard subdomain, AllowOrigin or credentials,
// a method or header that is not a token, a negative or sub-second MaxAge.
//
// A simple request from an allowed origin costs no allocation, listed or matched by a wildcard
// subdomain: the headers are values already in the request's copy of the head and in strings
// built here once (measured in ADR 0003). AllowOrigin's own allocations are the application's.
func CORS(o CORSOptions) Middleware {
	if len(o.Origins) == 0 && o.AllowOrigin == nil {
		refuseMiddleware("CORS", `Origins is empty and AllowOrigin is nil: list the full origins that may call this API ("https://app.example.com") or a wildcard subdomain ("https://*.example.com"), set AllowOrigin to decide at run time, or use "*" for any origin`)
	}
	c := &cors{credentials: o.Credentials, allowFunc: o.AllowOrigin}
	for i, org := range o.Origins {
		if org == "*" {
			for _, other := range o.Origins {
				if other != "*" && strings.Contains(other, "*") {
					refuseMiddleware("CORS", `Origins has "*" together with the wildcard subdomain %q: "*" already allows every origin, so list it alone`, other)
				}
			}
			if len(o.Origins) > 1 {
				refuseMiddleware("CORS", `Origins has "*" together with %d other origins: "*" already allows every origin, so list it alone`, len(o.Origins)-1)
			}
			if o.AllowOrigin != nil {
				refuseMiddleware("CORS", `Origins is "*" with AllowOrigin: "*" already allows every origin, so AllowOrigin would never be asked; drop one of them`)
			}
			if o.Credentials {
				refuseMiddleware("CORS", `Origins is "*" with Credentials: a browser refuses a wildcard answer to a request with credentials, so list the full origins (https://app.example.com) or drop Credentials`)
			}
			c.any = true
			continue
		}
		if strings.Contains(org, "*") {
			c.wildcards = append(c.wildcards, parseWildcard(i, org))
			continue
		}
		if why := badOrigin(org); why != "" {
			refuseMiddleware("CORS", "Origins[%d] %q is not a full origin: %s", i, org, why)
		}
		if c.origins == nil {
			c.origins = make(map[string]struct{}, len(o.Origins))
		}
		c.origins[strings.ToLower(org)] = struct{}{}
	}

	methods := o.Methods
	if len(methods) == 0 {
		methods = []string{"GET", "HEAD", "POST"}
	}
	c.methods = make(map[string]struct{}, len(methods))
	for i, m := range methods {
		if m == "*" && o.Credentials {
			refuseMiddleware("CORS", `Methods[%d] is "*" with Credentials: a browser reads "*" literally when credentials are sent, so name the methods`, i)
		}
		if !isToken(m) {
			refuseMiddleware("CORS", "Methods[%d] %q is not an HTTP method", i, m)
		}
		c.methods[m] = struct{}{}
	}
	c.allowMethods = strings.Join(methods, ", ")

	for i, h := range o.Headers {
		if h == "*" {
			if o.Credentials {
				refuseMiddleware("CORS", `Headers[%d] is "*" with Credentials: a browser reads "*" literally when credentials are sent, so name the headers`, i)
			}
			if len(o.Headers) > 1 {
				refuseMiddleware("CORS", `Headers has "*" together with %d other headers: "*" already allows every header, so list it alone`, len(o.Headers)-1)
			}
			c.anyHeader = true
			continue
		}
		if !isToken(h) {
			refuseMiddleware("CORS", "Headers[%d] %q is not an HTTP header name", i, h)
		}
		if c.headers == nil {
			c.headers = make(map[string]struct{}, len(o.Headers))
		}
		c.headers[strings.ToLower(h)] = struct{}{}
	}
	c.allowHeaders = strings.Join(o.Headers, ", ")

	for i, h := range o.ExposeHeaders {
		if !isToken(h) {
			refuseMiddleware("CORS", "ExposeHeaders[%d] %q is not an HTTP header name", i, h)
		}
	}
	c.expose = strings.Join(o.ExposeHeaders, ", ")

	if o.MaxAge < 0 || (o.MaxAge > 0 && o.MaxAge < time.Second) {
		refuseMiddleware("CORS", "MaxAge is %v, which is not a whole number of seconds: give zero for the browser's default, or at least a second", o.MaxAge)
	}
	if o.MaxAge > 0 {
		c.maxAge = strconv.FormatInt(int64(o.MaxAge/time.Second), 10)
	}
	for i, p := range o.Paths {
		switch {
		case p == "/":
			refuseMiddleware("CORS", `Paths[%d] is "/", which is every path: leave Paths empty for that`, i)
		case !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.ContainsAny(p, "{}?#"):
			refuseMiddleware("CORS", `Paths[%d] %q is not a path prefix: it starts with a slash, does not end with one and has no wildcard, query or fragment ("/api")`, i, p)
		case slices.Contains(o.Paths[:i], p):
			refuseMiddleware("CORS", "Paths[%d] %q is given twice", i, p)
		}
	}
	c.paths = slices.Clone(o.Paths)
	return c.serve
}

// covers reports whether path is under one of c's Paths, or c has none.
func (c *cors) covers(path string) bool {
	if len(c.paths) == 0 {
		return true
	}
	for _, p := range c.paths {
		if rest, ok := strings.CutPrefix(path, p); ok && (rest == "" || rest[0] == '/') {
			return true
		}
	}
	return false
}

// badOrigin says why s is not a full origin, or "" when it is.
func badOrigin(s string) string {
	rest, ok := strings.CutPrefix(strings.ToLower(s), "https://")
	if !ok {
		rest, ok = strings.CutPrefix(strings.ToLower(s), "http://")
	}
	switch {
	case s == "null":
		return `"null" is what any sandboxed page sends, so it cannot be listed`
	case !ok:
		return "it must start with http:// or https://"
	case rest == "":
		return "the host is missing"
	case strings.ContainsAny(rest, "*"):
		return "a * is allowed only as the whole leftmost label of the host (https://*.example.com)"
	case strings.ContainsAny(rest, "/?#@ \t"):
		return "it must be scheme://host[:port], with no path, no trailing slash, no query and no user"
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.HasSuffix(rest, "]") {
		if p, err := strconv.Atoi(rest[i+1:]); err != nil || p < 1 || p > 65535 || i == 0 {
			return "the port is not a number from 1 to 65535"
		}
	}
	return ""
}

// wildcard is one "https://*.example.com[:port]" entry: the scheme with "://", and the ".example.com"
// that follows the "*" with the port, all lower-case.
type wildcard struct {
	scheme string
	suffix string
}

// parseWildcard refuses, with the position and text of the entry, a "*" that is not the whole
// leftmost label of a host of at least two more labels.
func parseWildcard(i int, org string) wildcard {
	low := strings.ToLower(org)
	scheme, rest, ok := strings.Cut(low, "://")
	if !ok || (scheme != "http" && scheme != "https") {
		refuseMiddleware("CORS", "Origins[%d] %q is not a full origin: it must start with http:// or https://", i, org)
	}
	tail, ok := strings.CutPrefix(rest, "*.")
	if !ok || strings.Contains(tail, "*") {
		refuseMiddleware("CORS", "Origins[%d] %q has a * that is not the whole leftmost label: write a wildcard subdomain as https://*.example.com, and list any other host in full", i, org)
	}
	if why := badOrigin(scheme + "://" + tail); why != "" {
		refuseMiddleware("CORS", "Origins[%d] %q is not a full origin: %s", i, org, why)
	}
	host := tail
	if j := strings.LastIndexByte(host, ':'); j >= 0 {
		host = host[:j]
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || !labelChars(l) {
			refuseMiddleware("CORS", "Origins[%d] %q has an empty or invalid label after the *.: write it as https://*.example.com", i, org)
		}
	}
	if len(labels) < 2 {
		refuseMiddleware("CORS", "Origins[%d] %q is a wildcard over fewer than two labels: a wildcard such as https://*.com would allow a whole top-level domain, so name a domain you control (https://*.example.com)", i, org)
	}
	return wildcard{scheme: scheme + "://", suffix: "." + tail}
}

// labelChars reports whether s has only lower-case letters, digits, "-" and "_".
func labelChars(s string) bool {
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !('a' <= ch && ch <= 'z' || '0' <= ch && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

// match reports whether origin is the scheme, one or more non-empty labels, and the suffix.
func (w wildcard) match(origin string) bool {
	rest, ok := strings.CutPrefix(origin, w.scheme)
	if !ok {
		return false
	}
	head, ok := strings.CutSuffix(rest, w.suffix)
	if !ok || head == "" {
		return false
	}
	for {
		var l string
		var more bool
		l, head, more = strings.Cut(head, ".")
		if l == "" || !labelChars(l) {
			return false
		}
		if !more {
			return true
		}
	}
}

// wellFormedOrigin reports whether origin is lower-case scheme://host[:port], the only shape a
// browser sends, and the only one AllowOrigin is asked about. It allocates nothing.
func wellFormedOrigin(origin string) bool {
	for i := 0; i < len(origin); i++ {
		if 'A' <= origin[i] && origin[i] <= 'Z' {
			return false
		}
	}
	return badOrigin(origin) == ""
}

type cors struct {
	any         bool
	origins     map[string]struct{}
	wildcards   []wildcard
	allowFunc   func(origin string) bool
	credentials bool

	methods      map[string]struct{}
	allowMethods string
	headers      map[string]struct{} // lower-case
	anyHeader    bool
	allowHeaders string
	expose       string
	maxAge       string
	paths        []string // Paths; every path when empty
}

func (c *cors) allowed(origin string) bool {
	if c.any {
		return true
	}
	if _, ok := c.origins[origin]; ok {
		return true
	}
	for i := range c.wildcards {
		if c.wildcards[i].match(origin) {
			return true
		}
	}
	return c.allowFunc != nil && origin != "null" && wellFormedOrigin(origin) && c.allowFunc(origin)
}

func (c *cors) serve(ctx context.Context, req Request, next Next) (Response, error) {
	if !c.covers(req.Path()) {
		return next.Call(ctx), nil
	}
	origin := req.Header("Origin")
	if origin == "" {
		res := next.Call(ctx)
		if !c.any {
			res.AddHeader("Vary", "Origin")
		}
		return res, nil
	}
	if req.Method() == "OPTIONS" {
		if m := req.Header("Access-Control-Request-Method"); m != "" {
			return c.preflight(req, origin, m), nil
		}
	}
	res := next.Call(ctx)
	c.decorate(&res, origin)
	return res, nil
}

// decorate adds the headers every answer to an allowed origin carries.
func (c *cors) decorate(res *Response, origin string) {
	if !c.any {
		res.AddHeader("Vary", "Origin")
	}
	if !c.allowed(origin) {
		return
	}
	c.allowOrigin(res, origin)
	if c.expose != "" {
		res.AddHeader("Access-Control-Expose-Headers", c.expose)
	}
}

func (c *cors) allowOrigin(res *Response, origin string) {
	if c.any {
		res.AddHeader("Access-Control-Allow-Origin", "*")
	} else {
		res.AddHeader("Access-Control-Allow-Origin", origin)
	}
	if c.credentials {
		res.AddHeader("Access-Control-Allow-Credentials", "true")
	}
}

func (c *cors) preflight(req Request, origin, method string) Response {
	res := NewResponse(204)
	res.AddHeader("Vary", "Origin, Access-Control-Request-Method, Access-Control-Request-Headers")
	if !c.allowed(origin) {
		return res
	}
	if _, ok := c.methods[method]; !ok {
		return res
	}
	if !c.headersAllowed(req.Header("Access-Control-Request-Headers")) {
		return res
	}
	c.allowOrigin(&res, origin)
	res.AddHeader("Access-Control-Allow-Methods", c.allowMethods)
	if c.anyHeader {
		res.AddHeader("Access-Control-Allow-Headers", "*")
	} else if c.allowHeaders != "" {
		res.AddHeader("Access-Control-Allow-Headers", c.allowHeaders)
	}
	if c.maxAge != "" {
		res.AddHeader("Access-Control-Max-Age", c.maxAge)
	}
	return res
}

// headersAllowed reports whether every header in the comma-separated list a preflight asked for
// is allowed. An empty list asks for none.
func (c *cors) headersAllowed(list string) bool {
	for list != "" {
		var h string
		h, list, _ = strings.Cut(list, ",")
		h = strings.TrimSpace(h)
		if h == "" || c.anyHeader {
			continue
		}
		if !c.headerListed(h) {
			return false
		}
	}
	return true
}

func (c *cors) headerListed(h string) bool {
	var buf [64]byte
	if len(h) > len(buf) {
		return false
	}
	for i := 0; i < len(h); i++ {
		ch := h[i]
		if 'A' <= ch && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		buf[i] = ch
	}
	_, ok := c.headers[string(buf[:len(h)])] // the compiler does not allocate for a map lookup
	return ok
}
