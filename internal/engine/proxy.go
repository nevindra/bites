package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/nevindra/bites/internal/proxyproto"
)

// The PROXY protocol (ADR 0058). A listener served with a ProxyProtocol expects every connection
// to start with a PROXY header from a peer the app trusts. A peer it does not trust is closed at
// accept, before a byte is read, and gives its MaxConns place back at once. A trusted peer's
// connection is wrapped in a proxyConn before its goroutine starts, so that every other goroutine
// (Shutdown, a WebSocket's flusher) sees one c.nc for the connection's whole life; the header
// itself is read by the connection's goroutine:
//
//   - plain: as the first bytes of the first request. The wait for its first byte is the idle
//     wait, parked like any other (ADR 0005), and once that byte is in, the whole header must
//     arrive within HeadTimeout, as a head must. It is parsed in the pooled read buffer, and the
//     bytes after it stay there as the start of the first request.
//   - TLS: before the handshake, within the HandshakeTimeout that runs from accept (ADR 0017).
//     The bytes read past the header (the start of the ClientHello) are kept in the proxyConn,
//     which hands them to crypto/tls before it reads the socket again.
//
// A header that does not parse, does not arrive in time, or is missing (the peer sent HTTP or a
// ClientHello straight away) closes the connection with nothing written: the peer is a balancer
// speaking a protocol wrongly, not a client a problem+json would help.

// ProxyProtocol turns the PROXY protocol on for one listener.
type ProxyProtocol struct {
	// Trusts reports whether a TCP peer may send a header. It is the app's trusted-proxy list.
	Trusts func(netip.Addr) bool
	// Unix is whether a peer on a Unix socket may send one.
	Unix bool
}

// allows reports whether the peer of a connection just accepted may send a PROXY header.
func (p *ProxyProtocol) allows(nc net.Conn) bool {
	ap, unix := peerOf(nc)
	if unix {
		return p.Unix
	}
	return ap.IsValid() && p.Trusts != nil && p.Trusts(ap.Addr().Unmap())
}

// What c.px says of a connection.
const (
	pxNone uint8 = iota // its listener does not speak the PROXY protocol
	pxDue               // its header has not been read yet
	pxRead              // its header was read; the proxyConn says what it named
)

// proxyConn is the connection of a listener that speaks the PROXY protocol. It is the connection
// with the client the header named in place of the balancer, and, over TLS, with the bytes read
// past the header. Only these connections carry one (about 80 bytes); others are unchanged.
type proxyConn struct {
	net.Conn
	peer  netip.AddrPort // the client the header named, when named
	named bool
	rest  []byte // TLS: bytes read past the header, read before the socket
}

// Read hands back what was read past the header first, then reads the socket.
func (p *proxyConn) Read(b []byte) (int, error) {
	if len(p.rest) > 0 {
		n := copy(b, p.rest)
		p.rest = p.rest[n:]
		if len(p.rest) == 0 {
			p.rest = nil
		}
		return n, nil
	}
	return p.Conn.Read(b)
}

// RemoteAddr is the client the header named, or the connection's own peer when it named none
// (a LOCAL header, UNKNOWN). The engine reads the address through RemoteAddrPort, which does not
// allocate; this one builds a *net.TCPAddr, for a log line.
func (p *proxyConn) RemoteAddr() net.Addr {
	if p.named {
		return net.TCPAddrFromAddrPort(p.peer)
	}
	return p.Conn.RemoteAddr()
}

// proxyConn is the proxyConn under c.nc, or nil.
func (c *conn) proxyConn() *proxyConn {
	if c.px == pxNone {
		return nil
	}
	nc := c.nc
	if tc, ok := nc.(*tls.Conn); ok {
		nc = tc.NetConn()
	}
	pc, _ := nc.(*proxyConn)
	return pc
}

