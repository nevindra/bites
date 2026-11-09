package wire

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
	"uuid"
)

type status string

func (status) Values() []status { return []status{"draft", "sent"} }

type colour string

func (*colour) Values() []string { return []string{"red", "green"} }

type emptyEnum string

func (emptyEnum) Values() []emptyEnum { return nil }

type badEnum string

func (badEnum) Values() int { return 0 }

var errUnmarshal = errors.New("the type's own error text")

type hexByte byte

func (h *hexByte) UnmarshalText(b []byte) error {
	if len(b) != 2 {
		return errUnmarshal
	}
	*h = hexByte(b[0])
	return nil
}

// decode compiles T and decodes s into a fresh T.
func decode[T any](s string) (T, error) {
	var v T
	dec, err := CompileText(reflect.TypeFor[T]())
	if err != nil {
		panic(err)
	}
	return v, dec(unsafe.Pointer(&v), s)
}

func msg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type textCase[T comparable] struct {
	in   string
	want T
	err  string // exact message, or "" when accepted
}

func runText[T comparable](t *testing.T, cases []textCase[T]) {
	t.Helper()
	for _, c := range cases {
		got, err := decode[T](c.in)
		if msg(err) != c.err {
			t.Errorf("%T %q: error %q, want %q", got, c.in, msg(err), c.err)
			continue
		}
		if c.err == "" && got != c.want {
			t.Errorf("%T %q: got %v, want %v", got, c.in, got, c.want)
		}
		if c.err != "" && got != *new(T) {
			t.Errorf("%T %q: destination written on error: %v", got, c.in, got)
		}
	}
}

func TestAStringIsDecodedAsItselfWithoutACopy(t *testing.T) {
	src := "hello"
	got, err := decode[string](src)
	if err != nil || got != src || unsafe.StringData(got) != unsafe.StringData(src) {
		t.Errorf("got %q, %v", got, err)
	}
	type name string
	if n, _ := decode[name]("x"); n != "x" {
		t.Error("named string")
	}
	if got, err := decode[string](""); err != nil || got != "" {
		t.Error("empty string must be accepted")
	}
}

func TestABoolIsTrueFalseOneOrZeroAndNothingElse(t *testing.T) {
	const m = "expected true or false"
	runText(t, []textCase[bool]{
		{"true", true, ""}, {"false", false, ""}, {"1", true, ""}, {"0", false, ""},
		{"True", false, m}, {"yes", false, m}, {"", false, m}, {"t", false, m}, {"TRUE", false, m},
	})
}

func TestIntegersAreRangeCheckedPerKind(t *testing.T) {
	runText(t, []textCase[int8]{
		{"-128", -128, ""}, {"127", 127, ""}, {"+5", 5, ""},
		{"128", 0, "expected an integer between -128 and 127"},
		{"-129", 0, "expected an integer between -128 and 127"},
		{"", 0, "expected an integer"}, {"1.5", 0, "expected an integer"},
		{"abc", 0, "expected an integer"}, {"1_0", 0, "expected an integer"},
		{"0x10", 0, "expected an integer"}, {" 1", 0, "expected an integer"},
	})
	runText(t, []textCase[int64]{
		{"9223372036854775807", 1<<63 - 1, ""},
		{"9223372036854775808", 0, "expected an integer between -9223372036854775808 and 9223372036854775807"},
	})
	runText(t, []textCase[int16]{{"-32769", 0, "expected an integer between -32768 and 32767"}})
	runText(t, []textCase[int32]{{"2147483648", 0, "expected an integer between -2147483648 and 2147483647"}})
	runText(t, []textCase[int]{{"42", 42, ""}, {"x", 0, "expected an integer"}})
}

func TestUnsignedIntegersRefuseNegativesAndOverflow(t *testing.T) {
	runText(t, []textCase[uint8]{
		{"0", 0, ""}, {"255", 255, ""},
		{"256", 0, "expected an integer between 0 and 255"},
		{"-1", 0, "expected a non-negative integer"},
		{"", 0, "expected a non-negative integer"},
		{"a", 0, "expected a non-negative integer"},
	})
	runText(t, []textCase[uint64]{
		{"18446744073709551615", 1<<64 - 1, ""},
		{"18446744073709551616", 0, "expected an integer between 0 and 18446744073709551615"},
	})
	runText(t, []textCase[uint16]{{"65536", 0, "expected an integer between 0 and 65535"}})
	runText(t, []textCase[uint32]{{"4294967296", 0, "expected an integer between 0 and 4294967295"}})
	runText(t, []textCase[uint]{{"7", 7, ""}})
}

