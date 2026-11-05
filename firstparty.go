package bites

import (
	"fmt"
	"strings"
)

// The four first-party middleware of ADR 0032 (CORS, RequestID, AccessLog, Timeout) live in the
// root package, not a subpackage: each reaches the app's logger and clock and the request's slab
// through the Request it is given, which only this package can do without widening the public
// API (ADR 0022). CORS, RequestID and Timeout are plain Middleware values; nothing about them is
// special to the chain. AccessLog is an Observer since milestone 3 (ADR 0051).

// refuseMiddleware panics with a RegistrationError for a first-party middleware that was given a
// configuration that cannot work (ADR 0007). fn is the constructor ("CORS"), and the message
// ends with the line the application called the constructor from.
func refuseMiddleware(fn, format string, args ...any) {
	panic(&RegistrationError{msg: fn + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + callerWhere()})
}

// isToken reports whether s is an HTTP token (RFC 9110 5.6.2): a header name or a method.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
