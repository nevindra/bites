package engine

import (
	"bytes"
	"errors"
	"math"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/wire"
)

const (
	bodyNone uint8 = iota
	bodyUnread
	bodyRead
)

// The refusals ReadBody returns. They are *h1.Error so the framework can answer with
// errors.As; Close is true on all of them because the body's framing is no longer trusted
// (ADR 0045), and the engine closes after the response whatever the framework answers.
var (
	ErrBodyTimeout = &h1.Error{Status: 408, Close: true, Reason: "request body too slow"}
	ErrBodyShort   = &h1.Error{Status: 400, Close: true, Reason: "request body ended early"}
)

// BodyTooLarge is the 413 of a body over max bytes. Its reason names the limit, so a client, or
// an agent reading the answer, knows how far to cut the body without reading the server's code.
func BodyTooLarge(max int64) *h1.Error {
	return &h1.Error{Status: 413, Close: true, Reason: "request body too large: this route reads at most " + strconv.FormatInt(max, 10) + " bytes"}
}

// Exchange is one request in flight. The engine owns it and reuses it for the next request on
// the connection, so it never reaches user code (ADR 0008): the framework copies what users
// see into the slab.
type Exchange struct {
	c          *conn
	slab       wire.Slab
	ctx        *reqctx.Ctx
	headLen    int
	bodyState  uint8
	sent100    bool
	closeAfter bool
	body       []byte
	slabSize   int  // what the slab will be made with, until the first call of Slab
	slabReady  bool // the slab has its buffer
	reqID      string
	reqIDHdr   string // the header the request-ID middleware answers the id in, for the engine's own 500
	ws         *WS    // the WebSocket the route is opening, between NewWS and the end of the request (ws.go)

	// What an Observer is told when the request has ended (ADR 0051): set by the framework with
	// Observe, and counted by the engine as the body is read and the answer written.
	ender       Ender
	readN       int64 // body bytes read from the client
	wrote       int64 // body bytes written, after gzip
	writeFailed bool  // a write of the answer failed: the client left or stopped reading
}

// Ender is told once that an observed request has really ended (ADR 0051): after the last byte
// of its answer was written, after the 101 of an upgrade, or once the engine gave the connection
// up. End runs on the connection's goroutine, before the connection reads its next request, and
// reports whether the connection may carry on; false closes it (an Observer panicked in End and
// the engine cannot tell what state it left).
type Ender interface {
	End(ex *Exchange, e Ended) bool
}

// Ended is what the engine knows of a request once it has ended.
type Ended struct {
	Status    int    // the status sent; the status the chain left when nothing was sent (0 for none)
	Read      int64  // body bytes read from the client
	Written   int64  // body bytes written, after gzip; 0 for a HEAD and when nothing reached the client
	Streamed  bool   // the answer was a Stream or an SSE
	Upgraded  bool   // the answer was the 101 of a WebSocket upgrade
	Panicked  bool   // the handler, a middleware, an Observer's Start or the stream function panicked
	Abandoned bool   // the client left, or stopped reading, before the answer reached it whole
	Err       error  // a stream function's error after its first byte, which cut the stream
	Panic     any    // the value recovered when Panicked
	Stack     []byte // the stack of the goroutine that panicked; a fresh slice
}

// fault is what the engine recovered or was told while serving one request, kept in handle's
// frame and not in the connection, so an idle connection holds nothing for it.
type fault struct {
	err   error
	val   any
	stack []byte
}

// ErrorNoter is optionally implemented by an Ender that wants to be told, as it happens, of each
// error the framework answered (ADR 0051).
type ErrorNoter interface{ NoteError(err error) }

// NoteError tells the request's Ender, when it takes errors, that the framework answered err. It
// does nothing for a request nobody observes, and allocates nothing.
func (ex *Exchange) NoteError(err error) {
	if n, ok := ex.ender.(ErrorNoter); ok {
		n.NoteError(err)
	}
}

// Observe asks the engine to call e.End once this request has ended. The framework calls it
// before anything of its own can panic, so that End runs whatever happens; the value is dropped
// when End has run.
func (ex *Exchange) Observe(e Ender) { ex.ender = e }

