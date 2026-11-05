# Validation

**Types refuse what is the wrong kind of value; a type of yours carries the rule for one value, a `Validate` method says the rules across fields, and the 400 lists every mistake at once.**

**Reference:** [Constraint types and rule methods](../reference/input.md#constraint-types) · **Design:** [Request input](../design/request-input.md)

## Rules a type carries

**A named type of yours carries the rule for its own values in a method, and decoding checks it wherever the type decodes.** `Length() (min, max int)` on a string type bounds its length in characters, `Range() (min, max int64)` on an `int` or `int64` type bounds its value, `Range() (min, max float64)` on a `float32` or `float64` type does the same for an amount, `Items() (min, max int)` on a slice type bounds how many items it holds, `Members() (min, max int)` on a map type bounds how many members it holds, `Unique() bool` on a slice type says no item repeats, `ExclusiveMin() float64` and `ExclusiveMax() float64` on a float type bound it without including the value, `MultipleOf()` on a number type says it is a whole multiple of a step, `Pattern() string` on a string type makes it match a regular expression, and `Check() error` on any type runs your own test of a decoded value. **A `Check` error is sent to the client exactly as written.** Return a fixed error of your own, never the error of the function you called: `time.Parse`, `strconv` and `url.Parse` quote the input in their text, so `return err` from one of them sends the client its own value, which a 400 is built never to do. For a calendar date, use [`bites.Date`](../reference/input.md#date) and its fixed `ErrInvalidDate`. A `Format() string` on a string type names the `format` the API description gives the field, in the request and in a response that returns the type, and for `email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time` and `uuid` it is also checked: a value that is not of the format is a 400 with the rule `format`. Any other name is an annotation only. Registration reads `Length`, `Range`, `ExclusiveMin`, `ExclusiveMax`, `MultipleOf`, `Items`, `Unique`, `Members`, `Pattern` and `Format` once, as it reads [`Values`](../reference/input.md#values-method) for an enum ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md), [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)):

<!-- compiles: decls -->
```go
// RoomName is 3 to 40 characters.
type RoomName string

func (RoomName) Length() (min, max int) { return 3, 40 }

// Guests is 1 to 6 people.
type Guests int

func (Guests) Range() (min, max int64) { return 1, 6 }

// Tags is one to eight names.
type Tags []string

func (Tags) Items() (min, max int) { return 1, 8 }

// Email is an email address: the framework checks it, and the API description says so.
type Email string

func (Email) Format() string { return "email" }

// Price is 0.01 to 1000000, both included.
type Price float64

func (Price) Range() (min, max float64) { return 0.01, 1000000 }

type Booking struct {
	Query struct {
		Guests Guests
	}
	Body struct {
		Room    RoomName
		Guest   bites.NonEmpty
		Contact Email
		Price   Price
		Tags    Tags
		Ref     uuid.UUID
		Cap     bites.PositiveInt64
		Start   time.Time
		End     time.Time
	}
}
```

The type is the rule, so it is written once and every Input that uses `RoomName` gets it, and the rest of your code can take a `RoomName` too. The types are ordinary strings and integers, so a handler converts them (`string(in.Body.Room)`) and nothing is allocated for them. They work in a path, query, header, cookie, JSON body or `Form`, and inside `Opt`, `Null` and lists. The API description states a `Length` as `minLength` and `maxLength`, a `Range` as `minimum` and `maximum`, an `ExclusiveMin` or `ExclusiveMax` as `exclusiveMinimum` or `exclusiveMaximum`, a `MultipleOf` as `multipleOf`, an `Items` as `minItems` and `maxItems`, a `Unique` as `uniqueItems`, a `Members` as `minProperties` and `maxProperties` and a `Format` as `format` ([The OpenAPI document](./openapi.md)); what a `Check` tests it cannot state. A `Format` that is one of the eight the framework checks (`email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time`, `uuid`) is enforced on a request and answers `must be an email address` and the like, with the rule `format` and no echo of the value ([the definitions](../reference/input.md#format-method)); any other name is yours to choose and is only stated. A decimal type from another module has no `Range`: it checks its bounds in a `Check`.

**A bound that excludes its value, a step, a list with no repeat and a map's size.** A price above 0 is `ExclusiveMin() float64 { return 0 }` on a float type, and `0` itself is a 400; a side is stated once, so a type that also has a `Range` bounding the same side is refused (leave that side of `Range` at `-math.MaxFloat64` or `math.MaxFloat64`), and an integer has no exclusive bound: write `Range(1, ...)`. `MultipleOf() float64 { return 0.01 }` on a float type, or `MultipleOf() int64` on an `int` or `int64` type, says the value is a whole multiple, and a float is compared with a tolerance a few units in the last place wide, so `0.3` is a multiple of `0.1`. `Unique() bool { return true }` on a slice of strings, booleans, numbers or UUIDs says no item repeats, and the 400 is at the first item that does (`body.tags[3]`, `repeats an earlier item`); a list of structs is not compared, so its rule is the Input's `Validate`. `Members() (min, max int)` on a map type bounds how many members the object holds, and members past the maximum are skipped unstored. The 400s are the rules `range` (with `exclusive_min` or `exclusive_max`), `multiple` (with the step), `unique` and `members` ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)):

<!-- compiles: decls -->

```go
type Deposit float64

func (Deposit) ExclusiveMin() float64 { return 0 }

type Tip float64

func (Tip) MultipleOf() float64 { return 0.01 }

type Skills []string

func (Skills) Items() (min, max int) { return 1, 10 }
func (Skills) Unique() bool          { return true }

type Metadata map[string]string

func (Metadata) Members() (min, max int) { return 0, 20 }
```

**A list's bound stops the list early.** In a JSON body the items past an `Items` maximum are skipped without being stored, and in a query or form the values are counted before the list is allocated, so the bound is also a limit on what one request can make the server hold. The 400 has one entry, at the list's location (`body.tags`), whatever the list held.

**A UUID is already a type.** A `uuid.UUID` field (the Go 1.27 standard library package `uuid`; for a uuid type from a module, see [`WithTextFormat`](../reference/app.md#withtextformat)) decodes in every section, answers `expected a UUID` with the rule `type`, and is stated as `format: uuid`. It accepts what the standard library parses, which includes the `urn:uuid:` form and 32 digits without hyphens; to accept only the 36-character form, use a string type with a `Check` and `Format() string { return "uuid" }`.

**A pattern is a method too, in a subset both ends read the same.** `Pattern() string` on a string type returns a regular expression the value must match. It is compiled once at Registration with Go's `regexp`, and the API description states it as `pattern`, which a client reads as an ECMA-262 expression. The match is unanchored in both, so write `^` and `$` when the whole value must match. The failure is `must match the pattern` with the rule `pattern`; it holds neither the client's value nor the expression, which the document already gives the client. Because the two engines disagree about some constructs, Registration refuses the ones that differ (a flag such as `(?i)`, `\s`, `.`, `\A`, `\z`, a POSIX class, `\pL` and the rest listed under [Pattern method](../reference/input.md#pattern-method)) and names the construct, so a pattern that registered is one the client reads as the server does. Write `[ \t\n\r\f]` for a space, `[^\n\r]` for any character, and `\p{L}` for a letter:

<!-- compiles: decls -->
```go
// Slug is a lowercase word, or words joined by single hyphens, of 3 to 40 characters.
type Slug string

func (Slug) Length() (min, max int) { return 3, 40 }
func (Slug) Pattern() string        { return `^[a-z0-9]+(-[a-z0-9]+)*$` }
```

**Three rules common enough to name come with the framework.** [`NonEmpty`](../reference/input.md#nonempty) is a string of at least one character, and [`PositiveInt`](../reference/input.md#positiveint) and [`PositiveInt64`](../reference/input.md#positiveint64) are integers greater than zero.

A value that fails is a decoding error at its location. `Length`, `Range`, `Items`, `Pattern` and the named types answer a fixed sentence that never contains what the client sent: `must be between 3 and 40 characters`, `expected an integer between 1 and 6`, `expected a number between 0.01 and 1000000`, `must be an email address`, `must have between 1 and 8 items`, `must have at most 20 members`, `repeats an earlier item`, `expected a number greater than 0`, `expected a multiple of 0.01`, `must not be empty`, `expected a positive integer`, `must match the pattern`. A non-number keeps the base type's sentence, `expected an integer`. Length is counted in characters (Unicode code points), as JSON Schema counts it. A `Check` answers with its error's text, as you wrote it, so leave the client's value out of it as the framework does.

Registration refuses a `Members` that is not a bound or sits on a type that is not a map, a `Unique` that returns `false` or sits on anything but a slice of strings, booleans, numbers or UUIDs, a `MultipleOf` of zero or below (or of 1 on an integer) or on a type that is not an `int`, `int64`, `float32` or `float64`, an `ExclusiveMin` or `ExclusiveMax` on an integer type, beside a `Range` that bounds the same side, or that leaves no value, a `Length` or an `Items` that is not a bound (`min` below 0, or `max` below `min` or below 1), an `Items` on a type that is not a slice, a `Pattern` that is empty, does not compile or uses a construct RE2 and ECMA-262 read differently (below), a `Format` that returns an empty string, sits on an integer or float type (the document already gives those a format), or names a checked format on a type that is not a string, an enum or a type that decodes itself (nothing would check it), a `Range` whose `max` is below its `min` or that sits on an integer type other than `int` and `int64`, a float `Range` with a NaN or infinite bound, a `float32` bound outside its range, or the integer signature on a float type (and the float one on an integer type), a `Check` with a pointer receiver, and a method named `Length`, `Range`, `ExclusiveMin`, `ExclusiveMax`, `MultipleOf`, `Items`, `Unique`, `Members`, `Pattern`, `Format` or `Check` that is not that rule: a `Length() int` that counts something else is renamed, not silently ignored.

## Checking a rule across fields

**An Input may have a `Validate() error` method, run after every section has decoded without an error.** It is where "the end is after the start" lives, because that needs two fields. It is declared on the Input by value, and it reports each mistake with [`Invalid`](../reference/errors.md#invalid), giving a location in wire names and a message:

<!-- compiles: decls -->
```go
func (b Booking) Validate() error {
	var errs []error
	if !b.Body.End.After(b.Body.Start) {
		errs = append(errs, bites.Invalid("body.end", "must be after body.start"))
	}
	if int64(b.Body.Cap) < int64(b.Query.Guests) {
		errs = append(errs, bites.Invalid("body.cap", "must be at least query.guests"))
	}
	return errors.Join(errs...)
}

func book(ctx context.Context, in Booking) (bites.NoContent, error) {
	return bites.NoContent{}, nil
}
```

<!-- compiles: body -->
```go
app.Post("/bookings", book)
```

`errors.Join` of nothing is nil, so a request that passes is not slowed down. Each located error is answered in the same 400 as a decoding mistake, in the order you returned them:

```http
POST /bookings?guests=5 HTTP/1.1
Content-Type: application/json

{"room":"suite","guest":"Ada","contact":"ada@example.com","price":120,"tags":["sea"],"ref":"6f1c2a9e-4b7d-4c3e-9a51-0d2f8e7b6a14","cap":4,"start":"2026-05-02T10:00:00Z","end":"2026-05-01T10:00:00Z"}

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"body.end","message":"must be after body.start"},{"location":"body.cap","message":"must be at least query.guests"}]}
```

A located error wrapped with `%w` is found too. The location is a string you write, and the framework cannot check it: a wire name changed by the naming policy leaves a stale string behind, so write it in the names the client uses ([Requests](./requests.md#wire-names)). A [`FieldError`](../reference/errors.md#fielderror) is what `Invalid` returns.

## What runs when

**Decoding runs first, `Validate` runs only on a fully decoded Input, and the handler runs last.** The order is fixed ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md), [Routing](./routing.md#the-order-a-request-goes-through)):

1. Every section is decoded, and each value's `Length`, `Range`, checked `Format` or `Check` with it. A mistake of type, presence or rule is collected, and the 400 lists them all.
2. If decoding found none, `Validate` runs once, with the Input by value.
3. If it returned none, the handler runs.

Each rule of a type is named in its entry by a `rule` code (`length`, `range`, `items`, `members`, `unique`, `multiple`, `pattern`, `format`, `nonempty`, `positive`, `values`, `check`), with `min` and `max` for `length`, `range`, `items` and `members`, `exclusive_min` or `exclusive_max` for a float `range` that excludes its bound, `multiple` for the step of `multiple`, and a `format` member for `format`, so a client can show the bounds in its own language; a `Check` error keeps its text as the message. The errors of your own `Validate` carry no code. See [Errors](./errors.md#reading-a-400).

So `Validate` never sees a half-filled Input, and a request with `"room":"x"` and nine guests is answered with those two mistakes alone; the cross-field mistakes appear once they are fixed:

```http
POST /bookings?guests=9 HTTP/1.1
Content-Type: application/json

{"room":"x","guest":"Ada","contact":"ada@example.com","price":120,"tags":["sea"],"ref":"6f1c2a9e-4b7d-4c3e-9a51-0d2f8e7b6a14","cap":4,"start":"2026-05-02T10:00:00Z","end":"2026-05-01T10:00:00Z"}

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"query.guests","message":"expected an integer between 1 and 6","rule":"range","min":1,"max":6},{"location":"body.room","message":"must be between 3 and 40 characters","rule":"length","min":3,"max":40}]}
```

## Listing every error

**The 400 lists up to 20 errors, in order, and says when it cut the list.** A client fixing a form wants every mistake in one round trip. Past 20 the list is cut and the answer carries `"truncated":true` ([ADR 0012](../adr/0012-client-errors-are-400-problem-json.md), [ADR 0024](../adr/0024-limits-are-on-by-default.md)). Each entry has a `location` and a `message`, and [Errors](./errors.md#reading-a-400) has the full shape.

## Returning a status instead of a located error

**A `Validate` error with no located error in it is answered like a handler's error.** `return bites.Conflict("sold out")` is a 409, and any other error is a 500 with no message, logged ([ADR 0049](../adr/0049-errors-are-found-with-errors-as-and-timeouts-wait.md)). Use this for a rule that is not about a field. A check that needs the database is the handler's job, not `Validate`'s: `Validate` has no context and must not block.

## What is refused at Registration

**A `Validate` that cannot be called is refused, naming the route**, as are the rule methods above that cannot work. Each of these is a row of the refusal table ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)):

- a `Validate` whose type is not `func() error`;
- a `Validate` with a pointer receiver, because an Input is taken by value and it would never be found;
- a `Validate` on a section (`Body`, `Query` and the rest), which would never be called;
- a field with a tag another framework reads for a rule or a place (`validate`, `binding`, `minLength`, `query` and the rest, listed under [Coming from Fiber or Gin](#coming-from-fiber-or-gin)), because bites would ignore it and the rule would not hold.

## What it does not do

**A pattern outside the subset RE2 and ECMA-262 read alike is a `Check`.** A `Check` (with `regexp`, compiled once in a package variable) can use anything Go's `regexp` reads, but the API description cannot state it. Rules are not enforced on output and are not carried into a response schema. Tags such as `validate:"min=1"` were rejected on purpose: they are a language inside a string that no compiler checks, and their errors name Go fields ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)).

## Coming from Fiber or Gin

**A tag you bring along is refused, not ignored.** Registration panics on a field, in any Section or at any depth of a JSON Body, whose tag has one of these keys, and the message says what to write instead: `validate` and `binding`; `path`, `query`, `header`, `cookie`, `form`, `uri`, `param`, `params` and `reqHeader`; `minLength`, `maxLength`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minItems`, `maxItems`, `pattern`, `format` and `enum`; `required` and `default`. A `validate` or `binding` tag is let through when the Input has a `Validate`, so a type shared with code that runs go-playground/validator can keep its tags and run it there; a type that decodes itself is not looked into; `json`, `db`, `gorm`, `doc` and `example` are left alone ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)):

```text
POST /users: CreateUser.Body.Name has a validate tag, which bites does not read, so its rules would never run: give the field a type of your own with a rule method (Length, Range, Items, Pattern, Format, Check), or run your validator in CreateUser.Validate
	registered at main.go:31
```

**The tags of go-playground/validator, which Gin binds with and Fiber users reach for, each become a type, a method or the Input's `Validate`.** The rule moves from a string on the field to the type of the field, so the compiler reads it, the API description states it, and the 400 names the wire location instead of a Go field. Tags are on the left as plain text:

| validator tag | in bites |
|---|---|
| `validate:"required"` | A field is required unless it is an [`Opt`](../reference/input.md#opt); a missing member is a 400 `required` at its location. A string that must not be empty is [`NonEmpty`](../reference/input.md#nonempty) |
| `min=3,max=40` on a string | `Length() (min, max int)` on a named string type, in characters |
| `gte=1,lte=6` on an integer | `Range() (min, max int64)` on a named `int` or `int64` type |
| `gte=0.01,lte=1000000` on a float | `Range() (min, max float64)` on a named `float32` or `float64` type |
| `gt=0` on an integer | [`PositiveInt`](../reference/input.md#positiveint) or [`PositiveInt64`](../reference/input.md#positiveint64) |
| `gt=0`, `lt=1` on a float | `ExclusiveMin() float64` and `ExclusiveMax() float64` on a named `float32` or `float64` type |
| `multipleof=5` | `MultipleOf() int64` on a named `int` or `int64` type, `MultipleOf() float64` on a float type |
| `unique` on a slice | `Unique() bool` on a named slice type of strings, booleans, numbers or UUIDs; for a list of structs, the Input's `Validate` |
| `min=1,max=8` on a map | `Members() (min, max int)` on a named map type |
| `min=1,max=8` on a slice | `Items() (min, max int)` on a named slice type |
| `email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time`, `uuid` | A string type with `Format()` returning `email`, `uri`, `hostname`, `ipv4`, `ipv6`, `date`, `date-time` or `uuid`, which the framework checks; for another definition of an email, a `Check` |
| `uuid` | A `uuid.UUID` field, which answers `expected a UUID` and is stated as `format: uuid`; for only the 36-character form, a string type with `Format() string { return "uuid" }` |
| `oneof=admin member` | An enum: a named string type with [`Values`](../reference/input.md#values-method) |
| `regexp` or a pattern | `Pattern() string` on a named string type, within the subset above |
| `eqfield`, `gtfield`, `required_with` and the other cross-field tags | The Input's `Validate`, returning `bites.Invalid("body.end", "must be after body.start")` |
| `min=1,max=8` on a comma-separated list | the same `Items`, on a named type of [`CSV[T]`](../reference/input.md#csv) that repeats its `CommaSeparated()` marker |
| `dive` | Nothing to write: a list of a named element type checks each element with that type's rule, and a nested struct is checked field by field |

<!-- compiles: decls -->
```go
// Username is 3 to 20 lowercase letters, digits or underscores.
type Username string

func (Username) Length() (min, max int) { return 3, 20 }
func (Username) Pattern() string        { return `^[a-z0-9_]+$` }

// Age is an adult's age.
type Age int

func (Age) Range() (min, max int64) { return 18, 130 }

// Role is one of two values.
type Role string

func (Role) Values() []Role { return []Role{"admin", "member"} }

// Emails is up to five addresses, each checked by Email's own rule.
type Emails []Email

func (Emails) Items() (min, max int) { return 0, 5 }

type Signup struct {
	Body struct {
		Name     Username
		Age      Age
		Role     Role
		Contact  Email
		Also     Emails
		Password string
		Confirm  string
	}
}

func (s Signup) Validate() error {
	if s.Body.Password != s.Body.Confirm {
		return bites.Invalid("body.confirm", "must equal body.password")
	}
	return nil
}
```

**What has no translation.** A tag that is a function of the field's value alone (`ne`, `contains`, `startswith`, `ip`, `url`, `datetime`) is a `Check` on a named type, with a `Format` when the document should say what it is. A tag that needs the rest of the program (a custom validator registered by name, `required_if` that reads another struct) is `Validate`. Tags themselves are not offered ([ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)): they are a language inside a string that no compiler checks, and their errors name Go fields. An adapter that runs a validator library on the Input is not offered either, for three reasons: the rules would live in two places, the struct and the tags beside the types, so the type would no longer be the whole contract; they would never reach the API description, which reads types; and the location of an error would be guessed from a Go field name, where bites knows the wire name because it decoded it. Wrap a library call in a `Check` or a `Validate` when you need a rule it has and this page lacks.

## Where to go next

- [Errors](./errors.md): the 400 in full, and the answers you return on purpose.
- [Forms and uploads](./forms-and-uploads.md): the same rules in a `Form`.
