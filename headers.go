package bites

import (
	"iter"
	"strings"
)

// Headers yields every header field of the request as received, in order: the name with the
// capitalisation the client used, and the value with the whitespace around it trimmed. A field
// sent twice is yielded twice. The strings are the copies the view holds (ADR 0008), so keeping
// one is safe; the iteration allocates nothing.
//
// It exists for code that must hand the whole head to something else, such as the
// bites/stdhttp package building an http.Request (ADR 0019); a middleware that wants one field
// uses Header.
func (r Request) Headers() iter.Seq2[string, string] {
	return func(yield func(name, value string) bool) {
		b := r.headers
		for len(b) > 0 {
			i := strings.IndexByte(b, '\n')
			if i < 0 {
				return
			}
			line := b[:i]
			b = b[i+1:]
			if len(line) <= 1 { // the blank line that ends the block
				return
			}
			c := strings.IndexByte(line, ':')
			if c <= 0 {
				continue
			}
			v := line[c+1:]
			for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
				v = v[1:]
			}
			for len(v) > 0 && (v[len(v)-1] == '\r' || v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
				v = v[:len(v)-1]
			}
			if !yield(line[:c], v) {
				return
			}
		}
	}
}
