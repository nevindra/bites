package jsoncodec

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/nevindra/bites/internal/wire"
)

func TestDecodingIntoAStackValueAllocatesNothingInSteadyState(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	d, err := CompileDecoder(reflect.TypeFor[Small](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"plain":   smallBody,
		"escaped": strings.Replace(smallBody, "Ada Lovelace", `Ada \"A\" Lovelace`, 1),
	} {
		b := []byte(body)
		slab := wire.NewSlab(1 << 20)
		var errs Errors
		var in Small
		run := func() {
			in = Small{Items: in.Items[:0]} // the backing array of the last decode is reused
			errs.Reset()
			if err := d.Decode(b, unsafe.Pointer(&in), slab, &errs); err != nil || len(errs.List) > 0 {
				t.Fatal(err, errs.List)
			}
		}
		run() // warm-up: pools, and the slice's first growth
		if n := testing.AllocsPerRun(200, run); n != 0 {
			t.Errorf("%s: %v allocations a decode, want 0", name, n)
		}
		if len(in.Items) != 3 || in.Items[2].SKU != "c" || in.Addr.ZipCode != "12345" {
			t.Errorf("%s: %+v", name, in)
		}
		if slab.Fallbacks() != 0 {
			t.Errorf("%s: the slab overflowed", name)
		}
	}
}

func TestAFreshStackValueCostsOneAllocationForItsSlice(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	// The slice's backing array holds pointers and cannot live in the byte slab, so it is the
	// one allocation a body with a list pays. The destination itself stays on the stack.
	d, _ := CompileDecoder(reflect.TypeFor[Small](), Options{})
	b := []byte(smallBody)
	slab := wire.NewSlab(1 << 20)
	var errs Errors
	n := testing.AllocsPerRun(200, func() {
		var in Small
		errs.Reset()
		if err := d.Decode(b, unsafe.Pointer(&in), slab, &errs); err != nil {
			t.Fatal(err)
		}
	})
	if n != 1 {
		t.Errorf("%v allocations, want 1", n)
	}
}

func TestEncodingIntoAReusedSliceAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	e, err := CompileEncoder(reflect.TypeFor[Small](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := Small{Name: "Ada", Email: "e", Note: "n", Age: 3, Count: 4, Addr: Address{"s", "z"},
		Items: []Line{{"a", 1, 2.5}, {"b", 2, 3}, {"c", 3, 4}}, Newest: wire.Some("x")}
	buf := make([]byte, 0, 1024)
	run := func() {
		var err error
		buf, err = e.Append(buf[:0], unsafe.Pointer(&in))
		if err != nil {
			t.Fatal(err)
		}
	}
	run()
	if n := testing.AllocsPerRun(200, run); n != 0 {
		t.Errorf("%v allocations an encode, want 0", n)
	}
}

func benchSmall(tb testing.TB) (*Decoder, *Encoder, *wmap, Small) {
	d, err := CompileDecoder(reflect.TypeFor[Small](), Options{})
	if err != nil {
		tb.Fatal(err)
	}
	e, err := CompileEncoder(reflect.TypeFor[Small](), Options{})
	if err != nil {
		tb.Fatal(err)
	}
	wm := buildWire(reflect.TypeFor[Small](), wire.SnakeCase)
	var in Small
	var errs Errors
	if err := d.Decode([]byte(smallBody), unsafe.Pointer(&in), wire.NewSlab(1<<12), &errs); err != nil {
		tb.Fatal(err)
	}
	return d, e, wm, in
}

func BenchmarkDecodeOurs(b *testing.B) {
	d, _, _, _ := benchSmall(b)
	body := []byte(smallBody)
	slab := wire.NewSlab(1 << 20)
	var errs Errors
	var in Small
	b.ReportAllocs()
	for b.Loop() {
		in = Small{Items: in.Items[:0]}
		d.Decode(body, unsafe.Pointer(&in), slab, &errs)
	}
}

func BenchmarkDecodeOursFreshValue(b *testing.B) {
	d, _, _, _ := benchSmall(b)
	body := []byte(smallBody)
	slab := wire.NewSlab(1 << 22)
	var errs Errors
	b.ReportAllocs()
	for b.Loop() {
		var in Small
		d.Decode(body, unsafe.Pointer(&in), slab, &errs)
	}
}

func BenchmarkDecodeJSONv2(b *testing.B) {
	_, _, wm, _ := benchSmall(b)
	body := []byte(smallBody)
	b.ReportAllocs()
	for b.Loop() {
		w := reflect.New(wm.w)
		if err := jsonv2.Unmarshal(body, w.Interface(), jsonv2.RejectUnknownMembers(true)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeOurs(b *testing.B) {
	_, e, _, in := benchSmall(b)
	buf := make([]byte, 0, 1024)
	b.ReportAllocs()
	for b.Loop() {
		buf, _ = e.Append(buf[:0], unsafe.Pointer(&in))
	}
}

func BenchmarkEncodeJSONv2(b *testing.B) {
	_, _, wm, in := benchSmall(b)
	w := wm.toWire(reflect.ValueOf(in))
	wp := reflect.New(wm.w)
	wp.Elem().Set(w)
	arg := wp.Interface()
	var buf bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		buf.Reset()
		if err := jsonv2.MarshalWrite(&buf, arg); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAnEmptyListIsNotNilAndCostsNoAllocation(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	type L struct{ A int }
	type P struct {
		X []L
		Y wire.Opt[[]L]
	}
	d, err := CompileDecoder(reflect.TypeFor[P](), Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"x":[],"y":[]}`)
	slab := wire.NewSlab(1 << 12)
	var errs Errors
	var in P
	n := testing.AllocsPerRun(100, func() {
		in = P{}
		if err := d.Decode(body, unsafe.Pointer(&in), slab, &errs); err != nil {
			t.Fatal(err)
		}
	})
	y, _ := in.Y.Get()
	if n != 0 || in.X == nil || y == nil || len(in.X) != 0 {
		t.Fatalf("%v allocations, X=%#v Y=%#v: want 0 and two non-nil empty slices", n, in.X, y)
	}
}
