package jwt_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/jwt"
)

var enc = base64.RawURLEncoding

// clk is a clock whose time a test sets.
type clk struct {
	bites.Clock
	t time.Time
}

func (c *clk) Now() time.Time { return c.t }

var base = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func clockAt(t time.Time) *clk { return &clk{Clock: bitetest.NewClock(), t: t} }

type claims struct {
	Sub    string `json:"sub"`
	IsRoot bool   `json:"http://example.com/is_root"`
}

var secret = []byte("0123456789abcdef0123456789abcdef")

func b64j(v any) string {
	b, _ := json.Marshal(v)
	return enc.EncodeToString(b)
}

func hsToken(secret []byte, header, payload any) string {
	in := b64j(header) + "." + b64j(payload)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(in))
	return in + "." + enc.EncodeToString(m.Sum(nil))
}

func good(extra map[string]any) map[string]any {
	m := map[string]any{"sub": "ann", "exp": base.Add(time.Hour).Unix()}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func hs(kid string) map[string]any {
	h := map[string]any{"alg": "HS256", "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	return h
}

func newHS(cfg jwt.Config) *jwt.Verifier[claims] {
	if cfg.Keys == nil {
		cfg.Keys = jwt.Keys(jwt.HS256("", secret))
	}
	if cfg.Clock == nil {
		cfg.Clock = clockAt(base)
	}
	return jwt.New[claims](cfg)
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if !errors.Is(err, bites.ErrInvalidCredentials) {
		t.Fatalf("error %v does not wrap bites.ErrInvalidCredentials", err)
	}
}

func TestTheRFC7515HS256ExampleVerifiesAndItsClaimsDecode(t *testing.T) {
	const tok = "eyJ0eXAiOiJKV1QiLA0KICJhbGciOiJIUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ.dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	k, err := enc.DecodeString("AyM1SysPpbyDfgZld3umj1qzKObwVMkoqQ-EstJQLr_T-1qS0gZH75aKtMN3Yj0iPS4hcgUuTwjAzZr1Z9CAow")
	if err != nil {
		t.Fatal(err)
	}
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.HS256("", k)), Issuer: "joe", Clock: clockAt(time.Unix(1300819000, 0))})
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsRoot {
		t.Fatalf("claims = %+v, want is_root true", c)
	}
	late := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.HS256("", k)), Clock: clockAt(time.Unix(1300819380+31, 0))})
	_, err = late.Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrExpired)
}

func TestTheRFC7515ES256ExampleVerifies(t *testing.T) {
	const tok = "eyJhbGciOiJFUzI1NiJ9.eyJpc3MiOiJqb2UiLA0KICJleHAiOjEzMDA4MTkzODAsDQogImh0dHA6Ly9leGFtcGxlLmNvbS9pc19yb290Ijp0cnVlfQ.DtEhU3ljbEg8L38VWAfUAqOyKAM6-Xx-F4GawxaepmXFCgfTjDxw5djxLa8ISlSApmWQxfKTUJqPP3-Kg6NU1Q"
	x, _ := enc.DecodeString("f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU")
	y, _ := enc.DecodeString("x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0")
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.ES256("", pub)), Clock: clockAt(time.Unix(1300819000, 0))})
	c, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsRoot {
		t.Fatalf("claims = %+v", c)
	}
}

func esSign(t *testing.T, priv *ecdsa.PrivateKey, header, payload any) string {
	t.Helper()
	in := b64j(header) + "." + b64j(payload)
	h := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, priv, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + enc.EncodeToString(sig)
}

func rsKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func rsSign(t *testing.T, priv *rsa.PrivateKey, header, payload any) string {
	t.Helper()
	in := b64j(header) + "." + b64j(payload)
	h := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, 5, h[:]) // crypto.SHA256 is 5
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + enc.EncodeToString(sig)
}

func TestAnES256TokenSignedWithAGeneratedKeyVerifiesAndADERSignatureDoesNot(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.ES256("e", &priv.PublicKey)), Clock: clockAt(base)})
	h := map[string]any{"alg": "ES256", "kid": "e"}
	tok := esSign(t, priv, h, good(nil))
	if c, err := v.Verify(context.Background(), tok); err != nil || c.Sub != "ann" {
		t.Fatalf("= %+v, %v", c, err)
	}
	in := b64j(h) + "." + b64j(good(nil))
	sum := sha256.Sum256([]byte(in))
	der, _ := ecdsa.SignASN1(rand.Reader, priv, sum[:])
	_, err := v.Verify(context.Background(), in+"."+enc.EncodeToString(der))
	mustErr(t, err, jwt.ErrSignature)
	_, err = v.Verify(context.Background(), tok[:len(tok)-2])
	if !errors.Is(err, jwt.ErrSignature) && !errors.Is(err, jwt.ErrMalformed) {
		t.Fatalf("truncated = %v", err)
	}
}