func (ex *Exchange) begin(headLen int) {
	ex.headLen = headLen
	ex.reqID, ex.reqIDHdr = "", ""
	ex.body = nil
	ex.c.sc.mp = nil
	ex.sent100, ex.closeAfter = false, false
	ex.ws = nil
	ex.ender, ex.readN, ex.wrote, ex.writeFailed = nil, 0, 0, false
	req := &ex.c.sc.req
	ex.bodyState = bodyNone
	if req.Chunked || req.ContentLength > 0 {
		ex.bodyState = bodyUnread
	}
	ex.c.sc.resp.reset()
	ex.c.sc.resp.lateName, ex.c.sc.resp.late = "", ""
}

// SetLateCookie asks for the Set-Cookie value to be added to this request's answer when its head
// is completed, unless the answer's status is 400 or more or the answer already sets a cookie
// called name (the handler's own change wins). A second call replaces the first.
func (ex *Exchange) SetLateCookie(name, value string) {
	r := &ex.c.sc.resp
	r.lateName, r.late = name, value
}

// appendLate adds the late Set-Cookie line to head when SetLateCookie's conditions hold.
func (r *Response) appendLate(head []byte) []byte {
	if r.lateName == "" || r.status >= 400 || setsCookie(head, r.lateName) {
		return head
	}
	head = append(head, "Set-Cookie: "...)
	head = appendClean(head, r.late)
	return append(head, '\r', '\n')
}

// setsCookie reports whether head has a Set-Cookie line for the cookie called name.
func setsCookie(head []byte, name string) bool {
	const key = "set-cookie:"
	for len(head) > 0 {
		i := bytes.IndexByte(head, '\n')
		if i < 0 {
			break
		}
		line := head[:i]
		head = head[i+1:]
		if len(line) <= len(key) || !strings.EqualFold(string(line[:len(key)]), key) {
			continue
		}
		v := bytes.TrimLeft(line[len(key):], " \t")
		if len(v) > len(name) && string(v[:len(name)]) == name && v[len(name)] == '=' {
			return true
		}
	}
	return false
}

// Head is the parsed request head. It borrows the read buffer and is valid until the response
// is written.
func (ex *Exchange) Head() *h1.Request { return &ex.c.sc.req }

// RemoteAddr is the client's address as the connection reports it.
func (ex *Exchange) RemoteAddr() net.Addr { return ex.c.nc.RemoteAddr() }

// Slab is this request's text buffer: one allocation sized to the head, plus the declared
// Content-Length when the whole body was already in the read buffer with the head (ADR 0008,
// ADR 0044). A body still to arrive is read into a region of its own that grows with the bytes.
// The buffer is made on the first call, so AddSlab can still change its size before that.
func (ex *Exchange) Slab() *wire.Slab {
	if !ex.slabReady {
		ex.slabReady = true
		ex.slab.Init(ex.slabSize)
	}
	return &ex.slab
}

// AddSlab makes the slab n bytes larger, for a request whose route copies the head into it
// (ADR 0022). It has an effect only before the first call of Slab; afterwards the copy falls
// back to allocations of its own, which is correct and costs more.
func (ex *Exchange) AddSlab(n int) {
	if !ex.slabReady {
		ex.slabSize += n
	}
}

// RawHead is the request line and header block exactly as received, in the connection's read
// buffer: pooled memory the framework may copy but never keep (ADR 0008). It is valid until the
// response is written.
func (ex *Exchange) RawHead() []byte { return ex.c.sc.buf[:ex.headLen] }

// HeadLen is the size of the request line and header block as received.
func (ex *Exchange) HeadLen() int { return ex.headLen }

// SetRequestID records the id a middleware gave this request, so every problem+json answer
// carries it, including the 500 the engine writes after a panic (ADR 0012). The string is the
// caller's and is never modified.
func (ex *Exchange) SetRequestID(id string) { ex.reqID = id }

