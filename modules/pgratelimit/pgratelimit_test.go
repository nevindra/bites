package pgratelimit_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgratelimit"
)

// pgURL is the Postgres the integration tests use. They are skipped without it, with a message
// naming the variable, so a run with no database says what it left out.
func pgURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("BITES_PG_URL")
	if u == "" {
		t.Skip("BITES_PG_URL names no Postgres, so this test that needs one is skipped; for example BITES_PG_URL=postgres://postgres:pg@127.0.0.1:55433/postgres")
	}
	return u
}

var tableSeq atomic.Int32

// newStore returns a Store on a table of its own to the test, created now and dropped at the end,
// so tests that share a database run in parallel without hearing each other.
func newStore(t *testing.T, o pgratelimit.Options) (*pgratelimit.Store, *pgxpool.Pool) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	o.Pool = pool
	o.Table = fmt.Sprintf("t_rl_%d_%d", os.Getpid(), tableSeq.Add(1))
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	st := pgratelimit.New(o)
	if err := st.CreateTable(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+o.Table+`"`) })
	return st, pool
}

// tableOf reads the quoted table name out of the Schema, so a test needs no second name for it.
func tableOf(st *pgratelimit.Store) string {
	s := st.Schema()
	s = s[strings.Index(s, `"`):]
	return s[:strings.Index(s[1:], `"`)+2]
}

func tableName(st *pgratelimit.Store) string { return strings.Trim(tableOf(st), `"`) }

func rows(t *testing.T, pool *pgxpool.Pool, st *pgratelimit.Store) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+tableOf(st)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var ctx = context.Background()

func TestTheStoreKeepsTheContractOfEveryRateLimitStore(t *testing.T) {
	bitetest.TestRateLimitStore(t, func(t *testing.T) bites.RateLimitStore {
		st, _ := newStore(t, pgratelimit.Options{})
		return st
	})
}

// roundTrips counts the queries a pool sends.
type roundTrips struct{ n atomic.Int32 }

