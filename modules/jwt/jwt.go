// Package jwt verifies JSON Web Tokens (RFC 7519) for bites.BearerAuth, with the standard library
// and nothing else.
//
// The key fixes the algorithm. A Key is made for one algorithm (HS256, RS256 or ES256) and that
// algorithm is never taken from the token: a token whose header names another one, "none" included,
// is refused with ErrAlgorithm before its signature is looked at. This is what makes the
// "alg: none" token and the confusion of an RSA public key with an HMAC secret impossible.
//
// Verify checks, in this order: the size of the token, its three base64url parts and its header,
// the key the header's kid names, the algorithm, the signature, and only then the claims. A token
// must carry exp. exp, nbf and iat are compared with the clock given in Config.Clock, widened by
// Config.Leeway (30 seconds when zero). iss and aud are checked when Config.Issuer and
// Config.Audience are set. The payload is decoded into the type C only after all of that holds.
//
// Every failure wraps bites.ErrInvalidCredentials, so BearerAuth answers 401 with
// error="invalid_token", except a key source that could not look (a key set endpoint that is
// down): that is any other error, and BearerAuth answers 500. No error text holds the token or a
// part of it.
//
// JWKS, a KeySource that fetches a JSON Web Key Set, uses net/http and is optional: an app with
// fixed keys never needs it.
//
//	v := jwt.New[Claims](jwt.Config{
//		Keys:     jwt.Keys(jwt.HS256("2026-11", secret)),
//		Issuer:   "https://id.example.com",
//		Audience: "api",
//		Clock:    app.Clock(),
//	})
//	app.Provide(bites.BearerAuth(v.Verify))
package jwt

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"strings"
	"time"

	"github.com/nevindra/bites"
)

// The errors Verify returns. Each wraps bites.ErrInvalidCredentials.
var (
	// ErrMalformed is a token that is not three base64url parts, has bad JSON, is over MaxSize, has a
	// crit header, padding or bytes outside the base64url alphabet.
	ErrMalformed = fmt.Errorf("jwt: the token is malformed: %w", bites.ErrInvalidCredentials)
	// ErrAlgorithm is a header whose alg is not the algorithm of the key selected for the token.
	ErrAlgorithm = fmt.Errorf("jwt: the token's algorithm is not the key's: %w", bites.ErrInvalidCredentials)
	// ErrUnknownKey is a kid the key source does not have.
	ErrUnknownKey = fmt.Errorf("jwt: no key matches the token: %w", bites.ErrInvalidCredentials)
	// ErrSignature is a signature that does not verify.
	ErrSignature = fmt.Errorf("jwt: the signature is not valid: %w", bites.ErrInvalidCredentials)
	// ErrExpired is a token whose exp has passed, beyond the leeway.
	ErrExpired = fmt.Errorf("jwt: the token has expired: %w", bites.ErrInvalidCredentials)
	// ErrNotYetValid is a token whose nbf, or whose iat beyond the leeway, is in the future.
	ErrNotYetValid = fmt.Errorf("jwt: the token is not valid yet: %w", bites.ErrInvalidCredentials)
	// ErrIssuer is an iss that is not Config.Issuer.
	ErrIssuer = fmt.Errorf("jwt: the issuer is not the expected one: %w", bites.ErrInvalidCredentials)
	// ErrAudience is an aud that does not hold Config.Audience.
	ErrAudience = fmt.Errorf("jwt: the audience does not hold the expected one: %w", bites.ErrInvalidCredentials)
	// ErrNoExpiry is a token without exp, which is refused.
	ErrNoExpiry = fmt.Errorf("jwt: the token has no exp: %w", bites.ErrInvalidCredentials)
)

// Key is one verification key. Its algorithm is fixed when it is made and never taken from a token.
// The zero Key is not usable.
type Key struct {
	id     string
	alg    string
	secret []byte
	rsa    *rsa.PublicKey
	ec     *ecdsa.PublicKey
}

func caller() string {
	_, file, line, _ := runtime.Caller(2)
	return fmt.Sprintf("%s:%d", file, line)
}