// SetRequestIDHeader names the response header the request-ID middleware puts the id in, so the
// 500 the engine writes after a panic, which no middleware decorates, carries it too (ADR 0028).
// The name is the middleware's configuration, never request bytes.
func (ex *Exchange) SetRequestIDHeader(name string) { ex.reqIDHdr = name }

// RequestID is what SetRequestID recorded for this request, or "".
func (ex *Exchange) RequestID() string { return ex.reqID }

// RemoteAddrPort is the client's address without allocating for the usual connections: the
// client a PROXY header named (ADR 0058), or the connection's peer. A Unix socket's peer has no
// address and is the zero AddrPort (ADR 0068).
func (ex *Exchange) RemoteAddrPort() netip.AddrPort {
	if pc := ex.c.proxyConn(); pc != nil && pc.named {
		return pc.peer
	}
	ap, _ := peerOf(ex.c.raw())
	return ap
}

// PeerIsUnix reports whether the request's peer is the process at the other end of a Unix
// socket: the connection is a Unix socket and no PROXY header named another client. It is what
// trusting a Unix peer as a proxy starts from (ADR 0058), and allocates nothing.
func (ex *Exchange) PeerIsUnix() bool {
	if pc := ex.c.proxyConn(); pc != nil && pc.named {
		return false
	}
	_, unix := peerOf(ex.c.raw())
	return unix
}

// Pending is the response as built so far, for the framework to read its status, size and
// content type, and to change its status. It belongs to the engine's pool: the framework must
// not keep it past the end of Serve.
func (ex *Exchange) Pending() *Response { return &ex.c.sc.resp }

// Context is this request's context, created with the connection as its Watch (ADR 0016).
func (ex *Exchange) Context() *reqctx.Ctx { return ex.ctx }

// ReadBody reads the whole body into the slab. It sends 100 Continue first when the client
// asked for it, answers 413 without reading when the body is over max, and 408 when it takes
// longer than timeout. The engine never modifies them afterwards; the framework may, as a
// Form body is unescaped in place (form.go), since the slab is this request's own. A second call
// returns the same bytes. The errors are *h1.Error.
func (ex *Exchange) ReadBody(max int64, timeout time.Duration) ([]byte, error) {
	if ex.bodyState == bodyRead {
		return ex.body, nil
	}
	c := ex.c
	req := &c.sc.req
	if ex.bodyState == bodyNone {
		return nil, nil
	}
	if max <= 0 {
		max = DefaultBodyLimit
	}
	if timeout <= 0 {
		timeout = DefaultBodyTimeout
	}
	fail := func(e *h1.Error) ([]byte, error) { ex.closeAfter = true; return nil, e }
	if !req.Chunked && req.ContentLength > max {
		return fail(BodyTooLarge(max))
	}
	s := c.srv
	if req.Expect100 && !ex.sent100 {
		ex.sent100 = true
		c.setWriteDeadline(s.now().Add(s.o.WriteTimeout))
		if _, err := c.nc.Write(continueLine); err != nil {
			return fail(ErrBodyShort)
		}
	}
	// A body already whole in the read buffer is not waited for, so it needs no deadline: the
	// usual small POST skips a clock read and two timer changes (docs/research/hotpath-mac).
	if req.Chunked || int64(c.r1-c.r0) < req.ContentLength {
		c.holdBody() // before the deadline: stopping a reader goroutine moves it
		c.setReadDeadline(s.now().Add(timeout))
	}
	var body []byte
	var err error
	if req.Chunked {
		body, err = ex.readChunked(max)
	} else {
		body, err = ex.readLength(int(req.ContentLength))
	}
	if err != nil {
		ex.closeAfter = true
		if errors.Is(err, os.ErrDeadlineExceeded) {
			c.releaseBody(false)
			return nil, ErrBodyTimeout
		}
		var he *h1.Error
		if errors.As(err, &he) {
			c.releaseBody(false)
			return nil, he
		}
		c.releaseBody(true)
		return nil, ErrBodyShort
	}
	c.releaseBody(false)
	ex.bodyState, ex.body = bodyRead, body
	ex.readN = int64(len(body))
	return body, nil
}

var continueLine = []byte("HTTP/1.1 100 Continue\r\n\r\n")

