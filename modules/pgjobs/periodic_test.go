package pgjobs_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgjobs"
)

type report struct {
	Name string `json:"name"`
}

func TestAPeriodicJobIsEnqueuedOncePerPeriodBetweenTwoInstances(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	var mu sync.Mutex
	var periods []time.Time
	var keys []string
	h := func(_ context.Context, job pgjobs.Job[report]) error {
		mu.Lock()
		defer mu.Unlock()
		periods = append(periods, job.ScheduledAt)
		keys = append(keys, job.UniqueKey)
		if job.Args.Name != "sessions" {
			t.Errorf("args %+v", job.Args)
		}
		return nil
	}
	a, b := w.client(pgjobs.Options{Clock: clk}), w.client(pgjobs.Options{Clock: clk})
	for _, c := range []*pgjobs.Client{a, b} {
		pgjobs.Register(c, "sweep", h).Every("sweep-each-minute", time.Minute, report{Name: "sessions"})
	}
	start := clk.Now()
	for round := range 20 {
		var wg sync.WaitGroup
		for _, c := range []*pgjobs.Client{a, b} {
			wg.Go(func() { work(t, c) })
		}
		wg.Wait()
		mu.Lock()
		if len(periods) != round+1 {
			mu.Unlock()
			t.Fatalf("round %d: %d jobs ran in all, want one a period", round, len(periods))
		}
		mu.Unlock()
		clk.Advance(20 * time.Second) // three looks a period
		for _, c := range []*pgjobs.Client{a, b} {
			work(t, c)
		}
		clk.Advance(40 * time.Second)
	}
	for i, p := range periods {
		want := start.Add(time.Duration(i) * time.Minute)
		if !p.Equal(want) || keys[i] != "sweep-each-minute@"+strconv.FormatInt(want.Unix(), 10) {
			t.Fatalf("job %d has period %v and key %q, want %v", i, p, keys[i], want)
		}
	}
}

func TestAPeriodMissedByEveryInstanceRunsOnceWhenTheFirstComesBackAndTheOnesBeforeItDoNot(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	var runs atomic.Int32
	c := w.client(pgjobs.Options{Clock: clk})
	pgjobs.Register(c, "hourly", func(context.Context, pgjobs.Job[report]) error { runs.Add(1); return nil }).Every("hourly", time.Hour, report{})
	work(t, c)
	clk.Advance(5 * time.Hour) // every instance was down
	restarted := w.client(pgjobs.Options{Clock: clk})
	pgjobs.Register(restarted, "hourly", func(context.Context, pgjobs.Job[report]) error { runs.Add(1); return nil }).Every("hourly", time.Hour, report{})
	work(t, restarted)
	work(t, restarted)
	if runs.Load() != 2 {
		t.Fatalf("%d runs; want the first period and the current one, not the four in between", runs.Load())
	}
}

func TestACronScheduleIsEnqueuedOncePerOccurrenceBetweenTwoInstances(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	var runs atomic.Int32
	var at sync.Map
	for range 2 {
		// Advance runs the timers it passes on its own goroutine, so a job still running when the
		// test moves the clock five minutes would see its lease run out at once and run again. A
		// lease longer than the test keeps this test about the schedule.
		c := w.client(pgjobs.Options{Clock: clk, PollEvery: time.Second, Lease: time.Hour})
		pgjobs.Register(c, "digest", func(_ context.Context, job pgjobs.Job[report]) error {
			runs.Add(1)
			at.Store(job.ScheduledAt.Unix(), job.UniqueKey)
			return nil
		}).Cron("digest", "*/5 * * * *", time.UTC, report{Name: "digest"})
		serve(t, c, bites.WithClock(clk))
	}
	count := func() int {
		var n int
		w.pool.QueryRow(ctx, `SELECT count(*) FROM "`+w.table+`"`).Scan(&n)
		return n
	}
	for i := 1; i <= 3; i++ {
		clk.Advance(5 * time.Minute)
		waitFor(t, fmt.Sprintf("occurrence %d to be enqueued", i), func() bool { return count() >= i })
		time.Sleep(100 * time.Millisecond)
		if n := count(); n != i {
			t.Fatalf("after occurrence %d there are %d jobs: two instances enqueued one occurrence", i, n)
		}
	}
	clk.Advance(time.Second) // the poll of the fake clock claims what is due
	waitFor(t, "the three jobs to run", func() bool { return runs.Load() == 3 })
	for i := int64(1); i <= 3; i++ {
		sec := clk.Now().Truncate(5*time.Minute).Unix() - (3-i)*300
		if k, ok := at.Load(sec); !ok || k != "digest@"+strconv.FormatInt(sec, 10) {
			t.Fatalf("no job for the occurrence at %d (%v)", sec, k)
		}
	}
}
