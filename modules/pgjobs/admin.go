package pgjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nevindra/bites"
)

// State is where a job stands.
type State string

// The states of a job. A job is available until it is claimed, running while a worker holds its
// lease, and then completed, failed or cancelled; a retry, a snooze, a release at shutdown and a
// lease that ran out make it available again.
const (
	// StateAvailable is a job waiting to run: due, or waiting for its RunAt, a retry's back-off or a
	// snooze.
	StateAvailable State = "available"
	// StateRunning is a job a worker holds under a lease.
	StateRunning State = "running"
	// StateCompleted is a job whose handler returned nil. It is kept for Options.KeepCompleted.
	StateCompleted State = "completed"
	// StateFailed is a job that spent its attempts or was failed with Fail, kept with its last error
	// for Options.KeepFailed.
	StateFailed State = "failed"
	// StateCancelled is a job cancelled with Cancel, kept for Options.KeepFailed.
	StateCancelled State = "cancelled"
)

// Record is a job of any kind as it is stored, its arguments as raw JSON: what List and Get return
// and what an Observer sees.
type Record struct {
	ID          ID
	Kind        string
	Queue       string
	State       State
	Args        []byte // the arguments as JSON, as stored
	Priority    int
	Attempt     int // the attempts made so far, the one running included
	MaxAttempts int
	UniqueKey   string    // empty when none
	ScheduledAt time.Time // when it was first due; a retry or a snooze does not change it
	RunAt       time.Time // when it is next due, or was last due
	CreatedAt   time.Time
	AttemptedAt time.Time // when the last attempt started; zero before the first
	FinishedAt  time.Time // when it completed, failed or was cancelled; zero before
	LeaseUntil  time.Time // while it runs, when its lease ends; zero otherwise
	LastError   string    // the error of the last failed attempt; empty when none
	// Trace is the trace context the job carries from the request that enqueued it (bites.TraceFrom
	// of Enqueue's ctx), as bites.ParseTraceparent reads it: the trace ID, the enqueuing request's
	// span as Parent, and the flags; the zero Trace when there was none. The tracestate is not
	// carried. Each attempt's ctx carries the same trace with a new span of its own, so
	// bites.TraceFrom in the handler and in an Observer has the shape of a request's trace (Span its
	// own, Parent the caller's), which is what the otel module reads.
	Trace bites.Trace
}

// Observer sees each attempt of a job: Start before the handler, End after the attempt has been
// recorded. It is how an application adds logs, metrics and traces, as a bites.Observer does for a
// request. The first Observer of Options.Observers is the outermost: its Start runs first and its
// End last.
type Observer interface {
	// Start is called with the attempt's ctx and the job as claimed (StateRunning, its Attempt
	// counted), and returns the ctx the handler runs with: the place to start a span. It must not
	// block.
	Start(ctx context.Context, job Record) context.Context
	// End is called with the ctx Start returned, the same job, and what the attempt came to.
	End(ctx context.Context, job Record, out Outcome)
}

// Outcome is what one attempt came to.
type Outcome struct {
	// State is the state the attempt left the job in: completed, available (a retry, a snooze or a
	// release), failed, or cancelled when it was cancelled while it ran. It is empty when the job
	// was no longer this worker's (Lost) and could not be read.
	State State
	// Err is what the handler returned, a panic as an error, or nil when it completed.
	Err error
	// Panicked is true when the handler panicked.
	Panicked bool
	// Snoozed is true when the handler returned Snooze.
	Snoozed bool
	// Released is true when the Service was stopping and put the job back without counting the
	// attempt.
	Released bool
	// Lost is true when the job was no longer this worker's when the attempt ended (cancelled, or
	// its lease had run out and another worker took it), so nothing was recorded.
	Lost bool
	// Next is when the job is due again, for an attempt that left it available.
	Next time.Time
	// Took is how long the handler ran, in real time.
	Took time.Duration
}

// Count is how many jobs of one queue are in one state.
type Count struct {
	Queue string
	State State
	Jobs  int
}

// Counts returns how many jobs each queue has in each state, the pairs with none left out, sorted
// by queue and state. It reads the whole table, so on a table of millions of completed jobs it
// takes as long as a scan of them; KeepCompleted is what keeps it short.
func (c *Client) Counts(ctx context.Context) ([]Count, error) {
	rows, err := c.opt.Pool.Query(ctx, c.q.counts)
	if err != nil {
		return nil, fmt.Errorf("pgjobs: counts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Count, error) {
		var n Count
		var st string
		err := r.Scan(&n.Queue, &st, &n.Jobs)
		n.State = State(st)
		return n, err
	})
	if err != nil {
		return nil, fmt.Errorf("pgjobs: counts: %w", err)
	}
	return out, nil
}

// ListOptions select the jobs List returns. The zero value is the 100 newest jobs.
type ListOptions struct {
	// State, Queue and Kind, when not empty, keep only the jobs that have it.
	State State
	Queue string
	Kind  string
	// Before, when not the zero ID, keeps only the jobs enqueued before the job it names: give the
	// ID of the last job of a page for the next page.
	Before ID
	// Limit is the most jobs returned. Zero means 100; at most 1000.
	Limit int
}

