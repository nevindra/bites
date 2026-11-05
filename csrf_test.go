package bites_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

const csrfTok = "q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6j7k8l9z0x1c"

func csrfApp(t *testing.T, o bites.CSRFOptions) *bitetest.Server {
	t.Helper()
	return start(t, func(app *bites.App) {
		app.Use(bites.CSRF(o))
		app.Get("/users/{id}", getUser)
		app.Post("/transfer", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
	})
}

var appOnly = bites.CSRFOptions{Origins: []string{"https://app.example.com"}}

func transfer(h ...string) bitetest.Request {
	r := bitetest.Request{Method: "POST", Target: "/transfer", Header: map[string][]string{"Cookie": {"__Host-session=abc"}}}
	for i := 0; i+1 < len(h); i += 2 {
		if h[i+1] == "" {
			delete(r.Header, h[i])
			continue
		}
		r.Header[h[i]] = append(r.Header[h[i]], h[i+1])
	}
	return r
}

func TestAStateChangingRequestFromAListedOriginOrTheSameOriginPasses(t *testing.T) {
	srv := csrfApp(t, appOnly)
	for name, r := range map[string]bitetest.Request{
		"Sec-Fetch-Site same-origin":         transfer("Sec-Fetch-Site", "same-origin", "Origin", "https://app.example.com"),
		"Sec-Fetch-Site none":                transfer("Sec-Fetch-Site", "none"),
		"same-site from a listed origin":     transfer("Sec-Fetch-Site", "same-site", "Origin", "https://app.example.com"),
		"a listed Origin and no fetch data":  transfer("Origin", "https://app.example.com"),
		"no Origin, the token sent back":     transfer("Cookie", "__Host-session=abc; __Host-csrf="+csrfTok, "X-CSRF-Token", csrfTok),
		"no cookie at all, so no credential": transfer("Cookie", "", "Origin", "https://evil.example"),
	} {
		if got := srv.Do(r); got.Status != 200 {
			t.Errorf("%s: %d %s", name, got.Status, got.Text())
		}
	}
}

func TestAStateChangingRequestFromAnotherSiteIsA403(t *testing.T) {
	srv := csrfApp(t, appOnly)
	for name, r := range map[string]bitetest.Request{
		"cross-site":                        transfer("Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"),
		"same-site but not listed":          transfer("Sec-Fetch-Site", "same-site", "Origin", "https://blog.example.com"),
		"cross-site with no Origin":         transfer("Sec-Fetch-Site", "cross-site"),
		"an unlisted Origin":                transfer("Origin", "https://evil.example"),
		"Origin null":                       transfer("Origin", "null"),
		"a listed origin with another port": transfer("Origin", "https://app.example.com:8443"),
		"no Origin and no token":            transfer(),
		"no Origin and a wrong token":       transfer("Cookie", "__Host-csrf="+csrfTok, "X-CSRF-Token", strings.Repeat("x", len(csrfTok))),
		"no Origin and a token not echoed":  transfer("Cookie", "__Host-csrf="+csrfTok),
		"a header token with no cookie":     transfer("X-CSRF-Token", csrfTok),
	} {
		got := srv.Do(r)
		if got.Status != 403 || got.Get("content-type") != "application/problem+json" {
			t.Errorf("%s: %d %s", name, got.Status, got.Text())
		}
	}
}

func TestASafeRequestPassesAndGetsTheTokenCookieOnlyWhenItHasNone(t *testing.T) {
	srv := csrfApp(t, appOnly)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"Origin": {"https://evil.example"}}})
	sc := r.Get("set-cookie")
	if r.Status != 200 || !strings.HasPrefix(sc, "__Host-csrf=") || !strings.HasSuffix(sc, "; Path=/; SameSite=Strict; Secure") || strings.Contains(sc, "HttpOnly") {
		t.Fatalf("%d %s", r.Status, sc)
	}
	token := strings.TrimPrefix(strings.Split(sc, ";")[0], "__Host-csrf=")
	if len(token) != 43 {
		t.Fatalf("token %q", token)
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"Cookie": {"__Host-csrf=" + token}}})
	if r.Get("set-cookie") != "" {
		t.Fatalf("a token was given again: %v", r.Header)
	}
	if got := srv.Do(transfer("Cookie", "__Host-session=abc; __Host-csrf="+token, "X-CSRF-Token", token)); got.Status != 200 {
		t.Fatalf("the token given does not pass: %d", got.Status)
	}
}

