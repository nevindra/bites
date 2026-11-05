package bites_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

const (
	tpValid  = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tpTrace  = "4bf92f3577b34da6a3ce929d0e0e4736"
	tpParent = "00f067aa0ba902b7"
)

func TestParseTraceparentAcceptsWhatW3CTraceContextAllowsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		in    string
		ok    bool
		flags byte
	}{
		{tpValid, true, 0x01},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", true, 0x00},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09", true, 0x09},      // version 00 carries its flags as they are
		{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09", true, 0x01},      // a later version keeps sampled and random only
		{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what", true, 0x01}, // and may carry more after a '-'
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-what", false, 0},   // version 00 is exactly 55 bytes
		{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01what", false, 0},    // more without a '-'
		{"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false, 0},        // version ff is invalid
		{"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01", false, 0},        // upper-case hex
		{"00-00000000000000000000000000000000-00f067aa0ba902b7-01", false, 0},        // a zero trace id
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", false, 0},        // a zero parent id
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g", false, 0},        // flags that are not hex
		{"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", false, 0},        // a wrong separator
		{"00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01", false, 0},         // a short trace id
		{"", false, 0},
	} {
		got, ok := bites.ParseTraceparent(tc.in)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.ID.String() != tpTrace || got.Parent.String() != tpParent || got.Flags != tc.flags || got.Span != (bites.SpanID{}) {
			t.Errorf("%q: %+v", tc.in, got)
		}
	}
}

func TestAChildKeepsTheTraceAndNamesThisSpanAsItsParent(t *testing.T) {
	tr, _ := bites.ParseTraceparent(tpValid)
	tr.Span = bites.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	tr.State = "congo=t61rcWkgMzE"
	c := tr.Child()
	if c.ID != tr.ID || c.Parent != tr.Span || c.Span == tr.Span || c.Span == (bites.SpanID{}) || c.Flags != tr.Flags || c.State != tr.State {
		t.Fatalf("%+v", c)
	}
	want := "00-" + tpTrace + "-" + c.Span.String() + "-01"
	if c.Traceparent() != want || string(c.AppendTraceparent([]byte("x"))) != "x"+want {
		t.Fatalf("%s, want %s", c.Traceparent(), want)
	}
	if back, ok := bites.ParseTraceparent(c.Traceparent()); !ok || back.Parent != c.Span {
		t.Fatalf("a traceparent the app sends does not parse back: %+v %v", back, ok)
	}
}

// traced serves an app with WithTraceContext whose handler reports the trace it sees.
func traced(t *testing.T, tc bites.TraceContext, opts ...bites.Option) (*bitetest.Server, chan bites.Trace) {
	t.Helper()
	seen := make(chan bites.Trace, 4)
	app := bites.New(append([]bites.Option{bites.WithClock(bitetest.NewClock()), bites.WithTraceContext(tc)}, opts...)...)
	app.Get("/t", func(ctx context.Context, _ Empty) (Empty, error) {
		tr, ok := bites.TraceFrom(ctx)
		if !ok {
			tr = bites.Trace{State: "none"}
		}
		seen <- tr
		return Empty{}, nil
	})
	return bitetest.Start(t, app), seen
}

func TestARequestWithAValidTraceparentGetsTheCallersTraceAndASpanOfItsOwn(t *testing.T) {
	srv, seen := traced(t, bites.TraceContext{})
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, "GET /t HTTP/1.1\r\nHost: x\r\nTraceparent: "+tpValid+"\r\ntracestate: congo=t61rcWkgMzE\r\ntracestate: rojo=00f067aa0ba902b7\r\n\r\n")
	r := srv.Read(c, false)
	tr := <-seen
	if tr.ID.String() != tpTrace || tr.Parent.String() != tpParent || tr.Span == (bites.SpanID{}) || tr.Span == tr.Parent || !tr.Sampled() {
		t.Fatalf("%+v", tr)
	}
	if tr.State != "congo=t61rcWkgMzE,rojo=00f067aa0ba902b7" {
		t.Fatalf("state %q", tr.State)
	}
	if r.Get("traceparent") != "" || r.Get("traceresponse") != "" || r.Get("tracestate") != "" {
		t.Fatalf("the answer carries trace headers: %v", r.Header)
	}
}

func TestEachRequestOfATraceGetsADifferentSpan(t *testing.T) {
	srv, seen := traced(t, bites.TraceContext{})
	srv.Do(bitetestWith("/t", "traceparent", tpValid))
	srv.Do(bitetestWith("/t", "traceparent", tpValid))
	a, b := <-seen, <-seen
	if a.ID != b.ID || a.Span == b.Span {
		t.Fatalf("%+v %+v", a, b)
	}
}

func TestAnInvalidOrMissingTraceparentGivesNoTraceUnlessBeginIsSet(t *testing.T) {
	srv, seen := traced(t, bites.TraceContext{})
	srv.Do(bitetestWith("/t", "traceparent", "00-bad", "tracestate", "congo=1"))
	srv.Do(req("GET", "/t"))
	for range 2 {
		if tr := <-seen; tr.State != "none" {
			t.Fatalf("a trace without a valid traceparent: %+v", tr)
		}
	}
	srv, seen = traced(t, bites.TraceContext{Begin: true})
	srv.Do(bitetestWith("/t", "traceparent", "00-bad", "tracestate", "congo=1"))
	tr := <-seen
	if tr.ID == (bites.TraceID{}) || tr.Span == (bites.SpanID{}) || tr.Parent != (bites.SpanID{}) || tr.Flags != 0 || tr.State != "" {
		t.Fatalf("a begun trace: %+v", tr)
	}
}

func TestTwoTraceparentHeadersAreNotATrace(t *testing.T) {
	srv, seen := traced(t, bites.TraceContext{})
	c := srv.Dial("203.0.113.9:1")
	defer c.Close()
	fmt.Fprint(c, "GET /t HTTP/1.1\r\nHost: x\r\ntraceparent: "+tpValid+"\r\ntraceparent: "+tpValid+"\r\n\r\n")
	srv.Read(c, false)
	if tr := <-seen; tr.State != "none" {
		t.Fatalf("%+v", tr)
	}
}

func TestWithoutWithTraceContextARequestHasNoTrace(t *testing.T) {
	seen := make(chan bool, 1)
	srv := start(t, func(app *bites.App) {
		app.Get("/t", func(ctx context.Context, _ Empty) (Empty, error) {
			_, ok := bites.TraceFrom(ctx)
			seen <- ok
			return Empty{}, nil
		})
	})
	srv.Do(bitetestWith("/t", "traceparent", tpValid))
	if <-seen {
		t.Fatal("a trace without WithTraceContext")
	}
}

func TestTheTraceIsSeenByObserversMiddlewareProvidersAndAContextKeptAfterTheRequest(t *testing.T) {
	type spanCtx struct{ Span bites.SpanID }
	got := make(chan string, 8)
	kept := make(chan context.Context, 1)
	obs := endFunc(func(ctx context.Context, _ bites.Request, _ bites.Outcome) {
		tr, _ := bites.TraceFrom(ctx)
		got <- "end " + tr.ID.String()
	})
	app := bites.New(bites.WithClock(bitetest.NewClock()), bites.WithTraceContext(bites.TraceContext{}), bites.WithObserver(obs))
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		tr, _ := bites.TraceFrom(ctx)
		got <- "middleware " + tr.ID.String()
		return next.Call(ctx), nil
	})
	app.Provide(func(ctx context.Context, _ bites.Request, _ struct{}) (spanCtx, error) {
		tr, _ := bites.TraceFrom(ctx)
		got <- "provider " + tr.ID.String()
		return spanCtx{tr.Span}, nil
	})
	app.Get("/t", func(ctx context.Context, in struct{ S spanCtx }) (Empty, error) {
		kept <- ctx
		return Empty{}, nil
	})
	srv := bitetest.Start(t, app)
	srv.Do(bitetestWith("/t", "traceparent", tpValid))
	for _, want := range []string{"middleware ", "provider ", "end "} {
		select {
		case s := <-got:
			if s != want+tpTrace {
				t.Fatalf("%q, want %q", s, want+tpTrace)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s", want)
		}
	}
	ctx := <-kept
	if tr, ok := bites.TraceFrom(ctx); !ok || tr.ID.String() != tpTrace {
		t.Fatalf("a context kept after its request lost its trace: %+v %v", tr, ok)
	}
}

