# Input types

**The types a handler's Input is made of: presence types, constraint types, body types, uploads, and the options that bound a body.**

**Guide:** [requests](../guide/requests.md) · **Design:** [request input](../design/request-input.md)

A handler takes exactly one Input value: a struct whose fields named `Path`, `Query`, `Header`, `Cookie` and `Body` are its Sections, and whose other fields are filled by Providers ([providers](./providers.md)). No tag says where a value comes from: the Section it sits in does. A route with no input takes `struct{}`. Every field is a type that says what the request may hold, and a mistake in those types is a panic at Registration, not a failure of the first request ([ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md), [ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

<!-- compiles: decls -->

```go
type ListUsers struct {
	Query struct {
		Page  bites.Opt[bites.PositiveInt]
		Q     bites.Opt[bites.NonEmpty]
		Order bites.Opt[string]
	}
	Header struct{ XRequestID bites.Opt[string] }
}

type UserList struct{ Total int }

func listUsers(ctx context.Context, in ListUsers) (UserList, error) {
	page := in.Query.Page.Or(1)
	return UserList{Total: int(page)}, nil
}
```

## The Sections

The text Sections (`Path`, `Query`, `Header`, `Cookie`) decode each field by its type ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)). A field may be a string, a bool (`true`, `false`, `1`, `0`), a signed or unsigned integer, a float, a `time.Time` (RFC 3339 with an offset, kept as sent), a `uuid.UUID` (the Go 1.27 standard library package `uuid`), a [`Date`](#date), any `encoding.TextUnmarshaler` (a type from another module is registered with [`WithTextFormat`](./app.md#withtextformat)), an enum, or a slice of one of these. A slice is filled from repeated keys (`?tag=a&tag=b`); a comma is not a separator, unless the field is a [`CSV`](#csv). An enum is a named string type with a `Values() []T` method that Registration calls once; decoding refuses a value not listed.

- A plain field is required: a request without it is a 400 that says `required` at its location, unless its Section's [`Defaults`](#defaults-method) names it.
- A repeated key for a field that is not a slice is a 400 `sent more than once`; a key the type does not have is ignored in `Query` unless the route asks for it to be refused ([`RejectUnknownQueryKeys`](#rejectunknownquerykeys)).
- An empty value for a number (`?page=`) is a 400, not zero. A date with no time or offset is a 400 for a `time.Time`; a calendar date is a [`Date`](#date).
- A section has at most 64 fields and 8 lists. A list whose request fills it costs one allocation ([ADR 0003](../adr/0003-the-trade-budget.md)).
- A `TextUnmarshaler` error never reaches the client as written: the client gets `expected a valid value`.
- The built-in types cost no allocation; a `TextUnmarshaler` costs two ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).

A Section's struct may embed other structs by value, `Query struct{ Filter; Paging }`, and their fields are the Section's own, read under their own names; each embedded struct's `WireNames` names its own fields and its `Descriptions` describes them, and the Section's own method wins for a field both name or describe ([WireNames](#wirenames-method), [embedded sections](./openapi.md#descriptions-method)) ([embedded sections](./openapi.md#descriptions-method)). The optional methods of a Section's struct are `WireNames() map[string]string` (names no naming policy produces, checked against the fields), `Descriptions() map[string]string` ([openapi](./openapi.md#descriptions-method)), `Defaults() map[string]string` ([Defaults method](#defaults-method)), and, on the Input itself, `Validate() error` ([errors](./errors.md#invalid)).

An Input may embed a struct that holds only sections, so routes that share a section declare it once: with `type spacePath struct{ Path struct{ ID uuid.UUID } }`, the Input `struct{ spacePath; Body Rename }` reads `in.Path.ID` as if `Path` were its own field, and a mistake is still located at `path.id`. The embedded type may be unexported and may itself embed such structs. An embedded struct that holds no section is a field like any other, the value a Provider gives (`struct{ User; Query Page }` is filled by the Provider of `User`). One that holds sections and anything else, and a section given twice (by two embedded structs, or by one and the Input), are refused, since Go would read one and hide the other. Decision: [ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md).

What Registration refuses in an Input, each a row of the refusal table with the route label first and `registered at file.go:N` last:

| Mistake | Reason in the message |
|---|---|
| a `{id}` in the pattern and no `Path` section | `the pattern has {id}, but Empty has no Path section` |
| a `{id}` and no field for it | `the pattern has {id}, but PathNoField.Path has no field for it` |
| a `Path` field and no wildcard | `ExtraPath.Path.Org has no {org} in the pattern` |
| a `Body` on a GET, HEAD or DELETE route | `BodyOnGet.Body is a Body section, but a GET route has no request body` |
| a field that is not a Section and no Provider covers it | `WithAuth.Auth is not a section (Path, Query, Header, Cookie or Body), and no Provider of string covers this route` |
| an Input that is not a struct | `the handler's input must be a struct whose fields are the sections Path, Query, Header, Cookie and Body, not int` |
| an Input taken by pointer | `the handler takes *bites_test.GetUser, a pointer; an Input is taken by value, because a pointer cannot say whether nil meant absent or null (use Opt[T] or Null[T] for a field)` |
| a pointer field anywhere | `a pointer cannot say whether nil meant absent or null, use Opt[T] or Null[T]` |
| a `Null` in a text Section | `is a Null; text has no null, so the type would promise a state no request can produce: use Opt[T] for a value that may be missing` |
| a map or struct in a text Section | `is a map; text holds one scalar, give it an UnmarshalText method or use a type of its own` |
| an `Opt` or a list in `Path` | `is an Opt, but a path parameter is always present` and `is a list, but a path parameter holds one value` |
| two fields with one wire name | `both have the wire name "page_size"` |
| an unexported field, in a text Section or anywhere in a JSON `Body` (not one tagged `json:"-"`) | `is unexported, so nothing can fill it` |
| an embedded struct that holds sections and another field | `MixedEmbedIn embeds mixedSections, which holds sections and also Me, which is not one: an embedded struct of an Input holds only sections (Path, Query, Header, Cookie, Body); give Me to MixedEmbedIn itself` |
| a section given by an embedded struct and by the Input, or by two embedded structs | `TwiceEmbedIn gets a Path section from both idPathSection and TwiceEmbedIn, and Go would read one and hide the other: keep one` |
| a `Validate` of the wrong type, with a pointer receiver, or on a Section | see [`Invalid`](./errors.md#invalid) |
| a tag another framework reads for a rule or a place (`validate` and `binding` unless the Input has a `Validate`, `path`, `query`, `header`, `cookie`, `form`, `uri`, `param`, `params`, `reqHeader`, `minLength`, `maxLength`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`, `minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`, `pattern`, `format`, `enum`, `required`, `default`), in a Section or anywhere in a JSON `Body` | `tagMinLength.Query.Q has a minLength tag, which bites does not read, so the bound would never be checked: a length is a Length method on the field's type` |
| a `json` tag that renames a field of a Path, Query, Header, Cookie or Form section | `tagJSONQuery.Query.PageSize has a json tag naming it "pageSize", but its wire name is "page_size": a json tag is read in a JSON Body only, so name it with a WireNames method` |
| a `Defaults` entry that names no field, is for a path parameter, an `Opt`, a list or a `CSV`, is a value its type refuses, or sits on a JSON `Body` | see [Defaults method](#defaults-method) |
| an enum whose `Values` returns nothing | `Values returned nothing, so no request could ever be valid` |
| a `Length` that is not a length, a `Range` whose max is below its min or on an integer type other than `int` and `int64` | see [Length method](#length-method) and [Range method](#range-method) |
| a `Range` of the whole of its type, a `Length` of `(0, math.MaxInt)` | `POST /x: WholeRangeBody.Body.N: bites_test.wholeRange.Range returns the whole of int64 and bounds nothing: drop the method`; `POST /x: WholeLengthBody.Body.S: bites_test.wholeLength.Length returns (0, math.MaxInt), which is the whole of a string and bounds nothing: drop the method` |
| a `Check` with a pointer receiver | see [Check method](#check-method) |
| an `Items` that is not a count, or on a type that is not a slice | see [Items method](#items-method) |
| a `Pattern` that is empty, does not compile, uses a construct outside the common subset of RE2 and ECMA-262, is on an enum or on a type that decodes itself, or is on a type that is not a string | see [Pattern method](#pattern-method) |
| a `Format` that returns an empty string, or on an integer or float type, or one that names a checked format on a type that is not a string, an enum or a type that decodes itself | see [Format method](#format-method) |
| a float `Range` with a NaN or infinite bound, whose `max` is below its `min`, that is the whole of its type, that is outside a `float32`'s range, or with the integer signature (and an integer `Range` with the float one) | see [Range method](#range-method) |
| an `ExclusiveMin` or `ExclusiveMax` on an integer type, on a type that is not a float, with another signature, that is NaN, infinite or at the type's limit, that states a side a `Range` already states, or that leaves no value | see [ExclusiveMin and ExclusiveMax methods](#exclusivemin-and-exclusivemax-methods) |
| a `MultipleOf` of zero or below, of 1 on an integer type, on a string or any type that is not an `int`, `int64`, `float32` or `float64`, with another signature, or with a `Range` that holds no multiple of it | see [MultipleOf method](#multipleof-method) |
| a `Unique` that returns false, on a type that is not a slice or on a slice that decodes itself, or on a list whose items are not strings, booleans, numbers or UUIDs | see [Unique method](#unique-method) |
| a `Members` that is not a count, on a type that is not a map, or with another signature (and an `Items` on a map) | see [Members method](#members-method) |
| a `multipleOf`, `uniqueItems`, `minProperties` or `maxProperties` tag | `TagMultipleOf.Body.N has a multipleOf tag, which bites does not read, so the step would never be checked: a step is a MultipleOf method on the field's type` |
| a method named `Length`, `Range`, `Items`, `Pattern`, `Format`, `Check`, `ExclusiveMin`, `ExclusiveMax`, `MultipleOf`, `Unique` or `Members` that is not that rule | see [A method of a rule's name with another meaning](#a-method-of-a-rules-name-with-another-meaning) |

Guide: [requests](../guide/requests.md) and [validation](../guide/validation.md). Decisions: [ADR 0010](../adr/0010-one-input-struct-with-sections-by-field-name.md), [ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md), [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md).

### `CSV`

```go
type CSV[T any] = wire.CSV[T]
```

CSV is a list a client sends as one comma-separated value, `?stage=won,lost`, in a `Query` or a `Header` ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)). It is a slice to the handler: range, `len`, index, and `[]T(in.Query.Stage)` all work. Each element is decoded by `T`'s own text decoding and rules, and a bad element is located as `query.stage[1]` with that rule. `?stage=` is an empty list; an absent key is a 400 `required` unless the field is `Opt[CSV[T]]`; an empty element is decoded as the empty string, not dropped; `?stage=a&stage=b` is a 400 `sent more than once`. A comma cannot be escaped, so an element cannot contain one. In a header the elements are trimmed of spaces around the commas. The API document states `type: array` with the element's schema (a `$ref` for an enum), `minItems` and `maxItems` from an `Items` method, and, for a query, `style: form`, `explode: false`; for a header, `style: simple`. A plain `[]T` is still filled from repeated keys.

A bound is an `Items` method on a named type that repeats the marker: `type Stages bites.CSV[Stage]` with `func (Stages) CommaSeparated() {}` and `func (Stages) Items() (min, max int)`. It is refused in `Path`, `Cookie`, a `Form` and a JSON `Body` (`in JSON a list is an array, so use []T`), and a `CSV` of `Opt` or of a slice is refused. It allocates what the same list of repeated keys allocates: one backing array.

## Presence

Whether a field may be absent or null is said by its type, in every Section ([ADR 0041](../adr/0041-presence-is-a-type.md)). `Opt[Null[T]]` is the PATCH field: absent leaves the stored value, null clears it, a value sets it. In an Output an empty `Opt` is left out of the JSON and a `Null` is written as `null`.

### `Opt`

```go
type Opt[T any] = wire.Opt[T]
```

Opt is a value that may be absent. The zero Opt is absent, so a request that leaves a field out needs no work from the decoder. It is a field type only: an `Opt` as a list element or a map value is refused (`a list element cannot be Opt[string], absence means nothing there`). The one place it is not a field is the Body: an `Opt[T]` Body is a JSON body that may be absent (see [Body types](#body-types)). A `json:",omitempty"` tag does not make a field optional in a request; only `Opt` does. It is comparable when `T` is.

Methods: `Get() (T, bool)` returns the value and whether it was sent; `IsSet() bool`; [`Set(v T)`](#optset), for reflection only; `Or(def T) T` returns the value or `def`; `Ptr() *T` returns nil when the value was not sent and a pointer to a copy of it otherwise; `String()` and `GoString()` for printing. In the API document an `Opt` field is not required.

`Ptr` and `OptOf` exist for the boundary with a service that speaks `*T`. A pointer in an Input is still refused at Registration, because it cannot say whether `nil` meant absent or null; the Input's field stays an `Opt`, and the handler converts when it calls the service. `Ptr` copies, so writing through the pointer changes nothing in the Input.

Decision: [ADR 0041](../adr/0041-presence-is-a-type.md).

### `Some`

```go
func Some[T any](v T) Opt[T]
```

Some returns an Opt that is present and holds `v`.

### `OptOf`

```go
func OptOf[T any](p *T) Opt[T]
```

OptOf returns an Opt that is absent when `p` is nil and present, holding a copy of `*p`, otherwise. A pointer to the zero value is present: `OptOf(&"")` was sent, empty. It is the way back from `Opt.Ptr`, for a handler that receives a `*T` from a service and builds an Output or a call with an `Opt`.

### `Opt.Set`

```go
func (o *Opt[T]) Set(v T)
```

Set makes the Opt present and holding `v`, even when `v` is the zero value. It has a pointer receiver and exists for code that fills an `Opt` through reflection, such as [`modules/config`](./config.md), which cannot call the generic `Some` for a type known only at run time (`reflect.Value.Addr().MethodByName("Set")`). Application code builds an `Opt` with `Some` or `OptOf`; the second spelling is allowed for that one reason ([ADR 0080](../adr/0080-configuration-is-a-module-that-fills-a-typed-struct-from-flags-environment-and-files.md)).

### `Null`

```go
type Null[T any] = wire.Null[T]
```

Null is a value that may be sent as `null`. The zero Null is null, which is also what the decoder leaves for a JSON `null`. A required field sent as `null` is a 400 unless its type is `Null`. A `Null` in `Path`, `Query`, `Header` or `Cookie` is refused, because text has no null. It is comparable when `T` is.

Methods: `Get() (T, bool)` returns the value and false when null; `IsNull() bool`; `String()` prints the value or `null`; `GoString()`.

### `NonNull`

```go
func NonNull[T any](v T) Null[T]
```

NonNull returns a Null that holds `v`. There is no constructor for the null: the zero value is it.

<!-- compiles: decls -->

```go
type PatchUser struct {
	Path struct{ ID int64 }
	Body struct {
		Name  bites.Opt[string]
		Phone bites.Opt[bites.Null[string]]
	}
}

func patchUser(ctx context.Context, in PatchUser) (bites.NoContent, error) {
	if phone, set := in.Body.Phone.Get(); set {
		if v, ok := phone.Get(); ok {
			_ = v // set the phone to v
		} else {
			// clear the phone
		}
	}
	return bites.NoContent{}, nil
}
```

## Dates

A calendar date has no time of day and no zone, so it is not a `time.Time` ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).

### `Date`

```go
type Date = wire.Date
```

Date is a calendar date, read from `YYYY-MM-DD` in a Path, Query, Header, Cookie, `Form` or JSON body and written the same way. The API document says `type: string, format: date`, in requests and in responses. Anything that is not a calendar day (a timestamp, `2026-02-30`, a year outside 1 to 9999, `2026-1-3`) is a 400 with the sentence `expected a date as YYYY-MM-DD` and the rule `type`, which never contains the client's value. A `Date` is a comparable integer value; the zero `Date` is `0001-01-01`. It is decoded where it lies and costs no allocation. It may sit in an `Opt` or a list.

There is no `Time` method on purpose: a date has no instant, and choosing UTC or local midnight changes the day across zones, so the zone is written at the call with `In`. There is no `AddMonths` either: the 31st of January plus a month has no single answer, so that arithmetic goes through `In` and `time.Time.AddDate`, where the choice is visible.

Methods: `Year() int`, `Month() time.Month`, `Day() int`; `In(loc *time.Location) time.Time` returns midnight at the start of the date in `loc` (nil is UTC); `Before(u Date) bool`, `After(u Date) bool`, `Equal(u Date) bool`, `Compare(u Date) int`; `AddDays(n int) (Date, error)` returns the date `n` days later (earlier when negative), and `ErrInvalidDate` for a result outside 0001-01-01 to 9999-12-31, since `n` may come from a client; `Sub(u Date) int` is the number of days from `u` to the date, positive when the date is later, so lateness is `today.Sub(due)`; `IsZero() bool`; `String() string` returns `YYYY-MM-DD`; `MarshalText` and `UnmarshalText`, the second returning `ErrInvalidDate`.

<!-- compiles: decls -->

```go
type ListDue struct {
	Query struct {
		From bites.Opt[bites.Date]
	}
}

func listDue(ctx context.Context, in ListDue) (bites.NoContent, error) {
	if from, ok := in.Query.From.Get(); ok {
		_ = from.In(time.UTC) // midnight UTC of that day
	}
	return bites.NoContent{}, nil
}
```

### `NewDate`

```go
func NewDate(year int, month time.Month, day int) (Date, error)
```

NewDate returns the Date of a year (1 to 9999), a month and a day, or `ErrInvalidDate` for a day that does not exist. Nothing is normalised: `NewDate(2026, 2, 30)` is an error, where `time.Date` would answer 2 March.

### `DateOf`

```go
func DateOf(t time.Time) Date
```

DateOf returns the calendar day `t` falls on in `t`'s own location. A time in Jakarta at 01:00 on the 3rd is the 3rd, though it is the 2nd in UTC. A year outside 1 to 9999 gives the zero Date.

### `ParseDate`

```go
func ParseDate(s string) (Date, error)
```

ParseDate reads `YYYY-MM-DD`, or returns `ErrInvalidDate`.

### `ErrInvalidDate`

```go
var ErrInvalidDate = wire.ErrInvalidDate
```

ErrInvalidDate is the error `NewDate`, `ParseDate` and `Date.UnmarshalText` return. Its text, `expected a date as YYYY-MM-DD`, is fixed, so a [`Check`](#check-method) may return it and it reaches the client without the client's value.

## Constraint types

The constraint types, and the application's own named types that carry a `Length`, `Range`, `Items`, `Pattern` or `Check` method, are ordinary string, integer and slice types that decoding checks in every place their base type decodes (path, query, header, cookie, JSON body, inside `Opt`, `Null` and lists), and that the API document states as `minLength`, `maxLength`, `minimum`, `maximum`, `minItems`, `maxItems` and `pattern` for a request (and `format` for a `Format` method). A handler converts them (`string(in.Body.Name)`) and nothing is allocated for them. A value that fails is a 400 at its wire location with a fixed sentence that never holds the client's value. They are not enforced on output. A constraint type as a JSON map key is refused (`a map key cannot be wire.NonEmpty: a key is not checked against a constraint, so the type would promise what nothing enforces`).

Guide: [validation](../guide/validation.md). Decision: [ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md). A typed JSON request with constraint types, `Length`, `Range` and `Check` types and a passing `Validate` is still 2 allocations, and `Items`, `Pattern`, `Format` and a `uuid.UUID` add none to what a plain list, string and `uuid.UUID` cost ([ADR 0003](../adr/0003-the-trade-budget.md)).

### `NonEmpty`

```go
type NonEmpty = wire.NonEmpty
```

NonEmpty is a string with at least one character. A failure answers `must not be empty`.

### `PositiveInt`

```go
type PositiveInt = wire.PositiveInt
```

PositiveInt is an `int` greater than zero. A failure answers `expected a positive integer`; a non-number keeps the base type's sentence (`expected an integer`).

### `PositiveInt64`

```go
type PositiveInt64 = wire.PositiveInt64
```

PositiveInt64 is an `int64` greater than zero, answered like `PositiveInt`.

### Length method

A named string type with the method `Length() (min, max int)` is a string whose length in characters (Unicode code points, as JSON Schema counts) lies within `min` and `max`. Registration calls it once on a zero receiver, as it calls [`Values`](#values-method); the API document states `minLength` and `maxLength`. A failure answers `must be between 3 and 40 characters` (`must be at most 9 characters` when `min` is 0, `must be exactly 4 characters` when they are equal, `must be at least 12 characters` with no `max` member and no `maxLength` when `max` is `math.MaxInt`, which is "no upper bound"; a `Length` of `(0, math.MaxInt)` bounds nothing and is refused). A string longer in bytes than the most characters the bound allows is refused without being counted. `min` must be at least 0 and `max` at least `min` and 1:

```text
GET /x: BadLengthQuery.Query.Name: bites_test.badLength.Length returns (5, 2), which is not a length: it must be 0 <= min <= max with max at least 1
```

<!-- compiles: decls -->

```go
type TagName string

func (TagName) Length() (min, max int) { return 1, 64 }

type CreateTag struct {
	Body struct{ Name TagName }
}
```

### Range method

A named `int` or `int64` type with the method `Range() (min, max int64)` is an integer between `min` and `max`, both included. It is read once at Registration; the API document states `minimum` and `maximum`. A failure answers `expected an integer between 1 and 6`, and a non-number keeps the base type's sentence (`expected an integer`). **A bound equal to the type's own limit is not a bound** ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)): `Range() (0, math.MaxInt64)` on an `int64` type, or `(0, math.MaxInt)` on an `int` type, is "at least 0", so the document states `minimum` only and the failure answers `expected an integer of at least 0` with no `max` member (a `min` of `math.MinInt64` or `math.MinInt` is dropped the same way: `expected an integer of at most 10`). The limit is the declaring type's, so `math.MaxInt32` on an `int` type is a real bound on a 64-bit platform and is stated as one. A `Range` of the whole type bounds nothing and is refused. A `Range` whose `max` is below `min`, or on another integer type, is refused.

A named `float32` or `float64` type with `Range() (min, max float64)` is a number between `min` and `max`, both included (for a bound that excludes its value, see [ExclusiveMin and ExclusiveMax methods](#exclusivemin-and-exclusivemax-methods), and for a step, [MultipleOf method](#multipleof-method)), checked on every value decoded into it in every Section, in a JSON body, inside `Opt` and lists; the document states `minimum` and `maximum` for a request. A failure is the rule `range` with `min` and `max` as JSON numbers and the sentence `expected a number between 0.01 and 1000000` (`expected a number of at least 0` or `expected a number of at most 9.5` for a side that is not a bound). `-math.MaxFloat64` and `math.MaxFloat64` (`math.MaxFloat32` on a `float32` type) are "not a bound", as the integer limits are. A `float32` is compared as a `float32`, so `0.1` includes the `float32` nearest to it. A NaN or infinite bound, a `max` below the `min`, a `Range` of the whole type, a bound outside a `float32`, a float `Range` with integer bounds and an integer `Range` with float bounds are refused at Registration. A decimal type from another module (registered with [`WithTextFormat`](./app.md#withtextformat)) bounds its value with a [`Check`](#check-method), not a `Range`. Examples of the refusals:

```text
POST /x: NaNRangeBody.Body.F: bites_test.nanRange.Range returns (NaN, 1), and a bound must be a finite number: use -math.MaxFloat64 or math.MaxFloat64 for no bound on a side
POST /x: IntRangeOnFloatBody.Body.F: bites_test.intRangeOnFloat has a Range method that returns int64 bounds, but a float type's bound is float64: name it Range, func() (min, max float64), or make bites_test.intRangeOnFloat an int64 type
```

An integer `Range` that is refused:

```text
POST /x: NarrowRangeBody.Body.N: bites_test.narrowRange has a Range, which is read on an int or int64 type, and bites_test.narrowRange is a int32
```

<!-- compiles: decls -->

```go
type Guests int

func (Guests) Range() (min, max int64) { return 1, 6 }

type Amount float64

func (Amount) Range() (min, max float64) { return 0.01, 1000000 }

type Book struct {
	Query struct{ Guests Guests }
	Body  struct{ Price Amount }
}
```

### Check method

A named type with the method `Check() error` checks its own value: decoding runs it on every value of the type it stores, in every Section, inside `Opt` and lists, and an error is a 400 at the value's wire location whose message is the error's text, listed with the other decoding errors. The message is the application's: it is sent as written, so a `Check` that wants to keep the rule that a 400 never holds the client's value does not put the value in it. This includes the standard library's errors: `time.Parse` and `strconv` quote the input in their text, so `return err` from one of them sends the client its own value back. Return a fixed error of your own (or `ErrInvalidDate` for a date). A request pays one call per value and no allocation while `Check` returns nil (measured on a typed JSON request: 2 allocations, as without it). `Check` runs before `Validate`, which still checks rules across fields. It must have a value receiver, because the value is checked where it was decoded and must not be kept:

```text
POST /x: PtrCheckBody.Body.Mail: bites_test.ptrCheck.Check has a pointer receiver, but a value is checked as it was decoded and must not be kept: declare it on ptrCheck
```

The API document cannot state what `Check` checks; a [`Descriptions`](./openapi.md#descriptions-method) entry can say it in words.

<!-- compiles: decls -->

```go
type Email string

var errNotEmail = errors.New("must be an email address")

func (e Email) Check() error {
	if i := strings.IndexByte(string(e), '@'); i < 1 || i == len(e)-1 {
		return errNotEmail
	}
	return nil
}

type Invite struct {
	Body struct {
		To []Email
		Cc bites.Opt[Email]
	}
}
```

### Items method

A named slice type with the method `Items() (min, max int)` is a list of between `min` and `max` items, both included. It is read once at Registration on a zero receiver, as [`Length`](#length-method) is; the API document states `minItems` and `maxItems`. It is checked while the list is decoded: in a JSON body an item past `max` is skipped without being stored, and in a text Section (a list of repeated keys, in `Query`, `Header`, `Cookie` or a `Form`) the values are counted before the list is allocated. A failure is one entry at the list's location, whatever it held, with the rule `items` and `min` and `max`: `must have between 1 and 8 items` (`must have at most 8 items` when `min` is 0, `must have exactly 2 items` when they are equal). `min` must be at least 0 and `max` at least `min` and 1. A method of the signature `func() (min, max int)` on a type that is not a slice, or on a slice that decodes itself (`[]byte`, a `TextUnmarshaler`), is refused, because nothing would count the items:

```text
GET /x: ItemsOnStringQuery.Query.S: bites_test.itemsOnString has an Items method, but Items bounds a list: it is read on a named slice type, and bites_test.itemsOnString is a string
```

<!-- compiles: decls -->

```go
type Labels []string

func (Labels) Items() (min, max int) { return 1, 8 }

type Label struct {
	Body struct{ Names Labels }
}
```

### Pattern method

A named string type with the method `Pattern() string` is a string that matches a regular expression. It is read once at Registration on a zero receiver, compiled once with Go's `regexp`, and checked on each value decoded into the type, in every Section, inside `Opt` and lists, after the type's `Length` when it has one; the API document states `pattern`. The match is unanchored, as Go's `MatchString` and JSON Schema's `pattern` both are, so write `^` and `$` when the whole value must match. A value that fails is one entry at its location, `must match the pattern`, with the rule `pattern` and no `min`, `max` or pattern member: the sentence holds neither the client's value nor the expression, which the document already gives the client.

The document's reader runs the expression as ECMA-262, so Registration refuses what the two read differently ([`docs/research/pattern-dialect/`](../research/pattern-dialect/report.md)). Allowed: literals; classes of literals, ranges and `\d \D \w \W`; the escapes `\d \D \w \W \b \B \t \n \r \f \v \xHH`, `\p{X}` and `\P{X}` of a Unicode general category (`L`, `Lu`, `Nd`, `P`, ...), and a backslash before one of `^$\.*+?()[]{}|/` (and `-` in a class); the groups `(...)`, `(?:...)` and `(?<name>...)`; the quantifiers `* + ? {n} {n,} {n,m}`, each with an optional lazy `?`; `|`, `^` and `$`. Refused, each naming the construct: the flag groups `(?i)`, `(?s)`, `(?m)`, `(?U)` and `(?i:...)`; `(?P<name>...)`; lookaround; `\A`, `\z`, `\Q...\E`, `\x{...}`, `\a`, `\C` and escapes of other characters (`\_`, `\-` outside a class); a backreference; a POSIX class `[[:alpha:]]`; `\pL` and `\p{Greek}`; `\s` and `\S` (write `[ \t\n\r\f]`) and `.` (write `[^\n\r]`), which the two engines read as different sets; a lone `{`, `}` or `]`, and a `]` first in a class (write `\]`); a quantified `^`, `$` or `\b`; a class escape as the end of a range; `\b` in a class. An empty pattern, one that does not compile (the `regexp` error is quoted), a `Pattern` on an enum or on a type that decodes itself, and a `Pattern` of the signature `func() string` on a type that is not a string are refused too, and a `Pattern` with another signature on a string type, as for the other rules (below). Rows of the refusal table:

```text
POST /x: DotPatternBody.Body.S: bites_test.dotPattern.Pattern "^.+$" uses ., which RE2 reads as any character but \n and ECMA-262 as any but \n, \r, U+2028 and U+2029: write the class, as [^\n\r]
POST /x: EmptyPatternBody.Body.S: bites_test.emptyPattern.Pattern returns an empty string, which every value matches: remove the method or write a pattern
POST /x: IntPatternBody.Body.S: bites_test.intPattern has a Pattern method, but Pattern is read on a string type, and bites_test.intPattern is a int32
```

<!-- compiles: decls -->

```go
type Slug string

func (Slug) Length() (min, max int) { return 3, 40 }
func (Slug) Pattern() string        { return `^[a-z0-9]+(-[a-z0-9]+)*$` }
```

### Format method

A named string type with the method `Format() string` names the JSON Schema `format` of its field in the API document, in a request schema and in a response schema, and is read once at Registration on a zero receiver. **A name from this list is also checked** on every value decoded into the type, in every Section, inside `Opt` and lists, after the `Length` and the `Pattern` when it has them ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)): `email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time` and `uuid`. A value that is not of the format is a 400 at its location with the rule `format`, a `"format"` member naming it, and the sentence `must be an email address`, `must be an absolute URI`, `must be a host name`, `must be an IPv4 address`, `must be an IPv6 address`, `must be a date (YYYY-MM-DD)`, `must be a date and time (RFC 3339)` or `must be a UUID`; the client's value is never echoed. The definitions are in the ADR: `email` is one `@`, a local part of atext and dots up to 64 bytes and a host name of at least two labels, 254 bytes in all, with no quoted local part, display name or IP literal; `uri` is an absolute RFC 3986 URI of ASCII with only `%HH` escapes; `hostname` is RFC 1123 labels with no trailing dot; `ipv6` takes no zone; `date-time` takes a capital `T` and `Z` and an offset up to 23:59. A checked format is enforced on a request only; a response states it and does not check it. Any other name (a name of your own) stays an annotation that changes nothing a request does. An empty name is refused, wherever the type is described: a type that a handler returns is refused at its route as one that it accepts is. It is refused too, rather than allowed to override, on an integer or float type, where the document already states `int32`, `int64`, `float` or `double`; a name from the list on a type that is not a string, on an enum, or on a type that decodes itself is refused because a value of it could not be checked; a `Format` of another name on any other kind of type (`time.Time`'s takes a layout) is left alone. `uuid.UUID` is already `format: uuid`.

```text
POST /x: EmptyFormatBody.Body.S: bites_test.emptyFormat.Format returns an empty string, which names no format: remove the method or name one
```

<!-- compiles: decls -->

```go
type Address string

func (Address) Format() string { return "email" }
```

### ExclusiveMin and ExclusiveMax methods

A named `float32` or `float64` type with `ExclusiveMin() float64` is a number strictly above the value, and with `ExclusiveMax() float64` strictly below it; the value itself is refused ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)). Each is optional and read once at Registration on a zero receiver, and checked on every value decoded into the type, in every Section, in a JSON body, inside `Opt` and lists. A price that must be above 0 is `ExclusiveMin` and nothing else. **A side is stated once:** [`Range`](#range-method) bounds a side inclusively and these bound it exclusively, so a type with a `Range` that bounds the minimum and an `ExclusiveMin` is refused, and to bound one side each way the `Range` leaves the exclusive side at the type's limit (`-math.MaxFloat64` for the minimum, `math.MaxFloat64` for the maximum). There is no exclusive bound on an integer type, which is refused with the message that says to write `Range(n+1, ...)`.

A failure is the rule `range`, with the sentence `expected a number greater than 0`, `expected a number less than 1`, `expected a number greater than 0 and at most 1000`, `expected a number at least 1 and less than 10` or `expected a number greater than 0 and less than 1`, and the members `min` and `max` for an inclusive side and `exclusive_min` and `exclusive_max` for an exclusive one, as JSON numbers. The API document states `exclusiveMinimum` and `exclusiveMaximum` as numbers, as OpenAPI 3.1 does. A `float32` is compared as the nearest `float32`. Refused at Registration: either method on an integer type, a string type or any type that is not a float, with another signature, returning NaN, an infinity or the type's own limit (not a bound), stating a side a `Range` states, or leaving no value between the minimum and the maximum:

```text
POST /x: ExBothMinBody.Body.N: bites_test.exBothMin has ExclusiveMin and a Range that already bounds the minimum (1): a side is stated once, so leave Range's min at -math.MaxFloat64 and keep the exclusive bound, or drop ExclusiveMin
```

<!-- compiles: decls -->

```go
type Price float64

func (Price) ExclusiveMin() float64 { return 0 }

// Capped is above 0 and at most 1000.
type Capped float64

func (Capped) Range() (min, max float64) { return -math.MaxFloat64, 1000 }
func (Capped) ExclusiveMin() float64     { return 0 }

type Quote struct {
	Body struct {
		Price Price
		Fee   Capped
	}
}
```

### MultipleOf method

A named `int` or `int64` type with `MultipleOf() int64`, or a named `float32` or `float64` type with `MultipleOf() float64`, is a number that is a whole multiple of the step ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)). It is read once at Registration, checked in every Section, in a JSON body, inside `Opt` and lists, and stated in the API document as `multipleOf`. It composes with `Range` (the bounds are asked first: a value out of range is told it is out of range). An integer is checked with `v % step == 0`. **A float is checked by division with a tolerance:** `v / step` may differ from the nearest whole number by a few units in the last place, so `0.3` is a multiple of `0.1` although `0.3 / 0.1` is `2.9999999999999996`; zero is a multiple of every step and only zero is within reach of the whole number 0. Above 2^50 steps every float passes. For money that must be exact, store whole cents in an `int64` with `MultipleOf` and `Range`.

A failure is the rule `multiple` with the member `multiple`, the step as a JSON number, and the sentence `expected a multiple of 5` or `expected a multiple of 0.01`; the sentence never holds the client's value. Refused at Registration: a step of zero or below, a step of 1 on an integer type (every integer is a multiple, so it bounds nothing), `MultipleOf` on a string type, on an integer type other than `int` and `int64` or on any type that is not a number, with another signature, and with a `Range` that holds no multiple of the step:

```text
POST /x: MulNoneInRangeBody.Body.N: bites_test.mulNoneInRange.Range returns (6, 9) and MultipleOf returns 5, and no integer between the two is a multiple of 5, so no value could be valid
```

<!-- compiles: decls -->

```go
type Cents float64

func (Cents) MultipleOf() float64 { return 0.01 }

type PageSize int

func (PageSize) Range() (min, max int64) { return 10, 100 }
func (PageSize) MultipleOf() int64       { return 10 }

type Charge struct {
	Query struct{ Size PageSize }
	Body  struct{ Amount Cents }
}
```

### Unique method

A named slice type with `Unique() bool` that returns `true` is a list whose items are all different ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)). It is read once at Registration, and checked after the list is decoded wherever a list is: a JSON array, repeated keys of a `Query`, `Header`, `Cookie` or `Form`, and a [`CSV`](#csv). It composes with [`Items`](#items-method) on the same type. The items must be of a kind whose equality is plain: a string (compared byte for byte, so `a` and `A` differ), an integer, a boolean, a float (compared by value, so `1` and `1.0` are one number) or a `uuid.UUID`. A list of structs, lists, maps or `Opt` is refused, and the rule that two such items are the same is the Input's [`Validate`](./errors.md#invalid).

A failure is one entry at the first item that repeats an earlier one (`body.tags[3]`), with the rule `unique` and the sentence `repeats an earlier item`, which never holds the client's value. The check is made once every item has decoded and the length is within `Items`. A list of up to 16 items is compared pairwise and allocates nothing; a longer one goes through a set sized to it. The document states `uniqueItems: true`. Refused at Registration: a `Unique` that returns `false` (it states nothing), on a type that is not a slice (a map, a string, an integer), on a slice that decodes itself (`[]byte`, a `TextUnmarshaler`), with another signature on a slice, and on a slice whose items are not a kind above:

```text
POST /x: UniqueOnMapBody.Body.M: bites_test.uniqueOnMap has a Unique method, but Unique says a list holds no item twice: it is read on a named slice type, and bites_test.uniqueOnMap is a map
```

<!-- compiles: decls -->

```go
type Handles []string

func (Handles) Items() (min, max int) { return 1, 20 }
func (Handles) Unique() bool          { return true }

type Invite2 struct {
	Body struct{ Handles Handles }
}
```

### Members method

A named map type with `Members() (min, max int)` is an object of between `min` and `max` members, both included ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)). It mirrors [`Items`](#items-method): read once at Registration on a zero receiver, and counted while the object is decoded, so a member past `max` has its value skipped without being stored and a hostile object is never held larger than its bound. A failure is one entry at the map's location, with the rule `members`, `min` and `max`, and the sentence `must have between 1 and 20 members` (`at most 20` when `min` is 0, `exactly 2` when they are equal). The API document states `minProperties` and `maxProperties`. `min` must be at least 0 and `max` at least `min` and 1. Refused at Registration: a `Members` that is not a count, on a type that is not a map (a list's length is `Items`), with another signature on a map, and an `Items` on a map:

```text
POST /x: MembersOnListBody.Body.L: bites_test.membersOnList has a Members method, but Members bounds a map and bites_test.membersOnList is a list: a list's length is its Items method, func() (min, max int)
```

<!-- compiles: decls -->

```go
type Labels2 map[string]string

func (Labels2) Members() (min, max int) { return 1, 20 }

type Tagging struct {
	Body struct{ Labels Labels2 }
}
```

### A method of a rule's name with another meaning

On a string or integer type, the names `Length` and `Range`, on an integer or float type `MultipleOf`, on a float type `ExclusiveMin` and `ExclusiveMax`, on a slice type `Items` and `Unique`, on a map type `Members`, on a string type `Format` and `Pattern`, and on any decoded type the name `Check`, mean the rules above. A method of one of those names with another signature, a `Range` on a string type and a `Length` on an integer type are refused at Registration rather than ignored, so a domain type whose `Length` counts something else is renamed rather than leave the reader to guess which one decoding obeys:

```text
POST /x: CountLengthBody.Body.Name: bites_test.lengthIsACount has a Length method, so it carries a bound, but Length must be func() (min, max int): rename the method if it means something else
```

## Body types

The type of the `Body` Section decides how a request body is read ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)):

| `Body` is | Accepts | Read as |
|---|---|---|
| a struct, slice or map | `application/json` or any `*/*+json` | JSON |
| `Opt[T]` of a struct, slice or map | the same | JSON, or nothing: a request with no body leaves the `Opt` unset |
| `Raw[T]` | the same | JSON, with the exact bytes kept |
| `[]byte` | any | the bytes as sent |
| `Form[T]` | `application/x-www-form-urlencoded` | form fields, decoded like a Query section |
| `Multipart[T]` | `multipart/form-data` | fields and files decoded into `T` before the handler runs |
| `Upload` | `multipart/form-data` | a stream of parts, read by the handler |

A body whose `Content-Type` the route does not accept is a 415, a JSON body sent with none included. An empty `[]byte` body is allowed; an empty JSON or `Raw[T]` body is a 400 `required` at `body`, unless the Body is an `Opt[T]`. A body over `MaxBody` is a 413 and one that arrives too slowly is a 408. A JSON body with a member the type does not have is a 400 naming it (`unknown field`), unless the route has `AcceptUnknownMembers`. The decoder reports every error, not the first, up to 20, and nesting over `Limits.MaxDepth` is a 400 ([ADR 0015](../adr/0015-json-is-our-own-codec-on-jsontext.md)). A body that is a string, a pointer, or holds an interface is refused at Registration (`BodyString.Body is string; a Body is a struct, slice or map (read as JSON), []byte, Raw[T], Form[T], Multipart[T], Submitted[T] or Upload`).

**An optional Body.** `Body bites.Opt[T]`, with `T` a struct, slice or map, is a JSON body that may be absent. A request with no body (no `Content-Length` above 0 and not chunked) leaves the `Opt` unset, and the handler reads `b, ok := in.Body.Get()`. A request with a body has it read exactly as a plain JSON body: the same 415 for a type that is not JSON, and a required field inside is still required (`{}` is a 400 naming `body.note` when `Note` is required). A body of `{}` is a sent body, not an absent one. In the API document the route's `requestBody.required` is false; it stays true for every other body. It costs nothing on the guarded axes ([ADR 0003](../adr/0003-the-trade-budget.md)). Refused at Registration:

| Mistake | Reason in the message |
|---|---|
| an `Opt` Body of a scalar | `POST /x: BodyOpt.Body is Opt[string]; an Opt Body is a JSON body that may be absent, so it holds a struct, slice or map` |
| an `Opt` Body of a `Raw`, `Form`, `Multipart`, `Submitted`, `Upload` or `[]byte` | `POST /x: BodyOptRaw.Body is Opt[Raw[struct { N int }]]; an Opt Body is a JSON body that may be absent, and a Raw[struct { N int }] is a Body of its own: give it without Opt` |

Allocations: a typed JSON POST is 2; a body that did not all arrive with its head, or a chunked one, is 3; a `Form[T]` POST is 2; a `Multipart[T]` POST is 3, plus 2 for each file kept in memory ([ADR 0003](../adr/0003-the-trade-budget.md)).

Guide: [requests](../guide/requests.md) and [forms and uploads](../guide/forms-and-uploads.md).

### `Form`

```go
type Form[T any] struct {
	Value T
}
```

Form is a Body read as `application/x-www-form-urlencoded`. `T` is a struct whose fields are decoded exactly like a Query section: the same field types, repeated keys for a list, `Opt` for a field that may be missing, the constraint types, and wire names from the naming policy or `WireNames`. The handler reads `in.Body.Value.Name`. A mistake is located as `body.name` and a list element as `body.tags[1]`. A field the type does not have is a 400 `unknown field` at `body.<name>` (unlike Query), unless the route has `AcceptUnknownMembers`. A form with more fields than `Limits.MaxFields` is a 413.

Refused at Registration: a `Form` of a non-struct (`FormOfInt.Body is a Form of int; a Form wraps a struct whose fields are the form fields`), of an `Opt` or `Null`, a `Null` or nested struct field, a pointer to a `Form`, and a `Form` on a GET, HEAD or DELETE route.

### `Submitted`

```go
type Submitted[T any] struct {
	// Has unexported fields.
}
```

Submitted is a Body read as an HTML form, `application/x-www-form-urlencoded`, decoded exactly as a [`Form`](#form), whose mistakes are handed to the handler instead of answered as a 400: the handler runs and shows the form again as a [`Page`](./responses.md#page), with each mistake next to its field and what the person typed still in it. The mistakes are those of decoding the form and those the Input's `Validate` returns at a `body` location; `Validate` runs only when decoding found none. A mistake in the path, the query, a header or a cookie is still the 400 problem+json, listing the form's mistakes too, and so are a body over the limit (413), a form over `Limits.MaxFields` (413) and another media type (415). The zero value holds no form: `Valid` reports false, with no problems and no text.

Refused at Registration: a route whose handler returns no `Page` (alone, in a OneOf or among its `Returns`): `the Body is a Submitted form, whose mistakes the handler shows itself, but the handler returns struct {} and no Page to show the form in: return a Page (or a OneOf with one, such as OneOf2[SeeOther, Page]), or read the body as Form[T] and let the framework answer a bad form with the 400`; a Submitted of a non-struct (`SubmittedOfInt.Body is a Submitted of int; a Submitted wraps a struct whose fields are the form fields`), and the other refusals of a `Form`. In the API document its request body is the form's, and the route states no 400 unless another section can answer one. Allocations: a valid three-field Submitted POST is 5, a Form's 2 and 3 for the copy of the form it keeps ([ADR 0003](../adr/0003-the-trade-budget.md)). Decision: [ADR 0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md). Guide: [forms and uploads](../guide/forms-and-uploads.md#showing-a-form-again-with-its-mistakes).

### `Submitted.Valid`

```go
func (s Submitted[T]) Valid() (T, bool)
```

Valid returns the decoded form and true when it has no problem. On false the value holds what could be decoded, and the page is shown again from `Problem` and `Sent`.

### `Submitted.Problems`

```go
func (s Submitted[T]) Problems() []FieldError
```

Problems returns every problem of the form, in the order found, as the [`FieldError`](./errors.md#fielderror)s a 400 lists: the locations are `body.name`. At most 20 are kept from decoding, the number a 400 lists; `Add` appends past them. A page lists them at the top of the form too, since an unknown field (a submit button's name the type does not declare) has no field of its own to show its problem beside.

### `Submitted.Problem`

```go
func (s Submitted[T]) Problem(name string) string
```

Problem returns the message of the first problem of the field with the wire name `name` (`"capacity"`, not `"body.capacity"`), or `""` when it has none, for a template: `{{.Form.Problem "capacity"}}`.

### `Submitted.Sent`

```go
func (s Submitted[T]) Sent(name string) string
```

Sent returns the first value sent for the field with the wire name `name`, percent-decoded, or `""` when none was sent: what the person typed, to put back in the field, even when it could not be decoded (`"abc"` for a number) or had a bad escape (kept as sent). Write it through `html/template`, which escapes it. The text is a copy the handler may keep past the request.

### `Submitted.SentAll`

```go
func (s Submitted[T]) SentAll(name string) []string
```

SentAll returns every value sent for the field with the wire name `name`, in order (the boxes ticked of a list), or nil when none was sent.

### `Submitted.Add`

```go
func (s *Submitted[T]) Add(name, message string)
```

Add records a problem the handler found itself on the field with the wire name `name`, such as `"is booked on that date"`; `Valid` then reports false and `Problem` returns the message. It is how a handler that checks the form against its data shows the form again with that mistake. Copies of a Submitted share their problems.

### `Raw`

```go
type Raw[T any] struct {
	Value T
	// Has unexported fields.
}
```

Raw is a Body that keeps the exact bytes beside the decoded value, for a webhook whose signature covers the bytes as sent: re-encoding `Value` does not reproduce them. It requires a JSON content type, like a struct body. The handler checks `Bytes()` against the signature with the secret its struct holds. As the message type of a WebSocket route it keeps the message's bytes ([websocket](./websocket.md#inbox)).

### `Raw.Bytes`

```go
func (r Raw[T]) Bytes() []byte
```

Bytes is the request body exactly as it arrived. It is not modified afterwards and not reused for another request, so it may be kept ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)).

<!-- compiles: decls -->

```go
type Webhook struct {
	Header struct{ XSignature bites.Opt[string] } // Opt, so a request without it is the handler's 401, not a 400
	Body   bites.Raw[struct{ Event string }]
}

type Hooks struct{ Secret []byte }

func (h Hooks) Receive(ctx context.Context, in Webhook) (bites.NoContent, error) {
	sig, _ := in.Header.XSignature.Get()
	mac := hmac.New(sha256.New, h.Secret)
	mac.Write(in.Body.Bytes())
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return bites.NoContent{}, bites.Unauthorized("bad signature")
	}
	return bites.NoContent{}, nil
}
```

## Uploads

### `Upload`

```go
type Upload struct {
	// Has unexported fields.
}
```

Upload is a Body whose handler reads a `multipart/form-data` request part by part while it arrives. Nothing is buffered whole: the handler asks for the next part, reads it as far as it wants, and asks again, so a 2 GiB upload costs the handler's own choices and a 32 KiB window. The other Sections are decoded and `Validate` has run before the handler is called; the body has not been read, so a request the Input refuses never gets a `100 Continue`.

An Upload and every Part from it are valid only while the handler runs; after that `Next` and `Read` fail with [`ErrUploadClosed`](#erruploadclosed). A handler that returns before the last part leaves a body unread, and the connection is closed after the answer.

Limits: an Upload route must be given [`MaxBody`](#maxbody) on the route or a group above it; `MaxBody` set only through `WithLimits` does not count. `BodyTimeout` defaults to `max(30 s, MaxBody / 128 KiB/s)`. [`MaxInFlight`](#maxinflight) bounds the uploads in flight and [`MaxParts`](#maxparts) the parts of one (128 by default). A part header over 8 KiB or with more than 16 fields is a 431, and a boundary over 70 bytes is a 400.

Refused at Registration:

| Mistake | Reason in the message |
|---|---|
| no `MaxBody` on the route or a group above | `UploadIn.Body is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it` |
| an Upload on a GET, HEAD or DELETE route | `UploadIn.Body is a Body section, but a GET route has no request body` |
| an Upload as a field of a JSON body, or a list of them | `UploadInJSON.Body holds an Upload; an Upload is the whole Body, read part by part, not a field of a JSON body` |
| an Upload in a text Section | `UploadInQuery.Query.File: bites.Upload is a struct; text holds one scalar, ...` |

Allocations: 3 a request, plus 1 a part ([ADR 0003](../adr/0003-the-trade-budget.md)).

Guide: [forms and uploads](../guide/forms-and-uploads.md). Decision: [ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md).

### `Upload.Next`

```go
func (u Upload) Next() (Part, error)
```

Next moves to the next part, skipping what is left of the current one, and returns it. It returns `io.EOF` after the last part. For a body that is refused (too large, too slow, cut short, malformed) it returns a `*StatusError` with the status the client should see (400, 408, 413 or 431), so returning it answers correctly; the connection is closed after the answer. After the handler returned it returns `ErrUploadClosed`.

<!-- compiles: decls -->

```go
type Avatar struct {
	Path struct{ ID int64 }
	Body bites.Upload
}

func setAvatar(ctx context.Context, in Avatar) (bites.NoContent, error) {
	for {
		part, err := in.Body.Next()
		if err == io.EOF {
			return bites.NoContent{}, nil
		}
		if err != nil {
			return bites.NoContent{}, err
		}
		if part.Name != "avatar" {
			continue
		}
		if _, err := io.Copy(io.Discard, part); err != nil {
			return bites.NoContent{}, err
		}
	}
}
```

### `Upload.Each`

```go
func (u Upload) Each(fn func(Part) error) error
```

Each calls `fn` with each part in turn and returns nil after the last: it is the loop over `Next` written once. What `fn` leaves unread of a part is skipped, so `fn` returns nil to pass a part by. The first error stops it and is returned as it is, whether `fn` returned it or reading the upload did (the `*StatusError` `Next` returns), so a handler returns it directly; the parts after it are not read, and the connection is closed after the answer. The `Part` given to `fn` is valid until `fn` returns, and after the handler returned Each returns `ErrUploadClosed` without calling `fn`.

<!-- compiles: decls -->

```go
func setAvatarWithEach(ctx context.Context, in Avatar) (bites.NoContent, error) {
	return bites.NoContent{}, in.Body.Each(func(part bites.Part) error {
		if part.Name != "avatar" {
			return nil
		}
		_, err := io.Copy(io.Discard, part)
		return err
	})
}
```

Allocations: none beyond the loop over `Next` it replaces, a closure that captures the handler's variables included.

### `Part`

```go
type Part struct {
	Name        string
	FileName    string
	ContentType string

	// Has unexported fields.
}
```

Part is one part of an Upload. `Name`, `FileName` and `ContentType` are what the client sent, as data. `FileName` in particular is never a path: it may be `../../etc/passwd` or a Windows path, and the framework never opens it. A handler that stores a file under a name of its own choosing is safe; one that joins `FileName` to a directory has chosen to trust the client (`os.Root` refuses it anyway). `FileName` is empty for a plain form field. A Part is an `io.Reader`; it is valid until `Next` is called again.

### `Part.Read`

```go
func (p Part) Read(b []byte) (int, error)
```

Read reads the part's body and returns `io.EOF` at its end. A Read after `Next` moved on fails with `ErrPartClosed`; after the handler returned, with `ErrUploadClosed`. A refused body returns a `*StatusError` as `Next` does.

### `ErrUploadClosed`

```go
var ErrUploadClosed = engine.ErrUploadClosed
```

ErrUploadClosed is returned by an Upload or a Part used after its handler returned, so a goroutine that kept one cannot read another request's bytes ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). Test with `errors.Is(err, bites.ErrUploadClosed)`.

### `ErrPartClosed`

```go
var ErrPartClosed = engine.ErrPartGone
```

ErrPartClosed is returned by a Part read after `Next` moved the upload on. Test with `errors.Is(err, bites.ErrPartClosed)`.

## Multipart forms

### `Multipart`

```go
type Multipart[T any] struct {
	Value T
}
```

Multipart is a Body read as `multipart/form-data` and decoded into `T` before the handler runs. Choose [`Upload`](#upload) when the handler must see the bytes while they arrive (a file larger than it can keep, a signature checked on the way); choose `Multipart` when it wants the fields and the files together as values. `T` is a struct whose fields are the form's fields: a text field is decoded exactly like a [`Form`](#form) field (the same types, `Opt`, repeated names for a list, the constraint types, `WireNames`, `Defaults`, at most 64 fields and 8 lists), and a file field is a [`FormFile`](#formfile), a named struct embedding one, an `Opt` of either for at most one file, or a slice of either for any number under one name. The handler reads `in.Body.Value.Title`. A mistake is located as `body.title`, a list element as `body.tags[1]`, a file as `body.cover`; a field the type does not have is a 400 `unknown field` unless the route has `AcceptUnknownMembers`.

The other Sections are decoded first, and a mistake in one is a 400 answered before a byte of the body is read (so a client that sent `Expect: 100-continue` never sends it). The body is then read whole, before `Validate` and the handler, and every mistake in its fields and files is one 400, up to 20. `Validate` may call `Size` and `ContentType` of a file and not read it.

<!-- compiles: decls -->

```go
type Photo struct{ bites.FormFile }

func (Photo) Accept() []string { return []string{"image/png", "image/jpeg"} }
func (Photo) MaxSize() int64   { return 5 << 20 }

type NewListing struct {
	Body bites.Multipart[struct {
		Title   bites.NonEmpty
		Price   int64
		Cover   Photo            // exactly one file, required
		Gallery []Photo          // zero or more files under one name
		Floor   bites.Opt[Photo] // at most one file
	}]
}
```

Where a file lives: in memory while the request's files stay at or under 256 KiB (fixed in code) and the app's share (`Limits.UploadMemory`, 32 MiB) has room, and otherwise in an unlinked temporary file in `Limits.UploadDir` (`os.TempDir()` by default). Both figures are judgements, not measurements of a workload. The file is unlinked the moment it is made and the framework closes it when the request ends, so a handler that panics or a crash leaves nothing in the directory. A file part is read, and a file made, only for a field the type has.

Limits, all on by default and none a way out of `MaxBody`: the body (`MaxBody`, required on the route or a group above it, no default, 413); one file (the `MaxSize()` of its type, else `MaxBody`; 413 at `body.cover`, `the file is over 5242880 bytes`); the parts (`MaxParts`, 128, 413); one text field (`Limits.MaxFieldBytes`, 64 KiB) and all the text of a request (`Limits.MaxFormBytes`, 1 MiB), 413 at the field; the routes in flight (`MaxInFlight`). A file that would pass the memory bounds goes to disk and the client is told nothing. A part with no name, a truncated body and malformed framing are the 400 an `Upload` gives, and close the connection.

The located answers: `required` for a missing field or file; `the file must be sent as image/png or image/jpeg` (400, from `Accept`: it checks the type the client declared for the part, which is data and can lie, so it guards against a mistake and not an attacker); `expected one file, got 2` for a repeated name given to a single file; `a file part needs a filename` for a plain text part sent under a file field's name; `a text field was sent as a file` for the reverse.

A `Submitted[T]` whose `T` has a file field is read as multipart: it accepts `multipart/form-data` and refuses `application/x-www-form-urlencoded` with a 415. `Sent` of a file field is the empty string, because a browser never fills a file input again. A mistake in another Section is the 400 listing that Section's mistakes only. A body over a limit is the 413 problem+json.

In the API document the body is `multipart/form-data` with the object schema of `T`: a text field as in a `Form`, a file `{"type": "string", "format": "binary"}`, a list of files an array of those and not required, and an `encoding` entry for each file field whose type has `Accept` (`{"contentType": "image/png, image/jpeg"}`); `MaxSize` is stated in the field's description.

Refused at Registration (each a row of the refusal table, after the route label):

| Mistake | Message |
|---|---|
| `T` not a struct, or an `Opt` or `Null` | `NewListing.Body is a Multipart of Listing, but a Multipart wraps a struct whose fields are the form fields and files` |
| no `MaxBody` on the route or a group above it (also for a `Submitted` with a file) | `NewListing.Body has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give the route or its group MaxBody` |
| a `FormFile` in a `Form` | `NewListing.Body.Value.Cover is a FormFile in a Form; a Form is read as application/x-www-form-urlencoded and cannot carry a file, so make the Body a Multipart` |
| a `FormFile` in a Query, Header, Path or Cookie Section, or in a JSON body | `NewListing.Query.Cover is a FormFile in a Query; a file is a part of a multipart body, so make the Body a Multipart` |
| a pointer, map, array, `[]Opt[F]`, `Opt[[]F]`, `[]byte`, `*multipart.FileHeader` or `io.Reader` as a file field | `NewListing.Body.Value.Cover is a *bites.FormFile; a file field is a FormFile, an Opt of one, or a slice of one` (with `(bites has no multipart.FileHeader; FormFile.Open returns the reader)` for the last three) |
| an `Accept` entry that is not `type/subtype` or `type/*`, an empty list, or `*/*` | `NewListing.Body.Value.Cover: Photo.Accept returns "image"; an entry is a media type such as image/png or image/*` |
| a `MaxSize` under 1 or over the route's `MaxBody` | `NewListing.Body.Value.Cover: Photo.MaxSize returns 0; it is the most bytes of the file, and at most MaxBody` |
| `Accept` or `MaxSize` with another signature | `NewListing.Body.Value.Cover: Photo.MaxSize is func() int; it must be func() int64, the most bytes of the file` |
| a file type with fields besides the embedded `FormFile` | `NewListing.Body.Value.Cover is a Photo with fields besides the embedded FormFile; a file type is a FormFile or a struct whose only field is an embedded FormFile` |
| more than 8 lists, files included | `NewListing.Body.Value.Gallery has more than 8 lists` |
| a `Multipart` on a GET, HEAD or DELETE route | `NewListing.Body is a Multipart on a GET route; a body is sent with POST, PUT or PATCH` |
| a `Multipart` and an `Upload` in one Input | `NewListing holds a Multipart and an Upload; a request body is read one way` |
| a `Multipart` held by a JSON body or a slice | `NewListing.Body holds a Multipart; a Multipart is the whole Body, read as a form, not a field of a JSON body` |

`Limits.UploadMemory`, `MaxFieldBytes` and `MaxFormBytes` below zero, and a `Limits.UploadDir` that does not exist or cannot be written, are refused by `New` (see [`Limits`](./app.md#limits)). A spill directory on tmpfs is not refused: the start-up log warns that a file written there is memory.

Allocations: 3 a request, as for an Upload, plus 2 for each file kept in memory (its record and its bytes) and 1 more for the second and later files of a list; a file spilled to disk costs about 10 more (the temporary file's name and descriptor). The text fields cost nothing when the body arrived with its head and one allocation each when it did not. A typed JSON request still allocates 2 ([ADR 0003](../adr/0003-the-trade-budget.md)).

Guide: [forms and uploads](../guide/forms-and-uploads.md). Decision: [ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md).

### `FormFile`

```go
type FormFile struct {
	Name        string
	FileName    string
	ContentType string
	Size        int64

	// Has unexported fields.
}
```

FormFile is one file received in a [`Multipart`](#multipart) body, and the type a named struct embeds when the field must say what it accepts: a method `Accept() []string` on that type lists the media types allowed (`image/png`, `image/*`), and `MaxSize() int64` the most bytes of the file. A bare `FormFile` accepts any type and is bounded by the route's `MaxBody`. `Name` is the form field the file was sent under, `FileName` and `ContentType` are what the client sent, as data (`FileName` is never a path and the framework never opens it), and `Size` is the bytes actually received, not declared.

A FormFile is valid only while the handler runs. After it returns, `Open` and every reader from it fail with [`ErrUploadClosed`](#erruploadclosed), in memory or on disk, so a goroutine that kept one fails the same way for a small file and a large one and never reads another request's bytes ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)); a handler that wants the file after it returns copies it first. The zero FormFile (an `Opt` that was not sent) holds no file and its `Open` fails.

### `FormFile.Open`

```go
func (f FormFile) Open() (io.ReadSeekCloser, error)
```

Open returns an independent reader over the file each time it is called. Copy the file out in the handler (to a store, to a decoder): the reader fails with `ErrUploadClosed` once the handler has returned. A file never opened costs only the descriptor's close.

<!-- compiles: decls -->

```go
func create(ctx context.Context, in NewListing) (bites.Created[string], error) {
	r, err := in.Body.Value.Cover.Open()
	if err != nil {
		return bites.Created[string]{}, err
	}
	defer r.Close()
	if _, err := io.Copy(io.Discard, r); err != nil { // stands for the store
		return bites.Created[string]{}, err
	}
	return bites.Created[string]{Location: "/listings/1"}, nil
}
```

## Methods a type can declare

Two optional methods change how a type is read. Neither is an exported name of the package: they are conventions Registration looks for by reflection, once, when the route is added.

### WireNames method

A struct used as a `Path`, `Query`, `Header` or `Cookie` section, or as the `T` of a `Form[T]` body, may have the method `WireNames() map[string]string`, which renames fields on the wire by Go field name, replacing what the naming policy would give them ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md)). It is how a query parameter called `p` or `per-page` is read into a field with a Go name. A JSON body does not use it: the codec names JSON members by the naming policy alone. In a section made of embedded structs each struct's `WireNames` names its own fields, the section's own wins for a field both name, and a method only promoted from the one embedded struct that has it is that struct's, not the section's. Each key must name a field of the struct whose method gives it; one that does not is a Registration panic, a row of the refusal table:

```text
GET /x: StaleNamesIn.Query.WireNames names Gone, which is not a field
```

<!-- compiles: decls -->

```go
type ListQuery struct {
	Page    int
	PerPage bites.Opt[int]
}

func (ListQuery) WireNames() map[string]string {
	return map[string]string{"Page": "p", "PerPage": "per-page"}
}

type ListPage struct {
	Query ListQuery
}
```

### Defaults method

A struct used as a `Query`, `Header` or `Cookie` section, or as the `T` of a `Form[T]` body, may have the method `Defaults() map[string]string` (so the section is a named type: an inline `Query struct{...}` has no methods), which says by Go field name what an omitted field means, written as a client would send it ([ADR 0041](../adr/0041-presence-is-a-type.md)). The field stays a plain `T`: the handler reads `in.Query.Limit` and never sees an absence, the field is no longer required, and the API document gives the parameter `required: false` and `default` in its schema, from the same entry. Each entry is decoded at Registration by the field's own text decoder, so a default is held to the field type's `Range`, `Length`, `Values`, `Pattern` and `Check`, and the same decoder fills the field on each request that omits it. A field sent with a bad value (`?limit=x`, `?limit=`) is a 400 and does not fall back to its default, and `Validate` runs after the defaults are in. In a section made of embedded structs each struct's `Defaults` states its own fields' defaults and the section's own wins for a field both name, so two routes may share one `Paging` and differ in `Limit`. A field the method does not name is as required as ever. A default for a path parameter, an `Opt`, a list or a `CSV` is refused, and so is a `Defaults` method on a type read as a JSON `Body`. Each is a Registration panic, a row of the refusal table:

| Mistake | Message (after the route label) |
|---|---|
| an entry naming no field | `DefTypoIn.Query.Defaults names Lmit, which is not a field of defTypoQuery (its fields: Limit)` |
| a path parameter | `DefPathIn.Path.ID has a default, but a path parameter is always present` |
| an `Opt` | `DefOptIn.Query.Limit is an Opt and has a default: a default removes the absence an Opt exists to report, so make it a plain int` |
| a list or a `CSV` | `DefListIn.Query.Tags is a list and has a default: a list default would be one slice shared by every request or a copy made for each, so a list has none` |
| a value its type refuses | `DefRangeIn.Query.Limit has the default "500", which its own type refuses: expected an integer between 1 and 100` |
| a JSON `Body` | `DefBodyIn.Body: defBodyType has a Defaults method, but a JSON Body does not read it: use Opt[T] and Or in the handler, or a Form[T], whose fields are read like a Query` |

A default costs no allocation for a built-in type and what a sent value costs for a `TextUnmarshaler`. Nothing here is dynamic: a default that depends on the clock or on another field is an `Opt` the handler resolves.

<!-- compiles: decls -->

```go
type PageLimit int

func (PageLimit) Range() (min, max int64) { return 1, 100 }

type SortKey string

func (SortKey) Values() []SortKey { return []SortKey{"risk", "due"} }

type PagedQuery struct {
	Limit PageLimit
	Sort  SortKey
	Q     bites.Opt[string]
}

func (PagedQuery) Defaults() map[string]string {
	return map[string]string{"Limit": "50", "Sort": "risk"}
}
```

### Values method

A named string type with the method `Values() []T` (or `Values() []string`) is an enum: a text value or a JSON string is accepted only when it is one of the listed values, and anything else is a 400 whose message lists them (`expected one of: active, banned`); the API document gives the schema an `enum` ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)). `Values` is called once at Registration on a zero receiver, so it must not depend on the receiver's value. A `Values` with another signature, or one that returns no value, is a Registration panic:

```text
GET /x: BadEnum.Query.Kind: bites_test.emptyKind: Values returned nothing, so no request could ever be valid
```

On output the list is advisory: a handler may return a value outside it, and it is written as it is.

<!-- compiles: decls -->

```go
type Status string

func (Status) Values() []Status { return []Status{"active", "banned"} }

type SetStatus struct {
	Path struct{ ID int64 }
	Body struct{ Status Status }
}
```

## Body and upload options

These are `RouteOption` values: given to a route they apply to it, given to a `Group` they apply to every route in it, and the innermost wins. A value that cannot work panics where it is written: `POST /x: MaxBody(0) is not a limit: it must be a positive number of bytes`, `group "/g": BodyTimeout(0s) is not a limit: it must be a positive duration`.

### Discriminator method

A struct with the method `Discriminator() string`, on the value receiver, is a **tagged union**: a JSON value of several kinds, as a WebSocket protocol's frames are ([ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)). Each field is an `Opt` of a struct, one variant, whose tag is the field's wire name, and the method returns the member that carries the tag. A value has exactly one variant set; on the wire it is that variant's object with the tag as a member, `{"type":"join","room":7}`, written first and read wherever it sits. It may be an Inbox's or a Socket's message, a Topic's payload, a JSON `Body`, a response or a field of any of them.

<!-- compiles: decls -->
```go
type Join struct{ Room int64 }
type Say struct{ Text string }

type ClientFrame struct {
	Join bites.Opt[Join]
	Say  bites.Opt[Say]
}

func (ClientFrame) Discriminator() string { return "type" }

func handle(f ClientFrame) string {
	if j, ok := f.Join.Get(); ok {
		return fmt.Sprint("join ", j.Room)
	}
	s, _ := f.Say.Get()
	return "say " + s.Text
}
```

A tag that names no variant is a mistake at the discriminator (`type: expected one of: join, say`, rule `values`), a missing one is `type: required`, and a variant's fields are located as in any object. Encoding a value with no variant set, or two, is an error (`Socket.Send` returns it) and writes nothing. The API document makes the type `oneOf` its variants with `discriminator: {propertyName: "type"}`, each variant inline with the tag as a required `const` member and its tag as `title`. Refused at Registration, each naming the field:

| Mistake | Message |
|---|---|
| `Discriminator` on `*T` | `T declares Discriminator on *T, but the codec holds a T: declare it on the value receiver, func (T) Discriminator() string` |
| it returns `""` | `T.Discriminator returns "": return the name of the member that says which variant a value is, such as "type"` |
| fewer than two variants | `T is a union of 1 variants: a union has at least two, each an Opt field of a struct` |
| a field that is not an `Opt` of a struct | `plainVariantFrame.Say: a field of the union plainVariantFrame is string, but each is an Opt of a struct, the variant whose tag is "say"` |
| a variant with a member named as the discriminator | `typedVariant.Type: the variant typedVariant has a member "type", which is the discriminator of clashingFrame: rename the member, or return another name from Discriminator` |
| a variant that writes or reads its own JSON, is a union, or has `omitempty` or `omitzero` | `the variant V writes or reads itself, but a variant is an object the union writes with its discriminator: ...` and the like |

A union decode costs no allocation of its own. Decision: [ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md).

### ServerOnly method

A tagged union may have the optional method `ServerOnly() []string`, on the value receiver, which returns the Go names of the variants that only the server carries:

```go
func (ServerFrame) ServerOnly() []string { return []string{"Ref"} }
```

The case is a Topic's reference: a message too large for its broker is published as a variant (`Ref`) that [`Topic.Resolve`](websocket.md#topicresolve) turns back into the message on each instance. A server-only variant is left out of the API document (the `oneOf` of every direction), a client that sends its tag is told `expected one of:` the others, `Socket.Send` of a value that holds it returns an error naming the union, the tag and the field and sends nothing (`the union ServerFrame has its server-only variant ref (field Ref) set, which is never sent to a client`), a Topic does not send it to its Sockets (a brokered Topic logs a Warn naming the Topic and the variant, and its `Listen` functions still get it), and an HTTP answer that holds it is a 500 with the reason logged. A Topic's payload codec writes and reads it as any variant, so it crosses the broker. The zero value is no method: every variant is the clients'. Allocations: none per request, and no step for a type with no union or a union with no method. Refused at Registration, each naming the union:

| Mistake | Message |
|---|---|
| `ServerOnly` on `*T` | `T declares ServerOnly on *T, but the codec holds a T: declare it on the value receiver, func (T) ServerOnly() []string` |
| a name that is not a variant | `strayServerOnlyFrame.ServerOnly names Rfe, which is not a variant of strayServerOnlyFrame (its variants: Join, Ref): name a field that is an Opt of a variant` |
| a name twice | `T.ServerOnly names Ref twice: name each variant once` |
| every variant named | `T.ServerOnly names every variant (Join, Ref), so no client could be sent or send any: leave at least one out` |

Decision: [ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md).

### `AcceptUnknownMembers`

```go
func AcceptUnknownMembers() RouteOption
```

AcceptUnknownMembers lets a JSON body (or a `Form` body) carry members the Body type does not have; by default each is a 400 naming it. The unknown member is ignored. On a group it applies to every route of the group.

Decision: [ADR 0015](../adr/0015-json-is-our-own-codec-on-jsontext.md).

### `RejectUnknownQueryKeys`

```go
func RejectUnknownQueryKeys() RouteOption
```

RejectUnknownQueryKeys makes a request to this route a 400 when its query holds a key the `Query` section does not name. By default such a key is ignored, so a misspelled filter (`?knid=government`) returns every row and looks like one that worked. The error is located `query.<key>` with the sentence `unknown query parameter` followed by what the route accepts, is listed with the other errors, and appears once however many times the key is repeated; only the key, cut to 64 bytes, is echoed, never the value. The rest of the message names the nearest declared key when one is near (`; did you mean "user_access_id"?` for `userAccessId`, `user-access-id` or a one-letter slip; `knid` names `kind`) and then lists the declared keys, the first eight and `, and N more` (`; this route takes user_access_id, limit`), or says `; this route takes no query parameters` for a route with no `Query` section; the whole entry is `{"location":"query.account_id","message":"unknown query parameter; this route takes user_access_id, limit"}`. The names are the route's own, and its `Use` middleware has already run, so a client that sign-in refuses never reads them. The hint is built on the error path only. A broken percent-encoding in a key is a 400 at `query`. A route with no `Query` section takes no query at all. Header and cookie names are not checked. On a group it applies to every route of the group, and the innermost option wins. The check costs no allocation on a request that passes.

Decision: [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md).

### `AcceptUnknownQueryKeys`

```go
func AcceptUnknownQueryKeys() RouteOption
```

AcceptUnknownQueryKeys lets this route ignore query keys its `Query` section does not name, which is what a route does by default. It is the way out of [`WithRejectUnknownQueryKeys`](./app.md#withrejectunknownquerykeys) or of a group's `RejectUnknownQueryKeys` for the route that must tolerate tracking parameters it did not ask for.

### `MaxBody`

```go
func MaxBody(n int64) RouteOption
```

MaxBody sets the most bytes of request body this route (or every route of this group) reads; over it is a 413, before `100 Continue` when the `Content-Length` says so, and when the `n+1`th byte arrives for a chunked body. It must be positive. The default is `Limits.MaxBody`, 1 MiB. It is required for an Upload route.

Decision: [ADR 0024](../adr/0024-limits-are-on-by-default.md).

### `BodyTimeout`

```go
func BodyTimeout(d time.Duration) RouteOption
```

BodyTimeout sets how long this route (or every route of this group) waits for its whole body; over it is a 408 and the connection is closed. It must be positive. The default is `Limits.BodyTimeout`, 30 s, and for an Upload route `max(30 s, MaxBody / 128 KiB/s)`.

### `MaxParts`

```go
func MaxParts(n int) RouteOption
```

MaxParts sets the most parts one upload on this route (or on each Upload route of this group) may have; more is a 413. The default is 128. `n` must be positive. It has no effect on a route whose Body is not an Upload.

### `MaxInFlight`

```go
func MaxInFlight(n int) RouteOption
```

MaxInFlight bounds the uploads of this route (or of each Upload route of this group, counted per route, not across the group) running at once. The counter is checked after the head and before `100 Continue`, and a request over it is answered 503 with `Retry-After` and the connection closed, so the client never sends the body. `n` must be positive. There is no default, because one would turn away legitimate traffic; the start-up log says `unlimited` for an Upload route without it. It has no effect on a route whose Body is not an Upload.