func TestCSRFWithNotSecureNamesItsCookieWithoutAPrefix(t *testing.T) {
	srv := csrfApp(t, bites.CSRFOptions{Origins: []string{"http://localhost:5173"}, NotSecure: true, Header: "X-XSRF"})
	sc := srv.Do(req("GET", "/users/7")).Get("set-cookie")
	if !strings.HasPrefix(sc, "csrf=") || strings.Contains(sc, "Secure") {
		t.Fatalf("%s", sc)
	}
	if got := srv.Do(transfer("Cookie", "csrf="+csrfTok, "X-XSRF", csrfTok)); got.Status != 200 {
		t.Fatalf("%d", got.Status)
	}
	if got := srv.Do(transfer("Origin", "http://localhost:5173")); got.Status != 200 {
		t.Fatalf("%d", got.Status)
	}
}

func TestACrossSiteRequestCarryingCachedBasicCredentialsIsRefusedEvenWithNoCookie(t *testing.T) {
	srv := csrfApp(t, appOnly)
	r := srv.Do(transfer("Cookie", "", "Authorization", "Basic YWRhOnB3", "Origin", "https://evil.example"))
	if r.Status != 403 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(transfer("Cookie", "", "Authorization", "Bearer abc", "Origin", "https://evil.example")); r.Status != 200 {
		t.Fatalf("a Bearer token, which no browser adds on its own, was refused: %d", r.Status)
	}
}

