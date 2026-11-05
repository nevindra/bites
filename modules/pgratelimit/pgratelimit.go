// Package pgratelimit is a bites.RateLimitStore over Postgres: an application that runs several
// instances behind one balancer keeps the buckets of its RateLimiters in the Postgres it already
// has, so a limit holds for the application and not for each instance (ADR 0085, ADR 0055).
//
// A bucket is one row: the SHA-256 of the key (keys hold emails and IP addresses, which a dump of
// the table then does not hand out) and one number, the time the bucket is next full (GCRA, the
// algorithm of the in-memory store). Take is one statement, and so one round trip, that spends a
// request atomically with INSERT ... ON CONFLICT DO UPDATE: two instances taking at once cannot
// both get the last request, with no lock held beyond the statement. The time is the one Take is
// given, the application's clock, not Postgres's, so instances whose clocks differ by a skew
// differ in their limit by at most that skew. The package depends on github.com/jackc/pgx/v5.
//
// The table is UNLOGGED: a bucket is state that costs nothing to lose (an empty table is every
// bucket full), and skipping the write-ahead log makes a Take about forty times cheaper. A crash of
// Postgres, or a failover to a replica, empties it.
//
// Make the table with Schema (to paste into a migration) or CreateTable, give the Store to
// bites.RateLimitOptions, and register its Service so idle rows are deleted:
//
//	store := pgratelimit.New(pgratelimit.Options{Pool: pool, Name: "login"})
//	app.Service(store.Service())
//	perEmail := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 5, Period: time.Minute, Store: store, Name: "login"})
package pgratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
)

// DefaultTable is the table used when Options.Table is empty.
const DefaultTable = "bites_ratelimit"

// DefaultSweepEvery is how often idle rows are deleted when Options.SweepEvery is zero.
const DefaultSweepEvery = 10 * time.Minute

// SweepGrace is how long a bucket must have been full before the sweep deletes its row. A full
// bucket is what a new key gets, so deleting it loses nothing; the grace keeps that true when the
// clock of the instance that sweeps is ahead of the clock of the one that wrote the row.
const SweepGrace = time.Minute

// sweepBatch is how many idle rows one DELETE removes, so a sweep after a long outage holds no
// lock on the whole table and no long transaction.
const sweepBatch = 1000

// maxTable keeps the index name, which adds a suffix, inside Postgres's 63 bytes.
const maxTable = 50

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	_ bites.RateLimitStore = (*Store)(nil)
)

// Options are the settings of New.
type Options struct {
	// Pool is the application's pool. Required. The Store never closes it; stop the app (which
	// stops the Service) before closing the pool.
	Pool *pgxpool.Pool
	// Table is the table the buckets are kept in: a plain identifier of letters, digits and
	// underscores that does not start with a digit, at most 50 bytes. Empty means
	// "bites_ratelimit".
	Table string
	// Name names the limiter this Store serves. It is mixed into the hash of every key, so two
	// limiters that share a table (and a key, such as one email limited for sign-in and for
	// invitations) do not share a bucket: give each RateLimiter its own Store with its own Name,
	// as its Name. Empty is a name like any other, so at most one limiter per table may leave it
	// empty. Instances of one limiter must give the same Name, or they do not share its buckets.
	Name string
	// SweepEvery is how often the Service deletes idle rows. Zero means 10 minutes. A row that is
	// idle is the same as no row, so the sweep only frees space.
	SweepEvery time.Duration
	// Log receives each failed sweep and each sweep that deleted rows. Nil means slog.Default().
	Log *slog.Logger
}

// Store implements bites.RateLimitStore over Postgres. Make one with New; the zero value is not
// usable. It is safe for concurrent use, and any number of instances may share a table.
type Store struct {
	opt   Options
	table string // quoted
	q     queries

	mu     sync.Mutex
	cancel context.CancelFunc
}

type queries struct{ take, read, sweep, schema string }

