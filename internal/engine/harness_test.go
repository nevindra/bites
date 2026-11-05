package engine

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
)

var epoch = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

type handlerFunc func(ex *Exchange)

func (f handlerFunc) Serve(ex *Exchange) { f(ex) }

// rig is a Server on a memconn listener with a fake clock.
type rig struct {
	t    *testing.T
	clk  *clock.Fake
	ln   *memconn.Listener
	srv  *Server
	done chan error
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newRig(t *testing.T, h handlerFunc, mod func(*Options)) *rig {
	t.Helper()
	clk := clock.NewFake(epoch)
	o := Options{Clock: clk, Logger: quietLogger(), HeadTimeout: 7 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second}
	if mod != nil {
		mod(&o)
	}
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	r.srv = New(h, o)
	go func() { r.done <- r.srv.Serve(r.ln) }()
	t.Cleanup(func() { r.srv.Close() })
	return r
}

type client struct {
	t      *testing.T
	c      net.Conn
	br     *bufio.Reader
	isHead bool
}

func (r *rig) dial() *client {
	r.t.Helper()
	c, err := r.ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		r.t.Fatal(err)
	}
	return &client{t: r.t, c: c, br: bufio.NewReader(c)}
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := c.c.Write([]byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

type reply struct {
	status  int
	headers map[string]string
	body    string
	err     error
}

// read reads one response, failing the test if none arrives in five real seconds.
func (c *client) read() reply {
	c.t.Helper()
	ch := make(chan reply, 1)
	go func() { ch <- c.readNow() }()
	select {
	case rp := <-ch:
		if rp.err != nil {
			c.t.Fatalf("reading a response: %v", rp.err)
		}
		return rp
	case <-time.After(5 * time.Second):
		c.t.Fatal("no response in 5 s")
		return reply{}
	}
}

func (c *client) readNow() reply {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return reply{err: err}
	}
	f := strings.Fields(line)
	if len(f) < 2 {
		return reply{err: io.ErrUnexpectedEOF}
	}
	rp := reply{headers: map[string]string{}}
	rp.status, _ = strconv.Atoi(f[1])
	for {
		l, err := c.br.ReadString('\n')
		if err != nil {
			return reply{err: err}
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			break
		}
		k, v, _ := strings.Cut(l, ": ")
		rp.headers[strings.ToLower(k)] = v
	}
	if n, ok := rp.headers["content-length"]; ok && !(rp.status < 200 || rp.status == 204 || rp.status == 304) {
		cl, _ := strconv.Atoi(n)
		if c.isHead {
			return rp
		}
		b := make([]byte, cl)
		if _, err := io.ReadFull(c.br, b); err != nil {
			return reply{err: err}
		}
		rp.body = string(b)
	}
	return rp
}

// expectClosed fails unless the server closes the connection (EOF) within five real seconds.
func (c *client) expectClosed() {
	c.t.Helper()
	ch := make(chan error, 1)
	go func() { _, err := c.br.ReadByte(); ch <- err }()
	select {
	case err := <-ch:
		if err != io.EOF {
			c.t.Fatalf("want EOF, got %v", err)
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("connection not closed in 5 s")
	}
}

// waitNext waits until the earliest fake timer is at want, i.e. the server has armed the
// deadline the test is about to expire.
func waitNext(t *testing.T, clk *clock.Fake, want time.Time) {
	t.Helper()
	waitFor(t, func() bool { n, ok := clk.Next(); return ok && n.Equal(want) }, "the server to arm a deadline")
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// lockedBuf is a log sink safe for the server's goroutines.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func okHandler(body string) handlerFunc {
	return func(ex *Exchange) {
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		*r.Body() = append(*r.Body(), body...)
	}
}

func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
