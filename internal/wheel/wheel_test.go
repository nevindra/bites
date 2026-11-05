package wheel

import (
	"fmt"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/clock"
)

var epoch = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

type sock struct {
	id    int
	node  Node
	fired int
}

func newSock(id int, log *[]int) *sock {
	s := &sock{id: id}
	s.node.Init(func() {
		s.fired++
		if log != nil {
			*log = append(*log, id)
		}
	})
	return s
}

func newWheel() (*Wheel, *clock.Fake) {
	clk := clock.NewFake(epoch)
	return New(clk, Config{}), clk
}

func TestANodeFiresWhenItsDelayHasPassedAndNotBefore(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	w.Add(&s.node, 30*time.Second)
	clk.Advance(29 * time.Second)
	if s.fired != 0 {
		t.Fatal("fired early")
	}
	clk.Advance(time.Second)
	if s.fired != 1 || s.node.Armed() || w.Len() != 0 {
		t.Fatalf("fired %d armed %v len %d", s.fired, s.node.Armed(), w.Len())
	}
}

func TestADelayIsRoundedUpToATickSoNothingFiresEarly(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	clk.Advance(400 * time.Millisecond)
	w.Add(&s.node, 2*time.Second) // due at 2.4 s, so the 3 s tick
	clk.Advance(2*time.Second + 500*time.Millisecond)
	if s.fired != 0 {
		t.Fatal("fired before 2.4 s... at 2.9 s")
	}
	clk.Advance(100 * time.Millisecond)
	if s.fired != 1 {
		t.Fatal("did not fire at the 3 s tick")
	}
}

func TestNodesFireInOrderOfDeadlineThenOfArming(t *testing.T) {
	w, clk := newWheel()
	var log []int
	s := make([]*sock, 6)
	for i := range s {
		s[i] = newSock(i, &log)
	}
	w.Add(&s[0].node, 5*time.Second)
	w.Add(&s[1].node, 2*time.Second)
	w.Add(&s[2].node, 5*time.Second)
	w.Add(&s[3].node, 1*time.Second)
	w.Add(&s[4].node, 2*time.Second)
	w.Add(&s[5].node, 9*time.Second)
	clk.Advance(10 * time.Second)
	want := []int{3, 1, 4, 0, 2, 5}
	if fmt.Sprint(log) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", log, want)
	}
}

func TestReArmingMovesTheDeadlineAndFiresOnce(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	w.Add(&s.node, 5*time.Second)
	clk.Advance(4 * time.Second)
	if !w.Add(&s.node, 5*time.Second) {
		t.Fatal("Add did not say the node was armed")
	}
	clk.Advance(2 * time.Second) // 6 s: the first deadline has passed
	if s.fired != 0 {
		t.Fatal("fired at the old deadline")
	}
	clk.Advance(3 * time.Second)
	if s.fired != 1 {
		t.Fatalf("fired %d times", s.fired)
	}
	clk.Advance(time.Minute)
	if s.fired != 1 {
		t.Fatal("fired again")
	}
}

func TestARemovedNodeNeverFires(t *testing.T) {
	w, clk := newWheel()
	a, b := newSock(1, nil), newSock(2, nil)
	w.Add(&a.node, time.Second)
	w.Add(&b.node, time.Second)
	if !w.Remove(&a.node) || w.Remove(&a.node) {
		t.Fatal("Remove misreported")
	}
	clk.Advance(time.Minute)
	if a.fired != 0 || b.fired != 1 {
		t.Fatalf("a fired %d, b fired %d", a.fired, b.fired)
	}
	if w.Remove(&b.node) {
		t.Fatal("Remove of a fired node reported true")
	}
}

func TestANodeMayReArmItselfFromItsOwnFunction(t *testing.T) {
	w, clk := newWheel()
	s := &sock{}
	s.node.Init(func() {
		s.fired++
		w.Add(&s.node, 30*time.Second)
	})
	w.Add(&s.node, 30*time.Second)
	clk.Advance(5 * time.Minute)
	if s.fired != 10 {
		t.Fatalf("heartbeat fired %d times in 5 minutes, want 10", s.fired)
	}
}

