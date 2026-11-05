package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"runtime"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nevindra/bites/internal/wheel"
	"github.com/nevindra/bites/internal/wire"
	"github.com/nevindra/bites/internal/ws"
)

// WebSockets in the engine (ADR 0035 to ADR 0040, ADR 0046, ADR 0047). The framework runs the
// route (the handshake check, the Origin, the Providers, the Input, the user's handler) and
// hands the engine a WS and a WSHandler; the engine writes the 101, owns the read loop, and
// calls the handler for each message. User code never reads from the connection.
//
// # Where a WebSocket waits for bytes
//
// In conn.await, the one place every connection waits (ADR 0005): the same one-byte read an idle
// HTTP connection makes, holding no buffer, for moveAfter on the goroutine that served the last
// message, after which the socket parks and that goroutine ends (park.go); a socket assembling a
// message moves to a fresh goroutine instead and keeps its read deadline. Bytes that arrive are
// read into a pooled scratch, fed to the ws.Reader, and the scratch is returned as soon as the
// Reader has consumed everything, even in the middle of a frame or a message. A WebSocket
// carries no read deadline while idle: dead peers are the heartbeat's (on the wheel), and a
// message that did not arrive whole has the deadline of MessageTimeout.
//
// # Who writes
//
// Never the read loop and never a goroutine kept for the socket (ADR 0037). Send and Broadcast
// try one non-blocking write on plain TCP when nothing is queued; whatever the kernel did not
// take, and everything on TLS and every control frame, goes to the socket's queue, and a flusher
// goroutine is started when there is something to send and none runs. It writes until the queue
// is empty under a write deadline, and ends.
//
// # Closing
//
// beginClose is the one way a Close frame leaves: it is sent once, the read loop then drops
// everything but the client's Close (so a data frame cannot reach a handler after the socket
// started closing), and the connection is dropped when the client answers, when the connection
// ends, or after CloseTimeout on the wheel. Shutdown (ADR 0029) reaches it through goAway, which
// sends 1001 to every WebSocket after a delay spread over a window.

// WSHandler is implemented by the framework for each WebSocket. Both methods run on the
// connection's goroutine, one message at a time and in order.
type WSHandler interface {
	// Message handles one complete message. msg is its own buffer, made for it and never reused
	// (ADR 0008), with room past its length that slab may use. slab is a header the engine keeps
	// in its pooled scratch and clears afterwards. An error closes the socket: a *WSError with its
	// status, any other with 1011.
	Message(w *WS, op ws.Opcode, msg []byte, slab *wire.Slab) error
	// Closed is called once, after the last Message, when the connection has ended.
	Closed(w *WS, info WSCloseInfo)
}

// WSCause says why a WebSocket ended (ADR 0063). The set is fixed, so it is safe as a metric
// label. The zero value is not a cause: the engine sets one on every socket that ends.
type WSCause uint8

// The causes a WebSocket ends with. The first to start the close stands.
const (
	// WSCauseClient: the client started the close handshake.
	WSCauseClient WSCause = iota + 1
	// WSCauseServer: the application closed it, with Socket.Close or a CloseError.
	WSCauseServer
	// WSCauseHandlerError: OnMessage or OnMalformed returned an error or panicked.
	WSCauseHandlerError
	// WSCauseQueueFull: a send overflowed the socket's queue or the app's send budget.
	WSCauseQueueFull
	// WSCausePongTimeout: the client missed too many pings in a row.
	WSCausePongTimeout
	// WSCauseShutdown: the server was shutting down.
	WSCauseShutdown
	// WSCauseClientFault: the client broke the protocol or a limit on what it may send.
	WSCauseClientFault
	// WSCauseLost: the connection ended with no close handshake.
	WSCauseLost
)

// String is the cause as a metric label and a log value: "client", "server", "handler_error",
// "queue_full", "pong_timeout", "shutdown", "client_fault", "lost", or "unknown" for the zero value.
func (c WSCause) String() string {
	switch c {
	case WSCauseClient:
		return "client"
	case WSCauseServer:
		return "server"
	case WSCauseHandlerError:
		return "handler_error"
	case WSCauseQueueFull:
		return "queue_full"
	case WSCausePongTimeout:
		return "pong_timeout"
	case WSCauseShutdown:
		return "shutdown"
	case WSCauseClientFault:
		return "client_fault"
	case WSCauseLost:
		return "lost"
	}
	return "unknown"
}

// WSWatcher is implemented by a WSHandler that wants to be told of a socket's life besides its
// messages and its end (ADR 0063). Both methods run on a goroutine of the engine's and must not
// block.
type WSWatcher interface {
	// Opened is called once the 101 is written; a socket whose 101 could not be written never opens
	// and never has Closed called.
	Opened(w *WS)
	// PongMissed is called when a heartbeat tick finds that the last ping went unanswered.
	PongMissed(w *WS)
}

// WSCloseInfo says how a WebSocket ended.
type WSCloseInfo struct {
	// Status is the close status: the client's when it closed, the server's when it did, 1006
	// when the connection ended with no close handshake.
	Status uint16
	// Cause is why it ended (ADR 0063). It shares the padding after Status, so it costs no memory.
	Cause WSCause
	// Reason is the text that came with the status.
	Reason string
	// Remote is true when the client started the close.
	Remote bool
	// Err is why a connection ended with no close handshake: the read or write error.
	Err error
}