// firstBody is the most a body region holds before a byte of the body has proved the client is
// sending it (ADR 0044): a client that declares a large Content-Length and goes quiet holds this
// much, not what it declared.
const firstBody = 64 << 10

// readLength reads a Content-Length body of n bytes.
//
// Where the body lives. When the slab was sized for it (the whole body was already in the read
// buffer when the head was parsed, the common case) the body is reserved in the slab and nothing
// is allocated here. Otherwise the body goes in a region of its own, starting at
// min(n, firstBody) and doubling, capped at n, as bytes arrive. Growing moves bytes, which is
// safe only because nothing points into the region until this function returns: the framework
// makes strings over the body (JSON decoding takes views of it) after ReadBody, never during.
// The slab itself never grows, so the head's copies, made before ReadBody, never move (ADR 0008).
func (ex *Exchange) readLength(n int) ([]byte, error) {
	c := ex.c
	have := min(c.r1-c.r0, n)
	var dst []byte
	if slab := ex.Slab(); n <= slab.Remaining() {
		dst = slab.Reserve(n)
	} else {
		dst = make([]byte, min(n, max(firstBody, have)))
	}
	got := copy(dst, c.sc.buf[c.r0:c.r0+have])
	c.r0 += got
	for got < n {
		if got == len(dst) {
			nb := make([]byte, min(n, 2*len(dst)))
			copy(nb, dst[:got])
			dst = nb
		}
		m, err := c.nc.Read(dst[got:])
		got += m
		if err != nil && got < n {
			return nil, err
		}
	}
	return dst, nil
}

// readChunked decodes a chunked body straight into a region that doubles as bytes arrive, capped
// at limit+1 (one byte over the limit is enough to refuse it). The region is the body the
// framework keeps, so nothing is staged and nothing is copied afterwards; it starts at 4 KiB so
// a client that sends the head and goes quiet holds that, not the limit.
func (ex *Exchange) readChunked(limit int64) ([]byte, error) {
	c := ex.c
	sc := c.sc
	var dec h1.ChunkedDecoder
	dec.Reset(c.srv.o.Limits.MaxHead)
	lim := int(min(limit, math.MaxInt32)) + 1
	var body []byte
	in := sc.buf[c.r0:c.r1]
	fromAux := false
	for {
		if len(in) == 0 {
			if sc.aux == nil {
				sc.aux = make([]byte, bufSize)
			}
			m, err := c.nc.Read(sc.aux)
			if m == 0 {
				return nil, err
			}
			c.r0 = c.r1 // everything in buf is consumed from here on
			in, fromAux = sc.aux[:m], true
		}
		if cap(body)-len(body) < 4096 && cap(body) < lim {
			nb := make([]byte, len(body), min(lim, max(4096, 2*cap(body))))
			copy(nb, body)
			body = nb
		}
		consumed, outN, done, err := dec.Decode(in, body[len(body):cap(body)])
		body = body[:len(body)+outN]
		in = in[consumed:]
		if int64(len(body)) > limit {
			return nil, BodyTooLarge(limit)
		}
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	if fromAux {
		c.sc.auxRest = in
	} else {
		c.r0 = c.r1 - len(in)
	}
	return body[:len(body):len(body)], nil
}

// Response is the answer being built. It lives in the pooled scratch, so the framework fills
// it and returns; the engine writes it once after Serve returns (ADR 0009).
type Response struct {
	status int
	set    bool
	head   []byte
	body   []byte
	spare  []byte       // the scratch's own body buffer while Reserve has put a large one in body
	ctype  string       // the last Content-Type given to Header
	stream StreamRunner // set when the body is a stream, written after Serve returns (ADR 0030)
	live   bool
	sized  bool // the stream has a length it declared, sent as Content-Length (a File, ADR 0043)
	length int64

	upgrade *WS // set when the answer is the 101 of a WebSocket handshake (ws.go)

	// A Set-Cookie value the framework asked for while serving (a session's refresh, ADR 0056),
	// added when the head is completed unless the answer failed or sets a cookie of lateName
	// itself. Kept across Respond, which a later step calls, and cleared when a request begins.
	lateName, late string
}

func (r *Response) reset() {
	r.dropStream()
	r.set, r.status, r.head, r.body, r.ctype = false, 0, r.head[:0], r.body[:0], ""
	r.stream, r.live, r.sized, r.length = nil, false, false, 0
	r.upgrade = nil
}

func (r *Response) start(status int) *Response {
	r.reset()
	r.set, r.status = true, status
	r.head = appendStatusLine(r.head, status)
	return r
}

// Respond begins the response with status, discarding any earlier call's headers and body.
func (ex *Exchange) Respond(status int) *Response { return ex.c.sc.resp.start(status) }

// Header appends a header line. A CR, LF or NUL in the name or value is replaced by a space,
// because a value that reached here from a client would otherwise split the response. The
// engine adds Date, Content-Length and Connection itself.
func (r *Response) Header(name, value string) {
	if len(name) == 12 && strings.EqualFold(name, "content-type") {
		r.ctype = value
	}
	r.head = appendClean(r.head, name)
	r.head = append(r.head, ':', ' ')
	r.head = appendClean(r.head, value)
	r.head = append(r.head, '\r', '\n')
}

func appendClean(b []byte, s string) []byte {
	// The usual header has nothing to replace: look first, eight bytes at a time, then copy it in
	// one append. Three IndexByte calls cost more than the whole scan on a header this short.
	if !mayNeedClean(s) {
		return append(b, s...)
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\r' || ch == '\n' || ch == 0 {
			ch = ' '
		}
		b = append(b, ch)
	}
	return b
}

