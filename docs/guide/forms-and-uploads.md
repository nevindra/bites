# Forms and uploads

**A form is read into a typed struct, and an upload is read part by part while it arrives, so a large file is never held whole in memory.**

**Reference:** [`Form`, `Upload`, `Part`](../reference/input.md) · **Design:** [Request input](../design/request-input.md)

## Reading an HTML form

**A `Body` of type [`Form[T]`](../reference/input.md#form) is read as `application/x-www-form-urlencoded`, and `T`'s fields decode exactly like a Query section.** `Form` wraps the struct because a Go type cannot be a bare `T`, so the handler reads `in.Body.Value.Name` ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)). The same field types, `Opt`, constraint types, lists from repeated keys and wire names apply ([Requests](./requests.md#text-values-are-decoded-by-type), [ADR 0042](../adr/0042-text-values-are-decoded-by-type.md)).

<!-- compiles: decls -->
```go
type SignupForm struct {
	Name bites.NonEmpty
	Tags []string
	Age  bites.Opt[int]
}

type Signup struct {
	Body bites.Form[SignupForm]
}

func signup(ctx context.Context, in Signup) (bites.SeeOther, error) {
	f := in.Body.Value
	if f.Age.Or(18) < 13 {
		return bites.SeeOther{}, bites.Forbidden("too young")
	}
	return bites.SeeOther{Location: "/welcome?name=" + string(f.Name)}, nil
}
```

<!-- compiles: body -->
```go
app.Post("/signup", signup)
```

A mistake is a 400 located as `body.name`, and a list element as `body.tags[1]`. Unlike a Query section, a form refuses a field the type does not have, exactly as a JSON body refuses an unknown member, because a misspelt field silently dropped is a lost write. Opt out with [`AcceptUnknownMembers`](../reference/input.md#acceptunknownmembers).

```http
POST /signup HTTP/1.1
Content-Type: application/x-www-form-urlencoded

name=Bob&tags=a&tags=b&age=x&zip=1

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"body.age","message":"expected an integer"},{"location":"body.zip","message":"unknown field"}]}
```

`T` must be a struct. A `Null` field or a nested struct in it is refused at Registration with the text a Query field gets, and so is a `Form` on a `GET` route. A form has at most 64 fields and 8 lists, and at most `Limits.MaxFields` (100) keys in a request; raise that app-wide with [`WithLimits`](../reference/app.md#withlimits) ([Limits and timeouts](./limits.md)).

## Showing a form again with its mistakes

**For a form a person fills in a browser, read the body as [`Submitted[T]`](../reference/input.md#submitted) and answer with a [`Page`](../reference/responses.md#page): the handler gets the mistakes and shows the form again, with 422, each mistake next to its field and what was typed still in it.** A `Form[T]` answers a bad form itself, with the 400 problem+json an API client reads; a browser would show that JSON. A `Submitted[T]` decodes the same fields the same way and hands the result to the handler ([ADR 0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md)).

<!-- compiles: decls -->
```go
type EventForm struct {
	Name     bites.NonEmpty
	Capacity bites.PositiveInt
}

type NewEvent struct {
	Body bites.Submitted[EventForm]
}

type EventAnswer = bites.OneOf2[bites.SeeOther, bites.Page]

var eventForm = template.Must(template.New("event").Parse(`<form method="post">
<ul>{{range .Problems}}<li>{{.Location}}: {{.Message}}</li>{{end}}</ul>
<input name="name" value="{{.Sent "name"}}"> {{.Problem "name"}}
<input name="capacity" value="{{.Sent "capacity"}}"> {{.Problem "capacity"}}
<button>Save</button>
</form>`))

func createEvent(ctx context.Context, in NewEvent) (EventAnswer, error) {
	f, ok := in.Body.Valid()
	if ok && string(f.Name) == "Gala" {
		in.Body.Add("name", "is taken: choose another name") // a mistake found against the data
		ok = false
	}
	if !ok {
		var page bytes.Buffer
		if err := eventForm.Execute(&page, in.Body); err != nil {
			return EventAnswer{}, err
		}
		return EventAnswer{}.B(bites.Page{Status: 422, Body: page.Bytes()}), nil
	}
	return EventAnswer{}.A(bites.SeeOther{Location: "/events"}), nil
}
```

- `Valid()` returns the form and whether it has no problem. `Problem("capacity")` is the message for one field, `Problems()` every one (a page lists them at the top too, since an unknown field, such as a button's name the type does not declare, has no field to show its problem beside; or give the route `AcceptUnknownMembers()`), and `Sent("capacity")` and `SentAll("tags")` what was typed, even `"abc"` for a number. `html/template` escapes all of it.
- The Input's `Validate` runs when the form decoded whole, and what it returns at `body.*` joins the form's problems instead of the 400; a mistake it finds elsewhere is the 400.
- A mistake in the path, the query, a header or a cookie is still the 400 problem+json, and so are a body over `MaxBody` or `Limits.MaxFields` (413) and another media type (415): a browser posting the form sends neither.
- `Page{Status: 422}` is the status to show a form again with; a sign-in form after a wrong password is a 422 page too, since a 401 must carry a `WWW-Authenticate` challenge that a page does not have, and a Page with 401 is a logged 500 that says so. A page that is not there is `Page{Status: 404}`.
- A `Submitted` body on a route that returns no `Page` is refused at Registration: the handler would have nowhere to show the form.
- The API document states the Page as a `200` and a `4XX` `text/html` answer, and the route has no 400 for its form.

A Submitted keeps a copy of the form as sent, so a page can show it again: a valid three-field form costs 5 allocations against a Form's 2 ([ADR 0003](../adr/0003-the-trade-budget.md)).

## Receiving an upload

**A `Body` of type [`Upload`](../reference/input.md#upload) is a `multipart/form-data` request the handler reads one part at a time.** [`in.Body.Each`](../reference/input.md#uploadeach) calls your function with each [`Part`](../reference/input.md#part), which has its `Name`, `FileName` and `ContentType` as strings and a `Read` for its body. Nothing is buffered whole: the window the bytes pass through is 32 KiB, so a 2 GiB upload costs the handler's own choices ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).

<!-- compiles: decls -->
```go
type SetAvatar struct {
	Path struct{ ID int64 }
	Body bites.Upload
}

// store stands for your storage: a file, an object store writer.
func setAvatar(store func(name string) io.Writer) func(context.Context, SetAvatar) (bites.NoContent, error) {
	return func(ctx context.Context, in SetAvatar) (bites.NoContent, error) {
		err := in.Body.Each(func(part bites.Part) error {
			if part.Name != "avatar" {
				return nil // skipped: what the part had left is passed over
			}
			_, err := io.Copy(store(part.ContentType), part)
			return err
		})
		return bites.NoContent{}, err // a status error from the upload (400, 408, 413, 431) is already the answer
	}
}
```

`Each` returns nil after the last part, and stops at the first error, whether your function returned it or reading the upload did, so the handler returns it as it is; the parts after it are not read. It is the loop over [`Next`](../reference/input.md#uploadnext), which returns the next part or `io.EOF` after the last; use `Next` when the parts are read one call at a time, such as a metadata field that decides where the file after it goes.

The other sections are decoded and `Validate` runs before the handler, and the body has not been read by then. A request the Input refuses never gets a `100 Continue` and never sends its body. `Each`, `Next` and `Read` return a [`StatusError`](../reference/errors.md#statuserror) for what the client got wrong (400, 408, 413, 431), so returning the error answers with the right status. The connection is closed after the answer either way.

`Upload` is the whole `Body`: it cannot be a field of a JSON body, a list, or a text section. `Name`, `FileName` and `ContentType` are data the client sent. `FileName` may be `../../etc/passwd`; the framework never opens it, and a handler that joins it to a directory has chosen to trust the client (open files through an `os.Root`, which refuses to leave its directory).

## Receiving a raw body as it arrives

**A `Body` of type [`BodyReader`](../reference/input.md#bodyreader) is the request body, whatever its media type, as an `io.Reader` the handler reads while it arrives.** It is for what is sent as the raw body of a `PUT` or `PATCH`: a chunk of a resumable upload, or a file sent as it is. A `[]byte` Body would hold the whole body in memory first; a `BodyReader` holds only what the handler's own buffer holds ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).

<!-- compiles: decls -->
```go
type PutPart struct {
	Path   struct{ ID string }
	Header struct {
		ContentType string `wire:"Content-Type"`
	}
	Body bites.BodyReader
}

// store stands for your storage: a file, an object store writer.
func putPart(store func(id string) io.Writer) func(context.Context, PutPart) (bites.NoContent, error) {
	return func(ctx context.Context, in PutPart) (bites.NoContent, error) {
		_, err := io.Copy(store(in.Path.ID), in.Body)
		return bites.NoContent{}, err // a status error from the body (400, 408, 413) is already the answer
	}
}
```

It has the rules of an `Upload`: the route must state `MaxBody` (refused at Registration without), `BodyTimeout` defaults to the larger of 30 s and `MaxBody` at 128 KiB/s and covers the whole body from the first `Read`, `MaxInFlight` bounds the bodies in flight, and `Read` returns a [`StatusError`](../reference/errors.md#statuserror) for a body that is too large (413), too slow (408) or cut short (400), so returning it answers correctly and the connection is closed after the answer. A `BodyReader` is valid only while the handler runs: after it returns, `Read` fails with `ErrUploadClosed`. The media type is not checked; a handler that wants `application/pdf` reads the `Header` section.

## Receiving a form with its files

**A `Body` of type [`Multipart[T]`](../reference/input.md#multipart) is a `multipart/form-data` request decoded into a struct before the handler runs.** Ask one question to choose between it and an `Upload`: does the handler need the bytes while they arrive? If it streams a file to a store, checks a signature on the way, or takes a file larger than it can keep, the body is an `Upload`. If it wants the fields and the files together as values (a title and a photo, a CSV and its options), the body is a `Multipart[T]` ([ADR 0044](../adr/0044-the-body-type-decides-how-a-body-is-read.md)).

A text field is declared as in a `Form[T]`. A file field is a [`FormFile`](../reference/input.md#formfile), or a named struct that embeds one when the field must say what it accepts (`Accept` lists the media types and `MaxSize` the most bytes, both methods read once at Registration). A bare file is exactly one and required, `Opt[F]` is at most one, and `[]F` is any number under one name.

<!-- compiles: decls -->
```go
type CoverPhoto struct{ bites.FormFile }

func (CoverPhoto) Accept() []string { return []string{"image/png", "image/jpeg"} }
func (CoverPhoto) MaxSize() int64   { return 5 << 20 }

type NewListing struct {
	Body bites.Multipart[struct {
		Title   bites.NonEmpty
		Price   int64
		Cover   CoverPhoto            // exactly one file, required
		Gallery []CoverPhoto          // zero or more files under one name
		Floor   bites.Opt[CoverPhoto] // at most one file
	}]
}

// save stands for your storage.
func create(save func(title string, size int64, r io.Reader) error) func(context.Context, NewListing) (bites.Created[string], error) {
	return func(ctx context.Context, in NewListing) (bites.Created[string], error) {
		f := in.Body.Value
		r, err := f.Cover.Open() // an independent reader over this request's copy
		if err != nil {
			return bites.Created[string]{}, err
		}
		defer r.Close()
		if err := save(string(f.Title), f.Cover.Size, r); err != nil { // copy it out here
			return bites.Created[string]{}, err
		}
		return bites.Created[string]{Location: "/listings/1"}, nil
	}
}
```

Route it with `MaxBody` on the route or a group, as for an `Upload`: `app.Post("/listings", create(save), bites.MaxBody(20<<20))`.

- **A file is valid only while the handler runs.** After the handler returns, `Open` and every reader from it return `ErrUploadClosed`, whether the file is small or large, so a goroutine that kept one fails the same way every time. A handler that wants the file after it returns (a queue, a worker) copies it inside the handler.
- **Small files stay in memory and large ones go to a temporary file.** Up to 256 KiB of files a request, and 32 MiB for the whole app (`Limits.UploadMemory`), are kept in memory; a file that would pass either goes to an unlinked temporary file in `Limits.UploadDir` (`os.TempDir()` by default) and the client is told nothing. Both figures are judgements, not measurements of your workload. The framework closes the file when the request ends, so a panic or a crash leaves nothing in the directory. If the directory is on `tmpfs`, the start-up log warns that a file written there is memory.
- **Mistakes are located and listed together.** `body.title` `required`, `body.cover` `required` for a missing file, `body.cover` `expected one file, got 2`, `body.cover` `the file must be sent as image/png or image/jpeg` (a 400: the type is the one the client declared for the part, so it guards against a mistake and not an attacker; read the first bytes in the handler when it matters), `body.price` `expected an integer`. A file over `MaxSize` is a 413 at `body.cover` and so is a text field over `Limits.MaxFieldBytes` (64 KiB) or all the text over `Limits.MaxFormBytes` (1 MiB).
- **The other sections are decoded first.** A mistake in the path, the query, a header or a cookie is a 400 answered before a byte of the body is read, so a client that asked for `100-continue` never sends the body; the body mistakes are listed once those are right. `Validate` runs after the body is read and may call `Size` and `ContentType` of a file.
- **The whole body is read before the handler.** A route that wants to refuse after the first field (a quota on the owner named in field one) pays for the whole body, where an `Upload` could stop at once.
- `Name`, `FileName` and `ContentType` of a file are data the client sent, as for a `Part`.

A [`Submitted[T]`](#showing-a-form-again-with-its-mistakes) whose `T` has a file field is read the same way, because an HTML form with a file input is sent as `multipart/form-data`: it accepts that media type and refuses `application/x-www-form-urlencoded` with a 415. `Sent` of a file field is the empty string, because a browser never fills a file input again, and the page should say so ("choose the file again").

## Stating the limits an upload needs

**An `Upload` or `Multipart` route must state `MaxBody`, on the route or on a group above it, or it is refused at Registration.** How much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default. `MaxBody` set only through `WithLimits` does not count, because it is the default for every other route ([ADR 0024](../adr/0024-limits-are-on-by-default.md)).

<!-- compiles: body -->
```go
upload := app.Group("/uploads", bites.MaxBody(2<<20))
upload.Post("/users/{id}/avatar", setAvatar(func(string) io.Writer { return io.Discard }),
	bites.MaxParts(4),     // at most 4 parts in one upload; 128 by default
	bites.MaxInFlight(16), // at most 16 uploads of this route at once
	bites.BodyTimeout(time.Minute),
)
```

- [`MaxBody`](../reference/input.md#maxbody): a `Content-Length` over it is a 413 before `100 Continue`, and a chunked body over it is a 413 when the byte past the limit arrives.
- [`MaxParts`](../reference/input.md#maxparts): more parts than allowed is a 413; the default is 128.
- [`MaxInFlight`](../reference/input.md#maxinflight): a request over the count is a 503 with `Retry-After` and the connection closed, before `100 Continue`, so the client never sends the body. There is no default, and it is counted per route even when given to a group.
- [`BodyTimeout`](../reference/input.md#bodytimeout): optional; it defaults to the larger of 30 s and `MaxBody` at 128 KiB/s, and covers the whole body, started when the handler first asks for a part. A client that is slow but steady is cut when it passes.

A part header block over 8 KiB or 16 fields is a 431, a boundary over 70 bytes or a missing one is a 400, and a content type other than `multipart/form-data` is a 415. The parser is the framework's own (`mime/multipart` cannot bound its parts and headers), and a body that ends inside a part is a 400. The [start-up log](./limits.md) says `unlimited` for an upload route without `MaxInFlight`.

## Why a part cannot be kept

**An `Upload` and every `Part` from it work only while the handler runs, and a `Part` that `Each` gives only until your function returns.** Read afterwards they return [`ErrUploadClosed`](../reference/input.md#erruploadclosed), and a `Part` read after `Next` moved on returns [`ErrPartClosed`](../reference/input.md#errpartclosed), never another request's data ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). The window the bytes pass through is reused, so a goroutine that keeps a part and reads it after the handler returned is cut short, and the connection never waits for it.

```go
part, _ := in.Body.Next()
go func() {
	_, err := io.Copy(dst, part) // ErrUploadClosed (or ErrPartClosed) once the handler has returned
	_ = err
}()
return bites.NoContent{}, nil
```

Copy what you need before returning, or stream it to its destination inside the handler. A handler that returns before the closing delimiter leaves a body unread, and the connection is closed after its answer.

## What is refused at Registration

**Each of these panics naming the route** ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)):

```text
POST /up: Up.Body is an Upload, but the route has no MaxBody: how much a route may receive is the application's decision (an avatar wants 2 MiB, a video 2 GiB), so there is no default; give MaxBody(n) to the route or to a group above it
	registered at main.go:15
```

- an `Upload` route with no `MaxBody`, on the route or a group;
- an `Upload` on a `GET`, `HEAD` or `DELETE` route;
- an `Upload` held by a JSON body, a list, or a text section;
- a `BodyReader` with no `MaxBody`, on a `GET`, `HEAD` or `DELETE` route, or held by a JSON body or a list;
- a `Multipart` with no `MaxBody`, on a `GET` route, of a type that is not a struct, held by a JSON body or a list, or beside an `Upload` in one Input;
- a `FormFile` in a `Form`, a Query, a Header, a Path, a Cookie or a JSON body, a file field that is a pointer, a map, an array, a `[]byte`, a `*multipart.FileHeader` or an `io.Reader`, an `Accept` entry that is not a media type, and a `MaxSize` under 1 or over `MaxBody`;
- a `Limits.UploadMemory`, `MaxFieldBytes` or `MaxFormBytes` below zero, and a `Limits.UploadDir` that does not exist or cannot be written;
- `MaxInFlight` or `MaxParts` with 0 or a negative number;
- a `Form` on a `GET`, a pointer to a `Form`, and a `Null` field in a `Form`.

A `MaxInFlight` on a route with no `Upload` or `BodyReader` is not refused, since a group's option reaches routes of every kind.

## Where to go next

- [Limits and timeouts](./limits.md): every limit and where each is set.
- [Errors](./errors.md): the status errors a part returns.
- [Testing](./testing.md): sending a multipart body over memory.
