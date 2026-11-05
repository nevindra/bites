package jsoncodec

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

type jCode string

func (jCode) Length() (int, int) { return 1, 3 }

type constrained struct {
	Name  wire.NonEmpty
	Code  jCode
	N     wire.PositiveInt
	Big   wire.PositiveInt64
	Maybe wire.Opt[wire.NonEmpty]
	List  []wire.NonEmpty
}

func decodeConstrained(t *testing.T, body string) (constrained, []FieldError) {
	t.Helper()
	d, err := CompileDecoder(reflect.TypeFor[constrained](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var v constrained
	var errs Errors
	if err := d.Decode([]byte(body), unsafe.Pointer(&v), wire.NewSlab(1024), &errs); err != nil {
		t.Fatal(err)
	}
	return v, errs.List
}

func TestConstraintTypesInABodyAreCheckedAndNamedByWireLocation(t *testing.T) {
	v, errs := decodeConstrained(t, `{"name":"a","code":"abc","n":1,"big":2,"maybe":"m","list":["x"]}`)
	if len(errs) != 0 || v.Name != "a" || v.N != 1 || v.Big != 2 {
		t.Fatalf("%+v %v", v, errs)
	}
	_, errs = decodeConstrained(t, `{"name":"","code":"abcd","n":0,"big":-1,"maybe":"","list":["x",""]}`)
	want := []FieldError{
		{Location: "name", Message: "must not be empty", Rule: "nonempty"},
		{Location: "code", Message: "must be between 1 and 3 characters", Rule: "length", Min: 1, Max: 3},
		{Location: "n", Message: "expected a positive integer", Rule: "positive"},
		{Location: "big", Message: "expected a positive integer", Rule: "positive"},
		{Location: "maybe", Message: "must not be empty", Rule: "nonempty"},
		{Location: "list[1]", Message: "must not be empty", Rule: "nonempty"},
	}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %v\nwant %v", errs, want)
	}
}

func TestDescribeCarriesAConstraintForTheSchemaWhenDecodingOnly(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[jCode](), Options{}, Decoding)
	if err != nil || sh.Kind != KindString || sh.Constraint.MinLen != 1 || sh.Constraint.MaxLen != 3 {
		t.Fatalf("%+v %v", sh, err)
	}
	sh, err = Describe(reflect.TypeFor[wire.PositiveInt64](), Options{}, Decoding)
	if err != nil || sh.Kind != KindInt || sh.Constraint.Kind != wire.ConMin || sh.Constraint.MinInt != 1 {
		t.Fatalf("%+v %v", sh, err)
	}
	sh, _ = Describe(reflect.TypeFor[wire.NonEmpty](), Options{}, Encoding)
	if sh.Constraint.Kind != wire.ConNone {
		t.Errorf("an encoded value is not checked, so it carries no constraint: %+v", sh)
	}
}

func TestAConstraintTypeAsAMapKeyIsRefusedByTheDecoderAndTheDescription(t *testing.T) {
	typ := reflect.TypeFor[map[wire.NonEmpty]int]()
	_, cerr := CompileDecoder(typ, Options{})
	_, derr := Describe(typ, Options{}, Decoding)
	if cerr == nil || derr == nil || cerr.Error() != derr.Error() {
		t.Fatalf("%v vs %v", cerr, derr)
	}
}

type jPage int

func (jPage) Range() (int64, int64) { return 1, 50 }

type jEven int

func (jEven) Check() error { return errors.New("must be even") }

