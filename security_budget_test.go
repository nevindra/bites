package bites

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

// The allocation rows of the security track (ADR 0025, ADR 0053 to ADR 0057), beside budget_test.go
// and measured the same way: the typed JSON POST of ADR 0003, kept alive on one in-memory
// connection, behind the middleware or Provider under test.

func readsClient(ctx context.Context, req Request, next Next) (Response, error) {
	if !req.ClientAddr().IsValid() || req.Scheme() == "" {
		panic("no client address")
	}
	return next.Call(ctx), nil
}

func TestClientAddrAndSchemeAllocateNothingBehindTrustedProxies(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for _, tc := range []struct {
		name, extra string
		opts        []Option
	}{
		{"no trusted proxy", "X-Forwarded-For: 198.51.100.9\r\n", nil},
		{"a trusted peer, X-Forwarded-For of three", "X-Forwarded-For: 1.2.3.4, 198.51.100.9:5123, 203.0.113.1\r\nX-Forwarded-Proto: https\r\n",
			[]Option{WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})}},
		{"a trusted peer, IPv6 in brackets", "X-Forwarded-For: [2001:db8::1]:443\r\n",
			[]Option{WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})}},
		{"a trusted peer, a single-address header", "CF-Connecting-IP: 198.51.100.9\r\n",
			[]Option{WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, Header: "CF-Connecting-IP"})}},
	} {
		c := memRigWith(t, tc.opts, func(a *App) { a.Use(readsClient); a.Post("/users/{id}", budgetHandler) })
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tc.extra, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != 2 {
			t.Errorf("%s: allocs/request = %v, want exactly 2 (ADR 0025: ClientAddr and Scheme allocate nothing)", tc.name, got)
		}
	}
}

var publicURLSink string

func readsPublicHostAndPrefix(ctx context.Context, req Request, next Next) (Response, error) {
	if req.PublicHost() == "" || req.PublicPrefix() == "" {
		panic("no public host or prefix")
	}
	return next.Call(ctx), nil
}

func readsPublicURL(ctx context.Context, req Request, next Next) (Response, error) {
	publicURLSink = req.PublicURL("/users/42")
	return next.Call(ctx), nil
}

func TestPublicHostAndPrefixAllocateNothingAndPublicURLAllocatesItsString(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	rewriters := []Option{WithTrustedProxies(TrustedProxies{
		Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, ForwardedHost: true, ForwardedPrefix: true,
	})}
	forwarded := "X-Forwarded-Host: [2001:db8::1]:8443\r\nX-Forwarded-Prefix: /internal, /api/\r\nX-Forwarded-Proto: https\r\n"
	for _, tc := range []struct {
		name, extra string
		mw          Middleware
		want        float64
	}{
		{"PublicHost and PublicPrefix from a trusted peer", forwarded, readsPublicHostAndPrefix, 2},
		{"PublicURL from a trusted peer", forwarded, readsPublicURL, 3},
	} {
		c := memRigWith(t, rewriters, func(a *App) { a.Use(tc.mw); a.Post("/users/{id}", budgetHandler) })
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tc.extra, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want exactly %v (ADR 0025: PublicHost and PublicPrefix allocate nothing, PublicURL its string)", tc.name, got, tc.want)
		}
	}
	if publicURLSink != "https://[2001:db8::1]:8443/api/users/42" {
		t.Errorf("PublicURL = %q", publicURLSink)
	}
}

