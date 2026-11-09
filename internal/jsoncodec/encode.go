package jsoncodec

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

type ekind uint8

const (
	eBool ekind = iota + 1
	eInt
	eUint
	eFloat
	eString
	eUUID
	eTime
	eDate
	eStruct
	eSlice
	eArray
	eMap
	ePtr
	eOpt
	eNull
	eDelegate
	eUnion // a struct with a Discriminator method (union.go)
)

// enode is one node of an encoding plan.
type enode struct {
	kind ekind
	typ  reflect.Type
	bits int // int, uint, float: 8, 16, 32 or 64
	size uintptr

	elem   *enode // slice, array, map, ptr, opt, null
	arrLen int

	fields []efield // struct

	valOff, flagOff uintptr // opt, null

	variants []evariant // union
}

// evariant is one variant of a union: what is written before its members, `{"type":"join"`.
type evariant struct {
	head            []byte
	node            *enode
	valOff, flagOff uintptr
	tag, field      string // for a server-only variant, set only when this plan refuses one
}

type efield struct {
	name      []byte // `"wire_name":`, quoted once at Registration
	off       uintptr
	node      *enode
	typ       reflect.Type
	omitEmpty bool
	omitZero  bool
	zeroer    bool // the type has IsZero
	zeroerPtr bool // only *T has it, so the method could keep the pointer it is given
}

// Encoder is a compiled encoding plan for one type. It is safe for concurrent use.
type Encoder struct {
	root *enode
}

type ecompiler struct {
	opt  Options
	memo map[reflect.Type]*enode
}

// CompileEncoder builds the plan for t. The error is a Registration refusal reason naming
// the offending field path in Go terms.
func CompileEncoder(t reflect.Type, o Options) (*Encoder, error) {
	o = o.withDefaults()
	c := &ecompiler{opt: o, memo: map[reflect.Type]*enode{}}
	n, err := c.compile(t, typeName(t), false)
	if err != nil {
		return nil, err
	}
	return &Encoder{root: n}, nil
}

