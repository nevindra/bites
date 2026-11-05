package jsoncodec

import (
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

// wmap maps a codec type onto the "wire type" that encoding/json/v2 can handle by itself: a
// struct rebuilt with reflect.StructOf and json tags carrying the policy names (the trick
// proven in docs/research/facts/q1-naming). Embedded structs are flattened here by hand, Opt
// becomes a pointer with omitempty and Null a pointer, because v2 knows neither. toWire and
// fromWire copy a value across.
type wmap struct {
	orig, w reflect.Type
	kind    wkind
	fields  []wfield // struct
	elem    *wmap    // slice, array, map, ptr, opt, null
	vOff    uintptr
	fOff    uintptr
}

type wkind int

const (
	wSame wkind = iota
	wStruct
	wSlice
	wArray
	wMap
	wPtr
	wOpt
	wNull
)

type wfield struct {
	index []int // in the original struct, through embedded structs
	m     *wmap
}

func buildWire(t reflect.Type, names wire.Policy) *wmap {
	if kind, elem, vOff, fOff := wire.PresenceOf(t); kind != wire.None {
		inner := buildWire(elem, names)
		m := &wmap{orig: t, w: reflect.PointerTo(inner.w), elem: inner, vOff: vOff, fOff: fOff}
		m.kind = wOpt
		if kind == wire.Nullable {
			m.kind = wNull
		}
		return m
	}
	switch {
	case t == timeType || t == uuidType:
		return &wmap{orig: t, w: t}
	}
	switch t.Kind() {
	case reflect.Struct:
		m := &wmap{orig: t, kind: wStruct}
		var sfs []reflect.StructField
		var add func(t reflect.Type, prefix []int)
		add = func(t reflect.Type, prefix []int) {
			for i := range t.NumField() {
				f := t.Field(i)
				idx := append(append([]int(nil), prefix...), i)
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
					add(f.Type, idx)
					continue
				}
				if !f.IsExported() {
					continue
				}
				if name == "" {
					name = names(f.Name)
				}
				fm := buildWire(f.Type, names)
				if fm.kind == wOpt {
					opts = strings.TrimPrefix(opts+",omitzero", ",")
				}
				jt := name
				if opts != "" {
					jt += "," + opts
				}
				sfs = append(sfs, reflect.StructField{
					Name: "F" + strconv.Itoa(len(sfs)), Type: fm.w,
					Tag: reflect.StructTag(`json:"` + strings.ReplaceAll(jt, `"`, `\"`) + `"`),
				})
				m.fields = append(m.fields, wfield{index: idx, m: fm})
			}
		}
		add(t, nil)
		m.w = reflect.StructOf(sfs)
		return m
	case reflect.Slice:
		if isByteSeq(t) {
			return &wmap{orig: t, w: t}
		}
		e := buildWire(t.Elem(), names)
		return &wmap{orig: t, w: reflect.SliceOf(e.w), kind: wSlice, elem: e}
	case reflect.Array:
		e := buildWire(t.Elem(), names)
		return &wmap{orig: t, w: reflect.ArrayOf(t.Len(), e.w), kind: wArray, elem: e}
	case reflect.Map:
		e := buildWire(t.Elem(), names)
		return &wmap{orig: t, w: reflect.MapOf(t.Key(), e.w), kind: wMap, elem: e}
	case reflect.Pointer:
		e := buildWire(t.Elem(), names)
		return &wmap{orig: t, w: reflect.PointerTo(e.w), kind: wPtr, elem: e}
	}
	return &wmap{orig: t, w: t}
}

// toWire copies v (of m.orig) into a new value of m.w.
func (m *wmap) toWire(v reflect.Value) reflect.Value {
	switch m.kind {
	case wStruct:
		out := reflect.New(m.w).Elem()
		for i, f := range m.fields {
			out.Field(i).Set(f.m.toWire(v.FieldByIndex(f.index)))
		}
		return out
	case wSlice:
		if v.IsNil() {
			return reflect.Zero(m.w)
		}
		out := reflect.MakeSlice(m.w, v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(m.elem.toWire(v.Index(i)))
		}
		return out
	case wArray:
		out := reflect.New(m.w).Elem()
		for i := range v.Len() {
			out.Index(i).Set(m.elem.toWire(v.Index(i)))
		}
		return out
	case wMap:
		if v.IsNil() {
			return reflect.Zero(m.w)
		}
		out := reflect.MakeMapWithSize(m.w, v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), m.elem.toWire(it.Value()))
		}
		return out
	case wPtr:
		if v.IsNil() {
			return reflect.Zero(m.w)
		}
		out := reflect.New(m.elem.w)
		out.Elem().Set(m.elem.toWire(v.Elem()))
		return out
	case wOpt, wNull:
		p := reflect.New(m.orig)
		p.Elem().Set(v)
		base := p.UnsafePointer()
		if !*(*bool)(unsafe.Add(base, m.fOff)) {
			return reflect.Zero(m.w)
		}
		inner := reflect.NewAt(m.elem.orig, unsafe.Add(base, m.vOff)).Elem()
		out := reflect.New(m.elem.w)
		out.Elem().Set(m.elem.toWire(inner))
		return out
	}
	return v
}

// fromWire copies w (of m.w) into a new value of m.orig.
func (m *wmap) fromWire(w reflect.Value) reflect.Value {
	switch m.kind {
	case wStruct:
		out := reflect.New(m.orig).Elem()
		for i, f := range m.fields {
			out.FieldByIndex(f.index).Set(f.m.fromWire(w.Field(i)))
		}
		return out
	case wSlice:
		if w.IsNil() {
			return reflect.Zero(m.orig)
		}
		out := reflect.MakeSlice(m.orig, w.Len(), w.Len())
		for i := range w.Len() {
			out.Index(i).Set(m.elem.fromWire(w.Index(i)))
		}
		return out
	case wArray:
		out := reflect.New(m.orig).Elem()
		for i := range w.Len() {
			out.Index(i).Set(m.elem.fromWire(w.Index(i)))
		}
		return out
	case wMap:
		if w.IsNil() {
			return reflect.Zero(m.orig)
		}
		out := reflect.MakeMapWithSize(m.orig, w.Len())
		for it := w.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), m.elem.fromWire(it.Value()))
		}
		return out
	case wPtr:
		if w.IsNil() {
			return reflect.Zero(m.orig)
		}
		out := reflect.New(m.elem.orig)
		out.Elem().Set(m.elem.fromWire(w.Elem()))
		return out
	case wOpt, wNull:
		out := reflect.New(m.orig)
		if w.IsNil() {
			return out.Elem()
		}
		base := out.UnsafePointer()
		reflect.NewAt(m.elem.orig, unsafe.Add(base, m.vOff)).Elem().Set(m.elem.fromWire(w.Elem()))
		*(*bool)(unsafe.Add(base, m.fOff)) = true
		return out.Elem()
	}
	return w
}
