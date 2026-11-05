package main

import (
	"errors"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unsafe"
)

type Item struct {
	ItemID int
	Qty    int
}
type In struct {
	PageSize int
	UserName string
	Items    []Item
	Inner    struct{ FooBar string }
	Opt      *Item
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// wireType rebuilds struct types (recursively) with json tags from the policy.
// Layout is identical, so *T and *wire share memory.
func wireType(t reflect.Type) reflect.Type {
	switch t.Kind() {
	case reflect.Pointer:
		return reflect.PointerTo(wireType(t.Elem()))
	case reflect.Slice:
		return reflect.SliceOf(wireType(t.Elem()))
	case reflect.Struct:
		fs := make([]reflect.StructField, t.NumField())
		for i := range fs {
			f := t.Field(i)
			f.Type = wireType(f.Type)
			if f.Tag.Get("json") == "" {
				f.Tag = reflect.StructTag(fmt.Sprintf(`json:%q`, snake(f.Name)))
			}
			fs[i] = f
		}
		return reflect.StructOf(fs)
	}
	return t
}

func main() {
	// (1) MarshalToFunc[any]: which values does the hook see?
	seen := []string{}
	m := json.MarshalToFunc(func(e *jsontext.Encoder, v any) error {
		seen = append(seen, fmt.Sprintf("%T", v))
		return errors.ErrUnsupported
	})
	in := In{PageSize: 2, UserName: "u", Items: []Item{{1, 2}}, Opt: &Item{3, 4}}
	b, err := json.Marshal(in, json.WithMarshalers(m))
	fmt.Println("hook[any] sees:", seen, err)
	fmt.Println("output:", string(b))

	// (2) StructOf with policy tags + unsafe pointer cast.
	wt := wireType(reflect.TypeFor[In]())
	fmt.Println("wire type:", wt)
	pw := reflect.NewAt(wt, unsafe.Pointer(&in))
	b, err = json.Marshal(pw.Interface())
	fmt.Println("marshal via wire type:", string(b), err)

	var out In
	pu := reflect.NewAt(wt, unsafe.Pointer(&out))
	err = json.Unmarshal([]byte(`{"page_size":9,"user_name":"x","items":[{"item_id":5,"qty":6}],"inner":{"foo_bar":"z"},"opt":{"item_id":7,"qty":8},"bogus":1}`), pu.Interface(), json.RejectUnknownMembers(true))
	fmt.Printf("unmarshal via wire type: %+v err=%v\n", out, err)
	err = json.Unmarshal([]byte(`{"page_size":"a","items":[{"item_id":"b"}]}`), pu.Interface())
	fmt.Println("error location in wire names:", err)
}
