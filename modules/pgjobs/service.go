package pgjobs

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nevindra/bites"
)

// Service returns the bites.Service that runs the jobs on this instance: register it with
// app.Service after the pool's Service and before what the jobs need to outlive. It is named
// "pgjobs" ("pgjobs-<table>" for a table other than the default), and the Client serves one at a
// time.
//
// Start returns at once. From then on the Service claims the due jobs of each queue up to its
// number of workers, as soon as a notification says one was committed and at least every
// PollEvery; extends the lease of each running job every third of Lease; and every PollEvery takes
// back the jobs whose lease ran out, enqueues the periodic jobs whose period has come, and once a
// minute deletes the jobs past KeepCompleted and KeepFailed. Registration is closed once it has
// started.
//
// BeforeShutdown stops the claiming while the app still serves. Stop waits for the running jobs to
// return, until two seconds before its context's deadline (at once under Close); then it cancels
// the jobs still running (cause ErrStopping), waits up to one second more, and puts back each job
// whose handler returned, without counting the attempt, so another instance takes it at once. A
// handler that ignores its ctx keeps its job until the lease runs out, and Stop logs its id.
func (c *Client) Service() bites.Service {
	name := "pgjobs"
	if c.table != DefaultTable {
		name += "-" + c.table
	}
	current := func() *runner {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.run
	}
	return bites.Service{
		Name:  name,
		Start: c.start,
		BeforeShutdown: func(context.Context) error {
			if r := current(); r != nil {
				r.leave()
			}
			return nil
		},
		Stop: func(ctx context.Context) error {
			r := current()
			if r == nil {
				return nil
			}
			err := r.stop(ctx)
			c.mu.Lock()
			c.run = nil
			c.mu.Unlock()
			return err
		},
	}
}

// runner is a started Service.
type runner struct {
	c      *Client
	life   context.Context         // the Service's life, cancelled by the app just before Stop
	vals   context.Context         // the life's values (the app's logger and clock), never cancelled
	base   context.Context         // the jobs' parent: cancelled only when Stop gives up waiting
	cancel context.CancelCauseFunc // cancels base
	kinds  []string
	crons  []bites.Service

	wake      chan struct{}
	leaving   chan struct{}
	leaveOnce sync.Once
	loops     sync.WaitGroup // the claim loop and the housekeeping loop
	jobs      sync.WaitGroup // the running attempts
	hbStop    chan struct{}
	hbDone    chan struct{}
	unsub     func()

	mu   sync.Mutex
	held map[ID]*held
	free map[string]int  // the workers of each queue not running a job
	more map[string]bool // the last claim of the queue took all it asked for
}

// held is a job this instance runs.
type held struct {
	claim  [16]byte
	until  time.Time // the end of its lease as last written
	cancel context.CancelCauseFunc
}

func (c *Client) start(life context.Context) error {
	c.mu.Lock()
	if c.run != nil {
		c.mu.Unlock()
		return errors.New("pgjobs: the Service is already started: register one Service per Client")
	}
	c.started = true
	r := &runner{
		c: c, life: life, vals: context.WithoutCancel(life), kinds: c.kindNamesLocked(), crons: slices.Clone(c.crons),
		wake: make(chan struct{}, 1), leaving: make(chan struct{}), hbStop: make(chan struct{}), hbDone: make(chan struct{}),
		held: map[ID]*held{}, free: map[string]int{}, more: map[string]bool{},
	}
	r.base, r.cancel = context.WithCancelCause(r.vals)
	for q, n := range c.opt.Queues {
		r.free[q] = n
	}
	c.run = r
	c.mu.Unlock()
	if c.opt.Log == nil {
		c.appLog.Store(bites.Logger(life))
	}
	for i, svc := range r.crons {
		if err := svc.Start(life); err != nil {
			for _, started := range r.crons[:i] {
				_ = started.Stop(context.Background())
			}
			c.mu.Lock()
			c.run = nil
			c.mu.Unlock()
			return fmt.Errorf("pgjobs: the schedule %s did not start: %w", svc.Name, err)
		}
	}
	if c.opt.Notify != nil {
		r.unsub = c.opt.Notify.Subscribe(c.topic, func([]byte) { r.poke() }, r.poke)
	}
	r.loops.Add(2)
	go r.claimLoop()
	go r.houseLoop()
	go r.heartbeatLoop()
	queues := make([]string, 0, len(c.queues))
	for _, q := range c.queues {
		queues = append(queues, fmt.Sprintf("%s=%d", q, c.opt.Queues[q]))
	}
	c.log().Info("pgjobs: working", "table", c.table, "queues", strings.Join(queues, ","), "kinds", strings.Join(r.kinds, ","),
		"notify", c.opt.Notify != nil, "poll", c.opt.PollEvery, "lease", c.opt.Lease)
	return nil
}

