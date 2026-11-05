package wire

import (
	"errors"
	"time"
	"unsafe"
)

// Date is a calendar date: a year, a month and a day with no time of day and no zone, written
// YYYY-MM-DD and described in the API document as `type: string, format: date` (ADR 0042).
//
// It is stored as the number of days since 0001-01-01, so the zero Date is that day, as the zero
// time.Time is, and comparing two is comparing two integers. A Date has no instant: converting
// one to a time.Time means choosing a zone, so there is no Time method, only In.
type Date struct{ days int32 }

// The fixed sentences. They never quote the client's value (ADR 0012).
var (
	// ErrInvalidDate is what NewDate, ParseDate and UnmarshalText return. Its text is fixed so
	// that sending it to a client reflects nothing.
	ErrInvalidDate = errors.New("expected a date as YYYY-MM-DD")
)

const (
	daysTo1970 = 719162  // days from 0001-01-01 to 1970-01-01
	lastDay    = 3652058 // days from 0001-01-01 to 9999-12-31, the last date YYYY-MM-DD can write
)

// NewDate returns the Date of a year (1 to 9999), a month and a day. A day that does not exist
// in that month is ErrInvalidDate; nothing is normalised, unlike time.Date.
func NewDate(year int, month time.Month, day int) (Date, error) {
	d, ok := civil(year, int(month), day)
	if !ok {
		return Date{}, ErrInvalidDate
	}
	return d, nil
}

// DateOf returns the calendar day t falls on in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	if y < 1 || y > 9999 {
		return Date{}
	}
	date, _ := civil(y, int(m), d)
	return date
}

// ParseDate reads YYYY-MM-DD. Anything else, a timestamp included, is ErrInvalidDate.
func ParseDate(s string) (Date, error) {
	d, ok := parseDate(s)
	if !ok {
		return Date{}, ErrInvalidDate
	}
	return d, nil
}

func parseDate(s string) (Date, bool) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, false
	}
	y, ok1 := digits(s[0:4])
	m, ok2 := digits(s[5:7])
	d, ok3 := digits(s[8:10])
	if !ok1 || !ok2 || !ok3 {
		return Date{}, false
	}
	return civil(y, m, d)
}

func digits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// civil is days_from_civil of the proleptic Gregorian calendar, checked.
func civil(y, m, d int) (Date, bool) {
	if y < 1 || y > 9999 || m < 1 || m > 12 || d < 1 || d > daysIn(y, m) {
		return Date{}, false
	}
	if m <= 2 {
		y--
	}
	era := y / 400
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return Date{int32(era*146097 + doe - 719468 + daysTo1970)}, true
}

func daysIn(y, m int) int {
	switch m {
	case 2:
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// ymd is civil_from_days.
func (d Date) ymd() (y, m, day int) {
	z := int(d.days) - daysTo1970 + 719468
	era := z / 146097
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y = yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	day = doy - (153*mp+2)/5 + 1
	m = mp + 3
	if m > 12 {
		m -= 12
	}
	if m <= 2 {
		y++
	}
	return
}

// Year returns the year, 1 to 9999.
func (d Date) Year() int { y, _, _ := d.ymd(); return y }

// Month returns the month.
func (d Date) Month() time.Month { _, m, _ := d.ymd(); return time.Month(m) }

// Day returns the day of the month.
func (d Date) Day() int { _, _, day := d.ymd(); return day }

// In returns midnight at the start of the date in loc. A nil loc is UTC.
func (d Date) In(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, day := d.ymd()
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, loc)
}

// IsZero reports whether d is the zero Date, 0001-01-01.
func (d Date) IsZero() bool { return d.days == 0 }

// Before reports whether d is earlier than u.
func (d Date) Before(u Date) bool { return d.days < u.days }

// After reports whether d is later than u.
func (d Date) After(u Date) bool { return d.days > u.days }

// Equal reports whether d and u are the same day.
func (d Date) Equal(u Date) bool { return d.days == u.days }

// AddDays returns the date n days after d (before it when n is negative). A result outside
// 0001-01-01 to 9999-12-31 is ErrInvalidDate rather than a date YYYY-MM-DD cannot write; n may
// come from a client, so it is an error and not a panic. There is no AddMonths: the 31st of
// January plus a month has no single answer, so that arithmetic goes through In and
// time.Time.AddDate, where the choice is visible.
func (d Date) AddDays(n int) (Date, error) {
	days := int64(d.days) + int64(n)
	if days < 0 || days > lastDay {
		return Date{}, ErrInvalidDate
	}
	return Date{days: int32(days)}, nil
}

// Sub returns the number of days from u to d: positive when d is after u, so a due date's
// lateness is today.Sub(due). It cannot overflow, since both lie within 1 to 9999.
func (d Date) Sub(u Date) int { return int(d.days) - int(u.days) }

// Compare returns -1, 0 or +1 as d is before, the same as, or after u.
func (d Date) Compare(u Date) int {
	switch {
	case d.days < u.days:
		return -1
	case d.days > u.days:
		return 1
	}
	return 0
}

// String returns YYYY-MM-DD.
func (d Date) String() string { return string(d.AppendText(make([]byte, 0, 10))) }

// AppendText appends YYYY-MM-DD, as MarshalText writes it.
func (d Date) AppendText(b []byte) []byte {
	y, m, day := d.ymd()
	return append(b,
		byte('0'+y/1000), byte('0'+y/100%10), byte('0'+y/10%10), byte('0'+y%10), '-',
		byte('0'+m/10), byte('0'+m%10), '-',
		byte('0'+day/10), byte('0'+day%10))
}

// MarshalText returns YYYY-MM-DD.
func (d Date) MarshalText() ([]byte, error) { return d.AppendText(make([]byte, 0, 10)), nil }

// UnmarshalText reads YYYY-MM-DD, or returns ErrInvalidDate.
func (d *Date) UnmarshalText(b []byte) error {
	v, ok := parseDate(string(b))
	if !ok {
		return ErrInvalidDate
	}
	*d = v
	return nil
}

// decodeDate is the text decoder of a Date.
func decodeDate(dst unsafe.Pointer, s string) error {
	v, ok := parseDate(s)
	if !ok {
		return errDate
	}
	*(*Date)(dst) = v
	return nil
}
