package bites_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type Principal struct{ Name string }

type WhoAmI struct{ P Principal }

func whoami(_ context.Context, in WhoAmI) (Principal, error) { return in.P, nil }

const goodToken = "eyJhbGciOiJIUzI1NiJ9.e30.c2lnbmF0dXJl"

func verifyToken(_ context.Context, token string) (Principal, error) {
	switch token {
	case goodToken:
		return Principal{Name: "ada"}, nil
	case "no-scope":
		return Principal{}, bites.Forbidden("the token lacks the scope orders:write")
	case "db-down":
		return Principal{}, errors.New("token store unreachable")
	case "plain-401":
		return Principal{}, bites.Unauthorized("session revoked")
	}
	return Principal{}, fmt.Errorf("token %s: unknown: %w", token, bites.ErrInvalidCredentials)
}

func authApp(provide func(app *bites.App)) func(app *bites.App) {
	return func(app *bites.App) {
		provide(app)
		app.Get("/me", whoami)
	}
}

func authReq(h ...string) bitetest.Request {
	r := bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{}}
	for i := 0; i+1 < len(h); i += 2 {
		r.Header[h[i]] = append(r.Header[h[i]], h[i+1])
	}
	return r
}

func TestABearerTokenThatVerifiesFillsTheProvidedField(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.BearerAuth(verifyToken)) }))
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		r := srv.Do(authReq("Authorization", scheme+" "+goodToken))
		if r.Status != 200 || r.Text() != `{"name":"ada"}` {
			t.Errorf("%s: %d %s", scheme, r.Status, r.Text())
		}
	}
}

func TestARequestWithNoBearerTokenIsA401WithTheChallengeAndNoErrorCode(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.BearerAuth(verifyToken)) }))
	for name, r := range map[string]bitetest.Request{
		"no Authorization":       authReq(),
		"another scheme":         authReq("Authorization", "Basic YWRhOnB3"),
		"the scheme with no gap": authReq("Authorization", "Bearer"),
	} {
		got := srv.Do(r)
		if got.Status != 401 || got.Get("www-authenticate") != `Bearer realm="api"` || got.Get("content-type") != "application/problem+json" {
			t.Errorf("%s: %d %v %s", name, got.Status, got.Header, got.Text())
		}
	}
}

func TestAMalformedBearerTokenIsA400WithInvalidRequest(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.BearerAuth(verifyToken)) }))
	for _, v := range []string{"Bearer a b", "Bearer ab=cd", "Bearer =", "Bearer tok\"en"} {
		r := srv.Do(authReq("Authorization", v))
		if r.Status != 400 || r.Get("www-authenticate") != `Bearer realm="api", error="invalid_request"` {
			t.Errorf("%q: %d %v", v, r.Status, r.Header)
		}
	}
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()
	c.Write([]byte("GET /me HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + goodToken + "\r\nAuthorization: Bearer other\r\n\r\n"))
	if r := srv.Read(c, false); r.Status != 400 {
		t.Fatalf("two Authorization headers: %d %s", r.Status, r.Text())
	}
}

func TestABearerTokenVerifyRejectsIsA401WithInvalidTokenAndNeverTheVerifiersText(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.BearerAuth(verifyToken, bites.AuthOptions{Realm: "orders"}))
	}))
	r := srv.Do(authReq("Authorization", "Bearer expired.token"))
	wantProblem(t, r, 401, `{"title":"Unauthorized","status":401,"detail":"the Bearer token is not valid"}`)
	if r.Get("www-authenticate") != `Bearer realm="orders", error="invalid_token"` {
		t.Fatalf("%v", r.Header)
	}
}

func TestAStatusErrorFromVerifyIsTheAnswerAndA401OneGetsTheChallenge(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.BearerAuth(verifyToken)) }))
	r := srv.Do(authReq("Authorization", "Bearer no-scope"))
	wantProblem(t, r, 403, `{"title":"Forbidden","status":403,"detail":"the token lacks the scope orders:write"}`)
	if r.Get("www-authenticate") != "" {
		t.Fatalf("a 403 with a challenge: %v", r.Header)
	}
	r = srv.Do(authReq("Authorization", "Bearer plain-401"))
	if r.Status != 401 || r.Get("www-authenticate") != `Bearer realm="api", error="invalid_token"` {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestAVerifierThatFailsToVerifyIsA500AndNotA401(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.BearerAuth(verifyToken)) }))
	r := srv.Do(authReq("Authorization", "Bearer db-down"))
	wantProblem(t, r, 500, `{"title":"Internal Server Error","status":500}`)
}

