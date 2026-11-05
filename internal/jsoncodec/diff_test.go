package jsoncodec

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
	"uuid"

	"github.com/nevindra/bites/internal/wire"
)

// diffCase is a codec type, its plan, and the wire type v2 can handle on its own.
type diffCase struct {
	name  string
	t     reflect.Type
	dec   *Decoder
	enc   *Encoder
	wm    *wmap
	seeds []string
}

func diffCases(tb testing.TB) []diffCase {
	tb.Helper()
	var out []diffCase
	for _, c := range []struct {
		name  string
		t     reflect.Type
		seeds []string
	}{
		{"Order", reflect.TypeFor[Order](), []string{fullOrder, `{}`, `null`, `[]`}},
		{"Small", reflect.TypeFor[Small](), []string{smallBody, `{}`}},
	} {
		d, err := CompileDecoder(c.t, Options{})
		if err != nil {
			tb.Fatal(err)
		}
		e, err := CompileEncoder(c.t, Options{})
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, diffCase{c.name, c.t, d, e, buildWire(c.t, wire.SnakeCase), c.seeds})
	}
	return out
}

// randFill fills the addressable v with random data covering the codec's types.
func randFill(r *rand.Rand, v reflect.Value) {
	t := v.Type()
	if kind, elem, vOff, fOff := wire.PresenceOf(t); kind != wire.None {
		base := v.Addr().UnsafePointer()
		if kind == wire.Optional && r.IntN(3) == 0 {
			return
		}
		if kind == wire.Nullable && r.IntN(3) == 0 {
			return
		}
		inner := reflect.NewAt(elem, unsafe.Add(base, vOff)).Elem()
		randFill(r, inner)
		*(*bool)(unsafe.Add(base, fOff)) = true
		return
	}
	switch t {
	case timeType:
		loc := time.UTC
		if r.IntN(2) == 0 {
			loc = time.FixedZone("", (r.IntN(47)-23)*1800)
		}
		v.Set(reflect.ValueOf(time.Date(1+r.IntN(9999), time.Month(1+r.IntN(12)), 1+r.IntN(28),
			r.IntN(24), r.IntN(60), r.IntN(60), r.IntN(1e9)/(1<<r.IntN(20))*(1<<r.IntN(20)), loc)))
		return
	case uuidType:
		var u uuid.UUID
		for i := range u {
			u[i] = byte(r.IntN(256))
		}
		v.Set(reflect.ValueOf(u))
		return
	}
	switch t.Kind() {
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		bits := uint(t.Size() * 8)
		x := int64(r.Uint64()) >> (64 - bits + uint(r.IntN(int(bits))))
		v.SetInt(x)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		bits := uint(t.Size() * 8)
		v.SetUint(r.Uint64() >> (64 - bits + uint(r.IntN(int(bits)))))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(randFloat(r, int(t.Size())*8))
	case reflect.String:
		v.SetString(randString(r))
	case reflect.Slice:
		if r.IntN(4) == 0 {
			return
		}
		n := r.IntN(5)
		v.Set(reflect.MakeSlice(t, n, n))
		for i := range n {
			randFill(r, v.Index(i))
		}
	case reflect.Array:
		for i := range v.Len() {
			randFill(r, v.Index(i))
		}
	case reflect.Map:
		if r.IntN(4) == 0 {
			return
		}
		v.Set(reflect.MakeMap(t))
		for range r.IntN(4) {
			k := reflect.New(t.Key()).Elem()
			k.SetString(randString(r))
			e := reflect.New(t.Elem()).Elem()
			randFill(r, e)
			v.SetMapIndex(k, e)
		}
	case reflect.Struct:
		for i := range t.NumField() {
			if t.Field(i).IsExported() {
				randFill(r, v.Field(i))
			}
		}
	}
}

func randFloat(r *rand.Rand, bits int) float64 {
	pick := []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 1e21, 1e20, 1e-6, 1e-7, 123456789.123, 5e-324, math.MaxFloat32, 3.4e38, 1.5e300}
	switch r.IntN(4) {
	case 0:
		f := pick[r.IntN(len(pick))]
		if bits == 32 && math.IsInf(float64(float32(f)), 0) {
			return 1
		}
		return f
	case 1:
		return float64(r.IntN(2000) - 1000)
	}
	for {
		var f float64
		if bits == 32 {
			f = float64(math.Float32frombits(r.Uint32()))
		} else {
			f = math.Float64frombits(r.Uint64())
		}
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f
		}
	}
}

var runePool = []rune{'a', 'Z', '0', ' ', '"', '\\', '/', '\n', '\t', '\r', 0x01, 0x1f, 0x7f, 'é', '世', '😀', ' ', ' ', '<', '>', '&', '\x00'}

func randString(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(8) {
		b.WriteRune(runePool[r.IntN(len(runePool))])
	}
	return b.String()
}

