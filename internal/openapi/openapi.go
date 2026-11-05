// Package openapi turns a Go type into a JSON Schema 2020-12 schema, the dialect of OpenAPI
// 3.1, at Registration (ADR 0020). It is a pure function of the type and the naming policy
// and does not know about routes, parameters or responses: those are the document's job.
//
// It does not walk structs itself. Field lists, wire names, presence, enums and the
// classification of every type come from jsoncodec.Describe, which reads the same code the
// codec's decoder and encoder are compiled from, so the schema cannot name a field the wire
// does not carry. The root type is also compiled by the codec first, so a type the codec
// refuses is refused here with the codec's own message.
//
// Named structs and enums become components, so a repeated or recursive type is one schema and
// a $ref. A Registry collects them and names them when asked, once every type is known, so the
// names do not depend on the order types were added in.
package openapi

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Mode says which way a value travels, because the codec's two plans accept different types
// and a response field can be left out for reasons a request field cannot.
type Mode uint8

const (
	// Request is a body or section a client sends. Unknown members are refused, which the
	// schema states with additionalProperties: false.
	Request Mode = iota + 1
	// RequestOpen is a Request whose route accepts unknown members (the codec's AllowUnknown).
	RequestOpen
	// Response is a value a handler returns. A field with omitempty or omitzero is not
	// required, a pointer is nullable.
	Response
)

func (m Mode) String() string {
	switch m {
	case Request:
		return "Request"
	case RequestOpen:
		return "RequestOpen"
	case Response:
		return "Response"
	}
	return "Mode(" + strconv.Itoa(int(m)) + ")"
}

func (m Mode) dir() jsoncodec.Direction {
	if m == Response {
		return jsoncodec.Encoding
	}
	return jsoncodec.Decoding
}

// Schema is one JSON Schema object. Maps marshal with sorted keys, so a Schema is
// deterministic bytes. A Schema from Registry.Schema may hold references to components that
// are not named yet; pass it through Registry.Resolve before it goes into a document.
type Schema = map[string]any

// Options configure a Registry.
type Options struct {
	// Names is the app's naming policy (ADR 0011). Nil means wire.SnakeCase.
	Names wire.Policy
	// Body, when set, is asked about the root type of every Schema call and may return the
	// type to describe instead. The app uses it to see through Raw[T] (ADR 0044), which this
	// package cannot import.
	Body func(reflect.Type) (reflect.Type, bool)
	// Form, when set, is asked about the type of a request Body in BodySchema and returns the
	// struct to describe when the body is read as a form (Form[T], ADR 0044).
	Form func(reflect.Type) (reflect.Type, bool)
	// Descriptions, when set, is asked about every struct type and returns its field
	// descriptions by Go field name (ADR 0020). A name that is not a wire field of the struct
	// is a *DescriptionError, so a description cannot outlive its field.
	Descriptions func(reflect.Type) map[string]string
	// DeprecatedFields, when set, is asked about every struct type and returns the Go names of the
	// fields to mark deprecated (ADR 0020). A name that is not a wire field of the struct is a
	// *DescriptionError with Method "DeprecatedFields".
	DeprecatedFields func(reflect.Type) []string
	// Namer, when set, is asked once for each type that becomes a component, with its package
	// path and its default short name, and returns the component name or "" to keep the
	// default rules (names.go). The per-mode suffixes are added after it.
	Namer func(pkgPath, name string) string
	// Formats is the app's WithTextFormat registrations: a type listed here is described as a
	// string with the format given.
	Formats wire.Formats
	// JSONSchemas is the app's WithJSONSchema registrations: a type listed here is described
	// as the schema given.
	JSONSchemas wire.JSONSchemas
	// Files, when set, reports the types that are a file of a multipart form (ADR 0044): each is
	// described as {"type": "string", "format": "binary"}.
	Files func(reflect.Type) bool
}

// DescriptionError is a field description that names a field its struct does not have on the
// wire.
type DescriptionError struct {
	Type   reflect.Type
	Method string // the method that names the field; empty means Descriptions
	Name   string
	Fields []string // the Go names of the wire fields, in wire order
}

func (e *DescriptionError) Error() string {
	label := e.Type.Name()
	if label == "" {
		label = e.Type.String()
	}
	method := e.Method
	if method == "" {
		method = "Descriptions"
	}
	return fmt.Sprintf("%s.%s names %s, which is not a field of %s on the wire (its fields: %s)", label, method, e.Name, label, strings.Join(e.Fields, ", "))
}

