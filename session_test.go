package bites_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type Cart struct {
	Items []string `json:"items"`
}

type CartIn struct {
	S    bites.Session[Cart]
	Body struct{ Add string }
}

type CartView struct {
	New   bool     `json:"new"`
	Items []string `json:"items"`
}

func addToCart(ctx context.Context, in CartIn) (bites.OK[CartView], error) {
	cart := in.S.Value
	cart.Items = append(cart.Items, in.Body.Add)
	c, err := in.S.Save(ctx, cart)
	if err != nil {
		return bites.OK[CartView]{}, err
	}
	return bites.OK[CartView]{Body: CartView{New: in.S.IsNew(), Items: cart.Items}, Cookies: []bites.Cookie{c}}, nil
}

func showCart(_ context.Context, in struct{ S bites.Session[Cart] }) (CartView, error) {
	return CartView{New: in.S.IsNew(), Items: in.S.Value.Items}, nil
}

func key(b byte) []byte { return bytes.Repeat([]byte{b}, bites.SessionKeyLen) }

// cartApp serves the cart over sessions made with o, on a clock the test moves.
func cartApp(t *testing.T, o bites.SessionOptions, extra ...func(*bites.App, *bites.Sessions[Cart])) (*bitetest.Server, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	carts := bites.NewSessions[Cart](o)
	app.Provide(carts.Load)
	app.Post("/cart", addToCart)
	app.Get("/cart", showCart)
	for _, f := range extra {
		f(app, carts)
	}
	return bitetest.Start(t, app), clk
}

func add(item, cookie string) bitetest.Request {
	r := jsonReq("POST", "/cart", `{"add":"`+item+`"}`)
	if cookie != "" {
		r.Header["Cookie"] = []string{cookie}
	}
	return r
}

func show(cookie string) bitetest.Request {
	return bitetest.Request{Method: "GET", Target: "/cart", Header: map[string][]string{"Cookie": {cookie}}}
}

// sessionCookie is the name=value pair of the Set-Cookie an answer carries.
func sessionCookie(t *testing.T, r bitetest.Response) string {
	t.Helper()
	sc := r.Get("set-cookie")
	if sc == "" {
		t.Fatalf("no Set-Cookie: %d %v %s", r.Status, r.Header, r.Text())
	}
	pair, _, _ := strings.Cut(sc, ";")
	return pair
}