// WSError is what a handler returns to close the socket with a status other than 1011.
type WSError struct {
	Code   uint16
	Reason string
	// Checked asks the engine to check Code and Reason before sending them, because they came
	// from user code: a status a server may not send, or a reason too long or not UTF-8, closes
	// with 1011 instead and is logged.
	Checked bool
}

func (e *WSError) Error() string { return fmt.Sprintf("websocket close %d: %s", e.Code, e.Reason) }

// WSConfig are the limits of ADR 0037, ADR 0039 and ADR 0046 that belong to the whole app. A
// zero field takes the default in its comment.
type WSConfig struct {
	MessageTimeout    time.Duration // 30 s
	HeartbeatInterval time.Duration // 30 s
	MissedPongs       int           // 2
	CloseTimeout      time.Duration // 5 s
	GoAwayPer         time.Duration // 1 ms: Shutdown's 1001 spread, per open socket (ADR 0029)
	GoAwaySpread      time.Duration // 3 s: the most the 1001 of Shutdown is spread over
	ReassemblyBudget  int64         // 64 MiB
	SendQueue         int64         // 1 MiB per socket
	SendBudget        int64         // 64 MiB for the app
}

func (c *WSConfig) defaults() {
	if c.MessageTimeout <= 0 {
		c.MessageTimeout = 30 * time.Second
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 30 * time.Second
	}
	if c.MissedPongs <= 0 {
		c.MissedPongs = 2
	}
	if c.CloseTimeout <= 0 {
		c.CloseTimeout = 5 * time.Second
	}
	if c.GoAwayPer <= 0 {
		c.GoAwayPer = time.Millisecond
	}
	if c.GoAwaySpread <= 0 {
		c.GoAwaySpread = 3 * time.Second
	}
	if c.ReassemblyBudget <= 0 {
		c.ReassemblyBudget = 64 << 20
	}
	if c.SendQueue <= 0 {
		c.SendQueue = 1 << 20
	}
	if c.SendBudget <= 0 {
		c.SendBudget = 64 << 20
	}
}

// WSSpec is what a route tells the engine about its WebSocket.
type WSSpec struct {
	// Limits are the route's inbound limits (ADR 0046); the engine keeps the pointer.
	Limits *ws.Limits
	// AcceptsBinary says the route's message type takes binary frames; any other route closes
	// with 1003 on the first binary frame (ADR 0036).
	AcceptsBinary bool
}

// The errors Send returns.
var (
	// ErrWSClosed is returned by a send on a socket that is closed, closing or never opened.
	ErrWSClosed = errors.New("bites: the socket is closed")
	// ErrWSQueueFull is returned by a send that would overflow the socket's queue or the app's
	// send budget: the socket is closed.
	ErrWSQueueFull = errors.New("bites: the socket's send queue is full")
)

// wsRestWait is how long the read loop waits for the rest of a frame whose first byte woke it.
// When the rest has not come the byte is fed alone, the scratch is returned, and the socket goes
// back to the one-byte wait, so a client that sends half a header pins nothing.
const wsRestWait = 100 * time.Millisecond

// wsLeave is a set of sockets (a Topic) the socket is in, and what to call when it ends. key
// is how Forget finds it again.
type wsLeave struct {
	key any
	fn  func(*WS)
}

// WS is one WebSocket. The framework creates it with Exchange.NewWS before it calls the user's
// handler, so the Socket it gives that handler has something to send on; nothing leaves before
// the 101 does. A WS is never reused for another connection (ADR 0047).
type WS struct {
	c    *conn
	h    WSHandler
	lim  *ws.Limits
	bin  bool
	rd   ws.Reader // the read loop's alone
	node wheel.Node
	ctx  WSContext

	flushFn func() // w.flush, made once so starting a flusher allocates nothing

	pong atomic.Bool   // a pong came since the last tick (ADR 0039)
	echo atomic.Uint32 // the status our Close carried, for AllowEcho

	// asm is the message being assembled and msgDue when it must be whole (UnixNano, 0 for none);
	// both are the read loop's.
	asm       []byte
	asmCharge int64
	msgDue    int64

	// Heartbeat state, the wheel's goroutine's alone.
	pingOut bool
	missed  int

	mu         sync.Mutex // guards what follows
	q, spare   [][]byte   // queued frames, and the array the flusher just emptied
	qbytes     int64      // bytes queued, plus the batch the flusher holds
	inflight   int64      // bytes in the flusher's batch
	flushing   bool
	partial    bool // q[0] is the rest of a frame already partly written: it is never dropped
	active     bool // the 101 is out
	closeSent  bool
	closeAfter bool // the flusher drops the connection once the queue is empty
	done       bool // the connection has ended
	leaving    bool // Shutdown has told the socket to go: its wheel node is the 1001's, not the heartbeat's

	info    WSCloseInfo
	infoSet bool
	in      *inliner // the machinery of the inline write, made on first use (ws_linux.go)
	inTried bool
	leave   []wsLeave
	readErr error
}

