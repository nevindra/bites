package bites_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// limited serves GET /users/{id} behind register's limiter, on a clock the test moves.
func limited(t *testing.T, register func(app *bites.App), opts ...bites.Option) (*bitetest.Server, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(append([]bites.Option{bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)...)
	register(app)
	app.Get("/users/{id}", getUser)
	return bitetest.Start(t, app), clk
}

func perMinute(n int) bites.RateLimitOptions {
	return bites.RateLimitOptions{Limit: n, Period: time.Minute}
}

func TestAKeyMayMakeItsBurstAndTheNextRequestIsA429WithRetryAfter(t *testing.T) {
	srv, _ := limited(t, func(app *bites.App) { app.Use(bites.NewRateLimiter(perMinute(3)).Middleware()) })
	for i, want := range []string{`"default";r=2;t=20`, `"default";r=1;t=40`, `"default";r=0;t=60`} {
		r := srv.Do(req("GET", "/users/7"))
		if r.Status != 200 || r.Get("ratelimit") != want || r.Get("ratelimit-policy") != `"default";q=3;w=60` {
			t.Fatalf("request %d: %d %v", i, r.Status, r.Header)
		}
	}
	r := srv.Do(req("GET", "/users/7"))
	wantProblem(t, r, 429, `{"title":"Too Many Requests","status":429,"detail":"the rate limit is reached; retry after 20 s"}`)
	if r.Get("retry-after") != "20" || r.Get("ratelimit") != `"default";r=0;t=20` || r.Get("ratelimit-policy") != `"default";q=3;w=60` {
		t.Fatalf("%v", r.Header)
	}
}

func TestABucketEarnsARequestBackEveryPeriodOverLimit(t *testing.T) {
	srv, clk := limited(t, func(app *bites.App) { app.Use(bites.NewRateLimiter(perMinute(2)).Middleware()) })
	srv.Do(req("GET", "/users/7"))
	srv.Do(req("GET", "/users/7"))
	if r := srv.Do(req("GET", "/users/7")); r.Status != 429 {
		t.Fatalf("%d", r.Status)
	}
	clk.Advance(29 * time.Second)
	if r := srv.Do(req("GET", "/users/7")); r.Status != 429 || r.Get("retry-after") != "1" {
		t.Fatalf("before the interval: %d %v", r.Status, r.Header)
	}
	clk.Advance(time.Second)
	if r := srv.Do(req("GET", "/users/7")); r.Status != 200 {
		t.Fatalf("after the interval: %d", r.Status)
	}
}

func TestEachClientAddressHasItsOwnBucketAndAnIPv6ClientIsKeyedByItsSlash64(t *testing.T) {
	srv, _ := limited(t, func(app *bites.App) { app.Use(bites.NewRateLimiter(perMinute(1)).Middleware()) })
	at := func(remote string) int {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Remote: remote}).Status
	}
	if at("198.51.100.1:1") != 200 || at("198.51.100.2:1") != 200 || at("198.51.100.1:2") != 429 {
		t.Fatal("IPv4 clients do not have a bucket each")
	}
	if at("[2001:db8:1:2::1]:1") != 200 || at("[2001:db8:1:2:ffff::9]:1") != 429 || at("[2001:db8:1:3::1]:1") != 200 {
		t.Fatal("IPv6 clients are not keyed by their /64")
	}
}

func TestAClientCannotRotateItsBucketWithAForwardedForHeader(t *testing.T) {
	srv, _ := limited(t, func(app *bites.App) { app.Use(bites.NewRateLimiter(perMinute(1)).Middleware()) },
		bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}))
	direct := func(xff string) int {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"X-Forwarded-For": {xff}}}).Status
	}
	if direct("1.1.1.1") != 200 || direct("2.2.2.2") != 429 {
		t.Fatal("a direct client chose its bucket with a header")
	}
	viaBalancer := func(xff string) int {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Remote: "10.0.0.5:1", Header: map[string][]string{"X-Forwarded-For": {xff}}}).Status
	}
	if viaBalancer("1.1.1.1, 198.51.100.7") != 200 || viaBalancer("2.2.2.2, 198.51.100.7") != 429 || viaBalancer("198.51.100.8") != 200 {
		t.Fatal("behind the balancer the bucket is not the rightmost untrusted address")
	}
}

