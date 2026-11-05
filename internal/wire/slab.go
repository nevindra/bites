package wire

import (
	"bytes"
	"errors"
	"unsafe"
)

// ErrBadEscape is returned by Unescape for a '%' not followed by two hex digits.
var ErrBadEscape = errors.New("invalid percent-escape")

// Slab is the request's text buffer: the one allocation made for all the text a request
// carries (ADR 0008). Copy and Unescape append to it and return strings that point into it.
//
// Why unsafe.String is safe here: the bytes a string points at are never modified after the
// string exists (the slab only appends, and only past what it has handed out), and the slab
// is never reused or returned to a pool, so nothing can write into them later. The garbage
// collector treats the string's pointer as an ordinary reference into the buffer and keeps
// the whole buffer alive for as long as any string does (probe q6-unsafe-string in
// docs/research/facts/report.md: 0 of 100 strings corrupted and 0 of 100 buffers collected
// while a string was held, 100 of 100 collected once dropped). unsafe.String's own contract
// is the same single condition: the bytes must not be modified while the string exists.
//
// A Slab is a value type with Init so the engine can embed it in the request's one allocation
// (or on the stack) and pay for the buffer alone; NewSlab exists for tests and callers that
// do not care, and costs a second allocation for the struct when it escapes.
//
// A Slab is not safe for concurrent use; one request owns it.
type Slab struct {
	buf       []byte
	used      int
	fallbacks int
	src       []byte // the read buffer's copy of the head, while dup stands for it; see Alias
	dup       string
}

// Alias says that dup is a copy of src, made in this slab or held as long as it. From then on
// Copy (and Unescape of text with nothing to decode) of bytes that lie inside src returns a
// substring of dup instead of copying them again: the same bytes, in memory that is never
// modified, so the string is as safe as one made by a copy. A request with middleware copies its
// head once for the Request view (ADR 0022), and the Input then reads its header, cookie and
// plain path and query values out of that copy, which is what lets the slab be a head and a
// target long rather than twice a head.
func (s *Slab) Alias(src []byte, dup string) { s.src, s.dup = src, dup }

// alias returns the substring of the aliased copy that stands for b, if b lies inside src.
func (s *Slab) alias(b []byte) (string, bool) {
	if len(s.src) == 0 {
		return "", false
	}
	off := int(uintptr(unsafe.Pointer(unsafe.SliceData(b))) - uintptr(unsafe.Pointer(unsafe.SliceData(s.src))))
	if off < 0 || off+len(b) > len(s.src) {
		return "", false
	}
	return s.dup[off : off+len(b)], true
}

// NewSlab returns a Slab with size bytes of room.
func NewSlab(size int) *Slab {
	s := new(Slab)
	s.Init(size)
	return s
}

// Init gives the slab a fresh buffer of size bytes, discarding any earlier one. It must not
// be called while strings from an earlier buffer are in use and the buffer could be written
// again, which cannot happen since the old buffer is dropped, never rewound.
func (s *Slab) Init(size int) {
	*s = Slab{buf: make([]byte, size)}
}

// Adopt makes b, whose first used bytes hold text the caller has already put there, the slab's
// buffer, discarding any earlier one. A WebSocket message is assembled in a buffer of its own
// (ADR 0046) and that buffer becomes the slab it is decoded with, so the message costs the one
// allocation it was made with. The room past used serves strings that need unescaping.
func (s *Slab) Adopt(b []byte, used int) {
	*s = Slab{buf: b[:cap(b)], used: used}
}

// Remaining is the room left in the buffer.
func (s *Slab) Remaining() int { return len(s.buf) - s.used }

// Fallbacks counts the strings that did not fit and were allocated on their own instead.
// A test that sizes the slab for a request asserts it is zero; in production an overflow is
// correct but costs an allocation (ADR 0003).
func (s *Slab) Fallbacks() int { return s.fallbacks }

// Copy copies b into the slab and returns a string over the copy.
func (s *Slab) Copy(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if str, ok := s.alias(b); ok {
		return str
	}
	dst := s.take(len(b))
	if dst == nil {
		return string(b)
	}
	copy(dst, b)
	return unsafe.String(&dst[0], len(dst))
}

// Reserve returns n writable bytes in the slab, for a reader to fill (the engine reads a body
// into it), or a fresh allocation counted as a fallback when the slab has no room. The caller
// finishes writing before any string is made over the bytes, and never writes after: strings
// from the slab are immutable from then on.
func (s *Slab) Reserve(n int) []byte {
	if n == 0 {
		return nil
	}
	if dst := s.take(n); dst != nil {
		return dst
	}
	return make([]byte, n)
}

// take reserves n bytes, or counts a fallback and returns nil.
func (s *Slab) take(n int) []byte {
	if n > s.Remaining() {
		s.fallbacks++
		return nil
	}
	// The capacity is clipped so nothing can append past this string into the next one.
	dst := s.buf[s.used : s.used+n : s.used+n]
	s.used += n
	return dst
}

// Unescape percent-decodes b into the slab, and also turns '+' into a space when plus is set
// (a query component; in a path '+' is a plus sign, ADR 0042). A '%' not followed by two hex
// digits is ErrBadEscape. The result is never longer than b, so it needs at most len(b) bytes.
func (s *Slab) Unescape(b []byte, plus bool) (string, error) {
	// The usual value has nothing to decode: two vectorised scans say so, and it is a plain Copy.
	if bytes.IndexByte(b, '%') < 0 && (!plus || bytes.IndexByte(b, '+') < 0) {
		return s.Copy(b), nil
	}
	// First pass validates and sizes, so a bad escape reserves nothing.
	n, work := 0, false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '%':
			if i+2 >= len(b) || unhex(b[i+1]) < 0 || unhex(b[i+2]) < 0 {
				return "", ErrBadEscape
			}
			i += 2
			work = true
		case '+':
			work = work || plus
		}
		n++
	}
	if !work {
		return s.Copy(b), nil
	}
	if n == 0 {
		return "", nil
	}
	dst := s.take(n)
	if dst == nil {
		dst = make([]byte, n)
	}
	j := 0
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '%':
			dst[j] = byte(unhex(b[i+1])<<4 | unhex(b[i+2]))
			i += 2
		case c == '+' && plus:
			dst[j] = ' '
		default:
			dst[j] = c
		}
		j++
	}
	return unsafe.String(&dst[0], n), nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c-'a') + 10
	case 'A' <= c && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
