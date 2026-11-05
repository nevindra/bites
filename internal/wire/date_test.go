package wire

import (
	"math"
	"reflect"
	"testing"
	"time"
	"unsafe"
)

func TestADateReadsExactlyYYYYMMDDAndWritesTheSame(t *testing.T) {
	for _, s := range []string{"2026-11-03", "0001-01-01", "9999-12-31", "2024-02-29", "2000-02-29"} {
		d, err := ParseDate(s)
		if err != nil || d.String() != s {
			t.Errorf("%s: got %v, %v", s, d, err)
		}
	}
}

func TestADateRefusesWhatIsNotACalendarDayWithAFixedSentence(t *testing.T) {
	for _, s := range []string{
		"", "2026-1-3", "2026-11-3", "26-11-03", "2026/11/03", "2026-11-03T00:00:00Z", "2026-11-03 ",
		"2026-02-29", "1900-02-29", "2026-04-31", "2026-13-01", "2026-00-10", "2026-11-00", "0000-01-01",
		"+026-11-03", "2026-11-0x", "２０２６-11-03",
	} {
		if d, err := ParseDate(s); err != ErrInvalidDate || !d.IsZero() {
			t.Errorf("%q: got %v, %v", s, d, err)
		}
	}
	if ErrInvalidDate.Error() != "expected a date as YYYY-MM-DD" {
		t.Errorf("the sentence is %q", ErrInvalidDate)
	}
}

