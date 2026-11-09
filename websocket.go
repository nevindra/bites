package bites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/openapi"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/wire"
	"github.com/nevindra/bites/internal/ws"
)

// WebSockets (ADR 0035 to ADR 0040, ADR 0046, ADR 0047). A WebSocket route is registered like a
// typed route and its handshake is an HTTP request that goes through the same pipeline, so the
// Origin is checked first and then the middleware, the Providers and the Input run as for any
// GET, and the handler's error is the answer, all before the 101.

// ErrSocketClosed is what a Socket returns once its connection is closed or closing, and what a
// Socket kept after its connection ended always returns: a Socket is never reused for another
// connection (ADR 0047).
var ErrSocketClosed = engine.ErrWSClosed

// CloseError is an error OnMessage or OnMalformed returns to close the socket with a status of its
// own instead of 1011 (ADR 0047). It is found through wrapping with errors.As, as a StatusError is
// (ADR 0049). The status must be one a server may send (1000 to 1003, 1007 to 1014, 3000 to 4999)
// and the reason at most 123 bytes of UTF-8; one that is not cannot be caught before the error is
// returned, so the socket is then closed with 1011 and the mistake logged.
//
//	return &bites.CloseError{Status: 4001, Reason: "token expired"}
type CloseError struct {
	// Status is the close status sent to the client: 1000 to 1003, 1007 to 1014 or 3000 to 4999.
	Status uint16
	// Reason is the text sent to the client, at most 123 bytes of UTF-8.
	Reason string
}

func (e *CloseError) Error() string {
	return "bites: close the socket with " + strconv.Itoa(int(e.Status)) + ": " + e.Reason
}

// closeError gives the engine the status of a CloseError anywhere in err's chain.
func closeError(err error) error {
	if err == nil {
		return nil
	}
	var ce *CloseError
	if errors.As(err, &ce) {
		return &engine.WSError{Code: ce.Status, Reason: ce.Reason, Checked: true}
	}
	return err
}

// ErrSendQueueFull is what Send returns when the message would overflow the socket's send queue
// (Limits.SendQueue) or the app's (Limits.SendBudget); the socket is closed (ADR 0037).
var ErrSendQueueFull = engine.ErrWSQueueFull

// ErrMalformedMessage is matched by errors.Is on the error an Inbox's OnMalformed receives.
var ErrMalformedMessage = errors.New("bites: malformed message")

// MalformedError is why a message did not decode into the route's message type: the located
// errors, in the message's wire names.
type MalformedError struct {
	// Errors are the located decoding errors, each at message.<field>.
	Errors []FieldError
}

func (e *MalformedError) Error() string {
	if len(e.Errors) == 0 {
		return ErrMalformedMessage.Error()
	}
	return ErrMalformedMessage.Error() + ": " + e.Errors[0].Error()
}

func (e *MalformedError) Is(target error) bool { return target == ErrMalformedMessage }

// CloseInfo says how a WebSocket ended: the status and reason (the client's when it closed, the
// server's when the server did, 1006 for a connection that ended with no close handshake), whether
// the client started the close, and the error that ended an abnormal one.
//
// Its fields (go doc does not list a field through an alias, so they are here):
//
//	Status uint16   the close status: the client's when it closed, the server's when it did, 1006 with no close handshake
//	Reason string   the text that came with the status
//	Remote bool     true when the client started the close
//	Cause  CloseCause  why it ended: CauseClient, CauseServer, CauseHandlerError, CauseQueueFull, CausePongTimeout, CauseShutdown, CauseClientFault or CauseLost
//	Err    error    why a connection ended with no close handshake: the read or write error
type CloseInfo = engine.WSCloseInfo

// CloseCause says why a WebSocket ended (ADR 0063): it is CloseInfo.Cause, and what an Observer's
// SocketClosed is told. The set is fixed, so String, its one method, is safe as a metric label:
// "client", "server", "handler_error", "queue_full", "pong_timeout", "shutdown", "client_fault" or
// "lost". The zero value is not a cause.
type CloseCause = engine.WSCause

// The causes a WebSocket ends with. The first thing to start the close stands: a client close that
// arrives after the server's own is not the cause.
const (
	// CauseClient: the client started the close handshake.
	CauseClient = engine.WSCauseClient
	// CauseServer: the application closed it, with Socket.Close or a CloseError.
	CauseServer = engine.WSCauseServer
	// CauseHandlerError: OnMessage or OnMalformed returned an error or panicked (close status 1011).
	CauseHandlerError = engine.WSCauseHandlerError
	// CauseQueueFull: a send overflowed the socket's queue (Limits.SendQueue) or the app's send
	// budget (Limits.SendBudget), the case Send reports as ErrSendQueueFull. A reader too slow.
	CauseQueueFull = engine.WSCauseQueueFull
	// CausePongTimeout: the client missed Limits.MissedPongs pings in a row.
	CausePongTimeout = engine.WSCausePongTimeout
	// CauseShutdown: the server was shutting down (close status 1001).
	CauseShutdown = engine.WSCauseShutdown
	// CauseClientFault: the client broke the protocol or a limit on what it may send: a message
	// too large or unfinished, a binary frame to a text route, a malformed message with no
	// OnMalformed, a message to a socket that takes none.
	CauseClientFault = engine.WSCauseClientFault
	// CauseLost: the connection ended with no close handshake (status 1006).
	CauseLost = engine.WSCauseLost
)

// Inbox is what a WebSocket route's handler returns on upgrade: the functions the engine calls
// with the socket's messages and its close, and through their closures the socket's state
// (ADR 0035, ADR 0047). The engine owns the read loop; there is no Read for user code.
//
// A function that is nil is skipped, with two exceptions: a malformed message with no OnMalformed
// closes the socket with 1007, and any message to an Inbox with no OnMessage closes it with 1003
// and the reason "this socket takes no messages". A socket that only sends leaves OnMessage nil
// and takes struct{} as its Msg.
type Inbox[Msg any] struct {
	// OnMessage receives each message, decoded from JSON into Msg (a []byte Msg receives the
	// bytes of a text or binary frame, a Raw[T] keeps the exact bytes). It runs for one socket's
	// messages one at a time and in order, on the connection's own goroutine, and may block: that
	// pauses reading from this socket alone, which is the inbound backpressure. An error closes
	// the socket with 1011; so does a panic, which is recovered (ADR 0028). The context is
	// cancelled when the socket starts to close. Nil means the socket only sends: a message from
	// the client then closes it with 1003.
	OnMessage func(ctx context.Context, msg Msg) error
	// OnMalformed receives a message that did not decode: its bytes, which are its own, and the
	// *MalformedError. Returning nil drops the message and keeps the socket; an error closes it
	// with 1011. Without it, a malformed message closes the socket with 1007.
	OnMalformed func(ctx context.Context, raw []byte, err error) error
	// OnClose is called once, after the last OnMessage, when the connection has ended.
	OnClose func(info CloseInfo)
}

// Socket is the handle a WebSocket route's handler is given for sending to one client (ADR 0047).
// It is safe to use from any goroutine and safe to keep: it is never reused for another
// connection, so a Socket kept after its connection closed only returns ErrSocketClosed.
type Socket[Out any] struct {
	w    *engine.WS
	enc  *jsoncodec.Encoder // nil when Out is []byte
	bin  bool               // Out is []byte: a binary frame
	hint *atomic.Int32      // the size of the largest message sent on this route, to size the next one
}

