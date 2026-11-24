package pgjobs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgjobs"
)

func TestPruneDeletesCompletedAndFailedJobsPastTheirRetentionInBatches(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk, KeepCompleted: time.Hour, KeepFailed: 24 * time.Hour})
	k := pgjobs.Register(c, "j", func(_ context.Context, job pgjobs.Job[string]) error {
		if job.Args == "fail" {
			return pgjobs.Fail(errors.New("no"))
		}
		return nil
	})
	enqueue(t, k, w.pool, "ok")
	enqueue(t, k, w.pool, "fail")
	cancelled := enqueue(t, k, w.pool, "cancel", pgjobs.EnqueueOptions{Delay: time.Hour})
	pending := enqueue(t, k, w.pool, "pending", pgjobs.EnqueueOptions{Delay: 1000 * time.Hour})
	c.Cancel(ctx, cancelled.ID)
	work(t, c)
	// 2,500 completed rows from long ago, more than two batches.
	if _, err := w.pool.Exec(ctx, `INSERT INTO "`+w.table+`" (id, kind, queue, state, args, priority, attempt, max_attempts, scheduled_at, run_at, created_at, finished_at)
		SELECT gen_random_uuid(), 'j', 'default', 'completed', '"old"', 0, 1, 20, $1, $1, $1, $1 FROM generate_series(1, 2500)`, clk.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Prune(ctx); err != nil || n != 2500 {
		t.Fatalf("Prune = %d, %v; want the 2500 old completed rows", n, err)
	}
	clk.Advance(time.Hour + time.Second)
	if n, _ := c.Prune(ctx); n != 1 {
		t.Fatalf("an hour later Prune deleted %d, want the one completed job", n)
	}
	clk.Advance(24 * time.Hour)
	if n, _ := c.Prune(ctx); n != 2 {
		t.Fatalf("a day later Prune deleted %d, want the failed and the cancelled job", n)
	}
	if rs := must(c.List(ctx, pgjobs.ListOptions{})); len(rs) != 1 || rs[0].ID != pending.ID {
		t.Fatalf("left %d jobs; only the pending one should be", len(rs))
	}
}

func TestRetryRunsAFailedJobAgainWithFreshAttemptsAndRefusesARunningOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	fail := true
	var retryErr error
	var k *pgjobs.Kind[string]
	k = pgjobs.Register(c, "j", func(_ context.Context, job pgjobs.Job[string]) error {
		if job.Args == "self" {
			retryErr = c.Retry(ctx, job.ID)
			return nil
		}
		if fail {
			return errors.New("down")
		}
		return nil
	}, pgjobs.KindOptions{MaxAttempts: 1})
	e := enqueue(t, k, w.pool, "x")
	work(t, c)
	if r := get(t, c, e.ID); r.State != pgjobs.StateFailed {
		t.Fatalf("the job is %s", r.State)
	}
	if err := c.Retry(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	r := get(t, c, e.ID)
	if r.State != pgjobs.StateAvailable || r.Attempt != 0 || !r.FinishedAt.IsZero() || r.LastError != "down" {
		t.Fatalf("a retried job is %s, attempt %d, finished %v, %q", r.State, r.Attempt, r.FinishedAt, r.LastError)
	}
	fail = false
	if n := work(t, c); n != 1 || get(t, c, e.ID).State != pgjobs.StateCompleted {
		t.Fatal("the retried job did not run to completion")
	}
	enqueue(t, k, w.pool, "self")
	work(t, c)
	if !errors.Is(retryErr, pgjobs.ErrRunning) {
		t.Fatalf("Retry of a running job: %v, want ErrRunning", retryErr)
	}
	if err := c.Retry(ctx, pgjobs.ID{9}); !errors.Is(err, pgjobs.ErrNotFound) {
		t.Fatalf("Retry of no job: %v", err)
	}
	// A retried job whose key another job now holds is refused, naming why.
	keyed := enqueue(t, k, w.pool, "x", pgjobs.EnqueueOptions{UniqueKey: "k", MaxAttempts: 1})
	fail = true
	work(t, c)
	enqueue(t, k, w.pool, "y", pgjobs.EnqueueOptions{UniqueKey: "k", Delay: time.Hour})
	if err := c.Retry(ctx, keyed.ID); err == nil || !strings.Contains(err.Error(), "holds its unique key") {
		t.Fatalf("Retry under a held key: %v", err)
	}
}

func TestCountsListAndGetDescribeTheJobsOfEachQueueAndState(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{Queues: map[string]int{"mail": 1, "text": 1}})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil }, pgjobs.KindOptions{Queue: "mail"})
	var ids []pgjobs.ID
	for range 3 {
		ids = append(ids, enqueue(t, k, w.pool, mail{To: "a@b"}).ID)
	}
	later := enqueue(t, k, w.pool, mail{}, pgjobs.EnqueueOptions{Queue: "text", Delay: time.Hour, Priority: 7, UniqueKey: "u"})
	work(t, c)
	want := []pgjobs.Count{{Queue: "mail", State: pgjobs.StateCompleted, Jobs: 3}, {Queue: "text", State: pgjobs.StateAvailable, Jobs: 1}}
	if got := must(c.Counts(ctx)); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Counts = %+v, want %+v", got, want)
	}
	all := must(c.List(ctx, pgjobs.ListOptions{}))
	if len(all) != 4 || all[0].ID != later.ID || all[3].ID != ids[0] {
		t.Fatalf("List is not newest first: %v", all)
	}
	page := must(c.List(ctx, pgjobs.ListOptions{Limit: 2}))
	next := must(c.List(ctx, pgjobs.ListOptions{Limit: 2, Before: page[1].ID}))
	if len(page) != 2 || len(next) != 2 || next[0].ID != ids[1] || next[1].ID != ids[0] {
		t.Fatalf("paging gave %v then %v", page, next)
	}
	if rs := must(c.List(ctx, pgjobs.ListOptions{State: pgjobs.StateAvailable, Queue: "text", Kind: "send"})); len(rs) != 1 || rs[0].ID != later.ID {
		t.Fatalf("List by state, queue and kind gave %v", rs)
	}
	r := get(t, c, later.ID)
	if r.Kind != "send" || r.Queue != "text" || r.Priority != 7 || r.UniqueKey != "u" || r.MaxAttempts != pgjobs.DefaultMaxAttempts || !r.ScheduledAt.Equal(r.RunAt) || !r.RunAt.After(r.CreatedAt) || !r.AttemptedAt.IsZero() {
		t.Fatalf("Get = %+v", r)
	}
	if _, err := c.Get(ctx, pgjobs.ID{}); !errors.Is(err, pgjobs.ErrNotFound) {
		t.Fatalf("Get of no job: %v", err)
	}
	if _, err := c.List(ctx, pgjobs.ListOptions{Limit: 1001}); err == nil {
		t.Fatal("a limit over 1000 was taken")
	}
}
