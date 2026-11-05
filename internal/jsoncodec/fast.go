package jsoncodec

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

// The fast path decodes the common body, a flat object of scalars, without the tokenizer: no
// pooled state, no Reset, one pass over the bytes. It accepts only a strict subset of JSON whose
// meaning it is sure of (strings of printable ASCII with no escape, integers with no fraction or
// exponent, each member once, every member known, every required member present) and gives up on
// anything else, leaving the tokenizer to decode the body again from its first byte. So every
// error, and every body outside the subset, is answered by the one decoder that always was;
// FuzzTheFastPathAgreesWithTheTokenizer holds the two to the same result (docs/research/hotpath-mac).

// flatFields reports whether n is a struct the fast path can decode whole, and the mask of its
// required members: at most 64 fields, each a bool, a number, a string or an enum, or an Opt of
// one.
func flatFields(n *dnode) (required uint64, ok bool) {
	if n.kind != dStruct || len(n.fields) == 0 || len(n.fields) > 64 {
		return 0, false
	}
	for i, f := range n.fields {
		k := f.node
		if k.kind == dOpt {
			k = k.elem
		} else {
			required |= 1 << i
		}
		if k.kind == dChecked {
			k = k.elem
		}
		switch k.kind {
		case dBool, dInt, dUint, dFloat, dString, dEnum:
		default:
			return 0, false
		}
	}
	return required, true
}

// decodeFast decodes body into dst when it is inside the subset, and reports whether it did.
// When it gives up dst may hold some members; the tokenizer then stores every member the body
// has, and the fast path never stores one the body lacks, so what it left is overwritten.
func (d *Decoder) decodeFast(body []byte, dst unsafe.Pointer) bool {
	n := d.root
	i := skipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	i = skipSpace(body, i+1)
	var seen uint64
	if i < len(body) && body[i] == '}' {
		i++
	} else {
		for {
			if i >= len(body) || body[i] != '"' {
				return false
			}
			end, ok := plainString(body, i+1)
			if !ok {
				return false
			}
			fi := n.lookup(body[i+1 : end])
			if fi < 0 || seen&(1<<fi) != 0 {
				return false
			}
			seen |= 1 << fi
			i = skipSpace(body, end+1)
			if i >= len(body) || body[i] != ':' {
				return false
			}
			i = skipSpace(body, i+1)
			f := &n.fields[fi]
			vn, p := f.node, unsafe.Add(dst, f.off)
			var flag *bool
			if vn.kind == dOpt {
				flag = (*bool)(unsafe.Add(p, vn.flagOff))
				p, vn = unsafe.Add(p, vn.valOff), vn.elem
			}
			if i, ok = fastScalar(vn, body, i, p); !ok {
				return false
			}
			if flag != nil {
				*flag = true
			}
			i = skipSpace(body, i)
			if i >= len(body) {
				return false
			}
			if body[i] == '}' {
				i++
				break
			}
			if body[i] != ',' {
				return false
			}
			i = skipSpace(body, i+1)
		}
	}
	return skipSpace(body, i) == len(body) && seen&d.required == d.required
}