func TestACrossSiteFormWithNoCookieIsRefusedAsLoginCSRFAndAJSONOrSameSiteOneIsNot(t *testing.T) {
	srv := csrfApp(t, appOnly)
	form := "application/x-www-form-urlencoded"
	for name, r := range map[string]bitetest.Request{
		"a cross-site form":                 transfer("Cookie", "", "Content-Type", form, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"),
		"a multipart form from an Origin":   transfer("Cookie", "", "Content-Type", "multipart/form-data; boundary=x", "Origin", "https://evil.example"),
		"text/plain, as a no-cors fetch":    transfer("Cookie", "", "Content-Type", "Text/Plain;charset=utf-8", "Origin", "https://evil.example"),
		"a form from an unlisted same site": transfer("Cookie", "", "Content-Type", form, "Sec-Fetch-Site", "same-site", "Origin", "https://blog.example.com"),
	} {
		got := srv.Do(r)
		if got.Status != 403 || !strings.Contains(got.Text(), "login CSRF") {
			t.Errorf("%s: %d %s", name, got.Status, got.Text())
		}
	}
	for name, r := range map[string]bitetest.Request{
		"a form from the app's own page":    transfer("Cookie", "", "Content-Type", form, "Sec-Fetch-Site", "same-origin", "Origin", "https://app.example.com"),
		"a form from a listed Origin":       transfer("Cookie", "", "Content-Type", form, "Origin", "https://app.example.com"),
		"a form with no browser signals":    transfer("Cookie", "", "Content-Type", form),
		"JSON from another site, no cookie": transfer("Cookie", "", "Content-Type", "application/json", "Origin", "https://evil.example"),
		"a form with a Bearer token":        transfer("Cookie", "", "Content-Type", form, "Authorization", "Bearer abc", "Origin", "https://evil.example"),
	} {
		if got := srv.Do(r); got.Status == 403 {
			t.Errorf("%s was refused: %s", name, got.Text())
		}
	}
}

// ---- one list of origins for CSRF and WebSockets (ADR 0057, ADR 0046) ----

func originsApp(t *testing.T, opts []bites.Option, register func(*bites.App)) *bitetest.Server {
	t.Helper()
	srv, _, _ := startWS(t, register, opts...)
	return srv
}

func wsAny(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
	return bites.Inbox[wsMsg]{}, nil
}

func TestACSRFWithNoOriginsTakesTheAppsListAndTheAppsWebSocketsUseTheSameOne(t *testing.T) {
	srv := originsApp(t, []bites.Option{bites.WithAllowOrigins("https://app.example.com", "https://admin.example.com")}, func(app *bites.App) {
		app.Use(bites.CSRF(bites.CSRFOptions{}))
		app.Post("/transfer", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
		app.WebSocket("/live", wsAny)
	})
	for _, org := range []string{"https://app.example.com", "https://admin.example.com"} {
		if got := srv.Do(transfer("Origin", org)); got.Status != 200 {
			t.Errorf("CSRF, Origin %s: %d %s", org, got.Status, got.Text())
		}
		if w := memWS(t, srv, "/live", "Origin: "+org); w.status != 101 {
			t.Errorf("WebSocket, Origin %s: %d", org, w.status)
		}
	}
	if got := srv.Do(transfer("Origin", "https://evil.example")); got.Status != 403 {
		t.Errorf("CSRF, an origin outside the list: %d", got.Status)
	}
	if w := handshake(t, srv.Dial("203.0.113.9:4000"), "/live", "Origin: https://evil.example"); w.status != 403 {
		t.Errorf("WebSocket, an origin outside the list: %d", w.status)
	}
}

func TestACSRFWithNoOriginsOnAGroupOrARouteTakesTheAppsListToo(t *testing.T) {
	srv := originsApp(t, []bites.Option{bites.WithAllowOrigins("https://app.example.com")}, func(app *bites.App) {
		noop := func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil }
		app.Group("/g", bites.Use(bites.CSRF(bites.CSRFOptions{}))).Post("/t", noop)
		app.Post("/r", noop, bites.Use(bites.CSRF(bites.CSRFOptions{})))
	})
	for _, target := range []string{"/g/t", "/r"} {
		r := transfer("Origin", "https://app.example.com")
		r.Target = target
		if got := srv.Do(r); got.Status != 200 {
			t.Errorf("%s from the listed origin: %d", target, got.Status)
		}
		r = transfer("Origin", "https://evil.example")
		r.Target = target
		if got := srv.Do(r); got.Status != 403 {
			t.Errorf("%s from another origin: %d", target, got.Status)
		}
	}
}

func TestACSRFThatListsOriginsOfItsOwnKeepsThemAndTheSocketsKeepTheAppsList(t *testing.T) {
	srv := originsApp(t, []bites.Option{bites.WithAllowOrigins("https://app.example.com", "https://chat.example.com")}, func(app *bites.App) {
		app.Use(bites.CSRF(bites.CSRFOptions{Origins: []string{"https://app.example.com"}}))
		app.Post("/transfer", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
		app.WebSocket("/live", wsAny)
		app.Group("/c", bites.AllowOrigins("https://chat.example.com")).WebSocket("/live", wsAny)
	})
	if got := srv.Do(transfer("Origin", "https://chat.example.com")); got.Status != 403 {
		t.Errorf("CSRF with its own list: %d", got.Status)
	}
	if w := memWS(t, srv, "/live", "Origin: https://chat.example.com"); w.status != 101 {
		t.Errorf("the socket keeps the whole list: %d", w.status)
	}
	if w := handshake(t, srv.Dial("203.0.113.9:4000"), "/c/live", "Origin: https://app.example.com"); w.status != 403 {
		t.Errorf("a group narrowed to the chat page: %d", w.status)
	}
}

func TestAnAppThatListsOriginsForCSRFAndForSocketsApartKeepsBothAsLongAsTheCSRFListIsWithinTheSocketOne(t *testing.T) {
	// What an app written before the app's list was shared does: the same two calls, the same answers.
	srv := originsApp(t, []bites.Option{bites.WithAllowOrigins("https://app.example.com", "https://chat.example.com")}, func(app *bites.App) {
		app.Use(bites.CSRF(appOnly))
		app.Post("/transfer", func(context.Context, Empty) (struct{}, error) { return struct{}{}, nil })
		app.WebSocket("/live", wsAny)
	})
	if got := srv.Do(transfer("Origin", "https://app.example.com")); got.Status != 200 {
		t.Errorf("CSRF: %d", got.Status)
	}
	if w := memWS(t, srv, "/live", "Origin: https://chat.example.com"); w.status != 101 {
		t.Errorf("WebSocket: %d", w.status)
	}
}

func TestCORSOriginsAreNeverTakenFromTheAppsListAndCORSWithNoneIsStillRefused(t *testing.T) {
	got := recoverMessage(func() {
		bites.New(bites.WithAllowOrigins("https://app.example.com")).Use(bites.CORS(bites.CORSOptions{}))
	})
	if !strings.Contains(got, "CORS:") || !strings.Contains(got, "Origins") {
		t.Errorf("CORS with no origins on an app with a list: %q", got)
	}
}
