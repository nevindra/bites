package bites_test

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/ws"
)

type (
	wsIn  struct{ Path struct{ ID int64 } }
	wsMsg struct{ Text string }
	wsOut struct {
		Text string `json:"text"`
	}
)

// room is the route most tests use: it echoes each message, and reports how the socket ended.
type room struct {
	closed chan bites.CloseInfo
	socket chan *bites.Socket[wsOut]
}

func newRoom() *room {
	return &room{closed: make(chan bites.CloseInfo, 4), socket: make(chan *bites.Socket[wsOut], 4)}
}

func (e *room) join(_ context.Context, _ wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
	e.socket <- s
	return bites.Inbox[wsMsg]{
		OnMessage: func(_ context.Context, m wsMsg) error { return s.Send(wsOut{Text: m.Text}) },
		OnClose:   func(info bites.CloseInfo) { e.closed <- info },
	}, nil
}

func (e *room) waitClosed(t *testing.T) bites.CloseInfo {
	t.Helper()
	select {
	case i := <-e.closed:
		return i
	case <-time.After(5 * time.Second):
		t.Fatal("OnClose was not called in 5 s")
		return bites.CloseInfo{}
	}
}

func (e *room) sock(t *testing.T) *bites.Socket[wsOut] {
	t.Helper()
	select {
	case s := <-e.socket:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("the handler was not called in 5 s")
		return nil
	}
}

func TestAMessageSentToAWebSocketRouteIsDecodedAndTheReplyIsAJSONTextFrame(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/7")
	if got, want := w.hdr["sec-websocket-accept"], "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Fatalf("Sec-WebSocket-Accept %q, want %q", got, want)
	}
	if w.hdr["upgrade"] != "websocket" || !strings.EqualFold(w.hdr["connection"], "upgrade") {
		t.Fatalf("headers %v", w.hdr)
	}
	w.text(`{"text":"hello"}`)
	w.expectText(`{"text":"hello"}`)
	w.text(`{"text":"again \"quoted\""}`)
	w.expectText(`{"text":"again \"quoted\""}`)
}

func TestAPathParamThatIsNotANumberRefusesTheHandshakeWithA400NamingPathIDAndNoUpgrade(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := handshake(t, srv.Dial("203.0.113.9:4000"), "/rooms/abc")
	want := `{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer","rule":"type"}]}`
	if w.status != 400 || w.body != want {
		t.Fatalf("%d %s", w.status, w.body)
	}
	select {
	case <-e.socket:
		t.Fatal("the handler ran for a handshake whose Input was refused")
	default:
	}
}

// ---- the handshake ----

func TestAHandshakeThatIsNotAValidWebSocketOpeningIsRefusedWithTheStatusADR0046Gives(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	base := "Host: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + wsKey + "\r\nSec-WebSocket-Version: 13\r\n"
	for _, tc := range []struct {
		name, req string
		status    int
		allow     string
		version   string
	}{
		{"HEAD", "HEAD /rooms/1 HTTP/1.1\r\n" + base + "\r\n", 405, "GET", ""},
		{"a plain GET", "GET /rooms/1 HTTP/1.1\r\nHost: test\r\n\r\n", 400, "", ""},
		{"another version", "GET /rooms/1 HTTP/1.1\r\n" + strings.Replace(base, "Version: 13", "Version: 12", 1) + "\r\n", 426, "", "13"},
		{"no key", "GET /rooms/1 HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n\r\n", 400, "", ""},
		{"a body", "GET /rooms/1 HTTP/1.1\r\n" + base + "Content-Length: 2\r\n\r\nhi", 400, "", ""},
	} {
		w := rawHandshake(t, srv.Dial("203.0.113.9:4000"), tc.req)
		if w.status != tc.status || w.hdr["content-type"] != "application/problem+json" {
			t.Fatalf("%s: %d %v %s", tc.name, w.status, w.hdr, w.body)
		}
		if tc.allow != "" && w.hdr["allow"] != tc.allow {
			t.Fatalf("%s: Allow %q", tc.name, w.hdr["allow"])
		}
		if tc.version != "" && w.hdr["sec-websocket-version"] != tc.version {
			t.Fatalf("%s: Sec-WebSocket-Version %q", tc.name, w.hdr["sec-websocket-version"])
		}
		if tc.name != "HEAD" && !strings.Contains(w.body, "WebSocket route /rooms/{id}") {
			t.Fatalf("%s: the detail does not name the route as a WebSocket route: %s", tc.name, w.body)
		}
	}
	if w := rawHandshake(t, srv.Dial("203.0.113.9:4000"), "POST /rooms/1 HTTP/1.1\r\n"+base+"Content-Length: 0\r\n\r\n"); w.status != 405 {
		t.Fatalf("POST: %d", w.status)
	}
}

func TestAnOriginOfAnotherSiteIsRefusedWith403BeforeAnyProviderRunsAndTheRulesOfADR0046Hold(t *testing.T) {
	var providers atomic.Int32
	type origin struct{ User wsUser }
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.Provide(func(context.Context, bites.Request, struct{}) (wsUser, error) {
			providers.Add(1)
			return wsUser{Name: "ada"}, nil
		})
		app.WebSocket("/open", func(context.Context, origin, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{}, nil
		})
		app.WebSocket("/listed", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{}, nil
		}, bites.AllowOrigins("https://App.Example.com"))
		app.WebSocket("/any", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{}, nil
		}, bites.AllowAnyOrigin())
	})
	for _, tc := range []struct {
		name, target string
		origin       []string
		status       int
	}{
		{"no Origin is not a browser", "/open", nil, 101},
		{"the same host", "/open", []string{"Origin: http://test"}, 101},
		{"the same host in another case", "/open", []string{"Origin: http://TEST"}, 101},
		{"the default port of the Origin's scheme", "/open", []string{"Origin: https://test:443"}, 101},
		{"another site", "/open", []string{"Origin: https://evil.example"}, 403},
		{"a host that only starts the same", "/open", []string{"Origin: http://test.evil.example"}, 403},
		{"null", "/open", []string{"Origin: null"}, 403},
		{"another port", "/open", []string{"Origin: http://test:3000"}, 403},
		{"a path", "/open", []string{"Origin: http://test/"}, 403},
		{"a ws scheme", "/open", []string{"Origin: ws://test"}, 403},
		{"two Origin headers", "/open", []string{"Origin: http://test", "Origin: http://test"}, 403},
		{"a listed origin, in any case", "/listed", []string{"Origin: https://app.example.com"}, 101},
		{"the route's own host is still allowed beside a list", "/listed", []string{"Origin: http://test"}, 101},
		{"an unlisted origin", "/listed", []string{"Origin: https://app.example.com:8443"}, 403},
		{"AllowAnyOrigin lets another site in", "/any", []string{"Origin: https://evil.example"}, 101},
		{"AllowAnyOrigin lets null in", "/any", []string{"Origin: null"}, 101},
	} {
		before := providers.Load()
		w := handshake(t, srv.Dial("203.0.113.9:4000"), tc.target, tc.origin...)
		if w.status != tc.status {
			t.Fatalf("%s: %d %s", tc.name, w.status, w.body)
		}
		if tc.status == 403 {
			if w.hdr["content-type"] != "application/problem+json" || !strings.Contains(w.body, "WebSocket route "+tc.target) {
				t.Fatalf("%s: %v %s", tc.name, w.hdr, w.body)
			}
			if providers.Load() != before {
				t.Fatalf("%s: a Provider ran for a handshake whose Origin was refused", tc.name)
			}
		}
	}
}

