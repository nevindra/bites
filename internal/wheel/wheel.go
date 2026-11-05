// Package wheel is the timer wheel of ADR 0039: one per app, so that a socket's heartbeat and
// close deadlines cost it a Node inside a struct it already has, where a time.Timer each would
// add its memory to every idle connection (ADR 0003). It is engine-internal and is not called
// by the engine yet (milestone 2, step 2).
//
// # Shape
//
// Time is cut into ticks. A ring of Slots lists holds the nodes due at each tick, and the wheel
// runs on a single timer of the clock.Clock it is given, so a clock.Fake moves it. The timer
// exists only while a node is armed: an app with no sockets runs nothing.
//
// A deadline is rounded up to the next tick, so a node never fires early (it may fire up to one
// tick late, and later still if the wheel is overloaded). A delay longer than Max is clamped to
// Max; the caller re-arms, as a heartbeat does. Nodes due at the same tick fire in the order
// they were armed.
//
// # Cost
//
// A Node is 32 bytes on amd64 (two list pointers, the due tick, the function), embedded in the
// socket that owns it; arming, re-arming and removing one are O(1), take the wheel's lock for a
// few pointer writes and allocate nothing. The wheel allocates once, at New, plus the clock's
// timer when the first node is armed. What a socket costs in the measured idle-memory gate is
// written into ADR 0039 when step 12 builds it, not here.
//
// # Work per tick
//
// One run of the wheel does at most Config.MaxPerTick units of work, a unit being a node fired
// or an empty slot passed. If more is due, it yields and resumes a millisecond later, so a
// thousand sockets due in one second cannot hold the timer goroutine, and a wheel that fell
// behind (a suspended machine) catches up in slices. A node's function runs on the wheel's
// goroutine with no lock held: it must not block, and it may arm or remove nodes, including its
// own.
package wheel

import (
	"sync"
	"time"

	"github.com/nevindra/bites/internal/clock"
)

// Node is one deadline. Embed it in the struct that owns the deadline and call Init once. The
// zero Node is unarmed. A Node must not be copied while armed, and belongs to one Wheel.
type Node struct {
	next, prev *Node
	due        uint64
	fn         func()
}

// Init sets the function to run when the node fires. Call it before the first Add.
func (n *Node) Init(fn func()) { n.fn = fn }

// Armed reports whether the node is waiting to fire. It is not safe to call while another
// goroutine may be arming or firing the node; use it in tests and where the owner serialises.
func (n *Node) Armed() bool { return n.next != nil }

// Config sizes a Wheel. Zero fields take the defaults.
type Config struct {
	Tick       time.Duration // resolution; default 1 s
	Slots      int           // ring size; default 512, so Max is 511 s at the default tick
	MaxPerTick int           // work units per run; default 1024
}

const yield = time.Millisecond

// Wheel is a timer wheel on a clock.
type Wheel struct {
	clk   clock.Clock
	tick  time.Duration
	max   int
	epoch time.Time

	mu      sync.Mutex
	slots   []Node // circular list heads: slots[i].next is the first node due at a tick = i mod len
	cur     uint64 // the next tick to process
	count   int
	running bool
	timer   clock.Timer
	armed   bool // the timer is pending
	closed  bool
}

// New returns a wheel on clk whose tick 0 is clk.Now().
func New(clk clock.Clock, cfg Config) *Wheel {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.Slots <= 1 {
		cfg.Slots = 512
	}
	if cfg.MaxPerTick <= 0 {
		cfg.MaxPerTick = 1024
	}
	w := &Wheel{clk: clk, tick: cfg.Tick, max: cfg.MaxPerTick, epoch: clk.Now(), slots: make([]Node, cfg.Slots)}
	for i := range w.slots {
		w.slots[i].next, w.slots[i].prev = &w.slots[i], &w.slots[i]
	}
	return w
}

// Max is the longest delay Add honours.
func (w *Wheel) Max() time.Duration { return time.Duration(len(w.slots)-1) * w.tick }

// Len is the number of armed nodes.
func (w *Wheel) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count
}

// Add arms n to fire after d, or moves it if it is armed. d is rounded up to a tick and clamped
// to Max. It reports whether n was already armed.
func (w *Wheel) Add(n *Node, d time.Duration) (wasArmed bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	if n.next != nil {
		unlink(n)
		w.count--
		wasArmed = true
	}
	elapsed := w.clk.Now().Sub(w.epoch)
	if elapsed < 0 {
		elapsed = 0
	}
	if w.count == 0 && !w.running {
		// Nothing is waiting, so the ring can jump to the present without skipping a node.
		if t := uint64(elapsed / w.tick); t > w.cur {
			w.cur = t
		}
	}
	if d < 0 {
		d = 0
	}
	due := ceilTick(elapsed+d, w.tick)
	if due < w.cur {
		due = w.cur
	}
	if last := w.cur + uint64(len(w.slots)) - 1; due > last {
		due = last
	}
	n.due = due
	s := &w.slots[due%uint64(len(w.slots))]
	n.prev, n.next = s.prev, s
	s.prev.next = n
	s.prev = n
	w.count++
	if !w.armed && !w.running {
		w.arm(elapsed, false)
	}
	return wasArmed
}

// Remove disarms n and reports whether it was armed. It is a no-op for a node that has fired.
func (w *Wheel) Remove(n *Node) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n.next == nil {
		return false
	}
	unlink(n)
	w.count--
	return true
}

// Close stops the wheel's timer. Armed nodes never fire; Add after Close does nothing.
func (w *Wheel) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
	}
	w.armed = false
}

func unlink(n *Node) {
	n.prev.next = n.next
	n.next.prev = n.prev
	n.next, n.prev = nil, nil
}

// ceilTick is the smallest tick number whose start is at or after e.
func ceilTick(e, tick time.Duration) uint64 {
	t := e / tick
	if e%tick != 0 {
		t++
	}
	return uint64(t)
}

// arm sets the clock timer for the next tick boundary, or for a millisecond ahead when soon is
// set. Caller holds w.mu.
func (w *Wheel) arm(elapsed time.Duration, soon bool) {
	var d time.Duration
	if soon {
		d = yield
	} else {
		at := ceilTick(elapsed, w.tick)
		if at < w.cur {
			at = w.cur
		}
		d = time.Duration(at)*w.tick - elapsed
		if d < 0 {
			d = 0
		}
	}
	w.armed = true
	if r, ok := w.timer.(clock.Resetter); ok {
		r.Reset(d)
		return
	}
	w.timer = w.clk.AfterFunc(d, w.run)
}

// run is the timer's function: it fires what is due, within the budget, and re-arms.
func (w *Wheel) run() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.armed = false
	w.running = true
	budget := w.max
	more := false
	for {
		elapsed := w.clk.Now().Sub(w.epoch)
		if elapsed < 0 {
			elapsed = 0
		}
		target := uint64(elapsed / w.tick)
		if w.count == 0 {
			if target >= w.cur {
				w.cur = target + 1
			}
			break
		}
		if w.cur > target {
			break
		}
		if budget == 0 {
			more = true
			break
		}
		budget--
		s := &w.slots[w.cur%uint64(len(w.slots))]
		n := s.next
		if n == s {
			w.cur++
			continue
		}
		unlink(n)
		w.count--
		fn := n.fn
		w.mu.Unlock()
		if fn != nil {
			fn()
		}
		w.mu.Lock()
		if w.closed {
			w.running = false
			w.mu.Unlock()
			return
		}
	}
	w.running = false
	if w.count > 0 {
		w.arm(w.clk.Now().Sub(w.epoch), more)
	}
	w.mu.Unlock()
}
