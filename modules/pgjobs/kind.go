package pgjobs

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
)

// Job is one job of a kind as its handler gets it: its arguments decoded into T and where it stands.
type Job[T any] struct {
	// ID is the job's id, the same on every attempt.
	ID ID
	// Kind is the name the kind was registered with.
	Kind string
	// Queue is the queue the job was claimed from.
	Queue string
	// Args are the arguments it was enqueued with, decoded from JSON.
	Args T
	// Attempt is which attempt this is: 1 on the first run. A snooze and a release at shutdown do
	// not count; a lease that ran out does.
	Attempt int
	// MaxAttempts is how many attempts the job has in all; when Attempt equals it and the handler
	// returns an error, the job fails for good.
	MaxAttempts int
	// Priority is the priority it was enqueued with.
	Priority int
	// UniqueKey is the unique key it was enqueued with, empty when none. A periodic job's is its
	// schedule's name, "@" and the Unix seconds of its period.
	UniqueKey string
	// ScheduledAt is when the job was first due: the time it was enqueued, its RunAt or its Delay,
	// or the start of its period for a periodic job. A retry or a snooze does not change it, so it
	// is the value to key an idempotent write on ("the report of this period").
	ScheduledAt time.Time
	// CreatedAt is when it was enqueued.
	CreatedAt time.Time
}

// KindOptions are the settings of one kind, given to Register. The zero value is valid.
type KindOptions struct {
	// Queue is the queue the kind's jobs are enqueued on unless an enqueue names another. It must
	// be one of Options.Queues. Empty means DefaultQueue.
	Queue string
	// MaxAttempts is how many attempts a job of the kind has unless its enqueue says. Zero means
	// DefaultMaxAttempts (20); 1 means it is never tried again.
	MaxAttempts int
	// Timeout is how long one attempt may run: its ctx is cancelled then, with
	// context.DeadlineExceeded, and an attempt that returns an error after it is a failed attempt.
	// Zero means DefaultTimeout (one minute). A job that may take longer says so here; there is no
	// attempt without a timeout.
	Timeout time.Duration
	// Backoff is how long to wait before the next attempt after attempt n failed (n is 1 after the
	// first). Nil means DefaultBackoff. A function that returns a negative time is read as zero.
	Backoff func(attempt int) time.Duration
}

// EnqueueOptions are the settings of one Enqueue. The zero value is valid: due now, on the kind's
// queue, at priority 0, with the kind's attempts and no unique key.
type EnqueueOptions struct {
	// RunAt is when the job becomes due. Zero means now. A time in the past is due at once.
	RunAt time.Time
	// Delay makes the job due this long from now, on the Client's clock. Give RunAt or Delay, not
	// both; a negative Delay is refused.
	Delay time.Duration
	// Queue is the queue the job goes on, one of Options.Queues. Empty means the kind's.
	Queue string
	// Priority orders the due jobs of one queue: a higher one is claimed first, and jobs of one
	// priority by the time they became due, then by id. Between -100 and 100; zero by default.
	Priority int
	// MaxAttempts is how many attempts this job has in all. Zero means the kind's.
	MaxAttempts int
	// UniqueKey, when not empty, makes this Enqueue store nothing while a job of the same kind
	// with the same key is available (due or waiting for its time, a retry included) or running:
	// Enqueue then returns that job's id with Duplicate set. Once the job has completed, failed or
	// been cancelled, the key is free again. At most 512 bytes of UTF-8 with no NUL.
	UniqueKey string
}

// Enqueued is what Enqueue did.
type Enqueued struct {
	// ID is the id of the job stored, or, when Duplicate, of the job that already holds the key.
	ID ID
	// Duplicate is true when a job of the kind with the same UniqueKey was available or running,
	// and nothing was stored.
	Duplicate bool
}

// Kind is a registered kind of job with arguments of type T: the handle Enqueue is called on, so a
// job can only be enqueued for a kind that exists, with arguments of its type. Make one with
// Register.
type Kind[T any] struct {
	c *Client
	k *kind
}

// kind is what the Client keeps of a registered kind.
type kind struct {
	name        string
	queue       string
	maxAttempts int
	timeout     time.Duration
	backoff     func(int) time.Duration
	run         func(ctx context.Context, r Record) error // decodes the arguments and calls the handler
}