func TestASessionSavedInOneAnswerIsReadByTheNextRequest(t *testing.T) {
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}})
	r := srv.Do(add("tea", ""))
	if r.Text() != `{"new":true,"items":["tea"]}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	c := sessionCookie(t, r)
	r = srv.Do(add("milk", "theme=dark; "+c+"; lang=en"))
	if r.Text() != `{"new":false,"items":["tea","milk"]}` {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if got := srv.Do(show(sessionCookie(t, r))).Text(); got != `{"new":false,"items":["tea","milk"]}` {
		t.Fatalf("%s", got)
	}
}

func TestTheSessionCookieHasTheSafeAttributesAndTheHostPrefix(t *testing.T) {
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}})
	sc := srv.Do(add("tea", "")).Get("set-cookie")
	if !strings.HasPrefix(sc, "__Host-session=") || !strings.HasSuffix(sc, "; Path=/; Max-Age=86400; HttpOnly; Secure; SameSite=Lax") {
		t.Fatalf("%s", sc)
	}
	if strings.Contains(sc, "tea") {
		t.Fatalf("the value is readable in the cookie: %s", sc)
	}
}

func TestACookieThatWasTamperedWithOrForgedIsANewSession(t *testing.T) {
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}})
	c := sessionCookie(t, srv.Do(add("tea", "")))
	name, value, _ := strings.Cut(c, "=")
	flip := []byte(value)
	flip[len(flip)/2] ^= 'A' ^ 'B'
	for what, cookie := range map[string]string{
		"a flipped byte": name + "=" + string(flip),
		"cut short":      name + "=" + value[:len(value)-4],
		"not base64":     name + "=!!!",
		"empty":          name + "=",
		"from another key": sessionCookie(t, func() bitetest.Response {
			other, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(9)}})
			return other.Do(add("tea", ""))
		}()),
	} {
		if got := srv.Do(show(cookie)).Text(); got != `{"new":true,"items":[]}` {
			t.Errorf("%s: %s", what, got)
		}
	}
}

func TestAValueSealedForAnotherCookieNameDoesNotOpenAsThisOne(t *testing.T) {
	keys := [][]byte{key(1)}
	srv, _ := cartApp(t, bites.SessionOptions{Keys: keys})
	other, _ := cartApp(t, bites.SessionOptions{Keys: keys, Cookie: "__Host-other"})
	_, value, _ := strings.Cut(sessionCookie(t, other.Do(add("tea", ""))), "=")
	if got := srv.Do(show("__Host-session=" + value)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("%s", got)
	}
}

func TestASessionPastItsMaxAgeIsNewEvenIfTheBrowserSendsIt(t *testing.T) {
	srv, clk := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: time.Hour})
	c := sessionCookie(t, srv.Do(add("tea", "")))
	clk.Advance(59 * time.Minute)
	if got := srv.Do(show(c)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("before MaxAge: %s", got)
	}
	clk.Advance(time.Minute)
	if got := srv.Do(show(c)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("at MaxAge: %s", got)
	}
}

// slideRoutes adds a handler that clears the session and one that refuses the request.
func slideRoutes(app *bites.App, _ *bites.Sessions[Cart]) {
	app.Post("/logout", func(ctx context.Context, in struct{ S bites.Session[Cart] }) (bites.NoContent, error) {
		c, err := in.S.Clear(ctx)
		return bites.NoContent{Cookies: []bites.Cookie{c}}, err
	})
	app.Get("/forbidden", func(_ context.Context, in struct{ S bites.Session[Cart] }) (bites.NoContent, error) {
		return bites.NoContent{}, bites.Forbidden("no")
	})
}

func TestASessionUsedAfterRefreshAfterIsSavedAgainSoItLivesMaxAgeFromItsLastUse(t *testing.T) {
	srv, clk := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: 2 * time.Hour, RefreshAfter: time.Hour}, slideRoutes)
	c := sessionCookie(t, srv.Do(add("tea", "")))
	clk.Advance(30 * time.Minute)
	if r := srv.Do(show(c)); r.Get("set-cookie") != "" {
		t.Fatalf("a session saved half an hour ago was refreshed: %s", r.Get("set-cookie"))
	}
	clk.Advance(time.Hour)
	r := srv.Do(show(c))
	if r.Text() != `{"new":false,"items":["tea"]}` || !strings.HasSuffix(r.Get("set-cookie"), "; Path=/; Max-Age=7200; HttpOnly; Secure; SameSite=Lax") {
		t.Fatalf("not refreshed: %s %q", r.Text(), r.Get("set-cookie"))
	}
	fresh := sessionCookie(t, r)
	clk.Advance(time.Hour) // 2h30 after the Save: the old cookie has expired, the refreshed one lives
	if got := srv.Do(show(c)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("the cookie that was not refreshed still opens: %s", got)
	}
	if got := srv.Do(show(fresh)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("the refreshed cookie does not open: %s", got)
	}
}

func TestARefreshGivesWayToTheHandlersOwnCookieAndToAnAnswerThatFailed(t *testing.T) {
	srv, clk := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: 2 * time.Hour, RefreshAfter: time.Hour}, slideRoutes)
	c := sessionCookie(t, srv.Do(add("tea", "")))
	clk.Advance(90 * time.Minute)
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/logout", Header: map[string][]string{"Cookie": {c}}})
	if got := r.Header["set-cookie"]; len(got) != 1 || !strings.HasPrefix(got[0], "__Host-session=deleted;") {
		t.Fatalf("a Clear and a refresh: %q", got)
	}
	r = srv.Do(add("milk", c))
	if got := r.Header["set-cookie"]; len(got) != 1 {
		t.Fatalf("a Save and a refresh: %q", got)
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/forbidden", Header: map[string][]string{"Cookie": {c}}})
	if r.Status != 403 || r.Get("set-cookie") != "" {
		t.Fatalf("a failed answer carried a refresh: %d %q", r.Status, r.Get("set-cookie"))
	}
}

func TestARefreshOfAStoredSessionMovesItsExpiryInTheStoreAndKeepsItsId(t *testing.T) {
	st := newMemStore()
	srv, clk := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, MaxAge: 2 * time.Hour, RefreshAfter: time.Hour, Store: st})
	c := sessionCookie(t, srv.Do(add("tea", "")))
	ids := st.ids()
	before := st.expiry(ids[0])
	clk.Advance(90 * time.Minute)
	fresh := sessionCookie(t, srv.Do(show(c)))
	if got := st.ids(); len(got) != 1 || got[0] != ids[0] || st.expiry(ids[0]).Sub(before) != 90*time.Minute {
		t.Fatalf("store %q, expiry moved by %v", got, st.expiry(ids[0]).Sub(before))
	}
	if got := srv.Do(show(fresh)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("%s", got)
	}
}

func TestAKeyIsRotatedByPuttingTheNewOneFirstAndDroppingTheOldOneLater(t *testing.T) {
	old, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}})
	c := sessionCookie(t, old.Do(add("tea", "")))
	rotated, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(2), key(1)}})
	if got := rotated.Do(show(c)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("an old key no longer opens: %s", got)
	}
	resealed := sessionCookie(t, rotated.Do(add("milk", c)))
	dropped, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(2)}})
	if got := dropped.Do(show(c)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("a dropped key still opens: %s", got)
	}
	if got := dropped.Do(show(resealed)).Text(); got != `{"new":false,"items":["tea","milk"]}` {
		t.Fatalf("a cookie resealed with the new key does not open: %s", got)
	}
}

func TestASessionTooLargeForACookieIsA500AndNotACookieTheBrowserDrops(t *testing.T) {
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}})
	r := srv.Do(add(strings.Repeat("x", 3500), ""))
	if r.Status != 500 || r.Get("set-cookie") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

type memStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	expires map[string]time.Time
	deleted []string
	owners  map[string]string // id to owner, for SaveOwned
}

func newMemStore() *memStore {
	return &memStore{data: map[string][]byte{}, expires: map[string]time.Time{}}
}

func (m *memStore) Load(_ context.Context, id string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[id]
	return d, ok, nil
}

func (m *memStore) Save(_ context.Context, id string, data []byte, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[id], m.expires[id] = data, exp
	return nil
}

func (m *memStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, id)
	delete(m.owners, id)
	m.deleted = append(m.deleted, id)
	return nil
}

// SaveOwned and DeleteOwner make memStore an OwnerSessionStore.
func (m *memStore) SaveOwned(_ context.Context, id, owner string, data []byte, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[id], m.expires[id] = data, exp
	if m.owners == nil {
		m.owners = map[string]string{}
	}
	m.owners[id] = owner
	return nil
}

func (m *memStore) DeleteOwner(_ context.Context, owner, keep string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, o := range m.owners {
		if o == owner && id != keep {
			delete(m.data, id)
			delete(m.owners, id)
			n++
		}
	}
	return n, nil
}

func (m *memStore) ids() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id := range m.data {
		out = append(out, id)
	}
	return out
}

func TestWithAStoreTheCookieHoldsOnlyASealedIdAndTheValueIsInTheStore(t *testing.T) {
	st := newMemStore()
	srv, clk := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, Store: st})
	c := sessionCookie(t, srv.Do(add(strings.Repeat("x", 5000), "")))
	ids := st.ids()
	if len(ids) != 1 || len(ids[0]) != 22 || len(c) > 200 {
		t.Fatalf("store %q, cookie of %d bytes", ids, len(c))
	}
	if !st.expires[ids[0]].Equal(clk.Now().Add(24 * time.Hour)) {
		t.Fatalf("expires %v", st.expires[ids[0]])
	}
	if got := srv.Do(show(c)).Text(); !strings.HasPrefix(got, `{"new":false,"items":["xxx`) {
		t.Fatalf("%.60s", got)
	}
	st.Delete(context.Background(), ids[0])
	if got := srv.Do(show(c)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("a session deleted from the store is still read: %s", got)
	}
}

