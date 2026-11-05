package openapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// ExampleError is an Example method that cannot be used: it has the wrong signature, it panics,
// its value cannot be written, or its value is one its own type refuses (ADR 0020).
type ExampleError struct {
	Type   reflect.Type
	Reason string
}

func (e *ExampleError) Error() string {
	label := e.Type.Name()
	if label == "" {
		label = e.Type.String()
	}
	return label + ".Example " + e.Reason
}

type exampleKey struct {
	t        reflect.Type
	response bool
}

type exampleResult struct {
	raw json.RawMessage // nil when the type has no Example method
	err error
}

// example returns the JSON of t's Example method, nil when t has none. A named type may declare
// func (T) Example() T; the value is written once, at Registration, with the codec the wire uses,
// and read back through the codec's decoder, so a value the type's own rules refuse (Length,
// Range, Items, Pattern, Format, Check, a missing required member) is an error here, at the line
// that registered the route, and not a document whose example a client would be refused for.
// response is true when the schema being built is a response's: its value is read back by a plan
// in which nothing is required (an omitempty member may be left out), but every rule of a type
// still applies. A type the decoder cannot read is checked by writing it only.
func (r *Registry) example(t reflect.Type, response bool) (json.RawMessage, error) {
	if t.Name() == "" {
		return nil, nil
	}
	k := exampleKey{t, response}
	if res, ok := r.examples[k]; ok {
		return res.raw, res.err
	}
	raw, err := r.buildExample(t, response)
	if r.examples == nil {
		r.examples = map[exampleKey]exampleResult{}
	}
	r.examples[k] = exampleResult{raw, err}
	return raw, err
}

func (r *Registry) buildExample(t reflect.Type, response bool) (raw json.RawMessage, err error) {
	fn, ok, err := exampleMethod(t)
	if err != nil || !ok {
		return nil, err
	}
	fail := func(format string, args ...any) (json.RawMessage, error) {
		return nil, &ExampleError{Type: t, Reason: fmt.Sprintf(format, args...)}
	}
	defer func() {
		if p := recover(); p != nil {
			raw, err = fail("panicked: %v", p)
		}
	}()
	recv := reflect.New(t)
	v := fn.Func.Call([]reflect.Value{recv})[0]
	p := reflect.New(t)
	p.Elem().Set(v)
	enc, cerr := jsoncodec.CompileEncoder(t, r.codecOptions(Response))
	if cerr != nil {
		return fail("returns a value the codec cannot write: %v: a type of a multipart form has no JSON example", cerr)
	}
	b, werr := enc.Append(nil, p.UnsafePointer())
	if werr != nil {
		return fail("returns a value the codec cannot write: %v", werr)
	}
	o := r.codecOptions(Request)
	if response {
		o = jsoncodec.Options{Names: r.opt.Names, Formats: r.opt.Formats, JSONSchemas: r.opt.JSONSchemas, Undescribed: true}
	}
	dec, derr := jsoncodec.CompileDecoder(t, o)
	if derr != nil {
		return json.RawMessage(b), nil
	}
	var slab wire.Slab
	slab.Adopt(b, len(b))
	var errs jsoncodec.Errors
	dst := reflect.New(t)
	if err := dec.Decode(b, unsafe.Pointer(dst.UnsafePointer()), &slab, &errs); err != nil {
		return fail("returns %s, which the codec cannot read back: %v", clip(b), err)
	}
	if len(errs.List) > 0 {
		e := errs.List[0]
		at := ""
		if e.Location != "" {
			at = " at " + e.Location
		}
		return fail("returns %s, which its own type refuses%s: %s: change the value so the type accepts it", clip(b), at, e.Message)
	}
	return json.RawMessage(b), nil
}

func clip(b []byte) string {
	if len(b) > 80 {
		return string(b[:80]) + "..."
	}
	return string(b)
}

// exampleMethod finds func (T) Example() T on t, whether declared on T or on *T. A method named
// Example that returns another type is refused, unless t only has it promoted from an embedded
// struct, whose own example it is.
func exampleMethod(t reflect.Type) (reflect.Method, bool, error) {
	m, ok := reflect.PointerTo(t).MethodByName("Example")
	if !ok {
		return reflect.Method{}, false, nil
	}
	if m.Type.NumIn() == 1 && m.Type.NumOut() == 1 && m.Type.Out(0) == t {
		return m, true, nil
	}
	if m.Type.NumIn() == 1 && m.Type.NumOut() == 1 && promotedExample(t, m.Type.Out(0)) {
		return reflect.Method{}, false, nil
	}
	sig := strings.TrimPrefix(m.Type.String(), "func(")
	_, sig, _ = strings.Cut(sig, ")")
	label := t.Name()
	return reflect.Method{}, false, &ExampleError{Type: t, Reason: fmt.Sprintf("has the signature func()%s, but an example is a method that returns the type's own value: write func (%s) Example() %[2]s, or rename the method", sig, label)}
}

// promotedExample reports whether an embedded struct of t is the type out and declares the
// Example method that t promotes.
func promotedExample(t, out reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.Anonymous {
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft == out {
			return true
		}
		if promotedExample(ft, out) {
			return true
		}
	}
	return false
}