func TestAnRS256TokenVerifiesAndATamperedPayloadDoesNot(t *testing.T) {
	priv := rsKey(t)
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.RS256("r", &priv.PublicKey)), Clock: clockAt(base)})
	h := map[string]any{"alg": "RS256", "kid": "r"}
	tok := rsSign(t, priv, h, good(nil))
	if c, err := v.Verify(context.Background(), tok); err != nil || c.Sub != "ann" {
		t.Fatalf("= %+v, %v", c, err)
	}
	parts := strings.Split(tok, ".")
	parts[1] = b64j(good(map[string]any{"sub": "root"}))
	_, err := v.Verify(context.Background(), strings.Join(parts, "."))
	mustErr(t, err, jwt.ErrSignature)
	_, err = v.Verify(context.Background(), tok[:len(tok)-1])
	if !errors.Is(err, jwt.ErrSignature) && !errors.Is(err, jwt.ErrMalformed) {
		t.Fatalf("truncated = %v", err)
	}
}

func TestATokenWhoseAlgIsNoneIsRefusedWhateverItsSignature(t *testing.T) {
	rk := rsKey(t)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for _, key := range map[string]jwt.Key{
		"HS256": jwt.HS256("", secret),
		"RS256": jwt.RS256("", &rk.PublicKey),
		"ES256": jwt.ES256("", &ek.PublicKey),
	} {
		v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(key), Clock: clockAt(base)})
		for _, alg := range []string{"none", "None", "NONE", ""} {
			tok := b64j(map[string]any{"alg": alg}) + "." + b64j(good(nil)) + "."
			_, err := v.Verify(context.Background(), tok)
			mustErr(t, err, jwt.ErrAlgorithm)
		}
	}
}

func TestAnHMACTokenKeyedWithTheRSAPublicKeyIsRefusedAgainstAnRS256Key(t *testing.T) {
	priv := rsKey(t)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	tok := hsToken(der, hs(""), good(nil))
	v := jwt.New[claims](jwt.Config{Keys: jwt.Keys(jwt.RS256("", &priv.PublicKey)), Clock: clockAt(base)})
	_, err := v.Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrAlgorithm)
}

func TestAnRS256TokenAgainstAnHS256KeyIsRefused(t *testing.T) {
	priv := rsKey(t)
	tok := rsSign(t, priv, map[string]any{"alg": "RS256"}, good(nil))
	_, err := newHS(jwt.Config{}).Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrAlgorithm)
}

func TestAMalformedTokenIsRefused(t *testing.T) {
	v := newHS(jwt.Config{})
	valid := hsToken(secret, hs(""), good(nil))
	parts := strings.Split(valid, ".")
	cases := map[string]string{
		"empty":            "",
		"one part":         "abc",
		"two parts":        parts[0] + "." + parts[1],
		"four parts":       valid + ".AAAA",
		"padding":          parts[0] + "=." + parts[1] + "." + parts[2],
		"standard base64":  strings.Replace(valid, "-", "+", 1) + "+/",
		"header not json":  enc.EncodeToString([]byte("nope")) + "." + parts[1] + "." + parts[2],
		"header an array":  enc.EncodeToString([]byte("[]")) + "." + parts[1] + "." + parts[2],
		"header null":      enc.EncodeToString([]byte("null")) + "." + parts[1] + "." + parts[2],
		"alg a number":     b64j(map[string]any{"alg": 5}) + "." + parts[1] + "." + parts[2],
		"no alg":           b64j(map[string]any{"typ": "JWT"}) + "." + parts[1] + "." + parts[2],
		"kid a number":     b64j(map[string]any{"alg": "HS256", "kid": 7}) + "." + parts[1] + "." + parts[2],
		"crit":             b64j(map[string]any{"alg": "HS256", "crit": []string{"exp"}}) + "." + parts[1] + "." + parts[2],
		"trailing garbage": valid + "!",
	}
	for name, tok := range cases {
		_, err := v.Verify(context.Background(), tok)
		if !errors.Is(err, jwt.ErrMalformed) {
			t.Errorf("%s: error = %v, want ErrMalformed", name, err)
		}
		if !errors.Is(err, bites.ErrInvalidCredentials) {
			t.Errorf("%s: does not wrap ErrInvalidCredentials", name)
		}
	}
}