// Send encodes v on the calling goroutine and queues the frame; it never blocks. An Out of []byte
// is sent as a binary frame and every other Out as a text frame holding its JSON (ADR 0036). It
// fails with ErrSocketClosed once the socket is closing or closed, and with ErrSendQueueFull,
// closing the socket with 1008, when its queue would pass Limits.SendQueue (ADR 0037). A value of
// a tagged union whose present variant its ServerOnly method names is an error and nothing is
// sent: that variant is only the server's (ADR 0083).
func (s *Socket[Out]) Send(v Out) error {
	if s == nil || s.w == nil {
		return ErrSocketClosed
	}
	fb := framePool.Get().(*frameBuf)
	f, buf, err := s.frame(fb.b, &v)
	if err != nil {
		framePool.Put(fb)
		return err
	}
	kept, err := s.w.SendFrame(f)
	switch {
	case kept:
		fb.b = nil // the queue owns it until it is written
	case cap(buf) <= maxPooledFrame:
		fb.b = buf[:0]
	default:
		fb.b = nil
	}
	framePool.Put(fb)
	return err
}

// framePool holds the buffers Send builds a frame in. A frame written inline, the usual case,
// is on the wire before SendFrame returns, so its buffer is used again for the next Send instead
// of being left to the collector; a frame the engine queues keeps its buffer. A buffer never
// reaches the application (ADR 0008).
var framePool = sync.Pool{New: func() any { return new(frameBuf) }}

type frameBuf struct{ b []byte }

// maxPooledFrame is the largest frame buffer kept in framePool.
const maxPooledFrame = 64 << 10

// Close starts the close handshake with a status and a reason. The status must be one a server
// may send (1000 to 1003, 1007 to 1014, 3000 to 4999) and the reason at most 123 bytes of UTF-8.
// Later sends fail with ErrSocketClosed; the connection ends when the client answers or after
// five seconds.
func (s *Socket[Out]) Close(status uint16, reason string) error {
	if s == nil || s.w == nil {
		return ErrSocketClosed
	}
	return s.w.Close(status, reason)
}

// Context is cancelled when the socket starts to close.
func (s *Socket[Out]) Context() context.Context {
	if s == nil || s.w == nil {
		return closedContext
	}
	return s.w.Context()
}

var closedContext = func() context.Context {
	c, cancel := context.WithCancel(context.Background())
	cancel()
	return c
}()

// frame builds the complete frame for v in into, or in a buffer of its own when into is too small:
// the header in front of the payload in one buffer, which it returns beside the frame.
// The payload is encoded after ten reserved bytes and the header is written right before it, so
// whatever its length needs, the frame is a slice of that one buffer.
func (s *Socket[Out]) frame(into []byte, v *Out) (frame, buf []byte, err error) {
	const room = 10
	op := ws.OpText
	if s.bin {
		p := *(*[]byte)(unsafe.Pointer(v))
		buf = sized(into, room+len(p))
		buf = append(buf, p...)
		op = ws.OpBinary
	} else {
		buf = sized(into, room+int(s.hint.Load()))
		if buf, err = s.enc.Append(buf, noescapeP(unsafe.Pointer(v))); err != nil {
			return nil, nil, err
		}
		if n := int32(len(buf) - room); n > s.hint.Load() && n <= 4096 {
			s.hint.Store(n)
		}
	}
	n := len(buf) - room
	f := buf[room-ws.HeaderLen(n):]
	ws.AppendHeader(f[:0], true, op, n)
	return f, buf, nil
}

// sized returns into, or a new buffer when into has less capacity than n, holding the ten
// bytes reserved for the header.
func sized(into []byte, n int) []byte {
	if cap(into) < n {
		into = make([]byte, 0, n)
	}
	return into[:10]
}

// Topic is a set of Sockets that one published message reaches (ADR 0047, ADR 0038), and of the
// functions Listen added, so the server's own code hears what the Sockets hear (a cache that drops
// an entry on every instance at once). The zero value is ready and must not be copied. Publish
// encodes and frames the message once and queues the same frame on every subscriber (a message
// that holds a server-only variant of a union, ADR 0083, is not framed: its Sockets do not get it
// and a brokered Topic logs a Warn naming the Topic and the variant, while Listen functions get it
// still); a Socket leaves every Topic it is in when it closes, so nothing of the application's leaks a closed
// socket. A Topic is safe for concurrent use. Every Socket of a Topic must be a Socket[Out] of the
// same app: the first one's encoder is used.
type Topic[Out any] struct {
	mu      sync.Mutex
	subs    map[*engine.WS]*Socket[Out]
	skips   map[*engine.WS]func(Out) bool // SubscribeUnless's functions, only for the Sockets that have one
	fns     map[*listener[Out]]struct{}
	leaveFn func(*engine.WS)
	pool    sync.Pool                             // *snapshot[Out], the list of sockets a Publish walks
	link    *brokerLink                           // nil for a zero Topic; set by NewTopic (ADR 0081)
	resolve atomic.Pointer[func(Out) (Out, bool)] // Resolve's function, nil when none
	onEmpty func()                                // a Topics' own: called when the last Socket or function leaves
}

// listener is one function Listen added; its address is its identity, so the same function
// listened to twice is two listeners, each stopped by its own stop.
type listener[Out any] struct{ fn func(Out) }

// snapshot is held by pointer so putting it back in the pool does not allocate. skip is filled,
// beside ws, only when a Socket of the Topic has a SubscribeUnless function.
type snapshot[Out any] struct {
	ws   []*engine.WS
	skip []func(Out) bool
}

func (t *Topic[Out]) leave() func(*engine.WS) {
	if t.leaveFn == nil {
		t.leaveFn = func(w *engine.WS) {
			t.mu.Lock()
			delete(t.subs, w)
			delete(t.skips, w)
			t.mu.Unlock()
			t.changed()
		}
	}
	return t.leaveFn
}

// changed follows a Socket or a function joining or leaving: a Topic made with NewTopic listens to
// its broker while it has one, and a Topic of a Topics is dropped when it has none.
func (t *Topic[Out]) changed() {
	if t.link != nil {
		t.link.sync(t.count)
	}
	if t.onEmpty != nil && t.count() == 0 {
		t.onEmpty()
	}
}

// Subscribe adds s to the topic, every message reaching it. Subscribing twice, or a Socket that
// never opened or has closed, does nothing; subscribing a Socket that SubscribeUnless added takes
// its function away.
func (t *Topic[Out]) Subscribe(s *Socket[Out]) {
	t.subscribe(s, nil)
}

// SubscribeUnless adds s to the topic with a function that decides, for each message, whether s
// is left out: skip returns true and s does not get that message, which the other Sockets still
// get. It is for what differs from one Socket to the next on a Topic they share, such as the
// people a User muted or blocked: skip reads the User's current state, so muting needs no new
// subscription. The message is still encoded and framed once for every Socket that gets it.
//
// skip runs for each message on the goroutine that publishes (on a Topic made with NewTopic, the
// broker's), outside the Topic's lock, so it must be quick and must not block; it must not change
// v. A panic in skip is recovered and logged at Error, and s does not get that message: a filter
// that fails leaves the Socket out rather than letting through what it was meant to hold back.
// Calling it again replaces the function, and Subscribe takes it away. It panics when skip is
// nil: use Subscribe.
func (t *Topic[Out]) SubscribeUnless(s *Socket[Out], skip func(v Out) bool) {
	if skip == nil {
		panic("bites: Topic.SubscribeUnless: skip is nil: give the function that leaves this socket out of a message, or use Subscribe for every message")
	}
	t.subscribe(s, skip)
}

func (t *Topic[Out]) subscribe(s *Socket[Out], skip func(Out) bool) {
	if s == nil || s.w == nil {
		return
	}
	t.mu.Lock()
	if t.subs == nil {
		t.subs = map[*engine.WS]*Socket[Out]{}
	}
	_, had := t.subs[s.w]
	t.subs[s.w] = s
	if skip != nil {
		if t.skips == nil {
			t.skips = map[*engine.WS]func(Out) bool{}
		}
		t.skips[s.w] = skip
	} else if t.skips != nil {
		delete(t.skips, s.w)
	}
	fn := t.leave()
	t.mu.Unlock()
	if !had {
		s.w.Join(t, fn) // a socket that has ended leaves again at once
	}
	if t.link != nil {
		t.link.sync(t.count)
	}
}