func TestRenewGivesTheSessionANewIdAndDeletesTheOldOne(t *testing.T) {
	st := newMemStore()
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, Store: st}, func(app *bites.App, _ *bites.Sessions[Cart]) {
		app.Post("/login", func(ctx context.Context, in struct{ S bites.Session[Cart] }) (bites.NoContent, error) {
			c, err := in.S.Renew(ctx, in.S.Value)
			return bites.NoContent{Cookies: []bites.Cookie{c}}, err
		})
		app.Post("/logout", func(ctx context.Context, in struct{ S bites.Session[Cart] }) (bites.NoContent, error) {
			c, err := in.S.Clear(ctx)
			return bites.NoContent{Cookies: []bites.Cookie{c}}, err
		})
	})
	before := sessionCookie(t, srv.Do(add("tea", "")))
	oldID := st.ids()[0]
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/login", Header: map[string][]string{"Cookie": {before}}})
	after := sessionCookie(t, r)
	ids := st.ids()
	if len(ids) != 1 || ids[0] == oldID || len(st.deleted) != 1 || st.deleted[0] != oldID {
		t.Fatalf("ids %q, deleted %q, old %s", ids, st.deleted, oldID)
	}
	if got := srv.Do(show(before)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("the id from before the login still works: %s", got)
	}
	if got := srv.Do(show(after)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("%s", got)
	}
	r = srv.Do(bitetest.Request{Method: "POST", Target: "/logout", Header: map[string][]string{"Cookie": {after}}})
	if sc := r.Get("set-cookie"); !strings.HasPrefix(sc, "__Host-session=deleted; Path=/; Max-Age=0") || len(st.ids()) != 0 {
		t.Fatalf("%s %q", sc, st.ids())
	}
}