func fail(fn, where, format string, args ...any) {
	panic(fmt.Sprintf("jwt.%s: %s\n\tregistered at %s", fn, fmt.Sprintf(format, args...), where))
}

// HS256 returns a key that verifies HMAC SHA-256 signatures with secret. It panics for a secret
// shorter than 32 bytes (RFC 7518 section 3.2). The secret is copied.
func HS256(id string, secret []byte) Key {
	if len(secret) < 32 {
		fail("HS256", caller(), "the secret is %d bytes and must be at least 32 (RFC 7518 section 3.2)", len(secret))
	}
	return Key{id: id, alg: "HS256", secret: append([]byte(nil), secret...)}
}

// RS256 returns a key that verifies RSASSA-PKCS1-v1_5 SHA-256 signatures. It panics for a nil key
// and for a modulus under 2048 bits.
func RS256(id string, pub *rsa.PublicKey) Key {
	switch {
	case pub == nil || pub.N == nil:
		fail("RS256", caller(), "the public key is nil")
	case pub.N.BitLen() < 2048:
		fail("RS256", caller(), "the modulus is %d bits and must be at least 2048", pub.N.BitLen())
	}
	cp := *pub
	cp.N = new(big.Int).Set(pub.N)
	return Key{id: id, alg: "RS256", rsa: &cp}
}

// ES256 returns a key that verifies ECDSA P-256 SHA-256 signatures. It panics for a nil key and for
// a curve other than P-256.
func ES256(id string, pub *ecdsa.PublicKey) Key {
	switch {
	case pub == nil || pub.Curve == nil || pub.X == nil || pub.Y == nil:
		fail("ES256", caller(), "the public key is nil")
	case pub.Curve != elliptic.P256():
		fail("ES256", caller(), "the curve is %s and must be P-256", pub.Curve.Params().Name)
	}
	cp := ecdsa.PublicKey{Curve: pub.Curve, X: new(big.Int).Set(pub.X), Y: new(big.Int).Set(pub.Y)}
	return Key{id: id, alg: "ES256", ec: &cp}
}

// ID is the key's id, the kid a token names it by.
func (k Key) ID() string { return k.id }

// Algorithm is "HS256", "RS256" or "ES256", and "" for the zero Key.
func (k Key) Algorithm() string { return k.alg }

// KeySource finds the key a token names by its kid, "" when the header has none. It returns an
// error wrapping ErrUnknownKey for a kid it does not have, which is the token's fault and an
// invalid credential. Any other error means it could not look (a key set endpoint that is down):
// that is not an invalid credential, and BearerAuth answers 500 for it.
type KeySource interface {
	Key(ctx context.Context, kid string) (Key, error)
}

type fixed struct {
	keys []Key
	byID map[string]Key
}

// Keys returns a fixed KeySource. With one key, a token with no kid uses it; with several, a token
// must name one by kid. It panics for no key, a zero Key, two keys with one id, and several keys
// when one of them has an empty id.
func Keys(keys ...Key) KeySource {
	where := caller()
	if len(keys) == 0 {
		fail("Keys", where, "no key given")
	}
	f := &fixed{keys: append([]Key(nil), keys...), byID: make(map[string]Key, len(keys))}
	for _, k := range keys {
		switch {
		case k.alg == "":
			fail("Keys", where, "a key is the zero Key: make it with HS256, RS256 or ES256")
		case len(keys) > 1 && k.id == "":
			fail("Keys", where, "several keys are given and one has an empty id: a token could not name it")
		}
		if _, dup := f.byID[k.id]; dup {
			fail("Keys", where, "two keys have the id %q", k.id)
		}
		f.byID[k.id] = k
	}
	return f
}

func (f *fixed) Key(_ context.Context, kid string) (Key, error) {
	if kid == "" {
		if len(f.keys) == 1 {
			return f.keys[0], nil
		}
		return Key{}, ErrUnknownKey
	}
	if k, ok := f.byID[kid]; ok {
		return k, nil
	}
	return Key{}, ErrUnknownKey
}

