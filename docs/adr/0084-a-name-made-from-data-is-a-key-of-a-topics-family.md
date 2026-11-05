# A name made from data is a key of a Topics family, and a Topic can leave one Socket out of a message

A [`Topic`](0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md) is a name the code knows: it exists where it has Sockets, and an application that needs a room per id, or the frames of one User, kept a map of Topics of its own and dropped each when it emptied (`examples/chat` has that map). An application built on bites then gave two pieces of feedback. "A Topic's message type must now be all `Opt`, so a frame is written twice": it published bytes it had encoded itself to names it held no Topic for, and asked for `PublishTo(broker, name, v)`. "A Topic delivers one frame to every socket, so something for one socket needs a Topic of its own": the Topic per User, with a `LISTEN` of its own for each. A third item, "A Topic cannot skip a socket, so everyone but the ones who muted is not a Topic", had it leave the Topic and keep a map of Sockets per Space with a mute set, about 60 lines. Decided on 2026-11-04 and built.

**Decision.**

- **`Topics[Out]` is a family of Topics named by a key known at run time.** Each key is a Topic, made when its first Socket subscribes and dropped when its last leaves. The family keeps only the keys that have a Socket on this instance, and the map itself is set to nil when empty, because a Go map does not shrink and the busiest moment of a family would otherwise stay in memory. The zero value is the family of one process and is ready; `NewTopics[Out](prefix, broker, TopicsOptions)` makes one whose key `k` is the broker name `prefix+k` on every instance.

  ```go
  func NewTopics[Out any](prefix string, broker Broker, o TopicsOptions) *Topics[Out]
  type TopicsOptions struct { OnGap func(key string); MaxPayload int; Log *slog.Logger }
  func (ts *Topics[Out]) Subscribe(key string, s *Socket[Out]) error
  func (ts *Topics[Out]) SubscribeUnless(key string, s *Socket[Out], skip func(v Out) bool) error
  func (ts *Topics[Out]) Unsubscribe(key string, s *Socket[Out])
  func (ts *Topics[Out]) Publish(key string, v Out) error
  func (ts *Topics[Out]) PublishLocal(key string, v Out) error
  func (ts *Topics[Out]) Len(key string) int
  func (ts *Topics[Out]) Keys() int
  func (ts *Topics[Out]) Resolve(fn func(key string, v Out) (Out, bool))
  ```

- **`Publish(key, v)` on a brokered family goes straight to the broker under `prefix+key`, whether or not this instance has a Socket of `key`.** That is what a Topic could not do, since it exists only where it has Sockets: the instance that publishes to a User's frames is rarely the one that holds the User's Socket. A key with no Socket anywhere is no error and costs a publish and nothing else. On a zero family `Publish` is `PublishLocal`.
- **Subscribing is checked, not refused at Registration, because the key comes from data.** `Subscribe` and `SubscribeUnless` return an error for an empty key and for one the broker's `Accepts` refuses, and subscribe nothing; `Publish` returns the same errors. `Resolve` is the family's: it is set once and applies to every key, including those made later, and `fn` is given the key the message was published to (revised 2026-11-05: a resolver of a family must know which room or conversation a reference belongs to, and a message that repeated its key only so it could be resolved would write it twice). Each key's Topic holds `fn` with its key given, so the Topic's own `Resolve(fn func(Out))` is unchanged. A program whose broker is optional (an App built only to write its API document) uses a zero `Topics`, which has none, and does not call `Resolve`; a nil broker given to `NewTopics` stays a panic, since in production it is a mistake.
- **A gap is told once for each key that has a Socket here.** `TopicsOptions.OnGap(key)` runs for each, and the application usually `PublishLocal`s a message that makes that key's clients read their state again. With no `OnGap` the gap is logged at Warn, at most once a second whatever the number of keys, so a thousand rooms do not write a thousand lines.
- **How a Topic is dropped without a race.** A `pending` count on each key's entry lets a `Subscribe` that has taken the entry but not yet added its Socket keep the Topic from being dropped under it. The family's lock is never held while a Socket joins or leaves: a Socket that has already ended leaves its Topic at once, synchronously, and leaving takes the family's lock to drop the Topic, so holding it across the join would deadlock.

**`Topic.SubscribeUnless(s, skip func(v Out) bool)` leaves one Socket out of a Topic's messages** (a revision of [ADR 0047](0047-a-socket-is-a-handle-and-a-topic-is-a-broadcast.md), edited there in place). It is for what differs from one Socket to the next on a Topic they share: the people a User muted or blocked, or the language of a User.

- `skip` reads the User's current state, so muting needs no new subscription. Calling `SubscribeUnless` again replaces the function, and `Subscribe` takes it away. A nil `skip` panics.
- `skip` runs outside the Topic's lock, on the goroutine that publishes (on a brokered Topic, the broker's), for each message, so it must be quick and must not block or change the message.
- A panic in `skip` is recovered, logged at Error, and leaves that Socket out of that message: it fails closed, because a filter that fails must not let through what it was meant to hold back.
- The message is still encoded and framed once, and a message every Socket skips is not encoded at all. The functions are kept in a second map, only for the Sockets that have one, so a Topic with none pays one nil map and nothing more per `Publish`.
- `Topics.SubscribeUnless(key, s, skip)` is the same on a key.

**Two leaks found by the probe and fixed with it** ([`docs/research/topic-cost`](../research/topic-cost/README.md)). `bitetest.Broker`'s `stop` marked a subscription dead and kept it, holding the Topic (613 B a key left after its last Socket); it now forgets the subscription. The engine's `WS.Forget` shifted a Socket's list of Topics without clearing the last slot, so a Socket kept alive the last Topic it left; it now clears it. Without both, "dropped when its last Socket leaves" would have been false in memory.

