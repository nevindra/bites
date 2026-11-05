package wire

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"
)

func TestTheZeroOptIsAbsentAndTheZeroNullIsNull(t *testing.T) {
	var o Opt[int]
	if o.IsSet() {
		t.Error("zero Opt is set")
	}
	if v, ok := o.Get(); ok || v != 0 {
		t.Errorf("zero Opt Get = %v, %v", v, ok)
	}
	if o.Or(7) != 7 {
		t.Error("Or did not return the default")
	}
	var n Null[string]
	if !n.IsNull() {
		t.Error("zero Null is not null")
	}
	if v, ok := n.Get(); ok || v != "" {
		t.Errorf("zero Null Get = %q, %v", v, ok)
	}
}

func TestSomeAndNonNullHoldTheirValueEvenWhenItIsTheZeroValue(t *testing.T) {
	o := Some(0)
	if v, ok := o.Get(); !ok || v != 0 || !o.IsSet() || o.Or(5) != 0 {
		t.Errorf("Some(0) = %v", o)
	}
	n := NonNull("")
	if v, ok := n.Get(); !ok || v != "" || n.IsNull() {
		t.Errorf("NonNull(\"\") = %v", n)
	}
}

func TestPresenceTypesCompareByPresenceAndValue(t *testing.T) {
	if Some(1) != Some(1) || Some(1) == Some(2) || Some(0) == (Opt[int]{}) {
		t.Error("Opt equality is wrong")
	}
	if NonNull(1) != NonNull(1) || NonNull(0) == (Null[int]{}) {
		t.Error("Null equality is wrong")
	}
}

func TestPresenceTypesPrintWhatTheyHold(t *testing.T) {
	cases := []struct{ got, want string }{
		{fmt.Sprint(Opt[int]{}), "<absent>"},
		{fmt.Sprint(Some(3)), "3"},
		{fmt.Sprint(Null[int]{}), "null"},
		{fmt.Sprint(NonNull("x")), "x"},
		{fmt.Sprintf("%#v", Some(3)), "wire.Some(3)"},
		{fmt.Sprintf("%#v", NonNull("x")), `wire.NonNull("x")`},
		{fmt.Sprintf("%#v", Opt[int]{}), "wire.Opt[int]{}"},
		{fmt.Sprintf("%#v", Null[int]{}), "wire.Null[int]{}"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestPresenceOfRecognisesOnlyOptAndNull(t *testing.T) {
	type lookalike struct {
		value int
		set   bool
	}
	for _, c := range []struct {
		t    reflect.Type
		kind PresenceKind
	}{
		{reflect.TypeFor[Opt[int]](), Optional},
		{reflect.TypeFor[Null[string]](), Nullable},
		{reflect.TypeFor[Opt[Null[int]]](), Optional},
		{reflect.TypeFor[int](), None},
		{reflect.TypeFor[lookalike](), None},
		{reflect.TypeFor[*Opt[int]](), None},
		{nil, None},
	} {
		kind, _, _, _ := PresenceOf(c.t)
		if kind != c.kind {
			t.Errorf("PresenceOf(%v) = %v, want %v", c.t, kind, c.kind)
		}
	}
	_, elem, _, _ := PresenceOf(reflect.TypeFor[Opt[Null[int]]]())
	if elem != reflect.TypeFor[Null[int]]() {
		t.Errorf("elem = %v", elem)
	}
}

func TestPresenceOfOffsetsFillAValueAndItsFlagThroughUnsafe(t *testing.T) {
	var o Opt[[3]int64]
	kind, elem, vOff, fOff := PresenceOf(reflect.TypeOf(o))
	if kind != Optional || elem != reflect.TypeFor[[3]int64]() {
		t.Fatalf("kind %v elem %v", kind, elem)
	}
	*(*[3]int64)(unsafe.Add(unsafe.Pointer(&o), vOff)) = [3]int64{1, 2, 3}
	*(*bool)(unsafe.Add(unsafe.Pointer(&o), fOff)) = true
	if v, ok := o.Get(); !ok || v != [3]int64{1, 2, 3} {
		t.Errorf("Get = %v, %v", v, ok)
	}

	var n Null[string]
	kind, _, vOff, fOff = PresenceOf(reflect.TypeOf(n))
	if kind != Nullable {
		t.Fatal(kind)
	}
	*(*string)(unsafe.Add(unsafe.Pointer(&n), vOff)) = "hi"
	*(*bool)(unsafe.Add(unsafe.Pointer(&n), fOff)) = true
	if v, ok := n.Get(); !ok || v != "hi" {
		t.Errorf("Get = %v, %v", v, ok)
	}
}

func TestOptPtrIsNilWhenAbsentAndACopyWhenPresent(t *testing.T) {
	var none Opt[int]
	if none.Ptr() != nil {
		t.Error("an absent Opt gave a pointer")
	}
	o := Some(7)
	p := o.Ptr()
	if p == nil || *p != 7 {
		t.Fatalf("got %v", p)
	}
	*p = 9
	if v, _ := o.Get(); v != 7 {
		t.Errorf("writing through the pointer changed the Opt to %d", v)
	}
}

func TestOptOfIsAbsentForNilAndPresentWithACopyOtherwise(t *testing.T) {
	if OptOf[string](nil).IsSet() {
		t.Error("OptOf(nil) is present")
	}
	s := "a"
	o := OptOf(&s)
	s = "b"
	if v, ok := o.Get(); !ok || v != "a" {
		t.Errorf("got %q, %v", v, ok)
	}
	empty := ""
	if !OptOf(&empty).IsSet() {
		t.Error("a pointer to the zero value is present, not absent")
	}
}

func TestAnOptIsFilledThroughSetAndTheCoreReadsItBack(t *testing.T) {
	var o Opt[string]
	reflect.ValueOf(&o).MethodByName("Set").Call([]reflect.Value{reflect.ValueOf("x")})
	if v, ok := o.Get(); !ok || v != "x" {
		t.Errorf("after Set, Get = %q, %v", v, ok)
	}
	o.Set("")
	if v, ok := o.Get(); !ok || v != "" {
		t.Errorf("Set of the zero value must be present: Get = %q, %v", v, ok)
	}
}