// memRigWith is memRig with extra options for New. The client is 203.0.113.9:4000.
func memRigWith(t *testing.T, opts []Option, register func(*App)) net.Conn {
	t.Helper()
	app := New(append([]Option{WithClock(clock.NewFake(time.Now())), WithLogger(quiet())}, opts...)...)
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type budgetUser struct{ ID string }

type budgetAuthedIn struct {
	User  budgetUser
	Path  struct{ ID int64 }
	Query struct {
		Expand Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

func budgetAuthedHandler(_ context.Context, in budgetAuthedIn) (budgetOut, error) {
	return budgetOut{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
}

// budgetOptIn is budgetAuthedIn with the user optional (ADR 0069).
type budgetOptIn struct {
	User  Opt[budgetUser]
	Path  struct{ ID int64 }
	Query struct {
		Expand Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

func budgetOptHandler(_ context.Context, in budgetOptIn) (budgetOut, error) {
	return budgetOut{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
}

func TestTheSecurityMiddlewareAndProvidersCostWhatTheirADRsSay(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZGEifQ.c2lnbmF0dXJlLXNpZ25hdHVyZQ"
	bearer := BearerAuth(func(_ context.Context, tok string) (budgetUser, error) {
		if tok != token {
			return budgetUser{}, ErrInvalidCredentials
		}
		return budgetUser{ID: "ada"}, nil
	})
	apiKey := APIKeyAuth(func(_ context.Context, key string) (budgetUser, error) {
		if key != "k_live_0123456789abcdef" {
			return budgetUser{}, ErrInvalidCredentials
		}
		return budgetUser{ID: "svc"}, nil
	})
	csrfMW := CSRF(CSRFOptions{Origins: []string{"https://app.example.com"}})
	const csrfTok = "q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6j7k8l9z0x1c"
	// A limit no request of the loop reaches, so every answer has the same length.
	limiter := NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second})
	for _, tc := range []struct {
		name, extra string
		reg         func(a *App)
		want        float64
	}{
		{"SecurityHeaders, plain HTTP", "", func(a *App) { a.Use(SecurityHeaders()); a.Post("/users/{id}", budgetHandler) }, 2},
		{"SecurityHeaders, every option set (Permissions-Policy, opener, embedder and resource policies)", "", func(a *App) {
			a.Use(SecurityHeaders(SecurityHeadersOptions{CrossOriginOpenerPolicy: "same-origin-allow-popups", CrossOriginEmbedderPolicy: "credentialless", CrossOriginResourcePolicy: "cross-origin"}))
			a.Post("/users/{id}", budgetHandler)
		}, 2},
		{"RateLimiter, in memory, keyed by the client address", "", func(a *App) { a.Use(limiter.Middleware()); a.Post("/users/{id}", budgetHandler) }, 2},
		{"BearerAuth, a token that verifies", "Authorization: Bearer " + token + "\r\n", func(a *App) { a.Provide(bearer); a.Post("/users/{id}", budgetAuthedHandler) }, 2},
		{"CSRF, a POST from the same origin", "Cookie: s=1\r\nSec-Fetch-Site: same-origin\r\n", func(a *App) { a.Use(csrfMW); a.Post("/users/{id}", budgetHandler) }, 2},
		{"CSRF, a POST with the token and no Origin", "Cookie: s=1; __Host-csrf=" + csrfTok + "\r\nX-CSRF-Token: " + csrfTok + "\r\n", func(a *App) { a.Use(csrfMW); a.Post("/users/{id}", budgetHandler) }, 2},
		{"APIKeyAuth, a key that verifies", "X-API-Key: k_live_0123456789abcdef\r\n", func(a *App) { a.Provide(apiKey); a.Post("/users/{id}", budgetAuthedHandler) }, 2},
		{"AnyAuth of Bearer and an API key, the key, its second", "X-API-Key: k_live_0123456789abcdef\r\n", func(a *App) { a.Provide(AnyAuth(bearer, apiKey)); a.Post("/users/{id}", budgetAuthedHandler) }, 2},
		{"CustomAuth around the Bearer Provider", "Authorization: Bearer " + token + "\r\n", func(a *App) {
			a.Provide(CustomAuth(SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "JWT"}, bearer))
			a.Post("/users/{id}", budgetAuthedHandler)
		}, 2},
		{"an Opt field of BearerAuth, a token that verifies", "Authorization: Bearer " + token + "\r\n", func(a *App) { a.Provide(bearer); a.Post("/users/{id}", budgetOptHandler) }, 2},
		{"an Opt field of BearerAuth, no credential", "", func(a *App) { a.Provide(bearer); a.Post("/users/{id}", budgetOptHandler) }, 2},
	} {
		c := memRigWith(t, nil, tc.reg)
		got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", tc.extra, typedBody), "HTTP/1.1 200 OK")
		t.Logf("%s: %v allocations per request", tc.name, got)
		if got != tc.want {
			t.Errorf("%s: allocs/request = %v, want exactly %v", tc.name, got, tc.want)
		}
	}
}

type budgetCart struct {
	Items []string `json:"items"`
}

type budgetCartIn struct {
	S    Session[budgetCart]
	Path struct{ ID int64 }
}

// TestASessionCostsTheAllocationsADR0056Names measures a route that reads a session from its
// cookie, and one that also saves it. The numbers are named in ADR 0056 and held here exactly.
func TestASessionCostsTheAllocationsADR0056Names(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	k := make([]byte, SessionKeyLen)
	for i := range k {
		k[i] = byte(i + 1)
	}
	carts := NewSessions[budgetCart](SessionOptions{Keys: [][]byte{k}})
	// A cookie made by the same Sessions: Save on a Session it filled from nothing.
	seed := Session[budgetCart]{s: carts, clk: clock.NewFake(time.Now())}
	c, err := seed.Save(context.Background(), budgetCart{Items: []string{"tea", "milk"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := "Cookie: " + c.Name + "=" + c.Value + "\r\n"
	get := func(target string) []byte {
		return []byte("GET " + target + " HTTP/1.1\r\nHost: x\r\n" + cookie + "\r\n")
	}
	read := memRigWith(t, nil, func(a *App) {
		a.Provide(carts.Load)
		a.Get("/c/{id}", func(_ context.Context, in budgetCartIn) (budgetCart, error) { return in.S.Value, nil })
	})
	gotRead := keepAlive(t, read, get("/c/1"), "HTTP/1.1 200 OK")
	saved := memRigWith(t, nil, func(a *App) {
		a.Provide(carts.Load)
		a.Get("/c/{id}", func(ctx context.Context, in budgetCartIn) (OK[budgetCart], error) {
			c, err := in.S.Save(ctx, in.S.Value)
			return OK[budgetCart]{Body: in.S.Value, Cookies: []Cookie{c}}, err
		})
	})
	gotSave := keepAlive(t, saved, get("/c/1"), "HTTP/1.1 200 OK")
	t.Logf("a session read from its cookie: %v allocations per request; read and saved: %v", gotRead, gotSave)
	// Read: the slab and the context, the decoded cookie (opened in place), and what decoding a
	// budgetCart of two items allocates (the slice and its two strings).
	if gotRead != 6 {
		t.Errorf("a session read: allocs/request = %v, want exactly 6", gotRead)
	}
	// Saved: the read, then encoding, sealing and base64, the Cookies slice of the answer and the
	// Set-Cookie line the engine writes, 12 more.
	if gotSave != 18 {
		t.Errorf("a session read and saved: allocs/request = %v, want exactly 18", gotSave)
	}
}

func TestTheWholeSecurityStackOnATypedPOSTStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZGEifQ.c2lnbmF0dXJlLXNpZ25hdHVyZQ"
	limiter := NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second})
	opts := []Option{WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})}
	c := memRigWith(t, opts, func(a *App) {
		a.Use(RequestID(), SecurityHeaders(), limiter.Middleware(), CSRF(CSRFOptions{Origins: []string{"https://app.example.com"}}))
		a.Provide(BearerAuth(func(_ context.Context, tok string) (budgetUser, error) { return budgetUser{ID: "ada"}, nil }))
		a.Post("/users/{id}", budgetAuthedHandler)
	})
	extra := "X-Forwarded-For: 198.51.100.9\r\nX-Forwarded-Proto: https\r\nOrigin: https://app.example.com\r\nCookie: s=1\r\nAuthorization: Bearer " + token + "\r\n"
	got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", extra, typedBody), "HTTP/1.1 200 OK")
	t.Logf("RequestID, SecurityHeaders with HSTS, RateLimiter, CSRF and BearerAuth behind a trusted proxy: %v allocations per request", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2", got)
	}
}

func TestATypedRequestOverAPROXYProtocolConnectionStillAllocatesTwice(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	app := New(WithClock(clock.NewFake(time.Now())), WithLogger(quiet()),
		WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}}))
	app.Use(readsClient)
	app.Post("/users/{id}", budgetHandler)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln, ProxyProtocol())
	t.Cleanup(func() { app.Close() })
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	// The header is read once, with the connection's first bytes; every request after it reads
	// the client it named through RemoteAddrPort (ADR 0058).
	c.Write([]byte("PROXY TCP4 198.51.100.9 203.0.113.1 5123 443\r\n"))
	got := keepAlive(t, c, postWith("/users/42?expand=true&page=3", "X-Forwarded-For: 1.2.3.4\r\n", typedBody), "HTTP/1.1 200 OK")
	t.Logf("a typed POST over a PROXY protocol connection: %v allocations per request", got)
	if got != 2 {
		t.Errorf("allocs/request = %v, want exactly 2 (ADR 0058: the client the header named costs nothing per request)", got)
	}
}