// Media types a request body is described with.
const (
	MediaJSON = "application/json"
	MediaForm = "application/x-www-form-urlencoded"
)

// BodySchema describes the Body section of a route's Input: the schema of the body and the
// media type it is sent as. A form is an object with the same properties and rules as the JSON
// body of the same struct (a list is repeated keys, which is how OpenAPI's form style reads an
// array); any other body is JSON. m is Request or RequestOpen.
//
// input is the Input struct that holds the Body. When the body's type is an anonymous struct and
// the Input is named, the struct becomes a component called after the Input, Body appended
// (CreateUser's is CreateUserBody). Two Inputs that write the same anonymous struct hold one Go
// type, and a component named after one of them would name the other's body wrongly (an update's
// body called CreateRoomBody), so such a struct is written in place in each operation, where a
// client generator names it after the operation.
func (r *Registry) BodySchema(t reflect.Type, m Mode, input reflect.Type) (Schema, string, error) {
	media := MediaJSON
	if r.opt.Form != nil {
		if ft, ok := r.opt.Form(t); ok {
			t, media = ft, MediaForm
		}
	}
	if r.opt.Body != nil {
		if bt, ok := r.opt.Body(t); ok {
			t = bt
		}
	}
	if t.Kind() == reflect.Struct && t.Name() == "" && input != nil && input.Name() != "" {
		id := ident{pkg: input.PkgPath(), name: shortName(input) + "Body"}
		prev, ok := r.anon[t]
		if ok && prev != id {
			r.shared[t] = true
			r.final = nil
		}
		if !ok || id.name < prev.name {
			r.anon[t] = id
			r.final = nil
		}
	}
	s, err := r.Schema(t, m)
	return s, media, err
}

type key struct {
	t reflect.Type
	m Mode
}

type entry struct {
	k      key
	schema Schema
	forced bool // a named non-struct type that refers to itself
	done   bool
}

// Registry collects the components of the schemas built through it. It is not safe for
// concurrent use: Registration is single-threaded.
type Registry struct {
	opt     Options
	entries map[key]*entry
	named   map[key]*entry // components, a subset of entries
	final   *finalized
	anon    map[reflect.Type]ident // anonymous structs given a component name
	shared  map[reflect.Type]bool  // anonymous Bodies of several Inputs, written in place

	examples map[exampleKey]exampleResult // the Example methods read so far, by type
}

// New returns an empty Registry.
func New(o Options) *Registry {
	if o.Names == nil {
		o.Names = wire.SnakeCase
	}
	return &Registry{opt: o, entries: map[key]*entry{}, named: map[key]*entry{}, anon: map[reflect.Type]ident{}, shared: map[reflect.Type]bool{}}
}

func (r *Registry) codecOptions(m Mode) jsoncodec.Options {
	return jsoncodec.Options{Names: r.opt.Names, AllowUnknown: m == RequestOpen, Formats: r.opt.Formats, JSONSchemas: r.opt.JSONSchemas, Binary: r.opt.Files}
}

// Schema returns the schema of t for mode m. The codec compiles t first: whatever it refuses
// is returned as its error, unchanged. Components the schema needs are added to the Registry.
func (r *Registry) Schema(t reflect.Type, m Mode) (Schema, error) {
	if r.opt.Body != nil {
		if bt, ok := r.opt.Body(t); ok {
			t = bt
		}
	}
	o := r.codecOptions(m)
	var err error
	if m == Response {
		_, err = jsoncodec.CompileEncoder(t, o)
	} else {
		_, err = jsoncodec.CompileDecoder(t, o)
	}
	if err != nil {
		return nil, err
	}
	r.final = nil
	g := &gen{r: r, m: m, o: o, building: map[key]*entry{}}
	return g.schema(t)
}

type gen struct {
	r        *Registry
	m        Mode
	o        jsoncodec.Options
	building map[key]*entry
}

// refTo stands for "#/components/schemas/<name>" until the names are known.
type refTo struct{ k key }

func (g *gen) ref(k key) Schema { return Schema{"$ref": refTo{k}} }

