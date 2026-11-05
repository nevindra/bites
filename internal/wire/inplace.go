package wire

import "unsafe"

// UnescapeInPlace percent-decodes b where it lies, and also turns '+' into a space when plus is
// set, and returns a string over the decoded prefix of b. It is for a body the caller owns and
// will not read again as bytes (a form, ADR 0044): the result is never longer than b, so the
// decoding needs no room of its own and costs no allocation. A '%' not followed by two hex digits
// is ErrBadEscape, and b is left as it was. The caller must not decode the same bytes twice, and
// must not write to b afterwards, because the string aliases it.
func UnescapeInPlace(b []byte, plus bool) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	work := false
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
	}
	if !work {
		return unsafe.String(&b[0], len(b)), nil
	}
	j := 0
	for i := 0; i < len(b); i++ {
		switch c := b[i]; {
		case c == '%':
			b[j] = byte(unhex(b[i+1])<<4 | unhex(b[i+2]))
			i += 2
		case c == '+' && plus:
			b[j] = ' '
		default:
			b[j] = c
		}
		j++
	}
	if j == 0 {
		return "", nil
	}
	return unsafe.String(&b[0], j), nil
}