func TestNoCredentialIsWrittenInTheLogOrInAnyAnswer(t *testing.T) {
	const secret = "s3cr3tT0ken"
	srv, lb := startLogged(t, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Provide(bites.BearerAuth(func(_ context.Context, token string) (Principal, error) {
			if token == secret+"db" {
				return Principal{}, errors.New("lookup failed")
			}
			return Principal{}, bites.ErrInvalidCredentials
		}))
		app.Get("/me", whoami)
	}, bites.WithObserver(bites.AccessLog()))
	var bodies strings.Builder
	for _, v := range []string{"Bearer " + secret, "Bearer " + secret + "db", "Bearer " + secret + " x", "Basic " + secret} {
		r := srv.Do(authReq("Authorization", v))
		fmt.Fprintf(&bodies, "%d %v %s\n", r.Status, r.Header, r.Text())
	}
	if strings.Contains(lb.String(), secret) || strings.Contains(bodies.String(), secret) {
		t.Fatalf("a credential was written:\nlog:\n%s\nanswers:\n%s", lb.String(), bodies.String())
	}
}

func TestBasicCredentialsAreDecodedAndGivenToVerify(t *testing.T) {
	var gotUser, gotPass string
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.BasicAuth(func(_ context.Context, user, pass string) (Principal, error) {
			gotUser, gotPass = user, pass
			if user == "ada" && pass == "pa:ss wörd" {
				return Principal{Name: user}, nil
			}
			return Principal{}, bites.ErrInvalidCredentials
		}))
	}))
	basic := func(s string) string { return "basic " + base64.StdEncoding.EncodeToString([]byte(s)) }
	if r := srv.Do(authReq("Authorization", basic("ada:pa:ss wörd"))); r.Status != 200 || r.Text() != `{"name":"ada"}` {
		t.Fatalf("%d %s (user %q, password %q)", r.Status, r.Text(), gotUser, gotPass)
	}
	r := srv.Do(authReq("Authorization", basic("ada:wrong")))
	if r.Status != 401 || r.Get("www-authenticate") != `Basic realm="api", charset="UTF-8"` {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := srv.Do(authReq()); r.Status != 401 || r.Get("www-authenticate") != `Basic realm="api", charset="UTF-8"` {
		t.Fatalf("no credentials: %d %v", r.Status, r.Header)
	}
	for name, v := range map[string]string{
		"not base64": "Basic !!!!",
		"no colon":   basic("ada"),
		"not UTF-8":  "Basic " + base64.StdEncoding.EncodeToString([]byte("ada:\xff\xfe")),
		"empty":      "Basic =",
	} {
		if r := srv.Do(authReq("Authorization", v)); r.Status != 400 || r.Get("www-authenticate") != "" {
			t.Errorf("%s: %d %v", name, r.Status, r.Header)
		}
	}
}

