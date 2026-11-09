package jsoncodec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

// decodeInto compiles T with o, decodes body and returns the value, the errors and the
// fatal error.
func decodeInto[T any](t testing.TB, o Options, body string) (T, *Errors, error) {
	t.Helper()
	d, err := CompileDecoder(reflect.TypeFor[T](), o)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var v T
	errs := new(Errors)
	err = d.Decode([]byte(body), unsafe.Pointer(&v), wire.NewSlab(1<<12), errs)
	return v, errs, err
}

func wantErrors(t *testing.T, errs *Errors, want ...FieldError) {
	t.Helper()
	// The rule codes have their own tests; these compare where and what was said.
	got := make([]FieldError, len(errs.List))
	for i, e := range errs.List {
		got[i] = FieldError{Location: e.Location, Message: e.Message}
	}
	if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("errors\n got  %+v\n want %+v", got, want)
	}
}

type multi struct {
	Name string
	Age  int
	Tags []int
}

func TestSeveralErrorsInOneBodyAreAllCollectedWithTheirLocations(t *testing.T) {
	_, errs, err := decodeInto[multi](t, Options{}, `{"name":5,"age":"x","tags":[1,"b"],"extra":1}`)
	if err != nil {
		t.Fatal(err)
	}
	wantErrors(t, errs,
		FieldError{Location: "name", Message: "expected a string"},
		FieldError{Location: "age", Message: "expected an integer"},
		FieldError{Location: "tags[1]", Message: "expected an integer"},
		FieldError{Location: "extra", Message: "unknown field"},
	)
}

func TestAMissingFieldIsRequiredAtItsLocationInsideArrays(t *testing.T) {
	type in struct {
		Items []Line
		Addr  Address
	}
	_, errs, err := decodeInto[in](t, Options{}, `{"items":[{"sku":"a","qty":1,"price":1},{"sku":"b","price":2}],"addr":{"street":"s"}}`)
	if err != nil {
		t.Fatal(err)
	}
	wantErrors(t, errs, FieldError{Location: "items[1].qty", Message: "required"}, FieldError{Location: "addr.zip_code", Message: "required"})
}

func TestAMissingRootFieldHasNoDotInFrontOfIt(t *testing.T) {
	_, errs, _ := decodeInto[multi](t, Options{}, `{}`)
	wantErrors(t, errs, FieldError{Location: "name", Message: "required"}, FieldError{Location: "age", Message: "required"}, FieldError{Location: "tags", Message: "required"})
}

func TestNullForAPlainFieldIsMustNotBeNull(t *testing.T) {
	_, errs, _ := decodeInto[multi](t, Options{}, `{"name":null,"age":1,"tags":[null,2]}`)
	wantErrors(t, errs, FieldError{Location: "name", Message: "must not be null"}, FieldError{Location: "tags[0]", Message: "must not be null"})
	_, errs, _ = decodeInto[multi](t, Options{}, `null`)
	wantErrors(t, errs, FieldError{Location: "", Message: "must not be null"})
}

func TestOptNullAndTheTriStateOfAPatchField(t *testing.T) {
	type patch struct {
		Name  wire.Opt[string]
		Email wire.Null[string]
		Age   wire.Opt[wire.Null[int]]
	}
	// Absent: Opt unset, Null (required) reported, Opt[Null] unset.
	v, errs, _ := decodeInto[patch](t, Options{}, `{"email":null}`)
	wantErrors(t, errs)
	if v.Name.IsSet() || !v.Email.IsNull() || v.Age.IsSet() {
		t.Errorf("absent: %+v", v)
	}
	// null clears.
	v, _, _ = decodeInto[patch](t, Options{}, `{"email":"a@b","age":null}`)
	if a, ok := v.Age.Get(); !ok || !a.IsNull() {
		t.Errorf("age null: %+v", v.Age)
	}
	if e, ok := v.Email.Get(); !ok || e != "a@b" {
		t.Errorf("email: %+v", v.Email)
	}
	// a value sets.
	v, _, _ = decodeInto[patch](t, Options{}, `{"name":"n","email":null,"age":7}`)
	if n, _ := v.Name.Get(); n != "n" {
		t.Errorf("name: %+v", v.Name)
	}
	if a, ok := v.Age.Get(); !ok {
		t.Error("age not set")
	} else if x, ok := a.Get(); !ok || x != 7 {
		t.Errorf("age: %+v", a)
	}
	// An Opt that is not a Null refuses null, and stays absent.
	v, errs, _ = decodeInto[patch](t, Options{}, `{"name":null,"email":null}`)
	wantErrors(t, errs, FieldError{Location: "name", Message: "must not be null"})
	if v.Name.IsSet() {
		t.Error("a refused null left the Opt set")
	}
}

