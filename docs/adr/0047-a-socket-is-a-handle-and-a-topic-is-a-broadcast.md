# A socket is a typed handle that outlives nothing, and a Topic is how one message reaches many

A WebSocket route's handler is `Join(ctx, in JoinRoom, s *Socket[Out]) (Inbox[In], error)`: it receives the typed Input ([ADR 0036](0036-websocket-routes-are-typed-like-http-routes.md)) and a handle for sending, and returns the Inbox whose handlers receive messages.

**The Socket handle.**

- `s.Send(v Out) error` encodes `v` on the calling goroutine and queues the frame; it never blocks, and returns an error once the socket is closed or its queue is full ([ADR 0037](0037-a-slow-reader-is-closed-not-buffered-forever.md)).
- `s.Close(status, reason)`, and `s.Context()`, a context cancelled when the socket closes.
- It is safe to use from any goroutine, and safe to keep: **a Socket is never reused for another connection**, so a handle kept after its socket closed only ever returns errors. This is [ADR 0008](0008-no-pooled-object-reaches-the-user.md) extended to connections, and it stays true now that idle sockets park ([ADR 0005](0005-our-own-http1-engine-with-a-goroutine-per-connection.md)): a parked socket is the same WS, woken, never a new one.

**The Inbox.** `OnMessage(ctx, msg In) error` runs for one socket's messages one at a time, in order; it may block (a database call), which pauses reading from that socket alone and is the inbound backpressure. Returning an error closes the socket with 1011. `OnClose(info)` runs once, after the last `OnMessage`. A panic in either is recovered ([ADR 0028](0028-panics-are-recovered-by-the-engine.md)) and closes the socket with 1011.

**Topics.** `Topic[Out]` holds a set of sockets: `Subscribe(s)`, `Unsubscribe(s)`, `Publish(v)`. A publish encodes and frames `v` once and queues the shared frame on every subscriber ([ADR 0038](0038-a-broadcast-is-framed-once-and-shared.md)); a socket leaves every topic when it closes, including one whose handler subscribed it and then refused the handshake with an error (it is never upgraded, and what it queued goes back to the send budget), so no registry of the application's own leaks closed sockets. Server push from outside a request is a goroutine of the application's calling `Publish` or `Send`. A zero `Topic` reaches the sockets of one process. A Topic made with `bites.NewTopic(name, broker, opts)` reaches the sockets of every instance of the application through a `Broker`, and `PublishLocal` reaches this process's sockets only ([ADR 0081](0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)). _Revised 2026-11-04: `SubscribeUnless(s, skip)` adds a Socket that is left out of each message `skip` returns true for, the mutes, blocks or language of one User on a Topic they share ([ADR 0084](0084-a-name-made-from-data-is-a-key-of-a-topics-family.md)): `skip` reads the User's current state, runs outside the Topic's lock on the publishing goroutine, fails closed on a panic (the Socket is left out and the panic logged at Error), is replaced by calling it again and taken away by `Subscribe`, and costs a Topic with none nothing more per `Publish`. Rejected: `PublishExcept(v, keep)`, where the publisher must know each Socket's User and on another instance holds no Socket, and one filter in `TopicOptions`, which would look each Socket's User up on every message. A name made from data is a key of a `Topics`, not a Topic per name made by hand (ADR 0084)._

_Revised 2026-11-04: fan-out across processes was first the application's own, joined by hand with Postgres `LISTEN`; every application that did so met the same bugs (the payload limit, a connection that drops, notifications missed in silence), and the framework now owns them. That position moved under the evidence of `examples/chat`'s hand-written listener, which is gone._

**What was rejected (the name):** the value was first called Session, and `Handlers[In]` was considered. Both lost to Inbox, because `Handlers` collides with [ADR 0014](0014-services-are-plain-go-values.md)'s `Handlers` struct of route methods (`type Handlers struct{ db }` is a common Go habit, and a route function returning `Handlers[Msg]` reads as handlers returning handlers) and with `http.Handler`, and `Session` is what every web developer expects for a login session, which milestone 3 builds. Evidence: [`docs/research/decisions-middleware/`](../research/decisions-middleware/report.md), section "Session rename". Receiver was rejected because it collides with Go's method receiver.

