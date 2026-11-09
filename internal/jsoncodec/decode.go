package jsoncodec

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

type dkind uint8

const (
	dBool dkind = iota + 1
	dInt
	dUint
	dFloat
	dString
	dEnum
	dUUID
	dTime
	dDate
	dStruct
	dSlice
	dArray
	dMap
	dOpt
	dNull
	dDelegate
	dChecked
	dPtr   // only in an Undescribed plan: null and an absent member are nil
	dUnion // a struct with a Discriminator method (union.go)
)

// dnode is one node of a decoding plan.
type dnode struct {
	kind dkind
	typ  reflect.Type
	size uintptr // element size, for slices and arrays

	bits     int    // int, uint, float: 8, 16, 32 or 64
	rangeMsg string // int, uint: the out-of-range sentence

	con wire.Constraint // string, int: a constraint type's check (ADR 0021); Kind is ConNone otherwise

	vals    []string // enum
	enumMsg string
	delMsg  string // delegate: the sentence for a value its own method refuses
	delRule string

	check func(unsafe.Pointer) error // checked: the type's Check, run after elem decoded

	acceptsNull bool // delegate: json.RawMessage, jsontext.Value and RawJSON take null as a value (ADR 0084)

	items   *wire.Items   // slice: the type's Items and Unique rules (ADR 0021), nil when it has neither
	members *wire.Members // map: the type's Members bound (ADR 0021), nil when it has none

	elem   *dnode // slice, array, map, opt, null
	arrLen int
	arrMsg string

	fields []dfield // struct
	byName map[string]int

	valOff, flagOff uintptr // opt, null

	tagName  string     // union: the discriminator member
	variants []dvariant // union
	tagMsg   string     // union: the sentence for a tag that names no variant
}

type dvariant struct {
	tag             string
	node            *dnode
	valOff, flagOff uintptr
}

type dfield struct {
	name string
	off  uintptr
	node *dnode
}

// Decoder is a compiled decoding plan for one type. It is safe for concurrent use.
type Decoder struct {
	root *dnode
	opt  Options

	fast     bool   // the root is a flat struct the fast path decodes (fast.go)
	required uint64 // its members that are not Opt, for the fast path
}

type dcompiler struct {
	opt  Options
	memo map[reflect.Type]*dnode
}

// CompileDecoder builds the plan for t. The error is a Registration refusal reason naming the
// offending field path in Go terms.
func CompileDecoder(t reflect.Type, o Options) (*Decoder, error) {
	o = o.withDefaults()
	c := &dcompiler{opt: o, memo: map[reflect.Type]*dnode{}}
	n, err := c.compile(t, typeName(t), false)
	if err != nil {
		return nil, err
	}
	d := &Decoder{root: n, opt: o}
	d.required, d.fast = flatFields(n)
	if o.Undescribed {
		d.required = 0 // what the codec left out (an empty Opt, an omitempty member) is the zero value
	}
	return d, nil
}

// compile builds the node for t. inField says t is the type of a struct field, the only
// place an Opt can sit: an Opt elsewhere (a slice element, a map value) would have no
// "absent" to mean.
func (c *dcompiler) compile(t reflect.Type, path string, inField bool) (*dnode, error) {
	n, err := c.compileValue(t, path, inField)
	if err != nil {
		return nil, err
	}
	check, err := wire.CheckOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	if check != nil {
		return &dnode{kind: dChecked, typ: t, elem: n, check: check}, nil
	}
	return n, nil
}

