// Package pgsession is a bites.OwnerSessionStore over Postgres: an application that runs only
// Postgres keeps its sessions there, and ends every session of a User with
// Sessions.RevokeOwner, without writing a store of its own (ADR 0082, ADR 0056).
//
// The session id is kept only as its SHA-256, so a dump of the table hands out no id that a
// cookie could be forged from. One row holds the id's hash, the owner, the data and the expiry;
// the owner and the expiry are indexed, so ending an owner's sessions and sweeping expired rows
// do not scan the table. Several instances share one table without coordinating. The package
// depends on github.com/jackc/pgx/v5.
//
// Whether a session has expired is judged on the application's clock, bites.Now(ctx), never on
// Postgres's. To end sessions in the same transaction as the application's own change, such as a
// password change, give the calls a ctx made by WithTx.
//
// Make the table with Schema (to paste into a migration) or CreateTable, give the Store to
// bites.SessionOptions, and register its Service so expired rows are deleted:
//
//	store := pgsession.New(pgsession.Options{Pool: pool})
//	app.Service(store.Service())
//	carts := bites.NewSessions[Login](bites.SessionOptions{Keys: keys, Store: store})
package pgsession

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
)

// DefaultTable is the table used when Options.Table is empty.
const DefaultTable = "bites_sessions"

// DefaultSweepEvery is how often expired rows are deleted when Options.SweepEvery is zero.
const DefaultSweepEvery = 10 * time.Minute

// sweepBatch is how many expired rows one DELETE removes, so a sweep after a long outage holds
// no lock on the whole table and no long transaction.
const sweepBatch = 1000

// maxTable keeps the index names, which add a suffix, inside Postgres's 63 bytes.
const maxTable = 50

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	_ bites.OwnerSessionStore = (*Store)(nil)
)

// Options are the settings of New.
type Options struct {
	// Pool is the application's pool. Required. The Store never closes it; close the Store's
	// Service (stop the app) before the pool.
	Pool *pgxpool.Pool
	// Table is the table the sessions are kept in: a plain identifier of letters, digits and
	// underscores that does not start with a digit, at most 50 bytes. Empty means
	// "bites_sessions". Applications that share a database and want separate sessions use
	// different tables.
	Table string
	// SweepEvery is how often the Service deletes expired rows. Zero means 10 minutes. An expired
	// row is never loaded whether or not it was swept; the sweep only frees the space.
	SweepEvery time.Duration
	// Log receives each failed sweep and each sweep that deleted rows. Nil means slog.Default().
	Log *slog.Logger
}

// Store implements bites.OwnerSessionStore over Postgres. Make one with New; the zero value is
// not usable. It is safe for concurrent use, and any number of instances may share a table.
type Store struct {
	opt   Options
	table string // quoted
	q     queries

	mu     sync.Mutex
	cancel context.CancelFunc
}

type queries struct{ load, save, del, delOwner, sweep, schema string }