func TestSessionsWithoutTheHostPrefixAreNamedSessionAndKeepTheirAttributes(t *testing.T) {
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, Domain: "example.com", SameSite: bites.Strict, MaxAge: time.Hour})
	sc := srv.Do(add("tea", "")).Get("set-cookie")
	if !strings.HasPrefix(sc, "session=") || !strings.HasSuffix(sc, "; Path=/; Domain=example.com; Max-Age=3600; HttpOnly; Secure; SameSite=Strict") {
		t.Fatalf("%s", sc)
	}
}

func TestAMaxAgeShortenedInADeployAppliesToTheCookiesAlreadyOut(t *testing.T) {
	keys := [][]byte{key(1)}
	before, _ := cartApp(t, bites.SessionOptions{Keys: keys, MaxAge: 30 * 24 * time.Hour})
	c := sessionCookie(t, before.Do(add("tea", "")))
	after, clk2 := cartApp(t, bites.SessionOptions{Keys: keys, MaxAge: time.Hour})
	if got := after.Do(show(c)).Text(); got != `{"new":false,"items":["tea"]}` {
		t.Fatalf("within the new MaxAge: %s", got)
	}
	clk2.Advance(time.Hour)
	if got := after.Do(show(c)).Text(); got != `{"new":true,"items":[]}` {
		t.Fatalf("a cookie sealed under the old MaxAge outlived the new one: %s", got)
	}
}

type signedIn struct{ Items int }

