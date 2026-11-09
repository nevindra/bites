package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"os"
	"reflect"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/wire"
)

// Connection states, for Shutdown to tell an idle connection from a busy one (ADR 0029).
const (
	stActive  int32 = iota
	stIdle          // blocked on the one-byte read, holding no buffer
	stClosing       // claimed by Shutdown, which is closing it
	// stWS is a connection that has become a WebSocket: neither idle nor a request in flight.
	// Shutdown sends it a Close 1001 (ADR 0029).
	stWS
	// stLive is a connection whose request is a live stream (SSE, or a Stream declared Live)
	// running its function. Shutdown cancels the request's context with ErrServerClosed. It is
	// stored when such a stream starts and put back to stActive when its function returns, so a
	// plain request, and a plain Stream, never touch it (ADR 0029).
	stLive
)

// watch modes, chosen when a request first arms.
const (
	modeNone = iota
	modeEpoll
	modeMem
	modeReader
)

// scratch is everything the engine pools: what a request needs while it is read and answered,
// and which no handler can hold (ADR 0008).
// watchState is the arming state of one connection; mu guards it. onHangup takes no lock (the
// watcher calls it under its own).
type watchState struct {
	mu      sync.Mutex
	mode    int
	armed   bool
	watchID uint64
	raw     syscall.RawConn
	hangup  func()
	stop    chan struct{} // memconn helper
	fbDone  chan struct{} // reader goroutine
	fbStop  atomic.Bool
	pendBuf [1]byte
}

type scratch struct {
	buf  []byte // read buffer; the head and any pipelined bytes live here
	aux  []byte // chunked input and body draining; made on first use
	req  h1.Request
	resp Response
	mp   *Multipart // the upload being read, set by OpenMultipart; in the pooled scratch so an idle connection carries nothing for it (ADR 0044)
	slab wire.Slab  // the header a WebSocket message is decoded with; cleared after each message (ADR 0046)
	// wdl is the write deadline the connection's goroutine last set while it held this scratch,
	// in Unix nanoseconds, or 0 for none: write re-arms it only when less than half of
	// WriteTimeout is left, and it is cleared when the scratch leaves the connection (dropScratch).
	wdl int64
	// rdl is the read deadline await last armed for a hot wait while the connection held this
	// scratch, in Unix nanoseconds, or 0 when another read since set its own (setReadDeadline):
	// a hot wait re-arms only when less than half of moveAfter is left (await).
	rdl  int64
	iov  [2][]byte   // the head and body of a large answer, for one writev (write)
	bufs net.Buffers // iov while it is written
	// auxRest is the pipelined bytes left in aux after a chunked body, moved to buf by recycle.
	auxRest []byte
	// io is the raw read and write of a bare TCP socket (rawio_linux.go), and wnow the clock
	// reading of the answer write wrote without waiting, which the next wait takes as its start
	// instead of reading the clock again (await); zero when nothing since may have taken long.
	io   rawIO
	wnow time.Time
	// wsSeen is true when a WebSocket's hot wait has read bytes since rdl was armed: a timeout
	// then is not a quiet moveAfter, and the wait arms again instead of parking (await).
	wsSeen bool
}

func newScratch() *scratch {
	sc := &scratch{buf: make([]byte, bufSize)}
	sc.io.init()
	return sc
}

// conn is one connection: its goroutine's state, the Exchange it reuses, and the reqctx.Watch.
type conn struct {
	srv        *Server
	nc         net.Conn
	serveFn    func() // c.serve, made once so the hand-off to a fresh goroutine allocates nothing
	prev, next *conn
	state      atomic.Int32

	sc     *scratch
	r0, r1 int // unconsumed bytes of sc.buf are buf[r0:r1]
	one    [1]byte
	// rc is the socket's RawConn when it is a bare TCP connection, for the raw read of a hot wait
	// and the raw write of an answer (rawio_linux.go); nil for any other kind and off Linux. It is
	// made at the first answer (rcTried), once the PROXY protocol header and TLS are behind.
	rc syscall.RawConn

	ex  Exchange
	ws  *WS                        // set when the connection has become a WebSocket (ADR 0036); the connection then waits and reads as one
	cur atomic.Pointer[reqctx.Ctx] // the request in flight, for onHangup and Shutdown

	// Disconnect-watch state, made on the first Arm so a connection whose handlers never look at
	// their context does not carry it (it is about 100 bytes of every idle connection).
	w atomic.Pointer[watchState]

	pending byte // the byte the reader goroutine took; used by the next request
	hasPend bool

	idleUntil time.Time // when the idle wait in progress ends
	waiting   bool      // an idle wait is in progress: its state and idleUntil are set (ADR 0005)
	moved     bool      // the idle wait was handed to this goroutine after moveAfter
	px        uint8     // the PROXY protocol: pxNone, pxDue or pxRead (proxy.go); in what was padding, so the struct keeps its size class
	rcTried   bool      // rc was looked for
	pad       uint8     // the padding a hot wait blocks under (waitHot): a tier, the goroutine's, so serve resets it

	pk atomic.Int32 // who owns the connection: a goroutine, or the poller while it is parked (park.go)
	ps *parkState   // made the first time the connection parks
}

// What awaitRequest found.
const (
	waitServe      = iota // a request's first byte is in hand
	waitEnd               // the connection is over
	waitMove              // idle for moveAfter: the wait goes on on a fresh goroutine
	waitPark              // idle for moveAfter: the connection parks and its goroutine ends
	waitMsgTimeout        // a WebSocket message did not arrive whole in time (ADR 0046)
)

