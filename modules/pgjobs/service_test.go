package pgjobs_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgjobs"
	"github.com/nevindra/bites/modules/pgnotify"
)

// TestTheChildOfTheCrashTestClaimsAJobAndHangsUntilItIsKilled is the process
// TestAWorkerThatDiesMidJobLeavesItToBeRunAgainOnceItsLeaseHasRunOut starts and kills; run alone
// it is skipped.
func TestTheChildOfTheCrashTestClaimsAJobAndHangsUntilItIsKilled(t *testing.T) {
	table := os.Getenv("PGJOBS_CRASH_TABLE")
	if table == "" {
		t.Skip("run as the child of TestAWorkerThatDiesMidJobLeavesItToBeRunAgainOnceItsLeaseHasRunOut")
	}
	w := newWorld(t)
	w.table = table
	c := pgjobs.New(pgjobs.Options{Pool: w.pool, Table: table, Lease: time.Second, PollEvery: 20 * time.Millisecond})
	pgjobs.Register(c, "slow", func(_ context.Context, job pgjobs.Job[mail]) error {
		fmt.Printf("CLAIMED %s\n", job.ID)
		select {}
	})
	serve(t, c)
	select {}
}

func TestAWorkerThatDiesMidJobLeavesItToBeRunAgainOnceItsLeaseHasRunOut(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{Lease: time.Second})
	var runs atomic.Int32
	k := pgjobs.Register(c, "slow", func(_ context.Context, job pgjobs.Job[mail]) error {
		runs.Add(1)
		if job.Attempt != 2 {
			t.Errorf("the rerun is attempt %d, want 2: the crashed attempt counts", job.Attempt)
		}
		return nil
	})
	e := enqueue(t, k, w.pool, mail{To: "a@b"})

	cmd := exec.Command(os.Args[0], "-test.run=^TestTheChildOfTheCrashTestClaimsAJobAndHangsUntilItIsKilled$", "-test.v")
	cmd.Env = append(os.Environ(), "PGJOBS_CRASH_TABLE="+w.table)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	claimed := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if id, ok := strings.CutPrefix(sc.Text(), "CLAIMED "); ok {
				claimed <- id
			}
		}
	}()
	select {
	case id := <-claimed:
		if id != e.ID.String() {
			t.Fatalf("the child claimed %s, want %s", id, e.ID)
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the child never claimed the job")
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL: no Stop, no release, no heartbeat
		t.Fatal(err)
	}
	cmd.Wait()
	killed := time.Now()

	r := get(t, c, e.ID)
	if r.State != pgjobs.StateRunning || r.Attempt != 1 {
		t.Fatalf("the dead worker's job is %s, attempt %d", r.State, r.Attempt)
	}
	if n := work(t, c); n != 0 || runs.Load() != 0 {
		t.Fatal("the job ran again while its lease still held")
	}
	waitFor(t, "the job to run again after its lease", func() bool { return work(t, c) == 1 })
	if time.Since(killed) > 5*time.Second {
		t.Fatalf("the rerun came %v after the kill, with a lease of one second", time.Since(killed))
	}
	r = get(t, c, e.ID)
	if r.State != pgjobs.StateCompleted || r.Attempt != 2 || runs.Load() != 1 || !strings.Contains(r.LastError, "the lease ran out during attempt 1") {
		t.Fatalf("after the rerun the job is %s, attempt %d, %d runs, last error %q", r.State, r.Attempt, runs.Load(), r.LastError)
	}
	if !strings.Contains(w.logs.String(), "pgjobs: a job's lease ran out") {
		t.Fatalf("the rescue was not logged:\n%s", w.logs)
	}
}

