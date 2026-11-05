package bites_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// accessRecords are the "request" records a JSON handler wrote into lb, in order.
func accessRecords(t *testing.T, lb *logBuf) []map[string]any {
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
		if m["msg"] == "request" {
			out = append(out, m)
		}
	}
	return out
}

// waitRecords waits until the Observers' End of every request srv has received has run, and
// returns the n "request" records lb then holds. The record is written at End, after the last
// byte, so a client can read its answer before the record exists (ADR 0051); WaitEnded waits for
// it without sleeping (ADR 0027).
func waitRecords(t *testing.T, srv *bitetest.Server, lb *logBuf, n int) []map[string]any {
	t.Helper()
	srv.WaitEnded()
	recs := accessRecords(t, lb)
	if len(recs) != n {
		t.Fatalf("%d records, want %d:\n%s", len(recs), n, lb)
	}
	return recs
}

// startJSON starts the app with a JSON logger into the returned buffer as the app's logger, and
// AccessLog() as its Observer.
func startJSON(t *testing.T, clk *bitetest.Clock, register func(*bites.App), opts ...bites.Option) (*bitetest.Server, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	if len(opts) == 0 {
		opts = []bites.Option{bites.WithObserver(bites.AccessLog())}
	}
	opts = append([]bites.Option{bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewJSONHandler(lb, nil)))}, opts...)
	app := bites.New(opts...)
	register(app)
	return bitetest.Start(t, app), lb
}

func TestTheAccessLogWritesOneRecordPerRequestWithMethodPathStatusSizeAndDuration(t *testing.T) {
	clk := bitetest.NewClock()
	srv, lb := startJSON(t, clk, func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
			clk.Advance(1500 * time.Microsecond)
			return getUser(ctx, in)
		})
	})
	r := srv.Do(bitetestWith("/users/7?expand=true", "X-Request-ID", "req-1"))
	m := waitRecords(t, srv, lb, 1)[0]
	want := map[string]any{"method": "GET", "path": "/users/7", "status": 200.0, "size": float64(len(r.Body)), "duration": float64(1500 * time.Microsecond), "request_id": "req-1"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	for _, k := range []string{"streaming", "panicked", "abandoned", "trace_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s on a plain answer: %v", k, m)
		}
	}
	if strings.Contains(lb.String(), "expand") {
		t.Errorf("the query reached the log: %s", lb)
	}
}

func TestTheAccessLogRecordIsTimedAtEndOnTheAppsClockAndHasNoSourceEvenWithAddSource(t *testing.T) {
	clk := bitetest.NewClock()
	lb := &logBuf{}
	lg := slog.New(slog.NewJSONHandler(lb, &slog.HandlerOptions{AddSource: true}))
	app := bites.New(bites.WithClock(clk), bites.WithObserver(bites.AccessLog(bites.AccessLogOptions{Logger: lg})))
	app.Get("/users/{id}", func(ctx context.Context, in GetUser) (User, error) {
		clk.Advance(2 * time.Second)
		return getUser(ctx, in)
	})
	srv := bitetest.Start(t, app)
	srv.Do(req("GET", "/users/1"))
	m := waitRecords(t, srv, lb, 1)[0]
	if want := "2026-01-01T00:00:02Z"; m["time"] != want {
		t.Errorf("time = %v, want %v, the End on the app's clock", m["time"], want)
	}
	if src, ok := m["source"]; ok {
		t.Errorf("source = %v, want none: it would name accesslog.go for every request", src)
	}
}

