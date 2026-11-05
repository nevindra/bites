package bites_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// roomsApp registers /room/{id}, which subscribes each socket to the key of its room in rooms.
func roomsApp(t *testing.T, rooms *bites.Topics[wsOut]) *bitetest.Server {
	t.Helper()
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/room/{id}", func(_ context.Context, in struct{ Path struct{ ID string } }, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			return bites.Inbox[wsMsg]{}, rooms.Subscribe(in.Path.ID, s)
		})
	})
	return srv
}

func TestAKeyOfAZeroTopicsReachesOnlyItsSocketsAndIsDroppedWithItsLastOne(t *testing.T) {
	var rooms bites.Topics[wsOut]
	srv := roomsApp(t, &rooms)
	a, b := memWS(t, srv, "/room/1"), memWS(t, srv, "/room/2")
	waitFor(t, "both rooms to have their socket", func() bool { return rooms.Len("1") == 1 && rooms.Len("2") == 1 })
	if rooms.Keys() != 2 {
		t.Fatalf("Keys %d", rooms.Keys())
	}
	if err := rooms.Publish("1", wsOut{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	rooms.Publish("2", wsOut{Text: "two"})
	rooms.Publish("3", wsOut{Text: "nobody"}) // a key with no socket is no error
	a.expectText(`{"text":"one"}`)
	b.expectText(`{"text":"two"}`)
	a.closeWith(1000, "")
	a.expectEOF()
	waitFor(t, "the room whose last socket left to be dropped", func() bool { return rooms.Keys() == 1 })
	if rooms.Len("1") != 0 {
		t.Fatal("a dropped key still counts a socket")
	}
}

func TestPublishToAKeyReachesItsSocketsOnAnotherInstanceWhenThisOneHasNone(t *testing.T) {
	bus := bitetest.NewBroker()
	here := bites.NewTopics[wsOut]("room.", bus, bites.TopicsOptions{})
	there := bites.NewTopics[wsOut]("room.", bus.Peer(), bites.TopicsOptions{})
	c := memWS(t, roomsApp(t, there), "/room/7")
	waitFor(t, "the socket to subscribe", func() bool { return there.Len("7") == 1 })
	if here.Keys() != 0 || bus.Subscriptions("room.7") != 1 {
		t.Fatalf("the publishing instance holds %d keys; the bus has %d subscriptions", here.Keys(), bus.Subscriptions("room.7"))
	}
	if err := here.Publish("7", wsOut{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	c.expectText(`{"text":"hi"}`)
	c.closeWith(1000, "")
	c.expectEOF()
	waitFor(t, "the key to be dropped and stop listening", func() bool { return there.Keys() == 0 && bus.Subscriptions("room.7") == 0 })
}

func TestAKeyThatIsEmptyOrThatTheBrokerRefusesIsAnErrorAndSubscribesNothing(t *testing.T) {
	var local bites.Topics[wsOut]
	if err := local.Publish("", wsOut{}); err == nil || !strings.Contains(err.Error(), "the key is empty") {
		t.Fatalf("empty key: %v", err)
	}
	rooms := bites.NewTopics[wsOut]("room.", refusalBroker{reject: "the name is over 63 bytes"}, bites.TopicsOptions{})
	err := rooms.Publish("7", wsOut{})
	if err == nil || err.Error() != `bites: Topics "room.": the broker does not carry the key "7": the name is over 63 bytes` {
		t.Fatalf("a refused key: %v", err)
	}
	if err := rooms.Subscribe("7", nil); err == nil || rooms.Keys() != 0 {
		t.Fatalf("Subscribe with a refused key: %v, %d keys", err, rooms.Keys())
	}
}

func TestSubscribeUnlessAndResolveWorkOnEveryKeyOfAFamily(t *testing.T) {
	bus := bitetest.NewBroker()
	rooms := bites.NewTopics[wsOut]("room.", bus, bites.TopicsOptions{})
	rooms.Resolve(func(_ string, v wsOut) (wsOut, bool) { v.Text = strings.ToUpper(v.Text); return v, v.Text != "DROP" })
	joined := make(chan struct{}, 2)
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/room/{id}", func(_ context.Context, in wsIn, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			var err error
			if in.Path.ID == 2 {
				err = rooms.SubscribeUnless("9", s, func(v wsOut) bool { return v.Text == "MUTED" })
			} else {
				err = rooms.Subscribe("9", s)
			}
			joined <- struct{}{}
			return bites.Inbox[wsMsg]{}, err
		})
	})
	c1, c2 := memWS(t, srv, "/room/1"), memWS(t, srv, "/room/2")
	<-joined
	<-joined
	for _, m := range []string{"muted", "drop", "end"} {
		if err := rooms.Publish("9", wsOut{Text: m}); err != nil {
			t.Fatal(err)
		}
	}
	c1.expectText(`{"text":"MUTED"}`)
	c1.expectText(`{"text":"END"}`)
	c2.expectText(`{"text":"END"}`)
}

