package jsoncodec

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

// Flat has one field of every kind the fast path decodes, an Opt of two of them and two
// constraint types.
type Flat struct {
	Name   string
	Small  int8
	Age    int
	Count  uint16
	Big    uint64
	Ratio  float32
	Score  float64
	Active bool
	Level  Level
	Code   jCode
	N      wire.PositiveInt
	Nick   wire.Opt[string]
	Page   wire.Opt[int]
}

const flatBody = `{"name":"Ada Lovelace","small":-5,"age":36,"count":65535,"big":18446744073709551615,` +
	`"ratio":1.5,"score":-2.5e-3,"active":true,"level":"high","code":"abc","n":7,"nick":"ada","page":3}`

// flatRest is every required member but name, so a case can vary name alone.
const flatRest = `"small":1,"age":1,"count":1,"big":1,"ratio":1,"score":1,"active":true,"level":"low","code":"a","n":1}`

func flatDecoder(tb testing.TB) *Decoder {
	tb.Helper()
	d, err := CompileDecoder(reflect.TypeFor[Flat](), Options{})
	if err != nil {
		tb.Fatal(err)
	}
	if !d.fast {
		tb.Fatal("Flat is not on the fast path")
	}
	return d
}

// agree checks the property the fast path is held to: whatever it accepts, the tokenizer accepts
// with no error and decodes to the same value.
func agree(t *testing.T, d *Decoder, body []byte) (fast bool) {
	t.Helper()
	var a, b Flat
	if !d.decodeFast(body, unsafe.Pointer(&a)) {
		return false
	}
	var errs Errors
	if err := d.decodeTokens(body, unsafe.Pointer(&b), wire.NewSlab(len(body)+64), &errs); err != nil || len(errs.List) > 0 {
		t.Fatalf("the fast path accepted %q, the tokenizer refused it: %v %v", body, err, errs.List)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%q decodes to\n%+v on the fast path and\n%+v through the tokenizer", body, a, b)
	}
	return true
}

func TestAFlatBodyInsideTheSubsetIsDecodedOnTheFastPathAndAgreesWithTheTokenizer(t *testing.T) {
	d := flatDecoder(t)
	for _, s := range []string{
		flatBody,
		" \t\r\n" + flatBody + "\n ",
		`{"name":"","small":0,"age":-0,"count":0,"big":0,"ratio":0,"score":1E+2,"active":false,"level":"low","code":"a","n":1}`,
		`{ "n" : 1 , "code" : "ab" , "level" : "low" , "active" : true , "score" : 0.5 , "ratio" : 2 , "big" : 1 , "count" : 1 , "age" : 1 , "small" : 127 , "name" : "x~` + "\x7f" + `" }`,
	} {
		if !agree(t, d, []byte(s)) {
			t.Errorf("%q fell back to the tokenizer, want the fast path", s)
		}
	}
}

func TestABodyOutsideTheSubsetFallsBackAndTheTokenizerAnswersAsBefore(t *testing.T) {
	d := flatDecoder(t)
	for _, s := range []string{
		``, `null`, `[]`, `{}`, `{} x`, `{"name":"a"`, flatBody + `,`,
		`{"name":"` + "\\" + `u0041",` + flatRest, // an escape
		`{"name":"é",` + flatRest,                 // not ASCII
		`{"name":"a\tb",` + flatRest,              // a control byte
		`{"name":"a","name":"b"}`,                 // a duplicate
		`{"other":1}`,                             // unknown
		`{"age":1.0}`, `{"age":1e2}`, `{"age":01}`, `{"small":128}`, `{"count":-1}`, `{"ratio":1e999}`,
		`{"active":tru}`, `{"active":truex}`, `{"level":"mid"}`, `{"code":""}`, `{"n":0}`, `{"nick":null}`,
		`{"name":"a","small":1,"age":1,"count":1,"big":1,"ratio":1,"score":1,"active":true,"level":"low","code":"a"}`, // n missing
	} {
		if agree(t, d, []byte(s)) {
			t.Errorf("%q was taken by the fast path, want the tokenizer", s)
		}
	}
	// The error a client sees is the tokenizer's, as it was before the fast path.
	var v Flat
	var errs Errors
	if err := d.Decode([]byte(`{"age":1.5,"name":"a"}`), unsafe.Pointer(&v), wire.NewSlab(64), &errs); err != nil {
		t.Fatal(err)
	}
	if len(errs.List) == 0 || errs.List[0].Location != "age" {
		t.Fatalf("errors = %v, want age named first", errs.List)
	}
}