func TestAnUnknownMemberIsRefusedUnlessTheRouteAcceptsIt(t *testing.T) {
	_, errs, _ := decodeInto[Address](t, Options{}, `{"street":"a","zip_code":"b","extra":{"deep":[1,{"x":2}]}}`)
	wantErrors(t, errs, FieldError{Location: "extra", Message: "unknown field"})
	_, errs, err := decodeInto[Address](t, Options{AllowUnknown: true}, `{"street":"a","zip_code":"b","extra":{"deep":[1,{"x":2}]}}`)
	wantErrors(t, errs)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnUnknownMemberNameEchoedInALocationIsClipped(t *testing.T) {
	_, errs, _ := decodeInto[Address](t, Options{}, `{"street":"a","zip_code":"b","`+strings.Repeat("é", 100)+`":1}`)
	if len(errs.List) != 1 || len(errs.List[0].Location) > 64 || !strings.HasPrefix(errs.List[0].Location, "éé") {
		t.Errorf("%+v", errs.List)
	}
	for _, c := range errs.List[0].Location {
		if c == 0xFFFD {
			t.Error("the clip cut a rune in half")
		}
	}
}

func TestASyntaxErrorEndsDecodingWithAFixedMessage(t *testing.T) {
	for body, wantMsg := range map[string]string{
		`{"name":"a",`:                   "unexpected end of JSON",
		``:                               "unexpected end of JSON",
		`{"name":"a"} x`:                 "invalid JSON",
		`{"name":"s3cr3t" "age":1}`:      "invalid JSON",
		`{"name":"a","name":"s3cr3t"}`:   "duplicate member name",
		"{\"name\":\"\xff\xfe s3cr3t\"}": "invalid JSON",
	} {
		_, _, err := decodeInto[multi](t, Options{}, body)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("%q: got %v, want a *SyntaxError", body, err)
			continue
		}
		if !strings.HasPrefix(se.Error(), wantMsg) || strings.Contains(se.Error(), "s3cr3t") {
			t.Errorf("%q: message %q, want prefix %q and no client bytes", body, se.Error(), wantMsg)
		}
		if se.Unwrap() == nil {
			t.Errorf("%q: the tokenizer's error was dropped", body)
		}
	}
}

func TestErrorsFoundBeforeASyntaxErrorAreKept(t *testing.T) {
	_, errs, err := decodeInto[multi](t, Options{}, `{"name":5,"age":`)
	if err == nil {
		t.Fatal("no syntax error")
	}
	wantErrors(t, errs, FieldError{Location: "name", Message: "expected a string"})
}

func TestNestingOverTheDepthLimitEndsDecoding(t *testing.T) {
	type node struct {
		Kids []node
	}
	// MaxDepth counts the levels of objects and arrays: this body has four, the next six.
	o := Options{MaxDepth: 4}
	_, _, err := decodeInto[node](t, o, `{"kids":[{"kids":[]}]}`)
	if err != nil {
		t.Errorf("four levels with MaxDepth 4: got %v", err)
	}
	_, _, err = decodeInto[node](t, o, `{"kids":[{"kids":[{"kids":[]}]}]}`)
	if !errors.Is(err, ErrDepth) {
		t.Errorf("got %v, want ErrDepth", err)
	}
	// Skipped values count too, so unknown members cannot hide a deep body.
	_, _, err = decodeInto[Address](t, Options{MaxDepth: 5, AllowUnknown: true}, `{"street":"","zip_code":"","x":`+strings.Repeat("[", 6)+strings.Repeat("]", 6)+`}`)
	if !errors.Is(err, ErrDepth) {
		t.Errorf("skipped: got %v, want ErrDepth", err)
	}
	// The default is 64.
	d, _ := CompileDecoder(reflect.TypeFor[multi](), Options{})
	if d.opt.MaxDepth != 64 || d.opt.MaxErrors != 20 {
		t.Errorf("defaults: %+v", d.opt)
	}
}

