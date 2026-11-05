package wire

import (
	"encoding"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"
	"uuid"
)

// TextDecoder writes the decoded value of s into the value at dst, which points at a value of
// the type it was compiled for. It returns a *TextError when s does not fit the type and
// leaves the destination as it was. A string result points at s itself (s already lives in
// the request's slab, ADR 0008), so nothing is copied.
type TextDecoder func(dst unsafe.Pointer, s string) error

// TextError is a client-facing message: it becomes the "message" of an entry in the 400
// problem+json (ADR 0012). It never contains the client's value, so nothing can be reflected
// back and the 64-byte bound of ADR 0012 holds trivially.
//
// Rule is the machine-readable code of the sentence (ADR 0012), empty when the sentence has none;
// Min and Max are the bounds of a "length" or "range" rule and zero for every other.
type TextError struct {
	msg      string
	Rule     string
	Min, Max int64
	// Float says the bounds of a "range" rule are MinF and MaxF (-Inf or +Inf for a side the
	// type does not bound), not Min and Max.
	Float      bool
	MinF, MaxF float64
	// ExMin and ExMax say that MinF or MaxF is an exclusive bound.
	ExMin, ExMax bool
	// Format names the format of a "format" rule.
	Format string
}

func (e *TextError) Error() string { return e.msg }

// The fixed sentences. Ranges and enum lists are built once per type at compile time.
var (
	errInteger   = &TextError{msg: "expected an integer", Rule: RuleType}
	errNonNegInt = &TextError{msg: "expected a non-negative integer", Rule: RuleType}
	errBool      = &TextError{msg: "expected true or false", Rule: RuleType}
	errNumber    = &TextError{msg: "expected a number", Rule: RuleType}
	errTime      = &TextError{msg: "expected an RFC 3339 time with an offset", Rule: RuleType}
	errUUID      = &TextError{msg: "expected a UUID", Rule: RuleType}
	errDate      = &TextError{msg: "expected a date as YYYY-MM-DD", Rule: RuleType}
	errUnmarshal = &TextError{msg: "expected a valid value", Rule: RuleType}
)

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	timeType            = reflect.TypeFor[time.Time]()
	uuidType            = reflect.TypeFor[uuid.UUID]()
	dateType            = reflect.TypeFor[Date]()
)

// CompileText returns the decoder for t, or an error saying why t cannot be decoded from
// text. The caller turns the error into a Registration refusal naming the field (ADR 0007).
//
// Supported: string, bool, every int and uint kind (range-checked), float32 and float64,
// time.Time (RFC 3339 with an offset), uuid.UUID, Date, a type whose pointer implements
// encoding.TextUnmarshaler, a named string type with a Values method (an enum), and Opt or
// Null of any of these. Absence is the caller's business; Opt's decoder sets its flag.
//
// A type with both UnmarshalText and Values uses UnmarshalText: the author wrote the parser,
// and listing values is then its job.
//
// Slices are refused: repeated keys are the section decoder's job, which compiles the element
// type with CompileText and calls the result once per key (ADR 0042).
func CompileText(t reflect.Type) (TextDecoder, error) { return CompileTextWith(t, nil) }

// Formats maps a type to the format its author gave it with WithTextFormat. A type listed here
// decodes from text with its UnmarshalText, and says "expected a UUID" rather than "expected a
// valid value" when that fails.
type Formats map[reflect.Type]string

// FormatError is the sentence a client is told when a value of a type with the registered
// format does not decode. It never contains the client's value.
func FormatError(format string) *TextError {
	switch format {
	case "uuid":
		return errUUID
	case "date":
		return errDate
	}
	return &TextError{msg: "expected a valid " + format, Rule: RuleType}
}

