// The memory a Topic costs, for a design that counts them (a Topic per User, per room, per
// conversation): an idle Topic, the first Socket of a Topic, and each further Socket in one. Run:
//
//	go test -run TestTopicCost -v -count=1
package topiccost

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type frame struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

func heap() uint64 {
	var m runtime.MemStats
	for range 3 {
		runtime.GC()
	}
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// sockets opens n sockets and returns their server ends.
func sockets(t *testing.T, n int) []*bites.Socket[frame] {
	got := make(chan *bites.Socket[frame], n)
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[frame]) (bites.Inbox[frame], error) {
		got <- s
		return bites.Inbox[frame]{}, nil
	})
	srv := bitetest.Start(t, app)
	out := make([]*bites.Socket[frame], 0, n)
	for range n {
		srv.DialWS(bitetest.WSRequest{Target: "/ws", Timeout: 30 * time.Second})
		out = append(out, <-got)
	}
	return out
}

func TestTopicCost(t *testing.T) {
	const n = 2000
	socks := sockets(t, n)
	bus := bitetest.NewBroker()
	bites.NewTopic[frame]("warm", bus, bites.TopicOptions{}) // the payload plans, compiled once per type

	var keep [][]*bites.Topic[frame]
	for round := range 5 {
		h0 := heap()
		zero := make([]*bites.Topic[frame], n)
		for i := range zero {
			zero[i] = new(bites.Topic[frame])
		}
		h1 := heap()
		brokered := make([]*bites.Topic[frame], n)
		for i := range brokered {
			brokered[i] = bites.NewTopic[frame](fmt.Sprintf("user.%d.%d", round, i), bus, bites.TopicOptions{})
		}
		h2 := heap()
		for i, s := range socks { // a Topic per User: each socket is the first of its own Topic
			brokered[i].Subscribe(s)
		}
		h3 := heap()
		room := bites.NewTopic[frame](fmt.Sprintf("room.%d", round), bus, bites.TopicOptions{})
		room.Subscribe(socks[0])
		h4 := heap()
		for _, s := range socks[1:] { // one room every socket is in
			room.Subscribe(s)
		}
		h5 := heap()
		per := func(a, b uint64, k int) float64 { return float64(int64(b)-int64(a)) / float64(k) }
		runtime.KeepAlive(keep)
		t.Logf("round %d: zero Topic %.0f B, idle NewTopic %.0f B, first Socket of a brokered Topic %.0f B, each further Socket in a Topic %.0f B",
			round, per(h0, h1, n), per(h1, h2, n), per(h2, h3, n), per(h4, h5, n-1))
		for i, s := range socks {
			brokered[i].Unsubscribe(s)
			room.Unsubscribe(s)
		}
		keep = append(keep, zero, brokered, []*bites.Topic[frame]{room})
	}
}

// TestTopicsKeyCost is what a key of a Topics costs while it has a Socket here, what is left once
// its last Socket leaves, and what a SubscribeUnless function adds to a Socket of a Topic.
func TestTopicsKeyCost(t *testing.T) {
	const n = 2000
	socks := sockets(t, n)
	bus := bitetest.NewBroker()
	bites.NewTopic[frame]("warm", bus, bites.TopicOptions{})
	skip := func(frame) bool { return false }
	for round := range 5 {
		users := bites.NewTopics[frame](fmt.Sprintf("user.%d.", round), bus, bites.TopicsOptions{})
		h0 := heap()
		for i, s := range socks {
			users.Subscribe(fmt.Sprint(i), s)
		}
		h1 := heap()
		for i, s := range socks {
			users.Unsubscribe(fmt.Sprint(i), s)
		}
		h2 := heap()
		var room bites.Topic[frame]
		for _, s := range socks {
			room.Subscribe(s)
		}
		h3 := heap()
		for _, s := range socks {
			room.SubscribeUnless(s, skip)
		}
		h4 := heap()
		per := func(a, b uint64) float64 { return float64(int64(b)-int64(a)) / n }
		t.Logf("round %d: a key of a brokered Topics with its Socket %.0f B, left after its last Socket leaves %.0f B (keys %d), a SubscribeUnless function on a Socket of a Topic %.0f B",
			round, per(h0, h1), per(h0, h2), users.Keys(), per(h3, h4))
		for _, s := range socks {
			room.Unsubscribe(s)
		}
	}
}
