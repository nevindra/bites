package clock

import (
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestAFakeClockFiresDueTimersInOrderEachAtItsOwnTime(t *testing.T) {
	f := NewFake(epoch)
	var got []time.Duration
	for _, d := range []time.Duration{30, 10, 20, 50} {
		f.AfterFunc(d*time.Second, func() { got = append(got, f.Now().Sub(epoch)) })
	}
	f.Advance(35 * time.Second)
	want := []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("fired at %v, want %v", got, want)
	}
	if f.Pending() != 1 || f.Now() != epoch.Add(35*time.Second) {
		t.Fatalf("pending %d now %v", f.Pending(), f.Now())
	}
}

func TestAStoppedFakeTimerDoesNotFireAndStopSaysSo(t *testing.T) {
	f := NewFake(epoch)
	fired := false
	tm := f.AfterFunc(time.Second, func() { fired = true })
	if !tm.Stop() || tm.Stop() {
		t.Fatal("Stop must report true once, then false")
	}
	f.Advance(time.Hour)
	if fired {
		t.Fatal("stopped timer fired")
	}
}

func TestAResetFakeTimerFiresAtItsNewTime(t *testing.T) {
	f := NewFake(epoch)
	n := 0
	tm := f.AfterFunc(time.Second, func() { n++ }).(Resetter)
	tm.Reset(10 * time.Second)
	f.Advance(5 * time.Second)
	if n != 0 {
		t.Fatal("fired at the old time")
	}
	f.Advance(5 * time.Second)
	if n != 1 {
		t.Fatalf("fired %d times", n)
	}
}

func TestATimerSetByATimerThatFallsDueInTheSameAdvanceFires(t *testing.T) {
	f := NewFake(epoch)
	n := 0
	f.AfterFunc(time.Second, func() { f.AfterFunc(time.Second, func() { n++ }) })
	f.Advance(3 * time.Second)
	if n != 1 {
		t.Fatalf("chained timer fired %d times", n)
	}
}

func TestAFakeClockIsSafeForConcurrentUse(t *testing.T) {
	f := NewFake(epoch)
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tm := f.AfterFunc(time.Second, func() { mu.Lock(); n++; mu.Unlock() })
			if i%2 == 0 {
				tm.Stop()
			}
			f.Now()
			f.Advance(time.Millisecond)
		}()
	}
	wg.Wait()
	f.Advance(time.Minute)
	mu.Lock()
	defer mu.Unlock()
	if n != 25 {
		t.Fatalf("fired %d, want 25", n)
	}
}

func TestTheRealClockRunsAFunctionAndStops(t *testing.T) {
	c := Real()
	done := make(chan struct{})
	c.AfterFunc(time.Millisecond, func() { close(done) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("real timer never fired")
	}
	if c.AfterFunc(time.Hour, func() {}).Stop() != true {
		t.Fatal("Stop of a pending real timer must be true")
	}
}