func TestFloatsAreDecimalNumbersOnly(t *testing.T) {
	const m = "expected a number"
	runText(t, []textCase[float64]{
		{"1.5", 1.5, ""}, {"-2e3", -2000, ""}, {"0", 0, ""}, {".5", 0.5, ""},
		{"", 0, m}, {"abc", 0, m}, {"NaN", 0, m}, {"Inf", 0, m}, {"-inf", 0, m},
		{"1e999", 0, m}, {"0x1p-2", 0, m}, {"1_0", 0, m},
	})
	runText(t, []textCase[float32]{{"1.5", 1.5, ""}, {"1e39", 0, m}})
}

func TestATimeIsRFC3339WithAnOffsetAndABareDateIsRefused(t *testing.T) {
	const m = "expected an RFC 3339 time with an offset"
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := decode[time.Time]("2026-01-02T03:04:05Z")
	if err != nil || !got.Equal(want) {
		t.Errorf("got %v, %v", got, err)
	}
	got, err = decode[time.Time]("2026-01-02T05:04:05+02:00")
	if err != nil || !got.Equal(want) {
		t.Errorf("got %v, %v", got, err)
	}
	for _, in := range []string{"2026-01-01", "2026-01-02T03:04:05", "", "yesterday", "2026-13-01T00:00:00Z"} {
		if _, err := decode[time.Time](in); msg(err) != m {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestAUUIDIsParsedAndAnythingElseIsRefused(t *testing.T) {
	const s = "123e4567-e89b-42d3-a456-426614174000"
	got, err := decode[uuid.UUID](s)
	if err != nil || got.String() != s {
		t.Errorf("got %v, %v", got, err)
	}
	for _, in := range []string{"", "nope", s[:35], strings.ToUpper(s[:35]) + "Z"} {
		if _, err := decode[uuid.UUID](in); msg(err) != "expected a UUID" {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestATextUnmarshalerTypeUsesItsOwnParserAndHidesItsError(t *testing.T) {
	got, err := decode[hexByte]("ab")
	if err != nil || got != 'a' {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := decode[hexByte]("abc"); msg(err) != "expected a valid value of type wire.hexByte" {
		t.Errorf("err = %v", err)
	}
}

func TestAnEnumAcceptsOnlyTheListedValues(t *testing.T) {
	runText(t, []textCase[status]{
		{"draft", "draft", ""}, {"sent", "sent", ""},
		{"Draft", "", "expected one of: draft, sent"},
		{"", "", "expected one of: draft, sent"},
		{"other", "", "expected one of: draft, sent"},
	})
	// Values on a pointer receiver returning []string is also an enum.
	runText(t, []textCase[colour]{
		{"red", "red", ""}, {"blue", "", "expected one of: red, green"},
	})
}

func TestAnEnumWhoseValuesCannotWorkIsRefusedAtCompileTime(t *testing.T) {
	for _, ty := range []reflect.Type{reflect.TypeFor[emptyEnum](), reflect.TypeFor[badEnum]()} {
		if _, err := CompileText(ty); err == nil || !strings.Contains(err.Error(), "Values") {
			t.Errorf("%v: err = %v", ty, err)
		}
	}
}

func TestOptAndNullDecodersFillTheValueAndSetTheFlag(t *testing.T) {
	o, err := decode[Opt[int]]("5")
	if v, ok := o.Get(); err != nil || !ok || v != 5 {
		t.Errorf("Opt: %v, %v", o, err)
	}
	o, err = decode[Opt[int]]("x")
	if msg(err) != "expected an integer" || o.IsSet() {
		t.Errorf("Opt bad: %v, %v", o, err)
	}
	n, err := decode[Null[status]]("sent")
	if v, ok := n.Get(); err != nil || !ok || v != "sent" {
		t.Errorf("Null: %v, %v", n, err)
	}
	if _, err := decode[Null[status]]("zzz"); msg(err) != "expected one of: draft, sent" {
		t.Errorf("Null bad: %v", err)
	}
	nested, err := decode[Opt[Null[uuid.UUID]]]("123e4567-e89b-42d3-a456-426614174000")
	if v, ok := nested.Get(); err != nil || !ok || v.IsNull() {
		t.Errorf("nested: %v, %v", nested, err)
	}
	tm, err := decode[Opt[time.Time]]("2026-01-02T03:04:05Z")
	if v, ok := tm.Get(); err != nil || !ok || v.Year() != 2026 {
		t.Errorf("Opt[time]: %v, %v", tm, err)
	}
}

func TestTypesThatAreNotTextAreRefusedWithAReason(t *testing.T) {
	type st struct{ A int }
	cases := []struct {
		t    reflect.Type
		want string
	}{
		{reflect.TypeFor[*int](), "use Opt[T] or Null[T]"},
		{reflect.TypeFor[[]string](), "repeated keys"},
		{reflect.TypeFor[[]byte](), "repeated keys"},
		{reflect.TypeFor[Opt[[]string]](), "repeated keys"},
		{reflect.TypeFor[[2]int](), "repeated keys"},
		{reflect.TypeFor[map[string]string](), "UnmarshalText"},
		{reflect.TypeFor[st](), "UnmarshalText"},
		{reflect.TypeFor[any](), "interface"},
		{reflect.TypeFor[error](), "interface"},
		{reflect.TypeFor[complex128](), "cannot be decoded"},
		{reflect.TypeFor[chan int](), "cannot be decoded"},
		{reflect.TypeFor[Opt[*int]](), "use Opt[T] or Null[T]"},
	}
	for _, c := range cases {
		dec, err := CompileText(c.t)
		if err == nil || dec != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want containing %q", c.t, err, c.want)
		}
	}
}

func TestDecodersNeverEchoTheValueTheyRefuse(t *testing.T) {
	secret := strings.Repeat("S3CRET", 100)
	for _, ty := range []reflect.Type{
		reflect.TypeFor[int](), reflect.TypeFor[uint8](), reflect.TypeFor[bool](), reflect.TypeFor[float64](),
		reflect.TypeFor[time.Time](), reflect.TypeFor[uuid.UUID](), reflect.TypeFor[status](), reflect.TypeFor[hexByte](),
	} {
		dec, _ := CompileText(ty)
		p := reflect.New(ty)
		err := dec(p.UnsafePointer(), secret)
		if err == nil {
			t.Fatalf("%v accepted garbage", ty)
		}
		if strings.Contains(err.Error(), "S3CRET") || len(err.Error()) > 64 {
			t.Errorf("%v echoes or is long: %q", ty, err)
		}
	}
}

func TestDecodingIntBoolStringEnumAndUUIDAllocatesNothing(t *testing.T) {
	const u = "123e4567-e89b-42d3-a456-426614174000"
	check := func(name string, ty reflect.Type, in string) {
		dec, err := CompileText(ty)
		if err != nil {
			t.Fatal(err)
		}
		p := reflect.New(ty).UnsafePointer()
		if n := testing.AllocsPerRun(100, func() {
			if err := dec(p, in); err != nil {
				panic(err)
			}
		}); n != 0 {
			t.Errorf("%s: allocs = %v", name, n)
		}
	}
	check("int", reflect.TypeFor[int](), "12345")
	check("uint8", reflect.TypeFor[uint8](), "200")
	check("bool", reflect.TypeFor[bool](), "true")
	check("string", reflect.TypeFor[string](), "hello")
	check("enum", reflect.TypeFor[status](), "sent")
	check("uuid", reflect.TypeFor[uuid.UUID](), u)
	check("float", reflect.TypeFor[float64](), "1.25")
	check("Opt[int]", reflect.TypeFor[Opt[int]](), "9")
	check("Null[uuid]", reflect.TypeFor[Null[uuid.UUID]](), u)
}

type keeper struct{ n int }

var kept *keeper

func (k *keeper) UnmarshalText(b []byte) error { kept = k; k.n = len(b); return nil }

func TestAnUnmarshalTextMethodNeverReceivesTheDestinationItself(t *testing.T) {
	dec, err := CompileText(reflect.TypeFor[keeper]())
	if err != nil {
		t.Fatal(err)
	}
	var dst keeper
	if err := dec(unsafe.Pointer(&dst), "abc"); err != nil {
		t.Fatal(err)
	}
	if dst.n != 3 {
		t.Fatalf("the decoded value was not copied to the destination: %+v", dst)
	}
	if kept == &dst {
		t.Fatal("the method was given the destination, so a kept receiver would point into the caller's stack")
	}
}

// foreignID stands for another module's uuid type: bytes with a text form.
type foreignID [16]byte

func (f *foreignID) UnmarshalText(b []byte) error {
	if len(b) != 4 || string(b) == "nope" {
		return errUnmarshal
	}
	copy(f[:], b)
	return nil
}

func TestATypeRegisteredWithAFormatSaysTheFormatsSentenceWhenItDoesNotDecode(t *testing.T) {
	typ := reflect.TypeFor[foreignID]()
	for format, want := range map[string]string{"uuid": "expected a UUID", "date": "expected a date as YYYY-MM-DD", "ulid": "expected a valid ulid"} {
		dec, err := CompileTextWith(typ, Formats{typ: format})
		if err != nil {
			t.Fatal(err)
		}
		var v foreignID
		err = dec(unsafe.Pointer(&v), "toolong")
		if msg(err) != want {
			t.Errorf("%s: got %q, want %q", format, msg(err), want)
		}
		if err := dec(unsafe.Pointer(&v), "abcd"); err != nil || string(v[:4]) != "abcd" {
			t.Errorf("%s: %v %q", format, err, v[:4])
		}
	}
}

func TestATypeWithoutARegisteredFormatIsToldTheSentenceNamingItsType(t *testing.T) {
	dec, _ := CompileText(reflect.TypeFor[foreignID]())
	var v foreignID
	if got := msg(dec(unsafe.Pointer(&v), "toolong")); got != "expected a valid value of type wire.foreignID" {
		t.Errorf("got %q", got)
	}
}