func cartOwner(_ context.Context, _ bites.Request, deps struct{ S bites.Session[Cart] }) (signedIn, error) {
	if deps.S.IsNew() {
		return signedIn{}, bites.Unauthorized("sign in first")
	}
	return signedIn{Items: len(deps.S.Value.Items)}, nil
}

func TestAUserReadFromTheSessionIsACustomAuthWhoseSchemeTheDocumentStates(t *testing.T) {
	scheme := bites.SecurityScheme{Type: "apiKey", In: "cookie", Name: "__Host-session"}
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}}, func(app *bites.App, _ *bites.Sessions[Cart]) {
		app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
		app.Get("/me", func(_ context.Context, in struct{ U signedIn }) (signedIn, error) { return in.U, nil },
			bites.Provide(bites.CustomAuth(scheme, cartOwner)))
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/me"}); r.Status != 401 {
		t.Fatalf("no session: %d %s", r.Status, r.Text())
	}
	cookie := sessionCookie(t, srv.Do(add("tea", "")))
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Cookie": {cookie}}}); r.Status != 200 || r.Text() != `{"items":1}` {
		t.Fatalf("a session: %d %s", r.Status, r.Text())
	}
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	var d map[string]any
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	me := d["paths"].(map[string]any)["/me"].(map[string]any)["get"].(map[string]any)
	if me["security"] == nil || me["responses"].(map[string]any)["401"] == nil {
		t.Fatalf("/me states its scheme and its 401: %v", me)
	}
}

// Login is a session type that states its owner.
type Login struct {
	UserID string `json:"user_id"`
}

func (l Login) SessionOwner() string { return l.UserID }

// loginApp serves POST /login/{user} (Renew under that owner) and GET /me over Sessions[Login].
func loginApp(t *testing.T, st bites.SessionStore) (*bitetest.Server, *bites.Sessions[Login]) {
	t.Helper()
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	logins := bites.NewSessions[Login](bites.SessionOptions{Keys: [][]byte{key(1)}, Store: st})
	app.Provide(logins.Load)
	app.Post("/login/{user}", func(ctx context.Context, in struct {
		S    bites.Session[Login]
		Path struct{ User string }
	}) (bites.NoContent, error) {
		c, err := in.S.Renew(ctx, Login{UserID: in.Path.User})
		return bites.NoContent{Cookies: []bites.Cookie{c}}, err
	})
	app.Post("/anon", func(ctx context.Context, in struct{ S bites.Session[Login] }) (bites.NoContent, error) {
		c, err := in.S.Save(ctx, Login{})
		return bites.NoContent{Cookies: []bites.Cookie{c}}, err
	})
	app.Post("/logout-others", func(ctx context.Context, in struct{ S bites.Session[Login] }) (string, error) {
		n, err := in.S.RevokeOthers(ctx)
		if err != nil {
			return err.Error(), nil
		}
		return fmt.Sprint(n), nil
	})
	app.Post("/renew-then-revoke", func(ctx context.Context, in struct{ S bites.Session[Login] }) (string, error) {
		if _, err := in.S.Renew(ctx, in.S.Value); err != nil {
			return "", err
		}
		_, err := in.S.RevokeOthers(ctx)
		return fmt.Sprint(err), nil
	})
	app.Get("/me", func(_ context.Context, in struct{ S bites.Session[Login] }) (string, error) {
		if in.S.IsNew() {
			return "anonymous", nil
		}
		return in.S.Value.UserID, nil
	})
	return bitetest.Start(t, app), logins
}

func loginAs(t *testing.T, srv *bitetest.Server, user string) string {
	t.Helper()
	return sessionCookie(t, srv.Do(bitetest.Request{Method: "POST", Target: "/login/" + user}))
}

func whoAmI(srv *bitetest.Server, cookie string) string {
	return strings.Trim(srv.Do(show2("/me", cookie)).Text(), `"`)
}

func show2(target, cookie string) bitetest.Request {
	return bitetest.Request{Method: "GET", Target: target, Header: map[string][]string{"Cookie": {cookie}}}
}

