# Streaming and SSE

**A handler streams by returning a `Stream`, an `SSE` or an `Events`: the status and headers are fixed in the return value, and a function writes the body while the connection stays open.**

**Reference:** [`Stream`, `SSE`, `Events`](../reference/responses.md) · **Design:** [Responses](../design/responses.md)

Use [`Events[T]`](../reference/responses.md#events) for server-sent events whose data is JSON, which a browser's `EventSource` reads, [`SSE`](../reference/responses.md#sse) for events whose data is text of your own, and [`Stream`](../reference/responses.md#stream) for anything else that is written over time: a large export, NDJSON, a feed. The writer exists only inside the function, so a goroutine that kept it cannot write into a connection that has moved on to another request ([ADR 0030](../adr/0030-streaming-is-a-return-type.md), [ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). For a file whose size is known, use [`File`](./files.md) instead.

## Sending typed events

**An [`Events[T]`](../reference/responses.md#events) handler returns a function that receives an [`EventSender[T]`](../reference/responses.md#eventsender) and sends values of `T`, which go out as JSON.**

<!-- compiles: decls -->
```go
type Feed struct {
	Path struct{ Room string }
}

type Tick struct {
	N int
}

func feed(ctx context.Context, in Feed) (bites.Events[Tick], error) {
	return bites.Events[Tick]{Func: func(ctx context.Context, e bites.EventSender[Tick]) error {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for n := 1; ; n++ {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				err := e.SendEvent(bites.Event{ID: strconv.Itoa(n), Name: "tick"}, Tick{N: n})
				if err != nil {
					return err
				}
			}
		}
	}}, nil
}
```

<!-- compiles: body -->
```go
app.Get("/rooms/{room}/events", feed)
```

The browser sees an ordinary response whose body is the events:

```http
GET /rooms/lobby/events HTTP/1.1
Host: example.com
Accept: text/event-stream

HTTP/1.1 200 OK
Content-Type: text/event-stream
Cache-Control: no-cache
Transfer-Encoding: chunked

id: 1
event: tick
data: {"n":1}

id: 2
event: tick
data: {"n":2}
```

[`EventSender.Send`](../reference/responses.md#eventsendersend)`(name, v)` sends `v` as the data of an event of type `name` (`""` is the browser's `message`), and [`EventSender.SendEvent`](../reference/responses.md#eventsendersendevent)`(ev, v)` takes the `ID`, `Name` and `Retry` of an [`Event`](../reference/responses.md#event) as well: `ID` is the `Last-Event-ID` the browser sends back when it reconnects, and `Retry` is how long it waits before reconnecting, in whole milliseconds. The fields go out in the order `id`, `event`, `retry`, `data`, flushed at once. The value is one line of JSON, so it is one `data:` field, encoded with the field names of [`WithNaming`](../reference/app.md#withnaming) by the encoder compiled for `T` at Registration: a typed event allocates nothing, where a `json.Marshal` and a `string` of its result cost about 4 allocations an event and twice the time ([ADR 0030](../adr/0030-streaming-is-a-return-type.md)). The event's `Data` must be empty, since the data is `v`.

**A `T` that cannot be JSON is refused at Registration, and the API document states `T`'s schema for every event.** A channel or a function in `T`, `Events[[]byte]` (bytes would go out as base64; send them as text with an `SSE`), and an `Events` of a response type such as `Events[bites.Text]` panic at the route's line. In the [API document](./openapi.md) the response is `text/event-stream` with an `x-itemSchema` that gives one event, whose `data` is JSON of `T`'s schema.

**An `Events` is a `Stream` that is live, has the content type `text/event-stream` and `Cache-Control: no-cache` unless its `Header` says otherwise, and always answers 200.** Its function returns `nil` to end the response cleanly. Use `Header` and `Cookies` on the `Events` value to add headers and cookies to the head; a status other than 200 needs a `Stream`. A name or an ID with a line break fails with [`ErrBadEvent`](../reference/responses.md#errbadevent) and sends nothing, because it would end the field and let the sender begin another; a value JSON cannot hold, a NaN, fails with the encoder's error and sends nothing. The sender is safe to use from several goroutines, each event written whole.

**Send a comment now and then, so a proxy does not close an idle stream.** [`EventSender.Comment`](../reference/responses.md#eventsendercomment) writes a line the browser ignores, and flushes. It is on the typed sender too, because a proxy closes an idle typed stream as readily as any other.

<!-- compiles: decls -->
```go
func pulse(ctx context.Context, e bites.EventSender[Tick], every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := e.Comment("keep-alive"); err != nil {
				return err
			}
		}
	}
}
```

## Sending events of your own text

**An [`SSE`](../reference/responses.md#sse) handler returns a function that receives an [`EventWriter`](../reference/responses.md#eventwriter) and sends [`Event`](../reference/responses.md#event) values whose `Data` is a string.** Use it when the data is not JSON, or is JSON you already hold as text.

<!-- compiles: decls -->
```go
func status(ctx context.Context, in Feed) (bites.SSE, error) {
	return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
		return e.Send(bites.Event{Name: "status", Data: "room " + in.Path.Room + " is open\nwelcome"})
	}}, nil
}
```

[`EventWriter.Send`](../reference/responses.md#eventwritersend) writes the fields in the order `id`, `event`, `retry`, `data` and flushes at once. An `Event` has `ID`, `Name`, `Data` and `Retry`, as above. A `Data` with line breaks becomes one `data:` field a line, which the browser joins again. An `SSE` is live, `text/event-stream` and 200, as an `Events` is.

**Send a comment now and then here too.** [`EventWriter.Comment`](../reference/responses.md#eventwritercomment) writes a line the browser ignores, and flushes.

<!-- compiles: decls -->
```go
func heartbeat(ctx context.Context, e bites.EventWriter, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := e.Comment("keep-alive"); err != nil {
				return err
			}
		}
	}
}
```

## Streaming a large export

**A [`Stream`](../reference/responses.md#stream) carries a status, headers, cookies and a function that writes bytes to a [`StreamWriter`](../reference/responses.md#streamwriter).**

<!-- compiles: decls -->
```go
type Export struct{}

func export(ctx context.Context, in Export) (bites.Stream, error) {
	return bites.Stream{
		Attachment: "rows.csv",
		Header:     bites.Header{"Content-Type": {"text/csv; charset=utf-8"}},
		Func: func(ctx context.Context, w *bites.StreamWriter) error {
			for i := range 100000 {
				if _, err := fmt.Fprintf(w, "%d,row\n", i); err != nil {
					return err
				}
			}
			return nil
		},
	}, nil
}
```

`Attachment` makes the stream a download saved under that name, quoted and encoded for a name outside ASCII ([Files](./files.md#sending-a-file-as-a-download)).

`Status` is 200 when zero and must be a status that has a body (200 to 599 except 204 and 304). A `Content-Type` in `Header` is used; without one the stream is `application/octet-stream`. The `StreamWriter` is an `io.Writer` with `WriteString`, `ReadFrom` and `Flush`, and it is safe for concurrent use. Writes are buffered up to 64 KiB and sent as one chunk when the buffer fills, on `Flush`, and when the function returns.

**Nothing is sent before the first chunk, not even the headers, so an error before it is an ordinary answer.** If the function returns an error or panics before the first byte, the client gets the usual problem+json or 500. A function that waits a long time for its first event calls `Flush` to open the response: a browser's `EventSource` is not open until then.

**After the first byte the status cannot change, and an error closes the connection.** A function that returns an error, or panics, once bytes have gone out ends the connection with no terminating chunk, so a client sees a cut stream and never mistakes it for a complete one. The error is logged unless it is the context's cancellation or a failed write. A function that returns `nil` ends the response with the terminating chunk, and the connection serves its next request. An HTTP/1.0 client gets no chunking and the body ends when the connection does.

## Ending streams when the client leaves or the server stops

**The function's context is cancelled when the client leaves.** The cancellation is armed when the function first looks at `ctx.Done()` ([ADR 0016](../adr/0016-the-handler-context-is-cancelled-on-disconnect.md)). A function that never looks finds out from its next write failing, so return when a write returns an error.

**[`Shutdown`](../reference/app.md#appshutdown) cancels the context of a live stream with the cause [`ErrServerClosed`](../reference/app.md#errserverclosed), and waits for a plain one.** An SSE never ends by itself, so waiting for it would spend the whole Shutdown deadline on every deploy; a download of 2 GB must not be cut at the first signal ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md), [ADR 0030](../adr/0030-streaming-is-a-return-type.md)). `SSE` and `Events` are live by construction. A `Stream` is plain unless it sets `Live: true`, as an NDJSON feed that has no end of its own does:

<!-- compiles: decls -->
```go
func follow(ctx context.Context, in Export) (bites.Stream, error) {
	return bites.Stream{
		Live:   true,
		Header: bites.Header{"Content-Type": {"application/x-ndjson"}},
		Func: func(ctx context.Context, w *bites.StreamWriter) error {
			enc := json.NewEncoder(w)
			for n := 1; ; n++ {
				select {
				case <-ctx.Done():
					if errors.Is(context.Cause(ctx), bites.ErrServerClosed) {
						return nil // the server is going away
					}
					return ctx.Err()
				case <-time.After(time.Second):
					if err := enc.Encode(Tick{N: n}); err != nil {
						return err
					}
					if err := w.Flush(); err != nil {
						return err
					}
				}
			}
		},
	}, nil
}
```

At Shutdown an `SSE` or `Events` response ends with its terminating chunk (`EventSource` reconnects either way). A live `Stream` ends without its terminating chunk, so a client cannot mistake a cut feed for a complete one, whatever the function returns.

## Slow clients and write deadlines

**Every chunk of at most 64 KiB has the write timeout, so a client that stops reading cannot hold the connection for ever.** The engine arms the deadline before each chunk (30 seconds by default, see [limits](./limits.md#timeouts)), and only the total duration of the response is lifted. A write that fails because the client left or was too slow fails every later write too, and the connection is closed when the function returns. Return the error from the failed `Send` or `Write`, as the examples above do.

The deadline alone does not stop a slow reader of small events, because the kernel's socket buffer absorbs thousands of them first. When a stream starts, the engine also limits the unsent bytes the kernel holds on that socket to 16 KiB, which fell the backlog of a client that never reads from 4,132 KiB to 117 KiB ([ADR 0030](../adr/0030-streaming-is-a-return-type.md), measured in `docs/research/decisions-bodies/`, section R3).

**A writer kept past the function fails with [`ErrStreamClosed`](../reference/responses.md#errstreamclosed) and touches nothing.** A goroutine that outlives the function and keeps writing, or sending, gets that error and cannot reach the next request on the connection. A function that starts goroutines to write must wait for them before it returns.

## What a stream cannot do

**A stream is the whole return type, and some things that work for a value do not work for it.**

- The status and headers are fixed before the first byte. They cannot be set late ([ADR 0009](../adr/0009-the-response-comes-only-from-the-return-value.md)).
- It is not compressed. [`WithGzip`](./deploying.md#compressing-responses) compresses a finished body and applies to values only ([ADR 0031](../adr/0031-gzip-only-and-off-by-default.md)).
- A middleware sees it as `Streaming()` true with `Size()` 0: it can add headers and change the status before the head goes out, and cannot see or wrap the body ([middleware](./middleware.md#what-a-middleware-cannot-see)). A middleware that replaces it with a fresh `Response` means the function never runs. The [access log](./first-party-middleware.md#logging-each-request) records the head when the stream starts, not its end.
- A [`Timeout`](./first-party-middleware.md#cutting-off-a-slow-handler) bounds the time to produce the stream and not its life; end a stream by a time with its own timer.
- A HEAD request gets the head and no body, and the function does not run.

**Three mistakes are refused at Registration for every stream** ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)): a pointer (`*Stream`, `*SSE`, `*Events[T]`), because a stream is returned by value; a stream as the `Body` of a wrapper such as `OK[SSE]`, because its status and headers are its own; and a HEAD route that returns a stream. An `Events` adds the refusals of its `T`, [above](#sending-typed-events).

```text
GET /x: the handler returns *Stream; a stream is returned by value, because its function and its writer live for one response
	registered at main.go:20
```

A `Func` that is nil (on a `Stream`, an `SSE` or an `Events`), or a `Status` outside 200 to 599 (or 204 or 304), is a 500, logged, because the value is not known until the request.

## Cost

**A stream costs 4 allocations and each write costs none**, a plain request's 2 plus the writer and the runner that carries the function; an `SSE` event of up to 1 KiB and an `Events` event of up to 512 B, its data encoded, allocate nothing; a larger typed event grows the stream's buffer once and keeps it up to 64 KiB. The runner of an `Events` carries that 512-byte buffer, so a live `Events` holds about 600 B more than an `SSE` ([ADR 0003](../adr/0003-the-trade-budget.md), the row for streams).
