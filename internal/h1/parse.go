package h1

import (
	"bytes"
	"errors"
)

// Limits are the head limits of ADR 0024.
type Limits struct {
	MaxHead   int // bytes of request line + headers + final CRLF; default 8192
	MaxFields int // header field count; default 100
}

// DefaultLimits returns the defaults of ADR 0024.
func DefaultLimits() Limits { return Limits{MaxHead: 8192, MaxFields: 100} }

// Header is one field line. Name keeps the case it was sent in.
type Header struct{ Name, Value []byte }

// Request is a parsed head. All slices borrow from the buffer given to
// ParseHead.
type Request struct {
	Method        []byte
	Target        []byte // request-target as sent
	Path          []byte // raw, not percent-decoded; "/" for an absolute-form target with no path
	RawQuery      []byte // after '?', without it; nil if none
	Host          []byte // absolute-form target's authority if present, else the Host header
	Minor         int    // 0 for HTTP/1.0, 1 for HTTP/1.1
	Headers       []Header
	ContentLength int64 // -1 if absent (also -1 when Chunked)
	Chunked       bool
	KeepAlive     bool // the connection may serve another request after this one
	Expect100     bool
	Asterisk      bool // OPTIONS *
}

// Error is a request the parser refuses. Status is the answer; Close says
// the connection must be closed after answering (ADR 0045).
type Error struct {
	Status int
	Close  bool
	Reason string
}

func (e *Error) Error() string { return e.Reason }

// ErrNeedMore means buf does not yet hold a complete head.
var ErrNeedMore = errors.New("h1: incomplete request head")

func refuse(status int, close bool, reason string) *Error {
	return &Error{Status: status, Close: close, Reason: reason}
}

var (
	errTooLarge     = refuse(431, true, "request head too large")
	errTooManyField = refuse(431, true, "too many header fields")
	errBareLF       = refuse(400, true, "line ended in a bare LF")
	errBareCR       = refuse(400, true, "bare CR in a line")
	errRequestLine  = refuse(400, true, "malformed request line")
	errMethod       = refuse(400, true, "method is not a token")
	errTarget       = refuse(400, true, "malformed request target")
	errVersion      = refuse(505, true, "HTTP version not supported")
	errBadVersion   = refuse(400, true, "malformed HTTP version")
	errFold         = refuse(400, true, "obsolete line folding")
	errNoColon      = refuse(400, true, "header line without a colon")
	errFieldName    = refuse(400, true, "header name is not a token")
	errFieldValue   = refuse(400, true, "control character in a header value")
	errBadCL        = refuse(400, true, "invalid Content-Length")
	errDupCL        = refuse(400, true, "more than one Content-Length")
	errTEandCL      = refuse(400, true, "both Transfer-Encoding and Content-Length")
	errTE10         = refuse(400, true, "Transfer-Encoding in an HTTP/1.0 request")
	errTEUnsup      = refuse(501, true, "unsupported Transfer-Encoding")
	errExpect       = refuse(417, true, "unsupported Expect")
	errBadHost      = refuse(400, true, "invalid Host")
	errNoHost       = refuse(400, true, "missing Host")
	errManyHost     = refuse(400, true, "more than one Host")
)

// tchar marks RFC 9110 token characters.
var tchar = func() (t [256]bool) {
	for c := 0; c < 256; c++ {
		b := byte(c)
		switch {
		case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			t[c] = true
		case bytes.IndexByte([]byte("!#$%&'*+-.^_`|~"), b) >= 0:
			t[c] = true
		}
	}
	return
}()

// hostChar marks what may appear in a Host value or an authority: reg-name,
// IP-literal and port, never a path, query, fragment or userinfo delimiter.
// A percent sign is left out: "a%41" and "aA" name one host, so h1 refuses
// both readings (ADR 0045).
var hostChar = func() (t [256]bool) {
	for c := 0; c < 256; c++ {
		b := byte(c)
		switch {
		case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			t[c] = true
		case bytes.IndexByte([]byte("-._~!$&'()*+,;=:[]"), b) >= 0:
			t[c] = true
		}
	}
	return
}()

var slash = []byte("/") // shared and read-only: the path of "http://host"

