package bites

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Topics is a family of Topics named by a key the program knows only at run time: a room per id,
// the frames of one User, the activity of one Space (ADR 0084). Each key is a Topic of its own,
// made when its first Socket subscribes and dropped when its last one leaves, so a family of a
// million keys holds only those that have a Socket on this instance, and the application keeps no
// map of Topics. Publish to a key reaches its Sockets on every instance even when this one has
// none, which a Topic, held only where it has Sockets, cannot do. The zero value is the family of
// one process and is ready; NewTopics makes one that crosses instances through a Broker. It must
// not be copied, and it is safe for concurrent use.
//
//	var rooms bites.Topics[Frame]                    // or bites.NewTopics[Frame]("room.", broker, bites.TopicsOptions{})
//	rooms.Subscribe(strconv.FormatInt(in.Path.Room, 10), s) // in the socket's handler
//	rooms.Publish("7", frame)                        // anywhere
//
// A name the code knows (a lobby, a cache that every instance clears) is one Topic; a name made
// from data is a key of a Topics.
type Topics[Out any] struct {
	mu      sync.Mutex
	m       map[string]*topicEntry[Out]
	prefix  string
	broker  Broker
	o       TopicsOptions
	codec   *payloadCodec
	encode  func(unsafe.Pointer) ([]byte, error)
	resolve atomic.Pointer[func(string, Out) (Out, bool)]
	gapLog  atomic.Int64 // when a gap was last logged, in Unix nanoseconds, for a family with no OnGap
}

// topicEntry is one key of a Topics. pending counts the Subscribe calls between taking the entry
// and adding their Socket, so a Topic that is empty for that moment is not dropped under them.
type topicEntry[Out any] struct {
	t       *Topic[Out]
	pending int
}

// TopicsOptions are the settings of a Topics made with NewTopics. The zero value is valid.
type TopicsOptions struct {
	// OnGap is called on this instance, once for each key that has a Socket here, when the Broker
	// reports that payloads may have been lost, as TopicOptions.OnGap is for one Topic: usually
	// PublishLocal(key, a message that tells clients to reload). It must not block. Nil means a gap
	// is logged at Warn, one line a second at most whatever the number of keys.
	OnGap func(key string)
	// MaxPayload is the largest encoded message in bytes, as in TopicOptions. Zero means 1 MiB.
	MaxPayload int
	// Log receives what happens to a message in silence otherwise, as in TopicOptions. Nil means
	// slog.Default().
	Log *slog.Logger
}

// NewTopics returns a Topics whose key k is the broker name prefix+k on every instance, so every
// instance must use the same prefix and Out type, and the prefix must not begin another name the
// broker carries. It panics with a RegistrationError, naming the line, when prefix is empty,
// broker is nil, Out cannot be encoded as JSON, or MaxPayload is negative; a key the broker does
// not accept is the error of the Subscribe or Publish that names it.
func NewTopics[Out any](prefix string, broker Broker, o TopicsOptions) *Topics[Out] {
	refuse := func(format string, args ...any) {
		label := "NewTopics"
		if prefix != "" {
			label += fmt.Sprintf(" %q", prefix)
		}
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + callerWhere()})
	}
	switch {
	case prefix == "":
		refuse("the prefix is empty: give the start every instance puts before a key, such as \"room.\", so the names of this family are not those of another")
	case broker == nil:
		refuse("the broker is nil: give the Broker this family publishes through, or use a zero bites.Topics[Out] for a family of this process only")
	case o.MaxPayload < 0:
		refuse("MaxPayload is %d, which is not a limit: give a positive number of bytes, or zero for 1 MiB", o.MaxPayload)
	}
	pc, err := payloadCodecOf(reflect.TypeFor[Out]())
	if err != nil {
		refuse("%v", err)
	}
	if o.MaxPayload == 0 {
		o.MaxPayload = defaultMaxPayload
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	enc := pc.enc
	return &Topics[Out]{prefix: prefix, broker: broker, o: o, codec: pc, encode: func(p unsafe.Pointer) ([]byte, error) { return enc.Append(nil, p) }}
}

// check returns why key cannot name a Topic of the family, or nil.
func (ts *Topics[Out]) check(key string) error {
	if key == "" {
		return fmt.Errorf("bites: Topics %q: the key is empty: give the id the Topic is for, such as a room's", ts.prefix)
	}
	if ts.broker != nil {
		if err := ts.broker.Accepts(ts.prefix + key); err != nil {
			return fmt.Errorf("bites: Topics %q: the broker does not carry the key %q: %w", ts.prefix, key, err)
		}
	}
	return nil
}

// Subscribe adds s to the Topic of key, every message of it reaching s, as Topic.Subscribe does;
// the Topic is made if this is its first Socket. It returns an error, and subscribes nothing, for
// an empty key and for one the broker does not carry.
func (ts *Topics[Out]) Subscribe(key string, s *Socket[Out]) error {
	return ts.subscribe(key, s, nil)
}

// SubscribeUnless adds s to the Topic of key with a function that leaves s out of the messages it
// returns true for, as Topic.SubscribeUnless does. It returns Subscribe's errors, and panics when
// skip is nil.
func (ts *Topics[Out]) SubscribeUnless(key string, s *Socket[Out], skip func(v Out) bool) error {
	if skip == nil {
		panic("bites: Topics.SubscribeUnless: skip is nil: give the function that leaves this socket out of a message, or use Subscribe for every message")
	}
	return ts.subscribe(key, s, skip)
}

