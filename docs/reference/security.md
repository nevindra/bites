# Security

**The security middleware and Providers: `SecurityHeaders`, the auth Providers `BearerAuth`, `BasicAuth`, `APIKeyAuth`, `CustomAuth` and `AnyAuth`, the `RateLimiter`, `Session` and its `Sessions`, and `CSRF`.**

**Guide:** [security](../guide/security.md) · **Design:** [security](../design/security.md)

Each is a constructor in the root package that refuses a configuration that cannot work with a `RegistrationError` naming the constructor, the field and the line it was called from ([ADR 0032](../adr/0032-four-first-party-middleware.md)). The client's address and scheme they rely on are [`Request.ClientAddr`](./middleware.md#requestclientaddr) and [`Request.Scheme`](./middleware.md#requestscheme), behind the proxies of [`WithTrustedProxies`](./app.md#withtrustedproxies). A refused request is a problem+json answer like any other, carrying the request id ([errors](./errors.md)).

The order, outermost first: `RequestID`, `CORS`, `SecurityHeaders`, the `RateLimiter` by address, `CSRF`; then the auth Providers, which run after every middleware, and where a limit per user is called.

<!-- compiles: body -->
```go
limiter := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 600, Period: time.Minute})
app.Use(bites.RequestID(), bites.SecurityHeaders(), limiter.Middleware(),
	bites.CSRF(bites.CSRFOptions{Origins: []string{"https://app.example.com"}}))
```

## Security headers

### `SecurityHeaders`

```go
func SecurityHeaders(opts ...SecurityHeadersOptions) Middleware
```

SecurityHeaders adds to every answer, errors included: `Content-Security-Policy` (`DefaultCSP` unless `CSP` is given), `X-Frame-Options` as the CSP's `frame-ancestors` says (`'none'` gives `DENY`, `'self'` gives `SAMEORIGIN`, a list of origins gives none), `X-Content-Type-Options: nosniff`, `Referrer-Policy` (`no-referrer` by default), `Permissions-Policy` (`DefaultPermissionsPolicy` by default), `Cross-Origin-Resource-Policy` (`same-site` by default), `Cross-Origin-Opener-Policy` and `Cross-Origin-Embedder-Policy` (only when given) and, when `Request.Scheme` is `https`, `Strict-Transport-Security` (`max-age=31536000`). It allocates nothing.

A header a handler sets is invisible to middleware, so a route that sends its own CSP under this middleware gets both, and a browser enforces both: put a page group's own `SecurityHeaders` on that group.

It panics when more than one options value is given; the CSP has no `frame-ancestors` (`SecurityHeaders: CSP "default-src 'self'" has no frame-ancestors directive, so any site could frame these answers: ...`) or a byte a header cannot carry; the referrer policy is not one of the eight; the opener, embedder or resource policy is not one of its three values (`SecurityHeaders: CrossOriginOpenerPolicy "same-site" is not one of same-origin, same-origin-allow-popups, unsafe-none`); `PermissionsPolicy` is not a list of `name=value` members, leaves a quote or parenthesis open, or is given with `NoPermissionsPolicy`; `HSTSMaxAge` is negative or not whole seconds; `HSTSPreload` is asked without `HSTSIncludeSubdomains` and a year; or `NoHSTS` is given with an HSTS field.

Decision: [ADR 0053](../adr/0053-security-headers-are-one-middleware-with-safe-defaults.md).

### `SecurityHeadersOptions`

```go
type SecurityHeadersOptions struct {
	CSP                   string
	ReferrerPolicy        string
	HSTSMaxAge            time.Duration
	HSTSIncludeSubdomains bool
	HSTSPreload           bool
	NoHSTS                bool

	PermissionsPolicy         string
	NoPermissionsPolicy       bool
	CrossOriginOpenerPolicy   string
	CrossOriginEmbedderPolicy string
	CrossOriginResourcePolicy string
}
```

