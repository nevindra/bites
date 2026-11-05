package bitetest

import (
	"bytes"
	"context"
	"runtime"
	"sync"

	"github.com/nevindra/bites"
)

// Broker is an in-memory bites.Broker for tests (ADR 0081): the instances of an application that
// share one bus, in one process. A test makes one with NewBroker, gives it to the Topics of one
// instance, and gives Peer() to the Topics of another; a Publish on either reaches the Sockets of
// both, in the order of the calls, on the publisher's goroutine, so the test needs no sleep. Async
// makes each subscription deliver on a goroutine of its own instead, as modules/pgnotify does: use
// it for a test of a path that production reaches through such a Broker. The zero value is not
// usable.
type Broker struct {
	bus *bus
}

type bus struct {
	mu    sync.Mutex
	subs  map[string][]*subscription
	fail  error
	max   int // 0: no limit
	async bool

	// idle is signalled, under mu, when pending reaches zero. pending counts payloads queued or
	// being delivered under Async; workers holds the goroutine ids of the running workers, so a
	// Settle called from a callback is recognised.
	idle    *sync.Cond
	pending int
	workers map[uint64]struct{}
}

// goid is the id of the calling goroutine. The runtime offers no API for it, so it is read from
// the first line of the stack ("goroutine 18 [running]:"); this is a test package, and the id is
// used only to refuse a Settle from inside a delivery.
func goid() uint64 {
	var buf [32]byte
	n := runtime.Stack(buf[:], false)
	var id uint64
	for _, c := range buf[len("goroutine "):n] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

type subscription struct {
	deliver func([]byte)
	gap     func()
	live    bool

	// qmu guards queue and running, used only under Async. A worker goroutine runs while queue is
	// not empty and then ends, so a subscription holds no goroutine at rest.
	qmu     sync.Mutex
	queue   [][]byte
	running bool
}

// enqueue appends p and starts the worker if none runs, so one goroutine at a time delivers a
// subscription and its payloads keep the order of the Publish calls.
func (s *subscription) enqueue(b *bus, p []byte) {
	b.mu.Lock()
	b.pending++
	b.mu.Unlock()
	s.qmu.Lock()
	s.queue = append(s.queue, p)
	start := !s.running
	s.running = true
	s.qmu.Unlock()
	if start {
		go s.drain(b)
	}
}

func (s *subscription) drain(b *bus) {
	id := goid()
	b.mu.Lock()
	b.workers[id] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.workers, id)
		b.mu.Unlock()
	}()
	for {
		s.qmu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.qmu.Unlock()
			return
		}
		p := s.queue[0]
		s.queue = s.queue[1:]
		s.qmu.Unlock()
		b.mu.Lock()
		live := s.live
		b.mu.Unlock()
		if live { // a stopped subscription is delivered nothing more
			s.deliver(p)
		}
		b.mu.Lock()
		b.pending--
		if b.pending == 0 {
			b.idle.Broadcast()
		}
		b.mu.Unlock()
	}
}

// NewBroker returns a Broker on a new bus.
func NewBroker() *Broker {
	b := &bus{subs: map[string][]*subscription{}, workers: map[uint64]struct{}{}}
	b.idle = sync.NewCond(&b.mu)
	return &Broker{bus: b}
}

// Peer returns a Broker on the same bus: another instance of the application.
func (b *Broker) Peer() *Broker { return &Broker{bus: b.bus} }

// Async makes every subscription on the bus deliver on a goroutine of its own, in the order of the
// Publish calls for that subscription, as modules/pgnotify does: Publish returns before the
// Sockets have the message, so a test reads the frame it expects (or waits with WS.Pending after
// a ping of the receiving socket) instead of assuming it arrived. Without it, the state of a new
// bus, Publish delivers on the calling goroutine and the test needs no wait. Call it before the
// first Publish: payloads already delivered are not reordered, and one already queued keeps its
// goroutine. A subscription that stopped is delivered nothing more, queued payloads included.
func (b *Broker) Async() {
	b.bus.mu.Lock()
	b.bus.async = true
	b.bus.mu.Unlock()
}

