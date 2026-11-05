// Package mpart is the framework's own multipart/form-data parser (ADR 0044). It reads a body
// part by part over a window it is given, never holds the body whole, and refuses what
// mime/multipart would allow: a part header over 8 KiB or with more than 16 fields, more parts
// than the caller allows, a boundary over 70 bytes. It imports the standard library and h1 (for
// the error type) only, never net/http.
package mpart

import (
	"bytes"
	"io"

	"github.com/nevindra/bites/internal/h1"
)

const (
	// MaxBoundary is the longest boundary RFC 2046 allows.
	MaxBoundary = 70
	// MaxHeaderBytes is the most bytes one part's header block may hold.
	MaxHeaderBytes = 8 << 10
	// MaxHeaderFields is the most header fields one part may have.
	MaxHeaderFields = 16
	// MinWindow is the smallest window the parser works in: a header block, its terminator and
	// the longest delimiter must fit with room to spare.
	MinWindow = MaxHeaderBytes + 2*MaxBoundary + 64
)

// The refusals. All are *h1.Error, so the framework answers them with errors.As, and all close
// the connection: after one the framing of the rest of the body is not trusted (ADR 0045).
var (
	ErrNotMultipart   = &h1.Error{Status: 415, Reason: "the body must be sent as multipart/form-data"}
	ErrBadBoundary    = &h1.Error{Status: 400, Reason: "the multipart boundary is missing, longer than 70 bytes, or has a character a boundary cannot have"}
	ErrTooManyParts   = &h1.Error{Status: 413, Close: true, Reason: "the upload has more parts than this route allows"}
	ErrHeaderTooLarge = &h1.Error{Status: 431, Close: true, Reason: "a part's header block is over 8192 bytes"}
	ErrTooManyFields  = &h1.Error{Status: 431, Close: true, Reason: "a part has more than 16 header fields"}
	ErrMalformed      = &h1.Error{Status: 400, Close: true, Reason: "the multipart body is malformed"}
	ErrNoName         = &h1.Error{Status: 400, Close: true, Reason: "a part has no Content-Disposition of type form-data with a name"}
	ErrTruncated      = &h1.Error{Status: 400, Close: true, Reason: "the multipart body ended inside a part"}
	errNoProgress     = &h1.Error{Status: 400, Close: true, Reason: "the multipart body could not be read"}
)

// Source supplies the body's bytes. Fill writes up to len(dst) bytes into dst and returns how
// many; it returns io.EOF, with no bytes, only when the body has ended, and any other error is
// kept and returned by the parser as it is.
type Source interface {
	Fill(dst []byte) (int, error)
}

// Head is the header of a part. The slices are views of the parser's window: valid until the
// next call of Next or Read, so the caller copies what it keeps.
type Head struct {
	Name        []byte
	FileName    []byte
	HasFileName bool
	ContentType []byte
}

type state uint8

const (
	sPreamble state = iota // looking for the first delimiter
	sAfter                 // just past a delimiter: "--" ends the body, CRLF starts a part
	sBody                  // inside a part's body
	sEnd                   // the closing delimiter was read
)

// Parser reads one multipart body. It is not safe for concurrent use; the caller serializes.
type Parser struct {
	src      Source
	buf      []byte
	r, w     int
	eof      bool
	err      error // the source's error, kept
	delim    [4 + MaxBoundary]byte
	dlen     int
	st       state
	first    bool // at the start of the body: a delimiter may lack its CRLF
	parts    int
	maxParts int
}

// Reset readies p for a body with boundary, read through win from src, allowing maxParts parts.
func (p *Parser) Reset(boundary []byte, win []byte, src Source, maxParts int) error {
	if !validBoundary(boundary) || len(win) < MinWindow {
		return ErrBadBoundary
	}
	*p = Parser{src: src, buf: win, first: true, maxParts: maxParts}
	p.dlen = copy(p.delim[:], "\r\n--") + copy(p.delim[4:], boundary)
	return nil
}

// Release drops the window and the source. The parser is unusable until Reset.
func (p *Parser) Release() { p.buf, p.src, p.r, p.w = nil, nil, 0, 0 }

// Buffered is how many bytes the window holds that are not yet read: with the bytes the source
// still owes, an upper bound of what remains of the body.
func (p *Parser) Buffered() int { return p.w - p.r }

// Ended reports whether the closing delimiter was read: the body holds nothing more of parts.
func (p *Parser) Ended() bool { return p.st == sEnd }

// Drain reads the source to its end and discards it, using the window.
func (p *Parser) Drain() error {
	p.r, p.w = 0, 0
	for {
		if p.eof {
			return nil
		}
		if p.err != nil {
			return p.err
		}
		if err := p.fill(); err != nil && err != io.EOF {
			return err
		}
		p.r, p.w = 0, 0
	}
}

func validBoundary(b []byte) bool {
	if len(b) == 0 || len(b) > MaxBoundary {
		return false
	}
	for i, c := range b {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case bytes.IndexByte([]byte("'()+_,-./:=?"), c) >= 0:
		case c == ' ' && i < len(b)-1:
		default:
			return false
		}
	}
	return true
}

