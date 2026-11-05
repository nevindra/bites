package otel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan  = "00f067aa0ba902b7"
	traceparent = "00-" + callerTrace + "-" + callerSpan + "-01"
)

type seen struct {
	mu    sync.Mutex
	trace bites.Trace
	ok    bool
	span  trace.SpanContext
}

type userIn struct{ Path struct{ ID int64 } }
type user struct{ ID int64 }

// serve starts an app with the Tracing Observer and a recorder, and routes that note what the
// handler sees and start a child span as a database call would.
func serve(t *testing.T, tp trace.TracerProvider, opts ...bites.Option) (*bitetest.Server, *seen, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(lockedWriter{&mu, &log}, nil))
	all := append([]bites.Option{
		bites.WithClock(bitetest.NewClock()),
		bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		bites.WithObserver(otel.Tracing(otel.Config{TracerProvider: tp}), bites.AccessLog(bites.AccessLogOptions{Logger: logger})),
	}, opts...)
	app := bites.New(all...)
	s := &seen{}
	app.Get("/users/{id}", func(ctx context.Context, in userIn) (user, error) {
		s.mu.Lock()
		s.trace, s.ok = bites.TraceFrom(ctx)
		s.mu.Unlock()
		_, db := tp.Tracer("db").Start(ctx, "SELECT users")
		s.mu.Lock()
		s.span = db.SpanContext()
		s.mu.Unlock()
		db.End()
		return user{ID: in.Path.ID}, nil
	})
	app.Get("/fail", func(context.Context, struct{}) (user, error) { return user{}, errors.New("the database is down") })
	app.Get("/panic", func(context.Context, struct{}) (user, error) { panic("boom") })
	app.Get("/slow", func(ctx context.Context, _ struct{}) (user, error) {
		s.mu.Lock()
		s.ok = true
		s.mu.Unlock()
		<-ctx.Done()
		return user{}, ctx.Err()
	})
	app.Post("/echo", func(_ context.Context, in struct{ Body user }) (user, error) { return in.Body, nil })
	return bitetest.Start(t, app), s, &log
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func recorder() (*tracetest.SpanRecorder, *sdktrace.TracerProvider) {
	rec := tracetest.NewSpanRecorder()
	return rec, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
}

// serverSpan waits for the server span to end: End runs after the client has its answer.
func serverSpan(t *testing.T, rec *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range rec.Ended() {
			if s.SpanKind() == trace.SpanKindServer {
				return s
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no server span ended")
	return nil
}

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func TestARequestWithATraceparentIsAServerSpanChildOfTheCallersSpanWithTheHTTPAttributes(t *testing.T) {
	rec, tp := recorder()
	srv, _, _ := serve(t, tp)
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}, "tracestate": {"congo=t61rcWkgMzE"}, "User-Agent": {"probe/1"}}})
	if r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	s := serverSpan(t, rec)
	if s.SpanContext().TraceID().String() != callerTrace || s.Parent().SpanID().String() != callerSpan || !s.Parent().IsRemote() {
		t.Errorf("trace %s parent %s", s.SpanContext().TraceID(), s.Parent().SpanID())
	}
	if s.Name() != "GET /users/{id}" || s.SpanKind() != trace.SpanKindServer {
		t.Errorf("name %q kind %v", s.Name(), s.SpanKind())
	}
	if s.SpanContext().TraceState().String() != "congo=t61rcWkgMzE" {
		t.Errorf("tracestate %q", s.SpanContext().TraceState())
	}
	a := attrs(s)
	for k, want := range map[string]string{"http.request.method": "GET", "http.route": "/users/{id}", "url.path": "/users/7", "url.scheme": "http", "user_agent.original": "probe/1"} {
		if a[k].AsString() != want {
			t.Errorf("%s = %q, want %q", k, a[k].AsString(), want)
		}
	}
	if a["http.response.status_code"].AsInt64() != 200 || a["http.response.body.size"].AsInt64() != int64(len(r.Body)) {
		t.Errorf("status %v size %v (body %d)", a["http.response.status_code"], a["http.response.body.size"], len(r.Body))
	}
	if s.Status().Code != codes.Unset {
		t.Errorf("status %v", s.Status())
	}
}

