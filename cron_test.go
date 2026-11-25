package bites_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/clock"
)

// cronClock is a bitetest.Clock reading start, with a skew that moves what Now reports and not
// when its timers fire: the clock of a laptop that was suspended (skew forward) or of a host
// whose time was set back (skew backward), which the fake clock cannot do by Advance alone.
type cronClock struct {
	*clock.Fake
	skew atomic.Int64
}

func (c *cronClock) Now() time.Time { return c.Fake.Now().Add(time.Duration(c.skew.Load())) }

func (c *cronClock) setSkew(d time.Duration) { c.skew.Store(int64(d)) }

func cronDebugLog() (*svcLogBuf, *slog.Logger) {
	lb := &svcLogBuf{}
	return lb, slog.New(slog.NewTextHandler(lb, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func cronZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("time.LoadLocation(%q): %v", name, err)
	}
	return loc
}

// startCron serves one Cron job on a fake clock that reads start, with its log in lb.
func startCron(t *testing.T, start time.Time, spec string, loc *time.Location, fn func(context.Context) error, lock bites.JobLock) (*cronClock, *svcLogBuf, *bites.App) {
	t.Helper()
	clk := &cronClock{Fake: clock.NewFake(start)}
	lb, log := cronDebugLog()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(log))
	app.Service(bites.Cron("job", spec, loc, fn, bites.CronOptions{Lock: lock}))
	bitetest.Start(t, app)
	return clk, lb, app
}

// cronAdvanceTo moves the fake clock to when, once the job has armed its timer.
func cronAdvanceTo(t *testing.T, clk *cronClock, when time.Time) {
	t.Helper()
	svcEventually(t, "the job to arm its timer", func() bool { _, ok := clk.Next(); return ok })
	clk.Advance(when.Sub(clk.Fake.Now()))
}

func cronAt(s string, loc *time.Location) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronCallsFnAtEachTimeOfItsSpecOnTheAppsClockAndNotAtStart(t *testing.T) {
	var runs atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, _, _ := startCron(t, start, "*/5 * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, nil)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("fn ran %d times at start; want 0, the first run is the first time after the start", n)
	}
	for i := int32(1); i <= 3; i++ {
		cronAdvanceTo(t, clk, start.Add(time.Duration(i)*5*time.Minute))
		svcEventually(t, "the run", func() bool { return runs.Load() == i })
	}
	clk.Advance(4 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 3 {
		t.Fatalf("fn ran %d times 4 minutes after the third; want 3", n)
	}
}

func TestACronJobSeesTheAppsClockAndLoggerAndTheTimeItWasDueAt(t *testing.T) {
	jkt := cronZone(t, "Asia/Jakarta")
	var seen atomic.Value
	var now atomic.Value
	start := cronAt("2026-05-01 08:59", jkt)
	clk, lb, _ := startCron(t, start, "0 9 * * *", jkt, func(ctx context.Context) error {
		now.Store(bites.Now(ctx))
		bites.Logger(ctx).Info("from the job")
		seen.Store(bites.ScheduledAt(ctx)) // last: the test waits for it, and then reads the other two
		return nil
	}, nil)
	cronAdvanceTo(t, clk, cronAt("2026-05-01 09:00", jkt))
	svcEventually(t, "the run", func() bool { return seen.Load() != nil })
	got := seen.Load().(time.Time)
	if !got.Equal(cronAt("2026-05-01 09:00", jkt)) || got.Location() != time.UTC {
		t.Fatalf("ScheduledAt(ctx) = %v; want 09:00 in Jakarta given in UTC (02:00Z)", got)
	}
	if n := now.Load().(time.Time); !n.Equal(clk.Now()) {
		t.Fatalf("bites.Now(ctx) = %v; want the app's clock %v", n, clk.Now())
	}
	if !strings.Contains(lb.String(), "from the job") {
		t.Fatalf("bites.Logger(ctx) is not the app's logger; log:\n%s", lb.String())
	}
	if !bites.ScheduledAt(context.Background()).IsZero() {
		t.Fatal("ScheduledAt of a context that is not a job's is not the zero time")
	}
}

func TestACronJobLogsItsScheduleWhenItStarts(t *testing.T) {
	clk, lb, _ := startCron(t, cronAt("2026-01-01 00:00", time.UTC), "30 2 * * *", time.UTC, svcNoop, nil)
	_ = clk
	for _, want := range []string{"bites: job scheduled", "job=job", `spec="30 2 * * *"`, "zone=UTC", "lock=none", "next=2026-01-01T02:30:00.000Z"} {
		if !strings.Contains(lb.String(), want) {
			t.Fatalf("the start-up line lacks %q; log:\n%s", want, lb.String())
		}
	}
}

func TestACronJobAtATimeSkippedByDaylightSavingRunsOnceAtTheInstantAfterTheGap(t *testing.T) {
	ny := cronZone(t, "America/New_York")
	var runs atomic.Int32
	var last atomic.Value
	start := cronAt("2026-03-07 12:00", ny)
	clk, _, _ := startCron(t, start, "30 2 * * *", ny, func(ctx context.Context) error {
		runs.Add(1)
		last.Store(bites.ScheduledAt(ctx))
		return nil
	}, nil)
	cronAdvanceTo(t, clk, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)) // 03:00 EDT
	svcEventually(t, "the run", func() bool { return runs.Load() == 1 })
	if got := last.Load().(time.Time); !got.Equal(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("the run of the skipped 02:30 was due at %v; want 07:00Z", got)
	}
	clk.Advance(23*time.Hour + 29*time.Minute) // 02:29 EDT the next day
	time.Sleep(20 * time.Millisecond)
	clk.Advance(time.Minute)
	svcEventually(t, "the next day's run", func() bool { return runs.Load() == 2 })
	if got := last.Load().(time.Time); !got.Equal(cronAt("2026-03-09 02:30", ny)) {
		t.Fatalf("the next run was due at %v; want 02:30 EDT the next day", got.In(ny))
	}
}

