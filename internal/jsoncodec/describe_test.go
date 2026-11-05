package jsoncodec

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

func TestDescribeListsTheWireNamesTheEncoderWritesAndTheDecoderAccepts(t *testing.T) {
	for _, o := range []Options{{}, {Names: wire.CamelCase}} {
		typ := reflect.TypeFor[Order]()
		for _, dir := range []Direction{Decoding, Encoding} {
			sh, err := Describe(typ, o, dir)
			if err != nil || sh.Kind != KindStruct {
				t.Fatalf("%v %v", sh.Kind, err)
			}
			var names []string
			for _, f := range sh.Fields {
				names = append(names, f.Name)
			}
			// Encode a value with every field set and compare the member names.
			v := Order{ID: uuid.UUID{1}, Created: time.Now(), Nick: wire.Some("n"), Page: wire.Some(1),
				Deleted: wire.NonNull("d"), Alias: "a", Skip: "s"}
			enc, err := CompileEncoder(typ, o)
			if err != nil {
				t.Fatal(err)
			}
			out, err := enc.Append(nil, unsafe.Pointer(&v))
			if err != nil {
				t.Fatal(err)
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(out, &members); err != nil {
				t.Fatal(err)
			}
			var got []string
			for k := range members {
				got = append(got, k)
			}
			slices.Sort(got)
			slices.Sort(names)
			if !slices.Equal(got, names) {
				t.Errorf("dir %d policy %v: described %v, encoded %v", dir, o.Names != nil, names, got)
			}
		}
	}
}

func TestDescribeFollowsTheDirectionForPointersAndInterfaces(t *testing.T) {
	p := reflect.TypeFor[*int]()
	if sh, err := Describe(p, Options{}, Encoding); err != nil || sh.Kind != KindPtr || sh.Elem.Kind() != reflect.Int {
		t.Errorf("encoding a pointer: %v %v", sh, err)
	}
	_, derr := Describe(p, Options{}, Decoding)
	_, cerr := CompileDecoder(p, Options{})
	if derr == nil || cerr == nil || derr.Error() != cerr.Error() {
		t.Errorf("the refusal must be the decoder's: %v vs %v", derr, cerr)
	}
	if sh, err := Describe(reflect.TypeFor[any](), Options{}, Encoding); err != nil || sh.Kind != KindAny {
		t.Errorf("%v %v", sh, err)
	}
}

func TestDescribeReadsTheEnumFromValues(t *testing.T) {
	sh, err := Describe(reflect.TypeFor[Level](), Options{}, Decoding)
	if err != nil || sh.Kind != KindEnum || !slices.Equal(sh.Enum, []string{"low", "high"}) {
		t.Errorf("%v %v", sh, err)
	}
}