func TestARangeAndACheckInABodyCarryTheirRuleAndBounds(t *testing.T) {
	type in struct {
		Page jPage
		Even jEven
	}
	d, err := CompileDecoder(reflect.TypeFor[in](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var v in
	var errs Errors
	if err := d.Decode([]byte(`{"page":51,"even":3}`), unsafe.Pointer(&v), wire.NewSlab(1024), &errs); err != nil {
		t.Fatal(err)
	}
	want := []FieldError{
		{Location: "page", Message: "expected an integer between 1 and 50", Rule: "range", Min: 1, Max: 50},
		{Location: "even", Message: "must be even", Rule: "check"},
	}
	if !reflect.DeepEqual(errs.List, want) {
		t.Fatalf("got %+v\nwant %+v", errs.List, want)
	}
}

type jTags []string

func (jTags) Items() (int, int) { return 1, 2 }

type jMail string

func (jMail) Format() string { return "email" }

func TestDescribeCarriesItemsWhenDecodingOnlyAndFormatInBothDirections(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[jTags](), Options{}, Decoding)
	if err != nil || sh.Kind != KindSlice || sh.Items == nil || sh.Items.Min != 1 || sh.Items.Max != 2 {
		t.Fatalf("%+v %v", sh, err)
	}
	sh, err = Describe(reflect.TypeFor[jMail](), Options{}, Decoding)
	if err != nil || sh.Kind != KindString || sh.Format != "email" {
		t.Fatalf("%+v %v", sh, err)
	}
	sh, _ = Describe(reflect.TypeFor[jTags](), Options{}, Encoding)
	if sh.Items != nil {
		t.Errorf("an encoded list is not counted, so it carries no bound: %+v", sh)
	}
	sh, err = Describe(reflect.TypeFor[jMail](), Options{}, Encoding)
	if err != nil || sh.Kind != KindString || sh.Format != "email" {
		t.Errorf("an encoded string states its format, which is an annotation: %+v %v", sh, err)
	}
}

func TestAListPastItsMaxIsSkippedAndAnErrorNamesTheListOnce(t *testing.T) {
	type in struct{ Tags jTags }
	d, err := CompileDecoder(reflect.TypeFor[in](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var v in
	var errs Errors
	if err := d.Decode([]byte(`{"tags":["a","b","c","d"]}`), unsafe.Pointer(&v), wire.NewSlab(1024), &errs); err != nil {
		t.Fatal(err)
	}
	want := []FieldError{{Location: "tags", Message: "must have between 1 and 2 items", Rule: "items", Min: 1, Max: 2}}
	if !reflect.DeepEqual(errs.List, want) || len(v.Tags) != 2 {
		t.Fatalf("got %+v with %d stored\nwant %+v", errs.List, len(v.Tags), want)
	}
}

type jAmount float64

func (jAmount) Range() (float64, float64) { return 0.01, 1000000 }

type jRate float32

func (jRate) Range() (float64, float64) { return -math.MaxFloat32, 0.5 }

type jEmail2 string

func (jEmail2) Format() string { return "email" }

type jBounds struct {
	Amount jAmount
	Rate   jRate
	Maybe  wire.Opt[jAmount]
	List   []jAmount
	Mail   jEmail2
	Mails  []jEmail2
}

func decodeBounds(t *testing.T, body string) (jBounds, []FieldError) {
	t.Helper()
	d, err := CompileDecoder(reflect.TypeFor[jBounds](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var v jBounds
	var errs Errors
	if err := d.Decode([]byte(body), unsafe.Pointer(&v), wire.NewSlab(1024), &errs); err != nil {
		t.Fatal(err)
	}
	return v, errs.List
}

func TestAFloatRangeAndACheckedFormatAreCheckedInABodyInOptAndInLists(t *testing.T) {
	v, errs := decodeBounds(t, `{"amount":12.5,"rate":0.5,"maybe":1,"list":[0.01,1000000],"mail":"a@b.co","mails":["x@y.org"]}`)
	if len(errs) != 0 || v.Amount != 12.5 || v.Rate != 0.5 || v.Mail != "a@b.co" {
		t.Fatalf("%+v %v", v, errs)
	}
	_, errs = decodeBounds(t, `{"amount":0,"rate":0.51,"maybe":2000000,"list":[1,0.001],"mail":"nope","mails":["x@y.org","a@b"]}`)
	want := []FieldError{
		{Location: "amount", Message: "expected a number between 0.01 and 1000000", Rule: "range", Float: true, MinF: 0.01, MaxF: 1000000},
		{Location: "rate", Message: "expected a number of at most 0.5", Rule: "range", Float: true, MinF: math.Inf(-1), MaxF: 0.5},
		{Location: "maybe", Message: "expected a number between 0.01 and 1000000", Rule: "range", Float: true, MinF: 0.01, MaxF: 1000000},
		{Location: "list[1]", Message: "expected a number between 0.01 and 1000000", Rule: "range", Float: true, MinF: 0.01, MaxF: 1000000},
		{Location: "mail", Message: "must be an email address", Rule: "format", Format: "email"},
		{Location: "mails[1]", Message: "must be an email address", Rule: "format", Format: "email"},
	}
	if !reflect.DeepEqual(errs, want) {
		t.Fatalf("got %+v\nwant %+v", errs, want)
	}
}

func TestAPassingFloatRangeAndFormatAllocateNothingOnTheFastPathAndThroughTheTokenizer(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	type in struct {
		Amount jAmount
		Rate   jRate
		Mail   jEmail2
	}
	d, err := CompileDecoder(reflect.TypeFor[in](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"fast":      `{"amount":12.5,"rate":0.25,"mail":"ada@example.com"}`,
		"tokenizer": `{"amount":12.5,"rate":0.25,"mail":"ada@example.com"}`,
	} {
		b := []byte(body)
		slab := wire.NewSlab(1 << 16)
		var errs Errors
		var v in
		run := func() {
			errs.Reset()
			if err := d.Decode(b, unsafe.Pointer(&v), slab, &errs); err != nil || len(errs.List) > 0 {
				t.Fatal(err, errs.List)
			}
		}
		run()
		if n := testing.AllocsPerRun(200, run); n != 0 {
			t.Errorf("%s: %v allocations a decode, want 0", name, n)
		}
		if v.Amount != 12.5 || v.Mail != "ada@example.com" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestDescribeCarriesAFloatRangeForTheSchemaWhenDecodingOnly(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[jAmount](), Options{}, Decoding)
	if err != nil || sh.Kind != KindFloat || sh.Constraint.Kind != wire.ConFloat {
		t.Fatalf("%+v %v", sh, err)
	}
	if lo, hi := sh.Constraint.FloatBounds(); lo != 0.01 || hi != 1000000 {
		t.Errorf("bounds %v %v", lo, hi)
	}
	if sh, _ = Describe(reflect.TypeFor[jAmount](), Options{}, Encoding); sh.Constraint.Kind != wire.ConNone {
		t.Errorf("an encoded float is not checked: %+v", sh)
	}
}