func (c *dcompiler) compileValue(t reflect.Type, path string, inField bool) (*dnode, error) {
	if kind, elem, vOff, fOff := wire.PresenceOf(t); kind != wire.None {
		return c.compilePresence(t, kind, elem, vOff, fOff, path, inField)
	}
	if n, ok := c.memo[t]; ok {
		return n, nil
	}
	if c.opt.Binary != nil && c.opt.Binary(t) {
		return &dnode{kind: dDelegate, typ: t, delMsg: msgValid}, nil
	}
	switch t {
	case timeType:
		return &dnode{kind: dTime, typ: t}, nil
	case uuidType:
		return &dnode{kind: dUUID, typ: t}, nil
	case dateType:
		return &dnode{kind: dDate, typ: t}, nil
	}
	if t.Kind() == reflect.Pointer {
		if !c.opt.Undescribed {
			return nil, refusal(path, "a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]")
		}
		// A broker's payload is what the codec wrote from an Out, where a nil pointer is null
		// or left out: both read back as nil, which is all the Out could have said.
		n := &dnode{kind: dPtr, typ: t}
		c.memo[t] = n
		el, err := c.compileValue(t.Elem(), path, inField)
		if err != nil {
			return nil, err
		}
		n.elem = el
		return n, nil
	}
	if t.Kind() == reflect.Interface {
		return nil, refusal(path, "a field of interface type cannot be decoded")
	}
	if wire.CSVOf(t) {
		return nil, refusal(path, "%s is a comma-separated list, which only a Query or a Header section reads; in JSON a list is an array, so use []%s", wire.PublicName(t), wire.PublicName(t.Elem()))
	}
	fr, err := wire.FormatRuleOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	items, hasItems, err := wire.ItemsOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	members, hasMembers, err := wire.MembersOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	pat, err := wire.PatternOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	if c.opt.opaque(t, Decoding) {
		return nil, c.opt.opaqueRefusal(path, t, Decoding)
	}
	if hasUnmarshaler(t) || isByteSeq(t) {
		if hasItems {
			return nil, refusal(path, "%s has %s, but it decodes itself, so its items are not counted", t, wire.ItemsRuleName(items))
		}
		if hasMembers {
			return nil, refusal(path, "%s has a Members method, but it decodes itself, so its members are not counted", t)
		}
		if pat != nil {
			return nil, refusal(path, "%s has a Pattern method, but it decodes itself, so its pattern would not be checked", t)
		}
		if fr != nil {
			return nil, refusal(path, "%s has a Format method naming %q, which the framework checks, but it decodes itself, so its format would not be checked: drop the Format or check in its UnmarshalText", t, fr.Name)
		}
		dn := &dnode{kind: dDelegate, typ: t, delMsg: msgValid}
		if err := checkRawJSON(t, path); err != nil {
			return nil, err
		}
		if elem, ok := wire.RawJSONOf(t); ok {
			if _, err := c.compileValue(elem, path, false); err != nil {
				return nil, err
			}
		}
		dn.acceptsNull = t == rawMessageType || t == jsontextValue || isRawJSON(t)
		if hasUnmarshaler(t) {
			dn.delMsg = wire.UnmarshalError(t).Error()
		}
		if f, ok := c.opt.Formats[t]; ok {
			dn.delMsg = wire.FormatError(f).Error()
		}
		return dn, nil
	}
	if isUnion(t) {
		tag, vs, err := unionOf(t, c.opt, path)
		if err != nil {
			return nil, err
		}
		n := &dnode{kind: dUnion, typ: t, tagName: tag}
		c.memo[t] = n
		kept := vs[:0:0]
		for _, v := range vs {
			if v.serverOnly && !c.opt.ServerVariants {
				continue // a client cannot send a variant only the server carries
			}
			vn, err := c.compile(v.typ, joinPath(path, goFieldName(v.goPath)), false)
			if err != nil {
				delete(c.memo, t)
				return nil, err
			}
			kept = append(kept, v)
			n.variants = append(n.variants, dvariant{tag: v.tag, node: vn, valOff: v.valOff, flagOff: v.flagOff})
		}
		n.tagMsg = msgEnumPrefix + unionTags(kept)
		return n, nil
	}
	n := &dnode{typ: t}
	con, err := wire.ConstraintOf(t)
	if err != nil {
		return nil, refusal(path, "%v", err)
	}
	n.con = con
	switch k := t.Kind(); k {
	case reflect.Bool:
		n.kind = dBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n.kind, n.bits = dInt, int(t.Size())*8
		lo, hi := int64(math.MinInt64)>>(64-n.bits), int64(math.MaxInt64)>>(64-n.bits)
		n.rangeMsg = fmt.Sprintf("expected an integer between %d and %d", lo, hi)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n.kind, n.bits = dUint, int(t.Size())*8
		n.rangeMsg = fmt.Sprintf("expected an integer between 0 and %d", uint64(math.MaxUint64)>>(64-n.bits))
	case reflect.Float32, reflect.Float64:
		n.kind, n.bits = dFloat, int(t.Size())*8
	case reflect.String:
		vals, has, err := valuesOf(t)
		if err != nil {
			return nil, refusal(path, "%v", err)
		}
		n.kind = dString
		if has {
			n.kind, n.vals = dEnum, vals
			n.enumMsg = msgEnumPrefix + strings.Join(vals, ", ")
		}
	case reflect.Struct:
		c.memo[t] = n // before the fields, so a recursive type finds itself
		n.kind = dStruct
		specs, err := collectFields(t, c.opt, typeName(t), true)
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
			n.fields = append(n.fields, dfield{name: sp.name, off: sp.offset, node: fn})
		}
		if len(n.fields) > 8 {
			n.byName = make(map[string]int, len(n.fields))
			for i, f := range n.fields {
				n.byName[f.name] = i
			}
		}
	case reflect.Slice, reflect.Array:
		n.kind = dSlice
		if hasItems {
			n.items = &items
		}
		if k == reflect.Array {
			n.kind, n.arrLen = dArray, t.Len()
			n.arrMsg = fmt.Sprintf("expected an array of %d items", n.arrLen)
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
		if kc, _ := wire.ConstraintOf(t.Key()); kc.Kind != wire.ConNone {
			return nil, refusal(path, "a map key cannot be %s: a key is not checked against a constraint, so the type would promise what nothing enforces", t.Key())
		}
		n.kind = dMap
		if hasMembers {
			n.members = &members
		}
		c.memo[t] = n
		en, err := c.compile(t.Elem(), path+"[key]", false)
		if err != nil {
			delete(c.memo, t)
			return nil, err
		}
		n.elem = en
	default:
		return nil, refusal(path, "a field of kind %s cannot be decoded", k)
	}
	return n, nil
}

