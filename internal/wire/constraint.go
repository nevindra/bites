package wire

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"unicode/utf8"
	"unsafe"
)

// The rules of ADR 0021 that a type carries. Go has no value-level generics, so a bound cannot
// be written Min[1]; an application's own named type carries it in a method instead (Length on a
// string type, Range on an int or int64 type, Check on any type), read once at Registration, as
// Values makes an enum (ADR 0042). The three types below are the bounds common enough to name.
// Each is an ordinary string or integer type, so a handler reads it with a conversion and
// nothing is allocated for it.

// NonEmpty is a string with at least one character.
type NonEmpty string

// PositiveInt is an int greater than zero.
type PositiveInt int

// PositiveInt64 is an int64 greater than zero.
type PositiveInt64 int64

// The rule codes of ADR 0012: the "rule" member of an entry in the 400's errors list, so a client
// can translate a message without parsing it. RuleLength, RuleRange, RuleItems and RuleMembers also carry "min" and "max"; RuleMultiple carries "multiple".
const (
	RuleType     = "type"
	RuleRequired = "required"
	RuleLength   = "length"
	RuleRange    = "range"
	RuleNonEmpty = "nonempty"
	RulePositive = "positive"
	RuleValues   = "values"
	RuleCheck    = "check"
	RuleItems    = "items"
	RulePattern  = "pattern"
	RuleMultiple = "multiple"
	RuleMembers  = "members"
	RuleUnique   = "unique"
)

// RuleFormat (format.go) is the rule code of a value that is not of its type's checked Format.

// ConstraintKind says which check a constraint type makes.
type ConstraintKind uint8

const (
	// ConNone: the type is not a constraint type.
	ConNone ConstraintKind = iota
	// ConLength: a string whose length in characters is within MinLen and MaxLen.
	ConLength
	// ConMin: an integer of at least MinInt (unless NoMin), and at most MaxInt when HasMax.
	ConMin
	// ConPattern: a string that matches Pat, with no bound on its length. A string type with a
	// Length and a Pattern is ConLength with Pat set.
	ConPattern
	// ConFormat: a string of the checked Format in Fmt, with no bound on its length and no Pat.
	ConFormat
	// ConFloat: a number of at least MinF (unless NoMin) and at most MaxF (unless !HasMax).
	ConFloat
)

// Constraint is what a constraint type demands of a value, and the sentence a client is told
// when a value falls short. It is built once per type at Registration.
type Constraint struct {
	Kind           ConstraintKind
	MinLen, MaxLen int   // ConLength; MaxLen is -1 when there is no upper bound
	MinInt, MaxInt int64 // ConMin; MaxInt counts only when HasMax
	HasMax         bool
	NoMin          bool // ConMin: the lower bound is the base type's own minimum, so there is none to state
	Message        string
	Rule           string      // the rule code of ADR 0012
	Pat            *Pattern    // a string type's Pattern, nil when it has none
	Fmt            *FormatRule // a string type's checked Format, nil when it has none
	MinF, MaxF     float64     // ConFloat: the bounds as the author wrote them, for the document and the sentence
	loF, hiF       float64     // ConFloat: the bounds a stored value is compared with (float32 rounds them)

	// ExMin and ExMax say that MinF or MaxF is an exclusive bound (an ExclusiveMin or ExclusiveMax
	// method), so a value equal to it fails. They are for ConFloat only.
	ExMin, ExMax bool
	// MulInt is the step a ConMin value must be a multiple of, and MulF the step of a ConFloat
	// value, both 0 when the type has no MultipleOf. MulMessage is the sentence for a value that
	// is in range and not a multiple.
	MulInt     int64
	MulF       float64
	MulMessage string
	mulF       float64 // the step a stored value is compared with (float32 rounds it)
	mulEps     float64 // the relative tolerance of a float multiple
}

// FloatBounds returns the "min" and "max" of a float range, with -Inf or +Inf for a side the
// type does not bound (ADR 0021): the 400 leaves that side out.
func (c *Constraint) FloatBounds() (lo, hi float64) {
	lo, hi = c.MinF, c.MaxF
	if c.NoMin {
		lo = math.Inf(-1)
	}
	if !c.HasMax {
		hi = math.Inf(1)
	}
	return lo, hi
}

// OKFloat reports whether v, the value as it is stored (a float32's value widened), satisfies a
// ConFloat constraint.
func (c *Constraint) OKFloat(v float64) bool {
	return c.InRangeFloat(v) && (c.MulF == 0 || multipleOfFloat(v, c.mulF, c.mulEps))
}

// InRangeFloat reports whether v is within the bounds of a ConFloat constraint, leaving its
// MultipleOf aside: an inclusive bound admits its own value, an exclusive one does not.
func (c *Constraint) InRangeFloat(v float64) bool {
	if !c.NoMin && (v < c.loF || c.ExMin && v == c.loF) {
		return false
	}
	return !c.HasMax || (v < c.hiF || !c.ExMax && v == c.hiF)
}

// multipleOfFloat reports whether v is a whole multiple of m. A float cannot hold 0.3 or 0.1
// exactly, so v/m is compared with the nearest whole number and the two may differ by a few
// units in the last place (eps, relative to that number) and still be a multiple: 0.3 is a
// multiple of 0.1 although 0.3/0.1 is 2.9999999999999996. Zero is a multiple of everything and
// only zero is within reach of the whole number 0. Above 2^50 steps every float passes, since
// the spacing of floats there is wider than the step.
func multipleOfFloat(v, m, eps float64) bool {
	q := v / m
	r := math.Round(q)
	if r == 0 {
		return v == 0
	}
	return math.Abs(q-r) <= eps*math.Abs(r)
}

