# pgsession

**The module that keeps sessions in Postgres as a `bites.OwnerSessionStore`, so a session can be ended at once and every session of one User with `RevokeOwner`, with the session id stored only as its SHA-256 and expired rows swept.**

**Guide:** [Security](../guide/security.md) · **Design:** [Security](../design/security.md)

`github.com/nevindra/bites/modules/pgsession` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. It depends on `github.com/jackc/pgx/v5` at v5.10.0. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/pgsession && go test ./...`, and the tests that need a database run when `BITES_PG_URL` names one and are skipped, with a message naming it, otherwise. The decision is [ADR 0082](../adr/0082-a-postgres-session-store-is-a-module.md); the interfaces it implements are [`SessionStore`](./security.md) and `OwnerSessionStore` ([ADR 0056](../adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md)).

```go
store := pgsession.New(pgsession.Options{Pool: pool})
if err := store.CreateTable(ctx); err != nil { /* or paste store.Schema() into a migration */ }
app.Service(store.Service())
logins := bites.NewSessions[Login](bites.SessionOptions{Keys: keys, Store: store})
```

The table, as `Schema` returns it for the default name:

```sql
CREATE TABLE IF NOT EXISTS "bites_sessions" (
	id_hash bytea PRIMARY KEY,
	owner   text,
	data    bytea NOT NULL,
	expires timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS "bites_sessions_owner_idx" ON "bites_sessions" (owner) WHERE owner IS NOT NULL;
CREATE INDEX IF NOT EXISTS "bites_sessions_expires_idx" ON "bites_sessions" (expires);
```

**Changing the table from the application.** A deletion inside the application's own statement is a plain `DELETE FROM "bites_sessions" WHERE owner = $1` for every session of an owner; for one session the key is the hash of the id, `WHERE id_hash = sha256($1::bytea)` (Postgres 11 and later), for an operator who holds an id, since the id itself is never stored and the application never sees it. An application may add its own columns, constraints or foreign keys to the table through its own migration, provided the columns pgsession uses stay as `Schema` makes them (`id_hash bytea` as the primary key, `owner text` that may be NULL, `data bytea`, `expires timestamptz`) and a column it adds either has a default or may be NULL, since pgsession's `INSERT` names only those four. A foreign key from `owner` to a users table makes a session of an unknown owner a refused `SaveOwned`.

### `pgsession.New`

```go
func New(o Options) *Store
```

New returns a Store and connects nothing. It panics, `pgsession.New: <reason>` and `registered at file.go:N` on the next line, for each row of this table:

| Mistake | Message begins |
|---|---|
| `Pool` is nil | `Pool is nil: give the application's *pgxpool.Pool, such as pgxpool.New(ctx, url)` |
| `Table` has a character other than letters, digits and underscore, starts with a digit, or is over 50 bytes | `Table "<name>" is not a plain identifier: give letters, digits and underscores, not starting with a digit, at most 50 bytes, or leave it empty for "bites_sessions"` |
| `SweepEvery` is negative | `SweepEvery is <d>, which is not a time: give a positive duration, or zero for 10m0s` |

### `pgsession.Options`

```go
type Options struct {
	Pool       *pgxpool.Pool
	Table      string
	SweepEvery time.Duration
	Log        *slog.Logger
}
```

`Pool` is the application's pool; required. The Store never closes it. `Table` is the table the sessions are kept in, `bites_sessions` when empty: a plain identifier, at most 50 bytes so the index names fit Postgres's 63; applications that share a database and want separate sessions use different tables. `SweepEvery` is how often the Service deletes expired rows, 10 minutes when zero; an expired row is never loaded whether or not it was swept. `Log` receives each failed sweep (Warn) and each sweep that deleted rows (Info); `slog.Default()` when nil.

### `pgsession.DefaultTable`

```go
const DefaultTable = "bites_sessions"
```

DefaultTable is the table used when `Options.Table` is empty.

### `pgsession.DefaultSweepEvery`

```go
const DefaultSweepEvery = 10 * time.Minute
```

DefaultSweepEvery is how often expired rows are deleted when `Options.SweepEvery` is zero.

### `pgsession.Store`

```go
type Store struct { /* unexported */ }
```

Store implements `bites.OwnerSessionStore` over Postgres. Make one with `New`; the zero value is not usable. It is safe for concurrent use, and any number of instances may share a table. A Postgres error is returned wrapped, `pgsession: <operation>: <pgx error>`, with `errors.Is` and `errors.As` working through it; the framework answers a store error with a 500.

### `pgsession.Store.Schema`

```go
func (s *Store) Schema() string
```

Schema returns the SQL above for this Store's table: the table and its two indexes, each statement with `IF NOT EXISTS`, to paste into a migration of goose, Atlas or any other tool.

### `pgsession.Store.CreateTable`

```go
func (s *Store) CreateTable(ctx context.Context) error
```

CreateTable runs `Schema`, for an application with no migration tool. Running it twice changes nothing. It is the same text as `Schema`, so the two cannot differ.

### `pgsession.Store.Load`

```go
func (s *Store) Load(ctx context.Context, id string) ([]byte, bool, error)
```

Load returns the data saved for `id`, and false when there is none or it has expired. A row has expired when its `expires` is not after `bites.Now(ctx)`, the application's clock (`WithClock`, `bitetest.Clock`), passed as a parameter: the clock that computed `expires` and that checks the expiry sealed in the cookie ([ADR 0056](../adr/0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md)) decides, never Postgres's `now()`, so an application on a test clock and a database whose clock drifts do not disagree with the cookie. A ctx that carries no app reads `time.Now()`. It runs in the transaction of [`WithTx`](#pgsessionwithtx) when the ctx carries one.

### `pgsession.Store.Save`

```go
func (s *Store) Save(ctx context.Context, id string, data []byte, expires time.Time) error
```

Save keeps `data` for `id` until `expires`, replacing what `id` had, its owner included, which becomes none. It is an upsert on the hash of `id`. The framework calls `SaveOwned` instead for a session type with a `SessionOwner` method.

### `pgsession.Store.SaveOwned`

```go
func (s *Store) SaveOwned(ctx context.Context, id, owner string, data []byte, expires time.Time) error
```

SaveOwned is `Save` that also records `owner` for `id`, replacing the owner `id` had. An empty owner is stored as NULL: the session is kept, and `DeleteOwner` never finds it. An owner is text Postgres can store: one with a NUL byte or invalid UTF-8 is an error from the database, a 500; a User's id is neither.

### `pgsession.Store.Delete`

```go
func (s *Store) Delete(ctx context.Context, id string) error
```

Delete forgets `id`. Deleting an id that has nothing is not an error.

### `pgsession.Store.DeleteOwner`

```go
func (s *Store) DeleteOwner(ctx context.Context, owner, keep string) (int, error)
```

DeleteOwner forgets every session of `owner` but the one whose id is `keep` and returns how many rows it deleted, rows that expired and were not yet swept included. An empty `keep` keeps none (`Sessions.RevokeOwner`); a `keep` that is a session of `owner` is the one `Session.RevokeOthers` leaves. An owner with no session is `(0, nil)`. The empty owner names no one: it is `(0, nil)` with no query, and `Sessions.RevokeOwner` refuses it before it gets here.

### `pgsession.Querier`

```go
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

Querier is what `WithTx` accepts: the two methods a Store runs its statements with. A `pgx.Tx`, a `*pgxpool.Conn` and a `*pgxpool.Pool` all satisfy it, so nothing needs wrapping.

### `pgsession.WithTx`

```go
func WithTx(ctx context.Context, q Querier) context.Context
```

WithTx returns a ctx that makes every call of this Store made with it (`Load`, `Save`, `SaveOwned`, `Delete` and `DeleteOwner`) run on `q`, usually the application's `pgx.Tx`, instead of on the pool, so a session change commits or rolls back with the application's own statements. A password change sets the hash and ends the other sessions in one transaction, with the session id still hidden from the application:

```go
tx, _ := pool.Begin(ctx)
defer tx.Rollback(ctx)
// UPDATE users SET password_hash = ... inside tx
_, err := sess.RevokeOthers(pgsession.WithTx(ctx, tx))
err = tx.Commit(ctx)
```

Only this Store reads the value: another `SessionStore`, a Service and `Sweep` ignore it, and `Sweep` and `CreateTable` always use the pool. The Store does not begin, commit or roll back `q`. A `q` whose transaction has ended makes each call return the pgx error (`pgx.ErrTxClosed`), wrapped as `pgsession: <operation>: ...`, and does not fall back to the pool. A nil `q` panics at once, `pgsession.WithTx: q is nil: give the application's pgx.Tx, such as the one pool.Begin(ctx) returns`.

### `pgsession.Store.Sweep`

```go
func (s *Store) Sweep(ctx context.Context) (int, error)
```

Sweep deletes the rows whose `expires` has passed on `bites.Now(ctx)` (the Service's ctx carries the app's clock; any other ctx reads `time.Now()`), always on the pool and never in a `WithTx` transaction, in batches of 1000 so no sweep holds a long transaction, and returns how many. The Service runs it every `SweepEvery`; call it directly to sweep at another moment. Several instances may sweep at once: each batch takes its rows with `FOR UPDATE SKIP LOCKED`, so a row one is deleting is skipped by the others and none waits.

### `pgsession.Store.Service`

```go
func (s *Store) Service() bites.Service
```

Service returns the [`bites.Service`](./app.md) named `pgsession` that sweeps every `SweepEvery`, built on [`bites.Every`](./app.md#every): `app.Service(store.Service())`. `Start` returns at once and the sweeping goes on until `Stop`, which waits for a sweep in progress; the first sweep is after one `SweepEvery`, so a start never waits for the database. A failed sweep is logged at Warn by `Every`, with the app's logger, and tried again at the next tick. `Start` called a second time on a Store that is running returns an error: register one Service per Store. Register it after the pool's own Service, if the application has one, so it stops first.
