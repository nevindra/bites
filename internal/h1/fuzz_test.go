package h1

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// FuzzParseHead feeds the same bytes to ParseHead and to net/http's
// ReadRequest. A disagreement is a bug here or a deliberate difference; the
// deliberate ones are listed in allowed, each naming its ADR 0045 row.
func FuzzParseHead(f *testing.F) {
	seeds := []string{
		"GET / HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET /a/b?x=1&y=%20 HTTP/1.1\r\nHost: example.com\r\nUser-Agent: curl/8\r\nAccept: */*\r\n\r\n",
		"POST /p HTTP/1.1\r\nHost: a\r\nContent-Length: 5\r\n\r\nhello",
		"POST /p HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n",
		"GET / HTTP/1.0\r\n\r\n",
		"GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n",
		"GET http://h.example/p?q=1 HTTP/1.1\r\nHost: other\r\n\r\n",
		"GET http://h.example HTTP/1.1\r\nHost: h.example\r\n\r\n",
		"OPTIONS * HTTP/1.1\r\nHost: a\r\n\r\n",
		"CONNECT a.example:443 HTTP/1.1\r\nHost: a.example:443\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nExpect: 100-continue\r\nContent-Length: 3\r\n\r\n",
		// smuggling vectors
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 6\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\nG",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nContent-Length: 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked, identity\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: identity, chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding : chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding:\tchunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: \tchunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: xchunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: x\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\n Transfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding:\r\n chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nContent-Length: 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4, 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: +4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 99999999999999999999\r\n\r\n",
		"GET / HTTP/1.1\nHost: a\n\n",
		"GET / HTTP/1.1\r\nHost: a\nX: b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX: b\r\n c\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX: b\rc\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX: b\x00c\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX\x00: b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX : b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nX(: b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n",
		"GET / HTTP/1.1\r\n\r\n",
		"GET / HTTP/2.0\r\nHost: a\r\n\r\n",
		"GET /\x00 HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET /a b HTTP/1.1\r\nHost: a\r\n\r\n",
		"G(T / HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET /%zz HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET /#frag HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a b\r\n\r\n",
		// more smuggling vectors: casing, parameters, empty and odd values
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: Chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\ntransfer-encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked;q=1\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: \r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nContent-Length: 0\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: gzip, chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.0\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: -4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 0x4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 04\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length:\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\n 5\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\ncontent-length: 4\r\nCONTENT-LENGTH: 4\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\nTransfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\rTransfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\n\tTransfer-Encoding: chunked\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\x0b\r\n\r\n",
		"POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: \x00chunked\r\n\r\n",
		"GET  / HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET\t/ HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET / HTTP/1.1 \r\nHost: a\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nHost: a\r\n\r\n",
		"GET / HTTP/1.1\r\nhost: a\r\nHOST: b\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\n\r\nGET /2 HTTP/1.1\r\nHost: a\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\nExpect: 200-ok\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a\r\n\r",
		"GET / HTTP/1.1\r\nHost: a\r\n\n",
		"GET / HTTP/1.1\r\nHost: a\n\r\n",
		"\r\nGET / HTTP/1.1\r\nHost: a\r\n\r\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var req Request
		n, ours := ParseHead(data, &req, DefaultLimits())
		theirs, terr := http.ReadRequest(bufio.NewReader(bytes.NewReader(data)))

		if ours == nil && terr == nil {
			if n < 0 || n > len(data) {
				t.Fatalf("n=%d for %d bytes", n, len(data))
			}
			compare(t, data, &req, theirs)
			return
		}
		if ours != nil && terr != nil {
			return
		}
		if why := allowed(data, &req, ours, theirs, terr); why != "" {
			return
		}
		t.Fatalf("disagreement on %q\n  h1: n=%d err=%v\n  net/http: err=%v", data, n, ours, terr)
	})
}

