package jsoncodec

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/nevindra/bites/internal/wire"
)

// A union (ADR 0083) is a struct whose Discriminator method names the member that tells its
// variants apart. Each field is an Opt of a struct, one variant, and the field's wire name is the
// variant's tag: the struct
//
//	type ClientFrame struct {
//		Join bites.Opt[Join]
//		Say  bites.Opt[Say]
//	}
//	func (ClientFrame) Discriminator() string { return "type" }
//
// is {"type":"join","room":7} or {"type":"say","text":"hi"} on the wire, with exactly one variant
// set in the Go value.

type discriminator interface{ Discriminator() string }

var discriminatorType = reflect.TypeFor[discriminator]()

// A union may name variants only the server carries with a ServerOnly method (ADR 0083): the
// broker carries them as any variant, and no client is described, sent or read one.
type serverOnlyer interface{ ServerOnly() []string }

var serverOnlyType = reflect.TypeFor[serverOnlyer]()

// ServerOnlyError is the error of encoding a value whose union holds a variant that its
// ServerOnly method names, by a plan that is not for the server's own bytes (Options.ServerVariants):
// a Socket's Send, a response. Nothing was written. Union is the union's type, Variant the
// variant's tag and Field its Go field name. The zero value is an error about no union.
type ServerOnlyError struct {
	Union   reflect.Type
	Variant string
	Field   string
}

func (e *ServerOnlyError) Error() string {
	return fmt.Sprintf("jsoncodec: the union %s has its server-only variant %s (field %s) set, which is never sent to a client: set another variant, or turn it into one first (a Topic's Resolve does)", e.Union, e.Variant, e.Field)
}

// unionVariant is one variant of a union.
type unionVariant struct {
	tag     string       // the value of the discriminator member
	goPath  string       // Frame.Join
	opt     reflect.Type // the field's type, Opt[S]
	typ     reflect.Type // S
	off     uintptr      // the field in the union
	valOff  uintptr      // S in the Opt
	flagOff uintptr      // the Opt's presence flag

	serverOnly bool // named by the union's ServerOnly method
}

// isUnion reports whether t declares itself a union, on either receiver, so a method on the
// pointer is refused rather than missed.
func isUnion(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && (t.Implements(discriminatorType) || reflect.PointerTo(t).Implements(discriminatorType))
}

// unionOf reads the union t: the discriminator member's name and the variants, or the reason t
// cannot be one.
func unionOf(t reflect.Type, o Options, path string) (string, []unionVariant, error) {
	if !t.Implements(discriminatorType) {
		return "", nil, refusal(path, "%s declares Discriminator on *%s, but the codec holds a %s: declare it on the value receiver, func (%s) Discriminator() string", typeName(t), t.Name(), t.Name(), t.Name())
	}
	tag := reflect.New(t).Elem().Interface().(discriminator).Discriminator()
	if tag == "" {
		return "", nil, refusal(path, "%s.Discriminator returns \"\": return the name of the member that says which variant a value is, such as \"type\"", typeName(t))
	}
	specs, err := collectFields(t, o, typeName(t), true)
	if err != nil {
		return "", nil, err
	}
	if len(specs) < 2 {
		return "", nil, refusal(path, "%s is a union of %d variants: a union has at least two, each an Opt field of a struct", typeName(t), len(specs))
	}
	vs := make([]unionVariant, 0, len(specs))
	for _, sp := range specs {
		k, elem, vOff, fOff := wire.PresenceOf(sp.typ)
		if k != wire.Optional || elem.Kind() != reflect.Struct {
			return "", nil, refusal(sp.goPath, "a field of the union %s is %s, but each is an Opt of a struct, the variant whose tag is %q", typeName(t), sp.typ, sp.name)
		}
		if OwnJSON(elem) || hasUnmarshaler(elem) || hasMarshaler(elem) {
			return "", nil, refusal(sp.goPath, "the variant %s writes or reads itself, but a variant is an object the union writes with its discriminator: drop the JSON or text methods, or wrap the value in a field", typeName(elem))
		}
		if isUnion(elem) {
			return "", nil, refusal(sp.goPath, "the variant %s is a union itself: a variant is a struct of fields, so flatten its variants into %s", typeName(elem), typeName(t))
		}
		if sp.omitEmpty || sp.omitZero {
			return "", nil, refusal(sp.goPath, "a variant of a union takes no omitempty or omitzero: the one that is set is written, and the others never are")
		}
		inner, err := collectFields(elem, o, typeName(elem), false)
		if err != nil {
			return "", nil, err
		}
		for _, f := range inner {
			if f.name == tag {
				return "", nil, refusal(f.goPath, "the variant %s has a member %q, which is the discriminator of %s: rename the member, or return another name from Discriminator", typeName(elem), tag, typeName(t))
			}
		}
		vs = append(vs, unionVariant{tag: sp.name, goPath: sp.goPath, opt: sp.typ, typ: elem, off: sp.offset, valOff: sp.offset + vOff, flagOff: sp.offset + fOff})
	}
	if err := markServerOnly(t, vs, path); err != nil {
		return "", nil, err
	}
	return tag, vs, nil
}

// markServerOnly sets serverOnly on the variants the ServerOnly method of t names, or refuses
// the method.
func markServerOnly(t reflect.Type, vs []unionVariant, path string) error {
	if !t.Implements(serverOnlyType) {
		if reflect.PointerTo(t).Implements(serverOnlyType) {
			return refusal(path, "%s declares ServerOnly on *%s, but the codec holds a %s: declare it on the value receiver, func (%s) ServerOnly() []string", typeName(t), t.Name(), t.Name(), t.Name())
		}
		return nil
	}
	names := reflect.New(t).Elem().Interface().(serverOnlyer).ServerOnly()
	fields := make([]string, len(vs))
	for i, v := range vs {
		fields[i] = goFieldName(v.goPath)
	}
	n := 0
	for _, name := range names {
		i := slices.Index(fields, name)
		switch {
		case i < 0:
			return refusal(path, "%s.ServerOnly names %s, which is not a variant of %s (its variants: %s): name a field that is an Opt of a variant", typeName(t), name, typeName(t), strings.Join(fields, ", "))
		case vs[i].serverOnly:
			return refusal(path, "%s.ServerOnly names %s twice: name each variant once", typeName(t), name)
		}
		vs[i].serverOnly = true
		n++
	}
	if n == len(vs) {
		return refusal(path, "%s.ServerOnly names every variant (%s), so no client could be sent or send any: leave at least one out", typeName(t), strings.Join(fields, ", "))
	}
	return nil
}

// unionTags is the tags of vs, for the sentence of an unknown one.
func unionTags(vs []unionVariant) string {
	tags := make([]string, len(vs))
	for i, v := range vs {
		tags[i] = v.tag
	}
	return strings.Join(tags, ", ")
}