func TestTheAccessLogRecordsA404A405AndA400ByTheirStatus(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/users/{id}", getUser)
	})
	srv.Do(req("GET", "/nope"))
	waitRecords(t, srv, lb, 1)
	srv.Do(req("POST", "/users/1"))
	waitRecords(t, srv, lb, 2)
	srv.Do(req("GET", "/users/abc"))
	var got []string
	for _, m := range waitRecords(t, srv, lb, 3) {
		got = append(got, fmt.Sprint(m["method"], " ", m["path"], " ", m["status"]))
	}
	if want := "GET /nope 404, POST /users/1 405, GET /users/abc 400"; strings.Join(got, ", ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestTheAccessLogAddsTheAttributesAMiddlewareOrProviderAddedAfterItsOwn(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Provide(sessionFromHeader(nil))
		app.Provide(callerFromSession)
		app.Get("/me", me, bites.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			req.AddLogAttrs(slog.String("tenant", "t-9"))
			return next.Call(ctx), nil
		}))
	})
	srv.Do(withAuth("tok"))
	recs := waitRecords(t, srv, lb, 1)
	if recs[0]["caller"] != "user-tok" || recs[0]["tenant"] != "t-9" || recs[0]["status"] != 200.0 {
		t.Fatalf("%v", recs)
	}
}

func TestTheAccessLogOfAStreamIsWrittenWhenItEndsWithTheBytesItSent(t *testing.T) {
	clk := bitetest.NewClock()
	srv, lb := startJSON(t, clk, func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.SSE, error) {
			return bites.SSE{Func: func(_ context.Context, e bites.EventWriter) error {
				e.Comment("hi")
				clk.Advance(2 * time.Second) // the stream's life is in the duration, not only the time to produce it
				e.Comment("bye")
				return nil
			}}, nil
		})
	})
	r := srv.Do(req("GET", "/s"))
	recs := waitRecords(t, srv, lb, 1)
	m := recs[0]
	if m["status"] != 200.0 || m["size"] != float64(len(r.Body)) || m["streaming"] != true || m["duration"] != float64(2*time.Second) {
		t.Fatalf("%v (body %d bytes)", m, len(r.Body))
	}
}

func TestTheAccessLogOfAStreamThatFailsBeforeItsFirstByteSaysTheStatusSent(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/s", func(context.Context, Empty) (bites.Stream, error) {
			return bites.Stream{Func: func(context.Context, *bites.StreamWriter) error {
				return &bites.StatusError{Status: 409, Message: "taken"}
			}}, nil
		})
	})
	r := srv.Do(req("GET", "/s"))
	m := waitRecords(t, srv, lb, 1)[0]
	if r.Status != 409 || m["status"] != 409.0 || m["size"] != float64(len(r.Body)) || m["streaming"] != true {
		t.Fatalf("answer %d, record %v", r.Status, m)
	}
}

func TestTheAccessLogWritesARecordAfterAPanicWithPanickedAndThe500(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Use(bites.RequestID())
		app.Get("/boom", func(context.Context, Empty) (Empty, error) { panic("boom") })
	})
	r := srv.Do(bitetestWith("/boom", "X-Request-ID", "req-9"))
	m := waitRecords(t, srv, lb, 1)[0]
	if r.Status != 500 || m["status"] != 500.0 || m["panicked"] != true || m["request_id"] != "req-9" || m["size"] != float64(len(r.Body)) {
		t.Fatalf("answer %d, record %v", r.Status, m)
	}
}

func TestTheAccessLogSizeIsTheCompressedSizeWhenGzipCompressedTheAnswer(t *testing.T) {
	big := strings.Repeat("abcdefgh", 1024)
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/big", func(context.Context, Empty) (bites.Text, error) { return bites.Text{Body: big}, nil })
	}, bites.WithGzip(bites.Gzip{}), bites.WithObserver(bites.AccessLog()))
	r := srv.Do(bitetestWith("/big", "Accept-Encoding", "gzip"))
	if r.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not compressed: %v", r.Header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != big {
		t.Fatal("the compressed body is not the answer")
	}
	m := waitRecords(t, srv, lb, 1)[0]
	if m["size"] != float64(len(r.Body)) || len(r.Body) >= len(big) {
		t.Fatalf("size %v, compressed %d, plain %d", m["size"], len(r.Body), len(big))
	}
}

