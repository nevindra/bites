# Responses

**The values a handler returns to answer: JSON with a status, text, bytes, a file, a stream or server-sent events, with their headers and cookies.**

**Guide:** [responses](../guide/responses.md) · **Design:** [responses](../design/responses.md)

The response comes only from the return value; a handler never holds a writer ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)). A handler that returns a plain value `T` answers `200` with it as JSON. Anything else is a type the handler returns: a status wrapper (`OK`, `Created`, `Accepted`, `NoContent`, `NotModified`, `SeeOther`, `Redirect`), a body that is not JSON (`Text`, `Bytes`, `File`), or a body written over time (`Stream`, `SSE`, `Events`). Every wrapper has a `Header` and a `Cookies` field. Error statuses never come from the output type; they come from the returned error ([errors](./errors.md)).

A handler that can answer with more than one of these types returns a [`OneOf2`](#oneof2), [`OneOf3`](#oneof3) or [`OneOf4`](#oneof4) of them, which the compiler checks and which costs what a plain return costs; a handler that needs more than four, or an interface it already has, returns that interface and lists each type in [`Returns`](#returns). A pointer to a wrapper is accepted where the type is the wrapper's; a pointer to a `Stream`, `SSE`, `Events` or `File` is refused, and so is any of these as the Body of a wrapper.

Allocations: a plain typed JSON request is 2 (3 when the done channel of its context is used); a `OneOf` return is 2; an interface return with `Returns` is 3; a `Stream`, `SSE` or `Events` is 4 a stream and 0 a write or an event; a `File` answer is 14 to 16 today, a ceiling of 16; a gzip-compressed answer is 2 ([ADR 0003](../adr/0003-the-trade-budget.md)).

What Registration refuses in an Output:

| Mistake | Reason in the message |
|---|---|
| an output JSON cannot write | `the output type cannot be written as JSON: chan int: a field of kind chan cannot be encoded` |
| `*Stream`, `*SSE`, `*Events[T]` | `the handler returns *Stream; a stream is returned by value, because its function and its writer live for one response` |
| a Stream, SSE or Events as a wrapper's Body | `SSE is the Body of OK; a stream is the whole return type, not the body of a wrapper (its status and headers are its own fields)` |
| a HEAD route that returns a stream | `a HEAD route cannot return a stream: its answer has no body to stream` |
| `*File`, or a File as a wrapper's Body | `the handler returns *File; a File is returned by value, because the answer is built from its fields once` |
| an interface output with no `Returns` | see [`Returns`](#returns) |
| a `OneOf` variant that cannot be a return type, or a `OneOf` anywhere but the whole return type | see [`OneOf2`](#oneof2) |

Guide: [responses](../guide/responses.md). Decisions: [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md), [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md), [ADR 0030](../adr/0030-streaming-is-a-return-type.md).

<!-- compiles: decls -->

```go
type Item struct {
	ID   int64
	Name string
}

type CreateItem struct {
	Body struct{ Name string }
}

func createItem(ctx context.Context, in CreateItem) (bites.Created[Item], error) {
	it := Item{ID: 7, Name: in.Body.Name}
	return bites.Created[Item]{
		Body:     it,
		Location: "/items/7",
		Header:   bites.Header{"Cache-Control": {"no-store"}},
		Cookies:  []bites.Cookie{{Name: "last", Value: "7"}},
	}, nil
}
```

## JSON with a status

### `OK`

```go
type OK[T any] struct {
	Body    T
	Header  Header
	Cookies []Cookie
}
```

OK is a 200 with a JSON body and, if given, headers and cookies. A plain `T` is an `OK[T]` with neither.

### `Created`

```go
type Created[T any] struct {
	Body     T
	Location string
	Header   Header
	Cookies  []Cookie
}
```

Created is a 201 with a JSON body and a `Location` header when `Location` is not empty. The API document states the header only for a route registered with [`SetsLocation`](./openapi.md#setslocation), where it is required and an empty `Location` is a logged 500; on any other route it states none, and a `Location` the handler does set is still sent.

### `Accepted`

```go
type Accepted[T any] struct {
	Body    T
	Header  Header
	Cookies []Cookie
}
```

Accepted is a 202 with a JSON body.

### `NoContent`

```go
type NoContent struct {
	Header  Header
	Cookies []Cookie
}
```

NoContent is a 204 with no body.

### `NotModified`

```go
type NotModified struct {
	Header  Header
	Cookies []Cookie
}
```

NotModified is a 304 with no body. Put the validators (`ETag`) the client should keep in `Header`.

### `SeeOther`

```go
type SeeOther struct {
	Location string
	Header   Header
	Cookies  []Cookie
}
```

SeeOther is a 303 pointing at `Location`.

### `Redirect`

```go
type Redirect struct {
	Status   int
	Location string
	Header   Header
	Cookies  []Cookie
}
```

Redirect points at `Location` with `Status`, which must be 301, 302, 307 or 308. Any other value is a 500 and logged, because it can only be a mistake.

## Other bodies

### `Text`

```go
type Text struct {
	Body    string
	Header  Header
	Cookies []Cookie
}
```

Text is a 200 with a plain text body, `Content-Type: text/plain; charset=utf-8`. The body is written as it is, not as a JSON string.

### `Bytes`

```go
type Bytes struct {
	ContentType string
	Body        []byte
	Attachment  string
	Header      Header
	Cookies     []Cookie
}
```

Bytes is a 200 with a body of any content type. The content type is a value of the handler's, so it cannot be checked at Registration; it is checked per response, and an empty one, or one that is not `type/subtype` or holds a control character, is a 500 and logged. The bytes are copied into the response before the handler's return value is gone. In the API document its content is `*/*` with no schema.

`Attachment`, when it is not empty, makes the answer a download saved under that name, as on a [`File`](#file).

### `Page`

```go
type Page struct {
	Status  int
	Body    []byte
	Header  Header
	Cookies []Cookie
}
```

Page is an HTML page for a browser, sent as `text/html; charset=utf-8` with `Status`: 0 or 200, or a client error from 400 to 499. It is the one answer that carries an error status in place of a returned error, for a form shown again with its mistakes (422) or a page a browser opens that is not there (404); an API's error is still a returned error, answered as problem+json. `Body` is copied into the response before the handler's return value is gone, so render a template into a buffer and give its bytes. The zero value is an empty 200 page.

Checked per response, since the values are the handler's: a `Status` of 401 or 407 is a logged 500 (`a Page has status 401, which must name a challenge (WWW-Authenticate) that a page does not have: show the form again with 422, or return Unauthorized for an API`), and so is a 3xx, a 5xx or a 2xx other than 200, and a `Content-Type` in `Header` (`a Page has a Content-Type in its Header, but a Page is text/html; send another media type as a Bytes`). Refused at Registration: a Page as the Body of a status wrapper (`Page is the Body of OK; a Page is the whole return type, not the body of a wrapper (its status, headers and cookies are its own fields)`). In the API document a Page is a `200` and a `4XX` answer of `text/html`. Allocations: none of its own, as a `Bytes`. Decision: [ADR 0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md). Guide: [forms and uploads](../guide/forms-and-uploads.md#showing-a-form-again-with-its-mistakes).

<!-- compiles: body -->
```go
app.Get("/missing", func(ctx context.Context, in struct{}) (bites.Page, error) {
	return bites.Page{Status: 404, Body: []byte("<h1>Not here</h1>")}, nil
})
```

### `File`

```go
type File struct {
	Root        *os.Root
	FS          fs.FS
	Name        string
	ContentType string
	Attachment  string
	Header      Header
	Cookies     []Cookie
}
```

File is a 200 whose body is a file, sent with its `Content-Length` and `Last-Modified` and read in chunks as it is written: a file is never held whole in memory.

- `Root` and `FS`: exactly one says where the file is. Both set, or neither, is a 500 and logged.
- `Name` is the path inside it, slash separated. A `Root` is an `os.Root`, which cannot leave its directory: a `Name` with `..`, a symbolic link that points out and an absolute `Name` are refused by the operating system call. An `FS` is checked with `fs.ValidPath` first. A `Name` that is not valid, a file that does not exist, or one that is not a regular file is the same 404 problem+json for each, so an answer does not say which files exist.
- `ContentType` is used when given; otherwise the type of the extension of `Name`, and `application/octet-stream` when that has none. A `Content-Type` in `Header` replaces both.
- `Attachment`, when it is not empty, makes the answer a download saved under that name: `Content-Disposition: attachment` with the name written by the standard library's `mime.FormatMediaType`, quoted, and as `filename*` (RFC 2231) when it is outside ASCII or holds a line break or invalid UTF-8, so no name can split the response. Only the part after the last `/` or `\` is used, so `Name` itself can be given (`reports/q3.pdf` is saved as `q3.pdf`); a name with nothing left (`dir/`, `.`, `..`) sends `attachment` alone. It is sent on a 200 and a 206, not on a 304. A `Content-Disposition` in `Header` replaces it. Empty sends none, and the browser shows the file when it can.
- A HEAD request gets the head with the same `Content-Length` and no body.
- Validators: a file with a modification time is sent with `Last-Modified` and a strong `ETag` of that time in nanoseconds and the size, both in hex (`"18e3f01f9870d1c7-493e0"`); a file with none (an `embed.FS`) gets neither. An `ETag` or `Last-Modified` in `Header` replaces the made one in the answer and in every comparison; an `ETag` there that is not an entity-tag is a 500, logged.
- Conditional requests, in RFC 9110's order: a failed `If-Match` or `If-Unmodified-Since` is a 412 problem+json; a matching `If-None-Match`, or an `If-Modified-Since` at or after the file's time, is a 304 with the validators and the cache headers of `Header` and nothing opened (a 412 on a method other than GET and HEAD).
- Ranges: a GET with one byte range is a 206 with `Content-Range`, or a 416 problem+json with `Content-Range: bytes */size` when it starts past the end. Several ranges, another unit, a range that does not parse, a range on HEAD, and one whose `If-Range` no longer names the file are ignored and the whole file sent. Every 200 says `Accept-Ranges: bytes`; `Accept-Ranges: none` in `Header` turns ranges off. Decision: [ADR 0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md).
- A file whose size differs from the first stat when it is opened is a 500; one that shrinks while being sent closes the connection after the bytes that existed.
- Middleware sees a File as a `Response` with `Streaming()` true and `Size()` 0.

Guide: [files](../guide/files.md).

## Static files

A directory of files served under a prefix, every file answered as a [`File`](#file) is. Decision: [ADR 0065](../adr/0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md).

### `Static`

```go
type Static struct {
	Root      *os.Root
	FS        fs.FS
	Index     string
	Fallback  string
	MaxAge    time.Duration
	Immutable func(name string) bool
}
```

Static is the directory and its rules.

- `Root` and `FS`: exactly one says where the files are; an `embed.FS`, and any `fs.FS` whose files have no modification time (`fs.Sub` of an `embed.FS`), is read once at Registration, each file hashed into a strong `ETag` and its twins known, so a request makes no stat. Any other source is looked at on each request.
- `Index` is the file answered for a path that ends in a slash, the bare prefix included; `index.html` when empty. A directory named without its slash is a 404, never a redirect, and no directory is listed.
- `Fallback` is the file a single-page application answers for a path that names no file. It is answered only for a GET or HEAD whose `Accept` lists `text/html`, whose last segment has no extension, and whose first segment under the prefix is not one another route of the app uses, so an API's unknown path keeps its 404. Empty is no fallback.
- `MaxAge` is the `max-age` of a name with no content hash, in whole seconds; zero sends `no-cache`. A name with a content hash is always `max-age=31536000, immutable`, and the index and fallback always `no-cache`.
- `Immutable` reports whether a name holds a content hash; nil is the default rule: one part of the base name, between dots or dashes, of 8 to 64 characters that is lower-case hex with a digit and a letter, or letters and digits with an upper-case letter and a digit (`app-3f2a1b9c.js`, `index-BxR3k9aQ.js`, `chunk-ABCD1234.js`).

Precompressed twins: `app.js.br`, `app.js.zst` and `app.js.gz` beside `app.js` are answered by the weights of `Accept-Encoding` (br, then zstd, then gzip on a tie), with `Content-Encoding` and the type of `app.js`; every answer of a type that may have one (not an image, a video, a font or an archive) says `Vary: Accept-Encoding`. Which twins a file of a changing source has is remembered per version of the file (its size and time). Every missing, invalid or forbidden name is the same 404 problem+json as `File`'s.

### `App.Static`

```go
func (a *App) Static(prefix string, s Static, opts ...RouteOption)
```

Static registers `GET prefix/{path...}` (and HEAD) serving `s`: `prefix` is `/` for the root of the app, or a path that starts with a slash, does not end with one and has no wildcard. A more specific route wins over it, it runs the app's middleware and those given with `Use`, and it is left out of the API document. Refused at Registration, each a row of the refusal table:

| Mistake | Message (after `Static <prefix>: `) |
|---|---|
| both or neither of `Root` and `FS` | `give exactly one of Root and FS` |
| a prefix that is not `/` and has no leading slash, a trailing slash or a wildcard | `the prefix is "/" or starts with a slash, does not end with one, and has no wildcard` |
| an `Index` with a slash | `Index "pages/index.html" is not a plain file name: give a name with no slash, such as index.html` |
| a `Fallback` that is not a valid name | `Fallback "../index.html" is not a file name inside the directory` |
| a `Fallback` that is not a regular file in the directory | `Fallback "index.html" is not a regular file in the directory: a fallback that does not exist would answer every navigation with a 404` |
| a negative `MaxAge`, or one under a second | `MaxAge(500ms) is not a cache lifetime: give whole seconds, or zero for no-cache` |
| `Summary`, `Description` or `Returns` | `Summary, Description and Returns describe an operation of the API document, which leaves static files out; Tag is accepted and ignored` |

A static route that conflicts with another route is refused by the router, as any route is.

<!-- compiles: body -->

```go
app.Static("/", bites.Static{FS: os.DirFS("dist"), Fallback: "index.html"})
```

### `Group.Static`

```go
func (g *Group) Static(prefix string, s Static, opts ...RouteOption)
```

Static is `App.Static` under the group's prefix, with the group's options and middleware.

## Headers and cookies

### `Header`

```go
type Header map[string][]string
```

Header is the response headers a wrapper carries. The framework adds `Date`, `Content-Length` and `Connection` itself, and ignores those names (and `Transfer-Encoding`) if a handler sets them, because two of them would make the response unparseable. A `Content-Type` set here replaces the JSON one. A header whose name is decided at run time cannot appear in the API document.

### `Header.Add`

```go
func (h Header) Add(name, value string)
```

Add appends a value to `name`. The receiver must not be nil.

### `Header.Set`

```go
func (h Header) Set(name, value string)
```

Set replaces `name`'s values with one. The receiver must not be nil.

### `Cookie`

```go
type Cookie struct {
	Name, Value string
	// Path defaults to "/".
	Path   string
	Domain string
	// MaxAge is how long the browser keeps it; zero makes a session cookie and a negative value
	// deletes the cookie now.
	MaxAge  time.Duration
	Expires time.Time
	// SameSite defaults to Lax.
	SameSite SameSite
	// NotSecure drops the Secure attribute.
	NotSecure bool
	// NotHttpOnly drops HttpOnly, letting scripts in the page read the cookie.
	NotHttpOnly bool
}
```

Cookie is a cookie a response sets, with safe defaults: `HttpOnly`, `Secure`, `SameSite=Lax` and `Path=/`. Loosening any of them is written out (`NotSecure`, `NotHttpOnly`, `SameSite`), so a cookie that browser scripts can read says so in the code that sets it. Browsers treat localhost as a secure context, so `Secure` does not get in the way of development over plain HTTP. A cookie is read back by a field of the `Cookie` Section ([input](./input.md#the-sections)).

Decision: [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md).

### `SameSite`

```go
type SameSite uint8
```

SameSite is a cookie's `SameSite` attribute. The zero value is `Lax`.

### `Lax`

```go
const Lax SameSite = iota
```

Lax is the default: the cookie is sent on top-level navigations and same-site requests.

### `Strict`

```go
const Strict SameSite = 1
```

Strict withholds the cookie from every cross-site request.

### `NoSameSite`

```go
const NoSameSite SameSite = 2
```

NoSameSite sends the cookie cross-site; browsers require `Secure` with it.

## More than one answer

### `OneOf2`

```go
type OneOf2[A, B any] struct {
	// unexported: which variant is chosen, and each variant's value
}
```

OneOf2 is the return type of a handler that answers with `A` or `B`, each one of the types a handler may return alone: a plain value, a status wrapper, `Text`, `Bytes`, `File`, `Stream` or `SSE`. The handler chooses the answer with the method of the variant's letter, on any value of the type, usually the zero one; an alias names the type once. Registration compiles each variant as if the handler returned it alone, and the API document lists each variant's status and schema as [`Returns`](#returns) does (two variants with one status share a response, a `oneOf` when their schemas differ). The compiler refuses a handler that returns anything else, so there is no `Returns` to keep in step with the code.

The variant is held in the OneOf, not boxed in an interface: a OneOf return costs 2 allocations, what a plain return costs ([ADR 0003](../adr/0003-the-trade-budget.md)). Its size is the sum of its variants' sizes; it lives on the handler's stack.

A OneOf with no variant chosen (its zero value) returned with a nil error is a 500, logged with the method and path: `the handler returned a OneOf2 with no variant chosen and no error: choose one with the method of its letter, as in OneOf2{}.A(v)`. Returned with an error, the error answers and the OneOf is not read.

Refused at Registration:

| Mistake | Message (after the route label) |
|---|---|
| a variant JSON cannot write | `variant A of the OneOf2, chan int: the output type cannot be written as JSON: chan int: a field of kind chan cannot be encoded` (any refusal of a return type, after the variant's letter) |
| one type named twice | `variants A and C of the OneOf3 are both bites_test.User: each variant is a different answer, so name each type once` |
| a OneOf as a variant | `variant B of the OneOf2 is itself a OneOf2: list its types in the outer OneOf, which has room for up to four` |
| a pointer variant | `variant A of the OneOf2 is *bites_test.User, a pointer: name bites_test.User, which the OneOf holds by value` |
| an interface variant | `variant B of the OneOf2 is interface {}, an interface: a OneOf names the concrete types the handler returns` |
| a stream variant on a HEAD route | `variant B of the OneOf2, bites.SSE: a HEAD route cannot return a stream: its answer has no body to stream` |
| `Returns` on a route that returns a OneOf | `Returns[bites_test.User] is given, but the handler returns a OneOf2, whose type arguments already say what it may return: drop the Returns` |
| a OneOf given to `Returns` | `Returns[bites.OneOf2[...]] names a OneOf; make the OneOf2 the handler's return type, which needs no Returns` |
| a pointer to a OneOf as the return type | `the handler returns a pointer to a OneOf2; a OneOf is returned by value, its variant chosen with the method of its letter` |
| a OneOf as a wrapper's Body | `OneOf2 is the Body of OK; a OneOf is the whole return type, and each of its variants is a wrapper of its own` |
| a OneOf inside a returned value | `the output type oneOfHolder holds a OneOf2; a OneOf is the handler's whole return type, not a value written as JSON` |
| a OneOf in a request body | `OneOfBodyIn.Body holds a OneOf2; a OneOf is a handler's return type, not a request body` |
| a OneOf a Socket sends, or in an Inbox's message | `the handler's Socket sends OneOf2[...], one of the framework's response types; ...`; `the Inbox's message type holds a OneOf2; a OneOf is a handler's return type, not a WebSocket message` |

<!-- compiles: decls -->

```go
type Doc struct {
	ID   string
	Text string
}

type GetDoc struct {
	Path   struct{ ID string }
	Header struct{ IfNoneMatch bites.Opt[string] }
}

type DocAnswer = bites.OneOf2[bites.OK[Doc], bites.NotModified]

func getDoc(ctx context.Context, in GetDoc) (DocAnswer, error) {
	etag := `"v1"`
	if in.Header.IfNoneMatch.Or("") == etag {
		return DocAnswer{}.B(bites.NotModified{Header: bites.Header{"ETag": {etag}}}), nil
	}
	return DocAnswer{}.A(bites.OK[Doc]{Body: Doc{ID: in.Path.ID}, Header: bites.Header{"ETag": {etag}}}), nil
}

func registerDoc(app *bites.App) {
	app.Get("/docs/{id}", getDoc)
}
```

### `OneOf2.A`

```go
func (OneOf2[A, B]) A(v A) OneOf2[A, B]
```

A returns a OneOf2 that answers with `v`. The receiver is not read.

### `OneOf2.B`

```go
func (OneOf2[A, B]) B(v B) OneOf2[A, B]
```

B returns a OneOf2 that answers with `v`.

### `OneOf2.Value`

```go
func (o OneOf2[A, B]) Value() any
```

Value returns the variant `o` holds, boxed in an interface, or nil when none is chosen. It is for a test that calls a handler directly and switches on the type of its answer; the engine reads a OneOf without it.

### `OneOf3`

```go
type OneOf3[A, B, C any] struct {
	// unexported
}
```

OneOf3 is [`OneOf2`](#oneof2) with three variants.

### `OneOf3.A`

```go
func (OneOf3[A, B, C]) A(v A) OneOf3[A, B, C]
```

A returns a OneOf3 that answers with `v`.

### `OneOf3.B`

```go
func (OneOf3[A, B, C]) B(v B) OneOf3[A, B, C]
```

B returns a OneOf3 that answers with `v`.

### `OneOf3.C`

```go
func (OneOf3[A, B, C]) C(v C) OneOf3[A, B, C]
```

C returns a OneOf3 that answers with `v`.

### `OneOf3.Value`

```go
func (o OneOf3[A, B, C]) Value() any
```

Value returns the variant `o` holds, or nil when none is chosen.

### `OneOf4`

```go
type OneOf4[A, B, C, D any] struct {
	// unexported
}
```

OneOf4 is [`OneOf2`](#oneof2) with four variants. Go has no variadic type parameters, so each count is a type of its own, and four is the last: a handler with more answers than that returns an interface with [`Returns`](#returns).

### `OneOf4.A`

```go
func (OneOf4[A, B, C, D]) A(v A) OneOf4[A, B, C, D]
```

A returns a OneOf4 that answers with `v`.

### `OneOf4.B`

```go
func (OneOf4[A, B, C, D]) B(v B) OneOf4[A, B, C, D]
```

B returns a OneOf4 that answers with `v`.

### `OneOf4.C`

```go
func (OneOf4[A, B, C, D]) C(v C) OneOf4[A, B, C, D]
```

C returns a OneOf4 that answers with `v`.

### `OneOf4.D`

```go
func (OneOf4[A, B, C, D]) D(v D) OneOf4[A, B, C, D]
```

D returns a OneOf4 that answers with `v`.

### `OneOf4.Value`

```go
func (o OneOf4[A, B, C, D]) Value() any
```

Value returns the variant `o` holds, or nil when none is chosen.

Guide: [responses](../guide/responses.md). Decision: [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md).

## Interface returns

### `Returns`

```go
func Returns[T any]() RouteOption
```

Returns declares a type that a handler returning an interface may return; it is the fallback for a handler with more answers than [`OneOf4`](#oneof4) holds, or one that already returns an interface of its own. A route whose output is an interface needs one `Returns` for each type it may hold: the answer of each is compiled at Registration, and the API document gets its status and schema (two types with one status share a response, a `oneOf` when their schemas differ). `T` is the type itself (`OK[User]`, `Created[User]`, `NoContent`, a plain value); a handler may return it by value or by pointer. At run time a handler that returns an undeclared type gets a logged 500 naming the `Returns` to add.

Allocations: 3, the two of a plain output plus the one the handler makes boxing its wrapper ([ADR 0003](../adr/0003-the-trade-budget.md)).

Refused at Registration:

| Mistake | Message (after the route label) |
|---|---|
| an interface output with no `Returns` | `the handler returns interface {}, an interface, and the route has no Returns: declare each type it may return with Returns[T](), so the API description has its statuses and a wrong type is refused now and not answered with a 500` |
| `T` does not implement the output | `Returns[bites_test.Empty]: bites_test.Empty does not implement bites_test.namer, the type the handler returns` |
| the same `T` twice | `Returns[bites_test.User] is given twice` |
| the output is not an interface | `Returns[bites_test.User] is given, but the handler returns bites_test.User, which is not an interface: Returns declares what an interface return may hold` |
| given to a group | `Returns[bites_test.User] is given to a group, but each route's handler returns its own types: give it to the route` |
| `T` a pointer or an interface | `Returns[*bites_test.User] names a pointer; declare bites_test.User, which the handler may return by value or by pointer` |
| `T` a OneOf, or `Returns` on a route that returns one | see [`OneOf2`](#oneof2) |
| `T` a stream on a HEAD route, or on a WebSocket route | the HEAD refusal above; `Returns lists the answers of a handler that returns an interface, but a WebSocket handler returns an Inbox and answers 101: its messages are the Socket's and the Inbox's types` |

<!-- compiles: decls -->

```go
type Maybe interface{ maybe() }

type Found struct{ Name string }

func (Found) maybe() {}

type Gone struct{}

func (Gone) maybe() {}

func lookup(ctx context.Context, in struct{}) (Maybe, error) {
	return Found{Name: "x"}, nil
}

func registerLookup(app *bites.App) {
	app.Get("/lookup", lookup, bites.Returns[Found](), bites.Returns[Gone]())
}
```

Guide: [responses](../guide/responses.md). Decision: [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md).

## Streams

### `Stream`

```go
type Stream struct {
	Status     int
	Attachment string
	Header     Header
	Cookies    []Cookie
	// Live declares a stream with no end of its own (an NDJSON feed): Shutdown cancels its context
	// with ErrServerClosed instead of waiting for it. SSE is live by construction. A stream that is
	// not Live (a download) is drained.
	Live bool
	Func func(ctx context.Context, w *StreamWriter) error
}
```

Stream is a response whose body is written over time: a large download, NDJSON, a feed. The handler returns it with the status and headers fixed, and `Func` writes the body. The headers are fixed before the first byte, so they cannot be set late.

- `Func` runs after the handler and after every middleware has returned, on the connection's goroutine, with the request's context, which is cancelled if the client leaves ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)).
- It ends the response by returning: nil ends it cleanly (the terminating chunk). An error returned after the first byte, or a panic, closes the connection with no terminating chunk, so a client cannot mistake a cut stream for a complete one. An error or a panic before the first byte is the ordinary answer to an error or a panic.
- The body is chunked on HTTP/1.1; an HTTP/1.0 client gets the body until the connection closes. A HEAD request gets the head and no body, and `Func` does not run.
- `Status` is 200 when zero and must be a status that has a body: 1xx, 204 and 304 are a 500. A nil `Func` is a 500, logged. A `Content-Type` in `Header` is used; without one the stream is `application/octet-stream`.
- `Attachment`, when it is not empty, makes the answer a download saved under that name, as on a [`File`](#file).
- Middleware sees a Stream as a `Response` with `Streaming()` true and `Size()` 0, and a `Timeout` bounds only the time to produce it ([middleware](./middleware.md#timeout)).

Allocations: 4 a stream, 0 a write ([ADR 0003](../adr/0003-the-trade-budget.md)).

Guide: [streaming and SSE](../guide/streaming.md). Decision: [ADR 0030](../adr/0030-streaming-is-a-return-type.md).

<!-- compiles: decls -->

```go
func feed(ctx context.Context, in struct{}) (bites.Stream, error) {
	return bites.Stream{
		Header: bites.Header{"Content-Type": {"application/x-ndjson"}},
		Live:   true,
		Func: func(ctx context.Context, w *bites.StreamWriter) error {
			for i := 0; i < 3; i++ {
				if _, err := fmt.Fprintf(w, "{\"n\":%d}\n", i); err != nil {
					return err
				}
				if err := w.Flush(); err != nil {
					return err
				}
			}
			return nil
		},
	}, nil
}
```

### `StreamWriter`

```go
type StreamWriter = engine.StreamWriter
```

StreamWriter is the writer a Stream's function receives. It is valid only while that function runs: once the function has returned, `Write`, `WriteString` and `Flush` fail with [`ErrStreamClosed`](#errstreamclosed) and touch nothing, so a goroutine that kept the writer cannot write into whatever connection owns it next ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). It is safe for concurrent use.

Methods: `Write(p []byte) (int, error)`, `WriteString(s string) (int, error)`, `Flush() error`, `ReadFrom(r io.Reader) (int64, error)` (so `io.Copy` into it copies once), and `Live() bool`. Writes are buffered up to 64 KiB and sent as one chunk when the buffer fills, on `Flush`, and when the function returns. Nothing is sent before the first of those, not even the headers, so an error or a panic before it is still an ordinary 500. A write that fails (the client left, or took longer than `Limits.WriteTimeout` for one chunk) fails every later write too.

### `ErrStreamClosed`

```go
var ErrStreamClosed = engine.ErrStreamClosed
```

ErrStreamClosed is returned by a StreamWriter after its function has returned. Test with `errors.Is(err, bites.ErrStreamClosed)`.

## Server-sent events

### `SSE`

```go
type SSE struct {
	Header  Header
	Cookies []Cookie
	Func    func(ctx context.Context, e EventWriter) error
}
```

SSE is a Server-Sent Events response: a Stream, live by construction, with the content type `text/event-stream` and `Cache-Control: no-cache` unless `Header` says otherwise, whose function writes events and not bytes. Its status is 200. `Shutdown` cancels its context with `ErrServerClosed`, and the stream then ends cleanly with its terminating chunk. A function that has sent nothing yet still ends as a 200 with an empty event stream. In the API document its content is `text/event-stream`.

Guide: [streaming and SSE](../guide/streaming.md).

### `Event`

```go
type Event struct {
	// ID becomes the Last-Event-ID the browser sends when it reconnects.
	ID string
	// Name is the event type; the browser's default, "message", when empty.
	Name string
	Data string
	// Retry is how long the browser waits before reconnecting, in whole milliseconds; zero sends
	// nothing.
	Retry time.Duration
}
```

Event is one Server-Sent Event. `Data` may hold line breaks: each line is sent as its own `data` field and the browser joins them again. An event with an empty `Data` and a `Name` or an `ID` still sends one empty `data` field, which dispatches it; an event with only a `Retry` sends no data and dispatches nothing.

### `EventWriter`

```go
type EventWriter struct {
	// Has unexported fields.
}
```

EventWriter writes the events of an SSE response. It is valid only while the `SSE` function runs, like the StreamWriter under it. Each `Send` and `Comment` goes out at once.

### `EventWriter.Send`

```go
func (e EventWriter) Send(ev Event) error
```

Send writes `ev` and flushes it. It fails with [`ErrBadEvent`](#errbadevent) for an `ID` or `Name` that cannot be one, and with the writer's error once the client is gone or too slow (`ErrStreamClosed` after the function returned).

### `EventWriter.Comment`

```go
func (e EventWriter) Comment(text string) error
```

Comment writes a comment line, which the browser ignores: the heartbeat that keeps a proxy from closing an idle stream. Line breaks in `text` start further comment lines.

### `Events`

```go
type Events[T any] struct {
	Header  Header
	Cookies []Cookie
	Func    func(ctx context.Context, e EventSender[T]) error
}
```

Events is a Server-Sent Events response whose events carry a `T`, written as JSON: an `SSE` whose function sends values and not strings. `T` is known at Registration, so its encoder is compiled there with the framework's codec and the field names of [`WithNaming`](./app.md#withnaming), a `T` that cannot be written as JSON is refused there, and the API document states `T`'s schema as the data of every event. Like an `SSE` it is live, it is `text/event-stream` with `Cache-Control: no-cache` unless `Header` says otherwise, its status is 200, and `Shutdown` ends it cleanly with its terminating chunk. A `Func` that is nil is a 500, logged. It may be one of the types an interface return declares with [`Returns`](#returns).

In the API document its content is `text/event-stream` with the schema `{"type": "string"}` and, under the extension `x-itemSchema`, one event: an object of `id`, `event`, `retry` and a required `data`, a string whose `contentMediaType` is `application/json` and whose `contentSchema` is `T`. It is the shape OpenAPI 3.2 gives an event under `itemSchema`, which 3.1 has no keyword for. An interface return that holds two `Events` states both under `oneOf`; one that also holds an `SSE` states no item, since an `SSE`'s data is any text.

Allocations: 4 a stream, as for an `SSE`, and 0 an event; the runner carries a 512-byte buffer the events are built in, so a live `Events` holds about 600 B more than an `SSE` ([ADR 0030](../adr/0030-streaming-is-a-return-type.md)).

Refused at Registration:

| Mistake | Reason in the message |
|---|---|
| `*Events[T]` | `the handler returns *Events[User]; a stream is returned by value, because its function and its writer live for one response` |
| an `Events` as a wrapper's Body | `Events[User] is the Body of OK; a stream is the whole return type, not the body of a wrapper (its status and headers are its own fields)` |
| a HEAD route that returns an `Events` | `a HEAD route cannot return a stream: its answer has no body to stream` |
| a `T` JSON cannot write | `the handler returns Events[chan int], whose event type cannot be written as JSON: chan int: a field of kind chan cannot be encoded` |
| `Events[[]byte]` | `the handler returns Events[[]byte]; an event's data is text, and []byte would be sent as base64 JSON: send bytes as text with an SSE and its EventWriter` |
| a `T` that is one of the framework's response types | `the handler returns Events[Text]; Text is one of the framework's response types, and an event's data is a plain value, written as JSON, with no status or headers` |

Guide: [streaming and SSE](../guide/streaming.md#sending-typed-events).

### `EventSender`

```go
type EventSender[T any] struct {
	// Has unexported fields.
}
```

EventSender sends the events of an `Events` response. It is valid only while the `Events` function runs, like the StreamWriter under it, and it is safe for concurrent use: each event is built and written whole. Each `Send`, `SendEvent` and `Comment` goes out at once. A sender kept past its function, and the zero `EventSender`, return `ErrStreamClosed`.

### `EventSender.Send`

```go
func (e EventSender[T]) Send(name string, v T) error
```

Send writes `v` as the data of one event of type `name` (`""` is the browser's default, `message`) and flushes it. `v` is one line of JSON, so it is one `data` field; a line break a `MarshalJSON` wrote between tokens is sent as a space, which reads back to the same value. It fails with [`ErrBadEvent`](#errbadevent) for a `name` with a line break, with the encoder's error for a value JSON cannot hold (a NaN, a cycle), sending nothing, and with the writer's error once the client is gone or too slow.

### `EventSender.SendEvent`

```go
func (e EventSender[T]) SendEvent(ev Event, v T) error
```

SendEvent is `Send` with the `ID`, `Name` and `Retry` of `ev`, written in the order `id`, `event`, `retry`, `data`: the `ID` becomes the `Last-Event-ID` the browser sends when it reconnects. `ev.Data` must be empty, since the data is `v`; an `Event` with `Data` is an [`ErrBadEvent`](#errbadevent) and nothing is sent.

### `EventSender.Comment`

```go
func (e EventSender[T]) Comment(text string) error
```

Comment writes a comment line, which the browser ignores, and flushes it: the heartbeat that keeps a proxy from closing an idle stream, as [`EventWriter.Comment`](#eventwritercomment).

### `ErrBadEvent`

```go
var ErrBadEvent = errors.New("bites: an event id or name contains a line break")
```

ErrBadEvent is returned by `EventWriter.Send`, and by an `EventSender`, for an `ID` or `Name` with a line break (or a NUL in the `ID`), which would end the field and start another one of the sender's choosing. `EventSender.SendEvent` returns it wrapped for an `Event` that has `Data` of its own, with a message that says so. Test with `errors.Is(err, bites.ErrBadEvent)`.

<!-- compiles: decls -->

```go
func ticks(ctx context.Context, in struct{}) (bites.SSE, error) {
	return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
		for i := 0; ; i++ {
			if err := e.Send(bites.Event{ID: strconv.Itoa(i), Name: "tick", Data: "tock"}); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}}, nil
}
```