// poke wakes the claim loop; a wake already pending is enough.
func (r *runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// leave stops the claiming and the wake-ups; the running jobs go on.
func (r *runner) leave() {
	r.leaveOnce.Do(func() {
		close(r.leaving)
		if r.unsub != nil {
			r.unsub()
		}
	})
}

func (r *runner) isLeaving() bool {
	select {
	case <-r.leaving:
		return true
	default:
		return false
	}
}

func (r *runner) stop(ctx context.Context) error {
	r.leave()
	r.loops.Wait()
	done := make(chan struct{})
	go func() { r.jobs.Wait(); close(done) }()
	var giveUp <-chan time.Time
	if dl, ok := ctx.Deadline(); ok {
		t := time.NewTimer(max(time.Until(dl)-stopReserve, 0))
		defer t.Stop()
		giveUp = t.C
	}
	finished := false
	select {
	case <-done:
		finished = true
	case <-giveUp:
	case <-ctx.Done():
	}
	if !finished {
		r.mu.Lock()
		n := len(r.held)
		r.mu.Unlock()
		r.c.log().Info("pgjobs: stopping: cancelling the jobs still running so they can be put back", "running", n)
		r.cancel(ErrStopping)
		select {
		case <-done:
		case <-time.After(cancelWait):
			r.mu.Lock()
			for id := range r.held {
				r.c.log().Warn("pgjobs: a job did not return after its ctx was cancelled at shutdown; it is left to its lease and runs again once that has run out", "id", id.String(), "lease", r.c.opt.Lease)
			}
			r.mu.Unlock()
		}
	}
	close(r.hbStop)
	<-r.hbDone
	r.cancel(ErrStopping)
	var errs []error
	for _, svc := range r.crons {
		if err := svc.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ticker sends on the returned channel every d on the Client's clock, until stop is called.
func (c *Client) ticker(d time.Duration) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	var mu sync.Mutex
	stopped := false
	var t timer
	var fire func()
	fire = func() {
		select {
		case ch <- struct{}{}:
		default:
		}
		mu.Lock()
		if !stopped {
			t = c.afterFunc(d, fire)
		}
		mu.Unlock()
	}
	mu.Lock()
	t = c.afterFunc(d, fire)
	mu.Unlock()
	return ch, func() {
		mu.Lock()
		stopped = true
		t.Stop()
		mu.Unlock()
	}
}

func (r *runner) claimLoop() {
	defer r.loops.Done()
	tick, stop := r.c.ticker(r.c.opt.PollEvery)
	defer stop()
	for {
		r.claimAll()
		select {
		case <-r.leaving:
			return
		case <-r.life.Done():
			return
		case <-tick:
		case <-r.wake:
		}
	}
}

// claimAll claims, in each queue with a free worker, as many due jobs as it has free workers, and
// starts them.
func (r *runner) claimAll() {
	for _, q := range r.c.queues {
		if r.isLeaving() || r.life.Err() != nil {
			return
		}
		r.mu.Lock()
		n := min(r.free[q], maxBatch)
		r.mu.Unlock()
		if n <= 0 {
			continue
		}
		// Not the life's ctx: a claim cut off after Postgres ran it would leave its jobs to the
		// lease. It is bounded instead.
		ctx, cancel := context.WithTimeout(r.vals, writeWait)
		recs, claim, err := r.c.claim(ctx, q, n, r.kinds, nil)
		cancel()
		if err != nil {
			r.c.log().Warn("pgjobs: claiming jobs failed; trying again at the next poll", "queue", q, "err", err)
			continue
		}
		r.mu.Lock()
		r.more[q] = len(recs) == n
		r.mu.Unlock()
		for _, rec := range recs {
			r.begin(rec, claim)
		}
	}
}

// begin runs one claimed job on its own goroutine.
func (r *runner) begin(rec Record, claim [16]byte) {
	k := r.c.kindOf(rec.Kind)
	ctx, cancel := context.WithCancelCause(r.base)
	r.mu.Lock()
	r.held[rec.ID] = &held{claim: claim, until: rec.LeaseUntil, cancel: cancel}
	r.free[rec.Queue]--
	r.mu.Unlock()
	r.jobs.Add(1)
	go func() {
		defer r.jobs.Done()
		r.c.attempt(ctx, r.base, k, rec, claim)
		cancel(nil)
		r.mu.Lock()
		delete(r.held, rec.ID)
		r.free[rec.Queue]++
		more := r.more[rec.Queue]
		r.mu.Unlock()
		if more {
			r.poke()
		}
	}()
}

// lose cancels the job id names with ErrLost when this instance runs it.
func (r *runner) lose(id ID) {
	r.mu.Lock()
	h := r.held[id]
	r.mu.Unlock()
	if h != nil {
		h.cancel(ErrLost)
	}
}

func (r *runner) heartbeatLoop() {
	defer close(r.hbDone)
	tick, stop := r.c.ticker(r.c.opt.Lease / 3)
	defer stop()
	for {
		select {
		case <-r.hbStop:
			return
		case <-tick:
		}
		r.beat()
	}
}

// beat extends the lease of every job this instance runs, in one statement. A job whose row no
// longer carries its claim (cancelled, or taken after its lease ran out) is cancelled with ErrLost,
// and so is one whose lease has run out while the statement failed.
func (r *runner) beat() {
	r.mu.Lock()
	ids := make([][16]byte, 0, len(r.held))
	claims := make([][16]byte, 0, len(r.held))
	for id, h := range r.held {
		ids = append(ids, [16]byte(id))
		claims = append(claims, h.claim)
	}
	r.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	now := r.c.now()
	until := now.Add(r.c.opt.Lease)
	ctx, cancel := context.WithTimeout(r.vals, r.c.opt.Lease/3)
	defer cancel()
	rows, err := r.c.opt.Pool.Query(ctx, r.c.q.heartbeat, until, ids, claims)
	var kept []ID
	if err == nil {
		kept, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ID, error) {
			var id [16]byte
			err := row.Scan(&id)
			return ID(id), err
		})
	}
	var lost []*held
	r.mu.Lock()
	if err != nil {
		for id, h := range r.held {
			if !h.until.After(now) {
				lost = append(lost, h)
				r.c.log().Warn("pgjobs: the heartbeat failed and the job's lease has run out, so another worker may take it; its ctx is cancelled", "id", id.String(), "err", err)
			}
		}
	} else {
		for _, id := range ids {
			h := r.held[ID(id)]
			if h == nil {
				continue
			}
			if slices.Contains(kept, ID(id)) {
				h.until = until
			} else {
				lost = append(lost, h)
			}
		}
	}
	r.mu.Unlock()
	if err != nil {
		r.c.log().Warn("pgjobs: the heartbeat failed; trying again in a third of the lease", "jobs", len(ids), "err", err)
	}
	for _, h := range lost {
		h.cancel(ErrLost)
	}
}