func TestAHugeHeaderIsRefusedBeforeItIsDecoded(t *testing.T) {
	h := enc.EncodeToString([]byte(`{"alg":"HS256","x":"` + strings.Repeat("a", 100<<10) + `"}`))
	tok := h + ".e30.AAAA"
	_, err := newHS(jwt.Config{}).Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrMalformed)
	if !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("error = %v, want the size refusal", err)
	}
	_, err = newHS(jwt.Config{MaxSize: 1 << 20}).Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrSignature)
}

func TestATokenFailsOnItsClaims(t *testing.T) {
	ctx := context.Background()
	exp := base.Add(time.Hour).Unix()
	tests := []struct {
		name string
		cfg  jwt.Config
		pl   map[string]any
		want error
	}{
		{"expired beyond the leeway", jwt.Config{}, good(map[string]any{"exp": base.Add(-31 * time.Second).Unix()}), jwt.ErrExpired},
		{"expired exactly at the leeway edge", jwt.Config{}, good(map[string]any{"exp": base.Add(-30 * time.Second).Unix()}), jwt.ErrExpired},
		{"expired within the leeway", jwt.Config{}, good(map[string]any{"exp": base.Add(-29 * time.Second).Unix()}), nil},
		{"leeway widened", jwt.Config{Leeway: 5 * time.Minute}, good(map[string]any{"exp": base.Add(-4 * time.Minute).Unix()}), nil},
		{"nbf in the future", jwt.Config{}, good(map[string]any{"nbf": base.Add(time.Minute).Unix()}), jwt.ErrNotYetValid},
		{"nbf within the leeway", jwt.Config{}, good(map[string]any{"nbf": base.Add(10 * time.Second).Unix()}), nil},
		{"iat in the future", jwt.Config{}, good(map[string]any{"iat": base.Add(time.Minute).Unix()}), jwt.ErrNotYetValid},
		{"iat in the past", jwt.Config{}, good(map[string]any{"iat": base.Add(-time.Hour).Unix()}), nil},
		{"no exp", jwt.Config{}, map[string]any{"sub": "ann"}, jwt.ErrNoExpiry},
		{"exp a string", jwt.Config{}, good(map[string]any{"exp": "soon"}), jwt.ErrMalformed},
		{"exp fractional", jwt.Config{}, good(map[string]any{"exp": float64(exp) + 0.5}), nil},
		{"wrong issuer", jwt.Config{Issuer: "https://id"}, good(map[string]any{"iss": "https://evil"}), jwt.ErrIssuer},
		{"no issuer", jwt.Config{Issuer: "https://id"}, good(nil), jwt.ErrIssuer},
		{"right issuer", jwt.Config{Issuer: "https://id"}, good(map[string]any{"iss": "https://id"}), nil},
		{"audience a string", jwt.Config{Audience: "api"}, good(map[string]any{"aud": "api"}), nil},
		{"audience in an array", jwt.Config{Audience: "api"}, good(map[string]any{"aud": []string{"web", "api"}}), nil},
		{"audience missing", jwt.Config{Audience: "api"}, good(nil), jwt.ErrAudience},
		{"audience other", jwt.Config{Audience: "api"}, good(map[string]any{"aud": "web"}), jwt.ErrAudience},
		{"audience a number", jwt.Config{Audience: "api"}, good(map[string]any{"aud": 3}), jwt.ErrMalformed},
		{"audience not checked when unset", jwt.Config{}, good(map[string]any{"aud": "web"}), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newHS(tc.cfg).Verify(ctx, hsToken(secret, hs(""), tc.pl))
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			mustErr(t, err, tc.want)
		})
	}
}

func TestAClaimsTypeThatDoesNotFitIsMalformed(t *testing.T) {
	type bad struct {
		Sub int `json:"sub"`
	}
	v := jwt.New[bad](jwt.Config{Keys: jwt.Keys(jwt.HS256("", secret)), Clock: clockAt(base)})
	_, err := v.Verify(context.Background(), hsToken(secret, hs(""), good(nil)))
	mustErr(t, err, jwt.ErrMalformed)
}

func TestAnUnknownMemberInTheClaimsIsIgnored(t *testing.T) {
	c, err := newHS(jwt.Config{}).Verify(context.Background(), hsToken(secret, hs(""), good(map[string]any{"extra": []int{1}})))
	if err != nil || c.Sub != "ann" {
		t.Fatalf("= %+v, %v", c, err)
	}
}