func TestEncodingIsByteIdenticalToJSONv2ForGeneratedValues(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, c := range diffCases(t) {
		for i := range 4000 {
			v := reflect.New(c.t)
			randFill(r, v.Elem())
			got, err := c.enc.Append(nil, v.UnsafePointer())
			if err != nil {
				t.Fatalf("%s #%d: %v", c.name, i, err)
			}
			w := c.wm.toWire(v.Elem())
			want, err := jsonv2.Marshal(w.Interface(), jsonv2.Deterministic(true))
			if err != nil {
				t.Fatalf("%s #%d: v2: %v", c.name, i, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s #%d differs\n ours: %s\n v2:   %s", c.name, i, got, want)
			}
		}
	}
}

func TestADecodedValueEqualsWhatJSONv2DecodesFromGeneratedBodies(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, c := range diffCases(t) {
		for i := range 4000 {
			v := reflect.New(c.t)
			randFill(r, v.Elem())
			body, err := jsonv2.Marshal(c.wm.toWire(v.Elem()).Interface(), jsonv2.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			// A generated enum is any string, which the codec rightly refuses.
			if c.name == "Order" {
				v.Elem().FieldByName("Level").SetString("low")
				// omitempty on a decoded field does not make it optional (ADR 0041).
				v.Elem().FieldByName("Alias").SetString("al")
				body, _ = jsonv2.Marshal(c.wm.toWire(v.Elem()).Interface(), jsonv2.Deterministic(true))
			}
			dst := reflect.New(c.t)
			var errs Errors
			if err := c.dec.Decode(body, dst.UnsafePointer(), wire.NewSlab(len(body)+64), &errs); err != nil || len(errs.List) > 0 {
				t.Fatalf("%s #%d: %v %v\n%s", c.name, i, err, errs.List, body)
			}
			// Null[T] sent as null decodes the same as v2's nil pointer, and an Opt that is
			// set round-trips: compare to what v2 itself reads back.
			w := reflect.New(c.wm.w)
			if err := jsonv2.Unmarshal(body, w.Interface()); err != nil {
				t.Fatal(err)
			}
			want := c.wm.fromWire(w.Elem())
			if !reflect.DeepEqual(dst.Elem().Interface(), want.Interface()) {
				t.Fatalf("%s #%d differs\n body: %s\n ours: %+v\n v2:   %+v", c.name, i, body, dst.Elem().Interface(), want.Interface())
			}
		}
	}
}

// allowedExtra lists what the codec refuses and v2 deliberately accepts, as error messages:
//
//   - "required": v2 leaves a missing field at its zero value.
//   - "must not be null": v2 reads null into a non-pointer as a no-op (and into an Opt, mapped
//     to a pointer, as nil).
//   - "expected one of: ...": v2 does not know enums.
//
// Everything else must agree, including the length of a fixed array (v2 refuses a mismatch
// too), integers written as 1.0 or 1e2 (v2 refuses), a time with a one-digit hour, and a
// string with an escaped quote in a name.
func allowedExtra(msg string) bool {
	return msg == msgRequired || msg == msgNull || strings.HasPrefix(msg, msgEnumPrefix)
}

// compareDecode decodes body with the codec and with v2 and fails on any difference that is
// not in the allow-list above. v2 stops at its first error, so only acceptance and values
// are compared, not error lists.
func compareDecode(t testing.TB, c diffCase, body []byte) {
	t.Helper()
	dst := reflect.New(c.t)
	var errs Errors
	oerr := c.dec.Decode(body, dst.UnsafePointer(), wire.NewSlab(2*len(body)+64), &errs)
	w := reflect.New(c.wm.w)
	verr := jsonv2.Unmarshal(body, w.Interface(), jsonv2.RejectUnknownMembers(true))
	switch {
	case oerr != nil:
		if verr == nil && !errors.Is(oerr, ErrDepth) {
			t.Fatalf("%s: we report %v, v2 accepts\n%q", c.name, oerr, body)
		}
	case len(errs.List) == 0:
		if verr != nil {
			t.Fatalf("%s: we accept, v2 refuses: %v\n%q", c.name, verr, body)
		}
		want := c.wm.fromWire(w.Elem())
		if !reflect.DeepEqual(dst.Elem().Interface(), want.Interface()) {
			t.Fatalf("%s: values differ\n%q\n ours: %+v\n v2:   %+v", c.name, body, dst.Elem().Interface(), want.Interface())
		}
	default:
		if verr == nil {
			for _, e := range errs.List {
				if !allowedExtra(e.Message) {
					t.Fatalf("%s: we refuse (%s: %s), v2 accepts\n%q", c.name, e.Location, e.Message, body)
				}
			}
		}
	}
}

func FuzzDecode(f *testing.F) {
	cases := diffCases(f)
	for i, c := range cases {
		for _, s := range c.seeds {
			f.Add([]byte(s), uint8(i))
		}
	}
	for _, s := range []string{
		`{"age":1.0}`, `{"age":1e2}`, `{"age":-0}`, `{"count":-0}`, `{"pair":[1]}`, `{"pair":[1,2,3]}`,
		`{"ratio":1e999}`, `{"small":128}`, `{"count":65536}`, `{"created":"2026-10-31"}`,
		`{"id":"6ba7b8109dad11d180b400c04fd430c8"}`, `{"nick":null}`, `{"scores":{"a":null}}`,
		`{"customer":"\ud800"}`, `{"customer":"A"}`, `{"Customer":"x"}`, `{"customer":"a","customer":"b"}`,
	} {
		f.Add([]byte(s), uint8(0))
	}
	f.Fuzz(func(t *testing.T, body []byte, which uint8) {
		compareDecode(t, cases[int(which)%len(cases)], body)
	})
}

func TestDecodeMatchesJSONv2OnTheEdgeBodiesTheFuzzerSeeds(t *testing.T) {
	cases := diffCases(t)
	for _, s := range []string{
		fullOrder, `{}`, `null`, `[]`, `{"age":1.0}`, `{"age":1e2}`, `{"age":-0}`, `{"count":-0}`,
		`{"pair":[1]}`, `{"pair":[1,2,3]}`, `{"ratio":1e999}`, `{"small":128}`, `{"created":"2026-10-31"}`,
		`{"nick":null}`, `{"scores":{"a":null}}`, `{"customer":"\ud800"}`, `{"Customer":"x"}`,
		`{"customer":"a","customer":"b"}`, `{"customer":1`, ``, `{} x`,
	} {
		for _, c := range cases {
			compareDecode(t, c, []byte(s))
		}
	}
}
