// Package pgjobs is a job queue over Postgres for a bites application: work that must happen after
// a request has been answered (send a mail, extract a text, call a webhook) is stored as a row in
// the application's own database, in the caller's transaction, and run by a handler of its kind on
// whichever instance takes it first (ADR 0095).
//
// A kind is a Go type for the job's arguments and a handler, registered once with Register; its
// Kind is the only way to enqueue it, so the arguments are checked by the compiler. Enqueue takes
// the caller's pgx.Tx, so a job is stored if and only if the caller's change commits, or the pool
// for a job on its own. The Service claims due jobs with FOR UPDATE SKIP LOCKED under a lease that
// a running job extends by heartbeat: a job whose instance died is taken again once the lease
// has run out, and a slow job is not run twice. A failed attempt is tried again after an
// exponential back-off with jitter, and after its last attempt the job is kept as failed with its
// error. Delivery is at least once: a handler must be idempotent.
//
// The package depends on github.com/jackc/pgx/v5 and on modules/pgnotify, whose Broker wakes the
// workers of every instance as soon as a job is committed; without one they poll.
//
//	jobs := pgjobs.New(pgjobs.Options{Pool: pool, Notify: broker})
//	welcome := pgjobs.Register(jobs, "welcome-mail", func(ctx context.Context, job pgjobs.Job[Welcome]) error {
//		return mailer.Send(ctx, job.Args.Email)
//	})
//	app.Service(jobs.Service())
//
//	// in a handler, in the transaction that creates the user:
//	_, err = welcome.Enqueue(ctx, tx, Welcome{Email: u.Email})
package pgjobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/modules/pgnotify"
)

// DefaultTable is the table used when Options.Table is empty. The periodic jobs keep their last
// period in the table of the same name with "_periodic" added.
const DefaultTable = "bites_jobs"

// DefaultQueue is the queue of a kind whose KindOptions.Queue is empty, and the one queue of a
// Client whose Options.Queues is empty.
const DefaultQueue = "default"

// DefaultWorkers is how many jobs of DefaultQueue an instance runs at once when Options.Queues is
// empty.
const DefaultWorkers = 10

// DefaultMaxAttempts is how many times a job is tried when neither its kind nor its enqueue says.
const DefaultMaxAttempts = 20

// DefaultTimeout is how long one attempt may run when KindOptions.Timeout is zero.
const DefaultTimeout = time.Minute

// DefaultLease is how long a claim holds a job without a heartbeat when Options.Lease is zero.
const DefaultLease = 30 * time.Second

// DefaultPollEvery is how often the Service looks for due jobs when Options.PollEvery is zero.
const DefaultPollEvery = time.Second

// DefaultKeepCompleted is how long a completed job is kept when Options.KeepCompleted is zero.
const DefaultKeepCompleted = 24 * time.Hour

// DefaultKeepFailed is how long a failed or cancelled job is kept when Options.KeepFailed is zero.
const DefaultKeepFailed = 7 * 24 * time.Hour

// MaxArgs is the largest a job's arguments may be once encoded as JSON, in bytes. A job carries the
// ids of what it works on, not the data: a larger one is refused by Enqueue.
const MaxArgs = 1 << 20

// MaxUniqueKey is the longest a unique key may be, in bytes.
const MaxUniqueKey = 512

const (
	maxTable    = 40   // the longest index name, "<table>_periodic_pkey", stays inside Postgres's 63 bytes
	maxBatch    = 100  // the most jobs one claim takes
	pruneBatch  = 1000 // the most rows one DELETE of the housekeeping removes
	pruneEvery  = time.Minute
	maxError    = 4000 // the most bytes of an error kept in last_error
	stopReserve = 2 * time.Second
	cancelWait  = time.Second
	writeWait   = 30 * time.Second // the most a statement that records an attempt waits
)

var (
	tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	plainName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
)

// ErrNotFound is returned by Get, Retry and Cancel for an id that names no job: it never existed,
// or it was pruned.
var ErrNotFound = errors.New("pgjobs: no job has this id")

// ErrRunning is returned by Retry for a job that is running: it is retried after it ends, by its
// own back-off, or cancelled with Cancel first.
var ErrRunning = errors.New("pgjobs: the job is running; wait for it to end, or Cancel it first")

// ErrFinished is returned by Cancel for a job that has already completed, failed or been cancelled.
var ErrFinished = errors.New("pgjobs: the job has already finished (completed, failed or cancelled)")

