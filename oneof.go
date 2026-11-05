package bites

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
)

// The sum types a handler returns when it answers with more than one type (ADR 0013). A OneOf
// holds one of its type arguments, chosen with the method of the same letter on any value of
// it, usually the zero one:
//
//	type DocAnswer = bites.OneOf2[bites.OK[Doc], bites.NotModified]
//
//	return DocAnswer{}.B(bites.NotModified{Header: h}), nil
//
// Registration compiles each type argument as if the handler returned it alone, and the API
// document lists each one's status and schema; the compiler checks that the handler returns
// nothing else. The value is held in the OneOf, not boxed, so a OneOf costs what a plain return
// costs. A OneOf with no variant chosen (its zero value) returned with a nil error is a 500, logged
// with the route. Go has no variadic type parameters, so there is one type for each count, up to
// four.

// OneOf2 is the return type of a handler that answers with A or B.
type OneOf2[A, B any] struct {
	which uint8 // 0: none chosen; 1 + the index of the variant otherwise
	a     A
	b     B
}

// A returns a OneOf2 that answers with v.
func (OneOf2[A, B]) A(v A) OneOf2[A, B] { return OneOf2[A, B]{which: 1, a: v} }

// B returns a OneOf2 that answers with v.
func (OneOf2[A, B]) B(v B) OneOf2[A, B] { return OneOf2[A, B]{which: 2, b: v} }

// Value returns the variant o holds, boxed in an interface, or nil when none is chosen. It is for
// a test that calls a handler directly; the engine reads a OneOf without it.
func (o OneOf2[A, B]) Value() any {
	switch o.which {
	case 1:
		return o.a
	case 2:
		return o.b
	}
	return nil
}

// OneOf3 is the return type of a handler that answers with A, B or C.
type OneOf3[A, B, C any] struct {
	which uint8
	a     A
	b     B
	c     C
}

// A returns a OneOf3 that answers with v.
func (OneOf3[A, B, C]) A(v A) OneOf3[A, B, C] { return OneOf3[A, B, C]{which: 1, a: v} }

// B returns a OneOf3 that answers with v.
func (OneOf3[A, B, C]) B(v B) OneOf3[A, B, C] { return OneOf3[A, B, C]{which: 2, b: v} }

// C returns a OneOf3 that answers with v.
func (OneOf3[A, B, C]) C(v C) OneOf3[A, B, C] { return OneOf3[A, B, C]{which: 3, c: v} }

// Value returns the variant o holds, boxed in an interface, or nil when none is chosen.
func (o OneOf3[A, B, C]) Value() any {
	switch o.which {
	case 1:
		return o.a
	case 2:
		return o.b
	case 3:
		return o.c
	}
	return nil
}

// OneOf4 is the return type of a handler that answers with A, B, C or D.
type OneOf4[A, B, C, D any] struct {
	which uint8
	a     A
	b     B
	c     C
	d     D
}

// A returns a OneOf4 that answers with v.
func (OneOf4[A, B, C, D]) A(v A) OneOf4[A, B, C, D] { return OneOf4[A, B, C, D]{which: 1, a: v} }

// B returns a OneOf4 that answers with v.
func (OneOf4[A, B, C, D]) B(v B) OneOf4[A, B, C, D] { return OneOf4[A, B, C, D]{which: 2, b: v} }

// C returns a OneOf4 that answers with v.
func (OneOf4[A, B, C, D]) C(v C) OneOf4[A, B, C, D] { return OneOf4[A, B, C, D]{which: 3, c: v} }

// D returns a OneOf4 that answers with v.
func (OneOf4[A, B, C, D]) D(v D) OneOf4[A, B, C, D] { return OneOf4[A, B, C, D]{which: 4, d: v} }

// Value returns the variant o holds, boxed in an interface, or nil when none is chosen.
func (o OneOf4[A, B, C, D]) Value() any {
	switch o.which {
	case 1:
		return o.a
	case 2:
		return o.b
	case 3:
		return o.c
	case 4:
		return o.d
	}
	return nil
}

// alt is one variant of a OneOf: where it sits in the OneOf and its plan.
type alt struct {
	t    reflect.Type
	off  uintptr
	plan *outPlan
}

// isOneOf reports whether t is one of the OneOf types.
func isOneOf(t reflect.Type) bool { return isOwn(t, "OneOf") }

// oneOfName is "OneOf2", "OneOf3" or "OneOf4": the name of a OneOf without its type arguments,
// which the messages name it by.
func oneOfName(t reflect.Type) string { return strings.SplitN(t.Name(), "[", 2)[0] }

// compileOneOf builds the plan of a OneOf (ADR 0013): each variant is compiled as if the handler
// returned it alone, and a variant that could not be a handler's return, or that repeats another,
// is refused.
func (a *App) compileOneOf(t reflect.Type, method string) (*outPlan, error) {
	name := oneOfName(t)
	p := &outPlan{oneOf: name}
	for i := 1; i < t.NumField(); i++ {
		f := t.Field(i)
		vt := f.Type
		letter := string(rune('A' + i - 1))
		label := fmt.Sprintf("variant %s of the %s", letter, name)
		switch {
		case isOneOf(vt):
			return nil, refusalf("%s is itself a %s: list its types in the outer OneOf, which has room for up to four", label, oneOfName(vt))
		case vt.Kind() == reflect.Interface:
			return nil, refusalf("%s is %s, an interface: a OneOf names the concrete types the handler returns", label, vt)
		case vt.Kind() == reflect.Pointer:
			return nil, refusalf("%s is %s, a pointer: name %s, which the OneOf holds by value", label, vt, vt.Elem())
		}
		for j, prev := range p.alts {
			if prev.t == vt {
				return nil, refusalf("variants %c and %s of the %s are both %s: each variant is a different answer, so name each type once", 'A'+j, letter, name, vt)
			}
		}
		vp, err := a.compileOutput(vt)
		if err != nil {
			return nil, refusalf("%s, %s: %v", label, vt, err)
		}
		if vp.wk >= wrapStream && method == "HEAD" {
			return nil, refusalf("%s, %s: a HEAD route cannot return a stream: its answer has no body to stream", label, vt)
		}
		p.alts = append(p.alts, alt{t: vt, off: f.Offset, plan: vp})
	}
	return p, nil
}

// writeOneOf answers with the variant the OneOf at src holds, from where it sits in the OneOf: no
// copy and no boxing. A OneOf with none chosen is a logged 500.
func (p *outPlan) writeOneOf(a *App, ex *engine.Exchange, src unsafe.Pointer, ctx context.Context) {
	w := *(*uint8)(src)
	if w == 0 || int(w) > len(p.alts) {
		a.internalError(ex, refusalf("the handler returned a %s with no variant chosen and no error: choose one with the method of its letter, as in %s{}.A(v)", p.oneOf, p.oneOf))
		return
	}
	al := &p.alts[w-1]
	al.plan.write(a, ex, unsafe.Add(src, al.off), ctx)
}

// findOneOf returns the first OneOf t holds where JSON would read or write it (t itself, an
// element, an exported field), or nil. A OneOf's fields are unexported, so the codec would write
// it as {} and read any object into a zero one: it is only ever a handler's whole return type.
func findOneOf(t reflect.Type, seen map[reflect.Type]bool) reflect.Type {
	if isOneOf(t) {
		return t
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return findOneOf(t.Elem(), seen)
	case reflect.Map:
		if o := findOneOf(t.Key(), seen); o != nil {
			return o
		}
		return findOneOf(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() || f.Anonymous {
				if o := findOneOf(f.Type, seen); o != nil {
					return o
				}
			}
		}
	}
	return nil
}
