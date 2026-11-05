package jwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/nevindra/bites"
)

// JWKSOptions configures JWKS. The zero value is usable.
type JWKSOptions struct {
	// Client fetches the document. A client with a 10 second timeout when nil.
	Client *http.Client
	// MinRefresh is the shortest time between two fetches. 1 minute when zero. A kid the set does
	// not hold causes a fetch only when the last fetch is older than this.
	MinRefresh time.Duration
	// MaxAge is how long a fetched set is used before the next lookup fetches it again. 1 hour when
	// zero.
	MaxAge time.Duration
	// MaxSize is the largest document read, in bytes. 1 MiB when zero.
	MaxSize int64
	// Clock is the app's clock. The real clock when nil.
	Clock bites.Clock
}

type jwks struct {
	url        string
	client     *http.Client
	minRefresh time.Duration
	maxAge     time.Duration
	maxSize    int64
	now        func() time.Time

	fetchMu sync.Mutex // one fetch at a time; held across the request

	mu        sync.RWMutex
	haveSet   bool
	keys      []Key
	byID      map[string]Key
	fetchedAt time.Time
	attempt   time.Time
	attempted bool
	lastErr   error
}

// JWKS returns a KeySource that fetches a JSON Web Key Set (RFC 7517) from url. The first lookup
// fetches; lookups are safe for concurrent use and one fetch serves all that wait for it. A kid
// the cached set does not hold causes a fetch only when the last attempt is older than
// MinRefresh, so a client sending random kids causes at most one fetch per MinRefresh. A failed
// fetch keeps the old set. A lookup whose own fetch failed, or that follows a failed first fetch
// within MinRefresh, returns that fetch's error, which is not an invalid credential.
//
// RSA keys of 2048 bits or more and EC P-256 keys are read; a key with use other than "sig", an
// alg that does not fit its type, a symmetric key or another type is skipped. A key without kid is
// kept under "" and is used for a token without kid only when it is the set's only key. Of two
// keys with one kid the first is kept.
//
// JWKS panics, naming the file and line of the call, for a url that is not https (http is allowed
// for localhost, 127.0.0.1 and ::1, for tests) and for a negative option.
func JWKS(rawURL string, o JWKSOptions) KeySource {
	where := caller()
	u, err := url.Parse(rawURL)
	switch {
	case err != nil || u.Host == "":
		fail("JWKS", where, "the url %q is not a URL", rawURL)
	case secureURL(u):
	default:
		fail("JWKS", where, "the url %q must be https (http only for localhost)", rawURL)
	}
	if o.MinRefresh < 0 || o.MaxAge < 0 || o.MaxSize < 0 {
		fail("JWKS", where, "an option is negative")
	}
	j := &jwks{url: rawURL, client: o.Client, minRefresh: o.MinRefresh, maxAge: o.MaxAge, maxSize: o.MaxSize, now: time.Now}
	if j.client == nil {
		j.client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("jwt: the key set endpoint redirected more than 5 times")
			}
			if !secureURL(r.URL) {
				return fmt.Errorf("jwt: the key set endpoint redirected to %s, which is not https", r.URL.Redacted())
			}
			return nil
		}}
	}
	if j.minRefresh == 0 {
		j.minRefresh = time.Minute
	}
	if j.maxAge == 0 {
		j.maxAge = time.Hour
	}
	if j.maxSize == 0 {
		j.maxSize = 1 << 20
	}
	if o.Clock != nil {
		j.now = o.Clock.Now
	}
	return j
}

// secureURL reports whether u may serve a key set: https, or http on the loopback for a test.
func secureURL(u *url.URL) bool {
	h := u.Hostname()
	return u.Scheme == "https" || u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}

func pick(keys []Key, byID map[string]Key, kid string) (Key, bool) {
	if kid == "" {
		if len(keys) == 1 && keys[0].id == "" {
			return keys[0], true
		}
		return Key{}, false
	}
	k, ok := byID[kid]
	return k, ok
}