func (r *runner) houseLoop() {
	defer r.loops.Done()
	tick, stop := r.c.ticker(r.c.opt.PollEvery)
	defer stop()
	nextPrune := r.c.now()
	for {
		ctx, cancel := context.WithTimeout(r.vals, writeWait)
		if _, err := r.c.rescue(ctx); err != nil {
			r.c.log().Warn("pgjobs: taking back the jobs whose lease ran out failed; trying again at the next poll", "err", err)
		}
		if err := r.c.enqueuePeriodic(ctx); err != nil {
			r.c.log().Warn("pgjobs: enqueueing a periodic job failed; trying again at the next poll", "err", err)
		}
		if now := r.c.now(); !now.Before(nextPrune) {
			nextPrune = now.Add(pruneEvery)
			if _, err := r.c.Prune(ctx); err != nil {
				r.c.log().Warn("pgjobs: deleting the old jobs failed; trying again in a minute", "err", err)
			}
		}
		cancel()
		select {
		case <-r.leaving:
			return
		case <-r.life.Done():
			return
		case <-tick:
		}
	}
}

// claim takes up to n due jobs of queue whose kind is one of kinds and none of skip, for a lease,
// under one claim token.
func (c *Client) claim(ctx context.Context, queue string, n int, kinds []string, skip [][16]byte) ([]Record, [16]byte, error) {
	now := c.now()
	token := [16]byte(newID(now))
	if len(kinds) == 0 {
		return nil, token, nil
	}
	if skip == nil {
		skip = [][16]byte{}
	}
	rows, err := c.opt.Pool.Query(ctx, c.q.claim, now, now.Add(c.opt.Lease), token, queue, kinds, n, skip)
	if err != nil {
		return nil, token, err
	}
	recs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) { return scanRecord(row) })
	return recs, token, err
}

