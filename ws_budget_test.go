package bites_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/ws"
)

// The allocation rows of a WebSocket (ADR 0003, ADR 0047): a message read costs the one buffer it
// is copied into, a Send costs the frame it builds, a Publish costs one frame whatever the number
// of subscribers. Each is measured on a connection that has finished its handshake, over loopback
// TCP (the engine writes inline there) and through memory (the flusher writes there).

type budgetWSOut struct {
	Text string `json:"text"`
}

type budgetWSMsg struct{ Text string }

type budgetWSRig struct {
	c       net.Conn
	br      *bufio.Reader
	sock    *bites.Socket[budgetWSOut]
	got     atomic.Int64
	closeCh chan bites.CloseInfo
}

// budgetWS serves one WebSocket route on a connection of the kind rig makes and returns the
// client end after the 101. The handler counts each message it reads in got.
func budgetWS(t *testing.T, rig func(*testing.T, func(*bites.App)) net.Conn) *budgetWSRig {
	t.Helper()
	r := &budgetWSRig{closeCh: make(chan bites.CloseInfo, 1)}
	ready := make(chan struct{})
	c := rig(t, func(a *bites.App) {
		a.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[budgetWSOut]) (bites.Inbox[budgetWSMsg], error) {
			r.sock = s
			close(ready)
			return bites.Inbox[budgetWSMsg]{
				OnMessage: func(context.Context, budgetWSMsg) error { r.got.Add(1); return nil },
				OnClose:   func(info bites.CloseInfo) { r.closeCh <- info },
			}, nil
		})
	})
	r.c = c
	req := "GET /ws HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	r.br = bufio.NewReader(c)
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "HTTP/1.1 ") && !strings.HasPrefix(line, "HTTP/1.1 101") {
			t.Fatalf("handshake answered %q", line)
		}
		if line == "\r\n" {
			break
		}
	}
	<-ready
	return r
}

// perMessage returns the mallocs per message the client sends on r.
func (r *budgetWSRig) perMessage() float64 {
	frame := ws.AppendClientFrame(nil, true, ws.OpText, []byte(`{"text":"hello"}`), [4]byte{1, 2, 3, 4})
	do := func() {
		want := r.got.Load() + 1
		r.c.Write(frame)
		for r.got.Load() < want {
			runtime.Gosched()
		}
	}
	for range 30 {
		do()
	}
	return testing.AllocsPerRun(300, do)
}

// drain reads, without allocating, every byte the server writes to the client, so a Send never
// waits on a full socket buffer.
func (r *budgetWSRig) drain() {
	buf := make([]byte, 64<<10)
	go func() {
		for {
			if _, err := r.br.Read(buf); err != nil {
				return
			}
		}
	}()
}

func TestAMessageReadOnAWebSocketCostsOneAllocationOverTCPAndThroughMemory(t *testing.T) {
	if bites.RaceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	for name, rig := range map[string]func(*testing.T, func(*bites.App)) net.Conn{"tcp": wsTCP, "memory": wsMem} {
		t.Run(name, func(t *testing.T) {
			r := budgetWS(t, rig)
			got := r.perMessage()
			t.Logf("allocations per message read, %s: %v (the buffer the message is copied into)", name, got)
			if got != 1 {
				t.Fatalf("allocs/message = %v, want exactly 1 (ADR 0003: the message's own buffer)", got)
			}
		})
	}
}

func TestASendOnAWebSocketWrittenInlineAllocatesNothing(t *testing.T) {
	if bites.RaceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	r := budgetWS(t, wsTCP)
	r.drain()
	v := budgetWSOut{Text: "hello"}
	for range 100 {
		r.sock.Send(v)
	}
	got := testing.AllocsPerRun(2000, func() { r.sock.Send(v) })
	t.Logf("allocations per Send over TCP, inline write: %v", got)
	if got != 0 {
		t.Fatalf("allocs/Send = %v, want 0: a frame written inline is built in a pooled buffer that is on the wire before Send returns (ADR 0003)", got)
	}
}

func TestAPublishToManySubscribersBuildsTheFrameOnceAndCostsTheSameForOneAsForAHundred(t *testing.T) {
	if bites.RaceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the constant fan-out cost is the inline write's, which is Linux's (ws_linux.go)")
	}
	measure := func(n int) float64 {
		topic := &bites.Topic[budgetWSOut]{}
		rigs := make([]*budgetWSRig, n)
		for i := range rigs {
			rigs[i] = budgetWS(t, wsTCP)
			rigs[i].drain()
			topic.Subscribe(rigs[i].sock)
		}
		v := budgetWSOut{Text: "hello"}
		for range 20 {
			topic.Publish(v)
		}
		// Every socket is idle past the engine's 100 ms and has parked (ADR 0005), with each test
		// server's poller made, before the count: a Publish reaches idle subscribers, and what
		// parking costs once per connection is not a Publish's.
		time.Sleep(300 * time.Millisecond)
		return testing.AllocsPerRun(500, func() { topic.Publish(v) })
	}
	one, ten, many, more := measure(1), measure(10), measure(100), measure(300)
	t.Logf("allocations per Publish over TCP: 1 subscriber %v, 10 %v, 100 %v, 300 %v", one, ten, many, more)
	if one != 1 || ten != 1 {
		t.Fatalf("allocs/Publish is %v for 1 subscriber and %v for 10, want exactly 1 (ADR 0047: the frame)", one, ten)
	}
	if many > 5 || more > 5 {
		t.Fatalf("allocs/Publish is %v for 100 subscribers and %v for 300, want at most 5 (the frame, and the closure of each of at most four fan-out workers): the frame is built once and the fan-out costs a constant, not a count per subscriber (ADR 0047)", many, more)
	}
}

func wsQuiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func wsMem(t *testing.T, register func(*bites.App)) net.Conn {
	t.Helper()
	app := bites.New(bites.WithClock(clock.NewFake(time.Now())), bites.WithLogger(wsQuiet()))
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wsTCP(t *testing.T, register func(*bites.App)) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	app := bites.New(bites.WithLogger(wsQuiet()))
	register(app)
	go app.Serve(ln)
	t.Cleanup(func() { app.Close() })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// ADR 0081: a Topic with a Broker pays for the broker's encoding on top of the frame, and a Topic
// without one pays nothing more than before (the test above holds it at exactly 1).
func TestABrokeredPublishCostsTheEncodingTheDeliveryAndTheFrameAndNothingPerSubscriber(t *testing.T) {
	if bites.RaceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the constant fan-out cost is the inline write's, which is Linux's (ws_linux.go)")
	}
	measure := func(n int) float64 {
		topic := bites.NewTopic[budgetWSOut]("room.1", bitetest.NewBroker(), bites.TopicOptions{})
		for range n {
			r := budgetWS(t, wsTCP)
			r.drain()
			topic.Subscribe(r.sock)
		}
		v := budgetWSOut{Text: "hello"}
		for range 20 {
			topic.Publish(v)
		}
		time.Sleep(300 * time.Millisecond)
		return testing.AllocsPerRun(500, func() { topic.Publish(v) })
	}
	one, ten := measure(1), measure(10)
	t.Logf("allocations per brokered Publish over TCP: 1 subscriber %v, 10 %v", one, ten)
	if one > 8 || ten != one {
		t.Fatalf("allocs/brokered Publish is %v for 1 subscriber and %v for 10, want at most 8 and the same for both (ADR 0081)", one, ten)
	}
}
