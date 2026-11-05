// Package jwttest makes JSON Web Tokens for tests of an app that verifies them with the jwt
// module. It signs HS256 only, with a secret the test gives both to Token and to the app's
// jwt.HS256 key, and fails the test rather than return an error. It is not a way to issue tokens
// in production: the jwt module verifies tokens and does not issue them.
//
//	secret := []byte("a test secret of at least 32 bytes")
//	v := jwt.New[Claims](jwt.Config{Keys: jwt.Keys(jwt.HS256("k1", secret)), Clock: app.Clock()})
//	app.Provide(bites.BearerAuth(v.Verify))
//	tok := jwttest.Token(t, secret, "k1", Claims{Sub: "ada", Exp: time.Now().Add(time.Hour).Unix()})
//	srv.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Authorization": {"Bearer " + tok}}})
package jwttest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"testing"
)

// Token returns an HS256 token signed with secret, naming kid in its header when kid is not empty,
// whose payload is claims encoded as JSON. claims is the test's own value, a struct or a map, and
// must encode as a JSON object; the jwt module refuses a token without exp, so it usually carries
// one ("exp" as Unix seconds, on the app's clock). The test fails when secret is shorter than the
// 32 bytes jwt.HS256 requires, or when claims is not a JSON object.
func Token(tb testing.TB, secret []byte, kid string, claims any) string {
	tb.Helper()
	if len(secret) < 32 {
		tb.Fatalf("jwttest: the secret is %d bytes; jwt.HS256 requires at least 32", len(secret))
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		tb.Fatalf("jwttest: encoding the claims: %v", err)
	}
	if len(payload) == 0 || payload[0] != '{' {
		tb.Fatalf("jwttest: the claims encode as %s, not a JSON object", payload)
	}
	head := map[string]string{"alg": "HS256", "typ": "JWT"}
	if kid != "" {
		head["kid"] = kid
	}
	header, err := json.Marshal(head, json.Deterministic(true))
	if err != nil {
		tb.Fatalf("jwttest: encoding the header: %v", err)
	}
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(mac.Sum(nil))
}
