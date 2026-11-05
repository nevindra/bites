package bitetest

import (
	"context"
	"fmt"
	"github.com/nevindra/bites"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// recorder is a testing.TB that keeps what Log is given and runs its cleanups on demand.
type recorder struct {
	testing.TB
	mu       sync.Mutex
	lines    []string
	cleanups []func()
}

func (r *recorder) Helper() {}
func (r *recorder) Log(args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, strings.TrimSpace(sprint(args)))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *recorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func sprint(args []any) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(a.(string))
	}
	return b.String()
}

func TestALineLoggedAfterTheServerIsClosedIsDroppedAndNeverReachesTheTest(t *testing.T) {
	rec := &recorder{TB: t}
	l := newTestLog(rec)
	lg := slog.New(l).With("conn", 7).WithGroup("req")
	lg.Info("before", "path", "/x")
	l.stop()
	lg.Info("after", "path", "/y")
	if len(rec.lines) != 1 || !strings.Contains(rec.lines[0], "msg=before") || !strings.Contains(rec.lines[0], "conn=7") || !strings.Contains(rec.lines[0], "req.path=/x") {
		t.Fatalf("lines: %q", rec.lines)
	}
	if strings.Contains(rec.lines[0], "time=") {
		t.Fatalf("a time stamp in a test log line: %q", rec.lines[0])
	}
}

// captureDefault makes the process's default logger write into the returned builder.
func captureDefault(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &b
}

func TestStartLogsNothingToTheDefaultLoggerOfAnAppThatWasGivenNoLogger(t *testing.T) {
	out := captureDefault(t)
	app := bites.New(bites.WithClock(NewClock()))
	app.Get("/boom", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, fmt.Errorf("db down") })
	srv := Start(t, app)
	if r := srv.Do(Request{Method: "GET", Target: "/boom"}); r.Status != 500 {
		t.Fatalf("%d", r.Status)
	}
	srv.Close()
	if out.Len() != 0 {
		t.Fatalf("the default logger was written to:\n%s", out.String())
	}
}

func TestStartRoutesTheLogOfAnAppWithoutALoggerIntoTheTestsOwnLog(t *testing.T) {
	rec := &recorder{TB: t}
	app := bites.New(bites.WithClock(NewClock()))
	app.Get("/boom", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, fmt.Errorf("db down") })
	srv := Start(rec, app)
	srv.Do(Request{Method: "GET", Target: "/boom"})
	rec.finish()
	all := strings.Join(rec.lines, "\n")
	for _, w := range []string{"bites: settings", "bites: handler error", "db down"} {
		if !strings.Contains(all, w) {
			t.Errorf("the test's log lacks %q:\n%s", w, all)
		}
	}
}

func TestStartKeepsTheLoggerAnAppWasGivenWithWithLogger(t *testing.T) {
	out := captureDefault(t)
	var own strings.Builder
	rec := &recorder{TB: t}
	app := bites.New(bites.WithClock(NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(&own, nil))))
	app.Get("/boom", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, fmt.Errorf("db down") })
	srv := Start(rec, app)
	srv.Do(Request{Method: "GET", Target: "/boom"})
	rec.finish()
	if !strings.Contains(own.String(), "bites: handler error") || len(rec.lines) != 0 || out.Len() != 0 {
		t.Fatalf("own:\n%s\ntest log: %q\ndefault: %q", own.String(), rec.lines, out.String())
	}
}
