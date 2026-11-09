# Files

**A handler sends a file by returning a `File` that names a root and a path inside it, revalidation and ranges are answered for it, a whole directory is served with `Static`, and every name that is missing or not allowed is the same 404.**

**Reference:** [`File`, `Static`](../reference/responses.md) · **Design:** [Responses](../design/responses.md)

## Sending a file from a directory

**Open the directory once as an `os.Root` and return a [`File`](../reference/responses.md#file) with the client's path inside it.** An `os.Root` refuses, in the operating system call itself, any name that leaves its directory: a `..`, an absolute name and a symbolic link that points out (Go 1.24). So a name from the client cannot name a file outside the directory, even when the handler passes it straight through ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

<!-- compiles: decls -->
```go
type Download struct {
	Path struct{ Name string } // {name...}: the rest of the path
}

type Assets struct {
	root *os.Root
}

func openAssets(dir string) (*Assets, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Assets{root: root}, nil
}

func (a *Assets) Get(ctx context.Context, in Download) (bites.File, error) {
	return bites.File{Root: a.root, Name: in.Path.Name}, nil
}
```

<!-- compiles: body -->
```go
assets, err := openAssets("public")
if err != nil {
	log.Fatal(err)
}
app.Get("/assets/{name...}", assets.Get)
```

The answer carries `Content-Length`, `Last-Modified`, an `ETag` and `Accept-Ranges`, and the body is read in 64 KiB chunks as it is written, so a file is never held whole in memory, however large:

```http
GET /assets/logo.svg HTTP/1.1

HTTP/1.1 200 OK
Content-Type: image/svg+xml
Last-Modified: Fri, 30 Oct 2026 16:15:29 GMT
ETag: "18a3b5c2d1e0f4a7-4f9"
Accept-Ranges: bytes
Content-Length: 1273
```

## Sending a file from an fs.FS

**Give `FS` instead of `Root` for an embedded or in-memory file system.** An `fs.FS` is only ever handed a name that passes `fs.ValidPath` (no `..`, no leading slash, no empty element), so it cannot be asked for anything outside it either.

<!-- compiles: decls -->
```go
func fromFS(fsys fs.FS) func(context.Context, Download) (bites.File, error) {
	return func(ctx context.Context, in Download) (bites.File, error) {
		return bites.File{FS: fsys, Name: in.Path.Name}, nil
	}
}
```

<!-- compiles: body -->
```go
app.Get("/static/{name...}", fromFS(os.DirFS("public")))
```

Exactly one of `Root`, `FS`, `Open` and `Content` must be set. None or several is a 500, logged, because it can only be a mistake in the handler.

## Sending an object from a store, or bytes in memory

**Give `Open` for an object in S3, GCS or a database, and `Content` for bytes you already hold.** Neither is a file system, so neither needs a fake one, and both get the ranges, validators, conditional requests, HEAD and `Attachment` of the other sources ([ADR 0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md)). `Open` is called only when a body is sent, with the request's context, which is cancelled when the client leaves: a viewer who closes a 2 GB video stops the read from the store, and the store call can carry the request's deadline and trace. `Size` and `ModTime` come from the row you already read, so a HEAD, a 304 and a 412 open nothing.

<!-- compiles: decls -->
```go
type Object struct {
	Name, Version string
	Size          int64
	Modified      time.Time
}

type Blobs interface {
	Stat(ctx context.Context, id string) (Object, error)
	Reader(ctx context.Context, id string) (io.ReadSeekCloser, error)
}

func fromStore(ctx context.Context, store Blobs, id string) (bites.File, error) {
	obj, err := store.Stat(ctx, id) // size, modified, content type: no body read
	if err != nil {
		return bites.File{}, bites.NotFound("no such upload")
	}
	h := bites.Header{}
	h.Set("ETag", `"`+obj.Version+`"`) // a zero ModTime gives no made ETag
	return bites.File{
		Name: obj.Name, Size: obj.Size, ModTime: obj.Modified, Header: h,
		Open: func(ctx context.Context) (io.ReadSeekCloser, error) { return store.Reader(ctx, id) },
	}, nil
}
```

For bytes in memory, `bites.File{Content: snapshot, Name: "snapshot.json", ModTime: built}` is the whole answer. Do not change the slice until the answer is sent, and give `Content: []byte{}` (not nil) for an empty body. `Size` must be the length of what `Open` returns: a reader of another length is a 500 that names both numbers, found when the body is written (a HEAD cannot tell). `Name` is not looked up here; it only gives the content type through its extension and the saved name for `Attachment`.

## What a missing or forbidden name answers

**A name that is invalid, a file that does not exist, a file that is not a regular file, and one an `os.Root` refuses are the same 404 problem+json.** That is a name with `..`, a directory, a device, and a symbolic link out of the directory. The answer does not say which files exist ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

```http
GET /assets/..%2Fsecret HTTP/1.1

HTTP/1.1 404 Not Found
Content-Type: application/problem+json

{"title":"Not Found","status":404}
```

Any other failure to read the file is a 500, logged. A file whose size differs when it is opened from when it was looked at is a 500, and one that shrinks while it is sent closes the connection after the bytes that existed, so the client sees a short body and not a wrong one.

## Content types

**The content type is the `ContentType` field if you set it, else the type of the file name's extension, else `application/octet-stream`.** The extension's type comes from the system's MIME table with a built-in fallback, which differs between machines for uncommon extensions, so set `ContentType` for those. A `Content-Type` in `Header` replaces all of these. `Header` and `Cookies` work as on any [wrapper](./responses.md#the-other-status-wrappers).

<!-- compiles: decls -->
```go
func (a *Assets) Report(ctx context.Context, in struct{}) (bites.File, error) {
	h := bites.Header{}
	h.Set("Cache-Control", "private, max-age=300")
	return bites.File{Root: a.root, Name: "reports/latest.parquet", ContentType: "application/vnd.apache.parquet", Attachment: "report.parquet", Header: h}, nil
}
```

## Sending a file as a download

**Set `Attachment` to the name the browser saves the file under.** The answer then carries `Content-Disposition: attachment` with the name written by the standard library's `mime.FormatMediaType`: quoted when it needs it, and as `filename*=utf-8''...` (RFC 2231) when it is outside ASCII, which every browser reads. The same field is on [`Bytes`](./responses.md#answering-with-text-or-bytes) and [`Stream`](./streaming.md#streaming-a-large-export), and the header is sent on GET, HEAD, a 206 and with [`WithGzip`](../reference/app.md#withgzip) on, but not on a 304 ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).

<!-- compiles: decls -->
```go
func (a *Assets) Download(ctx context.Context, in Download) (bites.File, error) {
	return bites.File{Root: a.root, Name: in.Path.Name, Attachment: in.Path.Name}, nil // reports/q3.pdf is saved as q3.pdf
}
```

**Only the last element of the name is used**, after the last `/` or `\`, so a client's path or a database path can be given as it is and never names a directory on the user's disk. What the header says for each name (the tests `TestAFileWithAnAttachmentNameIsADownloadOnGetHeadRangeAndGzipButNotOnA304` and its twins for `Bytes` and `Stream` hold every row):

| `Attachment` | `Content-Disposition` |
|---|---|
| `report.pdf` | `attachment; filename=report.pdf` |
| `my report.pdf` | `attachment; filename="my report.pdf"` |
| `a"b.pdf` | `attachment; filename="a\"b.pdf"` |
| `laporan-é.pdf` | `attachment; filename*=utf-8''laporan-%C3%A9.pdf` |
| `reports/q3.pdf` | `attachment; filename=q3.pdf` |
| `../../etc/passwd` | `attachment; filename=passwd` |
| `dir/`, `.`, `..` | `attachment` (the browser names the file) |
| empty | no `Content-Disposition`: the browser shows the file when it can |

A line break or invalid UTF-8 in the name is percent-encoded into `filename*`, so a name cannot split the response. To show a file in the browser with a name for when it is saved, put `inline; filename=...` in `Header`: a `Content-Disposition` there replaces `Attachment`.

## HEAD requests

**A HEAD request, including the implicit one of a GET route, gets the head with the same `Content-Length` and no body, and opens nothing.** The file is looked at, not opened, so a HEAD costs one stat ([Routing](./routing.md#404-405-and-head)).

## Revalidation: ETag, 304 and 412

**Every `File` answers conditional requests itself, so a browser that has the file gets a 304 with no body and nothing is opened.** A file with a modification time is sent with `Last-Modified` and a strong `ETag` made of that time in nanoseconds and the size; `If-None-Match` and `If-Modified-Since` that still match are a 304, and `If-Match` and `If-Unmodified-Since` that no longer match are a 412 problem+json, evaluated in the order RFC 9110 gives ([ADR 0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md)).

```http
GET /assets/logo.svg HTTP/1.1
If-None-Match: "18a3b5c2d1e0f4a7-4f9"

HTTP/1.1 304 Not Modified
ETag: "18a3b5c2d1e0f4a7-4f9"
Last-Modified: Fri, 30 Oct 2026 16:15:29 GMT
```

A file with no modification time, as in an `embed.FS`, gets no validator from `File`; serve it with [`Static`](#serving-a-directory), which hashes an `embed.FS` once into tags. When the file is really a function of something else (a row, a version), put your own `ETag` in `Header`: it replaces the made one in the answer and in every comparison.

<!-- compiles: decls -->
```go
func (a *Assets) Export(ctx context.Context, in struct{}) (bites.File, error) {
	h := bites.Header{}
	h.Set("ETag", `"export-v42"`)
	return bites.File{Root: a.root, Name: "exports/latest.csv", Header: h}, nil
}
```

## Ranges

**A GET with one byte range gets a 206 with those bytes, which is how a video seeks and a download resumes.** A range that starts past the end is a 416 problem+json with `Content-Range: bytes */size`. A request with several ranges gets the whole file with a 200, which RFC 9110 allows, so a list of hundreds of overlapping ranges costs one scan and sends the file once. `If-Range` is honoured: a resumed download whose file changed gets the whole new file, never a splice of two versions.

```http
GET /assets/intro.mp4 HTTP/1.1
Range: bytes=1048576-

HTTP/1.1 206 Partial Content
Content-Range: bytes 1048576-7340031/7340032
Content-Length: 6291456
```

`Accept-Ranges: none` in `Header` turns ranges off for that answer. A 206 and a 416 are never compressed by [`WithGzip`](../reference/app.md#withgzip).

## Serving a directory

**[`app.Static(prefix, bites.Static{...})`](../reference/responses.md#appstatic) serves every file under a prefix, from an `os.Root`, an `fs.FS` or an `embed.FS`, as a `File` is served.** It is one route, `GET prefix/{path...}`, so a more specific route wins over it, it runs your middleware, and it is left out of the API document ([ADR 0065](../adr/0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md)).

```go
//go:embed dist
var dist embed.FS

func serveFrontEnd(app *bites.App) error {
	site, err := fs.Sub(dist, "dist")
	if err != nil {
		return err
	}
	app.Static("/", bites.Static{FS: site, Fallback: "index.html"})
	return nil
}
```

A path that ends in a slash is answered with `index.html` (or `Index`); a directory named without its slash is a 404, never a redirect, and no directory is listed. An `embed.FS`, and any `fs.FS` whose files have no modification time such as one reached through `fs.Sub`, is read once at Registration: every file hashed into an `ETag`, so a request makes no stat. Any other source, an `os.Root` above all, is looked at on each request and sees files as they are now.

**Precompressed twins.** Put `app.js.br`, `app.js.zst` or `app.js.gz` beside `app.js` (Vite, esbuild or a build step make them) and a browser gets the one its `Accept-Encoding` weighs highest, with `Content-Encoding` and the type of `app.js`, and every answer of a compressible type says `Vary: Accept-Encoding`. Nothing is compressed on the fly.

**Cache headers by name.** A name with a content hash (`app-3f2a1b9c.js`, `index-BxR3k9aQ.js`) is `max-age=31536000, immutable`; `index.html` and the fallback are always `no-cache`, because they name the hashed files; every other name is `no-cache`, which keeps the file in the browser and revalidates it with a 304, or `max-age=N` with `MaxAge`. A build with another naming scheme gives `Immutable` its own test.

**The single-page-app fallback.** With `Fallback`, a browser loading `/dashboard/settings` gets `index.html` so the app's own router can show the page. It is answered only for a navigation: a GET or HEAD whose `Accept` lists `text/html`, whose last segment has no extension, and whose first segment no other route uses. So with `/api/...` routes registered, `/api/nothing` is still the API's 404 problem+json, a `fetch` never gets HTML, and a missing `app.js` is a 404 and not a page the browser tries to run.

```http
GET /api/nothing HTTP/1.1
Accept: text/html

HTTP/1.1 404 Not Found
Content-Type: application/problem+json
```

Middleware sees a `File` as a response with `Streaming()` true and a `Size()` of 0, because it is written by the same path as a [`Stream`](../reference/responses.md#stream) with a declared length ([Middleware](./middleware.md), [ADR 0030](../adr/0030-streaming-is-a-return-type.md)). A body written this way is not compressed by [`WithGzip`](../reference/app.md#withgzip).

## Where to go next

- [Streaming and SSE](./streaming.md): bodies of unknown length.
- [Deploying](./deploying.md): putting a CDN or proxy in front of assets, and serving on several listeners.
