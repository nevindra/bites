// Package jsoncodec is the JSON codec of ADR 0015: each Input and Output type is compiled
// once, at Registration, into a plan (a tree of nodes holding wire names, kinds and byte
// offsets), and decoding and encoding run through the plan with no reflection per request
// on the hot path.
//
// Why not encoding/json/v2 alone: v2 has no naming policy, stops at the first semantic error,
// copies every decoded string and moves the destination to the heap (probes in
// docs/research/facts). What stays in the standard library is everything about the grammar:
// tokens come from encoding/json/jsontext, which checks the syntax, UTF-8 and duplicate
// member names, so this package owns only the meaning of a value, never what JSON is.
//
// Allocation: the decoder keeps a pooled jsontext.Decoder reading straight from the body
// (it parses a *bytes.Buffer in place). The pool holds only tokenizer state, which never
// reaches user code (ADR 0008). Strings without escapes point into the body, which lives in
// the request's slab (ADR 0008) and is never modified afterwards; escaped strings are
// unescaped into a pooled scratch and copied into the slab. A struct of strings, numbers,
// bools, enums, uuids, nested structs, Opt and Null decodes with no allocation.
//
// Slices are the exception: a slice's backing array holds pointers, so it cannot live in the
// slab (a pointer-free byte buffer the collector does not scan), and it is grown with the
// reflect machinery, one allocation per growth and none when the destination already has
// the capacity. ADR 0003 does not count that allocation; see the report that came with this
// package.
//
// The plan is walked with static calls and switches, not stored closures, so that the
// destination pointer does not escape through an unknown callee and a caller's Input can
// stay on its stack.
package jsoncodec

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/nevindra/bites/internal/wire"
)

// Options configure a compiled plan. The zero value is usable.
type Options struct {
	// Names turns a Go field name into a wire name for fields without a json tag name
	// (ADR 0011). Nil means wire.SnakeCase.
	Names wire.Policy
	// AllowUnknown accepts body members the type does not have. The default refuses them
	// with "unknown field" (ADR 0015).
	AllowUnknown bool
	// MaxDepth is the deepest nesting of objects and arrays a body may have. Default 64.
	MaxDepth int
	// MaxErrors bounds the semantic errors one Errors holds. Default 20 (ADR 0012).
	MaxErrors int
	// Formats names the format of a type the app registered with WithTextFormat. A listed type
	// is described with that format in both directions, and a request value that does not decode
	// into it is told the format's sentence instead of "expected a valid value".
	Formats wire.Formats
	// JSONSchemas names the schema of a type the app registered with WithJSONSchema: a type that
	// writes or reads its own JSON. A type like that with no entry here, and not json.RawMessage
	// or jsontext.Value, is refused (ADR 0076).
	JSONSchemas wire.JSONSchemas
	// Undescribed is a plan for bytes no API document describes, the payload a Topic hands its
	// broker (ADR 0081): a type that writes or reads its own JSON is delegated to with no
	// WithJSONSchema registration, since there is no document for it to be opaque in; a pointer
	// is read (null and an absent member are nil); and a member the payload left out is the zero
	// value, as the encoder left out an empty Opt or an omitempty member, so nothing is required.
	Undescribed bool
	// ServerVariants is a plan for bytes only servers read and write, the payload a Topic hands
	// its broker (ADR 0083): the variants of a union that its ServerOnly method names are
	// encoded and decoded as any other. Without it an encoder refuses a value that holds one with
	// a *ServerOnlyError and a decoder reads the tag as one the union does not have, so a client
	// can neither be sent nor send a variant that is only the server's. The zero value is a plan
	// for clients.
	ServerVariants bool
	// Binary reports a type that is a file of a multipart form (ADR 0044). The JSON codec never
	// reads or writes one; the API document describes it as a string with format binary. Nil means
	// no type is.
	Binary func(reflect.Type) bool
}

func (o Options) withDefaults() Options {
	if o.Names == nil {
		o.Names = wire.SnakeCase
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 64
	}
	if o.MaxErrors <= 0 {
		o.MaxErrors = 20
	}
	return o
}

// FieldError is one client-facing error. Location is in wire names from the body root,
// written as the caller will prefix it ("items[2].qty", and "" for the root). Message is one
// of a small fixed set of sentences and never contains the client's value.
//
// Rule is the machine-readable code of the sentence (ADR 0012), empty when it has none; Min and
// Max are the bounds of a "length", "range", "items" or "members" rule and zero for every other; a
// "multiple" rule holds its step in Min, or in MinF when Float is set.
type FieldError struct {
	Location, Message, Rule string
	Min, Max                int64
	// Float says the bounds of a "range" rule are MinF and MaxF (-Inf or +Inf for a side the
	// type does not bound), not Min and Max.
	Float      bool
	MinF, MaxF float64
	// ExMin and ExMax say that MinF or MaxF is an exclusive bound of a float "range" rule.
	ExMin, ExMax bool
	// Format names the format of a "format" rule, empty for every other.
	Format string
}

