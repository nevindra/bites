package jsoncodec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

type joinFrame struct{ Room int64 }
type sayFrame struct {
	Text  string
	Reply wire.Opt[int64]
}
type pingFrame struct{}

type clientFrame struct {
	Join wire.Opt[joinFrame]
	Say  wire.Opt[sayFrame]
	Ping wire.Opt[pingFrame]
}

func (clientFrame) Discriminator() string { return "type" }

type inbox struct {
	Frames []clientFrame
}

func TestAUnionIsDecodedIntoTheVariantItsDiscriminatorNamesWhereverTheMemberSits(t *testing.T) {
	v, errs, err := decodeInto[clientFrame](t, Options{}, `{"text":"hi","type":"say","reply":7}`)
	if err != nil || len(errs.List) > 0 {
		t.Fatalf("%v %+v", err, errs.List)
	}
	say, ok := v.Say.Get()
	reply, _ := say.Reply.Get()
	if !ok || say.Text != "hi" || reply != 7 || v.Join.IsSet() || v.Ping.IsSet() {
		t.Fatalf("decoded %+v", v)
	}
	v, errs, err = decodeInto[clientFrame](t, Options{}, `{"type":"ping"}`)
	if err != nil || len(errs.List) > 0 || !v.Ping.IsSet() {
		t.Fatalf("a variant with no members: %v %+v %+v", err, errs.List, v)
	}
}

func TestAUnionsMistakesAreLocatedAsInAnyObject(t *testing.T) {
	_, errs, _ := decodeInto[inbox](t, Options{}, `{"frames":[{"type":"join","room":"x"},{"type":"shout"},{"room":1},{"type":"say","text":"a","extra":1},"no"]}`)
	wantErrors(t, errs,
		FieldError{Location: "frames[0].room", Message: "expected an integer"},
		FieldError{Location: "frames[1].type", Message: "expected one of: join, say, ping"},
		FieldError{Location: "frames[2].type", Message: "required"},
		FieldError{Location: "frames[3].extra", Message: "unknown field"},
		FieldError{Location: "frames[4]", Message: "expected an object"},
	)
}

func TestAUnionIsWrittenAsItsVariantWithTheDiscriminatorFirstAndOneVariantMustBeSet(t *testing.T) {
	var f clientFrame
	f.Say.Set(sayFrame{Text: "hi"})
	if got := mustEncode(t, f); got != `{"type":"say","text":"hi"}` {
		t.Fatalf("got %s", got)
	}
	var p clientFrame
	p.Ping.Set(pingFrame{})
	if got := mustEncode(t, p); got != `{"type":"ping"}` {
		t.Fatalf("a variant with no members: %s", got)
	}
	if _, err := encodeOf(t, Options{}, clientFrame{}); err == nil || !strings.Contains(err.Error(), "has no variant set") {
		t.Fatalf("an empty union: %v", err)
	}
	f.Join.Set(joinFrame{Room: 1})
	if _, err := encodeOf(t, Options{}, f); err == nil || !strings.Contains(err.Error(), `has two variants set, "join" and "say"`) {
		t.Fatalf("two variants: %v", err)
	}
}

type ptrUnion struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (*ptrUnion) Discriminator() string { return "kind" }

type emptyTag struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (emptyTag) Discriminator() string { return "" }

type oneVariant struct{ A wire.Opt[joinFrame] }

func (oneVariant) Discriminator() string { return "type" }

type plainVariant struct {
	A wire.Opt[joinFrame]
	B sayFrame
}

func (plainVariant) Discriminator() string { return "type" }

type clashFrame struct{ Type string }

type clashing struct {
	A wire.Opt[joinFrame]
	B wire.Opt[clashFrame]
}

func (clashing) Discriminator() string { return "type" }