// allowed names the places where ADR 0045 deliberately differs from
// net/http's ReadRequest. It returns the reason, or "" for a real
// disagreement. ReadRequest is not the full server (the server adds the Host
// check), so some entries are the server's own rules that we also enforce.
func allowed(data []byte, req *Request, ours error, theirs *http.Request, terr error) string {
	var he *Error
	errors.As(ours, &he)
	if ours != nil && theirs != nil {
		// We refuse, net/http accepts.
		switch {
		case he == nil:
			// ErrNeedMore: net/http read to EOF where we would read more. A
			// complete head that net/http accepted without a final blank line
			// is impossible, so this is a real finding unless the input ends
			// mid-head, which net/http reports as an error, not acceptance.
			return ""
		case he.Status == 505:
			return "version: net/http's ReadRequest accepts any HTTP/x.y; the server answers 505 (ADR 0045 does not list versions, ADR 0024 neighbours)"
		case strings.Contains(he.Reason, "more than one Content-Length"):
			return "ADR 0045 row 2: duplicate Content-Length refused even when equal"
		case strings.Contains(he.Reason, "Transfer-Encoding"):
			return "ADR 0045 rows 1 and 3: Transfer-Encoding with Content-Length, other codings, or in HTTP/1.0"
		case strings.Contains(he.Reason, "bare LF"), strings.Contains(he.Reason, "bare CR"):
			return "ADR 0045 row 4: bare LF and bare CR"
		case strings.Contains(he.Reason, "folding"):
			return "ADR 0045 row 4: obs-fold"
		case he == errNoHost, he == errManyHost:
			return "ADR 0045 row 5: Host rules (ReadRequest leaves them to the server)"
		case strings.Contains(he.Reason, "Expect"):
			return "unknown Expect: net/http's server answers 417, ReadRequest accepts"
		case he == errTarget:
			return targetDifference(theirs)
		case he == errBadCL:
			return "Content-Length must be digits only (net/http also accepts a value continued by obs-fold or ended by bare LF, ADR 0045 row 4)"
		case he == errFieldName:
			return "ADR 0045 row 4: space before the colon and names that are not tokens (net/http trims trailing space of a name)"
		case he == errFieldValue:
			return "ADR 0045 row 4 (NUL), extended to every control character but HTAB"
		case he == errBadHost:
			return "ADR 0045 row 5: a Host value that is not an authority (ReadRequest does not validate it)"
		}
		return ""
	}
	if ours == nil && terr != nil {
		// We accept, net/http refuses.
		msg := terr.Error()
		switch {
		case strings.Contains(msg, "invalid URL escape"), strings.Contains(msg, "invalid URL"), strings.Contains(msg, "parse "):
			return "percent-decoding and URL validity belong to the router layer, not the head parser"
		}
		return ""
	}
	return ""
}

// targetDifference explains a target we refuse and net/http parsed: a
// fragment, userinfo, a scheme other than http(s), an authority-less
// absolute-form, or a non-origin target on a method that is not OPTIONS *.
func targetDifference(theirs *http.Request) string {
	uri := theirs.RequestURI
	switch {
	case strings.Contains(uri, "#"):
		return "ADR 0045 spirit: a fragment in a request-target is two readings (proxies strip it); refused"
	case theirs.URL.User != nil:
		return "RFC 9110 4.2.4: userinfo in an absolute-form target is refused"
	case theirs.URL.Scheme != "" && theirs.URL.Scheme != "http" && theirs.URL.Scheme != "https":
		return "absolute-form with a scheme other than http(s) is refused"
	case theirs.URL.Scheme != "" && theirs.URL.Host == "":
		return "absolute-form without an authority is refused"
	case theirs.URL.Scheme != "" && !validAuthority([]byte(theirs.URL.Host)):
		return "absolute-form whose authority has characters outside reg-name, IP-literal and port is refused"
	case uri == "*" && theirs.Method != "OPTIONS":
		return "asterisk-form only for OPTIONS"
	case theirs.Method == "CONNECT":
		return "CONNECT takes authority-form only"
	}
	return ""
}

