// Package proxyproto reads the header of the PROXY protocol, version 1 (text) and version 2
// (binary), that a load balancer sends at the start of a connection to say who its client is
// (ADR 0058). It parses bytes the engine has already read and keeps none of them; reading them,
// the deadline and the decision whether the peer may send a header at all are the engine's.
//
// The specification is HAProxy's "The PROXY protocol, Versions 1 & 2". What this parser accepts
// and refuses beyond it is written next to each rule below and in ADR 0058.
package proxyproto

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// MaxHeader is the longest header accepted, in bytes: a version 2 header whose TLVs run past it
// is refused. A balancer's TLVs (an AWS endpoint id, a TLS summary) take tens to a few hundred
// bytes, and 4 KiB is the engine's read buffer, so a header always fits the buffer it is read into.
const MaxHeader = 4096

// maxV1 is the longest version 1 line the specification allows, CRLF included.
const maxV1 = 107

var (
	// ErrNeedMore means the bytes so far are the beginning of a header that may still be valid.
	ErrNeedMore = errors.New("proxyproto: need more bytes")
	// ErrNotProxy means the connection does not start with a PROXY header at all: the peer
	// speaks something else (plain HTTP, TLS) to a listener that expects the header.
	ErrNotProxy = errors.New("proxyproto: the connection does not start with a PROXY header")
	// ErrMalformed is a header that starts as one and breaks a rule of the specification, or of
	// ADR 0058.
	ErrMalformed = errors.New("proxyproto: malformed PROXY header")
)

// Header is what a PROXY header says.
type Header struct {
	// Len is the length of the header in bytes; the connection's own bytes start after it.
	Len int
	// Source is the client's address and port, valid only when Named is true.
	Source netip.AddrPort
	// Named is true when the header names a TCP client over IPv4 or IPv6. It is false for a
	// version 2 LOCAL header (the balancer's own connection, a health check), a version 1
	// UNKNOWN, and a version 2 PROXY header whose family is unspecified or a Unix socket: the
	// specification says the receiver then uses the connection's real addresses.
	Named bool
	// Version is 1 or 2.
	Version int
}

// sigV2 is the 12-byte signature of a version 2 header.
var sigV2 = [12]byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

const sigV1 = "PROXY "

// Parse reads a header from the start of b. It returns ErrNeedMore while b is a valid beginning
// of one, ErrNotProxy when b cannot begin one, and ErrMalformed when it began one and broke a
// rule. It never reads past the header.
func Parse(b []byte) (Header, error) {
	if len(b) == 0 {
		return Header{}, ErrNeedMore
	}
	switch b[0] {
	case 'P':
		n := min(len(b), len(sigV1))
		if string(b[:n]) != sigV1[:n] {
			return Header{}, ErrNotProxy
		}
		if n < len(sigV1) {
			return Header{}, ErrNeedMore
		}
		return parseV1(b)
	case sigV2[0]:
		n := min(len(b), len(sigV2))
		if string(b[:n]) != string(sigV2[:n]) {
			return Header{}, ErrNotProxy
		}
		if n < len(sigV2) {
			return Header{}, ErrNeedMore
		}
		return parseV2(b)
	}
	return Header{}, ErrNotProxy
}