// CompileTextWith is CompileText for an app that registered formats of its own.
func CompileTextWith(t reflect.Type, f Formats) (TextDecoder, error) {
	dec, err := compileText(t, f)
	if err != nil {
		return nil, err
	}
	if _, err := FormatOf(t); err != nil {
		return nil, err
	}
	if _, err := PatternOf(t); err != nil {
		return nil, err
	}
	if it, has, err := ItemsOf(t); err != nil {
		return nil, err
	} else if has {
		return nil, fmt.Errorf("%s has %s, but it decodes itself from text, so its items are not counted", t, ItemsRuleName(it))
	}
	check, err := CheckOf(t)
	if err != nil {
		return nil, err
	}
	if check == nil {
		return dec, nil
	}
	return func(dst unsafe.Pointer, s string) error {
		if err := dec(dst, s); err != nil {
			return err
		}
		if err := check(dst); err != nil {
			return &TextError{msg: err.Error(), Rule: RuleCheck}
		}
		return nil
	}, nil
}

func compileText(t reflect.Type, f Formats) (TextDecoder, error) {
	if kind, elem, vOff, fOff := PresenceOf(t); kind != None {
		inner, err := CompileTextWith(elem, f)
		if err != nil {
			return nil, err
		}
		return func(dst unsafe.Pointer, s string) error {
			if err := inner(unsafe.Add(dst, vOff), s); err != nil {
				return err
			}
			*(*bool)(unsafe.Add(dst, fOff)) = true
			return nil
		}, nil
	}
	switch t {
	case timeType:
		return decodeTime, nil
	case uuidType:
		return decodeUUID, nil
	case dateType:
		return decodeDate, nil
	}
	if format, ok := f[t]; ok && reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return compileUnmarshaler(t, FormatError(format)), nil
	}
	if c, err := ConstraintOf(t); err != nil {
		return nil, err
	} else if c.Kind != ConNone {
		return constrainedDecoder(t, c), nil
	}
	if t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return compileUnmarshaler(t, errUnmarshal), nil
	}
	switch k := t.Kind(); k {
	case reflect.String:
		return compileString(t)
	case reflect.Bool:
		return decodeBool, nil
	case reflect.Int:
		return intDecoder[int](strconv.IntSize), nil
	case reflect.Int8:
		return intDecoder[int8](8), nil
	case reflect.Int16:
		return intDecoder[int16](16), nil
	case reflect.Int32:
		return intDecoder[int32](32), nil
	case reflect.Int64:
		return intDecoder[int64](64), nil
	case reflect.Uint:
		return uintDecoder[uint](strconv.IntSize), nil
	case reflect.Uint8:
		return uintDecoder[uint8](8), nil
	case reflect.Uint16:
		return uintDecoder[uint16](16), nil
	case reflect.Uint32:
		return uintDecoder[uint32](32), nil
	case reflect.Uint64:
		return uintDecoder[uint64](64), nil
	case reflect.Float32:
		return floatDecoder[float32](32), nil
	case reflect.Float64:
		return floatDecoder[float64](64), nil
	case reflect.Pointer:
		return nil, fmt.Errorf("%s is a pointer; a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]", t)
	case reflect.Slice, reflect.Array:
		return nil, fmt.Errorf("%s is a %s; a slice is filled from repeated keys by the section, so its element type is what decodes from text", PublicName(t), k)
	case reflect.Map, reflect.Struct:
		return nil, fmt.Errorf("%s is a %s; text holds one scalar, give it an UnmarshalText method or use a type of its own", t, k)
	case reflect.Interface:
		return nil, fmt.Errorf("%s is an interface; its type is unknown until the request arrives, so it can be neither decoded nor described", t)
	default:
		return nil, fmt.Errorf("%s (kind %s) cannot be decoded from text", t, k)
	}
}

func compileString(t reflect.Type) (TextDecoder, error) {
	if m, ok := valuesMethod(t); ok {
		vals, err := enumValues(t, m)
		if err != nil {
			return nil, err
		}
		return enumDecoder(vals), nil
	}
	return func(dst unsafe.Pointer, s string) error {
		*(*string)(dst) = s
		return nil
	}, nil
}

// valuesMethod finds Values on t, or on *t.
func valuesMethod(t reflect.Type) (reflect.Method, bool) {
	if m, ok := t.MethodByName("Values"); ok {
		return m, true
	}
	return reflect.PointerTo(t).MethodByName("Values")
}