func (j *jwks) Key(ctx context.Context, kid string) (Key, error) {
	now := j.now()
	j.mu.RLock()
	k, ok := pick(j.keys, j.byID, kid)
	fresh := j.haveSet && now.Sub(j.fetchedAt) < j.maxAge
	j.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}

	j.fetchMu.Lock()
	defer j.fetchMu.Unlock()
	now = j.now()
	j.mu.RLock()
	k, ok = pick(j.keys, j.byID, kid)
	haveSet := j.haveSet
	fresh = haveSet && now.Sub(j.fetchedAt) < j.maxAge
	canFetch := !j.attempted || now.Sub(j.attempt) >= j.minRefresh
	lastErr := j.lastErr
	j.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if !canFetch {
		switch {
		case ok:
			return k, nil
		case haveSet:
			return Key{}, ErrUnknownKey
		default:
			return Key{}, lastErr
		}
	}

	keys, err := j.fetch(ctx)
	j.mu.Lock()
	j.attempted, j.attempt = true, now
	if err != nil {
		j.lastErr = err
		j.mu.Unlock()
		if ok {
			return k, nil
		}
		return Key{}, err
	}
	j.lastErr = nil
	j.haveSet, j.fetchedAt = true, now
	j.keys = keys
	j.byID = make(map[string]Key, len(keys))
	for _, nk := range keys {
		if _, dup := j.byID[nk.id]; !dup {
			j.byID[nk.id] = nk
		}
	}
	k, ok = pick(j.keys, j.byID, kid)
	j.mu.Unlock()
	if !ok {
		return Key{}, ErrUnknownKey
	}
	return k, nil
}

func (j *jwks) fetch(ctx context.Context) ([]Key, error) {
	// The fetch serves every request waiting for the set, so the one that started it leaving must
	// not cancel it: a client that disconnects at once would otherwise fail the fetch and leave
	// every lookup without keys until MinRefresh has passed. The client's timeout bounds it.
	req, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodGet, j.url, nil)
	if err != nil {
		return nil, fmt.Errorf("jwt: fetching the key set failed: %w", err)
	}
	req.Header.Set("Accept", "application/jwk-set+json, application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwt: fetching the key set failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwt: the key set endpoint answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, j.maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("jwt: reading the key set failed: %w", err)
	}
	if int64(len(body)) > j.maxSize {
		return nil, fmt.Errorf("jwt: the key set is larger than %d bytes", j.maxSize)
	}
	var doc struct {
		Keys *[]jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Keys == nil {
		return nil, fmt.Errorf("jwt: the key set is not a JSON object with a keys array")
	}
	var keys []Key
	for _, jk := range *doc.Keys {
		if k, ok := jk.key(); ok {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) key() (Key, bool) {
	if k.Use != "" && k.Use != "sig" {
		return Key{}, false
	}
	switch k.Kty {
	case "RSA":
		if k.Alg != "" && k.Alg != "RS256" {
			return Key{}, false
		}
		n, err1 := b64.DecodeString(k.N)
		e, err2 := b64.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(n) == 0 || len(e) == 0 || len(e) > 4 {
			return Key{}, false
		}
		N := new(big.Int).SetBytes(n)
		E := new(big.Int).SetBytes(e)
		if N.BitLen() < 2048 || !E.IsInt64() || E.Int64() < 3 || E.Int64()%2 == 0 || E.Int64() > 1<<31-1 {
			return Key{}, false
		}
		return Key{id: k.Kid, alg: "RS256", rsa: &rsa.PublicKey{N: N, E: int(E.Int64())}}, true
	case "EC":
		if k.Crv != "P-256" || k.Alg != "" && k.Alg != "ES256" {
			return Key{}, false
		}
		x, err1 := b64.DecodeString(k.X)
		y, err2 := b64.DecodeString(k.Y)
		if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
			return Key{}, false
		}
		pt := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pt)
		if err != nil {
			return Key{}, false
		}
		return Key{id: k.Kid, alg: "ES256", ec: pub}, true
	}
	return Key{}, false
}
