package bites

import (
	"errors"
	"io"
	"reflect"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/jsoncodec"
)

// BodyReader is a Body whose handler reads the request body as it arrives, as an io.Reader, whatever
// its media type (ADR 0044). It is Upload for a body that is not multipart: a resumable upload's
// chunk sent as the raw body of a PUT or PATCH, or a file sent as it is (PUT /files/{name} with
// Content-Type: application/pdf). Nothing is buffered whole: a Read fills the slice the handler
// passes, so an 8 MiB body costs the handler's own choices, not 8 MiB.
//
//	type PutPart struct {
//		Path struct{ ID string }
//		Body bites.BodyReader
//	}
//
//	func putPart(ctx context.Context, in PutPart) (bites.NoContent, error) {
//		_, err := io.Copy(store.Writer(in.Path.ID), in.Body)
//		return bites.NoContent{}, err
//	}
//
// A BodyReader route must be given MaxBody, on the route or on a group above it: the most a route
// may receive is a decision of the application, so there is no default and a route without one is
// refused at Registration. BodyTimeout defaults to max(30 s, MaxBody / 128 KiB/s), as for an
// Upload, and covers the whole body from the first Read. MaxInFlight(n) bounds the bodies in
// flight. The media type is not checked: a handler that wants one checks the Header section. A
// request with no body reads as an empty one: the first Read returns io.EOF.
//
// The error Read returns when the body is refused (too large, too slow, cut short, malformed) is a
// *StatusError with the status the client should see, so returning it answers correctly; the
// connection is closed after the answer. The Section values of the Input are decoded, and
// Validate has run, before the handler is called; the body has not been read.
//
// A BodyReader is valid only while the handler runs. Once the handler has returned, Read fails
// with ErrUploadClosed and touches nothing, so a goroutine that kept one cannot read another
// request's bytes (ADR 0008); the zero value fails the same way. A handler that returns before the
// end of the body leaves bytes unread, and the connection is closed after the answer.
type BodyReader struct{ m *engine.Multipart }

// Read reads the next bytes of the body into b and returns io.EOF at its end. It waits for the
// client on the connection's goroutine, up to the route's BodyTimeout for the whole body.
func (r BodyReader) Read(b []byte) (int, error) {
	if r.m == nil {
		return 0, ErrUploadClosed
	}
	n, err := r.m.ReadRaw(b)
	if err != nil && err != io.EOF {
		err = uploadError(err)
	}
	return n, err
}

var bodyReaderType = reflect.TypeFor[BodyReader]()

// containsType reports whether t is target, or holds it at any depth.
func containsType(t, target reflect.Type, seen map[reflect.Type]bool) bool {
	if t == target {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return containsType(t.Elem(), target, seen)
	case reflect.Map:
		return containsType(t.Key(), target, seen) || containsType(t.Elem(), target, seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if containsType(t.Field(i).Type, target, seen) {
				return true
			}
		}
	}
	return false
}

// readBodyReader opens the request body to be read by the handler. It reads none of it. It
// reports false when it has answered (413 or 503).
func (b *bodyPlan) readBodyReader(ex *engine.Exchange, in unsafe.Pointer, _ *jsoncodec.Errors, cfg *routeConfig) bool {
	m, err := ex.OpenBody(engine.UploadOptions{
		Max: cfg.maxBody, Timeout: cfg.bodyTimeout, InFlight: cfg.inflight, Limit: int64(cfg.maxInFlight),
	})
	if err != nil {
		var he *h1.Error
		if errors.As(err, &he) {
			r := problem(ex, he.Status, he.Reason)
			if he == engine.ErrUploadBusy {
				r.Header("Retry-After", "5")
			}
		} else {
			problem(ex, 400, "")
		}
		return false
	}
	*(*BodyReader)(unsafe.Add(in, b.off)) = BodyReader{m}
	return true
}