func (c *ecompiler) compile(t reflect.Type, path string, inField bool) (*enode, error) {
	if kind, elem, vOff, fOff := wire.PresenceOf(t); kind != wire.None {
		n := &enode{typ: t, valOff: vOff, flagOff: fOff}
		if kind == wire.Optional {
			if !inField {
				return nil, refusal(path, "an Opt can only be a field of a struct, it has no absent value elsewhere")
			}
			if k, _, _, _ := wire.PresenceOf(elem); k == wire.Optional {
				return nil, refusal(path, "an Opt of an Opt is not allowed")
			}
			n.kind = eOpt
		} else {
			if k, _, _, _ := wire.PresenceOf(elem); k != wire.None {
				return nil, refusal(path, "a Null can hold neither an Opt nor a Null, use Opt[Null[T]] for a field that may be absent or null")
			}
			n.kind = eNull
		}
		en, err := c.compile(elem, path, false)
		if err != nil {
			return nil, err
		}
		n.elem = en
		return n, nil
	}
	if n, ok := c.memo[t]; ok {
		return n, nil
	}
	switch t {
	case timeType:
		return &enode{kind: eTime, typ: t}, nil
	case uuidType:
		return &enode{kind: eUUID, typ: t}, nil
	case dateType:
		return &enode{kind: eDate, typ: t}, nil
	}
	if _, err := wire.FormatOf(t); err != nil {
		return nil, refusal(path, "%v", err)
	}
	if c.opt.opaque(t, Encoding) {
		return nil, c.opt.opaqueRefusal(path, t, Encoding)
	}
	if err := checkRawJSON(t, path); err != nil {
		return nil, err
	}
	if elem, ok := wire.RawJSONOf(t); ok {
		if _, err := c.compile(elem, path, false); err != nil {
			return nil, err
		}
	}
	if t.Kind() == reflect.Interface || hasMarshaler(t) || isByteSeq(t) {
		return &enode{kind: eDelegate, typ: t}, nil
	}
	if isUnion(t) {
		tag, vs, err := unionOf(t, c.opt, path)
		if err != nil {
			return nil, err
		}
		n := &enode{kind: eUnion, typ: t}
		c.memo[t] = n
		for _, v := range vs {
			vn, err := c.compile(v.typ, joinPath(path, goFieldName(v.goPath)), false)
			if err != nil {
				delete(c.memo, t)
				return nil, err
			}
			head, _ := jsontext.AppendQuote([]byte{'{'}, tag)
			head = append(head, ':')
			head, _ = jsontext.AppendQuote(head, v.tag)
			ev := evariant{head: head, node: vn, valOff: v.valOff, flagOff: v.flagOff}
			if v.serverOnly && !c.opt.ServerVariants {
				ev.tag, ev.field = v.tag, goFieldName(v.goPath)
			}
			n.variants = append(n.variants, ev)
		}
		return n, nil
	}
	n := &enode{typ: t}
	switch k := t.Kind(); k {
	case reflect.Bool:
		n.kind = eBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n.kind, n.bits = eInt, int(t.Size())*8
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n.kind, n.bits = eUint, int(t.Size())*8
	case reflect.Float32, reflect.Float64:
		n.kind, n.bits = eFloat, int(t.Size())*8
	case reflect.String:
		n.kind = eString
	case reflect.Pointer:
		n.kind = ePtr
		c.memo[t] = n
		en, err := c.compile(t.Elem(), path, false)
		if err != nil {
			delete(c.memo, t)
			return nil, err
		}
		n.elem = en
	case reflect.Struct:
		n.kind = eStruct
		c.memo[t] = n
		specs, err := collectFields(t, c.opt, typeName(t), false)
		if err != nil {
			delete(c.memo, t)
			return nil, err
		}
		for _, sp := range specs {
			fn, err := c.compile(sp.typ, joinPath(path, goFieldName(sp.goPath)), true)
			if err != nil {
				delete(c.memo, t)
				return nil, err
			}
			name, _ := jsontext.AppendQuote(nil, sp.name)
			name = append(name, ':')
			n.fields = append(n.fields, efield{
				name: name, off: sp.offset, node: fn, typ: sp.typ,
				omitEmpty: sp.omitEmpty, omitZero: sp.omitZero,
				zeroer:    reflect.PointerTo(sp.typ).Implements(isZeroerType),
				zeroerPtr: !sp.typ.Implements(isZeroerType) && reflect.PointerTo(sp.typ).Implements(isZeroerType),
			})
		}
	case reflect.Slice, reflect.Array:
		n.kind = eSlice
		if k == reflect.Array {
			n.kind, n.arrLen = eArray, t.Len()
		}
		c.memo[t] = n
		en, err := c.compile(t.Elem(), path+"[]", false)
		if err != nil {
			delete(c.memo, t)
			return nil, err
		}
		n.elem, n.size = en, t.Elem().Size()
	case reflect.Map:
		if t.Key().Kind() != reflect.String || hasOwnJSON(t.Key()) {
			return nil, refusal(path, "a map key must be a string type, %s is not", t.Key())
		}
		n.kind = eMap
		c.memo[t] = n
		en, err := c.compile(t.Elem(), path+"[key]", false)
		if err != nil {
			delete(c.memo, t)
			return nil, err
		}
		n.elem = en
	default:
		return nil, refusal(path, "a field of kind %s cannot be encoded", k)
	}
	return n, nil
}

// maxEncodeDepth stops a cyclic value (a pointer loop) from overflowing the stack.
const maxEncodeDepth = 1000

var errEncodeDepth = errors.New("jsoncodec: value nests too deeply to encode, is it cyclic?")

// Append encodes the value at src, a *T of the type the plan was compiled for, to dst and
// returns the extended slice. On an error dst is returned as it was passed.
//
// Semantics follow encoding/json/v2's defaults: a nil slice is [], a nil map is {}, nothing
// is HTML-escaped, floats and strings are written by the same routines. An empty Opt is
// left out, a Null that is null is written null, a nil pointer is null.
func (e *Encoder) Append(dst []byte, src unsafe.Pointer) ([]byte, error) {
	out, err := appendValue(dst, e.root, src, 0)
	if err != nil {
		return dst, err
	}
	return out, nil
}