// attempt runs one claimed job with the handler of k and records what came of it. ctx is the job's
// own (Cancel and a lost lease cancel it); stopping is done when the caller stops waiting for it,
// which makes a failed attempt a release.
func (c *Client) attempt(ctx, stopping context.Context, k *kind, rec Record, claim [16]byte) Outcome {
	jctx, cancel := c.withTimeout(ctx, k.timeout)
	defer cancel()
	cur := jctx
	if rec.Trace.ID != (bites.TraceID{}) {
		// The attempt is a span of the trace that enqueued it: TraceFrom(ctx) in the handler and in
		// an Observer is a child of the request's span, as a request's own Trace is of its caller's.
		cur = bites.ContextWithTrace(cur, bites.Trace{ID: rec.Trace.ID, Span: rec.Trace.Parent, Flags: rec.Trace.Flags}.Child())
	}
	ends := make([]context.Context, len(c.opt.Observers))
	for i, ob := range c.opt.Observers {
		cur = c.observeStart(ob, cur, rec)
		ends[i] = cur
	}
	began := time.Now()
	err, panicked := c.call(cur, k, rec)
	took := time.Since(began)
	if err != nil && errors.Is(context.Cause(jctx), context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("the attempt ran past its timeout of %v: %w", k.timeout, err)
	}
	out := c.record(stopping, k, rec, claim, err)
	out.Panicked, out.Took = panicked, took
	for i := len(c.opt.Observers) - 1; i >= 0; i-- {
		c.observeEnd(c.opt.Observers[i], ends[i], rec, out)
	}
	return out
}

// withTimeout is ctx cancelled after d on the Client's clock, with context.DeadlineExceeded as its
// cause.
func (c *Client) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if c.clk == nil {
		return context.WithTimeout(ctx, d)
	}
	cctx, cancel := context.WithCancelCause(ctx)
	t := c.afterFunc(d, func() { cancel(context.DeadlineExceeded) })
	return cctx, func() { t.Stop(); cancel(context.Canceled) }
}

// call runs the handler, a panic recovered into an error and logged with its stack.
func (c *Client) call(ctx context.Context, k *kind, rec Record) (err error, panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			c.log().Error("pgjobs: panic in a job's handler", "id", rec.ID.String(), "kind", rec.Kind, "attempt", rec.Attempt, "panic", v, "stack", string(debug.Stack()))
			err, panicked = fmt.Errorf("panic: %v", v), true
		}
	}()
	return k.run(ctx, rec), false
}