// ErrStopping is the cause of a running job's ctx when the Service stops and the job has not
// returned in the time Stop could wait: context.Cause(ctx) == ErrStopping. A handler that returns
// after it is released, not counted as an attempt, and runs again on another instance.
var ErrStopping = errors.New("pgjobs: the Service is stopping and could not wait for this job any longer")

// ErrLost is the cause of a running job's ctx when the job is no longer this worker's: it was
// cancelled with Cancel, or the heartbeat could not extend its lease before it ran out and another
// worker may have taken it. Whatever the handler returns afterwards is not recorded.
var ErrLost = errors.New("pgjobs: the job is no longer this worker's: it was cancelled, or its lease ran out")

// Querier is what Enqueue writes through: the two methods of a pgx connection. A pgx.Tx, a
// *pgxpool.Pool, a *pgxpool.Conn and a *pgx.Conn all satisfy it. Give the pgx.Tx of the change
// the job belongs to, so the job is stored if and only if that change commits; give the pool for a
// job that belongs to nothing.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options are the settings of New.
type Options struct {
	// Pool is the application's pool. Required. The Client claims, records attempts and runs its
	// housekeeping on it, and never closes it: stop the app, which stops the Service, before
	// closing the pool.
	Pool *pgxpool.Pool
	// Table is the table the jobs are kept in: a plain identifier of letters, digits and
	// underscores that does not start with a digit, at most 40 bytes. Empty means "bites_jobs".
	// The periodic jobs use a second table, Table with "_periodic" added.
	Table string
	// Queues names the queues of the application and, for each, how many of its jobs one instance
	// runs at once (at least 1). A kind's queue and an enqueue's queue must be one of them. Every
	// instance of an application should give the same names; an instance that only enqueues does
	// not register the Service. Empty means one queue, DefaultQueue, running DefaultWorkers at once.
	Queues map[string]int
	// Notify, when set, wakes the workers of every instance as soon as a due job is committed:
	// Enqueue sends a notification in the caller's transaction (pgnotify.Broker.PublishTx) and the
	// Service subscribes to it. The application runs the Broker, as for its Topics. Nil means the
	// workers find new jobs by polling only, within PollEvery.
	Notify *pgnotify.Broker
	// Clock is the clock every time of the Client is read on and its timers run on: a job's run
	// time, a lease, a back-off, a period, a retention. Give the test's bitetest.Clock (the same one
	// given to bites.WithClock) to move the queue by advancing it. Nil means the real clock. The
	// timeout of an attempt and the waits of Stop are in real time when it is nil.
	Clock bites.Clock
	// PollEvery is how often the Service looks for due jobs without a notification, and how often
	// it takes back the jobs whose lease ran out and enqueues the periodic jobs whose period has
	// come. Zero means one second; at least 10ms.
	PollEvery time.Duration
	// Lease is how long a claim holds a job: the Service extends it every third of a Lease while
	// the job runs, and a job whose instance stopped extending it is taken again once it has run
	// out. Zero means 30 seconds; at least one second.
	Lease time.Duration
	// KeepCompleted is how long a completed job is kept before the housekeeping deletes it. Zero
	// means 24 hours.
	KeepCompleted time.Duration
	// KeepFailed is how long a failed or cancelled job is kept before the housekeeping deletes it.
	// Zero means 7 days.
	KeepFailed time.Duration
	// Observers see each attempt start and end, in the order given (the first is the outermost):
	// for logs, metrics and traces.
	Observers []Observer
	// Log receives the Client's own lines: a failed attempt (Warn), a job that failed for good, a
	// panic (Error), a lease that ran out (Warn), what the housekeeping deleted (Info). Nil means
	// the app's logger once the Service has started (bites.Logger of its context), and
	// slog.Default() before.
	Log *slog.Logger
}

// Client is the job queue of one application on one table: the kinds registered on it, Enqueue,
// the Service that runs the jobs, Work for a test, and what an operator page reads and changes.
// Make one with New; the zero value is not usable. It is safe for concurrent use, and any number of
// instances may share a table.
type Client struct {
	opt    Options
	clk    bites.Clock // nil is the machine's clock
	table  string
	topic  string
	queues []string // sorted
	q      queries

	mu        sync.Mutex
	kinds     map[string]*kind
	schedules map[string]*schedule
	crons     []bites.Service
	started   bool    // the Service has started: registration is closed
	run       *runner // the running Service, or nil

	appLog atomic.Pointer[slog.Logger]
}

type queries struct {
	insert, existing, claim, complete, retry, fail, snooze, release, heartbeat string
	rescue, prune, period, get, state, counts, retryJob, cancel, schema        string
}

