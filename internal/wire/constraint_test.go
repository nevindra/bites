package wire

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

type s2to5 string

func (s2to5) Length() (int, int) { return 2, 5 }

type s0to3 string

func (s0to3) Length() (int, int) { return 0, 3 }

type sBad string

func (sBad) Length() (int, int) { return 3, 1 }

type sZero string

func (sZero) Length() (int, int) { return 0, 0 }

type n1to6 int

func (n1to6) Range() (int64, int64) { return 1, 6 }

type n64 int64

func (n64) Range() (int64, int64) { return -5, 5 }

// The names of a bound with the wrong signature or on the wrong kind of type.
type (
	lengthCount   string
	rangeOnString string
	lengthOnInt   int
	rangeOnUint   uint
	checkBool     string
)

func (lengthCount) Length() int             { return 0 }
func (rangeOnString) Range() (int64, int64) { return 1, 2 }
func (lengthOnInt) Length() (int, int)      { return 1, 2 }
func (rangeOnUint) Range() (int64, int64)   { return 1, 2 }
func (checkBool) Check() bool               { return true }

func TestANonEmptyStringIsRefusedWhenEmptyAndStoredWhenNot(t *testing.T) {
	runText(t, []textCase[NonEmpty]{{"a", "a", ""}, {"日本", "日本", ""}, {"", "", "must not be empty"}})
}

func TestAPositiveIntegerKeepsTheMessageOfItsBaseTypeForNonNumbersAndRefusesZeroAndBelow(t *testing.T) {
	runText(t, []textCase[PositiveInt]{
		{"1", 1, ""}, {"9223372036854775807", 9223372036854775807, ""},
		{"0", 0, "expected a positive integer"}, {"-4", 0, "expected a positive integer"},
		{"x", 0, "expected an integer"}, {"", 0, "expected an integer"},
	})
	runText(t, []textCase[PositiveInt64]{
		{"7", 7, ""}, {"0", 0, "expected a positive integer"}, {"1.5", 0, "expected an integer"},
		{"9223372036854775808", 0, "expected an integer between -9223372036854775808 and 9223372036854775807"},
	})
}

func TestAStringTypesLengthCountsCharactersAndNamesItsBoundsInTheMessage(t *testing.T) {
	runText(t, []textCase[s2to5]{
		{"ab", "ab", ""}, {"abcde", "abcde", ""}, {"日本語字", "日本語字", ""},
		{"a", "", "must be between 2 and 5 characters"}, {"abcdef", "", "must be between 2 and 5 characters"},
		{"", "", "must be between 2 and 5 characters"},
	})
	runText(t, []textCase[s0to3]{{"", "", ""}, {"abc", "abc", ""}, {"abcd", "", "must be at most 3 characters"}})
}

func TestAHostileStringLongerThanAnyValidOneIsRefusedWithoutBeingCounted(t *testing.T) {
	c, err := ConstraintOf(reflect.TypeFor[s2to5]())
	if err != nil {
		t.Fatal(err)
	}
	if c.OKString(strings.Repeat("x", 6*1024*1024)) {
		t.Error("accepted")
	}
}

func TestAnIntegerTypesRangeBoundsItsValueAndNamesTheRangeInTheMessage(t *testing.T) {
	runText(t, []textCase[n1to6]{
		{"1", 1, ""}, {"6", 6, ""}, {"0", 0, "expected an integer between 1 and 6"},
		{"7", 0, "expected an integer between 1 and 6"}, {"x", 0, "expected an integer"},
	})
	runText(t, []textCase[n64]{{"-5", -5, ""}, {"-6", 0, "expected an integer between -5 and 5"}})
}

func TestAMethodWithABoundsNameThatIsNotThatBoundIsAnErrorAtCompileTime(t *testing.T) {
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[lengthCount]():   "has a Length method, so it carries a bound, but Length must be func() (min, max int)",
		reflect.TypeFor[rangeOnString](): "has a Range method, but a string type's bound is its length: name it Length",
		reflect.TypeFor[lengthOnInt]():   "has a Length method, but an integer type's bound is its value: name it Range",
		reflect.TypeFor[rangeOnUint]():   "has a Range, which is read on an int or int64 type",
		reflect.TypeFor[checkBool]():     "has a Check method, so it checks its value, but Check must be func() error",
	} {
		if _, err := CompileText(typ); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want %q", typ, err, want)
		}
	}
}

func TestALengthThatIsNotALengthIsAnErrorAtCompileTime(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[sBad](), reflect.TypeFor[sZero]()} {
		if _, err := ConstraintOf(typ); err == nil || !strings.Contains(err.Error(), ".Length returns") {
			t.Errorf("%v: %v", typ, err)
		}
		if _, err := CompileText(typ); err == nil {
			t.Errorf("%v: CompileText accepted it", typ)
		}
	}
}

func TestOnlyTheConstraintTypesHaveAConstraint(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[int64]()} {
		if c, err := ConstraintOf(typ); err != nil || c.Kind != ConNone {
			t.Errorf("%v: %v %v", typ, c, err)
		}
	}
}