// Unsubscribe removes s from the topic.
func (t *Topic[Out]) Unsubscribe(s *Socket[Out]) {
	if s == nil || s.w == nil {
		return
	}
	t.mu.Lock()
	_, had := t.subs[s.w]
	delete(t.subs, s.w)
	delete(t.skips, s.w)
	t.mu.Unlock()
	if had {
		s.w.Forget(t)
	}
	t.changed()
}

// Listen calls fn with every message the topic delivers, until the stop it returns is called: on a
// Topic made with NewTopic, every message the broker delivers from any instance, this one's
// included, in the broker's order; on a zero Topic, every message published in this process. It is
// how the server's own code hears a Topic, as a cache that drops an entry on every instance when
// one of them publishes its key; TopicOptions.OnGap then says when messages may have been lost, and
// a cache clears itself there. A Topic made with NewTopic listens to its broker while it has a
// Socket or a function, so a Topic with only functions needs no Socket.
//
// fn runs on the goroutine that delivers and must not block: on a Topic made with NewTopic that is
// the broker's, one message after another, so a slow fn holds back every later message of the
// topic on this instance; on a zero Topic it is the goroutine that called Publish, so two Publish
// calls at once call fn at once. Every function and Socket gets the same value, so fn must not
// change what a slice or map in it holds. A panic in fn is recovered and logged at Error with the
// topic's name, and the next message still reaches it. stop may be called more than once and from
// any goroutine, fn included; a message being delivered while stop runs may still reach fn, and
// none that is delivered after it returns does. Listen panics when fn is nil.
func (t *Topic[Out]) Listen(fn func(Out)) (stop func()) {
	if fn == nil {
		panic("bites: Topic.Listen: fn is nil: give the function that receives each message")
	}
	l := &listener[Out]{fn: fn}
	t.mu.Lock()
	if t.fns == nil {
		t.fns = map[*listener[Out]]struct{}{}
	}
	t.fns[l] = struct{}{}
	t.mu.Unlock()
	if t.link != nil {
		t.link.sync(t.count)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.fns, l)
			t.mu.Unlock()
			t.changed()
		})
	}
}

// Len is the number of Sockets subscribed; the functions Listen added are not counted.
func (t *Topic[Out]) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs)
}

// count is what keeps a Topic made with NewTopic listening to its broker: its Sockets and its
// functions.
func (t *Topic[Out]) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs) + len(t.fns)
}

// Publish sends v to every subscriber and never blocks: a subscriber whose queue is full is
// closed (ADR 0037) and does not hold the others. The error is the encoding's.
//
// On a Topic made with NewTopic, Publish hands v to the broker instead, and returns what the
// broker says (it may block for the broker's round trip, bounded by the broker's own timeout):
// the Sockets of every instance, this one's included, receive v when the broker delivers it, so
// a message published here and by another instance arrives in one order everywhere (ADR 0081).
func (t *Topic[Out]) Publish(v Out) error {
	if t.link != nil {
		return t.link.publish(noescapeP(unsafe.Pointer(&v)))
	}
	return t.PublishLocal(v)
}

// PublishLocal sends v to the subscribers in this process only, never through the broker, with
// Publish's guarantees of one frame and no blocking for the Sockets. On a Topic without a broker
// it is Publish. It is for a message about this instance, such as telling its Sockets to reload
// after a gap. The functions Listen added are called with v too, after the Sockets have it queued.
func (t *Topic[Out]) PublishLocal(v Out) error {
	t.mu.Lock()
	if len(t.fns) > 0 {
		defer t.callListeners(v)
	}
	n := len(t.subs)
	if n == 0 {
		t.mu.Unlock()
		return nil
	}
	snap, ok := t.pool.Get().(*snapshot[Out])
	if !ok {
		snap = &snapshot[Out]{}
	}
	targets, skips := snap.ws[:0], snap.skip[:0]
	var first *Socket[Out]
	for w, s := range t.subs {
		targets = append(targets, w)
		first = s
	}
	if len(t.skips) > 0 {
		for _, w := range targets {
			skips = append(skips, t.skips[w])
		}
	}
	t.mu.Unlock()
	var err error
	if kept := t.unskipped(targets, skips, v); len(kept) > 0 {
		var f []byte
		f, _, err = first.frame(nil, &v) // its own buffer: every subscriber's queue may keep it
		if err == nil {
			engine.Broadcast(kept, f)
		}
	}
	clear(targets)
	clear(skips)
	snap.ws, snap.skip = targets, skips
	t.pool.Put(snap)
	return err
}

// unskipped is targets without the Sockets whose SubscribeUnless function leaves them out of v,
// in place; skips is empty when no Socket has one, or holds each target's function or nil.
func (t *Topic[Out]) unskipped(targets []*engine.WS, skips []func(Out) bool, v Out) []*engine.WS {
	if len(skips) == 0 {
		return targets
	}
	kept := targets[:0]
	for i, w := range targets {
		if f := skips[i]; f == nil || !t.runSkip(f, v) {
			kept = append(kept, w)
		}
	}
	return kept
}

// runSkip calls a SubscribeUnless function; a panic is logged and leaves the Socket out.
func (t *Topic[Out]) runSkip(f func(Out) bool, v Out) (skip bool) {
	defer func() {
		if r := recover(); r != nil {
			t.logger().Error("bites: a function given to Topic.SubscribeUnless panicked; the socket was left out of the message", "topic", t.name(), "panic", r)
			skip = true
		}
	}()
	return f(v)
}

func (t *Topic[Out]) logger() *slog.Logger {
	if t.link != nil {
		return t.link.log
	}
	return slog.Default()
}

func (t *Topic[Out]) name() string {
	if t.link != nil {
		return t.link.name
	}
	return ""
}

// callListeners calls each function Listen added with v, taking the list under the lock and
// calling outside it, so a function may call Listen, stop or Publish.
func (t *Topic[Out]) callListeners(v Out) {
	var buf [4]*listener[Out]
	t.mu.Lock()
	ls := buf[:0]
	for l := range t.fns {
		ls = append(ls, l)
	}
	t.mu.Unlock()
	for _, l := range ls {
		t.mu.Lock()
		_, live := t.fns[l]
		t.mu.Unlock()
		if live {
			t.callOne(l, v)
		}
	}
}

func (t *Topic[Out]) callOne(l *listener[Out], v Out) {
	defer func() {
		if r := recover(); r != nil {
			t.logger().Error("bites: a function given to Topic.Listen panicked", "topic", t.name(), "panic", r)
		}
	}()
	l.fn(v)
}

// SocketOwners holds the open Sockets of each owner, the User a socket belongs to, so that the
// application can close every Socket of one owner at once, as when the User is signed out
// (ADR 0047). The application makes one; the zero value is ready and it must not be copied. A
// handler records its Socket with Add when it opens, and Close ends them. A Socket leaves it when
// it ends, and an owner is forgotten with its last Socket, so it holds nothing for a closed
// socket. It is safe for concurrent use. One per meaning of an owner: Users and tenants in one
// SocketOwners would mix their ids.
//
// The zero value reaches this process only. An application with several instances makes it with
// NewSocketOwners, so that Close on one instance closes the owner's Sockets on every instance
// (ADR 0047).
//
//	var users bites.SocketOwners                                                    // one process
//	users := bites.NewSocketOwners("signout", broker, bites.SocketOwnersOptions{}) // every instance
//	// in the handler:   users.Add(in.Me.ID, s)
//	// on sign-out:      n, err := users.Close(id, 4401, "signed out")
type SocketOwners struct {
	mu      sync.Mutex
	byOwner map[string]map[*engine.WS]struct{}
	ownerOf map[*engine.WS]string
	leaveFn func(*engine.WS)
	link    *ownersLink // nil for the zero value; set by NewSocketOwners
}