// New returns a Client; it connects nothing and creates nothing. It panics, naming the line that
// called it, when Pool is nil, Table is not a plain identifier of at most 40 bytes, a queue name
// is not a plain name or runs fewer than one job at once, a duration is negative, PollEvery is
// under 10ms, Lease is under a second, or an Observer is nil.
func New(o Options) *Client {
	where := callerWhere(2)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("pgjobs.New: %s\n\tregistered at %s", fmt.Sprintf(format, args...), where))
	}
	if o.Pool == nil {
		fail("Pool is nil: give the application's *pgxpool.Pool, such as pgxpool.New(ctx, url)")
	}
	if o.Table == "" {
		o.Table = DefaultTable
	}
	if len(o.Table) > maxTable || !tableName.MatchString(o.Table) {
		fail("Table %q is not a plain identifier: give letters, digits and underscores, not starting with a digit, at most %d bytes, or leave it empty for %q", o.Table, maxTable, DefaultTable)
	}
	if len(o.Queues) == 0 {
		o.Queues = map[string]int{DefaultQueue: DefaultWorkers}
	}
	queues := make([]string, 0, len(o.Queues))
	copied := make(map[string]int, len(o.Queues))
	for name, n := range o.Queues {
		if !plainName.MatchString(name) {
			fail("queue %q is not a plain name: give letters, digits and \".\", \"_\", \":\", \"-\", starting with a letter or digit, at most 100 bytes", name)
		}
		if n < 1 {
			fail("queue %q runs %d jobs at once: give at least 1; an instance that runs none of them does not register the Service", name, n)
		}
		queues = append(queues, name)
		copied[name] = n
	}
	o.Queues = copied
	slices.Sort(queues)
	for name, d := range map[string]time.Duration{"PollEvery": o.PollEvery, "Lease": o.Lease, "KeepCompleted": o.KeepCompleted, "KeepFailed": o.KeepFailed} {
		if d < 0 {
			fail("%s is %v, which is not a time: give a positive duration, or zero for the default", name, d)
		}
	}
	if o.PollEvery == 0 {
		o.PollEvery = DefaultPollEvery
	}
	if o.PollEvery < 10*time.Millisecond {
		fail("PollEvery is %v: give at least 10ms, since each poll is a query on every instance", o.PollEvery)
	}
	if o.Lease == 0 {
		o.Lease = DefaultLease
	}
	if o.Lease < time.Second {
		fail("Lease is %v: give at least one second, so a heartbeat every third of it can reach the database in time", o.Lease)
	}
	if o.KeepCompleted == 0 {
		o.KeepCompleted = DefaultKeepCompleted
	}
	if o.KeepFailed == 0 {
		o.KeepFailed = DefaultKeepFailed
	}
	for i, ob := range o.Observers {
		if ob == nil {
			fail("Observers[%d] is nil: give an Observer, or leave it out", i)
		}
	}
	o.Observers = slices.Clone(o.Observers)
	c := &Client{opt: o, clk: o.Clock, table: o.Table, queues: queues, kinds: map[string]*kind{}, schedules: map[string]*schedule{}}
	c.topic = "pgjobs:" + o.Table
	c.q = buildQueries(o.Table)
	return c
}