func TestTheHandlersSpansAreChildrenOfTheServerSpanAndTraceFromNamesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []bites.Option
	}{
		{"without WithTraceContext", nil},
		{"with WithTraceContext", []bites.Option{bites.WithTraceContext(bites.TraceContext{})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, tp := recorder()
			srv, seen, _ := serve(t, tp, tc.opts...)
			srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}}})
			s := serverSpan(t, rec)
			seen.mu.Lock()
			defer seen.mu.Unlock()
			if !seen.ok || seen.trace.ID != bites.TraceID(s.SpanContext().TraceID()) || seen.trace.Span != bites.SpanID(s.SpanContext().SpanID()) || seen.trace.Parent.String() != callerSpan || !seen.trace.Sampled() {
				t.Errorf("TraceFrom %+v, server span %s/%s", seen.trace, s.SpanContext().TraceID(), s.SpanContext().SpanID())
			}
			for _, e := range rec.Ended() {
				if e.Name() == "SELECT users" && e.Parent().SpanID() != s.SpanContext().SpanID() {
					t.Errorf("the database span's parent is %s, not the server span %s", e.Parent().SpanID(), s.SpanContext().SpanID())
				}
			}
			if seen.span.TraceID() != s.SpanContext().TraceID() {
				t.Errorf("the database span is in trace %s", seen.span.TraceID())
			}
		})
	}
}

func TestARequestWithNoTraceparentBeginsATraceOfItsOwn(t *testing.T) {
	for _, opts := range [][]bites.Option{nil, {bites.WithTraceContext(bites.TraceContext{Begin: true})}} {
		rec, tp := recorder()
		srv, seen, _ := serve(t, tp, opts...)
		srv.Do(bitetest.Request{Method: "GET", Target: "/users/7"})
		s := serverSpan(t, rec)
		seen.mu.Lock()
		if s.Parent().IsValid() || !seen.ok || seen.trace.ID != bites.TraceID(s.SpanContext().TraceID()) || seen.trace.Parent != (bites.SpanID{}) || !seen.trace.Sampled() {
			t.Errorf("parent %v, TraceFrom %+v", s.Parent(), seen.trace)
		}
		seen.mu.Unlock()
	}
}

func TestTheAccessLogNamesTheSpanTheModuleRecorded(t *testing.T) {
	rec, tp := recorder()
	srv, _, log := serve(t, tp)
	srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}}})
	s := serverSpan(t, rec)
	deadline := time.Now().Add(5 * time.Second)
	var line map[string]any
	for time.Now().Before(deadline) {
		if l, _, _ := strings.Cut(log.String(), "\n"); l != "" {
			json.Unmarshal([]byte(l), &line)
			break
		}
		time.Sleep(time.Millisecond)
	}
	if line["trace_id"] != s.SpanContext().TraceID().String() || line["span_id"] != s.SpanContext().SpanID().String() {
		t.Errorf("the record %v, the span %s/%s", line, s.SpanContext().TraceID(), s.SpanContext().SpanID())
	}
}

func TestA5xxAndAPanicAreRecordedAsErrorsAndA404HasNoRoute(t *testing.T) {
	for _, tc := range []struct {
		target, name, errType string
		status                int64
	}{
		{"/fail", "GET /fail", "500", 500},
		{"/panic", "GET /panic", "panic", 500},
		{"/nowhere", "GET", "", 404},
	} {
		rec, tp := recorder()
		srv, _, _ := serve(t, tp)
		srv.Do(bitetest.Request{Method: "GET", Target: tc.target})
		s := serverSpan(t, rec)
		a := attrs(s)
		if s.Name() != tc.name || a["http.response.status_code"].AsInt64() != tc.status || a["error.type"].AsString() != tc.errType {
			t.Errorf("%s: name %q attrs %v", tc.target, s.Name(), a)
		}
		if wantErr := tc.errType != ""; wantErr != (s.Status().Code == codes.Error) {
			t.Errorf("%s: status %v", tc.target, s.Status())
		}
		if _, ok := a["http.route"]; ok == (tc.status == 404) {
			t.Errorf("%s: route %v", tc.target, a["http.route"])
		}
	}
}

func TestTheBytesReadAndAnUnknownMethodAreRecordedWithoutGrowingTheMethodSet(t *testing.T) {
	rec, tp := recorder()
	srv, _, _ := serve(t, tp)
	srv.Do(bitetest.Request{Method: "POST", Target: "/echo", Body: []byte(`{"id":3}`), Header: map[string][]string{"Content-Type": {"application/json"}}})
	if a := attrs(serverSpan(t, rec)); a["http.request.body.size"].AsInt64() != 8 {
		t.Errorf("%v", a)
	}
	rec2, tp2 := recorder()
	srv2, _, _ := serve(t, tp2)
	srv2.Do(bitetest.Request{Method: "PURGE", Target: "/users/7"})
	s := serverSpan(t, rec2)
	if a := attrs(s); a["http.request.method"].AsString() != "_OTHER" || a["http.request.method_original"].AsString() != "PURGE" || s.Name() != "HTTP" {
		t.Errorf("%q %v", s.Name(), a)
	}
}

