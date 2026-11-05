package bitetest_test

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/bitetest"
)

func TestABrokerDeliversOnThePublishersGoroutineUntilAsyncIsCalled(t *testing.T) {
	b := bitetest.NewBroker()
	var got []string
	b.Subscribe("n", func(p []byte) { got = append(got, string(p)) }, func() {})
	b.Publish(t.Context(), "n", []byte("a"))
	b.Publish(t.Context(), "n", []byte("b"))
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %q by the time Publish returned, want %q", got, want)
	}
}

func TestAnAsyncBrokerReturnsFromPublishBeforeASubscriptionHasTheMessageAndDeliversInOrder(t *testing.T) {
	b := bitetest.NewBroker()
	b.Async()
	gate := make(chan struct{})
	got := make(chan string, 3)
	b.Subscribe("n", func(p []byte) { <-gate; got <- string(p) }, func() {})
	done := make(chan struct{})
	go func() {
		for _, p := range []string{"a", "b", "c"} {
			b.Publish(t.Context(), "n", []byte(p))
		}
		close(done)
	}()
	select {
	case <-done: // the deliver function is still blocked on the gate
	case <-time.After(5 * time.Second):
		t.Fatal("Publish waited for the subscription to deliver")
	}
	close(gate)
	for _, want := range []string{"a", "b", "c"} {
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("delivered %q, want %q: the order of the Publish calls is not kept", g, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q was not delivered", want)
		}
	}
}

func TestAnAsyncBrokerDeliversNothingMoreToASubscriptionThatStopped(t *testing.T) {
	b := bitetest.NewBroker()
	b.Async()
	gate := make(chan struct{})
	got := make(chan string, 2)
	stop := b.Subscribe("n", func(p []byte) { <-gate; got <- string(p) }, func() {})
	b.Publish(t.Context(), "n", []byte("a"))
	b.Publish(t.Context(), "n", []byte("b"))
	stop()
	close(gate)
	time.Sleep(20 * time.Millisecond)
	if len(got) > 1 { // "a" was already in flight, "b" was queued behind it
		t.Fatalf("a stopped subscription was delivered %d messages after it stopped", len(got)-1)
	}
}

func TestSettleReturnsOnlyAfterEveryDeliveryQueuedBeforeItHasRunOnAnAsyncBroker(t *testing.T) {
	b := bitetest.NewBroker()
	b.Async()
	gate := make(chan struct{})
	var mu sync.Mutex
	var got []string
	b.Subscribe("n", func(p []byte) {
		<-gate
		mu.Lock()
		got = append(got, string(p))
		mu.Unlock()
	}, func() {})
	b.Publish(t.Context(), "n", []byte("a"))
	b.Publish(t.Context(), "n", []byte("b"))
	settled := make(chan struct{})
	go func() { b.Settle(); close(settled) }()
	close(gate)
	<-settled
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered %q when Settle returned, want %q", got, want)
	}
}

func TestSettleOnASynchronousBrokerReturnsAtOnce(t *testing.T) {
	b := bitetest.NewBroker()
	b.Subscribe("n", func([]byte) {}, func() {})
	b.Publish(t.Context(), "n", []byte("a"))
	done := make(chan struct{})
	go func() { b.Settle(); close(done) }()
	<-done
}

func TestSettleWaitsForTheDeliveriesAChainOfDeliveriesPublishesInTurn(t *testing.T) {
	b := bitetest.NewBroker()
	b.Async()
	var mu sync.Mutex
	var got []string
	b.Subscribe("first", func(p []byte) {
		b.Publish(t.Context(), "second", p)
	}, func() {})
	b.Subscribe("second", func(p []byte) {
		mu.Lock()
		got = append(got, string(p))
		mu.Unlock()
	}, func() {})
	b.Publish(t.Context(), "first", []byte("x"))
	b.Settle()
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the second hop had delivered %q when Settle returned, want %q", got, want)
	}
}

func TestSettleCalledFromInsideAnAsyncCallbackPanicsNamingTheFix(t *testing.T) {
	b := bitetest.NewBroker()
	b.Async()
	msg := make(chan any, 1)
	b.Subscribe("n", func([]byte) {
		defer func() { msg <- recover() }()
		b.Settle()
	}, func() {})
	b.Publish(t.Context(), "n", []byte("a"))
	b.Settle()
	got, _ := (<-msg).(string)
	if !strings.Contains(got, "from inside a subscription's callback") || !strings.Contains(got, "test's goroutine") {
		t.Fatalf("Settle in a callback panicked with %q, want a message naming the callback and the fix", got)
	}
}
