package jwt_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/modules/jwt"
)

func rsJWK(kid string, pub *rsa.PublicKey, extra map[string]any) map[string]any {
	m := map[string]any{"kty": "RSA", "kid": kid, "n": enc.EncodeToString(pub.N.Bytes()), "e": enc.EncodeToString([]byte{1, 0, 1})}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func ecJWK(kid string, pub *ecdsa.PublicKey, extra map[string]any) map[string]any {
	x := make([]byte, 32)
	y := make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	m := map[string]any{"kty": "EC", "crv": "P-256", "kid": kid, "x": enc.EncodeToString(x), "y": enc.EncodeToString(y)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// keyServer serves a set the test replaces, and counts requests.
type keyServer struct {
	*httptest.Server
	mu     sync.Mutex
	keys   []map[string]any
	status int
	body   string
	hits   atomic.Int32
}

func newKeyServer(t *testing.T, keys ...map[string]any) *keyServer {
	t.Helper()
	ks := &keyServer{keys: keys, status: 200}
	ks.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ks.hits.Add(1)
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if ks.status != 200 {
			http.Error(w, "no", ks.status)
			return
		}
		if ks.body != "" {
			w.Write([]byte(ks.body))
			return
		}
		keys := ks.keys
		if keys == nil {
			keys = []map[string]any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(ks.Close)
	return ks
}

func (s *keyServer) set(status int, body string, keys ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body, s.keys = status, body, keys
}

func TestJWKSFetchesLazilyAndFindsRSAAndECKeys(t *testing.T) {
	rk := rsKey(t)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := newKeyServer(t, rsJWK("r", &rk.PublicKey, nil), ecJWK("e", &ek.PublicKey, map[string]any{"use": "sig", "alg": "ES256"}))
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{})
	if srv.hits.Load() != 0 {
		t.Fatal("fetched before the first lookup")
	}
	v := jwt.New[claims](jwt.Config{Keys: src, Clock: clockAt(base)})
	ctx := context.Background()
	if _, err := v.Verify(ctx, rsSign(t, rk, map[string]any{"alg": "RS256", "kid": "r"}, good(nil))); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, esSign(t, ek, map[string]any{"alg": "ES256", "kid": "e"}, good(nil))); err != nil {
		t.Fatal(err)
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
}

func TestAKeyAddedToTheSetLaterVerifiesAfterARefetch(t *testing.T) {
	a := rsKey(t)
	b := rsKey(t)
	srv := newKeyServer(t, rsJWK("a", &a.PublicKey, nil))
	c := clockAt(base)
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: c})
	v := jwt.New[claims](jwt.Config{Keys: src, Clock: c})
	ctx := context.Background()
	if _, err := v.Verify(ctx, rsSign(t, a, map[string]any{"alg": "RS256", "kid": "a"}, good(nil))); err != nil {
		t.Fatal(err)
	}
	srv.set(200, "", rsJWK("a", &a.PublicKey, nil), rsJWK("b", &b.PublicKey, nil))
	tokB := rsSign(t, b, map[string]any{"alg": "RS256", "kid": "b"}, good(nil))
	_, err := v.Verify(ctx, tokB)
	mustErr(t, err, jwt.ErrUnknownKey)
	if srv.hits.Load() != 1 {
		t.Fatal("refetched inside MinRefresh")
	}
	c.t = c.t.Add(2 * time.Minute)
	if _, err := v.Verify(ctx, tokB); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != 2 {
		t.Fatalf("%d fetches, want 2", srv.hits.Load())
	}
}

func TestRandomKidsWithinMinRefreshCauseNoExtraFetch(t *testing.T) {
	srv := newKeyServer(t)
	c := clockAt(base)
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: c})
	for i := 0; i < 50; i++ {
		_, err := src.Key(context.Background(), "kid-"+string(rune('a'+i)))
		if !errors.Is(err, jwt.ErrUnknownKey) {
			t.Fatalf("lookup %d = %v", i, err)
		}
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
	c.t = c.t.Add(time.Minute)
	src.Key(context.Background(), "again")
	if n := srv.hits.Load(); n != 2 {
		t.Fatalf("%d fetches after MinRefresh, want 2", n)
	}
}