// serve runs the connection's requests on one goroutine until the connection has been idle for
// moveAfter, then parks it, ending the goroutine, or where it cannot park hands the rest of the
// wait to a fresh goroutine and returns (ADR 0005). Go never shrinks the stack a request grew while
// its goroutine is blocked in a read, so an idle connection would otherwise keep the 4 KiB or more
// its last request left; a busy one never pays for either.
func (c *conn) serve() {
	c.pad = 0 // a fresh goroutine, a fresh stack (waitHot)
	for {
		switch c.await() {
		case waitPark:
			if c.park() {
				return
			}
			continue // woken while it registered: read what woke it
		case waitMove:
			go c.serveFn()
			return
		case waitEnd:
			c.finish()
			return
		case waitMsgTimeout:
			c.ws.messageTimedOut()
			continue
		}
		if c.px == pxDue {
			if !c.readProxy() {
				c.finish()
				return
			}
			continue // the first request is in the buffer already, or is awaited idle
		}
		if c.ws != nil {
			if !c.wsInput() {
				c.finish()
				return
			}
			continue
		}
		if !c.readHead() || !c.handle() {
			c.finish()
			return
		}
		c.recycle()
	}
}

// errNotHTTP11 is a handshake that ended on a protocol the engine does not speak.
var errNotHTTP11 = errors.New("bites: the client negotiated a protocol other than http/1.1")

// handshake is the first thing a TLS connection's goroutine does, once per connection (ADR 0017):
// the handshake under HandshakeTimeout on the engine's clock, then the connection joins the
// ordinary serve loop. It reads no deadline or clock on the request path; the two deadline calls
// here are the connection's, not a request's. The connection counts against MaxConns from accept,
// and is stIdle meanwhile, so Shutdown closes a handshake in progress as it does an idle wait
// (it has no request to finish).
func (c *conn) handshake() {
	s := c.srv
	tc := c.nc.(*tls.Conn)
	c.state.Store(stIdle)
	tc.SetDeadline(s.now().Add(s.o.HandshakeTimeout))
	if c.px == pxDue && !c.readProxyTLS() {
		c.finish() // the PROXY header comes before the ClientHello, under the same deadline (ADR 0058)
		return
	}
	err := tc.Handshake()
	if err == nil {
		// Never speak HTTP/1 to a client that negotiated something else. NextProtos is forced to
		// http/1.1, so this holds only if a config path escaped that; it is cheap and final. A
		// tls-alpn-01 challenge is answered by the handshake itself: it ends here, quietly, and is
		// not a failed handshake (ADR 0017).
		switch p := tc.ConnectionState().NegotiatedProtocol; p {
		case "", "http/1.1":
		case ACMEProto:
			c.finish()
			return
		default:
			err = errNotHTTP11
		}
	}
	if err != nil {
		c.handshakeFailed(err)
		c.finish()
		return
	}
	tc.SetDeadline(time.Time{})
	if !c.state.CompareAndSwap(stIdle, stActive) {
		c.finish() // Shutdown claimed it while it handshook
		return
	}
	c.serve()
}

// warnEvery is the least time between two Warn lines about client-rejected certificates.
const warnEvery = time.Minute

// handshakeFailed logs a handshake that did not complete (ADR 0017). A failure caused by a
// received client alert that rejects the certificate (bad_certificate, unsupported_certificate,
// certificate_revoked, certificate_expired, certificate_unknown, unknown_ca) is the one that
// points at the operator's certificate, so it is a Warn, at most one a minute on the engine's
// clock, the ones skipped counted into suppressed=N. Every other failure is internet noise (a
// port scan, plain HTTP, an old client, a GetCertificate error under autocert) and is a Debug
// line, built only when Debug is enabled: the remote address is not formatted otherwise.
func (c *conn) handshakeFailed(err error) {
	s := c.srv
	lg := s.o.Logger
	if clientRejectedCertificate(err) {
		now := s.now().UnixNano()
		last := s.hsWarnAt.Load()
		if last != 0 && now-last < int64(warnEvery) || !s.hsWarnAt.CompareAndSwap(last, now) {
			s.hsSuppressed.Add(1)
			return
		}
		lg.Warn("bites: a client rejected the TLS certificate (expired, wrong name or untrusted?)",
			"err", err, "remote", c.nc.RemoteAddr().String(), "suppressed", s.hsSuppressed.Swap(0))
		return
	}
	if lg.Enabled(context.Background(), slog.LevelDebug) {
		lg.Debug("bites: TLS handshake failed", "err", err, "remote", c.nc.RemoteAddr().String())
	}
}

// clientRejectedCertificate reports whether err carries a received TLS alert that rejects the
// server's certificate. crypto/tls reports a received alert as a *net.OpError with Op "remote
// error" whose Err is its unexported alert type, a uint8 (only the QUIC path wraps the exported
// tls.AlertError, which is accepted too), so the number is read with reflect: no string is
// matched, and nothing is allocated, because this runs on every failed handshake. The chain is
// walked by hand because errors.As would make its target escape.
func clientRejectedCertificate(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		var code uint8
		switch e := err.(type) {
		case tls.AlertError:
			code = uint8(e)
		case *net.OpError:
			if e.Op != "remote error" {
				continue
			}
			v := reflect.ValueOf(e.Err)
			if v.Kind() != reflect.Uint8 {
				return false
			}
			code = uint8(v.Uint())
		default:
			continue
		}
		switch code {
		case 42, 43, 44, 45, 46, 48: // bad_certificate .. unknown_ca, RFC 8446 section 6.2
			return true
		}
		return false
	}
	return false
}

