package engine

import "crypto/tls"

// IsTLS reports whether the request arrived over TLS that this engine terminated, which is what
// Request.Scheme starts from (ADR 0025). It is a type check on the connection and allocates
// nothing. It lives in a file of its own so that the method is easy to find and to move.
func (ex *Exchange) IsTLS() bool {
	_, ok := ex.c.nc.(*tls.Conn)
	return ok
}
