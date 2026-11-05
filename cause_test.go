package bites_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type ctxKey struct{}

func TestCauseIsErrClientGoneForAHandlerWhoseClientLeftAlsoThroughDerivedContexts(t *testing.T) {
	started := make(chan struct{})
	got := make(chan [3]error, 1)
	srv := start(t, func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			v := context.WithValue(ctx, ctxKey{}, 1)
			c, cancel := context.WithCancel(v)
			defer cancel()
			close(started)
			<-c.Done()
			got <- [3]error{bites.Cause(ctx), bites.Cause(v), bites.Cause(c)}
			return Empty{}, ctx.Err()
		})
	})
	conn := srv.Dial("203.0.113.9:1")
	fmt.Fprint(conn, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	<-started
	conn.Close()
	select {
	case causes := <-got:
		for i, c := range causes {
			if c != bites.ErrClientGone {
				t.Errorf("context %d: Cause = %v, want ErrClientGone", i, c)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context was not cancelled")
	}
}

func TestCauseReportsADerivedContextsOwnCauseAndIsContextCauseElsewhere(t *testing.T) {
	if c := bites.Cause(context.Background()); c != nil {
		t.Fatalf("a live context: %v", c)
	}
	mine := errors.New("mine")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(mine)
	if c := bites.Cause(ctx); c != mine {
		t.Fatalf("Cause = %v, want the cause given to cancel", c)
	}
	plain, stop := context.WithCancel(context.Background())
	stop()
	if c := bites.Cause(plain); c != context.Canceled {
		t.Fatalf("Cause = %v, want context.Canceled", c)
	}
}

func TestAHandlerThatTimesOutUnderADerivedDeadlineSeesDeadlineExceeded(t *testing.T) {
	got := make(chan error, 1)
	srv := start(t, func(app *bites.App) {
		app.Get("/d", func(ctx context.Context, _ Empty) (Empty, error) {
			c, cancel := context.WithTimeout(ctx, time.Millisecond)
			defer cancel()
			<-c.Done()
			got <- bites.Cause(c)
			return Empty{}, nil
		})
	})
	srv.Do(bitetest.Request{Method: "GET", Target: "/d"})
	if c := <-got; c != context.DeadlineExceeded {
		t.Fatalf("Cause = %v, want the derived context's own deadline", c)
	}
}