// Config configures New.
type Config struct {
	// Keys finds the key of a token. Required.
	Keys KeySource
	// Issuer, when set, must equal the token's iss.
	Issuer string
	// Audience, when set, must be the token's aud or one of its members.
	Audience string
	// Leeway is the clock skew allowed for exp, nbf and iat. 30 seconds when zero; New refuses a
	// negative one and one over 5 minutes.
	Leeway time.Duration
	// Clock is the app's clock (App.Clock). The real clock when nil.
	Clock bites.Clock
	// MaxSize is the longest token accepted, in bytes. 8192 when zero; New refuses a negative one.
	MaxSize int
}

// Verifier verifies tokens and decodes their claims into C. It is safe for concurrent use.
type Verifier[C any] struct {
	keys     KeySource
	issuer   string
	audience string
	leeway   time.Duration
	now      func() time.Time
	maxSize  int
}

// New returns a Verifier of C. It panics, naming the file and line of the call, for a nil Keys, a
// Leeway that is negative or over 5 minutes, and a negative MaxSize.
func New[C any](cfg Config) *Verifier[C] {
	where := caller()
	switch {
	case cfg.Keys == nil:
		fail("New", where, "Config.Keys is nil: give jwt.Keys(...) or jwt.JWKS(...)")
	case cfg.Leeway < 0 || cfg.Leeway > 5*time.Minute:
		fail("New", where, "Config.Leeway is %s and must be between 0 and 5m", cfg.Leeway)
	case cfg.MaxSize < 0:
		fail("New", where, "Config.MaxSize is %d and must not be negative", cfg.MaxSize)
	}
	v := &Verifier[C]{keys: cfg.Keys, issuer: cfg.Issuer, audience: cfg.Audience, leeway: cfg.Leeway, now: time.Now, maxSize: cfg.MaxSize}
	if v.leeway == 0 {
		v.leeway = 30 * time.Second
	}
	if v.maxSize == 0 {
		v.maxSize = 8192
	}
	if cfg.Clock != nil {
		v.now = cfg.Clock.Now
	}
	return v
}

var b64 = base64.RawURLEncoding.Strict()

// registered is the part of the claims Verify reads itself.
type registered struct {
	Exp *float64        `json:"exp"`
	Nbf *float64        `json:"nbf"`
	Iat *float64        `json:"iat"`
	Iss string          `json:"iss"`
	Aud json.RawMessage `json:"aud"`
}

// Verify checks the token and decodes its claims into a C. Its shape fits bites.BearerAuth. The
// error wraps bites.ErrInvalidCredentials and one of the Err values, except when the key source
// failed, which is returned wrapped and does not wrap ErrInvalidCredentials.
func (v *Verifier[C]) Verify(ctx context.Context, token string) (C, error) {
	var zero C
	if len(token) > v.maxSize {
		return zero, fmt.Errorf("the token is longer than %d bytes: %w", v.maxSize, ErrMalformed)
	}
	i := strings.IndexByte(token, '.')
	if i < 0 {
		return zero, ErrMalformed
	}
	j := strings.IndexByte(token[i+1:], '.')
	if j < 0 {
		return zero, ErrMalformed
	}
	j += i + 1
	if strings.IndexByte(token[j+1:], '.') >= 0 {
		return zero, ErrMalformed
	}
	rawHeader, err := b64.DecodeString(token[:i])
	if err != nil {
		return zero, ErrMalformed
	}
	rawPayload, err := b64.DecodeString(token[i+1 : j])
	if err != nil {
		return zero, ErrMalformed
	}
	sig, err := b64.DecodeString(token[j+1:])
	if err != nil {
		return zero, ErrMalformed
	}
	alg, kid, err := parseHeader(rawHeader)
	if err != nil {
		return zero, err
	}
	key, err := v.keys.Key(ctx, kid)
	if err != nil {
		if errors.Is(err, ErrUnknownKey) {
			return zero, err
		}
		return zero, fmt.Errorf("jwt: the key source failed: %w", err)
	}
	if key.alg == "" || alg != key.alg {
		return zero, ErrAlgorithm
	}
	if !key.verify([]byte(token[:j]), sig) {
		return zero, ErrSignature
	}
	if err := v.checkClaims(rawPayload); err != nil {
		return zero, err
	}
	var c C
	if err := json.Unmarshal(rawPayload, &c); err != nil {
		return zero, fmt.Errorf("the claims do not fit the claims type: %w", ErrMalformed)
	}
	return c, nil
}

