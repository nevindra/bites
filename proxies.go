package bites

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
)

// TrustedProxies names the proxies in front of the app whose forwarding headers are believed
// (ADR 0025). Without WithTrustedProxies no proxy is trusted: Request.ClientAddr is the peer of
// the connection and no forwarding header is read.
type TrustedProxies struct {
	// Prefixes are the networks the app's load balancers and proxies connect from
	// (netip.MustParsePrefix("10.0.0.0/8")). A connection from inside one of them may say who the
	// client is; any other connection cannot. A prefix with host bits set is masked. At least one
	// is required unless UnixSockets is set, and a prefix of length 0 is refused, since it would
	// trust every peer.
	Prefixes []netip.Prefix

	// UnixSockets trusts the peer of every Unix socket the app listens on (ListenUnix) as a proxy
	// (ADR 0058): the socket file's permission bits decide who may connect, so they are the list.
	// A Unix peer has no address to put in Prefixes. False by default.
	UnixSockets bool

	// Header is the header the trusted proxies write the client's address in. "X-Forwarded-For"
	// when empty: a list each proxy appends to, read from the right. Any other name
	// ("CF-Connecting-IP", "X-Real-IP", "Fly-Client-IP") is read as one address written by the
	// nearest trusted proxy. "Forwarded" (RFC 7239) and the forwarding headers that do not carry an
	// address (X-Forwarded-Host, X-Forwarded-Proto, X-Forwarded-Prefix) are refused.
	Header string

	// ForwardedHost says the trusted proxies write X-Forwarded-Host, replacing any the client sent:
	// Request.PublicHost then reads it from a trusted peer, and a WebSocket's Origin check accepts it
	// as the request's own host (ADR 0046). False by default, because a balancer
	// that writes X-Forwarded-For but passes X-Forwarded-Host through untouched would let a client
	// name the host of every link the app builds.
	ForwardedHost bool

	// ForwardedPrefix says the trusted proxies write X-Forwarded-Prefix, the path prefix they
	// strip before forwarding, replacing any the client sent: Request.PublicPrefix then reads it
	// from a trusted peer. False by default.
	ForwardedPrefix bool
}

const defaultForwardedHeader = "X-Forwarded-For"

// proxyPolicy is TrustedProxies checked and ready: the prefixes masked and unmapped, and whether
// the header is the X-Forwarded-For list or a single address.
type proxyPolicy struct {
	prefixes []netip.Prefix
	header   string
	list     bool
	unix     bool // a Unix socket's peer is trusted (ADR 0058)
	host     bool // X-Forwarded-Host is read from a trusted peer
	prefix   bool // X-Forwarded-Prefix is read from a trusted peer
	where    string
}

// WithTrustedProxies names the proxies in front of the app (ADR 0025). Request.ClientAddr then
// reads the client's address from p.Header when, and only when, the connection comes from one
// of p.Prefixes, and Request.Scheme reads X-Forwarded-Proto on the same condition; with
// p.ForwardedHost and p.ForwardedPrefix, Request.PublicHost and Request.PublicPrefix read
// X-Forwarded-Host and X-Forwarded-Prefix on it too.
//
// New panics when it is given twice, with no prefix, with a zero or a length-0 prefix
// (0.0.0.0/0, ::/0), with a Header that is not a token, or with a Header that does not carry
// one address (Forwarded, X-Forwarded-Host, X-Forwarded-Proto, X-Forwarded-Prefix).
func WithTrustedProxies(p TrustedProxies) Option {
	return func(a *App) {
		where := callerWhere()
		if a.proxies != nil {
			panic(&RegistrationError{msg: "WithTrustedProxies: given twice (first at " + a.proxies.where + ", again at " + where + "): give one TrustedProxies, because two could say different things"})
		}
		refuse := func(format string, args ...any) {
			panic(&RegistrationError{msg: "WithTrustedProxies: " + fmt.Sprintf(format, args...) + "\n\tregistered at " + where})
		}
		if len(p.Prefixes) == 0 && !p.UnixSockets {
			refuse(`Prefixes is empty and UnixSockets is false: list the networks your load balancers connect from (netip.MustParsePrefix("10.0.0.0/8")), set UnixSockets for a proxy on the app's Unix socket, or leave the option out when no proxy is in front`)
		}
		pol := &proxyPolicy{header: p.Header, unix: p.UnixSockets, host: p.ForwardedHost, prefix: p.ForwardedPrefix, where: where}
		if pol.header == "" {
			pol.header = defaultForwardedHeader
		}
		if !isToken(pol.header) {
			refuse("Header %q is not an HTTP header name", p.Header)
		}
		switch strings.ToLower(pol.header) {
		case "forwarded":
			refuse(`Header %q is not read: its elements are "for=...;proto=...;host=..." pairs, not one address, and only the X-Forwarded-* headers are read (ADR 0025); name X-Forwarded-For (the default), or the single-address header your proxy writes`, p.Header)
		case "x-forwarded-host", "x-forwarded-proto", "x-forwarded-prefix":
			refuse("Header %q does not carry the client's address: name X-Forwarded-For (the default), or the single-address header your proxy writes (CF-Connecting-IP, X-Real-IP); the forwarded host and prefix are ForwardedHost and ForwardedPrefix", p.Header)
		}
		pol.list = strings.EqualFold(pol.header, defaultForwardedHeader)
		for i, pf := range p.Prefixes {
			if !pf.IsValid() {
				refuse("Prefixes[%d] is not a valid prefix (the zero netip.Prefix, or a length past the address): build it with netip.ParsePrefix", i)
			}
			if pf.Addr().Is4In6() && pf.Bits() >= 96 {
				pf = netip.PrefixFrom(pf.Addr().Unmap(), pf.Bits()-96)
			}
			if pf.Bits() == 0 {
				refuse("Prefixes[%d] %s trusts every peer, so any client could name any address in %s: list the networks of your proxies", i, pf, pol.header)
			}
			pol.prefixes = append(pol.prefixes, pf.Masked())
		}
		a.proxies = pol
		a.custom["trusted proxies"] = true
	}
}

