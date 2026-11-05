package engine

// HeaderBytes is Header for a value the caller built in a buffer of its own (a file's ETag,
// Last-Modified or Content-Range, ADR 0064): the bytes are copied into the head and not kept, so
// a buffer on the caller's stack stays there. It is not for Content-Type, which the Response
// remembers as a string.
func (r *Response) HeaderBytes(name string, value []byte) {
	r.head = appendClean(r.head, name)
	r.head = append(r.head, ':', ' ')
	r.head = appendCleanBytes(r.head, value)
	r.head = append(r.head, '\r', '\n')
}

func appendCleanBytes(b, v []byte) []byte {
	start := len(b)
	b = append(b, v...)
	for i := start; i < len(b); i++ {
		if c := b[i]; c == '\r' || c == '\n' || c == 0 {
			b[i] = ' '
		}
	}
	return b
}