func TestBehindAProxyThatRewritesHostTheOriginIsComparedWithTheForwardedHostItWrote(t *testing.T) {
	h := func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
		return bites.Inbox[wsMsg]{}, nil
	}
	for _, tc := range []struct {
		name   string
		opts   []bites.Option
		peer   string
		extra  []string
		status int
	}{
		{"the forwarded host of a trusted proxy", []bites.Option{rewriters}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: chat.example.com", "Origin: https://chat.example.com"}, 101},
		{"the rightmost element, the one the nearest proxy wrote", []bites.Option{rewriters}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: evil.example, chat.example.com", "Origin: https://chat.example.com"}, 101},
		{"the Host is still the request's own authority", []bites.Option{rewriters}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: chat.example.com", "Origin: http://test"}, 101},
		{"another site behind the same proxy", []bites.Option{rewriters}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: chat.example.com", "Origin: https://evil.example"}, 403},
		{"a client that reached the port directly names its own", []bites.Option{rewriters}, "203.0.113.9:4000",
			[]string{"X-Forwarded-Host: evil.example", "Origin: https://evil.example"}, 403},
		{"a proxy trusted for X-Forwarded-For only", []bites.Option{balancers}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: evil.example", "Origin: https://evil.example"}, 403},
		{"a forwarded host that is not a host", []bites.Option{rewriters}, "10.0.0.5:4000",
			[]string{"X-Forwarded-Host: evil.example/x", "Origin: https://evil.example"}, 403},
	} {
		srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/open", h) }, tc.opts...)
		if w := handshake(t, srv.Dial(tc.peer), "/open", tc.extra...); w.status != tc.status {
			t.Errorf("%s: %d %s", tc.name, w.status, w.body)
		}
	}
}

func TestTheAppsOwnAllowedOriginsApplyToEveryWebSocketRouteAndAGroupsReplaceThem(t *testing.T) {
	h := func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
		return bites.Inbox[wsMsg]{}, nil
	}
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/a", h)
		app.Group("/g", bites.AllowOrigins("https://other.example")).WebSocket("/b", h)
	}, bites.WithAllowOrigins("https://app.example", "https://other.example"))
	for target, tc := range map[string]struct {
		origin string
		status int
	}{
		"/a":   {"https://app.example", 101},
		"/g/b": {"https://other.example", 101},
	} {
		if w := handshake(t, srv.Dial("203.0.113.9:4000"), target, "Origin: "+tc.origin); w.status != tc.status {
			t.Fatalf("%s: %d", target, w.status)
		}
	}
	if w := handshake(t, srv.Dial("203.0.113.9:4000"), "/g/b", "Origin: https://app.example"); w.status != 403 {
		t.Fatalf("the group's list replaces the app's: %d", w.status)
	}
}

// ---- before the 101: Providers, middleware, Validate and the handler's error ----

type wsUser struct{ Name string }

type wsAuthIn struct {
	User wsUser
	Path struct{ ID int64 }
}

func (in wsAuthIn) Validate() error {
	if in.Path.ID == 13 {
		return bites.Invalid("path.id", "unlucky")
	}
	return nil
}

func TestAProviderAnswersAnUnauthenticatedHandshakeWith401AndFillsTheInputOfAnAuthenticatedOne(t *testing.T) {
	var seen atomic.Value
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.Provide(func(_ context.Context, req bites.Request, _ struct{}) (wsUser, error) {
			if req.Header("Authorization") != "Bearer ok" {
				return wsUser{}, bites.Unauthorized("no token")
			}
			return wsUser{Name: "ada"}, nil
		})
		app.WebSocket("/rooms/{id}", func(_ context.Context, in wsAuthIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			seen.Store(in.User.Name)
			return bites.Inbox[wsMsg]{OnMessage: func(_ context.Context, m wsMsg) error { return s.Send(wsOut{Text: in.User.Name + ": " + m.Text}) }}, nil
		})
	})
	w := handshake(t, srv.Dial("203.0.113.9:4000"), "/rooms/1")
	if w.status != 401 || !strings.Contains(w.body, "no token") {
		t.Fatalf("%d %s", w.status, w.body)
	}
	w = memWS(t, srv, "/rooms/1", "Authorization: Bearer ok")
	w.text(`{"text":"hi"}`)
	w.expectText(`{"text":"ada: hi"}`)
	if seen.Load() != "ada" {
		t.Fatalf("the handler saw %v", seen.Load())
	}
}

func TestAnInputsValidateRunsBeforeTheUpgradeAndItsErrorIsTheAnswer(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.Provide(func(context.Context, bites.Request, struct{}) (wsUser, error) { return wsUser{}, nil })
		app.WebSocket("/rooms/{id}", func(context.Context, wsAuthIn, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			t.Error("the handler ran")
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	w := handshake(t, srv.Dial("203.0.113.9:4000"), "/rooms/13")
	if w.status != 400 || !strings.Contains(w.body, `"location":"path.id","message":"unlucky"`) {
		t.Fatalf("%d %s", w.status, w.body)
	}
}

func TestMiddlewareRunsBeforeTheUpgradeCanAddAHeaderToThe101AndCanRefuseIt(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) {
		g := app.Group("/api")
		g.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			if req.Header("X-Deny") != "" {
				return bites.Problem(403, "denied by middleware"), nil
			}
			res := next.Call(ctx)
			res.AddHeader("X-Seen", "yes")
			return res, nil
		})
		g.WebSocket("/rooms/{id}", e.join)
	})
	w := memWS(t, srv, "/api/rooms/1")
	if w.hdr["x-seen"] != "yes" {
		t.Fatalf("the 101 lacks the middleware's header: %v", w.hdr)
	}
	w.text(`{"text":"x"}`)
	w.expectText(`{"text":"x"}`)
	if d := handshake(t, srv.Dial("203.0.113.9:4000"), "/api/rooms/1", "X-Deny: 1"); d.status != 403 || !strings.Contains(d.body, "denied by middleware") {
		t.Fatalf("%d %s", d.status, d.body)
	}
}

func TestAHandlerErrorBeforeTheInboxIsTheAnswerAndTheSocketItWasGivenIsDead(t *testing.T) {
	got := make(chan *bites.Socket[wsOut], 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/rooms/{id}", func(_ context.Context, _ wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			got <- s
			return bites.Inbox[wsMsg]{}, bites.Forbidden("room is full")
		})
	})
	w := handshake(t, srv.Dial("203.0.113.9:4000"), "/rooms/1")
	if w.status != 403 || !strings.Contains(w.body, "room is full") || w.hdr["upgrade"] != "" {
		t.Fatalf("%d %v %s", w.status, w.hdr, w.body)
	}
	s := <-got
	if err := s.Send(wsOut{Text: "x"}); !errors.Is(err, bites.ErrSocketClosed) {
		t.Fatalf("Send on the socket of a refused handshake returned %v", err)
	}
	if s.Context().Err() == nil {
		t.Fatal("its context is live")
	}
}

func TestAMessageSentBeforeTheHandlerReturnsGoesOutAfterThe101(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/hello", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			if err := s.Send(wsOut{Text: "welcome"}); err != nil {
				return bites.Inbox[wsMsg]{}, err
			}
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	w := srv.DialWS(bitetest.WSRequest{Target: "/hello"})
	if got := w.ReadText(); got != `{"text":"welcome"}` {
		t.Fatalf("got %q", got)
	}
}

// ---- the close paths ----

func TestAClientCloseIsAnsweredWithTheSameStatusAndOnCloseSeesItAsTheClients(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.Close(3001, "bye")
	w.ExpectClose(3001)
	w.ExpectEOF()
	info := e.waitClosed(t)
	if info.Status != 3001 || info.Reason != "bye" || !info.Remote {
		t.Fatalf("%+v", info)
	}
	if err := e.sock(t).Send(wsOut{}); !errors.Is(err, bites.ErrSocketClosed) {
		t.Fatalf("a kept Socket returned %v", err)
	}
}

func TestAServerCloseSendsTheStatusWaitsForTheClientsAnswerAndOnCloseSeesItAsTheServers(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	s := e.sock(t)
	if err := s.Close(1000, "done"); err != nil {
		t.Fatal(err)
	}
	if got := w.expectClose(1000); got != "done" {
		t.Fatalf("reason %q", got)
	}
	if err := s.Send(wsOut{}); !errors.Is(err, bites.ErrSocketClosed) {
		t.Fatalf("Send after Close returned %v", err)
	}
	if err := s.Close(1000, ""); !errors.Is(err, bites.ErrSocketClosed) {
		t.Fatalf("a second Close returned %v", err)
	}
	if s.Context().Err() == nil {
		t.Fatal("the context was not cancelled by Close")
	}
	w.closeWith(1000, "")
	w.expectEOF()
	if info := e.waitClosed(t); info.Status != 1000 || info.Reason != "done" || info.Remote {
		t.Fatalf("%+v", info)
	}
}