func TestACronJobAtARepeatedTimeRunsOnceAndAnHourlyOneRunsTwice(t *testing.T) {
	ny := cronZone(t, "America/New_York")
	var daily, hourly atomic.Int32
	start := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC) // 00:00 EDT
	clk := &cronClock{Fake: clock.NewFake(start)}
	app := bites.New(bites.WithClock(clk))
	app.Service(bites.Cron("daily", "30 1 * * *", ny, func(context.Context) error { daily.Add(1); return nil }))
	app.Service(bites.Cron("hourly", "30 * * * *", ny, func(context.Context) error { hourly.Add(1); return nil }))
	bitetest.Start(t, app)
	for u := start.Add(30 * time.Minute); !u.After(time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)); u = u.Add(time.Hour) {
		cronAdvanceTo(t, clk, u)
		time.Sleep(10 * time.Millisecond) // let the runs end before the clock moves on
	}
	svcEventually(t, "the runs", func() bool { return hourly.Load() == 5 })
	if d := daily.Load(); d != 1 {
		t.Fatalf("the 01:30 job ran %d times on the day 01:30 happens twice; want 1", d)
	}
	// 00:30 EDT, 01:30 EDT, 01:30 EST, 02:30 EST, 03:30 EST: the hour that repeats has two.
	if h := hourly.Load(); h != 5 {
		t.Fatalf("the :30 job ran %d times between 00:00 EDT and 03:59 EST; want 5", h)
	}
}

func TestACronRunThatIsStillGoingWhenTheNextIsDueIsSkippedAndLoggedNeverOverlapped(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	entered, release := make(chan struct{}, 8), make(chan struct{})
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, lb, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		runs.Add(1)
		entered <- struct{}{}
		<-release
		running.Add(-1)
		return nil
	}, nil)
	cronAdvanceTo(t, clk, start.Add(time.Minute))
	svcWithin(t, "the first run to start", func() { <-entered })
	clk.Advance(time.Minute) // 00:02 is due while the 00:01 run is going
	svcEventually(t, "the skip to be logged", func() bool { return strings.Contains(lb.String(), "bites: job skipped: its last run is still going") })
	clk.Advance(time.Minute) // and 00:03
	svcEventually(t, "the second skip", func() bool { return strings.Count(lb.String(), "its last run is still going") == 2 })
	if runs.Load() != 1 {
		t.Fatalf("fn started %d times while the first was going; want 1", runs.Load())
	}
	close(release)
	svcEventually(t, "the run to end", func() bool { return running.Load() == 0 })
	clk.Advance(time.Minute) // 00:04: the schedule goes on, nothing was queued
	svcWithin(t, "the run at 00:04", func() { <-entered })
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 2 || maxRunning.Load() != 1 {
		t.Fatalf("runs=%d maxRunning=%d; want 2 runs, never two at once, and none made up for the skipped times", runs.Load(), maxRunning.Load())
	}
	for _, want := range []string{"due=2026-01-01T00:02:00.000Z", "due=2026-01-01T00:03:00.000Z", "dropped=1"} {
		if !strings.Contains(lb.String(), want) {
			t.Fatalf("the skip lines lack %q; log:\n%s", want, lb.String())
		}
	}
}