SecurityHeadersOptions configures `SecurityHeaders`; the zero value is the safe one. `CSP` must have a `frame-ancestors` directive. `ReferrerPolicy` is one of `no-referrer`, `no-referrer-when-downgrade`, `origin`, `origin-when-cross-origin`, `same-origin`, `strict-origin`, `strict-origin-when-cross-origin`, `unsafe-url`. `HSTSMaxAge` is 365 days when zero. `HSTSIncludeSubdomains` pins every subdomain to HTTPS too, so it is off by default. `NoHSTS` is for an app whose balancer already sends HSTS. `PermissionsPolicy` is a Structured Fields dictionary of `feature=allowlist` members and is `DefaultPermissionsPolicy` when empty; its shape is checked and its feature names are not, since a browser skips the names it does not know; `NoPermissionsPolicy` leaves it out, for a page group that needs the camera, the microphone or the location. `CrossOriginOpenerPolicy` is `same-origin`, `same-origin-allow-popups` or `unsafe-none` and is not sent when empty: `same-origin` breaks an OAuth or payment popup, `same-origin-allow-popups` keeps it. `CrossOriginEmbedderPolicy` is `require-corp`, `credentialless` or `unsafe-none` and is not sent when empty: `require-corp` blocks every cross-origin image, script or frame that does not send `Cross-Origin-Resource-Policy: cross-origin`. `CrossOriginResourcePolicy` is `same-site` when empty, `same-origin` or `cross-origin`: it decides who may load an answer as an image, script or frame, and a `fetch` under CORS is decided by CORS alone; `cross-origin` is for answers other sites embed.

### `DefaultCSP`

```go
const DefaultCSP = "default-src 'none'; frame-ancestors 'none'"
```

DefaultCSP is the policy `SecurityHeaders` sends when it is given none: nothing may load and no page may frame the answer, which is right for a JSON API.

### `DefaultPermissionsPolicy`

```go
const DefaultPermissionsPolicy = "geolocation=(), camera=(), microphone=()"
```

DefaultPermissionsPolicy is the `Permissions-Policy` `SecurityHeaders` sends when it is given none: the geolocation, camera and microphone off for the document and every frame in it, the value the OWASP Secure Headers Project recommends.

## Authentication

### `BearerAuth`

```go
func BearerAuth[T any](verify func(ctx context.Context, token string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error)
```