func TestACloseWithAStatusAServerMayNotSendOrALongReasonIsRefusedAndClosesNothing(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	s := e.sock(t)
	for _, code := range []uint16{1005, 1006, 1015, 999, 2000, 5000} {
		if err := s.Close(code, ""); err == nil {
			t.Fatalf("Close(%d) was accepted", code)
		}
	}
	if err := s.Close(1000, strings.Repeat("x", 124)); err == nil {
		t.Fatal("a 124 byte reason was accepted")
	}
	w.text(`{"text":"still open"}`)
	w.expectText(`{"text":"still open"}`)
}

func TestAClientThatNeverAnswersAServerCloseIsDroppedAfterFiveSeconds(t *testing.T) {
	e := newRoom()
	srv, clk, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	s := e.sock(t)
	s.Close(1001, "")
	w.expectClose(1001)
	for range 6 {
		clk.Advance(time.Second)
	}
	w.expectEOF()
	if info := e.waitClosed(t); info.Status != 1001 {
		t.Fatalf("%+v", info)
	}
}

func TestAClientThatLeavesWithoutAClosePutsOnCloseAt1006WithTheError(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	w.c.Close()
	info := e.waitClosed(t)
	if info.Status != 1006 || info.Remote || info.Err == nil {
		t.Fatalf("%+v", info)
	}
}

func TestAPingTheClientNeverAnswersClosesTheSocketAtTheSecondMissedPongWith1008(t *testing.T) {
	e := newRoom()
	srv, clk, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1", NoAutoPong: true})
	for range 2 {
		clk.Advance(30 * time.Second)
		w.ReadPing()
	}
	clk.Advance(30 * time.Second)
	w.ExpectClose(1008)
	w.Close(1008, "")
	w.ExpectEOF()
	if info := e.waitClosed(t); info.Status != 1008 || info.Reason != "ping timeout" {
		t.Fatalf("%+v", info)
	}
}

func TestAClientThatAnswersEveryPingKeepsTheSocketOpenAndAnUnsolicitedPongCountsToo(t *testing.T) {
	e := newRoom()
	srv, clk, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	for i := range 6 {
		clk.Advance(30 * time.Second)
		op, p := w.next()
		if op != ws.OpPing {
			t.Fatalf("round %d: opcode %d", i, op)
		}
		if i%2 == 0 {
			w.write(true, ws.OpPong, p)
		} else {
			w.write(true, ws.OpPong, nil) // a pong of another payload is still a pong
		}
		// A message after the pong makes sure the server has read it before the clock moves.
		w.text(`{"text":"alive"}`)
		w.expectText(`{"text":"alive"}`)
	}
}

func TestAPingFromTheClientIsAnsweredWithAPongOfTheSamePayload(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.Ping("hello")
	if m := w.Read(); m.Kind != bitetest.Pong || m.Text() != "hello" {
		t.Fatalf("got a %s %q", m.Kind, m.Data)
	}
}

func TestMoreThan32PingsAtOnceCloseTheSocketWith1008(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	var b []byte
	for range 40 {
		b = ws.AppendClientFrame(b, true, ws.OpPing, nil, [4]byte{1, 2, 3, 4})
	}
	w.c.Write(b)
	for {
		op, p := w.next()
		if op == ws.OpClose {
			if len(p) < 2 || p[0] != 0x03 || p[1] != 0xf0 {
				t.Fatalf("close %v", p)
			}
			break
		}
	}
}

func TestAMessageThatDoesNotDecodeClosesTheSocketWith1007NamingTheFieldUnlessOnMalformedKeepsIt(t *testing.T) {
	kept := make(chan error, 2)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/strict", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{OnMessage: func(_ context.Context, m wsMsg) error { return s.Send(wsOut{Text: m.Text}) }}, nil
		})
		app.WebSocket("/lenient", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{
				OnMessage: func(_ context.Context, m wsMsg) error { return s.Send(wsOut{Text: m.Text}) },
				OnMalformed: func(_ context.Context, raw []byte, err error) error {
					kept <- err
					return s.Send(wsOut{Text: "bad: " + string(raw)})
				},
			}, nil
		})
	})
	w := memWS(t, srv, "/strict")
	w.text(`{"text":5}`)
	if reason := w.expectClose(1007); !strings.Contains(reason, "text") {
		t.Fatalf("the reason %q does not name the field", reason)
	}
	w = memWS(t, srv, "/lenient")
	w.text(`{"text":5}`)
	w.expectText(`{"text":"bad: {\"text\":5}"}`)
	w.text(`not json`)
	w.expectText(`{"text":"bad: not json"}`)
	w.text(`{"text":"fine"}`)
	w.expectText(`{"text":"fine"}`)
	err := <-kept
	var me *bites.MalformedError
	if !errors.Is(err, bites.ErrMalformedMessage) || !errors.As(err, &me) || me.Errors[0].Location != "message.text" {
		t.Fatalf("%v %+v", err, me)
	}
}

func TestASocketThatOnlySendsClosesWith1003WhenTheClientSendsAnyMessage(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/feed", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[struct{}], error) {
			if err := s.Send(wsOut{Text: "hello"}); err != nil {
				return bites.Inbox[struct{}]{}, err
			}
			return bites.Inbox[struct{}]{}, nil
		})
	})
	for _, msg := range []string{`{}`, `not json`} {
		w := memWS(t, srv, "/feed")
		w.expectText(`{"text":"hello"}`)
		w.text(msg)
		if reason := w.expectClose(1003); reason != "this socket takes no messages" {
			t.Fatalf("%q: the reason is %q", msg, reason)
		}
	}
}

func TestAPanicInOnMessageIsRecoveredLoggedAndClosesTheSocketWith1011(t *testing.T) {
	closed := make(chan bites.CloseInfo, 1)
	srv, _, logs := startWS(t, func(app *bites.App) {
		app.WebSocket("/boom", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{
				OnMessage: func(context.Context, wsMsg) error { panic("kaput") },
				OnClose:   func(i bites.CloseInfo) { closed <- i },
			}, nil
		})
	})
	w := memWS(t, srv, "/boom")
	w.text(`{"text":"x"}`)
	w.expectClose(1011)
	w.closeWith(1011, "")
	w.expectEOF()
	if i := <-closed; i.Status != 1011 {
		t.Fatalf("%+v", i)
	}
	if !strings.Contains(logs.String(), "kaput") || !strings.Contains(logs.String(), "panic in a WebSocket OnMessage") {
		t.Fatalf("the log does not carry the panic: %s", logs.String())
	}
}

func TestAnErrorFromOnMessageClosesTheSocketWith1011AndAWSErrorChoosesTheStatus(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/err", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return errors.New("db down") }}, nil
		})
	})
	w := memWS(t, srv, "/err")
	w.text(`{"text":"x"}`)
	if reason := w.expectClose(1011); strings.Contains(reason, "db down") {
		t.Fatalf("the error text reached the client: %q", reason)
	}
}

func TestACloseErrorFromOnMessageClosesTheSocketWithItsStatusAndReasonEvenWrapped(t *testing.T) {
	closed := make(chan bites.CloseInfo, 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/c", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{
				OnMessage: func(context.Context, wsMsg) error {
					return fmt.Errorf("check token: %w", &bites.CloseError{Status: 4001, Reason: "token expired"})
				},
				OnClose: func(i bites.CloseInfo) { closed <- i },
			}, nil
		})
	})
	w := memWS(t, srv, "/c")
	w.text(`{"text":"x"}`)
	if reason := w.expectClose(4001); reason != "token expired" {
		t.Fatalf("reason %q", reason)
	}
	w.closeWith(4001, "")
	w.expectEOF()
	if i := <-closed; i.Status != 4001 || i.Remote {
		t.Fatalf("%+v", i)
	}
}

func TestACloseErrorFromOnMalformedChoosesTheStatusToo(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/m", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{
				OnMessage: func(context.Context, wsMsg) error { return nil },
				OnMalformed: func(context.Context, []byte, error) error {
					return &bites.CloseError{Status: 1003, Reason: "speak JSON"}
				},
			}, nil
		})
	})
	w := memWS(t, srv, "/m")
	w.text(`not json`)
	if reason := w.expectClose(1003); reason != "speak JSON" {
		t.Fatalf("reason %q", reason)
	}
}

