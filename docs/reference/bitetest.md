# bitetest

**The package that runs an App over in-memory connections and a clock that moves only when told to, so a request, a 400, a timeout or a client that leaves is a test with no port.**

**Guide:** [testing](../guide/testing.md) · **Design:** [testing](../design/testing.md)

`github.com/nevindra/bites/bitetest` serves a `bites.App` over memory. Nothing is skipped between the bytes and the handler: the request goes through the engine's reader and parser, the router, the decoding and the writer exactly as a socket's would, minus the kernel ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)). What it cannot simulate is the kernel: a reset connection, a partial write and a full socket buffer are covered by the engine's own loopback tests. The names here carry the `bitetest.` prefix in their headings because they live in another package.

Registration mistakes are not a matter for this package: they panic when the route is registered, so a test recovers a `*bites.RegistrationError` around the registration call ([`RegistrationError`](./app.md#registrationerror)).

<!-- compiles: decls -->

```go
type EchoIn struct {
	Query struct{ Name string }
}

func echo(ctx context.Context, in EchoIn) (bites.Text, error) {
	return bites.Text{Body: "hello " + in.Query.Name}, nil
}

func TestEcho(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk))
	app.Get("/echo", echo)
	srv := bitetest.Start(t, app)

	ok := srv.Do(bitetest.Request{Method: "GET", Target: "/echo?name=ada"})
	if ok.Status != 200 || ok.Text() != "hello ada" {
		t.Fatalf("got %d %q", ok.Status, ok.Text())
	}

	bad := srv.Do(bitetest.Request{Method: "GET", Target: "/echo"})
	if bad.Status != 400 || bad.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("got %d %q", bad.Status, bad.Get("Content-Type"))
	}
	problem := bitetest.JSON[struct {
		Errors []struct{ Location, Message string }
	}](t, bad)
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "query.name" {
		t.Fatalf("got %+v", problem.Errors)
	}
}
```

Guide: [testing](../guide/testing.md). Decision: [ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md).

## Starting a server

### `bitetest.Start`

```go
func Start(tb testing.TB, app *bites.App) *Server
```

Start serves `app` on an in-memory listener and stops it when the test ends (it registers `Close` with `tb.Cleanup`). The app's clock is the one its connections measure their deadlines on, so give the App a Clock with `bites.WithClock` to control them. Starting freezes the route table like `Serve`: a route registered afterwards panics. The app's server is built before `Start` returns, so a Registration mistake panics in the test, and a `Shutdown` called right after `Start` stops a server that is serving rather than finding none. An App that was not given a logger with `bites.WithLogger` logs into `tb.Log`, as text with no time stamp, instead of to the process's default logger: the start-up settings, a handler's error and a recovered panic then show only when the test fails or runs with `-v`. An App given a logger keeps it, and a line logged after `Close` is dropped, so nothing reaches `tb` once the test is over. The `Start` function of every `bites.Service` runs before `Start` returns, and one that fails fails the test with `tb.Fatalf` and its error; the cleanup runs the services' `Stop` functions with a context already done and waits for them ([ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md)). A failure to dial fails the test with `tb.Fatalf`, and once the app was shut down the message says so and names `WithShutdownDelay`.

### `bitetest.Server`

```go
type Server struct {
	// Has unexported fields.
}
```

Server is an App being served over memory. It is made by `Start`; the methods below fail the test with `tb.Fatal` rather than returning an error.

### `bitetest.Server.Do`

```go
func (s *Server) Do(r Request) Response
```

Do sends `r` on a new connection, reads one response and closes the connection. It is `Dial`, a request written as HTTP/1.1, and `Read`. A response that does not arrive in five real seconds fails the test. The response is read while the request is written, so an answer the app gives before it has read the whole body (a 413 for a body over `MaxBody`, a 401 from a middleware) is returned as any other. A request whose `Method` is `HEAD` is read as a head with no body. A request after the app was shut down (`Shutdown` with no `WithShutdownDelay`, or `Close`) fails the test with a message that says the app stopped accepting and names `WithShutdownDelay`, as do `Dial`, `Read`, `OpenEvents` and `DialWS`. Use `Dial` and `Read` for a pipelined request, a request that is not valid HTTP, or a client that keeps the connection open.

### `bitetest.Server.Dial`

```go
func (s *Server) Dial(remote string) net.Conn
```

Dial opens a connection to the server as the client at `remote` (`"203.0.113.9:4000"`), for a test that writes its own bytes: a pipelined request, a client that leaves, a slow head, a WebSocket handshake. The address is what a middleware sees as `Request.RemoteAddr`. The caller closes the connection.

### `bitetest.Server.Read`

```go
func (s *Server) Read(c net.Conn, head bool) Response
```

Read reads one response from `c`. `head` says the request was HEAD, so the body is not read. A response that does not arrive in five real seconds fails the test.

### `bitetest.Server.Close`

```go
func (s *Server) Close()
```

Close stops the server and closes every connection, as `App.Close` does. `Start` calls it when the test ends, so a test calls it only to stop early.

### `bitetest.Server.WaitEnded`

```go
func (s *Server) WaitEnded()
```

WaitEnded returns once every request the server has received whole has ended, which is when the End of its Observers has run ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)). `Do` and `Read` return when the answer is read, and End runs after the last byte, so a test that looks at what an Observer recorded (an access record, a counter) calls `WaitEnded` first. It neither sleeps nor polls: it waits on the connections, and a connection reads its next request only after End ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)). A request being handled, a stream still sending and a client that left mid-handler are waited for; a WebSocket is settled once its upgrade has been answered, since End runs at the 101, not when the socket closes; a request the client has only partly sent is not waited for. If the requests do not end in five real seconds, for a handler held on the fake `Clock` for example, it fails the test with `tb.Fatal`.