// fill reads more into the window, moving what is unread to the front first. It returns io.EOF
// when the source has ended and nothing was added, and the source's error when it failed.
func (p *Parser) fill() error {
	if p.r > 0 {
		p.w = copy(p.buf, p.buf[p.r:p.w])
		p.r = 0
	}
	if p.eof {
		return io.EOF
	}
	if p.err != nil {
		return p.err
	}
	for range 8 {
		if p.w == len(p.buf) {
			return errNoProgress
		}
		n, err := p.src.Fill(p.buf[p.w:])
		p.w += n
		if err != nil {
			if err == io.EOF {
				p.eof = true
				if n == 0 {
					return io.EOF
				}
				return nil
			}
			p.err = err
			if n == 0 {
				return err
			}
			return nil
		}
		if n > 0 {
			return nil
		}
	}
	return errNoProgress
}

// need makes sure the window holds at least n unread bytes, or says why it cannot.
func (p *Parser) need(n int) error {
	for p.w-p.r < n {
		if err := p.fill(); err != nil {
			if err == io.EOF {
				return ErrTruncated
			}
			return err
		}
	}
	return nil
}

// Next moves to the next part, discarding what is left of the current one, and returns its
// header. It returns io.EOF after the closing delimiter.
func (p *Parser) Next() (Head, error) {
	var h Head
	for {
		switch p.st {
		case sEnd:
			return h, io.EOF
		case sBody:
			if err := p.skipBody(); err != nil {
				return h, err
			}
		case sPreamble:
			if err := p.findFirst(); err != nil {
				return h, err
			}
		case sAfter:
			if err := p.need(2); err != nil {
				return h, err
			}
			two := p.buf[p.r : p.r+2]
			switch {
			case two[0] == '-' && two[1] == '-':
				p.r += 2
				p.st = sEnd
				return h, io.EOF
			case two[0] == '\r' && two[1] == '\n':
				p.r += 2
				if p.parts >= p.maxParts {
					return h, ErrTooManyParts
				}
				p.parts++
				return p.header()
			}
			return h, ErrMalformed
		}
	}
}

// findFirst skips the preamble up to and including the first delimiter.
func (p *Parser) findFirst() error {
	dash := p.delim[2:p.dlen]
	for {
		avail := p.buf[p.r:p.w]
		if p.first && len(avail) >= len(dash) {
			p.first = false
			if bytes.HasPrefix(avail, dash) {
				p.r += len(dash)
				p.st = sAfter
				return nil
			}
		}
		if i := bytes.Index(avail, p.delim[:p.dlen]); i >= 0 {
			p.first = false
			p.r += i + p.dlen
			p.st = sAfter
			return nil
		}
		if !p.first && len(avail) > p.dlen-1 {
			p.r += len(avail) - (p.dlen - 1)
		}
		if err := p.fill(); err != nil {
			if err == io.EOF {
				return ErrTruncated
			}
			return err
		}
	}
}

// header parses the header block that starts at p.r, after the delimiter line.
func (p *Parser) header() (Head, error) {
	var h Head
	// An empty header block is a bare CRLF.
	if err := p.need(2); err != nil {
		return h, err
	}
	if p.buf[p.r] == '\r' && p.buf[p.r+1] == '\n' {
		p.r += 2
		p.st = sBody
		return h, ErrNoName
	}
	for {
		avail := p.buf[p.r:p.w]
		if i := bytes.Index(avail, []byte("\r\n\r\n")); i >= 0 {
			if i > MaxHeaderBytes {
				return h, ErrHeaderTooLarge
			}
			block := p.buf[p.r : p.r+i]
			p.r += i + 4
			p.st = sBody
			return parseHeader(block)
		}
		if len(avail) > MaxHeaderBytes {
			return h, ErrHeaderTooLarge
		}
		if err := p.fill(); err != nil {
			if err == io.EOF {
				return h, ErrTruncated
			}
			return h, err
		}
	}
}

func parseHeader(block []byte) (Head, error) {
	var h Head
	var haveDisp, haveType bool
	fields := 0
	for len(block) > 0 {
		line := block
		if i := bytes.Index(block, []byte("\r\n")); i >= 0 {
			line, block = block[:i], block[i+2:]
		} else {
			block = nil
		}
		fields++
		if fields > MaxHeaderFields {
			return h, ErrTooManyFields
		}
		c := bytes.IndexByte(line, ':')
		if c <= 0 || line[0] == ' ' || line[0] == '\t' {
			return h, ErrMalformed
		}
		name, val := line[:c], bytes.TrimSpace(line[c+1:])
		switch {
		case equalFold(name, "content-disposition"):
			if haveDisp {
				return h, ErrMalformed
			}
			haveDisp = true
			if !parseDisposition(val, &h) {
				return h, ErrNoName
			}
		case equalFold(name, "content-type"):
			if haveType {
				return h, ErrMalformed
			}
			haveType = true
			h.ContentType = val
		}
	}
	if !haveDisp || h.Name == nil {
		return h, ErrNoName
	}
	return h, nil
}