func TestACloseErrorWithAStatusAServerMayNotSendClosesWith1011AndIsLogged(t *testing.T) {
	for _, ce := range []bites.CloseError{{Status: 1006}, {Status: 1005}, {Status: 5000}, {Status: 4000, Reason: strings.Repeat("x", 124)}, {Status: 4000, Reason: "\xff"}} {
		srv, _, logs := startWS(t, func(app *bites.App) {
			app.WebSocket("/bad", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
				return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return &ce }}, nil
			})
		})
		w := memWS(t, srv, "/bad")
		w.text(`{"text":"x"}`)
		w.expectClose(1011)
		if !strings.Contains(logs.String(), "a CloseError a server may not send") {
			t.Fatalf("%+v: not logged: %s", ce, logs.String())
		}
	}
}

func TestAContextHandedToOnMessageIsCancelledWhenTheSocketStartsToClose(t *testing.T) {
	ctxs := make(chan context.Context, 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/ctx", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{OnMessage: func(ctx context.Context, _ wsMsg) error { ctxs <- ctx; return nil }}, nil
		})
	})
	w := memWS(t, srv, "/ctx")
	w.text(`{"text":"x"}`)
	ctx := <-ctxs
	if ctx.Err() != nil {
		t.Fatal("cancelled while open")
	}
	w.closeWith(1000, "")
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not cancelled by the client's close")
	}
}

func TestAReaderThatFallsMegabyteBehindIsClosedWith1008AndItsQueueIsDropped(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	s := e.sock(t)
	big := wsOut{Text: strings.Repeat("x", 100<<10)}
	var err error
	sent := 0
	for ; err == nil && sent < 100; sent++ {
		err = s.Send(big)
	}
	if !errors.Is(err, bites.ErrSendQueueFull) || sent < 9 || sent > 13 {
		t.Fatalf("after %d sends of 100 KiB: %v", sent, err)
	}
	for {
		op, p := w.next()
		if op == ws.OpClose {
			if len(p) < 2 || p[0] != 0x03 || p[1] != 0xf0 {
				t.Fatalf("closed with %v, want 1008", p[:2])
			}
			break
		}
	}
	w.closeWith(1008, "")
	w.expectEOF()
	if info := e.waitClosed(t); info.Status != 1008 {
		t.Fatalf("%+v", info)
	}
}

func TestAMessageOverMaxMessageClosesTheSocketWith1009BeforeItIsRead(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join, bites.MaxMessage(1000)) })
	w := memWS(t, srv, "/rooms/1")
	w.text(`{"text":"` + strings.Repeat("x", 980) + `"}`)
	w.expectText(`{"text":"` + strings.Repeat("x", 980) + `"}`)
	w.text(`{"text":"` + strings.Repeat("x", 1000) + `"}`)
	w.expectClose(1009)
}

func TestABinaryFrameToARouteWhoseMessageIsNotBytesClosesTheSocketWith1003(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	w.write(true, ws.OpBinary, []byte(`{"text":"x"}`))
	w.expectClose(1003)
}

func TestARouteWhoseMessageAndSocketAreBytesTakesTextAndBinaryFramesAndSendsBinary(t *testing.T) {
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/raw", func(_ context.Context, _ Empty, s *bites.Socket[[]byte]) (bites.Inbox[[]byte], error) {
			return bites.Inbox[[]byte]{OnMessage: func(_ context.Context, m []byte) error { return s.Send(append([]byte("echo:"), m...)) }}, nil
		})
	})
	w := memWS(t, srv, "/raw")
	w.write(true, ws.OpBinary, []byte{0, 1, 2})
	if op, p := w.next(); op != ws.OpBinary || string(p) != "echo:\x00\x01\x02" {
		t.Fatalf("%d %q", op, p)
	}
	w.text("plain")
	if op, p := w.next(); op != ws.OpBinary || string(p) != "echo:plain" {
		t.Fatalf("%d %q", op, p)
	}
}

func TestARawMessageKeepsTheExactBytesBesideTheDecodedValue(t *testing.T) {
	got := make(chan bites.Raw[wsMsg], 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/signed", func(context.Context, Empty, *bites.Socket[wsOut]) (bites.Inbox[bites.Raw[wsMsg]], error) {
			return bites.Inbox[bites.Raw[wsMsg]]{OnMessage: func(_ context.Context, m bites.Raw[wsMsg]) error { got <- m; return nil }}, nil
		})
	})
	w := memWS(t, srv, "/signed")
	const body = `{ "text" : "a" }`
	w.text(body)
	m := <-got
	if m.Value.Text != "a" || string(m.Bytes()) != body {
		t.Fatalf("%+v %q", m.Value, m.Bytes())
	}
	w.write(true, ws.OpBinary, []byte(body))
	w.expectClose(1003)
}

// ---- assembly (ADR 0046) ----

func TestAMessageSplitIntoFragmentsAndALargeOneArePutTogetherBeforeTheHandlerSeesThem(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	w.write(false, ws.OpText, []byte(`{"te`))
	w.write(false, ws.OpContinuation, []byte(`xt":"fra`))
	w.write(true, ws.OpContinuation, []byte(`gmented"}`))
	w.expectText(`{"text":"fragmented"}`)
	big := strings.Repeat("0123456789", 5000) // 50,000 bytes: three size classes
	w.text(`{"text":"` + big + `"}`)
	w.expectText(`{"text":"` + big + `"}`)
}

func TestAMessageNotFinishedInMessageTimeoutClosesTheSocketWith1008AndGivesItsMemoryBack(t *testing.T) {
	e := newRoom()
	srv, clk, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	w.write(false, ws.OpText, []byte(`{"text":"unfinished`))
	w.write(true, ws.OpPing, []byte("sync")) // the pong shows the server has read the fragment
	if op, _ := w.next(); op != ws.OpPong {
		t.Fatalf("opcode %d", op)
	}
	passMoveAfter(t, clk)
	clk.Advance(31 * time.Second)
	w.expectClose(1008)
	w.closeWith(1008, "")
	w.expectEOF()
	if info := e.waitClosed(t); info.Status != 1008 {
		t.Fatalf("%+v", info)
	}
}

func TestAReassemblyBudgetThatIsSpentClosesTheNextSocketThatSpillsWith1013AndItsEchoIsAccepted(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/rooms/{id}", e.join, bites.MaxMessage(16<<10))
	}, bites.WithLimits(bites.Limits{ReassemblyBudget: 20 << 10}))
	chunk := []byte(strings.Repeat("a", 8000))
	first := memWS(t, srv, "/rooms/1")
	first.write(false, ws.OpText, chunk) // 8,000 bytes: past the free 4 KiB, 16 KiB charged
	second := memWS(t, srv, "/rooms/2")
	second.write(false, ws.OpText, chunk)
	second.expectClose(1013)
	second.closeWith(1013, "") // the echo of the status the server sent: not a protocol error
	second.expectEOF()
	// The first socket still has its share, and finishing its message gives it back.
	first.write(true, ws.OpContinuation, []byte(`"`))
	first.expectClose(1007) // the 8,001 bytes are not JSON: a decoded message, and a refused one
}

func TestARouteWhoseMaxMessageIsAboveTheReassemblyBudgetIsRefusedAtRegistration(t *testing.T) {
	defer func() {
		r := recover()
		var re *bites.RegistrationError
		if err, _ := r.(error); !errors.As(err, &re) || !strings.Contains(re.Error(), "MaxMessage is 32768 bytes, above the app's reassembly budget of 16384") {
			t.Fatalf("recovered %v", r)
		}
	}()
	app := bites.New(bites.WithLimits(bites.Limits{ReassemblyBudget: 16 << 10}))
	app.WebSocket("/x", newRoom().join, bites.MaxMessage(32<<10))
}

// ---- Topic ----