// New returns a Store; it connects nothing and creates nothing. It panics, naming the line that
// called it, when Pool is nil, Table is not a plain identifier of at most 50 bytes, or SweepEvery
// is negative.
func New(o Options) *Store {
	_, file, line, _ := runtime.Caller(1)
	where := fmt.Sprintf("%s:%d", filepath.Base(file), line)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("pgratelimit.New: %s\n\tregistered at %s", fmt.Sprintf(format, args...), where))
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
		// $1 key hash, $2 now, $3 interval, $4 the bucket's length (burst intervals), all in
		// nanoseconds. up is the spend: the insert of a new key (always allowed, since a burst
		// of at least one interval is checked before the call) or the update of a key whose
		// bucket, after one more request, is at most the bucket's length ahead of now. When
		// the WHERE is false the row is locked and left as it is (no new version is written),
		// and up is empty; cur is the row as the statement began, which is what a refused
		// request reports. cur is empty only when the key was created by another statement
		// during this one, and Take then reads the row.
		take: `WITH cur AS (SELECT tat FROM ` + t + ` WHERE key_hash = $1),
up AS (
	INSERT INTO ` + t + ` AS t (key_hash, tat) VALUES ($1, $2::bigint + $3::bigint)
	ON CONFLICT (key_hash) DO UPDATE SET tat = GREATEST(t.tat, $2::bigint) + $3::bigint
	WHERE GREATEST(t.tat, $2::bigint) + $3::bigint - $2::bigint <= $4::bigint
	RETURNING tat
)
SELECT (SELECT tat FROM up), (SELECT tat FROM cur)`,
		read:  `SELECT tat FROM ` + t + ` WHERE key_hash = $1`,
		sweep: fmt.Sprintf(`DELETE FROM %[1]s WHERE key_hash IN (SELECT key_hash FROM %[1]s WHERE tat < $1 LIMIT %d FOR UPDATE SKIP LOCKED)`, t, sweepBatch),
		schema: fmt.Sprintf(`CREATE UNLOGGED TABLE IF NOT EXISTS %[1]s (
	key_hash bytea PRIMARY KEY,
	tat      bigint NOT NULL
);
CREATE INDEX IF NOT EXISTS "%[2]s_tat_idx" ON %[1]s (tat);
`, t, o.Table),
	}
	return s
}

// Schema returns the SQL that creates the table (UNLOGGED, so a crash of Postgres empties it and
// every bucket is full again) and its index, each statement with IF NOT EXISTS,
// for an application that applies its schema with a migration tool: paste it into a migration.
// CreateTable runs the same text.
func (s *Store) Schema() string { return s.q.schema }

// CreateTable runs Schema, for an application that has no migration tool. Running it again, or
// against a table that exists, changes nothing.
func (s *Store) CreateTable(ctx context.Context) error {
	if _, err := s.opt.Pool.Exec(ctx, s.q.schema); err != nil {
		return fmt.Errorf("pgratelimit: create table %s: %w", s.opt.Table, err)
	}
	return nil
}

// hash is the key a bucket is stored under: the key is never written to the table. The Name is
// hashed in front of it with its length, so a Name and a key cannot be shifted into one another.
func (s *Store) hash(key string) []byte {
	h := sha256.New()
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(s.opt.Name))))
	h.Write([]byte(s.opt.Name))
	h.Write([]byte(key))
	return h.Sum(nil)
}

