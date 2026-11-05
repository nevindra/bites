package bites

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"slices"
	"strings"
	"unsafe"
)

// CSRFOptions configures CSRF (ADR 0057).
type CSRFOptions struct {
	// Origins are the full origins whose pages may send a state-changing request
	// ("https://app.example.com"), the app's own included: the origin of the pages that hold its
	// forms and scripts. They are matched exactly, as CORS matches them; there is no pattern and no
	// "*". They are compared with Origin, never with Host, which a proxy may have rewritten
	// (ADR 0025).
	//
	// Empty means the app's list: the origins given to WithAllowOrigins, the same list its
	// WebSocket routes take, so a page that holds forms and sockets is named once (ADR 0057).
	// Empty with no such list on the app, or with WithAllowAnyOrigin, is refused when the
	// middleware is given to the app. The list is read when a request arrives, so it is the
	// app's whole list; a list given here is the middleware's own and is not compared with the
	// app's.
	Origins []string

	// Cookie is the name of the double-submit token's cookie: "__Host-csrf" when empty, "csrf"
	// with NotSecure.
	Cookie string

	// Header is the request header a script sends the token back in; "X-CSRF-Token" when empty.
	Header string

	// NotSecure drops the token cookie's Secure attribute (and so its __Host- prefix).
	NotSecure bool
}

// CSRF is the cross-site request forgery middleware of ADR 0057, for routes a browser
// authenticates with a cookie. It lets a state-changing request through only when a browser says
// it came from a listed origin, or, from a client that says nothing, when it carries the token:
//
//   - GET, HEAD, OPTIONS and TRACE pass: they must not change state. One that arrives without
//     the token cookie gets it on its answer, a random 32 bytes readable by the page's scripts.
//   - A request with no Cookie header and no Basic credentials passes: it carries no ambient
//     credential a browser would add on its own, so a token-authenticated API (Bearer, API key)
//     behind the same middleware is untouched. The exception is a form's body (urlencoded,
//     multipart or text/plain) with no Authorization header, which a form cannot set, that the
//     browser says came from an origin not listed: it is
//     refused, since a page elsewhere could post the attacker's credentials to a sign-in form and
//     leave the browser signed in to the attacker's account (login CSRF). A client TLS certificate is ambient too and is not
//     seen here: a route authenticated by one is out of this middleware's reach.
//   - Sec-Fetch-Site same-origin or none passes; same-site or cross-site passes only when Origin
//     is listed.
//   - Without Sec-Fetch-Site, an Origin passes when it is listed, and is refused otherwise
//     ("null" included).
//   - With neither header (an old browser, a client that sends cookies but no Origin), the
//     request passes when the Header carries the value of the token cookie, compared in constant
//     time. A form cannot set a header, and a script on another origin can only with a CORS
//     preflight this app would have to allow.
//
// A refused request is a 403 problem+json and goes no further. It panics when no origin is
// given here or by the app (see CSRFOptions.Origins), an origin is not a full one or is "*", the
// cookie or header name is not a token, or the __Host- prefix is asked for with NotSecure; the
// first of these is refused when the middleware is given to the app, since only then is the
// app's list known. A request that passes allocates nothing; giving the
// token cookie allocates.
func CSRF(o CSRFOptions) Middleware {
	const fn = "CSRF"
	c := &csrf{origins: make(map[string]struct{}, len(o.Origins)), app: len(o.Origins) == 0}
	for i, org := range o.Origins {
		if org == "*" {
			refuseMiddleware(fn, `Origins[%d] is "*", which would let every site send requests with the user's cookies: list the origins`, i)
		}
		if why := badOrigin(org); why != "" {
			refuseMiddleware(fn, "Origins[%d] %q is not a full origin: %s", i, org, why)
		}
		c.origins[strings.ToLower(org)] = struct{}{}
	}
	c.cookie = o.Cookie
	if c.cookie == "" {
		c.cookie = "__Host-csrf"
		if o.NotSecure {
			c.cookie = "csrf"
		}
	}
	if !validCookieName(c.cookie) {
		refuseMiddleware(fn, "Cookie %q is not a cookie name: use an HTTP token", c.cookie)
	}
	if o.NotSecure && (strings.HasPrefix(c.cookie, "__Host-") || strings.HasPrefix(c.cookie, "__Secure-")) {
		refuseMiddleware(fn, "Cookie %q has a prefix browsers accept only with Secure: drop the prefix or NotSecure", c.cookie)
	}
	c.header = o.Header
	if c.header == "" {
		c.header = "X-CSRF-Token"
	}
	if !isToken(c.header) {
		refuseMiddleware(fn, "Header %q is not an HTTP header name", o.Header)
	}
	c.attrs = "; Path=/; SameSite=Strict"
	if !o.NotSecure {
		c.attrs += "; Secure"
	}
	if c.app {
		return c.serveAppOrigins
	}
	return c.serve
}

// bindMiddleware is Registration's look at the middleware a scope is given (ADR 0057): a CSRF with
// no origins of its own reads the app's list when a request arrives, so there must be one. It
// returns the refusal, or nil.
func (a *App) bindMiddleware(mw []Middleware) error {
	for _, m := range mw {
		if m == nil || !sameCode(m, (*csrf)(nil).serveAppOrigins) {
			continue
		}
		switch list := a.origin; {
		case list == nil || (!list.any && len(list.list) == 0):
			return refusalf(`CSRF: Origins is empty and the app has no list to take them from: list the full origins of the pages that send forms and scripts to this app ("https://app.example.com"), its own included, in CSRFOptions.Origins, or once for the app, WebSocket routes too, with WithAllowOrigins`)
		case list.any:
			return refusalf("CSRF: Origins is empty and the app's list is WithAllowAnyOrigin(), which would let every site send requests with the user's cookies: list the origins in CSRFOptions.Origins, or give the app WithAllowOrigins instead")
		}
	}
	return nil
}