// ParseHead parses one request head at the start of buf and returns the
// number of bytes it spans, through the blank line. In steady state it does
// not allocate.
func ParseHead(buf []byte, req *Request, lim Limits) (n int, err error) {
	if lim.MaxHead <= 0 {
		lim.MaxHead = 8192
	}
	if lim.MaxFields <= 0 {
		lim.MaxFields = 100
	}
	*req = Request{Headers: req.Headers[:0], ContentLength: -1}

	line, pos, e := readLine(buf, 0, lim.MaxHead)
	if e != nil {
		return 0, e
	}
	if e := parseRequestLine(line, req); e != nil {
		return 0, e
	}

	var (
		nHost, nCL  int
		teSeen      bool
		teBad       bool
		teTokens    int
		hostVal     []byte
		connClose   bool
		connKeep    bool
		expectValue []byte
		haveExpect  bool
	)
	for {
		line, pos, e = readLine(buf, pos, lim.MaxHead)
		if e != nil {
			return 0, e
		}
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			return 0, errFold
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			return 0, errNoColon
		}
		if colon == 0 {
			return 0, errFieldName
		}
		name := line[:colon]
		for _, b := range name {
			if !tchar[b] {
				return 0, errFieldName
			}
		}
		val := trimOWS(line[colon+1:])
		for _, b := range val {
			if (b < 0x20 && b != '\t') || b == 0x7f {
				if b == '\r' {
					return 0, errBareCR
				}
				return 0, errFieldValue
			}
		}
		if len(req.Headers) >= lim.MaxFields {
			return 0, errTooManyField
		}
		req.Headers = append(req.Headers, Header{name, val})

		switch len(name) {
		case 4:
			if foldEq(name, "host") {
				nHost++
				hostVal = val
			}
		case 6:
			if foldEq(name, "expect") {
				haveExpect = true
				expectValue = val
			}
		case 10:
			if foldEq(name, "connection") {
				for rest := val; ; {
					var tok []byte
					tok, rest = nextToken(rest)
					if foldEq(tok, "close") {
						connClose = true
					} else if foldEq(tok, "keep-alive") {
						connKeep = true
					}
					if rest == nil {
						break
					}
				}
			}
		case 14:
			if foldEq(name, "content-length") {
				nCL++
				if nCL > 1 {
					return 0, errDupCL
				}
				cl, ok := parseCL(val)
				if !ok {
					return 0, errBadCL
				}
				req.ContentLength = cl
			}
		case 17:
			if foldEq(name, "transfer-encoding") {
				teSeen = true
				// Every list element counts, empty ones included: the only
				// acceptable value is exactly one "chunked" (ADR 0045).
				for rest := val; ; {
					var tok []byte
					tok, rest = nextToken(rest)
					teTokens++
					if !foldEq(tok, "chunked") {
						teBad = true
					}
					if rest == nil {
						break
					}
				}
			}
		}
	}
	n = pos

	if teSeen && nCL > 0 {
		return 0, errTEandCL
	}
	if teSeen {
		if req.Minor == 0 {
			return 0, errTE10
		}
		if teBad || teTokens != 1 {
			return 0, errTEUnsup
		}
		req.Chunked = true
		req.ContentLength = -1
	}

	if haveExpect && req.Minor == 1 {
		if foldEq(expectValue, "100-continue") {
			req.Expect100 = true
		} else {
			return 0, errExpect
		}
	}

	if req.Minor == 1 {
		req.KeepAlive = !connClose
	} else {
		req.KeepAlive = connKeep && !connClose
	}

	if e := parseTarget(req); e != nil {
		return 0, e
	}

	// Host checks come last, so a head refused for its Host still reports
	// its full length and framing. The connection closes all the same,
	// like every refusal (ADR 0045).
	if nHost > 0 {
		for _, b := range hostVal {
			if !hostChar[b] {
				return 0, errBadHost
			}
		}
	}
	if req.Host == nil {
		req.Host = hostVal
	}
	if nHost > 1 {
		return n, errManyHost
	}
	if nHost == 0 && req.Minor == 1 && !bytes.Equal(req.Method, connectMethod) {
		return n, errNoHost
	}
	return n, nil
}

var connectMethod = []byte("CONNECT")

// readLine returns the line starting at pos without its CRLF, and the
// position after it. Bytes past maxHead are never looked at, so a head that
// does not fit is decided as soon as maxHead bytes are in hand.
func readLine(buf []byte, pos, maxHead int) (line []byte, next int, err error) {
	end := min(len(buf), maxHead)
	i := -1
	if pos < end {
		i = bytes.IndexByte(buf[pos:end], '\n')
	}
	if i < 0 {
		if len(buf) >= maxHead {
			return nil, 0, errTooLarge
		}
		return nil, 0, ErrNeedMore
	}
	lf := pos + i
	if lf == pos || buf[lf-1] != '\r' {
		return nil, 0, errBareLF
	}
	return buf[pos : lf-1], lf + 1, nil
}

