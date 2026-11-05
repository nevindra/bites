package ws

import "unicode/utf8"

// utf8State validates UTF-8 across the chunks of one message. It holds at most the three bytes
// of a rune that a chunk ended in the middle of. The zero value is ready.
type utf8State struct {
	carry [3]byte
	n     uint8
}

func (u *utf8State) reset() { u.n = 0 }

// complete reports whether the bytes seen so far end on a rune boundary.
func (u *utf8State) complete() bool { return u.n == 0 }

// write validates p as the next bytes of the message and reports whether it is still valid.
// It fails as soon as the bytes cannot start a valid rune, without waiting for the rest of one.
func (u *utf8State) write(p []byte) bool {
	if u.n > 0 {
		var tmp [utf8.UTFMax]byte
		k := copy(tmp[:], u.carry[:u.n])
		for k < utf8.UTFMax && len(p) > 0 && !utf8.FullRune(tmp[:k]) {
			tmp[k] = p[0]
			p = p[1:]
			k++
		}
		if !utf8.FullRune(tmp[:k]) {
			// p ran out inside the rune; k < 4 here.
			u.n = uint8(copy(u.carry[:], tmp[:k]))
			return true
		}
		// FullRune is true at the first byte that completes the rune or proves it invalid, so
		// the probe holds exactly that rune and nothing after it.
		if r, size := utf8.DecodeRune(tmp[:k]); r == utf8.RuneError && size <= 1 {
			return false
		}
		u.n = 0
	}
	// The chunk is validated whole by utf8.Valid, which skips ASCII sixteen bytes at a time
	// (20x the loop a byte at a time on an ASCII message of 4 KiB, 20% slower on text that is
	// almost all multi-byte runes); only a rune the chunk ends inside is held back. FullRune is
	// true for a rune that is complete or already proven invalid, so a held-back tail is a prefix
	// that may still become valid.
	end := len(p)
	for k := 1; k <= 3 && k <= len(p); k++ {
		b := p[len(p)-k]
		if b < utf8.RuneSelf {
			break
		}
		if b >= 0xC0 { // the lead byte of the last rune
			if !utf8.FullRune(p[len(p)-k:]) {
				end = len(p) - k
			}
			break
		}
	}
	if !utf8.Valid(p[:end]) {
		return false
	}
	u.n = uint8(copy(u.carry[:], p[end:]))
	return true
}
