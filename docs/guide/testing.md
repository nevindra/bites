# Testing

**`bitetest` runs your app through its real engine over in-memory connections, so a test is a request in and a response out with no port, and time moves only when the test says.**

**Reference:** [`bitetest`](../reference/bitetest.md) · **Design:** [Testing](../design/testing.md)

Nothing is skipped between the bytes and the handler: a request goes through the engine's reader and parser, the router, the middleware, the decoding and the writer exactly as a socket's would, minus the kernel ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)). So a 400 that names a field, a body that is too large, a client that leaves mid-handler, and a timeout reached by advancing a clock are all ordinary tests. What it cannot simulate is the kernel: a reset connection, a partial write and a full socket buffer are covered by the engine's own loopback tests.

The examples test this small app. Putting registration in a function that both `main` and the tests call is the habit the page ends on.

<!-- compiles: decls -->
```go
type GetUser struct {
	Path struct{ ID int64 }
}

type User struct {
	ID   int64
	Name string
}

func getUser(ctx context.Context, in GetUser) (User, error) {
	return User{ID: in.Path.ID, Name: "Ada"}, nil
}

func slow(ctx context.Context, in struct{}) (User, error) {
	<-ctx.Done()
	return User{}, nil
}

// newApp registers every route; main calls it too.
func newApp(opts ...bites.Option) *bites.App {
	app := bites.New(opts...)
	app.Get("/users/{id}", getUser)
	app.Get("/slow", slow, bites.Use(bites.Timeout(2*time.Second)))
	return app
}
```

## Sending a request and reading the answer