// raw is the connection under TLS and under the PROXY protocol's wrapper, or c.nc itself for a
// plain one. The disconnect watcher, parking and the linger close reach the kernel's socket
// through it; everything that moves bytes uses c.nc.
func (c *conn) raw() net.Conn {
	nc := c.nc
	if tc, ok := nc.(*tls.Conn); ok {
		nc = tc.NetConn()
	}
	if pc, ok := nc.(*proxyConn); ok {
		return pc.Conn
	}
	return nc
}

func (c *conn) finish() {
	if c.ws != nil {
		c.ws.end()
	}
	if c.sc != nil {
		c.srv.putScratch(c.sc)
		c.sc = nil
	}
	c.cur.Store(nil)
	c.disarm()
	c.unpark()
	c.nc.Close()
	c.srv.remove(c)
	<-c.srv.sem
}

func (s *Server) getScratch() *scratch { return s.pool.Get().(*scratch) }

func (s *Server) putScratch(sc *scratch) {
	if cap(sc.buf) != bufSize {
		sc.buf = make([]byte, bufSize)
	}
	s.cleanScratch(sc)
	sc.wdl, sc.rdl = 0, 0
	s.pool.Put(sc)
}

// cleanScratch readies a scratch for another request, kept by its connection (recycle) or back
// in the pool.
func (s *Server) cleanScratch(sc *scratch) {
	// A buffer that grew large is not kept with the scratch, so one big request does not raise
	// the footprint of every scratch: a large body goes back to its size class (bigbuf.go) and
	// the scratch keeps its own buffer, the one Reserve set aside.
	if cap(sc.buf) > wsHotBuf {
		sc.buf = make([]byte, bufSize) // a head that grew past MaxHead's buffer is not kept
	}
	if cap(sc.resp.head) > maxPooled {
		sc.resp.head = nil
	}
	if cap(sc.resp.body) > maxPooled {
		putBig(sc.resp.body)
		sc.resp.body = sc.resp.spare
	}
	sc.resp.spare = nil
	sc.req = h1.Request{Headers: sc.req.Headers[:0]}
	sc.slab = wire.Slab{}
	sc.auxRest = nil
	sc.wnow = time.Time{}
	sc.wsSeen = false
}

// dropScratch gives the connection's scratch back when it goes quiet, clearing the write deadline
// write left armed (an armed deadline is a runtime timer an idle socket would carry) and the last
// request's context, which handle leaves in cur for a busy connection's next request to replace:
// an idle connection keeps neither.
func (c *conn) dropScratch() {
	if c.sc.wdl != 0 {
		c.nc.SetWriteDeadline(time.Time{})
	}
	c.srv.putScratch(c.sc)
	c.sc = nil
	c.cur.Store(nil)
}

// waitStart is when a wait begins: the clock reading of the answer just written, when write took
// it and nothing since may have taken long (scratch.wnow), or else the clock. A busy connection
// reads the clock once a request instead of twice; the wait starts the microseconds of one
// non-blocking write early, which ends it as much earlier.
func (c *conn) waitStart() time.Time {
	if sc := c.sc; sc != nil && !sc.wnow.IsZero() {
		t := sc.wnow
		sc.wnow = time.Time{}
		return t
	}
	return c.srv.now()
}

// findRawConn looks for rc once, at the first hot read or answer: the PROXY protocol header and
// TLS are behind by then.
func (c *conn) findRawConn() {
	if !c.rcTried {
		c.rcTried = true
		if !c.srv.noInline {
			c.rc = rawConnOf(c.nc)
		}
	}
}

// readHot is the read of a hot wait: raw on a bare TCP socket (rawio_linux.go), else the
// net.Conn's.
func (c *conn) readHot(b []byte) (int, error) {
	c.findRawConn()
	if c.rc == nil {
		return c.nc.Read(b)
	}
	o := &c.sc.io
	o.b, o.n, o.err = b, 0, nil
	err := c.rc.Read(o.readFn)
	o.b = nil
	if err == nil {
		err = o.err
	}
	return o.n, err
}

// The collector halves the stack of a goroutine that uses less than a quarter of it (the runtime's
// shrinkstack), and a goroutine blocked in a hot wait's read uses about 1.7 KiB counting the
// runtime's guard. A connection whose requests grew its stack to 8 KiB or more (a typed route
// does, three middleware take it to 16) had it halved at every cycle, about a hundred a second
// under load, and the next request grew it back with a copy: 21% of a typed route's p99, 26%
// behind three middleware (docs/research/stack-shrink/). So a hot wait blocks under a frame of
// padding sized to keep it above that quarter, nothing at first: a wait that finds the stack
// moved under it (shrunk, since a blocked goroutine does not grow) takes the next tier, 1 KiB
// for a stack of 8 KiB, 3 KiB for 16, 7 KiB for 32. A larger stack is still halved. The tier
// only rises once the stack is large enough to be shrunk at the tier below, so the padding
// never grows the stack itself, and serve resets it, since each goroutine starts on a fresh
// stack. The padding costs no memory a connection would otherwise give back: a hot wait lasts
// at most moveAfter, then the connection parks or moves and its goroutine ends with its stack
// (ADR 0005); and it is not zeroed, so a wait costs one comparison of stack addresses.
const maxPad = 3