// peerOf is the address of a connection's peer, without allocating for a TCP or an in-memory
// connection, and whether the peer is on a Unix socket, which has no address (ADR 0068).
func peerOf(nc net.Conn) (netip.AddrPort, bool) {
	switch a := nc.RemoteAddr().(type) {
	case *net.TCPAddr:
		return a.AddrPort(), false
	case *net.UnixAddr:
		return netip.AddrPort{}, true
	case interface{ AddrPort() netip.AddrPort }:
		return a.AddrPort(), false
	case nil:
		return netip.AddrPort{}, false
	default:
		ap, _ := netip.ParseAddrPort(a.String())
		return ap, false
	}
}

// readProxy reads the PROXY header of a plain connection from the read buffer, whose first byte
// await has just read, and reports whether the connection goes on. The bytes after the header
// are moved to the front of the buffer, the start of the first request; with none, the scratch
// goes back to the pool and the connection waits idle for its first request.
func (c *conn) readProxy() bool {
	s := c.srv
	sc := c.sc
	c.setReadDeadline(s.now().Add(s.o.HeadTimeout))
	for {
		h, err := proxyproto.Parse(sc.buf[:c.r1])
		if err == nil {
			c.setProxied(h)
			c.r0, c.r1 = 0, copy(sc.buf, sc.buf[h.Len:c.r1])
			c.setReadDeadline(time.Time{})
			if c.r1 == 0 {
				s.putScratch(sc)
				c.sc = nil
			}
			return true
		}
		if err != proxyproto.ErrNeedMore || c.r1 == len(sc.buf) {
			s.proxyFailed(c, err)
			return false
		}
		m, rerr := c.nc.Read(sc.buf[c.r1:])
		c.r1 += m
		if rerr != nil && m == 0 {
			s.proxyFailed(c, rerr)
			return false
		}
	}
}

// readProxyTLS reads the PROXY header of a TLS connection from the socket, before the handshake
// and under its deadline. What was read past the header is kept for crypto/tls.
func (c *conn) readProxyTLS() bool {
	s := c.srv
	pc := c.proxyConn()
	sc := s.getScratch()
	defer s.putScratch(sc)
	n := 0
	for {
		h, err := proxyproto.Parse(sc.buf[:n])
		if err == nil {
			c.setProxied(h)
			if n > h.Len {
				pc.rest = append([]byte(nil), sc.buf[h.Len:n]...)
			}
			return true
		}
		if err != proxyproto.ErrNeedMore || n == len(sc.buf) {
			s.proxyFailed(c, err)
			return false
		}
		m, rerr := pc.Conn.Read(sc.buf[n:])
		n += m
		if rerr != nil && m == 0 {
			s.proxyFailed(c, rerr)
			return false
		}
	}
}

// setProxied records what a header said: the client it named becomes the connection's peer.
func (c *conn) setProxied(h proxyproto.Header) {
	if pc := c.proxyConn(); pc != nil && h.Named {
		pc.peer, pc.named = h.Source, true
	}
	c.px = pxRead
}

// proxyFailed logs a connection closed for its PROXY header. A trusted peer that sent no header
// at all is almost always a balancer not configured to send one, which closes every connection,
// so it is a Warn, at most one a minute with the ones skipped counted; a broken or slow header is
// a Debug line.
func (s *Server) proxyFailed(c *conn, err error) {
	if errors.Is(err, proxyproto.ErrNotProxy) {
		s.proxyWarn("bites: a trusted peer did not start its connection with a PROXY header (is the balancer set to send one?)", c.raw())
		return
	}
	if lg := s.o.Logger; lg.Enabled(context.Background(), slog.LevelDebug) {
		lg.Debug("bites: PROXY header refused", "err", err, "remote", c.raw().RemoteAddr().String())
	}
}

// proxyWarn writes a Warn about the PROXY protocol at most once a minute on the engine's clock.
func (s *Server) proxyWarn(msg string, nc net.Conn) {
	now := s.now().UnixNano()
	last := s.pxWarnAt.Load()
	if last != 0 && now-last < int64(warnEvery) || !s.pxWarnAt.CompareAndSwap(last, now) {
		s.pxSuppressed.Add(1)
		return
	}
	s.o.Logger.Warn(msg, "remote", nc.RemoteAddr().String(), "suppressed", s.pxSuppressed.Swap(0))
}
