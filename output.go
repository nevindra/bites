package bites

import (
	"strconv"
	"strings"
	"time"
)

// Header is the response headers a wrapper carries (ADR 0013). The framework adds Date,
// Content-Length and Connection itself, and ignores those names (and Transfer-Encoding) if a
// handler sets them, because two of them would make the response unparseable. A Content-Type
// set here replaces the JSON one.
type Header map[string][]string

// Set replaces name's values with one. The receiver must not be nil.
func (h Header) Set(name, value string) { h[name] = []string{value} }

// Add appends a value to name. The receiver must not be nil.
func (h Header) Add(name, value string) { h[name] = append(h[name], value) }

// SameSite is a cookie's SameSite attribute. The zero value is Lax (ADR 0043).
type SameSite uint8

const (
	// Lax is the default.
	Lax SameSite = iota
	// Strict withholds the cookie from every cross-site request.
	Strict
	// NoSameSite sends the cookie cross-site; browsers require Secure with it.
	NoSameSite
)

// Cookie is a cookie a response sets, with the safe defaults of ADR 0043: HttpOnly, Secure,
// SameSite=Lax and Path=/. Loosening any of them is written out, so a cookie that browser
// scripts can read says so in the code that sets it. Browsers treat localhost as a secure
// context, so Secure does not get in the way of development over plain HTTP.
type Cookie struct {
	// Name is the cookie name, which must be a valid token; otherwise the answer is a 500.
	Name string
	// Value is the cookie value; one with a character a cookie cannot carry is a 500.
	Value string
	// Path defaults to "/".
	Path string
	// Domain is the Domain attribute, left out when empty.
	Domain string
	// MaxAge is how long the browser keeps it; zero makes a session cookie and a negative value
	// deletes the cookie now.
	MaxAge time.Duration
	// Expires is the Expires attribute, left out when zero.
	Expires time.Time
	// SameSite defaults to Lax.
	SameSite SameSite
	// NotSecure drops the Secure attribute.
	NotSecure bool
	// NotHttpOnly drops HttpOnly, letting scripts in the page read the cookie.
	NotHttpOnly bool
}

// appendSetCookie renders c as a Set-Cookie value, or says why it cannot be one. A bad name or
// value is the handler's mistake and becomes a 500, never a header that splits the response.
func appendSetCookie(b []byte, c *Cookie) ([]byte, error) {
	if !validCookieName(c.Name) {
		return b, refusalf("the cookie name %q is not a valid token", c.Name)
	}
	if !validCookieValue(c.Value) {
		return b, refusalf("the value of cookie %s has a character a cookie cannot carry", c.Name)
	}
	if c.SameSite == NoSameSite && c.NotSecure {
		return b, refusalf("cookie %s is SameSite=None and not Secure, which browsers reject", c.Name)
	}
	b = append(b, c.Name...)
	b = append(b, '=')
	b = append(b, c.Value...)
	path := c.Path
	if path == "" {
		path = "/"
	}
	if strings.ContainsAny(path, ";\r\n") || strings.ContainsAny(c.Domain, ";\r\n") {
		return b, refusalf("cookie %s has a Path or Domain with a character a cookie attribute cannot carry", c.Name)
	}
	b = append(b, "; Path="...)
	b = append(b, path...)
	if c.Domain != "" {
		b = append(b, "; Domain="...)
		b = append(b, c.Domain...)
	}
	if !c.Expires.IsZero() {
		b = append(b, "; Expires="...)
		b = append(b, c.Expires.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")...)
	}
	switch {
	case c.MaxAge < 0:
		b = append(b, "; Max-Age=0"...)
	case c.MaxAge > 0:
		b = append(b, "; Max-Age="...)
		b = strconv.AppendInt(b, int64(c.MaxAge/time.Second), 10)
	}
	if !c.NotHttpOnly {
		b = append(b, "; HttpOnly"...)
	}
	if !c.NotSecure {
		b = append(b, "; Secure"...)
	}
	switch c.SameSite {
	case Strict:
		b = append(b, "; SameSite=Strict"...)
	case NoSameSite:
		b = append(b, "; SameSite=None"...)
	default:
		b = append(b, "; SameSite=Lax"...)
	}
	return b, nil
}

func validCookieName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenByte(s[i]) {
			return false
		}
	}
	return true
}