// Bounds returns the "min" and "max" a client is told with the rule, and whether the rule has any.
// A side the type does not bound is math.MinInt64 or math.MaxInt64, which the 400 leaves out
// (ADR 0021): a bound equal to the base type's own limit is not a bound.
func (c *Constraint) Bounds() (lo, hi int64, ok bool) {
	switch c.Rule {
	case RuleLength:
		hi = int64(c.MaxLen)
		if c.MaxLen < 0 {
			hi = math.MaxInt64
		}
		return int64(c.MinLen), hi, true
	case RuleRange:
		lo, hi = c.MinInt, c.MaxInt
		if c.NoMin {
			lo = math.MinInt64
		}
		if !c.HasMax {
			hi = math.MaxInt64
		}
		return lo, hi, true
	}
	return 0, 0, false
}

// Items is what a list type demands of its length and of its members: a named slice type's
// Items and Unique methods, read once at Registration (ADR 0021). Min and Max count items; Max is
// at least 1. A type with Unique and no Items has Bounded false, Min 0 and Max math.MaxInt, so
// OK holds for every length and a document states no bound.
type Items struct {
	Min, Max int
	Message  string
	// Bounded says the type has an Items method; the document states minItems and maxItems only then.
	Bounded bool
	// Dup is set when the type has a Unique method: it returns the index of the first item of the
	// n items at p that equals an earlier one, or -1. It allocates nothing for a list of at most
	// uniquePairwise items.
	Dup func(p unsafe.Pointer, n int) int
}

// ItemsRuleName names the method of it for a refusal: "an Items method", or "a Unique method"
// when the type states no bound.
func ItemsRuleName(it Items) string {
	if it.Bounded {
		return "an Items method"
	}
	return "a Unique method"
}

// UniqueMessage is the sentence for an item that repeats an earlier one. It never carries the
// client's value.
const UniqueMessage = "repeats an earlier item"

// uniquePairwise is the longest list whose items are compared pairwise, which allocates nothing.
// A longer list is compared through a set sized to it.
const uniquePairwise = 16

// OK reports whether a list of n items is within the bounds.
func (it *Items) OK(n int) bool { return n >= it.Min && n <= it.Max }

// ItemsOf reports the Items and Unique rules of t, and whether t has either. They are read on a
// named slice type with the signatures func() (min, max int) and func() bool, on a zero
// receiver. A method of an exact signature on a type that is not a slice is an error (it
// promises a rule nothing would read), except on a struct, whose method is the domain's own; a
// method of those names with another signature on a slice type is an error too, and on any other
// type it is the domain's own method and is left alone.
func ItemsOf(t reflect.Type) (Items, bool, error) {
	if t.Name() == "" {
		return Items{}, false, nil
	}
	if t.Kind() != reflect.Slice {
		if m, ok := methodOf(t, "Items"); ok {
			if mt := m.Type; mt.NumIn() == 1 && mt.NumOut() == 2 && mt.Out(0) == reflect.TypeFor[int]() && mt.Out(1) == reflect.TypeFor[int]() && t.Kind() != reflect.Struct {
				if t.Kind() == reflect.Map {
					return Items{}, false, fmt.Errorf("%s has an Items method, but Items bounds a list and %s is a map: a map's size is its Members method, func() (min, max int)", t, t)
				}
				return Items{}, false, fmt.Errorf("%s has an Items method, but Items bounds a list: it is read on a named slice type, and %s is a %s", t, t, t.Kind())
			}
		}
		if m, ok := methodOf(t, "Unique"); ok && t.Kind() != reflect.Struct && isUniqueSig(m.Type) {
			return Items{}, false, fmt.Errorf("%s has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and %s is a %s", t, t, t.Kind())
		}
		return Items{}, false, nil
	}
	it := Items{Max: math.MaxInt}
	lo, hi, ok, err := callPair(t, "Items", reflect.TypeFor[int](), "func() (min, max int)")
	if err != nil {
		return Items{}, false, err
	}
	if ok {
		if lo < 0 || hi < 1 || hi < lo {
			return Items{}, false, fmt.Errorf("%s.Items returns (%d, %d), which is not a count of items: it must be 0 <= min <= max with max at least 1", t, lo, hi)
		}
		var msg string
		switch {
		case lo == hi:
			msg = "must have exactly " + strconv.FormatInt(hi, 10) + " items"
		case lo == 0:
			msg = "must have at most " + strconv.FormatInt(hi, 10) + " items"
		default:
			msg = "must have between " + strconv.FormatInt(lo, 10) + " and " + strconv.FormatInt(hi, 10) + " items"
		}
		it = Items{Min: int(lo), Max: int(hi), Message: msg, Bounded: true}
	}
	if m, has := methodOf(t, "Unique"); has {
		if !isUniqueSig(m.Type) {
			return Items{}, false, fmt.Errorf("%s has a Unique method, so it says its items are distinct, but Unique must be func() bool: rename the method if it means something else", t)
		}
		if !m.Func.Call([]reflect.Value{reflect.New(t)})[0].Bool() {
			return Items{}, false, fmt.Errorf("%s.Unique returns false, which states nothing: drop the method, since a list may repeat an item unless Unique returns true", t)
		}
		dup, err := dupFinder(t.Elem())
		if err != nil {
			return Items{}, false, fmt.Errorf("%s.Unique cannot be checked: %v", t, err)
		}
		it.Dup = dup
		ok = true
	}
	return it, ok, nil
}

func isUniqueSig(mt reflect.Type) bool {
	return mt.NumIn() == 1 && mt.NumOut() == 1 && mt.Out(0) == reflect.TypeFor[bool]()
}