// NewWS makes the WS of a route whose handshake the framework has accepted. If the user's
// handler then fails, the framework calls Abort; if it succeeds, Upgrade.
func (ex *Exchange) NewWS(spec WSSpec) *WS {
	w := &WS{c: ex.c, lim: spec.Limits, bin: spec.AcceptsBinary}
	w.rd.Reset(spec.Limits)
	w.flushFn = w.flush
	w.node.Init(w.tick)
	ex.ws = w
	return w
}

// Abort ends a WS whose handler failed before the upgrade: every send on it fails from now on.
// It gives back what the handler took before failing, as end does for an upgraded socket: the
// topics it joined (or a refused handshake stays a subscriber for ever) and the bytes it queued
// against the app's SendBudget (or each refused handshake that sent first shrinks the budget for
// every socket). OnClose does not run: the socket never opened.
func (w *WS) Abort() {
	s := w.c.srv
	w.mu.Lock()
	w.done, w.closeSent = true, true
	leave := w.leave
	w.leave = nil
	if d := w.qbytes - w.inflight; d > 0 {
		s.sendUsed.Add(-d)
	}
	w.q, w.qbytes, w.partial = nil, w.inflight, false
	w.mu.Unlock()
	w.ctx.Cancel(ErrWSClosed)
	for _, l := range leave {
		l.fn(w)
	}
}

// Upgrade answers the handshake: the 101 is written after Serve returns and every middleware has
// decorated it (the status is checked again then), and the connection becomes a WebSocket served
// by h. key is the request's Sec-WebSocket-Key (ADR 0036). The WS is the one NewWS made.
func (ex *Exchange) Upgrade(w *WS, key []byte, h WSHandler) {
	w.h = h
	r := ex.Respond(101)
	r.Header("Upgrade", "websocket")
	r.Header("Connection", "Upgrade")
	var acc [32]byte
	r.Header("Sec-WebSocket-Accept", string(ws.AppendAccept(acc[:0], key)))
	r.upgrade = w
}

// Context is cancelled when the socket starts to close (ADR 0047).
func (w *WS) Context() context.Context { return &w.ctx }

// Logger is the logger of the server the socket belongs to.
func (w *WS) Logger() *slog.Logger { return w.c.srv.o.Logger }

// Join records that the socket is in the set key names, which fn is called to leave when the
// socket ends (a Topic removes it). A socket that has ended calls fn at once.
func (w *WS) Join(key any, fn func(*WS)) {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		fn(w)
		return
	}
	w.leave = append(w.leave, wsLeave{key, fn})
	w.mu.Unlock()
}

// GoAwayWindow is the window n sockets are spread over when one go-away must not make them all
// reconnect at once: the server's GoAwayPer a socket, at most its GoAwaySpread (ADR 0029).
func (w *WS) GoAwayWindow(n int) time.Duration {
	o := w.c.srv.o.WS
	return min(time.Duration(n)*o.GoAwayPer, o.GoAwaySpread)
}

// Forget undoes Join.
func (w *WS) Forget(key any) {
	w.mu.Lock()
	for i, x := range w.leave {
		if x.key == key {
			// Delete clears the slot the shift leaves at the end, which would otherwise keep the
			// last Topic this socket left alive for as long as the socket is.
			w.leave = slices.Delete(w.leave, i, i+1)
			break
		}
	}
	w.mu.Unlock()
}

// ---- the handshake's end ----

// upgraded is the end of handle() for a request whose route accepted the upgrade: the 101 is
// written and the connection becomes the WebSocket's.
func (c *conn) upgraded(w *WS) bool {
	s := c.srv
	r := &c.sc.resp
	// The heartbeat is armed before the client can see the 101, so its first ping is due an
	// interval after the handshake and not after whatever the server got to next.
	s.wsWheel().Add(&w.node, s.o.WS.HeartbeatInterval)
	head := append(r.head, '\r', '\n')
	c.setWriteDeadline(s.now().Add(s.o.WriteTimeout))
	_, err := c.nc.Write(head)
	c.setWriteDeadline(time.Time{}) // and the scratch records none: the flusher owns the deadline from here
	r.head = head[:0]
	if err != nil {
		s.wsWheel().Remove(&w.node)
		w.Abort()
		return false
	}
	// The connection is no longer a request's. active is set before the state, so a Shutdown that
	// finds stWS finds a socket that can send its Close and arm the close deadline.
	c.ws = w
	w.mu.Lock()
	w.active = true
	start := len(w.q) > 0 && !w.flushing
	if start {
		w.flushing = true
	}
	w.mu.Unlock()
	if start {
		go w.flushFn()
	}
	c.state.Store(stWS)
	if wt, ok := w.h.(WSWatcher); ok {
		wt.Opened(w)
	}
	// A handshake in flight when Shutdown starts (ADR 0029): the state is set and then the closing
	// flag is read, as Shutdown sets the flag and then walks, so one side sees the other. The 101
	// is already out, so the socket opens and is told to go like any other, spread over the same
	// window; if Shutdown has not yet fixed the window its walk finds this socket itself.
	if s.shutting.Load() {
		s.mu.Lock()
		set, win := s.leaveSet, s.leaveWin
		s.mu.Unlock()
		if set {
			w.goAway(s.goAwayDelay(win))
		}
	}
	return true
}