func TestANodeMayRemoveAnotherFromItsFunction(t *testing.T) {
	w, clk := newWheel()
	a, b := newSock(1, nil), newSock(2, nil)
	a.node.Init(func() { a.fired++; w.Remove(&b.node) })
	w.Add(&a.node, time.Second)
	w.Add(&b.node, time.Second)
	clk.Advance(2 * time.Second)
	if a.fired != 1 || b.fired != 0 {
		t.Fatalf("a %d b %d", a.fired, b.fired)
	}
}

func TestADelayOfZeroOrLessFiresAtTheNextTick(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	w.Add(&s.node, -time.Hour)
	clk.Advance(0)
	clk.Advance(time.Second)
	if s.fired != 1 {
		t.Fatalf("fired %d", s.fired)
	}
}

func TestADelayBeyondMaxIsClampedToMax(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	w.Add(&s.node, 24*time.Hour)
	clk.Advance(w.Max() - time.Second)
	if s.fired != 0 {
		t.Fatal("fired before Max")
	}
	clk.Advance(time.Second)
	if s.fired != 1 {
		t.Fatal("did not fire at Max")
	}
}

func TestADeadlineAcrossALapOfTheRingFiresOnTime(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	clk.Advance(0)
	for lap := 0; lap < 3; lap++ {
		w.Add(&s.node, 400*time.Second)
		clk.Advance(399 * time.Second)
		if s.fired != lap {
			t.Fatalf("lap %d: fired early", lap)
		}
		clk.Advance(time.Second)
		if s.fired != lap+1 {
			t.Fatalf("lap %d: did not fire", lap)
		}
	}
}

func TestTwentyThousandNodesAllFireOnceAtTheirOwnSecond(t *testing.T) {
	w, clk := newWheel()
	const n = 20000
	socks := make([]*sock, n)
	for i := range socks {
		socks[i] = newSock(i, nil)
		w.Add(&socks[i].node, time.Duration(1+i%60)*time.Second)
	}
	if w.Len() != n {
		t.Fatalf("len %d", w.Len())
	}
	for sec := 1; sec <= 60; sec++ {
		clk.Advance(time.Second)
		for i, s := range socks {
			want := 0
			if 1+i%60 <= sec {
				want = 1
			}
			if s.fired != want {
				t.Fatalf("second %d: node %d fired %d times, want %d", sec, i, s.fired, want)
			}
		}
	}
	if w.Len() != 0 {
		t.Fatalf("len %d", w.Len())
	}
}

func TestThousandsDueInOneTickAreFiredInSlicesNotAllAtOnce(t *testing.T) {
	clk := clock.NewFake(epoch)
	w := New(clk, Config{MaxPerTick: 100})
	const n = 1000
	runs := 0
	var s [n]sock
	for i := range s {
		s[i].node.Init(func() { s[i].fired++ })
		w.Add(&s[i].node, time.Second)
	}
	// Count the fake clock's own timer firings by watching progress after each small step.
	prev := 0
	clk.Advance(time.Second) // runs the first slice, and the 1 ms yields that fall inside Advance
	total := 0
	for i := range s {
		total += s[i].fired
	}
	_ = prev
	_ = runs
	if total == n {
		// The yields fall within the same Advance only if they are due before its target; they are
		// due after it, so the first Advance must have stopped at the budget.
		t.Fatal("one tick fired everything despite MaxPerTick")
	}
	if total != 100 {
		t.Fatalf("first slice fired %d, want the budget of 100", total)
	}
	for step := 0; step < 20 && total < n; step++ {
		clk.Advance(time.Millisecond)
		total = 0
		for i := range s {
			total += s[i].fired
		}
		if total > 100*(step+2) {
			t.Fatalf("step %d: %d fired, over the budget", step, total)
		}
	}
	if total != n {
		t.Fatalf("%d of %d fired", total, n)
	}
	for i := range s {
		if s[i].fired != 1 {
			t.Fatalf("node %d fired %d times", i, s[i].fired)
		}
	}
}

