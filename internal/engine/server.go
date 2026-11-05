// Package engine is the HTTP/1.1 server under the framework (ADR 0005): one goroutine per
// connection, blocked on a one-byte read while the connection is idle, with a read buffer taken
// from a pool only once a byte has arrived and returned before the next wait. A connection idle
// for moveAfter parks on the engine's poller and its goroutine ends until bytes wake it (park.go).
//
// # Why this shape
//
// The prototype in docs/research/engines (variant b) held 2.9 KB of heap and stack per idle
// connection and allocated nothing per request; net/http holds about 19 KB and allocates 13
// times. What it costs is a second read syscall per request (the one-byte read, then the rest),
// about 5% of plain throughput, which ADR 0003 accepts for the memory axis.
//
// # What a request allocates (ADR 0003)
//
// Two things, and only two: the slab (ADR 0008), sized by the engine to the head plus the
// declared body so the framework's copies and ReadBody never allocate again, and the
// reqctx.Ctx (ADR 0016). The read buffer, the parsed head, the response buffer and the chunked
// staging buffer live in one pooled scratch that the user never sees (ADR 0008 lets the engine
// pool what no handler can hold). The Exchange, the Response and the slab header are fields of
// the connection and are reused, again out of the user's reach: the framework copies what users
// see into the slab.
//
// # Refusals
//
// A refused head is answered with its status and a problem+json body and then the connection is
// closed, always (ADR 0045), even where the parser says the framing is still known (a missing
// Host): the table says "connection closed" for every refusal and a rule with no exceptions is
// the one a proxy cannot disagree with.
//
// # Disconnect detection (ADR 0016)
//
// The connection implements reqctx.Watch and is armed only when a handler first looks at its
// context. A *net.TCPConn on Linux is registered with the engine's one epoll watcher; a memconn
// connection is watched through its PeerClosed channel by one helper goroutine; anything else
// (other platforms, a wrapped connection) gets a reader goroutine that reads one byte and keeps
// it for the next request, as net/http's does. Over TLS the same choice is made on the
// connection under the *tls.Conn (NetConn), and the epoll watcher still only watches for hangup
// and never reads, so TLS records are not disturbed; the reader goroutine reads through the
// *tls.Conn, where one byte is one decrypted byte.
//
// # TLS (ADR 0017)
//
// ServeTLS wraps each accepted connection with crypto/tls and the connection's own goroutine
// handshakes first, under HandshakeTimeout, counted against MaxConns from accept. After it the
// connection is served exactly as a plain one: no extra field, no second waiting place, no
// deadline or clock read on the request path.
//
// # Shutdown (ADR 0029)
//
// Shutdown stops accepting, closes connections that are idle at that instant, cancels the context
// of every live stream with ErrServerClosed, sends every WebSocket a Close 1001 (each after its
// share of a window), lets requests in flight finish with Connection: close and, when its context
// ends first, closes the rest and cancels their live contexts with ErrServerClosed. The request contexts have no cancellable
// parent: a cancellable parent makes arming cost three more allocations (reqctx's
// measurements), so the engine cancels the live Ctxs itself.
package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/reqctx"
	"github.com/nevindra/bites/internal/wheel"
)

// ErrServerClosed is returned by Serve after Shutdown or Close, and is the cancel cause of the
// contexts of requests cut off when Shutdown's context ends.
var ErrServerClosed = errors.New("bites: server closed")

const (
	// DefaultBodyLimit is the body size the slab is sized for (ADR 0024).
	DefaultBodyLimit = 1 << 20
	// DefaultBodyTimeout is the time to read a body when the caller gives none (ADR 0024).
	DefaultBodyTimeout = 30 * time.Second

	bufSize      = 4096
	maxDrain     = 64 << 10
	drainTimeout = time.Second
	lingerTime   = 500 * time.Millisecond
	moveAfter    = 100 * time.Millisecond // idle this long, a connection waits on a fresh goroutine (ADR 0005)
	maxPooled    = 64 << 10               // buffers larger than this are not returned to the pool
	wsHotBuf     = 64 << 10               // the most a hot WebSocket's read buffer grows to (await)
)

// Options configure a Server. Zero fields take the defaults of ADR 0024.
type Options struct {
	Limits       h1.Limits
	HeadTimeout  time.Duration // 10s; time to read a head once its first byte arrived
	IdleTimeout  time.Duration // 60s
	WriteTimeout time.Duration // 30s
	// HandshakeTimeout bounds a TLS handshake, from accept to its end (10s). Only ServeTLS uses it.
	HandshakeTimeout time.Duration
	MaxConns         int         // 0: 90% of RLIMIT_NOFILE
	Clock            clock.Clock // clock.Real() if nil
	Logger           *slog.Logger
	WS               WSConfig       // the WebSocket limits of ADR 0037, ADR 0039 and ADR 0046
	Compress         CompressConfig // response compression, off by default (ADR 0031, ADR 0061)
	// NoPark keeps every idle connection on a goroutine, moved after moveAfter, as before parking
	// (ADR 0005). Parking is used only where it can be: on Linux, for a TCP connection.
	NoPark bool
}