// goFieldName takes the last element of a "Type.A.B" path.
func goFieldName(goPath string) string {
	if i := strings.LastIndexByte(goPath, '.'); i >= 0 {
		return goPath[i+1:]
	}
	return goPath
}

func (c *dcompiler) compilePresence(t reflect.Type, kind wire.PresenceKind, elem reflect.Type, vOff, fOff uintptr, path string, inField bool) (*dnode, error) {
	n := &dnode{typ: t, valOff: vOff, flagOff: fOff}
	if kind == wire.Optional {
		if !inField {
			return nil, refusal(path, "an Opt can only be a field of a struct, it has no absent value elsewhere")
		}
		if k, _, _, _ := wire.PresenceOf(elem); k == wire.Optional {
			return nil, refusal(path, "an Opt of an Opt is not allowed")
		}
		n.kind = dOpt
	} else {
		if k, _, _, _ := wire.PresenceOf(elem); k != wire.None {
			return nil, refusal(path, "a Null can hold neither an Opt nor a Null, use Opt[Null[T]] for a field that may be absent or null")
		}
		n.kind = dNull
	}
	en, err := c.compile(elem, path, false)
	if err != nil {
		return nil, err
	}
	n.elem = en
	return n, nil
}

// state is the pooled tokenizer state plus the scratch of one Decode call. It never reaches
// user code (ADR 0008): strings handed out are copies in the slab or slices of the body.
type state struct {
	dec  jsontext.Decoder
	buf  bytes.Buffer
	path []seg
	seen []uint64

	scratch []byte
	locBuf  []byte

	// per call
	plan  *Decoder
	body  []byte
	slab  *wire.Slab
	errs  *Errors
	depth int

	// skipName is the member a union's variant leaves to the union, at the depth of its object.
	skipName  string
	skipDepth int
}

type seg struct {
	name string
	idx  int
	isIx bool
}

var statePool = sync.Pool{New: func() any {
	s := new(state)
	s.dec.Reset(emptyReader)
	return s
}}

var emptyReader = bytes.NewReader(nil)

// emptyBase is where a non-nil empty slice points.
var emptyBase struct{}

// Decode decodes body into the value at dst, a *T of the type the plan was compiled for.
// body must stay unmodified for as long as the decoded value is used: strings without
// escapes point into it. Semantic errors are appended to errs and decoding goes on with the
// next value; the returned error is non-nil only for a syntax error (*SyntaxError) or a
// body nested over MaxDepth (ErrDepth), which end decoding.
//
// After a fatal error dst holds a partial value and must not be used.
func (d *Decoder) Decode(body []byte, dst unsafe.Pointer, slab *wire.Slab, errs *Errors) error {
	if d.fast && d.decodeFast(body, dst) {
		return nil
	}
	return d.decodeTokens(body, dst, slab, errs)
}

// decodeTokens is Decode through the tokenizer: every body, and the only path that reports an
// error.
func (d *Decoder) decodeTokens(body []byte, dst unsafe.Pointer, slab *wire.Slab, errs *Errors) error {
	s := statePool.Get().(*state)
	s.plan, s.body, s.slab, s.errs, s.depth = d, body, slab, errs, 0
	s.buf = *bytes.NewBuffer(body)
	s.dec.Reset(&s.buf)
	s.path = s.path[:0]
	s.seen = s.seen[:0]
	_, err := s.value(d.root, dst)
	if err == nil {
		// Nothing but whitespace may follow the value.
		if _, e := s.dec.ReadValue(); e != io.EOF {
			err = s.wrap(e)
		}
	}
	// Drop every reference to the request before the state goes back to the pool.
	s.plan, s.body, s.slab, s.errs = nil, nil, nil, nil
	s.buf = bytes.Buffer{}
	s.dec.Reset(emptyReader)
	statePool.Put(s)
	return err
}

// wrap turns a tokenizer error into a SyntaxError with a fixed message.
func (s *state) wrap(err error) error {
	if err == nil {
		err = errors.New("unexpected token")
	}
	var se *SyntaxError
	if errors.As(err, &se) {
		return err
	}
	msg := "invalid JSON"
	switch {
	case errors.Is(err, jsontext.ErrDuplicateName):
		msg = "duplicate member name"
	case errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		msg = "unexpected end of JSON"
	}
	return &SyntaxError{Offset: s.dec.InputOffset(), msg: msg, cause: err}
}

// syntax fetches the pending tokenizer error after PeekKind reported an invalid kind.
func (s *state) syntax() error {
	_, err := s.dec.ReadValue()
	return s.wrap(err)
}

// fail records a semantic error at the current path plus extra, unless MaxErrors is reached.
func (s *state) fail(extra, msg string) { s.failRule(extra, msg, "", 0, 0) }

