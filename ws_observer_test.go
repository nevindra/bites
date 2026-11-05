package bites_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// sockRec is an Observer and a SocketObserver that records what it is told (ADR 0063).
type sockRec struct {
	mu     sync.Mutex
	events []string
	closes []bites.CloseInfo
	panics bool
}

func (r *sockRec) Start(ctx context.Context, _ bites.Request) context.Context { return ctx }
func (r *sockRec) End(context.Context, bites.Request, bites.Outcome)          {}

func (r *sockRec) note(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *sockRec) SocketOpened(route string) {
	r.note("opened " + route)
	if r.panics {
		panic("observer bug")
	}
}

func (r *sockRec) SocketClosed(route string, info bites.CloseInfo) {
	r.mu.Lock()
	r.closes = append(r.closes, info)
	r.mu.Unlock()
	r.note("closed " + route + " " + info.Cause.String())
}

func (r *sockRec) SocketPongMissed(route string) { r.note("pong missed " + route) }

func (r *sockRec) got() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.events, "|")
}

func (r *sockRec) waitFor(t *testing.T, want string) {
	t.Helper()
	waitFor(t, "the events "+want+", have "+r.got(), func() bool { return r.got() == want })
}

func TestAnObserverIsToldWhenASocketOpensAndWhyItClosedAfterOnCloseHasRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		do    func(t *testing.T, w *wsc, e *room)
		cause bites.CloseCause
		last  string
	}{
		{"the client closes", func(t *testing.T, w *wsc, e *room) { w.closeWith(1000, "bye") }, bites.CauseClient, "closed /rooms/{id} client"},
		{"the connection is cut", func(t *testing.T, w *wsc, e *room) { w.c.Close() }, bites.CauseLost, "closed /rooms/{id} lost"},
		{"the application closes with Socket.Close", func(t *testing.T, w *wsc, e *room) {
			if err := e.sock(t).Close(4001, "kicked"); err != nil {
				t.Fatal(err)
			}
			w.expectClose(4001)
			w.closeWith(4001, "")
		}, bites.CauseServer, "closed /rooms/{id} server"},
		{"the client sends a binary frame to a text route", func(t *testing.T, w *wsc, e *room) {
			w.write(true, 2, []byte{1})
			w.expectClose(1003)
			w.closeWith(1003, "")
		}, bites.CauseClientFault, "closed /rooms/{id} client_fault"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &sockRec{}
			e := newRoom()
			srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) }, bites.WithObserver(rec))
			w := memWS(t, srv, "/rooms/1")
			rec.waitFor(t, "opened /rooms/{id}")
			tc.do(t, w, e)
			info := e.waitClosed(t)
			if info.Cause != tc.cause {
				t.Errorf("OnClose saw the cause %v, want %v", info.Cause, tc.cause)
			}
			rec.waitFor(t, "opened /rooms/{id}|"+tc.last)
		})
	}
}

func TestAnObserverIsToldWhyASocketOnAHandlerErrorOrASlowReaderOrAShutdownClosed(t *testing.T) {
	t.Run("OnMessage returns an error", func(t *testing.T) {
		rec := &sockRec{}
		srv, _, _ := startWS(t, func(app *bites.App) {
			app.WebSocket("/rooms/{id}", func(_ context.Context, _ wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
				return bites.Inbox[wsMsg]{OnMessage: func(context.Context, wsMsg) error { return errors.New("boom") }}, nil
			})
		}, bites.WithObserver(rec))
		w := memWS(t, srv, "/rooms/1")
		w.text(`{"text":"x"}`)
		w.expectClose(1011)
		w.closeWith(1011, "")
		rec.waitFor(t, "opened /rooms/{id}|closed /rooms/{id} handler_error")
	})
	t.Run("a reader a megabyte behind", func(t *testing.T) {
		rec := &sockRec{}
		e := newRoom()
		srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) }, bites.WithObserver(rec))
		w := memWS(t, srv, "/rooms/1")
		s := e.sock(t)
		big := wsOut{Text: strings.Repeat("x", 100<<10)}
		var err error
		for i := 0; err == nil && i < 100; i++ {
			err = s.Send(big)
		}
		if !errors.Is(err, bites.ErrSendQueueFull) {
			t.Fatal(err)
		}
		for {
			if op, _ := w.next(); op == 8 {
				break
			}
		}
		w.closeWith(1008, "")
		rec.waitFor(t, "opened /rooms/{id}|closed /rooms/{id} queue_full")
	})
	t.Run("Shutdown", func(t *testing.T) {
		rec := &sockRec{}
		e := newRoom()
		addr, app := serveTCP(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) }, bites.WithObserver(rec))
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		w := handshake(t, c, "/rooms/1")
		e.sock(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- app.Shutdown(ctx) }()
		w.expectClose(1001)
		w.closeWith(1001, "")
		w.expectEOF()
		<-done
		rec.waitFor(t, "opened /rooms/{id}|closed /rooms/{id} shutdown")
	})
}