// dupFinder returns the comparison for a list whose items are of type et, which must be a kind
// whose equality is plain and cheap: a string, a boolean, an integer, a float or a UUID. A
// struct, a list, a map or an Opt has no one reading of "equal" that a client could be told, so
// the application states which of its parts must not repeat in a Validate.
func dupFinder(et reflect.Type) (func(unsafe.Pointer, int) int, error) {
	if et == uuidType {
		return firstDup[[16]byte], nil
	}
	switch et.Kind() {
	case reflect.String:
		return firstDup[string], nil
	case reflect.Bool:
		return firstDup[bool], nil
	case reflect.Int:
		return firstDup[int], nil
	case reflect.Int8:
		return firstDup[int8], nil
	case reflect.Int16:
		return firstDup[int16], nil
	case reflect.Int32:
		return firstDup[int32], nil
	case reflect.Int64:
		return firstDup[int64], nil
	case reflect.Uint:
		return firstDup[uint], nil
	case reflect.Uint8:
		return firstDup[uint8], nil
	case reflect.Uint16:
		return firstDup[uint16], nil
	case reflect.Uint32:
		return firstDup[uint32], nil
	case reflect.Uint64:
		return firstDup[uint64], nil
	case reflect.Float32:
		return firstDup[float32], nil
	case reflect.Float64:
		return firstDup[float64], nil
	}
	return nil, fmt.Errorf("its item type %s is a %s, and only a string, boolean, number or UUID has an equality a client can be told: state which part of the item must not repeat in the Input's Validate", et, et.Kind())
}

// firstDup returns the index of the first of the n values at p that equals an earlier one, or -1.
// Up to uniquePairwise values are compared pairwise, which allocates nothing; more go through a
// set sized to the list, whose memory is a constant factor on the list the request already is.
// Floats compare by ==, so 1 and 1.0 are one value, as in JSON Schema, and -0 is 0; a JSON number
// is never NaN.
func firstDup[T comparable](p unsafe.Pointer, n int) int {
	if n < 2 {
		return -1
	}
	s := unsafe.Slice((*T)(p), n)
	if n <= uniquePairwise {
		for i := 1; i < n; i++ {
			for j := 0; j < i; j++ {
				if s[i] == s[j] {
					return i
				}
			}
		}
		return -1
	}
	seen := make(map[T]struct{}, n)
	for i, v := range s {
		if _, dup := seen[v]; dup {
			return i
		}
		seen[v] = struct{}{}
	}
	return -1
}

// Members is what a map type demands of its number of members: a named map type's Members
// method, read once at Registration (ADR 0021). Min and Max count members; Max is at least 1.
type Members struct {
	Min, Max int
	Message  string
}

// OK reports whether a map of n members is within the bounds.
func (m *Members) OK(n int) bool { return n >= m.Min && n <= m.Max }

// MembersOf reports the Members bound of t, and whether t has one. The method is read on a
// named map type with the signature func() (min, max int), on a zero receiver. The same method
// on a type that is not a map is an error (a list's size is its Items), except on a struct,
// whose method is the domain's own; a method of that name with another signature on a map type
// is an error too, and on any other type it is the domain's own method and is left alone.
func MembersOf(t reflect.Type) (Members, bool, error) {
	if t.Name() == "" {
		return Members{}, false, nil
	}
	if t.Kind() != reflect.Map {
		if m, ok := methodOf(t, "Members"); ok {
			if mt := m.Type; mt.NumIn() == 1 && mt.NumOut() == 2 && mt.Out(0) == reflect.TypeFor[int]() && mt.Out(1) == reflect.TypeFor[int]() && t.Kind() != reflect.Struct {
				if t.Kind() == reflect.Slice {
					return Members{}, false, fmt.Errorf("%s has a Members method, but Members bounds a map and %s is a list: a list's length is its Items method, func() (min, max int)", t, t)
				}
				return Members{}, false, fmt.Errorf("%s has a Members method, but Members bounds a map: it is read on a named map type, and %s is a %s", t, t, t.Kind())
			}
		}
		return Members{}, false, nil
	}
	lo, hi, ok, err := callPair(t, "Members", reflect.TypeFor[int](), "func() (min, max int)")
	if !ok || err != nil {
		return Members{}, false, err
	}
	if lo < 0 || hi < 1 || hi < lo {
		return Members{}, false, fmt.Errorf("%s.Members returns (%d, %d), which is not a count of members: it must be 0 <= min <= max with max at least 1", t, lo, hi)
	}
	var msg string
	switch {
	case lo == hi:
		msg = "must have exactly " + strconv.FormatInt(hi, 10) + " members"
	case lo == 0:
		msg = "must have at most " + strconv.FormatInt(hi, 10) + " members"
	default:
		msg = "must have between " + strconv.FormatInt(lo, 10) + " and " + strconv.FormatInt(hi, 10) + " members"
	}
	return Members{Min: int(lo), Max: int(hi), Message: msg}, true, nil
}

// FormatOf returns the JSON Schema format t names with its Format method, "" when it has none.
// The method is an annotation for the API document and changes nothing a request does. It is
// read on a named string type; on an integer or float type, where the document already states
// a format of its own (int32, int64, float, double), a Format of the exact signature is
// refused rather than allowed to override it. On any other type a Format method (time.Time's
// takes a layout) is the domain's own and is left alone.
func FormatOf(t reflect.Type) (string, error) {
	if t.Name() == "" {
		return "", nil
	}
	k := t.Kind()
	m, ok := methodOf(t, "Format")
	if !ok {
		return "", nil
	}
	mt := m.Type
	exact := mt.NumIn() == 1 && mt.NumOut() == 1 && mt.Out(0) == reflect.TypeFor[string]()
	switch {
	case k == reflect.String:
		if !exact {
			return "", fmt.Errorf("%s has a Format method, so it names its format in the API document, but Format must be func() string: rename the method if it means something else", t)
		}
	case exact && (k >= reflect.Int && k <= reflect.Uint64 || k == reflect.Float32 || k == reflect.Float64):
		return "", fmt.Errorf("%s has a Format method, but Format is read on a string type: the API document already gives a %s its own format", t, k)
	case exact:
		// A struct, slice or bool whose Format() string is the domain's own, unless it names a
		// format the framework checks: then a value of it could not be checked.
		if name := callFormatSafely(m, t); FormatRuleNamed(name) != nil {
			return "", fmt.Errorf("%s.Format returns %q, a format the framework checks, but %s is a %s and only a string can be checked: make it a string type, or name another format", t, name, t, k)
		}
		return "", nil
	default:
		return "", nil
	}
	f := m.Func.Call([]reflect.Value{reflect.New(t)})[0].String()
	if f == "" {
		return "", fmt.Errorf("%s.Format returns an empty string, which names no format: remove the method or name one", t)
	}
	return f, nil
}