func TestACronTimerThatFiresMinutesAfterItsTimeBecauseTheProcessWasPausedSkipsTheRunAndNeverBursts(t *testing.T) {
	var runs atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, lb, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, nil)
	svcEventually(t, "the timer", func() bool { _, ok := clk.Next(); return ok })
	clk.setSkew(40*time.Minute + 30*time.Second) // the clock reads 00:41:30 when the 00:01 timer fires
	clk.Advance(time.Minute)
	svcEventually(t, "the skip to be logged", func() bool {
		return strings.Contains(lb.String(), "its time passed while the clock jumped or the process was paused")
	})
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("fn ran %d times for a time 40 minutes past; want 0", n)
	}
	for _, want := range []string{"due=2026-01-01T00:01:00.000Z", "late=40m30s", "dropped=41"} {
		if !strings.Contains(lb.String(), want) {
			t.Fatalf("the skip line lacks %q; log:\n%s", want, lb.String())
		}
	}
	// The next time is 00:42 on the clock as it now reads: the schedule goes on from there, once.
	clk.Advance(time.Minute)
	svcEventually(t, "the run after the pause", func() bool { return runs.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times after the pause; want 1, one run and no burst", n)
	}
}

func TestACronRunThatIsAFewSecondsLateStillRuns(t *testing.T) {
	var runs atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, _, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, nil)
	svcEventually(t, "the timer", func() bool { _, ok := clk.Next(); return ok })
	clk.setSkew(30 * time.Second)
	clk.Advance(time.Minute)
	svcEventually(t, "the run", func() bool { return runs.Load() == 1 })
}

func TestACronJobNeverRepeatsATimeWhenTheClockIsSetBack(t *testing.T) {
	var runs atomic.Int32
	start := cronAt("2026-01-01 12:00", time.UTC)
	clk, lb, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, nil)
	cronAdvanceTo(t, clk, start.Add(time.Minute))
	svcEventually(t, "the run at 12:01", func() bool { return runs.Load() == 1 })
	svcEventually(t, "the next timer", func() bool { _, ok := clk.Next(); return ok })
	clk.setSkew(-30 * time.Minute) // the host's clock is set back to 11:31 as the 12:02 timer fires
	clk.Advance(time.Minute)
	svcEventually(t, "the log of the clock going back", func() bool { return strings.Contains(lb.String(), "the clock went back before a job's time") })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 1 {
		t.Fatalf("fn ran %d times when the clock was set back; want 1, nothing is run twice", n)
	}
	// The timer is re-armed for 12:02 on the clock as it reads (11:32), 30 minutes away; the times
	// in between (11:33 to 12:01 on that clock) are not run again.
	if next, ok := clk.Next(); !ok || next.Sub(clk.Fake.Now()) != 30*time.Minute {
		t.Fatalf("the re-armed timer is %v away (armed: %v); want 30m", next.Sub(clk.Fake.Now()), ok)
	}
}

func TestAPanicAndAnErrorInACronJobAreLoggedAndTheScheduleGoesOn(t *testing.T) {
	var runs, ended atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, lb, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error {
		defer ended.Add(1)
		switch runs.Add(1) {
		case 1:
			panic("boom")
		case 2:
			return errors.New("the database is down")
		}
		return nil
	}, nil)
	for i := 1; i <= 3; i++ {
		cronAdvanceTo(t, clk, start.Add(time.Duration(i)*time.Minute))
		svcEventually(t, "the run", func() bool { return runs.Load() == int32(i) })
		svcEventually(t, "the run to end", func() bool { return ended.Load() == int32(i) })
		time.Sleep(10 * time.Millisecond) // the job is idle again once fn returned
	}
	for _, want := range []string{"bites: panic in a job", "panic=boom", "bites: job failed, trying again at its next time", "the database is down"} {
		if !strings.Contains(lb.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, lb.String())
		}
	}
}