func TestMoreErrorsThanTheLimitSetTruncatedAndSyntaxIsStillChecked(t *testing.T) {
	type in struct{ Nums []int }
	body := `{"nums":["a","b","c","d","e"]}`
	_, errs, err := decodeInto[in](t, Options{MaxErrors: 3}, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs.List) != 3 || !errs.Truncated {
		t.Errorf("got %d errors, truncated=%v", len(errs.List), errs.Truncated)
	}
	_, errs, err = decodeInto[in](t, Options{MaxErrors: 3}, `{"nums":["a","b","c","d","e"],`)
	if err == nil || len(errs.List) != 3 || !errs.Truncated {
		t.Errorf("syntax after truncation: err=%v errors=%d truncated=%v", err, len(errs.List), errs.Truncated)
	}
}

func TestErrorsAlreadyInTheListCountAgainstTheLimit(t *testing.T) {
	d, _ := CompileDecoder(reflect.TypeFor[multi](), Options{MaxErrors: 2})
	errs := &Errors{List: []FieldError{{Location: "path.id", Message: "expected an integer"}}}
	var v multi
	if err := d.Decode([]byte(`{}`), unsafe.Pointer(&v), wire.NewSlab(64), errs); err != nil {
		t.Fatal(err)
	}
	if len(errs.List) != 2 || !errs.Truncated {
		t.Errorf("%v truncated=%v", errs.List, errs.Truncated)
	}
}

func within(p unsafe.Pointer, b []byte) bool {
	return uintptr(p) >= uintptr(unsafe.Pointer(unsafe.SliceData(b))) &&
		uintptr(p) < uintptr(unsafe.Pointer(unsafe.SliceData(b)))+uintptr(len(b))
}

func TestStringsWithoutEscapesPointIntoTheBodyAndEscapedOnesLandInTheSlab(t *testing.T) {
	type in struct{ A, B, C string }
	body := []byte(`{"a":"plain","b":"es\ncaped","c":"\u00e9"}`)
	d, err := CompileDecoder(reflect.TypeFor[in](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	slab := wire.NewSlab(64)
	before := slab.Remaining()
	var v in
	var errs Errors
	if err := d.Decode(body, unsafe.Pointer(&v), slab, &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs)
	}
	if v.A != "plain" || v.B != "es\ncaped" || v.C != "é" {
		t.Fatalf("%+v", v)
	}
	if !within(unsafe.Pointer(unsafe.StringData(v.A)), body) {
		t.Error("an unescaped string was copied instead of pointing into the body")
	}
	for _, s := range []string{v.B, v.C} {
		if within(unsafe.Pointer(unsafe.StringData(s)), body) {
			t.Errorf("%q points into the body but was unescaped", s)
		}
	}
	if used := before - slab.Remaining(); used != len(v.B)+len(v.C) || slab.Fallbacks() != 0 {
		t.Errorf("slab used %d bytes, want %d; fallbacks %d", used, len(v.B)+len(v.C), slab.Fallbacks())
	}
}

func TestIntegersAreRangeCheckedWithWiresSentences(t *testing.T) {
	type in struct {
		S int8
		U uint16
	}
	_, errs, _ := decodeInto[in](t, Options{}, `{"s":128,"u":-1}`)
	wantErrors(t, errs,
		FieldError{Location: "s", Message: "expected an integer between -128 and 127"},
		FieldError{Location: "u", Message: "expected a non-negative integer"})
	_, errs, _ = decodeInto[in](t, Options{}, `{"s":1.5,"u":65536}`)
	wantErrors(t, errs,
		FieldError{Location: "s", Message: "expected an integer"},
		FieldError{Location: "u", Message: "expected an integer between 0 and 65535"})
	v, errs, _ := decodeInto[in](t, Options{}, `{"s":-128,"u":65535}`)
	wantErrors(t, errs)
	if v.S != -128 || v.U != 65535 {
		t.Errorf("%+v", v)
	}
}

func TestFloatsRefuseWhatIsNotAFiniteNumber(t *testing.T) {
	type in struct {
		F float64
		G float32
	}
	_, errs, _ := decodeInto[in](t, Options{}, `{"f":1e999,"g":"x"}`)
	wantErrors(t, errs, FieldError{Location: "f", Message: "expected a number"}, FieldError{Location: "g", Message: "expected a number"})
	v, _, _ := decodeInto[in](t, Options{}, `{"f":-1.5e3,"g":0.1}`)
	if v.F != -1500 || v.G != 0.1 {
		t.Errorf("%+v", v)
	}
}

func TestWrongKindsNameWhatWasExpected(t *testing.T) {
	type in struct {
		B bool
		S string
		O Address
		A []int
		T time.Time
		U uuid.UUID
		L Level
	}
	_, errs, _ := decodeInto[in](t, Options{}, `{"b":"yes","s":true,"o":[],"a":{},"t":5,"u":5,"l":5}`)
	wantErrors(t, errs,
		FieldError{Location: "b", Message: "expected true or false"},
		FieldError{Location: "s", Message: "expected a string"},
		FieldError{Location: "o", Message: "expected an object"},
		FieldError{Location: "a", Message: "expected an array"},
		FieldError{Location: "t", Message: "expected an RFC 3339 time with an offset"},
		FieldError{Location: "u", Message: "expected a UUID"},
		FieldError{Location: "l", Message: "expected one of: low, high"})
}

func TestEnumsUuidsAndTimesAreCheckedAndParsed(t *testing.T) {
	type in struct {
		L Level
		U uuid.UUID
		T time.Time
	}
	v, errs, _ := decodeInto[in](t, Options{}, `{"l":"high","u":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","t":"2026-10-31T12:00:00+07:00"}`)
	wantErrors(t, errs)
	if v.L != "high" || v.U != uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8") || !v.T.Equal(time.Date(2026, 10, 31, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("%+v", v)
	}
	_, errs, _ = decodeInto[in](t, Options{}, `{"l":"medium","u":"nope","t":"2026-10-31"}`)
	wantErrors(t, errs,
		FieldError{Location: "l", Message: "expected one of: low, high"},
		FieldError{Location: "u", Message: "expected a UUID"},
		FieldError{Location: "t", Message: "expected an RFC 3339 time with an offset"})
}