func TestANoOpProviderRecordsNothingAndLeavesTheCoresTrace(t *testing.T) {
	srv, seen, _ := serve(t, noop.NewTracerProvider(), bites.WithTraceContext(bites.TraceContext{}))
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}}}); r.Status != 200 {
		t.Fatal(r.Status)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if !seen.ok || seen.trace.ID.String() != callerTrace || seen.trace.Parent.String() != callerSpan || seen.trace.Span.String() == callerSpan {
		t.Errorf("%+v", seen.trace)
	}
}

func TestASamplerThatDropsTheSpanStillGivesTheRequestASpanIDThatIsNotSampled(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.NeverSample()))
	srv, seen, _ := serve(t, tp)
	srv.Do(bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}}})
	time.Sleep(10 * time.Millisecond)
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if !seen.ok || seen.trace.Sampled() || seen.trace.ID.String() != callerTrace || seen.trace.Span.String() == callerSpan {
		t.Errorf("%+v", seen.trace)
	}
	if n := len(rec.Ended()); n != 0 {
		t.Errorf("%d spans recorded", n)
	}
}

func TestTheModuleImportsOnlyTheCoresExportedAPI(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.Contains(p, "bites/internal") {
			t.Errorf("the module imports %s", p)
		}
	}
}

func TestAClientThatLeavesMidHandlerIsRecordedAsAbandonedAndNotAsAServerError(t *testing.T) {
	rec, tp := recorder()
	srv, seen, _ := serve(t, tp)
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	for {
		seen.mu.Lock()
		ok := seen.ok
		seen.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.Close()
	s := serverSpan(t, rec)
	a := attrs(s)
	if !a["bites.abandoned"].AsBool() || s.Status().Code == codes.Error || len(s.Events()) != 1 || s.Events()[0].Name != "abandoned" {
		t.Errorf("attrs %v status %v events %v", a, s.Status(), s.Events())
	}
	if _, ok := a["http.response.body.size"]; ok {
		t.Errorf("an abandoned answer wrote nothing: %v", a)
	}
}

// allocsPerRequest is the allocations of one request through the in-memory server, client side
// included; the difference between two apps is what the Observer adds.
func allocsPerRequest(t *testing.T, opts ...bites.Option) float64 {
	t.Helper()
	app := bites.New(append([]bites.Option{bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, opts...)...)
	app.Get("/users/{id}", func(_ context.Context, in userIn) (user, error) { return user{ID: in.Path.ID}, nil })
	srv := bitetest.Start(t, app)
	r := bitetest.Request{Method: "GET", Target: "/users/7", Header: map[string][]string{"traceparent": {traceparent}}}
	srv.Do(r)
	return testing.AllocsPerRun(500, func() { srv.Do(r) })
}

func TestTheAllocationsTheObserverAddsToARequestAreMeasured(t *testing.T) {
	if testing.Short() {
		t.Skip("measures")
	}
	base := allocsPerRequest(t)
	_, sampled := recorder()
	dropped := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	for _, tc := range []struct {
		name string
		tp   trace.TracerProvider
		max  float64
	}{
		{"no-op provider", noop.NewTracerProvider(), 10},
		{"SDK, never sampled", dropped, 20},
		{"SDK, sampled, recorded", sampled, 60},
	} {
		got := allocsPerRequest(t, bites.WithObserver(otel.Tracing(otel.Config{TracerProvider: tc.tp})))
		t.Logf("%s: %.0f allocations a request more than without the Observer (%.0f against %.0f)", tc.name, got-base, got, base)
		if got-base > tc.max {
			t.Errorf("%s: %.0f more allocations than without the Observer, over %.0f", tc.name, got-base, tc.max)
		}
	}
}

func TestA5xxSpanRecordsTheApplicationsErrorAndAPanicItsValueAndStack(t *testing.T) {
	event := func(s sdktrace.ReadOnlySpan, name string) map[string]string {
		for _, e := range s.Events() {
			if e.Name == name {
				m := map[string]string{}
				for _, kv := range e.Attributes {
					m[string(kv.Key)] = kv.Value.Emit()
				}
				return m
			}
		}
		return nil
	}
	rec, tp := recorder()
	srv, _, _ := serve(t, tp)
	srv.Do(bitetest.Request{Method: "GET", Target: "/fail"})
	s := serverSpan(t, rec)
	if e := event(s, "exception"); e == nil || e["exception.message"] != "the database is down" || s.Status().Description != "the database is down" {
		t.Errorf("a handler's error: events %v status %v", s.Events(), s.Status())
	}

	rec, tp = recorder()
	srv, _, _ = serve(t, tp)
	srv.Do(bitetest.Request{Method: "GET", Target: "/panic"})
	e := event(serverSpan(t, rec), "exception")
	if e == nil || e["exception.message"] != "boom" || e["exception.type"] != "string" || !strings.Contains(e["exception.stacktrace"], "otel_test") {
		t.Errorf("a panic: %v", e)
	}
}