func (c *Client) observeStart(ob Observer, ctx context.Context, rec Record) (out context.Context) {
	defer func() {
		if v := recover(); v != nil {
			c.log().Error("pgjobs: panic in an Observer's Start; the job runs without it", "observer", fmt.Sprintf("%T", ob), "panic", v)
			out = ctx
		}
	}()
	if out = ob.Start(ctx, rec); out == nil {
		out = ctx
	}
	return out
}

func (c *Client) observeEnd(ob Observer, ctx context.Context, rec Record, o Outcome) {
	defer func() {
		if v := recover(); v != nil {
			c.log().Error("pgjobs: panic in an Observer's End", "observer", fmt.Sprintf("%T", ob), "panic", v)
		}
	}()
	ob.End(ctx, rec, o)
}

// record writes what an attempt came to, guarded by its claim, so a worker that lost the job
// writes nothing.
func (c *Client) record(stopping context.Context, k *kind, rec Record, claim [16]byte, err error) Outcome {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(stopping), writeWait)
	defer cancel()
	now := c.now()
	out := Outcome{Err: err}
	id := [16]byte(rec.ID)
	var snz *snoozeError
	var fl *failError
	failed := errors.As(err, &fl)
	if errors.As(err, &snz) && snz.d < 0 {
		err = fmt.Errorf("pgjobs.Snooze(%v): a snooze is a time to wait; give zero or more", snz.d)
		out.Err, snz = err, nil
	}
	var tag pgconn.CommandTag
	var werr error
	switch {
	case err == nil:
		out.State = StateCompleted
		tag, werr = c.opt.Pool.Exec(ctx, c.q.complete, id, claim, now)
	case stopping.Err() != nil && !failed:
		out.State, out.Released, out.Next = StateAvailable, true, now
		tag, werr = c.opt.Pool.Exec(ctx, c.q.release, id, claim, now)
	case snz != nil:
		out.State, out.Snoozed, out.Next = StateAvailable, true, now.Add(snz.d)
		tag, werr = c.opt.Pool.Exec(ctx, c.q.snooze, id, claim, out.Next)
	case failed || rec.Attempt >= rec.MaxAttempts:
		out.State = StateFailed
		tag, werr = c.opt.Pool.Exec(ctx, c.q.fail, id, claim, now, clean(err.Error()))
	default:
		out.State, out.Next = StateAvailable, now.Add(max(k.backoff(rec.Attempt), 0))
		tag, werr = c.opt.Pool.Exec(ctx, c.q.retry, id, claim, out.Next, clean(err.Error()))
	}
	log := c.log().With("id", rec.ID.String(), "kind", rec.Kind, "queue", rec.Queue, "attempt", rec.Attempt)
	switch {
	case werr != nil:
		log.Error("pgjobs: what the attempt came to could not be recorded; the job runs again once its lease has run out", "outcome", out.State, "err", werr)
		out.State = StateRunning
	case tag.RowsAffected() == 0:
		out.Lost = true
		out.State, _ = c.stateOf(ctx, rec.ID)
		log.Warn("pgjobs: the job was no longer this worker's when its attempt ended, so nothing was recorded", "state", out.State, "handler_err", err)
	case out.Released:
		log.Info("pgjobs: the job was put back at shutdown; the attempt is not counted")
	case out.State == StateCompleted:
		log.Debug("pgjobs: job completed")
	case out.Snoozed:
		log.Debug("pgjobs: job snoozed", "next", out.Next)
	case out.State == StateFailed:
		log.Error("pgjobs: job failed for good", "max_attempts", rec.MaxAttempts, "err", err)
	default:
		log.Warn("pgjobs: job failed; it is tried again later", "max_attempts", rec.MaxAttempts, "next", out.Next, "err", err)
	}
	return out
}