// schema describes t.
func (g *gen) schema(t reflect.Type) (Schema, error) {
	sh, err := jsoncodec.Describe(t, g.o, g.m.dir())
	if err != nil {
		return nil, err
	}
	k := key{t, g.m}
	_, isAnon := g.r.anon[t]
	named := t.Name() != "" || isAnon
	switch sh.Kind {
	case jsoncodec.KindOpt, jsoncodec.KindNull, jsoncodec.KindPtr:
		inner, err := g.schema(sh.Elem)
		if err != nil {
			return nil, err
		}
		if sh.Kind == jsoncodec.KindOpt {
			return inner, nil
		}
		return nullable(inner), nil
	case jsoncodec.KindStruct, jsoncodec.KindEnum, jsoncodec.KindUnion:
		if !named {
			return g.build(t, sh)
		}
		return g.component(k, t, sh)
	case jsoncodec.KindSlice, jsoncodec.KindArray, jsoncodec.KindMap:
		if !named {
			return g.build(t, sh)
		}
		if e, ok := g.building[k]; ok {
			e.forced = true
			return g.ref(k), nil
		}
		e := &entry{k: k}
		g.building[k] = e
		s, err := g.build(t, sh)
		delete(g.building, k)
		if err != nil {
			return nil, err
		}
		if !e.forced {
			return s, nil
		}
		e.schema, e.done = s, true
		g.r.entries[k], g.r.named[k] = e, e
		return g.ref(k), nil
	}
	return g.build(t, sh)
}

// component builds a named type once per mode and returns a reference to it.
func (g *gen) component(k key, t reflect.Type, sh jsoncodec.Shape) (Schema, error) {
	if _, ok := g.r.named[k]; ok {
		return g.ref(k), nil
	}
	e := &entry{k: k}
	g.r.entries[k], g.r.named[k] = e, e // before the fields, so a recursive type finds itself
	s, err := g.build(t, sh)
	if err != nil {
		delete(g.r.entries, k)
		delete(g.r.named, k)
		return nil, err
	}
	e.schema, e.done = s, true
	return g.ref(k), nil
}

// build describes t by its kind and adds what the type states about itself: an example from its
// Example method (example.go).
func (g *gen) build(t reflect.Type, sh jsoncodec.Shape) (Schema, error) {
	s, err := g.buildKind(t, sh)
	if err != nil {
		return nil, err
	}
	raw, err := g.r.example(t, g.m == Response)
	if err != nil {
		return nil, err
	}
	if raw != nil {
		s["examples"] = []any{raw}
	}
	return s, nil
}