// enumValues calls Values once, on a zero receiver, and returns the listed strings.
func enumValues(t reflect.Type, m reflect.Method) ([]string, error) {
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0).Kind() != reflect.Slice ||
		(mt.Out(0).Elem() != t && mt.Out(0).Elem().Kind() != reflect.String) {
		return nil, fmt.Errorf("%s has a Values method, so it is an enum, but Values must be func() []%s or func() []string", t, t.Name())
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
		return nil, fmt.Errorf("%s: Values returned nothing, so no request could ever be valid", t)
	}
	return vals, nil
}

func enumDecoder(vals []string) TextDecoder {
	err := &TextError{msg: "expected one of: " + strings.Join(vals, ", "), Rule: RuleValues}
	return func(dst unsafe.Pointer, s string) error {
		for _, v := range vals {
			if v == s {
				*(*string)(dst) = s
				return nil
			}
		}
		return err
	}
}

func decodeBool(dst unsafe.Pointer, s string) error {
	switch s {
	case "true", "1":
		*(*bool)(dst) = true
	case "false", "0":
		*(*bool)(dst) = false
	default:
		return errBool
	}
	return nil
}

func intDecoder[T int | int8 | int16 | int32 | int64](bits int) TextDecoder {
	lo, hi := int64(math.MinInt64)>>(64-bits), int64(math.MaxInt64)>>(64-bits)
	rangeErr := &TextError{msg: fmt.Sprintf("expected an integer between %d and %d", lo, hi)}
	return func(dst unsafe.Pointer, s string) error {
		v, err := strconv.ParseInt(s, 10, bits)
		if err != nil {
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return rangeErr
			}
			return errInteger
		}
		*(*T)(dst) = T(v)
		return nil
	}
}

func uintDecoder[T uint | uint8 | uint16 | uint32 | uint64](bits int) TextDecoder {
	hi := uint64(math.MaxUint64) >> (64 - bits)
	rangeErr := &TextError{msg: fmt.Sprintf("expected an integer between 0 and %d", hi)}
	return func(dst unsafe.Pointer, s string) error {
		v, err := strconv.ParseUint(s, 10, bits)
		if err != nil {
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return rangeErr
			}
			return errNonNegInt
		}
		*(*T)(dst) = T(v)
		return nil
	}
}

func floatDecoder[T float32 | float64](bits int) TextDecoder {
	return func(dst unsafe.Pointer, s string) error {
		// Hex floats and digit separators parse in Go but are not numbers a client means to
		// send, and nothing here may differ from what the JSON number grammar accepts.
		if strings.ContainsAny(s, "xX_") {
			return errNumber
		}
		v, err := strconv.ParseFloat(s, bits)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return errNumber
		}
		*(*T)(dst) = T(v)
		return nil
	}
}

func decodeTime(dst unsafe.Pointer, s string) error {
	// RFC3339 requires the offset (or Z), so a bare date is refused by the layout itself.
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return errTime
	}
	*(*time.Time)(dst) = v
	return nil
}

func decodeUUID(dst unsafe.Pointer, s string) error {
	var u uuid.UUID
	// A read-only view of s: uuid's UnmarshalText only reads, and the copy []byte(s) would be
	// the one allocation in this path. The view does not outlive the call.
	if err := u.UnmarshalText(unsafe.Slice(unsafe.StringData(s), len(s))); err != nil {
		return errUUID
	}
	*(*uuid.UUID)(dst) = u
	return nil
}

// compileUnmarshaler wraps a type's own UnmarshalText. The []byte copy is deliberate: the
// interface lets the method retain or modify its argument, and s is read-only memory that
// other fields point into. The price is an allocation, paid only by types that bring their
// own parser (two: the argument and the fresh receiver). The method's error text is dropped because it may quote the input.
func compileUnmarshaler(t reflect.Type, fail *TextError) TextDecoder {
	return func(dst unsafe.Pointer, s string) error {
		// The method runs on a fresh value that is copied over dst, never on dst itself:
		// the framework fills an Input that lives on the handler's stack, and a method that
		// kept its receiver would keep a pointer into a frame that is gone.
		v := reflect.New(t)
		u := v.Interface().(encoding.TextUnmarshaler)
		if err := u.UnmarshalText([]byte(s)); err != nil {
			return fail
		}
		reflect.NewAt(t, dst).Elem().Set(v.Elem())
		return nil
	}
}