func isTokenByte(c byte) bool {
	if c <= ' ' || c >= 0x7f {
		return false
	}
	return !strings.ContainsRune("()<>@,;:\\\"/[]?={}", rune(c))
}

// validCookieValue allows the cookie-octet range of RFC 6265: no control characters, space,
// double quote, comma, semicolon or backslash.
func validCookieValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= ' ' || c >= 0x7f || c == '"' || c == ',' || c == ';' || c == '\\' {
			return false
		}
	}
	return true
}

// The status wrappers (ADR 0013). A handler that returns a plain value answers 200 with it as
// JSON; any other success status, and any response header or cookie, is one of these types.
// Each has Header and Cookies; the registration reads the type, never calls a method on it, so
// the status is known before the first request.

// OK is a 200 with a JSON body and, if given, headers and cookies.
type OK[T any] struct {
	// Body is the value written as JSON.
	Body T
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// Created is a 201 with a JSON body and a Location header when Location is not empty.
type Created[T any] struct {
	// Body is the value written as JSON.
	Body T
	// Location is the URL sent in the Location header, which is left out when empty.
	Location string
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// Accepted is a 202 with a JSON body.
type Accepted[T any] struct {
	// Body is the value written as JSON.
	Body T
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// NoContent is a 204 with no body.
type NoContent struct {
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// NotModified is a 304 with no body. Put the validators (ETag) the client should keep in Header.
type NotModified struct {
	// Header holds the response headers to send, such as the validators (ETag).
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// SeeOther is a 303 pointing at Location.
type SeeOther struct {
	// Location is the URL the client is sent to.
	Location string
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// Redirect points at Location with Status, which must be 301, 302, 307 or 308; any other value
// is a 500 and logged, because it can only be a mistake.
type Redirect struct {
	// Status is the redirect status: 301, 302, 307 or 308.
	Status int
	// Location is the URL the client is sent to.
	Location string
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// Text is a 200 with a plain text body, Content-Type text/plain; charset=utf-8 (ADR 0043). The
// body is written as it is, not as a JSON string.
type Text struct {
	// Body is the text, written as it is.
	Body string
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// Bytes is a 200 with a body of any content type (ADR 0043): ContentType and the bytes. The
// content type is a value of the handler's, so it cannot be checked at Registration; it is
// checked per response, and an empty one, or one that is not type/subtype or holds a control
// character, is a 500 and logged, because it can only be a mistake. The bytes are copied into
// the response before the handler's return value is gone.
type Bytes struct {
	// ContentType is the media type of Body, checked per response; an empty one is a 500.
	ContentType string
	// Body is the bytes sent, copied before the handler returns.
	Body []byte
	// Attachment, when it is not empty, makes the answer a download saved under this name: the
	// answer carries Content-Disposition: attachment, with the name written by the standard
	// library's mime.FormatMediaType, which quotes it and encodes a name outside ASCII, a line
	// break or invalid UTF-8 as filename* (RFC 2231), so no name can split the response. Only the
	// part after the last "/" or "\" is used, so a name with a directory in it is saved under
	// its last element; a name with nothing usable left ("dir/", ".", "..") sends
	// "attachment" alone and the browser names the file. A Content-Disposition in Header
	// replaces it (an "inline" one, for instance). Empty sends no Content-Disposition, and the
	// browser shows the body when it can.
	Attachment string
	// Header holds the response headers to send.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
}

// validContentType accepts type/subtype with optional parameters, and nothing that could split
// the header: no control characters, and a token on each side of the slash.
func validContentType(s string) bool {
	slash := strings.IndexByte(s, '/')
	if slash <= 0 || slash == len(s)-1 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c == 0x7f {
			return false
		}
		if i < slash && !isTokenByte(s[i]) {
			return false
		}
	}
	return true
}