func TestKeyRotationVerifiesTokensOfBothKeysAndANoKidTokenNeedsASingleKey(t *testing.T) {
	a := []byte(strings.Repeat("a", 32))
	b := []byte(strings.Repeat("b", 32))
	v := newHS(jwt.Config{Keys: jwt.Keys(jwt.HS256("old", a), jwt.HS256("new", b))})
	ctx := context.Background()
	if _, err := v.Verify(ctx, hsToken(a, hs("old"), good(nil))); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, hsToken(b, hs("new"), good(nil))); err != nil {
		t.Fatal(err)
	}
	_, err := v.Verify(ctx, hsToken(a, hs(""), good(nil)))
	mustErr(t, err, jwt.ErrUnknownKey)
	_, err = v.Verify(ctx, hsToken(a, hs("new"), good(nil)))
	mustErr(t, err, jwt.ErrSignature)
	_, err = v.Verify(ctx, hsToken(a, hs("gone"), good(nil)))
	mustErr(t, err, jwt.ErrUnknownKey)
}

func TestASingleKeyServesATokenWithNoKidAndANamedOneMustMatch(t *testing.T) {
	v := newHS(jwt.Config{Keys: jwt.Keys(jwt.HS256("only", secret))})
	if _, err := v.Verify(context.Background(), hsToken(secret, hs(""), good(nil))); err != nil {
		t.Fatal(err)
	}
	_, err := v.Verify(context.Background(), hsToken(secret, hs("other"), good(nil)))
	mustErr(t, err, jwt.ErrUnknownKey)
}

type failing struct{ err error }

func (f failing) Key(context.Context, string) (jwt.Key, error) { return jwt.Key{}, f.err }

