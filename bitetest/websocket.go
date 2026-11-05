package bitetest

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/testcodec"
	"github.com/nevindra/bites/internal/ws"
)

// Kind is what a WebSocket Message is.
type Kind uint8

// The kinds of message a WebSocket client reads.
const (
	Text Kind = iota + 1
	Binary
	Ping
	Pong
	Close
)

func (k Kind) String() string {
	switch k {
	case Text:
		return "text"
	case Binary:
		return "binary"
	case Ping:
		return "ping"
	case Pong:
		return "pong"
	case Close:
		return "close"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Message is one thing the server sent: a whole text or binary message (fragments are joined), a
// ping or pong with its payload, or a Close with its status and reason.
type Message struct {
	// Kind is the kind of message: Text, Binary, Ping, Pong or Close.
	Kind Kind
	// Data is the payload of a text, binary, ping or pong message. For a Close it is the raw payload.
	Data []byte
	// Code and Reason are the status and reason of a Close; Code is 1005 when the server sent none.
	Code uint16
	// Reason is the reason the server sent with a Close.
	Reason string

	app *bites.App // the app that sent it, whose codec JSON reads with
}

// Text is Data as a string.
func (m Message) Text() string { return string(m.Data) }

// JSON decodes Data into v, a pointer, as the app's clients read it: with the app's codec and
// naming policy, an Opt, a Null and a tagged union included, and with the leniency and the
// fallback of Response.JSON.
func (m Message) JSON(v any) error { return decodeJSON(m.app, m.Data, v) }

// WSRequest is the handshake to send. Host is "test"; the key, version and upgrade headers are
// set by the client, and Header may add others ("Origin") or replace the Host.
type WSRequest struct {
	// Target is the path and query of the route.
	Target string
	// Header adds header fields, such as Origin, or replaces Host, which is "test". A name with
	// several values is sent as that many lines, as for Request.Header.
	Header map[string][]string
	// Remote is the client address the server sees; "203.0.113.9:4000" if empty.
	Remote string
	// NoAutoPong stops the client answering the server's pings. By default it answers each with a
	// pong of the same payload, as a browser does; a test of the missed-heartbeat close turns it off.
	NoAutoPong bool
	// Timeout is how long a read waits in real time for the server; five seconds if zero. The
	// server's own timeouts run on the App's clock and are moved with Clock.Advance, not by this.
	Timeout time.Duration
}

// WS is a WebSocket client over an in-memory connection, after a successful handshake. It reads
// on its own goroutine from the moment it is dialled, so a server that writes never waits on the
// test; a slow reader (ADR 0037) is tested with Server.Dial and frames written by hand instead.
// Its methods may be called from the test's goroutine only, except Send*, Ping, Pong and
// Close, which are safe to call concurrently.
type WS struct {
	tb       testing.TB
	app      *bites.App
	c        net.Conn
	in       chan frame
	timeout  time.Duration
	autoPong bool

	wmu sync.Mutex
	// ended is the error that ended the connection, set once the reader has stopped.
	ended error
	// Response is the 101 the server answered.
	Response Response
}

type frame struct {
	m   Message
	err error
}

// DialWS opens a WebSocket to r.Target and requires the 101; it fails the test with the answer
// otherwise. Use TryDialWS to test a refused handshake.
func (s *Server) DialWS(r WSRequest) *WS {
	s.tb.Helper()
	w, resp := s.TryDialWS(r)
	if w == nil {
		s.tb.Fatalf("bitetest: the handshake for %s answered %d %v %s", r.Target, resp.Status, resp.Header, resp.Body)
	}
	return w
}

// TryDialWS sends the handshake and returns the client when the server answered 101, or a nil
// client and the answer (a 400, 401, 403, 404, 405 or 426) when it did not.
func (s *Server) TryDialWS(r WSRequest) (*WS, Response) {
	s.tb.Helper()
	remote := r.Remote
	if remote == "" {
		remote = "203.0.113.9:4000"
	}
	c := s.Dial(remote)
	var keyRaw [16]byte
	rand.Read(keyRaw[:])
	key := base64.StdEncoding.EncodeToString(keyRaw[:])
	var b bytes.Buffer
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", r.Target)
	has := map[string]bool{}
	for k, vs := range r.Header {
		has[strings.ToLower(k)] = true
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	if !has["host"] {
		b.WriteString("Host: test\r\n")
	}
	fmt.Fprintf(&b, "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key)
	if _, err := c.Write(b.Bytes()); err != nil {
		s.tb.Fatalf("bitetest: writing the handshake: %v", err)
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	type result struct {
		resp Response
		br   *bufio.Reader
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		br := bufio.NewReader(c)
		resp, err := readResponse(br, false)
		ch <- result{resp, br, err}
	}()
	var res result
	select {
	case res = <-ch:
	case <-time.After(timeout):
		c.Close()
		s.tb.Fatalf("bitetest: no handshake answer in %v", timeout)
	}
	if res.err != nil {
		c.Close()
		s.tb.Fatalf("bitetest: reading the handshake answer: %v", s.explain(res.err))
	}
	res.resp.app = s.app
	if res.resp.Status != 101 {
		c.Close()
		return nil, res.resp
	}
	want := string(ws.AppendAccept(nil, []byte(key)))
	if got := res.resp.Get("Sec-WebSocket-Accept"); got != want {
		c.Close()
		s.tb.Fatalf("bitetest: Sec-WebSocket-Accept is %q, want %q", got, want)
	}
	w := &WS{tb: s.tb, app: s.app, c: c, in: make(chan frame, 64), timeout: timeout, autoPong: !r.NoAutoPong, Response: res.resp}
	s.tb.Cleanup(func() { c.Close() })
	go w.read(res.br)
	return w, res.resp
}

var errTimeout = errors.New("bitetest: no message in time")

// read parses the server's frames, joins fragments and sends whole messages to the test. A ping
// is answered here before it is delivered.
func (w *WS) read(br *bufio.Reader) {
	var msg []byte
	var kind Kind
	for {
		fin, op, p, err := readFrame(br)
		if err != nil {
			w.in <- frame{err: err}
			return
		}
		switch op {
		case ws.OpText, ws.OpBinary:
			kind = Text
			if op == ws.OpBinary {
				kind = Binary
			}
			msg = append(msg[:0], p...)
		case ws.OpContinuation:
			msg = append(msg, p...)
		case ws.OpPing:
			if w.autoPong {
				w.write(true, ws.OpPong, p)
			}
			w.in <- frame{m: Message{Kind: Ping, Data: p}}
			continue
		case ws.OpPong:
			w.in <- frame{m: Message{Kind: Pong, Data: p}}
			continue
		case ws.OpClose:
			m := Message{Kind: Close, Data: p, Code: 1005}
			if len(p) >= 2 {
				m.Code = binary.BigEndian.Uint16(p)
				m.Reason = string(p[2:])
			}
			w.in <- frame{m: m}
			continue
		}
		if fin {
			if kind == Text && !utf8.Valid(msg) {
				w.in <- frame{err: errors.New("bitetest: the server sent a text message that is not UTF-8")}
				return
			}
			w.in <- frame{m: Message{Kind: kind, Data: append([]byte(nil), msg...)}}
		}
	}
}

func readFrame(br *bufio.Reader) (fin bool, op ws.Opcode, p []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(br, h[:]); err != nil {
		return
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > 1<<30 {
		return false, 0, nil, errors.New("bitetest: a frame over 1 GiB")
	}
	p = make([]byte, n)
	if _, err = io.ReadFull(br, p); err != nil {
		return
	}
	return h[0]&0x80 != 0, ws.Opcode(h[0] & 0x0f), p, nil
}

func (w *WS) write(fin bool, op ws.Opcode, p []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	_, err := w.c.Write(ws.AppendClientFrame(nil, fin, op, p, [4]byte{9, 8, 7, 6}))
	return err
}

func (w *WS) send(op ws.Opcode, p []byte) {
	w.tb.Helper()
	if err := w.write(true, op, p); err != nil {
		w.tb.Fatalf("bitetest: writing a frame: %v", err)
	}
}

// SendText sends s as one text message.
func (w *WS) SendText(s string) { w.tb.Helper(); w.send(ws.OpText, []byte(s)) }

// SendBinary sends p as one binary message.
func (w *WS) SendBinary(p []byte) { w.tb.Helper(); w.send(ws.OpBinary, p) }

// SendJSON sends v as one text message, written as the app writes JSON: with its naming policy
// and its codec, so the route's own message type, with no json tags, arrives under the wire
// names the route reads ({"text":...} under SnakeCase, not {"Text":...}). A value the app's codec
// cannot write fails the test with the reason; send hand-written JSON with SendText instead.
func (w *WS) SendJSON(v any) {
	w.tb.Helper()
	b, err := testcodec.Encode(w.app, v)
	if err != nil {
		w.tb.Fatalf("bitetest: writing %T as the app writes JSON: %v", v, err)
	}
	w.send(ws.OpText, b)
}

// SendFragments sends one text message split over several frames, a fragment each.
func (w *WS) SendFragments(parts ...string) {
	w.tb.Helper()
	for i, p := range parts {
		op := ws.OpContinuation
		if i == 0 {
			op = ws.OpText
		}
		if err := w.write(i == len(parts)-1, op, []byte(p)); err != nil {
			w.tb.Fatalf("bitetest: writing a frame: %v", err)
		}
	}
}

// Ping sends a ping with payload (at most 125 bytes).
func (w *WS) Ping(payload string) { w.tb.Helper(); w.send(ws.OpPing, []byte(payload)) }

// Pong sends an unsolicited pong, which the server counts as a sign of life (ADR 0039).
func (w *WS) Pong(payload string) { w.tb.Helper(); w.send(ws.OpPong, []byte(payload)) }

// Close sends a Close frame with a status and a reason. It does not wait for the server's answer:
// read it with ReadClose.
func (w *WS) Close(code uint16, reason string) {
	w.tb.Helper()
	p := binary.BigEndian.AppendUint16(nil, code)
	w.send(ws.OpClose, append(p, reason...))
}

// Leave drops the connection with no Close frame, as a client whose network died does.
func (w *WS) Leave() { w.c.Close() }

// TryRead returns the next message the server sent, or the error that ended the connection
// (io.EOF when the server closed it) or a timeout error when nothing came in the real time given
// (the WSRequest's Timeout when d is zero). After the connection ended every call returns the
// same error.
func (w *WS) TryRead(d time.Duration) (Message, error) {
	if w.ended != nil {
		return Message{}, w.ended
	}
	if d == 0 {
		d = w.timeout
	}
	select {
	case f := <-w.in:
		if f.err != nil {
			w.ended = f.err
			return Message{}, f.err
		}
		f.m.app = w.app
		return f.m, nil
	case <-time.After(d):
		return Message{}, errTimeout
	}
}

// Quiet fails the test if the server has sent this socket a data message or a Close that the test
// has not read: every frame the server queued for it before Quiet was called is waited for, and
// nothing else. It is how a test says that something did not arrive (a Topic's message for the
// members of a private room, read by a socket of one who is not) without waiting a real interval.
// It is Pending that fails when the list is not empty, so it waits exactly as Pending does, and
// the failure names the first message that arrived; the messages it consumed are not left to
// read. A message that a handler or a goroutine of the application has not queued yet when the
// ping is read is not waited for: publish first, then call Quiet. It fails the test when the
// connection ended, or the pong did not come within the WSRequest's Timeout.
func (w *WS) Quiet() {
	w.tb.Helper()
	got := w.pending("Quiet")
	if len(got) == 0 {
		return
	}
	m := got[0]
	if m.Kind == Close {
		w.tb.Fatalf("bitetest: Quiet: the server sent a Close (%d %q), want nothing", m.Code, m.Reason)
	}
	w.tb.Fatalf("bitetest: Quiet: the server sent a %s message, want nothing: %q", m.Kind, m.Data)
}

// Pending returns the text and binary messages the server queued for this socket before the call,
// in the order they were sent, and removes them from what the test has still to read; nil when
// there are none. It is for a test that checks what arrived without knowing how many messages to
// read, such as the frames another socket's handler produced. It waits as Quiet does: it sends a
// ping with a payload of its own and reads up to the pong that answers it, which the server
// queues behind every frame already queued, so no real interval is spent. Pings, and pongs that
// answer other pings, are skipped. If the server's Close is among them it is the last Message
// returned, with Kind Close, and Pending does not wait for a pong, since a closed connection
// answers nothing: a Close is a message the test asked about, and failing here would hide the
// data before it. A message a handler or a goroutine of the application has not queued yet is
// not waited for. It fails the test when the connection ended with no Close, or the pong did not
// come within the WSRequest's Timeout.
func (w *WS) Pending() []Message {
	w.tb.Helper()
	return w.pending("Pending")
}

func (w *WS) pending(caller string) []Message {
	w.tb.Helper()
	var tag [8]byte
	rand.Read(tag[:])
	mark := "bitetest-quiet-" + base64.RawURLEncoding.EncodeToString(tag[:])
	w.send(ws.OpPing, []byte(mark))
	var got []Message
	for {
		m, err := w.TryRead(0)
		if err != nil {
			w.tb.Fatalf("bitetest: %s: the connection ended before the server answered its ping: %v", caller, err)
		}
		switch {
		case m.Kind == Pong && string(m.Data) == mark:
			return got
		case m.Kind == Ping || m.Kind == Pong:
		case m.Kind == Close:
			return append(got, m)
		default:
			got = append(got, m)
		}
	}
}

// Read returns the next message, pings and pongs included, and fails the test if the connection
// ended or nothing came in time.
func (w *WS) Read() Message {
	w.tb.Helper()
	m, err := w.TryRead(0)
	if err != nil {
		w.tb.Fatalf("bitetest: reading a message: %v", err)
	}
	return m
}

// ReadData returns the next text or binary message, skipping pings and pongs (a heartbeat that fell
// due while the test moved the clock), and fails the test on anything else.
func (w *WS) ReadData() Message {
	w.tb.Helper()
	for {
		m := w.Read()
		switch m.Kind {
		case Text, Binary:
			return m
		case Ping, Pong:
		default:
			w.tb.Fatalf("bitetest: got a %s (%d %q), want a data message", m.Kind, m.Code, m.Reason)
		}
	}
}

// ReadText returns the next text message as a string (see ReadData).
func (w *WS) ReadText() string {
	w.tb.Helper()
	m := w.ReadData()
	if m.Kind != Text {
		w.tb.Fatalf("bitetest: got a %s message, want text", m.Kind)
	}
	return m.Text()
}

// ReadJSON decodes the next text message into v as Message.JSON does, with the app's codec (see ReadData).
func (w *WS) ReadJSON(v any) {
	w.tb.Helper()
	m := w.ReadData()
	if err := m.JSON(v); err != nil {
		w.tb.Fatalf("bitetest: decoding %q: %v", m.Data, err)
	}
}

// ReadPing returns the payload of the next ping, skipping pongs, and fails the test on any
// other message. The client has already answered it unless NoAutoPong is set.
func (w *WS) ReadPing() string {
	w.tb.Helper()
	for {
		m := w.Read()
		switch m.Kind {
		case Ping:
			return m.Text()
		case Pong:
		default:
			w.tb.Fatalf("bitetest: got a %s, want a ping", m.Kind)
		}
	}
}

// ReadClose returns the status and reason of the server's Close, skipping pings and pongs, and
// fails the test if a data message or the end of the connection comes first.
func (w *WS) ReadClose() (code uint16, reason string) {
	w.tb.Helper()
	for {
		m := w.Read()
		switch m.Kind {
		case Close:
			return m.Code, m.Reason
		case Ping, Pong:
		default:
			w.tb.Fatalf("bitetest: got a %s message (%q), want a Close", m.Kind, m.Data)
		}
	}
}

// ExpectClose requires the server's Close to carry code and returns its reason.
func (w *WS) ExpectClose(code uint16) string {
	w.tb.Helper()
	got, reason := w.ReadClose()
	if got != code {
		w.tb.Fatalf("bitetest: closed with %d %q, want %d", got, reason, code)
	}
	return reason
}

// ExpectEOF requires the connection to end, discarding any messages still in flight.
func (w *WS) ExpectEOF() {
	w.tb.Helper()
	for range 100 {
		if _, err := w.TryRead(0); err != nil {
			if errors.Is(err, errTimeout) {
				w.tb.Fatal("bitetest: the connection did not end")
			}
			return
		}
	}
	w.tb.Fatal("bitetest: the connection did not end")
}