// failRule is fail with the rule code of ADR 0012 and, for "length" and "range", its bounds.
func (s *state) failRule(extra, msg, rule string, lo, hi int64) {
	if len(s.errs.List) >= s.plan.opt.MaxErrors {
		s.errs.Truncated = true
		return
	}
	s.errs.List = append(s.errs.List, FieldError{Location: s.loc(extra), Message: msg, Rule: rule, Min: lo, Max: hi})
}

// failInt records the failure of an integer constraint for v: its bounds, or its MultipleOf.
func (s *state) failInt(c *wire.Constraint, v int64) {
	if !c.InRangeInt(v) {
		lo, hi, _ := c.Bounds()
		s.failRule("", c.Message, c.Rule, lo, hi)
		return
	}
	s.failRule("", c.MulMessage, wire.RuleMultiple, c.MulInt, 0)
}

// failString records the failure of a string constraint: its length, or its pattern.
func (s *state) failString(c *wire.Constraint, str string) {
	msg, rule, lo, hi, format := c.FailString(str)
	if format != "" {
		s.appendError(FieldError{Location: s.loc(""), Message: msg, Rule: rule, Format: format})
		return
	}
	s.failRule("", msg, rule, lo, hi)
}

// failFloat records the failure of a float type's bounds or MultipleOf for v.
func (s *state) failFloat(c *wire.Constraint, v float64) {
	if !c.InRangeFloat(v) {
		lo, hi := c.FloatBounds()
		s.appendError(FieldError{Location: s.loc(""), Message: c.Message, Rule: c.Rule, Float: true, MinF: lo, MaxF: hi, ExMin: c.ExMin, ExMax: c.ExMax})
		return
	}
	s.appendError(FieldError{Location: s.loc(""), Message: c.MulMessage, Rule: wire.RuleMultiple, Float: true, MinF: c.MulF})
}

// appendError records e within the cap of MaxErrors.
func (s *state) appendError(e FieldError) {
	if len(s.errs.List) >= s.plan.opt.MaxErrors {
		s.errs.Truncated = true
		return
	}
	s.errs.List = append(s.errs.List, e)
}

const maxEchoedName = 64

func (s *state) loc(extra string) string {
	b := s.locBuf[:0]
	for _, g := range s.path {
		if g.isIx {
			b = append(b, '[')
			b = strconv.AppendInt(b, int64(g.idx), 10)
			b = append(b, ']')
			continue
		}
		if len(b) > 0 {
			b = append(b, '.')
		}
		b = append(b, g.name...)
	}
	if extra != "" {
		if len(b) > 0 {
			b = append(b, '.')
		}
		b = append(b, extra...)
	}
	s.locBuf = b
	return string(b)
}

// clipName bounds a client-supplied member name echoed in a location (ADR 0012), cutting on
// a rune boundary.
func clipName(n string) string {
	if len(n) <= maxEchoedName {
		return n
	}
	i := maxEchoedName
	for i > 0 && n[i]&0xC0 == 0x80 {
		i--
	}
	return n[:i]
}

// scalar reads the next value, of any kind (an object or array included), and returns its
// bytes inside body.
func (s *state) scalar() ([]byte, error) {
	v, err := s.dec.ReadValue()
	if err != nil {
		return nil, s.wrap(err)
	}
	end := int(s.dec.InputOffset())
	return s.body[end-len(v) : end], nil
}

// skip consumes the next value of any kind. Nesting while skipping counts against MaxDepth
// like nesting that is decoded.
func (s *state) skip() error {
	k := s.dec.PeekKind()
	if k != '{' && k != '[' {
		if k == 0 {
			return s.syntax()
		}
		if _, err := s.dec.ReadValue(); err != nil {
			return s.wrap(err)
		}
		return nil
	}
	level := 0
	for {
		switch s.dec.PeekKind() {
		case 0:
			return s.syntax()
		case '{', '[':
			level++
			if s.depth+level > s.plan.opt.MaxDepth {
				return ErrDepth
			}
			s.dec.ReadToken()
		case '}', ']':
			level--
			s.dec.ReadToken()
			if level == 0 {
				return nil
			}
		default:
			if _, err := s.dec.ReadValue(); err != nil {
				return s.wrap(err)
			}
		}
	}
}

// mismatch records a wrong-kind error and skips the value.
func (s *state) mismatch(msg string) (bool, error) {
	s.failRule("", msg, wire.RuleType, 0, 0)
	return false, s.skip()
}

// text returns the unescaped bytes of the string value raw (quotes included), and whether
// they are a slice of the body.
func (s *state) text(raw []byte) (b []byte, inBody bool) {
	inner := raw[1 : len(raw)-1]
	if bytes.IndexByte(inner, '\\') < 0 {
		return inner, true
	}
	s.scratch, _ = jsontext.AppendUnquote(s.scratch[:0], raw)
	return s.scratch, false
}