**[`bitetest.Start`](../reference/bitetest.md#biteteststart) serves the app and stops it when the test ends; [`Server.Do`](../reference/bitetest.md#bitetestserverdo) sends one request and returns the response.**

<!-- compiles: decls -->
```go
func TestGetUserAnswersTheUser(t *testing.T) {
	srv := bitetest.Start(t, newApp())

	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7"})

	if resp.Status != 200 {
		t.Fatalf("status %d: %s", resp.Status, resp.Text())
	}
	if ct := resp.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	got := bitetest.JSON[User](t, resp)
	if got.ID != 7 || got.Name != "Ada" {
		t.Fatalf("got %+v", got)
	}
}
```

A [`bitetest.Request`](../reference/bitetest.md#bitetestrequest) has `Method`, `Target` (the path and query), `Header`, `Body` and `Remote` (the client address the server sees, `203.0.113.9:4000` when empty). `Host` is `test`, and `Content-Length` is set from `Body` unless `Header` gives them. A [`bitetest.Response`](../reference/bitetest.md#bitetestresponse) has `Status`, `Header` (names lower-cased) and `Body`, and three helpers: [`Text`](../reference/bitetest.md#bitetestresponsetext) is the body as a string, [`JSON(&v)`](../reference/bitetest.md#bitetestresponsejson) decodes it, and [`Get(name)`](../reference/bitetest.md#bitetestresponseget) is the first value of a header by any case of its name. [`bitetest.JSON[T](t, resp)`](../reference/bitetest.md#bitetestjson) decodes the body as a `T` and fails the test, with the status and the body, when it does not decode. Both decode with the app's own codec and [naming policy](./requests.md#wire-names), as a client of the app reads the answer, so `User` needs no `json` tags to read back the `id` and `name` the app wrote, and an `Opt`, a `Null` or a tagged union the handler returns reads into the handler's own type, with no second copy of it for the test. The read is lenient: a member `User` has no field for is skipped and a member the answer left out stays zero, so check a value by comparing it. A `map[string]any` or an `any` is read with `encoding/json` instead; compare `Text()` to test the exact wire names. `Do` opens a connection for each call, reads one response, and fails the test if none arrives in five real seconds.

## What the app logs

**`bitetest.Start` gives an app that has no logger of its own the test's log, so a passing test prints nothing and a failing one shows what the app said.** An App logs a dozen start-up lines about its settings, and every handler error and recovered panic with its cause; to the default logger that was fourteen lines per app, every test, whether it failed or not. Through `tb.Log` they appear with `go test -v` and under a failing test, in order, where the failure is. To assert on the log, or to see it always, give the app a logger of your own with `bites.WithLogger`: `Start` leaves it alone.

<!-- compiles: body -->
```go
var buf bytes.Buffer
app = bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
```

## Testing what a client gets when it is wrong

**A mistake of the client is a problem+json answer, and a test checks the status, the type and the located error.**

<!-- compiles: decls -->
```go
func TestAPathIdThatIsNotANumberIsA400NamingThePathId(t *testing.T) {
	srv := bitetest.Start(t, newApp())

	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/users/abc"})

	if resp.Status != 400 || resp.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
	problem := bitetest.JSON[struct {
		Errors []struct{ Location, Message string }
	}](t, resp)
	if len(problem.Errors) != 1 || problem.Errors[0].Location != "path.id" {
		t.Fatalf("%+v", problem)
	}
}
```

The exchange it checks:

```http
GET /users/abc HTTP/1.1
Host: test

HTTP/1.1 400 Bad Request
Content-Type: application/problem+json

{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer","rule":"type"}]}
```

Name tests as sentences about behaviour, as above; they read as the specification of the route.

## Moving time

**Give the app a [`bitetest.Clock`](../reference/bitetest.md#bitetestclock) and a timeout is reached by advancing it, with nothing to sleep for.** [`bitetest.NewClock()`](../reference/bitetest.md#bitetestnewclock) reads 2026-01-01 00:00:00 UTC and moves only when `Advance` is called. Pass it with [`WithClock`](../reference/app.md#withclock) before the app starts.

<!-- compiles: decls -->
```go
func TestASlowHandlerIsAnswered503WhenTheTimeoutPasses(t *testing.T) {
	clk := bitetest.NewClock()
	srv := bitetest.Start(t, newApp(bites.WithClock(clk)))

	done := make(chan bitetest.Response, 1)
	go func() { done <- srv.Do(bitetest.Request{Method: "GET", Target: "/slow"}) }()
	for clk.Pending() == 0 { // wait for the handler to start the Timeout's timer
		time.Sleep(time.Millisecond)
	}

	clk.Advance(3 * time.Second)

	if resp := <-done; resp.Status != 503 {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}
```

The clock has [`Advance(d)`](../reference/bitetest.md#bitetestclock), `Now()`, `Next()` (when the next timer is due, and whether there is one) and `Pending()` (how many timers are set). The same clock moves the body timeout, the idle timeout, the access log's durations and the [`Timeout`](./first-party-middleware.md#cutting-off-a-slow-handler) middleware's deadline ([limits](./limits.md#timeouts)).

## Writing the bytes yourself

**[`Server.Dial`](../reference/bitetest.md#bitetestserverdial) opens a connection as a client at an address, and [`Server.Read`](../reference/bitetest.md#bitetestserverread) reads one response from it.** Use them for what `Do` cannot say: two pipelined requests, a body that stops half way, a client that leaves.

<!-- compiles: decls -->
```go
func TestTwoRequestsOnOneConnectionAreAnsweredInOrder(t *testing.T) {
	srv := bitetest.Start(t, newApp())
	c := srv.Dial("203.0.113.9:4000")
	defer c.Close()

	fmt.Fprint(c, "GET /users/1 HTTP/1.1\r\nHost: test\r\n\r\n")
	first := srv.Read(c, false)
	fmt.Fprint(c, "GET /users/2 HTTP/1.1\r\nHost: test\r\n\r\n")
	second := srv.Read(c, false)

	if first.Status != 200 || second.Status != 200 || !strings.Contains(second.Text(), `"id":2`) {
		t.Fatalf("%d %s, %d %s", first.Status, first.Text(), second.Status, second.Text())
	}
}
```

The second argument of `Read` says the request was a HEAD, so no body is read. The address you pass is what [`Request.RemoteAddr`](../reference/middleware.md#requestremoteaddr) reports.

## Testing a WebSocket

**A test that reads what an Observer recorded calls `srv.WaitEnded()` after the request.** An Observer's End runs after the last byte ([ADR 0051](../adr/0051-a-request-is-observed-when-it-starts-and-when-it-has-really-ended.md)), so `Do` can return before the access record, the counter or the span exists. [`WaitEnded`](../reference/bitetest.md#bitetestserverwaitended) returns once every request the server received has ended, with no sleep and no loop; it fails the test after five real seconds if one does not, as for a handler held on the fake clock.

**A WebSocket test dials the route with `DialWS`, sends text, binary or JSON, reads what the server sends, and closes with a status.** The client does the handshake and the masking, reads on its own goroutine so the server never waits on the test, answers the server's pings as a browser does, and gives up after five real seconds so a test never hangs ([ADR 0074](../adr/0074-the-bitetest-websocket-client-reads-on-its-own-goroutine-and-times-out-in-real-time.md)).

<!-- compiles -->
```go
package socket_test

import (
	"context"
	"testing"
	"time"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type Echo struct {
	Text string
}

func TestAWebSocketEchoesAMessageAndAnswersAClose(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk))
	app.WebSocket("/echo", func(ctx context.Context, in struct{}, s *bites.Socket[Echo]) (bites.Inbox[Echo], error) {
		return bites.Inbox[Echo]{
			OnMessage: func(ctx context.Context, m Echo) error { return s.Send(m) },
		}, nil
	})
	srv := bitetest.Start(t, app)
	ws := srv.DialWS(bitetest.WSRequest{Target: "/echo"})

	ws.SendJSON(Echo{Text: "hello"})
	var got Echo
	ws.ReadJSON(&got)
	if got.Text != "hello" {
		t.Fatalf("got %q", got.Text)
	}

	clk.Advance(30 * time.Second) // a heartbeat falls due
	ws.ReadPing()                 // the client has already answered it

	ws.Close(1000, "done")
	ws.ExpectClose(1000)
	ws.ExpectEOF()
}
```

A refused handshake (a 400 that names a field, a 401 from a Provider, a 403 for an origin) is tested with `TryDialWS`, which returns the HTTP answer and no client. `NoAutoPong: true` in the `WSRequest` stops the client answering pings, so a test can watch the server close it with 1008 at the second missed pong; `ReadClose`, `Quiet` (nothing arrived, without a wait), `Pending` (what arrived, without knowing how many to read) and `Leave` (a client that drops with no Close frame) cover the rest of a socket's life. The client reads eagerly and sends only well-formed frames, so a slow reader or a malformed frame is still a test written by hand over `Dial`, as the repository's own tests do ([WebSockets](./websockets.md)).

The client's methods, each linked to its reference entry:

| Method | What it does |
|---|---|
| [`func (s *Server) DialWS(r WSRequest) *WS`](../reference/bitetest.md#bitetestserverdialws) | opens a socket; fails the test unless the handshake is a 101 |
| [`func (s *Server) TryDialWS(r WSRequest) (*WS, Response)`](../reference/bitetest.md#bitetestservertrydialws) | a nil `*WS` and the HTTP answer when the handshake is refused |
| [`func (w *WS) SendText(s string)`](../reference/bitetest.md#bitetestwssendtext) | one text frame |
| [`func (w *WS) SendBinary(p []byte)`](../reference/bitetest.md#bitetestwssendbinary) | one binary frame |
| [`func (w *WS) SendJSON(v any)`](../reference/bitetest.md#bitetestwssendjson) | one text frame, encoded under the app's naming policy |
| [`func (w *WS) SendFragments(parts ...string)`](../reference/bitetest.md#bitetestwssendfragments) | one text message split into frames |
| [`func (w *WS) Ping(payload string)`](../reference/bitetest.md#bitetestwsping) | a ping |
| [`func (w *WS) Pong(payload string)`](../reference/bitetest.md#bitetestwspong) | an unsolicited pong |
| [`func (w *WS) Close(code uint16, reason string)`](../reference/bitetest.md#bitetestwsclose) | a Close frame |
| [`func (w *WS) Leave()`](../reference/bitetest.md#bitetestwsleave) | drops the connection with no Close frame |
| [`func (w *WS) Read() Message`](../reference/bitetest.md#bitetestwsread) | the next message of any kind, pings and pongs included; fails the test if nothing comes in time |
| [`func (w *WS) TryRead(d time.Duration) (Message, error)`](../reference/bitetest.md#bitetestwstryread) | the next message, or the error that ended the connection (`io.EOF`), or a timeout error after `d` |
| [`func (w *WS) ReadData() Message`](../reference/bitetest.md#bitetestwsreaddata) | the next text or binary message, skipping pings and pongs |
| [`func (w *WS) ReadText() string`](../reference/bitetest.md#bitetestwsreadtext) | the next text message |
| [`func (w *WS) ReadJSON(v any)`](../reference/bitetest.md#bitetestwsreadjson) | the next text message decoded into `v` with the app's codec, so an `Opt` or a union fills |
| [`func (w *WS) ReadPing() string`](../reference/bitetest.md#bitetestwsreadping) | the next ping's payload |
| [`func (w *WS) ReadClose() (code uint16, reason string)`](../reference/bitetest.md#bitetestwsreadclose) | the status and reason of the server's Close, skipping pings and pongs |
| [`func (w *WS) ExpectClose(code uint16) string`](../reference/bitetest.md#bitetestwsexpectclose) | fails unless the server closes with `code`; returns the reason |
| [`func (w *WS) ExpectEOF()`](../reference/bitetest.md#bitetestwsexpecteof) | fails unless the connection ends, discarding messages still in flight |
| [`func (w *WS) Quiet()`](../reference/bitetest.md#bitetestwsquiet) | fails if the server sent a message the test has not read; waits for what the server already queued, with a ping, and no real interval |
| [`func (w *WS) Pending() []Message`](../reference/bitetest.md#bitetestwspending) | returns, in order, the messages the server queued before the call (a Close last), removing them from what is left to read; waits as `Quiet` does |

## Testing a stream of events

**A route that returns `bites.SSE` or `bites.Events[T]` is read with `OpenEvents`, one event at a time as the app sends it.** `Do` waits for the end of an answer, which a live stream does not have. [`OpenEvents`](../reference/bitetest.md#bitetestserveropenevents) reads the head, fails the test if the answer is not a 200 stream, and returns an [`EventStream`](../reference/bitetest.md#bitetesteventstream) whose `Next` returns each [`Event`](../reference/bitetest.md#bitetestevent) (its id, name, data and retry) or comment, and fails the test after five real seconds of silence. Its three methods are [`Next() Event`](../reference/bitetest.md#bitetesteventstreamnext), [`TryNext(d time.Duration) (Event, error)`](../reference/bitetest.md#bitetesteventstreamtrynext), which returns an error after `d` instead of failing, and [`Close()`](../reference/bitetest.md#bitetesteventstreamclose).

<!-- compiles -->
```go
package orders_test

import (
	"context"
	"testing"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func TestOrderEvents(t *testing.T) {
	updates := make(chan string)
	app := bites.New()
	app.Get("/orders/{id}/events", func(_ context.Context, in struct{ Path struct{ ID int64 } }) (bites.SSE, error) {
		return bites.SSE{Func: func(ctx context.Context, e bites.EventWriter) error {
			if err := e.Comment("ready"); err != nil { // the test waits for this before it publishes
				return err
			}
			for state := range updates {
				if err := e.Send(bites.Event{Name: "status", Data: state}); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	})
	srv := bitetest.Start(t, app)

	events := srv.OpenEvents(bitetest.Request{Target: "/orders/7/events"})
	events.Next() // the "ready" comment
	updates <- "paid"
	if ev := events.Next(); ev.Name != "status" || ev.Data != "paid" {
		t.Fatalf("%+v", ev)
	}
	close(updates)
}
```

A stream that was refused (a 401, a 404 for an unknown order) is tested with `Do`, as any answer that is not a stream. `TryNext` returns `io.EOF` once the stream ends, and `Close` leaves as a client would, which cancels the stream function's context ([Streaming](./streaming.md)).

## Testing shutdown

**Shutdown with no [`WithShutdownDelay`](../reference/observability.md#withshutdowndelay) closes the listener at once, so a request a test sends after it fails, and the failure says so.** To test what a client sees while the app drains (readiness turning 503, requests still served), give the App a delay and a [`bitetest.Clock`](../reference/bitetest.md#bitetestclock): the delay is measured on the app's clock, so `Shutdown` waits on its own goroutine until the test advances the clock past it ([ADR 0052](../adr/0052-health-and-readiness-follow-shutdown.md)).

<!-- compiles -->
```go
package orders_test

import (
	"context"
	"testing"
	"time"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func TestReadinessFailsAndRequestsAreServedDuringTheShutdownDelay(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(
		bites.WithClock(clk),
		bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}),
		bites.WithShutdownDelay(5*time.Second),
	)
	app.Get("/orders", func(context.Context, struct{}) (bites.NoContent, error) { return bites.NoContent{}, nil })
	srv := bitetest.Start(t, app)

	stopped := make(chan error, 1)
	go func() { stopped <- app.Shutdown(context.Background()) }()
	for clk.Pending() == 0 { // wait for Shutdown to start the delay's timer
		time.Sleep(time.Millisecond)
	}

	if resp := srv.Do(bitetest.Request{Method: "GET", Target: "/readyz"}); resp.Status != 503 {
		t.Fatalf("readiness during the delay: %d", resp.Status)
	}
	if resp := srv.Do(bitetest.Request{Method: "GET", Target: "/orders"}); resp.Status != 204 {
		t.Fatalf("a request during the delay: %d", resp.Status)
	}

	clk.Advance(5 * time.Second)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
```

## Testing a service

**`bitetest.Start` runs the `Start` function of every [`Service`](../reference/app.md#service) before it returns, so the first request of a test sees a started service, and a `Start` that fails fails the test with its error.** The end-of-test cleanup (`Close`) runs each `Stop` with a context that is already done and waits for it, so a test leaks no worker. To see `BeforeShutdown` and a graceful `Stop`, call `app.Shutdown` yourself ([ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md)). Their deadlines are real time, never the app's clock.

<!-- compiles -->
```go
package orders_test

import (
	"context"
	"testing"
	"time"

	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

func TestAWorkerRunsWhileTheAppServesAndStopsAtShutdown(t *testing.T) {
	running, ended := make(chan struct{}), make(chan struct{})
	app := bites.New()
	app.Service(bites.Service{
		Name: "worker",
		Start: func(ctx context.Context) error {
			go func() { close(running); <-ctx.Done(); close(ended) }()
			return nil
		},
		Stop: func(context.Context) error { <-ended; return nil },
	})
	bitetest.Start(t, app)
	<-running

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
```

**A job made with [`bites.Every`](../reference/app.md#every) runs on the app's clock, so a test gives the app a `bitetest.Clock` and advances it instead of sleeping.** The first timer is armed before `bitetest.Start` returns, so the first `Advance` cannot miss it; after a run the job arms the next, so a test that advances again waits for it with `clk.Next()`.

<!-- compiles: decls -->
```go
func TestThePurgeRunsAfterOneInterval(t *testing.T) {
	clk := bitetest.NewClock()
	ran := make(chan struct{}, 1)
	app := bites.New(bites.WithClock(clk))
	app.Service(bites.Every("purge", time.Hour, func(context.Context) error { ran <- struct{}{}; return nil }))
	bitetest.Start(t, app)

	clk.Advance(time.Hour)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the purge did not run after an hour on the app's clock")
	}
}
```

## Holding the API document to a file

**The document is the contract your clients are built from, and a change to a handler's types changes it with no error.** A property becomes nullable and required, a `default` is lost, a component takes another name, a field's schema turns into `{}`: each compiles and each breaks a client. [`bitetest.Document`](../reference/bitetest.md#bitetestdocument) keeps the document in a file in the repository and fails when the app's differs.

<!-- compiles: decls -->
```go
func TestTheAPIDocumentIsTheOneInTestdata(t *testing.T) {
	app := newApp()
	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
	bitetest.Document(t, app, "testdata/openapi.json")
}
```

Record the file the first time, and again whenever you meant the change, with the environment variable and no flag, so it works for one package or for `./...`:

```text
BITETEST_UPDATE=1 go test -run TestTheAPIDocumentIsTheOneInTestdata .
```

The diff of the file in review is then the diff of the contract. A file that does not exist fails the test instead of being written, so a first run in CI cannot pass by recording whatever it found; the message gives the command. When the test fails, it names each change under a JSON Pointer (`~1` is a `/` in a path), `-` for what the file has and the app lost, `+` for what the app added and `~` for a value that moved:

```text
bitetest.Document: the API document differs from testdata/openapi.json (2 changes, - is in the file, + is in the app):
	~ /components/schemas/User/properties/email/type: "string" -> "null"
	+ /paths/~1teams: {"get":{"operationId":"getTeams","responses":{"200":{"content":{"application/jso...
```

The document comes from [`App.OpenAPIDocument`](../reference/openapi.md#appopenapidocument), so register the routes and call `OpenAPI` first; it does not go through the server and cannot record a 401 from a middleware. To compare two builds of an app (before and after a refactor) without a file, [`bitetest.DiffDocuments`](../reference/bitetest.md#bitetestdiffdocuments) takes the two sets of bytes and returns the same lines. See `examples/users/main_test.go` for the test in a program. Decision: [ADR 0075](../adr/0075-the-api-document-is-held-to-a-committed-file-by-a-structural-diff.md).

## Catching every refusal in a test

**A route that cannot work panics when it is registered, so a test that builds the app fails with the message; it does not wait for the deploy.** This is why `newApp` exists: `main` and the test call the same function.

<!-- compiles: decls -->
```go
func TestTheAppRegisters(t *testing.T) {
	_ = newApp() // a RegistrationError fails the test with its message
}
```

A Provider whose dependency nothing provides, an Input whose path wildcard has no field and a middleware given a bad option are all refused here ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)). A test that starts the whole app with `bitetest.Start` also covers the middleware chain and the OpenAPI document, which is built when the app starts serving.

## Testing the text of a refusal

**A refusal is a [`*RegistrationError`](../reference/app.md#registrationerror), and a test recovers it and reads its text.** It is an error, so `errors.As` finds it ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

<!-- compiles: decls -->
```go
type NoPath struct{}

// refusal returns the RegistrationError that register panics with, or nil.
func refusal(register func()) (re *bites.RegistrationError) {
	defer func() {
		if r := recover(); r != nil {
			err, _ := r.(error)
			if !errors.As(err, &re) {
				panic(r)
			}
		}
	}()
	register()
	return nil
}

func TestAPatternWithAWildcardNeedsAPathField(t *testing.T) {
	re := refusal(func() {
		bites.New().Get("/users/{id}", func(ctx context.Context, in NoPath) (User, error) {
			return User{}, nil
		})
	})
	if re == nil {
		t.Fatal("the route was accepted")
	}
	want := "GET /users/{id}: the pattern has {id}, but NoPath has no Path section"
	if !strings.HasPrefix(re.Error(), want) || !strings.Contains(re.Error(), "registered at ") {
		t.Fatalf("got %q", re.Error())
	}
}
```

The message is the route, the reason and where the route was registered, so a test can pin all three:

```text
GET /users/{id}: the pattern has {id}, but NoPath has no Path section
	registered at users_test.go:59
```

Pin the start of the message and the presence of `registered at`, not the line number, which moves when the file does. The same function checks the constructors of the [first-party middleware](./first-party-middleware.md) (a `CORS` with no origin), `WithLimits` with a negative field, and a group's late `Use`.

## Testing a store you wrote

**A store or a `Broker` of your own is held to the promises of its interface by a suite, so a promise you did not read in the doc comment fails with its sentence.** `bitetest` has one for each: [`TestRateLimitStore`](../reference/bitetest.md#bitetesttestratelimitstore), [`TestSessionStore`](../reference/bitetest.md#bitetesttestsessionstore), [`TestOwnerSessionStore`](../reference/bitetest.md#bitetesttestownersessionstore) and [`TestBroker`](../reference/bitetest.md#bitetesttestbroker) ([ADR 0086](../adr/0086-a-store-is-held-to-its-interface-by-a-conformance-suite-in-bitetest.md)). Each takes a function that returns a new store for each subtest (a new table, a new prefix), so no subtest hears another:

```go
func TestMyStoreKeepsTheContract(t *testing.T) {
	bitetest.TestRateLimitStore(t, func(t *testing.T) bites.RateLimitStore { return newMyStore(t) })
	bitetest.TestSessionStore(t, func(t *testing.T) bites.SessionStore { return newMySessions(t) })
	bitetest.TestOwnerSessionStore(t, func(t *testing.T) bites.OwnerSessionStore { return newMySessions(t) })
	bitetest.TestBroker(t, bitetest.BrokerSuite{
		New: func(t *testing.T) (bites.Broker, bites.Broker) { return newMyBroker(t), newMyBroker(t) }, // two instances that reach each other
	})
}
```

A test of a Topic across instances uses [`bitetest.NewBroker`](../reference/bitetest.md#bitetestnewbroker), which delivers on the publisher's goroutine: a Publish has reached every Socket's queue when it returns, so `Quiet` and a read need no wait. `modules/pgnotify` delivers on a goroutine of its own, and the order of events differs there; call [`Async`](../reference/bitetest.md#bitetestbrokerasync) on the Broker for a test of a path that production reaches that way, and call [`Settle`](../reference/bitetest.md#bitetestbrokersettle) on the Broker before asserting that a frame did not arrive, because `Quiet` alone passes before an asynchronous frame is queued: `Settle` returns once every queued delivery has run, and the ping of `Quiet` then waits for the connection side, so `broker.Publish(...)`, `broker.Settle()`, `outsider.Quiet()` needs no sleep. `Settle` also covers deliveries that publish again, and it panics when called from inside a subscription's callback. Reading the frame the test expects first still works. Run `TestBroker` against the Broker in both modes if your own Broker's tests use both.

A store whose owner column is a foreign key to a users table cannot be saved under the owners the suite names ("alice", "bob") and refuses an empty owner, so `TestOwnerSessionStore` takes a third argument, a [`bitetest.OwnerSessionSuite`](../reference/bitetest.md#bitetestownersessionsuite): its `Owner` creates the real row for each name the suite asks for (after `newStore`, with that subtest's store) and returns the owner to save under, and `NoEmptyOwner: true` skips the subtest about an empty owner and says so. That switch means the session type's `SessionOwner` must never return "", which `NewSessions` cannot check because the owner is read from each value at run time.

A store that implements `OwnerSessionStore` calls both session suites; one whose `Save` returns an error (written only for owned types) calls only `TestOwnerSessionStore`. The rate limit suite moves the `now` it gives `Take`, so it proves the store decides with that time and not its own clock. The session suites give every call a context of an app on a `bitetest.Clock` and move that clock instead of sleeping, so a session store judges expiry with `bites.Now(ctx)`: one that reads `time.Now()` finds the sessions of the suite expired at once and fails (a store that hands the expiry to a server, such as a Redis TTL, computes it as `expires` minus `bites.Now(ctx)`). `BrokerSuite` also takes the names `Accepts` must refuse and the largest payload the Broker carries, which are skipped when empty. The suites need nothing but the standard library and `bites`.