func appendValue(dst []byte, n *enode, p unsafe.Pointer, depth int) ([]byte, error) {
	if depth > maxEncodeDepth {
		return dst, errEncodeDepth
	}
	switch n.kind {
	case eBool:
		if *(*bool)(p) {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case eInt:
		var v int64
		switch n.bits {
		case 8:
			v = int64(*(*int8)(p))
		case 16:
			v = int64(*(*int16)(p))
		case 32:
			v = int64(*(*int32)(p))
		default:
			v = *(*int64)(p)
		}
		return strconv.AppendInt(dst, v, 10), nil
	case eUint:
		var v uint64
		switch n.bits {
		case 8:
			v = uint64(*(*uint8)(p))
		case 16:
			v = uint64(*(*uint16)(p))
		case 32:
			v = uint64(*(*uint32)(p))
		default:
			v = *(*uint64)(p)
		}
		return strconv.AppendUint(dst, v, 10), nil
	case eFloat:
		if n.bits == 32 {
			return appendFloat(dst, float64(*(*float32)(p)), 32)
		}
		return appendFloat(dst, *(*float64)(p), 64)
	case eString:
		out, err := appendString(dst, *(*string)(p))
		if err != nil {
			return dst, errors.New("jsoncodec: a string is not valid UTF-8")
		}
		return out, nil
	case eUUID:
		dst = append(dst, '"')
		dst, _ = (*(*uuid.UUID)(p)).AppendText(dst)
		return append(dst, '"'), nil
	case eDate:
		dst = append(dst, '"')
		dst = (*(*wire.Date)(p)).AppendText(dst)
		return append(dst, '"'), nil
	case eTime:
		t := *(*time.Time)(p)
		dst = append(dst, '"')
		out, err := t.AppendText(dst)
		if err != nil {
			return dst[:len(dst)-1], err
		}
		return append(out, '"'), nil
	case eUnion:
		var set *evariant
		for i := range n.variants {
			if *(*bool)(unsafe.Add(p, n.variants[i].flagOff)) {
				if set != nil {
					return dst, fmt.Errorf("jsoncodec: the union %s has two variants set, %s and %s: set one", n.typ, set.head[bytesIndexColon(set.head)+1:], n.variants[i].head[bytesIndexColon(n.variants[i].head)+1:])
				}
				set = &n.variants[i]
			}
		}
		if set == nil {
			return dst, fmt.Errorf("jsoncodec: the union %s has no variant set: set the Opt of the one to send", n.typ)
		}
		if set.tag != "" { // only a plan that refuses server-only variants sets it, so no other pays for this
			return dst, &ServerOnlyError{Union: n.typ, Variant: set.tag, Field: set.field}
		}
		start := len(dst)
		dst = append(dst, set.head...)
		mark := len(dst)
		out, err := appendValue(dst, set.node, unsafe.Add(p, set.valOff), depth+1)
		if err != nil {
			return dst[:start], err
		}
		if len(out) == mark+2 { // the variant wrote {}: the head is the whole object
			return append(out[:mark], '}'), nil
		}
		out[mark] = ',' // the variant's own { becomes the comma after the head
		return out, nil
	case ePtr:
		q := *(*unsafe.Pointer)(p)
		if q == nil {
			return append(dst, "null"...), nil
		}
		return appendValue(dst, n.elem, q, depth+1)
	case eNull:
		if !*(*bool)(unsafe.Add(p, n.flagOff)) {
			return append(dst, "null"...), nil
		}
		return appendValue(dst, n.elem, unsafe.Add(p, n.valOff), depth+1)
	case eOpt:
		// Only reachable for an Opt that is set (the struct loop skips an empty one).
		return appendValue(dst, n.elem, unsafe.Add(p, n.valOff), depth+1)
	case eStruct:
		return appendStruct(dst, n, p, depth)
	case eSlice:
		h := (*sliceHeader)(p)
		return appendList(dst, n, h.data, h.len, depth)
	case eArray:
		return appendList(dst, n, p, n.arrLen, depth)
	case eMap:
		return appendMap(dst, n, p, depth)
	case eDelegate:
		return appendDelegate(dst, n, p)
	}
	panic("jsoncodec: unknown plan node")
}

func appendStruct(dst []byte, n *enode, p unsafe.Pointer, depth int) ([]byte, error) {
	start := len(dst)
	dst = append(dst, '{')
	first := true
	for i := range n.fields {
		f := &n.fields[i]
		fp := unsafe.Add(p, f.off)
		node := f.node
		if node.kind == eOpt {
			if !*(*bool)(unsafe.Add(fp, node.flagOff)) {
				continue
			}
		}
		if f.omitZero && isZero(f, fp) {
			continue
		}
		mark := len(dst)
		if !first {
			dst = append(dst, ',')
		}
		dst = append(dst, f.name...)
		valStart := len(dst)
		var err error
		dst, err = appendValue(dst, node, fp, depth+1)
		if err != nil {
			return dst[:start], err
		}
		if f.omitEmpty && emptyJSON(dst[valStart:]) {
			dst = dst[:mark]
			continue
		}
		first = false
	}
	return append(dst, '}'), nil
}

// emptyJSON is v2's omitempty test: the value encoded as null, "", {} or [].
func emptyJSON(b []byte) bool {
	switch string(b) {
	case "null", `""`, "{}", "[]":
		return true
	}
	return false
}

// isZero is v2's omitzero test: the type's IsZero method when it has one, else Go's zero
// value. It goes through reflect, which allocates nothing for a pointer to the field.
func isZero(f *efield, p unsafe.Pointer) bool {
	v := reflect.NewAt(f.typ, noescape(p))
	if f.zeroerPtr {
		// A pointer-receiver method could keep its receiver, and p may be on a stack
		// noescape hid from the compiler, so the method gets a copy (appendDelegate's rule).
		cp := reflect.New(f.typ)
		cp.Elem().Set(v.Elem())
		return cp.Interface().(interface{ IsZero() bool }).IsZero()
	}
	if f.zeroer {
		// A value-receiver method is called through a wrapper that copies the value.
		return v.Interface().(interface{ IsZero() bool }).IsZero()
	}
	return v.Elem().IsZero()
}

func appendList(dst []byte, n *enode, data unsafe.Pointer, count int, depth int) ([]byte, error) {
	start := len(dst)
	dst = append(dst, '[')
	for i := range count {
		if i > 0 {
			dst = append(dst, ',')
		}
		var err error
		dst, err = appendValue(dst, n.elem, unsafe.Add(data, uintptr(i)*n.size), depth+1)
		if err != nil {
			return dst[:start], err
		}
	}
	return append(dst, ']'), nil
}

// appendMap writes the members in key order, so equal maps encode to equal bytes. v2's
// default order is unspecified; sorting costs allocations, and a map is already off the
// zero-allocation path.
func appendMap(dst []byte, n *enode, p unsafe.Pointer, depth int) ([]byte, error) {
	mv := reflect.NewAt(n.typ, noescape(p)).Elem()
	if mv.Len() == 0 {
		return append(dst, "{}"...), nil
	}
	keys := mv.MapKeys()
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		x, y := a.String(), b.String()
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	start := len(dst)
	tmp := reflect.New(n.typ.Elem())
	dst = append(dst, '{')
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		var err error
		dst, err = appendString(dst, k.String())
		if err != nil {
			return dst[:start], errors.New("jsoncodec: a map key is not valid UTF-8")
		}
		dst = append(dst, ':')
		tmp.Elem().Set(mv.MapIndex(k))
		dst, err = appendValue(dst, n.elem, tmp.UnsafePointer(), depth+1)
		if err != nil {
			return dst[:start], err
		}
	}
	return append(dst, '}'), nil
}