// parseDisposition reads `form-data; name="x"; filename="y"`. Quoted values are unescaped in
// place (the header block is ours and parsed once). It reports false when the type is not
// form-data or there is no name.
func parseDisposition(v []byte, h *Head) bool {
	typ := v
	rest := []byte(nil)
	if i := bytes.IndexByte(v, ';'); i >= 0 {
		typ, rest = v[:i], v[i+1:]
	}
	if !equalFold(bytes.TrimSpace(typ), "form-data") {
		return false
	}
	for {
		rest = bytes.TrimLeft(rest, " \t;")
		if len(rest) == 0 {
			break
		}
		eq := bytes.IndexByte(rest, '=')
		if eq <= 0 {
			return false
		}
		key := bytes.TrimSpace(rest[:eq])
		val, tail, ok := paramValue(rest[eq+1:])
		if !ok {
			return false
		}
		rest = tail
		switch {
		case equalFold(key, "name"):
			h.Name = val
			if h.Name == nil {
				h.Name = []byte{}
			}
		case equalFold(key, "filename"):
			h.FileName, h.HasFileName = val, true
		}
	}
	return h.Name != nil
}

// paramValue reads a token or a quoted string from the start of s and returns the value, what
// follows it, and whether it was well formed. A quoted string is unescaped in place.
func paramValue(s []byte) (val, rest []byte, ok bool) {
	s = bytes.TrimLeft(s, " \t")
	if len(s) > 0 && s[0] == '"' {
		out := 0
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '"':
				return s[1 : 1+out], s[i+1:], true
			case '\\':
				if i+1 < len(s) {
					i++
					c = s[i]
				}
				s[1+out] = c
				out++
			default:
				s[1+out] = c
				out++
			}
		}
		return nil, nil, false
	}
	end := bytes.IndexByte(s, ';')
	if end < 0 {
		end = len(s)
	}
	return bytes.TrimSpace(s[:end]), s[end:], true
}

func equalFold(b []byte, lower string) bool {
	if len(b) != len(lower) {
		return false
	}
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// skipBody discards the rest of the current part.
func (p *Parser) skipBody() error {
	var scratch [512]byte
	for p.st == sBody {
		if _, err := p.Read(scratch[:]); err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}

// Read reads the current part's body into b. It returns io.EOF at the part's end, and
// ErrTruncated when the body ends before the next delimiter.
func (p *Parser) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	d := p.delim[:p.dlen]
	for {
		if p.st != sBody {
			return 0, io.EOF
		}
		avail := p.buf[p.r:p.w]
		i := bytes.Index(avail, d)
		switch {
		case i > 0:
			n := copy(b, avail[:i])
			p.r += n
			return n, nil
		case i == 0:
			if p.w-p.r < p.dlen+2 {
				if err := p.need(p.dlen + 2); err != nil {
					return 0, err
				}
				continue
			}
			f := p.buf[p.r+p.dlen : p.r+p.dlen+2]
			if (f[0] == '-' && f[1] == '-') || (f[0] == '\r' && f[1] == '\n') {
				p.r += p.dlen
				p.st = sAfter
				return 0, io.EOF
			}
			// "\r\n--boundary" followed by more boundary characters is data.
			b[0] = '\r'
			p.r++
			return 1, nil
		}
		if safe := len(avail) - (p.dlen - 1); safe > 0 {
			n := copy(b, avail[:safe])
			p.r += n
			return n, nil
		}
		if err := p.fill(); err != nil {
			if err == io.EOF {
				return 0, ErrTruncated
			}
			return 0, err
		}
	}
}

// ParseContentType reads a Content-Type value for a multipart/form-data body and returns its
// boundary, a view of v. It returns ErrNotMultipart when the type is another one, and
// ErrBadBoundary when the boundary is missing or cannot be one.
func ParseContentType(v []byte) ([]byte, error) {
	typ, rest := v, []byte(nil)
	if i := bytes.IndexByte(v, ';'); i >= 0 {
		typ, rest = v[:i], v[i+1:]
	}
	if !equalFold(bytes.TrimSpace(typ), "multipart/form-data") {
		return nil, ErrNotMultipart
	}
	var boundary []byte
	for {
		rest = bytes.TrimLeft(rest, " \t;")
		if len(rest) == 0 {
			break
		}
		eq := bytes.IndexByte(rest, '=')
		if eq <= 0 {
			return nil, ErrBadBoundary
		}
		key := bytes.TrimSpace(rest[:eq])
		val, tail, ok := paramValue(rest[eq+1:])
		if !ok {
			return nil, ErrBadBoundary
		}
		rest = tail
		if equalFold(key, "boundary") {
			if boundary != nil {
				return nil, ErrBadBoundary
			}
			boundary = val
		}
	}
	if !validBoundary(boundary) {
		return nil, ErrBadBoundary
	}
	return boundary, nil
}