// Handler is implemented by the framework above the engine; it never reaches user code.
type Handler interface{ Serve(ex *Exchange) }

// Server is the HTTP/1.1 engine.
type Server struct {
	h Handler
	o Options

	mu        sync.Mutex
	conns     *conn // intrusive list: two pointers a connection, no map entry
	nconns    int
	listeners map[net.Listener]struct{}
	drained   chan struct{}
	drainOnce sync.Once
	leaveSet  bool          // Shutdown has fixed the window of the 1001 (mu)
	leaveWin  time.Duration // the window the 1001 of every WebSocket is spread over (mu)

	shutting atomic.Bool
	quit     chan struct{}
	quitOnce sync.Once
	sem      chan struct{}

	pool    sync.Pool
	coders  []coder   // o.Compress.Encoders with their Content-Encoding lines (ADR 0061)
	encBufs sync.Pool // *encBuf, the compressed body of one write

	hsWarnAt     atomic.Int64 // when the last certificate-rejected Warn was written, ns; 0: never
	hsSuppressed atomic.Int64 // such failures skipped since that Warn
	pxWarnAt     atomic.Int64 // when the last PROXY protocol Warn was written, ns; 0: never (ADR 0058)
	pxSuppressed atomic.Int64 // such Warns skipped since
	date         atomic.Pointer[dateEntry]

	wOnce         sync.Once
	watcher       atomic.Pointer[reqctx.Watcher]
	forceFallback bool // tests: use the reader goroutine even where epoll is available

	pollOnce sync.Once
	poll     atomic.Pointer[poller] // where idle connections park (park.go), made on the first park

	wheelOnce sync.Once
	wheel     atomic.Pointer[wheel.Wheel] // the heartbeats and close deadlines of every WebSocket and the idle timeouts of parked connections, made on first use (ADR 0039)
	asmUsed   atomic.Int64                // bytes charged to the reassembly budget (ADR 0046)
	sendUsed  atomic.Int64                // bytes queued to be sent, all sockets (ADR 0037)
	noInline  bool                        // tests: send every frame through the flusher
}