func (g *gen) buildKind(t reflect.Type, sh jsoncodec.Shape) (Schema, error) {
	switch sh.Kind {
	case jsoncodec.KindBool:
		return Schema{"type": "boolean"}, nil
	case jsoncodec.KindInt:
		s := intSchema(sh.Bits)
		if c := sh.Constraint; c.Kind == wire.ConMin {
			if c.NoMin {
				delete(s, "minimum")
			} else {
				s["minimum"] = c.MinInt
			}
			if c.HasMax {
				s["maximum"] = c.MaxInt
			}
			if c.MulInt != 0 {
				s["multipleOf"] = c.MulInt
			}
		}
		return s, nil
	case jsoncodec.KindUint:
		return uintSchema(sh.Bits), nil
	case jsoncodec.KindFloat:
		s := Schema{"type": "number", "format": "double"}
		if sh.Bits == 32 {
			s["format"] = "float"
		}
		if c := sh.Constraint; c.Kind == wire.ConFloat {
			lo, hi := c.FloatBounds()
			// An exclusive bound is exclusiveMinimum or exclusiveMaximum, a number, as OpenAPI 3.1
			// (JSON Schema 2020-12) states it.
			minKey, maxKey := "minimum", "maximum"
			if c.ExMin {
				minKey = "exclusiveMinimum"
			}
			if c.ExMax {
				maxKey = "exclusiveMaximum"
			}
			if !math.IsInf(lo, 0) {
				s[minKey] = lo
			}
			if !math.IsInf(hi, 0) {
				s[maxKey] = hi
			}
			if c.MulF != 0 {
				s["multipleOf"] = c.MulF
			}
		}
		return s, nil
	case jsoncodec.KindString, jsoncodec.KindText:
		s := Schema{"type": "string"}
		if c := sh.Constraint; c.Kind == wire.ConLength {
			s["minLength"] = c.MinLen
			if c.MaxLen >= 0 {
				s["maxLength"] = c.MaxLen
			}
		}
		if c := sh.Constraint; c.Pat != nil {
			s["pattern"] = c.Pat.Src
		}
		if sh.Format != "" {
			s["format"] = sh.Format
		}
		return s, nil
	case jsoncodec.KindEnum:
		vals := make([]any, len(sh.Enum))
		for i, v := range sh.Enum {
			vals[i] = v
		}
		s := Schema{"type": "string", "enum": vals}
		if sh.Format != "" {
			s["format"] = sh.Format
		}
		return s, nil
	case jsoncodec.KindUUID:
		return Schema{"type": "string", "format": "uuid"}, nil
	case jsoncodec.KindTime:
		return Schema{"type": "string", "format": "date-time"}, nil
	case jsoncodec.KindDate:
		return Schema{"type": "string", "format": "date"}, nil
	case jsoncodec.KindBytes:
		return Schema{"type": "string", "contentEncoding": "base64"}, nil
	case jsoncodec.KindAny:
		return Schema{}, nil
	case jsoncodec.KindDeclared:
		d := sh.Declared
		if d.Type == "any" {
			return Schema{}, nil
		}
		s := Schema{"type": d.Type}
		if d.Format != "" {
			s["format"] = d.Format
		}
		// Only a response can carry the null: the decoder refuses a null for a field that is not
		// a Null, before the type's own method is asked, so a request that sent it would be a 400.
		if d.Nullable && g.m == Response {
			return nullable(s), nil
		}
		return s, nil
	case jsoncodec.KindSlice, jsoncodec.KindArray:
		items, err := g.schema(sh.Elem)
		if err != nil {
			return nil, err
		}
		s := Schema{"type": "array", "items": items}
		if sh.Kind == jsoncodec.KindArray {
			s["minItems"], s["maxItems"] = sh.Len, sh.Len
		}
		if sh.Items != nil && sh.Items.Bounded {
			s["minItems"], s["maxItems"] = sh.Items.Min, sh.Items.Max
		}
		if sh.Items != nil && sh.Items.Dup != nil {
			s["uniqueItems"] = true
		}
		return s, nil
	case jsoncodec.KindMap:
		v, err := g.schema(sh.Elem)
		if err != nil {
			return nil, err
		}
		s := Schema{"type": "object", "additionalProperties": v}
		if sh.Members != nil {
			s["minProperties"], s["maxProperties"] = sh.Members.Min, sh.Members.Max
		}
		return s, nil
	case jsoncodec.KindStruct:
		return g.object(t, sh)
	case jsoncodec.KindUnion:
		return g.union(sh)
	}
	return nil, fmt.Errorf("openapi: %s: kind %d has no schema", t, sh.Kind)
}

// union is oneOf the variants, each its object with the discriminator as a required member of
// one constant value, and the discriminator named (ADR 0083). A variant is written inline rather
// than as a reference to its type, because a request's object refuses members it does not list,
// and the discriminator is a member of the variant only inside the union.
func (g *gen) union(sh jsoncodec.Shape) (Schema, error) {
	variants := make([]any, 0, len(sh.Fields))
	for _, f := range sh.Fields {
		vsh, err := jsoncodec.Describe(f.Type, g.o, g.m.dir())
		if err != nil {
			return nil, err
		}
		v, err := g.object(f.Type, vsh)
		if err != nil {
			return nil, err
		}
		props, _ := v["properties"].(Schema)
		if props == nil {
			props = Schema{}
		}
		withTag := Schema{sh.Tag: Schema{"type": "string", "const": f.Name}}
		for k, p := range props {
			withTag[k] = p
		}
		v["properties"] = withTag
		req, _ := v["required"].([]any)
		v["required"] = append([]any{sh.Tag}, req...)
		v["title"] = f.Name
		variants = append(variants, v)
	}
	return Schema{"oneOf": variants, "discriminator": Schema{"propertyName": sh.Tag}}, nil
}