func TestTheHeartbeatKeepsAJobLongerThanItsLeaseFromASecondRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	o := pgjobs.Options{Lease: time.Second, PollEvery: 20 * time.Millisecond}
	a, b := w.client(o), w.client(o)
	var runs atomic.Int32
	started := make(chan struct{}, 2)
	h := func(ctx context.Context, job pgjobs.Job[mail]) error {
		runs.Add(1)
		started <- struct{}{}
		select {
		case <-time.After(3500 * time.Millisecond): // three and a half leases
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	ka := pgjobs.Register(a, "long", h)
	pgjobs.Register(b, "long", h)
	e := enqueue(t, ka, w.pool, mail{})
	serve(t, a)
	<-started
	serve(t, b) // b polls every 20ms for the whole run
	first := get(t, a, e.ID).LeaseUntil
	time.Sleep(2 * time.Second)
	if later := get(t, a, e.ID).LeaseUntil; !later.After(first.Add(500 * time.Millisecond)) {
		t.Fatalf("the lease was not extended: %v then %v", first, later)
	}
	waitFor(t, "the long job to complete", func() bool { return get(t, a, e.ID).State == pgjobs.StateCompleted })
	if r := get(t, a, e.ID); runs.Load() != 1 || r.Attempt != 1 {
		t.Fatalf("the job ran %d times, attempt %d", runs.Load(), r.Attempt)
	}
}

func TestStopWaitsForARunningJobToFinish(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{PollEvery: 20 * time.Millisecond})
	started := make(chan struct{})
	k := pgjobs.Register(c, "short", func(context.Context, pgjobs.Job[mail]) error {
		close(started)
		time.Sleep(300 * time.Millisecond)
		return nil
	})
	e := enqueue(t, k, w.pool, mail{})
	app := serve(t, c)
	<-started
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := app.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	if r := get(t, c, e.ID); r.State != pgjobs.StateCompleted {
		t.Fatalf("after Shutdown the job is %s, want completed: Stop must wait for it", r.State)
	}
}

func TestStopPutsBackAJobThatDidNotFinishSoAnotherInstanceTakesItAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{PollEvery: 20 * time.Millisecond, Lease: time.Minute})
	started := make(chan struct{}, 1)
	var cause atomic.Value
	k := pgjobs.Register(c, "hang", func(ctx context.Context, job pgjobs.Job[mail]) error {
		started <- struct{}{}
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		return ctx.Err()
	})
	e := enqueue(t, k, w.pool, mail{})
	app := serve(t, c)
	<-started
	sctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond) // Stop keeps the last two seconds
	defer cancel()
	began := time.Now()
	app.Shutdown(sctx)
	if took := time.Since(began); took > 2500*time.Millisecond {
		t.Fatalf("Shutdown took %v, past its context", took)
	}
	if got, _ := cause.Load().(error); !errors.Is(got, pgjobs.ErrStopping) {
		t.Fatalf("the job's ctx was cancelled with %v, want ErrStopping", got)
	}
	r := get(t, c, e.ID)
	if r.State != pgjobs.StateAvailable || r.Attempt != 0 || r.RunAt.After(time.Now()) {
		t.Fatalf("the job left at shutdown is %s, attempt %d, due %v: it must be due now with the attempt not counted", r.State, r.Attempt, r.RunAt)
	}
	other := w.client(pgjobs.Options{})
	var attempt int
	pgjobs.Register(other, "hang", func(_ context.Context, job pgjobs.Job[mail]) error { attempt = job.Attempt; return nil })
	if n := work(t, other); n != 1 || attempt != 1 {
		t.Fatalf("another instance ran %d jobs at once, attempt %d; it must not wait for the minute's lease", n, attempt)
	}
}

func TestANotificationWakesTheWorkersOfAnotherInstanceAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	channel := "jobs_" + w.table
	broker := func() *pgnotify.Broker {
		b := pgnotify.New(pgnotify.Options{Config: w.pool.Config().ConnConfig, Channel: channel, Pool: w.pool, Log: slog.New(slog.NewTextHandler(w.logs, nil))})
		go b.Run(ctx)
		t.Cleanup(func() { b.Close() })
		waitFor(t, "the broker to listen", b.Listening)
		return b
	}
	sender := w.client(pgjobs.Options{Notify: broker()})
	worker := w.client(pgjobs.Options{Notify: broker(), PollEvery: time.Hour})
	done := make(chan string, 4)
	h := func(_ context.Context, job pgjobs.Job[mail]) error { done <- job.Args.To; return nil }
	k := pgjobs.Register(sender, "send", h)
	pgjobs.Register(worker, "send", h)
	serve(t, worker)
	time.Sleep(100 * time.Millisecond) // past the worker's first claim at start

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, k, tx, mail{To: "in-tx"})
	select {
	case got := <-done:
		t.Fatalf("%s ran before its transaction committed", got)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	enqueue(t, k, w.pool, mail{To: "pool"})
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a job was not run within five seconds; with a poll of an hour only the notification could have woken the worker")
		}
	}
}