func TestAShutdownWhileACronRunIsGoingCancelsItsContextAndWaitsForItToReturn(t *testing.T) {
	entered, returned := make(chan struct{}), make(chan struct{})
	var cause atomic.Value
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk := &cronClock{Fake: clock.NewFake(start)}
	app := bites.New(bites.WithClock(clk))
	app.Service(bites.Cron("job", "* * * * *", time.UTC, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		time.Sleep(30 * time.Millisecond) // work that finishes after the cancel
		close(returned)
		return ctx.Err()
	}))
	srv := bitetest.Start(t, app)
	_ = srv
	cronAdvanceTo(t, clk, start.Add(time.Minute))
	svcWithin(t, "the run to start", func() { <-entered })
	if err := app.Shutdown(shutdownCtx(t)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("Shutdown returned before the run in progress did")
	}
	if c, _ := cause.Load().(error); !errors.Is(c, bites.ErrServerClosed) {
		t.Fatalf("the run's ctx was cancelled with %v; want ErrServerClosed", c)
	}
	clk.Advance(5 * time.Minute)
	time.Sleep(20 * time.Millisecond)
}

// raceCron serves a Cron of one spec on n instances that share lock, each on its own clock, and
// counts the runs of each.
func raceCron(t *testing.T, n int, lock bites.JobLock, offsets []time.Duration) (clks []*cronClock, runs []*atomic.Int32, logs []*svcLogBuf) {
	t.Helper()
	start := cronAt("2026-01-01 00:00", time.UTC)
	for i := range n {
		runs = append(runs, new(atomic.Int32))
		clk := &cronClock{Fake: clock.NewFake(start.Add(offsets[i]))}
		lb, log := cronDebugLog()
		app := bites.New(bites.WithClock(clk), bites.WithLogger(log))
		app.Service(bites.Cron("report", "* * * * *", time.UTC, func(context.Context) error { runs[i].Add(1); return nil }, bites.CronOptions{Lock: lock}))
		bitetest.Start(t, app)
		clks, logs = append(clks, clk), append(logs, lb)
	}
	return clks, runs, logs
}

func TestTwoInstancesRacingForTheSameMinuteRunItOnceBetweenThem(t *testing.T) {
	lock := bites.NewMemoryJobLock()
	clks, runs, _ := raceCron(t, 2, lock, []time.Duration{0, 0})
	for _, c := range clks {
		svcEventually(t, "the timer", func() bool { _, ok := c.Next(); return ok })
	}
	for minute := 1; minute <= 30; minute++ {
		var wg sync.WaitGroup
		for _, c := range clks { // both reach the boundary at the same moment
			wg.Add(1)
			go func() { defer wg.Done(); c.Advance(time.Minute) }()
		}
		wg.Wait()
		want := int32(minute)
		svcEventually(t, "one run of the minute", func() bool { return runs[0].Load()+runs[1].Load() >= want })
		time.Sleep(2 * time.Millisecond)
		if got := runs[0].Load() + runs[1].Load(); got != want {
			t.Fatalf("after %d minutes the two instances ran the job %d times; want %d, once a minute between them", minute, got, want)
		}
	}
	if runs[0].Load() == 0 || runs[1].Load() == 0 {
		t.Logf("one instance won every minute (%d, %d): allowed, the lock decides", runs[0].Load(), runs[1].Load())
	}
}