func TestAPublishReachesEverySubscriberWithOneFrameAndAClosedSocketLeavesTheTopic(t *testing.T) {
	var topic bites.Topic[wsOut]
	socks := make(chan *bites.Socket[wsOut], 3)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/feed", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			topic.Subscribe(s)
			socks <- s
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	var ws3 [3]*wsc
	for i := range ws3 {
		ws3[i] = memWS(t, srv, "/feed")
	}
	for range 3 {
		<-socks
	}
	if topic.Len() != 3 {
		t.Fatalf("Len %d", topic.Len())
	}
	if err := topic.Publish(wsOut{Text: "all"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range ws3 {
		c.expectText(`{"text":"all"}`)
	}
	ws3[0].closeWith(1000, "")
	ws3[0].expectEOF()
	waitFor(t, "the closed socket to leave the topic", func() bool { return topic.Len() == 2 })
	topic.Publish(wsOut{Text: "two"})
	ws3[1].expectText(`{"text":"two"}`)
	ws3[2].expectText(`{"text":"two"}`)
	var empty bites.Topic[wsOut]
	if err := empty.Publish(wsOut{}); err != nil {
		t.Fatal(err)
	}
}

func TestUnsubscribingStopsMessagesAndSubscribingASocketThatClosedDoesNothing(t *testing.T) {
	var topic bites.Topic[wsOut]
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	w := memWS(t, srv, "/rooms/1")
	s := e.sock(t)
	topic.Subscribe(s)
	topic.Subscribe(s)
	if topic.Len() != 1 {
		t.Fatalf("Len %d", topic.Len())
	}
	topic.Unsubscribe(s)
	topic.Publish(wsOut{Text: "nobody"})
	w.text(`{"text":"marker"}`)
	w.expectText(`{"text":"marker"}`) // the first frame the client gets is not the publish
	w.closeWith(1000, "")
	w.expectEOF()
	e.waitClosed(t)
	topic.Subscribe(s)
	waitFor(t, "a closed socket to leave at once", func() bool { return topic.Len() == 0 })
}

// ---- real connections ----

func serveTCP(t *testing.T, register func(app *bites.App), opts ...bites.Option) (addr string, app *bites.App) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	lb := &logBuf{}
	app = bites.New(append([]bites.Option{bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil)))}, opts...)...)
	register(app)
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	return ln.Addr().String(), app
}

func TestAWebSocketOverLoopbackTCPEchoesFragmentsBroadcastsAndClosesCleanly(t *testing.T) {
	e := newRoom()
	var topic bites.Topic[wsOut]
	addr, _ := serveTCP(t, func(app *bites.App) {
		app.WebSocket("/rooms/{id}", func(ctx context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			topic.Subscribe(s)
			return e.join(ctx, in, s)
		})
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := handshake(t, c, "/rooms/1")
	if w.status != 101 {
		t.Fatalf("%d %s", w.status, w.body)
	}
	for i := range 50 {
		w.text(`{"text":"msg ` + strings.Repeat("y", i) + `"}`)
		w.expectText(`{"text":"msg ` + strings.Repeat("y", i) + `"}`)
	}
	big := strings.Repeat("z", 60000)
	w.text(`{"text":"` + big + `"}`)
	w.expectText(`{"text":"` + big + `"}`)
	w.write(false, ws.OpText, []byte(`{"text":"a`))
	w.write(true, ws.OpContinuation, []byte(`b"}`))
	w.expectText(`{"text":"ab"}`)
	topic.Publish(wsOut{Text: "pushed"})
	w.expectText(`{"text":"pushed"}`)
	// Sleep past the engine's move to a fresh goroutine, then talk again.
	time.Sleep(250 * time.Millisecond)
	w.text(`{"text":"after the move"}`)
	w.expectText(`{"text":"after the move"}`)
	w.closeWith(1000, "bye")
	w.expectClose(1000)
	w.expectEOF()
	if info := e.waitClosed(t); info.Status != 1000 || !info.Remote {
		t.Fatalf("%+v", info)
	}
}

func TestAWebSocketOverTLSEchoesAndBroadcastsThroughTheFlusherAlone(t *testing.T) {
	scfg, ccfg := tlsConfigs(t)
	e := newRoom()
	var topic bites.Topic[wsOut]
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.WebSocket("/rooms/{id}", func(ctx context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
		topic.Subscribe(s)
		return e.join(ctx, in, s)
	})
	go app.ServeTLS(ln, scfg)
	defer app.Close()
	c, err := tls.Dial("tcp", ln.Addr().String(), ccfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := handshake(t, c, "/rooms/1")
	if w.status != 101 {
		t.Fatalf("%d %s", w.status, w.body)
	}
	for i := range 20 {
		w.text(`{"text":"tls ` + strings.Repeat("y", i) + `"}`)
		w.expectText(`{"text":"tls ` + strings.Repeat("y", i) + `"}`)
	}
	big := strings.Repeat("z", 40000)
	w.text(`{"text":"` + big + `"}`)
	w.expectText(`{"text":"` + big + `"}`)
	topic.Publish(wsOut{Text: "pushed"})
	w.expectText(`{"text":"pushed"}`)
	time.Sleep(250 * time.Millisecond)
	w.text(`{"text":"after the move"}`)
	w.expectText(`{"text":"after the move"}`)
	w.closeWith(1000, "")
	w.expectClose(1000)
	if info := e.waitClosed(t); info.Status != 1000 {
		t.Fatalf("%+v", info)
	}
}

func TestShutdownSendsAnOpenWebSocketCloseWith1001AndOnCloseSeesIt(t *testing.T) {
	e := newRoom()
	addr, app := serveTCP(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) })
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := handshake(t, c, "/rooms/1")
	if w.status != 101 {
		t.Fatal(w.status)
	}
	e.sock(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(ctx) }()
	w.expectClose(1001)
	w.closeWith(1001, "")
	w.expectEOF()
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v: the socket answered its Close", err)
	}
	if info := e.waitClosed(t); info.Status != 1001 || info.Remote {
		t.Fatalf("OnClose saw %+v, want status 1001 sent by the server", info)
	}
}

func TestShutdownDropsAWebSocketThatDoesNotAnswerItsCloseAfterTheCloseTimeoutAndOnCloseStillSees1001(t *testing.T) {
	e := newRoom()
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(&logBuf{}, nil))))
	app.WebSocket("/rooms/{id}", e.join)
	srv := bitetest.Start(t, app)
	w := memWS(t, srv, "/rooms/1")
	e.sock(t)
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	w.expectClose(1001)
	waitFor(t, "the close deadline to be armed", func() bool { return clk.Pending() > 0 })
	clk.Advance(6 * time.Second)
	w.expectEOF()
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	if info := e.waitClosed(t); info.Status != 1001 {
		t.Fatalf("OnClose saw %+v, want 1001", info)
	}
}

