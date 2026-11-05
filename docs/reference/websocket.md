# WebSockets

**The WebSocket routes, the Socket a handler sends on, the Inbox that receives messages, the Topic that broadcasts, the SocketOwners that closes the sockets of one User, and the limits and origin checks of a route.**

**Guide:** [WebSockets](../guide/websockets.md) · **Design:** [WebSockets](../design/websocket.md)

A WebSocket route is typed like an HTTP route, and so are its messages ([ADR 0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md)). Its handler is `func(ctx context.Context, in In, s *Socket[Out]) (Inbox[Msg], error)`. The upgrade request is an HTTP request: the Input is decoded and checked exactly as an HTTP route's is (its Sections, its Providers, `Validate`, the refusals at Registration and the 400 policy), and the middleware around the route runs, all before the 101 and after the Origin check, so a request that would be refused is never upgraded. An error the handler returns before the Inbox is the answer in place of the upgrade, and the Socket it was given is dead. The returned Inbox carries the functions the engine calls with the socket's messages and its close; there is no `Read` for user code, because the engine owns the read loop and an idle socket holds no goroutine ([ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md)).

Messages: `Msg` is a struct, slice or map (decoded from the JSON of each text message), `[]byte` (the bytes of a text or binary frame, copied), or `Raw[T]` (decoded, with the exact bytes kept). `Out` is any type JSON can write, sent as a text frame, or `[]byte`, sent as a binary frame. Any `Msg` other than `[]byte` answers a binary frame with a close of 1003.

The handshake answers are 405 with `Allow: GET` for another method, 400 problem+json for a malformed handshake, 426 with `Sec-WebSocket-Version: 13` for another version and 403 problem+json for a refused Origin. The handshake needs no `Origin` header: a request with none is accepted, because browsers always send one.