func TestScalarSentencesAreTheOnesWireUses(t *testing.T) {
	// The query and the body must fail in the same words (ADR 0042, ADR 0012).
	type probe struct {
		name string
		typ  reflect.Type
		json string // a body value that fails in both
		text string
	}
	for _, p := range []probe{
		{"int8 range", reflect.TypeFor[int8](), `128`, "128"},
		{"int8 shape", reflect.TypeFor[int8](), `1.5`, "1.5"},
		{"uint16 range", reflect.TypeFor[uint16](), `65536`, "65536"},
		{"uint negative", reflect.TypeFor[uint16](), `-1`, "-1"},
		{"float", reflect.TypeFor[float64](), `1e999`, "1e999"},
		{"time", reflect.TypeFor[time.Time](), `"2026-10-31"`, "2026-10-31"},
		{"uuid", reflect.TypeFor[uuid.UUID](), `"x"`, "x"},
		{"enum", reflect.TypeFor[Level](), `"x"`, "x"},
	} {
		td, err := wire.CompileText(p.typ)
		if err != nil {
			t.Fatal(err)
		}
		ptr := reflect.New(p.typ)
		terr := td(ptr.UnsafePointer(), p.text)
		if terr == nil {
			t.Fatalf("%s: wire accepted %q", p.name, p.text)
		}
		d, err := CompileDecoder(p.typ, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var errs Errors
		d.Decode([]byte(p.json), reflect.New(p.typ).UnsafePointer(), wire.NewSlab(64), &errs)
		if len(errs.List) != 1 || errs.List[0].Message != terr.Error() {
			t.Errorf("%s: body says %+v, text says %+v", p.name, errs.List, terr)
		}
	}
}

func TestAMessageNeverCarriesTheClientsValue(t *testing.T) {
	const secret = "s3cr3tvalue"
	bodies := []string{
		`{"name":"` + secret + `","age":"` + secret + `","tags":["` + secret + `"],"` + secret + `":1}`,
		`{"name":{"` + secret + `":1},"age":[` + secret[:0] + `"` + secret + `"],"tags":"` + secret + `"}`,
	}
	for _, b := range bodies {
		_, errs, _ := decodeInto[multi](t, Options{}, b)
		for _, e := range errs.List {
			if strings.Contains(e.Message, secret) {
				t.Errorf("message %q carries the client's value", e.Message)
			}
		}
	}
}

func TestWireNamesComeFromTagsThenFromThePolicy(t *testing.T) {
	type in struct {
		ItemID  int
		HTTPURL string `json:"url"`
		Skipped string `json:"-"`
		Tagged  string `json:"Weird-Name,omitempty"`
		Audit
	}
	v, errs, err := decodeInto[in](t, Options{}, `{"item_id":1,"url":"u","Weird-Name":"w","created_by":"c","revision":2}`)
	if err != nil {
		t.Fatal(err)
	}
	wantErrors(t, errs)
	if v.ItemID != 1 || v.HTTPURL != "u" || v.Tagged != "w" || v.CreatedBy != "c" || v.Revision != 2 {
		t.Errorf("%+v", v)
	}
	// A custom policy replaces snake_case.
	v2, errs, _ := decodeInto[struct{ ItemID int }](t, Options{Names: wire.CamelCase}, `{"itemId":4}`)
	wantErrors(t, errs)
	if v2.ItemID != 4 {
		t.Errorf("%+v", v2)
	}
	// A member the tag renamed away is unknown under its old name, and "-" is never read.
	_, errs, _ = decodeInto[in](t, Options{}, `{"item_id":1,"http_url":"u","skipped":"x","Weird-Name":"w","created_by":"c","revision":2}`)
	wantErrors(t, errs, FieldError{Location: "http_url", Message: "unknown field"}, FieldError{Location: "skipped", Message: "unknown field"}, FieldError{Location: "url", Message: "required"})
}

func TestMapsAndArraysDecode(t *testing.T) {
	type in struct {
		M map[string]Line
		A [2]string
	}
	v, errs, err := decodeInto[in](t, Options{}, `{"m":{"x":{"sku":"a","qty":1,"price":2},"y":{"sku":"b","qty":3,"price":4}},"a":["p","q"]}`)
	if err != nil {
		t.Fatal(err)
	}
	wantErrors(t, errs)
	if len(v.M) != 2 || v.M["y"].Qty != 3 || v.A != [2]string{"p", "q"} {
		t.Errorf("%+v", v)
	}
	_, errs, _ = decodeInto[in](t, Options{}, `{"m":{"x":{"sku":"a","qty":"z","price":2},"n":null},"a":["p"]}`)
	wantErrors(t, errs, FieldError{Location: "m.x.qty", Message: "expected an integer"}, FieldError{Location: "m.n", Message: "must not be null"}, FieldError{Location: "a", Message: "expected an array of 2 items"})
}

func TestEmptyArrayAndObjectGiveEmptyNotNil(t *testing.T) {
	type in struct {
		S []int
		M map[string]int
	}
	v, _, _ := decodeInto[in](t, Options{}, `{"s":[],"m":{}}`)
	if v.S == nil || v.M == nil || len(v.S) != 0 || len(v.M) != 0 {
		t.Errorf("%#v", v)
	}
}

func TestADestinationSliceKeepsItsCapacityAcrossDecodes(t *testing.T) {
	d, _ := CompileDecoder(reflect.TypeFor[Small](), Options{})
	var v Small
	var errs Errors
	slab := wire.NewSlab(1 << 12)
	if err := d.Decode([]byte(smallBody), unsafe.Pointer(&v), slab, &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs)
	}
	first := &v.Items[0]
	v.Items = v.Items[:0]
	v.Items = append(v.Items[:0], Line{SKU: "stale", Qty: 99}) // old content in the array
	v.Items = v.Items[:0]
	if err := d.Decode([]byte(smallBody), unsafe.Pointer(&v), slab, &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs)
	}
	if &v.Items[0] != first || len(v.Items) != 3 || v.Items[0].SKU != "a" {
		t.Errorf("capacity not reused or stale data left: %+v", v.Items)
	}
	// A shorter list does not keep the earlier elements.
	if err := d.Decode([]byte(strings.Replace(smallBody, `,{"sku":"b","qty":2,"price":1.25},{"sku":"c","qty":3,"price":100}`, "", 1)), unsafe.Pointer(&v), slab, &errs); err != nil {
		t.Fatal(err)
	}
	if len(v.Items) != 1 {
		t.Errorf("%+v", v.Items)
	}
}

