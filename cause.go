package bites

import (
	"context"

	"github.com/nevindra/bites/internal/reqctx"
)

// ErrClientGone is the cause of a request's context when the client disconnected while the
// handler ran (ADR 0016). Compare it with what Cause returns:
//
//	if bites.Cause(ctx) == bites.ErrClientGone { ... }
//
// context.Cause cannot report it, because the context package finds a cause only through a key
// it does not export; it returns context.Canceled instead.
var ErrClientGone = reqctx.ErrClientGone

// Cause reports why ctx was cancelled, or nil while it is live. For a request's context, and for
// any context derived from one with WithValue, WithCancel or WithTimeout, it is the request's own
// cause where context.Cause would only say context.Canceled: ErrClientGone when the client left,
// ErrServerClosed when Shutdown cut a live stream, context.DeadlineExceeded after Timeout. A cause
// of the derived context's own (its deadline, the cause given to its cancel) is reported as it
// is. For any other context it is context.Cause.
func Cause(ctx context.Context) error {
	c := context.Cause(ctx)
	if c != context.Canceled {
		return c
	}
	if rc := reqctx.Of(ctx); rc != nil {
		if rcause := rc.Cause(); rcause != nil {
			return rcause
		}
	}
	return c
}