// parseV1 reads "PROXY TCP4 192.0.2.1 198.51.100.1 56324 443\r\n", or "PROXY UNKNOWN...\r\n".
// The fields are separated by exactly one space, as the specification writes them; the line is
// at most 107 bytes with its CRLF, and a bare LF is refused.
func parseV1(b []byte) (Header, error) {
	end := -1
	for i := len(sigV1); i < len(b) && i < maxV1; i++ {
		if b[i] == '\n' {
			end = i
			break
		}
		if b[i] == '\r' {
			if i+1 < len(b) && b[i+1] != '\n' {
				return Header{}, ErrMalformed
			}
			continue
		}
		if b[i] < 0x20 || b[i] > 0x7e {
			return Header{}, ErrMalformed // the line is ASCII text
		}
	}
	if end < 0 {
		if len(b) >= maxV1 {
			return Header{}, ErrMalformed
		}
		return Header{}, ErrNeedMore
	}
	if b[end-1] != '\r' {
		return Header{}, ErrMalformed
	}
	line := b[len(sigV1) : end-1]
	h := Header{Len: end + 1, Version: 1}
	proto, rest, _ := cut(line)
	switch string(proto) {
	case "UNKNOWN":
		return h, nil // whatever follows is ignored, as the specification says
	case "TCP4", "TCP6":
	default:
		return Header{}, ErrMalformed
	}
	src, rest, ok1 := cut(rest)
	dst, rest, ok2 := cut(rest)
	sport, dport, ok3 := cut(rest)
	if !ok1 || !ok2 || !ok3 {
		return Header{}, ErrMalformed
	}
	v4 := proto[3] == '4'
	sa, ok := addrV1(src, v4)
	if !ok {
		return Header{}, ErrMalformed
	}
	if _, ok := addrV1(dst, v4); !ok {
		return Header{}, ErrMalformed
	}
	sp, ok := portV1(sport)
	if !ok {
		return Header{}, ErrMalformed
	}
	if _, ok := portV1(dport); !ok {
		return Header{}, ErrMalformed
	}
	h.Source, h.Named = netip.AddrPortFrom(sa, sp), true
	return h, nil
}

// cut splits b at its first space.
func cut(b []byte) (before, after []byte, found bool) {
	for i, c := range b {
		if c == ' ' {
			return b[:i], b[i+1:], true
		}
	}
	return b, nil, false
}

// addrV1 parses an address of the family the line named: IPv4 for TCP4, IPv6 for TCP6, with no
// zone. It runs once a connection, never on a request.
func addrV1(b []byte, v4 bool) (netip.Addr, bool) {
	if len(b) == 0 || len(b) > 45 {
		return netip.Addr{}, false
	}
	a, err := netip.ParseAddr(string(b))
	if err != nil || a.Zone() != "" || a.Is4() != v4 {
		return netip.Addr{}, false
	}
	return a, true
}

// portV1 parses a decimal port from 0 to 65535 with no sign and no leading zero.
func portV1(b []byte) (uint16, bool) {
	if len(b) == 0 || len(b) > 5 || (b[0] == '0' && len(b) > 1) {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

// parseV2 reads the binary header: the signature, the version and command, the family and
// transport, a length, and that many bytes of addresses and TLVs. The TLVs are skipped unread.
func parseV2(b []byte) (Header, error) {
	if len(b) < 16 {
		return Header{}, ErrNeedMore
	}
	verCmd, fam := b[12], b[13]
	if verCmd>>4 != 2 {
		return Header{}, ErrMalformed
	}
	n := 16 + int(binary.BigEndian.Uint16(b[14:16]))
	if n > MaxHeader {
		return Header{}, ErrMalformed
	}
	cmd := verCmd & 0x0f
	if cmd > 1 {
		return Header{}, ErrMalformed
	}
	if fam>>4 > 3 || fam&0x0f > 2 {
		return Header{}, ErrMalformed
	}
	// The address block's length is checked before the bytes are waited for, so a header that
	// could never be valid is refused at its 16th byte.
	need := 0
	switch fam {
	case 0x11:
		need = 12
	case 0x21:
		need = 36
	case 0x31:
		need = 216
	}
	if cmd == 1 {
		if fam&0x0f == 2 {
			return Header{}, ErrMalformed // a datagram source cannot be this stream's client
		}
		if n-16 < need {
			return Header{}, ErrMalformed
		}
	}
	if len(b) < n {
		return Header{}, ErrNeedMore
	}
	h := Header{Len: n, Version: 2}
	if cmd == 0 {
		return h, nil // LOCAL: the balancer's own connection; the family is ignored
	}
	a := b[16:]
	switch fam {
	case 0x11:
		h.Source = netip.AddrPortFrom(netip.AddrFrom4([4]byte(a[0:4])), binary.BigEndian.Uint16(a[8:10]))
		h.Named = true
	case 0x21:
		h.Source = netip.AddrPortFrom(netip.AddrFrom16([16]byte(a[0:16])), binary.BigEndian.Uint16(a[32:34]))
		h.Named = true
	}
	// UNSPEC (0x00) and a Unix socket (0x31): the real addresses of the connection stand.
	return h, nil
}
