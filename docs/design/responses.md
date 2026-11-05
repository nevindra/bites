# Responses

**How a handler answers: the return value is the response, a wrapper type carries any other status, a stream is a return type, compression belongs to the engine with encoders the app registers, and a cookie has safe defaults.**

**Guide:** [responses](../guide/responses.md) · **Reference:** [responses](../reference/responses.md)

## Overview

A handler that returns a plain value `T` answers 200 with it as JSON. Everything else is a type it returns, so the status, the headers and the cookies are known to Registration and to the API document, and the framework writes the response once ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)).

```text
return T                         200, JSON
return Created[T], Accepted[T]   the wrapper's status; Header and Cookies on every wrapper
       NoContent, NotModified, SeeOther, Redirect
return Text / Bytes / File       a body that is not JSON
return Stream / SSE / Events[T]  status and headers now, a function that writes later
return OneOf2[A, B] .. OneOf4    the variant chosen, compiled as if returned alone
return an interface              Returns[A](), Returns[B]() declare what it may hold
returned error                   every error status (see errors)

engine, after the chain:  gzip (off by default) -> Content-Length, Vary, ETag weakened
```

A stream is the one answer that is not a value: its function runs on the connection's goroutine after every middleware has returned, with a writer that works only while it runs. Compression is likewise the engine's, because a middleware sees only an opaque body.

## Rules

1. **A success status other than 200, and any response header, is a generic wrapper type.** `OK[T]`, `Created[T]`, `Accepted[T]`, `NoContent`, `NotModified`, `SeeOther` and `Redirect` carry a `Header` and a `Cookies` field, and a plain `T` is an `OK[T]` with neither. A status comes from a type because Registration reads types and could not learn it from a `Status()` method. Error statuses never come from the output type. [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)

2. **A handler that can answer with more than one type returns a `OneOf2`, `OneOf3` or `OneOf4` of them; an interface with `Returns[T]()` is the fallback.** The compiler checks a OneOf's returns, Registration compiles each variant as if it were returned alone and refuses a repeat, a pointer, an interface, a nested OneOf and a OneOf anywhere but the whole return type, and the variant is held unboxed, so it costs the 2 allocations of a plain return. A handler with more than four answers, or an interface of its own, declares each type with `Returns[T]()`; an undeclared type returned at run time is a logged 500 naming the `Returns` to add, and the boxing costs a third allocation. Either list gives the API document its statuses and schemas. [ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)
3. **A streamed response is a return type whose writer exists only inside its function.** `Stream`, `SSE` and `Events[T]` fix the status and headers before the first byte, and `Events[T]` sends values of `T` as JSON through an encoder compiled at Registration, which refuses a `T` it cannot write and states `T` as the data of each event in the API document; the function runs after the outermost middleware has returned, so every middleware decorates the head; a write after the function has returned fails with `ErrStreamClosed` and touches nothing. [ADR 0030](../adr/0030-streaming-is-a-return-type.md)

4. **A stream is written in chunks of at most 64 KiB, each under its own write deadline, and ends by what its function returns.** Nil sends the terminating chunk and keeps the connection; an error or panic after the first byte closes the connection with no terminating chunk, so a cut stream is never taken for a complete one. Only a live stream (`SSE`, `Events`, or a `Stream` with `Live`) is cancelled at Shutdown, with `ErrServerClosed`. [ADR 0030](../adr/0030-streaming-is-a-return-type.md)
5. **`Text`, `Bytes`, `Page` and `File` are the bodies other than JSON, and there is no content negotiation.** A `Page` is HTML for a browser and the one answer with an error status of its own, 200 or a 4xx, for a form shown again with its mistakes; 401 and 407, which need a challenge, are a logged 500 [0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md). The body is copied as it is, a `Bytes` with an invalid content type is a logged 500, and a `File` from an `os.Root` or an `fs.FS` answers the same 404 for a name that does not exist, is not a regular file or tries to leave the directory. A `File` is sent with a declared length through the stream writer, so it is never held whole in memory. CSV and other bodies written over time are a `Stream`. A download is the `Attachment` field of a `File`, a `Bytes` or a `Stream`, its name written by `mime.FormatMediaType` and cut to its last element. [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)

6. **A `File` answers its own conditional and range requests.** A file with a modification time carries `Last-Modified` and a strong `ETag` of its time and size; `If-Match`, `If-Unmodified-Since`, `If-None-Match` and `If-Modified-Since` are evaluated in RFC 9110's order into a 412 or a 304 that opens nothing; a GET with one byte range is a 206 or a 416, and several ranges are ignored for the whole file, so a list of overlapping ranges costs one scan. An `ETag` or `Last-Modified` in `Header` replaces the made one. [ADR 0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md)

7. **A directory is served by `Static`, a route with twins, a cache rule by name and a fallback that never hides an API 404.** `app.js.br`, `.zst` or `.gz` beside a file is answered by `Accept-Encoding` weight with `Vary`; a hashed name is `immutable` for a year, the index and fallback `no-cache`, the rest `no-cache` or `MaxAge`; the single-page-app fallback answers only a browser navigation whose first segment no other route uses. An `embed.FS` is hashed once at Registration. [ADR 0065](../adr/0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md)

