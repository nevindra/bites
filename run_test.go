package bites_test

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/memconn"
)

// runOver serves app with Run over an in-memory listener and returns the listener and Run's
// result, so a test sends requests and cancels ctx without a port (ADR 0027).
func runOver(t *testing.T, app *bites.App, ctx context.Context, grace time.Duration) (*memconn.Listener, <-chan error) {
	t.Helper()
	ln := memconn.Listen(app.Clock())
	ran := make(chan error, 1)
	go func() { ran <- app.Run(ctx, grace, func() error { return app.Serve(ln) }) }()
	t.Cleanup(func() { app.Close() })
	return ln, ran
}

// sendOver writes a GET for target on a new connection to ln and returns the connection's reader.
func sendOver(t *testing.T, ln *memconn.Listener, target string) *bufio.Reader {
	t.Helper()
	var c net.Conn
	var err error
	for range 200 { // the serve goroutine may not have reached Accept yet
		if c, err = ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"}); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err := c.Write([]byte("GET " + target + " HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	return bufio.NewReader(c)
}

func runReturns(t *testing.T, ran <-chan error) error {
	t.Helper()
	select {
	case err := <-ran:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run has not returned in 5 s")
		return nil
	}
}

func runHasNotReturned(t *testing.T, ran <-chan error) {
	t.Helper()
	select {
	case err := <-ran:
		t.Fatalf("Run returned %v too early", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunFinishesTheRequestInFlightWhenItsContextEndsAndThenReturnsNil(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/slow", func(context.Context, Empty) (struct{}, error) { close(entered); <-release; return struct{}{}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	ln, ran := runOver(t, app, ctx, 5*time.Second)
	br := sendOver(t, ln, "/slow")
	<-entered
	cancel()
	runHasNotReturned(t, ran)
	close(release)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !resp.Close {
		t.Fatalf("status %d, Connection: close %v", resp.StatusCode, resp.Close)
	}
	if err := runReturns(t, ran); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

func TestRunReturnsOnlyAfterTheServicesHaveStopped(t *testing.T) {
	var stopped atomic.Bool
	started, stopping, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Service(bites.Service{Name: "db", Start: func(context.Context) error { close(started); return nil }, Stop: func(context.Context) error {
		close(stopping)
		<-release
		stopped.Store(true)
		return nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	_, ran := runOver(t, app, ctx, 5*time.Second)
	<-started
	cancel()
	<-stopping
	runHasNotReturned(t, ran)
	close(release)
	if err := runReturns(t, ran); err != nil || !stopped.Load() {
		t.Fatalf("Run returned %v, the service stopped %v", err, stopped.Load())
	}
}

func TestRunReturnsTheShutdownErrorWhenTheGracePeriodRunsOut(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/stuck", func(context.Context, Empty) (struct{}, error) { close(entered); <-release; return struct{}{}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	ln, ran := runOver(t, app, ctx, 50*time.Millisecond)
	sendOver(t, ln, "/stuck")
	<-entered
	cancel()
	err := runReturns(t, ran)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "bites: Run: shutting down within the grace period of 50ms") {
		t.Fatalf("Run returned %v, want the grace period's DeadlineExceeded", err)
	}
}

func TestRunReturnsTheErrorOfAListenThatFailsWithoutWaitingForTheContext(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	app := bites.New()
	ran := make(chan error, 1)
	go func() {
		ran <- app.Run(context.Background(), 5*time.Second, func() error { return app.Listen(busy.Addr().String()) })
	}()
	err = runReturns(t, ran)
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "listen" {
		t.Fatalf("Run returned %v, want the listen error", err)
	}
}

func TestRunStopsTheOtherListenersWhenOneFailsAndReturnsItsError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	app := bites.New()
	ln := memconn.Listen(app.Clock())
	served := make(chan error, 1)
	ran := make(chan error, 1)
	go func() {
		ran <- app.Run(context.Background(), 5*time.Second,
			func() error { err := app.Serve(ln); served <- err; return err },
			func() error { return app.Listen(busy.Addr().String()) })
	}()
	err = runReturns(t, ran)
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "listen" {
		t.Fatalf("Run returned %v, want the listen error", err)
	}
	if err := <-served; !errors.Is(err, bites.ErrServerClosed) {
		t.Fatalf("the other listener returned %v, want ErrServerClosed", err)
	}
}

func TestRunWithAContextAlreadyDoneStopsAtOnceAndReturnsNil(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ran := runOver(t, app, ctx, 5*time.Second)
	if err := runReturns(t, ran); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

func TestRunReturnsNilOnceAShutdownCalledElsewhereHasReturned(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/slow", func(context.Context, Empty) (struct{}, error) { close(entered); <-release; return struct{}{}, nil })
	ln, ran := runOver(t, app, context.Background(), 5*time.Second)
	sendOver(t, ln, "/slow")
	<-entered
	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown(context.Background()) }()
	runHasNotReturned(t, ran)
	close(release)
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if err := runReturns(t, ran); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

func TestRunReturnsAnErrorNamingAServeFunctionThatReturnedNil(t *testing.T) {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	ran := make(chan error, 1)
	go func() {
		ran <- app.Run(context.Background(), time.Second, func() error { return nil })
	}()
	err := runReturns(t, ran)
	if err == nil || !strings.Contains(err.Error(), "bites: Run: serve function 1 returned nil while the app was serving") {
		t.Fatalf("Run returned %v", err)
	}
}

func TestListenReturnsOnlyOnceShutdownHasReturnedSoMainCannotCutTheDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/slow", func(context.Context, Empty) (struct{}, error) { close(entered); <-release; return struct{}{}, nil })
	ln := memconn.Listen(app.Clock())
	served := make(chan error, 1)
	go func() { served <- app.Serve(ln) }()
	sendOver(t, ln, "/slow")
	<-entered
	shut := make(chan error, 1)
	go func() { shut <- app.Shutdown(context.Background()) }()
	select {
	case err := <-served:
		t.Fatalf("Serve returned %v while a request was draining", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-served; !errors.Is(err, bites.ErrServerClosed) {
		t.Fatalf("Serve returned %v", err)
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
}

func TestRunReturnsAnErrorNamingAServeFunctionThatDoesNotServeThisApp(t *testing.T) {
	defer bites.SetRunServeWait(20 * time.Millisecond)()
	app, other := bites.New(bites.WithClock(bitetest.NewClock())), bites.New(bites.WithClock(bitetest.NewClock()))
	ln, otherLn := memconn.Listen(app.Clock()), memconn.Listen(other.Clock())
	defer other.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() {
		ran <- app.Run(ctx, time.Second, func() error { return app.Serve(ln) }, func() error { return other.Serve(otherLn) })
	}()
	cancel()
	err := runReturns(t, ran)
	if err == nil || !strings.Contains(err.Error(), "bites: Run: serve function 2 has not returned 20ms after the app stopped, so it does not serve this app") {
		t.Fatalf("Run returned %v", err)
	}
}
