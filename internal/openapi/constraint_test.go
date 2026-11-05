package openapi

import (
	"reflect"
	"testing"

	"github.com/nevindra/bites/internal/wire"
)

type oCode string

func (oCode) Length() (int, int) { return 2, 40 }

type oShort string

func (oShort) Length() (int, int) { return 0, 9 }

type Constrained struct {
	Name  wire.NonEmpty
	Code  oCode
	Short oShort
	N     wire.PositiveInt
	Big   wire.PositiveInt64
	Maybe wire.Opt[wire.NonEmpty]
	Tags  []wire.NonEmpty
}

func TestConstraintTypesAreStatedInARequestSchema(t *testing.T) {
	s := schemaOf[Constrained](t, Request)
	want := `{"additionalProperties":false,"properties":{` +
		`"big":{"format":"int64","minimum":1,"type":"integer"},` +
		`"code":{"maxLength":40,"minLength":2,"type":"string"},` +
		`"maybe":{"minLength":1,"type":"string"},` +
		`"n":{"format":"int64","minimum":1,"type":"integer"},` +
		`"name":{"minLength":1,"type":"string"},` +
		`"short":{"maxLength":9,"minLength":0,"type":"string"},` +
		`"tags":{"items":{"minLength":1,"type":"string"},"type":"array"}},` +
		`"required":["name","code","short","n","big","tags"],"type":"object"}`
	if got := asJSON(s); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestConstraintTypesAreNotClaimedForAResponseWhichDoesNotCheckThem(t *testing.T) {
	s := schemaOf[Constrained](t, Response)
	props := s["properties"].(Schema)
	if asJSON(props["name"]) != `{"type":"string"}` || asJSON(props["n"]) != `{"format":"int64","type":"integer"}` {
		t.Fatalf("%s", asJSON(props))
	}
}

type formWrap struct{ Value formBody }

type formBody struct {
	Name wire.NonEmpty
	Tags []string
	Page wire.Opt[int]
}

func TestAFormBodyIsDescribedWithTheFormMediaTypeAndTheSchemaOfItsStruct(t *testing.T) {
	r := New(Options{Form: func(t reflect.Type) (reflect.Type, bool) {
		return reflect.TypeFor[formBody](), t == reflect.TypeFor[formWrap]()
	}})
	s, media, err := r.BodySchema(reflect.TypeFor[formWrap](), Request, nil)
	if err != nil || media != MediaForm {
		t.Fatalf("%q, %v", media, err)
	}
	comps, err := r.Components()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s["$ref"]; !ok {
		t.Fatalf("a struct body is a reference to its component: %v", s)
	}
	want := `{"additionalProperties":false,"properties":{"name":{"minLength":1,"type":"string"},"page":{"format":"int64","type":"integer"},"tags":{"items":{"type":"string"},"type":"array"}},"required":["name","tags"],"type":"object"}`
	if got := asJSON(comps["formBody"]); got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
	_, media, _ = r.BodySchema(reflect.TypeFor[formBody](), Request, nil)
	if media != MediaJSON {
		t.Fatalf("a body that is not a form is JSON: %q", media)
	}
}
