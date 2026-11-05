package config

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nevindra/bites"
)

// A leaf is a type read from one piece of text. Its decoder writes into dst (settable) and
// returns an error whose text is a sentence that never holds the text it was given.
type leaf struct {
	dec  func(dst reflect.Value, s string) error
	noun string // "a duration", used when a default is refused
	desc string // the type as an operator reads it, for the usage
}

// decodeError is a sentence for the operator: it never contains the value read.
type decodeError struct{ msg string }

func (e *decodeError) Error() string { return e.msg }

func sentence(s string) error { return &decodeError{s} }

var (
	errInteger  = sentence("expected an integer")
	errNonNeg   = sentence("expected a non-negative integer")
	errBool     = sentence("expected true or false")
	errNumber   = sentence("expected a number")
	errTime     = sentence("expected an RFC 3339 time with an offset")
	errDate     = sentence("expected a date as YYYY-MM-DD")
	errUnmarsh  = sentence("expected a valid value")
	errDuration = sentence("expected a duration such as 5s or 1m30s")
)

var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
	dateType     = reflect.TypeFor[bites.Date]()
	secretType   = reflect.TypeFor[Secret]()
	textUnm      = reflect.TypeFor[encoding.TextUnmarshaler]()
	checkerType  = reflect.TypeFor[interface{ Check() error }]()
)

// compileLeaf returns the leaf of t, or ok false when t is not a leaf (a struct, a slice, a
// map). A type that cannot be a setting at all is returned as a refusal text.
func compileLeaf(t reflect.Type) (lf *leaf, refusal string) {
	base, refusal := baseLeaf(t)
	if base == nil {
		return nil, refusal
	}
	if t.Name() != "" && !t.Implements(checkerType) && reflect.PointerTo(t).Implements(checkerType) {
		return nil, "has a Check method with a pointer receiver, but a value is checked as it was decoded: declare it on " + t.Name()
	}
	check := checkMethod(t)
	if check == nil {
		return base, ""
	}
	inner := base.dec
	out := *base
	out.dec = func(dst reflect.Value, s string) error {
		if err := inner(dst, s); err != nil {
			return err
		}
		if err := check(dst); err != nil {
			return sentence(err.Error())
		}
		return nil
	}
	return &out, ""
}

func checkMethod(t reflect.Type) func(reflect.Value) error {
	if t.Name() == "" || !t.Implements(checkerType) {
		return nil
	}
	return func(v reflect.Value) error { return v.Interface().(interface{ Check() error }).Check() }
}

func baseLeaf(t reflect.Type) (*leaf, string) {
	switch t {
	case timeType:
		return &leaf{noun: "an RFC 3339 time", desc: "time", dec: func(dst reflect.Value, s string) error {
			v, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return errTime
			}
			dst.Set(reflect.ValueOf(v))
			return nil
		}}, ""
	case durationType:
		return &leaf{noun: "a duration", desc: "duration", dec: func(dst reflect.Value, s string) error {
			v, err := time.ParseDuration(s)
			if err != nil {
				return errDuration
			}
			dst.SetInt(int64(v))
			return nil
		}}, ""
	case dateType:
		return &leaf{noun: "a date", desc: "date", dec: func(dst reflect.Value, s string) error {
			v, err := bites.ParseDate(s)
			if err != nil {
				return errDate
			}
			dst.Set(reflect.ValueOf(v))
			return nil
		}}, ""
	}
	if t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(textUnm) {
		return &leaf{noun: "a valid value", desc: t.String(), dec: func(dst reflect.Value, s string) error {
			if err := dst.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(s)); err != nil {
				return errUnmarsh
			}
			return nil
		}}, ""
	}
	switch t.Kind() {
	case reflect.String:
		return stringLeaf(t)
	case reflect.Bool:
		return &leaf{noun: "true or false", desc: "bool", dec: func(dst reflect.Value, s string) error {
			switch s {
			case "true", "1":
				dst.SetBool(true)
			case "false", "0":
				dst.SetBool(false)
			default:
				return errBool
			}
			return nil
		}}, ""
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return intLeaf(t)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := t.Bits()
		hi := uint64(math.MaxUint64) >> (64 - bits)
		rangeErr := sentence(fmt.Sprintf("expected an integer between 0 and %d", hi))
		return &leaf{noun: "a non-negative integer", desc: "integer", dec: func(dst reflect.Value, s string) error {
			v, err := strconv.ParseUint(s, 10, bits)
			if err != nil {
				if errors.Is(err, strconv.ErrRange) {
					return rangeErr
				}
				return errNonNeg
			}
			dst.SetUint(v)
			return nil
		}}, ""
	case reflect.Float32, reflect.Float64:
		return floatLeaf(t)
	}
	return nil, ""
}