func (c *conn) waitHot(b []byte) (n int, err error) {
	at := stackAt()
	switch c.pad {
	case 0:
		n, err = c.readHot(b)
	case 1:
		n, err = c.readHotPad1(b)
	case 2:
		n, err = c.readHotPad3(b)
	default:
		n, err = c.readHotPad7(b)
	}
	if c.pad < maxPad && stackAt() != at {
		c.pad++
	}
	return n, err
}

// stackAt is an address in its own frame: called twice from one frame, it differs only when the
// goroutine's stack was copied in between.
//
//go:noinline
func stackAt() uintptr {
	var b byte
	return uintptr(unsafe.Pointer(&b))
}

// padNever is never set: the padding of readHotPad* is declared under it so that the frame holds
// the room and nothing zeroes it.
var padNever bool

//go:noinline
func padKeep(p []byte) { _ = p }

//go:noinline
func (c *conn) readHotPad1(b []byte) (int, error) {
	if padNever {
		var p [1 << 10]byte
		padKeep(p[:])
	}
	return c.readHot(b)
}

//go:noinline
func (c *conn) readHotPad3(b []byte) (int, error) {
	if padNever {
		var p [3 << 10]byte
		padKeep(p[:])
	}
	return c.readHot(b)
}

//go:noinline
func (c *conn) readHotPad7(b []byte) (int, error) {
	if padNever {
		var p [7 << 10]byte
		padKeep(p[:])
	}
	return c.readHot(b)
}

// writeAnswer writes all of b, raw on a bare TCP socket (rawio_linux.go), else through the
// net.Conn. waited is true when the write had to wait for room in the kernel's buffer, and
// whenever it went through the net.Conn, which does not say.
func (c *conn) writeAnswer(b []byte) (waited bool, err error) {
	c.findRawConn()
	if c.rc == nil {
		_, err = c.nc.Write(b)
		return true, err
	}
	o := &c.sc.io
	o.b, o.n, o.err, o.waited = b, 0, nil, false
	err = c.rc.Write(o.writeFn)
	o.b = nil
	if err == nil {
		err = o.err
	}
	return o.waited, err
}

// setReadDeadline sets the socket's read deadline from the connection's goroutine for any read
// but a hot wait's, and forgets the one a hot wait armed, so the next hot wait arms its own.
func (c *conn) setReadDeadline(t time.Time) {
	c.nc.SetReadDeadline(t)
	if c.sc != nil {
		c.sc.rdl = 0
	}
}

// setWriteDeadline sets the socket's write deadline from the connection's goroutine and records
// it in the scratch, so write knows what is armed. A zero t clears it.
func (c *conn) setWriteDeadline(t time.Time) {
	c.nc.SetWriteDeadline(t)
	if c.sc != nil {
		c.sc.wdl = 0
		if !t.IsZero() {
			c.sc.wdl = t.UnixNano()
		}
	}
}