func TestAMessageSentToAWebSocketAfterShutdownBeganIsNotDelivered(t *testing.T) {
	e := newRoom()
	var got atomic.Int32
	addr, app := serveTCP(t, func(app *bites.App) {
		app.WebSocket("/rooms/{id}", func(_ context.Context, _ wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{
				OnMessage: func(context.Context, wsMsg) error { got.Add(1); return nil },
				OnClose:   func(info bites.CloseInfo) { e.closed <- info },
			}, nil
		})
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := handshake(t, c, "/rooms/1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(ctx) }()
	w.expectClose(1001)
	w.text(`{"text":"late"}`)
	w.closeWith(1001, "")
	w.expectEOF()
	<-done
	e.waitClosed(t)
	if got.Load() != 0 {
		t.Fatal("a message sent after the server's Close reached the handler")
	}
}

func TestAWebSocketHandshakeInFlightWhenShutdownStartsOpensAndThenGets1001(t *testing.T) {
	e := newRoom()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	addr, app := serveTCP(t, func(app *bites.App) {
		app.WebSocket("/rooms/{id}", func(ctx context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			entered <- struct{}{}
			<-release
			return e.join(ctx, in, s)
		})
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := "GET /rooms/1 HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + wsKey + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	res := make(chan *wsc, 1)
	go func() { res <- rawHandshake(t, c, req) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	w := <-res
	if w.status != 101 {
		t.Fatalf("handshake answered %d", w.status)
	}
	w.expectClose(1001)
	w.closeWith(1001, "")
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	if info := e.waitClosed(t); info.Status != 1001 {
		t.Fatalf("OnClose saw %+v", info)
	}
}

var _ = bitetest.NewClock

// A handler that sends and joins a topic, then refuses the handshake, gives both back: the topic
// does not keep the refused socket, and the bytes it queued return to the app's SendBudget, or a
// client could drain the budget with refused handshakes until every socket closed with 1013.
func TestSubscribeUnlessLeavesOneSocketOutOfTheMessagesItsFunctionSkipsAndAPanicLeavesItOut(t *testing.T) {
	var topic bites.Topic[wsOut]
	var muted atomic.Bool
	joined := make(chan struct{}, 3)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/feed/{id}", func(_ context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			switch in.Path.ID {
			case 1:
				topic.Subscribe(s)
			case 2:
				topic.SubscribeUnless(s, func(wsOut) bool { return muted.Load() })
			case 3:
				topic.SubscribeUnless(s, func(v wsOut) bool {
					if v.Text == "boom" {
						panic("a filter that fails")
					}
					return false
				})
			}
			joined <- struct{}{}
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	c1, c2, c3 := memWS(t, srv, "/feed/1"), memWS(t, srv, "/feed/2"), memWS(t, srv, "/feed/3")
	for range 3 {
		<-joined
	}
	topic.Publish(wsOut{Text: "a"})
	muted.Store(true)
	topic.Publish(wsOut{Text: "b"})
	topic.Publish(wsOut{Text: "boom"})
	muted.Store(false)
	topic.Publish(wsOut{Text: "c"})
	for _, f := range []string{"a", "b", "boom", "c"} {
		c1.expectText(`{"text":"` + f + `"}`)
	}
	for _, f := range []string{"a", "c"} {
		c2.expectText(`{"text":"` + f + `"}`) // b and boom were skipped while muted
	}
	for _, f := range []string{"a", "b", "c"} {
		c3.expectText(`{"text":"` + f + `"}`) // the panic left it out of boom only
	}
	if topic.Len() != 3 {
		t.Fatalf("Len %d: a socket with a function is a subscriber", topic.Len())
	}
}

func TestAMessageEverySocketSkipsIsNotEncodedAndSubscribeUnlessWithNilPanics(t *testing.T) {
	var topic bites.Topic[wsOut]
	joined := make(chan struct{}, 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/feed", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			topic.SubscribeUnless(s, func(v wsOut) bool { return v.Text == "skip" })
			joined <- struct{}{}
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	c := memWS(t, srv, "/feed")
	<-joined
	if err := topic.Publish(wsOut{Text: "skip"}); err != nil {
		t.Fatal(err)
	}
	topic.Publish(wsOut{Text: "next"})
	c.expectText(`{"text":"next"}`)
	defer func() {
		if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "skip is nil") {
			t.Fatalf("recovered %v", r)
		}
	}()
	topic.SubscribeUnless(nil, nil)
}

func TestARefusedHandshakeLeavesItsTopicsAndGivesBackTheSendBudgetItsEarlySendsTook(t *testing.T) {
	var topic bites.Topic[wsOut]
	big := strings.Repeat("x", 400)
	okSock := make(chan *bites.Socket[wsOut], 1)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/refused", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			s.Send(wsOut{Text: big})
			topic.Subscribe(s)
			return bites.Inbox[wsMsg]{}, bites.Forbidden("no")
		})
		app.WebSocket("/ok", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			okSock <- s
			return bites.Inbox[wsMsg]{}, nil
		})
	}, bites.WithLimits(bites.Limits{SendBudget: 1000}))
	for range 3 {
		if w := handshake(t, srv.Dial("203.0.113.9:4000"), "/refused"); w.status != 403 {
			t.Fatalf("status %d, want 403", w.status)
		}
	}
	if n := topic.Len(); n != 0 {
		t.Fatalf("the topic holds %d refused sockets, want 0", n)
	}
	memWS(t, srv, "/ok")
	if err := (<-okSock).Send(wsOut{Text: strings.Repeat("y", 300)}); err != nil {
		t.Fatalf("a Send of 300 bytes on a healthy socket under a budget of 1000: %v", err)
	}
}

// A slow reader on plain TCP takes inline writes until the kernel's buffer is full, then a queue
// until it overflows, which closes the socket and drops the queue. What reaches the client is
// whole frames and then the Close, never a Close inside a frame's payload. The narrow case of a
// frame cut short inline and its rest still queued when the queue is dropped is driven directly
// in internal/engine/ws_test.go.
func TestASlowReaderClosedForItsQueueReceivesEveryFrameWholeAndThenTheClose(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	done := make(chan error, 1)
	addr, _ := serveTCP(t, func(app *bites.App) {
		app.WebSocket("/flood", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			go func() {
				for {
					if err := s.Send(wsOut{Text: big}); err != nil {
						done <- err
						return
					}
				}
			}()
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	w := handshake(t, c, "/flood")
	if w.status != 101 {
		t.Fatalf("status %d", w.status)
	}
	select {
	case <-done: // the queue overflowed: the socket is closing
	case <-time.After(10 * time.Second):
		t.Fatal("the flood never filled the queue")
	}
	var size, frames int
	for {
		r := w.try()
		if r.err != nil {
			t.Fatalf("the connection ended after %d whole frames without a Close: %v", frames, r.err)
		}
		if r.op == ws.OpClose {
			if len(r.p) < 2 || binary.BigEndian.Uint16(r.p) != 1008 {
				t.Fatalf("close payload %q after %d frames, want status 1008", r.p, frames)
			}
			break
		}
		if r.op != ws.OpText || r.p[0] != '{' || r.p[len(r.p)-1] != '}' || (size != 0 && len(r.p) != size) {
			t.Fatalf("frame %d is not a whole message: opcode %v, %d bytes, starts %q", frames, r.op, len(r.p), r.p[:min(len(r.p), 16)])
		}
		size = len(r.p)
		frames++
	}
	t.Logf("%d whole frames of %d bytes, then the close", frames, size)
}

// ownedRoom registers /rooms/{id} (Out wsOut) and /feed/{id} (Out feedOut) whose sockets are added to
// owners under the path id, and reports each OnClose.
type feedOut struct {
	N int `json:"n"`
}

func ownedRoutes(owners *bites.SocketOwners, closed chan bites.CloseInfo) func(*bites.App) {
	return func(app *bites.App) {
		app.WebSocket("/rooms/{id}", func(_ context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			if err := owners.Add(fmt.Sprint(in.Path.ID), s); err != nil {
				return bites.Inbox[wsMsg]{}, err
			}
			return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return nil }, OnClose: func(i bites.CloseInfo) { closed <- i }}, nil
		})
		app.WebSocket("/feed/{id}", func(_ context.Context, in wsIn, s *bites.Socket[feedOut]) (bites.Inbox[wsMsg], error) {
			if err := owners.Add(fmt.Sprint(in.Path.ID), s); err != nil {
				return bites.Inbox[wsMsg]{}, err
			}
			return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return nil }, OnClose: func(i bites.CloseInfo) { closed <- i }}, nil
		})
	}
}

