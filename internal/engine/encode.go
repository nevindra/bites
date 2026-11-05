package engine

import (
	"bytes"
	"runtime/debug"

	"github.com/nevindra/bites/internal/h1"
)

// Response compression (ADR 0031, ADR 0061). The app registers the encoders it offers, in its
// order of preference; the engine picks one for each buffered answer from the request's
// Accept-Encoding, compresses the finished body into a pooled buffer and writes that. gzip is the
// core's; zstd and brotli are modules that implement Encoder.

// Encoder is one content coding the engine can compress an answer with.
//
// Encode appends the compressed form of src to dst and returns it. It is called from many
// connections at once, and neither src nor dst may be kept past its return: both are the
// engine's pooled buffers (ADR 0008), the same contract io.Writer states for its argument.
type Encoder interface {
	// Coding is the token of Accept-Encoding and Content-Encoding: "gzip", "br", "zstd". It is
	// read once, when the server is made.
	Coding() string
	Encode(dst, src []byte) []byte
}

// CompressConfig is the response compression of ADR 0031. No encoder is off, the default.
type CompressConfig struct {
	Encoders []Encoder // in the server's order of preference, used when the client weighs two alike
	MinSize  int       // a body shorter than this is sent as it is; the framework passes at least CompressFloor
}

// CompressFloor and CompressDefaultMinSize are the 1 KiB of ADR 0031: below it a response
// already fits one TCP segment, so compressing saves bytes and no packets.
const (
	CompressFloor          = 1024
	CompressDefaultMinSize = 1024
)

// coder is a registered encoder with its Content-Encoding line made once.
type coder struct {
	enc    Encoder
	coding string
	line   []byte // "Content-Encoding: br\r\n"
}

// encBuf is one pooled output buffer, taken for the length of one write.
type encBuf struct{ b []byte }

func (s *Server) putEncBuf(eb *encBuf) {
	if cap(eb.b) > maxPooled {
		eb.b = nil
	}
	eb.b = eb.b[:0]
	s.encBufs.Put(eb)
}

var varyLine = []byte("Vary: Accept-Encoding\r\n")

// negotiate is the engine's compression step for a buffered answer (ADR 0031). It adds Vary,
// decides whether to compress and with which encoder and, if it does, returns the compressed body
// in a pooled buffer the caller gives back with putEncBuf after the write; the head comes back
// with Content-Encoding added and a strong ETag weakened. Otherwise it returns the head with only
// Vary and a nil buffer.
func (c *conn) negotiate(head []byte) ([]byte, *encBuf) {
	s := c.srv
	r := &c.sc.resp
	req := &c.sc.req
	hs := scanHead(head)
	if !hs.varyCovers {
		head = append(head, varyLine...)
	}
	// Anything the head says that makes the body unfit moves nothing: Vary is still right.
	if r.status < 200 || r.status == 204 || r.status == 304 || r.status == 206 || r.stream != nil ||
		len(r.body) < s.o.Compress.MinSize || bytes.Equal(req.Method, headMethod) ||
		hs.encoded || hs.noTransform || hs.ranged || incompressible(r.ctype) {
		return head, nil
	}
	i := pick(req.Headers, s.coders)
	if i < 0 {
		return head, nil
	}
	cd := &s.coders[i]
	eb := s.encBufs.Get().(*encBuf)
	out, ok := c.encode(cd, eb.b[:0], r.body)
	eb.b = out
	if !ok || len(out) >= len(r.body) {
		s.putEncBuf(eb)
		return head, nil
	}
	if hs.etag >= 0 {
		// A strong tag names the exact bytes; the compressed ones are another representation.
		head = append(head, 0, 0)
		copy(head[hs.etag+2:], head[hs.etag:])
		head[hs.etag], head[hs.etag+1] = 'W', '/'
	}
	head = append(head, cd.line...)
	return head, eb
}