// SocketOwnersOptions are the settings of a SocketOwners made with NewSocketOwners. The zero value
// is valid.
type SocketOwnersOptions struct {
	// OnGap replaces what happens when the broker reports that orders may have been lost (for
	// Postgres, the LISTEN connection dropped and came back). A missed sign-out cannot be told from
	// none, so the default fails closed: every Socket this SocketOwners holds on this instance is
	// closed with 1012 (service restart), each after a random share of a window of 1 ms a socket,
	// at most the app's WithGoAwaySpread (3 s by default), so that the clients reconnect, are
	// authenticated again by the Provider and are Added again, and do not all arrive at once. The
	// default costs a reconnect of every Socket of the family for every gap, and a gap is every
	// reconnect of the broker's connection. Give OnGap to log instead, or to check each User in
	// your own database and call Close for those that were signed out: it replaces the default
	// entirely, so a missed sign-out is then yours to catch. It is called on the broker's
	// goroutine, must not block, and a panic in it is recovered and logged. Nil means the default.
	OnGap func()
	// Log receives what happens to an order in silence otherwise: one that did not decode or named
	// a status a server may not send, and a gap. Nil means slog.Default().
	Log *slog.Logger
}

// ownerOrder is what crosses the broker: close the sockets of Owner with Status and Reason.
type ownerOrder struct {
	Owner  string `json:"owner"`
	Status uint16 `json:"status"`
	Reason string `json:"reason"`
}

// ownersLink is what a SocketOwners made with NewSocketOwners holds.
type ownersLink struct {
	name   string
	broker Broker
	onGap  func()
	log    *slog.Logger
	pc     *payloadCodec

	mu   sync.Mutex // guards stop: the SocketOwners listens while it has a socket
	stop func()
}

// NewSocketOwners returns a SocketOwners whose Close reaches every instance of the application
// through broker under name: an order {owner, status, reason} is published, and each instance
// closes its own Sockets of that owner. Every instance must use the same name. It listens while
// it holds a Socket and stops when the last one leaves, as a Topic does, so one nobody is in
// costs the broker nothing and needs no Close. It panics with a RegistrationError, naming the
// line, when name is empty, broker is nil or does not accept the name (ADR 0007). The zero
// SocketOwners is still the one of one process (ADR 0047).
func NewSocketOwners(name string, broker Broker, o SocketOwnersOptions) *SocketOwners {
	refuse := func(format string, args ...any) {
		label := "NewSocketOwners"
		if name != "" {
			label += fmt.Sprintf(" %q", name)
		}
		panic(&RegistrationError{msg: label + ": " + fmt.Sprintf(format, args...) + "\n\tregistered at " + callerWhere()})
	}
	switch {
	case name == "":
		refuse("the name is empty: give the name every instance uses for these orders, such as \"signout\"")
	case broker == nil:
		refuse("the broker is nil: give the Broker the orders travel through, or use a zero bites.SocketOwners for sockets of this process only")
	}
	if err := broker.Accepts(name); err != nil {
		refuse("the broker does not carry this name: %v", err)
	}
	pc, err := payloadCodecOf(reflect.TypeFor[ownerOrder]())
	if err != nil {
		refuse("%v", err)
	}
	l := &ownersLink{name: name, broker: broker, onGap: o.OnGap, log: o.Log, pc: pc}
	if l.log == nil {
		l.log = slog.Default()
	}
	return &SocketOwners{link: l}
}

// deliver is what the broker hands every order to: close this instance's sockets of the owner.
func (x *SocketOwners) deliver(payload []byte) {
	l := x.link
	var o ownerOrder
	if err := decodePayload(l.pc.dec, payload, unsafe.Pointer(&o)); err != nil {
		l.log.Warn("bites: a sign-out order was dropped, it did not decode; every instance must use the same name for one SocketOwners only", "owners", l.name, "bytes", len(payload), "err", err)
		return
	}
	if _, err := x.closeLocal(o.Owner, o.Status, o.Reason); err != nil {
		l.log.Warn("bites: a sign-out order was dropped, its close status or reason is not one a server may send", "owners", l.name, "owner", o.Owner, "err", err)
	}
}

// gap is the broker's word that orders may have been lost.
func (x *SocketOwners) gap() {
	l := x.link
	if l.onGap == nil {
		x.closeAllSpread()
		return
	}
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("bites: SocketOwnersOptions.OnGap panicked", "owners", l.name, "panic", r)
		}
	}()
	l.onGap()
}

// closeAllSpread closes every socket this SocketOwners holds with 1012, each after a random share
// of a window of 1 ms a socket (the app's go-away spread at most), as Shutdown spreads its 1001.
func (x *SocketOwners) closeAllSpread() {
	x.mu.Lock()
	targets := make([]*engine.WS, 0, len(x.ownerOf))
	for w := range x.ownerOf {
		targets = append(targets, w)
	}
	x.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	win := targets[0].GoAwayWindow(len(targets))
	x.link.log.Warn("bites: the broker may have lost sign-out orders, so every socket of this SocketOwners is closed with 1012 to be authenticated again; set SocketOwnersOptions.OnGap to check your Users instead",
		"owners", x.link.name, "sockets", len(targets), "window", win)
	for _, w := range targets {
		if win <= 0 {
			w.Close(1012, "service restart")
			continue
		}
		time.AfterFunc(rand.N(win), func() { w.Close(1012, "service restart") })
	}
}

// sync starts or ends the broker subscription to match whether a socket is held, under the
// link's lock so an Add and a leave that race end in the right state.
func (x *SocketOwners) sync() {
	l := x.link
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	x.mu.Lock()
	n := len(x.ownerOf)
	x.mu.Unlock()
	if n > 0 && l.stop == nil {
		l.stop = l.broker.Subscribe(l.name, x.deliver, x.gap)
	} else if n == 0 && l.stop != nil {
		l.stop()
		l.stop = nil
	}
}

func (x *SocketOwners) leave() func(*engine.WS) {
	if x.leaveFn == nil {
		x.leaveFn = func(w *engine.WS) {
			x.mu.Lock()
			if owner, ok := x.ownerOf[w]; ok {
				delete(x.ownerOf, w)
				if set := x.byOwner[owner]; set != nil {
					delete(set, w)
					if len(set) == 0 {
						delete(x.byOwner, owner)
					}
				}
			}
			x.mu.Unlock()
			x.sync()
		}
	}
	return x.leaveFn
}

// Add records s as a Socket of owner. Call it in the handler, with the owner taken from the
// authenticated Input (the Provider's User), never from a message. It returns an error for an
// empty owner, because a Socket under no owner could never be closed by owner and the handler
// must not go on as if it were (return the error and the upgrade is refused), and for a Socket
// that already has a different owner: a Socket has one. Adding it again under the same owner does
// nothing, and so does adding a Socket that has ended. The owner is a string the application
// chooses, and Close logs it, so it is not a secret.
func (x *SocketOwners) Add[Out any](owner string, s *Socket[Out]) error {
	if owner == "" {
		return errors.New("bites: SocketOwners.Add was given an empty owner, so this socket could not be closed by owner: pass the id of the authenticated User, and return this error from the handler so the socket is not opened")
	}
	if s == nil || s.w == nil {
		return ErrSocketClosed
	}
	x.mu.Lock()
	if had, ok := x.ownerOf[s.w]; ok {
		x.mu.Unlock()
		if had != owner {
			return errors.New("bites: SocketOwners.Add was given a Socket that already has a different owner: a Socket has one owner, so add it once, with the id of the authenticated User")
		}
		return nil
	}
	if x.byOwner == nil {
		x.byOwner = map[string]map[*engine.WS]struct{}{}
		x.ownerOf = map[*engine.WS]string{}
	}
	set := x.byOwner[owner]
	if set == nil {
		set = map[*engine.WS]struct{}{}
		x.byOwner[owner] = set
	}
	set[s.w] = struct{}{}
	x.ownerOf[s.w] = owner
	fn := x.leave()
	x.mu.Unlock()
	x.sync()
	s.w.Join(x, fn) // a socket that has ended leaves again at once
	return nil
}