// str makes a Go string of text's result: a view of the body when it is one, else a copy in
// the slab.
func (s *state) str(b []byte, inBody bool) string {
	if len(b) == 0 {
		return ""
	}
	if inBody {
		return unsafe.String(unsafe.SliceData(b), len(b))
	}
	return s.slab.Copy(b)
}

func asString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// value decodes the next JSON value into dst according to n. It reports whether the value
// was stored; a semantic error is recorded and gives false with a nil error.
func (s *state) value(n *dnode, dst unsafe.Pointer) (bool, error) {
	k := s.dec.PeekKind()
	if k == 0 {
		return false, s.syntax()
	}
	if k == 'n' && n.kind == dPtr {
		if _, err := s.dec.ReadValue(); err != nil {
			return false, s.wrap(err)
		}
		*(*unsafe.Pointer)(dst) = nil
		return true, nil
	}
	if k == 'n' && n.kind != dNull && n.kind != dOpt && !n.acceptsNull {
		if _, err := s.dec.ReadValue(); err != nil {
			return false, s.wrap(err)
		}
		s.fail("", msgNull)
		return false, nil
	}
	switch n.kind {
	case dUnion:
		return s.union(n, k, dst)
	case dPtr:
		p := reflect.New(n.typ.Elem()).UnsafePointer()
		ok, err := s.value(n.elem, p)
		if ok {
			*(*unsafe.Pointer)(dst) = p
		}
		return ok, err
	case dChecked:
		ok, err := s.value(n.elem, dst)
		if ok {
			if e := n.check(noescape(dst)); e != nil {
				s.failRule("", e.Error(), wire.RuleCheck, 0, 0)
				return false, nil
			}
		}
		return ok, err
	case dOpt:
		ok, err := s.value(n.elem, unsafe.Add(dst, n.valOff))
		if ok {
			*(*bool)(unsafe.Add(dst, n.flagOff)) = true
		}
		return ok, err
	case dNull:
		if k == 'n' {
			if _, err := s.dec.ReadValue(); err != nil {
				return false, s.wrap(err)
			}
			*(*bool)(unsafe.Add(dst, n.flagOff)) = false
			return true, nil
		}
		ok, err := s.value(n.elem, unsafe.Add(dst, n.valOff))
		if ok {
			*(*bool)(unsafe.Add(dst, n.flagOff)) = true
		}
		return ok, err
	case dBool:
		if k != 't' && k != 'f' {
			return s.mismatch(msgBool)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		*(*bool)(dst) = raw[0] == 't'
		return true, nil
	case dInt, dUint, dFloat:
		return s.number(n, k, dst)
	case dString:
		if k != '"' {
			return s.mismatch(msgString)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		b, inBody := s.text(raw)
		str := s.str(b, inBody)
		if n.con.Kind != wire.ConNone && !n.con.OKString(str) {
			s.failString(&n.con, str)
			return false, nil
		}
		*(*string)(dst) = str
		return true, nil
	case dEnum:
		if k != '"' {
			return s.mismatch(n.enumMsg)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		b, _ := s.text(raw)
		for _, v := range n.vals {
			if string(b) == v {
				// The listed string, not a view of the body: it is static.
				*(*string)(dst) = v
				return true, nil
			}
		}
		s.failRule("", n.enumMsg, wire.RuleValues, 0, 0)
		return false, nil
	case dUUID:
		if k != '"' {
			return s.mismatch(msgUUID)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		b, _ := s.text(raw)
		var u uuid.UUID
		if u.UnmarshalText(b) != nil {
			s.failRule("", msgUUID, wire.RuleType, 0, 0)
			return false, nil
		}
		*(*uuid.UUID)(dst) = u
		return true, nil
	case dDate:
		if k != '"' {
			return s.mismatch(msgDate)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		b, _ := s.text(raw)
		d, err := wire.ParseDate(asString(b))
		if err != nil {
			s.failRule("", msgDate, wire.RuleType, 0, 0)
			return false, nil
		}
		*(*wire.Date)(dst) = d
		return true, nil
	case dTime:
		if k != '"' {
			return s.mismatch(msgTime)
		}
		raw, err := s.scalar()
		if err != nil {
			return false, err
		}
		b, _ := s.text(raw)
		t, ok := parseRFC3339(b)
		if !ok {
			s.failRule("", msgTime, wire.RuleType, 0, 0)
			return false, nil
		}
		*(*time.Time)(dst) = t
		return true, nil
	case dStruct:
		return s.object(n, k, dst)
	case dSlice:
		return s.slice(n, k, dst)
	case dArray:
		return s.array(n, k, dst)
	case dMap:
		return s.mapValue(n, k, dst)
	case dDelegate:
		return s.delegate(n, dst)
	}
	panic("jsoncodec: unknown plan node")
}

func (s *state) number(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '0' {
		msg := msgNumber
		switch n.kind {
		case dInt:
			msg = msgInteger
		case dUint:
			msg = msgNonNegInt
		}
		return s.mismatch(msg)
	}
	raw, err := s.scalar()
	if err != nil {
		return false, err
	}
	text := asString(raw)
	switch n.kind {
	case dInt:
		v, err := strconv.ParseInt(text, 10, n.bits)
		if err != nil {
			s.failNum(err, n.rangeMsg, msgInteger)
			return false, nil
		}
		if n.con.Kind != wire.ConNone && !n.con.OKInt(v) {
			s.failInt(&n.con, v)
			return false, nil
		}
		switch n.bits {
		case 8:
			*(*int8)(dst) = int8(v)
		case 16:
			*(*int16)(dst) = int16(v)
		case 32:
			*(*int32)(dst) = int32(v)
		default:
			*(*int64)(dst) = v
		}
	case dUint:
		v, err := strconv.ParseUint(text, 10, n.bits)
		if err != nil {
			s.failNum(err, n.rangeMsg, msgNonNegInt)
			return false, nil
		}
		switch n.bits {
		case 8:
			*(*uint8)(dst) = uint8(v)
		case 16:
			*(*uint16)(dst) = uint16(v)
		case 32:
			*(*uint32)(dst) = uint32(v)
		default:
			*(*uint64)(dst) = v
		}
	default:
		v, err := strconv.ParseFloat(text, n.bits)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			s.failRule("", msgNumber, wire.RuleType, 0, 0)
			return false, nil
		}
		if n.bits == 32 {
			v = float64(float32(v))
		}
		if n.con.Kind != wire.ConNone && !n.con.OKFloat(v) {
			s.failFloat(&n.con, v)
			return false, nil
		}
		if n.bits == 32 {
			*(*float32)(dst) = float32(v)
		} else {
			*(*float64)(dst) = v
		}
	}
	return true, nil
}

// numErr picks the sentence for a failed integer parse: out of range, or not an integer.
// failNum records a number that did not parse: the type's range sentence, which has no rule, when
// it parsed but was too large, and a "type" error when it was not an integer at all.
func (s *state) failNum(err error, rangeMsg, other string) {
	if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
		s.fail("", rangeMsg)
		return
	}
	s.failRule("", other, wire.RuleType, 0, 0)
}

func (s *state) enter() error {
	s.depth++
	if s.depth > s.plan.opt.MaxDepth {
		return ErrDepth
	}
	return nil
}

func (s *state) object(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '{' {
		return s.mismatch(msgObject)
	}
	if err := s.enter(); err != nil {
		return false, err
	}
	s.dec.ReadToken()
	words := (len(n.fields) + 63) / 64
	base := len(s.seen)
	for range words {
		s.seen = append(s.seen, 0)
	}
	for {
		nk := s.dec.PeekKind()
		if nk == '}' {
			s.dec.ReadToken()
			break
		}
		if nk == 0 {
			return false, s.syntax()
		}
		rawName, err := s.scalar()
		if err != nil {
			return false, err
		}
		nb, _ := s.text(rawName)
		fi := n.lookup(nb)
		if fi < 0 && s.skipName != "" && s.depth == s.skipDepth && string(nb) == s.skipName {
			if err := s.skip(); err != nil { // a union's discriminator, read by the union
				return false, err
			}
			continue
		}
		if fi < 0 {
			if !s.plan.opt.AllowUnknown {
				s.fail(clipName(string(nb)), msgUnknown)
			}
			if err := s.skip(); err != nil {
				return false, err
			}
			continue
		}
		s.seen[base+fi/64] |= 1 << (fi % 64)
		f := &n.fields[fi]
		s.path = append(s.path, seg{name: f.name})
		_, err = s.value(f.node, unsafe.Add(dst, f.off))
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return false, err
		}
	}
	for i := range n.fields {
		f := &n.fields[i]
		if s.seen[base+i/64]&(1<<(i%64)) == 0 && f.node.kind != dOpt && !s.plan.opt.Undescribed {
			s.failRule(f.name, msgRequired, wire.RuleRequired, 0, 0)
		}
	}
	s.seen = s.seen[:base]
	s.depth--
	return true, nil
}

func (n *dnode) lookup(name []byte) int {
	if n.byName != nil {
		if i, ok := n.byName[string(name)]; ok {
			return i
		}
		return -1
	}
	for i := range n.fields {
		if n.fields[i].name == string(name) {
			return i
		}
	}
	return -1
}

type sliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func (s *state) slice(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '[' {
		return s.mismatch(msgArray)
	}
	if err := s.enter(); err != nil {
		return false, err
	}
	s.dec.ReadToken()
	hdr := (*sliceHeader)(dst)
	sv := reflect.NewAt(n.typ, noescape(dst)).Elem()
	cnt, over, badItem := 0, false, false
	for {
		ek := s.dec.PeekKind()
		if ek == ']' {
			s.dec.ReadToken()
			break
		}
		if ek == 0 {
			return false, s.syntax()
		}
		if n.items != nil && cnt >= n.items.Max {
			// Past the most the type allows: the rest is skipped unstored, so a hostile list
			// costs no memory beyond the bound and the answer is the same 400.
			over = true
			if err := s.skip(); err != nil {
				return false, err
			}
			continue
		}
		if cnt == hdr.cap {
			// Double, and start at 4, so three small elements cost one allocation.
			sv.SetLen(cnt)
			sv.Grow(max(cnt, 4))
		}
		sv.SetLen(cnt + 1)
		elem := unsafe.Add(hdr.data, uintptr(cnt)*n.size)
		sv.Index(cnt).SetZero() // a reused backing array holds the last request's element
		s.path = append(s.path, seg{idx: cnt, isIx: true})
		ok, err := s.value(n.elem, elem)
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return false, err
		}
		badItem = badItem || !ok
		cnt++
	}
	if hdr.data == nil {
		// [] is an empty slice, not nil. reflect.MakeSlice would allocate the header it returns;
		// pointing at a zero-size variable is what make([]T, 0) does (ADR 0003: an empty list
		// costs nothing).
		hdr.data = unsafe.Pointer(&emptyBase)
	}
	hdr.len = cnt
	s.depth--
	if n.items != nil && (over || cnt < n.items.Min) {
		s.failRule("", n.items.Message, wire.RuleItems, int64(n.items.Min), int64(n.items.Max))
		return false, nil
	}
	if n.items != nil && n.items.Dup != nil && !badItem {
		// Compared only when every item decoded, since a failed one is left as zero and would
		// look like a repeat of another zero. The failure is at the first item that repeats.
		if i := n.items.Dup(hdr.data, cnt); i >= 0 {
			s.path = append(s.path, seg{idx: i, isIx: true})
			s.failRule("", wire.UniqueMessage, wire.RuleUnique, 0, 0)
			s.path = s.path[:len(s.path)-1]
			return false, nil
		}
	}
	return true, nil
}

func (s *state) array(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '[' {
		return s.mismatch(msgArray)
	}
	if err := s.enter(); err != nil {
		return false, err
	}
	s.dec.ReadToken()
	cnt, bad := 0, false
	for {
		ek := s.dec.PeekKind()
		if ek == ']' {
			s.dec.ReadToken()
			break
		}
		if ek == 0 {
			return false, s.syntax()
		}
		if cnt >= n.arrLen {
			bad = true
			if err := s.skip(); err != nil {
				return false, err
			}
			cnt++
			continue
		}
		s.path = append(s.path, seg{idx: cnt, isIx: true})
		_, err := s.value(n.elem, unsafe.Add(dst, uintptr(cnt)*n.size))
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return false, err
		}
		cnt++
	}
	s.depth--
	if bad || cnt != n.arrLen {
		s.fail("", n.arrMsg)
		return false, nil
	}
	return true, nil
}

