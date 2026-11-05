package bites

import (
	"context"
	"math/rand/v2"
	"unsafe"

	"github.com/nevindra/bites/internal/reqctx"
)

// RequestIDOptions configures RequestID.
type RequestIDOptions struct {
	// Header is the name of the request and response header that carries the id; "X-Request-ID"
	// when empty. It must be an HTTP token.
	Header string
}

const (
	defaultRequestIDHeader = "X-Request-ID"
	maxRequestIDLen        = 64
	generatedIDLen         = 16
)

// RequestID is the request-ID middleware of ADR 0032. It gives the request an id with
// Request.SetRequestID, so every problem+json answer carries it as request_id (ADR 0012), and
// sets it as a header of the response.
//
// The id is the one the client sent in the header when it is well formed: 1 to 64 bytes, each a
// letter, a digit or one of "-_.:". That covers a UUID, a ULID, a hex trace id and the ids
// gateways and load balancers add, and it keeps out whatever could split a header, break a log
// line or make an answer large (an id is echoed in the response, in every error body and in the
// access log, so it is client-controlled text in three places). Anything else, an id that is too
// long or has other bytes, is not rejected: it is replaced, because a client's formatting error
// is no reason to fail its request.
//
// Without a usable incoming id it generates one: 16 lower-case hex digits of a random 64-bit
// value from math/rand/v2's per-thread ChaCha8 generator. That is unique enough to find one
// request in a log and is not a secret or a token; do not use it as one. The text is made in the
// request's slab, so a request with a generated id costs no allocation of its own: the slab of a
// request whose chain holds RequestID is sized with room for it at Registration. RequestID called
// from inside another middleware is not seen there, and its generated id then costs one
// allocation. An incoming id is a substring of the copy
// of the head the Request already holds.
//
// Put it first in the chain (the first argument of app.Use) so that every answer written inside
// it, a preflight or a 404 included, carries the id. The 500 the engine writes after a panic
// carries it too, in its body and in the same header, although the chain never returned.
//
// It panics when more than one options value is given or the header is not a token.
func RequestID(opts ...RequestIDOptions) Middleware {
	var o RequestIDOptions
	switch len(opts) {
	case 0:
	case 1:
		o = opts[0]
	default:
		refuseMiddleware("RequestID", "%d options values were given; give one RequestIDOptions", len(opts))
	}
	if o.Header == "" {
		o.Header = defaultRequestIDHeader
	}
	if !isToken(o.Header) {
		refuseMiddleware("RequestID", "Header %q is not an HTTP header name: use letters, digits and the symbols of RFC 9110 token, without spaces or colons", o.Header)
	}
	return (&requestID{header: o.Header}).serve
}

// requestID is the RequestID middleware. It is a method value so that Registration can recognise
// it in a chain and size the slab of that chain's requests for the id it generates (slabRoom).
type requestID struct{ header string }

func (m *requestID) serve(ctx context.Context, req Request, next Next) (Response, error) {
	id := req.Header(m.header)
	if !validRequestID(id) {
		id = req.newRequestID()
	}
	req.SetRequestID(id)
	req.setRequestIDHeader(m.header)
	res := next.Call(ctx)
	res.AddHeader(m.header, id)
	return res, nil
}

// validRequestID reports whether an incoming id may be taken as it is.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

const hexDigits = "0123456789abcdef"

// newRequestID makes an id in the request's slab: the bytes are written once, before any string
// is made over them, and never again (the slab's own rule, internal/wire).
func (r Request) newRequestID() string {
	if !r.live() {
		return ""
	}
	b := r.run.ex.Slab().Reserve(generatedIDLen)
	v := rand.Uint64()
	for i := generatedIDLen - 1; i >= 0; i-- {
		b[i] = hexDigits[v&15]
		v >>= 4
	}
	return unsafe.String(&b[0], len(b))
}

// RequestIDFrom returns the id a middleware gave the request ctx belongs to with
// Request.SetRequestID (the RequestID middleware does), or "" when there is none, when no
// middleware ran, and when ctx is not a request's context or derived from one. It is how a
// handler, which holds only its ctx, reads the id that the access log and every problem+json
// answer carry, as Request.RequestID is for a middleware or an Observer, which hold a Request.
//
// It works on a context the handler derived (WithTimeout, WithValue, WithCancel), allocates
// nothing, and answers with its own request's id on a context kept after the request ended,
// whatever ran since: the id is text in the request's own memory (ADR 0008).
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if c := reqctx.Of(ctx); c != nil {
		return c.RequestID()
	}
	return ""
}
