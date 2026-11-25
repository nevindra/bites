package bitetest

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/nevindra/bites/internal/testcodec"
)

// Event is one thing a Server-Sent Events answer sent: an event, or a comment line. It is
// returned as it was written, so an event with only a Retry, which a browser would not
// dispatch, is returned too.
type Event struct {
	// ID is the event's id field; empty when it had none.
	ID string
	// Name is the event's event field; empty when it had none (a browser calls it "message").
	Name string
	// Data is the event's data fields joined by "\n".
	Data string
	// Retry is the event's retry field; zero when it had none.
	Retry time.Duration
	// Comment is the text of a comment line (": text"), and is set only on a comment, which has
	// no other field. A handler that writes a comment as soon as it is ready (for example after
	// subscribing to what it streams) lets a test wait for that before it publishes.
	Comment string
}

// EventStream is a Server-Sent Events answer (bites.SSE or bites.Events) being read as it
// arrives. The answer's head was read by OpenEvents; its events are read with Next.
type EventStream struct {
	// Status is the answer's status, always 200.
	Status int
	// Header holds the answer's header fields, with names lower-cased.
	Header map[string][]string

	srv   *Server
	c     io.Closer
	in    chan eventOrEnd
	ended error
}

type eventOrEnd struct {
	ev  Event
	err error
}

// OpenEvents sends r (a GET when Method is empty) on a new connection and reads the head of the
// answer, which must be a 200 with the content type text/event-stream; any other answer fails
// the test with its status and body (use Do to test an answer that is not a stream). The events
// are then read with Next as the app sends them, and the connection is closed when the test ends,
// or earlier by Close.
//
//	events := srv.OpenEvents(bitetest.Request{Target: "/orders/7/events"})
//	ev := events.Next()
func (s *Server) OpenEvents(r Request) *EventStream {
	s.tb.Helper()
	if r.Method == "" {
		r.Method = "GET"
	}
	req := s.requestBytes(r)
	c := s.Dial(remoteOf(r))
	if _, err := c.Write(req); err != nil {
		c.Close()
		s.tb.Fatalf("bitetest: writing the request: %v", err)
	}
	br := bufio.NewReader(c)
	type result struct {
		r   Response
		err error
	}
	ch := make(chan result, 1)
	go func() {
		h, err := readHead(br)
		ch <- result{h, err}
	}()
	var head Response
	select {
	case res := <-ch:
		if res.err != nil {
			c.Close()
			s.tb.Fatalf("bitetest: reading the answer to %s: %v", r.Target, s.explain(res.err))
		}
		head = res.r
	case <-time.After(wait(s.tb)):
		c.Close()
		s.tb.Fatalf("bitetest: no answer to %s in %s", r.Target, waitText(wait(s.tb)))
	}
	if ct := head.Get("content-type"); head.Status != 200 || !strings.HasPrefix(ct, "text/event-stream") {
		resp, _ := readBody(br, head, false)
		c.Close()
		s.tb.Fatalf("bitetest: %s answered %d %s, not a stream of events: %s", r.Target, head.Status, ct, resp.Body)
	}
	e := &EventStream{Status: head.Status, Header: head.Header, srv: s, c: c, in: make(chan eventOrEnd, 64)}
	var body io.Reader = br
	if te := head.Header["transfer-encoding"]; len(te) > 0 && strings.EqualFold(te[0], "chunked") {
		body = &chunked{br: br}
	}
	go e.read(bufio.NewReader(body))
	s.tb.Cleanup(func() { c.Close() })
	return e
}

// Next returns the next event or comment, and fails the test if the stream ended or nothing came
// in five real seconds (or the wait WaitEnv sets).
func (e *EventStream) Next() Event {
	e.srv.tb.Helper()
	ev, err := e.TryNext(0)
	if err != nil {
		e.srv.tb.Fatalf("bitetest: reading the next event: %v", err)
	}
	return ev
}

// TryNext returns the next event or comment, io.EOF once the app has ended the stream, or a
// timeout error when nothing came in the real time d (when d is zero, five seconds or the wait
// WaitEnv sets). After the
// stream ended every call returns the same error.
func (e *EventStream) TryNext(d time.Duration) (Event, error) {
	if e.ended != nil {
		return Event{}, e.ended
	}
	if d == 0 {
		d = wait(e.srv.tb)
	}
	select {
	case x := <-e.in:
		if x.err != nil {
			e.ended = x.err
			return Event{}, x.err
		}
		return x.ev, nil
	case <-time.After(d):
		return Event{}, errTimeout
	}
}

// Close closes the connection, as a client that leaves; the app's stream function sees its
// context cancelled.
func (e *EventStream) Close() { e.c.Close() }