type amount float64

func (amount) Range() (float64, float64) { return 0.01, 1000000 }

type atLeastAmount float64

func (atLeastAmount) Range() (float64, float64) { return 0, math.MaxFloat64 }

type atMostAmount float64

func (atMostAmount) Range() (float64, float64) { return -math.MaxFloat64, 9.5 }

type ratio32 float32

func (ratio32) Range() (float64, float64) { return 0.1, 0.9 }

func TestAFloatTypesRangeBoundsItsValueAndNamesTheRangeInTheMessage(t *testing.T) {
	runText(t, []textCase[amount]{
		{"0.01", 0.01, ""}, {"1000000", 1000000, ""}, {"12.5", 12.5, ""},
		{"0.0099", 0, "expected a number between 0.01 and 1000000"}, {"1000000.01", 0, "expected a number between 0.01 and 1000000"},
		{"x", 0, "expected a number"}, {"NaN", 0, "expected a number"},
	})
	runText(t, []textCase[atLeastAmount]{{"0", 0, ""}, {"1e300", 1e300, ""}, {"-0.1", 0, "expected a number of at least 0"}})
	runText(t, []textCase[atMostAmount]{{"-1e300", -1e300, ""}, {"9.5", 9.5, ""}, {"9.6", 0, "expected a number of at most 9.5"}})
}

func TestAFloat32RangeComparesInFloat32SoABoundWrittenAsADecimalIncludesItself(t *testing.T) {
	runText(t, []textCase[ratio32]{
		{"0.1", 0.1, ""}, {"0.9", 0.9, ""}, {"0.05", 0, "expected a number between 0.1 and 0.9"}, {"0.95", 0, "expected a number between 0.1 and 0.9"},
	})
}

func TestAFloatRangeCarriesItsBoundsAndLeavesOutASideThatIsNotABound(t *testing.T) {
	c, err := ConstraintOf(reflect.TypeFor[atLeastAmount]())
	if err != nil || c.Kind != ConFloat || c.Rule != RuleRange {
		t.Fatalf("%+v %v", c, err)
	}
	if lo, hi := c.FloatBounds(); lo != 0 || !math.IsInf(hi, 1) {
		t.Errorf("bounds %v %v", lo, hi)
	}
}

type (
	floatNaN       float64
	floatBackwards float64
	floatWhole     float64
	float32Wide    float32
	floatIntRange  float64
	intFloatRange  int64
	floatBadSig    float64
)

func (floatNaN) Range() (float64, float64)       { return math.NaN(), 1 }
func (floatBackwards) Range() (float64, float64) { return 2, 1 }
func (floatWhole) Range() (float64, float64)     { return -math.MaxFloat64, math.MaxFloat64 }
func (float32Wide) Range() (float64, float64)    { return 0, math.MaxFloat64 }
func (floatIntRange) Range() (int64, int64)      { return 1, 2 }
func (intFloatRange) Range() (float64, float64)  { return 1, 2 }
func (floatBadSig) Range() float64               { return 1 }

func TestAFloatRangeThatCannotWorkIsAnErrorAtCompileTime(t *testing.T) {
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[floatNaN]():       "a bound must be a finite number",
		reflect.TypeFor[floatBackwards](): "max is below min",
		reflect.TypeFor[floatWhole]():     "returns the whole of float64 and bounds nothing",
		reflect.TypeFor[float32Wide]():    "outside the range of a float32",
		reflect.TypeFor[floatIntRange]():  "returns int64 bounds, but a float type's bound is float64",
		reflect.TypeFor[intFloatRange]():  "returns float64 bounds, but an integer type's bound is int64",
		reflect.TypeFor[floatBadSig]():    "Range must be func() (min, max float64)",
	} {
		if _, err := CompileText(typ); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: got %v, want %q", typ, err, want)
		}
	}
}

type fmtEmail string

func (fmtEmail) Format() string { return "email" }

type fmtLenEmail string

func (fmtLenEmail) Format() string     { return "email" }
func (fmtLenEmail) Length() (int, int) { return 1, 12 }
func (fmtLenEmail) Pattern() string    { return `^[a-z]` }

type fmtMoney string

func (fmtMoney) Format() string { return "money" }

func TestAStringTypeWithACheckedFormatRefusesAValueOfAnotherFormatWithoutEchoingIt(t *testing.T) {
	dec, err := CompileText(reflect.TypeFor[fmtEmail]())
	if err != nil {
		t.Fatal(err)
	}
	var v fmtEmail
	err = dec(unsafe.Pointer(&v), "not an email")
	var te *TextError
	if !errors.As(err, &te) || te.Rule != RuleFormat || te.Format != "email" || te.Error() != "must be an email address" || v != "" {
		t.Fatalf("%v %+v %q", err, te, v)
	}
	if err := dec(unsafe.Pointer(&v), "ada@example.com"); err != nil || v != "ada@example.com" {
		t.Fatalf("%v %q", err, v)
	}
}

