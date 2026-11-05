package bitetest_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type in struct{ Path struct{ ID int64 } }
type msg struct{ Text string }
type out struct {
	Text string `json:"text"`
}

func startEcho(t *testing.T, closed chan bites.CloseInfo) (*bitetest.Server, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk))
	app.WebSocket("/rooms/{id}", func(_ context.Context, _ in, s *bites.Socket[out]) (bites.Inbox[msg], error) {
		return bites.Inbox[msg]{
			OnMessage: func(_ context.Context, m msg) error { return s.Send(out{Text: m.Text}) },
			OnClose: func(i bites.CloseInfo) {
				if closed != nil {
					closed <- i
				}
			},
		}, nil
	})
	return bitetest.Start(t, app), clk
}

func TestAWebSocketClientSendsTextAndJSONAndReadsTheEchoAsTextAndJSON(t *testing.T) {
	srv, _ := startEcho(t, nil)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/7"})
	if w.Response.Status != 101 || w.Response.Get("upgrade") != "websocket" {
		t.Fatalf("handshake answer %d %v", w.Response.Status, w.Response.Header)
	}
	w.SendText(`{"text":"hello"}`)
	if got := w.ReadText(); got != `{"text":"hello"}` {
		t.Fatalf("got %q", got)
	}
	w.SendJSON(map[string]string{"text": "again"})
	var o out
	w.ReadJSON(&o)
	if o.Text != "again" {
		t.Fatalf("got %+v", o)
	}
}

func TestAMessageSentInFragmentsIsOneMessageToTheServer(t *testing.T) {
	srv, _ := startEcho(t, nil)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.SendFragments(`{"te`, `xt":"in `, `parts"}`)
	if got := w.ReadText(); got != `{"text":"in parts"}` {
		t.Fatalf("got %q", got)
	}
}

func TestACloseWithAStatusIsAnsweredWithTheSameStatusAndThenTheConnectionEnds(t *testing.T) {
	closed := make(chan bites.CloseInfo, 1)
	srv, _ := startEcho(t, closed)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.Close(3001, "bye")
	w.ExpectClose(3001)
	w.ExpectEOF()
	if _, err := w.TryRead(0); !errors.Is(err, io.EOF) {
		t.Fatalf("after the end: %v", err)
	}
	select {
	case i := <-closed:
		if i.Status != 3001 || i.Reason != "bye" || !i.Remote {
			t.Fatalf("%+v", i)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnClose was not called")
	}
}

func TestAClientAnswersPingsUnlessToldNotToAndTheServerClosesAtTheSecondMissedPong(t *testing.T) {
	srv, clk := startEcho(t, nil)
	alive := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	for range 5 {
		clk.Advance(30 * time.Second)
		alive.ReadPing()
		// A message after the pong makes sure the server has read it before the clock moves.
		alive.SendText(`{"text":"alive"}`)
		alive.ReadText()
	}

	dead := srv.DialWS(bitetest.WSRequest{Target: "/rooms/2", NoAutoPong: true})
	for range 2 {
		clk.Advance(30 * time.Second)
		dead.ReadPing()
	}
	clk.Advance(30 * time.Second)
	dead.ExpectClose(1008)
}

func TestAPingFromTheClientIsAnsweredWithAPongOfTheSamePayload(t *testing.T) {
	srv, _ := startEcho(t, nil)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.Ping("hello")
	if m := w.Read(); m.Kind != bitetest.Pong || m.Text() != "hello" {
		t.Fatalf("got a %s %q", m.Kind, m.Data)
	}
}

func TestARefusedHandshakeComesBackAsTheHTTPAnswerWithNoClient(t *testing.T) {
	srv, _ := startEcho(t, nil)
	w, resp := srv.TryDialWS(bitetest.WSRequest{Target: "/rooms/abc"})
	if w != nil || resp.Status != 400 {
		t.Fatalf("got a client %v and %d %s", w != nil, resp.Status, resp.Body)
	}
	w, resp = srv.TryDialWS(bitetest.WSRequest{Target: "/nowhere"})
	if w != nil || resp.Status != 404 {
		t.Fatalf("got a client %v and %d", w != nil, resp.Status)
	}
}

func TestBinaryMessagesAndTheLeaveOfAClientWithoutACloseAreSentAndSeen(t *testing.T) {
	closed := make(chan bites.CloseInfo, 1)
	srv, _ := startEcho(t, closed)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	w.SendBinary([]byte{1, 2, 3}) // a JSON route closes a binary message with 1003
	w.ExpectClose(1003)
	w.Leave()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("OnClose was not called")
	}
}

