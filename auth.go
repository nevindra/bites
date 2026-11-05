package bites

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

// The auth Providers of ADR 0054: Bearer tokens, Basic auth and API keys. Each is a constructor
// that takes the application's verification function and returns a Provider (ADR 0023), given to
// App.Provide, Group.Provide or the Provide option. The framework reads the credential out of the
// request and answers a missing or malformed one; the application decides whether a credential
// is good, and what the value it stands for is.

// ErrInvalidCredentials is what a verification function given to BearerAuth, BasicAuth or
// APIKeyAuth returns, wrapped or not, for a credential that is not good: unknown, expired,
// revoked, signed by the wrong key. The answer is a 401 with the scheme's challenge. Any other
// error is a failure to verify (the database is down) and is answered as a 500, so an outage
// never tells every client its credential is wrong. Its text never reaches the client.
var ErrInvalidCredentials = errors.New("bites: invalid credentials")

// AuthOptions configures BearerAuth, BasicAuth and APIKeyAuth.
type AuthOptions struct {
	// Realm is the realm of the WWW-Authenticate challenge; "api" when empty. It is a quoted
	// string, so it cannot hold a double quote, a backslash or a control character.
	Realm string

	// Header is the request header APIKeyAuth reads the key from; "X-API-Key" when empty. It is
	// refused on BearerAuth and BasicAuth, which read Authorization as RFC 6750 and RFC 7617 say.
	Header string
}

const (
	defaultRealm        = "api"
	defaultAPIKeyHeader = "X-API-Key"
)

// authConfig is what the three constructors share once checked: the challenge and the header.
type authConfig struct {
	scheme    string // "Bearer", "Basic" or "APIKey"
	header    string
	challenge string // the WWW-Authenticate value for a request with no credential
	invalid   string // the value for a credential that was not good
	malformed string // the value for one that cannot be read, or "" for no challenge on a 400
	cred      credential
	doc       authDoc // what the probe reports: this one credential (ADR 0069)
}

func newAuthConfig(fn, scheme string, verifyNil bool, opts []AuthOptions) authConfig {
	if verifyNil {
		refuseMiddleware(fn, "the verification function is nil: give the function that decides whether a credential is good")
	}
	var o AuthOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseMiddleware(fn, "%d options values were given; give one AuthOptions", len(opts))
	}
	if o.Realm == "" {
		o.Realm = defaultRealm
	}
	for i := 0; i < len(o.Realm); i++ {
		if c := o.Realm[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			refuseMiddleware(fn, "Realm %q has a character a quoted realm cannot carry: use printable ASCII without a double quote or a backslash", o.Realm)
		}
	}
	c := authConfig{scheme: scheme, header: "Authorization"}
	if scheme == "APIKey" {
		c.header = o.Header
		if c.header == "" {
			c.header = defaultAPIKeyHeader
		}
		if !isToken(c.header) {
			refuseMiddleware(fn, "Header %q is not an HTTP header name", o.Header)
		}
		if strings.EqualFold(c.header, "Authorization") || strings.EqualFold(c.header, "Cookie") {
			refuseMiddleware(fn, "Header is %s, whose value is not a bare key: use BearerAuth for a token in Authorization, or a header of the key's own (X-API-Key)", c.header)
		}
	} else if o.Header != "" {
		refuseMiddleware(fn, "Header is %q, but %s reads Authorization, as its RFC says; Header is for APIKeyAuth", o.Header, fn)
	}
	realm := `realm="` + o.Realm + `"`
	switch scheme {
	case "Bearer":
		c.challenge = "Bearer " + realm
		c.invalid = "Bearer " + realm + `, error="invalid_token"`
		c.malformed = "Bearer " + realm + `, error="invalid_request"`
		c.cred = credential{typ: "http", scheme: "bearer"}
	case "Basic":
		c.challenge = "Basic " + realm + `, charset="UTF-8"`
		c.invalid = c.challenge
		c.cred = credential{typ: "http", scheme: "basic"}
	default:
		c.challenge = "APIKey " + realm + `, header="` + c.header + `"`
		c.invalid = c.challenge
		c.cred = credential{typ: "apiKey", in: "header", name: c.header}
	}
	c.cred.challenge = c.challenge
	return c
}

