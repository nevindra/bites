package bites

import (
	"bytes"
	"strconv"
	"time"

	"github.com/nevindra/bites/internal/h1"
)

// Conditional and range requests for a File (ADR 0064), read straight from the parsed head: the
// values are views of the engine's read buffer, used while the answer is built and never kept
// (ADR 0008).

// What the preconditions of RFC 9110 section 13.2.2 decided.
const (
	condProceed     = iota // answer as if there were none
	condNotModified        // 304
	condFailed             // 412
)

// validators are what the file offers to compare against: a strong or weak entity tag (quotes
// included, W/ for a weak one) and a modification time in whole seconds, either may be absent.
type validators struct {
	etag    []byte
	mod     time.Time
	hasMod  bool
	isGetOr bool // the method is GET or HEAD
}

// evaluate applies the four preconditions in the order RFC 9110 section 13.2.2 gives them:
// If-Match, else If-Unmodified-Since; then If-None-Match, else If-Modified-Since (GET and HEAD
// only). A header that does not parse is treated as absent, as section 13.1 allows.
func (v *validators) evaluate(hs []h1.Header) int {
	if present, match := v.matchHeader(hs, "if-match", true); present {
		if !match {
			return condFailed
		}
	} else if d, ok := firstHeader(hs, "if-unmodified-since"); ok && v.hasMod {
		if t, ok := parseHTTPDate(d); ok && v.mod.After(t) {
			return condFailed
		}
	}
	if present, match := v.matchHeader(hs, "if-none-match", false); present {
		if match {
			if v.isGetOr {
				return condNotModified
			}
			return condFailed
		}
	} else if d, ok := firstHeader(hs, "if-modified-since"); ok && v.isGetOr && v.hasMod {
		if t, ok := parseHTTPDate(d); ok && !v.mod.After(t) {
			return condNotModified
		}
	}
	return condProceed
}

// matchHeader reports whether the request has the header called name, on one line or several,
// and whether any of its entity-tags names the file's tag: "*" matches any file that exists (and
// this one does), strong comparison for If-Match and weak comparison for If-None-Match (RFC 9110
// section 8.8.3.2). A file with no tag matches only "*".
func (v *validators) matchHeader(hs []h1.Header, name string, strong bool) (present, match bool) {
	for i := range hs {
		if !bytes.EqualFold(hs[i].Name, []byte(name)) {
			continue
		}
		present = true
		list := hs[i].Value
		if t := bytes.TrimSpace(list); len(t) == 1 && t[0] == '*' {
			return true, true
		}
		if len(v.etag) == 0 {
			continue
		}
		for rest := list; ; {
			tag, next, ok := nextETag(rest)
			if !ok {
				break
			}
			if etagEqual(tag, v.etag, strong) {
				return true, true
			}
			rest = next
		}
	}
	return present, false
}

// nextETag reads one entity-tag (W/"x" or "x") from a comma-separated list, skipping spaces and
// commas before it. An opaque tag may hold commas, so the list is read by its quotes.
func nextETag(b []byte) (tag, rest []byte, ok bool) {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == ',') {
		i++
	}
	start := i
	if i+1 < len(b) && b[i] == 'W' && b[i+1] == '/' {
		i += 2
	}
	if i >= len(b) || b[i] != '"' {
		return nil, nil, false
	}
	end := bytes.IndexByte(b[i+1:], '"')
	if end < 0 {
		return nil, nil, false
	}
	end += i + 2
	return b[start:end], b[end:], true
}

// etagEqual compares two entity-tags. The strong comparison needs both strong and the same
// opaque tag; the weak one ignores W/.
func etagEqual(a, b []byte, strong bool) bool {
	aw, bw := bytes.HasPrefix(a, []byte("W/")), bytes.HasPrefix(b, []byte("W/"))
	if strong && (aw || bw) {
		return false
	}
	if aw {
		a = a[2:]
	}
	if bw {
		b = b[2:]
	}
	return bytes.Equal(a, b)
}