func TestAnAPIKeyIsReadFromItsOwnHeader(t *testing.T) {
	verify := func(_ context.Context, key string) (Principal, error) {
		if key == "k_live_123" {
			return Principal{Name: "svc"}, nil
		}
		return Principal{}, bites.ErrInvalidCredentials
	}
	srv := start(t, authApp(func(app *bites.App) { app.Provide(bites.APIKeyAuth(verify)) }))
	if r := srv.Do(authReq("x-api-key", "k_live_123")); r.Status != 200 || r.Text() != `{"name":"svc"}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r := srv.Do(authReq())
	if r.Status != 401 || r.Get("www-authenticate") != `APIKey realm="api", header="X-API-Key"` {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if r := srv.Do(authReq("X-API-Key", "nope")); r.Status != 401 {
		t.Fatalf("%d", r.Status)
	}
	if r := srv.Do(authReq("X-API-Key", "a\x01b")); r.Status != 400 {
		t.Fatalf("a key with a control character: %d", r.Status)
	}

	other := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.APIKeyAuth(verify, bites.AuthOptions{Header: "X-Service-Key"}))
	}))
	if r := other.Do(authReq("X-Service-Key", "k_live_123")); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := other.Do(authReq("X-API-Key", "k_live_123")); r.Status != 401 || !strings.Contains(r.Get("www-authenticate"), `header="X-Service-Key"`) {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

// Several credentials on one route, an optional credential, and a Provider the application wrote
// (ADR 0069).

func verifyKey(_ context.Context, key string) (Principal, error) {
	if key == "k_live_123" {
		return Principal{Name: "svc"}, nil
	}
	return Principal{}, bites.ErrInvalidCredentials
}

// headerValues is every value of the header called name, by any case of the name.
func headerValues(r bitetest.Response, name string) []string {
	var out []string
	for k, vs := range r.Header {
		if strings.EqualFold(k, name) {
			out = append(out, vs...)
		}
	}
	return out
}

func TestAnyAuthRunsTheFirstProviderWhoseCredentialTheRequestCarries(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.AnyAuth(bites.BearerAuth(verifyToken), bites.APIKeyAuth(verifyKey)))
	}))
	for name, c := range map[string]struct {
		req  bitetest.Request
		want string
	}{
		"a Bearer token":           {authReq("Authorization", "Bearer "+goodToken), `{"name":"ada"}`},
		"an API key":               {authReq("X-API-Key", "k_live_123"), `{"name":"svc"}`},
		"both, the first one wins": {authReq("Authorization", "Bearer "+goodToken, "X-API-Key", "k_live_123"), `{"name":"ada"}`},
		"Basic, then an API key":   {authReq("Authorization", "Basic YWRhOnB3", "X-API-Key", "k_live_123"), `{"name":"svc"}`},
	} {
		if r := srv.Do(c.req); r.Status != 200 || r.Text() != c.want {
			t.Errorf("%s: %d %s", name, r.Status, r.Text())
		}
	}
}

func TestAnyAuthWithNoCredentialIsA401WithTheChallengeOfEach(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.AnyAuth(bites.BearerAuth(verifyToken), bites.APIKeyAuth(verifyKey)))
	}))
	r := srv.Do(authReq())
	got := headerValues(r, "WWW-Authenticate")
	if r.Status != 401 || strings.Join(got, " | ") != `Bearer realm="api" | APIKey realm="api", header="X-API-Key"` {
		t.Fatalf("%d %q %s", r.Status, got, r.Text())
	}
}

func TestAnyAuthAnswersACredentialThatIsNotGoodWithItsOwnErrorAndTriesNoOther(t *testing.T) {
	keys := 0
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.AnyAuth(bites.BearerAuth(verifyToken), bites.APIKeyAuth(func(ctx context.Context, key string) (Principal, error) {
			keys++
			return verifyKey(ctx, key)
		})))
	}))
	r := srv.Do(authReq("Authorization", "Bearer expired", "X-API-Key", "k_live_123"))
	if r.Status != 401 || r.Get("www-authenticate") != `Bearer realm="api", error="invalid_token"` || keys != 0 {
		t.Fatalf("%d %v, the key's verifier ran %d times", r.Status, r.Header, keys)
	}
	if r := srv.Do(authReq("Authorization", "Bearer a b")); r.Status != 400 {
		t.Fatalf("a malformed token: %d", r.Status)
	}
	if r := srv.Do(authReq("X-API-Key", "nope")); r.Status != 401 || r.Get("www-authenticate") != `APIKey realm="api", header="X-API-Key"` {
		t.Fatalf("a key that is not good: %d %v", r.Status, r.Header)
	}
}

type MaybeWho struct{ P bites.Opt[Principal] }

func maybeWho(_ context.Context, in MaybeWho) (Principal, error) {
	return in.P.Or(Principal{Name: "anonymous"}), nil
}

func TestAnOptFieldOfAnAuthProviderIsAbsentWithNoCredentialAndFilledWithAGoodOne(t *testing.T) {
	calls := 0
	srv := start(t, func(app *bites.App) {
		app.Provide(bites.BearerAuth(func(ctx context.Context, tok string) (Principal, error) {
			calls++
			return verifyToken(ctx, tok)
		}))
		app.Get("/feed", maybeWho)
	})
	feed := func(h ...string) bitetest.Response {
		r := authReq(h...)
		r.Target = "/feed"
		return srv.Do(r)
	}
	if r := feed(); r.Status != 200 || r.Text() != `{"name":"anonymous"}` || calls != 0 {
		t.Fatalf("no credential: %d %s, the verifier ran %d times", r.Status, r.Text(), calls)
	}
	if r := feed("Authorization", "Basic YWRhOnB3"); r.Status != 200 || r.Text() != `{"name":"anonymous"}` {
		t.Fatalf("another scheme is no Bearer credential: %d %s", r.Status, r.Text())
	}
	if r := feed("Authorization", "Bearer "+goodToken); r.Status != 200 || r.Text() != `{"name":"ada"}` {
		t.Fatalf("a good token: %d %s", r.Status, r.Text())
	}
}

func TestAnOptFieldStillAnswersACredentialThatIsNotGoodWith401AndAMalformedOneWith400(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(bites.BearerAuth(verifyToken))
		app.Get("/feed", maybeWho)
	})
	r := authReq("Authorization", "Bearer expired")
	r.Target = "/feed"
	if got := srv.Do(r); got.Status != 401 || got.Get("www-authenticate") != `Bearer realm="api", error="invalid_token"` {
		t.Fatalf("an expired token is not anonymous: %d %v", got.Status, got.Header)
	}
	r = authReq("Authorization", "Bearer a b")
	r.Target = "/feed"
	if got := srv.Do(r); got.Status != 400 {
		t.Fatalf("a malformed token: %d", got.Status)
	}
}

type Viewer struct{ Name string }

func TestAProviderMayNeedTheCallerAsOptional(t *testing.T) {
	srv := start(t, func(app *bites.App) {
		app.Provide(bites.BearerAuth(verifyToken))
		app.Provide(func(_ context.Context, _ bites.Request, d struct{ P bites.Opt[Principal] }) (Viewer, error) {
			p, ok := d.P.Get()
			if !ok {
				return Viewer{Name: "guest"}, nil
			}
			return Viewer{Name: p.Name}, nil
		})
		app.Get("/v", func(_ context.Context, in struct{ V Viewer }) (Viewer, error) { return in.V, nil })
	})
	r := authReq()
	r.Target = "/v"
	if got := srv.Do(r); got.Status != 200 || got.Text() != `{"name":"guest"}` {
		t.Fatalf("%d %s", got.Status, got.Text())
	}
	r = authReq("Authorization", "Bearer "+goodToken)
	r.Target = "/v"
	if got := srv.Do(r); got.Status != 200 || got.Text() != `{"name":"ada"}` {
		t.Fatalf("%d %s", got.Status, got.Text())
	}
}

func TestACustomAuthProviderRunsAsItIsAndInAnyAuthOnlyWhenItsCookieIsSent(t *testing.T) {
	sessions := 0
	fromCookie := func(_ context.Context, req bites.Request, _ struct{}) (Principal, error) {
		sessions++
		if strings.Contains(req.Header("Cookie"), "sid=good") {
			return Principal{Name: "browser"}, nil
		}
		return Principal{}, bites.Unauthorized("no session")
	}
	cookie := bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"}
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.AnyAuth(bites.BearerAuth(verifyToken), bites.CustomAuth(cookie, fromCookie)))
	}))
	if r := srv.Do(authReq("Cookie", "theme=dark; sid=good")); r.Status != 200 || r.Text() != `{"name":"browser"}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	before := sessions
	if r := srv.Do(authReq("Cookie", "theme=dark", "Authorization", "Bearer "+goodToken)); r.Status != 200 || r.Text() != `{"name":"ada"}` || sessions != before {
		t.Fatalf("%d %s, the session Provider ran %d more times", r.Status, r.Text(), sessions-before)
	}
	alone := start(t, authApp(func(app *bites.App) { app.Provide(bites.CustomAuth(cookie, fromCookie)) }))
	if r := alone.Do(authReq()); r.Status != 401 || !strings.Contains(r.Text(), "no session") {
		t.Fatalf("CustomAuth alone runs the Provider as it is: %d %s", r.Status, r.Text())
	}
}