Allocations: an `OnMessage` costs 1 (the message's slab) plus 1 for each list the message fills; a `Send` costs 1 (the frame); a `Topic.Publish` costs 1 up to 64 subscribers and 3 to 4 beyond ([ADR 0003](../adr/0003-the-trade-budget.md)).

What Registration refuses (rows of the refusal table, after the label `WebSocket /x:`):

| Mistake | Reason in the message |
|---|---|
| a nil handler | `the handler is nil` |
| a wildcard and no `Path` section | `the pattern has {id}, but Empty has no Path section` |
| an Input with a `Body` | `BodyOnGet.Body is a Body section, but a GET route has no request body` |
| an Input field no Provider covers | `WithAuth.Auth is not a section (Path, Query, Header, Cookie or Body), and no Provider of string covers this route` |
| a Socket that sends a status wrapper | `the handler's Socket sends OK[...], one of the framework's response types; a WebSocket message is a plain value, written as JSON (or []byte, written as a binary frame), and has no status or headers` |
| an `Out` that cannot be written as JSON | `the Socket's message type cannot be written as JSON: chan int: a field of kind chan cannot be encoded` |
| a `Msg` that is a number or other scalar | `the Inbox's message type is int; a message is a struct, slice or map (read as JSON), []byte or Raw[T]` |
| a `Msg` that holds a `Form` or an `Upload` | `the Inbox's message type holds UploadInJSON, which is a kind of request body; a message is JSON, []byte or Raw[T]` |
| a `Msg` JSON cannot decode into | `the Inbox's message type cannot be read from JSON: struct { C chan int }.C: a field of kind chan cannot be decoded` |
| `Returns` | `Returns lists the answers of a handler that returns an interface, but a WebSocket handler returns an Inbox and answers 101: its messages are the Socket's and the Inbox's types` |
| `MediaType` or `SendsHeader` | `SendsHeader states what a handler's answer sends, but a WebSocket route answers 101 with no body, and its messages are the Socket's type` |
| `WithGoAwaySpread` of zero or below, or given twice | `WithGoAwaySpread: the window is 0s, which is not a window: give a positive duration such as 10*time.Second, or leave the option out for the default of 3s` |
| `MaxMessage` of zero or below | `MaxMessage(0) is not a limit: it must be a positive number of bytes` |
| `MaxMessage` above the reassembly budget | `MaxMessage is 134217728 bytes, above the app's reassembly budget of 67108864 bytes (Limits.ReassemblyBudget), so a message of that size could never be assembled: lower MaxMessage or raise the budget` |

The origin refusals are under [`AllowOrigins`](#alloworigins).

<!-- compiles: decls -->

```go
type JoinRoom struct {
	Path struct{ ID int64 }
}

type ChatIn struct{ Text string }

type ChatOut struct {
	From string
	Text string
}

type Rooms struct {
	topic bites.Topic[ChatOut]
}

func (r *Rooms) Join(ctx context.Context, in JoinRoom, s *bites.Socket[ChatOut]) (bites.Inbox[ChatIn], error) {
	r.topic.Subscribe(s)
	return bites.Inbox[ChatIn]{
		OnMessage: func(ctx context.Context, msg ChatIn) error {
			return r.topic.Publish(ChatOut{From: "someone", Text: msg.Text})
		},
		OnMalformed: func(ctx context.Context, raw []byte, err error) error {
			return s.Send(ChatOut{From: "server", Text: "could not read that"})
		},
		OnClose: func(info bites.CloseInfo) {},
	}, nil
}
```

<!-- compiles: body -->

```go
rooms := &Rooms{}
app.WebSocket("/rooms/{id}", rooms.Join, bites.MaxMessage(8<<10), bites.AllowOrigins("https://app.example.com"))
```

Guide: [WebSockets](../guide/websockets.md). Decisions: [ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md), [ADR 0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md), [ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md).

## Registering

### `App.WebSocket`

```go
func (a *App) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption)
```

WebSocket registers a WebSocket route: a GET whose handshake runs the pipeline of a typed route. The `Origin` is checked first, before the Providers and the Input run ([`AllowOrigins`](#alloworigins)). The route options it takes are `MaxMessage`, `AllowOrigins`, `AllowAnyOrigin`, `Use`, `Provide`, `Override` and `Tag`. A message sent from the handler before it returns goes out after the 101. A mistake in the types panics now.

### `Group.WebSocket`

```go
func (g *Group) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption)
```

WebSocket registers a WebSocket route under the group's prefix. See [`App.WebSocket`](#appwebsocket).

A protocol of several kinds of frame is a tagged union in each direction, a struct of `Opt` variants with a [`Discriminator`](./input.md#discriminator-method) method: `Inbox[ClientFrame]` reads `{"type":"join","room":7}` into `ClientFrame.Join`, and `Socket[ServerFrame]` writes the one variant set ([ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)).

## The Socket

### `Socket`

```go
type Socket[Out any] struct {
	// Has unexported fields.
}
```

Socket is the handle a WebSocket route's handler is given for sending to one client. It is safe to use from any goroutine and safe to keep: it is never reused for another connection, so a Socket kept after its connection closed only returns [`ErrSocketClosed`](#errsocketclosed) ([ADR 0008](../adr/0008-no-pooled-object-reaches-the-user.md)). Server push from outside a request, such as a Postgres `LISTEN` loop, is a goroutine of the application's calling `Send` or `Publish`.

### `Socket.Send`

```go
func (s *Socket[Out]) Send(v Out) error
```

Send encodes `v` on the calling goroutine and queues the frame; it never blocks. An `Out` of `[]byte` is sent as a binary frame and every other `Out` as a text frame holding its JSON. It fails with `ErrSocketClosed` once the socket is closing or closed, and with [`ErrSendQueueFull`](#errsendqueuefull), closing the socket with 1008, when its queue would pass `Limits.SendQueue`. It returns an encoding error for a value JSON cannot write, and for a value of a [tagged union](input.md#discriminator-method) whose present variant its [`ServerOnly`](input.md#serveronly-method) method names: the error is a [`*ServerOnlyError`](#serveronlyerror), found with `errors.As`, which names the union, the variant's tag and its field; nothing is sent and the socket stays open.


### `ServerOnlyError`

```go
type ServerOnlyError = jsoncodec.ServerOnlyError // fields: Union reflect.Type; Variant, Field string
```

ServerOnlyError is what `Socket.Send`, a Topic's publish to its sockets and an answer's encoding return when a [tagged union](input.md#discriminator-method) holds a variant its [`ServerOnly`](input.md#serveronly-method) method names: such a variant is never sent to a client. `Union` is the union's type, `Variant` the tag of the variant that was set and `Field` its Go field. Find it with `errors.As`; turn the value into a variant a client may receive first, which a Topic's `Resolve` does ([ADR 0083](../adr/0083-a-json-value-of-several-kinds-is-a-tagged-union-of-opt-variants.md)).
### `Socket.Close`

```go
func (s *Socket[Out]) Close(status uint16, reason string) error
```

Close starts the close handshake with a status and a reason. The status must be one a server may send (1000 to 1003, 1007 to 1014, 3000 to 4999) and the reason at most 123 bytes of UTF-8; otherwise it returns an error and leaves the socket open. Later sends fail with `ErrSocketClosed`; the connection ends when the client answers or after five seconds.

### `Socket.Context`

```go
func (s *Socket[Out]) Context() context.Context
```

Context is cancelled when the socket starts to close.

### `ErrSocketClosed`

```go
var ErrSocketClosed = engine.ErrWSClosed
```

ErrSocketClosed is what a Socket returns once its connection is closed or closing, and what a Socket kept after its connection ended always returns. Test with `errors.Is(err, bites.ErrSocketClosed)`.

### `ErrSendQueueFull`

```go
var ErrSendQueueFull = engine.ErrWSQueueFull
```

ErrSendQueueFull is what `Send` returns when the message would overflow the socket's send queue (`Limits.SendQueue`, 1 MiB) or the app's (`Limits.SendBudget`, 64 MiB); the socket is closed, with 1008 for its own queue and 1013 for the app's budget ([ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md)). Test with `errors.Is(err, bites.ErrSendQueueFull)`.

## Receiving

### `Inbox`

```go
type Inbox[Msg any] struct {
	OnMessage   func(ctx context.Context, msg Msg) error
	OnMalformed func(ctx context.Context, raw []byte, err error) error
	OnClose     func(info CloseInfo)
}
```

Inbox is what a WebSocket route's handler returns on upgrade: the functions the engine calls with the socket's messages and its close, and through their closures the socket's state. A function that is nil is skipped, with two exceptions: a malformed message with no `OnMalformed` closes the socket with 1007, and any message to an Inbox with no `OnMessage` closes it with 1003 and the reason `this socket takes no messages`. A socket that only sends leaves `OnMessage` nil and takes `struct{}` as its `Msg`.

- `OnMessage` receives each message; nil means the socket only sends, decoded from JSON into `Msg` (a `[]byte` `Msg` receives the bytes of a text or binary frame, a `Raw[T]` keeps the exact bytes). It runs for one socket's messages one at a time and in order, on the connection's own goroutine, and may block: that pauses reading from this socket alone, which is the inbound backpressure. An error closes the socket with 1011, unless a [`CloseError`](#closeerror) in its chain chooses the status; a panic closes with 1011 too, and is recovered. The context is cancelled when the socket starts to close.
- `OnMalformed` receives a message that did not decode: its bytes, which are its own, and the `*MalformedError`. Returning nil drops the message and keeps the socket; an error closes it with 1011, or with the status of a `CloseError` in its chain.
- `OnClose` is called once, after the last `OnMessage`, when the connection has ended, on the connection's goroutine. A panic in it is recovered and logged.

The inbound limits close the socket with the status RFC 6455 gives: a message over `MaxMessage` with 1009, one not finished in `MessageTimeout` with 1008, messages being assembled over `ReassemblyBudget` with 1013, a binary frame to a route whose message is not `[]byte` with 1003, a text message that is not UTF-8 with 1007, a frame that is not masked with 1002 ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)).

### `CloseError`

```go
type CloseError struct {
	Status uint16
	Reason string
}
```

CloseError is an error `OnMessage` or `OnMalformed` returns to close the socket with a status of its own instead of 1011 ([ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)). It is found through wrapping with `errors.As`, as a [`StatusError`](./errors.md#statuserror) is. The status must be one a server may send (1000 to 1003, 1007 to 1014, 3000 to 4999) and the reason at most 123 bytes of UTF-8. One that is not cannot be caught before it is returned, so the socket is then closed with 1011 and the mistake is logged at Error. The reason is sent to the client, so it holds nothing internal.

```go
return &bites.CloseError{Status: 4001, Reason: "token expired"}
```

### `CloseError.Error`

```go
func (e *CloseError) Error() string
```

Error is `bites: close the socket with ` followed by the status and the reason.

### `CloseInfo`

```go
type CloseInfo = engine.WSCloseInfo
```

CloseInfo says how a WebSocket ended. Its fields are:

- `Status uint16`: the close status, the client's when it closed, the server's when the server did, and 1006 for a connection that ended with no close handshake.
- `Reason string`: the text that came with the status.
- `Remote bool`: true when the client started the close.
- `Cause CloseCause`: why it ended, one of the [causes](#closecause) below; set on every socket that ends.
- `Err error`: why a connection ended with no close handshake, the read or write error.

A `Shutdown` ends every socket with 1001 (`server is shutting down`) and `Remote` false ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)).

### `CloseCause`

```go
type CloseCause = engine.WSCause
```

CloseCause says why a WebSocket ended, as `CloseInfo.Cause` and as an Observer's `SocketClosed` is told ([ADR 0063](../adr/0063-opentelemetry-is-a-module-whose-observer-records-the-server-span-and-names-it-to-the-core.md)). The set is fixed, so `String` is safe as a metric label; the zero value is not a cause. Its one method is `String() string`, the label: `client`, `server`, `handler_error`, `queue_full`, `pong_timeout`, `shutdown`, `client_fault`, `lost`, and `unknown` for the zero value. The first thing to start the close stands: a client's close that arrives after the server's own is not the cause.

### `CauseClient`

`CauseClient` (`client`): the client started the close handshake.

### `CauseServer`

`CauseServer` (`server`): the application closed the socket, with `Socket.Close` or a returned `CloseError`.

### `CauseHandlerError`

`CauseHandlerError` (`handler_error`): `OnMessage` or `OnMalformed` returned an error or panicked; the status is 1011.

### `CauseQueueFull`

`CauseQueueFull` (`queue_full`): a send overflowed the socket's queue (`Limits.SendQueue`) or the app's send budget (`Limits.SendBudget`), the case [`Send`](#socketsend) reports as `ErrSendQueueFull`: a reader too slow.

### `CausePongTimeout`

`CausePongTimeout` (`pong_timeout`): the client missed `Limits.MissedPongs` pings in a row.

### `CauseShutdown`

`CauseShutdown` (`shutdown`): the server was shutting down; the status is 1001.

### `CauseClientFault`

`CauseClientFault` (`client_fault`): the client broke the protocol or a limit on what it may send: a message too large or unfinished, a binary frame to a text route, a malformed message with no `OnMalformed`, a message to a socket that takes none.

### `CauseLost`

`CauseLost` (`lost`): the connection ended with no close handshake; the status is 1006.

### `MalformedError`

```go
type MalformedError struct{ Errors []FieldError }
```

MalformedError is why a message did not decode into the route's message type: the located errors, in the message's wire names, located as `message.<field>` and not `body.<field>`. `OnMalformed` receives it as its `err`; read it with `errors.As(err, &me)` with `var me *bites.MalformedError`, and test for the kind with `errors.Is(err, bites.ErrMalformedMessage)`.

### `MalformedError.Error`

```go
func (e *MalformedError) Error() string
```

Error returns `bites: malformed message`, followed by a colon and the first located error when there is one.

### `MalformedError.Is`

```go
func (e *MalformedError) Is(target error) bool
```

Is reports whether `target` is `ErrMalformedMessage`.

### `ErrMalformedMessage`

```go
var ErrMalformedMessage = errors.New("bites: malformed message")
```

ErrMalformedMessage is matched by `errors.Is` on the error an Inbox's `OnMalformed` receives.

## Broadcasting

### `Topic`

```go
type Topic[Out any] struct {
	// Has unexported fields.
}
```

Topic is a set of Sockets that one published message reaches, and of the functions [`Listen`](#topiclisten) added, so the server's own code hears what the Sockets hear. The zero value is ready and must not be copied. `Publish` encodes and frames the message once and queues the same frame on every subscriber ([ADR 0038](../adr/0038-a-broadcast-is-framed-once-and-shared.md)); a Socket leaves every Topic it is in when it closes, so nothing of the application's leaks a closed socket. A Topic is safe for concurrent use. Every Socket of a Topic must be a `Socket[Out]` of the same app: the first one's encoder is used. A Topic made with [`NewTopic`](#newtopic) reaches the sockets of every instance of the application through a [`Broker`](#broker) ([ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)).

### `Topic.Subscribe`

```go
func (t *Topic[Out]) Subscribe(s *Socket[Out])
```

Subscribe adds `s` to the topic. Subscribing twice, or a Socket that never opened or has closed, does nothing.

### `Topic.SubscribeUnless`

```go
func (t *Topic[Out]) SubscribeUnless(s *Socket[Out], skip func(v Out) bool)
```

SubscribeUnless adds `s` to the topic with a function that decides, for each message, whether `s` is left out: `skip` returns true and `s` does not get that message, which the other Sockets still get. It is for what differs from one Socket to the next on a Topic they share, such as the people a User muted or blocked: `skip` reads the User's current state, so muting needs no new subscription. The message is still encoded and framed once, and one that every Socket skips is not encoded. `skip` runs for each message on the goroutine that publishes (on a Topic made with [`NewTopic`](#newtopic), the broker's), outside the Topic's lock, so it must be quick, must not block and must not change `v`. A panic in `skip` is recovered and logged at Error, and `s` does not get that message: a filter that fails leaves the Socket out rather than letting through what it was meant to hold back. Calling it again replaces the function, [`Subscribe`](#topicsubscribe) takes it away, and a Socket that never opened or has closed is not added. A nil `skip` panics: use `Subscribe`. A Topic with no such function pays nothing more per `Publish`. Decision: [ADR 0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md).

### `Topic.Unsubscribe`

```go
func (t *Topic[Out]) Unsubscribe(s *Socket[Out])
```

Unsubscribe removes `s` from the topic.

### `Topic.Listen`

```go
func (t *Topic[Out]) Listen(fn func(Out)) (stop func())
```

Listen calls `fn` with every message the topic delivers until `stop` is called: on a Topic made with [`NewTopic`](#newtopic), every message the broker delivers from any instance, this one's included, in the broker's order; on a zero Topic, every message published in the process. It is how the server hears a Topic, as a cache that drops an entry on every instance when one of them publishes its key, with [`TopicOptions.OnGap`](#topicoptions) to clear it when messages may have been lost. A Topic made with `NewTopic` listens to its broker while it has a Socket or a function, so it needs no Socket. `fn` runs on the goroutine that delivers and must not block: the broker's, one message after another, or on a zero Topic the one that called `Publish`. Every function and Socket gets the same value, so `fn` must not change a slice or map in it. A panic in `fn` is recovered and logged at Error with the topic's name. `stop` may be called more than once and from any goroutine, `fn` included; a message delivered after it returns never reaches `fn`. A nil `fn` panics. Decision: [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md).

### `Topic.Resolve`

```go
func (t *Topic[Out]) Resolve(fn func(Out) (Out, bool))
```

Resolve gives a Topic made with [`NewTopic`](#newtopic) the function each instance runs on every message the broker delivers, before its Sockets and the functions of [`Listen`](#topiclisten) get it: they get what `fn` returns, and `false` drops the message on this instance. It is how a Topic carries a reference: a message too large for the broker (Postgres takes under 8000 bytes) is published as its id, and each instance reads the full message from its database in `fn`. `fn` runs on the broker's delivery goroutine, one message after another, so bound its work (a query with a timeout); a panic in it is recovered, logged at Error with the topic's name, and drops that message. `PublishLocal` does not run it. When `Out` is a tagged union, make the reference a variant its [`ServerOnly`](input.md#serveronly-method) method names: it crosses the broker, the API document and the clients never see it, and a message that still holds it after `fn`, or on a Topic with no `Resolve`, is not sent to the Sockets (a Warn names the topic and the variant) while [`Listen`](#topiclisten) functions get it. Call it once, right after `NewTopic`; a second call, a zero Topic and a nil `fn` panic. A program whose broker is optional (an App built only to write its API document has none) uses a zero Topic and does not call `Resolve`: a nil broker given to `NewTopic` in production is a mistake, and stays a panic. Decision: [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md).

### `Topic.Publish`

```go
func (t *Topic[Out]) Publish(v Out) error
```

Publish sends `v` to every subscriber and never blocks: a subscriber whose queue is full is closed and does not hold the others. The error is the encoding's.

On a Topic made with [`NewTopic`](#newtopic), Publish hands `v` to the broker instead and returns what the broker says, so it can wait for the broker's round trip (5 s at most for `pgnotify`). The sockets of every instance, this one's included, receive `v` when the broker delivers it, so a message published here and by another instance arrives in one order everywhere, and never twice on the publisher's own sockets. A Publish that fails delivers nothing, locally either. A message over `MaxPayload`, or over the broker's own limit, is a [`*PayloadError`](#payloaderror): find it with `errors.As` to publish a reference instead. Allocations: 7 for one local subscriber and for ten (the encoding, the decoding, the frame and the in-memory broker's copies, [ADR 0003](../adr/0003-the-trade-budget.md)).

### `Topic.PublishLocal`

```go
func (t *Topic[Out]) PublishLocal(v Out) error
```

PublishLocal sends `v` to the subscribers in this process only, never through the broker, with Publish's guarantees of one frame and no blocking for the Sockets, and calls the functions `Listen` added with it. On a Topic without a broker it is Publish. It is for a message about this instance, such as telling its sockets to reload after a gap ([`TopicOptions.OnGap`](#topicoptions)).

### `Topic.Len`

```go
func (t *Topic[Out]) Len() int
```

Len is the number of Sockets subscribed; the functions `Listen` added are not counted.

### `Topics`

```go
type Topics[Out any] struct {
	// Has unexported fields.
}
```

Topics is a family of Topics named by a key known only at run time: a room per id, the frames of one User, the activity of one Space. Each key is a Topic of its own, made when its first Socket subscribes and dropped when its last one leaves, so a family holds only the keys that have a Socket on this instance and the application keeps no map of Topics. `Publish` to a key reaches its Sockets on every instance even when this one has none, which a Topic, held only where it has Sockets, cannot do. The zero value is the family of one process and is ready; [`NewTopics`](#newtopics) makes one that crosses instances. It must not be copied and is safe for concurrent use. A name the code knows (a lobby, a cache every instance clears) is one [`Topic`](#topic); a name made from data is a key of a Topics. Key only from what the handler knows (the User's id from a Provider, a room id it checked), never from a client's message, or a client chooses what the family holds. Decision: [ADR 0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md).

### `Topics.Subscribe`

```go
func (ts *Topics[Out]) Subscribe(key string, s *Socket[Out]) error
```

Subscribe adds `s` to the Topic of `key`, every message of it reaching `s` as [`Topic.Subscribe`](#topicsubscribe) does; the Topic is made if this is its first Socket. It returns an error, and subscribes nothing, for an empty key and for one the broker's `Accepts` refuses.

### `Topics.SubscribeUnless`

```go
func (ts *Topics[Out]) SubscribeUnless(key string, s *Socket[Out], skip func(v Out) bool) error
```

SubscribeUnless adds `s` to the Topic of `key` with a function that leaves it out of the messages it returns true for, as [`Topic.SubscribeUnless`](#topicsubscribeunless) does, and returns the errors of `Subscribe`. A nil `skip` panics.

### `Topics.Unsubscribe`

```go
func (ts *Topics[Out]) Unsubscribe(key string, s *Socket[Out])
```

Unsubscribe removes `s` from the Topic of `key`; the Topic is dropped when `s` was its last Socket. A Socket that closes leaves by itself.

### `Topics.Publish`

```go
func (ts *Topics[Out]) Publish(key string, v Out) error
```

Publish sends `v` to the Sockets of `key`. On a Topics made with [`NewTopics`](#newtopics) it goes through the broker to every instance, this one's included, whether or not any has a Socket of `key`, and returns what the broker says, as [`Topic.Publish`](#topicpublish) does; on a zero Topics it reaches this process's Sockets of `key`. A key with no Socket anywhere is no error. It returns an error for an empty key, for one the broker does not carry, and the encoding's.

### `Topics.PublishLocal`

```go
func (ts *Topics[Out]) PublishLocal(key string, v Out) error
```

PublishLocal sends `v` to this instance's Sockets of `key` only, never through the broker, as [`Topic.PublishLocal`](#topicpublishlocal) does; a key with no Socket here is no error. It is what a [`TopicsOptions.OnGap`](#topicsoptions) answers with.

### `Topics.Len`

```go
func (ts *Topics[Out]) Len(key string) int
```

Len is the number of Sockets of `key` on this instance.

### `Topics.Keys`

```go
func (ts *Topics[Out]) Keys() int
```

Keys is the number of keys that have a Socket on this instance, each a Topic held in memory.

### `Topics.Resolve`

```go
func (ts *Topics[Out]) Resolve(fn func(key string, v Out) (Out, bool))
```

Resolve gives every key of a Topics made with [`NewTopics`](#newtopics) the function of [`Topic.Resolve`](#topicresolve), including keys made later, and `fn` is given the key the message was published to (a room's id, a conversation's), so a message need not repeat it to be resolved. Call it once, right after `NewTopics`; a second call, a zero Topics and a nil `fn` panic. A program whose broker is optional (an App built only to write its API document has none) uses a zero Topics and does not call `Resolve`.

### `SocketOwners`

```go
type SocketOwners struct {
	// Has unexported fields.
}
```

SocketOwners holds the open Sockets of each owner, the User a socket belongs to, so that the application can close every Socket of one owner at once, as when the User is signed out ([ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)). The application makes one; the zero value is ready, reaches this process only and must not be copied; a SocketOwners made with [`NewSocketOwners`](#newsocketowners) reaches every instance through a [`Broker`](#broker); it is safe for concurrent use. A handler records its Socket with `Add`, a Socket leaves when it ends, and an owner is forgotten with its last Socket. Keep one per meaning of an owner (Users, tenants): the ids would mix.

### `SocketOwners.Add`

```go
func (x *SocketOwners) Add[Out any](owner string, s *Socket[Out]) error
```

Add records `s` as a Socket of `owner`; one `SocketOwners` holds Sockets of any `Out`. Take the owner from the authenticated Input (the Provider's User), never from a message. It returns an error for an empty owner (a Socket under no owner could not be closed by owner, so return the error from the handler and the upgrade is refused), for a Socket that already has a different owner (a Socket has one), and `ErrSocketClosed` for a nil Socket. Adding it again under the same owner, or a Socket that has ended, does nothing. The owner is logged by `Close`, so it is not a secret.

### `SocketOwners.Close`

```go
func (x *SocketOwners) Close(owner string, status uint16, reason string) (int, error)
```

Close closes every open Socket of `owner` as `Socket.Close` does and returns how many it closed; a Socket already closing is not counted, and an owner with none is `(0, nil)`. Each Socket's `OnClose` sees `status` and `reason` with `Remote` false. A status a server may not send, or a reason over 123 bytes or not UTF-8, closes nothing and returns the error. When it closed any it logs one Info line, `bites: closed the sockets of an owner`, with the owner, the count, the status and the reason. A Socket added after `Close` copied the list is not closed: disable the User first, so a handshake that begins later is refused. Pick a status the client does not reconnect on (4401, not 1001 or 1012).

On a SocketOwners made with [`NewSocketOwners`](#newsocketowners), Close closes this instance's Sockets at once and then publishes the order `{owner, status, reason}` through the broker, so every other instance closes its own, and the returned count is this instance's alone. When the publish fails it returns the count and the error: this instance's Sockets are closed already, and the error says the other instances were not told. An instance that hears an order with a status or reason a server may not send drops it and logs a Warn.

```go
n, err := users.Close(userID, 4401, "signed out")
```

### `SocketOwners.Len`

```go
func (x *SocketOwners) Len(owner string) int
```

Len is the number of open Sockets of `owner`, those closing included until they end.

## Across instances

A Topic made with `NewTopic` listens to its broker while it has a socket, and stops when the last one leaves, so it needs no `Close`. Guide: [WebSockets across several instances](../guide/websockets.md#across-several-instances). Decision: [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md). The Postgres broker is the module [`pgnotify`](./pgnotify.md); [`bitetest.Broker`](./bitetest.md#bitetestbroker) is the in-memory one for tests.

### `NewTopic`

```go
func NewTopic[Out any](name string, broker Broker, o TopicOptions) *Topic[Out]
```

NewTopic returns a Topic that publishes through `broker` under `name` and delivers to its sockets what the broker receives from every instance. Every instance must use the same name and the same `Out` type, which is carried as the JSON a Socket sends, with snake_case names whatever the app's naming, so an `Opt` and a `Null` cross the broker as they are; a member this instance's `Out` does not have is read past. A Topic starts listening when its first socket subscribes and stops when its last one leaves; it is shared by pointer and never copied. It panics with a `RegistrationError` naming the line when `name` is empty, `broker` is nil, `MaxPayload` is negative, the broker does not accept the name (`Broker.Accepts`), or `Out` cannot be encoded (a channel or function field). Rows of the refusal table, ending `registered at file.go:N`:

| Mistake | Reason in the message |
|---|---|
| an empty name | `NewTopic: the name is empty: give the name every instance uses for this topic, such as "room.7"` |
| a nil broker | `NewTopic "room.1": the broker is nil: give the Broker this topic publishes through, or use a zero bites.Topic[Out] for a topic of this process only` |
| a negative `MaxPayload` | `NewTopic "room.1": MaxPayload is -1, which is not a limit: give a positive number of bytes, or zero for 1 MiB` |
| a name the broker cannot carry | `NewTopic "room.1": the broker does not carry this name: <the broker's reason>` |
| an `Out` JSON cannot encode | `NewTopic "room.1": the message type struct { C chan int } cannot be sent through a broker: struct { C chan int }.C: a field of kind chan cannot be encoded: use a type a Socket can send, without channel or function fields` |

### `NewTopics`

```go
func NewTopics[Out any](prefix string, broker Broker, o TopicsOptions) *Topics[Out]
```

NewTopics returns a [`Topics`](#topics) whose key `k` is the broker name `prefix+k` on every instance, so every instance must use the same prefix and `Out` type, carried as [`NewTopic`](#newtopic) carries it. The prefix must not begin another name the broker carries: give each family one of its own that ends in a separator, such as `"room."`. A key the broker does not accept is the error of the `Subscribe` or `Publish` that names it, not a panic, since a key comes from data. A program whose broker is optional (an App built only to write its API document has none) uses a zero `Topics` and calls neither `NewTopics` nor `Resolve`; a nil `broker` here in production is a mistake, and stays a panic. It panics with a `RegistrationError` naming the line when `prefix` is empty, `broker` is nil, `MaxPayload` is negative, or `Out` cannot be encoded. Rows of the refusal table, ending `registered at file.go:N`:

| Mistake | Reason in the message |
|---|---|
| an empty prefix | `NewTopics: the prefix is empty: give the start every instance puts before a key, such as "room.", so the names of this family are not those of another` |
| a nil broker | `NewTopics "room.": the broker is nil: give the Broker this family publishes through, or use a zero bites.Topics[Out] for a family of this process only` |
| a negative `MaxPayload` | `NewTopics "room.": MaxPayload is -1, which is not a limit: give a positive number of bytes, or zero for 1 MiB` |
| an `Out` JSON cannot encode | `NewTopics "room.": the message type struct { C chan int } cannot be sent through a broker: struct { C chan int }.C: a field of kind chan cannot be encoded: use a type a Socket can send, without channel or function fields` |

### `TopicsOptions`

```go
type TopicsOptions struct {
	OnGap      func(key string)
	MaxPayload int
	Log        *slog.Logger
}
```

`OnGap` is called on this instance, once for each key that has a Socket here, when the broker reports that messages may have been lost, as [`TopicOptions.OnGap`](#topicoptions) is for one Topic; usually it `PublishLocal`s to that key a message that makes its clients read their state again. It must not block. Nil means the gap is logged at Warn, at most once a second whatever the number of keys. `MaxPayload` and `Log` are those of `TopicOptions`.

### `NewSocketOwners`

```go
func NewSocketOwners(name string, broker Broker, o SocketOwnersOptions) *SocketOwners
```

NewSocketOwners returns a [`SocketOwners`](#socketowners) whose [`Close`](#socketownersclose) reaches every instance of the application through `broker` under `name`. Every instance must use the same name, and one name belongs to one SocketOwners. It listens to the broker while it holds a Socket and stops when the last one leaves, as a [`Topic`](#topic) does, so one nobody is in costs the broker nothing and needs no `Close`. The order carries the owner, the status and the reason. It panics with a `RegistrationError`, ending `registered at <file:line>`, for the mistakes below. The zero SocketOwners is still the one of one process. Decision: [ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md), [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md).

| Mistake | Reason in the message |
|---|---|
| an empty name | `NewSocketOwners: the name is empty: give the name every instance uses for these orders, such as "signout"` |
| a nil broker | `NewSocketOwners "signout": the broker is nil: give the Broker the orders travel through, or use a zero bites.SocketOwners for sockets of this process only` |
| a name the broker cannot carry | `NewSocketOwners "signout": the broker does not carry this name: <the broker's reason>` |

### `SocketOwnersOptions`

```go
type SocketOwnersOptions struct {
	OnGap func()
	Log   *slog.Logger
}
```

`OnGap` replaces what happens when the broker reports that orders may have been lost (for Postgres, the `LISTEN` connection dropped and came back). A missed sign-out cannot be told from none, so the default fails closed: every Socket this SocketOwners holds on this instance is closed with 1012 (service restart), each after a random share of a window of 1 ms a socket, at most the app's [`WithGoAwaySpread`](#withgoawayspread), so the clients reconnect, are authenticated again and do not all arrive at once. The default costs a reconnect of every Socket of the family on every gap, and a gap is every reconnect of the broker's connection. Give `OnGap` to log instead, or to check each User in your own database and call `Close` for those signed out; it replaces the default entirely, runs on the broker's goroutine, must not block, and a panic in it is recovered and logged at Error. Nil means the default. `Log` receives what happens in silence otherwise (an order that did not decode, a gap); nil means `slog.Default()`.

### `TopicOptions`

```go
type TopicOptions struct {
	OnGap      func()
	MaxPayload int
	Log        *slog.Logger
}
```

`OnGap` is called on this instance when the broker reports that messages may have been lost (for Postgres, the `LISTEN` connection dropped and came back), once the Topic is listening again. The framework tells the sockets nothing: the application does, usually with `PublishLocal` of a message that makes clients read their state again. It must not block; a panic in it is recovered and logged. Nil means the gap is logged at Warn naming the topic and this option. `MaxPayload` is the largest encoded message in bytes, checked on Publish (a [`*PayloadError`](#payloaderror), nothing sent) and on what arrives (dropped and logged); zero means 1 MiB, whatever the broker accepts: a broker with a lower limit says so in the `*PayloadError` of the Publish that goes over it. `Log` receives the drops and the gaps; nil means `slog.Default()`. A payload that does not decode as `Out` is dropped and logged at Warn with the topic and the size.

### `PayloadError`

```go
type PayloadError struct {
	Name  string
	Size  int
	Limit int
}

func (e *PayloadError) Error() string
```

PayloadError is the error of a Publish whose message is larger than the limit that applies to it: a Topic's or a Topics' `MaxPayload`, or the limit of the [`Broker`](#broker) it publishes through. It is returned as a `*PayloadError`, found with `errors.As`, so a program can tell a message that is too large, which it answers by publishing a reference and giving the Topic a [`Resolve`](#topicresolve), from a broker that is down, which it retries or reports. Nothing was sent. `Name` is the name published to (a Topics' prefix and key). `Size` is the encoded payload in bytes. `Limit` is the most bytes the payload of this `Name` may have: the smaller of `MaxPayload` and what the broker leaves the payload for this name (`pgnotify`'s 7999 bytes count the name and a newline, and its `Limit` has them taken off). A payload of `Limit` bytes is carried; one byte more is this error. The zero value is an error about 0 bytes over a limit of 0, for no name; only a Publish makes one. Decision: [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md).

### `PayloadError.Error`

```go
func (e *PayloadError) Error() string
```

Error says the topic's name, the size and the limit, and what to do: send less, or publish a reference and give the Topic a `Resolve`.

### `Broker`

```go
type Broker interface {
	Accepts(name string) error
	Publish(ctx context.Context, name string, payload []byte) error
	Subscribe(name string, deliver func(payload []byte), gap func()) (stop func())
}
```

Broker carries a Topic's messages between instances. It is implemented by modules and by tests, not called by an application: the server's own code hears a Topic with [`Topic.Listen`](#topiclisten). `Accepts` reports whether a name can be carried, so `NewTopic` refuses one that cannot. `Publish` sends `payload` to every instance subscribed to `name`, this one included, returns once the broker has accepted it, and never truncates: a payload over the broker's own limit is a [`*PayloadError`](#payloaderror) whose `Limit` is what is left to the payload for `name`, with what the broker counts besides it (the name, a separator) already taken off, so a program never computes it; any other failure is any other error. `Subscribe` starts delivering every payload published to `name`, in the order the broker received them, one at a time, and returns `stop`; `gap` is called when payloads may have been lost, from any goroutine, and must not block. `Subscribe` never fails (a broker that is down delivers again when it is back), and neither `Subscribe` nor `stop` blocks on the network. `deliver` may call `stop` synchronously, so a broker must not hold a lock `stop` needs while it delivers. Each payload given to `deliver` is the delivery's to keep: the broker gives every call bytes of its own and never changes them afterwards.

## Limits and origins

### `MaxMessage`

```go
func MaxMessage(n int) RouteOption
```

MaxMessage sets the most bytes of one message, reassembled, this WebSocket route (or every WebSocket route of this group) accepts; over it the socket is closed with 1009. It must be positive and not above the app's `Limits.ReassemblyBudget`. The default is `Limits.MaxMessage`, 64 KiB. It has no effect on a route that is not a WebSocket.

### `AllowOrigins`

```go
func AllowOrigins(origins ...string) RouteOption
```

AllowOrigins lets the listed origins open a WebSocket on this route, or on every WebSocket route of this group, besides the request's own host. A browser attaches cookies to a WebSocket handshake from any site and CORS does not apply to WebSockets, so every WebSocket route checks `Origin` before its Providers run, and a refused one is a 403 problem+json. By default the Origin's authority must equal the request's `Host`, or the `X-Forwarded-Host` a trusted proxy wrote when [`TrustedProxies.ForwardedHost`](./app.md#trustedproxies) is set (what [`Request.PublicHost`](./middleware.md#requestpublichost) reads), compared without case, dropping a port only when it is the default of the Origin's own scheme; the scheme of the connection is never inferred, because behind a TLS-terminating proxy the engine sees plain HTTP. Each origin listed is a full origin, `scheme://host[:port]`, compared exactly after lower-casing. A nearer scope's list replaces an enclosing one's.

Refused at Registration (after the label `WebSocket /x:`, or `group "/g":`):

| Mistake | Message |
|---|---|
| `"*"` | `AllowOrigins("*") is refused: an Origin check that lets every site in is AllowAnyOrigin(), which says so in the code; use it if that is what you mean` |
| `"null"` | `AllowOrigins("null") is refused: a sandboxed iframe or a file:// page sends Origin: null, so every such page would pass; list the sites that may connect` |
| no scheme | `AllowOrigins("app.example.com") has no scheme: an origin is scheme://host[:port], such as https://app.example.com` |
| a path, trailing slash, userinfo or space | `AllowOrigins("https://a.io/chat") has a path, a trailing slash, a userinfo or a space: an origin is scheme://host[:port] and nothing after it` |
| a wildcard | `AllowOrigins("https://*.example.com") has a wildcard, which is not supported: list each origin in full` |
| no origin | `AllowOrigins is given no origin: list the sites that may connect, or use AllowAnyOrigin() to say any may` |
| `AllowAnyOrigin` in the same scope | `AllowAnyOrigin() and AllowOrigins are both given in this scope: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one` |

Decision: [ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md).

### `AllowAnyOrigin`

```go
func AllowAnyOrigin() RouteOption
```

AllowAnyOrigin lets a page on any site open a WebSocket on this route, or on every WebSocket route of this group. It is its own name, not `AllowOrigins("*")`, so the choice shows in review and in a search: a browser attaches cookies to a WebSocket handshake from any site, so a route that authenticates by cookie is then open to cross-site hijacking. Given with `AllowOrigins` in one scope it is refused (above).

### `WithAllowOrigins`

```go
func WithAllowOrigins(origins ...string) Option
```

WithAllowOrigins is the app's list of the origins of its own pages: `AllowOrigins` for every WebSocket route of the app, and the origins of a `CSRF` given none ([security](./security.md#csrfoptions)), so a page that holds forms and sockets is named once. A group's or a route's own `AllowOrigins` replaces it for that scope and must be within it: a list outside the app's panics at Registration (`WebSocket /x: AllowOrigins("https://other.example") is not in the app's list (WithAllowOrigins at app.go:12): add it there, or give the app no list and list origins per route`). A bad entry panics in the call, naming `WithAllowOrigins` (`WithAllowOrigins("b.example") has no scheme: ...`, `WithAllowOrigins("ftp://files.example") is not a full origin: it must start with http:// or https://`), and so does no origin (`WithAllowOrigins is given no origin: list the sites that may connect, or use WithAllowAnyOrigin() to say any may`). It does not set CORS's origins, which name a different trust.

### `WithGoAwaySpread`

```go
func WithGoAwaySpread(d time.Duration) Option
```

WithGoAwaySpread sets the longest window over which `Shutdown` spreads the Close 1001 it sends every open WebSocket, so the clients do not all reconnect to the next instance in the same moment. The window is 1 ms for each open socket, at most `d`, and at most half of what is left of Shutdown's context, whichever is shortest; each socket is told after a random share of it. The default is 3 seconds, which is what an app that does not give the option gets. `d` is a ceiling and not a delay: fewer sockets still get 1 ms each. Because the context is known only at `Shutdown`, a `d` that half of it cannot honour is not refused; `Shutdown` logs `bites: shutdown: WebSocket Close 1001 spread` with `sockets`, `window` and `asked`, as a warning that says `cut short by half of what is left of Shutdown's context` when it was shortened. It has no effect on an app with no WebSocket route, and is not `WithShutdownDelay`. The start-up log lists `websocket shutdown close spread, at most` as `default` or `custom`. It panics in `New` for a zero or negative `d` (`WithGoAwaySpread: the window is -1s, which is not a window: give a positive duration such as 10*time.Second, or leave the option out for the default of 3s`) and when given twice (`WithGoAwaySpread: given twice (first at ..., again at ...): give one window`).

Guide: [deploying](../guide/deploying.md#spreading-the-websocket-closes-of-a-rolling-deploy). Decision: [ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md).

### `WithAllowAnyOrigin`

```go
func WithAllowAnyOrigin() Option
```

WithAllowAnyOrigin is `AllowAnyOrigin` for every WebSocket route of the app. Given together with `WithAllowOrigins` it panics in `New`: `WithAllowAnyOrigin() and WithAllowOrigins are both given: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one`.
