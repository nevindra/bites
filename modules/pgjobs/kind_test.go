package pgjobs_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgjobs"
)

func within(t *testing.T, what string, got, lo, hi time.Time) {
	t.Helper()
	if got.Before(lo.Add(-time.Microsecond)) || got.After(hi.Add(time.Microsecond)) {
		t.Fatalf("%s is %v, want between %v and %v", what, got, lo, hi)
	}
}

func TestAFailedAttemptIsTriedAgainAfterAnExponentialBackoffWithJitterOnTheTestClock(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk})
	var attempts []int
	k := pgjobs.Register(c, "flaky", func(_ context.Context, job pgjobs.Job[mail]) error {
		attempts = append(attempts, job.Attempt)
		return fmt.Errorf("boom %d", job.Attempt)
	})
	e := enqueue(t, k, w.pool, mail{})
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		now := clk.Now()
		if n := work(t, c); n != 1 {
			t.Fatalf("attempt %d: Work ran %d", i+1, n)
		}
		r := get(t, c, e.ID)
		if r.State != pgjobs.StateAvailable || r.Attempt != i+1 || r.LastError != fmt.Sprintf("boom %d", i+1) {
			t.Fatalf("after attempt %d the job is %s, attempt %d, %q", i+1, r.State, r.Attempt, r.LastError)
		}
		within(t, fmt.Sprintf("the run time after attempt %d", i+1), r.RunAt, now.Add(wait), now.Add(wait+wait/5))
		if n := work(t, c); n != 0 {
			t.Fatalf("a job waiting for its back-off ran")
		}
		clk.Advance(r.RunAt.Sub(now) - time.Millisecond)
		if n := work(t, c); n != 0 {
			t.Fatalf("a job ran a millisecond before its back-off ended")
		}
		clk.Advance(time.Millisecond + time.Microsecond) // Postgres keeps microseconds, rounded
	}
	if fmt.Sprint(attempts) != "[1 2 3 4]" {
		t.Fatalf("the handler saw attempts %v", attempts)
	}
}

func TestDefaultBackoffDoublesFromOneSecondToAnHourWithAFifthOfJitter(t *testing.T) {
	for attempt, base := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 12: 2048 * time.Second, 13: time.Hour, 40: time.Hour} {
		for range 50 {
			if d := pgjobs.DefaultBackoff(attempt); d < base || d > base+base/5 {
				t.Fatalf("DefaultBackoff(%d) = %v, want %v to %v", attempt, d, base, base+base/5)
			}
		}
	}
}

func TestAKindsOwnBackoffReplacesTheDefault(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk})
	k := pgjobs.Register(c, "flaky", func(context.Context, pgjobs.Job[mail]) error { return errors.New("no") },
		pgjobs.KindOptions{Backoff: func(attempt int) time.Duration { return time.Duration(attempt) * 10 * time.Minute }})
	e := enqueue(t, k, w.pool, mail{})
	work(t, c)
	if r := get(t, c, e.ID); !r.RunAt.Equal(clk.Now().Add(10 * time.Minute)) {
		t.Fatalf("run at %v, want ten minutes after %v", r.RunAt, clk.Now())
	}
}

func TestASnoozedJobRunsAgainAfterItsTimeWithoutCountingAnAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk})
	var seen []int
	k := pgjobs.Register(c, "wait-upload", func(_ context.Context, job pgjobs.Job[mail]) error {
		seen = append(seen, job.Attempt)
		if len(seen) < 3 {
			return pgjobs.Snooze(5 * time.Minute)
		}
		return nil
	}, pgjobs.KindOptions{MaxAttempts: 1})
	e := enqueue(t, k, w.pool, mail{})
	for range 2 {
		start := clk.Now()
		work(t, c)
		r := get(t, c, e.ID)
		if r.State != pgjobs.StateAvailable || r.Attempt != 0 || !r.RunAt.Equal(start.Add(5*time.Minute)) {
			t.Fatalf("a snoozed job is %s, attempt %d, due %v", r.State, r.Attempt, r.RunAt)
		}
		if n := work(t, c); n != 0 {
			t.Fatal("a snoozed job ran before its time")
		}
		clk.Advance(5 * time.Minute)
	}
	work(t, c)
	if r := get(t, c, e.ID); r.State != pgjobs.StateCompleted || fmt.Sprint(seen) != "[1 1 1]" {
		t.Fatalf("the job is %s after the handler saw attempts %v; two snoozes must not spend its one attempt", r.State, seen)
	}
}

func TestANegativeSnoozeIsAFailedAttemptNamingTheMistake(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "s", func(context.Context, pgjobs.Job[mail]) error { return pgjobs.Snooze(-time.Second) })
	e := enqueue(t, k, w.pool, mail{})
	work(t, c)
	if r := get(t, c, e.ID); r.Attempt != 1 || !strings.Contains(r.LastError, "pgjobs.Snooze(-1s): a snooze is a time to wait") {
		t.Fatalf("attempt %d, error %q", r.Attempt, r.LastError)
	}
}

func TestFailFailsTheJobAtOnceWithItsError(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error {
		return fmt.Errorf("sending: %w", pgjobs.Fail(errors.New("no such mailbox")))
	})
	e := enqueue(t, k, w.pool, mail{})
	work(t, c)
	r := get(t, c, e.ID)
	if r.State != pgjobs.StateFailed || r.Attempt != 1 || r.LastError != "sending: no such mailbox" || r.FinishedAt.IsZero() {
		t.Fatalf("the job is %s, attempt %d of %d, %q", r.State, r.Attempt, r.MaxAttempts, r.LastError)
	}
	if n := work(t, c); n != 0 {
		t.Fatal("a failed job ran again")
	}
}