**Refusals** (rows of `refusal_test.go`, each ending `registered at <file:line>`):

- `NewTopics: the prefix is empty: give the start every instance puts before a key, such as "room.", so the names of this family are not those of another`
- `NewTopics "room.": the broker is nil: give the Broker this family publishes through, or use a zero bites.Topics[Out] for a family of this process only`
- `NewTopics "room.": MaxPayload is -1, which is not a limit: give a positive number of bytes, or zero for 1 MiB`
- `NewTopics "room.": the message type <T> cannot be sent through a broker: <the codec's reason>: use a type a Socket can send, without channel or function fields`
- `Topic.SubscribeUnless` and `Topics.SubscribeUnless` with a nil `skip` panic with `skip is nil: give the function that leaves this socket out of a message, or use Subscribe for every message`.

**What it costs** ([ADR 0003](0003-the-trade-budget.md)). Neither axis moves: nothing is added to a connection, a route or a request. Allocations of `Publish` are unchanged for a Topic with no `SubscribeUnless` function (`TestAPublishToManySubscribersBuildsTheFrameOnceAndCostsTheSameForOneAsForAHundred`). Memory, measured with the probe ([`docs/research/topic-cost`](../research/topic-cost/README.md)) on an AMD Ryzen 7 9700X, Go 1.27.3, at commit f7ac6c3 with the working tree of 2026-11-04, pinned to cores 2,3,10,11, three runs: a zero `Topic` 120 B (104 before, two more words), an idle `NewTopic` 320 B (304 before), a key of a brokered `Topics` with its one Socket 783 B, **0 B left once the last Socket of every key has left**, and a `SubscribeUnless` function 37 B. A Topic per User through a `Topics` costs what the hand-made map cost with the code gone.

**Scenarios it was put against.**

- _A hostile client creating keys._ It cannot: a key comes from the handler (the User's id from a Provider, a room id the handler checked), never from a message, and the family keeps only keys that have a Socket, which the connection limits bound ([ADR 0005](0005-our-own-http1-engine-with-a-goroutine-per-connection.md)). A handler that passes a client-chosen string straight to `Subscribe` is the application's mistake, and the guide says to check it first.
- _A key with no Socket anywhere._ `Publish` costs the encoding and one `Broker.Publish`; no Topic is made on any instance, and nothing is left behind.
- _A gap with thousands of keys._ `OnGap(key)` runs once for each key that has a Socket here, and a family with no `OnGap` writes one Warn line a second at most.
- _A Socket closing while it subscribes._ It leaves at once, synchronously, from inside the join; the `pending` count keeps the Topic from being dropped between the entry being taken and the Socket added, and the family's lock is not held across the join. Tested in `topics_test.go`.
- _A filter that panics._ The Socket is left out of that message and an Error line says why; the other Sockets get it, and the next message runs the filter again.
- _A message every Socket skips._ It is not encoded; `Publish` returns without a frame.

**What it fails at.**

- **The broker subscription churn of a Topic per User.** A brokered key has one broker subscription while it has a Socket, so a family of a Topic per User is one `LISTEN` bookkeeping entry per User connected, the same as a hand-made Topic per User today. With `pgnotify` that is a map entry on the one shared channel, not a Postgres `LISTEN`, but every instance still receives and discards the notifications of keys it has no Socket for ([ADR 0081](0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)). A rate the database cannot carry for all instances needs a channel per shard, which is not built.
- **The prefix must not begin another name.** Keys of `room.` and a Topic named `room.7` are the same broker name, and a Topic named `room.lobby` would be heard as the key `lobby`. Nothing can see another instance's names, so the refusal cannot be made; the doc comment says to give each family a prefix of its own, ending in a separator.
- **A filter is not a security boundary for what the Topic carries.** `SubscribeUnless` holds back what a User chose not to see. A message that a non-member must never receive belongs on a Topic the non-member is not in.
- **`skip` runs on the publisher's goroutine for every Socket that has one**, so a slow `skip` slows the publish for the whole Topic; a Topic of ten thousand Sockets each with a filter costs ten thousand calls a message.

**Considered and what was rejected.**

- **`bites.PublishTo[Out](broker, name, v)`** (what the application asked for): it publishes, but each application would still keep its own map of Topics per name and drop them when empty, which is the code every application rewrites, and it would be a second way to publish to a Topic's name ([principle 1](0077-four-principles-in-the-order-they-win.md)).
- **Making `NewTopic` itself cheap and leaving the map to the application:** it is cheap (320 B), but the map, the lock, the drop on empty and the race between a join and a drop are the work this ADR is meant to save.
- **`Topic.PublishExcept(v, keep func(*Socket) bool)`:** the publisher would need to know each Socket's User at every call, and on another instance it holds no Socket.
- **One filter in `TopicOptions` for every Socket:** it would have to look each Socket's User up on every message; a function given with the Socket already holds the User.
- **Letting `skip` fail open:** a filter that panics would let through what it was meant to hold back; failing closed is the safe side ([principle 2](0077-four-principles-in-the-order-they-win.md)).
- **`Topics.Resolve(fn func(Out) (Out, bool))` without the key** (the first form): the resolver of a family had to find the key inside the message, so every message of a room repeated its room id. A key parameter costs nothing to a program that ignores it.
- **When to use which.** A name the code knows (a lobby, a cache cleared on every instance) is one `Topic`; a name made from data is a key of a `Topics`.