// checkNarrowed refuses a WebSocket route whose own AllowOrigins, or its group's, names an origin
// the app's list does not (ADR 0046): a nearer list narrows the app's, never widens it.
// AllowAnyOrigin is not a list and stays what it says.
func (a *App) checkNarrowed(cfg *routeConfig) error {
	if a.origin == nil || a.origin.any || cfg.originKind != originListed || cfg.originDepth < 0 {
		return nil
	}
	for _, org := range cfg.origin.list {
		if !slices.Contains(a.origin.list, org) {
			return refusalf("AllowOrigins(%q) is not in the app's list (WithAllowOrigins at %s): add it there, or give the app no list and list origins per route", org, a.originWhere)
		}
	}
	return nil
}

type csrf struct {
	app     bool // Origins was empty: the origins are the app's, read when a request arrives
	origins map[string]struct{}
	cookie  string
	header  string
	attrs   string
}

const csrfTokenLen = 43 // base64url of 32 bytes, unpadded

func (c *csrf) serve(ctx context.Context, req Request, next Next) (Response, error) {
	return c.run(ctx, req, next)
}

// serveAppOrigins is serve for a CSRF with no origins of its own: the listed origins are the
// app's WithAllowOrigins, read through the request (ADR 0057). It is its own method so
// Registration can tell it apart by code.
func (c *csrf) serveAppOrigins(ctx context.Context, req Request, next Next) (Response, error) {
	return c.run(ctx, req, next)
}

func (c *csrf) run(ctx context.Context, req Request, next Next) (Response, error) {
	token, hasCookies := c.token(req)
	switch req.Method() {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		res := next.Call(ctx)
		if token == "" {
			res.AddHeader("Set-Cookie", c.cookie+"="+newCSRFToken()+c.attrs)
		}
		return res, nil
	}
	if !hasCookies && !basicCredentials(req) {
		// No ambient credential, but a form a page elsewhere made the browser send can still sign
		// it in to the attacker's account (login CSRF): a form's body, from an origin the browser
		// says is not listed, is refused. A JSON client, and one that sends an Authorization a
		// form cannot set (a Bearer token), are untouched.
		if formBody(req.Header("Content-Type")) && req.Header("Authorization") == "" && c.foreign(req) {
			return Response{}, Forbidden("a form from a site this app does not list in its CSRF origins, sent with no cookie: a page elsewhere may be signing this browser in to another account (login CSRF)")
		}
		return next.Call(ctx), nil
	}
	origin := req.Header("Origin")
	switch site := req.Header("Sec-Fetch-Site"); site {
	case "same-origin", "none":
		return next.Call(ctx), nil
	case "":
	default:
		if c.listed(req, origin) {
			return next.Call(ctx), nil
		}
		return Response{}, Forbidden("the request came from a site this app does not list in its CSRF origins")
	}
	if origin != "" {
		if c.listed(req, origin) {
			return next.Call(ctx), nil
		}
		return Response{}, Forbidden("the request came from an origin this app does not list in its CSRF origins")
	}
	sent := req.Header(c.header)
	if token == "" || len(sent) != len(token) || subtle.ConstantTimeCompare(bytesOf(sent), bytesOf(token)) != 1 {
		return Response{}, Forbidden("the request has no Origin, and its CSRF token is missing or does not match")
	}
	return next.Call(ctx), nil
}

// formBody reports whether ct is a body an HTML form, or a script on another origin without a
// CORS preflight, can send: the three content types of a simple request.
func formBody(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	return strings.EqualFold(ct, "application/x-www-form-urlencoded") || strings.EqualFold(ct, "multipart/form-data") || strings.EqualFold(ct, "text/plain")
}

// foreign reports whether the browser says the request came from an origin c does not list:
// Sec-Fetch-Site same-site or cross-site with an unlisted Origin, or, without it, an unlisted
// Origin. A request with neither header says nothing and is not foreign.
func (c *csrf) foreign(req Request) bool {
	origin := req.Header("Origin")
	switch req.Header("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
		return origin != "" && !c.listed(req, origin)
	}
	return !c.listed(req, origin)
}

// basicCredentials reports whether the request carries Basic credentials, which a browser caches
// and sends on its own like a cookie, so they are an ambient credential a forged form would carry.
func basicCredentials(req Request) bool {
	_, ok := cutScheme(req.Header("Authorization"), "Basic")
	return ok
}

func (c *csrf) listed(req Request, origin string) bool {
	if c.app {
		if req.run == nil || req.run.a.origin == nil {
			return false
		}
		return slices.Contains(req.run.a.origin.list, origin)
	}
	_, ok := c.origins[origin]
	return ok
}

// token is the value of the token cookie, "" when there is none or it is not a token this
// middleware made, and whether the request has any Cookie header.
func (c *csrf) token(req Request) (string, bool) {
	has := false
	for b := req.headers; b != ""; {
		var line string
		var ok bool
		if line, b, ok = cutLine(b); !ok {
			break
		}
		has = true
		for line != "" {
			var pair string
			pair, line, _ = strings.Cut(line, ";")
			name, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && name == c.cookie && len(value) == csrfTokenLen {
				return value, true
			}
		}
	}
	return "", has
}

// bytesOf is the bytes of s, for a function that only reads them.
func bytesOf(s string) []byte { return unsafe.Slice(unsafe.StringData(s), len(s)) }

func newCSRFToken() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