func TestAnAttemptPastItsTimeoutIsCancelledAndCountedAsAFailedAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "slow", func(ctx context.Context, job pgjobs.Job[mail]) error {
		<-ctx.Done()
		return ctx.Err()
	}, pgjobs.KindOptions{Timeout: 50 * time.Millisecond})
	e := enqueue(t, k, w.pool, mail{})
	work(t, c)
	if r := get(t, c, e.ID); r.State != pgjobs.StateAvailable || r.Attempt != 1 || !strings.Contains(r.LastError, "the attempt ran past its timeout of 50ms: context deadline exceeded") {
		t.Fatalf("the job is %s, attempt %d, %q", r.State, r.Attempt, r.LastError)
	}
}

func TestAnAttemptsTimeoutRunsOnTheTestClock(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	c := w.client(pgjobs.Options{Clock: clk})
	k := pgjobs.Register(c, "slow", func(ctx context.Context, job pgjobs.Job[mail]) error {
		go clk.Advance(time.Minute)
		<-ctx.Done()
		return context.Cause(ctx)
	}, pgjobs.KindOptions{Timeout: time.Minute})
	e := enqueue(t, k, w.pool, mail{})
	work(t, c)
	if r := get(t, c, e.ID); !strings.Contains(r.LastError, "the attempt ran past its timeout of 1m0s") {
		t.Fatalf("last error %q", r.LastError)
	}
}

func TestAPanicInAHandlerIsAFailedAttemptLoggedWithItsStack(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "bad", func(context.Context, pgjobs.Job[mail]) error { panic("nil map") })
	e := enqueue(t, k, w.pool, mail{})
	if n := work(t, c); n != 1 {
		t.Fatalf("Work ran %d", n)
	}
	if r := get(t, c, e.ID); r.State != pgjobs.StateAvailable || r.Attempt != 1 || r.LastError != "panic: nil map" {
		t.Fatalf("the job is %s, attempt %d, %q", r.State, r.Attempt, r.LastError)
	}
	if logs := w.logs.String(); !strings.Contains(logs, "pgjobs: panic in a job's handler") || !strings.Contains(logs, "service_test.go") {
		t.Fatalf("the panic was not logged with its stack:\n%s", logs)
	}
}

func TestArgumentsThatDoNotDecodeFailTheJobAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "n", func(context.Context, pgjobs.Job[int]) error { return nil })
	e := enqueue(t, k, w.pool, 1)
	w.pool.Exec(ctx, `UPDATE "`+w.table+`" SET args = '"text"'`)
	work(t, c)
	if r := get(t, c, e.ID); r.State != pgjobs.StateFailed || !strings.Contains(r.LastError, "the arguments do not decode into int") {
		t.Fatalf("the job is %s, %q", r.State, r.LastError)
	}
}

func TestAJobOfAKindThisInstanceDoesNotKnowIsLeftForOneThatDoes(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	old, upgraded := w.client(pgjobs.Options{}), w.client(pgjobs.Options{})
	pgjobs.Register(old, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	k := pgjobs.Register(upgraded, "export", func(context.Context, pgjobs.Job[mail]) error { return nil })
	e := enqueue(t, k, w.pool, mail{})
	if n := work(t, old); n != 0 {
		t.Fatalf("an instance ran %d jobs of a kind it does not know", n)
	}
	if r := get(t, old, e.ID); r.State != pgjobs.StateAvailable || r.Attempt != 0 {
		t.Fatalf("the job is %s, attempt %d", r.State, r.Attempt)
	}
	if n := work(t, upgraded); n != 1 {
		t.Fatalf("the instance that knows the kind ran %d", n)
	}
}

type recorder struct {
	mu     sync.Mutex
	name   string
	events *[]string
	trace  bites.Trace
}

func (r *recorder) Start(ctx context.Context, job pgjobs.Record) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, r.name+" start "+job.Kind+fmt.Sprintf(" #%d", job.Attempt))
	r.trace, _ = bites.TraceFrom(ctx)
	return context.WithValue(ctx, r, r.name)
}

func (r *recorder) End(ctx context.Context, job pgjobs.Record, out pgjobs.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, fmt.Sprintf("%s end %s %v ctx=%v", r.name, out.State, out.Err, ctx.Value(r)))
}