// Close closes every open Socket of owner as Socket.Close does, with status and reason, and
// returns how many it closed; a Socket that was already closing is not counted, and an owner with
// none is (0, nil). Each Socket's OnClose sees the status and reason given, with Remote false.
// A status a server may not send (see Socket.Close) or a reason over 123 bytes of UTF-8 closes
// nothing and returns the error. When it closed any, it logs one Info line with the owner, the
// count, the status and the reason. A Socket added after Close has copied the list is not
// closed: disable the User in the application first, so a handshake that begins later is refused.
//
// On a SocketOwners made with NewSocketOwners, Close closes this instance's Sockets at once and
// then publishes the order through the broker, so every other instance closes its own; the
// count is this instance's alone, since the others answer nobody. When the publish fails, Close
// returns the count and the error: the sockets of this instance are closed already, and the
// error says the other instances were not told, so the caller retries or reports it.
func (x *SocketOwners) Close(owner string, status uint16, reason string) (int, error) {
	n, err := x.closeLocal(owner, status, reason)
	if err != nil || x.link == nil {
		return n, err
	}
	b, err := x.link.pc.enc.Append(nil, noescapeP(unsafe.Pointer(&ownerOrder{Owner: owner, Status: status, Reason: reason})))
	if err != nil {
		return n, fmt.Errorf("bites: SocketOwners %q: the order does not encode: %w", x.link.name, err)
	}
	if err := x.link.broker.Publish(context.Background(), x.link.name, b); err != nil {
		var pe *PayloadError
		if errors.As(err, &pe) {
			return n, pe
		}
		return n, fmt.Errorf("bites: SocketOwners %q: the sockets of this instance are closed but the order did not reach the other instances: publish through the broker: %w", x.link.name, err)
	}
	return n, nil
}

// closeLocal is Close for this process: it validates and closes the sockets held here.
func (x *SocketOwners) closeLocal(owner string, status uint16, reason string) (int, error) {
	if !ws.ServerCloseCode(status) {
		return 0, ws.ErrCloseStatus
	}
	if len(reason) > 123 {
		return 0, ws.ErrControlTooLong
	}
	if !utf8.ValidString(reason) {
		return 0, errors.New("bites: a close reason must be valid UTF-8")
	}
	x.mu.Lock()
	targets := make([]*engine.WS, 0, len(x.byOwner[owner]))
	for w := range x.byOwner[owner] {
		targets = append(targets, w)
	}
	x.mu.Unlock()
	n := 0
	for _, w := range targets {
		if w.Close(status, reason) == nil {
			n++
		}
	}
	if n > 0 {
		targets[0].Logger().Info("bites: closed the sockets of an owner", "owner", owner, "closed", n, "status", status, "reason", reason)
	}
	return n, nil
}

// Len is the number of open Sockets of owner, those closing included until they end.
func (x *SocketOwners) Len(owner string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.byOwner[owner])
}

// ---- the message handler the engine calls ----

// wsPlan is what Registration made of a WebSocket route's message and output types.
type wsPlan struct {
	lim      *ws.Limits
	spec     engine.WSSpec
	enc      *jsoncodec.Encoder // the Socket's Out; nil for []byte
	outBin   bool
	hint     atomic.Int32
	msgKind  bodyKind // bodyJSON, bodyRaw or bodyBytes
	dec      *jsoncodec.Decoder
	valOff   uintptr // Raw[T]: where the decoded value is
	rawOff   uintptr // Raw[T]: where the exact bytes are kept
	origin   *originPolicy
	pattern  string
	validate bool
	watch    *socketWatch // the Observers that watch sockets; nil when no Observer does (ADR 0063)
}

// socketWatch is what a route tells the SocketObservers of the app about its sockets.
type socketWatch struct {
	obs   []SocketObserver
	route string
	log   *slog.Logger
}

// guard runs one Observer callback with its own recovery: a panic in one is logged and the others
// still run, as for an Observer's End (ADR 0051). The callbacks run on the connection's or the
// heartbeat's goroutine, which must not die of an Observer.
func (sw *socketWatch) guard(o SocketObserver, what string) {
	if v := recover(); v != nil {
		sw.log.Error("bites: panic in a SocketObserver; the socket is unaffected", "panic", v, "stack", string(debug.Stack()),
			"observer", fmt.Sprintf("%T", o), "callback", what, "route", sw.route)
	}
}

func (sw *socketWatch) opened() {
	for _, o := range sw.obs {
		func() {
			defer sw.guard(o, "SocketOpened")
			o.SocketOpened(sw.route)
		}()
	}
}

func (sw *socketWatch) closed(info CloseInfo) {
	for i := len(sw.obs) - 1; i >= 0; i-- {
		func() {
			defer sw.guard(sw.obs[i], "SocketClosed")
			sw.obs[i].SocketClosed(sw.route, info)
		}()
	}
}

func (sw *socketWatch) pongMissed() {
	for _, o := range sw.obs {
		func() {
			defer sw.guard(o, "SocketPongMissed")
			o.SocketPongMissed(sw.route)
		}()
	}
}

// Opened and PongMissed are the engine's word of a socket's life (engine.WSWatcher). They do
// nothing for a route no Observer watches.
func (h *wsHandler[Msg]) Opened(w *engine.WS) {
	if sw := h.plan.watch; sw != nil {
		sw.opened()
	}
}

func (h *wsHandler[Msg]) PongMissed(w *engine.WS) {
	if sw := h.plan.watch; sw != nil {
		sw.pongMissed()
	}
}

type wsHandler[Msg any] struct {
	plan  *wsPlan
	inbox Inbox[Msg]
}

// noMessages is the close reason of a socket with no OnMessage that was sent a message.
const noMessages = "this socket takes no messages"

// Message decodes one message and calls OnMessage. The message is its own buffer, so the strings
// in the decoded value point into it and nothing pooled reaches the handler (ADR 0008, ADR 0046);
// that buffer is the one allocation of an OnMessage.
func (h *wsHandler[Msg]) Message(w *engine.WS, op ws.Opcode, msg []byte, slab *wire.Slab) error {
	in := &h.inbox
	if in.OnMessage == nil {
		// A socket that only sends was given a message: closing says so to the client, where
		// dropping it would leave both sides believing it was read (ADR 0035).
		return &engine.WSError{Code: ws.CloseUnsupported, Reason: noMessages}
	}
	var m Msg
	p := h.plan
	if p.msgKind == bodyBytes {
		*(*[]byte)(unsafe.Pointer(&m)) = msg[:len(msg):len(msg)]
		return closeError(in.OnMessage(w.Context(), m))
	}
	slab.Adopt(msg, len(msg))
	st := getState()
	val := noescapeP(unsafe.Pointer(&m))
	if p.msgKind == bodyRaw {
		*(*[]byte)(unsafe.Add(val, p.rawOff)) = msg[:len(msg):len(msg)]
		val = unsafe.Add(val, p.valOff)
	}
	err := p.dec.Decode(msg, val, slab, &st.errs)
	if err != nil || len(st.errs.List) > 0 {
		me := &MalformedError{}
		if err != nil {
			var se *jsoncodec.SyntaxError
			switch {
			case errors.As(err, &se):
				me.Errors = append(me.Errors, FieldError{Location: "message", Message: se.Error()})
			case errors.Is(err, jsoncodec.ErrDepth):
				me.Errors = append(me.Errors, FieldError{Location: "message", Message: msgNested})
			default:
				me.Errors = append(me.Errors, FieldError{Location: "message", Message: "expected valid JSON"})
			}
		}
		for _, e := range st.errs.List {
			me.Errors = append(me.Errors, FieldError{Location: messageLocation(e.Location), Message: e.Message})
		}
		putState(st)
		if in.OnMalformed != nil {
			return closeError(in.OnMalformed(w.Context(), msg, me))
		}
		return &engine.WSError{Code: ws.CloseInvalidData, Reason: closeReason("malformed message", me.Errors[0])}
	}
	putState(st)
	return closeError(in.OnMessage(w.Context(), m))
}