// List returns jobs, newest first, as ListOptions selects them.
func (c *Client) List(ctx context.Context, o ListOptions) ([]Record, error) {
	if o.Limit < 0 || o.Limit > 1000 {
		return nil, fmt.Errorf("pgjobs: list: Limit is %d: give 1 to 1000, or zero for 100", o.Limit)
	}
	if o.Limit == 0 {
		o.Limit = 100
	}
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if o.State != "" {
		add("state = $%d", string(o.State))
	}
	if o.Queue != "" {
		add("queue = $%d", o.Queue)
	}
	if o.Kind != "" {
		add("kind = $%d", o.Kind)
	}
	if o.Before != (ID{}) {
		add("id < $%d", [16]byte(o.Before))
	}
	sql := strings.TrimSuffix(c.q.get, " WHERE id = $1")
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, o.Limit)
	sql += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := c.opt.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("pgjobs: list: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Record, error) { return scanRecord(r) })
	if err != nil {
		return nil, fmt.Errorf("pgjobs: list: %w", err)
	}
	return out, nil
}

// Get returns the job id names, or ErrNotFound.
func (c *Client) Get(ctx context.Context, id ID) (Record, error) {
	r, err := scanRecord(c.opt.Pool.QueryRow(ctx, c.q.get, [16]byte(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("pgjobs: get: %w", err)
	}
	return r, nil
}

// Retry makes the job id names due now with a fresh set of attempts: a failed or cancelled job runs
// again, a completed one runs once more, and an available one stops waiting for its time. Its last
// error is kept until an attempt replaces it. It returns ErrNotFound, ErrRunning for a running job,
// and an error naming the key when the job has a unique key that another available or running job
// of its kind now holds.
func (c *Client) Retry(ctx context.Context, id ID) error {
	var queue string
	err := c.opt.Pool.QueryRow(ctx, c.q.retryJob, [16]byte(id), c.now()).Scan(&queue)
	var pe *pgconn.PgError
	switch {
	case err == nil:
		return c.wake(ctx, c.opt.Pool, queue)
	case errors.As(err, &pe) && pe.Code == "23505":
		return fmt.Errorf("pgjobs: retry %s: another available or running job of its kind holds its unique key; cancel that one first, or let it finish", id)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("pgjobs: retry %s: %w", id, err)
	}
	st, err := c.stateOf(ctx, id)
	if err != nil {
		return err
	}
	if st == StateRunning {
		return ErrRunning
	}
	return fmt.Errorf("pgjobs: retry %s: the job is %s and was not changed; try again", id, st)
}

// Cancel makes the job id names cancelled, so it never runs again: an available job at once; a
// running one is told through its ctx (cause ErrLost) at once when it runs on this instance and at
// its next heartbeat on another, and what its handler returns afterwards is not recorded. It
// returns ErrNotFound, and ErrFinished for a job that has already completed, failed or been
// cancelled.
func (c *Client) Cancel(ctx context.Context, id ID) error {
	var ok bool
	err := c.opt.Pool.QueryRow(ctx, c.q.cancel, [16]byte(id), c.now()).Scan(&ok)
	if err == nil {
		c.mu.Lock()
		r := c.run
		c.mu.Unlock()
		if r != nil {
			r.lose(id)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgjobs: cancel %s: %w", id, err)
	}
	if _, err := c.stateOf(ctx, id); err != nil {
		return err
	}
	return ErrFinished
}

// stateOf is the state of the job id names, or ErrNotFound.
func (c *Client) stateOf(ctx context.Context, id ID) (State, error) {
	var st string
	err := c.opt.Pool.QueryRow(ctx, c.q.state, [16]byte(id)).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("pgjobs: %s: %w", id, err)
	}
	return State(st), nil
}

// scanRecord reads the columns of the queries' cols list.
func scanRecord(row pgx.Row) (Record, error) {
	var r Record
	var id [16]byte
	var state string
	var key, lastErr, trace *string
	var attempted, finished, lease *time.Time
	err := row.Scan(&id, &r.Kind, &r.Queue, &state, &r.Args, &r.Priority, &r.Attempt, &r.MaxAttempts, &key,
		&r.ScheduledAt, &r.RunAt, &r.CreatedAt, &attempted, &finished, &lease, &lastErr, &trace)
	if err != nil {
		return Record{}, err
	}
	r.ID, r.State = ID(id), State(state)
	if key != nil {
		r.UniqueKey = *key
	}
	if lastErr != nil {
		r.LastError = *lastErr
	}
	if trace != nil {
		r.Trace, _ = bites.ParseTraceparent(*trace)
	}
	for _, p := range []struct {
		src *time.Time
		dst *time.Time
	}{{attempted, &r.AttemptedAt}, {finished, &r.FinishedAt}, {lease, &r.LeaseUntil}} {
		if p.src != nil {
			*p.dst = *p.src
		}
	}
	return r, nil
}
