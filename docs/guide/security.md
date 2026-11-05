# Security

**Put security headers on every answer, authenticate with a Bearer token (a JWT through the `jwt` module), Basic auth or an API key through a Provider, limit requests per client or per user, keep a `Session` in a sealed cookie, and refuse cross-site forgeries.**

**Reference:** [Security](../reference/security.md), [`jwt`](../reference/jwt.md) · **Design:** [Security](../design/security.md)

Each piece is a constructor that refuses a configuration that cannot work when your program starts, and each refusal of a request is a problem+json answer like every other error ([errors](./errors.md)). None of them costs the typed request an allocation; a route with a `Session` pays for its cookie ([ADR 0003](../adr/0003-the-trade-budget.md)). The examples share these types:

<!-- compiles: decls -->
```go
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Accounts stands in for the struct that owns a database pool.
type Accounts interface {
	ByTokenHash(ctx context.Context, hash string) (User, bool, error)
	ByPassword(ctx context.Context, name, password string) (User, bool, error)
}

type Profile struct {
	User User
}

func profile(ctx context.Context, in Profile) (User, error) { return in.User, nil }
```

## Behind a load balancer first

Rate limiting by address, HSTS and the access log all need the client's real address and scheme. Behind a balancer, name it with [`WithTrustedProxies`](../reference/app.md#withtrustedproxies), or every client has the balancer's address ([deploying](./deploying.md#behind-a-load-balancer)). With no proxy named, nothing a client sends can change its address.

## Security headers on every answer

**[`SecurityHeaders`](../reference/security.md#securityheaders) with no options is right for a JSON API.** It adds a `Content-Security-Policy` that lets nothing load and forbids framing, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Permissions-Policy: geolocation=(), camera=(), microphone=()`, `Cross-Origin-Resource-Policy: same-site`, and, when the request came over HTTPS, `Strict-Transport-Security` for a year.

<!-- compiles: body -->
```go
app.Use(bites.RequestID(), bites.SecurityHeaders())
```

```http
GET /users/7 HTTP/1.1

HTTP/1.1 200 OK
Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
X-Frame-Options: DENY
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: geolocation=(), camera=(), microphone=()
Cross-Origin-Resource-Policy: same-site
Strict-Transport-Security: max-age=31536000
```

**Pages that serve HTML get their own policy on their own group**, because a header your handler sets is invisible to middleware, and a browser that receives two policies enforces both. The frame policy is the CSP's `frame-ancestors`, and `X-Frame-Options` follows it; a policy without `frame-ancestors` is refused, since it would leave the pages frameable.

<!-- compiles: body -->
```go
pages := app.Group("/app", bites.Use(bites.SecurityHeaders(bites.SecurityHeadersOptions{
	CSP:            "default-src 'self'; frame-ancestors 'self'",
	ReferrerPolicy: "strict-origin-when-cross-origin",
})))
_ = pages
```

**The cross-origin headers that change what a page may open or load are off until you ask.** `Cross-Origin-Opener-Policy` and `Cross-Origin-Embedder-Policy` are not sent by default, because `same-origin` cuts the reference to an OAuth or payment popup and `require-corp` blocks every cross-origin image or script that does not send `Cross-Origin-Resource-Policy: cross-origin`. A page group that needs cross-origin isolation (`SharedArrayBuffer`) asks for `same-origin` with `credentialless` or `require-corp`; `same-origin-allow-popups` keeps the popups and does not isolate; a page group that is a video call or a map turns the `Permissions-Policy` it needs on, or `NoPermissionsPolicy` off. The default `Cross-Origin-Resource-Policy: same-site` stops another site from loading your answers as an image or script, and does not touch a `fetch` under CORS; give `cross-origin` for an avatar or widget script other sites embed.

<!-- compiles: body -->
```go
isolated := app.Group("/studio", bites.Use(bites.SecurityHeaders(bites.SecurityHeadersOptions{
	CSP:                       "default-src 'self'; frame-ancestors 'self'",
	CrossOriginOpenerPolicy:   "same-origin",
	CrossOriginEmbedderPolicy: "credentialless",
	PermissionsPolicy:         "camera=(self), microphone=(self), geolocation=()",
})))
_ = isolated
```

## Authenticating a caller

**An auth Provider reads the credential; your function decides whether it is good.** [`BearerAuth`](../reference/security.md#bearerauth) reads `Authorization: Bearer ...`, calls your function with the token, and gives your type to every Input that asks for it. The function is best a method of the service that owns the accounts, given as a method value as a handler is ([ADR 0014](../adr/0014-services-are-plain-go-values.md)). A route whose Input has a `User` field and no Provider covering it is refused at Registration ([providers](./providers.md)).

<!-- compiles: decls -->
```go
// Auth decides whether a credential is good; its methods are given to the auth Providers.
type Auth struct {
	accounts Accounts
}

func (a *Auth) VerifyToken(ctx context.Context, token string) (User, error) {
	sum := sha256.Sum256([]byte(token))
	u, ok, err := a.accounts.ByTokenHash(ctx, hex.EncodeToString(sum[:]))
	switch {
	case err != nil:
		return User{}, err // the store failed: a 500, and the client keeps its token
	case !ok:
		return User{}, bites.ErrInvalidCredentials // a 401 with error="invalid_token"
	}
	return u, nil
}
```

<!-- compiles: body -->
```go
var accounts Accounts // your database
auth := &Auth{accounts: accounts}
app.Provide(bites.BearerAuth(auth.VerifyToken))
app.Get("/me", profile)
```

What a client gets:

```http
GET /me HTTP/1.1

HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="api"
Content-Type: application/problem+json

{"title":"Unauthorized","status":401,"detail":"a Bearer token is required"}
```

- **An error that wraps [`ErrInvalidCredentials`](../reference/security.md#errinvalidcredentials)** is a 401 with `error="invalid_token"`, which an OAuth client reads as "refresh the token".
- **A status error** is the answer as it is: return `bites.Forbidden("...")` for a token without the scope.
- **Any other error** is a 500: your verifier could not decide. A 401 there would tell every client its token is wrong whenever the database is down.
- A malformed header (the `Bearer` scheme with no token or with characters a token cannot hold, or two `Authorization` headers) is a 400; a request with no Bearer credential at all is the 401 above. The token is never logged and never written in an answer.

**An API key** in its own header is [`APIKeyAuth`](../reference/security.md#apikeyauth), and **Basic auth** is [`BasicAuth`](../reference/security.md#basicauth), whose function gets the user-id and the password:

<!-- compiles: decls -->
```go
func (a *Auth) VerifyPassword(ctx context.Context, name, password string) (User, error) {
	u, ok, err := a.accounts.ByPassword(ctx, name, password) // compare hashes in constant time
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, bites.ErrInvalidCredentials
	}
	return u, nil
}
```

<!-- compiles: body -->
```go
auth := &Auth{}
app.Provide(bites.BasicAuth(auth.VerifyPassword, bites.AuthOptions{Realm: "admin"}))
```

Look keys and tokens up by a hash of them, so what you store is not a secret and nothing compares a secret byte by byte. Serve Basic auth only over HTTPS: it sends the password with every request.

### Accepting a token or an API key

**[`AnyAuth`](../reference/security.md#anyauth) is one Provider of several credentials.** Give it the Providers, each of one type; the first whose credential the request carries runs:

<!-- compiles: decls -->
```go
func (a *Auth) VerifyKey(ctx context.Context, key string) (User, error) {
	sum := sha256.Sum256([]byte(key))
	u, ok, err := a.accounts.ByTokenHash(ctx, hex.EncodeToString(sum[:]))
	if err != nil || !ok {
		return User{}, errors.Join(err, bites.ErrInvalidCredentials)
	}
	return u, nil
}
```

<!-- compiles: body -->
```go
auth := &Auth{}
app.Provide(bites.AnyAuth(bites.BearerAuth(auth.VerifyToken), bites.APIKeyAuth(auth.VerifyKey)))
```

- **The first credential sent decides.** A request with a Bearer token runs the token's verifier, one with only `X-API-Key` runs the key's, one with both runs the token's and ignores the key.
- **A credential that is not good is its own answer.** An expired token is a 401 with `error="invalid_token"` even when a good key is also sent: the next alternative is not tried.
- **A request with none** is a 401 whose `WWW-Authenticate` names each scheme, once a line.

### A route for signed-in and anonymous callers

**Ask for `bites.Opt[User]` instead of `User`.** The route then runs the `User` Provider only when the request carries its credential, and the field is absent when it does not:

<!-- compiles: decls -->
```go
type Article struct {
	Path   struct{ ID int64 }
	Reader bites.Opt[User]
}

func article(ctx context.Context, in Article) (bites.Text, error) {
	if u, ok := in.Reader.Get(); ok {
		return bites.Text{Body: "the article, with " + u.Name + "'s notes"}, nil
	}
	return bites.Text{Body: "the article"}, nil
}
```

A credential that is sent and not good is still a 401, never an anonymous request: a client with an expired token learns to refresh it. This works for a Provider made by `BearerAuth`, `BasicAuth`, `APIKeyAuth`, `AnyAuth` or `CustomAuth`, since the framework must know where the credential would be. A route that asks for both `User` and `Opt[User]` is refused at Registration: the credential is required there.

### A Provider you wrote, in the API document

**Give your own auth Provider to [`CustomAuth`](../reference/security.md#customauth) with the [`SecurityScheme`](../reference/security.md#securityscheme) it reads.** It runs as it is; the document states the scheme, and `AnyAuth` and an `Opt` field can use it:

<!-- compiles: decls -->
```go
func (a *Auth) FromSessionCookie(ctx context.Context, req bites.Request, _ struct{}) (User, error) {
	return User{}, bites.Unauthorized("no session") // read the cookie and find the session
}
```

<!-- compiles: body -->
```go
auth := &Auth{}
app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"}, auth.FromSessionCookie))
```

The scheme is your word for what your Provider reads; the framework cannot check it. `Type` is `"http"` (with `Scheme`, and `BearerFormat` such as `"JWT"`), `"apiKey"` in a header or a cookie, `"mutualTLS"` for a client certificate, or `"openIdConnect"` with the discovery URL. The Provider may have dependencies, as any Provider: the user read from a session (below) asks for the `Session` and is still given to `CustomAuth`. Only a Provider with none can be an `AnyAuth` alternative or made optional by an `Opt` field.

### Verifying a JWT

The core parses no token. The [`jwt` module](../reference/jwt.md) is a verifier whose `Verify` method is the function `BearerAuth` takes ([ADR 0062](../adr/0062-jwt-verification-is-a-module-whose-key-fixes-the-algorithm.md)):

```go
import "github.com/nevindra/bites/modules/jwt"

type Claims struct {
	Subject string `json:"sub"`
	Scope   string `json:"scope"`
}

v := jwt.New[Claims](jwt.Config{
	Keys:     jwt.JWKS("https://id.example.com/.well-known/jwks.json", jwt.JWKSOptions{}),
	Issuer:   "https://id.example.com",
	Audience: "orders-api",
	Clock:    app.Clock(),
})
app.Provide(bites.BearerAuth(v.Verify))
```

- **The key decides the algorithm.** A key is made with `jwt.HS256`, `jwt.RS256` or `jwt.ES256`, and a token whose header names another algorithm, `none` included, is refused before its signature is read.
- **Keys rotate by `kid`.** `jwt.Keys(old, new)` holds a fixed set, and a token must name its key when there are several; `jwt.JWKS` fetches your identity provider's set on the first token, again after an hour, and when a token names a key it lacks, at most once a minute whatever clients send.
- **The claims.** `exp` is required; `exp`, `nbf` and `iat` are checked on the app's clock with 30 seconds of leeway (`Leeway`), and `iss` and `aud` when `Issuer` and `Audience` are set. The payload is then decoded into your type.
- **The answers.** A bad, expired or foreign token is a 401 with `error="invalid_token"`; a key set endpoint that is down is a 500, logged, and clients keep their tokens.

The `jwt` module uses the standard library's crypto and, for `JWKS`, `net/http`. Add it with `go get github.com/nevindra/bites/modules/jwt` (until the first release tags the modules, from inside the repository; see [the API documentation UI](./api-docs-ui.md) for the same caveat).

### What the API document says about it

The [OpenAPI document](./openapi.md) reads the security of each route from what the route runs ([ADR 0069](../adr/0069-the-api-document-states-what-a-routes-providers-and-middleware-ask-and-answer-and-a-route-can-be-left-out.md)): a route whose Input asks for the `User` that `BearerAuth` provides has `security: [{"bearerAuth": []}]` and a 401, `APIKeyAuth` is an `apiKeyAuth` scheme naming its header, `BasicAuth` is `basicAuth`; an `AnyAuth` lists each of its credentials as an alternative (`[{"bearerAuth": []}, {"apiKeyAuth": []}]`), an `Opt` field adds the empty one (`[{"bearerAuth": []}, {}]`), and a `CustomAuth` states its scheme; a route behind a `RateLimiter`'s middleware has a 429 and one behind `CSRF` a 403 on its POST, PUT, PATCH and DELETE. A Provider you write yourself and do not give to `CustomAuth` is not recognised, so its routes have no `security` in the document.

**A permission for every route, refused at start when one is missing, is [`UseEach`](../reference/middleware.md#useeach).** An authorization check keyed on the operation ID, with an exempt list and an audit row of the final status, is written once and built for each route; a route without an entry stops the program instead of being served open, and that includes the API document and the metrics page, which the function sees marked `Builtin`. See [Default-deny: a middleware built for each route](./middleware.md#default-deny-a-middleware-built-for-each-route).

## Limiting requests

**A [`RateLimiter`](../reference/security.md#ratelimiter) gives each client a bucket**: `Limit` requests a `Period` on average, `Burst` at once. Its middleware keys by the client's address, an IPv6 client by its /64, and on the app it also counts 404s, so a client scanning paths is limited too.

<!-- compiles: body -->
```go
perIP := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 600, Period: time.Minute, Burst: 100})
app.Use(perIP.Middleware())
```

Every allowed answer says what is left, and the request over the limit is a 429 that goes no further:

```http
HTTP/1.1 200 OK
RateLimit-Policy: "default";q=600;w=60
RateLimit: "default";r=99;t=1

HTTP/1.1 429 Too Many Requests
Retry-After: 1
RateLimit-Policy: "default";q=600;w=60
RateLimit: "default";r=0;t=1
Content-Type: application/problem+json

{"title":"Too Many Requests","status":429,"detail":"the rate limit is reached; retry after 1 s"}
```

**A limit per user goes in the Provider that knows the user**, because middleware runs before any Provider. Authenticate, then call [`Allow`](../reference/security.md#ratelimiterallow) and return its error; every route that asks for the user is then limited per user:

<!-- compiles: decls -->
```go
func limitedUsers(auth *Auth, perUser *bites.RateLimiter) func(context.Context, bites.Request, struct{}) (User, error) {
	bearer := bites.BearerAuth(auth.VerifyToken)
	return func(ctx context.Context, req bites.Request, deps struct{}) (User, error) {
		u, err := bearer(ctx, req, deps)
		if err != nil {
			return User{}, err
		}
		return u, perUser.Allow(ctx, strconv.FormatInt(u.ID, 10))
	}
}
```

**A limit by a key in the body goes in the handler**, which is the only code that reads the body: a sign-in limited per email calls `Allow` with the email it decoded and returns its error, which is the same 429:

<!-- compiles: decls -->
```go
type SignIn struct{ Email, Password string }

func signIn(perEmail *bites.RateLimiter) func(context.Context, struct{ Body SignIn }) (bites.NoContent, error) {
	return func(ctx context.Context, in struct{ Body SignIn }) (bites.NoContent, error) {
		if err := perEmail.Allow(ctx, strings.ToLower(in.Body.Email)); err != nil {
			return bites.NoContent{}, err
		}
		return bites.NoContent{}, nil // check the password
	}
}
```

The API document lists the 429 of a middleware limiter by itself, but it cannot see a call inside a Provider or a handler: give the routes that ask for the user [`ErrorStatuses(429)`](../reference/openapi.md#errorstatuses), and their 429 is documented as the rate limit's answer, with `Retry-After` and `RateLimit` (the group in the example below does).

The in-memory store holds at most `MaxKeys` keys (100,000 by default, about 5 MB), so a client that rotates keys cannot grow it. **Several instances of the app each count on their own**: with the in-memory store, N instances allow N times the limit, and a sign-in, an emailed code or an invitation limit is a security control that should hold for the application. Give the limiter a shared [`RateLimitStore`](../reference/security.md#ratelimitstore) to share its buckets. With Postgres it is the module [`pgratelimit`](../reference/pgratelimit.md): one table, one statement and one round trip for each limited request, the key kept only as a hash. Give each limiter its own `Name` (its `Name` is the obvious value), because the store is not told which limiter calls it and one email limited for sign-in and for invitations would otherwise be one bucket:

```go
store := pgratelimit.New(pgratelimit.Options{Pool: pool, Name: "login"})
app.Service(store.Service()) // deletes the rows of buckets that have been full for a while
perEmail := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 5, Period: time.Minute, Store: store, Name: "login"})
```

Paste `store.Schema()` into a migration, or call `store.CreateTable(ctx)`. Keep the cheap in-memory limit by address first (`app.Use` with a limiter that has no `Store`) so a flood is stopped before it reaches the database, and use the shared store for the limits that must hold across instances. The time a bucket is measured on is the app's clock, so keep the instances' clocks within a second of each other (NTP): a skew of `s` moves a limit by `s` of bucket. The table is `UNLOGGED`, so a crash or failover of Postgres resets every limit once. A store's error is a 500: if you would rather let requests through while the database is down, make your store say so.

## Keeping a session

**A [`Session`](../reference/security.md#session) is a value of your type kept for a browser in a cookie sealed with AES-256-GCM.** Make the [`Sessions`](../reference/security.md#sessions) once with your keys and give its `Load` to `Provide`; an Input field of type `Session[T]` then holds the session.

<!-- compiles: decls -->
```go
type Cart struct {
	Items []string `json:"items"`
}

type AddItem struct {
	Cart bites.Session[Cart]
	Body struct{ Item string }
}

func addItem(ctx context.Context, in AddItem) (bites.OK[Cart], error) {
	cart := in.Cart.Value
	cart.Items = append(cart.Items, in.Body.Item)
	cookie, err := in.Cart.Save(ctx, cart)
	if err != nil {
		return bites.OK[Cart]{}, err
	}
	return bites.OK[Cart]{Body: cart, Cookies: []bites.Cookie{cookie}}, nil
}
```

<!-- compiles: body -->
```go
var keys [][]byte // 32 random bytes each, from your secret store
carts := bites.NewSessions[Cart](bites.SessionOptions{Keys: keys})
app.Provide(carts.Load)
app.Post("/cart", addItem)
```

**Changing the session is returning the cookie [`Save`](../reference/security.md#sessionsave) gives you**, in the answer's `Cookies`, as with any cookie. Nothing is written behind your handler: the answer is your return value, and a handler that returns an error sets no cookie. A request with no cookie, a forged one or an expired one is a new session (`IsNew()` true, `Value` zero), never an error.

```http
HTTP/1.1 200 OK
Set-Cookie: __Host-session=5Xq...; Path=/; Max-Age=86400; HttpOnly; Secure; SameSite=Lax
```

- **The cookie is safe by default**: `HttpOnly`, `Secure`, `SameSite=Lax`, and the `__Host-` prefix, which a browser accepts only from this host over HTTPS, so a sibling subdomain cannot plant one. Browsers treat `localhost` as secure, so development over plain HTTP works.
- **The session lives `MaxAge`** (24 hours) from its last `Save`, and the expiry is sealed inside, so a cookie kept longer does not keep the session. To make it slide, see [a sliding session](#a-sliding-session).
- **Keys rotate**: put the new key first, keep the old one after it until the longest session has expired, then drop it.
- **A cookie holds 4096 bytes.** `Save` returns an error rather than a cookie the browser would drop; keep less, or give a [`SessionStore`](../reference/security.md#sessionstore).
- **At login, call [`Renew`](../reference/security.md#sessionrenew)**, and at logout [`Clear`](../reference/security.md#sessionclear). With a store, `Renew` moves the session to a new id and `Clear` deletes it at once; without one, a cookie copied before `Clear` still opens until it expires, so a session that must be revocable needs a store.

**To end every session of one user, state the owner and use a store that can find it.** Give the session type a `SessionOwner` method on the value receiver, and a [`Store`](../reference/security.md#ownersessionstore) that implements `OwnerSessionStore` (`SaveOwned` records the owner, `DeleteOwner` forgets every session of one). [`RevokeOwner`](../reference/security.md#sessionsrevokeowner) then ends them all and returns how many:

<!-- compiles: decls -->
```go
func (l Login) SessionOwner() string { return strconv.FormatInt(l.UserID, 10) }

func revoke(ctx context.Context, logins *bites.Sessions[Login], userID int64) (int, error) {
	return logins.RevokeOwner(ctx, strconv.FormatInt(userID, 10))
}
```

`NewSessions` refuses a type that states an owner with no store (a cookie sealed with no store cannot be revoked) or with a store that cannot keep owners, so the revocation never silently reaches nothing. Disable the user in your own database first and make the `User` Provider refuse a disabled user on every request: a request that was already running can save its session back after the revocation, and the Provider is what refuses it. A user's open WebSockets are closed by the same handler, with [`SocketOwners`](./websockets.md#closing-every-socket-of-one-user).

**To sign out everywhere else, for a password change, call [`RevokeOthers`](../reference/security.md#sessionrevokeothers) on the session in hand.** It ends every other session of the same user and keeps this one, so the browser needs no new cookie and the handler returns none. Do not end all and save again: between the two the browser's id is gone and its next request is a 401. Call it before `Renew` if you renew the id too, because after `Renew` the old id is not in the store and `RevokeOthers` returns an error rather than end the wrong sessions.

<!-- compiles: decls -->
```go
func signOutElsewhere(ctx context.Context, login bites.Session[Login]) (int, error) {
	return login.RevokeOthers(ctx) // n other sessions ended; this one stays
}
```

**With Postgres, the store is the module [`pgsession`](../reference/pgsession.md).** It is an `OwnerSessionStore` over the application's `pgxpool.Pool` that keeps only the SHA-256 of each id; paste `store.Schema()` into a migration (or call `CreateTable`) and register `store.Service()`, which deletes expired rows:

```go
store := pgsession.New(pgsession.Options{Pool: pool})
app.Service(store.Service())
logins := bites.NewSessions[Login](bites.SessionOptions{Keys: keys, Store: store})
```

**A password change that must end the other sessions atomically runs `RevokeOthers` in the application's transaction:** give it a ctx made by `pgsession.WithTx`, and the sessions end when the transaction commits and not before, so a rollback leaves them alive. The session id stays hidden from your code:

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
// UPDATE users SET password_hash = $1 WHERE id = $2, run on tx
_, err := sess.RevokeOthers(pgsession.WithTx(ctx, tx))
err = tx.Commit(ctx)
```

### A sliding session

A session lives `MaxAge` from its last `Save`, so one that is only read ends `MaxAge` after login however busy the user is. Set `RefreshAfter` and the framework saves a session again, as `Save` would, when a request opens one that was saved longer ago than that; `MaxAge/2` is the usual value:

```go
carts := bites.NewSessions[Cart](bites.SessionOptions{
	Keys:         keys,
	MaxAge:       24 * time.Hour,
	RefreshAfter: 12 * time.Hour, // a session in use lives 24 hours from its last use
})
```

The new cookie is added to the answer only when its status is under 400 and the handler set no cookie of the session's name, so a `Save`, `Renew` or `Clear` of yours wins and a logout stays a logout. With a store the row keeps its id and gets the new expiry. A refresh that fails is logged at Warn and the request goes on. `NewSessions` refuses a value that is negative or not under `MaxAge`; zero (the default) never refreshes. A refresh racing a revocation can put a deleted session back, like a `Save`: keep the `User` Provider refusing a disabled user on every request.

A route that needs a signed-in user asks for `User`, and the `User` Provider needs the session and answers 401 when it is new:

<!-- compiles: decls -->
```go
type Login struct {
	UserID int64 `json:"user_id"`
}

func userFromSession(_ context.Context, _ bites.Request, deps struct{ S bites.Session[Login] }) (User, error) {
	if deps.S.IsNew() {
		return User{}, bites.Unauthorized("sign in first")
	}
	return User{ID: deps.S.Value.UserID}, nil
}
```

**Give that Provider to `CustomAuth`, so the API document states the session cookie as the credential of every route that asks for `User`**, with its 401. Without it the document cannot tell that a Provider you wrote reads a credential, and the routes look public:

<!-- compiles: body -->
```go
app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "__Host-session"}, userFromSession))
```

## Refusing cross-site forgeries

**A route a browser authenticates with a cookie needs [`CSRF`](../reference/security.md#csrf)**, or another site's page can make the browser send a request with your user's cookie. List the origins your pages are served from, your own included:

<!-- compiles: body -->
```go
app.Use(bites.CSRF(bites.CSRFOptions{Origins: []string{"https://app.example.com"}}))
```

**An app with forms and WebSockets lists its pages once.** `CSRFOptions.Origins` left empty reads the origins of [`WithAllowOrigins`](../reference/websocket.md#withalloworigins), the same list the app's WebSocket routes use, so adding a page's origin admits its forms and its sockets together and the two cannot drift apart:

<!-- compiles: decls -->
```go
func oneList() *bites.App {
	app := bites.New(bites.WithAllowOrigins("https://app.example.com", "https://admin.example.com"))
	app.Use(bites.CSRF(bites.CSRFOptions{})) // reads the app's list
	return app
}
```

`CSRF` with no origins on an app with no list, or with `WithAllowAnyOrigin()`, panics where it is given to the app. A list given to `CSRF` is its own and is not compared with the app's; `AllowOrigins` on a socket route or group narrows the app's list and must be within it. [`CORS`](./first-party-middleware.md) never takes the app's list: it lets another origin read answers, which is a different trust, so its `Origins` stay explicit.

- `GET`, `HEAD`, `OPTIONS` and `TRACE` pass. Keep them free of state changes.
- A request with no cookie and no Basic credentials passes: a Bearer-token client behind the same app is untouched. The exception is a sign-in form posted from another site: a form's body (urlencoded, multipart or `text/plain`) with no `Authorization`, from an origin the browser says is not listed, is a 403 even with no cookie, since a page elsewhere could sign the browser in to the attacker's account (login CSRF). So put `CSRF` on the sign-in route too; nothing more is needed.
- A browser says where a request came from in `Sec-Fetch-Site` and `Origin`, which no page can forge: same-origin passes, a listed origin passes, any other site is a 403.
- A client that sends neither header (an old browser, a script with a cookie jar) must send the `__Host-csrf` cookie's value back in `X-CSRF-Token`. The middleware gives the cookie on the first safe request, and the cookie is readable by your page's script for that purpose:

```js
fetch("/transfer", {
  method: "POST",
  headers: { "X-CSRF-Token": document.cookie.match(/__Host-csrf=([^;]+)/)[1] },
  body: JSON.stringify({ amount: 10 }),
})
```

**A plain HTML form, with no script, passes by the browser's own headers.** The token is read only from the `X-CSRF-Token` header, never from a form field, and a form cannot set a header; it needs none, because every current browser sends `Origin` on a form's POST, and `Sec-Fetch-Site` as well. A request a test builds sends neither, so a test of a form route behind `CSRF` sends what the browser would, or it gets the 403 `the request has no Origin, and its CSRF token is missing or does not match`:

<!-- compiles: decls -->
```go
func postEventForm(srv *bitetest.Server, session string) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "POST", Target: "/events", Body: []byte("name=Gala"), Header: map[string][]string{
		"Content-Type":   {"application/x-www-form-urlencoded"},
		"Sec-Fetch-Site": {"same-origin"},
		"Cookie":         {"__Host-session=" + session},
	}})
}
```

Put `CORS` before `CSRF`, so that a refused request still carries the CORS headers and your page can read the 403.

## Putting it together

The order, outermost first: the request id, CORS, the security headers, the limit by address, CSRF on the cookie-authenticated routes; then the Providers, where the user is authenticated and limited.

<!-- compiles: body -->
```go
var accounts Accounts
auth := &Auth{accounts: accounts}
perIP := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 600, Period: time.Minute})
perUser := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 60, Period: time.Minute, Name: "user"})
app.Use(bites.RequestID(), bites.SecurityHeaders(), perIP.Middleware())
api := app.Group("/api", bites.ErrorStatuses(429))
api.Provide(limitedUsers(auth, perUser))
api.Get("/me", profile)
```

## Testing them

Every piece runs over `bitetest`'s in-memory connections. `bitetest.Request.Remote` sets the client's address, so a test of a trusted proxy or of a limit by address picks it; the app's clock is the limiter's and the session's, so a test moves [`bitetest.Clock`](../reference/bitetest.md#bitetestclock) to refill a bucket or expire a session instead of waiting ([testing](./testing.md)).

**A test gets a token from [`jwttest.Token`](../reference/jwt.md#jwttesttoken).** It signs HS256 with a secret the test also gives the app's verifier, so a test of a route behind `BearerAuth` writes no signer of its own. An app that verifies RS256 tokens from an identity provider in production is given an HS256 key in its tests; what is tested is the route, not the provider's keys.

```go
secret := []byte("a test secret of at least 32 bytes")
tok := jwttest.Token(t, secret, "k1", map[string]any{"sub": "ada", "exp": time.Now().Add(time.Hour).Unix()})
req := bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Authorization": {"Bearer " + tok}}}
_ = req
```