// messageLocation prefixes a location relative to the message root with "message".
func messageLocation(loc string) string {
	switch {
	case loc == "":
		return "message"
	case loc[0] == '[':
		return "message" + loc
	}
	return "message." + loc
}

// closeReason is a close reason that names the first thing wrong, within the 123 bytes a Close
// frame has.
func closeReason(prefix string, e FieldError) string {
	r := prefix + ": " + e.Location + ": " + e.Message
	if len(r) > 123 {
		r = r[:123]
		for !utf8.ValidString(r) {
			r = r[:len(r)-1]
		}
	}
	return r
}

func (h *wsHandler[Msg]) Closed(w *engine.WS, info engine.WSCloseInfo) {
	if sw := h.plan.watch; sw != nil {
		defer sw.closed(info) // after OnClose, whether it returns or panics
	}
	if h.inbox.OnClose != nil {
		h.inbox.OnClose(info)
	}
}

// ---- Origin (ADR 0046) ----

// originPolicy decides which Origins may open a WebSocket on a route: the request's own
// authority always (its Host, and the X-Forwarded-Host a trusted proxy wrote), and the listed
// origins or any.
type originPolicy struct {
	any  bool
	list []string // full origins, lower-cased
}

// allows reports whether a handshake from origin to the host named by Host, or by fwd, the
// X-Forwarded-Host a trusted proxy wrote ("" when none), may proceed.
func (p *originPolicy) allows(origin, host, fwd string) bool {
	if sameAuthority(origin, host) || (fwd != "" && sameAuthority(origin, fwd)) {
		return true
	}
	if p == nil {
		return false
	}
	return p.any || slices.Contains(p.list, strings.ToLower(origin))
}

// sameAuthority compares the Origin's authority with the request's Host without case, dropping a
// port only when it is the default of the Origin's own scheme, and never inferring the scheme of
// the connection: behind a TLS-terminating proxy the engine sees plain HTTP while the browser's
// Origin says https. Only http and https origins are compared; "null", a path, a userinfo or
// another scheme never match.
func sameAuthority(origin, host string) bool {
	scheme, rest, ok := strings.Cut(origin, "://")
	if !ok || (scheme != "http" && scheme != "https") || rest == "" || strings.ContainsAny(rest, "/?#@ ") {
		return false
	}
	def := ":80"
	if scheme == "https" {
		def = ":443"
	}
	return strings.EqualFold(strings.TrimSuffix(rest, def), strings.TrimSuffix(host, def))
}

// checkOriginEntry says why entry cannot be an allowed origin, or "" (ADR 0046).
func checkOriginEntry(fn, entry string) string {
	switch {
	case entry == "*":
		return fn + `("*") is refused: an Origin check that lets every site in is AllowAnyOrigin(), which says so in the code; use it if that is what you mean`
	case entry == "null":
		return fn + `("null") is refused: a sandboxed iframe or a file:// page sends Origin: null, so every such page would pass; list the sites that may connect`
	case strings.Contains(entry, "*"):
		return fmt.Sprintf("%s(%q) has a wildcard, which is not supported: list each origin in full", fn, entry)
	}
	scheme, rest, ok := strings.Cut(entry, "://")
	switch {
	case !ok || scheme == "":
		return fmt.Sprintf("%s(%q) has no scheme: an origin is scheme://host[:port], such as https://app.example.com", fn, entry)
	case rest == "" || strings.ContainsAny(rest, "/?#@ "):
		return fmt.Sprintf("%s(%q) has a path, a trailing slash, a userinfo or a space: an origin is scheme://host[:port] and nothing after it", fn, entry)
	}
	if why := badOrigin(entry); why != "" {
		return fmt.Sprintf("%s(%q) is not a full origin: %s", fn, entry, why)
	}
	return ""
}

func lowerAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToLower(s)
	}
	return out
}

const (
	originInherited uint8 = iota
	originListed
	originAny
)

// AllowOrigins lets the listed origins open a WebSocket on this route, or on every WebSocket
// route of this group, besides the request's own host: its Host, and the X-Forwarded-Host a
// trusted proxy wrote when TrustedProxies.ForwardedHost is set (ADR 0046). Each is a full origin,
// scheme://host[:port], compared exactly after lower-casing. "*", "null", an entry with no scheme,
// a path or a wildcard are refused at Registration, and so is AllowAnyOrigin in the same scope.
// A nearer scope's list replaces an enclosing one's.
func AllowOrigins(origins ...string) RouteOption {
	return func(c *routeConfig) {
		if len(origins) == 0 {
			c.refuse("AllowOrigins is given no origin: list the sites that may connect, or use AllowAnyOrigin() to say any may")
			return
		}
		for _, o := range origins {
			if msg := checkOriginEntry("AllowOrigins", o); msg != "" {
				c.refuse("%s", msg)
				return
			}
		}
		if c.originKind == originAny && c.originDepth == c.depth {
			c.refuse("AllowOrigins and AllowAnyOrigin() are both given in this scope: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one")
			return
		}
		p := &originPolicy{}
		if c.originKind == originListed && c.originDepth == c.depth {
			p.list = slices.Clone(c.origin.list)
		}
		p.list = append(p.list, lowerAll(origins)...)
		c.origin, c.originKind, c.originDepth = p, originListed, c.depth
	}
}

// AllowAnyOrigin lets a page on any site open a WebSocket on this route, or on every WebSocket
// route of this group. It is its own name, not AllowOrigins("*"), so the choice shows in review
// and in a search: a browser attaches cookies to a WebSocket handshake from any site, so a route
// that authenticates by cookie is then open to cross-site hijacking (ADR 0046).
func AllowAnyOrigin() RouteOption {
	return func(c *routeConfig) {
		if c.originKind == originListed && c.originDepth == c.depth {
			c.refuse("AllowAnyOrigin() and AllowOrigins are both given in this scope: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one")
			return
		}
		c.origin, c.originKind, c.originDepth = &originPolicy{any: true}, originAny, c.depth
	}
}

// WithAllowOrigins is AllowOrigins for every WebSocket route of the app. A group's or a route's
// own AllowOrigins replaces it for that scope. A bad entry panics here, naming this call.
func WithAllowOrigins(origins ...string) Option {
	where := callerWhere()
	for _, o := range origins {
		if msg := checkOriginEntry("WithAllowOrigins", o); msg != "" {
			panic(&RegistrationError{msg: msg + "\n\tregistered at " + where})
		}
	}
	if len(origins) == 0 {
		panic(&RegistrationError{msg: "WithAllowOrigins is given no origin: list the sites that may connect, or use WithAllowAnyOrigin() to say any may\n\tregistered at " + where})
	}
	return func(a *App) {
		if a.origin != nil && a.origin.any {
			panic(&RegistrationError{msg: "WithAllowOrigins and WithAllowAnyOrigin() are both given: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one\n\tregistered at " + where})
		}
		if a.origin == nil {
			a.origin = &originPolicy{}
		}
		if a.originWhere == "" {
			a.originWhere = where
		}
		a.origin.list = append(a.origin.list, lowerAll(origins)...)
	}
}

// WithAllowAnyOrigin is AllowAnyOrigin for every WebSocket route of the app.
func WithAllowAnyOrigin() Option {
	where := callerWhere()
	return func(a *App) {
		if a.origin != nil && len(a.origin.list) > 0 {
			panic(&RegistrationError{msg: "WithAllowAnyOrigin() and WithAllowOrigins are both given: AllowAnyOrigin lets every site in, so the list would mean nothing; keep one\n\tregistered at " + where})
		}
		a.origin = &originPolicy{any: true}
	}
}