// peer is the request's peer address, unmapped, and whether it is a trusted proxy: inside one of
// the prefixes, or the process at the other end of a Unix socket when UnixSockets is set (whose
// address is the zero Addr). A nil policy trusts nobody.
func (p *proxyPolicy) peer(ex *engine.Exchange) (netip.Addr, bool) {
	a := ex.RemoteAddrPort().Addr().Unmap()
	if p == nil {
		return a, false
	}
	if a.IsValid() {
		return a, p.trusts(a)
	}
	return a, p.unix && ex.PeerIsUnix()
}

// trusts reports whether addr is inside one of the prefixes: a linear scan, a comparison or two
// a prefix (ADR 0025 says why a table waits).
func (p *proxyPolicy) trusts(addr netip.Addr) bool {
	for _, pf := range p.prefixes {
		if pf.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientAddr is the client's address as far as the app can trust it (ADR 0025): the peer of the
// connection, unless the peer is one of the trusted proxies of WithTrustedProxies, in which case
// it is read from their header.
//
// From X-Forwarded-For, the default header, it is the rightmost address that is not a trusted
// proxy: the list is read from the right, each line of a repeated header in turn (the last line
// rightmost), a port written after an address is dropped, and an element that does not parse
// (or is "unknown") stops the walk and answers the nearest trusted address to its right. When
// every element is trusted it is the leftmost one. From any other header it is the one address
// of its last line, and the peer when that does not parse.
//
// An IPv4 address mapped into IPv6 (::ffff:192.0.2.1) is answered as IPv4. The zero Addr after
// the request was answered. It allocates nothing.
func (r Request) ClientAddr() netip.Addr {
	if !r.live() {
		return netip.Addr{}
	}
	p := r.run.a.proxies
	peer, trusted := p.peer(r.run.ex)
	if !trusted {
		return peer
	}
	if !p.list {
		v, ok := r.lastHeader(p.header)
		if !ok {
			return peer
		}
		if a, ok := parseForwardedAddr(trimOWS(v)); ok {
			return a
		}
		return peer
	}
	last := peer // the nearest address the walk has trusted so far
	for b := r.headers; len(b) > 0; {
		var line string
		line, b = lastLine(b)
		v, ok := headerLineValue(line, p.header)
		if !ok {
			continue
		}
		for v != "" {
			var elem string
			if k := strings.LastIndexByte(v, ','); k >= 0 {
				elem, v = v[k+1:], v[:k]
			} else {
				elem, v = v, ""
			}
			elem = trimOWS(elem)
			if elem == "" {
				continue
			}
			a, ok := parseForwardedAddr(elem)
			if !ok {
				return last // a broken link: never a guess past it
			}
			if !p.trusts(a) {
				return a
			}
			last = a
		}
	}
	return last
}

// Scheme is "https" when the request came over TLS the app terminated, the value of
// X-Forwarded-Proto when the peer is a trusted proxy and the header's rightmost element says
// "http" or "https" (in any case), and "http" otherwise (ADR 0025). It is what a redirect, a
// Secure cookie and HSTS need. The empty string after the request was answered. It allocates
// nothing.
func (r Request) Scheme() string {
	if !r.live() {
		return ""
	}
	ex := r.run.ex
	if ex.IsTLS() {
		return "https"
	}
	if _, trusted := r.run.a.proxies.peer(ex); !trusted {
		return "http"
	}
	v, ok := r.lastHeader("X-Forwarded-Proto")
	if !ok {
		return "http"
	}
	if strings.EqualFold(rightmost(v), "https") {
		return "https"
	}
	return "http"
}

// PublicHost is the host, with its port when one was given, that the client asked for (ADR 0025):
// the X-Forwarded-Host a trusted proxy wrote, when TrustedProxies.ForwardedHost is set, and the
// Host header otherwise. Of X-Forwarded-Host the rightmost element of the last line is read, the
// one the nearest proxy wrote; it must be a host name, an IPv4 address or a bracketed IPv6
// address, with an optional port, and anything else (a path, a userinfo, a space, an IPv6
// address without brackets, a port past 65535) answers the Host header, never a guess.
//
// It is what an absolute URL the app builds for a client needs, with Scheme and PublicPrefix
// (PublicURL joins them). The empty string after the request was answered, and for an HTTP/1.0
// request with no Host. It allocates nothing.
func (r Request) PublicHost() string {
	if !r.live() {
		return ""
	}
	if p := r.run.a.proxies; p != nil && p.host {
		if _, trusted := p.peer(r.run.ex); trusted {
			if v, ok := r.lastHeader("X-Forwarded-Host"); ok {
				if h := rightmost(v); validHost(h) {
					return h
				}
			}
		}
	}
	return r.Host()
}

// forwardedHost is the host PublicHost reads from X-Forwarded-Host, taken from the head itself
// for a WebSocket's Origin check (ADR 0046), which runs before the request has a Request view:
// the rightmost element of the last line, when the peer is trusted and TrustedProxies.ForwardedHost
// is set, and "" otherwise or when it is not a host. The string points into the read buffer, so it
// is compared and dropped, never kept.
func (p *proxyPolicy) forwardedHost(ex *engine.Exchange) string {
	if p == nil || !p.host {
		return ""
	}
	if _, trusted := p.peer(ex); !trusted {
		return ""
	}
	hs := ex.Head().Headers
	for i := len(hs) - 1; i >= 0; i-- {
		if equalFold(hs[i].Name, "x-forwarded-host") {
			v := hs[i].Value
			if h := rightmost(unsafe.String(unsafe.SliceData(v), len(v))); validHost(h) {
				return h
			}
			return ""
		}
	}
	return ""
}

// PublicPrefix is the path prefix a trusted proxy strips before forwarding (ADR 0025): the
// X-Forwarded-Prefix it wrote, when TrustedProxies.ForwardedPrefix is set, and "" otherwise. Of
// X-Forwarded-Prefix the rightmost element of the last line is read, the one the nearest proxy
// wrote; a trailing slash is dropped, so "/api/" and "/api" are "/api" and "/" is "". It must be
// an absolute path of RFC 3986 path characters with no empty, "." or ".." segment, so that the
// prefix followed by a path can never be read as another host ("//evil.example", "/\evil.example")
// or climb out of it; anything else answers "".
//
// A link the app writes for the client is the prefix followed by the route's path: a Location of
// "/rooms/7" behind a proxy that strips "/chat" is PublicPrefix()+"/rooms/7". The empty string
// after the request was answered. It allocates nothing.
func (r Request) PublicPrefix() string {
	if !r.live() {
		return ""
	}
	p := r.run.a.proxies
	if p == nil || !p.prefix {
		return ""
	}
	if _, trusted := p.peer(r.run.ex); !trusted {
		return ""
	}
	v, ok := r.lastHeader("X-Forwarded-Prefix")
	if !ok {
		return ""
	}
	pre, ok := cleanPrefix(rightmost(v))
	if !ok {
		return ""
	}
	return pre
}

// PublicURL is the absolute URL the client would use for path, a path of the app's own routes
// that starts with a slash and is already percent-encoded: Scheme, "://", PublicHost,
// PublicPrefix and path (ADR 0025). Behind a proxy that strips "/chat" for https://example.com,
// PublicURL("/rooms/7") is "https://example.com/chat/rooms/7". With no host (an HTTP/1.0 request
// without one) it is PublicPrefix and path alone, a reference relative to the host. The empty
// string after the request was answered. It allocates the string it returns, one allocation.
func (r Request) PublicURL(path string) string {
	if !r.live() {
		return ""
	}
	host := r.PublicHost()
	if host == "" {
		return r.PublicPrefix() + path
	}
	return r.Scheme() + "://" + host + r.PublicPrefix() + path
}

// rightmost is the last element of a comma-separated list value, without the whitespace around
// it: the element the nearest proxy wrote.
func rightmost(v string) string {
	if k := strings.LastIndexByte(v, ','); k >= 0 {
		v = v[k+1:]
	}
	return trimOWS(v)
}

// validHost reports whether s is a URI authority with no userinfo: a host name or IPv4 address of
// letters, digits, '-', '.' and '_', or an IPv6 address in brackets without a zone, then
// optionally ':' and a port of 1 to 5 digits no larger than 65535. It allocates nothing.
func validHost(s string) bool {
	host, port := s, ""
	if strings.HasPrefix(s, "[") {
		j := strings.IndexByte(s, ']')
		if j < 0 {
			return false
		}
		a, err := netip.ParseAddr(s[1:j])
		if err != nil || !a.Is6() || a.Zone() != "" {
			return false
		}
		switch rest := s[j+1:]; {
		case rest == "":
			return true
		case rest[0] != ':':
			return false
		default:
			return validPort(rest[1:])
		}
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		host, port = s[:i], s[i+1:]
		if !validPort(port) {
			return false
		}
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// validPort reports whether s is a port number: 1 to 5 digits, at most 65535.
func validPort(s string) bool {
	if !isPort(s) {
		return false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n <= 65535
}

// cleanPrefix checks a path prefix and drops its trailing slash: "" and "/" are "", and anything
// else must start with a slash and be segments of RFC 3986 path characters (unreserved,
// percent-encoded, sub-delims, ':' and '@'), none of them empty, "." or ".." (written with dots or
// %2e). A backslash, '?', '#', a space, a control byte or a bad escape is refused. It allocates
// nothing.
func cleanPrefix(s string) (string, bool) {
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", true
	}
	if s[0] != '/' {
		return "", false
	}
	for rest := s[1:]; ; {
		seg := rest
		i := strings.IndexByte(rest, '/')
		if i >= 0 {
			seg = rest[:i]
		}
		if seg == "" || isDotSegment(seg) {
			return "", false
		}
		for k := 0; k < len(seg); k++ {
			c := seg[k]
			switch {
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
			case strings.IndexByte("-._~!$&'()*+,;=:@", c) >= 0:
			case c == '%':
				if k+2 >= len(seg) || !isHex(seg[k+1]) || !isHex(seg[k+2]) {
					return "", false
				}
				k += 2
			default:
				return "", false
			}
		}
		if i < 0 {
			return s, true
		}
		rest = rest[i+1:]
	}
}

// isDotSegment reports whether seg is "." or "..", each dot written as itself or as %2e.
func isDotSegment(seg string) bool {
	dots := 0
	for seg != "" {
		switch {
		case seg[0] == '.':
			seg = seg[1:]
		case len(seg) >= 3 && seg[0] == '%' && seg[1] == '2' && (seg[2] == 'e' || seg[2] == 'E'):
			seg = seg[3:]
		default:
			return false
		}
		dots++
	}
	return dots == 1 || dots == 2
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// lastHeader is the value of the last line of the header called name.
func (r Request) lastHeader(name string) (string, bool) {
	for b := r.headers; len(b) > 0; {
		var line string
		line, b = lastLine(b)
		if v, ok := headerLineValue(line, name); ok {
			return v, true
		}
	}
	return "", false
}

// lastLine splits the last line off a block of header lines, without its line ending, and
// returns it with what comes before it.
func lastLine(b string) (line, rest string) {
	j := len(b)
	if b[j-1] == '\n' {
		j--
	}
	i := strings.LastIndexByte(b[:j], '\n')
	line = b[i+1 : j]
	if i < 0 {
		return line, ""
	}
	return line, b[:i+1]
}

// headerLineValue is the value of one header line ("Name: value", a CR at the end allowed) when the
// line is the header called name, matched without regard to case.
func headerLineValue(line, name string) (string, bool) {
	n := len(name)
	if len(line) <= n || line[n] != ':' || !foldPrefix(line, name) {
		return "", false
	}
	return trimOWS(line[n+1:]), true
}

// trimOWS drops spaces, tabs and a trailing CR from both ends.
func trimOWS(v string) string {
	for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
		v = v[1:]
	}
	for len(v) > 0 && (v[len(v)-1] == '\r' || v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
		v = v[:len(v)-1]
	}
	return v
}

// parseForwardedAddr reads one element of a forwarding header: an address, an IPv4 address with
// a port, or a bracketed IPv6 address with or without a port. A zone is dropped and a mapped
// IPv4 address unmapped.
func parseForwardedAddr(s string) (netip.Addr, bool) {
	host := s
	switch {
	case strings.HasPrefix(s, "["):
		j := strings.IndexByte(s, ']')
		if j < 0 {
			return netip.Addr{}, false
		}
		host = s[1:j]
		if rest := s[j+1:]; rest != "" && (rest[0] != ':' || !isPort(rest[1:])) {
			return netip.Addr{}, false
		}
	case strings.Count(s, ":") == 1:
		var port string
		host, port, _ = strings.Cut(s, ":")
		if !isPort(port) {
			return netip.Addr{}, false
		}
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	if a.Zone() != "" {
		a = a.WithZone("")
	}
	return a.Unmap(), true
}

func isPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ListenOption is a setting of one listener, given to Listen, ListenTLS, ListenUnix, Serve or
// ServeTLS after their other arguments. ProxyProtocol is the one there is.
type ListenOption func(*listenConfig)

type listenConfig struct{ proxy bool }

// ProxyProtocol makes a listener expect the PROXY protocol, version 1 or 2, at the start of every
// connection (ADR 0058): the header a TCP balancer (AWS NLB, HAProxy, a Kubernetes ingress in TCP
// mode) sends to say who its client is. The client it names becomes the connection's peer, so
// Request.RemoteAddr, Request.ClientAddr and the forwarding walk start from it.
//
// Only a trusted proxy may send it: a connection from a peer outside TrustedProxies.Prefixes (or,
// on a Unix socket, one when UnixSockets is false) is closed as it is accepted, before a byte is
// read. A trusted peer must send it: a connection that starts with anything else, or with a
// header that does not parse, is closed with nothing written. The wait for its first byte is the
// connection's idle wait; once it has begun, the whole header must arrive within
// Limits.HeadTimeout, or, on a TLS listener, where it comes before the handshake, within
// Limits.HandshakeTimeout from accept.
//
// The serving call panics when the app has no WithTrustedProxies, when the listener is a Unix
// socket and UnixSockets is false, and when it is a TCP listener and Prefixes is empty: in each
// case no peer could ever send the header.
func ProxyProtocol() ListenOption { return func(lc *listenConfig) { lc.proxy = true } }

// proxyFor checks the ListenOptions of a serving call fn and returns the engine's PROXY protocol
// settings for its listener, nil when it is off.
func (a *App) proxyFor(fn string, unix bool, opts []ListenOption) *engine.ProxyProtocol {
	var lc listenConfig
	for i, o := range opts {
		if o == nil {
			panic(&RegistrationError{msg: fmt.Sprintf("%s: ListenOption %d is nil\n\tregistered at %s", fn, i+1, callerWhere())})
		}
		o(&lc)
	}
	if !lc.proxy {
		return nil
	}
	fail := func(msg string) {
		panic(&RegistrationError{msg: fn + ": " + msg + "\n\tregistered at " + callerWhere()})
	}
	p := a.proxies
	switch {
	case p == nil:
		fail("ProxyProtocol is given but the app trusts no proxy, so no peer could send the header: name the balancers with WithTrustedProxies (ADR 0058)")
	case unix && !p.unix:
		fail("ProxyProtocol on a Unix socket, but TrustedProxies.UnixSockets is false, so no peer on it could send the header: set UnixSockets, which trusts whoever the socket's permission bits let connect (ADR 0058)")
	case !unix && len(p.prefixes) == 0:
		fail("ProxyProtocol on a TCP listener, but TrustedProxies has no Prefixes, so no peer on it could send the header: list the networks the balancer connects from (ADR 0058)")
	}
	return &engine.ProxyProtocol{Trusts: p.trusts, Unix: p.unix}
}

// isUnix reports whether ln listens on a Unix socket.
func isUnix(ln net.Listener) bool {
	_, ok := ln.Addr().(*net.UnixAddr)
	return ok
}