// Register adds a kind named name to c, whose jobs carry arguments of type T and are run by handle,
// and returns the Kind to enqueue them with. Register every kind before the Service starts.
//
// handle returns nil when the job is done. Any other error is a failed attempt, tried again after
// the kind's Backoff until its MaxAttempts are spent, after which the job is kept as failed with
// that error; Fail(err) fails it at once, and Snooze(d) runs it again after d without counting an
// attempt. A panic is a failed attempt too, logged with its stack. Delivery is at least once (a
// job whose instance died mid-run is run again), so handle must be idempotent: key what it writes
// on job.ID or job.ScheduledAt.
//
// The arguments are encoded with encoding/json/v2 when enqueued and decoded into T for each
// attempt; a job whose arguments do not decode fails at once with the decoding error.
//
// Register panics, naming the line that called it, when name is not a plain name (letters,
// digits, ".", "_", ":", "-", at most 100 bytes), a kind of that name is registered, handle is
// nil, the zero T cannot be encoded as JSON, the queue is not one of Options.Queues, MaxAttempts
// or Timeout is negative, more than one KindOptions is given, or c's Service has started.
func Register[T any](c *Client, name string, handle func(ctx context.Context, job Job[T]) error, opts ...KindOptions) *Kind[T] {
	where := callerWhere(2)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("pgjobs.Register(%q): %s\n\tregistered at %s", name, fmt.Sprintf(format, args...), where))
	}
	if !plainName.MatchString(name) {
		fail("the name is not a plain name: give letters, digits and \".\", \"_\", \":\", \"-\", starting with a letter or digit, at most 100 bytes, such as \"send-mail\"")
	}
	if handle == nil {
		fail("handle is nil: give the function that runs one job of the kind")
	}
	var o KindOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		fail("%d options values were given; give one KindOptions", len(opts))
	}
	var zero T
	if _, err := json.Marshal(zero); err != nil {
		fail("the arguments type %T cannot be encoded as JSON: %v; give a type of JSON values, such as a struct of strings and numbers", zero, err)
	}
	if o.Queue == "" {
		o.Queue = DefaultQueue
	}
	if _, ok := c.opt.Queues[o.Queue]; !ok {
		fail("queue %q is not one of Options.Queues (%s): add it there, or name one of them", o.Queue, strings.Join(c.queues, ", "))
	}
	if o.MaxAttempts < 0 || o.Timeout < 0 {
		fail("MaxAttempts %d or Timeout %v is negative: give a positive value, or zero for the default", o.MaxAttempts, o.Timeout)
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	k := &kind{name: name, queue: o.Queue, maxAttempts: o.MaxAttempts, timeout: o.Timeout, backoff: o.Backoff}
	k.run = func(ctx context.Context, r Record) error {
		var args T
		if err := json.Unmarshal(r.Args, &args); err != nil {
			return Fail(fmt.Errorf("the arguments do not decode into %T: %w", args, err))
		}
		return handle(ctx, Job[T]{
			ID: r.ID, Kind: r.Kind, Queue: r.Queue, Args: args, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
			Priority: r.Priority, UniqueKey: r.UniqueKey, ScheduledAt: r.ScheduledAt, CreatedAt: r.CreatedAt,
		})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		fail("the Client's Service has started: register every kind before the app serves")
	}
	if _, dup := c.kinds[name]; dup {
		fail("a kind of this name is registered: a name is one kind; give the second its own")
	}
	c.kinds[name] = k
	return &Kind[T]{c: c, k: k}
}

// Name is the name the kind was registered with.
func (k *Kind[T]) Name() string { return k.k.name }

// Enqueue stores a job of the kind with args, through q. Give the caller's pgx.Tx so the job is
// stored if and only if the transaction commits (a job enqueued in a transaction that rolls back
// never runs), or the pool for a job on its own. With Options.Notify, a job due now also sends the
// wake-up in q, so the workers of every instance hear of it when it commits.
//
// With a UniqueKey that a job of the kind holds (available or running), nothing is stored and the
// result names that job, with Duplicate set. Enqueue returns an error, and stores nothing, for a
// nil q, args that do not encode as JSON or are over MaxArgs, both RunAt and Delay, a negative
// Delay or MaxAttempts, a priority outside -100 to 100, a queue that is not one of Options.Queues,
// a unique key over MaxUniqueKey or not UTF-8, more than one EnqueueOptions, and an error of the
// database (which, in a transaction, has aborted it).
func (k *Kind[T]) Enqueue(ctx context.Context, q Querier, args T, opts ...EnqueueOptions) (Enqueued, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return Enqueued{}, fmt.Errorf("pgjobs: enqueue %s: the arguments do not encode as JSON: %w", k.k.name, err)
	}
	var o EnqueueOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		return Enqueued{}, fmt.Errorf("pgjobs: enqueue %s: %d options values were given; give one EnqueueOptions", k.k.name, len(opts))
	}
	return k.c.enqueue(ctx, q, k.k, b, o)
}