func TestAKeySourceThatCouldNotLookIsNotAnInvalidCredential(t *testing.T) {
	boom := errors.New("endpoint down")
	v := newHS(jwt.Config{Keys: failing{boom}})
	_, err := v.Verify(context.Background(), hsToken(secret, hs(""), good(nil)))
	if !errors.Is(err, boom) || errors.Is(err, bites.ErrInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
}

func TestNoErrorTextHoldsTheTokenOrItsSignature(t *testing.T) {
	v := newHS(jwt.Config{})
	valid := hsToken(secret, hs(""), good(nil))
	parts := strings.Split(valid, ".")
	bad := hsToken([]byte(strings.Repeat("z", 32)), hs(""), good(nil))
	badParts := strings.Split(bad, ".")
	toks := []string{bad, valid + "x", parts[0] + "." + parts[1],
		hsToken(secret, hs(""), good(map[string]any{"exp": 1}))}
	for _, tok := range toks {
		_, err := v.Verify(context.Background(), tok)
		if err == nil {
			t.Fatalf("%q accepted", tok)
		}
		for _, p := range append(append([]string{tok}, parts...), badParts...) {
			if len(p) > 3 && strings.Contains(err.Error(), p) {
				t.Fatalf("error %q holds %q", err, p)
			}
		}
	}
}

func recovered(f func()) (msg string) {
	defer func() { msg, _ = recover().(string) }()
	f()
	return
}

func TestConstructorsRefuseAMistakeAndNameTheCaller(t *testing.T) {
	rsa1024, _ := rsa.GenerateKey(rand.Reader, 1024)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	k := jwt.HS256("a", secret)
	cases := map[string]func(){
		"short secret":   func() { jwt.HS256("a", []byte("short")) },
		"rsa 1024":       func() { jwt.RS256("a", &rsa1024.PublicKey) },
		"rsa nil":        func() { jwt.RS256("a", nil) },
		"ec nil":         func() { jwt.ES256("a", nil) },
		"ec p384":        func() { jwt.ES256("a", &p384.PublicKey) },
		"no keys":        func() { jwt.Keys() },
		"zero key":       func() { jwt.Keys(jwt.Key{}) },
		"duplicate ids":  func() { jwt.Keys(k, jwt.HS256("a", secret)) },
		"empty id among": func() { jwt.Keys(k, jwt.HS256("", secret)) },
		"nil keys":       func() { jwt.New[claims](jwt.Config{}) },
		"negative leeway": func() {
			jwt.New[claims](jwt.Config{Keys: jwt.Keys(k), Leeway: -1})
		},
		"leeway over 5m": func() {
			jwt.New[claims](jwt.Config{Keys: jwt.Keys(k), Leeway: 5*time.Minute + 1})
		},
		"negative max size": func() { jwt.New[claims](jwt.Config{Keys: jwt.Keys(k), MaxSize: -1}) },
	}
	for name, f := range cases {
		msg := recovered(f)
		if !strings.HasPrefix(msg, "jwt.") || !strings.Contains(msg, "\n\tregistered at ") || !strings.Contains(msg, "jwt_test.go:") {
			t.Errorf("%s: panic = %q", name, msg)
		}
	}
	if msg := recovered(func() { jwt.New[claims](jwt.Config{}) }); !strings.HasPrefix(msg, "jwt.New: ") {
		t.Errorf("New panic = %q", msg)
	}
}

func TestAKeyReportsItsIDAndAlgorithm(t *testing.T) {
	rk := rsKey(t)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for alg, k := range map[string]jwt.Key{"HS256": jwt.HS256("a", secret), "RS256": jwt.RS256("a", &rk.PublicKey), "ES256": jwt.ES256("a", &ek.PublicKey)} {
		if k.ID() != "a" || k.Algorithm() != alg {
			t.Errorf("%s: %q %q", alg, k.ID(), k.Algorithm())
		}
	}
	if (jwt.Key{}).Algorithm() != "" {
		t.Error("zero key has an algorithm")
	}
}

type meInput struct {
	Claims claims
}

func meApp(t *testing.T, ks jwt.KeySource, c *clk) *bitetest.Server {
	t.Helper()
	app := bites.New(bites.WithClock(c), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	v := jwt.New[claims](jwt.Config{Keys: ks, Clock: app.Clock()})
	app.Provide(bites.BearerAuth(v.Verify))
	app.Get("/me", func(_ context.Context, in meInput) (claims, error) { return in.Claims, nil })
	return bitetest.Start(t, app)
}

func TestThroughTheFrameworkAGoodTokenIs200AnExpiredOneIs401AndAFailingSourceIs500(t *testing.T) {
	c := clockAt(base)
	srv := meApp(t, jwt.Keys(jwt.HS256("", secret)), c)
	get := func(tok string) bitetest.Response {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Authorization": {"Bearer " + tok}}})
	}
	r := get(hsToken(secret, hs(""), good(map[string]any{"http://example.com/is_root": true})))
	var got claims
	if r.Status != 200 || r.JSON(&got) != nil || got.Sub != "ann" || !got.IsRoot {
		t.Fatalf("good = %d %s", r.Status, r.Body)
	}
	r = get(hsToken(secret, hs(""), good(map[string]any{"exp": base.Add(-time.Hour).Unix()})))
	if r.Status != 401 || !strings.Contains(r.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("expired = %d %v", r.Status, r.Header)
	}
	srv2 := meApp(t, failing{errors.New("down")}, c)
	r = srv2.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Authorization": {"Bearer " + hsToken(secret, hs(""), good(nil))}}})
	if r.Status != 500 {
		t.Fatalf("source failure = %d", r.Status)
	}
}

func hsVerifier() *jwt.Verifier[struct {
	Sub string `json:"sub"`
}] {
	return jwt.New[struct {
		Sub string `json:"sub"`
	}](jwt.Config{Keys: jwt.Keys(jwt.HS256("", secret)), Clock: clockAt(base)})
}

func TestVerifyingAnHS256TokenAllocatesLittle(t *testing.T) {
	v := hsVerifier()
	tok := hsToken(secret, hs(""), good(nil))
	ctx := context.Background()
	n := testing.AllocsPerRun(200, func() {
		if _, err := v.Verify(ctx, tok); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("HS256 Verify: %.0f allocations", n)
	if n >= 40 {
		t.Fatalf("%.0f allocations, want under 40", n)
	}
}

func BenchmarkVerifyHS256(b *testing.B) {
	v := hsVerifier()
	tok := hsToken(secret, hs(""), good(nil))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		v.Verify(ctx, tok)
	}
}

func BenchmarkVerifyES256(b *testing.B) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	type c struct {
		Sub string `json:"sub"`
	}
	v := jwt.New[c](jwt.Config{Keys: jwt.Keys(jwt.ES256("", &priv.PublicKey)), Clock: clockAt(base)})
	tok := esSign(&testing.T{}, priv, map[string]any{"alg": "ES256"}, good(nil))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		v.Verify(ctx, tok)
	}
}

func BenchmarkVerifyRS256(b *testing.B) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	type c struct {
		Sub string `json:"sub"`
	}
	v := jwt.New[c](jwt.Config{Keys: jwt.Keys(jwt.RS256("", &priv.PublicKey)), Clock: clockAt(base)})
	tok := rsSign(&testing.T{}, priv, map[string]any{"alg": "RS256"}, good(nil))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		v.Verify(ctx, tok)
	}
}

func TestTheModuleImportsOnlyTheCoresExportedAPI(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, "bites/internal") {
			t.Errorf("the module imports %s", p)
		}
	}
}
