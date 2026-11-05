package jsoncodec

import (
	"reflect"

	"github.com/nevindra/bites/internal/wire"
)

// Direction says which of the two plans Describe mirrors. The decoder and the encoder accept
// different types (a pointer encodes, as null for nil, but is refused for decoding), so a
// description must follow the plan the value will travel through.
type Direction uint8

const (
	// Decoding mirrors CompileDecoder: what a client may send.
	Decoding Direction = iota + 1
	// Encoding mirrors CompileEncoder: what a handler may return.
	Encoding
)

// Kind is the wire shape of one type.
type Kind uint8

const (
	KindBool Kind = iota + 1
	KindInt
	KindUint
	KindFloat
	KindString
	KindEnum
	KindUUID
	KindTime
	KindDate
	KindBytes    // []byte or [N]byte, written as a base64 string
	KindText     // the type has its own text form and no JSON method: a string
	KindDeclared // the app declared the schema of a type that writes or reads itself (Shape.Declared)
	KindAny      // the type writes or reads itself, or is an interface: the shape is unknown
	KindStruct   // Fields
	KindSlice    // Elem
	KindArray    // Elem, Len
	KindMap      // Elem, string keys
	KindPtr      // Elem, encoding only
	KindOpt      // Elem
	KindNull     // Elem
	KindUnion    // Fields, one a variant: Name is its tag and Type its struct; Tag is the discriminator
)

// Field is one wire field of a struct after inlining embedded structs, in the order the
// codec reads and writes them.
type Field struct {
	Name      string // the wire name, tag or policy
	GoPath    string
	Type      reflect.Type
	OmitEmpty bool
	OmitZero  bool
}

// Shape describes one type, one level deep, as the codec compiled it.
type Shape struct {
	Kind   Kind
	Bits   int          // KindInt, KindUint, KindFloat: 8, 16, 32 or 64
	Enum   []string     // KindEnum
	Elem   reflect.Type // KindSlice, KindArray, KindMap, KindPtr, KindOpt, KindNull
	Len    int          // KindArray, KindBytes of an array (0 for a slice)
	Fields []Field      // KindStruct

	// Constraint is a constraint type's check (ADR 0021), for KindString and KindInt when
	// describing a Decoding plan (KindFloat too); its Kind is ConNone otherwise.
	Constraint wire.Constraint

	// Items is a list type's bound (ADR 0021) for KindSlice, when describing a Decoding plan;
	// Format is the type's Format method for a string kind, in both directions.
	Items  *wire.Items
	Format string

	// Members is a map type's bound (ADR 0021) for KindMap, when describing a Decoding plan.
	Members *wire.Members

	// Declared is the schema WithJSONSchema gave a type that writes or reads its own JSON,
	// for KindDeclared.
	Declared wire.JSONSchema

	// Tag is the member that names a KindUnion's variant (its Discriminator).
	Tag string
}