// Settle returns once the bus is idle: no payload is queued and none is being delivered, under
// Async. It is how a test asserts that something did not arrive without a real-time wait: publish,
// Settle, then ask the receiving side (WS.Quiet, a counter the subscriber would have bumped).
//
// It waits for every delivery queued before the call, and for the deliveries those publish in
// turn (a callback that calls Publish again), because it returns only when nothing is queued or
// running. A delivery chain that never ends keeps Settle waiting, as it would keep the bus busy;
// so does a publisher on another goroutine that keeps publishing. Without Async, delivery runs on
// the publisher's goroutine and has finished when Publish returns, so Settle returns at once.
//
// It does not wait for what a delivery causes after the callback returned: a frame the Topic
// wrote to a Socket still travels through the in-memory connection to the client, which WS.Quiet
// and WS.Pending wait for with a ping (call Settle first, then Quiet). Gap callbacks, which run on
// the caller of Gap, are not deliveries.
//
// Settle called from inside a subscription's callback, under Async, would wait for the delivery
// that called it, so it panics, naming the fix: call it from the test's goroutine, after the
// Publish that started the chain.
func (b *Broker) Settle() {
	bu := b.bus
	bu.mu.Lock()
	defer bu.mu.Unlock()
	if bu.async {
		if _, in := bu.workers[goid()]; in {
			panic("bitetest.Broker.Settle: called from inside a subscription's callback, which would wait for its own delivery: call Settle from the test's goroutine after the Publish that started the chain, not from a Topic's OnMessage or a Subscribe callback ")
		}
	}
	for bu.pending > 0 {
		bu.idle.Wait()
	}
}

// Fail makes every Publish on the bus return err until it is called again with nil. Nothing is
// delivered by a Publish that fails, as with a real broker that is down.
func (b *Broker) Fail(err error) {
	b.bus.mu.Lock()
	b.bus.fail = err
	b.bus.mu.Unlock()
}

// Limit makes every Publish on the bus of a payload over n bytes return a *bites.PayloadError, as
// a Broker with a limit of its own does (Postgres takes under 8000), and send nothing. Zero, the
// state of a new bus, is no limit; a negative n panics.
func (b *Broker) Limit(n int) {
	if n < 0 {
		panic("bitetest.Broker.Limit: n is negative: give the most bytes a payload may have, or 0 for no limit")
	}
	b.bus.mu.Lock()
	b.bus.max = n
	b.bus.mu.Unlock()
}

// Gap tells every subscription on the bus, as a real broker does after its connection came back,
// that messages may have been lost: each Topic's OnGap runs, on the calling goroutine.
func (b *Broker) Gap() {
	for _, s := range b.live("") {
		s.gap()
	}
}

// Subscriptions is how many subscriptions to name are live on the bus. A Topic subscribes while
// it has a Socket and stops when its last one leaves, so this is the number of Topics listening.
func (b *Broker) Subscriptions(name string) int {
	return len(b.live(name))
}

func (b *Broker) live(name string) []*subscription {
	b.bus.mu.Lock()
	defer b.bus.mu.Unlock()
	var out []*subscription
	for n, list := range b.bus.subs {
		if name != "" && n != name {
			continue
		}
		for _, s := range list {
			if s.live {
				out = append(out, s)
			}
		}
	}
	return out
}

// Accepts accepts any name.
func (b *Broker) Accepts(string) error { return nil }

// Publish delivers payload to every live subscription to name, this Broker's included, on the
// calling goroutine, or on the goroutine of each subscription after Async. It returns
// the error given to Fail, and a *bites.PayloadError for a payload over Limit.
func (b *Broker) Publish(_ context.Context, name string, payload []byte) error {
	b.bus.mu.Lock()
	err, max, async := b.bus.fail, b.bus.max, b.bus.async
	b.bus.mu.Unlock()
	if err != nil {
		return err
	}
	if max > 0 && len(payload) > max {
		return &bites.PayloadError{Name: name, Size: len(payload), Limit: max}
	}
	for _, s := range b.live(name) {
		if async {
			s.enqueue(b.bus, bytes.Clone(payload))
		} else {
			s.deliver(bytes.Clone(payload))
		}
	}
	return nil
}

// Subscribe registers deliver and gap for name. The stop it returns forgets them, so a Topic that
// stopped listening is not held by the bus.
func (b *Broker) Subscribe(name string, deliver func([]byte), gap func()) func() {
	s := &subscription{deliver: deliver, gap: gap, live: true}
	b.bus.mu.Lock()
	b.bus.subs[name] = append(b.bus.subs[name], s)
	b.bus.mu.Unlock()
	return func() {
		b.bus.mu.Lock()
		defer b.bus.mu.Unlock()
		if !s.live {
			return
		}
		s.live = false
		list := b.bus.subs[name]
		for i, x := range list {
			if x == s {
				list = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(b.bus.subs, name)
		} else {
			b.bus.subs[name] = list
		}
	}
}

var (
	_ bites.Broker = (*Broker)(nil)
)
