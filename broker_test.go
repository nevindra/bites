package bites_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// brokerApp registers a feed route that subscribes each socket to topic.
func brokerApp(t *testing.T, topic *bites.Topic[wsOut]) *wsc {
	t.Helper()
	srv, _, _ := startWS(t, func(app *bites.App) {
		app.WebSocket("/feed", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
			topic.Subscribe(s)
			return bites.Inbox[wsMsg]{}, nil
		})
	})
	c := memWS(t, srv, "/feed")
	waitFor(t, "the socket to subscribe", func() bool { return topic.Len() == 1 })
	return c
}

func TestAMessagePublishedOnABrokeredTopicReachesTheOwnSocketsOnceAndOthersInstancesToo(t *testing.T) {
	bus := bitetest.NewBroker()
	a := bites.NewTopic[wsOut]("room.1", bus, bites.TopicOptions{})
	b := bites.NewTopic[wsOut]("room.1", bus.Peer(), bites.TopicOptions{})
	ca, cb := brokerApp(t, a), brokerApp(t, b)
	if err := a.Publish(wsOut{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(wsOut{Text: "two"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*wsc{ca, cb} {
		c.expectText(`{"text":"one"}`)
		c.expectText(`{"text":"two"}`) // the next frame is two: the own publish was not delivered twice
	}
}

func TestABrokeredTopicListensWhileItHasASocketAndOnlyThen(t *testing.T) {
	bus := bitetest.NewBroker()
	topic := bites.NewTopic[wsOut]("room.2", bus, bites.TopicOptions{})
	if bus.Subscriptions("room.2") != 0 {
		t.Fatal("a topic with no socket listens")
	}
	c := brokerApp(t, topic)
	if bus.Subscriptions("room.2") != 1 {
		t.Fatalf("a topic with a socket has %d subscriptions, want 1", bus.Subscriptions("room.2"))
	}
	c.closeWith(1000, "")
	c.expectEOF()
	waitFor(t, "the last socket to leave and the topic to stop listening", func() bool { return bus.Subscriptions("room.2") == 0 })
}

func TestABrokerThatFailsMakesPublishReturnItsErrorAndDeliversNothingLocally(t *testing.T) {
	bus := bitetest.NewBroker()
	br := bus
	topic := bites.NewTopic[wsOut]("room.3", br, bites.TopicOptions{})
	c := brokerApp(t, topic)
	boom := errors.New("connection refused")
	bus.Fail(boom)
	err := topic.Publish(wsOut{Text: "lost"})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), `Topic "room.3"`) {
		t.Fatalf("Publish returned %v, want an error wrapping the broker's and naming the topic", err)
	}
	bus.Fail(nil)
	topic.Publish(wsOut{Text: "next"})
	c.expectText(`{"text":"next"}`) // the failed one was not delivered locally either
}

func TestAMessageOverMaxPayloadIsAnErrorNamingBothSizesAndIsNeverSent(t *testing.T) {
	bus := bitetest.NewBroker()
	topic := bites.NewTopic[wsOut]("room.4", bus, bites.TopicOptions{MaxPayload: 64})
	c := brokerApp(t, topic)
	err := topic.Publish(wsOut{Text: strings.Repeat("x", 100)})
	var pe *bites.PayloadError
	if !errors.As(err, &pe) || pe.Name != "room.4" || pe.Size != 111 || pe.Limit != 64 {
		t.Fatalf("Publish returned %#v, want a *PayloadError of room.4, 111 bytes, limit 64", err)
	}
	if !strings.Contains(err.Error(), `Topic "room.4"`) || !strings.Contains(err.Error(), "111 bytes, over the limit of 64") {
		t.Fatalf("the message of the error does not say the name, the size and the limit: %v", err)
	}
	topic.Publish(wsOut{Text: "ok"})
	c.expectText(`{"text":"ok"}`)
}

func TestAMessageOverTheBrokersOwnLimitIsTheBrokersPayloadErrorAndAFailureIsNot(t *testing.T) {
	bus := bitetest.NewBroker()
	bus.Limit(50)
	topic := bites.NewTopic[wsOut]("room.8", bus, bites.TopicOptions{})
	err := topic.Publish(wsOut{Text: strings.Repeat("x", 100)})
	var pe *bites.PayloadError
	if !errors.As(err, &pe) || pe.Name != "room.8" || pe.Limit != 50 || pe.Size != 111 {
		t.Fatalf("Publish returned %#v, want the broker's *PayloadError with limit 50", err)
	}
	bus.Limit(0)
	bus.Fail(errors.New("down"))
	if err := topic.Publish(wsOut{Text: "x"}); err == nil || errors.As(err, &pe) {
		t.Fatalf("a broker that is down returned %v, which must not be a *PayloadError", err)
	}
}

func TestAPayloadFromTheBrokerThatDoesNotDecodeIsDroppedAndLoggedAndTheNextOneArrives(t *testing.T) {
	bus := bitetest.NewBroker()
	var logs bytes.Buffer
	topic := bites.NewTopic[wsOut]("room.5", bus, bites.TopicOptions{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	c := brokerApp(t, topic)
	other := bus.Peer()
	other.Publish(context.Background(), "room.5", []byte(`not json`))
	other.Publish(context.Background(), "room.5", []byte(`{"text":"after"}`))
	c.expectText(`{"text":"after"}`)
	if !strings.Contains(logs.String(), "did not decode") || !strings.Contains(logs.String(), "room.5") {
		t.Fatalf("the drop was not logged with the topic: %s", logs.String())
	}
}

func TestAGapCallsOnGapAndPublishLocalReachesOnlyThisInstancesSockets(t *testing.T) {
	bus := bitetest.NewBroker()
	var gaps atomic.Int32
	var a, b *bites.Topic[wsOut]
	a = bites.NewTopic[wsOut]("room.6", bus, bites.TopicOptions{OnGap: func() {
		gaps.Add(1)
		a.PublishLocal(wsOut{Text: "reload"})
	}})
	b = bites.NewTopic[wsOut]("room.6", bus.Peer(), bites.TopicOptions{Log: wsQuiet()})
	ca, cb := brokerApp(t, a), brokerApp(t, b)
	bus.Gap()
	ca.expectText(`{"text":"reload"}`)
	b.Publish(wsOut{Text: "next"})
	cb.expectText(`{"text":"next"}`) // b's socket was not sent the reload that a's OnGap made
	ca.expectText(`{"text":"next"}`)
	if gaps.Load() != 1 {
		t.Fatalf("OnGap ran %d times, want 1", gaps.Load())
	}
}

func TestAPanicInOnGapIsRecoveredAndLogged(t *testing.T) {
	bus := bitetest.NewBroker()
	var logs bytes.Buffer
	topic := bites.NewTopic[wsOut]("room.7", bus, bites.TopicOptions{
		OnGap: func() { panic("bad") }, Log: slog.New(slog.NewTextHandler(&logs, nil))})
	brokerApp(t, topic)
	bus.Gap()
	if !strings.Contains(logs.String(), "OnGap panicked") {
		t.Fatalf("the panic was not logged: %s", logs.String())
	}
}

func TestATopicWithNoGapHandlerLogsTheGapAtWarn(t *testing.T) {
	bus := bitetest.NewBroker()
	var logs bytes.Buffer
	topic := bites.NewTopic[wsOut]("room.8", bus, bites.TopicOptions{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	brokerApp(t, topic)
	bus.Gap()
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "OnGap") {
		t.Fatalf("a gap with no handler is not logged: %s", logs.String())
	}
}

func TestAPublishLocalOnATopicWithoutABrokerIsPublish(t *testing.T) {
	var topic bites.Topic[wsOut]
	c := brokerApp(t, &topic)
	topic.PublishLocal(wsOut{Text: "here"})
	c.expectText(`{"text":"here"}`)
}

type evict struct{ Key string }

func TestAFunctionListeningToABrokeredTopicHearsWhatEveryInstancePublishesWithNoSocket(t *testing.T) {
	bus := bitetest.NewBroker()
	a := bites.NewTopic[evict]("cache.sessions", bus, bites.TopicOptions{})
	b := bites.NewTopic[evict]("cache.sessions", bus.Peer(), bites.TopicOptions{})
	var heardA, heardB []string
	stopA := a.Listen(func(e evict) { heardA = append(heardA, e.Key) })
	defer b.Listen(func(e evict) { heardB = append(heardB, e.Key) })()
	if bus.Subscriptions("cache.sessions") != 2 {
		t.Fatalf("two topics with a function each have %d subscriptions, want 2", bus.Subscriptions("cache.sessions"))
	}
	if err := a.Publish(evict{Key: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(evict{Key: "s2"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(heardA, ",") != "s1,s2" || strings.Join(heardB, ",") != "s1,s2" {
		t.Fatalf("instance a heard %v and b heard %v, want s1,s2 each, the own publish once", heardA, heardB)
	}
	stopA()
	stopA() // a second stop does nothing
	b.Publish(evict{Key: "s3"})
	if len(heardA) != 2 || bus.Subscriptions("cache.sessions") != 1 {
		t.Fatalf("after stop, a heard %v and the bus has %d subscriptions", heardA, bus.Subscriptions("cache.sessions"))
	}
}

func TestAGapReachesOnGapOfATopicThatHasOnlyFunctions(t *testing.T) {
	bus := bitetest.NewBroker()
	cache := map[string]bool{"s1": true, "s2": true}
	topic := bites.NewTopic[evict]("cache.users", bus, bites.TopicOptions{OnGap: func() { clear(cache) }})
	defer topic.Listen(func(e evict) { delete(cache, e.Key) })()
	topic.Publish(evict{Key: "s1"})
	if cache["s1"] || !cache["s2"] {
		t.Fatalf("the evicted key is still cached, or the other is gone: %v", cache)
	}
	bus.Gap()
	if len(cache) != 0 {
		t.Fatalf("a gap did not clear the cache through OnGap: %v", cache)
	}
}

func TestAFunctionListeningToAZeroTopicHearsPublishBesideItsSocketsAndAPanicIsLogged(t *testing.T) {
	var topic bites.Topic[wsOut]
	c := brokerApp(t, &topic)
	var heard []string
	defer topic.Listen(func(m wsOut) {
		if m.Text == "bad" {
			panic("listener broke")
		}
		heard = append(heard, m.Text)
	})()
	if topic.Len() != 1 {
		t.Fatalf("Len counts the function: %d", topic.Len())
	}
	topic.Publish(wsOut{Text: "bad"})
	topic.Publish(wsOut{Text: "good"})
	c.expectText(`{"text":"bad"}`)
	c.expectText(`{"text":"good"}`)
	if strings.Join(heard, ",") != "good" {
		t.Fatalf("the function heard %v, want good after a panic on bad", heard)
	}
}

func TestListenWithANilFunctionPanicsSayingWhatToGive(t *testing.T) {
	var topic bites.Topic[evict]
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "fn is nil") {
			t.Fatalf("Listen(nil) recovered %v", r)
		}
	}()
	topic.Listen(nil)
}

type presenceFrame struct {
	Kind  string
	Note  bites.Opt[string]
	Reply bites.Opt[bites.Null[int]]
	Gone  bites.Opt[int]
}

func TestAnOptAndANullCrossTheBrokerAsTheyReachASocketAndAnUnknownMemberIsReadPast(t *testing.T) {
	bus := bitetest.NewBroker()
	a := bites.NewTopic[presenceFrame]("frames", bus, bites.TopicOptions{})
	var heard []presenceFrame
	defer a.Listen(func(f presenceFrame) { heard = append(heard, f) })()
	if err := a.Publish(presenceFrame{Kind: "edit", Note: bites.Some("hi"), Reply: bites.Some(bites.Null[int]{})}); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 1 {
		t.Fatalf("heard %d messages", len(heard))
	}
	f := heard[0]
	note, hasNote := f.Note.Get()
	reply, hasReply := f.Reply.Get()
	if f.Kind != "edit" || !hasNote || note != "hi" || !hasReply || !reply.IsNull() || f.Gone.IsSet() {
		t.Fatalf("the frame came back as %#v: an Opt or a Null was lost on the way", f)
	}
	// An instance of a later release that added a field: this one still reads the message.
	b := bites.NewTopic[struct{ Kind, Added string }]("frames", bus.Peer(), bites.TopicOptions{})
	if err := b.Publish(struct{ Kind, Added string }{"new", "x"}); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 2 || heard[1].Kind != "new" {
		t.Fatalf("a message with a member this type has not was dropped: %#v", heard)
	}
}

type note struct {
	Ref  string
	Text string
}

func TestResolveTurnsTheReferenceEachInstanceIsDeliveredIntoTheMessageItsSocketsGet(t *testing.T) {
	bus := bitetest.NewBroker()
	stored := map[string]string{"n1": strings.Repeat("long ", 3000)} // what the database holds
	var logs bytes.Buffer
	a := bites.NewTopic[note]("notes", bus, bites.TopicOptions{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	a.Resolve(func(n note) (note, bool) {
		if n.Ref == "boom" {
			panic("the database is gone")
		}
		text, ok := stored[n.Ref]
		return note{Ref: n.Ref, Text: text}, ok
	})
	var heard []note
	defer a.Listen(func(n note) { heard = append(heard, n) })()
	a.Publish(note{Ref: "n1"})
	a.Publish(note{Ref: "missing"})
	a.Publish(note{Ref: "boom"})
	a.PublishLocal(note{Ref: "local", Text: "as is"})
	if len(heard) != 2 || heard[0].Text != stored["n1"] || heard[1].Text != "as is" {
		t.Fatalf("heard %d messages: want the resolved n1, then the local one as published", len(heard))
	}
	if !strings.Contains(logs.String(), "Topic.Resolve panicked") || !strings.Contains(logs.String(), "topic=notes") {
		t.Fatalf("the panic in Resolve was not logged with the topic: %s", logs.String())
	}
}

func TestResolveIsRefusedTwiceOnAZeroTopicAndWithNil(t *testing.T) {
	try := func(f func()) (msg string) {
		defer func() { msg, _ = recover().(string) }()
		f()
		return ""
	}
	brokered := bites.NewTopic[note]("notes", bitetest.NewBroker(), bites.TopicOptions{})
	id := func(n note) (note, bool) { return n, true }
	brokered.Resolve(id)
	var zero bites.Topic[note]
	for want, f := range map[string]func(){
		"called twice":           func() { brokered.Resolve(id) },
		"Topic without a broker": func() { zero.Resolve(id) },
		"fn is nil":              func() { bites.NewTopic[note]("other", bitetest.NewBroker(), bites.TopicOptions{}).Resolve(nil) },
	} {
		if got := try(f); !strings.Contains(got, want) {
			t.Errorf("want a panic saying %q, got %q", want, got)
		}
	}
}

type framed struct {
	Kind  string `json:"kind"`
	Note  *note  `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

func TestAPointerAndAnOmitemptyMemberOfASocketsTypeCrossTheBrokerAsTheyWereSent(t *testing.T) {
	topic := bites.NewTopic[framed]("framed", bitetest.NewBroker(), bites.TopicOptions{})
	var heard []framed
	defer topic.Listen(func(f framed) { heard = append(heard, f) })()
	topic.Publish(framed{Kind: "message", Note: &note{Ref: "n1", Text: "hi"}})
	topic.Publish(framed{Kind: "error", Error: "bad"})
	if len(heard) != 2 || heard[0].Note == nil || heard[0].Note.Text != "hi" || heard[0].Error != "" || heard[1].Note != nil || heard[1].Error != "bad" {
		t.Fatalf("the frames came back as %+v", heard)
	}
}

// signOut is the order to close a user's sockets on every instance.
type signOut struct{ Owner string }

func TestATopicListenedToByEveryInstanceClosesAUsersSocketsWhereverTheyAre(t *testing.T) {
	bus := bitetest.NewBroker()
	instance := func(b bites.Broker) (*bites.Topic[signOut], *wsc) {
		var owners bites.SocketOwners
		orders := bites.NewTopic[signOut]("signout", b, bites.TopicOptions{})
		t.Cleanup(orders.Listen(func(o signOut) { owners.Close(o.Owner, 4401, "signed out") }))
		srv, _, _ := startWS(t, func(app *bites.App) {
			app.WebSocket("/feed", func(_ context.Context, _ Empty, s *bites.Socket[wsOut]) (bites.Inbox[wsMsg], error) {
				return bites.Inbox[wsMsg]{}, owners.Add("ana", s)
			})
		})
		c := memWS(t, srv, "/feed")
		waitFor(t, "the socket to be added", func() bool { return owners.Len("ana") == 1 })
		return orders, c
	}
	a, ca := instance(bus)
	_, cb := instance(bus.Peer())
	if err := a.Publish(signOut{Owner: "ana"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*wsc{ca, cb} {
		if reason := c.expectClose(4401); reason != "signed out" {
			t.Fatalf("closed with %q", reason)
		}
	}
}

func TestTheBitetestBrokerKeepsTheContractOfEveryBroker(t *testing.T) {
	bitetest.TestBroker(t, bitetest.BrokerSuite{New: func(*testing.T) (bites.Broker, bites.Broker) {
		b := bitetest.NewBroker()
		return b, b.Peer()
	}})
}

func TestTheBitetestBrokerKeepsTheContractOfEveryBrokerWhenItDeliversAsync(t *testing.T) {
	bitetest.TestBroker(t, bitetest.BrokerSuite{New: func(*testing.T) (bites.Broker, bites.Broker) {
		b := bitetest.NewBroker()
		b.Async()
		return b, b.Peer()
	}})
}

// relayWS serves a route whose Sockets subscribe to topic, and returns a client of it.
func relayWS(t *testing.T, topic *bites.Topic[RelayFrame]) *bitetest.WS {
	t.Helper()
	srv := bitetest.Start(t, relayApp(func(app *bites.App) {
		app.WebSocket("/feed", func(_ context.Context, _ struct{}, s *bites.Socket[RelayFrame]) (bites.Inbox[RelayFrame], error) {
			topic.Subscribe(s)
			return bites.Inbox[RelayFrame]{}, nil
		})
	}, &logBuf{}))
	w := srv.DialWS(bitetest.WSRequest{Target: "/feed"})
	waitFor(t, "the socket to subscribe", func() bool { return topic.Len() == 1 })
	return w
}

func TestATopicWithAResolveDeliversTheResolvedValueWhenAServerOnlyReferenceWasPublished(t *testing.T) {
	bus := bitetest.NewBroker()
	topic := bites.NewTopic[RelayFrame]("relay.1", bus, bites.TopicOptions{})
	topic.Resolve(func(f RelayFrame) (RelayFrame, bool) {
		ref, ok := f.Ref.Get()
		if !ok {
			return f, true
		}
		var full RelayFrame
		full.Said.Set(Said{Text: "full " + ref.ID, By: "db"})
		return full, true
	})
	var mu sync.Mutex
	var heard []RelayFrame
	defer topic.Listen(func(f RelayFrame) { mu.Lock(); heard = append(heard, f); mu.Unlock() })()
	got := func() []RelayFrame { mu.Lock(); defer mu.Unlock(); return slices.Clone(heard) }
	w := relayWS(t, topic)
	var ref RelayFrame
	ref.Ref.Set(RefOnly{ID: "n1"})
	if err := topic.Publish(ref); err != nil {
		t.Fatal(err)
	}
	if got := w.ReadText(); got != `{"type":"said","text":"full n1","by":"db"}` {
		t.Fatalf("the socket got %s", got)
	}
	waitFor(t, "Listen to hear the message", func() bool { return len(got()) == 1 })
	if s, ok := got()[0].Said.Get(); !ok || s.Text != "full n1" {
		t.Fatalf("Listen heard %+v: want the resolved value", got())
	}
}

func TestATopicWithoutAResolveDropsAServerOnlyVariantForItsSocketsWithAWarnAndListenStillGetsIt(t *testing.T) {
	bus := bitetest.NewBroker()
	var logs logBuf
	topic := bites.NewTopic[RelayFrame]("relay.2", bus, bites.TopicOptions{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	var mu sync.Mutex
	var heard []RelayFrame
	defer topic.Listen(func(f RelayFrame) { mu.Lock(); heard = append(heard, f); mu.Unlock() })()
	got := func() []RelayFrame { mu.Lock(); defer mu.Unlock(); return slices.Clone(heard) }
	w := relayWS(t, topic)
	var ref, ok RelayFrame
	ref.Ref.Set(RefOnly{ID: "n1"})
	ok.Said.Set(Said{Text: "next"})
	if err := topic.Publish(ref); err != nil {
		t.Fatal(err)
	}
	if err := topic.Publish(ok); err != nil {
		t.Fatal(err)
	}
	if got := w.ReadText(); got != `{"type":"said","text":"next","by":""}` {
		t.Fatalf("the first frame the socket got is %s: the reference must not reach it", got)
	}
	waitFor(t, "Listen to hear both messages", func() bool { return len(got()) == 2 })
	if r, has := got()[0].Ref.Get(); !has || r.ID != "n1" {
		t.Fatalf("Listen heard %+v: want the reference too", got())
	}
	for _, want := range []string{"level=WARN", "topic=relay.2", "variant=ref", "Resolve"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("the Warn lacks %q: %s", want, logs.String())
		}
	}
}
