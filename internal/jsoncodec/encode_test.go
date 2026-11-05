package jsoncodec

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

func encodeOf[T any](t testing.TB, o Options, v T) (string, error) {
	t.Helper()
	e, err := CompileEncoder(reflect.TypeFor[T](), o)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := e.Append([]byte("prefix:"), unsafe.Pointer(&v))
	if err != nil {
		if string(out) != "prefix:" {
			t.Errorf("an error must leave dst as passed, got %q", out)
		}
		return "", err
	}
	return strings.TrimPrefix(string(out), "prefix:"), nil
}

func mustEncode[T any](t testing.TB, v T) string {
	t.Helper()
	s, err := encodeOf(t, Options{}, v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAnEmptyOptIsLeftOutAndANullIsWrittenNull(t *testing.T) {
	type out struct {
		A wire.Opt[string]
		B wire.Null[string]
		C wire.Opt[wire.Null[int]]
		D wire.Opt[int]
	}
	got := mustEncode(t, out{D: wire.Some(0)})
	if got != `{"b":null,"d":0}` {
		t.Errorf("%s", got)
	}
	got = mustEncode(t, out{A: wire.Some("x"), B: wire.NonNull("y"), C: wire.Some(wire.Null[int]{})})
	if got != `{"a":"x","b":"y","c":null}` {
		t.Errorf("%s", got)
	}
	got = mustEncode(t, out{C: wire.Some(wire.NonNull(4))})
	if got != `{"b":null,"c":4}` {
		t.Errorf("%s", got)
	}
}

func TestPointersAreAllowedInAnOutputAndNilIsNull(t *testing.T) {
	type out struct {
		P *int
		Q *Address
		R **string
	}
	n := 5
	if got := mustEncode(t, out{P: &n, Q: &Address{"s", "z"}}); got != `{"p":5,"q":{"street":"s","zip_code":"z"},"r":null}` {
		t.Errorf("%s", got)
	}
}

func TestANilSliceIsAnEmptyArrayAndANilMapAnEmptyObject(t *testing.T) {
	type out struct {
		S []int
		M map[string]int
		E []string
	}
	if got := mustEncode(t, out{E: []string{}}); got != `{"s":[],"m":{},"e":[]}` {
		t.Errorf("%s", got)
	}
}

func TestMapMembersAreWrittenInKeyOrder(t *testing.T) {
	got := mustEncode(t, map[string][]int{"b": {1}, "a": nil, "c": {2, 3}})
	if got != `{"a":[],"b":[1],"c":[2,3]}` {
		t.Errorf("%s", got)
	}
}

func TestOmitemptyAndOmitzeroFollowV2(t *testing.T) {
	type out struct {
		A string         `json:"a,omitempty"`
		B []int          `json:"b,omitempty"`
		C map[string]int `json:"c,omitempty"`
		D *int           `json:"d,omitempty"`
		E int            `json:"e,omitempty"`
		F int            `json:"f,omitzero"`
		G time.Time      `json:"g,omitzero"`
		H Address        `json:"h,omitzero"`
		I struct{}       `json:"i,omitempty"`
		J string
	}
	if got := mustEncode(t, out{}); got != `{"e":0,"j":""}` {
		t.Errorf("%s", got)
	}
	if got := mustEncode(t, out{A: "x", B: []int{0}, E: 1, F: 2, H: Address{Street: "s"}}); got != `{"a":"x","b":[0],"e":1,"f":2,"h":{"street":"s","zip_code":""},"j":""}` {
		t.Errorf("%s", got)
	}
}

func TestStringsAreEscapedAsV2DoesAndInvalidUTF8IsAnError(t *testing.T) {
	if got := mustEncode(t, "a\"b\\c\n\x01é<&>\u2028"); got != "\"a\\\"b\\\\c\\n\\u0001é<&>\u2028\"" {
		t.Errorf("%s", got)
	}
	if _, err := encodeOf(t, Options{}, "bad\xff"); err == nil {
		t.Error("invalid UTF-8 was written")
	}
}

func TestFloatsUseTheShortestFormAndNaNIsAnError(t *testing.T) {
	cases := map[float64]string{0: "0", 1.5: "1.5", 1e21: "1e+21", 1e-7: "1e-7", 100000: "100000", -2.5e-9: "-2.5e-9", 123456789012: "123456789012"}
	for f, want := range cases {
		if got := mustEncode(t, f); got != want {
			t.Errorf("%v: %s, want %s", f, got, want)
		}
	}
	if got := mustEncode(t, math.Copysign(0, -1)); got != "-0" {
		t.Errorf("-0: %s", got)
	}
	if got := mustEncode(t, float32(0.1)); got != "0.1" {
		t.Errorf("float32: %s", got)
	}
	for _, f := range []float64{math.NaN(), math.Inf(1)} {
		if _, err := encodeOf(t, Options{}, f); err == nil {
			t.Errorf("%v was written", f)
		}
	}
}

func TestUuidAndTimeAreStrings(t *testing.T) {
	type out struct {
		U uuid.UUID
		T time.Time
	}
	got := mustEncode(t, out{uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8"), time.Date(2026, 10, 31, 12, 0, 0, 500, time.FixedZone("", 7*3600))})
	if got != `{"u":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","t":"2026-10-31T12:00:00.0000005+07:00"}` {
		t.Errorf("%s", got)
	}
	if _, err := encodeOf(t, Options{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Error("a year outside 0..9999 was written")
	}
}

func TestEncodingToAReusedSliceAppendsAfterWhatIsThere(t *testing.T) {
	e, _ := CompileEncoder(reflect.TypeFor[Address](), Options{})
	a := Address{"s", "z"}
	buf, err := e.Append([]byte("x"), unsafe.Pointer(&a))
	if err != nil || string(buf) != `x{"street":"s","zip_code":"z"}` {
		t.Errorf("%s %v", buf, err)
	}
}

func TestATypeWithItsOwnMarshalerAndAnInterfaceAreHandedToV2(t *testing.T) {
	type out struct {
		Price decimal
		Any   any
		Raw   []byte
		None  any
	}
	got, err := encodeOf(t, declaresDecimal, out{Price: decimal{cents: 7}, Any: map[string]int{"a": 1}, Raw: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"price":"7","any":{"a":1},"raw":"aGk=","none":null}` {
		t.Errorf("%s", got)
	}
}

func TestACyclicValueIsAnErrorNotAStackOverflow(t *testing.T) {
	type node struct{ Next *node }
	n := &node{}
	n.Next = n
	if _, err := encodeOf(t, Options{}, n); err == nil {
		t.Error("a cycle was written")
	}
}

func TestAnEmbeddedStructIsInlinedWhenEncoding(t *testing.T) {
	type out struct {
		Audit
		Name string
	}
	if got := mustEncode(t, out{Audit{"me", 3}, "n"}); got != `{"created_by":"me","revision":3,"name":"n"}` {
		t.Errorf("%s", got)
	}
}

func TestAnEncodedBodyDecodesBackToTheSameValue(t *testing.T) {
	d, _ := CompileDecoder(reflect.TypeFor[Order](), Options{})
	var o Order
	var errs Errors
	if err := d.Decode([]byte(fullOrder), unsafe.Pointer(&o), wire.NewSlab(1<<12), &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs.List)
	}
	body := mustEncode(t, o)
	var back Order
	if err := d.Decode([]byte(body), unsafe.Pointer(&back), wire.NewSlab(1<<12), &errs); err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs.List, body)
	}
	if !reflect.DeepEqual(o, back) {
		t.Errorf("\n%+v\n%+v", o, back)
	}
}

type keepsReceiver struct{ N int }

var keptReceiver *keepsReceiver

func (k *keepsReceiver) IsZero() bool { keptReceiver = k; return k.N == 0 }

func TestAPointerReceiverIsZeroIsCalledOnACopyNeverOnTheValueBeingEncoded(t *testing.T) {
	type out struct {
		K keepsReceiver `json:",omitzero"`
	}
	v := out{K: keepsReceiver{N: 1}}
	e, err := CompileEncoder(reflect.TypeFor[out](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Append(nil, unsafe.Pointer(&v))
	if err != nil || string(got) != `{"k":{"n":1}}` {
		t.Fatalf("%s %v", got, err)
	}
	if keptReceiver == nil || keptReceiver == &v.K {
		t.Fatalf("IsZero must run on a copy, got %p for the field at %p", keptReceiver, &v.K)
	}
}