```go
srv.Do(bitetest.Request{Method: "GET", Target: "/users/1"})
srv.WaitEnded()
// the access record, the counter, the span: all written
```

## Requests and responses

### `bitetest.Request`

```go
type Request struct {
	Method string
	Target string
	Header map[string][]string
	Body   []byte
	// Remote is the client address the server sees; "203.0.113.9:4000" if empty.
	Remote string
}
```

Request is one request to send.

- `Method` and `Target` make the request line (`GET /users/42?expand=true`).
- `Header` gives the header fields in the shape `Response.Header` has: a name with several values is sent as that many lines (`{"Authorization": {"Bearer " + token}}`). `Host` is `test` unless `Header` gives it.
- `Body` is the request body. `Content-Length` is set from it unless `Header` gives `Content-Length` or `Transfer-Encoding`.
- `Remote` is the client address the server sees; `203.0.113.9:4000` when empty.

### `bitetest.Response`

```go
type Response struct {
	Status int
	Header map[string][]string
	Body   []byte
}
```

Response is one response read back. `Status` is the status code, `Header` holds the header fields with their names lower-cased, and `Body` is the body bytes (a chunked body is decoded). Use `Get` to read a header by any case of its name.

### `bitetest.Response.JSON`

```go
func (r Response) JSON(v any) error
```

