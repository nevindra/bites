# pgjobs

**The module that keeps an application's background jobs in Postgres: typed kinds, enqueued in the caller's transaction, claimed under a lease a heartbeat extends, retried with back-off, unique while waiting or running, periodic once per period across instances, and run at least once.**

**Guide:** [Background jobs](../guide/background-jobs.md) · **Design:** [The engine](../design/engine.md)

`github.com/nevindra/bites/modules/pgjobs` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. It depends on `github.com/jackc/pgx/v5` at v5.10.0 and on `modules/pgnotify`, and adds no other dependency. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/pgjobs && go test ./...`, and the tests that need a database run when `BITES_PG_URL` names one and are skipped, with a message naming it, otherwise. The decision is [ADR 0095](../adr/0095-a-job-queue-in-postgres-is-a-module-whose-jobs-are-claimed-under-a-lease-and-delivered-at-least-once.md).

```go
jobs := pgjobs.New(pgjobs.Options{Pool: pool, Notify: broker})
if err := jobs.CreateTable(ctx); err != nil { /* or paste jobs.Schema() into a migration */ }
welcome := pgjobs.Register(jobs, "welcome-mail", sendWelcome)
app.Service(jobs.Service())

// in a handler, in the transaction that creates the user:
_, err = welcome.Enqueue(ctx, tx, Welcome{UserID: id})
```

**Delivery is at least once.** A job runs again when its instance dies mid-run, when the statement that records its success fails, and when a stop could not wait for it, so a handler must be idempotent: key what it writes on `Job.ID` or `Job.ScheduledAt`.

**The Postgres floor** is the oldest version the community supports, as for pgx (15 when this was written). The SQL uses nothing newer than Postgres 9.5; the module is tested on 18.7. Ids are UUIDv7 made in Go, not `uuidv7()`, which needs 18.

The tables, as `Schema` returns them for the default name:

```sql
CREATE TABLE IF NOT EXISTS "bites_jobs" (
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
CREATE INDEX IF NOT EXISTS "bites_jobs_claim_idx" ON "bites_jobs" (queue, priority DESC, run_at, id) WHERE state = 'available';
CREATE INDEX IF NOT EXISTS "bites_jobs_running_idx" ON "bites_jobs" (queue) WHERE state = 'running';
CREATE UNIQUE INDEX IF NOT EXISTS "bites_jobs_unique_idx" ON "bites_jobs" (kind, unique_key) WHERE unique_key IS NOT NULL AND state IN ('available', 'running');
CREATE INDEX IF NOT EXISTS "bites_jobs_finished_idx" ON "bites_jobs" (state, finished_at) WHERE finished_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS "bites_jobs_periodic" (
	name   text PRIMARY KEY,
	period timestamptz NOT NULL
);
```

The indexes are partial on the live states, so the claim stays an index scan however many completed jobs the table keeps: on 1,000,000 completed and 1,000 available jobs a claim of ten read 16 buffers in 0.18 to 0.24 ms ([ADR 0095](../adr/0095-a-job-queue-in-postgres-is-a-module-whose-jobs-are-claimed-under-a-lease-and-delivered-at-least-once.md)). An application may read the table with its own SQL (a page that shows the jobs of a File, by `args->>'file_id'`) and add its own indexes; it changes the rows only through the Client.

### `pgjobs.New`

```go
func New(o Options) *Client
```

New returns a Client and connects nothing. It panics, `pgjobs.New: <reason>` and `registered at file.go:N` on the next line, for each row of this table:

| Mistake | Message begins |
|---|---|
| `Pool` is nil | `Pool is nil: give the application's *pgxpool.Pool` |
| `Table` is not letters, digits and underscore, starts with a digit, or is over 40 bytes | `Table "<name>" is not a plain identifier` |
| a queue name is not a plain name | `queue "<name>" is not a plain name` |
| a queue runs fewer than one job at once | `queue "<name>" runs 0 jobs at once: give at least 1` |
| a duration is negative | `<Field> is <d>, which is not a time` |
| `PollEvery` under 10 ms | `PollEvery is <d>: give at least 10ms` |
| `Lease` under one second | `Lease is <d>: give at least one second` |
| an Observer is nil | `Observers[<i>] is nil` |

### `pgjobs.Options`

```go
type Options struct {
	Pool          *pgxpool.Pool
	Table         string
	Queues        map[string]int
	Notify        *pgnotify.Broker
	Clock         bites.Clock
	PollEvery     time.Duration
	Lease         time.Duration
	KeepCompleted time.Duration
	KeepFailed    time.Duration
	Observers     []Observer
	Log           *slog.Logger
}
```

`Pool` is the application's pool; required, and never closed by the Client. `Table` is the jobs table, `bites_jobs` when empty, and the periodic jobs use the same name with `_periodic`. `Queues` names each queue and how many of its jobs one instance runs at once; empty is `{"default": 10}`; a kind's queue and an enqueue's must be one of them. `Notify` is a `pgnotify.Broker` the application runs: Enqueue sends a wake-up in the caller's transaction and the Service hears it, so a job starts as soon as it commits; nil leaves the workers to poll. `Clock` is the one clock every time of the Client is read on (run times, leases, back-offs, periods, retentions) and its timers run on; give the test's `bitetest.Clock`, nil is the real clock. With the Service running on a test clock, an `Advance` past `Lease` while a job runs takes the job back, as a dead instance's would be, so such a test gives a longer `Lease` or runs its jobs with `Work`. `PollEvery` (one second) is how often the Service claims without a notification, takes back expired leases and enqueues due periods. `Lease` (30 s) is how long a claim holds a job without a heartbeat; the Service extends it every third. `KeepCompleted` (24 hours) and `KeepFailed` (7 days, for failed and cancelled jobs) are the retentions `Prune` applies. `Observers` see each attempt. `Log` receives the Client's lines; nil is the app's logger once the Service started, else `slog.Default()`.

### `pgjobs.Client`

```go
type Client struct { /* unexported */ }
```

Client is the job queue of one application on one table: its kinds, Enqueue, the Service, `Work`, and what an operator page reads and changes. Make one with `New`; the zero value is not usable. Safe for concurrent use; any number of instances may share a table.

### `pgjobs.Client.Schema`

```go
func (c *Client) Schema() string
```

Schema returns the SQL above for the Client's table: paste it into a migration.

### `pgjobs.Client.CreateTable`

```go
func (c *Client) CreateTable(ctx context.Context) error
```

CreateTable runs `Schema`, for an application with no migration tool. Running it again changes nothing.

### `pgjobs.Client.Service`

```go
func (c *Client) Service() bites.Service
```

Service returns the `bites.Service` that runs the jobs on this instance: register it with `app.Service`, after the pool's and before what the jobs need to outlive. An instance that only enqueues does not register it. It is named `pgjobs` (`pgjobs-<table>` for another table), and the Client serves one at a time: a second `Start` returns an error. `Start` returns at once and closes registration; from then on the Service claims due jobs at a notification and every `PollEvery`, extends the leases of its running jobs, takes back expired ones, enqueues due periods, and prunes once a minute. Its start-up line is `pgjobs: working`, with the table, the queues, the kinds, `notify`, `poll` and `lease`. `BeforeShutdown` stops the claiming while the app still serves. `Stop` waits for the running jobs until two seconds before its context's deadline (at once under `Close`), cancels the rest with `ErrStopping`, waits up to one second more, and makes each job whose handler returned available again with its attempt given back, so another instance takes it at once; a handler that ignores its `ctx` keeps its job until the lease runs out, and its id is logged at Warn.

### `pgjobs.Client.Work`

```go
func (c *Client) Work(ctx context.Context) (int, error)
```

Work runs every due job on the caller's goroutine, one at a time, until none is due, and returns how many attempts it ran. It first takes back expired leases and enqueues due periods. The jobs a run enqueues run too when due, and a job is attempted at most once by one call. It is for a test (enqueue, `Work`, look at the result; with `bitetest.Clock`, `Advance` reaches a delay or a back-off) and for a tool that drains a queue; its attempts have their timeout and no heartbeat. Cancelling `ctx` puts the running job back without counting the attempt.

### `pgjobs.Client.Prune`

```go
func (c *Client) Prune(ctx context.Context) (int, error)
```

Prune deletes the completed jobs that finished more than `KeepCompleted` ago and the failed and cancelled ones past `KeepFailed`, on the Client's clock, 1,000 rows a statement, and returns how many. The Service runs it once a minute; several instances may prune at once.

### `pgjobs.Client.Counts`

```go
func (c *Client) Counts(ctx context.Context) ([]Count, error)
```

Counts returns how many jobs each queue has in each state, sorted by queue and state, pairs with none left out. It reads the whole table: 41 to 45 ms for a million rows in [the measurement](../research/pgjobs/README.md).

### `pgjobs.Client.List`

```go
func (c *Client) List(ctx context.Context, o ListOptions) ([]Record, error)
```

List returns jobs newest first, as `ListOptions` selects them. A limit outside 0 to 1000 is an error.

### `pgjobs.Client.Get`

```go
func (c *Client) Get(ctx context.Context, id ID) (Record, error)
```

Get returns one job, or `ErrNotFound`.

### `pgjobs.Client.Retry`

```go
func (c *Client) Retry(ctx context.Context, id ID) error
```

Retry makes a job due now with a fresh set of attempts: a failed or cancelled job runs again, a completed one runs once more, an available one stops waiting. Its last error stays until an attempt replaces it. It returns `ErrNotFound`, `ErrRunning`, or `pgjobs: retry <id>: another available or running job of its kind holds its unique key; ...`.

### `pgjobs.Client.Cancel`

```go
func (c *Client) Cancel(ctx context.Context, id ID) error
```

Cancel makes a job cancelled so it never runs again: an available one at once; a running one is told through its `ctx`, cause `ErrLost`, at once on this instance and at the next heartbeat on another, and what its handler returns afterwards is not recorded. It returns `ErrNotFound`, and `ErrFinished` for a job that has completed, failed or been cancelled.

### `pgjobs.Register`

```go
func Register[T any](c *Client, name string, handle func(ctx context.Context, job Job[T]) error, opts ...KindOptions) *Kind[T]
```

Register adds a kind whose jobs carry arguments of type `T`, run by `handle`, and returns the `Kind` to enqueue them with: the only way to. `handle` returns nil when done; any other error is a failed attempt, tried again after the kind's back-off until its attempts are spent, and then the job is kept as failed with that error. `Fail(err)` fails it at once; `Snooze(d)` runs it again after `d` without counting an attempt; a panic is a failed attempt, logged with its stack. Arguments are JSON (`encoding/json/v2`); a job whose arguments do not decode into `T` fails at once. It panics, `pgjobs.Register("<name>"): <reason>` and the line, for a name that is not a plain name (letters, digits, `.`, `_`, `:`, `-`, at most 100 bytes), a kind of that name already registered, a nil `handle`, a `T` whose zero value does not encode, a queue not in `Options.Queues`, a negative `MaxAttempts` or `Timeout`, two options values, and a Register after the Service started.

### `pgjobs.KindOptions`

```go
type KindOptions struct {
	Queue       string
	MaxAttempts int
	Timeout     time.Duration
	Backoff     func(attempt int) time.Duration
}
```

`Queue` is where the kind's jobs go unless an enqueue says (`DefaultQueue` when empty). `MaxAttempts` is the attempts a job has unless its enqueue says (20 when zero; 1 is never retried). `Timeout` bounds one attempt (one minute when zero): its `ctx` is cancelled then with `context.DeadlineExceeded` as the cause, and an error after it is a failed attempt whose error begins `the attempt ran past its timeout of <d>`. `Backoff` is the wait after failed attempt n (`DefaultBackoff` when nil; a negative result is zero).

### `pgjobs.Kind`

```go
type Kind[T any] struct { /* unexported */ }
```

Kind is a registered kind with arguments of type `T`. Make one with `Register`.

### `pgjobs.Kind.Name`

```go
func (k *Kind[T]) Name() string
```

Name is the name the kind was registered with, for `ListOptions.Kind`.

### `pgjobs.Kind.Enqueue`

```go
func (k *Kind[T]) Enqueue(ctx context.Context, q Querier, args T, opts ...EnqueueOptions) (Enqueued, error)
```

Enqueue stores a job of the kind through `q`: the caller's `pgx.Tx`, so it is stored if and only if the transaction commits, or the pool. With `Options.Notify` a job due now also sends its wake-up through `q`. With a `UniqueKey` that an available or running job of the kind holds, nothing is stored and the result names that job with `Duplicate` set. The trace of `ctx` (`bites.TraceFrom`) is stored with the job. It returns an error beginning `pgjobs: enqueue <kind>: `, and stores nothing, for a nil `q`, arguments that do not encode or are over `MaxArgs`, `RunAt` and `Delay` both set, a negative `Delay` or `MaxAttempts`, a priority outside -100 to 100, a queue not in `Options.Queues`, a unique key over `MaxUniqueKey` or not UTF-8 with no NUL, two options values, and the database's error (which, in a transaction, has aborted it). A wake-up that fails through a pool or a `*pgx.Conn` is logged and the job is found by polling; in a transaction it is returned.

### `pgjobs.Kind.Every`

```go
func (k *Kind[T]) Every(name string, interval time.Duration, args T)
```

Every enqueues a job of the kind with `args` once in every period of `interval`, across every instance, with no leader: periods count from the zero time in UTC (24 hours is a UTC day), and the instance that first sees a period records it in the periodic table in the transaction that enqueues the job. The job's `ScheduledAt` is the period's start and its `UniqueKey` is `name@<Unix seconds>`. A period is seen at the Service's poll and by `Work`; a period every instance missed while stopped is enqueued once when the first starts, and the ones before it are not; a new schedule's current period runs at once. It panics, `pgjobs: Kind("<kind>").Every("<name>"): <reason>` and the line, for an interval under `PollEvery`, a name that is not plain or is another schedule's, arguments that do not encode, and a call after the Service started.

### `pgjobs.Kind.Cron`

```go
func (k *Kind[T]) Cron(name, spec string, loc *time.Location, args T)
```

Cron enqueues a job of the kind with `args` at each time of `spec` in `loc`, across every instance, with no leader: each instance runs a `bites.Cron` named `pgjobs-<name>` (its fields, zone and daylight-saving rules, [ADR 0092](../adr/0092-a-cron-schedule-is-a-service-read-in-a-given-zone-and-a-job-lock-lets-one-instance-run-each-occurrence.md)) on the app's clock, started and stopped with the Client's Service, and the instance that first records an occurrence in the periodic table enqueues it. The job's `ScheduledAt` is the occurrence and its `UniqueKey` is `name@<Unix seconds>`. It panics with `bites.Cron`'s message for a spec or zone it refuses, and as `Every` does for the rest, naming the line of the `Kind.Cron` call.

### `pgjobs.Job`

```go
type Job[T any] struct {
	ID          ID
	Kind        string
	Queue       string
	Args        T
	Attempt     int
	MaxAttempts int
	Priority    int
	UniqueKey   string
	ScheduledAt time.Time
	CreatedAt   time.Time
}
```

Job is what a handler gets. `Attempt` is 1 on the first run; a snooze and a release at shutdown do not count, a lease that ran out does. When `Attempt` equals `MaxAttempts` and the handler returns an error, the job fails for good. `ScheduledAt` is when the job was first due (enqueued, its `RunAt` or `Delay`, or its period's start) and does not change on a retry or a snooze: the value to key an idempotent write on. The attempt's `ctx` carries the trace the job was enqueued under, with a span of its own (`bites.TraceFrom(ctx)`).

### `pgjobs.EnqueueOptions`

```go
type EnqueueOptions struct {
	RunAt       time.Time
	Delay       time.Duration
	Queue       string
	Priority    int
	MaxAttempts int
	UniqueKey   string
}
```

The zero value is due now, on the kind's queue, priority 0, the kind's attempts and no unique key. `RunAt` is when it becomes due; `Delay` is that long from now on the Client's clock; give one. `Queue` overrides the kind's. `Priority` orders the due jobs of a queue, higher first, then by run time and id; -100 to 100. `MaxAttempts` overrides the kind's. `UniqueKey` makes the enqueue store nothing while a job of the same kind with that key is available (due, or waiting for its time or a back-off) or running; completed, failed and cancelled jobs do not hold it. At most 512 bytes.

### `pgjobs.Enqueued`

```go
type Enqueued struct {
	ID        ID
	Duplicate bool
}
```

Enqueued is what Enqueue did: `ID` is the job stored, or with `Duplicate` the job that holds the key.

### `pgjobs.Querier`

```go
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

Querier is what Enqueue writes through. A `pgx.Tx`, a `*pgxpool.Pool`, a `*pgxpool.Conn` and a `*pgx.Conn` satisfy it.

### `pgjobs.Snooze`

```go
func Snooze(d time.Duration) error
```

Snooze is what a handler returns to run its job again after `d` without counting the attempt. A negative `d` is a failed attempt whose error says `pgjobs.Snooze(-1s): a snooze is a time to wait; give zero or more`.

### `pgjobs.Fail`

```go
func Fail(err error) error
```

Fail is what a handler returns when its job can never succeed: the job fails at once with `err` as its last error. It may be wrapped (`fmt.Errorf("...: %w", pgjobs.Fail(err))`); `Fail(nil)` fails it with an error saying no error was given.

### `pgjobs.DefaultBackoff`

```go
func DefaultBackoff(attempt int) time.Duration
```

DefaultBackoff is the wait after failed attempt n when a kind gives none: one second after the first, doubling up to one hour (from attempt 13), plus up to a fifth more at random.

### `pgjobs.State`

```go
type State string
```

State is where a job stands: `StateAvailable`, `StateRunning`, `StateCompleted`, `StateFailed`, `StateCancelled`.

### `pgjobs.StateAvailable`

```go
const StateAvailable State = "available"
```

A job waiting to run: due, or waiting for its time, a back-off or a snooze.

### `pgjobs.StateRunning`

```go
const StateRunning State = "running"
```

A job a worker holds under a lease.

### `pgjobs.StateCompleted`

```go
const StateCompleted State = "completed"
```

A job whose handler returned nil, kept for `KeepCompleted`.

### `pgjobs.StateFailed`

```go
const StateFailed State = "failed"
```

A job that spent its attempts or was failed with `Fail`, kept with its last error for `KeepFailed`.

### `pgjobs.StateCancelled`

```go
const StateCancelled State = "cancelled"
```

A job cancelled with `Cancel`, kept for `KeepFailed`.

### `pgjobs.Record`

```go
type Record struct {
	ID          ID
	Kind        string
	Queue       string
	State       State
	Args        []byte
	Priority    int
	Attempt     int
	MaxAttempts int
	UniqueKey   string
	ScheduledAt time.Time
	RunAt       time.Time
	CreatedAt   time.Time
	AttemptedAt time.Time
	FinishedAt  time.Time
	LeaseUntil  time.Time
	LastError   string
	Trace       bites.Trace
}
```

Record is a job of any kind as stored, its arguments as raw JSON: what `List` and `Get` return and what an Observer sees. `Attempt` counts the attempts so far, the running one included. `AttemptedAt`, `FinishedAt` and `LeaseUntil` are zero when they do not apply. `Trace` is the trace context of the request that enqueued it as `bites.ParseTraceparent` reads it (the enqueuing request's span as `Parent`); the zero Trace when none, and the tracestate is not kept.

### `pgjobs.Observer`

```go
type Observer interface {
	Start(ctx context.Context, job Record) context.Context
	End(ctx context.Context, job Record, out Outcome)
}
```

Observer sees each attempt, as a `bites.Observer` sees a request: `Start` before the handler, returning the `ctx` the handler runs with, and `End` with that `ctx` after the attempt was recorded. The first of `Options.Observers` is the outermost. A panic in either is recovered and logged. A tracing Observer starts its span in `Start` from `bites.TraceFrom(ctx)`, whose `Parent` is the enqueuing request's span, as the otel module does for a request.

### `pgjobs.Outcome`

```go
type Outcome struct {
	State    State
	Err      error
	Panicked bool
	Snoozed  bool
	Released bool
	Lost     bool
	Next     time.Time
	Took     time.Duration
}
```

Outcome is what an attempt came to. `State` is the state it left the job in (empty when `Lost` and unreadable, `StateRunning` when the record could not be written). `Released` is a job put back at shutdown; `Lost` is a job that was no longer this worker's, so nothing was recorded. `Next` is when an available job is due again. `Took` is the handler's real time.

### `pgjobs.Count`

```go
type Count struct {
	Queue string
	State State
	Jobs  int
}
```

Count is the jobs of one queue in one state.

### `pgjobs.ListOptions`

```go
type ListOptions struct {
	State  State
	Queue  string
	Kind   string
	Before ID
	Limit  int
}
```

`State`, `Queue` and `Kind` keep only matching jobs when not empty. `Before` keeps the jobs enqueued before that id: give the last id of a page for the next. `Limit` is 100 when zero, at most 1000.

### `pgjobs.ID`

```go
type ID [16]byte
```

ID is a job's id, a UUIDv7 made from the Client's clock (to a 244 ns fraction of a millisecond) and 62 random bits, so ids sort by enqueue time and, within one process, in the order made. It is a `uuid` in Postgres and the canonical text in JSON and in a path, so a route can take it as a path parameter.

### `pgjobs.ID.String`

```go
func (id ID) String() string
```

String is the canonical form, `8-4-4-4-12` lower-case hex digits.

### `pgjobs.ID.MarshalText`

```go
func (id ID) MarshalText() ([]byte, error)
```

MarshalText is `String` as bytes.

### `pgjobs.ID.UnmarshalText`

```go
func (id *ID) UnmarshalText(b []byte) error
```

UnmarshalText reads what `ParseID` reads.

### `pgjobs.ParseID`

```go
func ParseID(s string) (ID, error)
```

ParseID reads the canonical form, upper or lower case; anything else is `pgjobs: "<s>" is not a job id: ...`.

### `pgjobs.ErrNotFound`

```go
var ErrNotFound = errors.New("pgjobs: no job has this id")
```

Returned by `Get`, `Retry` and `Cancel` for an id with no job: never stored, or pruned.

### `pgjobs.ErrRunning`

```go
var ErrRunning = errors.New("pgjobs: the job is running; wait for it to end, or Cancel it first")
```

Returned by `Retry` for a running job.

### `pgjobs.ErrFinished`

```go
var ErrFinished = errors.New("pgjobs: the job has already finished (completed, failed or cancelled)")
```

Returned by `Cancel` for a job that has finished.

### `pgjobs.ErrStopping`

```go
var ErrStopping = errors.New("pgjobs: the Service is stopping and could not wait for this job any longer")
```

The cause of a running job's `ctx` (`context.Cause`) when the Service stops and cannot wait for it. A handler that returns after it is put back without counting the attempt.

### `pgjobs.ErrLost`

```go
var ErrLost = errors.New("pgjobs: the job is no longer this worker's: it was cancelled, or its lease ran out")
```

The cause of a running job's `ctx` when it was cancelled, or when its lease could not be extended before it ran out and another worker may take it. What the handler returns afterwards is not recorded.

### `pgjobs.DefaultTable`

```go
const DefaultTable = "bites_jobs"
```

The table when `Options.Table` is empty.

### `pgjobs.DefaultQueue`

```go
const DefaultQueue = "default"
```

A kind's queue when `KindOptions.Queue` is empty, and the one queue when `Options.Queues` is.

### `pgjobs.DefaultWorkers`

```go
const DefaultWorkers = 10
```

The jobs of `DefaultQueue` an instance runs at once when `Options.Queues` is empty.

### `pgjobs.DefaultMaxAttempts`

```go
const DefaultMaxAttempts = 20
```

The attempts of a job when neither its kind nor its enqueue says.

### `pgjobs.DefaultTimeout`

```go
const DefaultTimeout = time.Minute
```

An attempt's timeout when `KindOptions.Timeout` is zero.

### `pgjobs.DefaultLease`

```go
const DefaultLease = 30 * time.Second
```

The lease when `Options.Lease` is zero.

### `pgjobs.DefaultPollEvery`

```go
const DefaultPollEvery = time.Second
```

The poll when `Options.PollEvery` is zero.

### `pgjobs.DefaultKeepCompleted`

```go
const DefaultKeepCompleted = 24 * time.Hour
```

How long a completed job is kept when `Options.KeepCompleted` is zero.

### `pgjobs.DefaultKeepFailed`

```go
const DefaultKeepFailed = 7 * 24 * time.Hour
```

How long a failed or cancelled job is kept when `Options.KeepFailed` is zero.

### `pgjobs.MaxArgs`

```go
const MaxArgs = 1 << 20
```

The largest a job's arguments may be as JSON, in bytes. A job carries the ids of what it works on, not the data.

### `pgjobs.MaxUniqueKey`

```go
const MaxUniqueKey = 512
```

The longest a unique key may be, in bytes.