func TestAnInstanceWhoseClockIsBehindDoesNotRunAnOccurrenceTheOtherHasFinished(t *testing.T) {
	lock := bites.NewMemoryJobLock()
	clks, runs, logs := raceCron(t, 2, lock, []time.Duration{0, 0})
	for _, c := range clks {
		svcEventually(t, "the timer", func() bool { _, ok := c.Next(); return ok })
	}
	clks[0].Advance(time.Minute) // instance A is on time, runs and finishes
	svcEventually(t, "A's run", func() bool { return runs[0].Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	clks[1].Advance(time.Minute) // instance B reaches the boundary 20 ms later, after A's run ended
	time.Sleep(50 * time.Millisecond)
	if runs[1].Load() != 0 {
		t.Fatalf("instance B ran an occurrence A had already run and finished; the claim must outlast the run")
	}
	if !strings.Contains(logs[1].String(), "another instance claimed this occurrence") {
		t.Fatalf("B's log does not say another instance has the occurrence:\n%s", logs[1].String())
	}
}

func TestAnInstanceThatMissesTheMinuteDoesNotMakeTheOtherRunItTwice(t *testing.T) {
	lock := bites.NewMemoryJobLock()
	clks, runs, _ := raceCron(t, 2, lock, []time.Duration{0, 0})
	for _, c := range clks {
		svcEventually(t, "the timer", func() bool { _, ok := c.Next(); return ok })
	}
	// Only B reaches 00:01 for now; A wakes 59 s later (within the grace) and finds it claimed.
	clks[1].Advance(time.Minute)
	svcEventually(t, "B's run", func() bool { return runs[1].Load() == 1 })
	clks[0].setSkew(59 * time.Second)
	clks[0].Advance(time.Minute)
	time.Sleep(50 * time.Millisecond)
	if runs[0].Load() != 0 {
		t.Fatal("A ran an occurrence B claimed")
	}
}

// cronBrokenLock is a JobLock whose store fails, panics or hangs on demand.
type cronBrokenLock struct {
	mode  atomic.Int32 // 0 works, 1 error, 2 panic, 3 hang until ctx is done
	inner bites.JobLock
	calls atomic.Int32
}

func (b *cronBrokenLock) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	b.calls.Add(1)
	switch b.mode.Load() {
	case 1:
		return true, errors.New("connection refused") // an error with true must still not run the job
	case 2:
		panic("store exploded")
	case 3:
		<-ctx.Done()
		return false, ctx.Err()
	}
	return b.inner.Claim(ctx, key, ttl)
}

func TestACronJobWhoseLockStoreFailsSkipsTheOccurrenceLogsItAndNeverRunsUnlocked(t *testing.T) {
	lock := &cronBrokenLock{inner: bites.NewMemoryJobLock()}
	var runs atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, lb, _ := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, lock)
	lock.mode.Store(1)
	cronAdvanceTo(t, clk, start.Add(time.Minute))
	svcEventually(t, "the failure to be logged", func() bool { return strings.Contains(lb.String(), "the lock store failed") })
	lock.mode.Store(2)
	clk.Advance(time.Minute)
	svcEventually(t, "the panic to be logged", func() bool { return strings.Contains(lb.String(), "panic: store exploded") })
	time.Sleep(20 * time.Millisecond)
	if n := runs.Load(); n != 0 {
		t.Fatalf("fn ran %d times while its lock store was failing; want 0, never unlocked", n)
	}
	for _, want := range []string{"level=ERROR", "err=\"connection refused\"", "key=job@1767225660"} {
		if !strings.Contains(lb.String(), want) {
			t.Fatalf("the failure line lacks %q; log:\n%s", want, lb.String())
		}
	}
	lock.mode.Store(0) // the store is back: the next occurrence runs
	clk.Advance(time.Minute)
	svcEventually(t, "the run after the store recovered", func() bool { return runs.Load() == 1 })
}

func TestACronJobWhoseLockStoreHangsIsSkippedWhenTheAppStops(t *testing.T) {
	lock := &cronBrokenLock{inner: bites.NewMemoryJobLock()}
	lock.mode.Store(3)
	var runs atomic.Int32
	start := cronAt("2026-01-01 00:00", time.UTC)
	clk, lb, app := startCron(t, start, "* * * * *", time.UTC, func(context.Context) error { runs.Add(1); return nil }, lock)
	cronAdvanceTo(t, clk, start.Add(time.Minute))
	svcEventually(t, "the claim to be asked", func() bool { return lock.calls.Load() == 1 })
	if err := app.Shutdown(shutdownCtx(t)); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if runs.Load() != 0 {
		t.Fatal("fn ran though its claim never answered")
	}
	if !strings.Contains(lb.String(), "bites: job skipped: the app is stopping") {
		t.Fatalf("the skipped occurrence is not logged; log:\n%s", lb.String())
	}
}

func TestAMemoryJobLockLetsTheTwoAppsOfOneProcessShareOneRunEachMinuteOverAnHour(t *testing.T) {
	lock := bites.NewMemoryJobLock()
	clks, runs, _ := raceCron(t, 3, lock, []time.Duration{0, 0, 0})
	for _, c := range clks {
		svcEventually(t, "the timer", func() bool { _, ok := c.Next(); return ok })
	}
	for minute := 1; minute <= 60; minute++ {
		for _, c := range clks {
			c.Advance(time.Minute)
		}
		svcEventually(t, "a run", func() bool { return runs[0].Load()+runs[1].Load()+runs[2].Load() >= int32(minute) })
	}
	time.Sleep(20 * time.Millisecond)
	if got := runs[0].Load() + runs[1].Load() + runs[2].Load(); got != 60 {
		t.Fatalf("three instances ran the job %d times in 60 minutes; want 60", got)
	}
}
