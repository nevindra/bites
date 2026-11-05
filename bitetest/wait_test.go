package bitetest_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// endCounter is an Observer that counts the requests whose End has run, after holding each one
// on gate when gate is set.
type endCounter struct {
	ended atomic.Int64
	gate  chan struct{}
	last  atomic.Value // bites.Outcome
}

func (e *endCounter) Start(ctx context.Context, _ bites.Request) context.Context { return ctx }
func (e *endCounter) End(_ context.Context, _ bites.Request, o bites.Outcome) {
	if e.gate != nil {
		<-e.gate
	}
	e.last.Store(o)
	e.ended.Add(1)
}

func (e *endCounter) outcome() bites.Outcome {
	o, _ := e.last.Load().(bites.Outcome)
	return o
}

type none struct{}

func startCounting(t *testing.T, e *endCounter, register func(*bites.App)) *bitetest.Server {
	t.Helper()
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithObserver(e))
	register(app)
	return bitetest.Start(t, app)
}

func getX(app *bites.App) {
	app.Get("/x", func(context.Context, none) (none, error) { return none{}, nil })
}

func TestWaitEndedReturnsOnlyAfterTheEndOfEveryRequestAnswered(t *testing.T) {
	e := &endCounter{}
	srv := startCounting(t, e, getX)
	for i := 1; i <= 200; i++ {
		srv.Do(bitetest.Request{Method: "GET", Target: "/x"})
		srv.WaitEnded()
		if got := e.ended.Load(); got != int64(i) {
			t.Fatalf("after request %d, %d Ends have run", i, got)
		}
	}
}

func TestWaitEndedWaitsForRequestsOnSeveralConnectionsAtOnce(t *testing.T) {
	e := &endCounter{}
	srv := startCounting(t, e, getX)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); srv.Do(bitetest.Request{Method: "GET", Target: "/x"}) }()
	}
	wg.Wait()
	srv.WaitEnded()
	if got := e.ended.Load(); got != 20 {
		t.Fatalf("%d Ends have run, want 20", got)
	}
}

func TestWaitEndedDoesNotReturnWhileAnObserversEndIsStillRunning(t *testing.T) {
	e := &endCounter{gate: make(chan struct{})}
	srv := startCounting(t, e, getX)
	srv.Do(bitetest.Request{Method: "GET", Target: "/x"})
	returned := make(chan struct{})
	go func() { srv.WaitEnded(); close(returned) }()
	select {
	case <-returned:
		t.Fatal("WaitEnded returned with an End still held")
	case <-time.After(50 * time.Millisecond):
	}
	close(e.gate)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitEnded did not return after the End finished")
	}
	if e.ended.Load() != 1 {
		t.Fatal("End had not run")
	}
}

func TestWaitEndedWaitsForAStreamsLastByteAndSeesItsSize(t *testing.T) {
	e := &endCounter{}
	srv := startCounting(t, e, func(app *bites.App) {
		app.Get("/s", func(context.Context, none) (bites.Stream, error) {
			return bites.Stream{Func: func(_ context.Context, w *bites.StreamWriter) error {
				w.Write([]byte("hello"))
				return nil
			}}, nil
		})
	})
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/s"}); r.Text() != "hello" {
		t.Fatalf("body %q", r.Text())
	}
	srv.WaitEnded()
	if o := e.outcome(); !o.Streamed || o.Written != 5 {
		t.Fatalf("outcome %+v", o)
	}
}

func TestWaitEndedSeesTheEndOfAWebSocketUpgradeWithoutClosingTheSocket(t *testing.T) {
	e := &endCounter{}
	srv := startCounting(t, e, func(app *bites.App) {
		app.WebSocket("/ws", func(_ context.Context, _ none, s *bites.Socket[out]) (bites.Inbox[msg], error) {
			return bites.Inbox[msg]{OnMessage: func(_ context.Context, m msg) error { return s.Send(out{Text: m.Text}) }}, nil
		})
	})
	w := srv.DialWS(bitetest.WSRequest{Target: "/ws"})
	srv.WaitEnded()
	if o := e.outcome(); e.ended.Load() != 1 || !o.Upgraded {
		t.Fatalf("%d Ends, outcome %+v", e.ended.Load(), o)
	}
	w.SendText(`{"text":"still open"}`)
	if got := w.ReadText(); got != `{"text":"still open"}` {
		t.Fatalf("got %q", got)
	}
}

func TestWaitEndedSeesTheEndOfARequestWhoseClientLeftMidHandler(t *testing.T) {
	e := &endCounter{}
	entered := make(chan struct{})
	srv := startCounting(t, e, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ none) (none, error) {
			close(entered)
			<-ctx.Done()
			return none{}, nil
		})
	})
	c := srv.Dial("203.0.113.9:4000")
	if _, err := c.Write([]byte("GET /slow HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	<-entered
	c.Close()
	srv.WaitEnded()
	if o := e.outcome(); e.ended.Load() != 1 || !o.Abandoned {
		t.Fatalf("%d Ends, outcome %+v", e.ended.Load(), o)
	}
}

func TestWaitEndedReturnsAtOnceWithNothingServedAndWithAnIdleConnection(t *testing.T) {
	srv := startCounting(t, &endCounter{}, getX)
	srv.WaitEnded()
	idle := srv.Dial("203.0.113.9:4000") // no request sent: idle, not in flight
	defer idle.Close()
	srv.WaitEnded()
}
