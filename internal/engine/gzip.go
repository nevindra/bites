package engine

import (
	"bytes"
	"compress/gzip"
	"sync"
)

// gzipEncoder is the core's Encoder (ADR 0031): compress/gzip at BestSpeed, its writers pooled
// in the encoder so that neither a writer nor its output reaches user code (ADR 0008). Live
// writers number about GOMAXPROCS, because compression never blocks.
type gzipEncoder struct{ pool sync.Pool }

// gzWriter is one pooled writer and the sink its output is appended to.
type gzWriter struct {
	w   *gzip.Writer
	out gzSink
}

// gzSink is the io.Writer the compressor appends to: the caller's dst.
type gzSink struct{ b []byte }

func (s *gzSink) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }

// gzipEnc is the one gzip Encoder of the process: its pool is shared by every Server.
var gzipEnc = &gzipEncoder{pool: sync.Pool{New: func() any {
	g := &gzWriter{}
	g.w, _ = gzip.NewWriterLevel(&g.out, gzip.BestSpeed) // BestSpeed is a valid level
	return g
}}}

// Gzip is the core's gzip Encoder (ADR 0031).
func Gzip() Encoder { return gzipEnc }

func (*gzipEncoder) Coding() string { return "gzip" }

func (e *gzipEncoder) Encode(dst, src []byte) []byte {
	g := e.pool.Get().(*gzWriter)
	g.out.b = dst
	g.w.Reset(&g.out)
	g.w.Write(src)
	g.w.Close()
	out := g.out.b
	g.out.b = nil // the pooled writer keeps no reference to the caller's buffer
	e.pool.Put(g)
	return out
}

// headScan is what the engine needs to know about the headers the handler and the middleware put
// in the response head.
type headScan struct {
	encoded     bool // Content-Encoding is present
	noTransform bool // Cache-Control lists no-transform
	ranged      bool // Content-Range is present
	varyCovers  bool // Vary already lists Accept-Encoding or *
	etag        int  // offset in the head of a strong ETag's opening quote, or -1
}

func scanHead(head []byte) headScan {
	hs := headScan{etag: -1}
	i := bytes.Index(head, crlf)
	if i < 0 {
		return hs
	}
	for i += 2; i < len(head); {
		end := bytes.Index(head[i:], crlf)
		if end < 0 {
			break
		}
		line := head[i : i+end]
		colon := bytes.IndexByte(line, ':')
		if colon > 0 {
			name := line[:colon]
			v := line[colon+1:]
			off := i + colon + 1
			for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
				v, off = v[1:], off+1
			}
			switch {
			case bytes.EqualFold(name, []byte("etag")):
				if len(v) > 0 && v[0] == '"' {
					hs.etag = off
				}
			case bytes.EqualFold(name, []byte("content-encoding")):
				hs.encoded = true
			case bytes.EqualFold(name, []byte("content-range")):
				hs.ranged = true
			case bytes.EqualFold(name, []byte("cache-control")):
				if hasToken(v, "no-transform") {
					hs.noTransform = true
				}
			case bytes.EqualFold(name, []byte("vary")):
				if hasToken(v, "accept-encoding") || hasToken(v, "*") {
					hs.varyCovers = true
				}
			}
		}
		i += end + 2
	}
	return hs
}

var crlf = []byte("\r\n")

// hasToken reports whether the comma-separated list v has the token want, ignoring case, spaces
// and any parameter after a semicolon or an equals sign.
func hasToken(v []byte, want string) bool {
	for len(v) > 0 {
		var item []byte
		if c := bytes.IndexByte(v, ','); c >= 0 {
			item, v = v[:c], v[c+1:]
		} else {
			item, v = v, nil
		}
		if e := bytes.IndexAny(item, ";="); e >= 0 {
			item = item[:e]
		}
		if string(bytes.ToLower(bytes.TrimSpace(item))) == want {
			return true
		}
	}
	return false
}

// Incompressible reports a Content-Type whose bytes are compressed already: a static route
// looks for no precompressed twin of such a file (ADR 0065).
func Incompressible(ct string) bool { return incompressible(ct) }

// incompressible reports a Content-Type whose bytes are compressed already.
func incompressible(ct string) bool {
	if ct == "" {
		return false
	}
	b := lowerPrefix(ct)
	for _, p := range [...]string{"image/", "video/", "audio/", "font/"} {
		if len(b) >= len(p) && b[:len(p)] == p {
			return b != "image/svg+xml" && !hasPrefixStr(b, "image/svg+xml;")
		}
	}
	for _, p := range [...]string{
		"application/zip", "application/gzip", "application/x-gzip", "application/zstd", "application/x-bzip2",
		"application/x-xz", "application/x-7z-compressed", "application/vnd.rar", "application/x-rar-compressed",
		"application/pdf", "application/java-archive", "application/x-brotli", "application/octet-stream+zip",
	} {
		if hasPrefixStr(b, p) {
			return true
		}
	}
	return false
}

func hasPrefixStr(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// lowerPrefix lowercases the media type up to its parameters.
func lowerPrefix(ct string) string {
	n := len(ct)
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' {
			n = i + 1
			break
		}
	}
	for i := 0; i < n; i++ {
		if c := ct[i]; c >= 'A' && c <= 'Z' {
			return string(bytes.ToLower([]byte(ct[:n])))
		}
	}
	return ct[:n]
}