func TestAUnionThatCannotWorkIsRefusedSayingWhatToChange(t *testing.T) {
	for _, tc := range []struct {
		t    reflect.Type
		want string
	}{
		{reflect.TypeFor[ptrUnion](), "declares Discriminator on *ptrUnion, but the codec holds a ptrUnion: declare it on the value receiver"},
		{reflect.TypeFor[emptyTag](), `Discriminator returns "": return the name of the member`},
		{reflect.TypeFor[oneVariant](), "is a union of 1 variants: a union has at least two"},
		{reflect.TypeFor[plainVariant](), `is jsoncodec.sayFrame, but each is an Opt of a struct, the variant whose tag is "b"`},
		{reflect.TypeFor[clashing](), `has a member "type", which is the discriminator of clashing`},
	} {
		for _, compile := range []func() error{
			func() error { _, err := CompileDecoder(tc.t, Options{}); return err },
			func() error { _, err := CompileEncoder(tc.t, Options{}); return err },
			func() error { _, err := Describe(tc.t, Options{}, Decoding); return err },
		} {
			if err := compile(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want %q", tc.t, err, tc.want)
			}
		}
	}
}

func TestDescribeNamesTheDiscriminatorAndEachVariantByItsTag(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[clientFrame](), Options{}, Decoding)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Kind != KindUnion || sh.Tag != "type" || len(sh.Fields) != 3 || sh.Fields[0].Name != "join" || sh.Fields[0].Type != reflect.TypeFor[joinFrame]() || sh.Fields[2].Name != "ping" {
		t.Fatalf("%+v", sh)
	}
}