func TestTheFormatIsCheckedAfterTheLengthAndThePattern(t *testing.T) {
	c, err := ConstraintOf(reflect.TypeFor[fmtLenEmail]())
	if err != nil || c.Kind != ConLength || c.Pat == nil || c.Fmt == nil {
		t.Fatalf("%+v %v", c, err)
	}
	for in, want := range map[string]string{
		"a@b.co": "", "a@b.company.example": "must be between 1 and 12 characters", "A@B.CO": "must match the pattern", "a@b": "must be an email address",
	} {
		if c.OKString(in) != (want == "") {
			t.Errorf("%q: OKString %v", in, c.OKString(in))
		}
		if want != "" {
			if msg, _, _, _, _ := c.FailString(in); msg != want {
				t.Errorf("%q: %q, want %q", in, msg, want)
			}
		}
	}
}

func TestAFormatThatTheFrameworkDoesNotCheckStaysAnAnnotation(t *testing.T) {
	c, err := ConstraintOf(reflect.TypeFor[fmtMoney]())
	if err != nil || c.Kind != ConNone {
		t.Fatalf("%+v %v", c, err)
	}
	if v, err := decode[fmtMoney]("anything"); err != nil || v != "anything" {
		t.Fatalf("%q %v", v, err)
	}
}

type (
	centsF64 float64
	centsF32 float32
	fiveInt  int
	tags3    []string
)

func (centsF64) MultipleOf() float64 { return 0.01 }
func (centsF32) MultipleOf() float64 { return 0.01 }
func (fiveInt) MultipleOf() int64    { return 5 }
func (tags3) Unique() bool           { return true }

func TestAFloatMultipleToleratesTheErrorOfDecimalDigitsForEveryWholeNumberOfCents(t *testing.T) {
	for _, tc := range []struct {
		t    reflect.Type
		bits int
	}{{reflect.TypeFor[centsF64](), 64}, {reflect.TypeFor[centsF32](), 32}} {
		c, err := ConstraintOf(tc.t)
		if err != nil || c.Kind != ConFloat || c.MulF != 0.01 {
			t.Fatalf("%s: %+v %v", tc.t, c, err)
		}
		for i := -3000; i <= 300000; i++ {
			s := strconv.FormatFloat(float64(i)/100, 'f', 2, 64)
			v, _ := strconv.ParseFloat(s, tc.bits)
			if tc.bits == 32 && i > 100000 {
				continue // a float32 holds about seven digits
			}
			if !c.OKFloat(v) {
				t.Fatalf("%s: %s is %d cents and was refused", tc.t, s, i)
			}
			half, _ := strconv.ParseFloat(strconv.FormatFloat(float64(i)/100, 'f', 2, 64)+"5", tc.bits)
			if c.OKFloat(half) {
				t.Fatalf("%s: %s5 is not a whole number of cents and was accepted", tc.t, s)
			}
		}
	}
}

func TestAnIntegerMultipleWithARangeThatHoldsNoMultipleIsRefused(t *testing.T) {
	if _, err := ConstraintOf(reflect.TypeFor[fiveInt]()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lo, hi, step int64
		want         bool
	}{{1, 4, 5, false}, {1, 5, 5, true}, {-4, -1, 5, false}, {-5, -1, 5, true}, {6, 9, 5, false}, {0, 0, 5, true}, {math.MaxInt64 - 1, math.MaxInt64, 5, false}} {
		if got := hasMultipleIn(tc.lo, tc.hi, tc.step); got != tc.want {
			t.Errorf("hasMultipleIn(%d, %d, %d) = %v, want %v", tc.lo, tc.hi, tc.step, got, tc.want)
		}
	}
}

func TestUniqueFindsTheFirstRepeatPairwiseAndThroughASetAndAllocatesNothingForAShortList(t *testing.T) {
	it, ok, err := ItemsOf(reflect.TypeFor[tags3]())
	if err != nil || !ok || it.Dup == nil || it.Bounded {
		t.Fatalf("%+v %v %v", it, ok, err)
	}
	if !it.OK(0) || !it.OK(1<<20) {
		t.Error("a Unique with no Items bounds no length")
	}
	short := []string{"a", "b", "c", "b"}
	if i := it.Dup(unsafe.Pointer(unsafe.SliceData(short)), len(short)); i != 3 {
		t.Errorf("first repeat at %d, want 3", i)
	}
	long := make([]string, 100)
	for i := range long {
		long[i] = strconv.Itoa(i)
	}
	if i := it.Dup(unsafe.Pointer(unsafe.SliceData(long)), len(long)); i != -1 {
		t.Errorf("a list of distinct items has a repeat at %d", i)
	}
	long[70] = "5"
	if i := it.Dup(unsafe.Pointer(unsafe.SliceData(long)), len(long)); i != 70 {
		t.Errorf("first repeat at %d, want 70", i)
	}
	p := unsafe.Pointer(unsafe.SliceData(short[:3]))
	if n := testing.AllocsPerRun(100, func() { it.Dup(p, 3) }); n != 0 {
		t.Errorf("a short list costs %v allocations, want 0", n)
	}
}