// encode runs an encoder with the engine's recovery around it (ADR 0028): an encoder is a
// module's code, and a panic in it sends the body uncompressed instead of ending the process.
func (c *conn) encode(cd *coder, dst, src []byte) (out []byte, ok bool) {
	defer func() {
		if v := recover(); v != nil {
			out, ok = dst[:0], false
			c.srv.o.Logger.Error("panic in an Encoder, the answer is sent uncompressed", "coding", cd.coding, "panic", v, "stack", string(debug.Stack()))
		}
	}()
	return cd.enc.Encode(dst, src), true
}

// pick applies RFC 9110 section 12.5.3 to the Accept-Encoding lines of a request and returns the
// index of the coder to use, or -1 to send the body as it is. A coding's weight is the one it is
// listed with, or that of "*" when it is not listed; a weight of 0 refuses it. Of the codings
// with a weight above 0 the heaviest wins, and on a tie the one first in the server's order. A
// coding listed twice with different weights is refused if either is 0, and weighs its first
// weight otherwise; several Accept-Encoding lines are one list. "x-gzip" is gzip.
func pick(hs []h1.Header, cs []coder) int {
	best, bestW := -1, 0
	for i := range cs {
		if w := weight(hs, cs[i].coding); w > bestW {
			best, bestW = i, w
		}
	}
	return best
}

// weight is the weight Accept-Encoding gives coding, in thousandths (0 to 1000), with "*"
// standing for a coding it does not list; 0 when neither is listed or a weight does not parse.
func weight(hs []h1.Header, coding string) int {
	w, star := -1, -1 // -1: not listed
	for _, h := range hs {
		if !bytes.EqualFold(h.Name, []byte("accept-encoding")) {
			continue
		}
		v := h.Value
		for len(v) > 0 {
			var item []byte
			if c := bytes.IndexByte(v, ','); c >= 0 {
				item, v = v[:c], v[c+1:]
			} else {
				item, v = v, nil
			}
			name, params := item, []byte(nil)
			if sc := bytes.IndexByte(item, ';'); sc >= 0 {
				name, params = item[:sc], item[sc+1:]
			}
			name = bytes.TrimSpace(name)
			q := 1000
			if params != nil {
				q = qValue(params)
			}
			switch {
			case foldEq(name, coding) || coding == "gzip" && foldEq(name, "x-gzip"):
				if q == 0 || w == -1 { // listed twice with different weights: a refusal wins
					w = q
				}
			case len(name) == 1 && name[0] == '*':
				if q == 0 || star == -1 {
					star = q
				}
			}
		}
	}
	if w >= 0 {
		return w
	}
	return max(star, 0)
}

// qValue is the weight in thousandths from the parameters after the semicolon (q=0.5; other=x):
// 1000 when no q is given, 0 for a weight that does not parse (RFC 9110 section 12.4.2: 0 or 1
// followed by up to three decimals, and nothing above 1).
func qValue(params []byte) int {
	for len(params) > 0 {
		var p []byte
		if sc := bytes.IndexByte(params, ';'); sc >= 0 {
			p, params = params[:sc], params[sc+1:]
		} else {
			p, params = params, nil
		}
		p = bytes.TrimSpace(p)
		if len(p) < 2 || (p[0] != 'q' && p[0] != 'Q') {
			continue
		}
		p = bytes.TrimSpace(p[1:])
		if len(p) == 0 || p[0] != '=' {
			return 0
		}
		w := bytes.TrimSpace(p[1:])
		if len(w) == 0 || len(w) > 5 || (w[0] != '0' && w[0] != '1') {
			return 0
		}
		if len(w) == 1 {
			return int(w[0]-'0') * 1000
		}
		if w[1] != '.' {
			return 0
		}
		n, scale := int(w[0]-'0')*1000, 100
		for _, d := range w[2:] {
			if d < '0' || d > '9' {
				return 0
			}
			n += int(d-'0') * scale
			scale /= 10
		}
		if n > 1000 {
			return 0
		}
		return n
	}
	return 1000
}

// foldEq reports whether b is s, ignoring ASCII case; s is lowercase.
func foldEq(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range len(b) {
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