// Types that bring their own JSON, handled by encoding/json/v2 for that value.
type decimal struct{ cents int }

func (d *decimal) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != '"' {
		return errors.New("not a quoted decimal")
	}
	n := 0
	for _, c := range b[1 : len(b)-1] {
		if c < '0' || c > '9' {
			return errors.New("bad digit")
		}
		n = n*10 + int(c-'0')
	}
	d.cents = n
	return nil
}

func (d decimal) MarshalJSON() ([]byte, error) {
	return []byte(`"` + string(rune('0'+d.cents%10)) + `"`), nil
}

// declaresDecimal is what an app says with WithJSONSchema so that decimal is not refused (ADR 0076).
var declaresDecimal = Options{JSONSchemas: wire.JSONSchemas{reflect.TypeFor[decimal](): {Type: "string"}}}

type tag struct{ s string }

func (t *tag) UnmarshalText(b []byte) error {
	if string(b) == "bad" {
		return errors.New("bad")
	}
	t.s = "tag:" + string(b)
	return nil
}

func TestATypeWithItsOwnUnmarshalerIsHandedToEncodingJSON(t *testing.T) {
	type in struct {
		Price decimal
		Tag   tag
		Raw   []byte
	}
	v, errs, err := decodeInto[in](t, declaresDecimal, `{"price":"123","tag":"x","raw":"aGk="}`)
	if err != nil {
		t.Fatal(err)
	}
	wantErrors(t, errs)
	if v.Price.cents != 123 || v.Tag.s != "tag:x" || string(v.Raw) != "hi" {
		t.Errorf("%+v", v)
	}
	_, errs, _ = decodeInto[in](t, declaresDecimal, `{"price":5,"tag":"bad","raw":3}`)
	wantErrors(t, errs,
		FieldError{Location: "price", Message: "expected a valid value of type jsoncodec.decimal"},
		FieldError{Location: "tag", Message: "expected a valid value of type jsoncodec.tag"},
		FieldError{Location: "raw", Message: "expected a valid value"})
}

