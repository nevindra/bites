package pgjobs_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites/modules/pgjobs"
	"github.com/nevindra/bites/modules/pgnotify"
)

// BenchmarkEnqueue is one Enqueue through the pool, one at a time, without and with the wake-up
// notification (a second statement).
func BenchmarkEnqueue(b *testing.B) {
	for _, notify := range []bool{false, true} {
		b.Run(fmt.Sprintf("notify=%v", notify), func(b *testing.B) {
			w := newWorld(b)
			o := pgjobs.Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			if notify {
				br := pgnotify.New(pgnotify.Options{Config: w.pool.Config().ConnConfig, Channel: "bench_" + w.table, Pool: w.pool, Log: o.Log})
				go br.Run(context.Background())
				b.Cleanup(func() { br.Close() })
				waitFor(b, "the broker to listen", br.Listening)
				o.Notify = br
			}
			c := w.client(o)
			k := pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
			b.ResetTimer()
			for b.Loop() {
				if _, err := k.Enqueue(ctx, w.pool, mail{To: "someone@example.com"}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "jobs/s")
		})
	}
}

// done counts attempts that have been recorded and closes ch at want.
type done struct {
	n    atomic.Int64
	want int64
	ch   chan struct{}
}

func (d *done) Start(ctx context.Context, _ pgjobs.Record) context.Context { return ctx }
func (d *done) End(context.Context, pgjobs.Record, pgjobs.Outcome) {
	if d.n.Add(1) == d.want {
		close(d.ch)
	}
}

// BenchmarkComplete is the throughput of the Service on jobs that are already stored, from the
// first claim to the last completion recorded, with one and with eight workers on one instance.
// The handler does nothing, so this is the queue's own cost: a claim of up to the free workers, an
// UPDATE that records each completion, and the heartbeat.
func BenchmarkComplete(b *testing.B) {
	for _, workers := range []int{1, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			w := newWorld(b)
			d := &done{want: int64(b.N), ch: make(chan struct{})}
			c := w.client(pgjobs.Options{Queues: map[string]int{"default": workers}, Observers: []pgjobs.Observer{d}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			k := pgjobs.Register(c, "noop", func(context.Context, pgjobs.Job[int]) error { return nil })
			for i := range b.N {
				enqueue(b, k, w.pool, i)
			}
			b.ResetTimer()
			serve(b, c)
			select {
			case <-d.ch:
			case <-time.After(5 * time.Minute):
				b.Fatalf("%d of %d jobs completed", d.n.Load(), b.N)
			}
			b.StopTimer()
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "jobs/s")
		})
	}
}

// TestTheClaimQueryIsAnIndexScanOnATableOfAMillionCompletedJobs fills a table with 1,000,000
// completed and 1,000 available jobs and prints the plans of the claim, the rescue and the prune,
// failing when the claim is not an index scan of the claim index. It takes a while, so it runs only
// with PGJOBS_EXPLAIN=1.
func TestTheClaimQueryIsAnIndexScanOnATableOfAMillionCompletedJobs(t *testing.T) {
	if os.Getenv("PGJOBS_EXPLAIN") == "" {
		t.Skip("PGJOBS_EXPLAIN is not set; this test fills a table of a million rows")
	}
	w := newWorld(t)
	c := w.client(pgjobs.Options{})
	pgjobs.Register(c, "send", func(context.Context, pgjobs.Job[mail]) error { return nil })
	began := time.Now()
	for _, q := range []string{
		`INSERT INTO "` + w.table + `" (id, kind, queue, state, args, priority, attempt, max_attempts, scheduled_at, run_at, created_at, attempted_at, finished_at)
		 SELECT gen_random_uuid(), 'send', 'default', 'completed', '{"to": "a@b"}', 0, 1, 20, now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', now() - interval '1 hour', now() - (g % 3600) * interval '1 second'
		 FROM generate_series(1, 1000000) g`,
		`INSERT INTO "` + w.table + `" (id, kind, queue, state, args, priority, attempt, max_attempts, scheduled_at, run_at, created_at)
		 SELECT gen_random_uuid(), 'send', 'default', 'available', '{"to": "a@b"}', 0, 0, 20, now() - g * interval '1 ms', now() - g * interval '1 ms', now()
		 FROM generate_series(1, 1000) g`,
		`ANALYZE "` + w.table + `"`,
	} {
		if _, err := w.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("filled in %v", time.Since(began))
	explain := func(name, q string, args ...any) string {
		tx, err := w.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			var l string
			rows.Scan(&l)
			lines = append(lines, l)
		}
		plan := strings.Join(lines, "\n")
		t.Logf("%s:\n%s", name, plan)
		return plan
	}
	now := time.Now()
	claim := `UPDATE "` + w.table + `" SET state = 'running', attempt = attempt + 1, attempted_at = $1, lease_until = $2, claim = $3
WHERE id IN (
	SELECT id FROM "` + w.table + `"
	WHERE state = 'available' AND queue = $4 AND run_at <= $1 AND kind = ANY($5) AND NOT (id = ANY($7))
	ORDER BY priority DESC, run_at, id
	LIMIT $6
	FOR UPDATE SKIP LOCKED)
RETURNING id`
	plan := explain("claim of 10", claim, now, now.Add(time.Minute), [16]byte{1}, "default", []string{"send"}, 10, [][16]byte{})
	if !strings.Contains(plan, `Index Scan using "`+w.table+`_claim_idx"`) && !strings.Contains(plan, `Index Scan using `+w.table+`_claim_idx`) {
		t.Errorf("the claim is not an index scan of the claim index")
	}
	if strings.Contains(plan, "Seq Scan") {
		t.Errorf("the claim scans the table")
	}
	explain("rescue", `SELECT id FROM "`+w.table+`" WHERE state = 'running' AND lease_until < $1 LIMIT 1000 FOR UPDATE SKIP LOCKED`, now)
	explain("prune batch", `SELECT id FROM "`+w.table+`" WHERE state = 'completed' AND finished_at IS NOT NULL AND finished_at < $1 LIMIT 1000 FOR UPDATE SKIP LOCKED`, now.Add(-30*time.Minute))
	for range 3 {
		began = time.Now()
		if _, err := c.Counts(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("Counts over 1,001,000 rows took %v", time.Since(began))
	}
	for range 3 {
		began = time.Now()
		n, err := c.Work(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("Work ran %d jobs in %v", n, time.Since(began))
	}
}
