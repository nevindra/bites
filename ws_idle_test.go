package bites_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
)

// The idle-memory gate of a WebSocket (ADR 0039, ADR 0033): connections that have done the
// handshake, been given their Socket and Inbox, and sit with nothing to read. What is measured is
// the marginal heap plus stack of one such connection through the whole app.

type idleWSIn struct{ Path struct{ ID int64 } }
type idleWSOut struct {
	Text string `json:"text"`
}
type idleWSMsg struct{ Text string }

const idleWSRequest = "GET /ws/7 HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"

// wsIdleCeiling is the gate of ADR 0039 for a WebSocket kept on a goroutine (WithoutParking): set
// from the first measurement (docs/research/ws-gate) with the headroom the other gates carry.
const wsIdleCeiling uint64 = 4700

// wsParkedIdleCeiling is the gate of a parked WebSocket (ADR 0039, ADR 0005): 20% over the
// 1,466 B this test measured when parking was built (docs/research/parking).
const wsParkedIdleCeiling uint64 = 1800

var wsLastStacks uint64

func wsHeapAndStack() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}}
	metrics.Read(s)
	wsLastStacks = s[1].Value.Uint64()
	return s[0].Value.Uint64() + s[1].Value.Uint64()
}

// TestWSIdleClientHelper is the client half, run as a child process so the client's own sockets
// are not counted against the server.
func TestWSIdleClientHelper(t *testing.T) {
	addr := os.Getenv("BITES_WSIDLE_ADDR")
	if addr == "" {
		t.Skip("helper for TestTheMarginalHeapAndStackOfAnIdleWebSocketStaysUnderItsCeiling")
	}
	var conns []net.Conn
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		n, _ := strconv.Atoi(strings.TrimPrefix(in.Text(), "open "))
		for range n {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				fmt.Println("error", err)
				return
			}
			c.Write([]byte(idleWSRequest))
			br := bufio.NewReader(c)
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					fmt.Println("error", err)
					return
				}
				if line == "\r\n" {
					break
				}
			}
			conns = append(conns, c)
		}
		fmt.Println("ready")
	}
}

func TestTheMarginalHeapAndStackOfAnIdleWebSocketStaysUnderItsCeiling(t *testing.T) {
	t.Run("parked", func(t *testing.T) { wsIdleMarginal(t, false, wsParkedIdleCeiling) })
	t.Run("without parking", func(t *testing.T) { wsIdleMarginal(t, true, wsIdleCeiling) })
}

func wsIdleMarginal(t *testing.T, noPark bool, ceiling uint64) {
	if runtime.GOOS != "linux" || testing.Short() || bites.RaceEnabled {
		t.Skip("Linux, not -short, not -race")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	opts := []bites.Option{bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	if noPark {
		opts = append(opts, bites.WithoutParking())
	}
	app := bites.New(opts...)
	app.WebSocket("/ws/{id}", func(_ context.Context, _ idleWSIn, s *bites.Socket[idleWSOut]) (bites.Inbox[idleWSMsg], error) {
		return bites.Inbox[idleWSMsg]{OnMessage: func(context.Context, idleWSMsg) error { return nil }}, nil
	})
	go app.Serve(ln)
	defer app.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestWSIdleClientHelper$")
	cmd.Env = append(os.Environ(), "BITES_WSIDLE_ADDR="+ln.Addr().String())
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	out := bufio.NewScanner(stdout)
	open := func(n int) {
		fmt.Fprintf(stdin, "open %d\n", n)
		for out.Scan() {
			if out.Text() == "ready" {
				return
			}
			if strings.HasPrefix(out.Text(), "error") {
				t.Fatal(out.Text())
			}
		}
		t.Fatal("client died")
	}
	const settle = 300 * time.Millisecond
	open(200)
	time.Sleep(settle)
	a := wsHeapAndStack()
	open(1000)
	time.Sleep(settle)
	b := wsHeapAndStack()
	sb := wsLastStacks
	open(1000)
	time.Sleep(settle)
	c := wsHeapAndStack()
	t.Logf("stacks alone: %d per connection (marginal 1200 to 2200)", (int64(wsLastStacks)-int64(sb))/1000)
	t.Logf("heap+stack at 200, 1200, 2200 WebSockets: %d, %d, %d bytes", a, b, c)
	t.Logf("marginal bytes per idle WebSocket through the whole app: %d (200 to 1200), %d (1200 to 2200)", (b-a)/1000, (c-b)/1000)
	if per := (c - b) / 1000; per > ceiling {
		t.Fatalf("an idle WebSocket costs %d B heap+stack, over the %d B ceiling", per, ceiling)
	}
}