type camelMsg struct{ ItemName string }

func TestSendJSONWritesTheRoutesOwnTypeUnderTheAppsNamingPolicy(t *testing.T) {
	for name, opts := range map[string][]bites.Option{"SnakeCase": nil, "CamelCase": {bites.WithNaming(bites.CamelCase)}} {
		app := bites.New(append([]bites.Option{bites.WithClock(bitetest.NewClock())}, opts...)...)
		app.WebSocket("/echo", func(_ context.Context, _ struct{}, s *bites.Socket[camelMsg]) (bites.Inbox[camelMsg], error) {
			return bites.Inbox[camelMsg]{OnMessage: func(_ context.Context, m camelMsg) error { return s.Send(m) }}, nil
		})
		w := bitetest.Start(t, app).DialWS(bitetest.WSRequest{Target: "/echo"})
		w.SendJSON(camelMsg{ItemName: "lamp"})
		var got camelMsg
		w.ReadJSON(&got)
		if got.ItemName != "lamp" {
			t.Errorf("%s: echoed %+v", name, got)
		}
	}
}

func TestQuietPassesForASocketTheTopicDidNotReachAndFailsNamingAMessageThatArrived(t *testing.T) {
	var members bites.Topic[out]
	f := &fakeTB{TB: t}
	app := bites.New()
	app.WebSocket("/rooms/{id}", func(_ context.Context, r in, s *bites.Socket[out]) (bites.Inbox[msg], error) {
		if r.Path.ID == 1 {
			members.Subscribe(s)
		}
		return bites.Inbox[msg]{}, nil
	})
	srv := bitetest.Start(f, app)
	member := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	outsider := srv.DialWS(bitetest.WSRequest{Target: "/rooms/2"})
	for members.Len() != 1 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	members.Publish(out{Text: "private"})
	outsider.Quiet()
	if f.failed {
		t.Fatalf("Quiet failed for a socket that was sent nothing: %s", f.msg)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Quiet took %v: it waited a real interval", d)
	}
	f.run(func(testing.TB) { member.Quiet() })
	if !f.failed || !strings.Contains(f.msg, `the server sent a text message, want nothing: "{\"text\":\"private\"}"`) {
		t.Fatalf("Quiet on a socket with an unread message: failed %v, %q", f.failed, f.msg)
	}
}

func TestPendingReturnsTheDataMessagesQueuedInOrderAndRemovesThemFromWhatIsLeftToRead(t *testing.T) {
	var all bites.Topic[out]
	app := bites.New()
	app.WebSocket("/rooms/{id}", func(_ context.Context, _ in, s *bites.Socket[out]) (bites.Inbox[msg], error) {
		all.Subscribe(s)
		return bites.Inbox[msg]{}, nil
	})
	srv := bitetest.Start(t, app)
	w := srv.DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	for all.Len() != 1 {
		time.Sleep(time.Millisecond)
	}
	if got := w.Pending(); got != nil {
		t.Fatalf("Pending with nothing queued returned %v, want nil", got)
	}
	all.Publish(out{Text: "one"})
	all.Publish(out{Text: "two"})
	got := w.Pending()
	if len(got) != 2 || got[0].Text() != `{"text":"one"}` || got[1].Text() != `{"text":"two"}` {
		t.Fatalf("Pending returned %v, want both messages in the order sent", got)
	}
	var m out
	if err := got[1].JSON(&m); err != nil || m.Text != "two" {
		t.Fatalf("a Message from Pending decoded as %+v, %v", m, err)
	}
	w.Quiet() // they were removed: nothing is left to read
	all.Publish(out{Text: "three"})
	w.ReadJSON(&m)
	if m.Text != "three" {
		t.Fatalf("the next read was %q, want three", m.Text)
	}
}

func TestPendingReturnsAClosePastTheDataBeforeItAsTheLastMessage(t *testing.T) {
	sockets := make(chan *bites.Socket[out], 1)
	app := bites.New()
	app.WebSocket("/rooms/{id}", func(_ context.Context, _ in, s *bites.Socket[out]) (bites.Inbox[msg], error) {
		sockets <- s
		return bites.Inbox[msg]{}, nil
	})
	w := bitetest.Start(t, app).DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	s := <-sockets
	if err := s.Send(out{Text: "last"}); err != nil {
		t.Fatal(err)
	}
	s.Close(4000, "done")
	got := w.Pending()
	if len(got) != 2 || got[0].Kind != bitetest.Text || got[1].Kind != bitetest.Close || got[1].Code != 4000 || got[1].Reason != "done" {
		t.Fatalf("Pending returned %+v, want the data message and then the Close 4000 done", got)
	}
}

