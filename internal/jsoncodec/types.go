package jsoncodec

import (
	"encoding"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"time"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

var (
	timeType = reflect.TypeFor[time.Time]()
	uuidType = reflect.TypeFor[uuid.UUID]()
	dateType = reflect.TypeFor[wire.Date]()

	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
	fromUnmarshalerType = reflect.TypeFor[jsonv2.UnmarshalerFrom]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	toMarshalerType   = reflect.TypeFor[jsonv2.MarshalerTo]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()

	rawMessageType = reflect.TypeFor[json.RawMessage]()
	jsontextValue  = reflect.TypeFor[jsontext.Value]()

	isZeroerType = reflect.TypeFor[interface{ IsZero() bool }]()
)

// hasUnmarshaler reports whether *t decodes itself (json, json/v2 or text).
func hasUnmarshaler(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(jsonUnmarshalerType) || p.Implements(fromUnmarshalerType) ||
		p.Implements(textUnmarshalerType)
}

// hasMarshaler reports whether t, or *t, encodes itself.
func hasMarshaler(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(jsonMarshalerType) || p.Implements(toMarshalerType) ||
		p.Implements(textMarshalerType)
}

func hasOwnJSON(t reflect.Type) bool { return hasUnmarshaler(t) || hasMarshaler(t) }

// Inlined reports whether the codec reads f's fields as members of the struct that embeds it:
// an embedded struct by value, with no JSON name of its own, not left out with "-", and not a
// type that reads or writes itself. The API document describes such a struct's fields where the
// codec puts them, so it asks the same question here rather than repeating the rule.
func Inlined(f reflect.StructField) bool {
	if !f.Anonymous || f.Type.Kind() != reflect.Struct {
		return false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return false
	}
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return false
	}
	if k, _, _, _ := wire.PresenceOf(f.Type); k != wire.None {
		return false
	}
	return !hasOwnJSON(f.Type)
}

// isByteSeq reports whether t is a []byte or a [N]byte, which v2 writes as base64 and this
// package leaves to it.
func isByteSeq(t reflect.Type) bool {
	return (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t.Elem().Kind() == reflect.Uint8 &&
		!hasOwnJSON(t.Elem())
}

// valuesOf returns the strings of t's Values method (the enum list, as wire reads it), and
// whether t has one. Called once at Registration.
func valuesOf(t reflect.Type) (vals []string, has bool, err error) {
	m, ok := t.MethodByName("Values")
	recvIsPtr := false
	if !ok {
		m, ok = reflect.PointerTo(t).MethodByName("Values")
		recvIsPtr = true
	}
	if !ok {
		return nil, false, nil
	}
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0).Kind() != reflect.Slice ||
		(mt.Out(0).Elem() != t && mt.Out(0).Elem().Kind() != reflect.String) {
		return nil, true, refusalf("%s has a Values method, so it is an enum, but Values must be func() []%s or func() []string", t, t.Name())
	}
	recv := reflect.New(t)
	if !recvIsPtr {
		recv = recv.Elem()
	}
	out := m.Func.Call([]reflect.Value{recv})[0]
	vals = make([]string, out.Len())
	for i := range vals {
		vals[i] = out.Index(i).String()
	}
	if len(vals) == 0 {
		return nil, true, refusalf("%s: Values returned nothing, so no request could ever be valid", t)
	}
	return vals, true, nil
}

// opaque reports whether t writes (Encoding) or reads (Decoding) its own JSON in a way the API
// document cannot see, and the app has not said what it is: it has a JSON method, or a text one
// beside a JSON one, and is neither registered nor one of the two types that mean free-form
// JSON by name. A text-only type is a string and is not this case (ADR 0076).
func (o Options) opaque(t reflect.Type, dir Direction) bool {
	if t == rawMessageType || t == jsontextValue || o.Undescribed || isRawJSON(t) {
		return false
	}
	if _, ok := o.JSONSchemas[t]; ok {
		return false
	}
	if dir == Decoding {
		return hasUnmarshaler(t) && delegateKind(t, true) == KindAny
	}
	return hasMarshaler(t) && delegateKind(t, false) == KindAny
}

func (o Options) opaqueRefusal(path string, t reflect.Type, dir Direction) error {
	verb, methods := "encodes", "MarshalJSON"
	if dir == Decoding {
		verb, methods = "decodes", "UnmarshalJSON"
	}
	return refusal(path, "%s %s itself through its own %s, so the API document cannot see its shape and would describe it as {}: register it with WithJSONSchema[%s](JSONSchema{Type: ..., Format: ...}), or with JSONSchema{Type: \"any\"} if it really is free-form JSON", t, verb, methods, t)
}

// OwnJSON reports whether t, or *t, has a json or json/v2 method in either direction, which is
// what WithJSONSchema registers. A type with text methods alone is WithTextFormat's.
func OwnJSON(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return p.Implements(jsonUnmarshalerType) || p.Implements(fromUnmarshalerType) ||
		p.Implements(jsonMarshalerType) || p.Implements(toMarshalerType)
}

func isRawJSON(t reflect.Type) bool {
	_, ok := wire.RawJSONOf(t)
	return ok
}

// checkRawJSON refuses a RawJSON whose T cannot be described: the document would say less than
// the author meant. T is then compiled in the direction of the plan, so it is refused for the
// same reasons as a field of that type.
func checkRawJSON(t reflect.Type, path string) error {
	elem, ok := wire.RawJSONOf(t)
	if !ok {
		return nil
	}
	switch {
	case elem.Kind() == reflect.Interface:
		return refusal(path, "%s names no shape: an interface has none, so use json.RawMessage for any JSON", wire.PublicName(t))
	case elem.Kind() == reflect.Pointer:
		return refusal(path, "%s: null is already accepted, so name the type itself, RawJSON[%s]", wire.PublicName(t), wire.PublicName(elem.Elem()))
	case elem == rawMessageType || elem == jsontextValue || isRawJSON(elem):
		return refusal(path, "%s: T is itself free-form JSON; use json.RawMessage, or name the shape the value has", wire.PublicName(t))
	}
	return nil
}
