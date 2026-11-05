package bites_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func TestLoggerLinesCarryTheRequestIDAndMethodOfTheAccessRecordAndTheRoute(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			bites.Logger(ctx).Info("looked up")
			return User{}, nil
		})
	})
	srv.Do(bitetestWith("/users/9", "X-Request-ID", "log-1"))
	access := waitRecords(t, srv, lb, 1)[0]
	var line map[string]any
	for _, m := range jsonLines(t, lb) {
		if m["msg"] == "looked up" {
			line = m
		}
	}
	if line == nil {
		t.Fatalf("no handler line in\n%s", lb)
	}
	for _, k := range []string{"request_id", "method"} {
		if line[k] == nil || line[k] != access[k] {
			t.Errorf("%s: handler line %v, access record %v", k, line[k], access[k])
		}
	}
	if line["route"] != "/users/{id}" || access["request_id"] != "log-1" {
		t.Errorf("handler line %v, access record %v", line, access)
	}
}

func TestLoggerLeavesOutTheRequestIDWhenThereIsNone(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			bites.Logger(ctx).Info("looked up")
			return User{}, nil
		})
	})
	srv.Do(req("GET", "/users/1"))
	waitRecords(t, srv, lb, 1)
	for _, m := range jsonLines(t, lb) {
		if m["msg"] == "looked up" && (m["request_id"] != nil || m["method"] != "GET" || m["route"] != "/users/{id}") {
			t.Errorf("%v", m)
		}
	}
}

func TestLoggerOnAContextThatIsNotARequestsIsTheDefaultLogger(t *testing.T) {
	if bites.Logger(context.Background()) != slog.Default() {
		t.Fatal("not slog.Default()")
	}
}

func TestLoggerOfAContextKeptPastTheRequestStillNamesItsOwnRequest(t *testing.T) {
	var mu sync.Mutex
	kept := map[string]context.Context{}
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			mu.Lock()
			kept[bites.RequestIDFrom(ctx)] = ctx
			mu.Unlock()
			return User{}, nil
		})
	})
	srv.Do(bitetestWith("/users/1", "X-Request-ID", "first"))
	srv.Do(bitetestWith("/users/2", "X-Request-ID", "second"))
	waitRecords(t, srv, lb, 2)
	mu.Lock()
	ctx := kept["first"]
	mu.Unlock()
	bites.Logger(ctx).Info("later")
	for _, m := range jsonLines(t, lb) {
		if m["msg"] == "later" && (m["request_id"] != "first" || m["route"] != "/users/{id}") {
			t.Errorf("%v", m)
		}
	}
}

// jsonLines are all the records a JSON handler wrote into lb.
func jsonLines(t *testing.T, lb *logBuf) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(lb.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not a JSON record: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func TestLoggerNamesANonStandardMethodOnARouteWithNoMiddleware(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Handle("PROPFIND", "/dav/{id}", func(ctx context.Context, in GetUser) (User, error) {
			bites.Logger(ctx).Info("looked up")
			return User{}, nil
		})
	}, bites.WithObserver())
	r := srv.Do(bitetest.Request{Method: "PROPFIND", Target: "/dav/1"})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	found := false
	for _, m := range jsonLines(t, lb) {
		if m["msg"] == "looked up" {
			found = true
			if m["method"] != "PROPFIND" || m["route"] != "/dav/{id}" {
				t.Errorf("%v", m)
			}
		}
	}
	if !found {
		t.Fatalf("no handler line in\n%s", lb)
	}
}
