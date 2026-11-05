// Package otel records a server span of OpenTelemetry for each request of a bites app, as an
// Observer (ADR 0051) in a module apart from the core (ADR 0063, ADR 0070), which keeps the
// OpenTelemetry API out of every application that does not import it.
//
//	app := bites.New(bites.WithObserver(otel.Tracing(otel.Config{TracerProvider: tp}), bites.AccessLog()))
//
// Start reads the caller's W3C trace context (ADR 0059): the trace the core read when the app has
// WithTraceContext, or else the request's traceparent and tracestate headers. It starts a server
// span whose remote parent is the caller's span, or a new trace, and returns a context holding
// it, so a span the handler starts (a database query's) is its child. It then gives the request
// the span's ids with Request.SetTrace, so bites.TraceFrom, an outgoing call's traceparent and
// the access log name the span this module records. End names the span "METHOD /route", sets the
// HTTP attributes of the OpenTelemetry semantic conventions (method, route, status, sizes),
// records a 5xx and a panic as an error and an abandoned request as an event, and ends the span.
//
// It depends on go.opentelemetry.io/otel and go.opentelemetry.io/otel/trace, v1.47.0. The SDK
// that exports spans is the application's choice, given as Config.TracerProvider.
package otel

import (
	"context"
	"fmt"
	"strconv"

	"github.com/nevindra/bites"
	gotel "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of the tracer: the module's import path.
const ScopeName = "github.com/nevindra/bites/modules/otel"

// Config configures Tracing.
type Config struct {
	// TracerProvider makes the tracer; the global one, otel.GetTracerProvider(), when nil. With
	// the global provider left as OpenTelemetry's no-op, no span is recorded and the request's
	// trace is left as the core read it.
	TracerProvider trace.TracerProvider
}

// Tracing returns the Observer that records a server span for each request. Give it to
// bites.WithObserver before AccessLog, so it wraps the access log: the record AccessLog writes
// at End then names the span this Observer recorded (it does in either order, since the span is
// set at Start, but the first Observer given is the outermost).
func Tracing(cfg Config) bites.Observer {
	tp := cfg.TracerProvider
	if tp == nil {
		tp = gotel.GetTracerProvider()
	}
	return &tracing{tracer: tp.Tracer(ScopeName)}
}

type tracing struct {
	tracer trace.Tracer
}

// The attribute keys of the OpenTelemetry semantic conventions for HTTP servers (stable since
// v1.23.0), written out so the module does not follow the semconv package's renames.
const (
	keyMethod         = "http.request.method"
	keyMethodOriginal = "http.request.method_original"
	keyRoute          = "http.route"
	keyStatus         = "http.response.status_code"
	keyRequestSize    = "http.request.body.size"
	keyResponseSize   = "http.response.body.size"
	keyPath           = "url.path"
	keyScheme         = "url.scheme"
	keyClient         = "client.address"
	keyServer         = "server.address"
	keyUserAgent      = "user_agent.original"
	keyErrorType      = "error.type"
)

// knownMethod reports whether m is one of the methods the conventions name; any other is
// recorded as "_OTHER", with the method itself as http.request.method_original, so a client
// cannot grow the set of values.
func knownMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return true
	}
	return false
}