func TestQuietPassesTooEarlyForAFrameAnAsyncBrokerHasNotDeliveredAndAReadOfTheExpectedFrameFixesIt(t *testing.T) {
	gate := make(chan struct{})
	bus := &gatedBroker{Broker: bitetest.NewBroker(), gate: gate}
	bus.Async()
	topic := bites.NewTopic[out]("room.1", bus, bites.TopicOptions{})
	app := bites.New()
	app.WebSocket("/rooms/{id}", func(_ context.Context, _ in, s *bites.Socket[out]) (bites.Inbox[msg], error) {
		topic.Subscribe(s)
		return bites.Inbox[msg]{}, nil
	})
	w := bitetest.Start(t, app).DialWS(bitetest.WSRequest{Target: "/rooms/1"})
	for topic.Len() != 1 {
		time.Sleep(time.Millisecond)
	}
	if err := topic.Publish(out{Text: "late"}); err != nil {
		t.Fatal(err)
	}
	w.Quiet() // passes: the broker has not handed the frame to the Topic, so the server has queued nothing
	close(gate)
	var m out
	w.ReadJSON(&m) // the fix: read the frame the test expects, and assert absence only after it
	if m.Text != "late" {
		t.Fatalf("read %q, want the frame Quiet passed too early for", m.Text)
	}
	if got := w.Pending(); got != nil {
		t.Fatalf("Pending after the frame returned %v, want nil", got)
	}
}

// gatedBroker holds each delivery until gate is closed, so a test chooses when the broker's
// goroutine hands a payload over.
type gatedBroker struct {
	*bitetest.Broker
	gate chan struct{}
}

func (g *gatedBroker) Subscribe(name string, deliver func([]byte), gap func()) func() {
	return g.Broker.Subscribe(name, func(p []byte) { <-g.gate; deliver(p) }, gap)
}

type clientFrame struct {
	Join  bites.Opt[joinMsg]
	Leave bites.Opt[joinMsg]
}

func (clientFrame) Discriminator() string { return "type" }

type joinMsg struct{ RoomID int64 }
type joinedMsg struct{ RoomID int64 }
type leftMsg struct{ RoomID int64 }

type serverFrame struct {
	Joined bites.Opt[joinedMsg]
	Left   bites.Opt[leftMsg]
}

func (serverFrame) Discriminator() string { return "type" }

func TestAWebSocketMessageReadsIntoATaggedUnionWithTheAppsCodecWhateverVariantArrives(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk))
	app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[serverFrame]) (bites.Inbox[clientFrame], error) {
		return bites.Inbox[clientFrame]{OnMessage: func(_ context.Context, f clientFrame) error {
			j, _ := f.Join.Get()
			var out serverFrame
			if j.RoomID == 0 {
				out.Left.Set(leftMsg{RoomID: 9})
			} else {
				out.Joined.Set(joinedMsg{RoomID: j.RoomID})
			}
			return s.Send(out)
		}}, nil
	})
	w := bitetest.Start(t, app).DialWS(bitetest.WSRequest{Target: "/ws"})
	w.SendJSON(clientFrame{Join: bites.Some(joinMsg{RoomID: 7})})
	var f serverFrame
	w.ReadJSON(&f)
	if j, ok := f.Joined.Get(); !ok || j.RoomID != 7 {
		t.Errorf("joined: %+v", f)
	}
	if _, ok := f.Left.Get(); ok {
		t.Errorf("the other variant is set: %+v", f)
	}
	w.SendText(`{"type":"join","room_id":0}`)
	var g serverFrame
	w.ReadJSON(&g)
	if l, ok := g.Left.Get(); !ok || l.RoomID != 9 {
		t.Errorf("left: %+v", g)
	}
	w.SendText(`{"type":"join","room_id":3}`)
	var m map[string]any
	w.ReadJSON(&m)
	if m["type"] != "joined" || m["room_id"] != float64(3) {
		t.Errorf("map: %v", m)
	}
}