func TestAFailingServerWithNoCachedSetIsNotAnInvalidCredential(t *testing.T) {
	srv := newKeyServer(t)
	srv.set(500, "")
	c := clockAt(base)
	v := jwt.New[claims](jwt.Config{Keys: jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: c}), Clock: c})
	tok := hsToken(secret, hs("x"), good(nil))
	for i := 0; i < 3; i++ {
		_, err := v.Verify(context.Background(), tok)
		if err == nil || errors.Is(err, bites.ErrInvalidCredentials) {
			t.Fatalf("error = %v", err)
		}
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches inside MinRefresh, want 1", n)
	}
	c.t = c.t.Add(time.Minute)
	srv.set(200, "")
	_, err := v.Verify(context.Background(), tok)
	mustErr(t, err, jwt.ErrUnknownKey)
}

func TestAServerFailureAfterAGoodFetchStillVerifiesCachedKids(t *testing.T) {
	a := rsKey(t)
	srv := newKeyServer(t, rsJWK("a", &a.PublicKey, nil))
	c := clockAt(base)
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: c, MaxAge: time.Hour})
	v := jwt.New[claims](jwt.Config{Keys: src, Clock: c})
	tok := rsSign(t, a, map[string]any{"alg": "RS256", "kid": "a"}, good(nil))
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	srv.set(500, "")
	c.t = c.t.Add(2 * time.Hour)
	if _, err := src.Key(context.Background(), "a"); err != nil {
		t.Fatalf("stale set, refetch fails: %v", err)
	}
	if srv.hits.Load() != 2 {
		t.Fatalf("%d fetches, want 2", srv.hits.Load())
	}
	c.t = c.t.Add(3 * time.Minute)
	_, err := src.Key(context.Background(), "zz")
	if err == nil || errors.Is(err, bites.ErrInvalidCredentials) {
		t.Fatalf("unknown kid while the server fails = %v, want the fetch error", err)
	}
}

func TestEncryptionKeysSymmetricKeysAndWeakKeysAreSkipped(t *testing.T) {
	good1 := rsKey(t)
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := newKeyServer(t,
		rsJWK("enc", &good1.PublicKey, map[string]any{"use": "enc"}),
		map[string]any{"kty": "oct", "kid": "sym", "k": enc.EncodeToString(secret)},
		rsJWK("weak", &weak.PublicKey, nil),
		rsJWK("wrongalg", &good1.PublicKey, map[string]any{"alg": "RS512"}),
		ecJWK("ecwrongalg", &ek.PublicKey, map[string]any{"alg": "ES384"}),
		ecJWK("ok", &ek.PublicKey, nil),
	)
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: clockAt(base)})
	for _, kid := range []string{"enc", "sym", "weak", "wrongalg", "ecwrongalg"} {
		if _, err := src.Key(context.Background(), kid); !errors.Is(err, jwt.ErrUnknownKey) {
			t.Errorf("%s: error = %v, want ErrUnknownKey", kid, err)
		}
	}
	k, err := src.Key(context.Background(), "ok")
	if err != nil || k.Algorithm() != "ES256" {
		t.Fatalf("ok = %v, %v", k, err)
	}
}

func TestAKeyWithoutKidServesATokenWithoutKidOnlyWhenItIsTheOnlyKey(t *testing.T) {
	a := rsKey(t)
	b := rsKey(t)
	srv := newKeyServer(t, rsJWK("", &a.PublicKey, nil))
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: clockAt(base)})
	if _, err := src.Key(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	srv2 := newKeyServer(t, rsJWK("", &a.PublicKey, nil), rsJWK("x", &b.PublicKey, nil))
	src2 := jwt.JWKS(srv2.URL, jwt.JWKSOptions{Clock: clockAt(base)})
	if _, err := src2.Key(context.Background(), ""); !errors.Is(err, jwt.ErrUnknownKey) {
		t.Fatalf("error = %v", err)
	}
}