// accounts is a service whose methods verify a token and read a session cookie, given to the
// auth constructors as method values.
type accounts struct {
	calls int
}

func (a *accounts) Verify(ctx context.Context, token string) (Principal, error) {
	a.calls++
	return verifyToken(ctx, token)
}

func (a *accounts) FromCookie(_ context.Context, req bites.Request, _ struct{}) (Principal, error) {
	a.calls++
	if strings.Contains(req.Header("Cookie"), "sid=good") {
		return Principal{Name: "browser"}, nil
	}
	return Principal{}, bites.Unauthorized("no session")
}

func TestTheAuthConstructorsTakeMethodValuesAndTheDocumentStillNamesEachScheme(t *testing.T) {
	acc := &accounts{}
	cookie := bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "sid"}
	srv := start(t, func(app *bites.App) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Provide(bites.AnyAuth(bites.BearerAuth(acc.Verify), bites.CustomAuth(cookie, acc.FromCookie)))
		app.Get("/me", whoami)
		app.Get("/feed", maybeWho)
	})
	if acc.calls != 0 {
		t.Fatalf("a method was called %d times at Registration", acc.calls)
	}
	if r := srv.Do(authReq("Authorization", "Bearer "+goodToken)); r.Status != 200 || r.Text() != `{"name":"ada"}` {
		t.Fatalf("a Bearer token: %d %s", r.Status, r.Text())
	}
	if r := srv.Do(authReq("Cookie", "sid=good")); r.Status != 200 || r.Text() != `{"name":"browser"}` {
		t.Fatalf("a session cookie: %d %s", r.Status, r.Text())
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/feed"}); r.Status != 200 || r.Text() != `{"name":"anonymous"}` {
		t.Fatalf("an Opt field with no credential: %d %s", r.Status, r.Text())
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}); !strings.Contains(r.Text(), `"security":[{"bearerAuth":[]},{"cookieAuth.sid":[]}]`) {
		t.Fatalf("the document: %s", r.Text())
	}
}

