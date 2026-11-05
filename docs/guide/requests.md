# Requests

**An Input reads the request through five sections, and the type of each field decides how its text or JSON is decoded and whether it may be missing.**

**Reference:** [Input types](../reference/input.md) · **Design:** [Request input](../design/request-input.md)

## One Input with every section

**A section is a field of the Input named `Path`, `Query`, `Header`, `Cookie` or `Body`.** Each is a struct of the values it reads; the field names inside become the names the client uses ([Wire names](#wire-names)). The running example of this page reads an order:

<!-- compiles: decls -->
```go
type Line struct {
	SKU string
	Qty int
}

type PlaceOrder struct {
	Path struct {
		ShopID int64
	}
	Query struct {
		Express bites.Opt[bool]
		Coupon  bites.Opt[[]string]
	}
	Header struct {
		IdempotencyKey string
	}
	Cookie struct {
		Session bites.Opt[string]
	}
	Body struct {
		Customer string
		Lines    []Line
		Note     bites.Null[string]
	}
}

type Receipt struct {
	Shop    int64
	Express bool
	Lines   int
}

func placeOrder(ctx context.Context, in PlaceOrder) (bites.Created[Receipt], error) {
	r := Receipt{Shop: in.Path.ShopID, Express: in.Query.Express.Or(false), Lines: len(in.Body.Lines)}
	return bites.Created[Receipt]{Body: r, Location: "/orders/1"}, nil
}
```

<!-- compiles: body -->
```go
app.Post("/shops/{shop_id}/orders", placeOrder)
```

**Routes that share a section embed it.** A struct that holds only sections, embedded in an Input, gives the Input its sections, so twenty routes of a shop declare its `Path` once and each reads `in.Path.ShopID`:

<!-- compiles: decls -->
```go
type shopPath struct{ Path struct{ ShopID int64 } }

type RenameShop struct {
	shopPath
	Body struct{ Name string }
}
```

An embedded struct with no section is still a value a Provider gives (`struct{ User }`). One that mixes sections with other fields, and a section given twice, are refused at Registration ([the Sections](../reference/input.md#the-sections)).

## Reading an Input without tags

**An Input has no tags, so three things you would read in a tag are said elsewhere: the name by the naming policy, the type by the Go type, and whether the value may be missing by the wrapper.** Once those three are known, a field reads as what the client sends:

| To know | Said by | Example |
|---|---|---|
| the name the client uses | the app's [naming policy](#wire-names), snake_case by default; a `json` tag in a `Body` wins over it | `ShopID` is `shop_id`, `CreatedAt` is `created_at`; a header is `Idempotency-Key` |
| the type of the value | the field's Go type, or the type under a named type | `NonEmpty` and `type RoomName string` are strings, `PositiveInt` is an integer, `[]Line` is a list of objects |
| whether it may be missing | the wrapper | a plain field is required, `Opt[T]` may be absent, `Null[T]` may be `null` but must be sent, `Opt[Null[T]]` may be either |
| a rule beyond the type | a method on the field's type | `Length`, `Range`, `Values`, `Check` ([Validation](./validation.md)) |

**A tag another framework reads is refused, so a habit from Gin or Huma stops at Registration instead of being ignored.** `validate:"min=3"`, `binding:"required"`, `query:"q"`, `path:"id"`, `minLength:"3"`, `required:"true"` and the rest of the list in [Validation](./validation.md#coming-from-fiber-or-gin) panic naming the field and what to write instead, and so does a `json` tag that renames a field of a Path, Query, Header, Cookie or Form section, where only the naming policy and `WireNames` name it ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md), [ADR 0021](../adr/0021-validation-is-a-method-and-a-few-constraint-types.md)). Tags for other readers (`json` in a Body, `db`, `gorm`, `doc`) are left alone.

Read that way, the running example accepts:

| Field | The client sends | Required |
|---|---|---|
| `Path.ShopID int64` | the `{shop_id}` segment, an integer: `/shops/7/orders` | yes, as every path parameter |
| `Query.Express Opt[bool]` | `?express=true` | no |
| `Query.Coupon Opt[[]string]` | `?coupon=a&coupon=b` | no |
| `Header.IdempotencyKey string` | `Idempotency-Key: k1` | yes |
| `Cookie.Session Opt[string]` | `Cookie: session=abc` | no |
| `Body.Customer string` | `"customer": "Ada"` | yes |
| `Body.Lines []Line` | `"lines": [{"sku": "A1", "qty": 2}]` | yes; `[]` counts as sent |
| `Body.Note Null[string]` | `"note": "leave it at the door"` or `"note": null` | yes; leaving it out is a 400 `required` at `body.note` |

So the smallest body `placeOrder` accepts is:

```json
{"customer": "Ada", "lines": [{"sku": "A1", "qty": 2}], "note": null}
```

A value converts back to a plain Go value where you need one: `string(in.Body.Name)` for a named string type, `in.Query.Express.Or(false)` for an `Opt`. When a rule belongs to a type of your own (`type CustomerName string` with a `Length`), your service can take that type and nothing converts. The [API document](./openapi.md) lists every field with its wire name, type, whether it is required and its bounds, which is the quickest way to see an Input whole.

**A section may embed structs, so a filter or a page is written once.** `Query struct{ Filter; Paging }` reads the fields of both as the section's own, under their own names; two embedded structs with a field of one name are refused as any two fields with one wire name are. Each embedded struct's `Descriptions` describes its own fields in the API document ([descriptions](./openapi.md)). Embed by value; an embedded pointer is refused.

## Reading a path parameter

**A Path field is filled from the `{name}` of the same wire name, and the pattern must have one for every field.** `ShopID` is `shop_id`, so the pattern above says `{shop_id}`. A segment that does not decode is a 400 at `path.shop_id` ([Routing](./routing.md#matching-a-wildcard-to-a-field)).

## Reading a query parameter

**A Query field is read from the query string by its wire name, and a key the Input does not name is ignored unless you ask for it to be refused.** A repeated key fills a list: `?coupon=a&coupon=b` gives `Coupon` two values. A comma is not a separator, because values contain commas ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)). An API that already sends `?stage=won,lost` makes the field a [`CSV`](#a-list-sent-as-one-comma-separated-value) instead.

**An ignored key hides a misspelled filter.** `?knid=government` against a `Kind` field returns every row, and the answer looks exactly like a filter that worked. [`RejectUnknownQueryKeys()`](../reference/input.md#rejectunknownquerykeys) on a route or a group, or [`WithRejectUnknownQueryKeys()`](../reference/app.md#withrejectunknownquerykeys) for the whole app, makes each such key a 400 located `query.knid` with the sentence `unknown query parameter` and what the route takes (`; did you mean "kind"?; this route takes kind, page, tags`: the nearest declared key when one is close, then the declared keys), and [`AcceptUnknownQueryKeys()`](../reference/input.md#acceptunknownquerykeys) takes one route back out (a link shortener adds parameters to URLs your API did not ask for). The app-wide switch holds a route with no `Query` section too: it takes no query at all. The default stays lenient because the parameters a proxy or an analytics tag adds would otherwise break working clients ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).

## Reading a header or a cookie

**A Header or Cookie field is read like a query value.** A header's name is the canonical kebab form of the Go name, not the naming policy's: `IdempotencyKey` is `Idempotency-Key` and `IfNoneMatch` is `If-None-Match`, so `XApiKey` is `X-Api-Key`. A leading `X` before a capital is a word of its own, so `XAPIKey` is `X-Api-Key` too, and since header names are matched without regard to case it reads the `X-API-Key` a client sends. Three kinds of name no rule gets right take `WireNames`: a brand in camel case (`XGitHubEvent` would be `X-Git-Hub-Event`), a run of digits that is a word (`X-Hub-Signature-256`) and `ETag`. An error in a header is located by that canonical name, `header.X-Api-Key`. A cookie is named by the naming policy. The name is matched without regard to case. A repeated header for a field that is not a list is a 400 ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md)).

## Text values are decoded by type

**A Path, Query, Header or Cookie field may be a string, a bool, an integer, a float, a `time.Time`, a `uuid.UUID`, a `Date`, a `TextUnmarshaler`, or a slice of one of these.** Anything else, such as a nested struct or a map, is refused at Registration ([ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).

| Type | Accepts |
|---|---|
| `bool` | `true`, `false`, `1`, `0` |
| integers, floats | a number; an empty value is a 400, not zero |
| `time.Time` | RFC 3339 with an offset (`2026-01-01T10:00:00+07:00`); a date alone is a 400 |
| `uuid.UUID` | the standard library's `uuid` package, Go 1.27, imported as `"uuid"`; it accepts what that package parses and is always written hyphenated |
| `bites.Date` | a calendar date, `YYYY-MM-DD`, stated as `format: date`; a timestamp or a day that does not exist is a 400 |
| an enum | a named string type with a `Values` method returning its own values (`func (Stage) Values() []Stage`) or strings (`[]string`); anything not listed is a 400 |
| `encoding.TextUnmarshaler` | what its `UnmarshalText` accepts; the client never sees its error text |
| a slice | the repeated key, one element each |
| a `CSV[T]` | one value split at commas, in a Query or a Header: `?stage=won,lost` |

**`uuid.UUID` means the standard library's.** Go 1.27 has a `uuid` package of its own, and the framework knows that type and no other: `import "uuid"`, then `ID uuid.UUID` in a Path, a Query or a Body. A uuid type from a module, such as `github.com/google/uuid`'s, is a `TextUnmarshaler` like any other: it decodes, but it answers `expected a valid value` and the API document calls it a plain string. Tell the app what it is once, with [`WithTextFormat`](../reference/app.md#withtextformat), and it gets the sentence `expected a UUID` and `format: uuid` everywhere, in requests and in responses:

```go
import guuid "github.com/google/uuid"

func newApp() *bites.App {
	return bites.New(bites.WithTextFormat[guuid.UUID]("uuid"))
}
```

`*T` needs `UnmarshalText` and `T` needs `MarshalText`, or `New` panics naming the line; a registered type costs the two allocations any `TextUnmarshaler` costs in a text section. Until you do, the app names each such type once in its start-up log, at Info, with the first field that has it and this option written out: `bites: a uuid type is read as text with no format: ... give the app bites.WithTextFormat[uuid.UUID]("uuid") ...`, for a type named `UUID` in a package whose path holds `uuid`, read by a Path, Query, Header, Cookie or Form field.

**A calendar date is a `bites.Date`.** A due date, a birthday or a billing day has no time of day and no zone, so it is not a `time.Time`. `Date` reads and writes `YYYY-MM-DD`, is stated as `format: date`, refuses a timestamp or `2026-02-30` with the fixed sentence `expected a date as YYYY-MM-DD`, and costs no allocation. It has no `Time` method, because a date has no instant and midnight UTC is a different day from midnight in Jakarta: write the zone where you need it, `d.In(loc)`, or build a date from a time with `bites.DateOf(t)`. See [`Date`](../reference/input.md#date).

A repeated key for a field that is not a slice is a 400, never first-wins or last-wins, which closes parameter pollution. The constraint types and the `Length`, `Range` and `Check` methods of [Validation](./validation.md) are checked in any of these places.

## A list sent as one comma-separated value

**A field of type `bites.CSV[T]` reads `?stage=won,lost` as a list of two, for an API whose clients already send lists that way.** It is the opt-in; a plain `[]T` is still filled from repeated keys. The handler uses it as a slice:

<!-- compiles: decls -->
```go
type Stage string

func (Stage) Values() []Stage { return []Stage{"won", "lost", "open"} }

type ListDeals struct {
	Query struct {
		Stage bites.CSV[Stage]
		Ids   bites.Opt[bites.CSV[int64]]
	}
}
```

Each element is decoded by `T` with its rules, so `?stage=won,bogus` is a 400 at `query.stage[1]` with `expected one of: won, lost, open`. `?stage=` is an empty list, an empty element (`won,,lost`) is decoded as the empty string and refused by a type that does not accept it, and `?stage=won&stage=lost` is a 400 `sent more than once`: one wire form per field. The API document says `type: array`, `style: form`, `explode: false`.

**The price is that an element cannot contain a comma**, because nothing escapes it; if a value can, keep the repeated-key slice. `CSV` is refused in `Path`, `Cookie`, a `Form` and a JSON body, where a list is an array. In a `Header` it reads one line, `X-Ids: 1, 2, 3`, trimming the spaces. To bound the count, name the type and repeat the marker: `type Stages bites.CSV[Stage]` with `CommaSeparated()` and `Items() (min, max int)` methods.

## Marking a field optional, nullable or both

**A plain field is required; `Opt[T]` may be absent; `Null[T]` may be `null`.** A pointer in an Input is refused at Registration, because it cannot say whether `nil` meant absent or null ([ADR 0041](../adr/0041-presence-is-a-type.md)).

- [`Opt[T]`](../reference/input.md#opt): `in.Query.Express.Or(false)` reads it with a default, `Get()` says whether it was sent, and the zero value is absent. A service that speaks `*T` takes `in.Query.Express.Ptr()` (nil when absent, a copy otherwise) and hands one back with [`OptOf`](../reference/input.md#optof); the Input field is still an `Opt`, since a pointer there is refused. [`Some`](../reference/input.md#some) makes a present one.
- [`Null[T]`](../reference/input.md#null): a body field sent as `null`. The zero value is null; [`NonNull`](../reference/input.md#nonnull) makes one with a value. In a text section `Null` is refused, because text has no null.
- `Opt[Null[T]]` is the PATCH field: absent leaves the stored value, `null` clears it, a value sets it.

**A field that has a default is optional for the client and present for the handler.** Say the default on the Section's struct with a `Defaults` method, in the text a client would send, and keep the field a plain type (a method needs a named type, so an inline `Query struct{ Limit int }` cannot have one: declare `type listQuery struct{ Limit int }` and write `Query listQuery`): `in.Query.Limit` is 50 when the request omits it, there is no `Or(50)` to disagree with the document, and the API document gives the parameter `default: 50`. Registration holds the default to the field type's own rules, so a `Limit` with `Range() (1, 100)` refuses a default of `"500"` before the first request. It works in `Query`, `Header`, `Cookie` and a `Form[T]`; a path parameter, an `Opt`, a list and a JSON body member have none (see [`Defaults`](../reference/input.md#defaults-method)). Use `Opt` instead when the handler acts on the absence itself.

A list is required too, so a request without `?coupon=` is a 400 unless the field is `Opt[[]string]`. A `json:",omitempty"` tag does not make a request field optional; only `Opt` does.

## Wire names

**One naming policy turns Go field names into the names a client sends.** The default is snake_case: `PageSize` is `page_size`, `ItemID` is `item_id`, `HTTPStatus` is `http_status`. It applies to path, query, cookie and body fields, and error locations use the same names ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md)).

Choose another policy once, for the whole app, with [`WithNaming`](../reference/app.md#withnaming):

<!-- compiles: body -->
```go
camel := bites.New(bites.WithNaming(bites.CamelCase)) // ItemID is itemId, PageSize is pageSize
camel.Post("/shops/{shopId}/orders", placeOrder)
```

[`SnakeCase`](../reference/app.md#snakecase) is the default and [`CamelCase`](../reference/app.md#camelcase) the other built in. A body type that already has `json` tags keeps them, and a tag wins over the policy. Two fields that produce one wire name are refused at Registration. A name no rule produces (`$top`, `page[size]`) comes from a `WireNames() map[string]string` method on the section's struct, which Registration checks against the fields. One app has one policy; an API whose v1 is camelCase and v2 snake_case is two apps.

## Seeing the names a client sends

**Print the table with [`app.WriteWireTable`](../reference/app.md#appwritewiretable) when a name that matters appears nowhere in the source.** With a policy such as `CamelCase`, nobody writes `orgUnitId` in Go, so a review cannot grep for it and a reader cannot see a body's JSON shape without running the app. The method writes one block a route, with a row for each path, query, header and cookie field, each body field (nested ones as `shipTo.city`, lists as `lines[].sku`) and each success response:

<!-- compiles: body -->
```go
if err := app.WriteWireTable(os.Stdout); err != nil {
	log.Fatal(err)
}
```

Call it from a test or a small `main` that builds the app, and keep the output in a golden file: a rename of a field then shows in a diff as a changed wire name, which is what a client breaks on. The table is read from the plans Registration built, with the app's own policy, so it is right for a policy only the application defines, and it is never out of step with the decoder ([ADR 0011](../adr/0011-one-naming-policy-for-wire-names.md)). The [API document](./openapi.md) says the same names too, but only for a route that is documented and in the shape of a schema.

## Reading a JSON body

**A struct, slice or map `Body` is read as JSON when the request is `application/json` or any `*+json` type.** A mistake is a 400 that names the body location, and all of them are listed together ([ADR 0015](../adr/0015-json-is-our-own-codec-on-jsontext.md), [Errors](./errors.md)).

```http
POST /shops/x/orders?express=maybe HTTP/1.1
Content-Type: application/json

{"customer":"Ada","lines":[{"sku":"a","qty":1},{"sku":"b","qty":"two"}],"note":5}

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"path.shop_id","message":"expected an integer"},{"location":"query.express","message":"expected true or false"},{"location":"header.Idempotency-Key","message":"required"},{"location":"body.lines[1].qty","message":"expected an integer"},{"location":"body.note","message":"expected a string"}]}
```

Three more answers are worth knowing:

- **A body whose `Content-Type` the route does not accept is a 415**, including a JSON body sent with none. It is the one input mistake that is not a 400, because the media type is wrong and not the content ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).
- **An empty JSON body is a 400 `required` at `body`.** A `[]byte` body may be empty, and so may a body typed `bites.Opt[T]` (below).
- **A member the Body type does not have is a 400 `unknown field` at `body.<name>`.** A misspelt field is a lost write if it is dropped silently. Opt out per route or group with [`AcceptUnknownMembers`](../reference/input.md#acceptunknownmembers), which a Query section never needs, since it ignores keys it does not know.

<!-- compiles: body -->
```go
app.Post("/shops/{shop_id}/orders", placeOrder, bites.AcceptUnknownMembers())
```

**A type that writes its own JSON must say what it is.** A field whose type has its own `MarshalJSON` or `UnmarshalJSON`, such as pgx's `pgtype.Timestamptz` or a decimal, is read and written by those methods, but the API document cannot look inside a method and would call the field `{}`, which a generated client turns into `unknown`. So Registration refuses it, naming the route and the field, until the app declares the shape once with [`WithJSONSchema`](../reference/app.md#withjsonschema) ([ADR 0076](../adr/0076-a-type-that-writes-its-own-json-is-described-by-a-registration-or-refused.md)):

```go
func newApp() *bites.App {
	return bites.New(
		// Valid=false writes null, so a response says so; a request that may send null is a Null[T].
		bites.WithJSONSchema[pgtype.Timestamptz](bites.JSONSchema{Type: "string", Format: "date-time", Nullable: true}),
		// A column of JSON that the API passes through as it is.
		bites.WithJSONSchema[Settings](bites.JSONSchema{Type: "any"}),
	)
}
```

A `json.RawMessage` field is free-form by name and needs nothing. A type with only a text form is a string already, and takes [`WithTextFormat`](../reference/app.md#withtextformat) when it needs a format.

## A body that may be absent

**`Body bites.Opt[T]` is a JSON body the client may leave out.** `T` is a struct, slice or map. A request with no body leaves the `Opt` unset, and the handler asks `Get`; a request with a body is read as any JSON body, so a required field inside is still required and `{}` is a sent body, not an absent one. In the API document the body is not required ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).

<!-- compiles: decls -->
```go
type RestoreSpace struct {
	Path struct{ ID int64 }
	Body bites.Opt[struct{ Name string }]
}

func restoreSpace(ctx context.Context, in RestoreSpace) (bites.NoContent, error) {
	if b, ok := in.Body.Get(); ok {
		_ = b.Name // the new name the client chose
	}
	return bites.NoContent{}, nil
}
```

An `Opt` of a scalar, of a `Raw`, a `Form` or an `Upload` is refused at Registration. An empty body is not read as `{}` for every route, because that would turn a client mistake into a write with defaults.

## Reading the raw bytes of a body

**The type of `Body` decides how it is read** ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).

| `Body` is | Accepts | Read as |
|---|---|---|
| a struct, slice or map | `application/json` or `*+json` | JSON |
| [`Raw[T]`](../reference/input.md#raw) | the same | JSON, and the exact bytes kept: `in.Body.Bytes()` |
| `[]byte` | any | the bytes as sent |
| [`Form[T]`](../reference/input.md#form) | `application/x-www-form-urlencoded` | form fields |
| [`Upload`](../reference/input.md#upload) | `multipart/form-data` | parts, read by the handler |

`Raw[T]` is for webhooks: Stripe and GitHub sign the exact bytes, and re-encoding the decoded value does not reproduce them. Check [`Raw.Bytes`](../reference/input.md#rawbytes) against the signature. The body is decoded before the handler runs, as every section is, so an unsigned body that is not the type's JSON is a 400 before the handler can check the signature; a caller without the secret learns the body's shape from it, never more. A webhook that must check the signature before anything is decoded takes `Body []byte` and decodes it itself after the check. **The signature header is an `Opt`.** A plain `string` header is required, so a request without it is a 400 naming the header before the handler runs, and the 401 a missing credential should get is never reached; with `Opt[string]` the absent header reaches the handler, which answers 401 for it and for a wrong one alike. A credential the framework can read without the body (a Bearer token, an API key) belongs in an auth Provider instead, which answers 401 on its own ([security](./security.md)).

<!-- compiles: decls -->
```go
type Webhook struct {
	Header struct{ XSignature bites.Opt[string] } // Opt: a request without it reaches the check below
	Body   bites.Raw[struct{ Event string }]
}

type Blob struct {
	Body []byte
}

type Hooks struct{ Secret []byte }

func (h Hooks) Receive(ctx context.Context, in Webhook) (bites.NoContent, error) {
	sig, _ := in.Header.XSignature.Get() // "" when the header is absent, which matches no MAC
	mac := hmac.New(sha256.New, h.Secret)
	mac.Write(in.Body.Bytes()) // exactly what the sender signed
	if !hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return bites.NoContent{}, bites.Unauthorized("bad signature")
	}
	return bites.NoContent{}, nil
}

func store(ctx context.Context, in Blob) (bites.Created[int], error) {
	return bites.Created[int]{Body: len(in.Body)}, nil
}
```

A form is read by `Form[T]` and a multipart body by `Upload`: see [Forms and uploads](./forms-and-uploads.md).

## Limits on the request

**A request is bounded before your code sees it.** A head over 8 KiB is a 431, a body over 1 MiB is a 413, a slow body is a 408, and JSON nested deeper than 64 levels is a 400. All are on by default and change for the app, a group or a route: [`MaxBody`](../reference/input.md#maxbody) and [`BodyTimeout`](../reference/input.md#bodytimeout) are route options, and [`WithLimits`](../reference/app.md#withlimits) sets the app's. See [Limits and timeouts](./limits.md) and [ADR 0024](../adr/0024-limits-are-on-by-default.md).

## Where to go next

- [Validation](./validation.md): rules a type cannot say.
- [Forms and uploads](./forms-and-uploads.md): the other two kinds of body.
- [Errors](./errors.md): the shape of the 400.