// WithGoAwaySpread sets the longest window over which Shutdown spreads the Close 1001 it sends
// every open WebSocket, so that the clients do not all reconnect to the next instance in the same
// moment (ADR 0029). The window is 1 ms for each open socket, at most d, and at most half of what
// is left of Shutdown's context, whichever is shortest; each socket is told after a random share
// of it. The default is 3 s, which spreads 3,000 sockets and more over 3 s: give a larger d when
// the instances that take the reconnects cannot take as many as that a second (20,000 sockets
// over d of 10 s are told to go at about 2,000 a second). d is a ceiling, not a delay: fewer
// sockets still get 1 ms each, and a Shutdown context with less than 2*d left shortens the window
// to half of it. Shutdown logs the window it used, with a warning that says so when the context
// cut it. Read the arithmetic as a plan, not a measurement. The option has no effect on an app
// with no WebSocket route. It is not WithShutdownDelay, which is how long the listener stays
// open before any of this.
//
// New panics for a d that is zero or negative, and for the option given twice.
func WithGoAwaySpread(d time.Duration) Option {
	return func(a *App) {
		where := callerWhere()
		if a.goAwayWhere != "" {
			panic(&RegistrationError{msg: "WithGoAwaySpread: given twice (first at " + a.goAwayWhere + ", again at " + where + "): give one window"})
		}
		if d <= 0 {
			panic(&RegistrationError{msg: fmt.Sprintf("WithGoAwaySpread: the window is %v, which is not a window: give a positive duration such as 10*time.Second, or leave the option out for the default of 3s\n\tregistered at %s", d, where)})
		}
		a.goAwaySpread, a.goAwayWhere = d, where
		a.custom["go-away spread"] = true
	}
}

// MaxMessage sets the most bytes of one message, reassembled, this WebSocket route (or every
// WebSocket route of this group) accepts; over it the socket is closed with 1009 (ADR 0046). It
// must be positive and not above the app's Limits.ReassemblyBudget, since a larger message could
// never be assembled. It has no effect on a route that is not a WebSocket.
func MaxMessage(n int) RouteOption {
	return func(c *routeConfig) {
		if n <= 0 {
			c.refuse("MaxMessage(%d) is not a limit: it must be a positive number of bytes", n)
			return
		}
		c.maxMessage = n
	}
}

// originOf is the Origin header of the handshake: "" with ok when there is none, and bad when
// there are two (ADR 0046).
func originOf(ex *engine.Exchange) (origin string, bad bool) {
	n := 0
	for i := range ex.Head().Headers {
		h := &ex.Head().Headers[i]
		if equalFold(h.Name, "origin") {
			n++
			origin = string(h.Value)
		}
	}
	return origin, n > 1
}

// ---- Registration ----

// WebSocket registers a WebSocket route (ADR 0036): a GET whose handshake runs the pipeline of a
// typed route and whose handler receives the typed Input and a Socket to send on, and returns the
// Inbox that receives the socket's messages:
//
//	app.WebSocket("/rooms/{id}", rooms.Join)
//
//	func (r *Rooms) Join(ctx context.Context, in JoinRoom, s *bites.Socket[Out]) (bites.Inbox[Msg], error)
//
// The Input, its Providers, Validate and the middleware around the route are run before the 101,
// after the Origin check, and an error returned before the Inbox is the answer in place of the
// upgrade. Msg is decoded from the JSON of each text message (or []byte, or Raw[T]); Out is
// encoded as JSON for each Send, or sent as a binary frame when it is []byte. A mistake in those
// types panics now (ADR 0007).
func (a *App) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption) {
	where := callerWhere()
	a.seal("WebSocket "+pattern, where)
	a.mu.Lock()
	enclosing := a.provOpts
	a.mu.Unlock()
	registerWS(a, "", pattern, h, scoped(enclosing, opts), where)
}

// WebSocket registers a WebSocket route under the group's prefix (see App.WebSocket).
func (g *Group) WebSocket[In, Out, Msg any](pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts ...RouteOption) {
	where := callerWhere()
	g.seal("WebSocket "+g.prefix+pattern, where)
	registerWS(g.app, g.prefix, pattern, h, scoped(g.opts, opts), where)
}

type wsRoute[In, Out, Msg any] struct {
	h        func(context.Context, In, *Socket[Out]) (Inbox[Msg], error)
	in       *inputPlan
	cfg      routeConfig
	plan     *wsPlan
	validate func(In) error
}

func registerWS[In, Out, Msg any](a *App, prefix, pattern string, h func(context.Context, In, *Socket[Out]) (Inbox[Msg], error), opts []RouteOption, where string) {
	full := prefix + pattern
	label := "WebSocket " + full
	fail := func(err error) { panic(registrationFailure(label, err, where)) }
	if h == nil {
		fail(refusalf("the handler is nil"))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.serving {
		panic(&RegistrationError{msg: "route " + label + " registered at " + where + " after the app started serving: routes can be added only before it does"})
	}
	rt := a.addRoute("GET", full, label, where)
	cfg := routeConfig{maxBody: a.limits.MaxBody, bodyTimeout: a.limits.BodyTimeout, maxMessage: a.limits.MaxMessage, origin: a.origin, originDepth: -1}
	if a.origin != nil {
		cfg.originKind = originListed
		if a.origin.any {
			cfg.originKind = originAny
		}
	}
	if err := applyOptions(&cfg, opts); err != nil {
		fail(err)
	}
	if len(cfg.returns) > 0 {
		fail(refusalf("Returns lists the answers of a handler that returns an interface, but a WebSocket handler returns an Inbox and answers 101: its messages are the Socket's and the Inbox's types"))
	}
	if cfg.setsLocation {
		fail(refusalf("SetsLocation is given to a WebSocket route, which answers 101 and has no Created to carry a Location"))
	}
	if cfg.mediaType != "" || len(cfg.sendsHeaders) > 0 {
		name := "MediaType"
		if cfg.mediaType == "" {
			name = "SendsHeader"
		}
		fail(refusalf("%s states what a handler's answer sends, but a WebSocket route answers 101 with no body, and its messages are the Socket's type", name))
	}
	info := RouteInfo{Method: "GET", Pattern: full, OperationID: routeOperationID(&cfg, "GET", full), Tags: slices.Clone(cfg.tags), Deprecated: cfg.deprecated, WebSocket: true}
	a.noteRoute(info)
	if len(cfg.each) > 0 {
		cfg.addEach(info, label, where)
	}
	if int64(cfg.maxMessage) > a.limits.ReassemblyBudget {
		fail(refusalf("MaxMessage is %d bytes, above the app's reassembly budget of %d bytes (Limits.ReassemblyBudget), so a message of that size could never be assembled: lower MaxMessage or raise the budget", cfg.maxMessage, a.limits.ReassemblyBudget))
	}
	c := &compiler{a: a, method: "GET", params: patternParams(full), cfg: cfg, browserOpened: true}
	in, err := c.compileInput(reflect.TypeFor[In]())
	if err != nil {
		fail(err)
	}
	if err := a.checkNarrowed(&cfg); err != nil {
		fail(err)
	}
	plan := &wsPlan{origin: cfg.origin, pattern: full, watch: a.socketWatch(full)}
	lim := ws.DefaultLimits()
	lim.MaxMessage = cfg.maxMessage
	plan.lim = &lim
	if err := a.compileSocket(plan, reflect.TypeFor[Out]()); err != nil {
		fail(err)
	}
	if err := a.compileMessage(plan, reflect.TypeFor[Msg](), cfg.unknown); err != nil {
		fail(err)
	}
	plan.spec = engine.WSSpec{Limits: plan.lim, AcceptsBinary: plan.msgKind == bodyBytes}
	if !cfg.undocumented {
		op, err := a.describeRoute("GET", full, where, reflect.TypeFor[In](), in, nil, nil, &cfg, c.provDefs, c.provSecs)
		if err != nil {
			fail(err)
		}
		if err := a.describeMessages(op, reflect.TypeFor[Out](), reflect.TypeFor[Msg](), cfg.unknown); err != nil {
			fail(err)
		}
		a.docs = append(a.docs, op)
	}
	a.methods["GET"] = true
	wr := &wsRoute[In, Out, Msg]{h: h, in: in, cfg: cfg, plan: plan}
	if in.validates {
		m, _ := reflect.TypeFor[In]().MethodByName("Validate")
		wr.validate = m.Func.Interface().(func(In) error)
	}
	rt.serve = wr.serve
	if err := a.bindMiddleware(cfg.mw); err != nil {
		fail(err)
	}
	a.useOn(rt, cfg.mw)
	rt.chain = len(cfg.mw) > 0 || len(in.provs) > 0
	a.wsRoutes++
}

// describeMessages states a WebSocket route's messages under x-websocket, since OpenAPI 3.1 has no
// keyword for them (ADR 0020): what the client sends (the Inbox's type) and what the server sends
// (the Socket's), as JSON schemas, or a binary string for []byte.
func (a *App) describeMessages(op *routeDoc, out, msg reflect.Type, unknown bool) error {
	reg := a.registry()
	schema := func(t reflect.Type, m openapi.Mode) (any, error) {
		if t == bytesType {
			return map[string]any{"type": "string", "contentEncoding": "binary"}, nil
		}
		s, err := reg.Schema(t, m)
		if err != nil {
			return nil, refusalf("the API description cannot describe the WebSocket's messages: %v", err)
		}
		return s, nil
	}
	mode := openapi.Request
	if unknown {
		mode = openapi.RequestOpen
	}
	client, err := schema(msg, mode)
	if err != nil {
		return err
	}
	server, err := schema(out, openapi.Response)
	if err != nil {
		return err
	}
	op.op["x-websocket"] = map[string]any{"clientMessage": client, "serverMessage": server}
	return nil
}

// compileSocket reads the type a Socket sends (ADR 0036): any type that can be written as JSON,
// or []byte, which is sent as a binary frame.
func (a *App) compileSocket(p *wsPlan, t reflect.Type) error {
	if t == bytesType {
		p.outBin = true
		return nil
	}
	if t.PkgPath() == ownPkg && t.Kind() == reflect.Struct {
		return refusalf("the handler's Socket sends %s, one of the framework's response types; a WebSocket message is a plain value, written as JSON (or []byte, written as a binary frame), and has no status or headers", typeLabel(t))
	}
	if o := findOneOf(t, map[reflect.Type]bool{}); o != nil {
		return refusalf("the handler's Socket sends %s, which holds a %s; a OneOf is a handler's return type, not a WebSocket message", typeLabel(t), oneOfName(o))
	}
	e, err := jsoncodec.CompileEncoder(t, jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas})
	if err != nil {
		return refusalf("the Socket's message type cannot be written as JSON: %v", err)
	}
	p.enc = e
	return nil
}

