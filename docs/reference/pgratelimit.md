# pgratelimit

**The module that keeps the buckets of a `RateLimiter` in Postgres as a `bites.RateLimitStore`, so a limit holds across every instance of an application with one statement and one round trip for each limited request, the key stored only as its SHA-256 and idle rows swept.**

**Guide:** [Security](../guide/security.md) · **Design:** [Security](../design/security.md)

`github.com/nevindra/bites/modules/pgratelimit` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. It depends on `github.com/jackc/pgx/v5` at v5.10.0. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/pgratelimit && go test ./...`, and the tests that need a database run when `BITES_PG_URL` names one and are skipped, with a message naming it, otherwise. The decision is [ADR 0085](../adr/0085-a-postgres-rate-limit-store-is-a-module.md); the interface it implements is [`RateLimitStore`](./security.md#ratelimitstore) ([ADR 0055](../adr/0055-rate-limiting-is-a-token-bucket-per-key-with-a-bounded-store.md)), and it passes [`bitetest.TestRateLimitStore`](./bitetest.md#bitetesttestratelimitstore).

```go
store := pgratelimit.New(pgratelimit.Options{Pool: pool, Name: "login"})
if err := store.CreateTable(ctx); err != nil { /* or paste store.Schema() into a migration */ }
app.Service(store.Service())
perEmail := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 5, Period: time.Minute, Store: store, Name: "login"})
```

A bucket is one row, the time it is next full (GCRA, as the in-memory store keeps it). The table, as `Schema` returns it for the default name:

```sql
CREATE UNLOGGED TABLE IF NOT EXISTS "bites_ratelimit" (
	key_hash bytea PRIMARY KEY,
	tat      bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS "bites_ratelimit_tat_idx" ON "bites_ratelimit" (tat);
```

What to know before using it:

- **One round trip for each limited request** (two in the instant two instances create the same key). Measured against Postgres 17 over loopback, about 26 µs a `Take`; a network adds its round trip.
- **The time is the application's clock**, the `now` a `Take` is given, not Postgres's. Instances whose clocks differ by `s` differ in their limits by at most `s` of bucket (`s/interval` requests): keep them within a second with NTP.
- **The table is `UNLOGGED`**: a crash of Postgres or a failover to a replica empties it, which resets every limit once. A bucket is not worth a write-ahead log (a logged table measured about forty times slower a `Take`); an application that wants one changes the word in the `Schema` text.
- **The key is stored as a hash** of the `Name` and the key, so a dump of the table shows no email or address. A key from a small space (an email) can still be tried against a list.
- **Give each `RateLimiter` its own store with its own `Name`.** A store is not told which limiter calls it, so two limiters on one table and one `Name` share the bucket of a key (an email limited for sign-in and for invitations).
- **Register one Service for each table, not one for each Store.** The sweep deletes the idle rows of every limiter in the table, whatever its `Name`, so the Service of any one Store on a table sweeps for all of them. Every Service is named `pgratelimit`, so the app refuses a second one (`Service "pgratelimit" is registered twice`); a Store on another table is swept by a `bites.Every` of another name that calls its `Sweep`.

### `pgratelimit.New`

```go
func New(o Options) *Store
```

New returns a Store and connects nothing. It panics, `pgratelimit.New: <reason>` and `registered at file.go:N` on the next line, for each row of this table:

| Mistake | Message begins |
|---|---|
| `Pool` is nil | `Pool is nil: give the application's *pgxpool.Pool, such as pgxpool.New(ctx, url)` |
| `Table` has a character other than letters, digits and underscore, starts with a digit, or is over 50 bytes | `Table "<name>" is not a plain identifier: give letters, digits and underscores, not starting with a digit, at most 50 bytes, or leave it empty for "bites_ratelimit"` |
| `SweepEvery` is negative | `SweepEvery is <d>, which is not a time: give a positive duration, or zero for 10m0s` |

Not refused, because nothing can see it before a request: two limiters that share a table and a `Name`, and instances of one limiter given different `Name`s, which do not share their buckets.

### `pgratelimit.Options`

```go
type Options struct {
	Pool       *pgxpool.Pool
	Table      string
	Name       string
	SweepEvery time.Duration
	Log        *slog.Logger
}
```

`Pool` is the application's pool; required. The Store never closes it. `Table` is the table the buckets are kept in, `bites_ratelimit` when empty: a plain identifier, at most 50 bytes so the index name fits Postgres's 63. `Name` names the limiter this Store serves and is mixed into the hash of every key, so limiters that share a table do not share a bucket; empty is a name like any other, so at most one limiter per table leaves it empty, and the instances of one limiter must give the same `Name`. `SweepEvery` is how often the Service deletes idle rows, 10 minutes when zero; a row that is idle is the same as no row. `Log` receives each failed sweep (Warn) and each sweep that deleted rows (Info); `slog.Default()` when nil.

### `pgratelimit.DefaultTable`

```go
const DefaultTable = "bites_ratelimit"
```

DefaultTable is the table used when `Options.Table` is empty.

### `pgratelimit.DefaultSweepEvery`

```go
const DefaultSweepEvery = 10 * time.Minute
```

DefaultSweepEvery is how often idle rows are deleted when `Options.SweepEvery` is zero.

### `pgratelimit.SweepGrace`

```go
const SweepGrace = time.Minute
```

SweepGrace is how long a bucket must have been full before the sweep deletes its row. A full bucket is what a new key gets, so deleting it loses nothing; the grace keeps that true when the clock of the instance that sweeps is ahead of the clock of the one that wrote the row.

### `pgratelimit.Store`

```go
type Store struct { /* unexported */ }
```

Store implements `bites.RateLimitStore` over Postgres. Make one with `New`; the zero value is not usable. It is safe for concurrent use, and any number of instances may share a table. A Postgres error is returned wrapped, `pgratelimit: <operation>: <pgx error>`, with `errors.Is` and `errors.As` working through it; a limiter answers a store error with a 500 and so fails closed.

### `pgratelimit.Store.Schema`

```go
func (s *Store) Schema() string
```

Schema returns the SQL above for this Store's table: the unlogged table and its index, each statement with `IF NOT EXISTS`, to paste into a migration of goose, Atlas or any other tool. `IF NOT EXISTS` never converts a table that exists to or from `UNLOGGED`.

### `pgratelimit.Store.CreateTable`

```go
func (s *Store) CreateTable(ctx context.Context) error
```

CreateTable runs `Schema`, for an application with no migration tool. Running it twice changes nothing. It is the same text as `Schema`, so the two cannot differ.

### `pgratelimit.Store.Take`

```go
func (s *Store) Take(ctx context.Context, key string, now time.Time, burst int, interval time.Duration) (bites.RateLimitDecision, error)
```

Take spends one request of `key`'s bucket at `now`, for a bucket of `burst` requests that earns one back every `interval`, and says what is left, as [`RateLimitStore`](./security.md#ratelimitstore) defines. It is one statement (`INSERT ... ON CONFLICT DO UPDATE ... WHERE ... RETURNING`), atomic for a key across every instance that shares the table, so two callers never both get the last request. A refused request writes no new version of the row, so a client that keeps trying does not push its own `RetryAfter` away; its `RetryAfter` and `Reset` are read from the row as the statement began, so under heavy contention on one key `RetryAfter` can be a little early and is never negative. A `burst` under one, an `interval` that is not positive, or a `burst` and `interval` whose bucket does not fit in an int64 of nanoseconds is an error that says what to change, as is a failure of the database. You do not call it: the `RateLimiter` does.

### `pgratelimit.Store.Sweep`

```go
func (s *Store) Sweep(ctx context.Context) (int, error)
```

Sweep deletes the rows whose bucket has been full for longer than `SweepGrace`, in batches of 1000 so no sweep holds a long transaction, and returns how many. It decides with `bites.Now(ctx)`, the application's clock (the Service's ctx carries it; any other ctx reads `time.Now()`), the clock `Take` is given its `now` from. The Service runs it every `SweepEvery`; call it directly to sweep at another moment. Several instances may sweep at once: each batch takes its rows with `FOR UPDATE SKIP LOCKED`, so a row one is deleting is skipped by the others and none waits.

### `pgratelimit.Store.Service`

```go
func (s *Store) Service() bites.Service
```

Service returns the [`bites.Service`](./app.md) named `pgratelimit` that sweeps every `SweepEvery`: `app.Service(store.Service())`. `Start` returns at once and the sweeping goes on until `Stop`, which waits for a sweep in progress; the first sweep is after one `SweepEvery`, so a start never waits for the database. The Service is a [`bites.Every`](./app.md#every) and sweeps on the app's clock. A failed sweep is logged at Warn by `Every`, with the app's logger, and tried again at the next tick. `Start` called a second time without a `Stop` is an error saying to register one Service per Store. The sweep is of the whole table, every `Name` in it, so several Stores on one table register the Service of one of them only; the app refuses a second Service named `pgratelimit`.
