package main

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites/bitetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

var requestID = regexp.MustCompile(`,"request_id":"[0-9a-f]+"`)

// withoutID is the body with the request ID taken out, which differs for every request.
func withoutID(r bitetest.Response) string { return requestID.ReplaceAllString(r.Text(), "") }

func testConfig() Config {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		panic(err)
	}
	return cfg
}

// start builds the app over a fake store and an in-memory broker.
func start(t *testing.T) (*bitetest.Server, *fakeStore, *Hub) {
	t.Helper()
	hub := NewHub(quiet, bitetest.NewBroker())
	store := &fakeStore{}
	return bitetest.Start(t, newApp(testConfig(), store, hub, quiet)), store, hub
}

func auth(name string) map[string][]string {
	return map[string][]string{"Authorization": {"Bearer toy-" + name}, "Content-Type": {"application/json"}}
}

func post(srv *bitetest.Server, target, user, body string) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "POST", Target: target, Header: auth(user), Body: []byte(body)})
}

func get(srv *bitetest.Server, target string) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "GET", Target: target})
}

func TestTheAppRegisters(t *testing.T) {
	_ = newApp(testConfig(), &fakeStore{}, NewHub(quiet, bitetest.NewBroker()), quiet) // a Registration mistake fails here
}

func TestACreatedRoomAnswers201WithItsLocation(t *testing.T) {
	srv, _, _ := start(t)
	r := post(srv, "/rooms", "ada", `{"name":"general"}`)
	if r.Status != 201 || r.Get("location") != "/rooms/1" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	var room Room
	if err := r.JSON(&room); err != nil || room.ID != 1 || room.Name != "general" {
		t.Fatalf("%v %+v", err, room)
	}
}