**What an `OnMessage` costs.** One allocation, the message's slab, and one more for each list the message fills ([ADR 0003](0003-the-trade-budget.md), [ADR 0046](0046-websocket-origins-and-inbound-limits.md)); not zero, because the message is copied out of the read buffer before it is decoded.

**The public API, as built** (the names the earlier text left to the build):

```go
func (a *App) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption)
func (g *Group) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption)

type Inbox[Msg any] struct {
    OnMessage   func(ctx context.Context, msg Msg) error
    OnMalformed func(ctx context.Context, raw []byte, err error) error
    OnClose     func(info CloseInfo)
}

func (s *Socket[Out]) Send(v Out) error
func (s *Socket[Out]) Close(status uint16, reason string) error
func (s *Socket[Out]) Context() context.Context

func (t *Topic[Out]) Subscribe(s *Socket[Out])
func (t *Topic[Out]) SubscribeUnless(s *Socket[Out], skip func(v Out) bool) // ADR 0084
func (t *Topic[Out]) Unsubscribe(s *Socket[Out])
func (t *Topic[Out]) Len() int
func (t *Topic[Out]) Publish(v Out) error
func (t *Topic[Out]) PublishLocal(v Out) error
func NewTopic[Out any](name string, broker Broker, o TopicOptions) *Topic[Out] // ADR 0081
```

`CloseInfo` has the status, the reason, whether the client started the close and the error of an abnormal end; a connection that ended with no close handshake reports 1006. `Send` returns `ErrSocketClosed` once the socket is closing and `ErrSendQueueFull` when the queue or the app's budget closed it. `Close` refuses a status a server may not send (1004 to 1006, 1015, anything outside 1000 to 1014 and 3000 to 4999) and a reason over 123 bytes, returning an error and leaving the socket open. The `Topic` is a value that needs no constructor; its zero value is empty and ready, and a socket leaves it when the socket ends. A handler error before the Inbox is returned answers the upgrade request with the error's status, and the Socket the handler was given is dead (`Send` returns `ErrSocketClosed`); a message sent from the handler before it returns goes out after the 101.

**Decisions the ADRs did not fix, made in the build:**

- **`Topic` has `Len`**, so a test or a metric can read the number of subscribers; it was not in the text and costs one lock.
- **`Subscribe` of a socket that has ended does nothing**, and a socket that ends leaves every Topic it joined (through a list the socket keeps, set only when it subscribes, so an unsubscribed socket pays nothing).
- **`OnMalformed` is in the Inbox**, not a route option, because it needs the socket's state like the other two handlers do ([ADR 0036](0036-websocket-routes-are-typed-like-http-routes.md)).
- **`OnClose` runs on the connection's goroutine after the last `OnMessage`**, never concurrently with it, and a panic in it is recovered and logged.

**An error chooses its close status with `CloseError`** (built 2026-11-01, milestone 3 wave 0). As first built, any error from `OnMessage` or `OnMalformed` closed with 1011, because the engine's error type that carries a status was not exported. `*bites.CloseError{Status, Reason}`, found anywhere in the error's chain with `errors.As`, now closes with its own status and reason, so an application closes with 4001 when a token expires mid-session or 1008 for a policy breach without holding the `Socket` in its closure. The status and reason come from user code and are checked when the error arrives (a status a server may send, at most 123 bytes of UTF-8); one that fails closes with 1011 and is logged at Error, because nothing about a returned error can be checked at Registration. It costs nothing on a message that returns nil: one nil comparison; the `errors.As` runs only on an error. Exporting the engine's `WSError` as it is was rejected: its field `Code` would sit beside `CloseInfo.Status` for the same number, and its unchecked path is the engine's own, for the 1007 it writes (`TestACloseErrorFromOnMessageClosesTheSocketWithItsStatusAndReasonEvenWrapped`, `TestACloseErrorWithAStatusAServerMayNotSendClosesWith1011AndIsLogged`).

