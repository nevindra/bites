package wire

import (
	"reflect"
	"strings"
)

// CSV is a list sent as one comma-separated value, `?stage=won,lost` (ADR 0042). It is a plain
// slice for the handler: range, len, index and a conversion to []T all work. Only a Query or a
// Header section reads it; a comma inside an element cannot be sent, because nothing escapes it.
type CSV[T any] []T

// CommaSeparated marks the type as a comma-separated list. A named type of the application's
// own, `type Stages bites.CSV[Stage]`, does not inherit it and says it again with a method of
// the same name when it wants an Items bound.
func (CSV[T]) CommaSeparated() {}

// CSVOf reports whether t is a comma-separated list: a slice type with a CommaSeparated method.
func CSVOf(t reflect.Type) bool {
	if t.Kind() != reflect.Slice || t.Name() == "" {
		return false
	}
	m, ok := t.MethodByName("CommaSeparated")
	return ok && m.Type.NumIn() == 1 && m.Type.NumOut() == 0
}

// PublicName is t as the application writes it: a type of this package is re-exported by the
// root package, so wire.CSV[int64] is named bites.CSV[int64] in a message.
func PublicName(t reflect.Type) string {
	return strings.ReplaceAll(t.String(), "wire.", "bites.")
}