func TestEveryDayFromYear1To9999RoundTripsThroughItsParts(t *testing.T) {
	d := Date{}
	for ; d.days <= 3652058; d.days += 37 {
		y, m, day := d.Year(), d.Month(), d.Day()
		back, err := NewDate(y, m, day)
		if err != nil || back != d {
			t.Fatalf("day %d is %d-%d-%d, which builds %v, %v", d.days, y, m, day, back, err)
		}
		if !d.In(time.UTC).Equal(time.Date(y, m, day, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("day %d: In(UTC) disagrees with time.Date", d.days)
		}
	}
}

func TestADateAgreesWithTimeOnTheCalendar(t *testing.T) {
	for _, tm := range []time.Time{time.Unix(0, 0).UTC(), time.Date(2024, 2, 29, 23, 59, 0, 0, time.UTC), time.Date(1999, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2100, 3, 1, 0, 0, 0, 0, time.UTC)} {
		d := DateOf(tm)
		if d.Year() != tm.Year() || d.Month() != tm.Month() || d.Day() != tm.Day() {
			t.Errorf("%v: got %v", tm, d)
		}
	}
}

func TestDateOfTakesTheCalendarDayInTheTimesOwnLocation(t *testing.T) {
	jkt := time.FixedZone("WIB", 7*3600)
	tm := time.Date(2026, 11, 3, 1, 0, 0, 0, jkt) // still 2026-11-02 in UTC
	if got := DateOf(tm).String(); got != "2026-11-03" {
		t.Errorf("got %s", got)
	}
	if got := DateOf(tm.UTC()).String(); got != "2026-11-02" {
		t.Errorf("got %s", got)
	}
}

func TestADateInALocationIsMidnightThereAndNilIsUTC(t *testing.T) {
	d, _ := NewDate(2026, time.November, 3)
	jkt := time.FixedZone("WIB", 7*3600)
	if got := d.In(jkt); got.Hour() != 0 || got.Day() != 3 || got.Location() != jkt {
		t.Errorf("got %v", got)
	}
	if got := d.In(nil); got.Location() != time.UTC || got.Day() != 3 {
		t.Errorf("got %v", got)
	}
}

func TestDatesCompareByTheCalendar(t *testing.T) {
	a, _ := ParseDate("2026-02-28")
	b, _ := ParseDate("2026-03-01")
	if !a.Before(b) || b.Before(a) || !b.After(a) || a.After(b) || a.Equal(b) || !a.Equal(a) {
		t.Error("Before, After or Equal is wrong")
	}
	if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(a) != 0 {
		t.Error("Compare is wrong")
	}
	if !(Date{}).IsZero() || a.IsZero() || (Date{}).String() != "0001-01-01" {
		t.Error("the zero Date is 0001-01-01")
	}
}

func TestADateIsAnEightByteOrSmallerValueWithNoPointer(t *testing.T) {
	if unsafe.Sizeof(Date{}) != 4 {
		t.Errorf("size %d", unsafe.Sizeof(Date{}))
	}
	if reflect.TypeFor[Date]().Comparable() == false {
		t.Error("a Date must be comparable")
	}
}

func TestADateDecodesFromTextWithoutAllocatingAndNeverQuotesTheValue(t *testing.T) {
	d, err := decode[Date]("2026-11-03")
	if err != nil || d.String() != "2026-11-03" {
		t.Fatalf("got %v, %v", d, err)
	}
	_, err = decode[Date]("2026-11-03T00:00:00Z")
	if msg(err) != "expected a date as YYYY-MM-DD" {
		t.Errorf("got %q", msg(err))
	}
	dec, _ := CompileText(reflect.TypeFor[Date]())
	var v Date
	allocs := testing.AllocsPerRun(100, func() { _ = dec(unsafe.Pointer(&v), "2026-11-03") })
	if allocs != 0 {
		t.Errorf("a Date costs %v allocations to decode from text", allocs)
	}
}

func TestADateMarshalsAsTextAndUnmarshalsWithTheFixedError(t *testing.T) {
	d, _ := NewDate(2026, 11, 3)
	b, err := d.MarshalText()
	if err != nil || string(b) != "2026-11-03" {
		t.Fatal(string(b), err)
	}
	var back Date
	if err := back.UnmarshalText(b); err != nil || back != d {
		t.Fatal(back, err)
	}
	if err := back.UnmarshalText([]byte("nope")); err != ErrInvalidDate {
		t.Errorf("got %v", err)
	}
}

func TestAddingDaysCrossesMonthsYearsAndLeapDaysAsTheCalendarDoes(t *testing.T) {
	for _, c := range []struct {
		from string
		n    int
		want string
	}{
		{"2026-01-31", 1, "2026-02-01"},
		{"2024-02-28", 1, "2024-02-29"},
		{"2026-12-31", 1, "2027-01-01"},
		{"2026-03-01", -1, "2026-02-28"},
		{"2026-11-03", 30, "2026-12-03"},
		{"2026-11-03", 0, "2026-11-03"},
	} {
		d, _ := ParseDate(c.from)
		got, err := d.AddDays(c.n)
		if err != nil || got.String() != c.want {
			t.Errorf("%s %+d: got %v, %v; want %s", c.from, c.n, got, err, c.want)
		}
		if back := got.Sub(d); back != c.n {
			t.Errorf("%s: Sub gave %d, want %d", c.from, back, c.n)
		}
	}
}

func TestAddingDaysPastYear9999OrBeforeYear1IsTheFixedErrorAndNeverWraps(t *testing.T) {
	last, _ := ParseDate("9999-12-31")
	if d, _ := NewDate(9999, 12, 31); d.days != lastDay {
		t.Fatalf("lastDay is %d, but 9999-12-31 is %d", lastDay, d.days)
	}
	first := Date{}
	for _, c := range []struct {
		d Date
		n int
	}{{last, 1}, {first, -1}, {first, math.MaxInt}, {last, math.MinInt}} {
		if got, err := c.d.AddDays(c.n); err != ErrInvalidDate || !got.IsZero() {
			t.Errorf("%s %+d: got %v, %v; want ErrInvalidDate", c.d, c.n, got, err)
		}
	}
	if got, err := first.AddDays(lastDay); err != nil || got != last {
		t.Errorf("0001-01-01 + lastDay: got %v, %v", got, err)
	}
}

func TestSubCountsDaysFromTheArgumentSoLatenessIsPositive(t *testing.T) {
	due, _ := ParseDate("2026-10-31")
	today, _ := ParseDate("2026-11-03")
	if got := today.Sub(due); got != 3 {
		t.Errorf("today.Sub(due) = %d, want 3", got)
	}
	if got := due.Sub(today); got != -3 {
		t.Errorf("due.Sub(today) = %d, want -3", got)
	}
}
