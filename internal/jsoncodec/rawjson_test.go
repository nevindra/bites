package jsoncodec

import (
	"encoding/json"
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/nevindra/bites/internal/wire"
)

func TestARawMessageTakesNullAsTheBytesNullAndWritesItBack(t *testing.T) {
	type body struct {
		Style json.RawMessage
		Other jsontext.Value
		Kept  json.RawMessage
	}
	v, errs, err := decodeInto[body](t, Options{}, `{"style":null,"other":null,"kept":{"a":1}}`)
	if err != nil || len(errs.List) != 0 {
		t.Fatalf("%v %v", err, errs.List)
	}
	if string(v.Style) != "null" || string(v.Other) != "null" || string(v.Kept) != `{"a":1}` {
		t.Errorf("%q %q %q", v.Style, v.Other, v.Kept)
	}
	got, err := encodeOf(t, Options{}, v)
	if err != nil || got != `{"style":null,"other":null,"kept":{"a":1}}` {
		t.Errorf("%s %v", got, err)
	}
	if v, _, _ = decodeInto[body](t, Options{}, `{}`); v.Style != nil {
		t.Errorf("an absent member held %q", v.Style)
	}
}

func TestARawJSONKeepsTheExactBytesNullIncludedAndAnEmptyOneIsWrittenAsNull(t *testing.T) {
	type body struct {
		Style wire.RawJSON[map[string]string]
	}
	v, errs, err := decodeInto[body](t, Options{}, `{"style":{ "bold" : "yes" }}`)
	if err != nil || len(errs.List) != 0 || string(v.Style.JSON) != `{ "bold" : "yes" }` {
		t.Fatalf("%q %v %v", v.Style.JSON, err, errs.List)
	}
	v, errs, _ = decodeInto[body](t, Options{}, `{"style":null}`)
	if len(errs.List) != 0 || string(v.Style.JSON) != "null" {
		t.Errorf("%q %v", v.Style.JSON, errs.List)
	}
	got, err := encodeOf(t, Options{}, body{})
	if err != nil || got != `{"style":null}` {
		t.Errorf("%s %v", got, err)
	}
}

func TestARawJSONInsideOptAndNullKeepsThePresenceRules(t *testing.T) {
	type body struct {
		A wire.Opt[wire.RawJSON[int]]
		B wire.Null[wire.RawJSON[int]]
	}
	v, errs, _ := decodeInto[body](t, Options{}, `{"a":null,"b":null}`)
	if len(errs.List) != 0 || string(v.A.Or(wire.RawJSON[int]{}).JSON) != "null" || !v.B.IsNull() {
		t.Errorf("%v %+v", errs.List, v)
	}
}

func TestARawJSONThatNamesNoDescribableShapeIsRefused(t *testing.T) {
	type iface struct{ X wire.RawJSON[any] }
	type ptr struct{ X wire.RawJSON[*int] }
	type free struct{ X wire.RawJSON[json.RawMessage] }
	type nested struct {
		X wire.RawJSON[wire.RawJSON[int]]
	}
	type bad struct{ X wire.RawJSON[chan int] }
	for name, typ := range map[string]reflect.Type{"interface": reflect.TypeFor[iface](), "pointer": reflect.TypeFor[ptr](), "free-form": reflect.TypeFor[free](), "nested": reflect.TypeFor[nested](), "chan": reflect.TypeFor[bad]()} {
		if _, err := CompileDecoder(typ, Options{}); err == nil {
			t.Errorf("%s: a decoder was compiled", name)
		}
		if _, err := CompileEncoder(typ, Options{}); err == nil {
			t.Errorf("%s: an encoder was compiled", name)
		}
	}
}

func TestARawJSONIsDescribedAsRawWithItsElement(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[wire.RawJSON[Address]](), Options{}, Encoding)
	if err != nil || sh.Kind != KindRaw || sh.Elem != reflect.TypeFor[Address]() {
		t.Errorf("%+v %v", sh, err)
	}
}