func TestClosingAnOwnerClosesEverySocketOfThatOwnerWhateverItsRouteAndNoOthersAndLogsIt(t *testing.T) {
	var owners bites.SocketOwners
	closed := make(chan bites.CloseInfo, 8)
	srv, _, logs := startWS(t, ownedRoutes(&owners, closed))
	a1, a2, a3 := memWS(t, srv, "/rooms/1"), memWS(t, srv, "/feed/1"), memWS(t, srv, "/rooms/1")
	b := memWS(t, srv, "/rooms/2")
	if owners.Len("1") != 3 || owners.Len("2") != 1 {
		t.Fatalf("Len %d %d", owners.Len("1"), owners.Len("2"))
	}
	n, err := owners.Close("1", 4401, "signed out")
	if err != nil || n != 3 {
		t.Fatalf("Close returned %d, %v; want 3", n, err)
	}
	for _, w := range []*wsc{a1, a2, a3} {
		if got := w.expectClose(4401); got != "signed out" {
			t.Fatalf("reason %q", got)
		}
		w.closeWith(4401, "")
	}
	for range 3 {
		select {
		case i := <-closed:
			if i.Status != 4401 || i.Reason != "signed out" || i.Remote {
				t.Fatalf("OnClose saw %+v", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an OnClose did not run")
		}
	}
	for _, want := range []string{"closed the sockets of an owner", "owner=1", "closed=3", "status=4401"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the log lacks %q: %s", want, logs.String())
		}
	}
	b.text(`{"text":"still open"}`)
	if n, err := owners.Close("2", 1000, ""); err != nil || n != 1 {
		t.Fatalf("closing the other owner: %d, %v", n, err)
	}
}

func TestAnOwnerIsForgottenWhenItsLastSocketEndsWhetherByCloseByTheClientOrByAHandlerError(t *testing.T) {
	var owners bites.SocketOwners
	closed := make(chan bites.CloseInfo, 8)
	srv, _, _ := startWS(t, func(app *bites.App) {
		ownedRoutes(&owners, closed)(app)
		app.WebSocket("/refused/{id}", func(_ context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			owners.Add("9", s)
			return bites.Inbox[wsMsg]{}, bites.Forbidden("no")
		})
	})
	w := memWS(t, srv, "/rooms/1")
	if w2 := handshake(t, srv.Dial("203.0.113.9:4001"), "/refused/9"); w2.status != 403 {
		t.Fatalf("%d", w2.status)
	}
	if owners.Len("9") != 0 {
		t.Fatal("a socket whose handshake was refused stayed in the index")
	}
	w.closeWith(1000, "")
	w.expectClose(1000)
	<-closed
	waitFor(t, "the owner to be forgotten", func() bool { return owners.Len("1") == 0 })
	if n, err := owners.Close("1", 1000, ""); n != 0 || err != nil {
		t.Fatalf("closing a forgotten owner returned %d, %v", n, err)
	}
	w = memWS(t, srv, "/rooms/1")
	srv.Dial("203.0.113.9:4002").Close() // an unrelated connection ends
	w.c.Close()
	waitFor(t, "the owner to be forgotten after the client dropped", func() bool { return owners.Len("1") == 0 })
}

func TestAddingASocketUnderAnEmptyOwnerRefusesTheHandshakeAndNamesWhatToPass(t *testing.T) {
	var owners2 bites.SocketOwners
	srv2, _, _ := startWS(t, func(a *bites.App) {
		a.WebSocket("/x", func(_ context.Context, _ struct{}, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			if err := owners2.Add("", s); err != nil {
				return bites.Inbox[wsMsg]{}, err
			}
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	w := handshake(t, srv2.Dial("203.0.113.9:4000"), "/x")
	if w.status != 500 || owners2.Len("") != 0 {
		t.Fatalf("an empty owner answered %d, not a refusal", w.status)
	}
	if err := owners2.Add("", (*bites.Socket[wsOut])(nil)); err == nil || !strings.Contains(err.Error(), "empty owner") || !strings.Contains(err.Error(), "pass the id of the authenticated User") {
		t.Fatalf("err %v", err)
	}
}

func TestASocketHasOneOwnerAddingItAgainUnderTheSameOwnerIsFineAndUnderAnotherIsAnError(t *testing.T) {
	var owners bites.SocketOwners
	errs := make(chan [2]error, 1)
	srv, _, _ := startWS(t, func(a *bites.App) {
		a.WebSocket("/x", func(_ context.Context, _ struct{}, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			e1 := owners.Add("ana", s)
			e2 := owners.Add("ana", s)
			e3 := owners.Add("budi", s)
			errs <- [2]error{errors.Join(e1, e2), e3}
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	memWS(t, srv, "/x")
	e := <-errs
	if e[0] != nil || e[1] == nil || !strings.Contains(e[1].Error(), "already has a different owner") {
		t.Fatalf("%v", e)
	}
	if owners.Len("ana") != 1 || owners.Len("budi") != 0 {
		t.Fatalf("Len %d %d", owners.Len("ana"), owners.Len("budi"))
	}
}

func TestClosingAnOwnerWithAStatusAServerMayNotSendClosesNothingAndSaysSo(t *testing.T) {
	var owners bites.SocketOwners
	srv, _, _ := startWS(t, ownedRoutes(&owners, make(chan bites.CloseInfo, 1)))
	w := memWS(t, srv, "/rooms/1")
	for _, c := range []struct {
		status uint16
		reason string
	}{{1005, ""}, {1006, ""}, {2000, ""}, {1000, strings.Repeat("x", 124)}, {1000, "\xff"}} {
		if n, err := owners.Close("1", c.status, c.reason); n != 0 || err == nil {
			t.Fatalf("Close(%d, %.5q) returned %d, %v", c.status, c.reason, n, err)
		}
	}
	w.text(`{"text":"open"}`)
	if owners.Len("1") != 1 {
		t.Fatal("the socket left")
	}
}

func TestASecondCloseOfTheSameOwnerCountsNoSocketThatIsAlreadyClosing(t *testing.T) {
	var owners bites.SocketOwners
	srv, _, _ := startWS(t, ownedRoutes(&owners, make(chan bites.CloseInfo, 1)))
	memWS(t, srv, "/rooms/1")
	if n, _ := owners.Close("1", 4401, "x"); n != 1 {
		t.Fatalf("first %d", n)
	}
	if n, _ := owners.Close("1", 4401, "x"); n != 0 {
		t.Fatalf("a socket already closing was counted again: %d", n)
	}
}

func TestOnCloseOfAClosedOwnersSocketMayUseTheOwnersWithoutADeadlockAndConcurrentUseIsSafe(t *testing.T) {
	var owners bites.SocketOwners
	lens := make(chan int, 64)
	srv, _, _ := startWS(t, func(a *bites.App) {
		a.WebSocket("/rooms/{id}", func(_ context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			if err := owners.Add("u", s); err != nil {
				return bites.Inbox[wsMsg]{}, err
			}
			return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return nil }, OnClose: func(bites.CloseInfo) { lens <- owners.Len("u") }}, nil
		})
	})
	var conns []*wsc
	for range 8 {
		conns = append(conns, memWS(t, srv, "/rooms/1"))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			owners.Close("u", 4401, "x")
			owners.Len("u")
		}
	}()
	extra := memWS(t, srv, "/rooms/1")
	<-done
	owners.Close("u", 4401, "x")
	for _, w := range append(conns, extra) {
		w.expectClose(4401)
		w.closeWith(4401, "")
	}
	for range 9 {
		select {
		case <-lens:
		case <-time.After(5 * time.Second):
			t.Fatal("an OnClose blocked")
		}
	}
	waitFor(t, "the owner to be forgotten", func() bool { return owners.Len("u") == 0 })
}

// shutdownSpreadLog opens three sockets on an in-memory app, shuts it down with ctx and returns
// the app's log once Shutdown has said how it spread the Close 1001.
func shutdownSpreadLog(t *testing.T, ctx context.Context, opts ...bites.Option) string {
	t.Helper()
	e := newRoom()
	lb := &logBuf{}
	app := bites.New(append([]bites.Option{bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil)))}, opts...)...)
	app.WebSocket("/rooms/{id}", e.join)
	srv := bitetest.Start(t, app)
	for range 3 {
		memWS(t, srv, "/rooms/1")
	}
	go app.Shutdown(ctx)
	waitFor(t, "Shutdown to log its spread", func() bool { return strings.Contains(lb.String(), "Close 1001 spread") })
	return lb.String()
}

func TestWithGoAwaySpreadSetsTheCeilingOfTheWindowShutdownLogs(t *testing.T) {
	out := shutdownSpreadLog(t, context.Background(), bites.WithGoAwaySpread(time.Millisecond))
	for _, want := range []string{"sockets=3", "window=1ms", "asked=1ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	out = shutdownSpreadLog(t, context.Background(), bites.WithGoAwaySpread(time.Hour))
	if !strings.Contains(out, "window=3ms") {
		t.Fatalf("a wider ceiling must not widen a window of 1 ms a socket:\n%s", out)
	}
}

func TestWithoutGoAwaySpreadTheWindowIsOneMillisecondASocketAsBefore(t *testing.T) {
	out := shutdownSpreadLog(t, context.Background())
	if !strings.Contains(out, "window=3ms") || !strings.Contains(out, "asked=3ms") || strings.Contains(out, "cut short") {
		t.Fatalf("the default window changed:\n%s", out)
	}
}

func TestShutdownSaysWhenHalfOfItsContextCutTheGoAwayWindowShorterThanAskedFor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Millisecond)
	defer cancel()
	out := shutdownSpreadLog(t, ctx)
	for _, want := range []string{"level=WARN", "cut short by half of what is left of Shutdown's context", "asked=3ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
}

func TestTheStartUpSettingsMarkTheGoAwaySpreadDefaultOrCustom(t *testing.T) {
	for _, tc := range []struct {
		opts          []bites.Option
		value, source string
	}{
		{nil, "3s", "default"},
		{[]bites.Option{bites.WithGoAwaySpread(10 * time.Second)}, "10s", "custom"},
	} {
		lb := &logBuf{}
		app := bites.New(append(tc.opts, bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))))...)
		app.WebSocket("/rooms/{id}", newRoom().join)
		bites.StartForTest(app)
		want := `setting="websocket shutdown close spread, at most" value=` + tc.value + " source=" + tc.source
		if !strings.Contains(lb.String(), want) {
			t.Errorf("the settings lack %s:\n%s", want, lb.String())
		}
	}
}