func (g *gen) object(t reflect.Type, sh jsoncodec.Shape) (Schema, error) {
	s := Schema{"type": "object"}
	props := Schema{}
	var required []any
	descs, err := g.descriptions(t, sh)
	if err != nil {
		return nil, err
	}
	deprecated, err := g.deprecated(t, sh)
	if err != nil {
		return nil, err
	}
	for _, f := range sh.Fields {
		fs, err := g.schema(f.Type)
		if err != nil {
			return nil, err
		}
		if d, ok := descs[goName(f.GoPath)]; ok {
			fs["description"] = d
		}
		if deprecated[goName(f.GoPath)] {
			fs["deprecated"] = true
		}
		props[f.Name] = fs
		fsh, err := jsoncodec.Describe(f.Type, g.o, g.m.dir())
		if err != nil {
			return nil, err
		}
		// The decoder demands every field that is not an Opt. The encoder writes every field
		// that is not an empty Opt and not left out by omitempty or omitzero.
		if fsh.Kind == jsoncodec.KindOpt {
			continue
		}
		// A list of files may be empty (ADR 0044), unlike a list of text, which a form requires.
		if fsh.Kind == jsoncodec.KindSlice && g.o.Binary != nil && g.o.Binary(fsh.Elem) {
			continue
		}
		if g.m == Response && (f.OmitEmpty || f.OmitZero) {
			continue
		}
		required = append(required, f.Name)
	}
	if len(props) > 0 {
		s["properties"] = props
	}
	if len(required) > 0 {
		s["required"] = required
	}
	if g.m == Request {
		s["additionalProperties"] = false
	}
	return s, nil
}

// descriptions returns t's field descriptions, after checking that every name is a wire field.
func (g *gen) descriptions(t reflect.Type, sh jsoncodec.Shape) (map[string]string, error) {
	if g.r.opt.Descriptions == nil {
		return nil, nil
	}
	descs := g.r.opt.Descriptions(t)
	if len(descs) == 0 {
		return nil, nil
	}
	names := make([]string, len(sh.Fields))
	have := map[string]bool{}
	for i, f := range sh.Fields {
		names[i] = goName(f.GoPath)
		have[names[i]] = true
	}
	keys := make([]string, 0, len(descs))
	for k := range descs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !have[k] {
			return nil, &DescriptionError{Type: t, Name: k, Fields: names}
		}
	}
	return descs, nil
}

// deprecated returns the Go names of t's deprecated fields, after checking that every name is a
// wire field.
func (g *gen) deprecated(t reflect.Type, sh jsoncodec.Shape) (map[string]bool, error) {
	if g.r.opt.DeprecatedFields == nil {
		return nil, nil
	}
	names := g.r.opt.DeprecatedFields(t)
	if len(names) == 0 {
		return nil, nil
	}
	fields := make([]string, len(sh.Fields))
	have := map[string]bool{}
	for i, f := range sh.Fields {
		fields[i] = goName(f.GoPath)
		have[fields[i]] = true
	}
	out := map[string]bool{}
	for _, n := range slices.Sorted(slices.Values(names)) {
		if !have[n] {
			return nil, &DescriptionError{Type: t, Method: "DeprecatedFields", Name: n, Fields: fields}
		}
		out[n] = true
	}
	return out, nil
}

// goName is the last element of a field's Go path: the name its struct gives it.
func goName(goPath string) string {
	if i := strings.LastIndexByte(goPath, '.'); i >= 0 {
		return goPath[i+1:]
	}
	return goPath
}

func intSchema(bits int) Schema {
	switch bits {
	case 8, 16:
		return Schema{"type": "integer", "minimum": -(int64(1) << (bits - 1)), "maximum": int64(1)<<(bits-1) - 1}
	case 32:
		return Schema{"type": "integer", "format": "int32"}
	}
	return Schema{"type": "integer", "format": "int64"}
}

func uintSchema(bits int) Schema {
	var max json.Number
	if bits == 64 {
		max = json.Number(strconv.FormatUint(math.MaxUint64, 10))
	} else {
		max = json.Number(strconv.FormatUint(uint64(1)<<bits-1, 10))
	}
	return Schema{"type": "integer", "minimum": 0, "maximum": max}
}

// nullable lets a schema accept null as well, in the form 2020-12 gives it: "null" joins the
// type. A schema without a plain type (a reference, a union) is wrapped in anyOf, and the
// empty schema already accepts null.
func nullable(s Schema) Schema {
	if len(s) == 0 {
		return s
	}
	if ts, ok := s["type"].([]any); ok && slices.Contains(ts, any("null")) {
		return s // a declared type that already admits null (ADR 0076)
	}
	typ, ok := s["type"].(string)
	if !ok {
		return Schema{"anyOf": []any{s, Schema{"type": "null"}}}
	}
	out := Schema{}
	for k, v := range s {
		out[k] = v
	}
	out["type"] = []any{typ, "null"}
	if enum, ok := s["enum"].([]any); ok {
		out["enum"] = append(append([]any{}, enum...), nil)
	}
	return out
}