// done fills what the probe reports once the config sits where it stays, in its Provider.
func (c *authConfig) done() { c.doc = authDoc{alts: []authAlt{{cred: &c.cred}}} }

// missing is the 401 for a request with no credential of this scheme.
func (c *authConfig) missing(detail string) error {
	return Unauthorized(detail).WithHeader("WWW-Authenticate", c.challenge)
}

// bad is the 400 for a credential that cannot be read.
func (c *authConfig) bad(detail string) error {
	e := BadRequest(detail)
	if c.malformed != "" {
		e = e.WithHeader("WWW-Authenticate", c.malformed)
	}
	return e
}

// verified turns what the verification function returned into the Provider's answer.
func verified[T any](c *authConfig, v T, err error, detail string) (T, error) {
	if err == nil {
		return v, nil
	}
	var zero T
	se := findStatus(err)
	switch {
	case se != nil:
		if se.Status == 401 && !se.hasHeader("WWW-Authenticate") {
			return zero, se.WithHeader("WWW-Authenticate", c.invalid)
		}
		return zero, se
	case errors.Is(err, ErrInvalidCredentials):
		return zero, Unauthorized(detail).WithHeader("WWW-Authenticate", c.invalid)
	}
	return zero, err
}

func (e *StatusError) hasHeader(name string) bool {
	for _, h := range e.headers {
		if foldEq(h.name, name) {
			return true
		}
	}
	return false
}