// compileMessage reads the Inbox's message type: a struct, slice, map or array read as JSON,
// []byte, which takes text and binary frames, or Raw[T] (ADR 0036).
func (a *App) compileMessage(p *wsPlan, t reflect.Type, unknown bool) error {
	if o := findOneOf(t, map[reflect.Type]bool{}); o != nil {
		return refusalf("the Inbox's message type holds a %s; a OneOf is a handler's return type, not a WebSocket message", oneOfName(o))
	}
	opts := jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas, AllowUnknown: unknown, MaxDepth: a.limits.MaxDepth}
	compile := func(dt reflect.Type) (*jsoncodec.Decoder, error) {
		d, err := jsoncodec.CompileDecoder(dt, opts)
		if err != nil {
			return nil, refusalf("the Inbox's message type cannot be read from JSON: %v", err)
		}
		return d, nil
	}
	switch {
	case t == bytesType:
		p.msgKind = bodyBytes
		return nil
	case isOwn(t, "Raw["):
		d, err := compile(t.Field(0).Type)
		if err != nil {
			return err
		}
		p.msgKind, p.dec, p.valOff, p.rawOff = bodyRaw, d, t.Field(0).Offset, t.Field(1).Offset
		return nil
	case t == uploadType || isOwn(t, "Form[") || isMultipart(t) || isSubmitted(t) || t == bodyReaderType || containsUpload(t, map[reflect.Type]bool{}):
		return refusalf("the Inbox's message type holds %s, which is a kind of request body; a message is JSON, []byte or Raw[T]", typeLabel(t))
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Slice, reflect.Map, reflect.Array:
		d, err := compile(t)
		if err != nil {
			return err
		}
		p.msgKind, p.dec = bodyJSON, d
		return nil
	}
	return refusalf("the Inbox's message type is %s; a message is a struct, slice or map (read as JSON), []byte or Raw[T]", t)
}

// refuseHandshake answers a handshake the codec refused with the status ADR 0046 gives it, the
// problem naming the route as a WebSocket route.
func (p *wsPlan) refuseHandshake(ex *engine.Exchange, he *ws.HandshakeError) {
	r := problem(ex, he.Status, "WebSocket route "+p.pattern+": "+he.Reason)
	if he.Status == 405 {
		r.Header("Allow", "GET")
	}
	if he.WantVersion {
		r.Header("Sec-WebSocket-Version", "13")
	}
}

// serve is the whole of a WebSocket route's request: the handshake, the Origin, then the Providers,
// the Input and the handler as for a typed route, and the upgrade.
func (rt *wsRoute[In, Out, Msg]) serve(a *App, ex *engine.Exchange, ps *router.Params, ctx context.Context, req *Request) {
	p := rt.plan
	head := ex.Head()
	key, he := ws.CheckHandshake(head)
	if he == nil && (head.Chunked || head.ContentLength > 0) {
		he = &ws.HandshakeError{Status: 400, Reason: "a WebSocket handshake has no body"}
	}
	if he != nil {
		p.refuseHandshake(ex, he)
		return
	}
	origin, bad := originOf(ex)
	if bad || (origin != "" && !p.origin.allows(origin, string(head.Host), a.proxies.forwardedHost(ex))) {
		problem(ex, 403, "WebSocket route "+p.pattern+": the Origin of this handshake is not allowed")
		return
	}
	var in In
	if steps := rt.in.provs; steps != nil {
		f := provFrame{a: a, ex: ex, ps: ps, ctx: ctx, req: req, in: noescapeP(unsafe.Pointer(&in)), steps: steps, tail: rt}
		f.key = key
		steps[0].node.run(noescape(&f), 0)
		return
	}
	st := getState()
	ok := rt.in.decode(a, ex, ps, noescapeP(unsafe.Pointer(&in)), st, &rt.cfg)
	putState(st)
	if !ok {
		return
	}
	rt.call(a, ex, ctx, in, key)
}

func (rt *wsRoute[In, Out, Msg]) afterProviders(f *provFrame) {
	in := (*In)(f.in)
	st := getState()
	ok := rt.in.decode(f.a, f.ex, f.ps, unsafe.Pointer(in), st, &rt.cfg)
	putState(st)
	if !ok {
		return
	}
	rt.call(f.a, f.ex, f.ctx, *in, f.key)
}

// call runs Validate and the handler, and upgrades when the handler returns an Inbox.
func (rt *wsRoute[In, Out, Msg]) call(a *App, ex *engine.Exchange, ctx context.Context, in In, key []byte) {
	if rt.validate != nil {
		if err := rt.validate(in); err != nil {
			a.answerInvalid(ex, err)
			return
		}
	}
	p := rt.plan
	w := ex.NewWS(p.spec)
	sock := &Socket[Out]{w: w, enc: p.enc, bin: p.outBin, hint: &p.hint}
	inbox, err := rt.h(ctx, in, sock)
	if err != nil {
		w.Abort()
		a.answerError(ex, err)
		return
	}
	ex.Upgrade(w, key, &wsHandler[Msg]{plan: p, inbox: inbox})
}