func (r *roundTrips) TraceQueryStart(c context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	r.n.Add(1)
	return c
}
func (r *roundTrips) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestATakeIsOneRoundTripWhetherItIsAllowedOrRefused(t *testing.T) {
	t.Parallel()
	url := pgURL(t)
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	rt := &roundTrips{}
	cfg.ConnConfig.Tracer = rt
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	table := fmt.Sprintf("t_rl_rt_%d", os.Getpid())
	st := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: table})
	if err := st.CreateTable(ctx); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DROP TABLE IF EXISTS "`+table+`"`)
	now := time.Unix(1_700_000_000, 0)
	st.Take(ctx, "warm", now, 1, time.Hour) // the first use prepares the statement
	for i, want := range []bool{true, true, false, false} {
		before := rt.n.Load()
		d, err := st.Take(ctx, "k", now, 2, time.Hour)
		if err != nil || d.Allowed != want {
			t.Fatalf("take %d: %+v, %v", i, d, err)
		}
		if n := rt.n.Load() - before; n != 1 {
			t.Fatalf("take %d (allowed %v) sent %d queries, want 1", i, d.Allowed, n)
		}
	}
}

func TestTwoStoresOnOneTableShareTheirBucketsAndNeverBothGetTheLastRequest(t *testing.T) {
	t.Parallel()
	a, pool := newStore(t, pgratelimit.Options{Name: "login"})
	other, err := pgxpool.New(ctx, pgURL(t)) // a second instance has a pool of its own
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	b := pgratelimit.New(pgratelimit.Options{Pool: other, Table: tableName(a), Name: "login"})
	_ = pool
	now := time.Unix(1_700_000_000, 0)
	for round := range 40 {
		key := fmt.Sprint("shared-", round)
		var allowed atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 8 {
			st := []*pgratelimit.Store{a, b}[i%2]
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if d, err := st.Take(ctx, key, now, 3, time.Hour); err != nil {
					t.Error(err)
				} else if d.Allowed {
					allowed.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if allowed.Load() != 3 {
			t.Fatalf("round %d: two instances were allowed %d requests of a burst of 3", round, allowed.Load())
		}
	}
}

func TestARateLimiterOnTwoInstancesLimitsOnceNotTwice(t *testing.T) {
	t.Parallel()
	a, _ := newStore(t, pgratelimit.Options{Name: "login"})
	other, err := pgxpool.New(ctx, pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	b := pgratelimit.New(pgratelimit.Options{Pool: other, Table: tableName(a), Name: "login"})
	opts := func(s bites.RateLimitStore) bites.RateLimitOptions {
		return bites.RateLimitOptions{Limit: 3, Period: time.Minute, Store: s, Name: "login"}
	}
	one, two := bites.NewRateLimiter(opts(a)), bites.NewRateLimiter(opts(b))
	for i := range 3 {
		l := []*bites.RateLimiter{one, two}[i%2]
		if err := l.Allow(ctx, "ana@example.com"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	var se *bites.StatusError
	for _, l := range []*bites.RateLimiter{one, two} {
		if err := l.Allow(ctx, "ana@example.com"); !errors.As(err, &se) || se.Status != 429 {
			t.Fatalf("the fourth request, on either instance, was %v, want a 429", err)
		}
	}
	if err := two.Allow(ctx, "budi@example.com"); err != nil {
		t.Fatalf("another key was limited: %v", err)
	}
}

func TestLimitersWithDifferentNamesOnOneTableDoNotShareABucket(t *testing.T) {
	t.Parallel()
	login, pool := newStore(t, pgratelimit.Options{Name: "login"})
	invite := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: tableName(login), Name: "invite"})
	now := time.Unix(1_700_000_000, 0)
	if d, _ := login.Take(ctx, "ana@example.com", now, 1, time.Hour); !d.Allowed {
		t.Fatal("the first request was refused")
	}
	if d, _ := login.Take(ctx, "ana@example.com", now, 1, time.Hour); d.Allowed {
		t.Fatal("the second request of one name was allowed")
	}
	if d, err := invite.Take(ctx, "ana@example.com", now, 1, time.Hour); err != nil || !d.Allowed {
		t.Fatalf("the same key under another Name shared the bucket: %+v, %v", d, err)
	}
	// A Name that ends where a key begins is not the same bucket as the Name and key joined.
	ab := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: tableName(login), Name: "a"})
	ab2 := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: tableName(login), Name: "a\x00b"})
	ab.Take(ctx, "b\x00c", now, 1, time.Hour)
	if d, _ := ab2.Take(ctx, "c", now, 1, time.Hour); !d.Allowed {
		t.Fatal("a Name and a key were shifted into one another")
	}
}

func TestTheKeyIsNotStoredInTheClear(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{Name: "login"})
	st.Take(ctx, "ana@example.com", time.Now(), 3, time.Minute)
	var id []byte
	if err := pool.QueryRow(ctx, `SELECT key_hash FROM `+tableOf(st)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 || bytes.Contains(id, []byte("ana")) {
		t.Fatalf("the key is %x, not a SHA-256", id)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM `+tableOf(st)+` WHERE key_hash = $1`, []byte("ana@example.com")).Scan(&n)
	if n != 0 {
		t.Fatal("the raw key is a key in the table")
	}
}

func TestARefusedTakeWritesNothingToTheTable(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{})
	now := time.Unix(1_700_000_000, 0)
	st.Take(ctx, "k", now, 1, time.Hour)
	var before string
	pool.QueryRow(ctx, `SELECT ctid::text FROM `+tableOf(st)).Scan(&before)
	for range 20 {
		if d, _ := st.Take(ctx, "k", now, 1, time.Hour); d.Allowed {
			t.Fatal("a request over the burst was allowed")
		}
	}
	var after string
	pool.QueryRow(ctx, `SELECT ctid::text FROM `+tableOf(st)).Scan(&after)
	if before != after {
		t.Fatalf("refused requests wrote new versions of the row: ctid %s became %s", before, after)
	}
}