func parseHeader(b []byte) (alg, kid string, err error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return "", "", ErrMalformed
	}
	var h map[string]json.RawMessage
	if json.Unmarshal(b, &h) != nil {
		return "", "", ErrMalformed
	}
	if _, ok := h["crit"]; ok {
		return "", "", fmt.Errorf("the header has a crit member, which is not supported: %w", ErrMalformed)
	}
	a, ok := h["alg"]
	if !ok || !jsonString(a, &alg) {
		return "", "", fmt.Errorf("the header has no alg string: %w", ErrMalformed)
	}
	if k, ok := h["kid"]; ok && !jsonString(k, &kid) {
		return "", "", fmt.Errorf("the header's kid is not a string: %w", ErrMalformed)
	}
	return alg, kid, nil
}

func jsonString(raw json.RawMessage, out *string) bool {
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, out) == nil
}

func (k Key) verify(signed, sig []byte) bool {
	switch k.alg {
	case "HS256":
		m := hmac.New(sha256.New, k.secret)
		m.Write(signed)
		return hmac.Equal(m.Sum(nil), sig)
	case "RS256":
		h := sha256.Sum256(signed)
		return rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, h[:], sig) == nil
	case "ES256":
		if len(sig) != 64 {
			return false
		}
		h := sha256.Sum256(signed)
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(k.ec, h[:], r, s)
	}
	return false
}

func numericDate(f float64) (time.Time, bool) {
	if math.Abs(f) > 1e14 {
		return time.Time{}, false
	}
	sec := math.Floor(f)
	return time.Unix(int64(sec), int64((f-sec)*1e9)), true
}

func (v *Verifier[C]) checkClaims(payload []byte) error {
	if p := bytes.TrimSpace(payload); len(p) == 0 || p[0] != '{' {
		return fmt.Errorf("the payload is not a JSON object: %w", ErrMalformed)
	}
	var r registered
	if json.Unmarshal(payload, &r) != nil {
		return fmt.Errorf("a registered claim has the wrong type: %w", ErrMalformed)
	}
	var exp, nbf, iat time.Time
	for _, d := range []struct {
		f   *float64
		out *time.Time
	}{{r.Exp, &exp}, {r.Nbf, &nbf}, {r.Iat, &iat}} {
		if d.f == nil {
			continue
		}
		t, ok := numericDate(*d.f)
		if !ok {
			return fmt.Errorf("a date claim is out of range: %w", ErrMalformed)
		}
		*d.out = t
	}
	var aud []string
	if raw := r.Aud; len(raw) > 0 && string(raw) != "null" {
		switch raw[0] {
		case '"':
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return fmt.Errorf("aud is malformed: %w", ErrMalformed)
			}
			aud = []string{s}
		case '[':
			if json.Unmarshal(raw, &aud) != nil {
				return fmt.Errorf("aud is not an array of strings: %w", ErrMalformed)
			}
		default:
			return fmt.Errorf("aud is neither a string nor an array: %w", ErrMalformed)
		}
	}
	now := v.now()
	switch {
	case r.Exp == nil:
		return ErrNoExpiry
	case !now.Before(exp.Add(v.leeway)):
		return ErrExpired
	case r.Nbf != nil && now.Add(v.leeway).Before(nbf):
		return ErrNotYetValid
	case r.Iat != nil && iat.After(now.Add(v.leeway)):
		return ErrNotYetValid
	case v.issuer != "" && r.Iss != v.issuer:
		return ErrIssuer
	}
	if v.audience != "" {
		for _, a := range aud {
			if a == v.audience {
				return nil
			}
		}
		return ErrAudience
	}
	return nil
}