// callFormatSafely calls a Format method of a type that is not a string, which is the domain's
// own and may not be written for a zero receiver; a panic is read as "names nothing".
func callFormatSafely(m reflect.Method, t reflect.Type) (name string) {
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	return m.Func.Call([]reflect.Value{reflect.New(t)})[0].String()
}

// FormatRuleOf returns the checked format of t, nil when its Format method is absent or names a
// format the framework only annotates. It returns FormatOf's refusals.
func FormatRuleOf(t reflect.Type) (*FormatRule, error) {
	name, err := FormatOf(t)
	if err != nil || name == "" {
		return nil, err
	}
	return FormatRuleNamed(name), nil
}

// OKString reports whether s satisfies the length, the pattern and the checked format of a string constraint.
func (c *Constraint) OKString(s string) bool {
	return c.OKLength(s) && c.OKPattern(s) && (c.Fmt == nil || c.Fmt.OK(s))
}

// OKPattern reports whether s matches the constraint's Pattern, true when it has none. The
// match is unanchored, as ECMA-262's is.
func (c *Constraint) OKPattern(s string) bool { return c.Pat == nil || c.Pat.Re.MatchString(s) }

// FailString returns the sentence, rule, bounds and format name of the check s fails: the length
// first, then the pattern, then the format. It is meant for an s that OKString refused.
func (c *Constraint) FailString(s string) (msg, rule string, lo, hi int64, format string) {
	if !c.OKLength(s) {
		lo, hi, _ = c.Bounds()
		return c.Message, c.Rule, lo, hi, ""
	}
	if !c.OKPattern(s) {
		return PatternMessage, RulePattern, 0, 0, ""
	}
	return c.Fmt.Message, RuleFormat, 0, 0, c.Fmt.Name
}

// OKLength reports whether s satisfies a ConLength constraint, true for any other kind.
// Characters are counted, not bytes, and a string longer in bytes than the most characters can
// occupy fails without being walked, so a hostile string costs nothing to refuse.
func (c *Constraint) OKLength(s string) bool {
	if c.Kind != ConLength {
		return true
	}
	if c.MaxLen >= 0 && len(s) > c.MaxLen*utf8.UTFMax {
		return false
	}
	n := utf8.RuneCountInString(s)
	return n >= c.MinLen && (c.MaxLen < 0 || n <= c.MaxLen)
}

// OKInt reports whether v satisfies a ConMin constraint: its bounds and its MultipleOf.
func (c *Constraint) OKInt(v int64) bool {
	return c.InRangeInt(v) && (c.MulInt == 0 || v%c.MulInt == 0)
}

// InRangeInt reports whether v is within the bounds of a ConMin constraint, leaving its
// MultipleOf aside.
func (c *Constraint) InRangeInt(v int64) bool { return v >= c.MinInt && (!c.HasMax || v <= c.MaxInt) }

var (
	nonEmptyType    = reflect.TypeFor[NonEmpty]()
	positiveIntType = reflect.TypeFor[PositiveInt]()
	positive64Type  = reflect.TypeFor[PositiveInt64]()
)