func TestTheDecoderIsSafeForConcurrentUse(t *testing.T) {
	d, _ := CompileDecoder(reflect.TypeFor[Small](), Options{})
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 200 {
				var v Small
				var errs Errors
				if err := d.Decode([]byte(smallBody), unsafe.Pointer(&v), wire.NewSlab(1<<10), &errs); err != nil || len(errs.List) > 0 || v.Items[2].SKU != "c" {
					t.Error(err, errs, v)
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}

func TestDecodingNeverModifiesTheBody(t *testing.T) {
	d, _ := CompileDecoder(reflect.TypeFor[Order](), Options{})
	body := []byte(fullOrder)
	orig := append([]byte(nil), body...)
	var o Order
	var errs Errors
	if err := d.Decode(body, unsafe.Pointer(&o), wire.NewSlab(1<<12), &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs.List)
	}
	if string(body) != string(orig) {
		t.Error("the body was changed; strings point into it")
	}
}

func TestAPooledDecoderKeepsNothingOfTheLastRequest(t *testing.T) {
	// A second decode must not see the first one's errors, depth or path.
	d, _ := CompileDecoder(reflect.TypeFor[multi](), Options{})
	var errs Errors
	var v multi
	d.Decode([]byte(`{"name":5,"age":[[[`), unsafe.Pointer(&v), wire.NewSlab(64), &errs)
	errs.Reset()
	v = multi{}
	if err := d.Decode([]byte(`{"name":"a","age":1,"tags":[]}`), unsafe.Pointer(&v), wire.NewSlab(64), &errs); err != nil || len(errs.List) > 0 {
		t.Errorf("%v %v", err, errs.List)
	}
}

func TestEveryKindOfBodyErrorCarriesItsRuleCode(t *testing.T) {
	type rules struct {
		Name  string
		Age   int
		Level Level
		Req   string
		Tags  []int
	}
	_, errs, err := decodeInto[rules](t, Options{}, `{"name":5,"age":"x","level":"nope","tags":[1,"b"]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []FieldError{
		{Location: "name", Message: "expected a string", Rule: wire.RuleType},
		{Location: "age", Message: "expected an integer", Rule: wire.RuleType},
		{Location: "level", Message: "expected one of: low, high", Rule: wire.RuleValues},
		{Location: "tags[1]", Message: "expected an integer", Rule: wire.RuleType},
		{Location: "req", Message: "required", Rule: wire.RuleRequired},
	}
	if !reflect.DeepEqual(errs.List, want) {
		t.Errorf("got %+v\nwant %+v", errs.List, want)
	}
}
