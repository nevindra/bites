package jsoncodec

import (
	"encoding/binary"
	"math/bits"
	"unicode/utf8"
	"unsafe"

	"encoding/json/jsontext"
)

// The scan for the bytes a JSON string cannot hold as they are, shared by the decoder's fast
// path and the encoder: a quote, a backslash, a control byte below 0x20, or a byte at or above
// 0x80 (the start of a rune, which needs its UTF-8 checked). Eight bytes are tested at a time.

const (
	lsb = 0x0101010101010101
	msb = 0x8080808080808080
)

// specialIn returns, in each byte's high bit, whether the byte of x is one the scan stops at.
// A borrow can set a higher byte's bit as well, but never a lower one, so the lowest set bit
// always marks the first such byte exactly.
func specialIn(x uint64) uint64 {
	q := x ^ (lsb * '"')
	s := x ^ (lsb * '\\')
	return (x | (x - lsb*0x20) | (q-lsb)&^q | (s-lsb)&^s) & msb
}

// plainRun returns the index of the first byte of b[i:] that a JSON string cannot hold as it
// is, or len(b). Eight bytes are tested a round, and the last bytes, fewer
// than eight, one at a time by plainTail.
func plainRun(b []byte, i int) int {
	for ; i+8 <= len(b); i += 8 {
		if m := specialIn(binary.LittleEndian.Uint64(b[i:])); m != 0 {
			return i + bits.TrailingZeros64(m)>>3
		}
	}
	return plainTail(b, i)
}

func plainTail(b []byte, i int) int {
	for ; i < len(b); i++ {
		if c := b[i]; c < 0x20 || c >= 0x80 || c == '"' || c == '\\' {
			return i
		}
	}
	return i
}

// appendString writes s quoted, as jsontext.AppendQuote does with its defaults: nothing is
// HTML-escaped, and a string that is not valid UTF-8 is an error. A string with no byte to
// escape (valid UTF-8 included) is copied as it is; any other goes to jsontext.
func appendString(dst []byte, s string) ([]byte, error) {
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	for i := 0; ; {
		i = plainRun(b, i)
		if i == len(b) {
			dst = append(dst, '"')
			dst = append(dst, s...)
			return append(dst, '"'), nil
		}
		if b[i] < utf8.RuneSelf { // a quote, a backslash or a control byte: escaped by jsontext
			break
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			break // not UTF-8: jsontext reports it
		}
		i += size
	}
	return jsontext.AppendQuote(dst, s)
}