// ConstraintOf reports the constraint of t, or a zero Constraint when t is not one of the
// constraint types and carries no bound of its own. It returns an error for a bound that cannot
// work or a method of a bound's name that is not one, which Registration turns into a refusal
// naming the field.
func ConstraintOf(t reflect.Type) (Constraint, error) {
	switch t {
	case nonEmptyType:
		return Constraint{Kind: ConLength, MinLen: 1, MaxLen: -1, Message: "must not be empty", Rule: RuleNonEmpty}, nil
	case positiveIntType, positive64Type:
		return Constraint{Kind: ConMin, MinInt: 1, Message: "expected a positive integer", Rule: RulePositive}, nil
	}
	if t.Name() == "" {
		return Constraint{}, nil
	}
	switch t.Kind() {
	case reflect.String:
		if _, ok := methodOf(t, "Range"); ok {
			return Constraint{}, fmt.Errorf("%s has a Range method, but a string type's bound is its length: name it Length, func() (min, max int), in characters", t)
		}
		for _, name := range []string{"MultipleOf", "ExclusiveMin", "ExclusiveMax"} {
			if _, ok := methodOf(t, name); ok {
				return Constraint{}, fmt.Errorf("%s has a %s method, but %s is read on a number type and %s is a string: a string's bound is its length, a Length method, func() (min, max int), in characters", t, name, name, t)
			}
		}
		if err := misplacedRules(t); err != nil {
			return Constraint{}, err
		}
		pat, err := PatternOf(t)
		if err != nil {
			return Constraint{}, err
		}
		if pat != nil {
			if _, ok := valuesMethod(t); ok {
				return Constraint{}, fmt.Errorf("%s has a Pattern method and a Values method, but an enum lists every value it accepts: drop the Pattern", t)
			}
			if reflect.PointerTo(t).Implements(textUnmarshalerType) {
				return Constraint{}, fmt.Errorf("%s has a Pattern method, but it decodes itself from text, so its pattern would not be checked: drop the Pattern or check in UnmarshalText", t)
			}
		}
		fr, err := FormatRuleOf(t)
		if err != nil {
			return Constraint{}, err
		}
		if fr != nil {
			if _, ok := valuesMethod(t); ok {
				return Constraint{}, fmt.Errorf("%s has a Format method naming %q, which the framework checks, and a Values method, but an enum lists every value it accepts: drop the Format or the Values", t, fr.Name)
			}
			if reflect.PointerTo(t).Implements(textUnmarshalerType) {
				return Constraint{}, fmt.Errorf("%s has a Format method naming %q, which the framework checks, but it decodes itself from text, so its format would not be checked: drop the Format or check in UnmarshalText", t, fr.Name)
			}
		}
		lo, hi, ok, err := callPair(t, "Length", reflect.TypeFor[int](), "func() (min, max int)")
		if err != nil {
			return Constraint{}, err
		}
		if !ok {
			switch {
			case pat != nil:
				return Constraint{Kind: ConPattern, MaxLen: -1, Message: PatternMessage, Rule: RulePattern, Pat: pat, Fmt: fr}, nil
			case fr != nil:
				return Constraint{Kind: ConFormat, MaxLen: -1, Message: fr.Message, Rule: RuleFormat, Fmt: fr}, nil
			}
			return Constraint{}, nil
		}
		if lo < 0 || hi < 1 || hi < lo {
			return Constraint{}, fmt.Errorf("%s.Length returns (%d, %d), which is not a length: it must be 0 <= min <= max with max at least 1", t, lo, hi)
		}
		c := lengthConstraint(int(lo), int(hi))
		if c.MaxLen < 0 && c.MinLen == 0 {
			return Constraint{}, fmt.Errorf("%s.Length returns (0, math.MaxInt), which is the whole of a string and bounds nothing: drop the method", t)
		}
		c.Pat, c.Fmt = pat, fr
		return c, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if _, ok := methodOf(t, "Length"); ok {
			return Constraint{}, fmt.Errorf("%s has a Length method, but an integer type's bound is its value: name it Range, func() (min, max int64)", t)
		}
		if m, ok := methodOf(t, "Range"); ok && isFloatPair(m.Type) {
			return Constraint{}, fmt.Errorf("%s has a Range method that returns float64 bounds, but an integer type's bound is int64: name it Range, func() (min, max int64), or make %s a float64 type", t, t)
		}
		for _, name := range []string{"ExclusiveMin", "ExclusiveMax"} {
			if _, ok := methodOf(t, name); ok {
				return Constraint{}, fmt.Errorf("%s has an %s method, but an integer's bound is stated once, as Range, func() (min, max int64), both included: for a value above n write Range(n+1, ...) and for a value below n write Range(..., n-1)", t, name)
			}
		}
		if err := misplacedRules(t); err != nil {
			return Constraint{}, err
		}
		lo, hi, ok, err := callPair(t, "Range", reflect.TypeFor[int64](), "func() (min, max int64)")
		if err != nil {
			return Constraint{}, err
		}
		mul, hasMul, err := callOne(t, "MultipleOf", reflect.TypeFor[int64](), "func() int64")
		if err != nil {
			return Constraint{}, err
		}
		if !ok && !hasMul {
			return Constraint{}, nil
		}
		if k := t.Kind(); k != reflect.Int && k != reflect.Int64 {
			rule := "Range"
			if !ok {
				rule = "MultipleOf"
			}
			return Constraint{}, fmt.Errorf("%s has a %s, which is read on an int or int64 type, and %s is a %s", t, rule, t, k)
		}
		if hasMul && mul < 1 {
			return Constraint{}, fmt.Errorf("%s.MultipleOf returns %d, and a step must be at least 1", t, mul)
		}
		if hasMul && mul == 1 {
			return Constraint{}, fmt.Errorf("%s.MultipleOf returns 1, and every integer is a multiple of 1, so it bounds nothing: drop the method", t)
		}
		if !ok {
			lo, hi = math.MinInt64, math.MaxInt64
			if t.Kind() == reflect.Int {
				lo, hi = math.MinInt, math.MaxInt
			}
		}
		if hi < lo {
			return Constraint{}, fmt.Errorf("%s.Range returns (%d, %d), and max is below min, so no value could be valid", t, lo, hi)
		}
		tmin, tmax := int64(math.MinInt64), int64(math.MaxInt64)
		if t.Kind() == reflect.Int {
			tmin, tmax = int64(math.MinInt), int64(math.MaxInt)
		}
		noMin, hasMax := lo <= tmin, hi < tmax
		if ok && noMin && !hasMax {
			return Constraint{}, fmt.Errorf("%s.Range returns the whole of %s and bounds nothing: drop the method", t, t.Kind())
		}
		if hasMul && !noMin && hasMax && !hasMultipleIn(lo, hi, mul) {
			return Constraint{}, fmt.Errorf("%s.Range returns (%d, %d) and MultipleOf returns %d, and no integer between the two is a multiple of %d, so no value could be valid", t, lo, hi, mul, mul)
		}
		var msg string
		switch {
		case !ok:
		case noMin:
			msg = "expected an integer of at most " + strconv.FormatInt(hi, 10)
		case !hasMax:
			msg = "expected an integer of at least " + strconv.FormatInt(lo, 10)
		default:
			msg = "expected an integer between " + strconv.FormatInt(lo, 10) + " and " + strconv.FormatInt(hi, 10)
		}
		c := Constraint{Kind: ConMin, MinInt: lo, MaxInt: hi, HasMax: hasMax, NoMin: noMin, Message: msg, Rule: RuleRange}
		if noMin {
			c.MinInt = math.MinInt64
		}
		if hasMul {
			c.MulInt, c.MulMessage = mul, "expected a multiple of "+strconv.FormatInt(mul, 10)
		}
		return c, nil
	case reflect.Float32, reflect.Float64:
		if err := misplacedRules(t); err != nil {
			return Constraint{}, err
		}
		return floatConstraintOf(t)
	}
	if err := misplacedRules(t); err != nil {
		return Constraint{}, err
	}
	return Constraint{}, nil
}

// hasMultipleIn reports whether some multiple of step lies in [lo, hi], for lo <= hi.
func hasMultipleIn(lo, hi, step int64) bool {
	r := lo % step
	if r == 0 {
		return true
	}
	gap := step - r
	if r < 0 {
		gap = -r
	}
	return uint64(hi)-uint64(lo) >= uint64(gap)
}