// compare checks the fields both parsers accepted.
func compare(t *testing.T, data []byte, req *Request, theirs *http.Request) {
	t.Helper()
	fail := func(what string, ours, theirs any) {
		t.Fatalf("%s differs on %q: h1 %v, net/http %v", what, data, ours, theirs)
	}
	if string(req.Method) != theirs.Method {
		fail("method", string(req.Method), theirs.Method)
	}
	if req.Asterisk {
		if theirs.URL.Path != "*" && theirs.RequestURI != "*" {
			fail("asterisk", true, theirs.RequestURI)
		}
	} else if !isConnect(req) {
		wantPath := theirs.URL.Path
		if wantPath == "" && theirs.URL.Host != "" {
			wantPath = "/"
		}
		got, err := url.PathUnescape(string(req.Path))
		if err != nil {
			// net/http accepted a path our decoder refuses: its own rules differ, skip.
			return
		}
		if got != wantPath {
			fail("path", got, wantPath)
		}
		if string(req.RawQuery) != theirs.URL.RawQuery {
			fail("query", string(req.RawQuery), theirs.URL.RawQuery)
		}
	}
	if string(req.Host) != theirs.Host && !isConnect(req) {
		// ADR 0045 row 6: the host is kept as sent, and a percent sign in it
		// is refused, so nothing is left to decode here.
		fail("host", string(req.Host), theirs.Host)
	}
	theirsChunked := len(theirs.TransferEncoding) > 0 && theirs.TransferEncoding[0] == "chunked"
	if req.Chunked != theirsChunked {
		fail("chunked", req.Chunked, theirsChunked)
	}
	wantCL := theirs.ContentLength
	if !req.Chunked && req.ContentLength == -1 && wantCL == 0 {
		wantCL = -1 // net/http reports 0 where no length was sent
	}
	if req.ContentLength != wantCL {
		fail("content-length", req.ContentLength, wantCL)
	}
	if req.Minor != theirs.ProtoMinor {
		fail("minor", req.Minor, theirs.ProtoMinor)
	}
}

func isConnect(req *Request) bool { return string(req.Method) == "CONNECT" }

var _ = fmt.Sprint

// decodeChunked runs the decoder over body fed in pieces of step bytes
// through a small out buffer. ok is true when the body ended cleanly.
func decodeChunked(body []byte, step int) (got []byte, used int, ok bool, err error) {
	var d ChunkedDecoder
	d.Reset(1 << 20)
	out := make([]byte, 7)
	for used < len(body) {
		end := min(used+step, len(body))
		piece := body[used:end]
		for len(piece) > 0 {
			c, n, done, err := d.Decode(piece, out)
			got = append(got, out[:n]...)
			used += c
			if err != nil {
				return got, used, false, err
			}
			if done {
				return got, used, true, nil
			}
			if c == 0 && n == 0 {
				return got, used, false, errors.New("no progress")
			}
			piece = piece[c:]
		}
	}
	return got, used, false, nil
}

