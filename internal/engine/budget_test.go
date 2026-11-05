package engine

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
	"time"
)

// steadyAllocs drives n keep-alive GETs over memconn and returns the mallocs per request.
func steadyAllocs(t *testing.T, h handlerFunc) float64 {
	return steadyAllocsFor(t, h, get)
}

// steadyAllocsFor is steadyAllocs for the request text req.
func steadyAllocsFor(t *testing.T, h handlerFunc, reqText string) float64 {
	r := newRig(t, h, nil)
	c := r.dial()
	req := []byte(reqText)
	buf := make([]byte, 512)
	var want int
	do := func() {
		c.c.Write(req)
		if want == 0 {
			n, _ := c.c.Read(buf)
			want = n
			return
		}
		io.ReadFull(c.c, buf[:want])
	}
	do()
	for i := 0; i < 50; i++ {
		do()
	}
	return testing.AllocsPerRun(2000, do)
}

func TestAKeepAliveGETWithNoBodyAllocatesTwiceAndTheContextsDoneChannelIsNotMadeUnlessAsked(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	h := func(ex *Exchange) {
		_ = ex.Slab().Copy(ex.Head().Path)
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		*r.Body() = append(*r.Body(), "0123456789abcdefghij"...)
	}
	got := steadyAllocs(t, h)
	t.Logf("allocations per request, Done not called: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (slab + Ctx)", got)
	}
}

func TestABodyThatArrivesWithItsHeadIsReadWithOneAllocation(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	h := func(ex *Exchange) {
		b, err := ex.ReadBody(1<<20, 5*time.Second)
		if err != nil || string(b) != `{"name":"ada","age":36}` {
			ex.Respond(500)
			return
		}
		_ = ex.Slab().Copy(ex.Head().Path)
		okHandler("0123456789abcdefghij")(ex)
	}
	body := `{"name":"ada","age":36}`
	got := steadyAllocsFor(t, h, "POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+body)
	t.Logf("allocations per request, body with its head: %v", got)
	if got != 2 {
		t.Fatalf("allocs/request = %v, want exactly 2 (slab holding head copies and body, Ctx)", got)
	}
}

func TestCallingDoneOverMemconnCostsTheArmingHelperAndNotTheEpollPath(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	got := steadyAllocs(t, func(ex *Exchange) {
		ex.Context().Done()
		okHandler("0123456789abcdefghij")(ex)
	})
	t.Logf("allocations per request, Done called, memconn helper goroutine: %v", got)
}

func heapAndStack() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64() + s[1].Value.Uint64()
}

// settle is how long the test waits after opening connections: past moveAfter, so every idle
// connection has parked, or where it cannot park waits on a fresh goroutine's stack (ADR 0005).
const settle = moveAfter + 200*time.Millisecond

// TestIdleClientHelper is the client half of the idle-memory test, run as a child process so
// the client's own sockets are not counted against the server.
func TestIdleClientHelper(t *testing.T) {
	addr := os.Getenv("ENGINE_IDLE_ADDR")
	if addr == "" {
		t.Skip("helper for TestTheMarginalHeapAndStackOfAnIdleLoopbackConnection")
	}
	var conns []net.Conn
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		n, _ := strconv.Atoi(strings.TrimPrefix(in.Text(), "open "))
		for i := 0; i < n; i++ {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				fmt.Println("error", err)
				return
			}
			c.Write([]byte(get))
			b := make([]byte, 1024)
			got := 0
			for !strings.HasSuffix(string(b[:got]), "ok") {
				m, err := c.Read(b[got:])
				if err != nil {
					fmt.Println("error", err)
					return
				}
				got += m
			}
			conns = append(conns, c)
		}
		fmt.Println("ready")
	}
}

func TestTheMarginalHeapAndStackOfAnIdleLoopbackConnection(t *testing.T) {
	if runtime.GOOS != "linux" || testing.Short() || raceEnabled {
		t.Skip("Linux, not -short, not -race")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	srv := New(okHandler("ok"), Options{Logger: quietLogger()})
	go srv.Serve(ln)
	defer srv.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestIdleClientHelper$")
	cmd.Env = append(os.Environ(), "ENGINE_IDLE_ADDR="+ln.Addr().String())
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
	open(200) // warm the pool, the poller and the maps
	time.Sleep(settle)
	a := heapAndStack()
	open(1000)
	time.Sleep(settle)
	b := heapAndStack()
	open(1000)
	time.Sleep(settle)
	c := heapAndStack()
	t.Logf("heap+stack at 200, 1200, 2200 connections: %d, %d, %d bytes", a, b, c)
	t.Logf("marginal bytes per idle connection: %d (200 to 1200), %d (1200 to 2200); ADR 0005's prototype: about 2900",
		(b-a)/1000, (c-b)/1000)
	if per := (c - b) / 1000; per > 4500 {
		t.Fatalf("an idle connection costs %d B heap+stack, over the 4500 B ceiling", per)
	}
}

func TestCallingDoneOverLoopbackTCPWithTheWatcherCostsTheDoneChannelAndNothingElse(t *testing.T) {
	if raceEnabled || runtime.GOOS != "linux" {
		t.Skip("Linux, not -race")
	}
	_, c := tcpPair(t, false, func(ex *Exchange) {
		ex.Context().Done()
		okHandler("0123456789abcdefghij")(ex)
	})
	req := []byte(get)
	buf := make([]byte, 512)
	want := 0
	do := func() {
		c.Write(req)
		if want == 0 {
			want, _ = c.Read(buf)
			return
		}
		io.ReadFull(c, buf[:want])
	}
	for i := 0; i < 100; i++ {
		do()
	}
	got := testing.AllocsPerRun(2000, do)
	t.Logf("allocations per request over TCP, Done called, epoll watcher: %v", got)
	if got > 3 {
		t.Fatalf("allocs/request = %v, want at most 3 (slab, Ctx, done channel)", got)
	}
}