// validETag reports whether s is an entity-tag a response may carry: W/ or nothing, then a
// quoted string of visible characters without a quote.
func validETag(s string) bool {
	if len(s) >= 2 && s[0] == 'W' && s[1] == '/' {
		s = s[2:]
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for i := 1; i < len(s)-1; i++ {
		if c := s[i]; c == '"' || c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

// A byte range a request asked for, resolved against the file's size.
const (
	rangeNone          = iota // no Range, one that is not for bytes, one that does not parse, or several: the whole file
	rangeOne                  // one satisfiable range: a 206
	rangeUnsatisfiable        // one range that starts past the end: a 416
)

// parseRange reads a Range header for a file of size bytes (RFC 9110 section 14.2). Only one
// range is answered; a list of more than one is ignored and the whole file sent, which the RFC
// allows (ADR 0064 says why). It returns the offset and length of the part to send.
func parseRange(v []byte, size int64) (kind int, off, n int64) {
	v = bytes.TrimSpace(v)
	if len(v) < 6 || !bytes.EqualFold(v[:6], []byte("bytes=")) {
		return rangeNone, 0, 0
	}
	spec := bytes.TrimSpace(v[6:])
	if bytes.IndexByte(spec, ',') >= 0 || size <= 0 {
		return rangeNone, 0, 0
	}
	dash := bytes.IndexByte(spec, '-')
	if dash < 0 {
		return rangeNone, 0, 0
	}
	first, last := bytes.TrimSpace(spec[:dash]), bytes.TrimSpace(spec[dash+1:])
	if len(first) == 0 { // a suffix: the last n bytes
		k, ok := parseDigits(last)
		if !ok {
			return rangeNone, 0, 0
		}
		if k == 0 {
			return rangeUnsatisfiable, 0, 0
		}
		k = min(k, size)
		return rangeOne, size - k, k
	}
	a, ok := parseDigits(first)
	if !ok {
		return rangeNone, 0, 0
	}
	end := size - 1
	if len(last) > 0 {
		b, ok := parseDigits(last)
		if !ok || b < a {
			return rangeNone, 0, 0
		}
		end = min(b, size-1)
	}
	if a >= size {
		return rangeUnsatisfiable, 0, 0
	}
	return rangeOne, a, end - a + 1
}

// parseDigits reads a non-negative decimal; a number past what an int64 holds saturates, which
// is past the end of any file.
func parseDigits(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (1<<62)/10 {
			n = 1 << 62
			continue
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// ifRangeHolds reports whether the If-Range value (RFC 9110 section 13.1.5) names the file as it
// is now: an entity-tag compared strongly, or a date equal to its modification time.
func (v *validators) ifRangeHolds(val []byte) bool {
	val = bytes.TrimSpace(val)
	if len(val) > 0 && (val[0] == '"' || bytes.HasPrefix(val, []byte("W/"))) {
		return len(v.etag) > 0 && etagEqual(val, v.etag, true)
	}
	t, ok := parseHTTPDate(val)
	return ok && v.hasMod && t.Equal(v.mod)
}

// firstHeader is the value of the first line of the header called name.
func firstHeader(hs []h1.Header, name string) ([]byte, bool) {
	for i := range hs {
		if bytes.EqualFold(hs[i].Name, []byte(name)) {
			return hs[i].Value, true
		}
	}
	return nil, false
}

var months = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// parseHTTPDate reads an HTTP-date (RFC 9110 section 5.6.7): the IMF-fixdate every sender uses
// is read by hand without allocating, and the two obsolete forms a recipient must accept go
// through time.Parse.
func parseHTTPDate(b []byte) (time.Time, bool) {
	b = bytes.TrimSpace(b)
	// "Sun, 06 Nov 1994 08:49:37 GMT"
	if len(b) == 29 && b[3] == ',' && b[4] == ' ' && b[7] == ' ' && b[11] == ' ' && b[16] == ' ' &&
		b[19] == ':' && b[22] == ':' && b[25] == ' ' && string(b[26:]) == "GMT" {
		day, ok1 := twoDigits(b[5:7])
		year, ok2 := parseDigits(b[12:16])
		hh, ok3 := twoDigits(b[17:19])
		mm, ok4 := twoDigits(b[20:22])
		ss, ok5 := twoDigits(b[23:25])
		mon := -1
		for i, m := range months {
			if string(b[8:11]) == m {
				mon = i
				break
			}
		}
		if !(ok1 && ok2 && ok3 && ok4 && ok5) || mon < 0 || day < 1 || day > 31 || hh > 23 || mm > 59 || ss > 60 {
			return time.Time{}, false
		}
		return time.Date(int(year), time.Month(mon+1), day, hh, mm, ss, 0, time.UTC), true
	}
	s := string(b)
	for _, layout := range [...]string{"Monday, 02-Jan-06 15:04:05 GMT", "Mon Jan _2 15:04:05 2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func twoDigits(b []byte) (int, bool) {
	if len(b) != 2 || b[0] < '0' || b[0] > '9' || b[1] < '0' || b[1] > '9' {
		return 0, false
	}
	return int(b[0]-'0')*10 + int(b[1]-'0'), true
}

// appendETag appends the tag the engine makes for a file that has a modification time (ADR 0064):
// "<nanoseconds since 1970 in hex>-<size in hex>", strong.
func appendETag(b []byte, mod time.Time, size int64) []byte {
	b = append(b, '"')
	b = strconv.AppendInt(b, mod.UnixNano(), 16)
	b = append(b, '-')
	b = strconv.AppendInt(b, size, 16)
	return append(b, '"')
}

// appendHTTPDate appends t as an IMF-fixdate.
func appendHTTPDate(b []byte, t time.Time) []byte {
	return t.UTC().AppendFormat(b, "Mon, 02 Jan 2006 15:04:05 GMT")
}