func TestRevokingAnOwnerDeletesEverySessionOfThatOwnerAndNoOtherOwnersAndReturnsHowMany(t *testing.T) {
	st := newMemStore()
	srv, logins := loginApp(t, st)
	laptop, phone, tablet := loginAs(t, srv, "ana"), loginAs(t, srv, "ana"), loginAs(t, srv, "ana")
	budi := loginAs(t, srv, "budi")
	if whoAmI(srv, phone) != "ana" || whoAmI(srv, budi) != "budi" {
		t.Fatal("the sessions were not saved")
	}
	n, err := logins.RevokeOwner(context.Background(), "ana")
	if err != nil || n != 3 {
		t.Fatalf("RevokeOwner returned %d, %v; want 3", n, err)
	}
	for _, c := range []string{laptop, phone, tablet} {
		if got := whoAmI(srv, c); got != "anonymous" {
			t.Fatalf("a revoked session still reads as %q", got)
		}
	}
	if whoAmI(srv, budi) != "budi" {
		t.Fatal("another owner's session was revoked")
	}
	if n, err := logins.RevokeOwner(context.Background(), "ana"); n != 0 || err != nil {
		t.Fatalf("revoking an owner with nothing returned %d, %v", n, err)
	}
}

func TestASessionSavedUnderAnotherOwnerIsFoundUnderTheNewOneAndRenewLeavesNoOldId(t *testing.T) {
	st := newMemStore()
	srv, logins := loginApp(t, st)
	c := loginAs(t, srv, "ana")
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/login/budi", Header: map[string][]string{"Cookie": {c}}})
	c2 := sessionCookie(t, r)
	if n, _ := logins.RevokeOwner(context.Background(), "ana"); n != 0 {
		t.Fatalf("the session that changed hands was still found under ana: %d", n)
	}
	if len(st.ids()) != 1 || whoAmI(srv, c2) != "budi" {
		t.Fatalf("store %q", st.ids())
	}
	if n, _ := logins.RevokeOwner(context.Background(), "budi"); n != 1 {
		t.Fatalf("revoked %d under budi", n)
	}
}

func TestRevokingAnEmptyOwnerIsAnErrorAndDeletesNoAnonymousSession(t *testing.T) {
	st := newMemStore()
	srv, logins := loginApp(t, st)
	anon := sessionCookie(t, srv.Do(bitetest.Request{Method: "POST", Target: "/anon"}))
	if _, err := logins.RevokeOwner(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "empty owner") {
		t.Fatalf("err %v", err)
	}
	if whoAmI(srv, anon) != "" || len(st.ids()) != 1 {
		t.Fatal("an anonymous session was deleted")
	}
}

func TestRevokeOthersEndsEveryOtherSessionOfTheOwnerAndKeepsThisOne(t *testing.T) {
	st := newMemStore()
	srv, _ := loginApp(t, st)
	laptop, phone, tablet := loginAs(t, srv, "ana"), loginAs(t, srv, "ana"), loginAs(t, srv, "ana")
	budi := loginAs(t, srv, "budi")
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/logout-others", Header: map[string][]string{"Cookie": {phone}}})
	if r.Text() != `"2"` || r.Get("set-cookie") != "" {
		t.Fatalf("RevokeOthers answered %s, set-cookie %q", r.Text(), r.Get("set-cookie"))
	}
	if whoAmI(srv, phone) != "ana" || whoAmI(srv, budi) != "budi" {
		t.Fatal("the session kept, or another owner's, was ended")
	}
	if whoAmI(srv, laptop) != "anonymous" || whoAmI(srv, tablet) != "anonymous" {
		t.Fatal("another session of the owner survived")
	}
}