// appendDelegate hands the value to encoding/json/v2, which runs the type's own methods (or
// the dynamic type of an interface). The value is copied first so that p is never given to
// code outside this package.
func appendDelegate(dst []byte, n *enode, p unsafe.Pointer) ([]byte, error) {
	cp := reflect.New(n.typ)
	cp.Elem().Set(reflect.NewAt(n.typ, noescape(p)).Elem())
	var arg any = cp.Interface() // a pointer, so pointer-receiver methods are found
	if n.typ.Kind() == reflect.Interface {
		arg = cp.Elem().Interface() // the dynamic value; nil is null
	}
	out, err := jsonv2.Marshal(arg)
	if err != nil {
		return dst, fmt.Errorf("jsoncodec: %s: %w", n.typ, err)
	}
	return append(dst, out...), nil
}

// appendFloat writes f as v2 does: the shortest digits that read back, 'f' notation unless
// the magnitude is under 1e-6 or at least 1e21 (then 'e' with the exponent's leading zero
// removed). It is the rule of ECMAScript's Number-to-String, which RFC 8785 adopted.
func appendFloat(dst []byte, f float64, bits int) ([]byte, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dst, errors.New("jsoncodec: NaN and Inf cannot be written as JSON")
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) || bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, format, -1, bits)
	if format == 'e' {
		// Clean up e-09 to e-9.
		n := len(dst)
		if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, nil
}

// bytesIndexColon is the index of the colon in a union variant's head, so an error can quote the tag.
func bytesIndexColon(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == ':' {
			return i
		}
	}
	return -1
}