func TestAMethodValueOfTheShapeOfAnAuthProviderIsNeverCalledAtRegistration(t *testing.T) {
	acc := &accounts{}
	srv := start(t, authApp(func(app *bites.App) { app.Provide(acc.FromCookie) }))
	if acc.calls != 0 {
		t.Fatalf("the application's method was called %d times at Registration", acc.calls)
	}
	if r := srv.Do(authReq("Cookie", "sid=good")); r.Status != 200 || r.Text() != `{"name":"browser"}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

type revokedError struct{}

func (revokedError) Error() string             { return "session store: row 7 revoked" }
func (revokedError) HTTPStatus() (int, string) { return 401, "session revoked" }

func TestAVerifyErrorTypeThatSaysItsStatusIsTheAnswerAndA401OneGetsTheChallenge(t *testing.T) {
	srv := start(t, authApp(func(app *bites.App) {
		app.Provide(bites.BearerAuth(func(context.Context, string) (Principal, error) {
			return Principal{}, fmt.Errorf("verify: %w", revokedError{})
		}))
	}))
	r := srv.Do(authReq("Authorization", "Bearer x.y.z"))
	wantProblem(t, r, 401, `{"title":"Unauthorized","status":401,"detail":"session revoked"}`)
	if r.Get("www-authenticate") != `Bearer realm="api", error="invalid_token"` {
		t.Fatalf("%v", r.Header)
	}
}

type tenantID struct{ ID string }

// TenantFirst asks for the tenant before the credential, so field order alone would check the
// tenant first.
type TenantFirst struct {
	T tenantID
	P Principal
}

func TestTheCredentialIsCheckedBeforeAnyOtherProviderWhateverTheFieldOrder(t *testing.T) {
	tenantRan := false
	srv := start(t, func(app *bites.App) {
		app.Provide(func(_ context.Context, req bites.Request, _ struct{}) (tenantID, error) {
			tenantRan = true
			if req.Header("X-Tenant") == "" {
				return tenantID{}, bites.Invalid("header.X-Tenant", "is required")
			}
			return tenantID{ID: req.Header("X-Tenant")}, nil
		})
		app.Provide(bites.BearerAuth(verifyToken))
		app.Get("/me", func(_ context.Context, in TenantFirst) (Principal, error) { return in.P, nil })
	})
	if r := srv.Do(authReq()); r.Status != 401 || tenantRan {
		t.Fatalf("no credential and no tenant: %d %s (the tenant Provider ran: %v)", r.Status, r.Text(), tenantRan)
	}
	if r := srv.Do(authReq("Authorization", "Bearer "+goodToken)); r.Status != 400 || !strings.Contains(r.Text(), "header.X-Tenant") {
		t.Fatalf("a credential and no tenant: %d %s", r.Status, r.Text())
	}
}