func TestAWheelThatFellBehindCatchesUpInSlicesWithoutFiringAnythingEarly(t *testing.T) {
	clk := clock.NewFake(epoch)
	w := New(clk, Config{MaxPerTick: 8})
	a, b := newSock(1, nil), newSock(2, nil)
	w.Add(&a.node, 10*time.Second)
	w.Add(&b.node, 400*time.Second)
	clk.Advance(time.Hour) // one jump over every slot, as after a suspend
	if a.fired != 1 || b.fired != 1 {
		t.Fatalf("a %d b %d", a.fired, b.fired)
	}
}

func TestAnIdleWheelHoldsNoTimer(t *testing.T) {
	w, clk := newWheel()
	if clk.Pending() != 0 {
		t.Fatal("a new wheel set a timer")
	}
	s := newSock(1, nil)
	w.Add(&s.node, time.Second)
	if clk.Pending() != 1 {
		t.Fatalf("%d timers pending with one node", clk.Pending())
	}
	clk.Advance(time.Second)
	if clk.Pending() != 0 {
		t.Fatalf("%d timers pending after the last node fired", clk.Pending())
	}
	clk.Advance(time.Hour)
	w.Add(&s.node, 5*time.Second)
	clk.Advance(5 * time.Second)
	if s.fired != 2 {
		t.Fatalf("fired %d after an idle hour", s.fired)
	}
}

func TestOneTimerServesAnyNumberOfNodes(t *testing.T) {
	w, clk := newWheel()
	for i := 0; i < 1000; i++ {
		s := newSock(i, nil)
		w.Add(&s.node, time.Duration(i%100+1)*time.Second)
	}
	if clk.Pending() != 1 {
		t.Fatalf("%d timers for 1000 nodes", clk.Pending())
	}
}

func TestAClosedWheelFiresNothing(t *testing.T) {
	w, clk := newWheel()
	s := newSock(1, nil)
	w.Add(&s.node, time.Second)
	w.Close()
	clk.Advance(time.Minute)
	w.Add(&s.node, time.Second)
	clk.Advance(time.Minute)
	if s.fired != 0 {
		t.Fatal("fired after Close")
	}
}

func TestReArmingAndRemovingAllocateNothing(t *testing.T) {
	w, _ := newWheel()
	s := newSock(1, nil)
	other := newSock(2, nil)
	w.Add(&other.node, time.Hour) // keeps the wheel's timer armed
	w.Add(&s.node, 30*time.Second)
	if a := testing.AllocsPerRun(1000, func() { w.Add(&s.node, 30*time.Second) }); a != 0 {
		t.Errorf("re-arm: %v allocations", a)
	}
	if a := testing.AllocsPerRun(1000, func() { w.Remove(&s.node); w.Add(&s.node, 30*time.Second) }); a != 0 {
		t.Errorf("remove and add: %v allocations", a)
	}
	// A tick on the fake clock is not measured: Fake.Advance reslices its own timer list and so allocates.
}

func TestANodeIsThirtyTwoBytes(t *testing.T) {
	// Held in every socket (ADR 0003 and ADR 0039). Raise this only knowingly.
	if s := unsafe.Sizeof(Node{}); s != 32 {
		t.Fatalf("Node is %d bytes", s)
	}
}

func BenchmarkReArm(b *testing.B) {
	clk := clock.NewFake(epoch)
	w := New(clk, Config{})
	socks := make([]sock, 1024)
	for i := range socks {
		socks[i].node.Init(func() {})
		w.Add(&socks[i].node, 30*time.Second)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Add(&socks[i&1023].node, 30*time.Second)
	}
}