func TestObserversSeeEachAttemptOutermostFirstWithTheTraceOfTheRequestThatEnqueuedIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var events []string
	outer, inner := &recorder{name: "outer", events: &events}, &recorder{name: "inner", events: &events}
	c := w.client(pgjobs.Options{Observers: []pgjobs.Observer{outer, inner}})
	var inHandler bites.Trace
	k := pgjobs.Register(c, "send", func(ctx context.Context, job pgjobs.Job[mail]) error {
		inHandler, _ = bites.TraceFrom(ctx)
		if job.Attempt == 1 {
			return errors.New("first try fails")
		}
		return nil
	}, pgjobs.KindOptions{Backoff: func(int) time.Duration { return 0 }})

	type signup struct{ Body mail }
	app := bites.New(bites.WithTraceContext(bites.TraceContext{}))
	var reqTrace bites.Trace
	app.Post("/signup", func(ctx context.Context, in signup) (bites.Created[mail], error) {
		reqTrace, _ = bites.TraceFrom(ctx)
		_, err := k.Enqueue(ctx, w.pool, in.Body)
		return bites.Created[mail]{Body: in.Body}, err
	})
	srv := bitetest.Start(t, app)
	resp := srv.Do(bitetest.Request{Method: "POST", Target: "/signup", Body: []byte(`{"to":"a@b"}`), Header: map[string][]string{
		"Content-Type": {"application/json"},
		"Traceparent":  {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
	}})
	if resp.Status != 201 {
		t.Fatalf("POST /signup: %d %s", resp.Status, resp.Body)
	}
	work(t, c)
	work(t, c)
	want := "outer start send #1|inner start send #1|inner end available first try fails ctx=inner|outer end available first try fails ctx=outer|" +
		"outer start send #2|inner start send #2|inner end completed <nil> ctx=inner|outer end completed <nil> ctx=outer"
	if got := strings.Join(events, "|"); got != want {
		t.Fatalf("the Observers saw\n%s\nwant\n%s", got, want)
	}
	if reqTrace.ID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("the request's trace is %v", reqTrace)
	}
	for name, tr := range map[string]bites.Trace{"the handler": inHandler, "an Observer": outer.trace} {
		if tr.ID != reqTrace.ID || tr.Parent != reqTrace.Span || tr.Span == reqTrace.Span {
			t.Errorf("%s sees trace %+v, want a child of the request's span %+v", name, tr, reqTrace)
		}
	}
	if r := must(c.List(ctx, pgjobs.ListOptions{})); r[0].Trace.ID != reqTrace.ID || r[0].Trace.Parent != reqTrace.Span {
		t.Fatalf("the stored trace is %+v", r[0].Trace)
	}
}

// The way an application test uses the module: a request enqueues in its transaction, Work runs the
// job at once, and the test reads what it did, with no Service and no sleep.
func TestAnApplicationTestRunsTheJobsARequestEnqueuedWithWorkAndNoSleep(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	clk := bitetest.NewClock()
	jobs := w.client(pgjobs.Options{Clock: clk})
	var sent []string
	welcome := pgjobs.Register(jobs, "welcome", func(_ context.Context, job pgjobs.Job[mail]) error {
		sent = append(sent, job.Args.To)
		return nil
	})
	reminder := pgjobs.Register(jobs, "reminder", func(_ context.Context, job pgjobs.Job[mail]) error {
		sent = append(sent, "reminder:"+job.Args.To)
		return nil
	})
	type signup struct{ Body mail }
	app := bites.New(bites.WithClock(clk))
	app.Post("/signup", func(ctx context.Context, in signup) (bites.Created[mail], error) {
		tx, err := w.pool.Begin(ctx)
		if err != nil {
			return bites.Created[mail]{}, err
		}
		defer tx.Rollback(ctx)
		if _, err := welcome.Enqueue(ctx, tx, in.Body); err != nil {
			return bites.Created[mail]{}, err
		}
		if _, err := reminder.Enqueue(ctx, tx, in.Body, pgjobs.EnqueueOptions{Delay: 24 * time.Hour}); err != nil {
			return bites.Created[mail]{}, err
		}
		return bites.Created[mail]{Body: in.Body}, tx.Commit(ctx)
	})
	srv := bitetest.Start(t, app)
	srv.Do(bitetest.Request{Method: "POST", Target: "/signup", Body: []byte(`{"to":"ada@example.com"}`), Header: map[string][]string{"Content-Type": {"application/json"}}})
	if n := work(t, jobs); n != 1 || strings.Join(sent, ",") != "ada@example.com" {
		t.Fatalf("Work ran %d, sent %v", n, sent)
	}
	clk.Advance(24 * time.Hour)
	if n := work(t, jobs); n != 1 || sent[1] != "reminder:ada@example.com" {
		t.Fatalf("a day later Work ran %d, sent %v", n, sent)
	}
}