8. **A cookie is written through the `Cookies` field of a wrapper, and defaults to `HttpOnly`, `Secure`, `SameSite=Lax` and `Path=/`.** Loosening any of them is written out; browsers treat `localhost` as a secure context, so `Secure` does not get in the way of development. [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)
9. **Response compression is the engine's, off by default, and chooses among the app's encoders.** `WithCompression(Compression{MinSize, Encoders})` compresses a finished body of at least `MinSize` (1 KiB at least) with the registered coding the request's `Accept-Encoding` weighs highest, the app's first on a tie; gzip at `BestSpeed` is the core's (`GzipEncoder`, and `WithGzip` is gzip alone), zstd and brotli are the modules `modules/zstd` and `modules/brotli` at about gzip's CPU. Encoders' states and the output buffer are pooled and never reach user code, and a panic in one sends the answer plain. Already compressed types, a stream, SSE and a file are skipped, `Vary: Accept-Encoding` goes on every answer, and a middleware sees the uncompressed `Size()`. [ADR 0031](../adr/0031-gzip-only-and-off-by-default.md), [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md)

## Decisions

| ADR | What it decides |
|---|---|
| [0013](../adr/0013-other-statuses-are-wrapper-types.md) | status wrappers with `Header` and `Cookies`, `OneOf2` to `OneOf4` for more than one answer, and `Returns` for interface returns |
| [0030](../adr/0030-streaming-is-a-return-type.md) | `Stream`, `SSE` and `Events[T]` as return types with a writer valid only inside the function |
| [0031](../adr/0031-gzip-only-and-off-by-default.md) | compression off by default, decided in the engine's write step, gzip in the core |
| [0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md) | the `Encoder` seam, the client's weights then the app's order, zstd and brotli as modules at fixed levels |
| [0043](../adr/0043-cookies-and-bodies-other-than-json.md) | cookie defaults, `Text`, `Bytes` and `File` as output types, and `Attachment` for a download |
| [0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md) | a `File`'s `ETag` from time and size, 304 and 412, one `Range` answered and a list ignored |
| [0065](../adr/0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md) | `Static`: precompressed twins, `Cache-Control` by name, an `embed.FS` hashed once, a fallback that never hides an API 404 |
| [0078](../adr/0078-a-page-for-a-browser-is-a-page-and-a-form-it-shows-again-is-a-submitted-body.md) | a `Page` is HTML for a browser with 200 or a 4xx, and a `Submitted[T]` form hands its mistakes to the handler that shows it again |

## Open questions

- **A OneOf returned with no variant chosen** is a 500 at request time, which neither the compiler nor Registration can see; past four answers a handler falls back to an interface, which can still return an undeclared type, and `Returns` can drift from the real returns ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).
- **A header whose name is decided at run time** cannot appear in the API description ([ADR 0013](../adr/0013-other-statuses-are-wrapper-types.md)).
- **Streams are opaque to middleware.** A middleware sees a stream's start, not its size or end (an [Observer](observability.md) sees both), and compression and ETag cannot apply; a slow first byte holds the connection unless the function calls `Flush`; the 64 KiB chunk may be wrong for a real-time client; throughput and the `TCP_NOTSENT_LOWAT` calls over TLS are not measured ([ADR 0030](../adr/0030-streaming-is-a-return-type.md), [roadmap](../roadmap.md#nice-to-have-what-few-applications-need)).
- **Shutdown cuts every live SSE stream at once**; the 1001 of a WebSocket is spread over a window and an SSE stream is not ([ADR 0030](../adr/0030-streaming-is-a-return-type.md), [ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)).
- **A `File`'s `ETag` is its time and size, not its bytes**: a rewrite of the same size within the time stamp's resolution keeps its tag, replicas with different times revalidate for nothing, and several ranges are never answered as such; its type for an uncommon extension depends on the machine's MIME table, and its allocation count is measured and not designed ([ADR 0064](../adr/0064-a-file-answers-its-own-conditional-and-range-requests.md), [ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md), [ADR 0003](../adr/0003-the-trade-budget.md)).
- **`Static`'s fallback guesses an API by its first segment**, so a page and an endpoint that share one get the endpoint, and a route whose first segment is a wildcard turns the fallback off; a twin added beside an unchanged file is not seen until the file changes; the hashed-name rule is a guess an application can replace ([ADR 0065](../adr/0065-static-files-are-a-route-with-twins-cache-rules-and-a-fallback-that-never-hides-a-404.md)).
- **A status other than 200 for `Text` and `Bytes`** waits for a need ([ADR 0043](../adr/0043-cookies-and-bodies-other-than-json.md)).
- **Compression:** streams and Range are not compressed; one order of preference serves every route and the modules' levels are fixed; peak memory is about GOMAXPROCS live states per coding in use, a claim not measured under load; idle memory, throughput and p99 over TCP with compression on are not measured ([ADR 0031](../adr/0031-gzip-only-and-off-by-default.md), [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md), [roadmap](../roadmap.md#nice-to-have-what-few-applications-need)).