// FuzzChunked feeds the same chunked body to the decoder and to net/http's
// reader, and to the decoder twice more in different piece sizes. A body both
// finish must decode to the same bytes; the decoder must not depend on how the
// bytes arrive. The deliberate differences are in allowedChunked.
func FuzzChunked(f *testing.F) {
	for _, s := range []string{
		"5\r\nhello\r\n0\r\n\r\n",
		"5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n",
		"0\r\n\r\n",
		"5;ext=1\r\nhello\r\n0\r\n\r\n",
		"5 \r\nhello\r\n0\r\n\r\n",
		"5\t;x\r\nhello\r\n0\r\n\r\n",
		"0\r\nTrailer: v\r\n\r\n",
		"0\r\nno colon\r\n\r\n",
		"0\r\n folded\r\n\r\n",
		"5\nhello\n0\n\n",
		"5\r\nhello\n0\r\n\r\n",
		"5\r\nhello\r\n0\r\n\n",
		"5\r\nhello0\r\n\r\n",
		"5\r\nhelloXX0\r\n\r\n",
		"+5\r\nhello\r\n0\r\n\r\n",
		"-5\r\nhello\r\n0\r\n\r\n",
		"0x5\r\nhello\r\n0\r\n\r\n",
		"05\r\nhello\r\n0\r\n\r\n",
		"00000000000000000000005\r\nhello\r\n0\r\n\r\n",
		"ffffffffffffffff\r\nx",
		"7fffffffffffffff\r\nx",
		"8000000000000000\r\nx",
		"\r\n5\r\nhello\r\n0\r\n\r\n",
		"5\r\nhel\rlo\r\n0\r\n\r\n",
		"5;\r\nhello\r\n0\r\n\r\n",
		"5;a=\"b\r\nhello\r\n0\r\n\r\n",
		"5\x00\r\nhello\r\n0\r\n\r\n",
		"5\r\nhello\r\n0\r\nX: a\x00b\r\n\r\n",
		"5\r\nhello\r\n0\r\n\r\nGET / HTTP/1.1\r\n",
		"5\r\nhello",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		whole, usedW, okW, errW := decodeChunked(body, len(body)+1)
		one, usedO, okO, errO := decodeChunked(body, 1)
		if (errW == nil) != (errO == nil) || okW != okO || !bytes.Equal(whole, one) || (errW == nil && usedW != usedO) {
			t.Fatalf("piece size changes the result on %q: whole (%q %d %v %v), bytewise (%q %d %v %v)", body, whole, usedW, okW, errW, one, usedO, okO, errO)
		}
		if usedW > len(body) {
			t.Fatalf("used %d of %d bytes", usedW, len(body))
		}

		head := "POST / HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n"
		r := bufio.NewReader(bytes.NewReader(append([]byte(head), body...)))
		req, err := http.ReadRequest(r)
		if err != nil {
			t.Fatalf("net/http refused the fixed head: %v", err)
		}
		theirs, terr := io.ReadAll(req.Body)

		switch {
		case errW == nil && okW && terr == nil:
			if !bytes.Equal(whole, theirs) {
				t.Fatalf("body differs on %q: h1 %q, net/http %q", body, whole, theirs)
			}
		case errW == nil && !okW && terr != nil:
			// both ran out of input
		case errW == nil && !okW && terr == nil:
			// net/http accepted a body that ends without the last blank line
			// (it treats the end of the trailers at EOF as the end).
			if !bytes.HasPrefix(theirs, whole) {
				t.Fatalf("partial body differs on %q: h1 %q, net/http %q", body, whole, theirs)
			}
		case errW != nil && terr != nil:
		default:
			if why := allowedChunked(body, errW, theirs, terr); why == "" {
				t.Fatalf("disagreement on %q\n  h1: ok=%v err=%v body=%q\n  net/http: err=%v body=%q", body, okW, errW, whole, terr, theirs)
			}
		}
	})
}

var bwsBeforeExt = regexp.MustCompile(`(?m)^[0-9a-fA-F]+[ \t]+;`)

// allowedChunked names where the decoder deliberately differs from net/http's
// chunked reader, each against its ADR 0045 row. It returns "" for a real
// disagreement.
func allowedChunked(body []byte, ours error, theirs []byte, terr error) string {
	if ours != nil && terr == nil {
		var he *Error
		errors.As(ours, &he)
		switch {
		case bytes.Contains(body, []byte("\n")) && !bytes.Contains(body, []byte("\r\n")):
			return "ADR 0045 row 4: bare LF as a line end"
		case he != nil && he == errChunkSyntax:
			return "ADR 0045 row 4: bare LF, bare CR, NUL or obs-fold in the framing"
		case he != nil && he == errChunkSize:
			return "ADR 0045: a chunk size is bare hex digits, at most 16 of them"
		}
		return ""
	}
	if ours == nil && terr != nil {
		if strings.Contains(terr.Error(), "invalid byte in chunk length") && bwsBeforeExt.Match(body) {
			return "ADR 0045 chunk row: extensions accepted, with the BWS before ';' that RFC 9112 allows and net/http refuses"
		}
		if strings.Contains(terr.Error(), "header line too long") {
			return "ADR 0045 chunk row: extensions have a budget of their own (the head limit by default), not net/http's fixed line cap"
		}
		if strings.Contains(terr.Error(), "suspiciously long trailer") {
			return "ADR 0045 chunk row: trailers have a budget of their own (the head limit by default), not net/http's fixed 10 KiB"
		}
		if strings.Contains(terr.Error(), "MIME header") {
			return "ADR 0045 chunk row: trailers are discarded, not parsed, so a malformed trailer is accepted"
		}
	}
	return ""
}