// Errors collects FieldErrors. The caller reuses one across requests; Decode appends to
// List. Truncated is set when more errors were found than MaxErrors allowed.
type Errors struct {
	List      []FieldError
	Truncated bool
}

// Reset empties e, keeping its capacity.
func (e *Errors) Reset() {
	clear(e.List)
	e.List = e.List[:0]
	e.Truncated = false
}

// The fixed sentences. The scalar ones are the text of wire's sentences (ADR 0042), so a
// query parameter and a body field fail with the same words; a test compares them.
const (
	msgRequired   = "required"
	msgNull       = "must not be null"
	msgUnknown    = "unknown field"
	msgObject     = "expected an object"
	msgArray      = "expected an array"
	msgString     = "expected a string"
	msgNumber     = "expected a number"
	msgBool       = "expected true or false"
	msgInteger    = "expected an integer"
	msgNonNegInt  = "expected a non-negative integer"
	msgTime       = "expected an RFC 3339 time with an offset"
	msgUUID       = "expected a UUID"
	msgDate       = "expected a date as YYYY-MM-DD"
	msgValid      = "expected a valid value"
	msgEnumPrefix = "expected one of: "
)

// ErrDepth is returned by Decode when the body nests deeper than Options.MaxDepth.
var ErrDepth = errors.New("jsoncodec: JSON nested too deeply")

// SyntaxError is returned by Decode for a body that is not JSON. Its text is a fixed
// sentence plus the byte offset: the underlying error may quote the client's bytes, so it is
// kept for errors.Unwrap and logs, never for the client.
type SyntaxError struct {
	Offset int64
	msg    string
	cause  error
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s at byte %d", e.msg, e.Offset)
}

// Unwrap returns the tokenizer's own error.
func (e *SyntaxError) Unwrap() error { return e.cause }

// refusal builds a Registration refusal reason: the Go path, then the reason.
func refusal(path, format string, args ...any) error {
	return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
}

// joinPath extends a Go path for messages: Order, Order.Items, Order.Items[].Price.
func refusalf(format string, args ...any) error { return fmt.Errorf(format, args...) }

func joinPath(path, field string) string { return path + "." + field }

func typeName(t reflect.Type) string {
	if n := t.Name(); n != "" {
		return n
	}
	return t.String()
}

// fieldSpec is one wire field after inlining embedded structs.
type fieldSpec struct {
	goPath    string
	name      string
	offset    uintptr
	typ       reflect.Type
	omitEmpty bool
	omitZero  bool
}

// collectFields lists the wire fields of struct t in Go order, inlining embedded structs and
// applying the tag and the policy. The rules are those of ADR 0015 and ADR 0011: a tag name
// wins over the policy, "-" skips, omitempty and omitzero are the only options, and two
// fields with one wire name are refused (v2 and v1 both resolve this silently by depth, which
// hides a real mistake).
func collectFields(t reflect.Type, o Options, path string, decoding bool) ([]fieldSpec, error) {
	var out []fieldSpec
	byName := map[string]string{}
	var walk func(t reflect.Type, base uintptr, path string) error
	walk = func(t reflect.Type, base uintptr, path string) error {
		for i := range t.NumField() {
			f := t.Field(i)
			fpath := joinPath(path, f.Name)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			var omitEmpty, omitZero bool
			if opts != "" {
				for opt := range strings.SplitSeq(opts, ",") {
					switch opt {
					case "omitempty":
						omitEmpty = true
					case "omitzero":
						omitZero = true
					default:
						return refusal(fpath, "json tag option %q is not supported (only omitempty and omitzero)", opt)
					}
				}
			}
			if f.Anonymous && name == "" {
				ft := f.Type
				if ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct {
					return refusal(fpath, "an embedded pointer to a struct cannot be inlined, embed %s by value", ft.Elem().Name())
				}
				if k, _, _, _ := wire.PresenceOf(ft); k != wire.None {
					return refusal(fpath, "an embedded %s is not supported, give the field a name", ft.Name())
				}
				if Inlined(f) {
					if err := walk(ft, base+f.Offset, fpath); err != nil {
						return err
					}
					continue
				}
			}
			if !f.IsExported() {
				if decoding {
					return refusal(fpath, "it is unexported, so nothing can fill it: give it an exported name, or tag it json:\"-\" to leave it out on purpose")
				}
				continue
			}
			if name == "" {
				name = o.Names(f.Name)
			}
			if prev, dup := byName[name]; dup {
				return refusal(fpath, "wire name %q is also the wire name of %s", name, prev)
			}
			byName[name] = fpath
			out = append(out, fieldSpec{
				goPath: fpath, name: name, offset: base + f.Offset, typ: f.Type,
				omitEmpty: omitEmpty, omitZero: omitZero,
			})
		}
		return nil
	}
	if err := walk(t, 0, path); err != nil {
		return nil, err
	}
	return out, nil
}
