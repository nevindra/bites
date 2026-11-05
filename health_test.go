package bites_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func healthApp(t *testing.T, register func(*bites.App), opts ...bites.Option) (*bites.App, *bitetest.Server, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(append([]bites.Option{bites.WithClock(clk), bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"})}, opts...)...)
	if register != nil {
		register(app)
	}
	app.Get("/users/{id}", getUser)
	srv := bitetest.Start(t, app)
	if r := srv.Do(req("GET", "/livez")); r.Status != 200 { // the app is serving: Shutdown has a server to stop
		t.Fatalf("live: %d", r.Status)
	}
	// The connection's own deadlines are on the fake clock too: wait until they are gone, so the
	// only timer a test sees after Shutdown is the delay's.
	waitFor(t, "the probe's connection to end", func() bool { return clk.Pending() == 0 })
	return app, srv, clk
}

func TestLiveAndReadyAre200OkAsTextWhileTheAppServes(t *testing.T) {
	_, srv, _ := healthApp(t, nil)
	for _, p := range []string{"/livez", "/readyz"} {
		r := srv.Do(req("GET", p))
		if r.Status != 200 || r.Text() != "ok" || !strings.HasPrefix(r.Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q %v", p, r.Status, r.Body, r.Header)
		}
	}
	if r := srv.Do(req("HEAD", "/readyz")); r.Status != 200 {
		t.Fatalf("HEAD: %d", r.Status)
	}
}

func TestAHealthPathAnswersAnotherMethodWith405(t *testing.T) {
	_, srv, _ := healthApp(t, nil)
	r := srv.Do(req("POST", "/livez"))
	if r.Status != 405 || r.Get("Allow") != "GET, HEAD" || r.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestReadyIs503ProblemJSONWhileSetReadyFalseHoldsAndLiveStays200(t *testing.T) {
	app, srv, _ := healthApp(t, nil)
	app.SetReady(false)
	r := srv.Do(req("GET", "/readyz"))
	if r.Status != 503 || r.Get("Content-Type") != "application/problem+json" || !strings.Contains(r.Text(), "the app is not ready") {
		t.Fatalf("%d %v %s", r.Status, r.Header, r.Body)
	}
	if r := srv.Do(req("GET", "/livez")); r.Status != 200 {
		t.Fatalf("live: %d", r.Status)
	}
	app.SetReady(true)
	if r := srv.Do(req("GET", "/readyz")); r.Status != 200 {
		t.Fatalf("ready again: %d", r.Status)
	}
}

func TestHealthIsAnsweredBeforeAppMiddlewareAndIsNotObserved(t *testing.T) {
	var mu sync.Mutex
	var log []string
	rec := newRecorder("r", &mu, &log)
	_, srv, _ := healthApp(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, _ bites.Request, _ bites.Next) (bites.Response, error) {
			return bites.Problem(401, "a token is required"), nil
		})
	}, bites.WithObserver(rec))
	if r := srv.Do(req("GET", "/readyz")); r.Status != 200 {
		t.Fatalf("an app middleware answered the probe: %d", r.Status)
	}
	rec.none(t)
	if r := srv.Do(req("GET", "/users/1")); r.Status != 401 {
		t.Fatalf("the middleware does not run on routes: %d", r.Status)
	}
	rec.outcome(t)
}

func TestHealthPathsAreNotInTheAPIDocument(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}))
	app.OpenAPI("/openapi.json", bites.Info{Title: "t", Version: "1"})
	app.Get("/users/{id}", getUser)
	srv := bitetest.Start(t, app)
	doc := srv.Do(req("GET", "/openapi.json")).Text()
	if !strings.Contains(doc, "/users/{id}") || strings.Contains(doc, "livez") || strings.Contains(doc, "readyz") {
		t.Fatalf("%s", doc)
	}
}

func TestReadyFailsFromTheMomentShutdownStartsAndTheDelayKeepsServingUntilItEnds(t *testing.T) {
	app, srv, clk := healthApp(t, nil, bites.WithShutdownDelay(5*time.Second))
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	waitFor(t, "the delay to be armed", func() bool { return clk.Pending() > 0 })
	// During the delay the listener is open: a new connection is accepted and served.
	if r := srv.Do(req("GET", "/readyz")); r.Status != 503 || !strings.Contains(r.Text(), "the app is shutting down") {
		t.Fatalf("ready during the delay: %d %s", r.Status, r.Body)
	}
	if r := srv.Do(req("GET", "/livez")); r.Status != 200 {
		t.Fatalf("live during the delay: %d", r.Status)
	}
	if r := srv.Do(req("GET", "/users/1")); r.Status != 200 {
		t.Fatalf("a route during the delay: %d", r.Status)
	}
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned before the delay: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	clk.Advance(5 * time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the delay")
	}
}

func TestTheShutdownDelayIsBoundedByHalfOfWhatIsLeftOfShutdownsContext(t *testing.T) {
	app, _, clk := healthApp(t, nil, bites.WithShutdownDelay(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go app.Shutdown(ctx)
	waitFor(t, "the delay to be armed", func() bool { return clk.Pending() > 0 })
	at, _ := clk.Next()
	if d := at.Sub(clk.Now()); d > 2*time.Second || d <= time.Second {
		t.Fatalf("the delay is %v; want at most half of the 4 s left", d)
	}
}

func TestASecondShutdownDuringTheDelayWaitsForTheSameDelay(t *testing.T) {
	app, _, clk := healthApp(t, nil, bites.WithShutdownDelay(5*time.Second))
	done := make(chan error, 2)
	go func() { done <- app.Shutdown(context.Background()) }()
	waitFor(t, "the delay to be armed", func() bool { return clk.Pending() > 0 })
	clk.Advance(3 * time.Second)
	go func() { done <- app.Shutdown(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	if n := clk.Pending(); n != 1 {
		t.Fatalf("%d timers pending; the second Shutdown armed its own delay", n)
	}
	clk.Advance(2 * time.Second)
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a Shutdown did not return when the delay ended")
		}
	}
}

func TestCloseDuringTheShutdownDelayEndsIt(t *testing.T) {
	app, _, clk := healthApp(t, nil, bites.WithShutdownDelay(5*time.Second))
	done := make(chan error, 1)
	go func() { done <- app.Shutdown(context.Background()) }()
	waitFor(t, "the delay to be armed", func() bool { return clk.Pending() > 0 })
	app.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown kept waiting after Close")
	}
}