func TestRevokeOthersRefusesASessionItCannotKeepAndEndsNothing(t *testing.T) {
	st := newMemStore()
	srv, _ := loginApp(t, st)
	laptop, phone := loginAs(t, srv, "ana"), loginAs(t, srv, "ana")
	if r := srv.Do(bitetest.Request{Method: "POST", Target: "/logout-others"}); !strings.Contains(r.Text(), "on a new session, which is not in the store") {
		t.Fatalf("a new session: %s", r.Text())
	}
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/renew-then-revoke", Header: map[string][]string{"Cookie": {phone}}})
	if !strings.Contains(r.Text(), "call RevokeOthers before Renew") {
		t.Fatalf("after Renew: %s", r.Text())
	}
	if whoAmI(srv, laptop) != "ana" {
		t.Fatal("a refused RevokeOthers ended a session")
	}
}

func TestRevokeOwnerOnASessionTypeWithNoOwnerIsAnErrorThatSaysWhatToAdd(t *testing.T) {
	carts := bites.NewSessions[Cart](bites.SessionOptions{Keys: [][]byte{key(1)}, Store: newMemStore()})
	_, err := carts.RevokeOwner(context.Background(), "ana")
	if err == nil || !strings.Contains(err.Error(), "Cart, which has no SessionOwner method") {
		t.Fatalf("err %v", err)
	}
}

func TestAnOwnerStoreIsNotToldAnOwnerForASessionTypeThatStatesNone(t *testing.T) {
	st := newMemStore()
	srv, _ := cartApp(t, bites.SessionOptions{Keys: [][]byte{key(1)}, Store: st})
	srv.Do(add("tea", ""))
	if len(st.ids()) != 1 || len(st.owners) != 0 {
		t.Fatalf("ids %q owners %v", st.ids(), st.owners)
	}
}

func TestRevokingWhileRequestsSaveAtOnceIsSafeUnderRace(t *testing.T) {
	st := newMemStore()
	srv, logins := loginApp(t, st)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); loginAs(t, srv, "ana") }()
		go func() { defer wg.Done(); logins.RevokeOwner(context.Background(), "ana"); _ = i }()
	}
	wg.Wait()
	if _, err := logins.RevokeOwner(context.Background(), "ana"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.DeleteOwner(context.Background(), "ana", ""); n != 0 {
		t.Fatalf("%d sessions of ana survived the last revocation", n)
	}
}

// plainStore is a SessionStore that keeps nothing and cannot keep owners.
type plainStore struct{}

func (plainStore) Load(context.Context, string) ([]byte, bool, error)    { return nil, false, nil }
func (plainStore) Save(context.Context, string, []byte, time.Time) error { return nil }
func (plainStore) Delete(context.Context, string) error                  { return nil }

type ptrOwner struct{ ID string }

func (*ptrOwner) SessionOwner() string { return "x" }

type intOwner struct{ ID int }

func (intOwner) SessionOwner() int { return 1 }

func (m *memStore) expiry(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expires[id]
}

// expiringStore is memStore that also holds what a store promises: a session past its expiry is not
// found, and no one is the owner "".
type expiringStore struct{ *memStore }

func (e expiringStore) Load(ctx context.Context, id string) ([]byte, bool, error) {
	d, ok, err := e.memStore.Load(ctx, id)
	e.mu.Lock()
	defer e.mu.Unlock()
	if ok && !e.expires[id].After(bites.Now(ctx)) {
		return nil, false, nil
	}
	return d, ok, err
}

func (e expiringStore) DeleteOwner(ctx context.Context, owner, keep string) (int, error) {
	if owner == "" {
		return 0, nil
	}
	return e.memStore.DeleteOwner(ctx, owner, keep)
}

func TestTheMemoryStoreOfTheTestsKeepsTheContractOfEverySessionStore(t *testing.T) {
	bitetest.TestSessionStore(t, func(*testing.T) bites.SessionStore { return expiringStore{newMemStore()} })
	bitetest.TestOwnerSessionStore(t, func(*testing.T) bites.OwnerSessionStore { return expiringStore{newMemStore()} })
}