// BearerAuth returns a Provider of T that reads a Bearer token (RFC 6750) from the Authorization
// header and calls verify with it (ADR 0054):
//
//	app.Provide(bites.BearerAuth(func(ctx context.Context, token string) (User, error) { ... }))
//
// The scheme is matched without regard to case, as RFC 9110 says, and the token must be a
// b64token. A request with no Authorization, or one of another scheme, is a 401 with
// WWW-Authenticate: Bearer realm="api". A malformed one (no token, a character a token cannot
// have, the header twice) is a 400 with error="invalid_request". verify returning an error that
// wraps ErrInvalidCredentials is a 401 with error="invalid_token"; a *StatusError it returns is
// the answer as it is (a 403 for a token without the scope), a 401 among them given the
// challenge; any other error is a 500. A token in the query or a cookie is not read.
//
// A JWT is verified by giving BearerAuth the verifier's function; the framework parses no token.
// The token reaches verify as a substring of the request's own copy of the head (ADR 0022) and
// is never logged, nor written in any answer.
//
// It panics when verify is nil, the realm cannot be quoted, Header is set, or more than one
// options value is given.
//
//go:noinline
func BearerAuth[T any](verify func(ctx context.Context, token string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error) {
	p := &bearerAuth[T]{c: newAuthConfig("BearerAuth", "Bearer", verify == nil, opts), verify: verify}
	p.c.done()
	return p.asProvider()
}

// bearerAuth is the Provider BearerAuth returns, as a method value. The constructors are never
// inlined, so the code of the function they return is one place for each instantiation, which
// is how the API document recognises them among a route's Providers (ADR 0069).
type bearerAuth[T any] struct {
	c      authConfig
	verify func(ctx context.Context, token string) (T, error)
}

func (p *bearerAuth[T]) provide(ctx context.Context, req Request, _ struct{}) (T, error) {
	var zero T
	if pr, ok := ctx.(*authProbe); ok {
		pr.doc = &p.c.doc
		return zero, nil
	}
	v, n := req.uniqueHeader("Authorization")
	if n > 1 {
		return zero, p.c.bad("the request has more than one Authorization header")
	}
	rest, ok := cutScheme(v, "Bearer")
	if !ok {
		return zero, p.c.missing("a Bearer token is required")
	}
	if !isB64Token(rest) {
		return zero, p.c.bad("the Bearer token is malformed")
	}
	u, err := p.verify(ctx, rest)
	return verified(&p.c, u, err, "the Bearer token is not valid")
}

// BasicAuth returns a Provider of T that reads Basic credentials (RFC 7617) from the
// Authorization header and calls verify with the user-id and the password (ADR 0054). A request
// with none is a 401 with WWW-Authenticate: Basic realm="api", charset="UTF-8"; credentials that
// are not base64, have no colon or are not UTF-8 are a 400; verify's errors are answered as for
// BearerAuth. The comparison of a password is verify's: compare a hash with
// crypto/subtle.ConstantTimeCompare, never with ==.
//
// The decoded user-id and password are in the request's slab, never in a pooled buffer
// (ADR 0008), and are never logged. Basic auth sends the password with every request, so serve
// it only over TLS. It panics as BearerAuth does.
//
//go:noinline
func BasicAuth[T any](verify func(ctx context.Context, user, password string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error) {
	p := &basicAuth[T]{c: newAuthConfig("BasicAuth", "Basic", verify == nil, opts), verify: verify}
	p.c.done()
	return p.asProvider()
}

// basicAuth is the Provider BasicAuth returns, as a method value (see bearerAuth).
type basicAuth[T any] struct {
	c      authConfig
	verify func(ctx context.Context, user, password string) (T, error)
}

func (p *basicAuth[T]) provide(ctx context.Context, req Request, _ struct{}) (T, error) {
	var zero T
	if pr, ok := ctx.(*authProbe); ok {
		pr.doc = &p.c.doc
		return zero, nil
	}
	v, n := req.uniqueHeader("Authorization")
	if n > 1 {
		return zero, p.c.bad("the request has more than one Authorization header")
	}
	rest, ok := cutScheme(v, "Basic")
	if !ok {
		return zero, p.c.missing("Basic credentials are required")
	}
	cred, ok := req.decodeBasic(rest)
	if !ok {
		return zero, p.c.bad("the Basic credentials are malformed")
	}
	user, pass, ok := strings.Cut(cred, ":")
	if !ok {
		return zero, p.c.bad("the Basic credentials are malformed")
	}
	u, err := p.verify(ctx, user, pass)
	return verified(&p.c, u, err, "the Basic credentials are not valid")
}

// APIKeyAuth returns a Provider of T that reads an API key from a header of its own,
// "X-API-Key" unless AuthOptions.Header names another, and calls verify with it (ADR 0054). A
// request without the header, or with it empty, is a 401 with WWW-Authenticate: APIKey
// realm="api", header="X-API-Key" (RFC 9110 asks a 401 for a challenge; no scheme for API keys is
// registered, so this one says where the key goes); the header twice, or a key with a space or a
// control character, is a 400; verify's errors are answered as for BearerAuth.
//
// A key is looked up by verify, best by a hash of it (SHA-256 of a long random key), so the
// stored keys are not secrets and nothing compares a secret byte by byte. A key in the query is
// not read: a query is written in access logs and browser histories. It panics as BearerAuth
// does, and when Header is not a token or is Authorization or Cookie.
//
//go:noinline
func APIKeyAuth[T any](verify func(ctx context.Context, key string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error) {
	p := &apiKeyAuth[T]{c: newAuthConfig("APIKeyAuth", "APIKey", verify == nil, opts), verify: verify}
	p.c.done()
	return p.asProvider()
}

// apiKeyAuth is the Provider APIKeyAuth returns, as a method value (see bearerAuth).
type apiKeyAuth[T any] struct {
	c      authConfig
	verify func(ctx context.Context, key string) (T, error)
}

func (p *apiKeyAuth[T]) provide(ctx context.Context, req Request, _ struct{}) (T, error) {
	var zero T
	if pr, ok := ctx.(*authProbe); ok {
		pr.doc = &p.c.doc
		return zero, nil
	}
	v, n := req.uniqueHeader(p.c.header)
	if n > 1 {
		return zero, p.c.bad("the request has more than one " + p.c.header + " header")
	}
	if v == "" {
		return zero, p.c.missing("an API key is required")
	}
	for i := 0; i < len(v); i++ {
		if v[i] <= ' ' || v[i] == 0x7f {
			return zero, p.c.bad("the API key is malformed")
		}
	}
	u, err := p.verify(ctx, v)
	return verified(&p.c, u, err, "the API key is not valid")
}

// SecurityScheme is how a request sends the credential that a Provider the application wrote
// reads, as the API document states it (ADR 0069); CustomAuth gives it to the Provider. Type is
// one of four, and each has its own fields:
//
//   - "http": Scheme is the auth scheme of Authorization ("bearer", "basic", "digest"), and
//     BearerFormat a hint of what a bearer token is ("JWT");
//   - "apiKey": In is "header" or "cookie", and Name the header's or the cookie's name;
//   - "mutualTLS": a client certificate (ADR 0066), with no field of its own;
//   - "openIdConnect": OpenIDConnectURL is the provider's discovery document, and the token is
//     sent as a Bearer token.
//
// Description is prose for any of them.
type SecurityScheme struct {
	// Type is "http", "apiKey", "mutualTLS" or "openIdConnect".
	Type string
	// Scheme is the authorization scheme of an "http" type: "bearer", "basic" or "digest", in lower case.
	Scheme string
	// BearerFormat is the format of a bearer token, such as "JWT".
	BearerFormat string
	// In is where an "apiKey" credential is sent: "header" or "cookie".
	In string
	// Name is the name of the header or cookie that carries an "apiKey" credential.
	Name string
	// OpenIDConnectURL is the discovery document of an "openIdConnect" type.
	OpenIDConnectURL string
	// Description is prose about the scheme, for any type.
	Description string
}

// CustomAuth returns provider, a Provider the application wrote that reads a credential, together
// with the SecurityScheme it reads (ADR 0069): the API document states the scheme on every route
// that runs it, with its 401, as for BearerAuth; AnyAuth can offer it among other credentials,
// and an Opt field can make it optional. The Provider is called as it is; CustomAuth adds one
// comparison a request and no allocation.
//
//	app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"}, userFromSession))
//
// The Provider may have dependencies, as any Provider: a user read from a session asks for the
// Session the session Provider gives (deps struct{ S bites.Session[Login] }), and is still
// recognised. Only one with none (struct{}) can be given to AnyAuth or made optional by an Opt
// field, since both call it without filling anything first.
//
// The framework cannot read provider, so the scheme is the application's word for what it reads.
// It panics when provider is nil, Type is not one of the four, a field Type has is missing or
// malformed, or a field Type does not have is given. An "apiKey" In "query" is for a Provider
// that reads a Query section, which only a WebSocket or an event stream route allows, since a
// browser opens those without a header of its own; BearerAuth and APIKeyAuth read none, since a
// query is kept by proxies and browser histories, so a query credential is best a short-lived
// ticket the page fetched with its own credential.
//
//go:noinline
func CustomAuth[T, D any](scheme SecurityScheme, provider func(ctx context.Context, req Request, deps D) (T, error)) func(context.Context, Request, D) (T, error) {
	if provider == nil {
		refuseMiddleware("CustomAuth", "the Provider is nil")
	}
	p := &customAuth[T, D]{c: schemeCredential(scheme), provider: provider}
	p.doc = authDoc{alts: []authAlt{{cred: &p.c}}}
	return p.asProvider()
}

// customAuth is the Provider CustomAuth returns, as a method value (see bearerAuth).
type customAuth[T, D any] struct {
	c        credential
	doc      authDoc
	provider func(context.Context, Request, D) (T, error)
}

func (p *customAuth[T, D]) provide(ctx context.Context, req Request, d D) (T, error) {
	if pr, ok := ctx.(*authProbe); ok {
		pr.doc = &p.doc
		var zero T
		return zero, nil
	}
	return p.provider(ctx, req, d)
}

// schemeCredential checks a SecurityScheme and returns the credential it describes.
func schemeCredential(s SecurityScheme) credential {
	refuse := func(format string, args ...any) { refuseMiddleware("CustomAuth", format, args...) }
	c := credential{typ: s.Type, description: s.Description}
	has := map[string]bool{"Scheme": s.Scheme != "", "BearerFormat": s.BearerFormat != "", "In": s.In != "", "Name": s.Name != "", "OpenIDConnectURL": s.OpenIDConnectURL != ""}
	var own []string
	switch s.Type {
	case "http":
		own = []string{"Scheme", "BearerFormat"}
		if !isToken(s.Scheme) {
			refuse("Scheme %q is not an HTTP auth scheme: give a token, such as \"bearer\" or \"digest\"", s.Scheme)
		}
		c.scheme = strings.ToLower(s.Scheme)
		if s.BearerFormat != "" && c.scheme != "bearer" {
			refuse("BearerFormat is given, but Scheme is %q: a bearer format describes a Bearer token", s.Scheme)
		}
		c.bearerFormat = s.BearerFormat
	case "apiKey":
		own = []string{"In", "Name"}
		switch s.In {
		case "header":
			if !isToken(s.Name) || strings.EqualFold(s.Name, "Authorization") || strings.EqualFold(s.Name, "Cookie") {
				refuse("Name %q is not the header of a key: give a header name of its own, such as X-API-Key (Authorization is Type \"http\", a cookie is In \"cookie\")", s.Name)
			}
		case "cookie":
			if !validCookieName(s.Name) {
				refuse("Name %q is not a cookie name: use an HTTP token", s.Name)
			}
		case "query":
			if !isToken(s.Name) {
				refuse("Name %q is not the query key of a credential: use an HTTP token, such as ticket", s.Name)
			}
		default:
			refuse("In %q is not where an API key is described: give \"header\", \"cookie\" or, for a Provider that reads a Query section on a WebSocket or event stream route, \"query\"", s.In)
		}
		c.in, c.name = s.In, s.Name
	case "mutualTLS":
	case "openIdConnect":
		own = []string{"OpenIDConnectURL"}
		if u, err := url.Parse(s.OpenIDConnectURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			refuse("OpenIDConnectURL %q is not an absolute URL: give the provider's discovery document, such as https://id.example.com/.well-known/openid-configuration", s.OpenIDConnectURL)
		}
		c.oidcURL = s.OpenIDConnectURL
	default:
		refuse("Type %q is not a scheme CustomAuth states: give \"http\", \"apiKey\", \"mutualTLS\" or \"openIdConnect\" (an OAuth 2 access token is Type \"http\" with Scheme \"bearer\")", s.Type)
	}
	for _, f := range []string{"Scheme", "BearerFormat", "In", "Name", "OpenIDConnectURL"} {
		if has[f] && !slices.Contains(own, f) {
			refuse("%s is given, but a scheme of Type %q has no %s", f, s.Type, f)
		}
	}
	return c
}

// AnyAuth returns a Provider of T that accepts any one of several credentials (ADR 0069): Bearer
// or an API key, a session cookie or a Bearer token. Each of providers must be one that
// BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth made, so that where its credential is
// sent is known:
//
//	app.Provide(bites.AnyAuth(bites.BearerAuth(jwt.Verify), bites.APIKeyAuth(keys.Lookup)))
//
// The first of providers, in the order given, whose credential the request carries is the one
// that runs, and its answer is the answer: a credential that is not good is its 401, and the next
// one is not tried. A request that carries none is answered by the first, whose 401 gets the
// challenges of the others. The API document lists each credential as an alternative of the
// route's security. It panics when fewer than two Providers are given, one is nil or was not
// made by those five, or two read the same credential, since the second would never run.
//
//go:noinline
func AnyAuth[T any](providers ...func(ctx context.Context, req Request, deps struct{}) (T, error)) func(context.Context, Request, struct{}) (T, error) {
	if len(providers) < 2 {
		refuseMiddleware("AnyAuth", "it chooses among two Providers or more, and %d is given: give a single one to Provide directly", len(providers))
	}
	p := &anyAuth[T]{}
	for i, fn := range providers {
		if fn == nil {
			refuseMiddleware("AnyAuth", "Provider %d is nil", i+1)
		}
		d := authOf(fn)
		if d == nil {
			refuseMiddleware("AnyAuth", "Provider %d was not made by BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth, so where its credential is sent is not known: give it to CustomAuth with its SecurityScheme", i+1)
		}
		for _, alt := range d.alts {
			for j, prev := range p.alts {
				if prev.cred.place() == alt.cred.place() {
					refuseMiddleware("AnyAuth", "Provider %d reads %s, as Provider %d does, so it would never run: keep one of the two", i+1, alt.cred.place(), p.from[j])
				}
			}
			p.alts = append(p.alts, anyAlt[T]{cred: alt.cred, fn: alt.fn.(func(context.Context, Request, struct{}) (T, error))})
			p.from = append(p.from, i+1)
			p.doc.alts = append(p.doc.alts, alt)
		}
	}
	return p.asProvider()
}

// anyAuth is the Provider AnyAuth returns, as a method value (see bearerAuth). A nested AnyAuth's
// alternatives are its own, in their order.
type anyAuth[T any] struct {
	alts []anyAlt[T]
	from []int // the position in AnyAuth's arguments each alternative came from, for a refusal
	doc  authDoc
}

type anyAlt[T any] struct {
	cred *credential
	fn   func(context.Context, Request, struct{}) (T, error)
}

func (p *anyAuth[T]) provide(ctx context.Context, req Request, d struct{}) (T, error) {
	if pr, ok := ctx.(*authProbe); ok {
		pr.doc = &p.doc
		var zero T
		return zero, nil
	}
	for i := range p.alts {
		if p.alts[i].cred.present(req) {
			return p.alts[i].fn(ctx, req, d)
		}
	}
	v, err := p.alts[0].fn(ctx, req, d)
	var se *StatusError
	if err == nil {
		return v, err
	}
	if se = findStatus(err); se == nil || se.Status != 401 {
		return v, err
	}
	for _, a := range p.alts[1:] {
		if c := a.cred.challenge; c != "" && c != p.alts[0].cred.challenge {
			se = se.WithHeader("WWW-Authenticate", c)
		}
	}
	return v, se
}

// authProbe is the context Registration passes to a Provider it has recognised, by its code, as
// one that BearerAuth, BasicAuth, APIKeyAuth, CustomAuth or AnyAuth made, so that the Provider
// reports the credentials it reads instead of reading a request (ADR 0069). A Provider the
// framework did not make is never called at Registration.
type authProbe struct {
	context.Context
	doc *authDoc
}

// authDoc is the credentials an auth Provider reads: one for BearerAuth, BasicAuth, APIKeyAuth
// and CustomAuth, one for each alternative of AnyAuth in order, and, for the optional form an
// Opt field asks for, a last entry with no credential (ADR 0069).
type authDoc struct {
	alts []authAlt
}

// authAlt is one way through an auth Provider: the credential, nil for none, and the Provider
// that reads it, a func(context.Context, Request, struct{}) (T, error), nil until authOf fills it.
type authAlt struct {
	cred *credential
	fn   any
}

// present reports whether req carries any of the credentials of d.
func (d *authDoc) present(req Request) bool {
	for i := range d.alts {
		if c := d.alts[i].cred; c != nil && c.present(req) {
			return true
		}
	}
	return false
}

// asProvider is the Provider each of the five returns: its provide method as a method value,
// made at this one place, which is never inlined, so that every such Provider of one T has one
// code address whatever its receiver (a method value made inside a generic function is a closure
// of that place, not the method's own wrapper).
//
//go:noinline
func (p *bearerAuth[T]) asProvider() func(context.Context, Request, struct{}) (T, error) {
	return p.provide
}

//go:noinline
func (p *basicAuth[T]) asProvider() func(context.Context, Request, struct{}) (T, error) {
	return p.provide
}

//go:noinline
func (p *apiKeyAuth[T]) asProvider() func(context.Context, Request, struct{}) (T, error) {
	return p.provide
}

//go:noinline
func (p *customAuth[T, D]) asProvider() func(context.Context, Request, D) (T, error) {
	return p.provide
}

//go:noinline
func (p *anyAuth[T]) asProvider() func(context.Context, Request, struct{}) (T, error) {
	return p.provide
}

// authOf is the credentials fn reads when fn is a Provider that BearerAuth, BasicAuth,
// APIKeyAuth, CustomAuth or AnyAuth returned, and nil otherwise. Comparing fn's code with that of
// the Provider each of the five makes for T, through asProvider on a receiver that is never
// used, tells them apart from every other function without calling one; only then is fn called,
// with a probe, to read what it reads.
func authOf[T any](fn func(context.Context, Request, struct{}) (T, error)) *authDoc {
	switch reflect.ValueOf(fn).Pointer() {
	case reflect.ValueOf((&bearerAuth[T]{}).asProvider()).Pointer(),
		reflect.ValueOf((&basicAuth[T]{}).asProvider()).Pointer(),
		reflect.ValueOf((&apiKeyAuth[T]{}).asProvider()).Pointer(),
		reflect.ValueOf((&customAuth[T, struct{}]{}).asProvider()).Pointer(),
		reflect.ValueOf((&anyAuth[T]{}).asProvider()).Pointer():
	default:
		return nil
	}
	pr := &authProbe{Context: context.Background()}
	fn(pr, Request{}, struct{}{})
	if pr.doc == nil {
		return nil
	}
	alts := slices.Clone(pr.doc.alts)
	for i := range alts {
		if alts[i].fn == nil {
			alts[i].fn = fn
		}
	}
	return &authDoc{alts: alts}
}

// customAuthOf is the credential fn reads when fn is a Provider with dependencies that CustomAuth
// returned, and nil otherwise; authOf is the same for a Provider with none. Such a Provider has
// no optional form and cannot be an AnyAuth alternative, so the doc names no function to call.
func customAuthOf[T, D any](fn func(context.Context, Request, D) (T, error)) *authDoc {
	if reflect.ValueOf(fn).Pointer() != reflect.ValueOf((&customAuth[T, D]{}).asProvider()).Pointer() {
		return nil
	}
	pr := &authProbe{Context: context.Background()}
	var zero D
	fn(pr, Request{}, zero)
	if pr.doc == nil {
		return nil
	}
	return &authDoc{alts: slices.Clone(pr.doc.alts)}
}

// credential is where a request carries one kind of credential, as the API document states it
// (ADR 0069): an HTTP auth scheme in Authorization, an API key in a header or a cookie, a client
// certificate, or an OpenID Connect token, which is sent as a Bearer token.
type credential struct {
	typ          string // "http", "apiKey", "mutualTLS" or "openIdConnect"
	scheme       string // http: the auth scheme, in lower case
	in, name     string // apiKey: "header" or "cookie", and the header's or the cookie's name
	bearerFormat string
	oidcURL      string
	description  string
	challenge    string // the WWW-Authenticate value of a 401 for a missing one; "" when not known
}

// present reports whether req carries this credential, well formed or not: what AnyAuth and an
// optional field ask before running the Provider that reads it. A header sent twice is present,
// so that the Provider answers its 400.
func (c *credential) present(req Request) bool {
	switch c.typ {
	case "http", "openIdConnect":
		scheme := c.scheme
		if c.typ == "openIdConnect" {
			scheme = "bearer"
		}
		v, n := req.uniqueHeader("Authorization")
		_, ok := cutScheme(v, scheme)
		return n > 1 || ok
	case "apiKey":
		if c.in == "cookie" {
			return hasCookie(req, c.name)
		}
		if c.in == "query" {
			found := false
			wire.EachQuery(unsafe.Slice(unsafe.StringData(req.RawQuery()), len(req.RawQuery())), func(k, v []byte) bool {
				found = found || (string(k) == c.name && len(v) > 0)
				return !found
			})
			return found
		}
		v, n := req.uniqueHeader(c.name)
		return n > 1 || v != ""
	case "mutualTLS":
		return len(req.VerifiedChains()) > 0
	}
	return false
}

// place names where the credential is sent: two alternatives of AnyAuth with one place would
// never both run.
func (c *credential) place() string {
	switch c.typ {
	case "http":
		return "the " + c.scheme + " scheme of Authorization"
	case "openIdConnect":
		return "the bearer scheme of Authorization"
	case "apiKey":
		if c.in == "cookie" {
			return "the cookie " + c.name
		}
		return "the header " + strings.ToLower(c.name)
	}
	return "a client certificate"
}

// object is the credential's entry in components.securitySchemes.
func (c *credential) object() map[string]any {
	o := map[string]any{"type": c.typ}
	switch c.typ {
	case "http":
		o["scheme"] = c.scheme
		if c.bearerFormat != "" {
			o["bearerFormat"] = c.bearerFormat
		}
	case "apiKey":
		o["in"], o["name"] = c.in, c.name
	case "openIdConnect":
		o["openIdConnectUrl"] = c.oidcURL
	}
	if c.description != "" {
		o["description"] = c.description
	}
	return o
}

// hasCookie reports whether the request has a cookie called name.
func hasCookie(req Request, name string) bool {
	for b := req.headers; b != ""; {
		var line string
		var ok bool
		if line, b, ok = cutLine(b); !ok {
			return false
		}
		for line != "" {
			var pair string
			pair, line, _ = strings.Cut(line, ";")
			if n, _, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && n == name {
				return true
			}
		}
	}
	return false
}

// optProvider is the optional form of an auth Provider of T, which an Opt[T] field asks for
// (ADR 0069): a request that carries none of its credentials gets an absent Opt and the Provider
// does not run; one that carries one runs it, and its error is the answer, so a credential that
// is not good is still a 401.
type optProvider[T any] struct {
	fn  func(context.Context, Request, struct{}) (T, error)
	doc *authDoc
}

func (p *optProvider[T]) run(f *provFrame, k int) {
	st := &f.steps[k]
	var v Opt[T]
	if p.doc.present(*f.req) {
		u, err := p.fn(f.ctx, *f.req, struct{}{})
		if err != nil {
			f.a.answerError(f.ex, err)
			return
		}
		v = Some(u)
	}
	f.slots[k] = noescapeP(unsafe.Pointer(&v))
	for _, off := range st.into {
		*(*Opt[T])(unsafe.Add(f.in, off)) = v
	}
	if k+1 < len(f.steps) {
		f.steps[k+1].node.run(f, k+1)
	} else {
		f.tail.afterProviders(f)
	}
	f.slots[k] = nil
}

// uniqueHeader is the value of the first line of the header called name and how many lines of
// it the request has.
func (r Request) uniqueHeader(name string) (string, int) {
	var first string
	n := 0
	b := r.headers
	for len(b) > 0 {
		i := strings.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		line := b[:i]
		b = b[i+1:]
		if v, ok := headerLineValue(line, name); ok {
			if n == 0 {
				first = v
			}
			n++
		}
	}
	return first, n
}

// cutScheme returns what follows the auth scheme in an Authorization value, matched without
// regard to case and followed by at least one space (RFC 9110 11.4).
func cutScheme(v, scheme string) (string, bool) {
	n := len(scheme)
	if len(v) <= n || v[n] != ' ' || !foldEq(v[:n], scheme) {
		return "", false
	}
	return strings.TrimLeft(v[n:], " "), true
}

// isB64Token reports whether s is a b64token (RFC 6750 2.1): letters, digits and "-._~+/", then
// any number of "=".
func isB64Token(s string) bool {
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~+/", c) >= 0) {
			break
		}
	}
	if i == 0 {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] != '=' {
			return false
		}
	}
	return true
}

// decodeBasic decodes the token68 of Basic credentials into the request's slab, so the strings
// made over it are the request's own and never pooled memory (ADR 0008).
func (r Request) decodeBasic(s string) (string, bool) {
	if !r.live() || len(s) > 4096 {
		return "", false
	}
	n := base64.StdEncoding.DecodedLen(len(s))
	if n == 0 {
		return "", false
	}
	b := r.run.ex.Slab().Reserve(n)
	m, err := base64.StdEncoding.Decode(b, unsafe.Slice(unsafe.StringData(s), len(s))) // read, never written
	if err != nil || !utf8.Valid(b[:m]) {
		return "", false
	}
	if m == 0 {
		return "", false
	}
	return unsafe.String(&b[0], m), true
}