func TestADuplicateKidKeepsTheFirstKey(t *testing.T) {
	a := rsKey(t)
	b := rsKey(t)
	srv := newKeyServer(t, rsJWK("d", &a.PublicKey, nil), rsJWK("d", &b.PublicKey, nil))
	v := jwt.New[claims](jwt.Config{Keys: jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: clockAt(base)}), Clock: clockAt(base)})
	h := map[string]any{"alg": "RS256", "kid": "d"}
	if _, err := v.Verify(context.Background(), rsSign(t, a, h, good(nil))); err != nil {
		t.Fatal(err)
	}
	_, err := v.Verify(context.Background(), rsSign(t, b, h, good(nil)))
	mustErr(t, err, jwt.ErrSignature)
}

func TestADocumentOverMaxSizeOrNotAKeySetFails(t *testing.T) {
	a := rsKey(t)
	srv := newKeyServer(t, rsJWK("a", &a.PublicKey, nil))
	src := jwt.JWKS(srv.URL, jwt.JWKSOptions{MaxSize: 100, Clock: clockAt(base)})
	_, err := src.Key(context.Background(), "a")
	if err == nil || errors.Is(err, jwt.ErrUnknownKey) || errors.Is(err, bites.ErrInvalidCredentials) {
		t.Fatalf("error = %v", err)
	}
	for _, body := range []string{"not json", `{"nokeys":1}`, `[]`} {
		srv.set(200, body)
		src := jwt.JWKS(srv.URL, jwt.JWKSOptions{Clock: clockAt(base)})
		if _, err := src.Key(context.Background(), "a"); err == nil || errors.Is(err, bites.ErrInvalidCredentials) {
			t.Errorf("%q: error = %v", body, err)
		}
	}
}

func TestAHundredConcurrentLookupsOnAColdSetMakeOneFetch(t *testing.T) {
	a := rsKey(t)
	srv := newKeyServer(t, rsJWK("a", &a.PublicKey, nil))
	v := jwt.New[claims](jwt.Config{Keys: jwt.JWKS(srv.URL, jwt.JWKSOptions{}), Clock: clockAt(base)})
	tok := rsSign(t, a, map[string]any{"alg": "RS256", "kid": "a"}, good(nil))
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := v.Verify(context.Background(), tok)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
}

func TestJWKSRefusesAMistakeAtRegistration(t *testing.T) {
	for name, f := range map[string]func(){
		"http to a remote host": func() { jwt.JWKS("http://example.com/jwks", jwt.JWKSOptions{}) },
		"not a url":             func() { jwt.JWKS("", jwt.JWKSOptions{}) },
		"ftp":                   func() { jwt.JWKS("ftp://example.com/jwks", jwt.JWKSOptions{}) },
		"negative min refresh":  func() { jwt.JWKS("https://example.com/jwks", jwt.JWKSOptions{MinRefresh: -1}) },
		"negative max age":      func() { jwt.JWKS("https://example.com/jwks", jwt.JWKSOptions{MaxAge: -1}) },
		"negative max size":     func() { jwt.JWKS("https://example.com/jwks", jwt.JWKSOptions{MaxSize: -1}) },
	} {
		msg := recovered(f)
		if !strings.HasPrefix(msg, "jwt.JWKS: ") || !strings.Contains(msg, "jwks_test.go:") {
			t.Errorf("%s: panic = %q", name, msg)
		}
	}
	jwt.JWKS("https://example.com/jwks", jwt.JWKSOptions{})
	jwt.JWKS("http://localhost:8080/jwks", jwt.JWKSOptions{})
	jwt.JWKS("http://[::1]:8080/jwks", jwt.JWKSOptions{})
}

func TestAClientThatLeavesDoesNotCancelTheFetchEveryOtherLookupWaitsFor(t *testing.T) {
	rk := rsKey(t)
	srv := newKeyServer(t, rsJWK("r", &rk.PublicKey, nil))
	v := jwt.New[claims](jwt.Config{Keys: jwt.JWKS(srv.URL, jwt.JWKSOptions{}), Clock: clockAt(base)})
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Verify(gone, rsSign(t, rk, map[string]any{"alg": "RS256", "kid": "r"}, good(nil))); err != nil {
		t.Fatalf("the lookup of a request whose client left: %v", err)
	}
	if _, err := v.Verify(context.Background(), rsSign(t, rk, map[string]any{"alg": "RS256", "kid": "r"}, good(nil))); err != nil || srv.hits.Load() != 1 {
		t.Fatalf("%v after %d fetches", err, srv.hits.Load())
	}
}