// New returns a Server that hands every request to h.
func New(h Handler, o Options) *Server {
	if o.Limits.MaxHead <= 0 || o.Limits.MaxFields <= 0 {
		d := h1.DefaultLimits()
		if o.Limits.MaxHead <= 0 {
			o.Limits.MaxHead = d.MaxHead
		}
		if o.Limits.MaxFields <= 0 {
			o.Limits.MaxFields = d.MaxFields
		}
	}
	if o.HeadTimeout <= 0 {
		o.HeadTimeout = 10 * time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 60 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 30 * time.Second
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 10 * time.Second
	}
	if o.MaxConns <= 0 {
		o.MaxConns = fileLimit() / 10 * 9
		if o.MaxConns <= 0 {
			o.MaxConns = 10000
		}
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	o.WS.defaults()
	s := &Server{
		h: h, o: o,
		listeners: map[net.Listener]struct{}{},
		drained:   make(chan struct{}),
		quit:      make(chan struct{}),
		sem:       make(chan struct{}, o.MaxConns),
	}
	s.pool.New = func() any { return newScratch() }
	s.encBufs.New = func() any { return new(encBuf) }
	for _, e := range o.Compress.Encoders {
		cd := e.Coding()
		s.coders = append(s.coders, coder{enc: e, coding: cd, line: []byte("Content-Encoding: " + cd + "\r\n")})
	}
	if s.o.Compress.MinSize <= 0 {
		s.o.Compress.MinSize = CompressDefaultMinSize
	}
	return s
}

func (s *Server) now() time.Time { return s.o.Clock.Now() }

// Serve accepts connections on l until Shutdown or Close, and returns ErrServerClosed then.
// While MaxConns connections are open it does not call Accept, so the kernel's backlog holds
// the rest (ADR 0024).
func (s *Server) Serve(l net.Listener) error { return s.serve(l, nil, nil) }

// ServeWith is Serve with the settings of one listener: TLS when tlsCfg is not nil (as
// ServeTLS), and the PROXY protocol when px is not nil (ADR 0058), each connection then starting
// with a header from a peer px allows, and any other peer closed at accept.
func (s *Server) ServeWith(l net.Listener, tlsCfg *tls.Config, px *ProxyProtocol) error {
	if tlsCfg != nil {
		tlsCfg = forceHTTP11(tlsCfg)
	}
	return s.serve(l, tlsCfg, px)
}

// ServeTLS is Serve for TLS: every accepted connection is wrapped with crypto/tls and handshakes
// on its own goroutine within HandshakeTimeout before its first request is awaited (ADR 0017).
// The config is cloned, and only http/1.1 is ever negotiated for traffic: the engine speaks nothing
// else, so it must never leave a client believing it agreed to h2. acme-tls/1 is kept when the
// config lists it, for a certificate challenge, and such a connection is closed after its
// handshake. The caller has already refused a
// config that offers anything else (bites.ListenTLS does); forcing NextProtos here, including on
// a config GetConfigForClient returns, is what keeps that true for one the caller could not see.
func (s *Server) ServeTLS(l net.Listener, cfg *tls.Config) error {
	return s.serve(l, forceHTTP11(cfg), nil)
}

// forceHTTP11 is the config ServeTLS serves with: a clone that offers http/1.1, and acme-tls/1
// when listed, on every path a config can come from.
func forceHTTP11(cfg *tls.Config) *tls.Config {
	cfg = cfg.Clone()
	cfg.NextProtos = allowedProtos(cfg.NextProtos)
	if inner := cfg.GetConfigForClient; inner != nil {
		cfg.GetConfigForClient = func(hi *tls.ClientHelloInfo) (*tls.Config, error) {
			c, err := inner(hi)
			if c != nil {
				c = c.Clone()
				c.NextProtos = allowedProtos(c.NextProtos)
			}
			return c, err
		}
	}
	return cfg
}

// ACMEProto is the protocol of a CA's tls-alpn-01 validation (RFC 8737): the handshake itself
// answers the challenge, with the certificate GetCertificate returns for it, and no request
// follows (ADR 0017).
const ACMEProto = "acme-tls/1"

// allowedProtos is what the engine offers: http/1.1 first, so a client that offers both gets it
// (the server's order decides in crypto/tls), and acme-tls/1 only when the config listed it.
func allowedProtos(listed []string) []string {
	for _, p := range listed {
		if p == ACMEProto {
			return []string{"http/1.1", ACMEProto}
		}
	}
	return []string{"http/1.1"}
}

func (s *Server) serve(l net.Listener, tlsCfg *tls.Config, px *ProxyProtocol) error {
	s.mu.Lock()
	if s.shutting.Load() {
		s.mu.Unlock()
		l.Close()
		return ErrServerClosed
	}
	s.listeners[l] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, l)
		s.mu.Unlock()
		l.Close()
	}()

	var delay time.Duration
	for {
		select {
		case s.sem <- struct{}{}:
		case <-s.quit:
			return ErrServerClosed
		}
		nc, err := l.Accept()
		if err != nil {
			<-s.sem
			select {
			case <-s.quit:
				return ErrServerClosed
			default:
			}
			if retryable(err) {
				delay = min(max(delay*2, 5*time.Millisecond), time.Second)
				s.o.Logger.Warn("accept failed, retrying", "err", err, "delay", delay)
				select {
				case <-time.After(delay):
					continue
				case <-s.quit:
					return ErrServerClosed
				}
			}
			return err
		}
		delay = 0
		var pxState uint8
		if px != nil {
			if !px.allows(nc) {
				// Not a peer the app trusts to say who its client is: closed before a byte is
				// read, its place given back at once (ADR 0058).
				s.proxyWarn("bites: a connection to a PROXY protocol listener came from a peer that is not a trusted proxy, and was closed", nc)
				nc.Close()
				<-s.sem
				continue
			}
			nc, pxState = &proxyConn{Conn: nc}, pxDue
		}
		if tlsCfg != nil {
			nc = tls.Server(nc, tlsCfg) // no bytes move until the connection's goroutine handshakes
		}
		c := &conn{srv: s, nc: nc, px: pxState}
		c.ex.c = c
		c.serveFn = c.serve
		if !s.add(c) {
			nc.Close()
			<-s.sem
			return ErrServerClosed
		}
		if tlsCfg != nil {
			go c.handshake()
		} else {
			go c.serveFn()
		}
	}
}

func retryable(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ECONNABORTED)
}

func (s *Server) add(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutting.Load() {
		return false
	}
	c.next = s.conns
	if s.conns != nil {
		s.conns.prev = c
	}
	s.conns = c
	s.nconns++
	return true
}

func (s *Server) remove(c *conn) {
	s.mu.Lock()
	if c.prev != nil {
		c.prev.next = c.next
	} else if s.conns == c {
		s.conns = c.next
	}
	if c.next != nil {
		c.next.prev = c.prev
	}
	c.prev, c.next = nil, nil
	s.nconns--
	done := s.nconns == 0 && s.shutting.Load()
	s.mu.Unlock()
	if done {
		s.drainOnce.Do(func() { close(s.drained) })
		s.closeWatcher()
	}
}