func stringLeaf(t reflect.Type) (*leaf, string) {
	if _, ok := reflect.PointerTo(t).MethodByName("Range"); ok {
		return nil, "has a Range method, but a string type's bound is its length: name it Length, func() (min, max int)"
	}
	desc := "string"
	if t != reflect.TypeFor[string]() {
		desc = t.Name()
	}
	if m, ok := valuesMethod(t); ok {
		vals, err := enumValues(t, m)
		if err != nil {
			return nil, "has a Values method that is not an enum: " + err.Error()
		}
		e := sentence("expected one of: " + strings.Join(vals, ", "))
		return &leaf{noun: "one of " + strings.Join(vals, ", "), desc: desc, dec: func(dst reflect.Value, s string) error {
			for _, v := range vals {
				if v == s {
					dst.SetString(s)
					return nil
				}
			}
			return e
		}}, ""
	}
	if m, ok := reflect.PointerTo(t).MethodByName("Length"); ok {
		mt := m.Type
		if mt.NumIn() != 1 || mt.NumOut() != 2 || mt.Out(0) != reflect.TypeFor[int]() || mt.Out(1) != reflect.TypeFor[int]() {
			return nil, "has a Length method, so it carries a bound, but Length must be func() (min, max int)"
		}
		out := m.Func.Call([]reflect.Value{reflect.New(t)})
		lo, hi := int(out[0].Int()), int(out[1].Int())
		if lo < 0 || hi < 1 || hi < lo {
			return nil, fmt.Sprintf("has a Length method returning (%d, %d), which is not a length: it must be 0 <= min <= max with max at least 1", lo, hi)
		}
		msg := lengthMessage(lo, hi)
		e := sentence(msg)
		return &leaf{noun: "a string " + strings.TrimPrefix(msg, "must be "), desc: desc, dec: func(dst reflect.Value, s string) error {
			if hi != math.MaxInt && len(s) > hi*utf8.UTFMax {
				return e
			}
			n := utf8.RuneCountInString(s)
			if n < lo || (hi != math.MaxInt && n > hi) {
				return e
			}
			dst.SetString(s)
			return nil
		}}, ""
	}
	return &leaf{noun: "a string", desc: desc, dec: func(dst reflect.Value, s string) error {
		dst.SetString(s)
		return nil
	}}, ""
}

// lengthMessage is the sentence the core gives a Length rule (internal/wire.lengthConstraint),
// copied here because the module may not import internal packages; a test holds the two equal.
func lengthMessage(lo, hi int) string {
	if hi == math.MaxInt {
		return "must be at least " + strconv.Itoa(lo) + " characters"
	}
	switch {
	case lo == hi:
		return "must be exactly " + strconv.Itoa(hi) + " characters"
	case lo == 0:
		return "must be at most " + strconv.Itoa(hi) + " characters"
	}
	return "must be between " + strconv.Itoa(lo) + " and " + strconv.Itoa(hi) + " characters"
}

func valuesMethod(t reflect.Type) (reflect.Method, bool) {
	if m, ok := t.MethodByName("Values"); ok {
		return m, true
	}
	return reflect.PointerTo(t).MethodByName("Values")
}

func enumValues(t reflect.Type, m reflect.Method) ([]string, error) {
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0).Kind() != reflect.Slice ||
		(mt.Out(0).Elem() != t && mt.Out(0).Elem().Kind() != reflect.String) {
		return nil, fmt.Errorf("Values must be func() []%s or func() []string", t.Name())
	}
	recv := reflect.New(t)
	if mt.In(0) == t {
		recv = recv.Elem()
	}
	out := m.Func.Call([]reflect.Value{recv})[0]
	vals := make([]string, out.Len())
	for i := range vals {
		vals[i] = out.Index(i).String()
	}
	if len(vals) == 0 {
		return nil, errors.New("Values returned nothing, so no setting could ever be valid")
	}
	return vals, nil
}