func (s *state) mapValue(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '{' {
		return s.mismatch(msgObject)
	}
	if err := s.enter(); err != nil {
		return false, err
	}
	s.dec.ReadToken()
	mv := reflect.NewAt(n.typ, noescape(dst)).Elem()
	if mv.IsNil() {
		mv.Set(reflect.MakeMap(n.typ))
	}
	kt, et := n.typ.Key(), n.typ.Elem()
	cnt, over := 0, false
	for {
		nk := s.dec.PeekKind()
		if nk == '}' {
			s.dec.ReadToken()
			break
		}
		if nk == 0 {
			return false, s.syntax()
		}
		rawName, err := s.scalar()
		if err != nil {
			return false, err
		}
		if n.members != nil && cnt >= n.members.Max {
			// Past the most the type allows: the member's value is skipped unstored, so a
			// hostile object costs no memory beyond the bound and the answer is the same 400.
			over = true
			if err := s.skip(); err != nil {
				return false, err
			}
			continue
		}
		cnt++
		nb, inBody := s.text(rawName)
		key := s.str(nb, inBody)
		tmp := reflect.New(et)
		s.path = append(s.path, seg{name: key})
		ok, err := s.value(n.elem, tmp.UnsafePointer())
		s.path = s.path[:len(s.path)-1]
		if err != nil {
			return false, err
		}
		if ok {
			kv := reflect.New(kt).Elem()
			kv.SetString(key)
			mv.SetMapIndex(kv, tmp.Elem())
		}
	}
	s.depth--
	if n.members != nil && (over || cnt < n.members.Min) {
		s.failRule("", n.members.Message, wire.RuleMembers, int64(n.members.Min), int64(n.members.Max))
		return false, nil
	}
	return true, nil
}