func TestARoomNameThatIsTakenIs409(t *testing.T) {
	srv, _, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	r := post(srv, "/rooms", "ada", `{"name":"general"}`)
	want := `{"title":"Conflict","status":409,"detail":"a room with that name exists"}`
	if r.Status != 409 || withoutID(r) != want {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestARoomNameThatIsTooShortIsA400NamingBodyName(t *testing.T) {
	srv, _, _ := start(t)
	r := post(srv, "/rooms", "ada", `{"name":"x"}`)
	if r.Status != 400 || r.Get("content-type") != "application/problem+json" || !strings.Contains(r.Text(), `"location":"body.name"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestACallerWithoutATokenIs401AndATokenThatIsNotToyIs401(t *testing.T) {
	srv, _, _ := start(t)
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/rooms", Body: []byte(`{"name":"general"}`),
		Header: map[string][]string{"Content-Type": {"application/json"}}})
	if r.Status != 401 || r.Get("www-authenticate") == "" {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "POST", Target: "/rooms", Body: []byte(`{"name":"general"}`),
		Header: map[string][]string{"Content-Type": {"application/json"}, "Authorization": {"Bearer nope"}}})
	if r.Status != 401 || !strings.Contains(r.Get("www-authenticate"), "invalid_token") {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestAStoreThatFailsIsA500ThatDoesNotLeakTheReason(t *testing.T) {
	srv, store, _ := start(t)
	store.fail = errors.New("connection refused by 10.1.2.3")
	r := post(srv, "/rooms", "ada", `{"name":"general"}`)
	if r.Status != 500 || strings.Contains(r.Text(), "10.1.2.3") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestRoomsArePagedWithACursor(t *testing.T) {
	srv, _, _ := start(t)
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		post(srv, "/rooms", "ada", `{"name":"`+n+`"}`)
	}
	var page RoomPage
	if r := get(srv, "/rooms?limit=2"); r.Status != 200 || r.JSON(&page) != nil || len(page.Items) != 2 || page.Next == nil || *page.Next != 2 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	page = RoomPage{}
	if r := get(srv, "/rooms?limit=2&after=2"); r.Status != 200 || r.JSON(&page) != nil || len(page.Items) != 1 || page.Next != nil {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := get(srv, "/rooms?after=99"); r.Text() != `{"items":[]}` {
		t.Fatalf("%s", r.Text())
	}
}

func TestAPageLimitOverTheMaximumIsA400NamingQueryLimit(t *testing.T) {
	srv, _, _ := start(t)
	r := get(srv, "/rooms?limit=101")
	want := `{"title":"Bad Request","status":400,"errors":[{"location":"query.limit","message":"at most 100"}]}`
	if r.Status != 400 || withoutID(r) != want {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := get(srv, "/rooms?limit=abc"); r.Status != 400 || !strings.Contains(r.Text(), "query.limit") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAMessageIsStoredWithItsAuthorFromTheToken(t *testing.T) {
	srv, _, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	r := post(srv, "/rooms/1/messages", "grace", `{"text":"hello"}`)
	var m Message
	if r.Status != 201 || r.JSON(&m) != nil || m.Author != "grace" || m.Text != "hello" || m.RoomID != 1 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestABlankMessageIsA400NamingBodyText(t *testing.T) {
	srv, _, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	r := post(srv, "/rooms/1/messages", "ada", `{"text":"   "}`)
	want := `{"title":"Bad Request","status":400,"errors":[{"location":"body.text","message":"must not be blank"}]}`
	if r.Status != 400 || withoutID(r) != want {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = post(srv, "/rooms/1/messages", "ada", `{"text":""}`)
	if r.Status != 400 || !strings.Contains(r.Text(), `"location":"body.text"`) {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAMessageInARoomThatDoesNotExistIs404(t *testing.T) {
	srv, _, _ := start(t)
	r := post(srv, "/rooms/7/messages", "ada", `{"text":"hello"}`)
	if r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := get(srv, "/rooms/7/messages"); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestHistoryIsNewestFirstAndPaged(t *testing.T) {
	srv, _, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	for _, text := range []string{"one", "two", "three"} {
		post(srv, "/rooms/1/messages", "ada", `{"text":"`+text+`"}`)
	}
	var page MessagePage
	if r := get(srv, "/rooms/1/messages?limit=2"); r.JSON(&page) != nil || len(page.Items) != 2 ||
		page.Items[0].Text != "three" || page.Items[1].Text != "two" || page.Next == nil || *page.Next != 2 {
		t.Fatalf("%s", r.Text())
	}
	page = MessagePage{}
	if r := get(srv, "/rooms/1/messages?limit=2&before=2"); r.JSON(&page) != nil || len(page.Items) != 1 ||
		page.Items[0].Text != "one" || page.Next != nil {
		t.Fatalf("%s", r.Text())
	}
}

func TestEverySocketInTheRoomHearsAMessageAndTheSenderToo(t *testing.T) {
	srv, _, hub := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	post(srv, "/rooms", "ada", `{"name":"random"}`)
	ada := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")})
	grace := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("grace")})
	other := srv.DialWS(bitetest.WSRequest{Target: "/rooms/2/ws", Header: auth("linus")})

	ada.SendJSON(Say{Text: "hello"}) // inserted by the socket, announced, delivered to both
	for name, ws := range map[string]*bitetest.WS{"ada": ada, "grace": grace} {
		var f Frame
		ws.ReadJSON(&f)
		if f.Kind != "message" || f.Message == nil || f.Message.Author != "ada" || f.Message.Text != "hello" || f.Message.RoomID != 1 {
			t.Fatalf("%s got %+v", name, f)
		}
	}

	post(srv, "/rooms/1/messages", "grace", `{"text":"from http"}`) // the HTTP route reaches the sockets too
	var f Frame
	grace.ReadJSON(&f)
	if f.Message == nil || f.Message.Text != "from http" || f.Message.Author != "grace" {
		t.Fatalf("%+v", f)
	}
	ada.ReadJSON(&f)
	other.Quiet() // room 2 hears nothing of room 1
	if hub.Rooms() != 2 {
		t.Fatalf("rooms with a topic: %d", hub.Rooms())
	}
}

func TestAnInvalidSocketMessageIsAnErrorFrameAndTheSocketStaysOpen(t *testing.T) {
	srv, store, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	ws := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")})
	for _, bad := range []string{`{"text":"   "}`, `not json`} {
		ws.SendText(bad)
		var f Frame
		ws.ReadJSON(&f)
		if f.Kind != "error" || f.Error == "" {
			t.Fatalf("%q: %+v", bad, f)
		}
	}
	if len(store.msgs) != 0 {
		t.Fatalf("stored %d", len(store.msgs))
	}
	ws.SendJSON(Say{Text: "fine"})
	var f Frame
	ws.ReadJSON(&f)
	if f.Kind != "message" {
		t.Fatalf("%+v", f)
	}
}

func TestASocketForARoomThatDoesNotExistOrWithoutATokenIsRefusedBeforeTheUpgrade(t *testing.T) {
	srv, _, _ := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	if _, r := srv.TryDialWS(bitetest.WSRequest{Target: "/rooms/9/ws", Header: auth("ada")}); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if _, r := srv.TryDialWS(bitetest.WSRequest{Target: "/rooms/1/ws"}); r.Status != 401 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if _, r := srv.TryDialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")}); r.Status == 401 || r.Status == 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestADroppedSocketLeavesNoTopicBehindOnTheNextDelivery(t *testing.T) {
	srv, _, hub := start(t)
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	ws := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")})
	ws.Close(1000, "bye")
	ws.ExpectClose(1000)
	ws.ExpectEOF()
	post(srv, "/rooms/1/messages", "ada", `{"text":"anyone?"}`)
	if hub.Rooms() != 0 {
		t.Fatalf("rooms with a topic: %d", hub.Rooms())
	}
}

func TestTheOperationalRoutesAnswer(t *testing.T) {
	srv, _, _ := start(t)
	if r := get(srv, "/livez"); r.Status != 200 {
		t.Fatalf("livez %d", r.Status)
	}
	if r := get(srv, "/readyz"); r.Status != 200 {
		t.Fatalf("readyz %d", r.Status)
	}
	get(srv, "/rooms")
	r := get(srv, "/metrics")
	if r.Status != 200 || !strings.Contains(r.Text(), `route="/rooms"`) {
		t.Fatalf("metrics %d %s", r.Status, r.Text())
	}
	if r.Get("x-content-type-options") != "nosniff" || r.Get("x-request-id") == "" {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestTheOpenAPIDocumentDescribesTheRoutesAndTheSocketsHandshake(t *testing.T) {
	srv, _, _ := start(t)
	r := get(srv, "/openapi.json")
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(r.Body, &doc); err != nil || r.Status != 200 {
		t.Fatalf("%d %v", r.Status, err)
	}
	if _, ok := doc.Paths["/rooms"]["post"]; !ok {
		t.Fatalf("paths %v", doc.Paths)
	}
	if _, ok := doc.Paths["/rooms/{id}/messages"]["get"]; !ok {
		t.Fatalf("paths %v", doc.Paths)
	}
	ws, ok := doc.Paths["/rooms/{id}/ws"]["get"].(map[string]any)
	if !ok {
		t.Fatalf("the socket route is the get of its handshake: %v", doc.Paths["/rooms/{id}/ws"])
	}
	if _, ok := ws["responses"].(map[string]any)["101"]; !ok || ws["x-websocket"] == nil {
		t.Fatalf("the socket answers 101 and states its messages: %v", ws)
	}
}

func TestBehindAProxyThatStripsAPrefixTheLocationAndTheDocumentsServersCarryThePublicURL(t *testing.T) {
	env := map[string]string{"CHAT_PUBLIC_URL": "https://example.com/chat"}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	srv := bitetest.Start(t, newApp(cfg, &fakeStore{}, NewHub(quiet, bitetest.NewBroker()), quiet))
	if r := post(srv, "/rooms", "ada", `{"name":"general"}`); r.Status != 201 || r.Get("location") != "/chat/rooms/1" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	var doc struct {
		Servers []struct{ URL string } `json:"servers"`
	}
	if err := json.Unmarshal(get(srv, "/openapi.json").Body, &doc); err != nil || len(doc.Servers) != 1 || doc.Servers[0].URL != "https://example.com/chat" {
		t.Fatalf("%v %+v", err, doc)
	}
}

func TestTheRateLimitAnswers429WhenTheBucketIsEmpty(t *testing.T) {
	hub := NewHub(quiet, bitetest.NewBroker())
	cfg := testConfig()
	cfg.RateLimitPerMinute = 6 // a bucket of 1
	srv := bitetest.Start(t, newApp(cfg, &fakeStore{}, hub, quiet))
	if r := get(srv, "/rooms"); r.Status != 200 {
		t.Fatalf("%d", r.Status)
	}
	if r := get(srv, "/rooms"); r.Status != 429 || r.Get("retry-after") == "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestTheConfigHasDefaultsAndNamesEveryBadVariable(t *testing.T) {
	cfg := testConfig()
	if cfg.Addr != ":8080" || cfg.RateLimitPerMinute != 600 || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("%+v", cfg)
	}
	env := map[string]string{"CHAT_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.0.0/16", "CHAT_RATE_LIMIT": "30", "CHAT_SHUTDOWN_DELAY": "0s"}
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil || len(cfg.TrustedProxies) != 2 || cfg.RateLimitPerMinute != 30 || cfg.ShutdownDelay != 0 {
		t.Fatalf("%v %+v", err, cfg)
	}
	env = map[string]string{"CHAT_TRUSTED_PROXIES": "10.0.0.0/99", "CHAT_RATE_LIMIT": "many", "CHAT_SHUTDOWN_DELAY": "soon", "CHAT_PUBLIC_URL": "https://example.com/chat/"}
	_, err = LoadConfig(func(k string) string { return env[k] })
	for _, name := range []string{"CHAT_TRUSTED_PROXIES", "CHAT_RATE_LIMIT", "CHAT_SHUTDOWN_DELAY", "CHAT_PUBLIC_URL"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%q not named in %v", name, err)
		}
	}
}

func TestTrustedProxiesFromTheEnvironmentRegister(t *testing.T) {
	cfg := testConfig()
	cfg.TrustedProxies = nil
	env := map[string]string{"CHAT_TRUSTED_PROXIES": "10.0.0.0/8"}
	cfg, _ = LoadConfig(func(k string) string { return env[k] })
	_ = newApp(cfg, &fakeStore{}, NewHub(quiet, bitetest.NewBroker()), quiet)
}

func TestAGapInTheBrokerTellsTheSocketsOfThatInstanceToReadTheHistoryAgain(t *testing.T) {
	broker := bitetest.NewBroker()
	hub := NewHub(quiet, broker)
	srv := bitetest.Start(t, newApp(testConfig(), &fakeStore{}, hub, quiet))
	post(srv, "/rooms", "ada", `{"name":"general"}`)
	ada := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")})
	for broker.Subscriptions("room.1") == 0 {
		time.Sleep(time.Millisecond)
	}
	broker.Gap()
	var f Frame
	ada.ReadJSON(&f)
	if f.Kind != "resync" || f.Message != nil {
		t.Fatalf("%+v", f)
	}
}

func TestAMessagePostedOnAnotherInstanceReachesThisInstancesSocketsOnce(t *testing.T) {
	broker := bitetest.NewBroker()
	store := &fakeStore{}
	here := bitetest.Start(t, newApp(testConfig(), store, NewHub(quiet, broker), quiet))
	there := bitetest.Start(t, newApp(testConfig(), store, NewHub(quiet, broker.Peer()), quiet))
	post(here, "/rooms", "ada", `{"name":"general"}`)
	ada := here.DialWS(bitetest.WSRequest{Target: "/rooms/1/ws", Header: auth("ada")})
	for broker.Subscriptions("room.1") == 0 {
		time.Sleep(time.Millisecond)
	}
	post(there, "/rooms/1/messages", "grace", `{"text":"from afar"}`)
	post(there, "/rooms/1/messages", "grace", `{"text":"second"}`)
	var f Frame
	ada.ReadJSON(&f)
	if f.Message == nil || f.Message.Text != "from afar" {
		t.Fatalf("%+v", f)
	}
	ada.ReadJSON(&f)
	if f.Message == nil || f.Message.Text != "second" {
		t.Fatalf("%+v", f)
	}
}