func TestAKeyFunctionChoosesTheBucket(t *testing.T) {
	srv, _ := limited(t, func(app *bites.App) {
		o := perMinute(1)
		o.Key = func(req bites.Request) string { return req.Header("X-Tenant") }
		app.Use(bites.NewRateLimiter(o).Middleware())
	})
	tenant := func(name string) int {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"X-Tenant": {name}}}).Status
	}
	if tenant("a") != 200 || tenant("b") != 200 || tenant("a") != 429 {
		t.Fatal("the key function did not choose the bucket")
	}
	if srv.Do(req("GET", "/users/7")).Status != 200 || srv.Do(req("GET", "/users/7")).Status != 429 {
		t.Fatal("requests with an empty key do not share one bucket")
	}
}

type Account struct{ ID string }

func TestALimitPerUserIsAllowCalledByTheProviderThatKnowsTheUser(t *testing.T) {
	perUser := bites.NewRateLimiter(perMinute(1))
	srv, _ := limited(t, func(app *bites.App) {
		app.Provide(func(ctx context.Context, req bites.Request, _ struct{}) (Account, error) {
			id := req.Header("X-User")
			if id == "" {
				return Account{}, bites.Unauthorized("who are you")
			}
			return Account{ID: id}, perUser.Allow(ctx, id)
		})
		app.Get("/account", func(_ context.Context, in struct{ A Account }) (Account, error) { return in.A, nil })
	})
	as := func(user string) bitetest.Response {
		return srv.Do(bitetest.Request{Method: "GET", Target: "/account", Header: map[string][]string{"X-User": {user}}})
	}
	if r := as("ada"); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := as("bob"); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r := as("ada")
	if r.Status != 429 || r.Get("retry-after") != "60" || r.Get("ratelimit-policy") != `"default";q=1;w=60` {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
}

type signIn struct{ Email string }

func TestAHandlerLimitsByAKeyFromTheBodyWithAllowOnTheAppsClock(t *testing.T) {
	perEmail := bites.NewRateLimiter(perMinute(1))
	srv, clk := limited(t, func(app *bites.App) {
		app.Post("/sign-in", func(ctx context.Context, in struct{ Body signIn }) (bites.NoContent, error) {
			return bites.NoContent{}, perEmail.Allow(ctx, in.Body.Email)
		})
	})
	as := func(email string) bitetest.Response {
		return srv.Do(bitetest.Request{Method: "POST", Target: "/sign-in", Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"email":"` + email + `"}`)})
	}
	if r := as("ada@example.com"); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := as("bob@example.com"); r.Status != 204 {
		t.Fatalf("another email shares the first one's bucket: %d %s", r.Status, r.Text())
	}
	if r := as("ada@example.com"); r.Status != 429 || r.Get("retry-after") != "60" {
		t.Fatalf("the second sign-in of one email in a minute: %d %v %s", r.Status, r.Header, r.Text())
	}
	clk.Advance(time.Minute)
	if r := as("ada@example.com"); r.Status != 204 {
		t.Fatalf("a minute later on the app's clock the bucket has refilled, but: %d %s", r.Status, r.Text())
	}
}

type fakeStore struct {
	mu   sync.Mutex
	keys []string
	err  error
}

func (f *fakeStore) Take(_ context.Context, key string, _ time.Time, burst int, interval time.Duration) (bites.RateLimitDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	if f.err != nil {
		return bites.RateLimitDecision{}, f.err
	}
	return bites.RateLimitDecision{Allowed: true, Remaining: burst - 1, Reset: interval}, nil
}

func TestASharedStoreIsGivenTheKeyAndItsErrorIsA500(t *testing.T) {
	st := &fakeStore{}
	srv, _ := limited(t, func(app *bites.App) {
		o := perMinute(10)
		o.Store = st
		app.Use(bites.NewRateLimiter(o).Middleware())
	})
	srv.Do(req("GET", "/users/7"))
	srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Remote: "[2001:db8:1:2::5]:1"})
	if len(st.keys) != 2 || st.keys[0] != "203.0.113.9" || st.keys[1] != "2001:db8:1:2::/64" {
		t.Fatalf("keys %q", st.keys)
	}
	st.err = errors.New("redis: connection refused")
	wantProblem(t, srv.Do(req("GET", "/users/7")), 500, `{"title":"Internal Server Error","status":500}`)
}

func TestTheMemoryRateLimitStoreKeepsTheContractOfEveryRateLimitStore(t *testing.T) {
	bitetest.TestRateLimitStore(t, func(*testing.T) bites.RateLimitStore { return bites.MemoryRateLimitStore() })
}