func buildQueries(table string) queries {
	t := `"` + table + `"`
	p := `"` + table + `_periodic"`
	const cols = `id, kind, queue, state, args, priority, attempt, max_attempts, unique_key, scheduled_at, run_at, created_at, attempted_at, finished_at, lease_until, last_error, trace`
	const unique = `(kind, unique_key) WHERE unique_key IS NOT NULL AND state IN ('available', 'running')`
	return queries{
		insert: `INSERT INTO ` + t + ` (id, kind, queue, state, args, priority, attempt, max_attempts, unique_key, scheduled_at, run_at, created_at, trace)
VALUES ($1, $2, $3, 'available', $4, $5, 0, $6, $7, $8, $8, $9, $10)
ON CONFLICT ` + unique + ` DO NOTHING RETURNING id`,
		existing: `SELECT id FROM ` + t + ` WHERE kind = $1 AND unique_key = $2 AND state IN ('available', 'running')`,
		claim: `UPDATE ` + t + ` SET state = 'running', attempt = attempt + 1, attempted_at = $1, lease_until = $2, claim = $3
WHERE id IN (
	SELECT id FROM ` + t + `
	WHERE state = 'available' AND queue = $4 AND run_at <= $1 AND kind = ANY($5) AND NOT (id = ANY($7))
	ORDER BY priority DESC, run_at, id
	LIMIT $6
	FOR UPDATE SKIP LOCKED)
RETURNING ` + cols,
		complete:  `UPDATE ` + t + ` SET state = 'completed', finished_at = $3, lease_until = NULL, claim = NULL WHERE id = $1 AND claim = $2`,
		retry:     `UPDATE ` + t + ` SET state = 'available', run_at = $3, last_error = $4, lease_until = NULL, claim = NULL WHERE id = $1 AND claim = $2`,
		fail:      `UPDATE ` + t + ` SET state = 'failed', finished_at = $3, last_error = $4, lease_until = NULL, claim = NULL WHERE id = $1 AND claim = $2`,
		snooze:    `UPDATE ` + t + ` SET state = 'available', run_at = $3, attempt = attempt - 1, lease_until = NULL, claim = NULL WHERE id = $1 AND claim = $2`,
		release:   `UPDATE ` + t + ` SET state = 'available', run_at = $3, attempt = attempt - 1, lease_until = NULL, claim = NULL WHERE id = $1 AND claim = $2`,
		heartbeat: `UPDATE ` + t + ` AS t SET lease_until = $1 FROM unnest($2::uuid[], $3::uuid[]) AS h(id, claim) WHERE t.id = h.id AND t.claim = h.claim RETURNING t.id`,
		rescue: `UPDATE ` + t + ` SET
	state = CASE WHEN attempt >= max_attempts THEN 'failed' ELSE 'available' END,
	finished_at = CASE WHEN attempt >= max_attempts THEN $1::timestamptz END,
	run_at = $1, lease_until = NULL, claim = NULL,
	last_error = 'the lease ran out during attempt ' || attempt || ': the instance that ran it stopped without finishing it (a crash, a lost connection to the database, or a stop that could not wait)'
WHERE id IN (
	SELECT id FROM ` + t + ` WHERE state = 'running' AND lease_until < $1
	LIMIT ` + fmt.Sprint(pruneBatch) + ` FOR UPDATE SKIP LOCKED)
RETURNING id, kind, queue, state, attempt`,
		prune: `DELETE FROM ` + t + ` WHERE id IN (
	SELECT id FROM ` + t + ` WHERE state = $1 AND finished_at IS NOT NULL AND finished_at < $2
	LIMIT ` + fmt.Sprint(pruneBatch) + ` FOR UPDATE SKIP LOCKED)`,
		period:   `INSERT INTO ` + p + ` (name, period) VALUES ($1, $2) ON CONFLICT (name) DO UPDATE SET period = EXCLUDED.period WHERE ` + p + `.period < EXCLUDED.period RETURNING true`,
		get:      `SELECT ` + cols + ` FROM ` + t + ` WHERE id = $1`,
		state:    `SELECT state FROM ` + t + ` WHERE id = $1`,
		counts:   `SELECT queue, state, count(*) FROM ` + t + ` GROUP BY queue, state ORDER BY queue, state`,
		retryJob: `UPDATE ` + t + ` SET state = 'available', attempt = 0, run_at = $2, finished_at = NULL, lease_until = NULL, claim = NULL WHERE id = $1 AND state <> 'running' RETURNING queue`,
		cancel:   `UPDATE ` + t + ` SET state = 'cancelled', finished_at = $2, lease_until = NULL, claim = NULL WHERE id = $1 AND state IN ('available', 'running') RETURNING true`,
		schema: fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
	id           uuid PRIMARY KEY,
	kind         text NOT NULL,
	queue        text NOT NULL,
	state        text NOT NULL CHECK (state IN ('available', 'running', 'completed', 'failed', 'cancelled')),
	args         jsonb NOT NULL,
	priority     smallint NOT NULL,
	attempt      integer NOT NULL,
	max_attempts integer NOT NULL,
	unique_key   text,
	scheduled_at timestamptz NOT NULL,
	run_at       timestamptz NOT NULL,
	created_at   timestamptz NOT NULL,
	attempted_at timestamptz,
	finished_at  timestamptz,
	lease_until  timestamptz,
	claim        uuid,
	last_error   text,
	trace        text
);
CREATE INDEX IF NOT EXISTS "%[2]s_claim_idx" ON %[1]s (queue, priority DESC, run_at, id) WHERE state = 'available';
CREATE INDEX IF NOT EXISTS "%[2]s_running_idx" ON %[1]s (queue) WHERE state = 'running';
CREATE UNIQUE INDEX IF NOT EXISTS "%[2]s_unique_idx" ON %[1]s %[4]s;
CREATE INDEX IF NOT EXISTS "%[2]s_finished_idx" ON %[1]s (state, finished_at) WHERE finished_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS %[3]s (
	name   text PRIMARY KEY,
	period timestamptz NOT NULL
);
`, t, table, p, unique),
	}
}

// Schema returns the SQL that creates the jobs table, its four indexes and the table of the
// periodic jobs, each statement with IF NOT EXISTS, for an application that applies its schema
// with a migration tool: paste it into a migration. CreateTable runs the same text.
func (c *Client) Schema() string { return c.q.schema }

// CreateTable runs Schema, for an application that has no migration tool. Running it again, or
// against tables that exist, changes nothing.
func (c *Client) CreateTable(ctx context.Context) error {
	if _, err := c.opt.Pool.Exec(ctx, c.q.schema); err != nil {
		return fmt.Errorf("pgjobs: create table %s: %w", c.table, err)
	}
	return nil
}

// now is the time on the Client's clock.
func (c *Client) now() time.Time {
	if c.clk == nil {
		return time.Now()
	}
	return c.clk.Now()
}

// timer is what afterFunc returns: a pending call that Stop cancels.
type timer interface{ Stop() bool }

// afterFunc runs f once d has passed on the Client's clock.
func (c *Client) afterFunc(d time.Duration, f func()) timer {
	if c.clk == nil {
		return time.AfterFunc(d, f)
	}
	return c.clk.AfterFunc(d, f)
}

// log is Options.Log, else the app's logger once the Service started, else slog.Default().
func (c *Client) log() *slog.Logger {
	if c.opt.Log != nil {
		return c.opt.Log
	}
	if l := c.appLog.Load(); l != nil {
		return l
	}
	return slog.Default()
}

// callerWhere is "file.go:N" of the caller skip frames up.
func callerWhere(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}

// clean makes an error's text one a text column keeps: valid UTF-8, no NUL, at most maxError bytes.
func clean(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	if len(s) > maxError {
		s = strings.ToValidUTF8(s[:maxError], "") + " ..."
	}
	return s
}

// ID is a job's id: a UUIDv7 (RFC 9562) made by the Client from its clock, to a 244ns fraction of
// a millisecond, and 62 random bits, so ids sort by the time their jobs were enqueued, and those
// made by one process sort in the order it made them. It is a uuid column in Postgres and the
// canonical text form (8-4-4-4-12 hex digits) in JSON and in a path.
type ID [16]byte

var (
	idMu   sync.Mutex
	lastID ID
)

// newID is a UUIDv7 of t: 48 bits of Unix milliseconds, the version, 12 bits of the fraction of the
// millisecond, the variant and 62 random bits; one that would not sort after the last made by this
// process is the last plus one.
func newID(t time.Time) ID {
	var id ID
	_, _ = rand.Read(id[8:])
	ms := uint64(t.UnixMilli())
	frac := uint64(t.Nanosecond()%1e6) * 4096 / 1e6
	for i := range 6 {
		id[i] = byte(ms >> (40 - 8*i))
	}
	id[6] = 0x70 | byte(frac>>8)
	id[7] = byte(frac)
	id[8] = 0x80 | id[8]&0x3f
	idMu.Lock()
	defer idMu.Unlock()
	if bytes.Compare(id[:], lastID[:]) <= 0 {
		id = lastID
		for i := 15; i >= 8; i-- {
			id[i]++
			if id[i] != 0 && (i > 8 || id[8]&0xc0 == 0x80) {
				break
			}
		}
		id[8] = 0x80 | id[8]&0x3f
	}
	lastID = id
	return id
}

// String is the canonical text form, such as "019a6c3e-8f00-7c3a-9d2e-0b6f3c1a2b4d".
func (id ID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], id[10:16])
	return string(b[:])
}

// MarshalText is String as bytes, so an ID is a JSON string.
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText reads the canonical text form, upper or lower case, as ParseID does.
func (id *ID) UnmarshalText(b []byte) error {
	v, err := ParseID(string(b))
	if err != nil {
		return err
	}
	*id = v
	return nil
}

// ParseID reads an ID from its canonical text form (36 characters, hex digits in groups of
// 8-4-4-4-12), the form String writes. Any other text is an error naming what it got.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, fmt.Errorf("pgjobs: %q is not a job id: give 36 characters, hex digits in groups of 8-4-4-4-12", s)
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	if _, err := hex.Decode(id[:], []byte(h)); err != nil {
		return ID{}, fmt.Errorf("pgjobs: %q is not a job id: %v", s, err)
	}
	return id, nil
}