// Take spends one request of key's bucket at now, for a bucket of burst requests that earns one
// back every interval, with one statement and so one round trip (two in the instant two
// instances create the same key), and says what is left (see
// bites.RateLimitStore). It is atomic for a key across every instance that shares the table. A
// request that is refused changes nothing, so a client that keeps trying does not push its own
// retry time away. now is the time Take decides with (the application's clock), never Postgres's:
// see ADR 0085 for what a skew between instances does. A burst under one, an interval that is not
// positive, or a bucket whose length does not fit in nanoseconds in an int64 is an error that says
// so, as is any failure of the database, which a RateLimiter answers as a 500.
func (s *Store) Take(ctx context.Context, key string, now time.Time, burst int, interval time.Duration) (bites.RateLimitDecision, error) {
	switch {
	case burst < 1:
		return bites.RateLimitDecision{}, fmt.Errorf("pgratelimit: burst is %d: a bucket holds at least one request", burst)
	case interval <= 0:
		return bites.RateLimitDecision{}, fmt.Errorf("pgratelimit: interval is %v: a request must take a positive time to earn back", interval)
	case int64(burst) > math.MaxInt64/4/int64(interval):
		return bites.RateLimitDecision{}, fmt.Errorf("pgratelimit: a burst of %d at one request every %v does not fit in an int64 of nanoseconds: give a shorter interval or a smaller burst", burst, interval)
	}
	t, iv, limit := now.UnixNano(), int64(interval), int64(burst)*int64(interval)
	var up, cur *int64
	if err := s.opt.Pool.QueryRow(ctx, s.q.take, s.hash(key), t, iv, limit).Scan(&up, &cur); err != nil {
		return bites.RateLimitDecision{}, fmt.Errorf("pgratelimit: take: %w", err)
	}
	if up != nil {
		return bites.RateLimitDecision{
			Allowed:   true,
			Remaining: int((limit - (*up - t)) / iv),
			Reset:     time.Duration(*up - t),
		}, nil
	}
	if cur == nil {
		// Two instances created the key in the same instant: this statement lost the insert,
		// so its snapshot has no row yet and the row it was refused by does. Read it once.
		if err := s.opt.Pool.QueryRow(ctx, s.q.read, s.hash(key)).Scan(&cur); err != nil {
			return bites.RateLimitDecision{}, fmt.Errorf("pgratelimit: take: %w", err)
		}
	}
	tat := max(*cur, t)
	// The row was read as the statement began; a request that changed it since can leave this
	// a little early, never negative.
	return bites.RateLimitDecision{
		RetryAfter: time.Duration(max(tat+iv-t-limit, 1)),
		Reset:      time.Duration(tat - t),
	}, nil
}

// Sweep deletes the rows whose bucket has been full for longer than SweepGrace, in batches of
// 1000, and returns how many. It is what the Service runs every SweepEvery; call it directly to
// sweep at a moment of the application's choosing. Several instances may sweep at once: a row
// being deleted by one is skipped by the others. It decides with bites.Now(ctx), the application's
// clock (the Service's ctx carries it; any other ctx reads time.Now()), the clock Take is given
// its now from.
func (s *Store) Sweep(ctx context.Context) (int, error) {
	cutoff := bites.Now(ctx).Add(-SweepGrace).UnixNano()
	total := 0
	for {
		tag, err := s.opt.Pool.Exec(ctx, s.q.sweep, cutoff)
		if err != nil {
			return total, fmt.Errorf("pgratelimit: sweep: %w", err)
		}
		n := int(tag.RowsAffected())
		total += n
		if n < sweepBatch {
			return total, nil
		}
	}
}

// Service returns the bites.Service that sweeps idle rows every SweepEvery: register it with
// app.Service. It is a bites.Every named "pgratelimit": Start returns at once and the sweeping
// goes on until Stop, which waits for a sweep in progress. It sweeps first after one SweepEvery,
// not at start, so a start never waits for the database, and on the app's clock. A failed sweep is
// logged at Warn by Every, with the app's logger, and tried again at the next tick. The Store
// serves one Service at a time.
//
// A sweep deletes the idle rows of every limiter in the table, whatever its Name, so an
// application with several Stores on one table registers the Service of one of them only: every
// Service is named "pgratelimit", and the app refuses a second Service of that name. Stores on
// different tables each register their own, wrapped in a bites.Every of another name that calls
// Sweep.
func (s *Store) Service() bites.Service {
	svc := bites.Every("pgratelimit", s.opt.SweepEvery, func(ctx context.Context) error {
		n, err := s.Sweep(ctx)
		if err != nil {
			return fmt.Errorf("%w (table %s, name %q, %d rows deleted before it failed)", err, s.opt.Table, s.opt.Name, n)
		}
		if n > 0 {
			s.opt.Log.Info("pgratelimit: swept idle rate limit buckets", "table", s.opt.Table, "name", s.opt.Name, "deleted", n)
		}
		return nil
	})
	start, stop := svc.Start, svc.Stop
	svc.Start = func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cancel != nil {
			return errors.New("pgratelimit: the Service is already started: register one Service per Store")
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