func (s *Server) stopAccepting() {
	s.shutting.Store(true)
	s.quitOnce.Do(func() { close(s.quit) })
	s.mu.Lock()
	ls := make([]net.Listener, 0, len(s.listeners))
	for l := range s.listeners {
		ls = append(ls, l)
	}
	s.mu.Unlock()
	for _, l := range ls {
		l.Close()
	}
}

// Shutdown stops accepting, closes idle connections now, cancels the context of every live stream
// with ErrServerClosed, sends every WebSocket a Close 1001 (spread over a window) and waits for
// requests in flight, a plain stream among them (their connections close after the response). When
// ctx ends first, the rest are closed, their live contexts cancelled, and ctx.Err() returned
// (ADR 0029).
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopAccepting()
	// The flag is set (stopAccepting) before the walk, and a stream or a handshake sets its state
	// before it reads the flag, so each of them is seen by one side or the other (ADR 0029).
	s.mu.Lock()
	var sockets []*WS
	for c := s.conns; c != nil; c = c.next {
		switch c.state.Load() {
		case stIdle:
			if c.state.CompareAndSwap(stIdle, stClosing) {
				c.closeFromOutside(c.nc) // a parked connection is woken to end
			}
		case stWS:
			sockets = append(sockets, c.ws)
		case stLive:
			if x := c.cur.Load(); x != nil {
				x.Cancel(ErrServerClosed)
			}
		}
	}
	win, asked := s.goAwayWindow(ctx, len(sockets))
	s.leaveSet, s.leaveWin = true, win
	empty := s.nconns == 0
	s.mu.Unlock()
	// The 1001 of every WebSocket, each after its own share of the window so that 20,000 clients
	// do not reconnect to the next instance in the same millisecond. Outside the lock: beginClose
	// takes the socket's.
	if len(sockets) > 0 {
		// Nothing is shortened in silence (ADR 0029): the window used is logged, and a window the
		// context's half cut says so and what to change.
		if win < asked {
			s.o.Logger.Warn("bites: shutdown: WebSocket Close 1001 spread cut short by half of what is left of Shutdown's context",
				"sockets", len(sockets), "window", win, "asked", asked, "fix", "give Shutdown a longer context, or WithGoAwaySpread a shorter window")
		} else {
			s.o.Logger.Info("bites: shutdown: WebSocket Close 1001 spread", "sockets", len(sockets), "window", win, "asked", asked)
		}
	}
	for _, w := range sockets {
		w.goAway(s.goAwayDelay(win))
	}
	if empty {
		s.drainOnce.Do(func() { close(s.drained) })
	}
	select {
	case <-s.drained:
		s.closeWatcher()
		return nil
	case <-ctx.Done():
		s.forceClose()
		return ctx.Err()
	}
}

// Close stops accepting and closes every connection at once.
func (s *Server) Close() error {
	s.stopAccepting()
	s.forceClose()
	return nil
}

func (s *Server) forceClose() {
	s.mu.Lock()
	for c := s.conns; c != nil; c = c.next {
		if x := c.cur.Load(); x != nil {
			x.Cancel(ErrServerClosed)
		}
		c.closeFromOutside(c.nc)
	}
	s.mu.Unlock()
	s.closeWatcher()
}

func (s *Server) closeWatcher() {
	// The watcher is created lazily and shared; closing it with connections still armed would
	// strand them, so it is closed only once none is left to cancel.
	s.mu.Lock()
	n := s.nconns
	s.mu.Unlock()
	if w := s.watcher.Load(); n == 0 && w != nil {
		w.Close()
	}
	if w := s.wheel.Load(); n == 0 && w != nil {
		w.Close()
	}
	if p := s.poll.Load(); n == 0 && p != nil {
		p.close()
	}
}

// getWatcher creates the engine-wide epoll watcher on first use; nil means use the fallback.
func (s *Server) getWatcher() *reqctx.Watcher {
	s.wOnce.Do(func() {
		if w, err := reqctx.NewWatcher(); err == nil {
			s.watcher.Store(w)
		}
	})
	return s.watcher.Load()
}

type dateEntry struct {
	sec  int64
	line []byte // "Date: ...\r\n"
}

// dateLine is the Date header at now, formatted once a second (not once a response). The caller
// passes the time it already read for its write deadline, so a response reads the clock once.
func (s *Server) dateLine(now time.Time) []byte {
	now = now.UTC()
	if e := s.date.Load(); e != nil && e.sec == now.Unix() {
		return e.line
	}
	line := append([]byte("Date: "), now.Format("Mon, 02 Jan 2006 15:04:05 GMT")...)
	line = append(line, '\r', '\n')
	e := &dateEntry{sec: now.Unix(), line: line}
	s.date.Store(e)
	return line
}
