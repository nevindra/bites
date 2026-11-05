package bites

import (
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
)

// Opt is a value that may be absent (ADR 0041): the zero Opt is absent, Some(v) is present.
// It is a field type only, in every section of an Input.
//
// Its methods (go doc does not list a method through an alias, so they are here):
//
//	Get() (T, bool)   the value and whether it was sent; T's zero when it was not
//	IsSet() bool      whether the value was sent
//	Or(def T) T       the value, or def when it was not sent
//	Ptr() *T          nil when not sent, else a pointer to a copy that is the caller's
//	Set(v T)          (pointer receiver) makes it present holding v; for code that fills an Opt
//	                  through reflection, such as modules/config. Application code uses Some
//	String() string   the value, or "<absent>"
//	GoString() string a form that reads back as the Go that made it
type Opt[T any] = wire.Opt[T]

// CSV is a list a client sends as one comma-separated value, `?stage=won,lost`, in a Query or a
// Header (ADR 0042). It is a plain slice to the handler. A plain []T is still filled from repeated keys.
//
// Its one method (go doc does not list a method through an alias, so they are here), CommaSeparated(), only marks the type and does nothing when called. A
// named type of the application's own, `type Stages bites.CSV[Stage]`, does not inherit it and
// is read from repeated keys: use bites.CSV[Stage] itself.
type CSV[T any] = wire.CSV[T]

// Null is a value that may be sent as null (ADR 0041): the zero Null is null, NonNull(v) holds
// a value. Opt[Null[T]] is the PATCH field: absent leaves the stored value, null clears it.
//
// Its methods (go doc does not list a method through an alias, so they are here):
//
//	Get() (T, bool)   the value, and false when it is null; T's zero when null
//	IsNull() bool     whether the value is null
//	String() string   the value, or "null"
//	GoString() string a form that reads back as the Go that made it
type Null[T any] = wire.Null[T]

// Some returns an Opt that is present and holds v.
func Some[T any](v T) Opt[T] { return wire.Some(v) }

// OptOf returns an Opt that is absent when p is nil and holds a copy of *p otherwise. It is for
// the boundary with a service that speaks *T (Opt.Ptr is the way back); an Input still cannot
// hold a pointer (ADR 0041).
func OptOf[T any](p *T) Opt[T] { return wire.OptOf(p) }

// Date is a calendar date, written YYYY-MM-DD in every section and described as
// `type: string, format: date` (ADR 0042). It has no time of day and no zone, so it has no
// instant: In names the zone when one is needed.
//
// Its methods (go doc does not list a method through an alias, so they are here):
//
//	Year() int, Month() time.Month, Day() int   its parts; the year is 1 to 9999
//	AddDays(n int) (Date, error)                n days later (earlier when negative), or ErrInvalidDate past 0001-01-01 to 9999-12-31
//	Sub(u Date) int                             days from u to d: lateness is today.Sub(due)
//	Before(u Date) bool, After(u Date) bool     whether d is earlier, or later, than u
//	Equal(u Date) bool, Compare(u Date) int     the same day; -1, 0 or +1
//	In(loc *time.Location) time.Time            midnight at its start in loc (UTC when nil)
//	IsZero() bool                               whether it is the zero Date, 0001-01-01
//	String() string                             YYYY-MM-DD
//	MarshalText() ([]byte, error)               YYYY-MM-DD
//	AppendText(b []byte) []byte                 YYYY-MM-DD appended to b
//	UnmarshalText(b []byte) error               reads YYYY-MM-DD, or ErrInvalidDate (on *Date)
type Date = wire.Date

// ErrInvalidDate is the error NewDate, ParseDate and Date.UnmarshalText return. Its text is
// fixed, so it is safe to send to a client from a Check method.
var ErrInvalidDate = wire.ErrInvalidDate

// NewDate returns the Date of a year (1 to 9999), a month and a day, or ErrInvalidDate for a
// day that does not exist. Nothing is normalised.
func NewDate(year int, month time.Month, day int) (Date, error) {
	return wire.NewDate(year, month, day)
}

// DateOf returns the calendar day t falls on in t's own location.
func DateOf(t time.Time) Date { return wire.DateOf(t) }

// ParseDate reads YYYY-MM-DD, or returns ErrInvalidDate.
func ParseDate(s string) (Date, error) { return wire.ParseDate(s) }

// NonNull returns a Null that holds v.
func NonNull[T any](v T) Null[T] { return wire.NonNull(v) }

// NamingPolicy turns a Go field name into a wire name (ADR 0011). One is chosen per app.
type NamingPolicy = wire.Policy

// SnakeCase is the default naming policy: ItemID is item_id.
func SnakeCase(goName string) string { return wire.SnakeCase(goName) }

// CamelCase is the lowerCamel naming policy: ItemID is itemId.
func CamelCase(goName string) string { return wire.CamelCase(goName) }

// Clock reads the time and schedules a function; the app takes one so a timeout is tested by
// moving time forward instead of waiting for it (ADR 0027). The real clock is the default, and
// bitetest.NewClock is the one a test moves.
//
// Its methods (go doc does not list a method through an alias, so they are here):
//
//	Now() time.Time                                the current time
//	AfterFunc(d time.Duration, f func()) Timer     runs f once d has passed; the Timer's Stop() bool cancels it
type Clock = clock.Clock

// The constraint types of ADR 0021. Each is an ordinary string or integer type that decoding
// checks in every section, and the API description states. A value that fails is a 400 naming
// the field, together with the other decoding errors. Any other bound is carried by the
// application's own named type, in a method read at Registration:
//
//	type RoomName string
//	func (RoomName) Length() (min, max int) { return 3, 40 } // characters
//
//	type Guests int
//	func (Guests) Range() (min, max int64) { return 1, 6 }
//
//	type Email string
//	func (e Email) Check() error { ... } // run on each decoded value

// NonEmpty is a string with at least one character.
type NonEmpty = wire.NonEmpty

// PositiveInt is an int greater than zero.
type PositiveInt = wire.PositiveInt

// PositiveInt64 is an int64 greater than zero.
type PositiveInt64 = wire.PositiveInt64

// ServerOnlyError is the error of Socket.Send, of a Topic's publish to its Sockets and of an
// answer's encoding when a tagged union holds a variant its ServerOnly method names (ADR 0083):
// such a variant is never sent to a client. Find it with errors.As. Union is the union's type,
// Variant the tag of the variant that was set, and Field its Go field; Error says all three and
// what to do.
type ServerOnlyError = jsoncodec.ServerOnlyError