// enqueue stores a job of k with the encoded args through q.
func (c *Client) enqueue(ctx context.Context, q Querier, k *kind, args []byte, o EnqueueOptions) (Enqueued, error) {
	bad := func(format string, a ...any) (Enqueued, error) {
		return Enqueued{}, fmt.Errorf("pgjobs: enqueue %s: %s", k.name, fmt.Sprintf(format, a...))
	}
	switch {
	case q == nil:
		return bad("q is nil: give the transaction the job belongs to, or the pool")
	case len(args) > MaxArgs:
		return bad("the arguments are %d bytes of JSON, over MaxArgs (%d): store the data in a table of the application and give the job its id", len(args), MaxArgs)
	case !o.RunAt.IsZero() && o.Delay != 0:
		return bad("both RunAt and Delay are given: give one")
	case o.Delay < 0:
		return bad("Delay is %v: give a positive delay, or RunAt for a time in the past", o.Delay)
	case o.MaxAttempts < 0:
		return bad("MaxAttempts is %d: give at least 1, or zero for the kind's %d", o.MaxAttempts, k.maxAttempts)
	case o.Priority < -100 || o.Priority > 100:
		return bad("Priority is %d: give a priority between -100 and 100", o.Priority)
	case len(o.UniqueKey) > MaxUniqueKey:
		return bad("the unique key is %d bytes, over MaxUniqueKey (%d)", len(o.UniqueKey), MaxUniqueKey)
	case !utf8.ValidString(o.UniqueKey) || strings.IndexByte(o.UniqueKey, 0) >= 0:
		return bad("the unique key is not UTF-8 text with no NUL")
	}
	queue := o.Queue
	if queue == "" {
		queue = k.queue
	}
	if _, ok := c.opt.Queues[queue]; !ok {
		return bad("queue %q is not one of Options.Queues (%s)", queue, strings.Join(c.queues, ", "))
	}
	maxAttempts := o.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = k.maxAttempts
	}
	now := c.now()
	runAt := now
	switch {
	case !o.RunAt.IsZero():
		runAt = o.RunAt
	case o.Delay > 0:
		runAt = now.Add(o.Delay)
	}
	var key, trace *string
	if o.UniqueKey != "" {
		key = &o.UniqueKey
	}
	if t, ok := bites.TraceFrom(ctx); ok {
		s := t.Traceparent()
		trace = &s
	}
	id := newID(now)
	for range 3 {
		var got [16]byte
		err := q.QueryRow(ctx, c.q.insert, [16]byte(id), k.name, queue, args, o.Priority, maxAttempts, key, runAt, now, trace).Scan(&got)
		if err == nil {
			if !runAt.After(now) {
				if err := c.wake(ctx, q, queue); err != nil {
					return Enqueued{ID: id}, err
				}
			}
			return Enqueued{ID: id}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Enqueued{}, fmt.Errorf("pgjobs: enqueue %s: %w", k.name, err)
		}
		// The key is held. Find the job that holds it; one that finished in between frees the key,
		// and the insert is tried again.
		err = q.QueryRow(ctx, c.q.existing, k.name, o.UniqueKey).Scan(&got)
		if err == nil {
			return Enqueued{ID: ID(got), Duplicate: true}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Enqueued{}, fmt.Errorf("pgjobs: enqueue %s: %w", k.name, err)
		}
	}
	return bad("the unique key %q was taken and freed three times while this enqueue looked; try again", o.UniqueKey)
}

// wake sends the notification that a job of queue is due, through q, when the Client has a Broker.
// Through the pool the job is already stored, so a failed notification is logged and the workers
// find the job by polling; in a transaction the failure has aborted it and is returned.
func (c *Client) wake(ctx context.Context, q Querier, queue string) error {
	if c.opt.Notify == nil {
		return nil
	}
	err := c.opt.Notify.PublishTx(ctx, q, c.topic, []byte(queue))
	if err == nil {
		return nil
	}
	switch q.(type) {
	case *pgxpool.Pool, *pgx.Conn:
		c.log().Warn("pgjobs: the job is stored but the wake-up was not sent; the workers find it by polling", "queue", queue, "err", err)
		return nil
	}
	return fmt.Errorf("pgjobs: the wake-up in the transaction failed, which aborts it: %w", err)
}

// DefaultBackoff is the wait after failed attempt n when a kind has no Backoff: one second after
// the first, doubling with each attempt up to one hour, plus up to a fifth more at random so that
// jobs that failed together do not come back together. Attempts 1 to 5 wait about 1s, 2s, 4s, 8s
// and 16s; attempt 13 and later about an hour.
func DefaultBackoff(attempt int) time.Duration {
	d := time.Hour
	if attempt < 13 {
		d = time.Second << max(attempt-1, 0)
	}
	return d + rand.N(d/5+1)
}

// Snooze is what a handler returns to run its job again after d, without counting an attempt: the
// job is available again at d from now on the Client's clock, with the attempt it had. Use it for
// a job that waits for something (a file still being uploaded, a rate limit of another service). A
// negative d is a failed attempt naming the mistake.
func Snooze(d time.Duration) error { return &snoozeError{d: d} }

// Fail is what a handler returns when its job can never succeed (the thing it works on was
// deleted, the input is invalid): the job fails at once with err as its last error, whatever
// attempts it has left. Fail(nil) fails it too, with an error saying so.
func Fail(err error) error { return &failError{err: err} }

type snoozeError struct{ d time.Duration }

func (s *snoozeError) Error() string { return fmt.Sprintf("pgjobs: snoozed for %v", s.d) }

type failError struct{ err error }

func (f *failError) Error() string {
	if f.err == nil {
		return "pgjobs.Fail(nil): the handler failed the job for good and gave no error"
	}
	return f.err.Error()
}

func (f *failError) Unwrap() error { return f.err }