// spanSetter is an Observer that begins its own span, as a tracer does, and makes it the
// request's.
type spanSetter struct{ span bites.SpanID }

func (s spanSetter) Start(ctx context.Context, req bites.Request) context.Context {
	tr, _ := bites.TraceFrom(ctx)
	tr.Span = s.span
	req.SetTrace(tr)
	return ctx
}
func (spanSetter) End(context.Context, bites.Request, bites.Outcome) {}

func TestAnObserverThatSetsTheTraceIsWhatTheHandlerAndTheAccessLogSee(t *testing.T) {
	want := bites.SpanID{9, 9, 9, 9, 9, 9, 9, 9}
	lbSeen := make(chan bites.Trace, 1)
	srv, lb := startJSON(t, bitetest.NewClock(), func(app *bites.App) {
		app.Get("/t", func(ctx context.Context, _ Empty) (Empty, error) {
			tr, _ := bites.TraceFrom(ctx)
			lbSeen <- tr
			return Empty{}, nil
		})
	}, bites.WithTraceContext(bites.TraceContext{}), bites.WithObserver(bites.AccessLog(), spanSetter{want}))
	srv.Do(bitetestWith("/t", "traceparent", tpValid, "tracestate", "congo=1"))
	tr := <-lbSeen
	if tr.Span != want || tr.ID.String() != tpTrace || tr.State != "congo=1" {
		t.Fatalf("%+v", tr)
	}
	if m := waitRecords(t, srv, lb, 1)[0]; m["span_id"] != want.String() {
		t.Fatalf("%v", m)
	}
}

func TestSetTraceWithAZeroIDRemovesTheTrace(t *testing.T) {
	srv, seen := traced(t, bites.TraceContext{}, bites.WithObserver(endFuncStart(func(_ context.Context, req bites.Request) {
		req.SetTrace(bites.Trace{})
	})))
	srv.Do(bitetestWith("/t", "traceparent", tpValid))
	if tr := <-seen; tr.State != "none" {
		t.Fatalf("%+v", tr)
	}
}

// endFuncStart is an Observer whose Start runs f.
type endFuncStart func(context.Context, bites.Request)

func (f endFuncStart) Start(ctx context.Context, req bites.Request) context.Context {
	f(ctx, req)
	return ctx
}
func (endFuncStart) End(context.Context, bites.Request, bites.Outcome) {}

func TestContextWithTraceGivesTraceFromATraceOutsideARequest(t *testing.T) {
	tr, _ := bites.ParseTraceparent(tpValid)
	if _, ok := bites.TraceFrom(context.Background()); ok {
		t.Fatal("a background context has a trace")
	}
	got, ok := bites.TraceFrom(bites.ContextWithTrace(context.Background(), tr))
	if !ok || got != tr {
		t.Fatalf("%+v %v", got, ok)
	}
	if s := tr.ID.String(); !strings.HasPrefix(s, "4bf9") || len(s) != 32 {
		t.Fatal(s)
	}
}