**Closing every Socket of one owner** (decided 2026-11-04). When a User is signed out ([ADR 0056](0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md)), the sockets that User opened must close too, and an application kept its own map from a User to its sockets, with the leaks of a map the application must remember to clean. `SocketOwners` is that map, kept by the framework: a value the application makes (its zero value is ready, as a `Topic`'s is, and it must not be copied), in which a handler records a Socket under an owner when it opens, and from which the application closes every Socket of an owner.

```go
func (x *SocketOwners) Add[Out any](owner string, s *Socket[Out]) error
func (x *SocketOwners) Close(owner string, status uint16, reason string) (int, error)
func (x *SocketOwners) Len(owner string) int
```

- **`Add(owner, s)`** records `s` under `owner`. It returns an error for an empty owner (a socket under no owner could never be closed by owner, and the one thing a revocation must not do is skip a socket in silence, so the handler fails and the upgrade is refused, with the message `bites: SocketOwners.Add was given an empty owner: ...`), for a Socket that already has a different owner (a socket has one owner; the same owner twice is not an error), and `ErrSocketClosed` for a nil Socket. Adding a Socket that has already ended does nothing, as `Topic.Subscribe` does.
- **`Close(owner, status, reason)`** closes every open Socket of `owner` as `Socket.Close` does and returns how many it closed, so a Socket already closing is not counted. The status and reason are checked first, once, by the rules of `Socket.Close`; one that fails closes nothing and returns `(0, err)`. Each closed Socket's `OnClose` sees `info.Status` and `info.Reason` as given, `Remote` false, and its `Context()` is cancelled at once. When it closed any, it logs one Info line through the app's logger: `bites: closed the sockets of an owner`, with the owner, the count, the status and the reason (so the owner is logged: do not use a secret as an owner). An owner with nothing returns `(0, nil)` and logs nothing.
- **`Len(owner)`** is how many Sockets the owner has now, for a test or a metric.

**Across instances** (decided 2026-11-05). Sockets of the owner on other instances were first left to the application, with a recipe of a brokered `Topic` and `Topic.Listen` in the guide; every application wrote it again and got its gap wrong (see _what was rejected_ below). `NewSocketOwners(name, broker, SocketOwnersOptions{OnGap, Log})` makes a `SocketOwners` that is brokered, the way `NewTopic` makes a Topic ([ADR 0081](0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)); the zero value stays the one-process value and is unchanged. On a brokered one:

- **`Close(owner, status, reason)`** validates as above, closes this instance's Sockets of the owner at once, and publishes the order `{owner, status, reason}` under `name`. Every instance, this one included, closes its own Sockets of the owner when the order arrives; on the publisher the echo finds nothing left to close (a Socket still in its close handshake is not counted twice, nor logged twice). The returned count is this instance's alone. A failed publish returns the count and the error, and the error says the other instances were not told; the local Sockets are closed already, and a repeated `Close` is safe.
- **It listens while it holds a Socket**, not for the life of the application, the rule a Topic follows (ADR 0081): an instance with no Socket of the family holds no broker subscription (for Postgres, none of the shared connection's work), and an order only matters to an instance that has a Socket to close. The window this leaves is the one already stated for a Socket added while `Close` runs: a Socket added after an order was published is not closed by it, and the User is disabled first so its handshake is refused. Listening for the application's life would cost a subscription per `SocketOwners` per instance for nothing and need a `Close`; it was rejected.
- **A gap fails closed.** A broker that reports it may have lost orders cannot say whether a sign-out was among them, and a missed sign-out cannot be told from none. The default closes every Socket the `SocketOwners` holds on this instance with 1012 (service restart), each after a random share of a window of 1 ms a socket, at most the app's `WithGoAwaySpread` (the window Shutdown spreads its 1001 over, [ADR 0029](0029-shutdown-drains-and-then-closes.md)), so the clients reconnect, are authenticated again by the Provider and are Added again, and do not all arrive in one moment. It logs one Warn with the count and the window. `SocketOwnersOptions.OnGap` replaces it entirely, for an application that would rather log or check its Users itself. The closes use a timer each (one allocation a socket, on a gap only), not the engine's wheel, because the wheel's node is the socket's go-away and heartbeat state.
- **Refusals** (rows of `refusal_test.go`): an empty name, a nil broker (the message points to the zero value for one process), and a name the broker does not accept. There is no `MaxPayload`: an order is the owner, a status and a reason of at most 123 bytes, so the broker's own limit, a `*PayloadError` on `Close`, is the only one that applies.
- **The payload** is written by the framework's codec with the options of a Topic's, `{"owner":"","status":0,"reason":""}`, unknown members read past, so an instance of a later release that adds a member does not make this one drop its orders. One that does not decode, or whose status or reason a server may not send, is dropped and logged at Warn, never closing anything.

**How it holds.** The index is the `SocketOwners` value's own, never package state ([ADR 0026](0026-no-global-state-and-one-behaviour.md)), so two of them (one for Users, one for tenants) cannot collide on a key. A Socket joins it through the same list a Topic uses (`WS.Join`), set only when `Add` is called, so a Socket that is never added pays nothing; when the Socket ends, or is aborted because the handler failed after adding it, it leaves the index and the owner's entry is deleted with its last Socket. Memory is therefore bounded by the open Sockets, which the connection limits bound ([ADR 0005](0005-our-own-http1-engine-with-a-goroutine-per-connection.md)): a client cannot grow it by opening and closing, and a Socket closing (the five seconds of the close handshake included) leaves at its end. `Close` copies the owner's Sockets under the lock and closes them after releasing it, so a Socket's `OnClose` that calls `Add` or `Len` cannot deadlock. It is safe for concurrent use.

**One composition, from the revoke of a User** (the example the guide gives): the application records the owner on login, in the session type and in the socket's handler, and one handler ends everything.

```go
type Login struct{ UserID string }
func (l Login) SessionOwner() string { return l.UserID }

func (h *Handlers) Join(ctx context.Context, in struct{ Me User }, s *bites.Socket[Event]) (bites.Inbox[Cmd], error) {
	if err := h.sockets.Add(in.Me.ID, s); err != nil {
		return bites.Inbox[Cmd]{}, err
	}
	return bites.Inbox[Cmd]{ /* ... */ }, nil
}

func (h *Handlers) Disable(ctx context.Context, in struct{ ID string `path:"id"` }) (bites.NoContent, error) {
	if err := h.users.Disable(ctx, in.ID); err != nil { // the authority first
		return bites.NoContent{}, err
	}
	if _, err := h.sessions.RevokeOwner(ctx, in.ID); err != nil { // no new request is let in
		return bites.NoContent{}, err
	}
	_, err := h.sockets.Close(in.ID, 4401, "signed out") // and the open sockets go
	return bites.NoContent{}, err
}
```

**Scenarios it was put against.**

- _A User with three tabs_ (three Sockets, two routes with different `Out` types): `Add` is generic so one `SocketOwners` holds Sockets of any `Out`; `Close` closes the three and returns 3 (`TestClosingAnOwnerClosesEverySocketOfThatOwnerWhateverItsRouteAndNoOthers`).
- _The hostile and racing case_, a socket that opens while its User is revoked: a handshake that passed the Provider before the sessions were deleted may call `Add` after `Close` copied the list, and that Socket stays open. The window is the length of one handshake. What closes it is the order in the example (the User is disabled first, so a handshake that reads the User after that is refused, and the Provider is the gate) and, for an application that cannot tolerate even that, a second `Close` after a short delay; the framework does not hold an owner "revoked" for later, because that is a record that grows with Users revoked and with what a client can make an application revoke. It is written under _fails at_.
- _A client that opens and drops sockets under one owner without end_: each leaves at its end, so the owner's entry never exceeds the open Sockets, which `MaxConns` bounds.
- _A key of another User_: an owner comes from the handler's authenticated Input, never from the client's message; the guide says to use the Provider's User, and the empty-owner error stops the unauthenticated case from being skipped.
- _A Socket that is slow to answer the Close_: it is counted when `Close` sent the frame, and stays in the index until its end (five seconds at most), so a second `Close` of the same owner counts 0 for it.
- _`Close` called from inside an `OnMessage` of one of the owner's sockets_: `Close` only queues the Close frame and never waits for an `OnClose`, so the socket whose handler is running is closed too and no lock is held across user code.

**What it fails at.** The window above. An owner is a string the application chose: two applications of one process using one `SocketOwners` for different meanings mix them, so a value per meaning is the rule. The zero `SocketOwners` reaches this process's Sockets only: an application with several instances makes it with `NewSocketOwners`, and one that does not is signing out on one instance. On a brokered one the order is as reliable as the broker: Postgres keeps nothing for a listener that is down, so a gap closes every Socket of the family by default (a reconnect of all of them for every reconnect of the broker's connection, spread but not free), and an instance that is cut from the broker for longer than its Sockets live misses an order for good unless `OnGap` or the Provider catches it. An order reaches only the Sockets held when it arrives. The order is not authenticated: a broker is trusted by the application as the database is, so anything that can publish on its name can close a User's Sockets, and the name is one the application's own code and the broker's access control keep. The log line carries the owner. A status the client treats as a reason to reconnect (1001, 1012) makes a well-behaved client reconnect at once into a refusal; the example uses 4401 for that reason.

**What was rejected for the instances.** The guide's recipe (a `Topic[SignOut]` and `Listen`): it worked, and every application wrote it again with its own gap, usually none, so a sign-out lost across a reconnect left the Socket open with no one to know; the safe gap handling belongs where the Sockets are. A `Close` that returns the sum of every instance's count: the others answer nobody, and waiting for them would turn a sign-out into a vote with a timeout. Listening for the life of the application: above. A gap that only logs by default: it fails open, which for a sign-out is the wrong way to fail (Principle 2); the cost of failing closed is stated beside `OnGap` and the application that minds replaces it. A `MaxPayload` for symmetry with `NewTopic`: a knob with nothing to bound.

**Axis it spends:** none for a Socket that is never added (no field on `Socket`, no work on open or close); for one that is, a map insert and a list entry under a lock, no allocation on `Send` or `Publish`, and the idle-connection memory is unchanged for every socket not added (the budget tests are unchanged: 2 and 3 allocations, and the idle-connection memory). The zero `SocketOwners` gains one pointer, nil, and is 40 bytes (`unsafe.Sizeof`, `TestWhatAnIdleSocketOwnersCostsIsOnePointerAndNoBrokerConnection`); a brokered one with no Socket is those 40 bytes plus one link of about 100 bytes made once at startup and holds no subscription. With a Socket it holds one broker subscription, the cost of one Topic that is listening. Nothing is on the request path.

**What was rejected.**

- _`Socket.SetOwner(key)` with an index kept by the `App`_ (the shape of the first proposal): it puts a pointer to the index in every Socket, 8 bytes on each idle connection whether or not the application uses it, against the guarded axis of idle memory; it needs the Socket to reach the `App`, which no Socket does today; and one app-wide index puts every use of owners in one keyspace, so a User's id and a tenant's id collide. A value the application makes costs nothing where it is not used and cannot collide.
- _A package-level index_: global state ([ADR 0026](0026-no-global-state-and-one-behaviour.md)).
- _An owner as a property of the route_ (`OwnerFrom(func(In) string)` as a `RouteOption`): it is declarative, but it hides the owner in an option away from the handler that opens the socket, it cannot express a socket that is under an owner only after a first message, and it is a second way beside `Add` as soon as one route needs it.
- _Skipping an empty owner without an error_: a socket that cannot be revoked is the failure `Close` exists to prevent, and doing it in silence is what the second principle refuses.
- _Closing every Socket of an owner on the `Sessions` revoking_: see [ADR 0056](0056-a-session-is-sealed-in-a-cookie-and-changed-by-returning-its-cookie.md), "What was rejected (owners)".
- _A held "revoked" mark for the owner_ (so a late `Add` is refused): unbounded growth with owners revoked, above.
- _`Close` returning only the count_: a status the server may not send would then close nothing and look like an owner with no Sockets.