// read parses the event stream format: fields until a blank line, which ends an event; a line
// that starts with a colon is a comment.
func (e *EventStream) read(br *bufio.Reader) {
	var ev Event
	var data []string
	fields := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" {
				e.in <- eventOrEnd{err: io.EOF}
			} else {
				e.in <- eventOrEnd{err: err}
			}
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if fields {
				ev.Data = strings.Join(data, "\n")
				e.in <- eventOrEnd{ev: ev}
			}
			ev, data, fields = Event{}, nil, false
			continue
		}
		if c, ok := strings.CutPrefix(line, ":"); ok {
			e.in <- eventOrEnd{ev: Event{Comment: strings.TrimPrefix(c, " ")}}
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		fields = true
		switch name {
		case "data":
			data = append(data, value)
		case "id":
			ev.ID = value
		case "event":
			ev.Name = value
		case "retry":
			if ms, err := strconv.ParseInt(value, 10, 64); err == nil {
				ev.Retry = time.Duration(ms) * time.Millisecond
			}
		}
	}
}

// chunked reads a chunked body's data, ending with io.EOF at the last chunk.
type chunked struct {
	br   *bufio.Reader
	left int64
	done bool
}

func (c *chunked) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	if c.left == 0 {
		l, err := c.br.ReadString('\n')
		if err != nil {
			return 0, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(l), 16, 64)
		if err != nil {
			return 0, fmt.Errorf("a chunk size %q: %w", strings.TrimSpace(l), err)
		}
		if n == 0 {
			c.done = true
			return 0, io.EOF
		}
		c.left = n
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.br.Read(p)
	c.left -= int64(n)
	if c.left == 0 && err == nil {
		_, err = c.br.Discard(2)
	}
	return n, err
}

// requestBytes is r on the wire, with its JSON written as the app writes JSON. A Request with both
// Body and JSON, or a JSON value the app's codec cannot write, fails the test.
func (s *Server) requestBytes(r Request) []byte {
	s.tb.Helper()
	if r.JSON != nil {
		if r.Body != nil {
			s.tb.Fatalf("bitetest: the Request to %s %s has both Body and JSON: give JSON for a Go value written as the app writes JSON, or Body for the bytes themselves, not both", r.Method, r.Target)
		}
		if hint := bodyWrapper(r.JSON); hint != "" {
			s.tb.Fatalf("bitetest: the JSON of the Request to %s %s is a %T, a Body type of bites and not a JSON value: %s", r.Method, r.Target, r.JSON, hint)
		}
		b, err := testcodec.Encode(s.app, r.JSON)
		if err != nil {
			s.tb.Fatalf("bitetest: writing the JSON of the Request to %s %s, a %T, as the app writes JSON: %v; send hand-written JSON in Body instead", r.Method, r.Target, r.JSON, err)
		}
		r.Body = b
		if !hasHeader(r.Header, "Content-Type") {
			h := make(map[string][]string, len(r.Header)+1)
			for k, v := range r.Header {
				h[k] = v
			}
			h["Content-Type"] = []string{"application/json"}
			r.Header = h
		}
	}
	return wireBytes(r)
}

// bodyWrapper says what to send instead when v is one of the Body types of bites that wrap or
// replace a JSON value, whose own fields would otherwise be written ({"value":...}); "" for any
// other value.
func bodyWrapper(v any) string {
	t := reflect.TypeOf(v)
	if t.PkgPath() != "github.com/nevindra/bites" {
		return ""
	}
	name := t.Name()
	switch {
	case strings.HasPrefix(name, "Raw["):
		return "give the value it decodes, in.Body.Value, as JSON"
	case strings.HasPrefix(name, "Form["), strings.HasPrefix(name, "Submitted["):
		return "a form is not JSON: send the encoded form (url.Values.Encode) in Body with Content-Type application/x-www-form-urlencoded"
	case strings.HasPrefix(name, "Multipart["), name == "Upload", name == "BodyReader":
		return "an upload is not JSON: send the bytes in Body with their Content-Type"
	}
	return ""
}

// hasHeader reports whether h has a field called name, in any case.
func hasHeader(h map[string][]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// wireBytes is r on the wire, its body as Body holds it.
func wireBytes(r Request) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", r.Method, r.Target)
	has := map[string]bool{}
	for k, vs := range r.Header {
		has[strings.ToLower(k)] = true
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	if !has["host"] {
		b.WriteString("Host: test\r\n")
	}
	if len(r.Body) > 0 && !has["content-length"] && !has["transfer-encoding"] {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(r.Body))
	}
	b.WriteString("\r\n")
	b.Write(r.Body)
	return b.Bytes()
}

// remoteOf is the client address r is sent from.
func remoteOf(r Request) string {
	if r.Remote == "" {
		return "203.0.113.9:4000"
	}
	return r.Remote
}