// await is the one place a connection waits for its next bytes, plain, TLS or WebSocket
// (ADR 0005), and the one place it decides to park (park.go). For TLS the read below is on the
// *tls.Conn, which reads a whole record from the kernel and hands back one byte, so bytes the
// client sent can sit decrypted inside crypto/tls while the kernel's socket shows nothing
// readable. A TLS connection is parked only after this read timed out with no byte, which
// tls.Conn.Read does only when it holds no decrypted byte and no whole record (park.go, "TLS").
//
// A WebSocket waits the same way, holding no buffer, and differs in three things: the wait has
// no idle timeout (the heartbeat on the wheel judges a dead peer, ADR 0039), only the deadline of
// a message that did not arrive whole (ADR 0046); it is not stIdle, so Shutdown does not close
// it as an idle connection; and what follows the first byte is the frame reader, not the head
// parser.
//
// await blocks until a request's first byte is in hand, either already buffered (pipelining),
// stashed by the reader goroutine, or arriving on the one-byte idle read. The idle read first
// waits only moveAfter; a connection still idle then returns waitPark when it can park, or
// waitMove, staying stIdle so Shutdown can claim it. A parked connection's idle timeout is on the
// wheel; a moved one's next goroutine waits out the rest of IdleTimeout. A connection woken from
// parking comes back here with its wait still in progress (waiting), and reads for at most
// moveAfter again before it parks again: what woke it is usually already there.
func (c *conn) await() int {
	s := c.srv
	w := c.ws
	if c.hasPend {
		if c.sc == nil {
			c.sc = s.getScratch()
			c.r0, c.r1 = 0, 0
		}
		if c.r1 == len(c.sc.buf) {
			c.growBuf()
		}
		c.sc.buf[c.r1] = c.pending
		c.r1++
		c.hasPend = false
		return waitServe
	}
	if c.r1 > c.r0 {
		return waitServe
	}
	until := c.idleUntil
	hot := false
	if !c.moved && !c.waiting && w != nil && c.sc != nil && c.sc.rdl != 0 && w.msgDue == 0 {
		// A busy WebSocket between messages: the hot wait keeps the deadline the last one armed
		// and reads no clock. A timeout with bytes read since it was armed (wsSeen) is not a
		// quiet moveAfter, and the wait arms again (below), so a quiet socket parks after between
		// one and two moveAfter, and a busy one reads the clock once a moveAfter, not a message.
		c.idleUntil = time.Time{}
		c.waiting = true
		hot = true
		until = time.Unix(0, c.sc.rdl)
	} else if !c.moved {
		now := c.waitStart()
		if !c.waiting {
			if w == nil {
				if s.shutting.Load() {
					return waitEnd
				}
				c.state.Store(stIdle)
				if s.shutting.Load() && c.state.CompareAndSwap(stIdle, stClosing) {
					return waitEnd
				}
				c.idleUntil = now.Add(s.o.IdleTimeout)
			} else {
				c.idleUntil = w.messageDeadline() // zero: no deadline, the heartbeat watches
			}
			c.waiting = true
			// A wait that begins with the last request's scratch still held is hot: until it moves
			// it reads straight into the scratch, one read for the next request instead of a byte
			// and then the rest. It holds the scratch only for the moveAfter it already holds a
			// goroutine and its stack for; parked or moved, it holds nothing (ADR 0005).
			hot = c.sc != nil
		}
		until = now.Add(moveAfter)
		if !c.idleUntil.IsZero() && c.idleUntil.Before(until) {
			until = c.idleUntil
		}
		if hot {
			// The deadline the last hot wait armed is kept while at least half of moveAfter is
			// left of it and it falls before until: a busy connection changes its read deadline
			// every moveAfter/2 instead of on each request. The wait ends a little earlier than
			// moveAfter, which only moves or parks it sooner; it never ends past idleUntil.
			if d := c.sc.rdl; d != 0 && d <= until.UnixNano() && d-now.UnixNano() >= int64(moveAfter/2) {
				until = time.Unix(0, d)
			} else {
				c.nc.SetReadDeadline(until)
				c.sc.rdl = until.UnixNano()
				c.sc.wsSeen = false
			}
		} else {
			c.setReadDeadline(until)
		}
	} else {
		c.setReadDeadline(until)
	}
	var n int
	var err error
	if hot {
		n, err = c.waitHot(c.sc.buf)
		if n == 0 && w != nil && c.sc.wsSeen && errors.Is(err, os.ErrDeadlineExceeded) {
			c.sc.rdl, c.sc.wsSeen = 0, false
			c.waiting = false
			return c.await() // not quiet: arm from the clock and wait again
		}
		if n == 0 {
			c.dropScratch()
		} else if w != nil {
			c.sc.wsSeen = true
		}
	} else {
		n, err = c.nc.Read(c.one[:])
	}
	if n == 0 && !c.moved && errors.Is(err, os.ErrDeadlineExceeded) && (c.idleUntil.IsZero() || until.Before(c.idleUntil)) {
		if c.parkable() {
			return waitPark
		}
		c.moved = true
		return waitMove
	}
	c.moved, c.waiting = false, false
	if w == nil && !c.state.CompareAndSwap(stIdle, stActive) {
		return waitEnd // Shutdown claimed it
	}
	if n == 0 {
		if w != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && !c.idleUntil.IsZero() {
				return waitMsgTimeout
			}
			w.readErr = err
		}
		return waitEnd
	}
	if hot {
		c.r0, c.r1 = 0, n
		if w != nil && n == len(c.sc.buf) && n < wsHotBuf {
			// A WebSocket whose messages fill the buffer reads them in pieces, each piece a read
			// and the message an assembly: the buffer doubles, up to wsHotBuf, for the next one.
			// It stays while the socket is hot and goes back to bufSize with the scratch.
			nb := make([]byte, min(2*n, wsHotBuf))
			copy(nb, c.sc.buf[:n])
			c.sc.buf = nb
		}
		return waitServe
	}
	c.sc = s.getScratch()
	c.sc.buf[0] = c.one[0]
	c.r0, c.r1 = 0, 1
	return waitServe
}

func (c *conn) growBuf() {
	sc := c.sc
	n := min(len(sc.buf)*2, max(c.srv.o.Limits.MaxHead, len(sc.buf)+1))
	nb := make([]byte, n)
	copy(nb, sc.buf[:c.r1])
	sc.buf = nb
}

// readHead reads and parses one head into c.sc.req. It reports false when the connection is
// over: closed silently on a timeout or a short read, or after answering a refusal.
func (c *conn) readHead() bool {
	s := c.srv
	sc := c.sc
	// HeadTimeout is armed only when the head did not come whole with the first read. Otherwise
	// the wait's deadline is left as it is: every read after the head (a body, a drain, the
	// reader-goroutine watch, the next wait) arms or clears its own.
	armed := false
	for {
		n, err := h1.ParseHead(sc.buf[:c.r1], &sc.req, s.o.Limits)
		switch {
		case err == nil:
			c.ex.begin(n)
			c.r0 = n
			if armed {
				c.setReadDeadline(time.Time{}) // body reads and draining set their own
			}
			return true
		case err == h1.ErrNeedMore:
			if !armed {
				c.setReadDeadline(s.now().Add(s.o.HeadTimeout))
				armed = true
			}
			if c.r1 == len(sc.buf) {
				if len(sc.buf) >= s.o.Limits.MaxHead {
					c.refuse(431)
					return false
				}
				c.growBuf()
				sc = c.sc
			}
			m, rerr := c.nc.Read(sc.buf[c.r1:])
			c.r1 += m
			if rerr != nil && m == 0 {
				return false // timeout, EOF or reset: ADR 0024 says closed, not answered
			}
		default:
			status := 400
			var he *h1.Error
			if errors.As(err, &he) {
				status = he.Status
			}
			c.refuse(status)
			return false
		}
	}
}

