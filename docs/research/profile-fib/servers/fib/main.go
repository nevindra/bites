// The fib server of the harness (github.com/lesismal/fib v1.6.0, the version of
// docs/research/engines). fib is an event-loop engine, not a typed framework: it has no routing
// by pattern, no typed decoding and no schema, so this server routes on the path by hand and
// reads and writes JSON by hand. It is compared as an engine (see bench/README.md).
//
// Mode: fib.DefaultConfig() with the synchronous handler, the one docs/research/engines
// measured as "fib": the handler runs on fib's worker pool, and GET /block blocks it with
// time.Sleep. This is NOT the "fib-async" mode (c.Retain, the sleep on another goroutine).
//
// Validation, as far as a hand-written parser reasonably goes, matches nilo-go: the path id must
// be an integer, page a required integer, expand an optional boolean, the body an object with
// name, email and age all present and no other member. A mistake is a plain 400 (no problem+json).
package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"strconv"
	"strings"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/nevindra/bites/research/profile-fib/common"
	"github.com/nevindra/bites/research/profile-fib/prof"
)

func main() {
	flag.Parse()
	common.StartStats()
	prof.Start()
	app := fibhttp.HandlerFunc(handle)
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Addr: *common.Addr, Handler: fibhttp.NewHandler(app)})
	if err != nil {
		panic(err)
	}
	defer engine.Close()
	panic(engine.Run())
}

func handle(c *fibhttp.Context) {
	path := c.Request.URL.Path
	switch {
	case path == "/block":
		if c.Request.Method != http.MethodGet {
			bad(c, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		time.Sleep(common.BlockDelay)
		_ = c.Respond(http.StatusOK, "application/json", []byte(`{"ok":true}`))
	case strings.HasPrefix(path, "/users/"):
		id, err := strconv.ParseInt(path[len("/users/"):], 10, 64)
		if err != nil {
			bad(c, http.StatusBadRequest, "path.id: expected an integer")
			return
		}
		switch c.Request.Method {
		case http.MethodGet:
			b := appendUser(make([]byte, 0, 96), id)
			b = append(b, '}')
			_ = c.Respond(http.StatusOK, "application/json", b)
		case http.MethodPost:
			post(c, id)
		default:
			bad(c, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		bad(c, http.StatusNotFound, "not found")
	}
}

func post(c *fibhttp.Context, id int64) {
	page, err := strconv.Atoi(c.Query("page"))
	if err != nil {
		bad(c, http.StatusBadRequest, "query.page: expected an integer")
		return
	}
	if e := c.Query("expand"); e != "" {
		if _, err := strconv.ParseBool(e); err != nil {
			bad(c, http.StatusBadRequest, "query.expand: expected a boolean")
			return
		}
	}
	u, msg := parseUser(c.Body())
	if msg != "" {
		bad(c, http.StatusBadRequest, "body: "+msg)
		return
	}
	b := make([]byte, 0, 128)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, id, 10)
	b = append(b, `,"name":`...)
	b = appendString(b, u.Name)
	b = append(b, `,"email":`...)
	b = appendString(b, u.Email)
	b = append(b, `,"age":`...)
	b = strconv.AppendInt(b, int64(u.Age), 10)
	b = append(b, `,"page":`...)
	b = strconv.AppendInt(b, int64(page), 10)
	b = append(b, '}')
	_ = c.Respond(http.StatusOK, "application/json", b)
}

// appendUser writes the answer of GET /users/{id} without its closing brace.
func appendUser(b []byte, id int64) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, id, 10)
	return append(b, `,"name":"ada","email":"ada@example.dev","age":36`...)
}

func bad(c *fibhttp.Context, status int, msg string) {
	_ = c.Respond(status, "text/plain; charset=utf-8", []byte(msg+"\n"))
}

// parseUser reads {"name":"..","email":"..","age":N} by hand: every member required, no unknown
// member, no duplicate. It returns a message on a mistake. Strings are copied out of the body,
// which fib owns only until the handler returns, so nothing escapes the request.
func parseUser(b []byte) (u common.NewUser, msg string) {
	p := scanner{b: b}
	if !p.eat('{') {
		return u, "expected an object"
	}
	var seen int
	for first := true; ; first = false {
		if p.eat('}') {
			break
		}
		if !first && !p.eat(',') {
			return u, "expected , or }"
		}
		key, ok := p.str()
		if !ok || !p.eat(':') {
			return u, "expected a member name and :"
		}
		var bit int
		switch key {
		case "name", "email":
			v, ok := p.str()
			if !ok {
				return u, key + ": expected a string"
			}
			if key == "name" {
				u.Name, bit = v, 1
			} else {
				u.Email, bit = v, 2
			}
		case "age":
			n, ok := p.int()
			if !ok {
				return u, "age: expected an integer"
			}
			u.Age, bit = n, 4
		default:
			return u, "unknown member " + strconv.Quote(key)
		}
		if seen&bit != 0 {
			return u, "duplicate member " + key
		}
		seen |= bit
	}
	p.space()
	if p.i != len(p.b) {
		return u, "unexpected data after the object"
	}
	if seen != 7 {
		return u, "a member is missing"
	}
	return u, ""
}

type scanner struct {
	b []byte
	i int
}

func (p *scanner) space() {
	for p.i < len(p.b) && (p.b[p.i] == ' ' || p.b[p.i] == '\t' || p.b[p.i] == '\n' || p.b[p.i] == '\r') {
		p.i++
	}
}

func (p *scanner) eat(c byte) bool {
	p.space()
	if p.i < len(p.b) && p.b[p.i] == c {
		p.i++
		return true
	}
	return false
}

// str reads a JSON string; one without an escape is a plain copy, one with an escape goes
// through encoding/json to decode it.
func (p *scanner) str() (string, bool) {
	p.space()
	if p.i >= len(p.b) || p.b[p.i] != '"' {
		return "", false
	}
	start, esc := p.i, false
	for p.i++; p.i < len(p.b); p.i++ {
		switch c := p.b[p.i]; {
		case c == '"':
			p.i++
			if !esc {
				return string(p.b[start+1 : p.i-1]), true
			}
			var s string
			return s, json.Unmarshal(p.b[start:p.i], &s) == nil
		case c == '\\':
			esc = true
			p.i++
		case c < 0x20:
			return "", false
		}
	}
	return "", false
}

func (p *scanner) int() (int, bool) {
	p.space()
	start := p.i
	if p.i < len(p.b) && p.b[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
		p.i++
	}
	n, err := strconv.Atoi(string(p.b[start:p.i]))
	return n, err == nil
}

const hex = "0123456789abcdef"

// appendString writes s as a JSON string.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}
