package pgjobs_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgjobs"
)

// pgURL is the Postgres the integration tests use. They are skipped without it, with a message
// naming the variable, so a run with no database says what it left out.
func pgURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("BITES_PG_URL")
	if u == "" {
		t.Skip("BITES_PG_URL names no Postgres, so this test that needs one is skipped; for example BITES_PG_URL=postgres://postgres:pg@127.0.0.1:55433/postgres")
	}
	return u
}

var tableSeq atomic.Int32

// logBuf is a log destination safe for the goroutines of the tests.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// world is one test's database: a pool and a table of its own, dropped at the end, so tests that
// share a database run in parallel without seeing each other's jobs.
type world struct {
	t     testing.TB
	pool  *pgxpool.Pool
	table string
	logs  *logBuf
}

func newWorld(t testing.TB) *world {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, pool: pool, table: fmt.Sprintf("t_jobs_%d_%d", os.Getpid(), tableSeq.Add(1)), logs: &logBuf{}}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+w.table+`", "`+w.table+`_periodic"`)
		pool.Close()
	})
	return w
}

// client is a Client on the world's table, which it creates.
func (w *world) client(o pgjobs.Options) *pgjobs.Client {
	w.t.Helper()
	o.Pool, o.Table = w.pool, w.table
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	c := pgjobs.New(o)
	if err := c.CreateTable(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	return c
}

// serve starts an app whose only Service is c's, and returns it; the test's end stops it.
func serve(t testing.TB, c *pgjobs.Client, opts ...bites.Option) *bites.App {
	t.Helper()
	app := bites.New(opts...)
	app.Service(c.Service())
	bitetest.Start(t, app)
	return app
}

func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func get(t testing.TB, c *pgjobs.Client, id pgjobs.ID) pgjobs.Record {
	t.Helper()
	r, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func work(t testing.TB, c *pgjobs.Client) int {
	t.Helper()
	n, err := c.Work(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func enqueue[T any](t testing.TB, k *pgjobs.Kind[T], q pgjobs.Querier, args T, o ...pgjobs.EnqueueOptions) pgjobs.Enqueued {
	t.Helper()
	e, err := k.Enqueue(context.Background(), q, args, o...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type mail struct {
	To string `json:"to"`
}

var ctx = context.Background()

func mustPanic(t *testing.T, name, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r, _ := recover().(string)
		if !strings.Contains(r, want) || !strings.Contains(r, "_test.go:") || !strings.Contains(r, "\n\tregistered at ") {
			t.Errorf("%s: panic %q, want %q and the line", name, r, want)
		}
	}()
	f()
}

// lazyPool is a pool that connects nothing: enough for what is refused before the database.
func lazyPool(t *testing.T) *pgxpool.Pool {
	p, err := pgxpool.New(ctx, "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestNewRefusesAnOptionThatCannotWorkNamingTheLine(t *testing.T) {
	pool := lazyPool(t)
	for name, tc := range map[string]struct {
		o    pgjobs.Options
		want string
	}{
		"no pool":      {pgjobs.Options{}, "pgjobs.New: Pool is nil"},
		"table":        {pgjobs.Options{Pool: pool, Table: "jobs; drop"}, `Table "jobs; drop" is not a plain identifier`},
		"long table":   {pgjobs.Options{Pool: pool, Table: strings.Repeat("t", 41)}, "at most 40 bytes"},
		"queue name":   {pgjobs.Options{Pool: pool, Queues: map[string]int{"a b": 1}}, `queue "a b" is not a plain name`},
		"no workers":   {pgjobs.Options{Pool: pool, Queues: map[string]int{"mail": 0}}, `queue "mail" runs 0 jobs at once: give at least 1`},
		"negative":     {pgjobs.Options{Pool: pool, KeepFailed: -time.Second}, "KeepFailed is -1s, which is not a time"},
		"poll":         {pgjobs.Options{Pool: pool, PollEvery: time.Millisecond}, "PollEvery is 1ms: give at least 10ms"},
		"lease":        {pgjobs.Options{Pool: pool, Lease: 500 * time.Millisecond}, "Lease is 500ms: give at least one second"},
		"nil observer": {pgjobs.Options{Pool: pool, Observers: []pgjobs.Observer{nil}}, "Observers[0] is nil"},
	} {
		mustPanic(t, name, tc.want, func() { pgjobs.New(tc.o) })
	}
}

func TestRegisterRefusesAKindThatCannotWorkNamingTheLine(t *testing.T) {
	c := pgjobs.New(pgjobs.Options{Pool: lazyPool(t), Queues: map[string]int{"mail": 2}})
	ok := func(context.Context, pgjobs.Job[mail]) error { return nil }
	pgjobs.Register(c, "send", ok, pgjobs.KindOptions{Queue: "mail"})
	mustPanic(t, "name", `pgjobs.Register("send mail"): the name is not a plain name`, func() { pgjobs.Register(c, "send mail", ok) })
	mustPanic(t, "nil", `pgjobs.Register("x"): handle is nil`, func() { pgjobs.Register[mail](c, "x", nil) })
	mustPanic(t, "dup", `pgjobs.Register("send"): a kind of this name is registered`, func() { pgjobs.Register(c, "send", ok, pgjobs.KindOptions{Queue: "mail"}) })
	mustPanic(t, "queue", `queue "default" is not one of Options.Queues (mail)`, func() { pgjobs.Register(c, "y", ok) })
	mustPanic(t, "negative", "MaxAttempts -1 or Timeout 0s is negative", func() { pgjobs.Register(c, "z", ok, pgjobs.KindOptions{Queue: "mail", MaxAttempts: -1}) })
	mustPanic(t, "two", "2 options values were given; give one KindOptions", func() { pgjobs.Register(c, "w", ok, pgjobs.KindOptions{}, pgjobs.KindOptions{}) })
	mustPanic(t, "json", "the arguments type chan int cannot be encoded as JSON", func() {
		pgjobs.Register(c, "v", func(context.Context, pgjobs.Job[chan int]) error { return nil }, pgjobs.KindOptions{Queue: "mail"})
	})
}

func TestAScheduleThatCannotWorkIsRefusedNamingTheLine(t *testing.T) {
	c := pgjobs.New(pgjobs.Options{Pool: lazyPool(t), PollEvery: time.Second})
	k := pgjobs.Register(c, "report", func(context.Context, pgjobs.Job[mail]) error { return nil })
	k.Every("hourly", time.Hour, mail{})
	mustPanic(t, "short", `Kind("report").Every("fast"): the interval 500ms is under PollEvery (1s)`, func() { k.Every("fast", 500*time.Millisecond, mail{}) })
	mustPanic(t, "dup", `Kind("report").Every("hourly"): a schedule of this name exists`, func() { k.Every("hourly", time.Hour, mail{}) })
	mustPanic(t, "name", `the name is not a plain name`, func() { k.Every("a b", time.Hour, mail{}) })
	mustPanic(t, "spec", `Kind("report").Cron("bad"): bites.Cron("pgjobs-bad"): spec "0 25 * * *" is not a schedule: hour "25"`, func() { k.Cron("bad", "0 25 * * *", time.UTC, mail{}) })
	mustPanic(t, "zone", `the time zone is nil`, func() { k.Cron("nozone", "0 2 * * *", nil, mail{}) })
}

func TestEnqueueRefusesWhatCannotWorkAndStoresNothing(t *testing.T) {
	c := pgjobs.New(pgjobs.Options{Pool: lazyPool(t)})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	pool := lazyPool(t)
	for name, tc := range map[string]struct {
		q    pgjobs.Querier
		o    []pgjobs.EnqueueOptions
		want string
	}{
		"nil q":      {nil, nil, "q is nil"},
		"both times": {pool, []pgjobs.EnqueueOptions{{RunAt: time.Now(), Delay: time.Second}}, "both RunAt and Delay are given"},
		"delay":      {pool, []pgjobs.EnqueueOptions{{Delay: -time.Second}}, "Delay is -1s"},
		"priority":   {pool, []pgjobs.EnqueueOptions{{Priority: 101}}, "Priority is 101: give a priority between -100 and 100"},
		"attempts":   {pool, []pgjobs.EnqueueOptions{{MaxAttempts: -2}}, "MaxAttempts is -2"},
		"queue":      {pool, []pgjobs.EnqueueOptions{{Queue: "nope"}}, `queue "nope" is not one of Options.Queues (default)`},
		"key":        {pool, []pgjobs.EnqueueOptions{{UniqueKey: strings.Repeat("k", 513)}}, "the unique key is 513 bytes, over MaxUniqueKey (512)"},
		"key nul":    {pool, []pgjobs.EnqueueOptions{{UniqueKey: "a\x00b"}}, "not UTF-8 text with no NUL"},
		"two":        {pool, []pgjobs.EnqueueOptions{{}, {}}, "2 options values were given"},
	} {
		_, err := k.Enqueue(ctx, tc.q, mail{To: "a@b"}, tc.o...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "pgjobs: enqueue send: ") {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	big := pgjobs.Register(c, "big", func(context.Context, pgjobs.Job[string]) error { return nil })
	if _, err := big.Enqueue(ctx, pool, strings.Repeat("x", pgjobs.MaxArgs)); err == nil || !strings.Contains(err.Error(), "over MaxArgs") {
		t.Errorf("arguments over MaxArgs: %v", err)
	}
}

func TestAnIDIsAUUIDv7InItsCanonicalTextAndReadsBack(t *testing.T) {
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	a, b := enqueue(t, k, w.pool, mail{}), enqueue(t, k, w.pool, mail{})
	s := a.ID.String()
	if len(s) != 36 || s[14] != '7' || !strings.ContainsRune("89ab", rune(s[19])) {
		t.Fatalf("%s is not a UUIDv7 in canonical form", s)
	}
	back, err := pgjobs.ParseID(strings.ToUpper(s))
	if err != nil || back != a.ID {
		t.Fatalf("ParseID(%q) = %v, %v", s, back, err)
	}
	var txt pgjobs.ID
	if err := txt.UnmarshalText([]byte(s)); err != nil || txt != a.ID {
		t.Fatalf("UnmarshalText = %v, %v", txt, err)
	}
	if string(must(a.ID.MarshalText())) != s {
		t.Fatal("MarshalText differs from String")
	}
	if b.ID.String() < s[:13] {
		t.Fatalf("ids do not sort by time: %s then %s", s, b.ID)
	}
	var probe string
	if err := w.pool.QueryRow(ctx, `SELECT id::text FROM "`+w.table+`" WHERE id = $1::uuid`, s).Scan(&probe); err != nil || probe != s {
		t.Fatalf("Postgres reads the id as %q, %v", probe, err)
	}
	for _, bad := range []string{"", "nope", strings.Repeat("g", 36), s[:35] + "-"} {
		if _, err := pgjobs.ParseID(bad); err == nil || !strings.Contains(err.Error(), "is not a job id") {
			t.Errorf("ParseID(%q) = %v", bad, err)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestCreateTableRunsTheSchemaAndCanRunAgain(t *testing.T) {
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	if err := c.CreateTable(ctx); err != nil {
		t.Fatalf("a second CreateTable: %v", err)
	}
	for _, want := range []string{`CREATE TABLE IF NOT EXISTS "` + w.table + `"`, `"` + w.table + `_claim_idx"`, `WHERE state = 'available'`, `CREATE UNIQUE INDEX IF NOT EXISTS "` + w.table + `_unique_idx"`, `"` + w.table + `_periodic"`} {
		if !strings.Contains(c.Schema(), want) {
			t.Errorf("Schema lacks %q", want)
		}
	}
	var n int
	w.pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE tablename = $1`, w.table).Scan(&n)
	if n != 5 {
		t.Fatalf("the table has %d indexes, want the key and four", n)
	}
}

func TestAModuleImportsOnlyTheExportedCore(t *testing.T) {
	for _, f := range []string{"pgjobs.go", "kind.go", "service.go", "admin.go", "periodic.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "bites/internal") {
			t.Errorf("%s imports the core's internal packages (ADR 0070)", f)
		}
	}
}

// ---- against a real Postgres

func TestAJobEnqueuedInATransactionRunsOnlyIfTheTransactionCommits(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	var got []string
	k := pgjobs.Register(c, "send", func(_ context.Context, job pgjobs.Job[mail]) error {
		got = append(got, job.Args.To)
		return nil
	})
	rolled, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, k, rolled, mail{To: "rolled-back@example.com"})
	rolled.Rollback(ctx)
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := enqueue(t, k, tx, mail{To: "committed@example.com"})
	if n := work(t, c); n != 0 {
		t.Fatalf("Work ran %d jobs before the transaction committed", n)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := work(t, c); n != 1 || len(got) != 1 || got[0] != "committed@example.com" {
		t.Fatalf("Work ran %d jobs, the handler saw %v; want only the committed one", n, got)
	}
	r := get(t, c, e.ID)
	if r.State != pgjobs.StateCompleted || r.Attempt != 1 || r.FinishedAt.IsZero() || string(r.Args) != `{"to": "committed@example.com"}` {
		t.Fatalf("the job is %+v", r)
	}
	if counts := must(c.Counts(ctx)); len(counts) != 1 || counts[0] != (pgjobs.Count{Queue: "default", State: pgjobs.StateCompleted, Jobs: 1}) {
		t.Fatalf("Counts = %+v: the rolled-back job was stored", counts)
	}
}

func TestTwoWorkersRacingForOneJobRunItOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var runs atomic.Int32
	h := func(context.Context, pgjobs.Job[mail]) error {
		runs.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	a, b := w.client(pgjobs.Options{}), w.client(pgjobs.Options{})
	ka := pgjobs.Register(a, "send", h)
	pgjobs.Register(b, "send", h)
	for round := range 30 {
		enqueue(t, ka, w.pool, mail{})
		var wg sync.WaitGroup
		var ran atomic.Int32
		for _, c := range []*pgjobs.Client{a, b} {
			wg.Go(func() { ran.Add(int32(work(t, c))) })
		}
		wg.Wait()
		if ran.Load() != 1 || runs.Load() != int32(round+1) {
			t.Fatalf("round %d: the two ran %d attempts, the handler %d times in all", round, ran.Load(), runs.Load())
		}
	}
}

func TestTwoInstancesRunningManyJobsRunEachOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var mu sync.Mutex
	seen := map[pgjobs.ID]int{}
	h := func(_ context.Context, job pgjobs.Job[int]) error {
		mu.Lock()
		seen[job.ID]++
		mu.Unlock()
		return nil
	}
	o := pgjobs.Options{PollEvery: 20 * time.Millisecond, Queues: map[string]int{"default": 4}}
	a, b := w.client(o), w.client(o)
	ka := pgjobs.Register(a, "count", h)
	pgjobs.Register(b, "count", h)
	const n = 400
	for i := range n {
		enqueue(t, ka, w.pool, i)
	}
	serve(t, a)
	serve(t, b)
	waitFor(t, "every job to complete", func() bool {
		cs := must(a.Counts(ctx))
		return len(cs) == 1 && cs[0].State == pgjobs.StateCompleted && cs[0].Jobs == n
	})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("%d jobs ran, want %d", len(seen), n)
	}
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("job %s ran %d times", id, k)
		}
	}
}