func TestDecodingAUnionCostsNoAllocationOfItsOwn(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts mean nothing under the race detector")
	}
	d, err := CompileDecoder(reflect.TypeFor[clientFrame](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"room":7,"type":"join"}`)
	slab := wire.NewSlab(256)
	var errs Errors
	n := testing.AllocsPerRun(200, func() {
		var v clientFrame
		errs.Reset()
		d.Decode(body, unsafe.Pointer(&v), slab, &errs)
	})
	if n != 0 {
		t.Fatalf("a union decode allocates %v times", n)
	}
}

func TestAUnionWithTheDiscriminatorTwiceIsRefusedBeforeAVariantIsChosen(t *testing.T) {
	_, _, err := decodeInto[clientFrame](t, Options{}, `{"type":"join","type":"say","room":1}`)
	if err == nil {
		t.Fatal("a duplicate discriminator was read")
	}
}

type refFrame struct{ ID string }

// relayFrame has a variant, Ref, that only servers carry.
type relayFrame struct {
	Said wire.Opt[sayFrame]
	Ping wire.Opt[pingFrame]
	Ref  wire.Opt[refFrame]
}

func (relayFrame) Discriminator() string { return "type" }
func (relayFrame) ServerOnly() []string  { return []string{"Ref"} }

type ptrServerOnly struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (ptrServerOnly) Discriminator() string { return "type" }
func (*ptrServerOnly) ServerOnly() []string { return []string{"B"} }

type strayServerOnly struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (strayServerOnly) Discriminator() string { return "type" }
func (strayServerOnly) ServerOnly() []string  { return []string{"Rfe"} }

type allServerOnly struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (allServerOnly) Discriminator() string { return "type" }
func (allServerOnly) ServerOnly() []string  { return []string{"A", "B"} }

type twiceServerOnly struct {
	A wire.Opt[joinFrame]
	B wire.Opt[sayFrame]
}

func (twiceServerOnly) Discriminator() string { return "type" }
func (twiceServerOnly) ServerOnly() []string  { return []string{"B", "B"} }

func TestAServerOnlyVariantIsLeftOutOfWhatDescribesAUnionToClients(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[relayFrame](), Options{}, Encoding)
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Fields) != 2 || sh.Fields[0].Name != "said" || sh.Fields[1].Name != "ping" {
		t.Fatalf("the variants described are %+v: want said and ping only", sh.Fields)
	}
}

func TestAnEncoderForClientsRefusesAServerOnlyVariantAndOneForTheBrokerWritesIt(t *testing.T) {
	var f relayFrame
	f.Ref.Set(refFrame{ID: "n1"})
	_, err := encodeOf(t, Options{}, f)
	var so *ServerOnlyError
	if !errors.As(err, &so) || so.Variant != "ref" || so.Field != "Ref" {
		t.Fatalf("got %v, want a *ServerOnlyError naming ref", err)
	}
	if !strings.Contains(err.Error(), "the union jsoncodec.relayFrame has its server-only variant ref (field Ref) set") {
		t.Fatalf("the message is %q", err)
	}
	b, err := encodeOf(t, Options{ServerVariants: true}, f)
	if err != nil || string(b) != `{"type":"ref","id":"n1"}` {
		t.Fatalf("the broker's plan wrote %s, %v", b, err)
	}
	var ok relayFrame
	ok.Said.Set(sayFrame{Text: "hi"})
	if got := mustEncode(t, ok); got != `{"type":"said","text":"hi"}` {
		t.Fatalf("a variant that is not server-only: %s", got)
	}
}

func TestADecoderForClientsReadsAServerOnlyTagAsOneTheUnionDoesNotHave(t *testing.T) {
	_, errs, err := decodeInto[relayFrame](t, Options{}, `{"type":"ref","id":"n1"}`)
	if err != nil || len(errs.List) != 1 || errs.List[0].Message != "expected one of: said, ping" {
		t.Fatalf("a client sent the server-only tag: %v %+v", err, errs.List)
	}
	v, errs, err := decodeInto[relayFrame](t, Options{ServerVariants: true}, `{"type":"ref","id":"n1"}`)
	if r, ok := v.Ref.Get(); err != nil || len(errs.List) != 0 || !ok || r.ID != "n1" {
		t.Fatalf("the broker's plan read %+v, %v, %+v", v, err, errs.List)
	}
}

func TestAServerOnlyMethodThatCannotWorkIsRefusedSayingWhatToChange(t *testing.T) {
	for _, tc := range []struct {
		t    reflect.Type
		want string
	}{
		{reflect.TypeFor[ptrServerOnly](), "declares ServerOnly on *ptrServerOnly, but the codec holds a ptrServerOnly: declare it on the value receiver"},
		{reflect.TypeFor[strayServerOnly](), "ServerOnly names Rfe, which is not a variant of strayServerOnly (its variants: A, B)"},
		{reflect.TypeFor[allServerOnly](), "ServerOnly names every variant (A, B), so no client could be sent or send any: leave at least one out"},
		{reflect.TypeFor[twiceServerOnly](), "ServerOnly names B twice: name each variant once"},
	} {
		for _, compile := range []func() error{
			func() error { _, err := CompileDecoder(tc.t, Options{}); return err },
			func() error { _, err := CompileEncoder(tc.t, Options{}); return err },
			func() error { _, err := Describe(tc.t, Options{}, Decoding); return err },
		} {
			if err := compile(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: got %v, want %q", tc.t, err, tc.want)
			}
		}
	}
}

func TestEncodingAUnionWithAServerOnlyMethodAllocatesNothingMoreThanOneWithout(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts mean nothing under the race detector")
	}
	enc := func(t reflect.Type) *Encoder {
		e, err := CompileEncoder(t, Options{})
		if err != nil {
			panic(err)
		}
		return e
	}
	var plain clientFrame
	plain.Say.Set(sayFrame{Text: "hi"})
	var relay relayFrame
	relay.Said.Set(sayFrame{Text: "hi"})
	pe, re := enc(reflect.TypeFor[clientFrame]()), enc(reflect.TypeFor[relayFrame]())
	buf := make([]byte, 0, 256)
	a := testing.AllocsPerRun(200, func() { pe.Append(buf[:0], unsafe.Pointer(&plain)) })
	b := testing.AllocsPerRun(200, func() { re.Append(buf[:0], unsafe.Pointer(&relay)) })
	if a != 0 || b != 0 {
		t.Fatalf("encoding allocates %v times without the method and %v with it", a, b)
	}
}
