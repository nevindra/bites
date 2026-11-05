package bites_test

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/internal/ws"
)

// The test client of the WebSocket tests: it speaks the handshake and the frames by hand, so a
// test sees exactly the bytes the server writes.

type wsc struct {
	t      *testing.T
	c      net.Conn
	br     *bufio.Reader
	status int
	hdr    map[string]string
	body   string
}

const wsKey = "dGhlIHNhbXBsZSBub25jZQ=="

// handshake sends the upgrade request for target over c and reads the answer; extra are header
// lines ("Origin: https://x"). A request line other than a valid handshake is made by naming the
// method and leaving out headers with raw.
func handshake(t *testing.T, c net.Conn, target string, extra ...string) *wsc {
	t.Helper()
	req := "GET " + target + " HTTP/1.1\r\nHost: test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + wsKey + "\r\nSec-WebSocket-Version: 13\r\n"
	for _, e := range extra {
		req += e + "\r\n"
	}
	return rawHandshake(t, c, req+"\r\n")
}

func rawHandshake(t *testing.T, c net.Conn, req string) *wsc {
	t.Helper()
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	w := &wsc{t: t, c: c, br: bufio.NewReader(c), hdr: map[string]string{}}
	line := w.line()
	f := strings.Fields(line)
	if len(f) < 2 {
		t.Fatalf("no status line: %q", line)
	}
	w.status, _ = strconv.Atoi(f[1])
	for l := w.line(); l != ""; l = w.line() {
		k, v, _ := strings.Cut(l, ": ")
		w.hdr[strings.ToLower(k)] = v
	}
	if n, ok := w.hdr["content-length"]; ok && w.status != 101 && !strings.HasPrefix(req, "HEAD ") {
		cl, _ := strconv.Atoi(n)
		b := make([]byte, cl)
		if _, err := io.ReadFull(w.br, b); err != nil {
			t.Fatal(err)
		}
		w.body = string(b)
	}
	return w
}

func (w *wsc) line() string {
	w.t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := w.br.ReadString('\n'); ch <- res{s, err} }()
	select {
	case r := <-ch:
		if r.err != nil {
			w.t.Fatalf("reading the handshake answer: %v", r.err)
		}
		return strings.TrimRight(r.s, "\r\n")
	case <-time.After(5 * time.Second):
		w.t.Fatal("no handshake answer in 5 s")
		return ""
	}
}

// memWS opens a WebSocket on an in-memory server and requires the 101.
func memWS(t *testing.T, srv *bitetest.Server, target string, extra ...string) *wsc {
	t.Helper()
	w := handshake(t, srv.Dial("203.0.113.9:4000"), target, extra...)
	if w.status != 101 {
		t.Fatalf("handshake answered %d %v %s", w.status, w.hdr, w.body)
	}
	return w
}

// write sends one masked frame.
func (w *wsc) write(fin bool, op ws.Opcode, p []byte) {
	w.t.Helper()
	if _, err := w.c.Write(ws.AppendClientFrame(nil, fin, op, p, [4]byte{9, 8, 7, 6})); err != nil {
		w.t.Fatalf("writing a frame: %v", err)
	}
}

func (w *wsc) text(s string) { w.t.Helper(); w.write(true, ws.OpText, []byte(s)) }

func (w *wsc) closeWith(code uint16, reason string) {
	w.t.Helper()
	p := binary.BigEndian.AppendUint16(nil, code)
	w.write(true, ws.OpClose, append(p, reason...))
}

type frameRead struct {
	op  ws.Opcode
	p   []byte
	err error
}

// try reads one server frame, or the error that ended the connection.
func (w *wsc) try() frameRead {
	w.t.Helper()
	ch := make(chan frameRead, 1)
	go func() { ch <- readFrame(w.br) }()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		w.t.Fatal("no frame in 5 s")
		return frameRead{}
	}
}

func readFrame(br *bufio.Reader) frameRead {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return frameRead{err: err}
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return frameRead{err: err}
		}
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return frameRead{err: err}
		}
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(br, p); err != nil {
		return frameRead{err: err}
	}
	return frameRead{op: ws.Opcode(h[0] & 0x0f), p: p}
}

// next reads one frame and fails the test if the connection ended instead.
func (w *wsc) next() (ws.Opcode, []byte) {
	w.t.Helper()
	r := w.try()
	if r.err != nil {
		w.t.Fatalf("the connection ended: %v", r.err)
	}
	return r.op, r.p
}

func (w *wsc) expectText(want string) {
	w.t.Helper()
	op, p := w.next()
	if op != ws.OpText || string(p) != want {
		w.t.Fatalf("got opcode %d %q, want text %q", op, p, want)
	}
}

// expectClose reads a Close frame and returns its status and reason.
func (w *wsc) expectClose(code uint16) string {
	w.t.Helper()
	op, p := w.next()
	for op == ws.OpPing { // a heartbeat that fell due while the test moved the clock
		op, p = w.next()
	}
	if op != ws.OpClose || len(p) < 2 {
		w.t.Fatalf("got opcode %d %q, want a Close with %d", op, p, code)
	}
	if got := binary.BigEndian.Uint16(p); got != code {
		w.t.Fatalf("closed with %d %q, want %d", got, p[2:], code)
	}
	return string(p[2:])
}

// expectEOF requires the connection to end, discarding any frames still in flight.
func (w *wsc) expectEOF() {
	w.t.Helper()
	for range 100 {
		r := w.try()
		if r.err != nil {
			return
		}
	}
	w.t.Fatal("the connection did not end")
}

// startWS serves an app over memory with a fake clock and a log the test can read.
func startWS(t *testing.T, register func(app *bites.App), opts ...bites.Option) (*bitetest.Server, *bitetest.Clock, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	clk := bitetest.NewClock()
	all := append([]bites.Option{bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil)))}, opts...)
	app := bites.New(all...)
	register(app)
	return bitetest.Start(t, app), clk, lb
}

// waitFor polls cond for up to five real seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

var _ = fmt.Sprint

// passMoveAfter moves the clock past the engine's 100 ms wait on the goroutine that served the
// last frame, and waits for the connection to arm its deadline again on the fresh goroutine it
// moves to. A test that advances a long way in one step must do this first: a deadline armed
// while Advance is running is measured from a moment in the middle of it and can land after its
// end.
func passMoveAfter(t *testing.T, clk *bitetest.Clock) {
	t.Helper()
	before := clk.Pending()
	clk.Advance(150 * time.Millisecond)
	waitFor(t, "the connection to wait again on a fresh goroutine", func() bool { return clk.Pending() >= before })
}