func parseRequestLine(line []byte, req *Request) *Error {
	sp1 := bytes.IndexByte(line, ' ')
	if sp1 < 0 {
		return errRequestLine
	}
	sp2 := bytes.LastIndexByte(line, ' ')
	if sp2 == sp1 {
		return errRequestLine
	}
	method, target, version := line[:sp1], line[sp1+1:sp2], line[sp2+1:]
	if len(method) == 0 {
		return errMethod
	}
	for _, b := range method {
		if !tchar[b] {
			return errMethod
		}
	}
	if len(target) == 0 {
		return errTarget
	}
	for _, b := range target {
		if b <= 0x20 || b == 0x7f {
			if b == '\r' {
				return errBareCR
			}
			return errTarget
		}
	}
	switch string(version) {
	case "HTTP/1.1":
		req.Minor = 1
	case "HTTP/1.0":
		req.Minor = 0
	default:
		if len(version) == 8 && string(version[:5]) == "HTTP/" && version[6] == '.' &&
			isDigit(version[5]) && isDigit(version[7]) {
			return errVersion
		}
		return errBadVersion
	}
	req.Method, req.Target = method, target
	return nil
}

// parseTarget fills Path, RawQuery, Host (absolute-form) and Asterisk.
func parseTarget(req *Request) *Error {
	t := req.Target
	if bytes.Equal(req.Method, connectMethod) {
		// authority-form: host:port, nothing else.
		if !validAuthority(t) {
			return errTarget
		}
		req.Host = t
		return nil
	}
	if bytes.IndexByte(t, '#') >= 0 {
		return errTarget
	}
	switch {
	case t[0] == '/':
		splitPathQuery(req, t)
	case len(t) == 1 && t[0] == '*':
		if string(req.Method) != "OPTIONS" {
			return errTarget
		}
		req.Asterisk = true
	default:
		// absolute-form: scheme "://" authority [path] [? query]
		i := bytes.Index(t, []byte("://"))
		if i <= 0 {
			return errTarget
		}
		scheme := t[:i]
		if !foldEq(scheme, "http") && !foldEq(scheme, "https") {
			return errTarget
		}
		rest := t[i+3:]
		end := bytes.IndexAny(rest, "/?")
		authority := rest
		if end >= 0 {
			authority = rest[:end]
		}
		if !validAuthority(authority) {
			return errTarget
		}
		req.Host = authority
		if end < 0 {
			req.Path = slash
			return nil
		}
		if rest[end] == '?' {
			req.Path = slash
			req.RawQuery = rest[end+1:]
			return nil
		}
		splitPathQuery(req, rest[end:])
	}
	return nil
}

func splitPathQuery(req *Request, pq []byte) {
	if q := bytes.IndexByte(pq, '?'); q >= 0 {
		req.Path, req.RawQuery = pq[:q], pq[q+1:]
		return
	}
	req.Path = pq
}

// validAuthority accepts a non-empty host[:port] with no userinfo (RFC 9110
// 4.2.1 says to treat one as an error).
func validAuthority(a []byte) bool {
	if len(a) == 0 {
		return false
	}
	for _, b := range a {
		if !hostChar[b] {
			return false
		}
	}
	return true
}

func parseCL(v []byte) (int64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	var n int64
	for _, b := range v {
		if !isDigit(b) {
			return 0, false
		}
		d := int64(b - '0')
		if n > (1<<63-1-d)/10 {
			return 0, false
		}
		n = n*10 + d
	}
	return n, true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func trimOWS(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// nextToken returns the first comma-separated element of v, trimmed, and the
// remainder; rest is nil after the last element.
func nextToken(v []byte) (tok, rest []byte) {
	if i := bytes.IndexByte(v, ','); i >= 0 {
		return trimOWS(v[:i]), v[i+1:]
	}
	return trimOWS(v), nil
}

// foldEq reports whether b equals the lower-case ASCII string s, ignoring the
// case of b, without allocating.
func foldEq(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != s[i] {
			return false
		}
	}
	return true
}