// delegate hands one value to encoding/json/v2, which runs the type's own methods. It is
// correct and allocates, and the value is built in a fresh variable and copied over so that
// dst is never handed to code this package does not control (a method could keep it).
func (s *state) delegate(n *dnode, dst unsafe.Pointer) (bool, error) {
	raw, err := s.scalar()
	if err != nil {
		return false, err
	}
	tmp := reflect.New(n.typ)
	if err := jsonv2.Unmarshal(raw, tmp.Interface(), jsonv2.RejectUnknownMembers(!s.plan.opt.AllowUnknown)); err != nil {
		s.failRule("", n.delMsg, wire.RuleType, 0, 0)
		return false, nil
	}
	reflect.NewAt(n.typ, noescape(dst)).Elem().Set(tmp.Elem())
	return true, nil
}

// noescape hides dst from escape analysis. reflect's Set and Grow are written so that the
// destination escapes, which would force every caller's Input onto the heap (one allocation
// a request, ADR 0003) although they only write through it for the length of the call. No
// reflect call here keeps the pointer, so the lie is a true one.
//
// It is never inlined. Inlined, the conversion to uintptr is an ordinary value the compiler may
// compute once and keep across a call, in a slot the runtime does not treat as a pointer; when
// the goroutine's stack is moved during that call, the pointer made back from it still names the
// old stack, and writes through it are lost. A list in a JSON body nested two objects deep came
// back as a nil slice and a 500 for about two requests in three on a fresh connection (found in
// milestone 2 step 8; the test is in the root package's input_test.go). As a call, the conversion and its reverse
// happen inside this function with no call between them, where the stack cannot move, and the
// argument is a pointer the runtime adjusts.
//
//go:noinline
func noescape(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return *(*unsafe.Pointer)(unsafe.Pointer(&x))
}