func TestATakeThatCannotWorkIsAnErrorSayingWhatToChange(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgratelimit.Options{})
	now := time.Now()
	for name, c := range map[string]struct {
		burst    int
		interval time.Duration
		want     string
	}{
		"no burst":     {0, time.Second, "burst is 0: a bucket holds at least one request"},
		"no interval":  {1, 0, "interval is 0s"},
		"a huge burst": {1 << 40, 24 * time.Hour, "does not fit in an int64 of nanoseconds: give a shorter interval or a smaller burst"},
	} {
		if _, err := st.Take(ctx, "k", now, c.burst, c.interval); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Take = %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestTheSweepDeletesBucketsThatHaveBeenFullForAWhileAndKeepsTheOthers(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{})
	now := time.Now()
	for i := range 2500 {
		st.Take(ctx, fmt.Sprint("idle", i), now.Add(-time.Hour), 1, time.Second) // full for an hour
	}
	st.Take(ctx, "recent", now.Add(-10*time.Second), 1, time.Second) // full 9 s ago: inside the grace
	st.Take(ctx, "limited", now, 3, time.Hour)                       // not full for 3 hours
	n, err := st.Sweep(ctx)
	if err != nil || n != 2500 {
		t.Fatalf("Sweep = %d, %v; want 2500 (more than one batch)", n, err)
	}
	if left := rows(t, pool, st); left != 2 {
		t.Fatalf("%d rows left; want the recent and the limited one", left)
	}
	if d, _ := st.Take(ctx, "limited", now, 3, time.Hour); d.Remaining != 1 {
		t.Fatalf("a limited key lost its spent requests to the sweep: %+v", d)
	}
}

func TestTheServiceSweepsOnItsInterval(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{SweepEvery: 20 * time.Millisecond})
	st.Take(ctx, "idle", time.Now().Add(-time.Hour), 1, time.Second)
	svc := st.Service()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx); err == nil || !strings.Contains(err.Error(), "one Service per Store") {
		t.Fatalf("a second Start = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rows(t, pool, st) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the service did not sweep the idle row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(ctx); err != nil {
		t.Fatalf("a second Stop: %v", err)
	}
}

func TestSweepsOnSeveralInstancesAtOnceDeleteEachRowOnce(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{})
	for i := range 3000 {
		st.Take(ctx, fmt.Sprint("idle", i), time.Now().Add(-time.Hour), 1, time.Second)
	}
	var wg sync.WaitGroup
	var total atomic.Int32
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: tableName(st)}).Sweep(ctx)
			if err != nil {
				t.Error(err)
			}
			total.Add(int32(n))
		}()
	}
	wg.Wait()
	if total.Load() != 3000 {
		t.Fatalf("the sweeps deleted %d rows between them; want 3000", total.Load())
	}
}

func TestCreateTableCanRunTwiceAndSchemaNamesTheTableAndItsIndex(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgratelimit.Options{})
	if err := st.CreateTable(ctx); err != nil {
		t.Fatalf("a second CreateTable: %v", err)
	}
	for _, want := range []string{"CREATE UNLOGGED TABLE IF NOT EXISTS", "key_hash bytea PRIMARY KEY", "tat      bigint NOT NULL", "_tat_idx"} {
		if !strings.Contains(st.Schema(), want) {
			t.Errorf("Schema lacks %q:\n%s", want, st.Schema())
		}
	}
}

