# jwt

**The module that verifies JSON Web Tokens for `bites.BearerAuth`, with the standard library only, where the key fixes the algorithm and a token's header never chooses it.**

**Guide:** [security](../guide/security.md) · **Design:** [security](../design/security.md)

`github.com/nevindra/bites/modules/jwt` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/jwt && go test ./...`. The decision is [ADR 0062](../adr/0062-jwt-verification-is-a-module-whose-key-fixes-the-algorithm.md).

Every error `Verify` returns wraps `bites.ErrInvalidCredentials`, so `BearerAuth` answers 401 with `error="invalid_token"`, except a key source that could not look: that is any other error and a 500. No error text holds the token or a part of it.

### `jwt.Key`

```go
type Key struct { /* unexported */ }
```

Key is one verification key. Its algorithm is fixed when it is made and never taken from a token. The zero `Key` is not usable.

### `jwt.Key.ID`

```go
func (k Key) ID() string
```

ID is the key's id, the `kid` a token names it by.

### `jwt.Key.Algorithm`

```go
func (k Key) Algorithm() string
```

Algorithm is `"HS256"`, `"RS256"` or `"ES256"`, and `""` for the zero `Key`.

### `jwt.HS256`

```go
func HS256(id string, secret []byte) Key
```

HS256 returns a key that verifies HMAC SHA-256 signatures. The secret is copied. It panics, naming the file and line of the call, for a secret shorter than 32 bytes (RFC 7518 section 3.2).

### `jwt.RS256`

```go
func RS256(id string, pub *rsa.PublicKey) Key
```

RS256 returns a key that verifies RSASSA-PKCS1-v1_5 SHA-256 signatures. It panics for a nil key and for a modulus under 2048 bits.

### `jwt.ES256`

```go
func ES256(id string, pub *ecdsa.PublicKey) Key
```

ES256 returns a key that verifies ECDSA P-256 SHA-256 signatures, in the 64 byte `r||s` form of RFC 7518 section 3.4; a DER signature is refused. It panics for a nil key and for a curve other than P-256.

### `jwt.KeySource`

```go
type KeySource interface {
	Key(ctx context.Context, kid string) (Key, error)
}
```

KeySource finds the key a token names by its `kid`, `""` when the header has none. It returns an error wrapping `ErrUnknownKey` for a `kid` it does not have, which is an invalid credential. Any other error means it could not look, is not an invalid credential, and is a 500.

### `jwt.Keys`

```go
func Keys(keys ...Key) KeySource
```

Keys returns a fixed `KeySource`. With one key, a token with no `kid` uses it; with several, a token must name one by `kid`, which is how a secret is rotated. It panics for no key, a zero `Key`, two keys with one id, and several keys when one has an empty id.

### `jwt.Config`

```go
type Config struct {
	Keys     KeySource
	Issuer   string
	Audience string
	Leeway   time.Duration
	Clock    bites.Clock
	MaxSize  int
}
```

Config configures `New`. `Keys` is required. `Issuer`, when set, must equal `iss`. `Audience`, when set, must be `aud` or one of its members. `Leeway` is the clock skew allowed for `exp`, `nbf` and `iat`: 30 seconds when zero, refused when negative or over 5 minutes. `Clock` is the app's clock (`App.Clock`), the real clock when nil. `MaxSize` is the longest token accepted in bytes, 8192 when zero, refused when negative.

### `jwt.Verifier`

```go
type Verifier[C any] struct { /* unexported */ }
```

Verifier verifies tokens and decodes their claims into `C`. It is safe for concurrent use.

### `jwt.New`

```go
func New[C any](cfg Config) *Verifier[C]
```

New returns a `Verifier` of `C`. It panics, with a message starting `jwt.New: ` and ending with the file and line of the call, for a nil `Keys`, a `Leeway` out of range and a negative `MaxSize`.

### `jwt.Verifier.Verify`

```go
func (v *Verifier[C]) Verify(ctx context.Context, token string) (C, error)
```

Verify checks the token and decodes its claims into a `C`; its shape fits `bites.BearerAuth(v.Verify)`. It checks, in this order: the size, the three strict base64url parts and the header (`crit` is refused), the key the `kid` names, that the header's `alg` is the key's algorithm, the signature, and only then the claims: `exp` is required, then `exp`, `nbf` and `iat` against the clock and the leeway, `iss` and `aud`. Unknown claims are ignored.

### `jwt.JWKS`

```go
func JWKS(url string, o JWKSOptions) KeySource
```

JWKS returns a `KeySource` that fetches a JSON Web Key Set (RFC 7517) from `url`, on the first lookup and then when `MaxAge` has passed or when a `kid` is not in the set and the last attempt is older than `MinRefresh`. One fetch serves all concurrent lookups, and it does not take the request's cancellation, so a client that leaves does not fail it for the others; the default client gives up after 10 seconds and follows a redirect only to https, at most five. A failed fetch keeps the old set; a lookup whose own fetch failed returns that error, which is not an invalid credential. RSA keys of 2048 bits or more and EC P-256 keys are read; a key whose `use` is not `sig`, whose `alg` does not fit, a symmetric key and any other type are skipped. It panics for a `url` that is not https (http is allowed for `localhost`, `127.0.0.1` and `::1`) and for a negative option.

### `jwt.JWKSOptions`

```go
type JWKSOptions struct {
	Client     *http.Client
	MinRefresh time.Duration
	MaxAge     time.Duration
	MaxSize    int64
	Clock      bites.Clock
}
```

JWKSOptions configures `JWKS`. `Client` is a client with a 10 second timeout when nil. `MinRefresh` is the shortest time between two fetches, 1 minute when zero. `MaxAge` is how long a set is used, 1 hour when zero. `MaxSize` is the largest document read, 1 MiB when zero. `Clock` is the real clock when nil.

### `jwt.ErrMalformed`

```go
var ErrMalformed error
```

A token that is not three base64url parts, has bad JSON, is over `MaxSize`, has a `crit` header, or has padding or bytes outside the base64url alphabet.

### `jwt.ErrAlgorithm`

```go
var ErrAlgorithm error
```

A header whose `alg` is not the algorithm of the key selected for the token, `"none"` and an RS256 and HS256 swap included.

### `jwt.ErrUnknownKey`

```go
var ErrUnknownKey error
```

A `kid` the key source does not have, or no `kid` where several keys exist.

### `jwt.ErrSignature`

```go
var ErrSignature error
```

A signature that does not verify, or an ES256 signature that is not 64 bytes.

### `jwt.ErrExpired`

```go
var ErrExpired error
```

A token whose `exp` has passed, beyond the leeway.

### `jwt.ErrNotYetValid`

```go
var ErrNotYetValid error
```

A token whose `nbf` is in the future, or whose `iat` is in the future beyond the leeway.

### `jwt.ErrIssuer`

```go
var ErrIssuer error
```

An `iss` that is not `Config.Issuer`.

### `jwt.ErrAudience`

```go
var ErrAudience error
```

An `aud` that does not hold `Config.Audience`.

### `jwt.ErrNoExpiry`

```go
var ErrNoExpiry error
```

A token without `exp`, which is refused.

## Tokens for tests

`github.com/nevindra/bites/modules/jwt/jwttest` is a package of the same module that makes tokens for tests. It signs HS256 only and fails the test rather than return an error; the module verifies tokens and does not issue them in production.

### `jwttest.Token`

```go
func Token(tb testing.TB, secret []byte, kid string, claims any) string
```

Token returns an HS256 token signed with `secret`, naming `kid` in its header when it is not empty, whose payload is `claims` encoded as JSON: the test's own struct or map, which must encode as a JSON object. The jwt module refuses a token without `exp`, so the claims usually carry one, as Unix seconds on the app's clock. The test fails when `secret` is shorter than the 32 bytes `jwt.HS256` requires, or when `claims` is not a JSON object. Give the app `jwt.Keys(jwt.HS256(kid, secret))` with the same secret and kid.