BearerAuth returns a Provider of `T` that reads a Bearer token (RFC 6750) from `Authorization` and calls `verify` with it. The scheme is matched without regard to case and the token must be a `b64token`. No `Authorization`, or another scheme, is a 401 with `WWW-Authenticate: Bearer realm="api"`; a malformed one (no token, a character a token cannot have, the header twice) is a 400 with `error="invalid_request"`. `verify` returning an error that wraps `ErrInvalidCredentials` is a 401 with `error="invalid_token"`; a `*StatusError` it returns is the answer as it is (a 401 among them given the challenge); any other error is a 500. The token is never logged nor written in an answer, and is not read from the query or a cookie. A JWT verifier is given as `verify`: [`jwt.Verifier.Verify`](./jwt.md#jwtverifierverify). A route that runs the Provider has `bearerAuth` in the API document's `security`, with a 401; `BasicAuth` and `APIKeyAuth` are described the same way ([`Undocumented`](./openapi.md#undocumented) and [ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md) have the rule).

<!-- compiles: decls -->
```go
type User struct{ ID int64 }
```

<!-- compiles: body -->
```go
app.Provide(bites.BearerAuth(func(ctx context.Context, token string) (User, error) {
	if token != "s3cret" { // look the token up by its hash in your store instead
		return User{}, bites.ErrInvalidCredentials
	}
	return User{ID: 1}, nil
}))
```

It panics when `verify` is nil, the realm cannot be quoted, `Header` is set, or more than one options value is given. It allocates nothing for a token that verifies.

Decision: [ADR 0054](../adr/0054-auth-providers-parse-the-credential-and-the-application-verifies-it.md).

### `BasicAuth`

```go
func BasicAuth[T any](verify func(ctx context.Context, user, password string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error)
```

BasicAuth returns a Provider of `T` that reads Basic credentials (RFC 7617) from `Authorization` and calls `verify` with the user-id and the password. None is a 401 with `WWW-Authenticate: Basic realm="api", charset="UTF-8"`; credentials that are not base64, have no colon or are not UTF-8 are a 400; `verify`'s errors are answered as for `BearerAuth`. Compare the password's hash with `crypto/subtle.ConstantTimeCompare`, never with `==`. The decoded strings are in the request's slab, never in a pooled buffer. It panics as `BearerAuth` does.

### `APIKeyAuth`

```go
func APIKeyAuth[T any](verify func(ctx context.Context, key string) (T, error), opts ...AuthOptions) func(context.Context, Request, struct{}) (T, error)
```

APIKeyAuth returns a Provider of `T` that reads an API key from a header of its own, `X-API-Key` unless `AuthOptions.Header` names another, and calls `verify` with it. None, or an empty one, is a 401 with `WWW-Authenticate: APIKey realm="api", header="X-API-Key"`; the header twice, or a key with a space or a control character, is a 400; `verify`'s errors are answered as for `BearerAuth`. Look a key up by its hash, so the stored keys are not secrets. It panics as `BearerAuth` does, and when `Header` is not a token or is `Authorization` or `Cookie`. It allocates nothing for a key that verifies.

### `AnyAuth`

```go
func AnyAuth[T any](providers ...func(ctx context.Context, req Request, deps struct{}) (T, error)) func(context.Context, Request, struct{}) (T, error)
```

AnyAuth returns a Provider of `T` that accepts any one of several credentials: a Bearer token or an API key, a session cookie or a token ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)). Each of `providers` must be made by `BearerAuth`, `BasicAuth`, `APIKeyAuth`, [`CustomAuth`](#customauth) or `AnyAuth`, so that where its credential is sent is known; a nested `AnyAuth`'s alternatives are taken in their order. The first Provider, in the order given, whose credential the request carries runs, and its answer is the answer: a credential that is not good is its 401 or 400, and the next one is not tried. A request that carries none is answered by the first, whose 401 gets the challenges of the others (`WWW-Authenticate` once for each). The API document lists each credential as an alternative requirement of the route's `security` (`[{"bearerAuth": []}, {"apiKeyAuth": []}]`). It adds, per request, a look at where each credential before the one sent would be, and no allocation.

<!-- compiles: body -->
```go
type Caller struct{ ID string }
app.Provide(bites.AnyAuth(
	bites.BearerAuth(func(ctx context.Context, token string) (Caller, error) { return Caller{ID: "ada"}, nil }),
	bites.APIKeyAuth(func(ctx context.Context, key string) (Caller, error) { return Caller{ID: "svc"}, nil }),
))
```

It panics when fewer than two Providers are given, one is nil or was not made by those five, or two read the same credential (the bearer scheme of `Authorization` from `BearerAuth` and from a `CustomAuth` of `Scheme: "bearer"`, say), since the second would never run.

**An optional credential is an `Opt` field.** An Input field, or a Provider's dependency, of type `Opt[T]` that no Provider of `Opt[T]` covers is filled by the optional form of the Provider of `T` when that one is made by the five constructors here: a request that carries none of its credentials gets an absent `Opt` and the Provider does not run; one that carries one runs it, and its error is the answer, so an expired token is a 401 and not an anonymous request. The document adds the empty requirement `{}` to the route's `security`. A route that needs both `T` and `Opt[T]` is refused, since the credential is required there anyway; an `Opt[T]` whose `T` has a Provider the application wrote without `CustomAuth` is refused, since where its credential is sent is not known. It allocates nothing.

<!-- compiles: body -->
```go
type Reader struct{ ID string }
app.Provide(bites.BearerAuth(func(ctx context.Context, token string) (Reader, error) { return Reader{ID: "ada"}, nil }))
app.Get("/articles/{id}", func(ctx context.Context, in struct {
	Path   struct{ ID int64 }
	Reader bites.Opt[Reader]
}) (bites.Text, error) {
	if r, ok := in.Reader.Get(); ok {
		return bites.Text{Body: "for " + r.ID}, nil
	}
	return bites.Text{Body: "for anyone"}, nil
})
```

### `CustomAuth`

```go
func CustomAuth[T, D any](scheme SecurityScheme, provider func(ctx context.Context, req Request, deps D) (T, error)) func(context.Context, Request, D) (T, error)
```

CustomAuth returns `provider`, a Provider the application wrote that reads a credential, together with the [`SecurityScheme`](#securityscheme) it reads ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)). The API document states the scheme on every route that runs it, with its 401, as for `BearerAuth`; `AnyAuth` can offer it among other credentials, and an `Opt` field can make it optional, since the scheme says where the credential is sent. The Provider may have dependencies like any Provider (a user read from a session asks for `Session[Login]`); only one with none (`struct{}`) can be given to `AnyAuth` or made optional, since both call it without filling anything first. The Provider is called as it is, never at Registration, and CustomAuth adds one comparison a request and no allocation. A Provider that needs other provided types cannot be given: it calls the function of the one it needs itself (a `Sessions.Load`, say). The framework cannot read `provider`, so the scheme is the application's word for what it reads.

<!-- compiles: body -->
```go
type Member struct{ ID string }
app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"},
	func(ctx context.Context, req bites.Request, _ struct{}) (Member, error) {
		return Member{ID: "ada"}, nil // read the cookie and look the session up instead
	}))
```

It panics when `provider` is nil, `Type` is not one of the four, a field `Type` has is missing or malformed, or a field `Type` does not have is given; the messages are rows of the refusal table.

### `SecurityScheme`

```go
type SecurityScheme struct {
	Type             string
	Scheme           string
	BearerFormat     string
	In               string
	Name             string
	OpenIDConnectURL string
	Description      string
}
```

SecurityScheme is how a request sends the credential a [`CustomAuth`](#customauth) Provider reads, as the API document states it. `Type` is one of four, each with its own fields: `"http"` with `Scheme`, the auth scheme of `Authorization` (`"bearer"`, `"digest"`; written in lower case), and `BearerFormat` for a bearer token (`"JWT"`); `"apiKey"` with `In`, `"header"` or `"cookie"`, and `Name`, the header's or the cookie's; `"mutualTLS"`, a client certificate, with no field of its own; `"openIdConnect"` with `OpenIDConnectURL`, the discovery document, the token sent as a Bearer token. `Description` is prose for any of them. A key in the query is refused, for the reason `APIKeyAuth` reads none: a query is written in access logs. OAuth 2 is not a `Type`: its flows and scopes are not described, so an OAuth 2 access token is `"http"` with `Scheme: "bearer"`.

| Mistake | Message |
|---|---|
| a nil Provider | `CustomAuth: the Provider is nil` |
| a `Type` it cannot state | `CustomAuth: Type "oauth2" is not a scheme CustomAuth states: give "http", "apiKey", "mutualTLS" or "openIdConnect" (an OAuth 2 access token is Type "http" with Scheme "bearer")` |
| `"http"` with no `Scheme` | `CustomAuth: Scheme "" is not an HTTP auth scheme: give a token, such as "bearer" or "digest"` |
| `BearerFormat` for another scheme | `CustomAuth: BearerFormat is given, but Scheme is "basic": a bearer format describes a Bearer token` |
| a key anywhere but a header, a cookie or the query | `CustomAuth: In "body" is not where an API key is described: give "header", "cookie" or, for a Provider that reads a Query section on a WebSocket or event stream route, "query"` |
| a key in `Authorization` or `Cookie` | `CustomAuth: Name "Authorization" is not the header of a key: give a header name of its own, such as X-API-Key (Authorization is Type "http", a cookie is In "cookie")` |
| a cookie name that is not a token | `CustomAuth: Name "my sid" is not a cookie name: use an HTTP token` |
| a discovery URL that is not absolute | `CustomAuth: OpenIDConnectURL "/.well-known/openid-configuration" is not an absolute URL: give the provider's discovery document, such as https://id.example.com/.well-known/openid-configuration` |
| a field its `Type` does not have | `CustomAuth: Name is given, but a scheme of Type "mutualTLS" has no Name` |

### `AuthOptions`

```go
type AuthOptions struct {
	Realm  string
	Header string
}
```

AuthOptions configures the three auth Providers. `Realm` is the realm of the challenge, `api` when empty, printable ASCII without a double quote or a backslash. `Header` is the header `APIKeyAuth` reads, `X-API-Key` when empty; it is refused on `BearerAuth` and `BasicAuth`.

### `ErrInvalidCredentials`

```go
var ErrInvalidCredentials = errors.New("bites: invalid credentials")
```

ErrInvalidCredentials is what a verification function returns, wrapped or not, for a credential that is not good: unknown, expired, revoked, wrongly signed. The answer is a 401 with the scheme's challenge and a fixed detail; the error's own text never reaches the client. Any other error from a verifier is a failure to verify and is a 500, so an outage never tells every client its credential is wrong.

## Rate limiting

### `NewRateLimiter`

```go
func NewRateLimiter(o RateLimitOptions) *RateLimiter
```

NewRateLimiter returns a token-bucket limiter. It panics when `Limit` or `Period` is not positive, `Period` is not whole seconds, the limit is more than one request a nanosecond, `Burst` or `MaxKeys` is negative, the bucket takes more than ten years to refill, `MaxKeys` is given with a `Store`, or `Name` is not up to 16 letters, digits, `-` or `_`.

Decision: [ADR 0055](../adr/0055-rate-limiting-is-a-token-bucket-per-key-with-a-bounded-store.md).

### `RateLimiter`

```go
type RateLimiter struct {
	// unexported fields
}
```

RateLimiter limits requests by key: a key may make `Burst` requests at once and earns one back every `Period/Limit`. One `RateLimiter` is one set of buckets, shared by every place it is used.

### `RateLimiter.Middleware`

```go
func (l *RateLimiter) Middleware() Middleware
```

Middleware limits every request it sees by `RateLimitOptions.Key`, or by `Request.ClientAddr` when that is nil, an IPv6 client keyed by its /64. An allowed answer carries `RateLimit-Policy: "default";q=100;w=60` and `RateLimit: "default";r=57;t=3`; a request over the limit is a 429 problem+json with `Retry-After` and the same two headers, and goes no further. On the app it also counts 404s. With the in-memory store and the default key it allocates nothing for a key it holds. Every operation behind it, on the route, a group or the app, has a 429 in the API document ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).

### `RateLimiter.Allow`

```go
func (l *RateLimiter) Allow(ctx context.Context, key string) error
```

Allow spends one request of `key`'s bucket: nil when the request may go on, a 429 `*StatusError` with `Retry-After`, `RateLimit-Policy` and `RateLimit` when it may not, and the store's error when the store failed. `ctx` is the request's, or one derived from it, and the bucket is measured on the app's clock ([`Now`](./app.md#now)); on another context it is the real clock. A limit per user is the user's Provider calling it, since middleware runs before any Provider, and a limit by a key in the body (the email of a sign-in) is the handler calling it with `in.Body.Email` and returning its error:

<!-- compiles: body -->
```go
perUser := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 60, Period: time.Minute})
app.Provide(func(ctx context.Context, req bites.Request, _ struct{}) (User, error) {
	u := User{ID: 1} // authenticate first
	return u, perUser.Allow(ctx, strconv.FormatInt(u.ID, 10))
})
```

The API document cannot see a call inside a Provider or a handler, so give the routes [`ErrorStatuses(429)`](./openapi.md#errorstatuses): their 429 is then documented as the rate limit's answer, with its headers.

### `RateLimitOptions`

```go
type RateLimitOptions struct {
	Limit   int
	Period  time.Duration
	Burst   int
	Key     func(Request) string
	Store   RateLimitStore
	MaxKeys int
	Name    string
}
```

RateLimitOptions configures `NewRateLimiter`. `Limit` requests a `Period` (whole seconds) are required. `Burst` is `Limit` when zero. `Key` replaces the client address for `Middleware`; a key of `""` is one bucket shared by every request that has it. `Store` is a shared store; without it the in-memory store holds at most `MaxKeys` keys (100,000 when zero, about 50 B each), dropping first the keys whose bucket is full again. `Name` is the policy's name in the headers, `default` when empty.

### `RateLimitStore`

```go
type RateLimitStore interface {
	Take(ctx context.Context, key string, now time.Time, burst int, interval time.Duration) (RateLimitDecision, error)
}
```

RateLimitStore keeps buckets outside the process, so several instances share them. `Take` spends one request of `key`'s bucket of `burst` requests that earns one back every `interval`, atomically for a key. Its error is the answer (a 500): the limiter fails closed, and a store that should fail open returns `Allowed` and swallows its error. The key is the address as text, or its `/64` prefix for IPv6.

### `RateLimitDecision`

```go
type RateLimitDecision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	Reset      time.Duration
}
```

RateLimitDecision is what a store answers: whether the request may go on, how many more the key may make now, how long until one is allowed when it is not, and how long until the bucket is full.

## Sessions

### `NewSessions`

```go
func NewSessions[T any](o SessionOptions) *Sessions[T]
```

NewSessions returns the `Sessions` of type `T`, whose `Load` is the Provider of `Session[T]`. It panics when there is no key, a key is not `SessionKeyLen` bytes, is all zeros or is given twice, the cookie name is not a token or its prefix cannot hold with the other options, `MaxAge` is negative or not whole seconds, `SameSite` is `NoSameSite` without `Secure`, `T` cannot be encoded as JSON, or `T` has a `SessionOwner` method and the `Store` is missing or is not an `OwnerSessionStore`, or the method is on `*T` or is not `func() string` (the rows are under [`OwnerSessionStore`](#ownersessionstore)), or `RefreshAfter` is negative or not under `MaxAge`:

| Mistake | Message |
|---|---|
| `RefreshAfter` is negative or not under `MaxAge` | `RefreshAfter is 1h0m0s, but it must be under MaxAge (1h0m0s) and not negative, or a session would expire before it is refreshed: give MaxAge/2, or zero for a session that does not slide` |

Decision: [ADR 0056](../adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md).

### `Sessions`

```go
type Sessions[T any] struct {
	// unexported fields
}
```

Sessions seals and opens the session cookie of type `T` with AES-256-GCM, or keeps the value in a `SessionStore` with a sealed id in the cookie.

### `Sessions.Load`

```go
func (s *Sessions[T]) Load(ctx context.Context, req Request, _ struct{}) (Session[T], error)
```

Load is the Provider of `Session[T]`, given as `app.Provide(sessions.Load)`. A request with no cookie, one that opens with no key, or one past its `MaxAge` is a new session (`IsNew` true, `Value` zero), not an error; requiring a session is the application's own Provider. A store's error is a 500.

### `Session`

```go
type Session[T any] struct {
	Value T
	// unexported fields
}
```

Session is the request's session: the value the application keeps for a browser, filled by `Sessions.Load` into an Input field. Changing `Value` changes nothing by itself; `Save`, `Renew` and `Clear` return the `Cookie` the handler puts in its answer's `Cookies`, so the answer stays the return value.

### `Session.IsNew`

```go
func (s Session[T]) IsNew() bool
```

IsNew reports whether the request came with no session: no cookie, one that did not open, or one that had expired.

### `Session.Save`

```go
func (s Session[T]) Save(ctx context.Context, v T) (Cookie, error)
```

Save makes `v` the session's value for `MaxAge` from now and returns the cookie that carries it. With a store, `v` is saved there under the session's id (a new one for a new session). It is an error when the `Set-Cookie` would be over 4096 bytes, which a browser drops, and when the store fails.

### `Session.Renew`

```go
func (s Session[T]) Renew(ctx context.Context, v T) (Cookie, error)
```

Renew is `Save` under a new id, deleting the old one from the store. Call it at login, so an id that was planted or seen before cannot follow the session up. Without a store it is `Save`.

### `Session.Clear`

```go
func (s Session[T]) Clear(ctx context.Context) (Cookie, error)
```

Clear deletes the session from the store and returns the cookie that deletes it from the browser (`Max-Age=0`). Without a store, a copy of the cookie taken before still opens until it expires: a session that must be revocable needs a store.

### `Session.RevokeOthers`

```go
func (s Session[T]) RevokeOthers(ctx context.Context) (int, error)
```

RevokeOthers ends every other session of this session's owner (from `s.Value.SessionOwner()`) and keeps this one, and returns how many it ended: "sign out everywhere else", and a password change. The browser keeps its cookie, so the handler returns none. It needs what [`RevokeOwner`](#sessionsrevokeowner) needs, a `T` with `SessionOwner` and an `OwnerSessionStore`, and returns an error, ending nothing, for: a new session (it is not in the store; `Save` it first); an empty owner; and a session whose id this request already replaced with `Renew` or deleted with `Clear`, found by loading its own id before deleting anything (the error says to call `RevokeOthers` before `Renew`). To renew the id as well, call `RevokeOthers` first and `Renew` after.

```go
n, err := login.RevokeOthers(ctx) // n other sessions of this User ended
```

Decision: [ADR 0056](../adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md).

### `SessionOptions`

```go
type SessionOptions struct {
	Keys         [][]byte
	Cookie       string
	MaxAge       time.Duration
	Path, Domain string
	SameSite     SameSite
	NotSecure    bool
	Store        SessionStore
	RefreshAfter time.Duration
}
```

SessionOptions configures `NewSessions`. `Keys` are 32 random bytes each; the first seals and every one opens, so a key is rotated by putting the new one first and dropping the old one after `MaxAge`. `Cookie` is `__Host-session` when empty (`session` when `Domain`, another `Path` or `NotSecure` rule the prefix out). `MaxAge` is 24 hours when zero, sealed in the cookie and checked on every request. The cookie is always `HttpOnly`, and `Secure` and `SameSite=Lax` unless loosened. `RefreshAfter` makes a session slide: a request whose session was saved more than `RefreshAfter` ago gets it saved again by the framework, as `Save` would with the value loaded, so a session in use lives `MaxAge` from its last use and one left alone still expires. `Load` saves it (in the `Store`, with the new expiry, under the same id) and adds the new cookie to the answer when its status is under 400 and the answer sets no cookie of the session's name itself, so a `Save`, `Renew` or `Clear` of the handler wins; a WebSocket's 101 is not refreshed. Zero is never. It must be under `MaxAge`, and `MaxAge/2` saves a session at most twice a `MaxAge`. A refresh that fails is logged at Warn (`bites: a session was not refreshed; it keeps the expiry of its last Save`) and the request goes on. A refresh racing `RevokeOwner` can put a deleted row back, as a `Save` can; the Provider of the User must refuse a disabled User on every request.

### `SessionStore`

```go
type SessionStore interface {
	Load(ctx context.Context, id string) (data []byte, found bool, err error)
	Save(ctx context.Context, id string, data []byte, expires time.Time) error
	Delete(ctx context.Context, id string) error
}
```

SessionStore keeps sessions on the server, for a value too large for a cookie or a session that must be revoked at once. An id is 22 characters of base64url over 16 random bytes; the data is the JSON of `T`. `Load` judges whether a session has expired on `bites.Now(ctx)`, the clock that computed `expires`, never on its own clock or a database's `now()`, so a store and the cookie agree and a test on `bitetest.Clock` moves both; a store with a TTL of its own (Redis) sets it to `expires` minus `bites.Now(ctx)`. `Save` is not called for a `T` that has a `SessionOwner` method, whose saves all go through [`OwnerSessionStore.SaveOwned`](#ownersessionstore), so a store written only for owned types may return an error from it.

### `OwnerSessionStore`

```go
type OwnerSessionStore interface {
	SessionStore
	SaveOwned(ctx context.Context, id, owner string, data []byte, expires time.Time) error
	DeleteOwner(ctx context.Context, owner, keep string) (int, error)
}
```

OwnerSessionStore is a `SessionStore` that can find every session of one owner, the User a session belongs to. A session type `T` with a `SessionOwner` method needs one: every `Save` and `Renew` then calls `SaveOwned` with the owner, which replaces the owner the id had (an empty owner is a session no owner reaches), and `Sessions.RevokeOwner` calls `DeleteOwner` with an empty `keep`. `DeleteOwner` forgets every session of `owner` but the one whose id is `keep` (an empty `keep` keeps none; `Session.RevokeOthers` passes its own id), returns how many sessions it forgot and is `(0, nil)` for an owner with none. A store written before `keep` existed must add the parameter. A store that is an `OwnerSessionStore` is not told an owner for a `T` without `SessionOwner`: `Save` is called as before; for a `T` with one, `Save` is never called. The store is told no tenant or other column: what it needs beyond the owner it reads from its own rows of that owner. Two `Sessions` types over one store share its owners.

Refused by `NewSessions` (after the label `NewSessions:`):

| Mistake | Message |
|---|---|
| `T` has `SessionOwner` and there is no `Store` | `Login states an owner (a SessionOwner method) but SessionOptions has no Store, and a session sealed in a cookie cannot be revoked: give a Store that implements OwnerSessionStore, or drop the SessionOwner method` |
| `T` has `SessionOwner` and the `Store` is not an `OwnerSessionStore` | `Login states an owner (a SessionOwner method) but the Store *x.Redis does not implement OwnerSessionStore (SaveOwned and DeleteOwner), so it cannot find the sessions of one owner: add those two methods to the store, or drop the SessionOwner method` |
| `SessionOwner` is declared on `*T` | `SessionOwner is declared on *Login but a Session holds a Login, so it would never be found: declare it on the value receiver, func (Login) SessionOwner() string` |
| `SessionOwner` is not `func() string` | `Login has a SessionOwner method of type func(x.Login) int; it must be func() string, returning the owner of the session` |

Decision: [ADR 0056](../adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md).

### `Sessions.RevokeOwner`

```go
func (s *Sessions[T]) RevokeOwner(ctx context.Context, owner string) (int, error)
```

RevokeOwner ends every session of `owner` at once and returns how many it ended: "sign out everywhere", and a User who has been disabled. `T` must have `func (T) SessionOwner() string` and the `Store` must be an `OwnerSessionStore`, both checked by `NewSessions`; for a `T` without `SessionOwner` it returns an error saying what to add, and an empty owner is an error too, since it names no one. A browser that holds the cookie of an ended session is a new session on its next request. A request that loaded the session before the call and saves it after puts it back, so the Provider of the User must refuse a disabled User on every request. To close the User's sockets as well, call [`SocketOwners.Close`](./websocket.md#socketownersclose) after this.

```go
func (l Login) SessionOwner() string { return l.UserID }

n, err := logins.RevokeOwner(ctx, userID) // n sessions ended
```

### `SessionKeyLen`

```go
const SessionKeyLen = 32
```

SessionKeyLen is the length of a session key, for AES-256-GCM. Generate one with `crypto/rand` and keep it out of the source.

## Cross-site request forgery

### `CSRF`

```go
func CSRF(o CSRFOptions) Middleware
```

CSRF lets a state-changing request through only when the browser says it came from a listed origin, or, from a client that says nothing, when it carries the token. `GET`, `HEAD`, `OPTIONS` and `TRACE` pass, and one without the token cookie gets it. A request with no `Cookie` header and no Basic credentials passes, except a form's body (`application/x-www-form-urlencoded`, `multipart/form-data` or `text/plain`) with no `Authorization` header, which the browser says came from an origin not listed, is refused even with no cookie, because a page elsewhere could post the attacker's own credentials to a sign-in form and leave the browser signed in to the attacker's account (login CSRF). `Sec-Fetch-Site: same-origin` or `none` passes; `same-site` or `cross-site` passes only with a listed `Origin`. Without `Sec-Fetch-Site`, a listed `Origin` passes and any other (`null` included) is refused. With neither, the `X-CSRF-Token` header must equal the token cookie, compared in constant time. A refused request is a 403 problem+json. A request that passes allocates nothing.

It panics when an origin is `"*"` or not a full origin, the cookie or header name is not a token, or a `__Host-` or `__Secure-` cookie is asked for with `NotSecure`. With no origin of its own it reads the app's list, and is refused when it is given to an app with none, or to one with `WithAllowAnyOrigin()` (below, and [ADR 0057](../adr/0057-csrf-trusts-the-browsers-origin-and-falls-back-to-a-double-submit-token.md)).

An operation behind it whose method is not `GET`, `HEAD`, `OPTIONS` or `TRACE` has a 403 in the API document ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)).

Decision: [ADR 0057](../adr/0057-csrf-trusts-the-browsers-origin-and-falls-back-to-a-double-submit-token.md).

### `CSRFOptions`

```go
type CSRFOptions struct {
	Origins   []string
	Cookie    string
	Header    string
	NotSecure bool
}
```

CSRFOptions configures `CSRF`. `Origins` are full origins, the app's own included, matched exactly as CORS matches them and compared with `Origin`, never with `Host`. Empty means the app's list, the origins given to [`WithAllowOrigins`](./websocket.md#withalloworigins), so an app with forms and sockets names its pages once; the middleware reads them when a request arrives, and is refused when it is given to the app (`App.Use`, or a group's or a route's `Use`), not in `CSRF`,  when the app has no list (`CSRF: Origins is empty and the app has no list to take them from: ...`) or its list is `WithAllowAnyOrigin()`. A list given here is the middleware's own and is not compared with the app's. CORS's `Origins` do not default to the app's list, because they name a different trust. `Cookie` is `__Host-csrf` when empty (`csrf` with `NotSecure`); the cookie is not `HttpOnly`, because the page's script reads it and sends it back in `Header`, `X-CSRF-Token` when empty.
