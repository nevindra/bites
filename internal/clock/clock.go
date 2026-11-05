// Package clock is the one source of time the engine and its tests share.
//
// ADR 0027 has the application take a Clock so that a timeout, a heartbeat or an idle limit
// is tested by moving time forward instead of waiting for it. Real is the production clock;
// Fake moves only when told to.
//
// Timer has only Stop, as the engine needs. A timer that can also be re-armed in place
// implements Resetter, and callers that set a deadline on every request (memconn) use it so
// the steady state allocates nothing; time.Timer and the fake's timers both have it.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock reads the time and schedules a function after a duration.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc.
type Timer interface {
	// Stop prevents the function from running. It reports whether it stopped a pending call.
	Stop() bool
}

// Resetter is a Timer that can be re-armed in place.
type Resetter interface {
	Timer
	Reset(d time.Duration) bool
}

type real struct{}

// Real returns the clock of the machine.
func Real() Clock { return real{} }

func (real) Now() time.Time { return time.Now() }

func (real) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Fake is a clock that moves only when Advance is called. It is safe for concurrent use.
// A timer set for a duration of zero or less is due at the next Advance (even Advance(0)).
type Fake struct {
	adv sync.Mutex // serialises Advance so timers fire in order

	mu     sync.Mutex
	now    time.Time
	seq    uint64
	timers []*fakeTimer // pending, ordered by (when, seq)
}

// NewFake returns a fake clock reading start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now is the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// AfterFunc schedules fn for d from the fake now.
func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	t := &fakeTimer{f: f, fn: fn}
	f.mu.Lock()
	f.arm(t, d)
	f.mu.Unlock()
	return t
}

// Pending is the number of timers waiting to fire.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// Next is the time of the earliest pending timer. Tests use it to wait until code under test
// has set the deadline they are about to expire, without sleeping.
func (f *Fake) Next() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.timers) == 0 {
		return time.Time{}, false
	}
	return f.timers[0].when, true
}

// Advance moves the clock forward by d, running every timer that falls due, earliest first,
// each with the clock reading its own time. Timer functions run on the caller's goroutine
// with no lock held, so they may set new timers, which also fire if they fall due.
func (f *Fake) Advance(d time.Duration) {
	f.adv.Lock()
	defer f.adv.Unlock()
	f.mu.Lock()
	target := f.now.Add(d)
	for len(f.timers) > 0 && !f.timers[0].when.After(target) {
		t := f.timers[0]
		f.timers = f.timers[1:]
		t.active = false
		if t.when.After(f.now) {
			f.now = t.when
		}
		fn := t.fn
		f.mu.Unlock()
		fn()
		f.mu.Lock()
	}
	f.now = target
	f.mu.Unlock()
}

// arm inserts t, due d from now. Caller holds f.mu.
func (f *Fake) arm(t *fakeTimer, d time.Duration) {
	f.seq++
	t.seq = f.seq
	t.when = f.now.Add(d)
	t.active = true
	i := sort.Search(len(f.timers), func(i int) bool {
		o := f.timers[i]
		return o.when.After(t.when) || (o.when.Equal(t.when) && o.seq > t.seq)
	})
	f.timers = append(f.timers, nil)
	copy(f.timers[i+1:], f.timers[i:])
	f.timers[i] = t
}

func (f *Fake) disarm(t *fakeTimer) bool {
	if !t.active {
		return false
	}
	for i, o := range f.timers {
		if o == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			break
		}
	}
	t.active = false
	return true
}

type fakeTimer struct {
	f      *Fake
	fn     func()
	when   time.Time
	seq    uint64
	active bool
}

func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	return t.f.disarm(t)
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.f.disarm(t)
	t.f.arm(t, d)
	return was
}