func TestAStructWithANestedObjectOrAListIsNotOnTheFastPath(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[Small](), reflect.TypeFor[Order](), reflect.TypeFor[constrained]()} {
		d, err := CompileDecoder(typ, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if d.fast {
			t.Errorf("%v is on the fast path", typ)
		}
	}
}

func TestTheFastPathAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	d := flatDecoder(t)
	body := []byte(flatBody)
	slab := wire.NewSlab(64)
	var errs Errors
	n := testing.AllocsPerRun(200, func() {
		var v Flat
		if err := d.Decode(body, unsafe.Pointer(&v), slab, &errs); err != nil || len(errs.List) > 0 {
			t.Fatal(err, errs.List)
		}
	})
	if n != 0 {
		t.Fatalf("%v allocations, want 0", n)
	}
}

func FuzzTheFastPathAgreesWithTheTokenizer(f *testing.F) {
	for _, s := range []string{
		flatBody, `{}`, `{"age":1.0}`, `{"name":"a","name":"b"}`, `{"small":-128}`, `{"count":65536}`,
		`{"score":-0.0e-0}`, `{"ratio":3.4028235e38}`, `{"ratio":3.5e38}`, `{"level":"low"}`, `{"nick":"x","page":-1}`,
	} {
		f.Add([]byte(s))
	}
	d := flatDecoder(f)
	f.Fuzz(func(t *testing.T, body []byte) {
		agree(t, d, body)
	})
}

func BenchmarkDecodeAFlatBody(b *testing.B) {
	d := flatDecoder(b)
	body := []byte(flatBody)
	slab := wire.NewSlab(64)
	var errs Errors
	b.Run("fast", func(b *testing.B) {
		for range b.N {
			var v Flat
			d.Decode(body, unsafe.Pointer(&v), slab, &errs)
		}
	})
	b.Run("tokenizer", func(b *testing.B) {
		for range b.N {
			var v Flat
			d.decodeTokens(body, unsafe.Pointer(&v), slab, &errs)
		}
	})
}

type fastMail string

func (m fastMail) Check() error {
	if len(m) == 0 || m[0] == '@' {
		return errFastMail
	}
	return nil
}

var errFastMail = &wire.TextError{}

type CheckedFlat struct {
	Mail fastMail
	Cc   wire.Opt[fastMail]
}

func TestAFlatBodyWhoseFieldsHaveACheckStaysOnTheFastPathUntilACheckFails(t *testing.T) {
	d, err := CompileDecoder(reflect.TypeFor[CheckedFlat](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !d.fast {
		t.Fatal("a flat struct whose fields have a Check is not on the fast path")
	}
	var v CheckedFlat
	if !d.decodeFast([]byte(`{"mail":"a@b","cc":"c@d"}`), unsafe.Pointer(&v)) || v.Mail != "a@b" || v.Cc.Or("") != "c@d" {
		t.Fatalf("a body whose checks pass was not decoded on the fast path: %+v", v)
	}
	if d.decodeFast([]byte(`{"mail":"@b"}`), unsafe.Pointer(&v)) {
		t.Fatal("a body whose check fails was accepted by the fast path, which reports no errors")
	}
}

// The cost of a Check on the fast path: one body, decoded into a type whose second field is a
// plain string and into one whose second field checks itself (docs/research/type-rules).
type benchMail string

func (m benchMail) Check() error {
	if strings.IndexByte(string(m), '@') < 1 {
		return errFastMail
	}
	return nil
}

type (
	benchPlain   struct{ Name, Mail string }
	benchChecked struct {
		Name string
		Mail benchMail
	}
)

func benchFast[T any](b *testing.B) {
	d, err := CompileDecoder(reflect.TypeFor[T](), Options{})
	if err != nil {
		b.Fatal(err)
	}
	var v T
	body := []byte(`{"name":"Ada Lovelace","mail":"ada@example.com"}`)
	b.ReportAllocs()
	for b.Loop() {
		if !d.decodeFast(body, unsafe.Pointer(&v)) {
			b.Fatal("the body left the fast path")
		}
	}
}

func BenchmarkAFlatBodyWithAPlainField(b *testing.B)   { benchFast[benchPlain](b) }
func BenchmarkAFlatBodyWithACheckedField(b *testing.B) { benchFast[benchChecked](b) }