func (o *tracing) Start(ctx context.Context, req bites.Request) context.Context {
	parent, ok := bites.TraceFrom(ctx)
	if !ok {
		parent, ok = bites.ParseTraceparent(req.Header("traceparent"))
		if ok {
			parent.State = req.Header("tracestate")
		}
	}
	// A trace the core began with TraceContext.Begin has no parent: the span is a root.
	remote := ok && parent.Parent != (bites.SpanID{})
	if remote {
		ts, _ := trace.ParseTraceState(parent.State)
		ctx = trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    trace.TraceID(parent.ID),
			SpanID:     trace.SpanID(parent.Parent),
			TraceFlags: trace.TraceFlags(parent.Flags),
			TraceState: ts,
			Remote:     true,
		}))
	}
	method := req.Method()
	attrs := make([]attribute.KeyValue, 0, 8)
	if knownMethod(method) {
		attrs = append(attrs, attribute.String(keyMethod, method))
	} else {
		attrs = append(attrs, attribute.String(keyMethod, "_OTHER"), attribute.String(keyMethodOriginal, method))
		method = "HTTP"
	}
	attrs = append(attrs, attribute.String(keyPath, req.Path()), attribute.String(keyScheme, req.Scheme()))
	if a := req.ClientAddr(); a.IsValid() {
		attrs = append(attrs, attribute.String(keyClient, a.String()))
	}
	if h := req.Host(); h != "" {
		attrs = append(attrs, attribute.String(keyServer, h))
	}
	if ua := req.Header("user-agent"); ua != "" {
		attrs = append(attrs, attribute.String(keyUserAgent, ua))
	}
	ctx, span := o.tracer.Start(ctx, method, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
	sc := span.SpanContext()
	// A no-op tracer hands back the parent's context as its span's: there is no span of its own
	// to name, so the request keeps the trace the core read.
	if !sc.IsValid() || (remote && sc.SpanID() == trace.SpanID(parent.Parent)) {
		return ctx
	}
	t := bites.Trace{ID: bites.TraceID(sc.TraceID()), Span: bites.SpanID(sc.SpanID()), Flags: byte(sc.TraceFlags())}
	if remote {
		t.Parent = parent.Parent
		t.State = parent.State
		if ts := sc.TraceState(); ts.Len() > 0 && ts.String() != parent.State {
			t.State = ts.String() // the sampler changed it
		}
	}
	req.SetTrace(t)
	return ctx
}

func (o *tracing) End(ctx context.Context, req bites.Request, out bites.Outcome) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		span.End()
		return
	}
	method := req.Method()
	if !knownMethod(method) {
		method = "HTTP"
	}
	attrs := make([]attribute.KeyValue, 0, 8)
	if out.Route != "" {
		span.SetName(method + " " + out.Route)
		attrs = append(attrs, attribute.String(keyRoute, out.Route))
	}
	if out.Status != 0 {
		attrs = append(attrs, attribute.Int(keyStatus, out.Status))
	}
	if out.Read > 0 {
		attrs = append(attrs, attribute.Int64(keyRequestSize, out.Read))
	}
	if out.Written > 0 {
		attrs = append(attrs, attribute.Int64(keyResponseSize, out.Written))
	}
	switch {
	case out.Panicked:
		attrs = append(attrs, attribute.String(keyErrorType, "panic"))
		span.SetStatus(codes.Error, "panic")
		// The exception event of the semantic conventions, with the stack of the goroutine that
		// panicked, which RecordError cannot give: it would take the stack of this End.
		span.AddEvent("exception", trace.WithAttributes(
			attribute.String("exception.type", fmt.Sprintf("%T", out.Panic)),
			attribute.String("exception.message", fmt.Sprint(out.Panic)),
			attribute.String("exception.stacktrace", string(out.Stack)),
		))
	case out.Status >= 500:
		attrs = append(attrs, attribute.String(keyErrorType, strconv.Itoa(out.Status)))
		if out.Err != nil {
			span.RecordError(out.Err)
			span.SetStatus(codes.Error, out.Err.Error())
		} else {
			span.SetStatus(codes.Error, "")
		}
	case out.Err != nil && out.Streamed:
		// A stream cut by its function's error after a 2xx head: the status says nothing of it.
		attrs = append(attrs, attribute.String(keyErrorType, "stream"))
		span.RecordError(out.Err)
		span.SetStatus(codes.Error, out.Err.Error())
	}
	if out.Upgraded {
		attrs = append(attrs, attribute.Bool("bites.upgraded", true))
	}
	if out.Streamed {
		attrs = append(attrs, attribute.Bool("bites.streamed", true))
	}
	if out.Abandoned {
		// The client left, or stopped reading, before the answer reached it (ADR 0049). It is not
		// the server's error, so the status is left to the answer the chain chose.
		attrs = append(attrs, attribute.Bool("bites.abandoned", true))
		span.AddEvent("abandoned")
	}
	span.SetAttributes(attrs...)
	span.End()
}
