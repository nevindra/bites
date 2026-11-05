package engine

import (
	"net"
	"syscall"

	"github.com/nevindra/bites/internal/wheel"
)

// Parking (ADR 0005, step 16 of the roadmap). A connection still idle after moveAfter is
// registered with the engine's poller (one epoll instance and one goroutine, beside the disconnect
// watcher) for one readiness event, and its goroutine ends: an idle connection then holds no
// goroutine and no stack. The first of these to happen gives it a goroutine again, which waits as
// a connection that has just moved (conn.await) and so reads what woke it at once:
//
//   - bytes, a hangup or an error on the socket (the poller);
//   - the idle timeout, which moves from the read deadline to the engine's timer wheel;
//   - Shutdown or Close, which close the socket and then wake it;
//   - a WebSocket's own end from another goroutine: the close handshake's deadline on the wheel,
//     or the flusher or an inline Send that closes the socket.
//
// Everything else a parked WebSocket needs runs on other goroutines already and goes on doing so:
// the heartbeat is the wheel's, and Send, Publish, Close and Shutdown's 1001 are written by the
// inline write or the flusher (ADR 0037), never by the goroutine that reads.
//
// # Exactly one goroutine owns a connection
//
// The connection's pk word says who owns it:
//
//	pkOwned    a goroutine owns it (serving, or waiting in a read)
//	pkParking  its goroutine is registering it; a wake now is left to that goroutine
//	pkParked   no goroutine owns it; the first wake to swap it back to pkOwned starts one
//	pkWoken    a wake came while it registered; the registering goroutine keeps it
//
// The owner sets pkParking before it registers and swaps pkParking for pkParked after: if a wake
// came in between, the swap fails, the owner sees pkWoken, takes the connection back and reads
// again. A wake swaps pkParked for pkOwned, and only the one whose swap succeeds starts a
// goroutine, so two readiness events, or readiness and the idle timeout together, start one.
// Bytes that arrived between the decision to park and the registration are not lost: epoll checks
// readiness when a descriptor is added or re-armed and reports bytes already queued, and nothing
// read them, because the decision follows a read that returned none. The registration is one-shot
// (EPOLLONESHOT), so it fires once and stays silent until the next park re-arms it. A connection
// that has ended is pkOwned for ever (only its owner ends it), so a late event, a late wheel
// callback or Shutdown never starts a goroutine for it.
//
// # TLS
//
// crypto/tls may hold bytes it has decrypted, or a whole record it has read, while the kernel's
// socket shows nothing to read, and it has no method that says so. The engine never needs one:
// it parks a connection only after the one-byte idle read on the *tls.Conn returned a timeout
// with no byte, and tls.Conn.Read returns a timeout only when its buffer of decrypted bytes is
// empty and it is waiting on the socket for the rest of a record. So what it holds then is at
// most a part of a record, whose rest has to come from the socket and wakes it. A handshake never
// parks: it runs before the connection first waits.
//
// # What a connection that parks carries
//
// One parkState, made the first time it parks and kept: the raw socket, the callback for the
// registration, and the wheel node of the idle timeout. A connection that is never idle past
// moveAfter makes none.

// The pk word.
const (
	pkOwned int32 = iota
	pkParking
	pkParked
	pkWoken
)

// parkState is what a connection that has parked once keeps for its next parks.
type parkState struct {
	c        *conn
	rc       syscall.RawConn
	resumeFn func()     // c.resume, made once so a wake allocates no closure
	node     wheel.Node // the idle timeout of a parked HTTP connection
	pollerState
}

// parkable reports whether the connection may park now, at the end of a moveAfter wait that read
// nothing. The rest (a poller, a socket it can register) is found out by park itself.
func (c *conn) parkable() bool {
	s := c.srv
	if s.o.NoPark || c.hasPend || c.sc != nil || c.r1 > c.r0 {
		return false
	}
	if w := c.ws; w != nil && (w.msgDue != 0 || w.asm != nil) {
		return false // a message that did not arrive whole keeps its deadline on a goroutine (ADR 0046)
	}
	_, ok := socketOf(c.raw())
	return ok
}

