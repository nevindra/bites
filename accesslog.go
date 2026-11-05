package bites

import (
	"context"
	"log/slog"
)

// AccessLogOptions configures AccessLog.
type AccessLogOptions struct {
	// Logger receives the records; the app's own logger (WithLogger) when nil.
	Logger *slog.Logger

	// Level is the level of every record; slog.LevelInfo, the zero value, by default. When the
	// logger is not enabled at this level the Observer does nothing at End, and allocates nothing.
	Level slog.Level
}

// AccessLog is the access log of ADR 0032, an Observer (ADR 0051) given with
// WithObserver(AccessLog()): one slog record, "request", for each observed request, written when
// the request has really ended, with the attributes
//
//	method, path, status, size, duration, request_id (when a middleware set one),
//	trace_id and span_id (when the request has a Trace, ADR 0059),
//	streaming=true (an answer that is a Stream or SSE), abandoned=true (the client left),
//	panicked=true (a panic was answered with a 500),
//	then every attribute a Provider or middleware added with Request.AddLogAttrs (ADR 0023)
//
// The path is the raw path without the query, because a query is where secrets and personal data
// go. status is what was sent: the real status of a stream, the 500 after a panic, the 503 of
// Timeout. size is the body bytes written, after gzip: the bytes a stream sent, the size of a
// Problem a middleware returned, 0 for a HEAD. duration is the Outcome's, from the parsed head to
// the last byte, on the app's clock; for a stream it is the stream's whole life. The record's
// time is End on the app's clock, and it has no source, which would be the same line of
// bites for every request.
//
// When the client left (ADR 0016) nothing reached it, status is whatever the chain left (0 when
// nothing) and size is 0, and the record has abandoned=true, which is how a vanished client
// differs from a failure of the server (ADR 0049). Every request that reaches the app is logged,
// 404, 405, a CORS preflight and a refusal by a middleware included; the health and readiness
// probes of WithHealth are not observed and not logged.
//
// It panics when more than one options value is given.
func AccessLog(opts ...AccessLogOptions) Observer {
	var o AccessLogOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseMiddleware("AccessLog", "%d options values were given; give one AccessLogOptions", len(opts))
	}
	return &accessLog{o: o}
}

// accessLog is the Observer AccessLog returns.
type accessLog struct{ o AccessLogOptions }

// Start adds nothing: the record is written at End.
func (l *accessLog) Start(ctx context.Context, _ Request) context.Context { return ctx }

// End writes the record.
func (l *accessLog) End(ctx context.Context, req Request, out Outcome) {
	lg := l.o.Logger
	if lg == nil {
		lg = req.run.a.log
	}
	if !lg.Enabled(ctx, l.o.Level) {
		return
	}
	var buf [16]slog.Attr
	at := append(buf[:0],
		slog.String("method", req.Method()),
		slog.String("path", req.Path()),
		slog.Int("status", out.Status),
		slog.Int64("size", out.Written),
		slog.Duration("duration", out.Duration),
	)
	if id := req.RequestID(); id != "" {
		at = append(at, slog.String("request_id", id))
	}
	if id, span := traceHex(ctx); id != "" {
		at = append(at, slog.String("trace_id", id), slog.String("span_id", span))
	}
	if out.Streamed {
		at = append(at, slog.Bool("streaming", true))
	}
	if out.Abandoned {
		at = append(at, slog.Bool("abandoned", true))
	}
	if out.Panicked {
		at = append(at, slog.Bool("panicked", true))
	}
	if out.Err != nil {
		// The application's error, so the access line of a failure says why on its own; only a
		// failed request pays for the text (ADR 0051).
		at = append(at, slog.String("error", out.Err.Error()))
	}
	at = req.AppendLogAttrs(at)
	// The record is built here rather than by LogAttrs: its time is the End the Outcome was
	// measured to, on the app's clock, and it has no source, which would be this line for every
	// request. That spares a clock read and runtime.Callers (docs/research/hotpath-mac).
	r := slog.NewRecord(req.run.ended, l.o.Level, "request", 0)
	r.AddAttrs(at...)
	lg.Handler().Handle(ctx, r)
}