// refuse answers a refused request and ends the connection (ADR 0045).
func (c *conn) refuse(status int) {
	r := c.sc.resp.start(status)
	r.Header("Content-Type", "application/problem+json")
	r.body = appendProblem(r.body, status, "")
	c.write(false)
	c.lingerClose()
}

// call runs the handler with the engine's recovery around it (ADR 0028).
func (c *conn) call(f *fault) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			st := debug.Stack()
			f.val, f.stack = v, st
			req := &c.sc.req
			c.srv.o.Logger.Error("panic serving request", "panic", v, "stack", string(st),
				"method", string(req.Method), "target", string(req.Target), "remote", c.nc.RemoteAddr().String())
		}
	}()
	c.srv.h.Serve(&c.ex)
	return false
}

// handle serves the request whose head was just read, and reports whether the connection
// continues.
func (c *conn) handle() bool {
	s := c.srv
	ex := &c.ex
	sc := c.sc
	req := &sc.req

	// The slab holds the head's copies, and the body too only when all of it is already in the
	// read buffer: then it is one allocation and costs no more than what the client has sent. A
	// body still to arrive is not reserved on the strength of a header (ADR 0044): ReadBody
	// grows a region of its own as bytes come, so a client that declares 1 MiB and sends nothing
	// holds nothing, and a route that never reads the body reserves nothing for it.
	size := ex.headLen
	if cl := req.ContentLength; cl > 0 && cl <= DefaultBodyLimit && int64(c.r1-c.r0) >= cl {
		size += int(cl)
	}
	ex.slabSize, ex.slabReady = size, false
	sc.wnow = time.Time{} // only this request's answer may start the next wait (waitStart)
	ctx := reqctx.New(context.Background(), c)
	ex.ctx = ctx
	c.cur.Store(ctx)

	var f fault
	panicked := c.call(&f)
	ex.finishUpload()

	// A stream's function runs now, after every middleware has decorated the head, and the
	// request's context stays live until it returns.
	streamed := streamFallback
	isStream := !panicked && sc.resp.set && sc.resp.stream != nil
	if isStream {
		streamed, panicked = c.runStream(&f)
	} else {
		sc.resp.dropStream() // a panic after the terminal step: the function will never run
	}

	ctx.Finish()
	gone := ctx.Cause() == reqctx.ErrClientGone
	// cur keeps the finished context until the next request replaces it, one atomic store a
	// request instead of two: a finished context cannot be armed and cancelling it again does
	// nothing, so onHangup and Shutdown are not misled. A connection that goes quiet, upgrades or
	// ends clears it (dropScratch, upgraded below, finish), so an idle one pins none of its text.
	c.disarm()
	ex.ctx = nil
	ex.slab, ex.slabReady = wire.Slab{}, false // drop the buffer: an idle connection must not pin a request's text

	// An upgrade (ADR 0036): the route accepted the handshake and its handler returned an Inbox.
	// A middleware may have changed the status since, and then the answer is that status.
	if w := ex.ws; w != nil {
		ex.ws = nil
		c.cur.Store(nil)
		sc.wnow = time.Time{}
		if !panicked && sc.resp.set && sc.resp.upgrade == w && sc.resp.status == 101 {
			ok := c.upgraded(w)
			// An Observer is told at the 101 (ADR 0051): the socket's life is not the request's.
			return c.ended(Ended{Status: 101, Upgraded: true, Abandoned: !ok}) && ok
		}
		w.Abort()
		sc.resp.upgrade = nil
	}

	r := &sc.resp
	if streamed == streamAbort {
		c.ended(Ended{Status: r.status, Streamed: true, Panicked: panicked, Err: f.err, Panic: f.val, Stack: f.stack, Abandoned: gone || ex.writeFailed})
		return false // bytes are out and no 500 can follow: closing is the only honest end
	}
	switch {
	case streamed == streamKeep || streamed == streamClose:
		// The response is on the wire already.
	case panicked || !r.set:
		if gone {
			// Nobody is waiting (ADR 0049): the engine's own 500 is not written, to a closed
			// socket or to one only half-closed, and the connection ends here. A panic was
			// already logged with its stack by call.
			ex.reqID, ex.reqIDHdr = "", ""
			c.ended(Ended{Status: r.Status(), Streamed: isStream, Panicked: panicked, Err: f.err, Panic: f.val, Stack: f.stack, Abandoned: true})
			return false
		}
		r.start(500)
		r.Header("Content-Type", "application/problem+json")
		if ex.reqID != "" && ex.reqIDHdr != "" {
			r.Header(ex.reqIDHdr, ex.reqID) // what the request-ID middleware would have added on the way out
		}
		r.body = appendProblem(r.body, 500, ex.reqID)
	}

	ex.reqID, ex.reqIDHdr = "", "" // the answer is built; an idle connection must not pin the id

	if streamed == streamKeep || streamed == streamClose {
		ok := c.ended(Ended{Status: r.status, Streamed: true, Abandoned: gone})
		if streamed == streamClose || !ok {
			c.lingerClose()
			return false
		}
		return true
	}

	keep := req.KeepAlive && !ex.closeAfter && !panicked && !s.shutting.Load()
	drain := int64(0)
	if keep && ex.bodyState == bodyUnread {
		switch {
		case req.Chunked, req.Expect100 && !ex.sent100:
			keep = false // the rest of the body cannot be found, or was never invited
		default:
			rest := req.ContentLength - int64(min(c.r1-c.r0, int(req.ContentLength)))
			if rest > maxDrain {
				keep = false
			}
			drain = rest
		}
	}
	wrote := c.write(keep)
	if !c.ended(Ended{Status: r.status, Streamed: isStream, Panicked: panicked, Err: f.err, Panic: f.val, Stack: f.stack, Abandoned: gone || !wrote}) {
		keep = false
	}
	if !wrote {
		return false
	}
	if !keep {
		c.lingerClose()
		return false
	}
	if ex.bodyState == bodyUnread && !c.skipBody(drain) {
		return false
	}
	return true
}