func intLeaf(t reflect.Type) (*leaf, string) {
	bits := t.Bits()
	lo, hi := int64(math.MinInt64)>>(64-bits), int64(math.MaxInt64)>>(64-bits)
	rangeErr := sentence(fmt.Sprintf("expected an integer between %d and %d", lo, hi))
	noun := "an integer"
	var limit *sentenceRange
	if _, ok := reflect.PointerTo(t).MethodByName("Length"); ok {
		return nil, "has a Length method, but an integer type's bound is its value: name it Range, func() (min, max int64)"
	}
	if m, ok := reflect.PointerTo(t).MethodByName("Range"); ok {
		mt := m.Type
		if mt.NumIn() != 1 || mt.NumOut() != 2 || mt.Out(0) != reflect.TypeFor[int64]() || mt.Out(1) != reflect.TypeFor[int64]() {
			return nil, "has a Range method, so it carries a bound, but Range must be func() (min, max int64)"
		}
		out := m.Func.Call([]reflect.Value{reflect.New(t)})
		rlo, rhi := out[0].Int(), out[1].Int()
		if rhi < rlo {
			return nil, fmt.Sprintf("has a Range method returning (%d, %d), and max is below min, so no value could be valid", rlo, rhi)
		}
		noMin, hasMax := rlo <= lo, rhi < hi
		if noMin && !hasMax {
			return nil, "has a Range method returning the whole of " + t.Kind().String() + ", which bounds nothing: drop the method"
		}
		var msg string
		switch {
		case noMin:
			msg = "expected an integer of at most " + strconv.FormatInt(rhi, 10)
		case !hasMax:
			msg = "expected an integer of at least " + strconv.FormatInt(rlo, 10)
		default:
			msg = "expected an integer between " + strconv.FormatInt(rlo, 10) + " and " + strconv.FormatInt(rhi, 10)
		}
		limit = &sentenceRange{lo: rlo, hi: rhi, noMin: noMin, hasMax: hasMax, err: sentence(msg)}
		noun = strings.Replace(msg, "expected ", "", 1)
	}
	desc := "integer"
	if t.Name() != "" && t.PkgPath() != "" {
		desc = t.Name()
	}
	return &leaf{noun: noun, desc: desc, dec: func(dst reflect.Value, s string) error {
		v, err := strconv.ParseInt(s, 10, bits)
		if err != nil {
			if errors.Is(err, strconv.ErrRange) {
				return rangeErr
			}
			return errInteger
		}
		if limit != nil && ((!limit.noMin && v < limit.lo) || (limit.hasMax && v > limit.hi)) {
			return limit.err
		}
		dst.SetInt(v)
		return nil
	}}, ""
}

type sentenceRange struct {
	lo, hi        int64
	noMin, hasMax bool
	err           error
}

func floatLeaf(t reflect.Type) (*leaf, string) {
	bits := t.Bits()
	var lo, hi float64
	var noMin, hasMax, ranged bool
	var rerr error
	noun := "a number"
	if _, ok := reflect.PointerTo(t).MethodByName("Length"); ok {
		return nil, "has a Length method, but a float type's bound is its value: name it Range, func() (min, max float64)"
	}
	if m, ok := reflect.PointerTo(t).MethodByName("Range"); ok {
		mt := m.Type
		if mt.NumIn() != 1 || mt.NumOut() != 2 || mt.Out(0) != reflect.TypeFor[float64]() || mt.Out(1) != reflect.TypeFor[float64]() {
			return nil, "has a Range method, so it carries a bound, but Range must be func() (min, max float64)"
		}
		out := m.Func.Call([]reflect.Value{reflect.New(t)})
		lo, hi = out[0].Float(), out[1].Float()
		if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
			return nil, fmt.Sprintf("has a Range method returning (%v, %v), and a bound must be a finite number: use -math.MaxFloat64 or math.MaxFloat64 for no bound on a side", lo, hi)
		}
		limit := math.MaxFloat64
		if bits == 32 {
			limit = math.MaxFloat32
		}
		if hi < lo {
			return nil, fmt.Sprintf("has a Range method returning (%s, %s), and max is below min, so no value could be valid", fmtFloat(lo), fmtFloat(hi))
		}
		noMin, hasMax = lo <= -limit, hi < limit
		if noMin && !hasMax {
			return nil, "has a Range method returning the whole of " + t.Kind().String() + ", which bounds nothing: drop the method"
		}
		var msg string
		switch {
		case noMin:
			msg = "expected a number of at most " + fmtFloat(hi)
		case !hasMax:
			msg = "expected a number of at least " + fmtFloat(lo)
		default:
			msg = "expected a number between " + fmtFloat(lo) + " and " + fmtFloat(hi)
		}
		rerr = sentence(msg)
		noun = strings.Replace(msg, "expected ", "", 1)
		ranged = true
		if bits == 32 {
			lo, hi = float64(float32(lo)), float64(float32(hi))
		}
	}
	return &leaf{noun: noun, desc: "number", dec: func(dst reflect.Value, s string) error {
		if strings.ContainsAny(s, "xX_") {
			return errNumber
		}
		v, err := strconv.ParseFloat(s, bits)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return errNumber
		}
		if ranged && ((!noMin && v < lo) || (hasMax && v > hi)) {
			return rerr
		}
		dst.SetFloat(v)
		return nil
	}}, ""
}

func fmtFloat(f float64) string {
	format := byte('f')
	if a := math.Abs(f); a != 0 && (a < 1e-6 || a >= 1e21) {
		format = 'e'
	}
	return strconv.FormatFloat(f, format, -1, 64)
}

func isTypeError(err error) bool {
	switch err {
	case errInteger, errNonNeg, errBool, errNumber, errTime, errDate, errUnmarsh, errDuration:
		return true
	}
	return false
}