func TestAJobIsKeptAsFailedWithItsLastErrorAfterItsLastAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "send", func(_ context.Context, job pgjobs.Job[mail]) error {
		return fmt.Errorf("try %d of %d failed", job.Attempt, job.MaxAttempts)
	}, pgjobs.KindOptions{MaxAttempts: 5, Backoff: func(int) time.Duration { return 0 }})
	e := enqueue(t, k, w.pool, mail{}, pgjobs.EnqueueOptions{MaxAttempts: 3})
	for i := 1; i <= 3; i++ {
		if n := work(t, c); n != 1 {
			t.Fatalf("Work %d ran %d", i, n)
		}
	}
	r := get(t, c, e.ID)
	if r.State != pgjobs.StateFailed || r.Attempt != 3 || r.MaxAttempts != 3 || r.LastError != "try 3 of 3 failed" {
		t.Fatalf("after three attempts the job is %s, attempt %d of %d, %q", r.State, r.Attempt, r.MaxAttempts, r.LastError)
	}
	if n := work(t, c); n != 0 {
		t.Fatal("a job past its last attempt ran")
	}
	if !strings.Contains(w.logs.String(), "pgjobs: job failed for good") {
		t.Fatalf("no Error line for the job that failed:\n%s", w.logs)
	}
}

func TestAUniqueKeyMakesASecondEnqueueANoOpOnlyWhileTheJobIsAvailableOrRunning(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	key := pgjobs.EnqueueOptions{UniqueKey: "file-42"}
	var inRun pgjobs.Enqueued
	var k *pgjobs.Kind[string]
	k = pgjobs.Register(c, "text", func(ctx context.Context, job pgjobs.Job[string]) error {
		switch job.Args {
		case "running":
			inRun = enqueue(t, k, w.pool, "while running", key)
		case "fail":
			return pgjobs.Fail(errors.New("unreadable"))
		}
		return nil
	})
	other := pgjobs.Register(c, "thumbnail", func(context.Context, pgjobs.Job[string]) error { return nil })

	// Available: a due job, and a job waiting for its time.
	first := enqueue(t, k, w.pool, "running", key)
	if again := enqueue(t, k, w.pool, "again", key); !again.Duplicate || again.ID != first.ID {
		t.Fatalf("a second enqueue while the first is available stored %+v", again)
	}
	if o := enqueue(t, other, w.pool, "x", key); o.Duplicate {
		t.Fatal("a key of one kind held a job of another kind")
	}
	// Running: the handler enqueues with its own key.
	work(t, c)
	if !inRun.Duplicate || inRun.ID != first.ID {
		t.Fatalf("an enqueue while the job runs stored %+v", inRun)
	}
	// Completed: the key is free.
	afterDone := enqueue(t, k, w.pool, "fail", key)
	if afterDone.Duplicate {
		t.Fatal("the key of a completed job still held")
	}
	// Failed: the key is free.
	work(t, c)
	if r := get(t, c, afterDone.ID); r.State != pgjobs.StateFailed {
		t.Fatalf("the job is %s", r.State)
	}
	afterFail := enqueue(t, k, w.pool, "later", pgjobs.EnqueueOptions{UniqueKey: "file-42", Delay: time.Hour})
	if afterFail.Duplicate {
		t.Fatal("the key of a failed job still held")
	}
	// A job waiting for its time holds the key; cancelled frees it.
	if again := enqueue(t, k, w.pool, "x", key); !again.Duplicate || again.ID != afterFail.ID {
		t.Fatalf("a job waiting for its time did not hold the key: %+v", again)
	}
	if err := c.Cancel(ctx, afterFail.ID); err != nil {
		t.Fatal(err)
	}
	if afterCancel := enqueue(t, k, w.pool, "x", key); afterCancel.Duplicate {
		t.Fatal("the key of a cancelled job still held")
	}
}

func TestAUniqueKeyHoldsAcrossTransactionsThatEnqueueAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "text", func(context.Context, pgjobs.Job[int]) error { return nil })
	const n = 8
	stored := make(chan bool, n)
	start := make(chan struct{})
	for i := range n {
		go func() {
			<-start
			tx, err := w.pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				stored <- false
				return
			}
			defer tx.Rollback(ctx)
			e, err := k.Enqueue(ctx, tx, i, pgjobs.EnqueueOptions{UniqueKey: "same"})
			if err != nil {
				t.Error(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Error(err)
			}
			stored <- !e.Duplicate
		}()
	}
	close(start)
	got := 0
	for range n {
		if <-stored {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d of %d concurrent enqueues stored a job under one key", got, n)
	}
}

func TestPriorityAndRunTimeOrderTheJobsOfAQueue(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk})
	var order []string
	k := pgjobs.Register(c, "p", func(_ context.Context, job pgjobs.Job[string]) error { order = append(order, job.Args); return nil })
	enqueue(t, k, w.pool, "low", pgjobs.EnqueueOptions{Priority: -5})
	enqueue(t, k, w.pool, "plain")
	enqueue(t, k, w.pool, "urgent", pgjobs.EnqueueOptions{Priority: 10})
	enqueue(t, k, w.pool, "later-but-urgent", pgjobs.EnqueueOptions{Priority: 50, Delay: time.Minute})
	enqueue(t, k, w.pool, "earlier", pgjobs.EnqueueOptions{RunAt: clk.Now().Add(-time.Hour)})
	work(t, c)
	if got := strings.Join(order, ","); got != "urgent,earlier,plain,low" {
		t.Fatalf("ran %s", got)
	}
	clk.Advance(time.Minute)
	work(t, c)
	if order[len(order)-1] != "later-but-urgent" {
		t.Fatalf("ran %v", order)
	}
}