// parseRFC3339 reads b as an RFC 3339 time with an offset. package time's own parser is
// looser than the RFC (a one-digit hour, a comma before the fraction, an offset of +99:99),
// so the same three checks encoding/json/v2 adds on top of UnmarshalText are repeated here;
// the differential fuzz test is what keeps the two in step.
func parseRFC3339(b []byte) (time.Time, bool) {
	var t time.Time
	if t.UnmarshalText(b) != nil || len(b) < len("2006-01-02T15:04:05Z") {
		return t, false
	}
	dec2 := func(p []byte) int { return int(p[0]-'0')*10 + int(p[1]-'0') }
	switch {
	case b[len("2006-01-02T")+1] == ':': // the hour must be two digits
		return t, false
	case len(b) > len("2006-01-02T15:04:05") && b[len("2006-01-02T15:04:05")] == ',': // the fraction starts with a period
		return t, false
	case b[len(b)-1] != 'Z' && (dec2(b[len(b)-len("07:00"):]) >= 24 || dec2(b[len(b)-len("00"):]) >= 60):
		return t, false // the offset's hour and minute must be in range
	}
	return t, true
}

// union decodes a union (union.go): the object is read whole, its discriminator member is found
// wherever it sits, and the object is decoded again as that variant, which reads past the
// discriminator. A tag that names no variant is a mistake at the discriminator's location, and so
// is a missing one.
func (s *state) union(n *dnode, k jsontext.Kind, dst unsafe.Pointer) (bool, error) {
	if k != '{' {
		return s.mismatch(msgObject)
	}
	raw, err := s.scalar() // a slice of the body, so the variant's strings may point into it
	if err != nil {
		return false, err
	}
	sub := statePool.Get().(*state)
	defer func() {
		sub.plan, sub.body, sub.slab, sub.errs, sub.skipName = nil, nil, nil, nil, ""
		sub.buf = bytes.Buffer{}
		sub.dec.Reset(emptyReader)
		statePool.Put(sub)
	}()
	sub.buf = *bytes.NewBuffer(raw)
	sub.dec.Reset(&sub.buf)
	tag, found, isString := "", false, false
	sub.dec.ReadToken() // {
	for sub.dec.PeekKind() != '}' {
		name, err := sub.dec.ReadValue()
		if err != nil {
			return false, s.wrap(err)
		}
		if nb, _ := sub.text(name); string(nb) != n.tagName {
			if err := sub.dec.SkipValue(); err != nil {
				return false, s.wrap(err)
			}
			continue
		}
		v, err := sub.dec.ReadValue()
		if err != nil {
			return false, s.wrap(err)
		}
		found = true
		if v.Kind() == '"' {
			tb, _ := sub.text(v)
			tag, isString = asString(tb), true
		}
		break
	}
	if !found {
		s.failRule(n.tagName, msgRequired, wire.RuleRequired, 0, 0)
		return false, nil
	}
	var vr *dvariant
	if isString {
		for i := range n.variants {
			if n.variants[i].tag == tag {
				vr = &n.variants[i]
				break
			}
		}
	}
	if vr == nil {
		s.failRule(n.tagName, n.tagMsg, wire.RuleValues, 0, 0)
		return false, nil
	}
	sub.buf = *bytes.NewBuffer(raw)
	sub.dec.Reset(&sub.buf)
	sub.plan, sub.body, sub.slab, sub.errs, sub.depth = s.plan, raw, s.slab, s.errs, s.depth
	sub.path = append(sub.path[:0], s.path...)
	sub.seen = sub.seen[:0]
	sub.skipName, sub.skipDepth = n.tagName, s.depth+1
	ok, err := sub.value(vr.node, unsafe.Add(dst, vr.valOff))
	if ok {
		*(*bool)(unsafe.Add(dst, vr.flagOff)) = true
	}
	return ok, err
}
