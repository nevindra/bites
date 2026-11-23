package bitetest

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/nevindra/bites"
)

// testing keeps a finished parallel test, with the cleanup functions it registered, until its parent
// ends; the recorder keeps its cleanups after running them the same way, so this is that case without
// a parallel test.
func TestTheCleanupStartRegistersLetsTheAppGoOnceItHasRun(t *testing.T) {
	rec := &recorder{TB: t}
	freed := make(chan struct{})
	func() {
		app := bites.New(bites.WithClock(NewClock()))
		app.Get("/x", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
		runtime.AddCleanup(app, func(ch chan struct{}) { close(ch) }, freed)
		srv := Start(rec, app)
		if r := srv.Do(Request{Method: "GET", Target: "/x"}); r.Status != 200 {
			t.Fatalf("%d", r.Status)
		}
	}()
	rec.finish()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		runtime.GC()
		select {
		case <-freed:
			runtime.KeepAlive(rec)
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the App is still reachable from the cleanup Start registered, after that cleanup ran")
		}
	}
}

func TestCloseCalledByTheTestAndAgainByTheCleanupStopsTheServerOnce(t *testing.T) {
	rec := &recorder{TB: t}
	app := bites.New(bites.WithClock(NewClock()))
	app.Get("/x", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	srv := Start(rec, app)
	srv.Close()
	srv.Close()
	rec.finish()
}