// mayNeedClean reports whether s may hold a CR, an LF or a NUL: false is certain, true means a byte
// below 0x0e is there (a tab is one), and appendClean then looks at each byte.
func mayNeedClean(s string) bool {
	const ones, high = 0x0101010101010101, 0x8080808080808080
	i := 0
	for ; i+8 <= len(s); i += 8 {
		x := uint64(s[i]) | uint64(s[i+1])<<8 | uint64(s[i+2])<<16 | uint64(s[i+3])<<24 |
			uint64(s[i+4])<<32 | uint64(s[i+5])<<40 | uint64(s[i+6])<<48 | uint64(s[i+7])<<56
		if (x-ones*0x0e)&^x&high != 0 {
			return true
		}
	}
	for ; i < len(s); i++ {
		if s[i] < 0x0e {
			return true
		}
	}
	return false
}

// Body is where the body is appended (a JSON encoder appends to it). The slice belongs to the
// engine's pool: the framework must not keep it past the end of Serve.
func (r *Response) Body() *[]byte { return &r.body }

// Reserve makes room for n more body bytes at once, from the engine's pools of large buffers
// (bigbuf.go), so an answer the framework expects to be large is not grown by doubling from the
// scratch's buffer, copying and leaving garbage at each step. It does nothing when the room is
// there. The bytes already in the body are kept.
func (r *Response) Reserve(n int) {
	need := len(r.body) + n
	if need <= cap(r.body) {
		return
	}
	nb := append(takeBig(need), r.body...)
	if cap(r.body) <= maxPooled && r.spare == nil {
		r.spare = r.body[:0]
	} else {
		putBig(r.body)
	}
	r.body = nb
}

// Status is the status of the response so far, or 0 when none has been begun.
func (r *Response) Status() int {
	if !r.set {
		return 0
	}
	return r.status
}

// BodyLen is the number of body bytes written so far.
func (r *Response) BodyLen() int { return len(r.body) }

// SetStatus changes the status line of a response already begun, keeping its headers and body.
func (r *Response) SetStatus(status int) {
	if !r.set || r.status == status {
		return
	}
	i := bytes.Index(r.head, []byte("\r\n"))
	if i < 0 {
		return
	}
	var tmp [64]byte
	line := appendStatusLine(tmp[:0], status)
	r.head = slices.Replace(r.head, 0, i+2, line...)
	r.status = status
}

// ContentType is the value of the Content-Type the response was given, or "" when it has none.
// It is the string the framework passed to Header, never a view of the pooled head.
func (r *Response) ContentType() string { return r.ctype }