func TestResolveOfAFamilyIsGivenTheKeyTheMessageWasPublishedTo(t *testing.T) {
	bus := bitetest.NewBroker()
	rooms := bites.NewTopics[wsOut]("room.", bus, bites.TopicsOptions{})
	var seen []string
	rooms.Resolve(func(key string, v wsOut) (wsOut, bool) {
		seen = append(seen, key)
		v.Text = key + ":" + v.Text
		return v, true
	})
	srv := roomsApp(t, rooms)
	c1, c2 := memWS(t, srv, "/room/1"), memWS(t, srv, "/room/2")
	waitFor(t, "both rooms subscribed", func() bool { return rooms.Keys() == 2 })
	rooms.Publish("2", wsOut{Text: "hi"})
	rooms.Publish("1", wsOut{Text: "yo"})
	c2.expectText(`{"text":"2:hi"}`)
	c1.expectText(`{"text":"1:yo"}`)
	if len(seen) != 2 || seen[0] != "2" || seen[1] != "1" {
		t.Fatalf("the resolver was given the keys %q, want [2 1]", seen)
	}
}

func TestAPublishToAKeyOverMaxPayloadIsAPayloadErrorNamingTheNameTheSizeAndTheLimit(t *testing.T) {
	rooms := bites.NewTopics[wsOut]("room.", bitetest.NewBroker(), bites.TopicsOptions{MaxPayload: 64})
	err := rooms.Publish("7", wsOut{Text: strings.Repeat("x", 100)})
	var pe *bites.PayloadError
	if !errors.As(err, &pe) || pe.Name != "room.7" || pe.Size != 111 || pe.Limit != 64 {
		t.Fatalf("Publish returned %#v, want a *PayloadError of room.7, 111 bytes, limit 64", err)
	}
}

func TestAGapOfAFamilyCallsOnGapWithEachKeyThatHasASocket(t *testing.T) {
	bus := bitetest.NewBroker()
	var mu sync.Mutex
	var gaps []string
	var rooms *bites.Topics[wsOut]
	rooms = bites.NewTopics[wsOut]("room.", bus, bites.TopicsOptions{OnGap: func(key string) {
		mu.Lock()
		gaps = append(gaps, key)
		mu.Unlock()
		rooms.PublishLocal(key, wsOut{Text: "reload"})
	}})
	srv := roomsApp(t, rooms)
	c := memWS(t, srv, "/room/4")
	waitFor(t, "the socket to subscribe", func() bool { return rooms.Len("4") == 1 })
	bus.Gap()
	c.expectText(`{"text":"reload"}`)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(gaps, ",") != "4" {
		t.Fatalf("gaps %v", gaps)
	}
}

func TestSocketsJoiningAndLeavingOneKeyAtOnceLeaveNoKeyBehind(t *testing.T) {
	var rooms bites.Topics[wsOut]
	srv := roomsApp(t, &rooms)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			c := memWS(t, srv, "/room/1")
			c.closeWith(1000, "")
			c.expectEOF()
		})
	}
	wg.Wait()
	waitFor(t, "every socket to leave and the key to be dropped", func() bool { return rooms.Keys() == 0 })
	if err := rooms.Publish("1", wsOut{Text: "after"}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOnAZeroTopicsAndSubscribeUnlessWithNilPanicSayingWhatToGive(t *testing.T) {
	for _, tc := range []struct {
		do   func()
		want string
	}{
		{func() { var r bites.Topics[wsOut]; r.Resolve(func(_ string, v wsOut) (wsOut, bool) { return v, true }) }, "make the Topics with NewTopics"},
		{func() { var r bites.Topics[wsOut]; r.SubscribeUnless("1", nil, nil) }, "skip is nil"},
	} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), tc.want) {
					t.Errorf("recovered %v, want %q", r, tc.want)
				}
			}()
			tc.do()
		}()
	}
}