// ended tells the request's Observers, through the framework's Ender, that the request has ended
// (ADR 0051), and reports whether the connection may carry on. It does nothing for a request
// nobody observes. A panic that escapes the Ender is recovered and logged here, and closes the
// connection: the answer is already out, and the engine cannot tell what state was left.
func (c *conn) ended(e Ended) (ok bool) {
	ex := &c.ex
	en := ex.ender
	if en == nil {
		return true
	}
	if c.sc != nil {
		c.sc.wnow = time.Time{} // an Observer's End runs for as long as it runs: the next wait reads the clock
	}
	ex.ender = nil
	e.Read, e.Written = ex.readN, ex.wrote
	if e.Abandoned {
		e.Written = 0
	}
	defer func() {
		if v := recover(); v != nil {
			ok = false
			req := &c.sc.req
			c.srv.o.Logger.Error("panic in an Observer's End", "panic", v, "stack", string(debug.Stack()),
				"method", string(req.Method), "target", string(req.Target), "remote", c.nc.RemoteAddr().String())
		}
	}()
	return en.End(ex, e)
}

// skipBody discards the rest of an unread Content-Length body so the next request starts at
// its head: what is buffered first, then up to maxDrain more, briefly (ADR 0045).
func (c *conn) skipBody(rest int64) bool {
	sc := c.sc
	cl := sc.req.ContentLength
	c.r0 += min(c.r1-c.r0, int(cl))
	if rest <= 0 {
		return true
	}
	sc.wnow = time.Time{} // draining waits for the client: the next wait reads the clock
	if sc.aux == nil {
		sc.aux = make([]byte, bufSize)
	}
	c.setReadDeadline(c.srv.now().Add(drainTimeout))
	for rest > 0 {
		m, err := c.nc.Read(sc.aux[:min(int64(len(sc.aux)), rest)])
		rest -= int64(m)
		if err != nil && rest > 0 {
			return false
		}
	}
	return true
}

// recycle readies the connection for the next request: pipelined bytes move to the front of
// the buffer, which is kept; with none, the scratch is cleaned and kept for the next wait, which
// gives it back if the connection stays quiet (await, dropScratch).
func (c *conn) recycle() {
	c.ex.bodyState, c.ex.sent100, c.ex.closeAfter = bodyNone, false, false
	c.ex.body = nil // would pin the request's slab while the connection idles
	sc := c.sc
	if len(sc.auxRest) > 0 && c.r0 == c.r1 {
		c.r0, c.r1 = 0, copy(sc.buf, sc.auxRest)
		sc.auxRest = nil
		return
	}
	sc.auxRest = nil
	if c.r1 > c.r0 {
		c.r1 = copy(sc.buf, sc.buf[c.r0:c.r1])
		c.r0 = 0
		return
	}
	// Kept for the next wait, which reads into it for hotWait (await).
	c.srv.cleanScratch(sc)
	c.r0, c.r1 = 0, 0
}

// write builds the response once and writes it with one call (head and a small body in one
// buffer, a large body as a second buffer of the same writev).
func (c *conn) write(keep bool) bool {
	s := c.srv
	sc := c.sc
	r := &sc.resp
	req := &sc.req
	head := r.head
	payload := r.body
	if len(s.coders) > 0 {
		// ADR 0031, ADR 0061: the decision and the encoder are the engine's, made from the
		// finished response; the pooled buffer is given back once the bytes are written.
		var eb *encBuf
		head, eb = c.negotiate(head)
		if eb != nil {
			defer s.putEncBuf(eb)
			payload = eb.b
		}
	}
	now := s.now()
	head = r.appendLate(head)
	head = append(head, s.dateLine(now)...)
	hasBody := r.status >= 200 && r.status != 204 && r.status != 304
	if hasBody {
		head = append(head, "Content-Length: "...)
		head = appendInt(head, len(payload))
		head = append(head, '\r', '\n')
	}
	switch {
	case !keep:
		head = append(head, "Connection: close\r\n"...)
	case req.Minor == 0:
		head = append(head, "Connection: keep-alive\r\n"...)
	}
	head = append(head, '\r', '\n')
	var body []byte
	if hasBody && !bytes.Equal(req.Method, headMethod) {
		body = payload
	}
	// The write deadline stays armed between the requests of a busy connection and is moved only
	// when less than half of WriteTimeout is left, so a write has between half and all of it; it
	// is cleared when the connection goes quiet (dropScratch). Arming and clearing it on every
	// answer was two runtime-timer changes a request.
	if d := sc.wdl; d == 0 || d-now.UnixNano() < int64(s.o.WriteTimeout/2) {
		c.setWriteDeadline(now.Add(s.o.WriteTimeout))
	}
	var err error
	if len(body) <= 32<<10 {
		head = append(head, body...)
		var waited bool
		if waited, err = c.writeAnswer(head); !waited && err == nil {
			sc.wnow = now // the next wait starts from it (waitStart)
		}
	} else {
		// The two-slice vector lives in the scratch: a local net.Buffers escapes through WriteTo's
		// pointer receiver, two allocations an answer. It is cleared after so it pins nothing.
		sc.iov[0], sc.iov[1] = head, body
		sc.bufs = sc.iov[:]
		_, err = sc.bufs.WriteTo(c.nc)
		sc.iov, sc.bufs = [2][]byte{}, nil
	}
	r.head = head[:0]
	if err != nil {
		c.ex.writeFailed = true
		return false
	}
	c.ex.wrote = int64(len(body))
	return true
}