// misplacedRules refuses a method that has the exact signature of a number rule (MultipleOf,
// ExclusiveMin, ExclusiveMax) or of a collection rule (Unique, Members) on a type that cannot
// carry it, because nothing would check it. A struct's method of the name is the domain's own
// and is left alone, as is a method with another signature on a type the rule does not apply to.
func misplacedRules(t reflect.Type) error {
	switch t.Kind() {
	case reflect.Struct, reflect.Interface, reflect.Pointer, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil
	}
	k := t.Kind()
	isNum := k >= reflect.Int && k <= reflect.Uint64 || k == reflect.Float32 || k == reflect.Float64
	f64, i64 := reflect.TypeFor[float64](), reflect.TypeFor[int64]()
	one := func(mt reflect.Type, out reflect.Type) bool {
		return mt.NumIn() == 1 && mt.NumOut() == 1 && mt.Out(0) == out
	}
	if !isNum {
		for _, name := range []string{"MultipleOf", "ExclusiveMin", "ExclusiveMax"} {
			if m, ok := methodOf(t, name); ok && (one(m.Type, f64) || name == "MultipleOf" && one(m.Type, i64)) {
				return fmt.Errorf("%s has a %s method, but %s is read on an int, int64, float32 or float64 type, and %s is a %s", t, name, name, t, k)
			}
		}
	}
	if k != reflect.Slice {
		if m, ok := methodOf(t, "Unique"); ok && isUniqueSig(m.Type) {
			return fmt.Errorf("%s has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and %s is a %s", t, t, k)
		}
	}
	return nil
}

// callOne calls t's method name on a zero receiver and returns its one result. A method of that
// name with another signature is an error, for the reason callPair gives.
func callOne(t reflect.Type, name string, want reflect.Type, sig string) (v int64, ok bool, err error) {
	m, ok := methodOf(t, name)
	if !ok {
		return 0, false, nil
	}
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0) != want {
		return 0, false, fmt.Errorf("%s has a %s method, so it carries a rule, but %s must be %s: rename the method if it means something else", t, name, name, sig)
	}
	return m.Func.Call([]reflect.Value{reflect.New(t)})[0].Int(), true, nil
}

func isFloatPair(mt reflect.Type) bool {
	f := reflect.TypeFor[float64]()
	return mt.NumIn() == 1 && mt.NumOut() == 2 && mt.Out(0) == f && mt.Out(1) == f
}

// floatConstraintOf reads the rules of a float32 or float64 type: Range, a number between min and
// max both included, ExclusiveMin and ExclusiveMax, a bound the value must be beyond and not
// equal to, and MultipleOf, a step. Range and the exclusive bounds share the rule code "range" and
// its sentences; MultipleOf is the rule "multiple". A bound equal to the type's own limit
// (-math.MaxFloat64 and math.MaxFloat64, or math.MaxFloat32 for a float32) is not a bound, as for
// an integer. A side is stated once: by Range or by an exclusive method, never both.
func floatConstraintOf(t reflect.Type) (Constraint, error) {
	limit := math.MaxFloat64
	if t.Kind() == reflect.Float32 {
		limit = math.MaxFloat32
	}
	lo, hi := -limit, limit
	rm, hasRange := methodOf(t, "Range")
	if hasRange {
		if mt := rm.Type; mt.NumIn() == 1 && mt.NumOut() == 2 && mt.Out(0) == reflect.TypeFor[int64]() && mt.Out(1) == reflect.TypeFor[int64]() {
			return Constraint{}, fmt.Errorf("%s has a Range method that returns int64 bounds, but a float type's bound is float64: name it Range, func() (min, max float64), or make %s an int64 type", t, t)
		}
		if !isFloatPair(rm.Type) {
			return Constraint{}, fmt.Errorf("%s has a Range method, so it carries a bound, but Range must be func() (min, max float64): rename the method if it means something else", t)
		}
		out := rm.Func.Call([]reflect.Value{reflect.New(t)})
		lo, hi = out[0].Float(), out[1].Float()
		if math.IsNaN(lo) || math.IsNaN(hi) || math.IsInf(lo, 0) || math.IsInf(hi, 0) {
			return Constraint{}, fmt.Errorf("%s.Range returns (%v, %v), and a bound must be a finite number: use -math.MaxFloat64 or math.MaxFloat64 for no bound on a side", t, lo, hi)
		}
		if t.Kind() == reflect.Float32 && (lo < -limit || hi > limit || lo > limit || hi < -limit) {
			return Constraint{}, fmt.Errorf("%s.Range returns (%s, %s), which is outside the range of a float32 (%s to %s): use -math.MaxFloat32 or math.MaxFloat32 for no bound on a side, or make %s a float64 type", t, fmtFloat(lo), fmtFloat(hi), fmtFloat(-limit), fmtFloat(limit), t)
		}
		if hi < lo {
			return Constraint{}, fmt.Errorf("%s.Range returns (%s, %s), and max is below min, so no value could be valid", t, fmtFloat(lo), fmtFloat(hi))
		}
	}
	noMin, hasMax := lo <= -limit, hi < limit
	if hasRange && noMin && !hasMax {
		return Constraint{}, fmt.Errorf("%s.Range returns the whole of %s and bounds nothing: drop the method", t, t.Kind())
	}
	exMin, hasExMin, err := floatBound(t, "ExclusiveMin", limit)
	if err != nil {
		return Constraint{}, err
	}
	exMax, hasExMax, err := floatBound(t, "ExclusiveMax", limit)
	if err != nil {
		return Constraint{}, err
	}
	if hasExMin {
		if !noMin {
			return Constraint{}, fmt.Errorf("%s has ExclusiveMin and a Range that already bounds the minimum (%s): a side is stated once, so leave Range's min at -math.MaxFloat64 and keep the exclusive bound, or drop ExclusiveMin", t, fmtFloat(lo))
		}
		lo, noMin = exMin, false
	}
	if hasExMax {
		if hasMax {
			return Constraint{}, fmt.Errorf("%s has ExclusiveMax and a Range that already bounds the maximum (%s): a side is stated once, so leave Range's max at math.MaxFloat64 and keep the exclusive bound, or drop ExclusiveMax", t, fmtFloat(hi))
		}
		hi, hasMax = exMax, true
	}
	if (hasExMin || hasExMax) && !noMin && hasMax && hi <= lo {
		return Constraint{}, fmt.Errorf("%s states a minimum of %s and a maximum of %s, and an exclusive bound leaves no value between them", t, fmtFloat(lo), fmtFloat(hi))
	}
	mul, hasMul, err := floatBound(t, "MultipleOf", math.Inf(1))
	if err != nil {
		return Constraint{}, err
	}
	if hasMul && mul <= 0 {
		return Constraint{}, fmt.Errorf("%s.MultipleOf returns %s, and a step must be greater than 0", t, fmtFloat(mul))
	}
	eps := 4 * 0x1p-52
	if t.Kind() == reflect.Float32 {
		eps = 4 * 0x1p-23
		mulStored := float64(float32(mul))
		if hasMul && (mul > limit || mulStored == 0) {
			return Constraint{}, fmt.Errorf("%s.MultipleOf returns %s, which a float32 cannot hold: use a step within %s to %s, or make %s a float64 type", t, fmtFloat(mul), fmtFloat(math.SmallestNonzeroFloat32), fmtFloat(limit), t)
		}
	}
	if !hasRange && !hasExMin && !hasExMax && !hasMul {
		return Constraint{}, nil
	}
	var msg string
	switch {
	case noMin && !hasMax:
	case noMin:
		msg = "expected a number " + upperPart(hi, hasExMax, true)
	case !hasMax:
		msg = "expected a number " + lowerPart(lo, hasExMin, true)
	case !hasExMin && !hasExMax:
		msg = "expected a number between " + fmtFloat(lo) + " and " + fmtFloat(hi)
	default:
		msg = "expected a number " + lowerPart(lo, hasExMin, false) + " and " + upperPart(hi, hasExMax, false)
	}
	c := Constraint{Kind: ConFloat, MinF: lo, MaxF: hi, HasMax: hasMax, NoMin: noMin, ExMin: hasExMin, ExMax: hasExMax, Message: msg, Rule: RuleRange, loF: lo, hiF: hi}
	if t.Kind() == reflect.Float32 {
		c.loF, c.hiF = float64(float32(lo)), float64(float32(hi))
	}
	if hasMul {
		c.MulF, c.mulF, c.mulEps = mul, mul, eps
		if t.Kind() == reflect.Float32 {
			c.mulF = float64(float32(mul))
		}
		c.MulMessage = "expected a multiple of " + fmtFloat(mul)
		if hasRange || hasExMin || hasExMax {
			if !noMin && hasMax && !c.hasMultipleIn() {
				return Constraint{}, fmt.Errorf("%s states a minimum of %s, a maximum of %s and MultipleOf %s, and no number between the two is a multiple of it, so no value could be valid", t, fmtFloat(lo), fmtFloat(hi), fmtFloat(mul))
			}
		}
	}
	return c, nil
}