func TestCancelStopsAnAvailableJobAndTellsARunningOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{PollEvery: 20 * time.Millisecond})
	running := make(chan pgjobs.ID, 1)
	var cause atomic.Value
	k := pgjobs.Register(c, "long", func(ctx context.Context, job pgjobs.Job[mail]) error {
		running <- job.ID
		<-ctx.Done()
		cause.Store(context.Cause(ctx))
		return nil // what it returns after the cancel is not recorded
	})
	waiting := enqueue(t, k, w.pool, mail{}, pgjobs.EnqueueOptions{Delay: time.Hour})
	if err := c.Cancel(ctx, waiting.ID); err != nil {
		t.Fatal(err)
	}
	if r := get(t, c, waiting.ID); r.State != pgjobs.StateCancelled || r.FinishedAt.IsZero() {
		t.Fatalf("a cancelled job is %s", r.State)
	}
	serve(t, c)
	e := enqueue(t, k, w.pool, mail{})
	<-running
	if err := c.Cancel(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the handler to see the cancel", func() bool { return cause.Load() != nil })
	if got := cause.Load().(error); !errors.Is(got, pgjobs.ErrLost) {
		t.Fatalf("the running job's ctx was cancelled with %v, want ErrLost", got)
	}
	waitFor(t, "the attempt to end", func() bool { return strings.Contains(w.logs.String(), "was no longer this worker's") })
	if r := get(t, c, e.ID); r.State != pgjobs.StateCancelled {
		t.Fatalf("the cancelled running job ended %s: its handler's nil was recorded", r.State)
	}
	if err := c.Cancel(ctx, e.ID); !errors.Is(err, pgjobs.ErrFinished) {
		t.Fatalf("a second Cancel: %v, want ErrFinished", err)
	}
	if err := c.Cancel(ctx, pgjobs.ID{1}); !errors.Is(err, pgjobs.ErrNotFound) {
		t.Fatalf("Cancel of no job: %v, want ErrNotFound", err)
	}
}

func TestTheServiceRefusesToStartTwiceAndRegistrationClosesWhenItStarts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	serve(t, c)
	if err := c.Service().Start(ctx); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("a second Start: %v", err)
	}
	mustPanic(t, "after start", "the Client's Service has started", func() {
		pgjobs.Register(c, "late", func(context.Context, pgjobs.Job[mail]) error { return nil })
	})
	mustPanic(t, "schedule after start", "the Client's Service has started", func() { k.Every("late", time.Hour, mail{}) })
}

func TestALeaseThatRunsOutOnTheLastAttemptFailsTheJobNamingWhy(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	var runs atomic.Int32
	k := pgjobs.Register(c, "once", func(context.Context, pgjobs.Job[mail]) error { runs.Add(1); return nil }, pgjobs.KindOptions{MaxAttempts: 1})
	e := enqueue(t, k, w.pool, mail{})
	// What a worker that died during its claim leaves: running, its one attempt counted, the lease past.
	if _, err := w.pool.Exec(ctx, `UPDATE "`+w.table+`" SET state = 'running', attempt = 1, claim = gen_random_uuid(), lease_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if n := work(t, c); n != 0 || runs.Load() != 0 {
		t.Fatal("a job whose last attempt was spent ran again")
	}
	if r := get(t, c, e.ID); r.State != pgjobs.StateFailed || r.FinishedAt.IsZero() || !strings.Contains(r.LastError, "the lease ran out during attempt 1") {
		t.Fatalf("the job is %s, %q", r.State, r.LastError)
	}
}

func TestIDsMadeAtOneInstantStillSortInTheOrderTheyWereMade(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{Clock: bitetest.NewClock()})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	var last string
	for range 200 {
		id := enqueue(t, k, w.pool, mail{}).ID.String()
		if id <= last {
			t.Fatalf("%s after %s", id, last)
		}
		last = id
	}
}