func TestTheAccessLogSizeOfAProblemAMiddlewareReturnedIsTheBodySent(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			return bites.Problem(401, "a token is required"), nil
		})
		app.Get("/users/{id}", getUser)
	})
	r := srv.Do(req("GET", "/users/1"))
	m := waitRecords(t, srv, lb, 1)[0]
	if r.Status != 401 || m["status"] != 401.0 || m["size"] != float64(len(r.Body)) || len(r.Body) == 0 {
		t.Fatalf("answer %d %q, record %v", r.Status, r.Body, m)
	}
}

func TestTheAccessLogMarksARequestWhoseClientLeftAsAbandoned(t *testing.T) {
	started := make(chan struct{})
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			close(started)
			<-ctx.Done()
			return Empty{}, ctx.Err()
		})
	})
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	<-started
	c.Close()
	recs := waitRecords(t, srv, lb, 1)
	if recs[0]["abandoned"] != true || recs[0]["size"] != 0.0 {
		t.Fatalf("%v\n%s", recs, lb)
	}
	if strings.Contains(lb.String(), `"level":"ERROR"`) {
		t.Fatalf("an abandoned request was logged as an error: %s", lb)
	}
}

func TestTheAccessLogUsesTheLoggerAndLevelItWasGivenInsteadOfTheApps(t *testing.T) {
	own := &logBuf{}
	srv, appLog := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/users/{id}", getUser)
	}, bites.WithObserver(bites.AccessLog(bites.AccessLogOptions{
		Logger: slog.New(slog.NewJSONHandler(own, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Level:  slog.LevelDebug,
	})))
	srv.Do(req("GET", "/users/1"))
	waitRecords(t, srv, own, 1)
	if !strings.Contains(own.String(), `"level":"DEBUG"`) {
		t.Fatalf("own logger: %s", own)
	}
	if n := len(accessRecords(t, appLog)); n != 0 {
		t.Fatalf("the app's logger got %d records", n)
	}
}

func TestTheAccessLogWritesNothingWhenItsLevelIsNotEnabled(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/users/{id}", getUser)
	}, bites.WithObserver(bites.AccessLog(bites.AccessLogOptions{Level: slog.LevelDebug}))) // the app's handler is at Info
	if r := srv.Do(req("GET", "/users/1")); r.Status != 200 {
		t.Fatal(r.Status)
	}
	srv.Do(req("GET", "/users/2")) // the first request's End has run once the second is answered on a fresh connection
	time.Sleep(10 * time.Millisecond)
	if n := len(accessRecords(t, lb)); n != 0 {
		t.Fatalf("%d records", n)
	}
}

func TestTheAccessLogWritesTheTraceIdAndSpanIdOfARequestThatHasATrace(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/users/{id}", getUser)
	}, bites.WithTraceContext(bites.TraceContext{}), bites.WithObserver(bites.AccessLog()))
	srv.Do(bitetestWith("/users/1", "traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))
	m := waitRecords(t, srv, lb, 1)[0]
	span, _ := m["span_id"].(string)
	if m["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" || len(span) != 16 || span == "00f067aa0ba902b7" {
		t.Fatalf("%v", m)
	}
}

func TestTheAccessLogOfAFailedRequestCarriesTheApplicationsErrorAndADecodingErrorDoesNot(t *testing.T) {
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/fail", func(context.Context, Empty) (Empty, error) { return Empty{}, errors.New("the database is down") })
		app.Get("/users/{id}", func(context.Context, struct{ Path struct{ ID int64 } }) (Empty, error) { return Empty{}, nil })
	})
	srv.Do(bitetestWith("/fail", "X-Any", "1"))
	srv.Do(bitetestWith("/users/x", "X-Any", "1"))
	recs := waitRecords(t, srv, lb, 2)
	if recs[0]["status"] != 500.0 || recs[0]["error"] != "the database is down" {
		t.Errorf("a handler's error: %v", recs[0])
	}
	if _, ok := recs[1]["error"]; recs[1]["status"] != 400.0 || ok {
		t.Errorf("a 400 the framework wrote has no error: %v", recs[1])
	}
}