// hasMultipleIn reports whether a multiple of the step lies within the bounds of a ConFloat
// constraint with both sides bounded, by trying the first multiple at or above the minimum.
func (c *Constraint) hasMultipleIn() bool {
	k := math.Ceil(c.loF / c.mulF)
	for _, n := range []float64{k - 1, k, k + 1} {
		if v := n * c.mulF; c.OKFloat(v) {
			return true
		}
	}
	return false
}

// lowerPart and upperPart write one side of a range: "at least 5" and "greater than 5", "at most
// 9" and "less than 9". A side alone reads "of at least 5", so the sentence is "expected a number
// of at least 5" and "expected a number greater than 5".
func lowerPart(v float64, exclusive, alone bool) string {
	switch {
	case exclusive:
		return "greater than " + fmtFloat(v)
	case alone:
		return "of at least " + fmtFloat(v)
	}
	return "at least " + fmtFloat(v)
}

func upperPart(v float64, exclusive, alone bool) string {
	switch {
	case exclusive:
		return "less than " + fmtFloat(v)
	case alone:
		return "of at most " + fmtFloat(v)
	}
	return "at most " + fmtFloat(v)
}

// floatBound calls the method name of t, which must be func() float64, on a zero receiver. A
// result that is NaN or infinite, or beyond limit (the type's own limit, which is not a bound),
// is an error.
func floatBound(t reflect.Type, name string, limit float64) (v float64, ok bool, err error) {
	m, ok := methodOf(t, name)
	if !ok {
		return 0, false, nil
	}
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 1 || mt.Out(0) != reflect.TypeFor[float64]() {
		return 0, false, fmt.Errorf("%s has a %s method, so it carries a rule, but %s must be func() float64: rename the method if it means something else", t, name, name)
	}
	v = m.Func.Call([]reflect.Value{reflect.New(t)})[0].Float()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, fmt.Errorf("%s.%s returns %v, and a bound must be a finite number", t, name, v)
	}
	if name != "MultipleOf" && math.Abs(v) >= limit {
		return 0, false, fmt.Errorf("%s.%s returns %s, which is the limit of %s and not a bound: drop the method, or give a number inside the type's range", t, name, fmtFloat(v), t.Kind())
	}
	return v, true, nil
}

// fmtFloat writes a bound the way a person writes it: digits and no exponent unless the number
// is very large or very small.
func fmtFloat(f float64) string {
	return string(AppendFloat(nil, f))
}

// AppendFloat appends f as a JSON number in the shortest digits that read back, without an
// exponent unless f is below 1e-6 or at least 1e21.
func AppendFloat(dst []byte, f float64) []byte {
	format := byte('f')
	if a := math.Abs(f); a != 0 && (a < 1e-6 || a >= 1e21) {
		format = 'e'
	}
	return strconv.AppendFloat(dst, f, format, -1, 64)
}

// methodOf finds t's method name on the value or the pointer.
func methodOf(t reflect.Type, name string) (reflect.Method, bool) {
	return reflect.PointerTo(t).MethodByName(name)
}

