# Responses

**A handler answers with the value it returns: a plain value is a 200 with JSON, and a wrapper type chooses another status and carries headers and cookies.**

**Reference:** [Responses](../reference/responses.md) · **Design:** [Responses](../design/responses.md)

## Answering with a plain value

**A handler that returns a value `T` answers 200, with `T` as JSON and `Content-Type: application/json`.** A plain `T` is the same as [`OK[T]`](../reference/responses.md#ok) with no headers or cookies. There is no content negotiation: a typed value is always JSON ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md), [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)). The running example of this page is a document:

<!-- compiles: decls -->
```go
type Doc struct {
	ID    int64
	Title string
}

type GetDoc struct {
	Path   struct{ ID int64 }
	Header struct{ IfNoneMatch bites.Opt[string] }
}

type CreateDoc struct {
	Body struct{ Title bites.NonEmpty }
}

func createDoc(ctx context.Context, in CreateDoc) (bites.Created[Doc], error) {
	doc := Doc{ID: 42, Title: string(in.Body.Title)}
	return bites.Created[Doc]{Body: doc, Location: "/docs/42"}, nil
}
```

## Answering 201 with a Location

**[`Created`](../reference/responses.md#created) is a 201 with a JSON body and a `Location` header when `Location` is not empty.** The API document states the header only for a route that says it always sets one with [`SetsLocation()`](../reference/openapi.md#setslocation), where it is required and an empty `Location` is a logged 500.

```http
POST /docs HTTP/1.1
Content-Type: application/json

{"title":"Plan"}

HTTP/1.1 201 Created
Location: /docs/42
Content-Type: application/json

{"id":42,"title":"Plan"}
```

## The other status wrappers

**Each wrapper is a type with the fields its status needs, plus `Header` and `Cookies`.** A handler that returns one has its status declared in its type, so the API description can state it ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).

| Wrapper | Status | Fields beyond `Header` and `Cookies` |
|---|---|---|
| [`OK[T]`](../reference/responses.md#ok) | 200 | `Body` |
| [`Created[T]`](../reference/responses.md#created) | 201 | `Body`, `Location` |
| [`Accepted[T]`](../reference/responses.md#accepted) | 202 | `Body` |
| [`NoContent`](../reference/responses.md#nocontent) | 204 | none, no body |
| [`NotModified`](../reference/responses.md#notmodified) | 304 | none, no body; put the `ETag` in `Header` |
| [`SeeOther`](../reference/responses.md#seeother) | 303 | `Location` |
| [`Redirect`](../reference/responses.md#redirect) | 301, 302, 307 or 308 | `Status`, `Location` |

A `Redirect` with any other status is a 500, logged, because it can only be a mistake. The framework never answers an error status from a wrapper: error statuses come from the returned error ([Errors](./errors.md)).

<!-- compiles: decls -->
```go
func startExport(ctx context.Context, in struct{}) (bites.Accepted[string], error) {
	h := bites.Header{}
	h.Set("Location", "/exports/9")
	return bites.Accepted[string]{Body: "queued", Header: h}, nil
}

func afterLogin(ctx context.Context, in struct{}) (bites.SeeOther, error) {
	return bites.SeeOther{Location: "/home"}, nil
}

func movedPermanently(ctx context.Context, in struct{}) (bites.Redirect, error) {
	return bites.Redirect{Status: 301, Location: "/docs"}, nil
}
```

## Setting headers

**`Header` on any wrapper is a map of response headers, with [`Header.Set`](../reference/responses.md#headerset) to replace a name and [`Header.Add`](../reference/responses.md#headeradd) to append.** The map must not be nil when you call its methods; a literal works as well. The framework owns `Date`, `Content-Length`, `Connection` and `Transfer-Encoding` and ignores them if you set them, because two of them would make the response unparseable. A `Content-Type` here replaces the default ([`Header`](../reference/responses.md#header)).

<!-- compiles: decls -->
```go
type DocAnswer = bites.OneOf2[bites.OK[Doc], bites.NotModified]

func getDoc(ctx context.Context, in GetDoc) (DocAnswer, error) {
	const etag = `"v1"`
	if in.Header.IfNoneMatch.Or("") == etag {
		return DocAnswer{}.B(bites.NotModified{Header: bites.Header{"ETag": {etag}}}), nil
	}
	h := bites.Header{}
	h.Set("ETag", etag)
	h.Set("Cache-Control", "max-age=60")
	return DocAnswer{}.A(bites.OK[Doc]{Body: Doc{ID: in.Path.ID, Title: "Plan"}, Header: h}), nil
}
```

```http
GET /docs/42 HTTP/1.1

HTTP/1.1 200 OK
Cache-Control: max-age=60
ETag: "v1"
Content-Type: application/json

{"id":42,"title":"Plan"}
```

A header whose name is chosen at run time cannot appear in the API description; a route that sends one declares it as a registration option ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).

## Setting a cookie

**A [`Cookie`](../reference/responses.md#cookie) defaults to `HttpOnly`, `Secure`, `SameSite=Lax` and `Path=/`, and loosening any of them is written out.** A cookie that scripts in the page can read says so in the code that sets it: `NotHttpOnly`, `NotSecure`, or a different [`SameSite`](../reference/responses.md#samesite) of [`Lax`](../reference/responses.md#lax), [`Strict`](../reference/responses.md#strict) or [`NoSameSite`](../reference/responses.md#nosamesite). Browsers treat `localhost` as a secure context, so `Secure` does not get in the way of development ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

<!-- compiles: decls -->
```go
func login(ctx context.Context, in struct{}) (bites.SeeOther, error) {
	return bites.SeeOther{
		Location: "/home",
		Cookies: []bites.Cookie{
			{Name: "sid", Value: "abc123", MaxAge: 24 * time.Hour},
			{Name: "flash", Value: "welcome", MaxAge: 5 * time.Second, NotHttpOnly: true},
		},
	}, nil
}
```

```http
HTTP/1.1 303 See Other
Location: /home
Set-Cookie: sid=abc123; Path=/; Max-Age=86400; HttpOnly; Secure; SameSite=Lax
Set-Cookie: flash=welcome; Path=/; Max-Age=5; Secure; SameSite=Lax
```

`MaxAge` zero makes a session cookie, and a negative value deletes the cookie now. Reading a cookie is a section of the Input ([Requests](./requests.md#reading-a-header-or-a-cookie)).

## Answering with text or bytes

**[`Text`](../reference/responses.md#text) is a 200 with a plain text body, and [`Bytes`](../reference/responses.md#bytes) is a 200 with any content type.** The body is copied as it is, never encoded as JSON. A `Bytes` content type is the handler's value, so Registration cannot see it: an empty one, one that is not `type/subtype`, or one with a control character is a 500, logged, per response ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

<!-- compiles: decls -->
```go
func health(ctx context.Context, in struct{}) (bites.Text, error) {
	return bites.Text{Body: "ok"}, nil // text/plain; charset=utf-8
}

func exportCSV(ctx context.Context, in struct{}) (bites.Bytes, error) {
	return bites.Bytes{ContentType: "text/csv", Body: []byte("id,title\n42,Plan\n"), Attachment: "docs.csv"}, nil
}
```

`Attachment` makes the answer a download saved under that name, quoted and encoded for a name outside ASCII ([Files](./files.md#sending-a-file-as-a-download)).

A body written over time is a [`Stream`](../reference/responses.md#stream) ([Streaming and SSE](./streaming.md)), and a file on disk is a [`File`](../reference/responses.md#file) ([Files](./files.md)).

## Answering with one of several types

**A handler that can answer with more than one type returns a [`OneOf2`](../reference/responses.md#oneof2), [`OneOf3`](../reference/responses.md#oneof3) or [`OneOf4`](../reference/responses.md#oneof4) of them, and chooses its answer with the method of the variant's letter.** `getDoc` above returns `DocAnswer`, an alias of `OneOf2[OK[Doc], NotModified]`: `DocAnswer{}.A(...)` answers with the first type and `DocAnswer{}.B(...)` with the second. The compiler refuses a handler that returns anything else, Registration compiles each variant as if the handler returned it alone, and the API description gets each status and schema, with no option to write ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)). The variant is held in the OneOf, not boxed, so it costs what a plain value costs ([ADR 0003](../adr/0003-the-trade-budget.md)).

<!-- compiles: body -->
```go
app.Get("/docs/{id}", getDoc)
app.Post("/docs", createDoc)
```

A variant that could not be returned alone (a type JSON cannot write, a pointer, an interface, a stream on a HEAD route), the same type named twice, a OneOf inside a OneOf, and a OneOf anywhere but as the whole return type are refused at Registration ([`OneOf2`](../reference/responses.md#oneof2) lists the messages). The one mistake the compiler and Registration cannot see is a OneOf returned with no variant chosen, its zero value, with a nil error: that is a logged 500, so a test that runs the handler should catch it ([Testing](./testing.md)). A test that calls the handler directly reads the answer with `Value()`.

**A handler with more than four answers, or one that already returns an interface of its own, returns the interface and declares each type it may hold with [`Returns`](../reference/responses.md#returns).** Registration needs a `Returns[T]()` for each type, compiles each answer then, and describes them as it describes a OneOf's variants.

<!-- compiles: decls -->
```go
type DocResult interface{}

func findDoc(ctx context.Context, in GetDoc) (DocResult, error) {
	if in.Path.ID == 0 {
		return bites.NoContent{}, nil
	}
	return bites.OK[Doc]{Body: Doc{ID: in.Path.ID, Title: "Plan"}}, nil
}
```

<!-- compiles: body -->
```go
app.Get("/find/{id}", findDoc,
	bites.Returns[bites.OK[Doc]](),
	bites.Returns[bites.NoContent](),
)
```

A route whose output is an interface with no `Returns`, a `Returns[T]` whose `T` does not implement the interface, a repeated one, and `Returns` on a route whose output is not an interface or on a group are all refused at Registration. A handler that returns a type it did not declare is a logged 500 that names the `Returns` to add; the compiler cannot catch that, so a test that runs the handler should. An interface return costs one allocation more than a plain value or a OneOf, the one the handler makes boxing its value ([ADR 0003](../adr/0003-the-trade-budget.md)).

## Compressing responses

**Compression is off by default; [`WithCompression`](../reference/app.md#withcompression) turns it on for finished bodies of at least `MinSize` bytes, with the encoders you list, and [`WithGzip`](../reference/app.md#withgzip) with gzip alone.** `New(WithGzip(Gzip{}))` uses the 1 KiB default, which is also the floor ([`MinCompressSize`](../reference/app.md#mincompresssize)). gzip is in the core; brotli and zstd are the modules [`modules/brotli`](../reference/brotli.md) and [`modules/zstd`](../reference/zstd.md), and the client's `Accept-Encoding` weights choose among what you list, your order breaking a tie ([ADR 0031](../adr/0031-gzip-only-and-off-by-default.md), [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md)). Your handler returns the plain value either way. The details, the order to list them in, and when to compress at the proxy instead, are in [Deploying](./deploying.md#compressing-responses).

## Where to go next

- [Files](./files.md): sending a file.
- [Streaming and SSE](./streaming.md): bodies written over time.
- [Errors](./errors.md): the failure statuses.