// rescue takes back the jobs whose lease ran out: each is available again, or failed when that was
// its last attempt, and is logged.
func (c *Client) rescue(ctx context.Context) (int, error) {
	total := 0
	for {
		rows, err := c.opt.Pool.Query(ctx, c.q.rescue, c.now())
		if err != nil {
			return total, fmt.Errorf("pgjobs: rescue: %w", err)
		}
		var id [16]byte
		var kind, queue, state string
		var attempt int
		n := 0
		_, err = pgx.ForEachRow(rows, []any{&id, &kind, &queue, &state, &attempt}, func() error {
			n++
			c.log().Warn("pgjobs: a job's lease ran out: the instance that ran it stopped without finishing it", "id", ID(id).String(), "kind", kind, "queue", queue, "attempt", attempt, "now", state)
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("pgjobs: rescue: %w", err)
		}
		total += n
		if n < pruneBatch {
			return total, nil
		}
	}
}

// Prune deletes the completed jobs older than KeepCompleted and the failed and cancelled ones older
// than KeepFailed (by when they finished, on the Client's clock), in batches of 1000, and returns
// how many. The Service runs it once a minute; call it directly to prune at a moment of the
// application's choosing. Several instances may prune at once: a row being deleted by one is
// skipped by the others.
func (c *Client) Prune(ctx context.Context) (int, error) {
	now := c.now()
	total := 0
	for _, p := range []struct {
		state State
		keep  time.Duration
	}{{StateCompleted, c.opt.KeepCompleted}, {StateFailed, c.opt.KeepFailed}, {StateCancelled, c.opt.KeepFailed}} {
		for {
			tag, err := c.opt.Pool.Exec(ctx, c.q.prune, string(p.state), now.Add(-p.keep))
			if err != nil {
				return total, fmt.Errorf("pgjobs: prune %s jobs: %w (%d deleted before it failed)", p.state, err, total)
			}
			n := int(tag.RowsAffected())
			total += n
			if n < pruneBatch {
				break
			}
		}
	}
	if total > 0 {
		c.log().Info("pgjobs: deleted the jobs past their retention", "table", c.table, "deleted", total)
	}
	return total, nil
}

// Work runs, on the caller's goroutine and one at a time, every job that is due on the Client's
// clock in the queues of Options.Queues, until none is due, and returns how many attempts it ran.
// Before it looks it takes back the jobs whose lease ran out and enqueues the periodic jobs whose
// period has come, as the Service does. The jobs a run enqueues are run too when they are due; a
// job is attempted at most once by one call, so a failure with no back-off or a zero snooze does
// not loop.
//
// It is for a test, which then needs no sleep and no Service (enqueue, call Work, look at what the
// job did), and for a tool that drains a queue. It runs beside a started Service, which may take
// some of the jobs first. Its attempts have the kind's timeout and no heartbeat. Cancelling ctx
// cancels the running job, which is put back without counting the attempt, and Work returns the
// count so far with ctx's error.
func (c *Client) Work(ctx context.Context) (int, error) {
	if _, err := c.rescue(ctx); err != nil {
		return 0, err
	}
	if err := c.enqueuePeriodic(ctx); err != nil {
		return 0, err
	}
	c.mu.Lock()
	kinds := c.kindNamesLocked()
	c.mu.Unlock()
	var seen [][16]byte
	n := 0
	for {
		ran := false
		for _, q := range c.queues {
			recs, claim, err := c.claim(ctx, q, 1, kinds, seen)
			if err != nil {
				return n, fmt.Errorf("pgjobs: work: %w", err)
			}
			for _, rec := range recs {
				c.attempt(ctx, ctx, c.kindOf(rec.Kind), rec, claim)
				seen = append(seen, [16]byte(rec.ID))
				n++
				ran = true
			}
		}
		if !ran || ctx.Err() != nil {
			return n, ctx.Err()
		}
	}
}

func (c *Client) kindOf(name string) *kind {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kinds[name]
}

func (c *Client) kindNamesLocked() []string {
	names := make([]string, 0, len(c.kinds))
	for n := range c.kinds {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