// New returns a Store; it connects nothing and creates nothing. It panics, naming the line that
// called it, when Pool is nil, Table is not a plain identifier of at most 50 bytes, or SweepEvery
// is negative.
func New(o Options) *Store {
	_, file, line, _ := runtime.Caller(1)
	where := fmt.Sprintf("%s:%d", filepath.Base(file), line)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("pgsession.New: %s\n\tregistered at %s", fmt.Sprintf(format, args...), where))
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
	if o.SweepEvery < 0 {
		fail("SweepEvery is %v, which is not a time: give a positive duration, or zero for %v", o.SweepEvery, DefaultSweepEvery)
	}
	if o.SweepEvery == 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	t := `"` + o.Table + `"`
	s := &Store{opt: o, table: t}
	s.q = queries{
		load:     `SELECT data FROM ` + t + ` WHERE id_hash = $1 AND expires > $2`,
		save:     `INSERT INTO ` + t + ` (id_hash, owner, data, expires) VALUES ($1, NULLIF($2, ''), $3, $4) ON CONFLICT (id_hash) DO UPDATE SET owner = EXCLUDED.owner, data = EXCLUDED.data, expires = EXCLUDED.expires`,
		del:      `DELETE FROM ` + t + ` WHERE id_hash = $1`,
		delOwner: `DELETE FROM ` + t + ` WHERE owner = $1 AND id_hash <> $2`,
		sweep:    fmt.Sprintf(`DELETE FROM %[1]s WHERE id_hash IN (SELECT id_hash FROM %[1]s WHERE expires <= $1 LIMIT %d FOR UPDATE SKIP LOCKED)`, t, sweepBatch),
		schema: fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s (
	id_hash bytea PRIMARY KEY,
	owner   text,
	data    bytea NOT NULL,
	expires timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS "%[2]s_owner_idx" ON %[1]s (owner) WHERE owner IS NOT NULL;
CREATE INDEX IF NOT EXISTS "%[2]s_expires_idx" ON %[1]s (expires);
`, t, o.Table),
	}
	return s
}

// Schema returns the SQL that creates the table and its two indexes, each statement with IF NOT
// EXISTS, for an application that applies its schema with a migration tool: paste it into a
// migration. CreateTable runs the same text.
func (s *Store) Schema() string { return s.q.schema }

// CreateTable runs Schema, for an application that has no migration tool. Running it again, or
// against a table that exists, changes nothing.
func (s *Store) CreateTable(ctx context.Context) error {
	if _, err := s.opt.Pool.Exec(ctx, s.q.schema); err != nil {
		return fmt.Errorf("pgsession: create table %s: %w", s.opt.Table, err)
	}
	return nil
}

// Querier is what WithTx accepts: the two methods of a pgx connection that a Store runs its
// statements with. A pgx.Tx, a *pgxpool.Conn and a *pgxpool.Pool all satisfy it.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// WithTx returns a ctx that makes every Store call made with it (Load, Save, SaveOwned, Delete and
// DeleteOwner) run on q, usually the application's pgx.Tx, instead of on the pool, so a session
// change commits or rolls back with the application's own statements: a password change sets the
// hash and ends the other sessions in one transaction,
//
//	tx, _ := pool.Begin(ctx)
//	defer tx.Rollback(ctx)
//	// UPDATE users SET password_hash = ... inside tx
//	err = sess.RevokeOthers(pgsession.WithTx(ctx, tx))
//	err = tx.Commit(ctx)
//
// Only this Store reads the value: another store, a Service and Sweep ignore it, and Sweep and
// CreateTable always use the pool. The Store does not begin, commit or roll back q. A q whose
// transaction has ended makes each call return the pgx error (pgx.ErrTxClosed), wrapped as
// "pgsession: <operation>: ...", and does not fall back to the pool. It panics when q is nil, since
// a nil would fail only at the first call, far from the line that made it.
func WithTx(ctx context.Context, q Querier) context.Context {
	if q == nil {
		panic("pgsession.WithTx: q is nil: give the application's pgx.Tx, such as the one pool.Begin(ctx) returned")
	}
	return context.WithValue(ctx, txKey{}, q)
}

// db is where a statement runs: the Querier WithTx put in ctx, else the pool.
func (s *Store) db(ctx context.Context) Querier {
	if q, ok := ctx.Value(txKey{}).(Querier); ok {
		return q
	}
	return s.opt.Pool
}

// hash is the key a session is stored under: the id is never written to the table.
func hash(id string) []byte { h := sha256.Sum256([]byte(id)); return h[:] }

// Load returns the data saved for id, and false when there is none or it has expired. Whether a
// row has expired is judged on the application's clock, bites.Now(ctx), which is the same clock
// that computed the expiry and that checks the one sealed in the cookie, and never on Postgres's:
// a database whose clock drifts from the application's, or an application on a test clock, does
// not disagree with the cookie. A ctx that carries no app (a test's context.Background) reads
// time.Now(). It runs in the transaction of WithTx when ctx carries one.
func (s *Store) Load(ctx context.Context, id string) ([]byte, bool, error) {
	var data []byte
	err := s.db(ctx).QueryRow(ctx, s.q.load, hash(id), bites.Now(ctx)).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("pgsession: load: %w", err)
	}
	return data, true, nil
}

// Save keeps data for id until expires with no owner, replacing what id had, owner included. The
// framework calls SaveOwned, not Save, for a session type that states an owner.
func (s *Store) Save(ctx context.Context, id string, data []byte, expires time.Time) error {
	return s.SaveOwned(ctx, id, "", data, expires)
}

// SaveOwned is Save that also records owner for id, replacing the owner id had. An empty owner
// is stored as NULL: the session is kept and DeleteOwner never finds it. An owner is text
// Postgres can store: one with a NUL byte or invalid UTF-8 is an error from the database, which
// the framework answers as a 500; a User's id is neither.
func (s *Store) SaveOwned(ctx context.Context, id, owner string, data []byte, expires time.Time) error {
	if _, err := s.db(ctx).Exec(ctx, s.q.save, hash(id), owner, data, expires); err != nil {
		return fmt.Errorf("pgsession: save: %w", err)
	}
	return nil
}

// Delete forgets id. Deleting an id that has nothing is not an error.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db(ctx).Exec(ctx, s.q.del, hash(id)); err != nil {
		return fmt.Errorf("pgsession: delete: %w", err)
	}
	return nil
}

// DeleteOwner forgets every session of owner but the one whose id is keep, expired rows not yet
// swept included, and returns how many rows it deleted. An empty keep keeps none: no id hashes to
// the digest of "". An owner with no session, and the empty owner, which names no one, are
// (0, nil) without a query.
func (s *Store) DeleteOwner(ctx context.Context, owner, keep string) (int, error) {
	if owner == "" {
		return 0, nil
	}
	tag, err := s.db(ctx).Exec(ctx, s.q.delOwner, owner, hash(keep))
	if err != nil {
		return 0, fmt.Errorf("pgsession: delete owner: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Sweep deletes the rows that have expired on the application's clock, bites.Now(ctx) (the
// Service's ctx carries it; any other ctx reads time.Now()), in batches of 1000, and returns how
// many. It always runs on the pool, never in the transaction of WithTx. It is what
// the Service runs every SweepEvery; call it directly to sweep at a moment of the application's
// choosing. Several instances may sweep at once: a row being deleted by one is skipped by the
// others.
func (s *Store) Sweep(ctx context.Context) (int, error) {
	now := bites.Now(ctx)
	total := 0
	for {
		tag, err := s.opt.Pool.Exec(ctx, s.q.sweep, now)
		if err != nil {
			return total, fmt.Errorf("pgsession: sweep: %w", err)
		}
		n := int(tag.RowsAffected())
		total += n
		if n < sweepBatch {
			return total, nil
		}
	}
}

// Service returns the bites.Service that sweeps expired rows every SweepEvery: register it with
// app.Service. It is a bites.Every named "pgsession": Start returns at once and the sweeping goes
// on until Stop, which waits for a sweep in progress to end. It sweeps first after one SweepEvery,
// not at start, so a start never waits for the database, and on the app's clock. A failed sweep
// is logged at Warn by Every, with the app's logger, and tried again at the next tick. The Store
// serves one Service at a time.
func (s *Store) Service() bites.Service {
	svc := bites.Every("pgsession", s.opt.SweepEvery, func(ctx context.Context) error {
		n, err := s.Sweep(ctx)
		if err != nil {
			return fmt.Errorf("%w (table %s, %d rows deleted before it failed)", err, s.opt.Table, n)
		}
		if n > 0 {
			s.opt.Log.Info("pgsession: swept expired sessions", "table", s.opt.Table, "deleted", n)
		}
		return nil
	})
	start, stop := svc.Start, svc.Stop
	svc.Start = func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cancel != nil {
			return errors.New("pgsession: the Service is already started: register one Service per Store")
		}
		ctx, cancel := context.WithCancel(ctx)
		if err := start(ctx); err != nil {
			cancel()
			return err
		}
		s.cancel = cancel
		return nil
	}
	svc.Stop = func(ctx context.Context) error {
		s.mu.Lock()
		cancel := s.cancel
		s.cancel = nil
		s.mu.Unlock()
		if cancel == nil {
			return nil
		}
		cancel()
		return stop(ctx)
	}
	return svc
}