func (s *Server) wsWheel() *wheel.Wheel { return s.timers() }

// timers is the engine's one timer wheel (ADR 0039): every WebSocket's heartbeat and close
// deadline, and the idle timeout of every parked connection (park.go).
func (s *Server) timers() *wheel.Wheel {
	s.wheelOnce.Do(func() { s.wheel.Store(wheel.New(s.o.Clock, wheel.Config{})) })
	return s.wheel.Load()
}

// ---- reading ----

// messageDeadline is when the message being assembled must be whole, or the zero time.
func (w *WS) messageDeadline() time.Time {
	if w.msgDue == 0 {
		return time.Time{}
	}
	return time.Unix(0, w.msgDue)
}

func (w *WS) closing() bool {
	w.mu.Lock()
	v := w.closeSent
	w.mu.Unlock()
	return v
}

// goAwayTick is the wheel's resolution (its default): a delay shorter than a tick would only be
// rounded up to one, so the Close goes at once instead.
const goAwayTick = time.Second

// goAway is Shutdown's word to one socket (ADR 0029): send Close 1001 after delay. The wheel's
// node is re-used for the wait, so the socket carries nothing more; the heartbeat does not
// re-arm it once the socket is leaving. A socket already closing, or ended, is left alone.
func (w *WS) goAway(delay time.Duration) {
	w.mu.Lock()
	if w.leaving || w.closeSent || w.done {
		w.mu.Unlock()
		return
	}
	w.leaving = true
	if delay >= goAwayTick {
		w.c.srv.wsWheel().Add(&w.node, delay)
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.beginClose(ws.CloseGoingAway, "server is shutting down", WSCloseInfo{Status: ws.CloseGoingAway, Reason: "server is shutting down", Cause: WSCauseShutdown}, false, false)
}

// goAwayWindow is how long Shutdown spreads the 1001 of n sockets over: GoAwayPer a socket, at
// most GoAwaySpread, and at most half of what is left of ctx so that the closes finish inside it.
// It also returns what the window would have been without the context's half, so Shutdown can say
// when the context shortened it (ADR 0029).
func (s *Server) goAwayWindow(ctx context.Context, n int) (win, asked time.Duration) {
	win = min(time.Duration(n)*s.o.WS.GoAwayPer, s.o.WS.GoAwaySpread)
	asked = win
	if dl, ok := ctx.Deadline(); ok {
		win = min(win, time.Until(dl)/2)
	}
	return max(win, 0), asked
}

// goAwayDelay is one socket's share of the window: uniform in [0, win).
func (s *Server) goAwayDelay(win time.Duration) time.Duration {
	if win < goAwayTick {
		return 0
	}
	return time.Duration(rand.Int64N(int64(win)))
}

// wsInput feeds the bytes in the scratch to the Reader and acts on what it finds. It reports
// false when the connection is over. The scratch is returned when everything has been consumed.
func (c *conn) wsInput() bool {
	w := c.ws
	s := c.srv
	sc := c.sc
	if c.r1-c.r0 == 1 {
		// One byte woke the socket. The rest of the frame has usually been sent with it: take it
		// if it is there, and if it is not within a moment feed the byte alone and wait again with
		// nothing held.
		c.setReadDeadline(s.now().Add(wsRestWait))
		m, err := c.nc.Read(sc.buf[c.r1:])
		c.r1 += m
		if err != nil && m == 0 && !isTimeout(err) {
			w.readErr = err
			return false
		}
	}
	for c.r0 < c.r1 {
		if e := w.echo.Load(); e != 0 {
			w.rd.AllowEcho(uint16(e))
		}
		n, ev := w.rd.Feed(sc.buf[c.r0:c.r1], s.now())
		c.r0 += n
		if ev.Kind == ws.KindNeedMore {
			if n == 0 {
				c.r0 = c.r1
			}
			continue
		}
		if !w.event(ev) {
			return false
		}
		if ev.Kind == ws.KindFail {
			c.r0 = c.r1 // the Reader repeats its refusal for ever: the rest of the input is not read
		}
	}
	// Kept for the next wait, which reads into it for hotWait (await).
	s.cleanScratch(sc)
	c.r0, c.r1 = 0, 0
	return true
}

// event acts on one thing the Reader reported. It reports false when the connection should end
// now.
func (w *WS) event(ev ws.Event) bool {
	if w.closing() {
		// Our Close is out. Only the client's answer matters; data, pings and refusals are dropped.
		if ev.Kind == ws.KindClose {
			w.mu.Lock()
			after := w.closeAfter
			w.mu.Unlock()
			return after // answering a Close we did not start is the flusher's to end
		}
		return true
	}
	switch ev.Kind {
	case ws.KindData:
		w.data(ev)
	case ws.KindPing:
		if f, err := ws.AppendPong(make([]byte, 0, 2+len(ev.Payload)), ev.Payload); err == nil {
			w.control(f)
		}
	case ws.KindPong:
		w.pong.Store(true)
	case ws.KindClose:
		code := ev.Code
		info := WSCloseInfo{Status: code, Cause: WSCauseClient, Reason: string(ev.Payload), Remote: true}
		echo := code
		if code == ws.CloseNoStatus || !ws.ServerCloseCode(code) {
			echo = ws.CloseNormal
		}
		w.beginClose(echo, "", info, false, true)
	case ws.KindFail:
		w.beginClose(ev.Err.Code, ev.Err.Reason, WSCloseInfo{Status: ev.Err.Code, Cause: WSCauseClientFault, Reason: ev.Err.Reason}, false, false)
	}
	return true
}

// data takes a chunk of a message. A message that arrives whole in one read is copied once into
// a buffer of its own; one that does not is assembled in a buffer grown in size classes and
// charged to the app's reassembly budget (ADR 0046).
func (w *WS) data(ev ws.Event) {
	s := w.c.srv
	if ev.Start && ev.Opcode == ws.OpBinary && !w.bin {
		w.beginClose(ws.CloseUnsupported, "binary frames are not accepted on this route", WSCloseInfo{Status: ws.CloseUnsupported, Cause: WSCauseClientFault, Reason: "binary frame"}, false, false)
		return
	}
	if ev.Start && ev.End {
		// The handler's own copy (ADR 0008), made by append so the bytes are copied in without
		// being zeroed first: make then copy writes the message twice.
		msg := append([]byte(nil), ev.Payload...)
		w.deliver(ev.Opcode, msg)
		return
	}
	if ev.Start {
		w.asm = nil
		w.msgDue = s.now().Add(s.o.WS.MessageTimeout).UnixNano()
	}
	need := len(w.asm) + len(ev.Payload)
	if need > cap(w.asm) && !w.grow(need) {
		return
	}
	w.asm = append(w.asm, ev.Payload...)
	if ev.End {
		msg := w.asm
		w.asm = nil
		s.asmUsed.Add(-w.asmCharge)
		w.asmCharge = 0
		w.msgDue = 0
		w.deliver(ev.Opcode, msg)
	}
}

// firstClass is the size of the first assembly buffer, which no budget is charged for.
const firstClass = 4 << 10

// grow gives the assembly buffer room for need bytes, in classes four times apart from 4 KiB
// and never above the route's limit, charging the capacity held. It closes the socket with 1013
// and reports false when the budget is spent.
func (w *WS) grow(need int) bool {
	s := w.c.srv
	c := firstClass
	for c < need {
		c *= 4
	}
	c = min(c, w.lim.MaxMessage)
	c = max(c, need)
	charge := int64(0)
	if c > firstClass {
		charge = int64(c)
	}
	if d := charge - w.asmCharge; d > 0 && !s.charge(&s.asmUsed, d, s.o.WS.ReassemblyBudget) {
		s.asmUsed.Add(-w.asmCharge)
		w.asmCharge, w.asm, w.msgDue = 0, nil, 0
		w.beginClose(ws.CloseTryAgain, "too many messages are being received", WSCloseInfo{Status: ws.CloseTryAgain, Cause: WSCauseClientFault, Reason: "reassembly budget"}, false, false)
		return false
	} else if d < 0 {
		s.asmUsed.Add(d)
	}
	w.asmCharge = charge
	nb := make([]byte, len(w.asm), c)
	copy(nb, w.asm)
	w.asm = nb
	return true
}

// messageTimedOut is the read loop's answer to the deadline of a message that did not arrive
// whole (ADR 0046).
func (w *WS) messageTimedOut() {
	s := w.c.srv
	s.asmUsed.Add(-w.asmCharge)
	w.asmCharge, w.asm, w.msgDue = 0, nil, 0
	w.beginClose(ws.ClosePolicy, "message not finished in time", WSCloseInfo{Status: ws.ClosePolicy, Cause: WSCauseClientFault, Reason: "message timeout"}, false, false)
}

// deliver gives a message to the handler, recovering a panic (ADR 0028). An error closes the
// socket.
func (w *WS) deliver(op ws.Opcode, msg []byte) {
	sc := w.c.sc
	err := w.callMessage(op, msg, &sc.slab)
	sc.slab = wire.Slab{}
	if err == nil {
		return
	}
	var we *WSError
	if errors.As(err, &we) && we.Checked && (!ws.ServerCloseCode(we.Code) || len(we.Reason) > 123 || !utf8.ValidString(we.Reason)) {
		w.c.srv.o.Logger.Error("bites: a WebSocket handler returned a CloseError a server may not send; closing with 1011",
			"status", we.Code, "reason_bytes", len(we.Reason), "remote", w.c.nc.RemoteAddr().String())
		we = nil
	}
	if we != nil {
		cause := WSCauseClientFault // the framework's own refusal: a message to a socket that takes none, a malformed one
		if we.Checked {
			cause = WSCauseServer // the application chose the status with a CloseError
		}
		w.beginClose(we.Code, we.Reason, WSCloseInfo{Status: we.Code, Cause: cause, Reason: we.Reason, Err: err}, false, false)
		return
	}
	w.beginClose(ws.CloseInternal, "internal error", WSCloseInfo{Status: ws.CloseInternal, Cause: WSCauseHandlerError, Reason: "internal error", Err: err}, false, false)
}

var errHandlerPanic = errors.New("bites: a WebSocket handler panicked")

func (w *WS) callMessage(op ws.Opcode, msg []byte, slab *wire.Slab) (err error) {
	defer func() {
		if v := recover(); v != nil {
			w.c.srv.o.Logger.Error("panic in a WebSocket OnMessage", "panic", v, "stack", string(debug.Stack()), "remote", w.c.nc.RemoteAddr().String())
			err = errHandlerPanic
		}
	}()
	return w.h.Message(w, op, msg, slab)
}

// end is the last thing that happens to a WS, on the connection's goroutine from finish: the
// context is cancelled, the topics left, the budgets given back, the wheel's node removed and
// the handler's Closed called, once.
func (w *WS) end() {
	s := w.c.srv
	w.ctx.Cancel(ErrWSClosed)
	s.asmUsed.Add(-w.asmCharge)
	w.asmCharge, w.asm = 0, nil
	w.mu.Lock()
	already := w.done
	w.done = true
	w.mu.Unlock()
	// Removed after done is set: a tick running now re-arms the node only under mu and only while
	// the socket is not done, so either it sees done, or it armed before and this removes it.
	s.wsWheel().Remove(&w.node)
	if already {
		return
	}
	w.mu.Lock()
	if !w.infoSet {
		w.info = WSCloseInfo{Status: ws.CloseAbnormal, Cause: WSCauseLost, Err: w.readErr}
		if w.readErr == nil {
			w.info.Err = net.ErrClosed
		}
		w.infoSet = true
	}
	info := w.info
	leave := w.leave
	w.leave = nil
	if drop := w.qbytes - w.inflight; drop > 0 {
		s.sendUsed.Add(-drop)
	}
	w.q, w.qbytes, w.partial = nil, w.inflight, false
	w.mu.Unlock()
	for _, l := range leave {
		l.fn(w)
	}
	if w.h != nil {
		func() {
			defer func() {
				if v := recover(); v != nil {
					s.o.Logger.Error("panic in a WebSocket OnClose", "panic", v, "stack", string(debug.Stack()))
				}
			}()
			w.h.Closed(w, info)
		}()
	}
}

// ---- closing ----

// beginClose sends the Close frame, once: the first caller's status and info stand. drop
// discards the queue first (a reader that never reads would otherwise keep the Close behind a
// megabyte), after makes the flusher drop the connection once the frame is out (the client's own
// Close is being answered). The connection is dropped after CloseTimeout whatever happens.
func (w *WS) beginClose(code uint16, reason string, info WSCloseInfo, drop, after bool) {
	if len(reason) > 123 || !utf8.ValidString(reason) {
		reason = ""
	}
	frame, err := ws.AppendClose(make([]byte, 0, 4+len(reason)), code, reason)
	if err != nil {
		frame, _ = ws.AppendClose(nil, ws.CloseInternal, "")
	}
	s := w.c.srv
	w.mu.Lock()
	if w.closeSent || w.done {
		w.mu.Unlock()
		return
	}
	w.closeSent, w.closeAfter = true, after
	if !w.infoSet {
		w.info, w.infoSet = info, true
	}
	if drop {
		// A head that is the rest of a frame already partly on the wire stays: dropping it would
		// put the Close frame inside that frame's payload.
		keep := 0
		if w.partial && len(w.q) > 0 {
			keep = 1
		}
		var d int64
		for _, f := range w.q[keep:] {
			d += int64(len(f))
		}
		s.sendUsed.Add(-d)
		w.qbytes -= d
		clear(w.q[keep:])
		w.q = w.q[:keep]
	}
	start := w.queue(frame)
	if w.active {
		// Armed before the frame can leave, and under mu: a client that has read the Close can
		// rely on the deadline being set.
		s.wsWheel().Add(&w.node, s.o.WS.CloseTimeout)
	}
	w.mu.Unlock()
	w.echo.Store(uint32(code))
	w.ctx.Cancel(closeCause(code))
	if start {
		go w.flushFn()
	}
}

// closeCause is the cancel cause of a socket that started to close.
func closeCause(code uint16) error { return fmt.Errorf("%w (status %d)", ErrWSClosed, code) }

// Close starts the close handshake from the server's side with a status and reason the caller
// chose (Socket.Close). The status must be one a server may send.
func (w *WS) Close(code uint16, reason string) error {
	if !ws.ServerCloseCode(code) {
		return ws.ErrCloseStatus
	}
	if len(reason) > 123 {
		return ws.ErrControlTooLong
	}
	if !utf8.ValidString(reason) {
		return errors.New("bites: a close reason must be valid UTF-8")
	}
	w.mu.Lock()
	closed := w.closeSent || w.done
	w.mu.Unlock()
	if closed {
		return ErrWSClosed
	}
	w.beginClose(code, reason, WSCloseInfo{Status: code, Cause: WSCauseServer, Reason: reason}, false, false)
	return nil
}

// ---- heartbeat ----

// tick runs on the wheel's goroutine and must not block. In the closing state it is the close
// handshake's deadline; otherwise it is the heartbeat: a pong since the last tick resets the
// count, a ping that went unanswered adds to it, and the socket that has missed MissedPongs in a
// row is closed (ADR 0039).
func (w *WS) tick() {
	s := w.c.srv
	w.mu.Lock()
	closing, leaving := w.closeSent, w.leaving
	w.mu.Unlock()
	if closing {
		w.c.closeFromOutside(w.c.raw()) // the close handshake did not finish: drop the connection
		return
	}
	if leaving {
		// Shutdown's wait is over: this is the 1001, and beginClose arms the close deadline.
		w.beginClose(ws.CloseGoingAway, "server is shutting down", WSCloseInfo{Status: ws.CloseGoingAway, Reason: "server is shutting down", Cause: WSCauseShutdown}, false, false)
		return
	}
	if w.pingOut {
		if w.pong.Swap(false) {
			w.missed = 0
		} else {
			w.missed++
			if wt, ok := w.h.(WSWatcher); ok {
				wt.PongMissed(w)
			}
		}
	}
	if w.missed >= s.o.WS.MissedPongs {
		w.beginClose(ws.ClosePolicy, "no answer to ping", WSCloseInfo{Status: ws.ClosePolicy, Cause: WSCausePongTimeout, Reason: "ping timeout"}, true, false)
		return
	}
	ping, _ := ws.AppendPing(make([]byte, 0, 2), nil)
	w.control(ping)
	w.pingOut = true
	// Re-armed under mu and only while the socket is not done: end sets done under mu before it
	// removes the node, so a tick racing end cannot keep a dead socket on the wheel.
	w.mu.Lock()
	if !w.done && !w.leaving {
		if w.closeSent {
			// beginClose armed the node for the close handshake while this tick was running.
			s.wsWheel().Add(&w.node, s.o.WS.CloseTimeout)
		} else {
			s.wsWheel().Add(&w.node, s.o.WS.HeartbeatInterval)
		}
	}
	w.mu.Unlock()
}

// ---- writing ----

// Send queues a data frame, complete with its header, and never blocks. The frame is shared
// and immutable once given (ADR 0038): a broadcast hands the same slice to every socket. On
// plain TCP with nothing queued one non-blocking write is tried first; the rest, and everything
// on TLS, goes to the queue for the flusher. A socket whose queue would pass SendQueue is closed
// with 1008, and one that would pass the app's SendBudget with 1013.
func (w *WS) Send(frame []byte) error {
	_, err := w.SendFrame(frame)
	return err
}

// SendFrame is Send that also reports whether the engine kept frame: true when it was queued,
// whole or in part, and so belongs to the queue until it is written; false when it was written
// inline before SendFrame returned, or refused, so the caller may reuse it at once.
func (w *WS) SendFrame(frame []byte) (kept bool, err error) {
	s := w.c.srv
	w.mu.Lock()
	if w.closeSent || w.done {
		w.mu.Unlock()
		return false, ErrWSClosed
	}
	size := int64(len(frame))
	if w.qbytes+size > s.o.WS.SendQueue {
		w.mu.Unlock()
		w.beginClose(ws.ClosePolicy, "send queue full", WSCloseInfo{Status: ws.ClosePolicy, Cause: WSCauseQueueFull, Reason: "send queue full"}, true, false)
		return false, ErrWSQueueFull
	}
	if w.active && !w.flushing && len(w.q) == 0 && w.canInline() {
		n, err := w.in.write(frame)
		if err != nil {
			w.mu.Unlock()
			w.c.closeFromOutside(w.c.raw())
			return false, ErrWSClosed
		}
		if n == len(frame) {
			w.mu.Unlock()
			return false, nil
		}
		// Part of the frame is on the wire: the rest must follow it whole, before anything else,
		// so it is queued whatever the budget says, and marked so that a close dropping the queue
		// keeps it. A budget it overdraws refuses the next Send instead.
		rest := frame[n:]
		w.q = append(w.q, rest)
		w.qbytes += int64(len(rest))
		w.partial = true
		s.sendUsed.Add(int64(len(rest)))
		start := w.wantFlusher()
		w.mu.Unlock()
		if start {
			go w.flushFn()
		}
		return true, nil
	}
	if !s.charge(&s.sendUsed, size, s.o.WS.SendBudget) {
		w.mu.Unlock()
		w.beginClose(ws.CloseTryAgain, "too many messages are waiting to be sent", WSCloseInfo{Status: ws.CloseTryAgain, Cause: WSCauseQueueFull, Reason: "send budget"}, true, false)
		return false, ErrWSQueueFull
	}
	start := w.queueCharged(frame, size)
	w.mu.Unlock()
	if start {
		go w.flushFn()
	}
	return true, nil
}

// control queues a ping, pong or other control frame: not counted against the queue's bound,
// not written inline. A closed socket drops it.
func (w *WS) control(frame []byte) {
	w.mu.Lock()
	if w.closeSent || w.done {
		w.mu.Unlock()
		return
	}
	start := w.queue(frame)
	w.mu.Unlock()
	if start {
		go w.flushFn()
	}
}

// queue appends a frame that is not charged to the budget (control frames); the caller holds
// mu. It reports whether the caller must start the flusher.
func (w *WS) queue(frame []byte) bool {
	w.q = append(w.q, frame)
	w.qbytes += int64(len(frame))
	w.c.srv.sendUsed.Add(int64(len(frame)))
	return w.wantFlusher()
}

func (w *WS) queueCharged(frame []byte, size int64) bool {
	w.q = append(w.q, frame)
	w.qbytes += size
	return w.wantFlusher()
}

func (w *WS) wantFlusher() bool {
	if w.active && !w.flushing {
		w.flushing = true
		return true
	}
	return false
}

// flush writes the queue until it is empty, under the write timeout, and ends. It is the only
// writer besides the inline try, which runs only when no flusher does.
func (w *WS) flush() {
	c := w.c
	s := c.srv
	for {
		w.mu.Lock()
		batch := w.q
		if len(batch) == 0 {
			w.flushing = false
			after := w.closeAfter && w.closeSent
			sent := w.closeSent
			if cap(w.spare) > 64 {
				w.spare = nil
			}
			w.mu.Unlock()
			switch {
			case after:
				c.closeFromOutside(c.nc) // the client's Close is answered: the connection is done
			case sent:
				if tc, ok := c.raw().(*net.TCPConn); ok {
					tc.CloseWrite() // the client sees the end of our frames and closes
				}
			}
			return
		}
		w.q, w.spare = w.spare[:0], nil
		w.partial = false // the head, partial or not, is in this batch and is written whole
		var bytes int64
		for _, f := range batch {
			bytes += int64(len(f))
		}
		w.inflight = bytes
		w.mu.Unlock()

		c.nc.SetWriteDeadline(s.now().Add(s.o.WriteTimeout))
		var err error
		if len(batch) == 1 {
			_, err = c.nc.Write(batch[0])
		} else {
			bufs := net.Buffers(batch)
			_, err = bufs.WriteTo(c.nc)
		}
		c.nc.SetWriteDeadline(time.Time{}) // an armed deadline is a runtime timer an idle socket would carry
		clear(batch)

		w.mu.Lock()
		w.qbytes -= bytes
		w.inflight = 0
		if cap(batch) <= 64 {
			w.spare = batch[:0]
		}
		w.mu.Unlock()
		s.sendUsed.Add(-bytes)
		if err != nil {
			w.mu.Lock()
			w.flushing = false
			if d := w.qbytes; d > 0 {
				s.sendUsed.Add(-d)
				w.qbytes = 0
			}
			w.q = nil
			w.mu.Unlock()
			c.closeFromOutside(c.raw()) // a write that failed or timed out ends the socket; the read loop sees it
			return
		}
	}
}

// ---- helpers ----

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// charge adds n to *used unless that would pass max.
func (s *Server) charge(used *atomic.Int64, n, max int64) bool {
	for {
		cur := used.Load()
		if cur+n > max {
			return false
		}
		if used.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// ---- the socket's context ----

// WSContext is the context of a WebSocket: cancelled when the socket starts to close. It is
// smaller than reqctx.Ctx because it is held for every idle socket (ADR 0003); it has no
// deadline and carries no values.
type WSContext struct {
	mu    sync.Mutex
	done  chan struct{}
	gone  atomic.Bool
	cause error
}

func (c *WSContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *WSContext) Value(any) any               { return nil }

func (c *WSContext) Err() error {
	if c.gone.Load() {
		return context.Canceled
	}
	return nil
}

// Done returns the channel closed when the socket starts to close; it is made on first use.
func (c *WSContext) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil {
		c.done = make(chan struct{})
		if c.gone.Load() {
			close(c.done)
		}
	}
	return c.done
}

// Cause is why the context was cancelled, or nil.
func (c *WSContext) Cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cause
}

// Cancel cancels the context with cause; the first cause stands.
func (c *WSContext) Cancel(cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone.Load() {
		return
	}
	c.cause = cause
	c.gone.Store(true)
	if c.done != nil {
		close(c.done)
	}
}

// ---- broadcast ----

// broadcastInline is the most sockets a Broadcast handles on the caller's goroutine.
const broadcastInline = 64

// Broadcast sends one framed message to every socket in targets (ADR 0038), never blocking: each
// socket takes it through Send, so a slow one is queued or closed and cannot hold the others.
// Many sockets are walked by a few workers at once, each writing inline where it can, which is
// what keeps 20,000 sockets from needing 20,000 goroutines (ADR 0037). The error is the first
// that a socket other than a closed one returned.
func Broadcast(targets []*WS, frame []byte) {
	if len(targets) <= broadcastInline {
		for _, t := range targets {
			t.Send(frame)
		}
		return
	}
	k := min(runtime.GOMAXPROCS(0), 4)
	var wg sync.WaitGroup
	per := (len(targets) + k - 1) / k
	for i := 0; i < len(targets); i += per {
		part := targets[i:min(i+per, len(targets))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, t := range part {
				t.Send(frame)
			}
		}()
	}
	wg.Wait()
}
