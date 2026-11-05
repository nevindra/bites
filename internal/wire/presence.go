package wire

import (
	"fmt"
	"reflect"
	"strings"
)

// Opt is a value that may be absent (ADR 0041). The zero Opt is absent, so a request that
// leaves a field out needs no work from the decoder. Opt is comparable when T is.
type Opt[T any] struct {
	// The value comes first and the flag second so PresenceOf can report both offsets by
	// field index; their order is not otherwise important.
	value T
	set   bool
}

// Some returns an Opt that is present and holds v.
func Some[T any](v T) Opt[T] { return Opt[T]{value: v, set: true} }

// OptOf returns an Opt that is absent when p is nil and holds a copy of *p otherwise. It is for
// the boundary with a service that speaks *T; an Input still cannot hold a pointer (ADR 0041).
func OptOf[T any](p *T) Opt[T] {
	if p == nil {
		return Opt[T]{}
	}
	return Opt[T]{value: *p, set: true}
}

// Set makes o present and holding v. It exists for code that fills an Opt through reflection,
// such as modules/config, which cannot call the generic Some for a type known only at run time
// (reflect.Value.Addr().MethodByName("Set")); application code builds an Opt with Some (ADR 0080
// says why a second spelling is allowed here).
func (o *Opt[T]) Set(v T) {
	o.value = v
	o.set = true
}

// Get returns the value and whether it was sent. When it was not, the value is T's zero.
func (o Opt[T]) Get() (T, bool) {
	if !o.set {
		var zero T
		return zero, false
	}
	return o.value, true
}

// Or returns the value, or def when it was not sent.
func (o Opt[T]) Or(def T) T {
	if o.set {
		return o.value
	}
	return def
}

// IsSet reports whether the value was sent.
func (o Opt[T]) IsSet() bool { return o.set }

// Ptr returns nil when the value was not sent, and a pointer to a copy of it otherwise. The copy
// is the caller's: writing through it changes nothing in the Opt.
func (o Opt[T]) Ptr() *T {
	if !o.set {
		return nil
	}
	v := o.value
	return &v
}

// String prints the value, or "<absent>".
func (o Opt[T]) String() string {
	if !o.set {
		return "<absent>"
	}
	return fmt.Sprint(o.value)
}

// GoString prints a form that reads back as the Go that made it.
func (o Opt[T]) GoString() string {
	if !o.set {
		return fmt.Sprintf("%T{}", o)
	}
	return fmt.Sprintf("wire.Some(%#v)", o.value)
}

// Null is a value that may be sent as null (ADR 0041). The zero Null is null, which is also
// what a decoder leaves when it sees the JSON null. Null is comparable when T is.
type Null[T any] struct {
	value T
	valid bool
}

// NonNull returns a Null that holds v. The name says what the constructor does: Null[T]{}
// is already the null, so the only constructor needed is the one for a value. It pairs with
// Some, and avoids "Value", which reads as a getter and would be a poor top-level name in the
// public package.
func NonNull[T any](v T) Null[T] { return Null[T]{value: v, valid: true} }

// Get returns the value, and false when it is null. When null, the value is T's zero.
func (n Null[T]) Get() (T, bool) {
	if !n.valid {
		var zero T
		return zero, false
	}
	return n.value, true
}

// IsNull reports whether the value is null.
func (n Null[T]) IsNull() bool { return !n.valid }

// String prints the value, or "null".
func (n Null[T]) String() string {
	if !n.valid {
		return "null"
	}
	return fmt.Sprint(n.value)
}

// GoString prints a form that reads back as the Go that made it.
func (n Null[T]) GoString() string {
	if !n.valid {
		return fmt.Sprintf("%T{}", n)
	}
	return fmt.Sprintf("wire.NonNull(%#v)", n.value)
}

// PresenceKind says whether a type is one of the presence wrappers.
type PresenceKind int

const (
	// None: the type is not a presence wrapper.
	None PresenceKind = iota
	// Optional: the type is Opt[T].
	Optional
	// Nullable: the type is Null[T].
	Nullable
)

func (k PresenceKind) String() string {
	switch k {
	case Optional:
		return "Optional"
	case Nullable:
		return "Nullable"
	}
	return "None"
}

var (
	wirePkgPath  = reflect.TypeFor[Opt[int]]().PkgPath()
	optNamePart  = "Opt["
	nullNamePart = "Null["
)

// PresenceOf reports whether t is an Opt or a Null (or the public package's alias of one, which
// is the same type), its element type T, and the byte offsets of the value and of the flag
// inside the struct. A decoder compiled at Registration uses the offsets with unsafe to fill
// the value and set the flag without exported fields and without a reflect call per request.
// For any other type it returns None and zeros.
//
// The type is recognised by package path and instantiated name: a generic type has no
// reflect-visible origin, and a struct of the same shape in another package must not match.
func PresenceOf(t reflect.Type) (kind PresenceKind, elem reflect.Type, valueOff, flagOff uintptr) {
	if t == nil || t.Kind() != reflect.Struct || t.PkgPath() != wirePkgPath || t.NumField() != 2 {
		return None, nil, 0, 0
	}
	switch {
	case strings.HasPrefix(t.Name(), optNamePart):
		kind = Optional
	case strings.HasPrefix(t.Name(), nullNamePart):
		kind = Nullable
	default:
		return None, nil, 0, 0
	}
	v, f := t.Field(0), t.Field(1)
	return kind, v.Type, v.Offset, f.Offset
}