func (ts *Topics[Out]) subscribe(key string, s *Socket[Out], skip func(Out) bool) error {
	if err := ts.check(key); err != nil {
		return err
	}
	ts.mu.Lock()
	e := ts.m[key]
	if e == nil {
		e = ts.entry(key)
	}
	e.pending++
	ts.mu.Unlock()
	// Outside the family's lock: a Socket that has ended leaves its Topic at once, and leaving
	// takes that lock to drop the Topic.
	e.t.subscribe(s, skip)
	ts.mu.Lock()
	e.pending--
	ts.dropIfEmpty(key, e)
	ts.mu.Unlock()
	return nil
}

// entry makes the Topic of key; the caller holds ts.mu.
func (ts *Topics[Out]) entry(key string) *topicEntry[Out] {
	var t *Topic[Out]
	if ts.broker == nil {
		t = &Topic[Out]{}
	} else {
		o := TopicOptions{MaxPayload: ts.o.MaxPayload, Log: ts.o.Log, OnGap: ts.gapOf(key)}
		t = brokeredTopic[Out](ts.prefix+key, ts.broker, o, ts.codec)
		if r := ts.resolve.Load(); r != nil {
			t.resolve.Store(keyed(key, *r))
		}
	}
	e := &topicEntry[Out]{t: t}
	t.onEmpty = func() {
		ts.mu.Lock()
		ts.dropIfEmpty(key, e)
		ts.mu.Unlock()
	}
	if ts.m == nil {
		ts.m = map[string]*topicEntry[Out]{}
	}
	ts.m[key] = e
	return e
}

// dropIfEmpty forgets the Topic of key when nothing is in it or joining it; the caller holds ts.mu.
func (ts *Topics[Out]) dropIfEmpty(key string, e *topicEntry[Out]) {
	if ts.m[key] == e && e.pending == 0 && e.t.count() == 0 {
		delete(ts.m, key)
		if len(ts.m) == 0 {
			ts.m = nil // a map does not shrink: an empty family gives back what its busiest moment took
		}
	}
}

// gapOf is the OnGap of the Topic of key.
func (ts *Topics[Out]) gapOf(key string) func() {
	if fn := ts.o.OnGap; fn != nil {
		return func() { fn(key) }
	}
	return func() {
		now := time.Now().UnixNano()
		last := ts.gapLog.Load()
		if now-last < int64(time.Second) || !ts.gapLog.CompareAndSwap(last, now) {
			return
		}
		ts.o.Log.Warn("bites: the broker may have lost messages; set TopicsOptions.OnGap to tell clients to reload", "topics", ts.prefix)
	}
}

// Unsubscribe removes s from the Topic of key; the Topic is dropped when s was its last Socket.
func (ts *Topics[Out]) Unsubscribe(key string, s *Socket[Out]) {
	ts.mu.Lock()
	e := ts.m[key]
	ts.mu.Unlock()
	if e != nil {
		e.t.Unsubscribe(s) // leaving drops the Topic, which takes the family's lock
	}
}

// Publish sends v to the Sockets of key: on a Topics made with NewTopics, through the broker to
// every instance, this one's included, whether or not any has a Socket of key (it returns what the
// broker says, as Topic.Publish does); on a zero Topics, to this process's Sockets of key. A key
// with no Socket anywhere is no error. It returns an error for an empty key and for one the broker
// does not carry, and the encoding's.
func (ts *Topics[Out]) Publish(key string, v Out) error {
	if err := ts.check(key); err != nil {
		return err
	}
	if ts.broker != nil {
		return publishThrough(ts.broker, ts.prefix+key, ts.o.MaxPayload, ts.encode, noescapeP(unsafe.Pointer(&v)))
	}
	return ts.PublishLocal(key, v)
}

// PublishLocal sends v to this instance's Sockets of key only, never through the broker, as
// Topic.PublishLocal does; a key with no Socket here is no error.
func (ts *Topics[Out]) PublishLocal(key string, v Out) error {
	ts.mu.Lock()
	e := ts.m[key]
	ts.mu.Unlock()
	if e == nil {
		return nil
	}
	return e.t.PublishLocal(v)
}

// Len is the number of Sockets of key on this instance.
func (ts *Topics[Out]) Len(key string) int {
	ts.mu.Lock()
	e := ts.m[key]
	ts.mu.Unlock()
	if e == nil {
		return 0
	}
	return e.t.Len()
}

// Keys is the number of keys that have a Socket on this instance, each a Topic held in memory.
func (ts *Topics[Out]) Keys() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.m)
}

// keyed is fn with key given, the function a Topic of the family runs.
func keyed[Out any](key string, fn func(string, Out) (Out, bool)) *func(Out) (Out, bool) {
	f := func(v Out) (Out, bool) { return fn(key, v) }
	return &f
}

// Resolve gives every Topic of the family the function Topic.Resolve gives one: each instance runs
// it on every message the broker delivers, before the Sockets get it, and false drops the message
// on this instance. fn is given the key the message was published to (a room's id, a
// conversation's), so a message need not repeat it to be resolved. Call it once, right after
// NewTopics. A second call, a call on a zero Topics, which has no broker (a program whose broker is
// optional, such as an App built only to write its API document, makes a zero Topics and does not
// call Resolve), and a nil fn panic.
func (ts *Topics[Out]) Resolve(fn func(key string, v Out) (Out, bool)) {
	switch {
	case fn == nil:
		panic("bites: Topics.Resolve: fn is nil: give the function that turns a delivered message into the one the sockets get")
	case ts.broker == nil:
		panic("bites: Topics.Resolve on a Topics without a broker: a zero Topics delivers what this process publishes, which needs no resolving; make the Topics with NewTopics")
	}
	if !ts.resolve.CompareAndSwap(nil, &fn) {
		panic("bites: Topics.Resolve called twice on the family " + strconv.Quote(ts.prefix) + ": give one function")
	}
	ts.mu.Lock()
	for key, e := range ts.m {
		e.t.resolve.Store(keyed(key, fn))
	}
	ts.mu.Unlock()
}