// socketOf is the kernel socket under a connection the engine may park or watch with epoll: a TCP
// connection, or a Unix stream socket, which reports readiness and the peer's half-close the same
// way (ADR 0068). Anything else (an in-memory connection, a wrapper) has none.
func socketOf(nc net.Conn) (syscall.Conn, bool) {
	switch c := nc.(type) {
	case *net.TCPConn:
		return c, true
	case *net.UnixConn:
		return c, true
	}
	return nil, false
}

// park hands the connection to the poller. It reports whether this goroutine must now end: true
// when the connection is parked, or when parking failed and the wait moved to a fresh goroutine
// instead (ADR 0005's move); false when a wake came while it registered, and the caller goes on
// waiting with the connection.
func (c *conn) park() bool {
	s := c.srv
	p := s.getPoller()
	if p == nil {
		return c.move()
	}
	ps := c.ps
	if ps == nil {
		sk, ok := socketOf(c.raw())
		if !ok {
			return c.move()
		}
		rc := c.rc // the RawConn of a bare TCP socket, made at its first answer
		if rc == nil {
			var err error
			if rc, err = sk.SyscallConn(); err != nil {
				return c.move()
			}
		}
		ps = &parkState{c: c, rc: rc}
		ps.resumeFn = c.resume
		ps.node.Init(c.wake)
		ps.init()
		c.ps = ps
	}
	c.pk.Store(pkParking)
	if err := p.arm(ps); err != nil {
		c.pk.Store(pkOwned)
		return c.move() // the socket is closed (the moved read sees it and ends) or epoll refused it
	}
	timed := !c.idleUntil.IsZero()
	if timed {
		s.timers().Add(&ps.node, c.idleUntil.Sub(s.now()))
	}
	if c.pk.CompareAndSwap(pkParking, pkParked) {
		return true // from here on this goroutine touches nothing of the connection
	}
	// Woken while registering: the connection stays with this goroutine. The registration may
	// still be armed; an event from it finds the connection owned and is dropped.
	c.pk.Store(pkOwned)
	if timed {
		s.timers().Remove(&ps.node)
	}
	return false
}

// move hands the rest of the wait to a fresh goroutine, the path of ADR 0005 that parking
// replaces where it can.
func (c *conn) move() bool {
	c.moved = true
	go c.serveFn()
	return true
}

// wake gives a parked connection a goroutine, at most once whoever calls it and however often.
// It is safe from any goroutine, the poller's and the wheel's included: it never blocks.
func (c *conn) wake() {
	for {
		switch c.pk.Load() {
		case pkParked:
			if c.pk.CompareAndSwap(pkParked, pkOwned) {
				go c.ps.resumeFn()
				return
			}
		case pkParking:
			if c.pk.CompareAndSwap(pkParking, pkWoken) {
				return
			}
		default:
			return // owned: its goroutine sees for itself what happened
		}
	}
}

// resume is the goroutine a wake started. The idle timeout's node is taken off the wheel, then
// the connection waits as one that has just been woken: what woke it is read at once.
func (c *conn) resume() {
	if !c.idleUntil.IsZero() {
		if t := c.srv.wheel.Load(); t != nil {
			t.Remove(&c.ps.node)
		}
	}
	c.serve()
}

// closeFromOutside closes the connection (nc is c.nc or the socket under it) from a goroutine
// that does not own it, and wakes it if it is parked, so that its owner sees the close and ends:
// only the owner runs finish, which gives back MaxConns, the server's list and a WebSocket's
// OnClose. The close comes first, so the woken read fails at once.
func (c *conn) closeFromOutside(nc net.Conn) {
	nc.Close()
	c.wake()
}

// unpark takes a connection that has parked off the poller for good, before its socket is
// closed. Only the owner calls it, from finish.
func (c *conn) unpark() {
	if c.ps == nil {
		return
	}
	if p := c.srv.poll.Load(); p != nil {
		p.drop(c.ps)
	}
	if t := c.srv.wheel.Load(); t != nil {
		t.Remove(&c.ps.node)
	}
}
