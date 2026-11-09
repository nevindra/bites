package jsoncodec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nevindra/bites/internal/wire"
)

// foreign is another module's id type: it has a text form and no JSON method.
type foreign [4]byte

func (f foreign) MarshalText() ([]byte, error) { return f[:], nil }
func (f *foreign) UnmarshalText(b []byte) error {
	if len(b) != 4 {
		return errors.New("bad id") // the codec drops this text
	}
	copy(f[:], b)
	return nil
}

type withDate struct {
	On    wire.Date
	Until wire.Opt[wire.Date]
}

func TestADateInABodyIsReadAndWrittenAsYYYYMMDD(t *testing.T) {
	v, errs, err := decodeInto[withDate](t, Options{}, `{"on":"2026-11-03","until":"2027-02-28"}`)
	if err != nil || len(errs.List) > 0 {
		t.Fatal(err, errs.List)
	}
	if v.On.String() != "2026-11-03" || v.Until.Or(wire.Date{}).String() != "2027-02-28" {
		t.Fatalf("got %+v", v)
	}
	if got := mustEncode(t, v); got != `{"on":"2026-11-03","until":"2027-02-28"}` {
		t.Errorf("got %s", got)
	}
}

func TestADateInABodyThatIsNotACalendarDayIsOneSentenceThatNeverQuotesTheValue(t *testing.T) {
	for _, in := range []string{`"2026-02-30"`, `"2026-11-03T10:00:00Z"`, `"secret-value-xyz"`} {
		_, errs, _ := decodeInto[withDate](t, Options{}, `{"on":`+in+`}`)
		wantErrors(t, errs, FieldError{Location: "on", Message: "expected a date as YYYY-MM-DD"})
		if strings.Contains(errs.List[0].Message, "secret") {
			t.Error("the client's value was echoed")
		}
	}
	_, errs, _ := decodeInto[withDate](t, Options{}, `{"on":20261103}`)
	wantErrors(t, errs, FieldError{Location: "on", Message: "expected a date as YYYY-MM-DD"})
}

func TestADateIsDescribedAsAStringWithTheDateFormatInBothDirections(t *testing.T) {
	for _, dir := range []Direction{Decoding, Encoding} {
		sh, err := Describe(reflect.TypeFor[wire.Date](), Options{}, dir)
		if err != nil || sh.Kind != KindDate {
			t.Errorf("%v: %v %v", dir, sh.Kind, err)
		}
	}
}

func TestARegisteredTypeIsDescribedWithItsFormatAndToldTheFormatsSentence(t *testing.T) {
	o := Options{Formats: wire.Formats{reflect.TypeFor[foreign](): "uuid"}}
	for _, dir := range []Direction{Decoding, Encoding} {
		sh, err := Describe(reflect.TypeFor[foreign](), o, dir)
		if err != nil || sh.Kind != KindText || sh.Format != "uuid" {
			t.Errorf("%v: %+v %v", dir, sh, err)
		}
	}
	type in struct{ ID foreign }
	_, errs, _ := decodeInto[in](t, o, `{"id":"toolong"}`)
	wantErrors(t, errs, FieldError{Location: "id", Message: "expected a UUID"})
	_, errs, _ = decodeInto[in](t, Options{}, `{"id":"toolong"}`)
	wantErrors(t, errs, FieldError{Location: "id", Message: "expected a valid value of type jsoncodec.foreign"})
	v, errs, _ := decodeInto[in](t, o, `{"id":"abcd"}`)
	if len(errs.List) > 0 || string(v.ID[:]) != "abcd" {
		t.Errorf("%v %q", errs.List, v.ID[:])
	}
	if got := mustEncode(t, v); got != `{"ID":"abcd"}` && got != `{"id":"abcd"}` {
		t.Errorf("got %s", got)
	}
}

func TestTheDecoderRefusesAnUnexportedFieldInABodyAtAnyDepthButNotOneTaggedOutAndTheEncoderStaysLenient(t *testing.T) {
	type inner struct{ hidden int }
	for name, typ := range map[string]reflect.Type{
		"at the root": reflect.TypeFor[struct {
			Name   string
			secret int
		}](),
		"in a nested struct": reflect.TypeFor[struct{ In inner }](),
		"in a slice":         reflect.TypeFor[struct{ In []inner }](),
		"in a map":           reflect.TypeFor[map[string]inner](),
		"in an Opt":          reflect.TypeFor[struct{ In wire.Opt[inner] }](),
	} {
		_, err := CompileDecoder(typ, Options{})
		if err == nil || !strings.Contains(err.Error(), "it is unexported, so nothing can fill it: give it an exported name, or tag it json:\"-\" to leave it out on purpose") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := CompileEncoder(typ, Options{}); err != nil {
			t.Errorf("%s: the encoder refused it: %v", name, err)
		}
	}
	type tagged struct {
		Name   string
		secret int `json:"-"`
	}
	if _, err := CompileDecoder(reflect.TypeFor[tagged](), Options{}); err != nil {
		t.Errorf("a field tagged json:\"-\" was refused: %v", err)
	}
}

func TestATypeThatDecodesItselfIsRefusedUntilItsSchemaIsDeclared(t *testing.T) {
	type in struct{ Price decimal }
	_, err := CompileDecoder(reflect.TypeFor[in](), Options{})
	if err == nil || !strings.Contains(err.Error(), "in.Price: jsoncodec.decimal decodes itself through its own UnmarshalJSON") || !strings.Contains(err.Error(), "WithJSONSchema[jsoncodec.decimal]") {
		t.Fatalf("got %v", err)
	}
	if _, err := CompileDecoder(reflect.TypeFor[in](), declaresDecimal); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTheDirectionTheTypeTravelsIsRefused(t *testing.T) {
	// tag has UnmarshalText alone: it is a string in both directions.
	type out struct{ Tag tag }
	if _, err := CompileEncoder(reflect.TypeFor[out](), Options{}); err != nil {
		t.Errorf("a text-only type is a string and is not refused: %v", err)
	}
	if _, err := CompileDecoder(reflect.TypeFor[struct{ Tag tag }](), Options{}); err != nil {
		t.Errorf("a text-only type is a string and is not refused: %v", err)
	}
}

func TestARawMessageIsFreeFormByNameAndNeedsNoSchema(t *testing.T) {
	type body struct{ Payload json.RawMessage }
	if _, err := CompileDecoder(reflect.TypeFor[body](), Options{}); err != nil {
		t.Error(err)
	}
	if _, err := CompileEncoder(reflect.TypeFor[body](), Options{}); err != nil {
		t.Error(err)
	}
}