// fastScalar stores the value at b[i:] in p, as n says, and returns the index after it. A value
// the next byte does not end (a number followed by '.', "truex") is refused by the caller, which
// wants space, ',' or '}' there.
func fastScalar(n *dnode, b []byte, i int, p unsafe.Pointer) (int, bool) {
	if i >= len(b) {
		return i, false
	}
	switch n.kind {
	case dChecked:
		j, ok := fastScalar(n.elem, b, i, p)
		if !ok || n.check(noescape(p)) != nil {
			return i, false // the tokenizer decodes it again and reports the check's error
		}
		return j, true
	case dString, dEnum:
		if b[i] != '"' {
			return i, false
		}
		end, ok := plainString(b, i+1)
		if !ok {
			return i, false
		}
		v := b[i+1 : end]
		if n.kind == dEnum {
			for _, e := range n.vals {
				if string(v) == e {
					*(*string)(p) = e // the listed string, as the tokenizer's path stores
					return end + 1, true
				}
			}
			return i, false
		}
		s := asString(v)
		if n.con.Kind != wire.ConNone && !n.con.OKString(s) {
			return i, false
		}
		*(*string)(p) = s
		return end + 1, true
	case dBool:
		if len(b)-i >= 4 && string(b[i:i+4]) == "true" {
			*(*bool)(p) = true
			return i + 4, true
		}
		if len(b)-i >= 5 && string(b[i:i+5]) == "false" {
			*(*bool)(p) = false
			return i + 5, true
		}
		return i, false
	case dInt:
		end := intEnd(b, i)
		if end < 0 {
			return i, false
		}
		v, err := strconv.ParseInt(asString(b[i:end]), 10, n.bits)
		if err != nil || (n.con.Kind != wire.ConNone && !n.con.OKInt(v)) {
			return i, false
		}
		switch n.bits {
		case 8:
			*(*int8)(p) = int8(v)
		case 16:
			*(*int16)(p) = int16(v)
		case 32:
			*(*int32)(p) = int32(v)
		default:
			*(*int64)(p) = v
		}
		return end, true
	case dUint:
		if b[i] == '-' {
			return i, false
		}
		end := intEnd(b, i)
		if end < 0 {
			return i, false
		}
		v, err := strconv.ParseUint(asString(b[i:end]), 10, n.bits)
		if err != nil {
			return i, false
		}
		switch n.bits {
		case 8:
			*(*uint8)(p) = uint8(v)
		case 16:
			*(*uint16)(p) = uint16(v)
		case 32:
			*(*uint32)(p) = uint32(v)
		default:
			*(*uint64)(p) = v
		}
		return end, true
	case dFloat:
		end := numberEnd(b, i)
		if end < 0 {
			return i, false
		}
		v, err := strconv.ParseFloat(asString(b[i:end]), n.bits)
		if err != nil {
			return i, false
		}
		if n.bits == 32 {
			v = float64(float32(v))
		}
		if n.con.Kind != wire.ConNone && !n.con.OKFloat(v) {
			return i, false
		}
		if n.bits == 32 {
			*(*float32)(p) = float32(v)
		} else {
			*(*float64)(p) = v
		}
		return end, true
	}
	return i, false
}

// skipSpace returns the index of the first byte at or after i that is not JSON whitespace.
func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// plainString returns the index of the quote that closes the string whose content starts at i,
// when every byte before it is printable ASCII and none is a backslash: then the content is the
// string's value, with nothing to unescape and nothing to check as UTF-8.
func plainString(b []byte, i int) (int, bool) {
	// A string shorter than eight bytes ends in its first word: found here, inlined.
	if i+8 <= len(b) {
		if m := specialIn(binary.LittleEndian.Uint64(b[i:])); m != 0 {
			if j := i + bits.TrailingZeros64(m)>>3; b[j] == '"' {
				return j, true
			}
			return 0, false
		}
		i += 8
	}
	if i = plainRun(b, i); i < len(b) && b[i] == '"' {
		return i, true
	}
	return 0, false
}

// intEnd returns the end of the integer -?(0|[1-9][0-9]*) at b[i:], or -1.
func intEnd(b []byte, i int) int {
	if i < len(b) && b[i] == '-' {
		i++
	}
	if i >= len(b) {
		return -1
	}
	if b[i] == '0' {
		return i + 1
	}
	if b[i] < '1' || b[i] > '9' {
		return -1
	}
	for i++; i < len(b) && b[i] >= '0' && b[i] <= '9'; i++ {
	}
	return i
}

// numberEnd returns the end of the JSON number at b[i:], fraction and exponent included, or -1.
func numberEnd(b []byte, i int) int {
	i = intEnd(b, i)
	if i < 0 {
		return -1
	}
	if i < len(b) && b[i] == '.' {
		j := i + 1
		for j < len(b) && b[j] >= '0' && b[j] <= '9' {
			j++
		}
		if j == i+1 {
			return -1
		}
		i = j
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		j := i + 1
		if j < len(b) && (b[j] == '+' || b[j] == '-') {
			j++
		}
		k := j
		for k < len(b) && b[k] >= '0' && b[k] <= '9' {
			k++
		}
		if k == j {
			return -1
		}
		i = k
	}
	return i
}