func TestAStoreErrorIsWrappedWithTheOperation(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(ctx, pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: "t_rl_never_created"})
	if _, err := st.Take(ctx, "x", time.Now(), 1, time.Second); err == nil || !strings.HasPrefix(err.Error(), "pgratelimit: take: ") {
		t.Fatalf("Take on a missing table = %v", err)
	}
	if _, err := st.Sweep(ctx); err == nil || !strings.HasPrefix(err.Error(), "pgratelimit: sweep: ") {
		t.Fatalf("Sweep on a missing table = %v", err)
	}
}

func TestNewRefusesABadOptionAndSaysWhatToChange(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(ctx, "postgres://u@127.0.0.1:1/none") // lazy: nothing connects
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, c := range []struct {
		name string
		o    pgratelimit.Options
		want string
	}{
		{"a nil pool", pgratelimit.Options{}, "Pool is nil: give the application's *pgxpool.Pool"},
		{"a table with a dash", pgratelimit.Options{Pool: pool, Table: "my-limits"}, `Table "my-limits" is not a plain identifier`},
		{"a table with a quote", pgratelimit.Options{Pool: pool, Table: `a"; drop table x; --`}, "is not a plain identifier"},
		{"a table that starts with a digit", pgratelimit.Options{Pool: pool, Table: "1st"}, "is not a plain identifier"},
		{"a table over 50 bytes", pgratelimit.Options{Pool: pool, Table: strings.Repeat("a", 51)}, "at most 50 bytes"},
		{"a negative sweep", pgratelimit.Options{Pool: pool, SweepEvery: -time.Second}, "SweepEvery is -1s, which is not a time: give a positive duration, or zero for 10m0s"},
	} {
		func() {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, c.want) || !strings.Contains(msg, "registered at pgratelimit_test.go:") {
					t.Errorf("%s: panic = %q; want it to contain %q and the caller's line", c.name, msg, c.want)
				}
			}()
			pgratelimit.New(c.o)
			t.Errorf("%s: New did not panic", c.name)
		}()
	}
}

// BenchmarkTake is the cost of one Take against a Postgres on this machine, over distinct keys and
// over one key shared by every goroutine (the worst case for the row lock).
func BenchmarkTake(b *testing.B) {
	url := os.Getenv("BITES_PG_URL")
	if url == "" {
		b.Skip("BITES_PG_URL names no Postgres")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	table := fmt.Sprintf("t_rl_bench_%d", os.Getpid())
	st := pgratelimit.New(pgratelimit.Options{Pool: pool, Table: table})
	if err := st.CreateTable(ctx); err != nil {
		b.Fatal(err)
	}
	defer pool.Exec(ctx, `DROP TABLE IF EXISTS "`+table+`"`)
	now := time.Now()
	b.Run("distinct keys, one caller", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			if _, err := st.Take(ctx, fmt.Sprint("k", i), now, 5, time.Minute); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("one key refused, one caller", func(b *testing.B) {
		st.Take(ctx, "hot", now, 1, time.Hour)
		for b.Loop() {
			if _, err := st.Take(ctx, "hot", now, 1, time.Hour); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("one key, parallel callers", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := st.Take(ctx, "shared", now, 1000, time.Millisecond); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}

func TestTheSweepAndTheServiceJudgeIdleOnTheAppClock(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgratelimit.Options{SweepEvery: time.Minute})
	clk := bitetest.NewClock() // 2026-01-01: far from the real now
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Service(st.Service())
	bitetest.Start(t, app)
	// A bucket that is full from 2026-01-01 00:00:10 on the app's clock.
	st.Take(ctx, "k", clk.Now().Add(-time.Hour+10*time.Second), 1, time.Second)
	st.Take(ctx, "k2", clk.Now(), 1, time.Hour) // full at 01:00:00
	for range 100 {
		clk.Advance(time.Minute)
		time.Sleep(20 * time.Millisecond)
		if rows(t, pool, st) == 1 {
			break
		}
	}
	if left := rows(t, pool, st); left != 1 {
		t.Fatalf("%d rows left on the app's clock after the sweeps; want only the bucket that is not yet full", left)
	}
}