// callPair calls t's method name on a zero receiver and returns its two results. A method of
// that name with another signature is an error: on a string or integer type the name means a
// bound, and a domain type whose method means something else renames it rather than leave the
// reader to guess which one decoding obeys.
func callPair(t reflect.Type, name string, want reflect.Type, sig string) (lo, hi int64, ok bool, err error) {
	m, ok := methodOf(t, name)
	if !ok {
		return 0, 0, false, nil
	}
	mt := m.Type
	if mt.NumIn() != 1 || mt.NumOut() != 2 || mt.Out(0) != want || mt.Out(1) != want {
		return 0, 0, false, fmt.Errorf("%s has a %s method, so it carries a bound, but %s must be %s: rename the method if it means something else", t, name, name, sig)
	}
	out := m.Func.Call([]reflect.Value{reflect.New(t)})
	return out[0].Int(), out[1].Int(), true, nil
}

func lengthConstraint(lo, hi int) Constraint {
	var msg string
	if hi == math.MaxInt {
		return Constraint{Kind: ConLength, MinLen: lo, MaxLen: -1, Message: "must be at least " + strconv.Itoa(lo) + " characters", Rule: RuleLength}
	}
	switch {
	case lo == hi:
		msg = "must be exactly " + strconv.Itoa(hi) + " characters"
	case lo == 0:
		msg = "must be at most " + strconv.Itoa(hi) + " characters"
	default:
		msg = "must be between " + strconv.Itoa(lo) + " and " + strconv.Itoa(hi) + " characters"
	}
	return Constraint{Kind: ConLength, MinLen: lo, MaxLen: hi, Message: msg, Rule: RuleLength}
}

// Checker is a named type that checks its own value: Check runs after the value decoded, and
// an error is a decoding error at the value's location whose message is the error's text.
type Checker interface{ Check() error }

var checkerType = reflect.TypeFor[Checker]()

// CheckOf returns what runs t's Check on a decoded value, or nil when t has none. The method is
// reached through a pointer to the value, so nothing is boxed and a request pays one call. Check
// must have a value receiver: it then gets a copy and cannot keep the pointer, which lets the
// decoder hide the pointer from escape analysis as it does for reflect (jsoncodec's noescape).
func CheckOf(t reflect.Type) (func(unsafe.Pointer) error, error) {
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface || t.Name() == "" {
		return nil, nil
	}
	if _, ok := methodOf(t, "Check"); !ok {
		return nil, nil
	}
	if !reflect.PointerTo(t).Implements(checkerType) {
		return nil, fmt.Errorf("%s has a Check method, so it checks its value, but Check must be func() error: rename the method if it means something else", t)
	}
	if !t.Implements(checkerType) {
		return nil, fmt.Errorf("%s.Check has a pointer receiver, but a value is checked as it was decoded and must not be kept: declare it on %s", t, t.Name())
	}
	return func(p unsafe.Pointer) error {
		return reflect.NewAt(t, p).Interface().(Checker).Check()
	}, nil
}

// constrainedDecoder decodes a constraint type (ADR 0021): the base type's decoder, then the
// check. A value that fails is not stored, so the destination stays as it was.
func constrainedDecoder(t reflect.Type, c Constraint) TextDecoder {
	lo, hi, _ := c.Bounds()
	err := &TextError{msg: c.Message, Rule: c.Rule, Min: lo, Max: hi}
	merr := &TextError{msg: c.MulMessage, Rule: RuleMultiple, Min: c.MulInt}
	if c.Kind == ConFloat {
		flo, fhi := c.FloatBounds()
		err.Float, err.MinF, err.MaxF, err.ExMin, err.ExMax = true, flo, fhi, c.ExMin, c.ExMax
		merr.Float, merr.MinF, merr.Min = true, c.MulF, 0
		if t.Kind() == reflect.Float32 {
			base := floatDecoder[float32](32)
			return func(dst unsafe.Pointer, s string) error {
				old := *(*float32)(dst)
				if e := base(dst, s); e != nil {
					return e
				}
				if v := float64(*(*float32)(dst)); !c.OKFloat(v) {
					*(*float32)(dst) = old
					if !c.InRangeFloat(v) {
						return err
					}
					return merr
				}
				return nil
			}
		}
		base := floatDecoder[float64](64)
		return func(dst unsafe.Pointer, s string) error {
			old := *(*float64)(dst)
			if e := base(dst, s); e != nil {
				return e
			}
			if v := *(*float64)(dst); !c.OKFloat(v) {
				*(*float64)(dst) = old
				if !c.InRangeFloat(v) {
					return err
				}
				return merr
			}
			return nil
		}
	}
	if c.Kind == ConLength || c.Kind == ConPattern || c.Kind == ConFormat {
		perr := &TextError{msg: PatternMessage, Rule: RulePattern}
		var ferr *TextError
		if c.Fmt != nil {
			ferr = &TextError{msg: c.Fmt.Message, Rule: RuleFormat, Format: c.Fmt.Name}
		}
		return func(dst unsafe.Pointer, s string) error {
			if !c.OKLength(s) {
				return err
			}
			if !c.OKPattern(s) {
				return perr
			}
			if ferr != nil && !c.Fmt.OK(s) {
				return ferr
			}
			*(*string)(dst) = s
			return nil
		}
	}
	if t.Kind() == reflect.Int {
		base := intDecoder[int](strconv.IntSize)
		return func(dst unsafe.Pointer, s string) error {
			old := *(*int)(dst)
			if e := base(dst, s); e != nil {
				return e
			}
			if v := int64(*(*int)(dst)); !c.OKInt(v) {
				*(*int)(dst) = old
				if !c.InRangeInt(v) {
					return err
				}
				return merr
			}
			return nil
		}
	}
	base := intDecoder[int64](64)
	return func(dst unsafe.Pointer, s string) error {
		old := *(*int64)(dst)
		if e := base(dst, s); e != nil {
			return e
		}
		if v := *(*int64)(dst); !c.OKInt(v) {
			*(*int64)(dst) = old
			if !c.InRangeInt(v) {
				return err
			}
			return merr
		}
		return nil
	}
}
