# WebSockets

**A WebSocket route is a typed route whose handler receives a `Socket` to send on and returns an `Inbox` of functions that the engine calls with each message; there is no read loop in your code.**

**Reference:** [WebSockets](../reference/websocket.md) · **Design:** [WebSockets](../design/websocket.md)

The Input, its [Providers](./providers.md), `Validate` and the middleware around the route run before the upgrade, so a refused handshake is an ordinary problem+json answer and a socket exists only for a request that passed ([ADR 0036](../adr/0036-websocket-routes-are-typed-like-http-routes.md)). After the 101 the engine owns the connection: it reads, reassembles, decodes and hands your function one message at a time ([ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md)). This page builds a small chat room.

<!-- compiles: decls -->
```go
type User struct{ Name string }

type Said struct {
	Text string
}

type Line struct {
	From string
	Text string
}

func userFromHeader(ctx context.Context, req bites.Request, _ struct{}) (User, error) {
	name := req.Header("X-User")
	if name == "" {
		return User{}, bites.Unauthorized("no user")
	}
	return User{Name: name}, nil
}

type Chat struct {
	room bites.Topic[Line]
}

type JoinChat struct {
	User User
}
```

## Receiving messages and replying

**Register the route with [`app.WebSocket`](../reference/websocket.md#appwebsocket); the handler returns an [`Inbox`](../reference/websocket.md#inbox) whose `OnMessage` receives each message already decoded.**

<!-- compiles: decls -->
```go
func (c *Chat) Join(ctx context.Context, in JoinChat, s *bites.Socket[Line]) (bites.Inbox[Said], error) {
	c.room.Subscribe(s)
	return bites.Inbox[Said]{
		OnMessage: func(ctx context.Context, m Said) error {
			return c.room.Publish(Line{From: in.User.Name, Text: m.Text})
		},
		OnMalformed: func(ctx context.Context, raw []byte, err error) error {
			return s.Send(Line{From: "server", Text: "that was not a message"})
		},
		OnClose: func(info bites.CloseInfo) {
			slog.Info("left", "user", in.User.Name, "status", info.Status, "by_client", info.Remote)
		},
	}, nil
}
```

<!-- compiles: body -->
```go
chat := &Chat{}
app.Provide(userFromHeader)
app.WebSocket("/chat", chat.Join)
```

A client sends the upgrade request, then frames. The first exchange is HTTP and the rest is WebSocket:

```http
GET /chat HTTP/1.1
Host: example.com
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==
Sec-WebSocket-Version: 13
X-User: ada

HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
```

```text
client -> {"text":"hello"}
server -> {"from":"ada","text":"hello"}
```

**The three type parameters are the Input, what the server sends and what it receives.** `Msg` (here `Said`) is decoded from the JSON of each text message and a message that does not decode is [`OnMalformed`](../reference/websocket.md#inbox)'s; `Out` (`Line`) is encoded as JSON for each send. When `Msg` or `Out` is `[]byte` the route takes and sends bytes, a binary frame in either direction, and a `Raw[T]` message keeps the exact bytes beside the decoded value. A mistake in those types is a panic at Registration ([ADR 0007](../adr/0007-mistakes-are-refused-at-registration.md)).

**`OnMessage` runs one message at a time, in order, on the connection's own goroutine, and may block.** A database call in it pauses reading from this socket alone, which is the inbound backpressure. An error it returns closes the socket with 1011, and so does a panic, which is recovered ([ADR 0028](../adr/0028-panics-are-recovered-by-the-engine.md)); the error text is not sent to the client. To close with a status of your own, return a [`*bites.CloseError`](../reference/websocket.md#closeerror), wrapped or not: `&bites.CloseError{Status: 4001, Reason: "token expired"}` sends 4001 and that reason. A status a server may not send closes with 1011 and is logged. The context is cancelled when the socket starts to close.

**`OnMalformed` decides what a bad message costs.** It receives the message's own bytes and a [`*MalformedError`](../reference/websocket.md#malformederror) (matched by `errors.Is(err, bites.ErrMalformedMessage)`) naming the fields in the message's wire names. Returning `nil` drops the message and keeps the socket; returning an error closes it with 1011, or with the status of a `CloseError`. Without an `OnMalformed`, a malformed message closes the socket with 1007 and a reason that names the field. A binary frame to a route whose message is not `[]byte` closes with 1003.

**`OnClose` is called once, after the last `OnMessage`.** [`CloseInfo`](../reference/websocket.md#closeinfo) has the `Status` and `Reason`, `Remote` (true when the client started the close) and `Err` (why a connection ended with no handshake, status 1006). A nil function in the `Inbox` is skipped, except `OnMessage` (below).

## A protocol of several kinds of frame

**Make each direction a tagged union: a struct of `Opt` variants with a `Discriminator` method.** A route has one client type and one server type, and a protocol has a dozen frames; a struct whose every field is an `Opt` of the struct of one frame, with `func (ClientFrame) Discriminator() string { return "type" }`, reads `{"type":"join","room":7}` into its `Join` and refuses a `type` it does not know, naming the ones it has. Each frame's fields are its own, so the API document and a generated client know that a `join` has a `room` and a `say` has a `text`:

<!-- compiles: decls -->
```go
type JoinFrame struct{ Room int64 }
type SayFrame struct{ Text string }

type ClientFrame struct {
	Join bites.Opt[JoinFrame]
	Say  bites.Opt[SayFrame]
}

func (ClientFrame) Discriminator() string { return "type" }

type SaidFrame struct{ Text, By string }
type ErrorFrame struct{ Message string }

type ServerFrame struct {
	Said  bites.Opt[SaidFrame]
	Error bites.Opt[ErrorFrame]
}

func (ServerFrame) Discriminator() string { return "type" }

func frames(s *bites.Socket[ServerFrame]) bites.Inbox[ClientFrame] {
	return bites.Inbox[ClientFrame]{OnMessage: func(_ context.Context, f ClientFrame) error {
		if m, ok := f.Say.Get(); ok {
			var out ServerFrame
			out.Said.Set(SaidFrame{Text: m.Text, By: "ada"})
			return s.Send(out)
		}
		return nil
	}}
}
```

**Write one constructor function for each variant of a server frame**, so a value is not spelled with its variant twice at every call: `func Activity(a ActivityFrame) ServerFrame { return ServerFrame{Activity: bites.Some(a)} }`, then `s.Send(Activity(a))` and `room.Publish(Activity(a))`. It is a pattern of the application's own code, not an API of bites.

**Name the variants only the server carries with `ServerOnly`.** A Topic that carries a reference ([Across several instances](#across-several-instances)) needs the reference to be a value of its `Out`, so with a union it is a variant, and a client would be told about a frame it never receives. `func (ServerFrame) ServerOnly() []string { return []string{"Ref"} }` names it by its Go field: the API document and a generated client leave it out, a client that sends its tag is refused, `Send` returns an error naming it and writes nothing, and a Topic does not send it to its Sockets (it logs a Warn naming the Topic and the variant, and `Listen` functions still get it). Naming a field that is not a variant, or every variant, is refused at Registration ([ServerOnly method](../reference/input.md#serveronly-method)).

A union with only one variant is refused, so a server type with one frame today is a plain struct; add the method when it gains a second. Sending a frame with no variant set, or two, is an error from `Send`, never `{}`. The rules and the refusals are under [Discriminator method](../reference/input.md#discriminator-method).

## A socket that only sends

**A route that only pushes to the client (a status feed, a notification stream) takes `struct{}` as its `Msg` and returns an `Inbox` with no `OnMessage`.** The socket is subscribed or kept for sending as any other is; a message the client sends anyway closes it with 1003 and the reason `this socket takes no messages`, so a client that expected to be heard learns it was not ([ADR 0035](../adr/0035-websockets-are-message-handlers-with-no-user-read-loop.md)).

<!-- compiles: decls -->
```go
type Watch struct {
	Path struct{ ID int64 }
}

type Status struct {
	Order int64
	State string
}

type Orders struct{ updates bites.Topic[Status] }

func (o *Orders) Watch(ctx context.Context, in Watch, s *bites.Socket[Status]) (bites.Inbox[struct{}], error) {
	o.updates.Subscribe(s)
	return bites.Inbox[struct{}]{}, nil
}
```

A client that may send something harmless (a keep-alive of its own) is given an `OnMessage` that ignores it: `OnMessage: func(context.Context, struct{}) error { return nil }`, with a `[]byte` `Msg` if what it sends is not JSON.

## Sending from anywhere

**A [`Socket`](../reference/websocket.md#socket) is a handle that is safe to keep and to use from any goroutine.** It is never reused for another connection, so a handle kept after its connection closed only returns [`ErrSocketClosed`](../reference/websocket.md#errsocketclosed) ([ADR 0047](../adr/0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md)).

- [`Send(v)`](../reference/websocket.md#socketsend) encodes `v` on the calling goroutine and queues the frame. It never blocks.
- [`Close(status, reason)`](../reference/websocket.md#socketclose) starts the close handshake. The status must be one a server may send (1000 to 1003, 1007 to 1014, 3000 to 4999) and the reason at most 123 bytes; the connection ends when the client answers or after five seconds.
- [`Context()`](../reference/websocket.md#socketcontext) is cancelled when the socket starts to close.

Server push from outside a request, such as a database `LISTEN` loop, is a goroutine of yours calling `Send` or `Publish`.

## Broadcasting to many sockets

**A [`Topic`](../reference/websocket.md#topic) is a set of sockets that one published message reaches.** The zero value is ready and must not be copied, so keep it in a struct you share by pointer, as `Chat` does above. [`Subscribe`](../reference/websocket.md#topicsubscribe) adds a socket, [`Unsubscribe`](../reference/websocket.md#topicunsubscribe) removes it, [`Len`](../reference/websocket.md#topiclen) counts, and [`Publish(v)`](../reference/websocket.md#topicpublish) sends `v` to every subscriber.

A publish encodes and frames the message once and queues the same frame on every subscriber ([ADR 0038](../adr/0038-a-broadcast-is-framed-once-and-shared.md)). It never blocks: a subscriber whose queue is full is closed and does not hold the others. A socket leaves every topic when it closes, so your own registry never leaks a closed socket. 

**A message for one user, or for the members of a private room only, goes on a Topic of their own, and a Topic made from data is a key of a [`Topics`](../reference/websocket.md#topics).** A publish is one frame for every subscriber, so it cannot carry something meant for one of them (the sender's own acknowledgement, a private room's activity that another member of the Space must not see): give each User a Topic (`user.42`) and each private room one, subscribe a socket to its User's Topic and to the rooms it may see, and publish to the Topic whose subscribers are exactly the readers. A socket of a non-member is then never in the Topic, which is the check, and a test proves it with [`Quiet`](../reference/bitetest.md#bitetestwsquiet) on that socket. Do not keep a map of Topics for this: that is a `Topics`, below.

Topics are cheap enough to count by the hundred per socket (measured in [`docs/research/topic-cost`](../research/topic-cost/README.md)): a zero Topic is 120 B, a `NewTopic` with no socket 320 B, the first socket of a brokered Topic about 310 B (its broker subscription included), and each further socket in a Topic 37 B, beside the 1695 B an idle socket costs.

## A family of Topics by key

**A name the code knows (a lobby, a cache every instance clears) is one `Topic`; a name made from data (a room per id, the frames of one User) is a key of a [`Topics`](../reference/websocket.md#topics).** Each key is a Topic made when its first socket subscribes and dropped when its last leaves, so the family holds only the keys that have a socket on this instance and you keep no map and no lock. The zero value is the family of one process:

<!-- compiles: decls -->
```go
type RoomFrame struct{ Text string }

type Rooms struct{ frames bites.Topics[RoomFrame] }

// in the socket's handler, once the user is known to be a member of room:
func (r *Rooms) join(room string, s *bites.Socket[RoomFrame]) error {
	return r.frames.Subscribe(room, s)
}

// anywhere, such as the handler of a POST:
func (r *Rooms) say(room, text string) error {
	return r.frames.Publish(room, RoomFrame{Text: text})
}
```

[`Subscribe`](../reference/websocket.md#topicssubscribe) returns an error for an empty key, so return it from the handler. Take the key from what the handler knows (the User from a Provider, a room id it checked against the database), never straight from a client's message: the family holds a key for as long as a socket is in it. [`Unsubscribe`](../reference/websocket.md#topicsunsubscribe), [`Len(key)`](../reference/websocket.md#topicslen), [`Keys()`](../reference/websocket.md#topicskeys) and [`PublishLocal`](../reference/websocket.md#topicspublishlocal) are the Topic's, by key.

**Across instances, make it with [`NewTopics`](../reference/websocket.md#newtopics), a prefix and a broker.** Key `7` is the broker name `room.7` on every instance, so every instance uses the same prefix, and the prefix must not begin another name the broker carries: give each family a prefix of its own that ends in a separator. `Publish(key, v)` goes straight to the broker whether or not this instance has a socket of that key, which a `Topic` cannot do, so the instance that handles a POST need not hold the room's sockets. `TopicsOptions.OnGap` is called once for each key that has a socket here; without it a gap is logged at Warn, one line a second at most. The design and what it costs are [ADR 0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md): a key with its one socket is 783 B and nothing once its last socket has left.

<!-- compiles: decls -->
```go
func newRooms(broker bites.Broker) *bites.Topics[RoomFrame] {
	var rooms *bites.Topics[RoomFrame]
	rooms = bites.NewTopics[RoomFrame]("room.", broker, bites.TopicsOptions{
		OnGap: func(key string) { rooms.PublishLocal(key, RoomFrame{Text: "resync"}) },
	})
	return rooms
}
```

## Leaving one socket out of a message

**A message that every socket of a Topic should get, except the ones whose user muted its sender, is one Topic with [`SubscribeUnless`](../reference/websocket.md#topicsubscribeunless).** Subscribe the socket with a function that returns true for the messages it must not get; the others still get them, and the message is still framed once. The function reads the user's current state, so a mute needs no new subscription:

<!-- compiles: decls -->
```go
type SpaceMsg struct {
	From int64
	Text string
}

type Mutes struct {
	mu    sync.RWMutex
	muted map[int64]bool
}

func (m *Mutes) has(from int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.muted[from]
}

func joinSpace(space *bites.Topic[SpaceMsg], s *bites.Socket[SpaceMsg], mutes *Mutes) {
	space.SubscribeUnless(s, func(v SpaceMsg) bool { return mutes.has(v.From) })
}
```

`Subscribe` on the same socket takes the function away, and `SubscribeUnless` again replaces it. The function runs on the goroutine that publishes (the broker's on a brokered Topic), outside the Topic's lock, so keep it quick and never block in it. A panic in it is logged at Error and leaves that socket out of that message: a filter that fails holds the message back, it does not let it through. This is for what a user chose not to see; a message a non-member must never receive belongs on a Topic the non-member is not in. A `Topics` has the same method per key, [`Topics.SubscribeUnless`](../reference/websocket.md#topicssubscribeunless). Decision: [ADR 0084](../adr/0084-a-name-made-from-data-is-a-key-of-a-topics-family.md).

## Across several instances

**A zero `Topic` reaches the sockets of one process. To reach the sockets of every instance, make the Topic with [`NewTopic`](../reference/websocket.md#newtopic) and a [`Broker`](../reference/websocket.md#broker).** The Postgres broker is the module [`pgnotify`](../reference/pgnotify.md), which uses the database the application already has:

```go
broker := pgnotify.New(pgnotify.Options{
	URL: dbURL, Pool: pool, // the pool Publish uses; nil opens one from URL
	OnState: func(listening bool) { app.SetReady(listening) },
})
go broker.Run(ctx)       // holds the one LISTEN connection, reopens it when it drops
defer broker.Close()     // before the pool it publishes on is closed

room := bites.NewTopic[Frame]("room.7", broker, bites.TopicOptions{
	OnGap: func() { room.PublishLocal(Frame{Kind: "resync"}) },
})
```

Four things hold, and the design is [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md):

- **`Publish` goes through the broker, and the broker delivers to every instance's sockets, the publisher's own included.** There is no second, local path, so a message is never delivered twice to the sockets of the instance that published it, and every instance hears the same order. A `Publish` that fails delivers nothing and returns the error: post to the database first, then publish, and treat a failed publish as a log line, because the message is stored.
- **A message too big is an error, never cut.** Postgres limits a notification to 7999 bytes, the topic name and a newline included; `Publish` over it returns a [`*bites.PayloadError`](../reference/websocket.md#payloaderror), found with `errors.As`, whose `Limit` is what is left to the payload of that name (the name and the newline already taken off), so you never compute it, and a failed broker is a different error. Keep the message small, or publish a reference and give the Topic a [`Resolve`](../reference/websocket.md#topicresolve) that reads the full message on each instance before its sockets get it: `room.Resolve(func(f Frame) (Frame, bool) { if f.Ref == "" { return f, true }; full, err := store.Message(ctx, f.Ref); return full, err == nil })`, with a `ctx` that has a timeout, since it runs on the delivery goroutine. `TopicOptions.MaxPayload` (1 MiB) bounds every broker; the broker's own lower limit is the `*PayloadError` of the `Publish` that goes over it. A family's resolver is given the key: `rooms.Resolve(func(key string, f Frame) (Frame, bool) { ... })`, so a reference need not repeat its room.

  ```go
  var pe *bites.PayloadError
  if err := room.Publish(frame); errors.As(err, &pe) {
	err = room.Publish(Frame{Ref: frame.ID}) // too big for the broker: send the reference
  }
  ```
- **Notifications can be missed, and the Topic says so.** Postgres keeps none for a listener that is not connected. When the connection comes back, `OnGap` runs on each Topic with sockets. The framework does not know your message type, so you decide what the clients are told: `PublishLocal` a message that makes them read the history again. Without `OnGap` the gap is only logged at Warn.
- **A Topic listens while it has a socket.** Make one when the first socket joins and drop it when empty, which a [`Topics`](../reference/websocket.md#topics) does for you; a Topic nobody is in costs the broker nothing. Report `/readyz` as failing while `Listening()` is false (`OnState`), so a balancer sends new sockets to an instance that hears.

**When a Topic's message is a [tagged union](#a-protocol-of-several-kinds-of-frame), make the reference a variant that only servers carry**, so no client is told of it: it crosses the broker as any variant, `Resolve` turns it into the one the sockets get, and one that is still there is not sent (a Warn names the Topic and the variant):

<!-- compiles: decls -->
```go
type NoteRef struct{ ID string }
type Note struct{ ID, Text string }

type RelayedFrame struct {
	Note bites.Opt[Note]
	Ref  bites.Opt[NoteRef] // published when the message is too big for the broker
}

func (RelayedFrame) Discriminator() string { return "type" }
func (RelayedFrame) ServerOnly() []string  { return []string{"Ref"} }

func resolving(room *bites.Topic[RelayedFrame], load func(id string) (Note, error)) {
	room.Resolve(func(f RelayedFrame) (RelayedFrame, bool) {
		ref, ok := f.Ref.Get()
		if !ok {
			return f, true
		}
		full, err := load(ref.ID)
		return RelayedFrame{Note: bites.Some(full)}, err == nil
	}) // the reference never reaches the API document or a socket
}
```

**The server's own code hears a Topic with [`Listen`](../reference/websocket.md#topiclisten).** A cache that must drop an entry on every instance at once (a revoked session, a suspended tenant) is a Topic of functions only: each instance listens, the instance that changes the entry publishes its key, and `OnGap` clears the whole cache, since an eviction may have been missed. The Topic listens to its broker while it has a function, with no socket:

<!-- compiles: decls -->
```go
type Evict struct{ Key string }

func evictions(broker bites.Broker, cache *sync.Map) (*bites.Topic[Evict], func()) {
	t := bites.NewTopic[Evict]("cache.sessions", broker, bites.TopicOptions{OnGap: func() { cache.Clear() }})
	stop := t.Listen(func(e Evict) { cache.Delete(e.Key) })
	return t, stop // on a revocation, on any instance: t.Publish(Evict{Key: id})
}
```

The function runs on the broker's delivery goroutine, one message after another, so it must not block: one that has slow work starts its own goroutine. A panic in it is logged and the next message still arrives.

A test uses [`bitetest.NewBroker`](../reference/bitetest.md#bitetestbroker), with `Peer()` for a second instance, `Gap()` and `Fail`. The whole program is `examples/chat`.

## Choosing which sites may open a socket

**Every WebSocket route checks `Origin` before any Provider runs, and an origin of another site is a 403.** A browser attaches cookies to a handshake from any site and CORS does not apply to WebSockets, so without the check a page on another site could open a socket that a cookie-reading Provider authenticates as the victim ([ADR 0046](../adr/0046-websocket-origins-and-inbound-limits.md)).

By default the origin's host and port must equal the request's `Host`, or, behind a proxy that rewrites the host, the `X-Forwarded-Host` it wrote when you set [`ForwardedHost`](./deploying.md#behind-a-load-balancer). A handshake with no `Origin` header is accepted, because browsers always send one, so its absence means a client that is not a browser. Other origins are listed:

<!-- compiles: body -->
```go
// for every WebSocket route of the app
app = bites.New(bites.WithAllowOrigins("https://app.example.com"))

// for one route, or a group; the nearest list replaces the enclosing one
app.WebSocket("/chat", (&Chat{}).Join, bites.AllowOrigins("https://app.example.com", "http://localhost:5173"))
```

[`WithAllowOrigins`](../reference/websocket.md#withalloworigins) sets the app's list, the origins of your own pages: [`CSRF`](../reference/security.md#csrfoptions) with no `Origins` takes the same list, so a page with forms and sockets is named once ([security guide](./security.md#refusing-cross-site-forgeries)). [`AllowOrigins`](../reference/websocket.md#alloworigins) sets a route's or a group's and narrows the app's: when the app has a list, an origin outside it panics at Registration (`WebSocket /chat: AllowOrigins("https://other.example") is not in the app's list ...`); to give one route an origin of its own, add it to the app's list, or give the app no list and list origins per route. Each entry is a full origin, `scheme://host[:port]`, compared exactly after lower-casing. [`AllowAnyOrigin`](../reference/websocket.md#allowanyorigin) and [`WithAllowAnyOrigin`](../reference/websocket.md#withallowanyorigin) let any site in; they are separate names so the choice shows in review, and a route that authenticates by cookie is then open to cross-site hijacking. `AllowOrigins("*")`, `"null"`, an entry with no scheme, a path or a wildcard, and `AllowAnyOrigin` beside a list are refused at Registration.

```http
GET /chat HTTP/1.1
Host: api.example.com
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==
Sec-WebSocket-Version: 13
Origin: https://evil.example

HTTP/1.1 403 Forbidden
Content-Type: application/problem+json
```

The other handshake answers are 405 with `Allow: GET` for another method, 400 for a malformed handshake, and 426 with `Sec-WebSocket-Version: 13` for another version. Each has a `detail` that names the route as a WebSocket route.

## Limits, slow readers and heartbeats

**Everything a client sends and everything queued for it is bounded, and a socket that passes a bound is closed with the status RFC 6455 gives.** The defaults are on the [limits page](./limits.md#websocket-limits).

- [`MaxMessage(n)`](../reference/websocket.md#maxmessage) sets the most bytes of one reassembled message on a route or a group (64 KiB by default); over it the socket is closed with 1009. It cannot exceed the app-wide reassembly budget, which is refused at Registration.
- A message not finished within the message timeout closes with 1008, and the memory all sockets hold for messages being assembled is one app-wide budget; a socket that would pass it closes with 1013.
- **A socket's send queue is bounded, 1 MiB by default.** `Send` and `Publish` never block; a message that would overflow the queue makes `Send` return [`ErrSendQueueFull`](../reference/websocket.md#errsendqueuefull) and closes the socket with 1008. The bytes queued on all sockets together are bounded too (64 MiB), and a socket that would pass that closes with 1013 ([ADR 0037](../adr/0037-a-slow-reader-is-closed-not-buffered-forever.md)). A reader that falls behind loses its connection and does not grow the server's memory.
- **The server pings every socket every 30 seconds and closes it at the second ping in a row that goes unanswered**, with 1008. Any pong, solicited or not, counts. All sockets share one timer wheel, so an idle socket holds no timer of its own ([ADR 0039](../adr/0039-heartbeats-run-on-one-timer-wheel.md)). A burst of more than 32 pings or pongs closes with 1008.

All of these are fields of [`Limits`](../reference/app.md#limits) given to [`WithLimits`](../reference/app.md#withlimits).

## Closing, and the 1001 at Shutdown

**Close a socket with `s.Close(status, reason)`, or by returning an error from `OnMessage`.** The server sends the close frame, waits for the client's answer, and ends the connection after five seconds if none comes; `OnClose` sees the server's status with `Remote` false.

**[`Shutdown`](../reference/app.md#appshutdown) sends every open WebSocket a close with status 1001 (going away).** Each is sent after a short random delay, 1 ms a socket and at most 3 seconds, so thousands of clients do not reconnect in the same millisecond; [`WithGoAwaySpread`](../reference/websocket.md#withgoawayspread) raises the 3 seconds for an app with more sockets than the other instances can take at once ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md), [deploying](./deploying.md#spreading-the-websocket-closes-of-a-rolling-deploy)). `OnClose` sees 1001 whether the client answers or not. Write the client so that 1001 and 1006 mean "reconnect with a back-off".

## Closing every socket of one user

**A [`SocketOwners`](../reference/websocket.md#socketowners) closes all the sockets of one user at once**, as when the user is signed out. Make one value in your service, record each socket under the user's id when it opens, and close by id. It replaces a map of your own from users to sockets, and forgets a socket when it ends, so it never holds a closed one.

<!-- compiles: decls -->
```go
type Account struct{ ID int64 }

type Event struct{ Kind string }

// Login is the session type; its owner is the account's id (see security).
type Login struct{ UserID int64 }

func (l Login) SessionOwner() string { return strconv.FormatInt(l.UserID, 10) }

type Feed struct {
	sockets  *bites.SocketOwners // &bites.SocketOwners{} for one process, NewSocketOwners for several
	sessions *bites.Sessions[Login]
}

func (f *Feed) Join(ctx context.Context, in struct{ Me Account }, s *bites.Socket[Event]) (bites.Inbox[struct{}], error) {
	if err := f.sockets.Add(strconv.FormatInt(in.Me.ID, 10), s); err != nil {
		return bites.Inbox[struct{}]{}, err
	}
	return bites.Inbox[struct{}]{}, nil
}

// Disable signs a user out everywhere: sessions first, so no new request is let in, then sockets.
func (f *Feed) Disable(ctx context.Context, in struct{ Path struct{ ID int64 } }) (bites.NoContent, error) {
	owner := strconv.FormatInt(in.Path.ID, 10)
	if _, err := f.sessions.RevokeOwner(ctx, owner); err != nil {
		return bites.NoContent{}, err
	}
	_, err := f.sockets.Close(owner, 4401, "signed out")
	return bites.NoContent{}, err
}
```

- **`Add` returns an error for an empty owner**; return it from the handler, so a socket that could not be closed later is never opened. Take the owner from the Provider's `User`, never from a message.
- **`Close` returns how many sockets it closed** and logs one line with the owner, the count and the status. Each socket's `OnClose` sees the status and reason you gave. Use a status your client does not reconnect on (4401, not 1001).
- **A socket that opens while you revoke** can be missed, for the length of one handshake. Disable the user in your database before you revoke, so a handshake that begins afterwards is refused by your `User` Provider.
- **The zero value reaches this process's sockets.** With several instances, make it with `NewSocketOwners`, as below.

**With several instances, make it with [`NewSocketOwners`](../reference/websocket.md#newsocketowners), a name and a [`Broker`](../reference/websocket.md#broker).** Every instance uses the same name, and `Close` on any of them closes the owner's sockets on all of them: it closes this instance's at once and publishes the order, and every instance closes its own (the one that published finds nothing left). Nothing else changes: `Add` and `Len` are the same, and no database connection of your own is needed.

<!-- compiles: decls -->
```go
func NewFeed(b bites.Broker) *Feed {
	return &Feed{sockets: bites.NewSocketOwners("signout", b, bites.SocketOwnersOptions{})}
}
```

- **The count `Close` returns is this instance's.** The other instances answer nobody.
- **A failed publish closes this instance's sockets and returns the count and the error.** The error says the other instances were not told: retry the `Close`, which is safe to repeat, or report it.
- **It listens while it holds a socket**, as a `Topic` does, so an instance with no socket of the family holds no broker subscription and needs no `Close`.
- **A gap closes every socket of the family by default.** A broker that reports it may have lost orders (for Postgres, the `LISTEN` connection dropped and came back) cannot say whether a sign-out was among them, so each socket this instance holds is closed with 1012 (service restart), spread over a window of 1 ms a socket, at most the app's [`WithGoAwaySpread`](../reference/websocket.md#withgoawayspread). The client reconnects, your Provider authenticates it again and a disabled user is refused. It costs a reconnect of every socket of the family on every reconnect of the broker's connection. `SocketOwnersOptions.OnGap` replaces it, for an application that would rather log, or check its users in its own database and `Close` the ones signed out; then a missed sign-out is yours to catch.
- **A server that hears an order for the sockets of other code**, such as a cache that drops an entry, uses a brokered `Topic` and [`Topic.Listen`](../reference/websocket.md#topiclisten) ([across several instances](#across-several-instances)), not `SocketOwners`.

## Providers and middleware on a socket route

**A Provider or a middleware refuses a handshake exactly as it refuses a request.** The order is: the origin check, then the middleware around the route, the Providers, Input decoding and `Validate`, the handler, and then the 101 ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)). A Provider that returns `bites.Unauthorized` answers 401 with no upgrade, and a middleware can add a header to the 101. An error returned by the handler before it returns the `Inbox` is the answer in place of the upgrade, and the `Socket` it was given is dead. A message sent through the socket before the handler returned goes out after the 101.

A Provider runs once at the handshake, not for each message; a message handler reads what the Provider gave from the closure, as `in.User` is read above. Browsers cannot set headers on a WebSocket, so a browser-facing route authenticates with a cookie or a query ticket, which is why the origin check comes first.

## A browser and its credential

**A browser's `new WebSocket(url)` and `new EventSource(url)` send no header the page chose, so a route they open takes its credential from a cookie or from a ticket in the query.** A cookie needs nothing more: the session user of [security](./security.md#keeping-a-session) is given to `CustomAuth` and works here as on any route. A token kept by the page's script is exchanged first for a short-lived ticket, which the page puts in the URL, and a Provider reads it from a `Query` section of its dependencies. A Provider may read a `Query` section only on a WebSocket route or a route that answers `SSE` or `Events`; it is decoded and checked as the Input's query is, and a strict query (`RejectUnknownQueryKeys`) lets its keys through:

<!-- compiles: decls -->
```go
type Viewer struct{ Name string }

func viewerFromTicket(ctx context.Context, req bites.Request, deps struct {
	Query struct{ Ticket bites.Opt[string] }
}) (Viewer, error) {
	t, ok := deps.Query.Ticket.Get()
	if !ok || t == "" {
		return Viewer{}, bites.Unauthorized("open the socket with ?ticket=, from POST /tickets")
	}
	return Viewer{Name: t}, nil // look the ticket up, and forget it: it is good once
}
```

<!-- compiles: body -->
```go
app.Provide(bites.CustomAuth(bites.SecurityScheme{Type: "apiKey", In: "query", Name: "ticket"}, viewerFromTicket))
```

Make the ticket good once and for a minute: a URL is kept by proxies and, for a page, by the browser's history, though bites' access log writes the path without the query. `CustomAuth` with `In: "query"` puts the scheme in the API document for an event stream route; WebSocket routes are not in it.

The [OpenAPI document](./openapi.md#what-is-left-out) states a WebSocket route as the `get` of its handshake, with its parameters, its credential (the ticket above as an API key in the query), its refusals and its messages under `x-websocket`; a [test](./testing.md#testing-a-websocket) speaks the frames over the in-memory connections.
