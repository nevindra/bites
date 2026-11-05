package bites

import (
	"context"
	"log/slog"

	"github.com/nevindra/bites/internal/reqctx"
)

// Logger returns the app's logger (the one WithLogger gave, else slog.Default() as it was when
// the app was made) with the attributes of the request ctx belongs to: request_id when a
// middleware gave the request an id, method, and route, the pattern that matched, when one did.
// request_id and method are the names of the access log (AccessLog), so a line a handler writes
// and the access line of the same request are joined on request_id.
//
// On a Service's life (the ctx its Start receives, and what it derives) it returns the app's logger
// without request attributes. On any other context that is not a request's it returns slog.Default(). It works
// on a context the handler derived and on one kept after the request ended, which still names its
// own request.
//
// It allocates when it is called (the With of the attributes), and a request that does not call
// it pays nothing. Call it once in the handler and keep the logger for its lines.
func Logger(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	c := reqctx.Of(ctx)
	if c == nil {
		if a := serviceApp(ctx); a != nil && a.Log != nil {
			return a.Log
		}
		return slog.Default()
	}
	lg, method, route := c.Request()
	if lg == nil {
		lg = slog.Default()
	}
	var buf [3]any
	args := buf[:0]
	if id := c.RequestID(); id != "" {
		args = append(args, slog.String("request_id", id))
	}
	args = append(args, slog.String("method", method))
	if route != "" {
		args = append(args, slog.String("route", route))
	}
	return lg.With(args...)
}