var headMethod = []byte("HEAD")

func appendInt(b []byte, n int) []byte {
	if n == 0 {
		return append(b, '0')
	}
	var t [20]byte
	i := len(t)
	for n > 0 {
		i--
		t[i] = byte('0' + n%10)
		n /= 10
	}
	return append(b, t[i:]...)
}

// lingerClose ends a connection after an answer the client may not have finished sending
// for. Closing a socket with unread bytes in its receive buffer sends a reset that can destroy
// the response before the client reads it, so a TCP connection half-closes and discards for a
// moment first. An in-memory connection has no reset to fear.
func (c *conn) lingerClose() {
	if t, ok := c.nc.(*tls.Conn); ok {
		t.CloseWrite() // close_notify: a clean end for the client, before the socket's half-close
	}
	// A TCP socket, and a Unix stream socket (ADR 0068): both half-close, and a Unix socket closed
	// with unread bytes resets its peer as TCP does. Each has its own loop so that the buffer stays
	// on the stack: a read through an interface would move it to the heap.
	until := c.srv.now().Add(lingerTime)
	switch sk := c.raw().(type) {
	case *net.TCPConn:
		sk.CloseWrite()
		sk.SetReadDeadline(until)
		var b [512]byte
		for total := 0; total < maxDrain; {
			n, err := sk.Read(b[:])
			total += n
			if err != nil {
				return
			}
		}
	case *net.UnixConn:
		sk.CloseWrite()
		sk.SetReadDeadline(until)
		var b [512]byte
		for total := 0; total < maxDrain; {
			n, err := sk.Read(b[:])
			total += n
			if err != nil {
				return
			}
		}
	}
}

// ---- reqctx.Watch ----

type peerCloser interface{ PeerClosed() <-chan struct{} }

// Arm is called the first time a handler looks at its context (ADR 0016).
func (c *conn) Arm(x *reqctx.Ctx) {
	w := c.w.Load()
	if w == nil {
		w = new(watchState)
		c.w.Store(w)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed || c.cur.Load() != x {
		return
	}
	raw := c.raw()
	if pc, ok := raw.(peerCloser); ok {
		stop := make(chan struct{})
		peer := pc.PeerClosed()
		w.mode, w.stop, w.armed = modeMem, stop, true
		go func() {
			select {
			case <-peer:
				x.Cancel(reqctx.ErrClientGone)
			case <-stop:
			}
		}()
		return
	}
	if sk, ok := socketOf(raw); ok && !c.srv.forceFallback {
		if ew := c.srv.getWatcher(); ew != nil {
			if w.raw == nil {
				if rc, err := sk.SyscallConn(); err == nil {
					w.raw = rc
				}
			}
			if w.raw != nil {
				if w.hangup == nil {
					w.hangup = c.onHangup
				}
				if id, err := ew.Watch(w.raw, w.hangup); err == nil {
					w.mode, w.watchID, w.armed = modeEpoll, id, true
					return
				}
			}
		}
	}
	c.armReader(w, x)
}

// onHangup runs on the watcher goroutine, under its lock: it must not take the state mutex.
func (c *conn) onHangup() {
	if x := c.cur.Load(); x != nil {
		x.Cancel(reqctx.ErrClientGone)
	}
}

// armReader is the portable fallback: a goroutine reads one byte. A byte means the client
// pipelined its next request, which is kept for the next read and ends the watching (the
// kernel cannot tell us more without consuming it); an error means the client left.
func (c *conn) armReader(w *watchState, x *reqctx.Ctx) {
	c.nc.SetReadDeadline(time.Time{})
	done := make(chan struct{})
	w.mode, w.fbDone, w.armed = modeReader, done, true
	w.fbStop.Store(false)
	go func() {
		defer close(done)
		n, err := c.nc.Read(w.pendBuf[:])
		if n == 1 {
			c.pending, c.hasPend = w.pendBuf[0], true
			return
		}
		if err != nil && !w.fbStop.Load() {
			x.Cancel(reqctx.ErrClientGone)
		}
	}()
}

// Disarm is called when the Ctx finishes; the engine also calls it, so it is idempotent.
func (c *conn) Disarm() { c.disarm() }

func (c *conn) disarm() {
	w := c.w.Load()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.armed {
		return
	}
	w.armed = false
	switch w.mode {
	case modeEpoll:
		if ew := c.srv.watcher.Load(); ew != nil {
			ew.Unwatch(w.watchID)
		}
	case modeMem:
		close(w.stop)
		w.stop = nil
	case modeReader:
		w.fbStop.Store(true)
		c.setReadDeadline(time.Unix(1, 0)) // interrupts the blocked read
		<-w.fbDone
		w.fbDone = nil
	}
	w.mode = modeNone
}
