package jsoncodec

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/wire"
)

type badEnum string

func (badEnum) Values() int { return 1 }

type emptyEnum string

func (emptyEnum) Values() []string { return nil }

type embedPtr struct {
	*Audit
}

type embedOpt struct {
	wire.Opt[int]
}

type dupNames struct {
	ItemID int
	ItemId int
}

type dupViaEmbed struct {
	Audit
	CreatedBy string
}

type withPtr struct{ Name *string }

type CreateOrder struct {
	Items []struct {
		Price any
	}
}

// refusals is the table of ADR 0007: every shape the decoder refuses, with the reason it
// must give, naming the field path in Go terms.
var decoderRefusals = []struct {
	name string
	typ  reflect.Type
	says string
}{
	{"a pointer field", reflect.TypeFor[withPtr](), "withPtr.Name: a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]"},
	{"a pointer in a slice", reflect.TypeFor[struct{ L []*int }](), "a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]"},
	{"an interface field", reflect.TypeFor[CreateOrder](), "CreateOrder.Items[].Price: a field of interface type cannot be decoded"},
	{"a map with int keys", reflect.TypeFor[struct{ M map[int]int }](), "a map key must be a string type, int is not"},
	{"an unsupported tag option", reflect.TypeFor[struct {
		N int `json:"n,string"`
	}](), `json tag option "string" is not supported (only omitempty and omitzero)`},
	{"two fields with one wire name", reflect.TypeFor[dupNames](), `wire name "item_id" is also the wire name of dupNames.ItemID`},
	{"a name taken through an embedded struct", reflect.TypeFor[dupViaEmbed](), `wire name "created_by" is also the wire name of`},
	{"an embedded pointer", reflect.TypeFor[embedPtr](), "an embedded pointer to a struct cannot be inlined"},
	{"an embedded Opt", reflect.TypeFor[embedOpt](), "an embedded Opt[int] is not supported"},
	{"an Opt in a slice", reflect.TypeFor[struct{ L []wire.Opt[int] }](), "an Opt can only be a field of a struct"},
	{"an Opt of an Opt", reflect.TypeFor[struct{ O wire.Opt[wire.Opt[int]] }](), "an Opt of an Opt is not allowed"},
	{"a Null of an Opt", reflect.TypeFor[struct{ O wire.Null[wire.Opt[int]] }](), "a Null can hold neither an Opt nor a Null"},
	{"a Null of a Null", reflect.TypeFor[struct{ O wire.Null[wire.Null[int]] }](), "a Null can hold neither an Opt nor a Null"},
	{"a pointer inside an Opt", reflect.TypeFor[struct{ O wire.Opt[*int] }](), "O: a pointer cannot say"},
	{"a channel", reflect.TypeFor[struct{ C chan int }](), "a field of kind chan cannot be decoded"},
	{"a func", reflect.TypeFor[struct{ F func() }](), "a field of kind func cannot be decoded"},
	{"a complex number", reflect.TypeFor[struct{ C complex128 }](), "a field of kind complex128 cannot be decoded"},
	{"a Values method of the wrong shape", reflect.TypeFor[struct{ E badEnum }](), "Values must be func() []badEnum or func() []string"},
	{"an enum that lists nothing", reflect.TypeFor[struct{ E emptyEnum }](), "Values returned nothing"},
	{"a root that is a pointer", reflect.TypeFor[*Address](), "a pointer cannot say"},
}

func TestEveryRefusedShapeSaysWhyAndWhere(t *testing.T) {
	for _, c := range decoderRefusals {
		_, err := CompileDecoder(c.typ, Options{})
		if err == nil {
			t.Errorf("%s: compiled", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s:\n got  %s\n want ...%s", c.name, err, c.says)
		}
	}
}

func TestTheEncoderRefusesWhatCannotBeWrittenAndAllowsWhatTheDecoderRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  reflect.Type
		says string
	}{
		{"a map with int keys", reflect.TypeFor[struct{ M map[int]int }](), "a map key must be a string type"},
		{"an unsupported tag option", reflect.TypeFor[struct {
			N int `json:"n,string"`
		}](), "json tag option"},
		{"two fields with one wire name", reflect.TypeFor[dupNames](), "wire name"},
		{"an Opt in a slice", reflect.TypeFor[struct{ L []wire.Opt[int] }](), "an Opt can only be a field of a struct"},
		{"a channel", reflect.TypeFor[struct{ C chan int }](), "a field of kind chan cannot be encoded"},
	} {
		_, err := CompileEncoder(c.typ, Options{})
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v, want ...%s", c.name, err, c.says)
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[withPtr](), reflect.TypeFor[CreateOrder](), reflect.TypeFor[map[string]any]()} {
		if _, err := CompileEncoder(typ, Options{}); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
}

func TestARecursiveTypeCompilesOnce(t *testing.T) {
	type tree struct {
		Name string
		Kids []*tree
	}
	if _, err := CompileEncoder(reflect.TypeFor[tree](), Options{}); err != nil {
		t.Error(err)
	}
	type dtree struct {
		Name string
		Kids []dtree
	}
	d, err := CompileDecoder(reflect.TypeFor[dtree](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, errs, derr := decodeInto[dtree](t, Options{}, `{"name":"a","kids":[{"name":"b","kids":[]}]}`)
	_ = d
	if derr != nil || len(errs.List) > 0 || v.Kids[0].Name != "b" {
		t.Errorf("%+v %v %v", v, derr, errs.List)
	}
}