JSON decodes the body into `v`, a pointer, as a client of the app reads it: with the app's own codec and naming policy ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)), so an answer fills the handler's own output type, an `Opt`, a `Null`, a `Date` and a tagged union included (`created_at` fills `CreatedAt` under `SnakeCase`, a union's variant is chosen by its discriminator). The read is lenient where a request is strict: a member `v` has no field for is skipped, and a member the answer left out leaves its field zero, even one a request would require. A member of the wrong type, or a body that is not JSON, returns an error. For a type the app's codec has no plan for (`map[string]any`, `any`, a type with a channel field), and for a `Response` built by hand with no app behind it, the body is read with `encoding/json/v2` instead, matching a member to a field ignoring case, underscores and dashes. A test of the exact wire names compares `Text`. [`bitetest.JSON`](#bitetestjson) is the same decoding for a test that wants the value.

### `bitetest.JSON`

```go
func JSON[T any](tb testing.TB, r Response) T
```

JSON decodes the body of `r` as a `T`, as `Response.JSON` does, and returns it. A body that is not JSON of that shape fails the test with `tb.Fatalf`, naming the status, the type, the error and the body. It does not look at the status: check it first, since a problem+json answer decoded as a success type gives the fields it shares and zero for the rest.

```go
resp := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7"})
if resp.Status != 200 {
	t.Fatalf("%d %s", resp.Status, resp.Text())
}
got := bitetest.JSON[User](t, resp)
```

### `bitetest.Response.Text`

```go
func (r Response) Text() string
```

Text is the body as a string.

### `bitetest.Response.Get`

```go
func (r Response) Get(name string) string
```

Get returns the first value of a header, by any case of its name, or the empty string when there is none.

## The WebSocket client

A WebSocket route is tested by dialling it over the same in-memory connections ([ADR 0074](../adr/0074-the-bitetest-websocket-client-reads-on-its-own-goroutine-and-times-out-in-real-time.md)). Every method fails the test with `tb.Fatal` rather than returning an error, except `TryDialWS` and `TryRead`, which return what a test about a refusal or a close needs.

### `bitetest.WSRequest`

```go
type WSRequest struct {
	Target string
	Header map[string][]string
	// Remote is the client address the server sees; "203.0.113.9:4000" if empty.
	Remote string
	NoAutoPong bool
	Timeout time.Duration
}
```

WSRequest is the handshake to send. `Target` is the path and query of the route. `Header` adds fields (an `Origin`, a cookie), each with its values as in `Request.Header`, or replaces the `Host`, which is `test`; the upgrade headers and the key are set by the client. `NoAutoPong` stops the client answering the server's pings, for the test of the missed pong. `Timeout` is how long a read waits in real time (five seconds when zero); the server's own timeouts run on the app's clock and are moved with `Clock.Advance`.

### `bitetest.Server.DialWS`

```go
func (s *Server) DialWS(r WSRequest) *WS
```

DialWS opens a WebSocket to `r.Target` and requires the 101; it fails the test, printing the answer, otherwise. It also checks the `Sec-WebSocket-Accept` the server sent.

### `bitetest.Server.TryDialWS`

```go
func (s *Server) TryDialWS(r WSRequest) (*WS, Response)
```

TryDialWS sends the handshake and returns the client when the server answered 101, or a nil client and the answer (a 400 naming a field, a 401 from a Provider, a 403 for an origin, a 404, a 405, a 426) when it did not.

### `bitetest.WS`

```go
type WS struct {
	// Response is the 101 the server answered.
	Response Response
	// Has unexported fields.
}
```

WS is a WebSocket client after a successful handshake. One goroutine reads from the moment it is dialled, so the server's writes never wait on the test, and it answers each ping with a pong of the same payload before delivering the ping, unless `NoAutoPong` is set. Because it reads eagerly it cannot show a slow reader, and it sends only well-formed frames: the tests of [ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md) and of protocol errors use `Server.Dial` and bytes. The Send methods and `Close` may be called from several goroutines; the Read methods are for the test's goroutine.

### `bitetest.WS.SendText`

```go
func (w *WS) SendText(s string)
```

SendText sends `s` as one text message.

### `bitetest.WS.SendBinary`

```go
func (w *WS) SendBinary(p []byte)
```

SendBinary sends `p` as one binary message. A route whose message type is a struct closes the socket with 1003.

### `bitetest.WS.SendJSON`

```go
func (w *WS) SendJSON(v any)
```

SendJSON sends `v` as one text message, written as the app writes JSON: with its naming policy and its codec, so the route's own message type, with no `json` tags, arrives under the wire names the route reads (`{"text":...}` under `SnakeCase`, not `{"Text":...}`). A value the app's codec cannot write fails the test with the reason; send hand-written JSON with `SendText`.

### `bitetest.WS.SendFragments`

```go
func (w *WS) SendFragments(parts ...string)
```

SendFragments sends one text message split over several frames, one per part, the last with the final bit set.

### `bitetest.WS.Ping`

```go
func (w *WS) Ping(payload string)
```

Ping sends a ping with `payload` (at most 125 bytes); the server answers with a pong of the same payload.

### `bitetest.WS.Pong`

```go
func (w *WS) Pong(payload string)
```

Pong sends an unsolicited pong, which the server counts as a sign of life ([ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md)).

### `bitetest.WS.Close`

```go
func (w *WS) Close(code uint16, reason string)
```

Close sends a Close frame with a status and a reason. It does not wait for the server's answer: read it with `ReadClose` or `ExpectClose`, then `ExpectEOF`.

### `bitetest.WS.Leave`

```go
func (w *WS) Leave()
```

Leave drops the connection with no Close frame, as a client whose network died does; the server's `OnClose` sees 1006.

### `bitetest.WS.TryRead`

```go
func (w *WS) TryRead(d time.Duration) (Message, error)
```

TryRead returns the next message the server sent, or the error that ended the connection (`io.EOF` when the server closed it), or a timeout error when nothing came in `d` of real time (the `WSRequest`'s `Timeout` when `d` is zero). After the connection ended every call returns the same error.

### `bitetest.WS.Read`

```go
func (w *WS) Read() Message
```

Read returns the next message of any kind, pings and pongs included, and fails the test if the connection ended or nothing came in time.

### `bitetest.WS.ReadData`

```go
func (w *WS) ReadData() Message
```

ReadData returns the next text or binary message, skipping pings and pongs (a heartbeat that fell due while the test moved the clock), and fails the test on anything else.

### `bitetest.WS.ReadText`

```go
func (w *WS) ReadText() string
```

ReadText returns the next text message as a string, and fails the test if it is binary (see `ReadData`).

### `bitetest.WS.ReadJSON`

```go
func (w *WS) ReadJSON(v any)
```

ReadJSON decodes the next text message into `v` as [`Message.JSON`](#bitetestmessagejson) does, with the app's codec, so `var f ServerFrame; ws.ReadJSON(&f); f.Activity.Get()` works on the route's own types (see `ReadData`). A message that does not decode fails the test.

### `bitetest.WS.ReadPing`

```go
func (w *WS) ReadPing() string
```

ReadPing returns the payload of the next ping, skipping pongs, and fails the test on any other message. The client has already answered it unless `NoAutoPong` is set.

### `bitetest.WS.ReadClose`

```go
func (w *WS) ReadClose() (code uint16, reason string)
```

ReadClose returns the status and reason of the server's Close, skipping pings and pongs, and fails the test if a data message or the end of the connection comes first.

### `bitetest.WS.ExpectClose`

```go
func (w *WS) ExpectClose(code uint16) string
```

ExpectClose requires the server's Close to carry `code` and returns its reason.

### `bitetest.WS.ExpectEOF`

```go
func (w *WS) ExpectEOF()
```

ExpectEOF requires the connection to end, discarding any messages still in flight.

### `bitetest.WS.Quiet`

```go
func (w *WS) Quiet()
```

Quiet fails the test if the server has sent the socket a data message or a Close the test has not read. It is [`Pending`](#bitetestwspending) that fails when the list is not empty, so it waits exactly as Pending does (a ping with a payload of its own, read up to its pong, with no real-time wait) and the failure names the first message that arrived. It is how a test says that something did not arrive, such as a Topic's message for a private room read by the socket of someone outside it: publish, then call `Quiet` on that socket. A message a handler or an application goroutine has not queued yet when the ping is read is not waited for; a message sent only after the clock moves is tested by moving the clock first. The test fails if the connection ended or the pong does not come within the `WSRequest`'s `Timeout`. **Three cases pass too early**, because the frame is queued by a goroutine the ping does not wait for: a frame that another socket's handler publishes (ping that socket first, so its handler has run, then call `Quiet` on this one), a frame a socket's `OnClose` sends after the client closed it (wait for the close to be handled, such as with `waitFor` on the Topic's `Len`), and a frame carried by a broker that delivers on its own goroutine, as `modules/pgnotify` does and `bitetest.Broker` does after [`Async`](#bitetestbrokerasync) (the test of such a path calls [`Broker.Settle`](#bitetestbrokersettle) before `Quiet`, or reads the frame it expects and asserts the absence only once that frame arrived).

### `bitetest.WS.Pending`

```go
func (w *WS) Pending() []Message
```

Pending returns the text and binary messages the server queued for the socket before the call, in the order they were sent, and removes them from what the test has still to read; it returns nil when there are none. It is for a test that checks what arrived without knowing how many messages to read, such as the frames another socket's handler produced after the test pinged that socket. It waits as `Quiet` does, and the three cases that pass too early for `Quiet` are too early for it. If the server's Close is among the messages it is the last one returned, with `Kind` `Close`, and Pending does not wait for a pong, since a closed connection answers nothing; a test that wants no Close checks the last `Kind`. The test fails if the connection ended with no Close, or the pong does not come within the `WSRequest`'s `Timeout`.

### `bitetest.Message`

```go
type Message struct {
	Kind Kind
	// Data is the payload of a text, binary, ping or pong message; for a Close, the raw payload.
	Data []byte
	// Code and Reason are the status and reason of a Close; Code is 1005 when none was sent.
	Code   uint16
	Reason string
}
```

Message is one thing the server sent: a whole text or binary message (fragments are joined), a ping or pong with its payload, or a Close.

### `bitetest.Message.Text`

```go
func (m Message) Text() string
```

Text is `Data` as a string.

### `bitetest.Message.JSON`

```go
func (m Message) JSON(v any) error
```

JSON decodes `Data` into `v`, a pointer, as the app's clients read it: with the app's codec and naming policy, an `Opt`, a `Null` and a tagged union included, with the leniency and the fallback of [`Response.JSON`](#bitetestresponsejson).

### `bitetest.Kind`

```go
type Kind uint8
```

Kind is what a `Message` is. It has a `String` method that gives its name (`text`, `binary`, `ping`, `pong`, `close`).

### `bitetest.Kind.String`

```go
func (k Kind) String() string
```

String is the name of the kind.

### `bitetest.Text`

```go
const Text Kind = ...
```

The kind of a text message.

### `bitetest.Binary`

```go
const Binary Kind = ...
```

The kind of a binary message.

### `bitetest.Ping`

```go
const Ping Kind = ...
```

The kind of a ping.

### `bitetest.Pong`

```go
const Pong Kind = ...
```

The kind of a pong.

### `bitetest.Close`

```go
const Close Kind = ...
```

The kind of a Close frame; its `Code` and `Reason` are set.

## The event stream client

### `bitetest.Server.OpenEvents`

```go
func (s *Server) OpenEvents(r Request) *EventStream
```

OpenEvents sends `r` (a GET when `Method` is empty) on a new connection and reads the head of the answer, which must be a 200 with the content type `text/event-stream`: the answer of a route that returns `bites.SSE` or `bites.Events[T]`. Any other answer fails the test with its status and body; test such an answer with `Do`. The events are then read with `Next` as the app sends them, and the connection is closed when the test ends, or earlier by `Close`.

### `bitetest.EventStream`

```go
type EventStream struct {
	Status int
	Header map[string][]string
	// Has unexported fields.
}
```

EventStream is a Server-Sent Events answer being read as it arrives. `Status` is always 200 and `Header` holds the answer's header fields with their names lower-cased. One goroutine reads from the moment the stream is opened, so the app never waits on the test.

### `bitetest.EventStream.Next`

```go
func (e *EventStream) Next() Event
```

Next returns the next event or comment, and fails the test if the stream ended or nothing came in five real seconds.

### `bitetest.EventStream.TryNext`

```go
func (e *EventStream) TryNext(d time.Duration) (Event, error)
```

TryNext returns the next event or comment, `io.EOF` once the app has ended the stream, or a timeout error when nothing came in `d` of real time (five seconds when `d` is zero). After the stream ended every call returns the same error.

### `bitetest.EventStream.Close`

```go
func (e *EventStream) Close()
```

Close closes the connection, as a client that leaves; the app's stream function sees its context cancelled.

### `bitetest.Event`

```go
type Event struct {
	ID      string
	Name    string
	Data    string
	Retry   time.Duration
	Comment string
}
```

Event is one thing a Server-Sent Events answer sent, as it was written: `ID`, `Name` (the `event` field, empty when the app sent none), `Data` (the data fields joined by `\n`) and `Retry`, or, for a comment line, only `Comment`. An event with only a `Retry`, which a browser would not dispatch, is returned too. A handler that writes a comment as soon as it is ready (after subscribing to what it streams) lets a test wait for it before it publishes.

## The API document

### `bitetest.Document`

```go
func Document(tb testing.TB, app *bites.App, path string)
```

Document holds the API document of `app` to the JSON file at `path`, so a change to the contract a client is built from is a failing test with the lines that moved ([ADR 0075](../adr/0075-the-api-document-is-held-to-a-committed-file-by-a-structural-diff.md)). The document is [`App.OpenAPIDocument`](./openapi.md#appopenapidocument), never the answer of the document route, so a middleware that refuses the route cannot put its body in the file. It is compared in a canonical form: keys sorted, two-space indentation, one trailing newline. On a difference the test fails with one line per change, `-` for what only the file has, `+` for what only the app has and `~ old -> new` for a value that moved, each under a JSON Pointer, in a fixed order, the first 40 and the count of the rest, and then the command that accepts the app's document. A file holding the same document in another layout fails and says so. With `BITETEST_UPDATE=1` in the environment the directory is made, the file is written and the test passes. A file that does not exist fails the test with that command and is not written. An App without `OpenAPI`, a document that cannot be built and a file that is not JSON also fail the test.

```text
bitetest.Document: the API document differs from testdata/openapi.json (2 changes, - is in the file, + is in the app):
	~ /components/schemas/User/properties/email/type: "string" -> "null"
	+ /paths/~1teams: {"get":{"operationId":"getTeams","responses":{"200":{"content":{"application/jso...
to accept the app's document, run
	BITETEST_UPDATE=1 go test -run '^TestTheAPIDocument$' .
```

### `bitetest.UpdateEnv`

```go
const UpdateEnv = "BITETEST_UPDATE"
```

UpdateEnv is the name of the environment variable that makes [`Document`](#bitetestdocument) write its file: `1` or `true`, for one run. Any other value, or none, compares.

### `bitetest.DiffDocuments`

```go
func DiffDocuments(before, after []byte) ([]string, error)
```

DiffDocuments compares two JSON documents structurally and returns one line per change, or none when they are the same document whatever their layout or key order. [`Document`](#bitetestdocument) is built on it; call it to compare two builds of an App, the document before a refactor and the one after. Object keys are visited in sorted order, depth first, and array elements by index, so the lines come in the same order every run. A line is `- /pointer: value` (only in `before`), `+ /pointer: value` (only in `after`) or `~ /pointer: old -> new`; `/` in a key is written `~1` and `~` is `~0`, and the root is `/`. A value is compact JSON cut at 80 bytes with `...`. An added or removed object or array is one line. An array is compared by index, so an element inserted at the front reads as a change to each element after it. Numbers are compared as `float64`. It returns an error when either side is not JSON, and it returns every line: only `Document` caps them.

## The clock

### `bitetest.Clock`

```go
type Clock struct{ *clock.Fake }
```

Clock is a clock that moves only when `Advance` is called. It satisfies `bites.Clock`. Give it to the App with `bites.WithClock` before starting it, so a timeout (`Timeout`, `HeadTimeout`, `BodyTimeout`, `IdleTimeout`, WebSocket heartbeats) is reached by advancing the clock instead of waiting. The methods of the embedded fake clock are:

- `Now() time.Time`: the fake time.
- `AfterFunc(d time.Duration, fn func())`: schedules `fn` for `d` from the fake now.
- `Advance(d time.Duration)`: moves the clock forward by `d`, running every timer that falls due, earliest first, each with the clock reading its own time. Timer functions run on the caller's goroutine, and a timer set for zero or less is due at the next `Advance`.
- `Pending() int`: the number of timers waiting to fire.
- `Next() (time.Time, bool)`: the time of the earliest pending timer. A test uses it to wait until the code under test has set the deadline it is about to expire, without sleeping.

<!-- compiles: decls -->

```go
func TestTimeout(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk))
	started := make(chan struct{})
	app.Get("/slow", func(ctx context.Context, in struct{}) (bites.Text, error) {
		close(started)
		<-ctx.Done()
		return bites.Text{}, ctx.Err()
	}, bites.Use(bites.Timeout(time.Second)))
	srv := bitetest.Start(t, app)

	done := make(chan bitetest.Response, 1)
	go func() { done <- srv.Do(bitetest.Request{Method: "GET", Target: "/slow"}) }()
	<-started
	clk.Advance(time.Second)
	if res := <-done; res.Status != 503 {
		t.Fatalf("got %d", res.Status)
	}
}
```

### `bitetest.NewClock`

```go
func NewClock() *Clock
```

NewClock returns a Clock reading 2026-01-01 00:00:00 UTC.

## Testing across instances

### `bitetest.Broker`

```go
type Broker struct { /* unexported */ }
```

Broker is an in-memory [`bites.Broker`](./websocket.md#broker) for tests ([ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)). One Broker is one instance of the application on a bus; give it to the Topics made with [`bites.NewTopic`](./websocket.md#newtopic) of one app, and give its `Peer()` to those of a second app. A Publish on either reaches the sockets of both, in the order of the calls and on the publisher's goroutine, so a test needs no sleep; after [`Async`](#bitetestbrokerasync) each subscription delivers on a goroutine of its own, as `modules/pgnotify` does. Make one with `NewBroker`; the zero value is not usable.

### `bitetest.NewBroker`

```go
func NewBroker() *Broker
```

NewBroker returns a Broker on a new bus.

### `bitetest.Broker.Peer`

```go
func (b *Broker) Peer() *Broker
```

Peer returns a Broker on the same bus: another instance of the application.

### `bitetest.Broker.Async`

```go
func (b *Broker) Async()
```

Async makes every subscription on the bus deliver on a goroutine of its own, in the order of the Publish calls for that subscription, as `modules/pgnotify` does: Publish returns before the sockets have the message. Use it for a test of a path that production reaches through such a broker, where the frame may arrive after the code that published it moved on; the test then reads the frame it expects, or calls [`Settle`](#bitetestbrokersettle) and then `Quiet` or `Pending`, instead of assuming it arrived. Leave it off, the default, for every other test: delivery on the publisher's goroutine needs no wait. Call it before the first Publish. A subscription that stopped is delivered nothing more, queued payloads included.

### `bitetest.Broker.Fail`

```go
func (b *Broker) Fail(err error)
```

Fail makes every Publish on the bus return `err` and deliver nothing, as a broker that is down does, until it is called with nil.

### `bitetest.Broker.Limit`

```go
func (b *Broker) Limit(n int)
```

Limit makes every Publish on the bus of a payload over `n` bytes return a [`*bites.PayloadError`](./websocket.md#payloaderror) and deliver nothing, as a broker with a limit of its own does. Zero, the state of a new bus, is no limit; a negative `n` panics.

### `bitetest.Broker.Gap`

```go
func (b *Broker) Gap()
```

Gap tells every subscription on the bus that messages may have been lost, as a real broker does once its connection is back: each Topic's `OnGap` runs, on the calling goroutine.

### `bitetest.Broker.Subscriptions`

```go
func (b *Broker) Subscriptions(name string) int
```

Subscriptions is how many Topics named `name` are listening on the bus. A Topic listens while it has a socket, so a test waits on it to know a socket has joined.

### `bitetest.Broker.Accepts`

```go
func (b *Broker) Accepts(name string) error
```

Accepts accepts any name; it is part of `bites.Broker`.

### `bitetest.Broker.Publish`

```go
func (b *Broker) Publish(ctx context.Context, name string, payload []byte) error
```

Publish delivers `payload` to every live subscription to `name` on the bus, this Broker's included, on the calling goroutine or, after `Async`, on that of each subscription, or returns the error `Fail` set, or a `*bites.PayloadError` for a payload over `Limit`. A Topic calls it; a test calls `Topic.Publish`.

### `bitetest.Broker.Settle`

```go
func (b *Broker) Settle()
```

Settle returns once the bus is idle: no payload is queued and none is being delivered, under `Async`. It is how a test asserts that something did not arrive without a real-time wait: publish, `Settle`, then ask the receiving side (`WS.Quiet`, a counter the subscriber would have bumped). It waits for every delivery queued before the call and for the deliveries those publish in turn, since it returns only when nothing is queued or running; a chain that never ends, or a publisher on another goroutine that keeps publishing, keeps it waiting. Without `Async` delivery has finished when Publish returns, so Settle returns at once. It does not wait for what a delivery causes after its callback returned: a frame the Topic wrote to a socket still travels through the in-memory connection, which [`WS.Quiet`](#bitetestwsquiet) and [`WS.Pending`](#bitetestwspending) wait for with a ping, so call `Settle` first and `Quiet` after it. `Gap` callbacks run on the caller of `Gap` and are not deliveries. Called from inside a subscription's callback under `Async` it panics, because it would wait for its own delivery; call it from the test's goroutine, after the Publish that started the chain.

### `bitetest.Broker.Subscribe`

```go
func (b *Broker) Subscribe(name string, deliver func([]byte), gap func()) (stop func())
```

Subscribe registers `deliver` and `gap` for `name`; `stop` ends the subscription and forgets it, so the Broker holds nothing of a Topic that has stopped. A Topic calls it; a test does not.

## Conformance suites

Each suite runs one subtest for each promise of an interface's doc comment, so a store or a `Broker` written outside the core, in a module or an application, is checked from its own test ([ADR 0086](../adr/0086-a-store-is-held-to-its-interface-by-a-conformance-suite-in-bitetest.md), [testing](../guide/testing.md#testing-a-store-you-wrote)). The factory is called once for each subtest and must return a store with no state of any earlier one; it may call `t.Cleanup`. A nil factory fails the test with a message that says what to give.

### `bitetest.TestRateLimitStore`

```go
func TestRateLimitStore(t *testing.T, newStore func(t *testing.T) bites.RateLimitStore)
```

TestRateLimitStore checks a [`bites.RateLimitStore`](./security.md#ratelimitstore): a bucket of `burst` requests that earns one back every `interval`, `Remaining`, `RetryAfter` and `Reset` as the interface defines them, a refused request that spends nothing, a `now` earlier than one already seen that gives nothing back, keys that never share a bucket (the empty key, emails, invalid UTF-8 and a NUL byte included), and `Take` atomic for a key under concurrent callers (60 rounds of 8 callers on a burst of 1, 40 callers on a burst of 5, and 12 keys at once). The suite gives `Take` its own times and moves them, never sleeping, so a store must decide with the `now` it is given. Durations are compared to within a millisecond.

### `bitetest.TestSessionStore`

```go
func TestSessionStore(t *testing.T, newStore func(t *testing.T) bites.SessionStore)
```

TestSessionStore checks a [`bites.SessionStore`](./security.md#sessionstore): `Load` of an id never saved, deleted or expired is not found and not an error, `Save` keeps data byte for byte (binary, empty and 1 MiB) and replaces data and expiry, `Delete` of an id with nothing is not an error, ids that differ in a character do not share a session, and concurrent saves, loads and deletes are safe. Expiry is judged on `bites.Now(ctx)`: the suite gives every call a context of an app on a `bitetest.Clock`, expiries relative to that clock, and moves it with `Advance` instead of sleeping, so a store that reads `time.Now()` or a database's clock fails. It calls `Save`, so a store written only for owned types calls `TestOwnerSessionStore` alone.

### `bitetest.TestOwnerSessionStore`

```go
func TestOwnerSessionStore(t *testing.T, newStore func(t *testing.T) bites.OwnerSessionStore, suite ...OwnerSessionSuite)
```

TestOwnerSessionStore checks the owner half of a [`bites.OwnerSessionStore`](./security.md#ownersessionstore): `SaveOwned` keeps data and replaces the owner an id had, an empty owner is a session that is kept and that `DeleteOwner` never finds, and `DeleteOwner` with `keep` empty, `keep` a session of the owner, `keep` a session of another owner, an unknown `keep` and an unknown owner returns the count of sessions forgotten and touches no other owner's. A store calls `TestSessionStore` as well. An owner is a User's id: one with a NUL byte is not tested. With no third argument the suite saves sessions for owners it names itself ("alice", "bob" and six more) and for an empty owner; a store whose owner column refers to a users table passes one [`OwnerSessionSuite`](#bitetestownersessionsuite). More than one fails the test with `2 OwnerSessionSuite values given: give one, with the Owner and NoEmptyOwner the store needs`.

### `bitetest.OwnerSessionSuite`

```go
type OwnerSessionSuite struct {
	Owner        func(t *testing.T, s bites.OwnerSessionStore, name string) string
	NoEmptyOwner bool
}
```

OwnerSessionSuite is what `TestOwnerSessionStore` needs when the store cannot take the owners the suite would choose; its zero value is the default.

`Owner` returns the owner the suite saves sessions under for `name`, one of "alice", "bob", "nobody", "Alice", "alice ", "ali", "alice@example.com" and "café". It is called in each subtest, after `newStore`, with that subtest's store and at most once for a name, so it can create the real row the owner column refers to and remove it with `t.Cleanup`. It must return a non-empty owner and a different one for each name: otherwise the test fails with `OwnerSessionSuite.Owner returned an empty owner for "alice": return the id of a real owner, and set NoEmptyOwner if the store refuses an empty one`, or `OwnerSessionSuite.Owner returned "x" for both "alice" and "bob": return a different owner for each name`. Returning `name` is right for a text column; for an id that cannot be spelled like the names (a UUID) return one for each name, and the subtest about owners that differ in case, space or prefix then holds the ids apart and no more. Nil means the names as they are.

`NoEmptyOwner` is true for a store that refuses an empty owner, as a foreign key does. The subtest about a session saved with an empty owner is skipped, and the skip says why in the test output. The store then cannot keep an anonymous session: the session type's `SessionOwner` must never return "". `bites` cannot refuse that when the app is built, because `SessionOwner` is read from each session value at `Save` and `Renew` and `NewSessions` sees only the type, so a session with an empty owner is the store's error at the request that saves it. Give a visitor who is not signed in a session type with no `SessionOwner`. False means the store keeps a session with an empty owner that `DeleteOwner` never finds, as the interface says.

```go
bitetest.TestOwnerSessionStore(t, newSessions, bitetest.OwnerSessionSuite{
	Owner: func(t *testing.T, s bites.OwnerSessionStore, name string) string {
		return insertUser(t, s.(*mySessions).db, name) // a users row, removed by ON DELETE CASCADE or t.Cleanup
	},
	NoEmptyOwner: true,
})
```

### `bitetest.BrokerSuite`

```go
type BrokerSuite struct {
	New        func(t *testing.T) (a, b bites.Broker)
	Name       string
	Refused    []string
	MaxPayload int
	Wait       time.Duration
}
```

BrokerSuite is what `TestBroker` needs. `New` is required: it returns two Brokers that reach each other (the instances of one application on one bus, channel or database), started, and ends them with `t.Cleanup`. `Name` is the Topic name every subtest uses, `conformance.topic` when empty, and must be one the Brokers accept. `Refused` lists names whose `Accepts` must be an error, and the subtest is skipped when it is empty. `MaxPayload` is the largest payload in bytes a Publish to `Name` carries; the suite publishes one of that size, which must be delivered whole, and one a byte larger, which must be a [`*bites.PayloadError`](./websocket.md#payloaderror) (found with `errors.As`) whose `Name` is `Name`, `Size` the larger payload and `Limit` exactly `MaxPayload`, and must deliver nothing; zero skips both subtests. `Wait` is how long a delivery is waited for before the test fails, 5 seconds when zero, and a fiftieth of it is how long the suite listens for a delivery that must not come.

### `bitetest.TestBroker`

```go
func TestBroker(t *testing.T, s BrokerSuite)
```

TestBroker checks a [`bites.Broker`](./websocket.md#broker): `Accepts` is nil for a name it carries and an error for one it cannot; a Publish reaches a subscription on a peer and on the publishing Broker, every subscription exactly once; one publisher's payloads arrive in the order of its calls, and two publishers at once lose nothing; names are separate streams; each payload given to `deliver` is its own bytes, unchanged when the publisher reuses its slice, when a subscriber writes on its copy and when later payloads arrive; `stop` ends its subscription and no other; and the payload limit is an error that names it and sends nothing. The payloads are JSON, as a Topic's are, so a Broker that carries only text is checked fairly. A `gap` is not tested, since the suite cannot make a Broker lose its connection.