// twoInstances makes two apps with their own SocketOwners over one bus, each with one open socket
// of owner "1" and one of owner "2" on the route /rooms/{id}.
func twoInstances(t *testing.T, bus *bitetest.Broker, oa, ob bites.SocketOwnersOptions) (a, b *bites.SocketOwners, sa, sb *bitetest.Server) {
	t.Helper()
	a = bites.NewSocketOwners("signout", bus, oa)
	b = bites.NewSocketOwners("signout", bus.Peer(), ob)
	closed := make(chan bites.CloseInfo, 16)
	sa, _, _ = startWS(t, ownedRoutes(a, closed))
	sb, _, _ = startWS(t, ownedRoutes(b, closed))
	return a, b, sa, sb
}

func TestSignOutOnOneInstanceClosesTheOwnersSocketsOnTheOtherAndTheCountIsThisInstances(t *testing.T) {
	bus := bitetest.NewBroker()
	a, b, sa, sb := twoInstances(t, bus, bites.SocketOwnersOptions{}, bites.SocketOwnersOptions{})
	a1, a2 := memWS(t, sa, "/rooms/1"), memWS(t, sa, "/rooms/2")
	b1, b1b, b2 := memWS(t, sb, "/rooms/1"), memWS(t, sb, "/rooms/1"), memWS(t, sb, "/rooms/2")
	n, err := a.Close("1", 4401, "signed out")
	if err != nil || n != 1 {
		t.Fatalf("Close returned %d, %v; want this instance's 1, not the 3 of both", n, err)
	}
	for _, w := range []*wsc{a1, b1, b1b} {
		if got := w.expectClose(4401); got != "signed out" {
			t.Fatalf("reason %q", got)
		}
	}
	if a.Len("2") != 1 || b.Len("2") != 1 {
		t.Fatal("a socket of another owner was closed")
	}
	a2.text(`{"text":"still open"}`)
	b2.text(`{"text":"still open"}`)
}

func TestASocketOwnersListensWhileItHoldsASocketAndOnlyThen(t *testing.T) {
	bus := bitetest.NewBroker()
	o := bites.NewSocketOwners("signout", bus, bites.SocketOwnersOptions{})
	srv, _, _ := startWS(t, ownedRoutes(o, make(chan bites.CloseInfo, 4)))
	if bus.Subscriptions("signout") != 0 {
		t.Fatal("a SocketOwners with no socket listens")
	}
	w := memWS(t, srv, "/rooms/1")
	if bus.Subscriptions("signout") != 1 {
		t.Fatalf("%d subscriptions, want 1", bus.Subscriptions("signout"))
	}
	w.closeWith(1000, "")
	w.expectClose(1000)
	waitFor(t, "the last socket to leave and the listener to stop", func() bool { return bus.Subscriptions("signout") == 0 })
}

func TestAFailedPublishOfASignOutStillClosesThisInstancesSocketsAndReturnsTheCountAndTheError(t *testing.T) {
	bus := bitetest.NewBroker()
	a, _, sa, _ := twoInstances(t, bus, bites.SocketOwnersOptions{}, bites.SocketOwnersOptions{})
	w := memWS(t, sa, "/rooms/1")
	boom := errors.New("connection refused")
	bus.Fail(boom)
	n, err := a.Close("1", 4401, "signed out")
	if n != 1 || !errors.Is(err, boom) || !strings.Contains(err.Error(), `SocketOwners "signout"`) || !strings.Contains(err.Error(), "other instances") {
		t.Fatalf("Close returned %d, %v; want 1 and an error wrapping the broker's, saying the others were not told", n, err)
	}
	w.expectClose(4401)
}

func TestASignOutWithAStatusAServerMayNotSendIsRefusedWithoutPublishing(t *testing.T) {
	bus := bitetest.NewBroker()
	a, _, _, sb := twoInstances(t, bus, bites.SocketOwnersOptions{}, bites.SocketOwnersOptions{})
	w := memWS(t, sb, "/rooms/1")
	if n, err := a.Close("1", 1005, ""); n != 0 || err == nil {
		t.Fatalf("Close returned %d, %v; want an error", n, err)
	}
	w.text(`{"text":"still open"}`)
}

func TestAGapClosesEverySocketOfASocketOwnersWith1012ByDefault(t *testing.T) {
	bus := bitetest.NewBroker()
	_, _, sa, sb := twoInstances(t, bus, bites.SocketOwnersOptions{}, bites.SocketOwnersOptions{})
	a1, a2 := memWS(t, sa, "/rooms/1"), memWS(t, sa, "/rooms/2")
	b1 := memWS(t, sb, "/rooms/1")
	bus.Gap()
	for _, w := range []*wsc{a1, a2, b1} {
		w.expectClose(1012)
	}
}

func TestOnGapReplacesTheDefaultCloseOfEverySocket(t *testing.T) {
	bus := bitetest.NewBroker()
	var gaps atomic.Int32
	_, _, sa, _ := twoInstances(t, bus, bites.SocketOwnersOptions{OnGap: func() { gaps.Add(1) }}, bites.SocketOwnersOptions{})
	w := memWS(t, sa, "/rooms/1")
	bus.Gap()
	if gaps.Load() != 1 {
		t.Fatalf("OnGap ran %d times, want 1", gaps.Load())
	}
	w.text(`{"text":"still open"}`)
}

func TestAPanicInASocketOwnersOnGapIsRecoveredAndLogged(t *testing.T) {
	bus := bitetest.NewBroker()
	var logs logBuf
	o := bites.NewSocketOwners("signout", bus, bites.SocketOwnersOptions{OnGap: func() { panic("boom") }, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	srv, _, _ := startWS(t, ownedRoutes(o, make(chan bites.CloseInfo, 4)))
	w := memWS(t, srv, "/rooms/1")
	bus.Gap()
	if !strings.Contains(logs.String(), "SocketOwnersOptions.OnGap panicked") {
		t.Fatalf("the panic was not logged: %s", logs.String())
	}
	w.text(`{"text":"still open"}`)
}

func TestTheZeroSocketOwnersStillClosesOnItsOwnWithNoBroker(t *testing.T) {
	var o bites.SocketOwners
	srv, _, _ := startWS(t, ownedRoutes(&o, make(chan bites.CloseInfo, 4)))
	w := memWS(t, srv, "/rooms/1")
	if n, err := o.Close("1", 4401, "signed out"); n != 1 || err != nil {
		t.Fatalf("Close returned %d, %v", n, err)
	}
	w.expectClose(4401)
}

func TestWhatAnIdleSocketOwnersCostsIsOnePointerAndNoBrokerConnection(t *testing.T) {
	t.Logf("sizeof SocketOwners %d bytes", unsafe.Sizeof(bites.SocketOwners{}))
	bus := bitetest.NewBroker()
	_ = bites.NewSocketOwners("signout", bus, bites.SocketOwnersOptions{})
	if bus.Subscriptions("signout") != 0 {
		t.Fatal("a SocketOwners with no socket holds a subscription")
	}
}