func TestAnObserverIsToldOfEachMissedPongAndThenOfThePongTimeout(t *testing.T) {
	rec := &sockRec{}
	e := newRoom()
	srv, clk, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) }, bites.WithObserver(rec))
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1", NoAutoPong: true})
	for range 2 {
		clk.Advance(30 * time.Second)
		w.ReadPing()
	}
	clk.Advance(30 * time.Second)
	w.ExpectClose(1008)
	w.Close(1008, "")
	rec.waitFor(t, "opened /rooms/{id}|pong missed /rooms/{id}|pong missed /rooms/{id}|closed /rooms/{id} pong_timeout")
}

func TestAPanicInASocketObserverIsLoggedAndTheSocketAndTheOthersAreUnaffected(t *testing.T) {
	bad, good := &sockRec{panics: true}, &sockRec{}
	e := newRoom()
	srv, _, lb := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) }, bites.WithObserver(bad, good))
	w := memWS(t, srv, "/rooms/1")
	w.text(`{"text":"hi"}`)
	w.expectText(`{"text":"hi"}`)
	w.closeWith(1000, "")
	good.waitFor(t, "opened /rooms/{id}|closed /rooms/{id} client")
	if !strings.Contains(lb.String(), "panic in a SocketObserver") || !strings.Contains(lb.String(), "SocketOpened") {
		t.Errorf("the panic was not logged: %s", lb.String())
	}
}

func TestAnAppWhoseObserversWatchNoSocketsStillServesSocketsAndHasNothingToCall(t *testing.T) {
	e := newRoom()
	srv, _, _ := startWS(t, func(app *bites.App) { app.WebSocket("/rooms/{id}", e.join) },
		bites.WithObserver(onlyObserver{}))
	w := memWS(t, srv, "/rooms/1")
	w.closeWith(1000, "")
	if info := e.waitClosed(t); info.Cause != bites.CauseClient {
		t.Fatalf("%+v", info)
	}
}

type onlyObserver struct{}

func (onlyObserver) Start(ctx context.Context, _ bites.Request) context.Context { return ctx }
func (onlyObserver) End(context.Context, bites.Request, bites.Outcome)          {}

func TestMetricsCountOpenSocketsAndClosesByReasonAndMissedPongsPerRoute(t *testing.T) {
	m := bites.NewMetrics()
	clk := bitetest.NewClock()
	e := newRoom()
	app := bites.New(bites.WithClock(clk), bites.WithObserver(m), bites.WithLogger(slog.New(slog.NewTextHandler(&logBuf{}, nil))))
	app.ServeMetrics("/metrics", m)
	app.WebSocket("/rooms/{id}", e.join)
	srv := bitetest.Start(t, app)

	a := memWS(t, srv, "/rooms/1")
	b := srv.DialWS(bitetest.WSRequest{Target: "/rooms/2", NoAutoPong: true})
	text := scrape(t, srv, `bites_websocket_connections_open{route="/rooms/{id}"} 2`)
	for _, want := range []string{
		"# TYPE bites_websocket_connections_open gauge\n",
		`bites_websocket_connections_opened_total{route="/rooms/{id}"} 2` + "\n",
		`bites_websocket_connections_closed_total{route="/rooms/{id}",reason="client"} 0` + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}

	a.closeWith(1000, "")
	e.waitClosed(t)
	clk.Advance(30 * time.Second)
	b.ReadPing()
	clk.Advance(30 * time.Second)
	b.ReadPing()
	clk.Advance(30 * time.Second)
	b.ExpectClose(1008)
	b.Close(1008, "")
	text = scrape(t, srv, `bites_websocket_connections_open{route="/rooms/{id}"} 0`)
	for _, want := range []string{
		`bites_websocket_connections_closed_total{route="/rooms/{id}",reason="client"} 1` + "\n",
		`bites_websocket_connections_closed_total{route="/rooms/{id}",reason="pong_timeout"} 1` + "\n",
		`bites_websocket_connections_closed_total{route="/rooms/{id}",reason="queue_full"} 0` + "\n",
		`bites_websocket_pongs_missed_total{route="/rooms/{id}"} 2` + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}

func TestMetricsOfAnAppWithNoWebSocketRouteHaveNoSocketSamples(t *testing.T) {
	m := bites.NewMetrics()
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) { app.Get("/users/{id}", getUser) })
	text := scrape(t, srv, "bites_websocket_connections_open")
	if strings.Contains(text, "bites_websocket_connections_open{") {
		t.Errorf("a sample without a socket:\n%s", text)
	}
}

func TestMetricsPastMaxSeriesCountTheSocketsOfANewRouteAsDropped(t *testing.T) {
	m := bites.NewMetrics(bites.MetricsOptions{MaxSeries: 1})
	m.SocketOpened("/a")
	m.SocketOpened("/b")
	var sb strings.Builder
	m.WriteTo(&sb)
	if !strings.Contains(sb.String(), "bites_metrics_dropped_total 1\n") || strings.Contains(sb.String(), `route="/b"`) {
		t.Errorf("%s", sb.String())
	}
}