// Describe reports how the codec treats t, using the same classification, the same field
// collection and the same wire names as CompileDecoder or CompileEncoder, so that a document
// built from it cannot disagree with the wire. It looks one level deep: the caller recurses
// through Elem and Field.Type. A type the plan refuses is refused here with the same reason
// for what Describe can see at its level; CompileDecoder or CompileEncoder on the root checks
// the whole tree. It is for Registration and is not used on the request path.
func Describe(t reflect.Type, o Options, dir Direction) (Shape, error) {
	o = o.withDefaults()
	path := typeName(t)
	var itemsOfType *wire.Items
	var membersOfType *wire.Members
	// Format annotates the type in both directions: it enforces nothing, so a response
	// states what a request states (ADR 0021). The other rules are read only when decoding.
	formatOfType, err := wire.FormatOf(t)
	if err != nil {
		return Shape{}, refusal(path, "%v", err)
	}
	if kind, elem, _, _ := wire.PresenceOf(t); kind != wire.None {
		if kind == wire.Optional {
			return Shape{Kind: KindOpt, Elem: elem}, nil
		}
		return Shape{Kind: KindNull, Elem: elem}, nil
	}
	switch t {
	case timeType:
		return Shape{Kind: KindTime}, nil
	case uuidType:
		return Shape{Kind: KindUUID}, nil
	case dateType:
		return Shape{Kind: KindDate}, nil
	}
	if o.Binary != nil && o.Binary(t) {
		return Shape{Kind: KindDeclared, Declared: wire.JSONSchema{Type: "string", Format: "binary"}}, nil
	}
	if js, ok := o.JSONSchemas[t]; ok {
		return Shape{Kind: KindDeclared, Declared: js}, nil
	}
	if f, ok := o.Formats[t]; ok {
		formatOfType = f
	}
	if dir == Decoding {
		if t.Kind() == reflect.Pointer {
			return Shape{}, refusal(path, "a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]")
		}
		if t.Kind() == reflect.Interface {
			return Shape{}, refusal(path, "a field of interface type cannot be decoded")
		}
		format, err := wire.FormatOf(t)
		if err != nil {
			return Shape{}, refusal(path, "%v", err)
		}
		items, hasItems, err := wire.ItemsOf(t)
		if err != nil {
			return Shape{}, refusal(path, "%v", err)
		}
		members, hasMembers, err := wire.MembersOf(t)
		if err != nil {
			return Shape{}, refusal(path, "%v", err)
		}
		if hasMembers {
			if hasUnmarshaler(t) || isByteSeq(t) {
				return Shape{}, refusal(path, "%s has a Members method, but it decodes itself, so its members are not counted", t)
			}
			membersOfType = &members
		}
		pat, err := wire.PatternOf(t)
		if err != nil {
			return Shape{}, refusal(path, "%v", err)
		}
		if fr, _ := wire.FormatRuleOf(t); fr != nil && (hasUnmarshaler(t) || isByteSeq(t)) {
			return Shape{}, refusal(path, "%s has a Format method naming %q, which the framework checks, but it decodes itself, so its format would not be checked", t, fr.Name)
		}
		if pat != nil && (hasUnmarshaler(t) || isByteSeq(t)) {
			return Shape{}, refusal(path, "%s has a Pattern method, but it decodes itself, so its pattern would not be checked", t)
		}
		if hasItems {
			if hasUnmarshaler(t) || isByteSeq(t) {
				return Shape{}, refusal(path, "%s has %s, but it decodes itself, so its items are not counted", t, wire.ItemsRuleName(items))
			}
			itemsOfType = &items
		}
		if f, ok := o.Formats[t]; ok {
			format = f
		}
		formatOfType = format
		if hasUnmarshaler(t) {
			return Shape{Kind: delegateKind(t, true), Format: format}, nil
		}
	} else if t.Kind() == reflect.Interface {
		return Shape{Kind: KindAny}, nil
	} else if hasMarshaler(t) {
		return Shape{Kind: delegateKind(t, false), Format: formatOfType}, nil
	}
	if isByteSeq(t) {
		s := Shape{Kind: KindBytes}
		if t.Kind() == reflect.Array {
			s.Len = t.Len()
		}
		return s, nil
	}
	var con wire.Constraint
	if dir == Decoding {
		var err error
		if con, err = wire.ConstraintOf(t); err != nil {
			return Shape{}, refusal(path, "%v", err)
		}
	}
	switch k := t.Kind(); k {
	case reflect.Bool:
		return Shape{Kind: KindBool}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return Shape{Kind: KindInt, Bits: int(t.Size()) * 8, Constraint: con}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Shape{Kind: KindUint, Bits: int(t.Size()) * 8}, nil
	case reflect.Float32, reflect.Float64:
		return Shape{Kind: KindFloat, Bits: int(t.Size()) * 8, Constraint: con}, nil
	case reflect.String:
		vals, has, err := valuesOf(t)
		if err != nil {
			if dir == Decoding {
				return Shape{}, refusal(path, "%v", err)
			}
			return Shape{Kind: KindString}, nil // the encoder never reads Values
		}
		if has {
			return Shape{Kind: KindEnum, Enum: vals, Format: formatOfType}, nil
		}
		return Shape{Kind: KindString, Constraint: con, Format: formatOfType}, nil
	case reflect.Pointer:
		return Shape{Kind: KindPtr, Elem: t.Elem()}, nil // dir is Encoding here
	case reflect.Struct:
		if isUnion(t) {
			tag, vs, err := unionOf(t, o, path)
			if err != nil {
				return Shape{}, err
			}
			s := Shape{Kind: KindUnion, Tag: tag, Fields: make([]Field, 0, len(vs))}
			for _, v := range vs {
				if v.serverOnly { // a variant only the server carries is not described to clients (ADR 0083)
					continue
				}
				s.Fields = append(s.Fields, Field{Name: v.tag, GoPath: v.goPath, Type: v.typ})
			}
			return s, nil
		}
		specs, err := collectFields(t, o, path, dir == Decoding)
		if err != nil {
			return Shape{}, err
		}
		s := Shape{Kind: KindStruct, Fields: make([]Field, len(specs))}
		for i, sp := range specs {
			s.Fields[i] = Field{Name: sp.name, GoPath: sp.goPath, Type: sp.typ, OmitEmpty: sp.omitEmpty, OmitZero: sp.omitZero}
		}
		return s, nil
	case reflect.Slice:
		return Shape{Kind: KindSlice, Elem: t.Elem(), Items: itemsOfType}, nil
	case reflect.Array:
		return Shape{Kind: KindArray, Elem: t.Elem(), Len: t.Len()}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String || hasOwnJSON(t.Key()) {
			return Shape{}, refusal(path, "a map key must be a string type, %s is not", t.Key())
		}
		if dir == Decoding {
			if kc, _ := wire.ConstraintOf(t.Key()); kc.Kind != wire.ConNone {
				return Shape{}, refusal(path, "a map key cannot be %s: a key is not checked against a constraint, so the type would promise what nothing enforces", t.Key())
			}
		}
		return Shape{Kind: KindMap, Elem: t.Elem(), Members: membersOfType}, nil
	default:
		verb := "encoded"
		if dir == Decoding {
			verb = "decoded"
		}
		return Shape{}, refusal(path, "a field of kind %s cannot be %s", k, verb)
	}
}

// delegateKind says whether a type that handles its own JSON is known to be a string: it has
// a text method and no JSON one.
func delegateKind(t reflect.Type, decoding bool) Kind {
	p := reflect.PointerTo(t)
	if decoding {
		if !p.Implements(jsonUnmarshalerType) && !p.Implements(fromUnmarshalerType) && p.Implements(textUnmarshalerType) {
			return KindText
		}
		return KindAny
	}
	if !p.Implements(jsonMarshalerType) && !p.Implements(toMarshalerType) && p.Implements(textMarshalerType) {
		return KindText
	}
	return KindAny
}
