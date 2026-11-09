package wire

import (
	"encoding/json"
	"reflect"
	"strings"
)

// RawJSON is a JSON value the handler reads and writes as bytes, and the API document describes
// as T (ADR 0084). It is for a member whose JSON the application owns and does not decode, such
// as a patch of style fields an editor defines: json.RawMessage says "any JSON" and the document
// would say {}.
//
// The bytes are what was sent, and what is written back. Null is accepted and written like any
// value: a member that arrived as null holds the four bytes null, and a member never set (a nil
// JSON) is written as null. T is not checked against the bytes; it only decides the schema.
type RawJSON[T any] struct {
	// JSON is the value exactly as it arrived, or the bytes to write. Nil is null on the wire.
	JSON json.RawMessage
	_    [0]T // T for the API document; no data
}

// MarshalJSON writes JSON as it is, or null when it is empty.
func (r RawJSON[T]) MarshalJSON() ([]byte, error) {
	if len(r.JSON) == 0 {
		return []byte("null"), nil
	}
	return r.JSON, nil
}

// UnmarshalJSON keeps a copy of b, null included.
func (r *RawJSON[T]) UnmarshalJSON(b []byte) error {
	r.JSON = append(r.JSON[:0], b...)
	return nil
}

// RawJSONOf reports whether t is a RawJSON (or the public package's alias of one) and its T.
func RawJSONOf(t reflect.Type) (reflect.Type, bool) {
	if t == nil || t.Kind() != reflect.Struct || t.PkgPath() != wirePkgPath || t.NumField() != 2 || !strings.HasPrefix(t.Name(), "RawJSON[") {
		return nil, false
	}
	return t.Field(1).Type.Elem(), true
}
