// Package bitetest runs a bites App over in-memory connections and a clock that moves only when
// told to (ADR 0027). Nothing is skipped between the bytes and the handler: the request goes
// through the engine's reader and parser, the router, the decoding and the writer exactly as a
// socket's would, minus the kernel. So a request and its 400, a body that is too large, a client
// that leaves mid-handler, or a timeout reached by advancing the clock are all tests with no port.
//
//	clk := bitetest.NewClock()
//	app := bites.New(bites.WithClock(clk))
//	app.Get("/users/{id}", users.Get)
//	srv := bitetest.Start(t, app)
//	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/users/abc"})
//
// What it cannot simulate is the kernel: a reset connection, a partial write and a full socket
// buffer are covered by the engine's own loopback tests.
package bitetest

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/servehook"
	"github.com/nevindra/bites/internal/testcodec"
	"github.com/nevindra/bites/internal/testlog"
	"github.com/nevindra/bites/internal/wire"
)

// Clock is a clock that moves only when Advance is called. Give it to the App with
// bites.WithClock before starting it.
type Clock struct{ *clock.Fake }

// NewClock returns a Clock reading 2026-01-01 00:00:00 UTC.
func NewClock() *Clock { return &Clock{clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))} }

// Server is an App being served over memory.
type Server struct {
	tb   testing.TB
	app  *bites.App
	ln   *memconn.Listener
	done chan error
	logs *testLog
}

// Start serves app on an in-memory listener and stops it when the test ends. The app's clock is
// the one its connections measure their deadlines on, so give the App a Clock to control them.
// The app's server is built before Start returns: a mistake in its routes panics here, in the
// test, and a Shutdown called right after Start stops a server that is serving.
//
// An App that was not given a logger with bites.WithLogger logs into tb.Log instead of to the
// process's default logger, so its start-up lines and handler errors show only when the test
// fails or runs with -v. One that was given a logger keeps it. Nothing is logged into tb after
// the server is closed.
func Start(tb testing.TB, app *bites.App) *Server {
	tb.Helper()
	s := &Server{tb: tb, app: app, ln: memconn.Listen(app.Clock()), done: make(chan error, 1)}
	s.logs = newTestLog(tb)
	testlog.Set(app, slog.New(s.logs))
	tb.Cleanup(s.Close)
	serve, err := servehook.Start(app, s.ln) // the server is built now, so a Shutdown right after Start stops it
	if err != nil {
		tb.Fatalf("bitetest.Start: the app did not start: %v", err)
	}
	go func() { s.done <- serve() }()
	return s
}

// Close stops the server and closes every connection.
func (s *Server) Close() {
	s.app.Close()
	s.ln.Close()
	s.logs.stop()
}

// WaitEnded returns once every request the server has received whole has ended, which is when
// the End of its Observers has run (ADR 0051, ADR 0027). Do and Read return when the answer is
// read, and the Observers' End runs after the last byte, so a test that looks at what an Observer
// recorded (an access record, a counter) calls WaitEnded first. It does not sleep or poll: it
// waits on the connections, and a connection reads its next request only after End. A request
// being handled, a stream still sending, and a connection the client closed but the server has
// not finished with are waited for; a WebSocket is settled once its upgrade has been answered
// (End runs at the 101), not when the socket closes, and a request the client has only partly
// sent is not waited for. If they do not settle in five real seconds, for a handler held on the
// fake Clock for example, the test fails.
func (s *Server) WaitEnded() {
	s.tb.Helper()
	if !s.ln.Settled(5 * time.Second) {
		s.tb.Fatal("bitetest: a request has not ended in 5 s")
	}
}

// Dial opens a connection to the server as the client at remote ("203.0.113.9:4000"), for a test
// that writes its own bytes: a pipelined request, a client that leaves, a slow head.
func (s *Server) Dial(remote string) net.Conn {
	s.tb.Helper()
	c, err := s.ln.Dial(memconn.Addr{Name: remote})
	if err != nil {
		s.tb.Fatalf("bitetest: %v", s.explain(err))
	}
	return c
}

// explain adds to an error of a connection why a test most often meets it once the server has
// stopped accepting: the app was shut down, and Shutdown with no WithShutdownDelay closes the
// listener at once, so a request after it is refused or its connection closed unanswered.
func (s *Server) explain(err error) error {
	if !s.ln.Closed() {
		return err
	}
	return fmt.Errorf("%w: the app is not accepting connections, because Shutdown or Close has stopped it; "+
		"to test requests served while the app shuts down, give the App bites.WithShutdownDelay, which keeps "+
		"the listener open for the delay after Shutdown is called, and send them during it", err)
}

// Request is one request to send. Host is "test" and Content-Length is set from Body unless Header
// gives them.
type Request struct {
	// Method is the HTTP method, such as GET.
	Method string
	// Target is the request path and query, such as /users/42?expand=true.
	Target string
	// Header holds the header fields to send, as Response.Header holds them: a name sent with
	// several values is sent as that many lines. Host is "test" unless set here.
	Header map[string][]string
	// Body is the request body; Content-Length is set from it unless Header sets it.
	Body []byte
	// Remote is the client address the server sees; "203.0.113.9:4000" if empty.
	Remote string
}

// Response is one response read back. Header names are lower-cased.
type Response struct {
	// Status is the status code of the response.
	Status int
	// Header holds the header fields, with names lower-cased.
	Header map[string][]string
	// Body is the body bytes, with a chunked body decoded.
	Body []byte

	app *bites.App // the app that answered, whose codec JSON reads with; nil for a Response built by hand
}

// JSON decodes the body into v, a pointer, as a client of the app reads it: with the app's own
// codec and naming policy (ADR 0074), so an answer fills the handler's own output type, a
// bites.Opt, a bites.Null and a tagged union included ("created_at" fills CreatedAt under
// SnakeCase, and a union's variant is chosen by its discriminator).
//
// An answer is not a request, so the read is lenient where a request is strict: a member v has no
// field for is skipped, and a member the answer left out leaves its field zero, even one a
// request would require. A member of the wrong type, or JSON that is not JSON, is an error. For a
// type the app's codec has no plan for (a map[string]any, an any, a type with a channel field) the
// body is read with encoding/json instead, matching a field name without case, underscores and
// dashes. A Response a test built by hand, with no app behind it, is read that way too.
func (r Response) JSON(v any) error { return decodeJSON(r.app, r.Body, v) }

// JSON decodes the body of r as a T and returns it, failing the test with the status and the body
// when it is not JSON of that shape. It decodes as Response.JSON does:
//
//	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/users/7"})
//	got := bitetest.JSON[User](t, resp)
func JSON[T any](tb testing.TB, r Response) T {
	tb.Helper()
	var v T
	if err := r.JSON(&v); err != nil {
		tb.Fatalf("bitetest: decoding the %d answer as %T: %v\nbody: %s", r.Status, v, err, r.Body)
	}
	return v
}

// decodeJSON is how bitetest reads what the app wrote: with the app's codec when it has a plan for
// v's type, so what a client of the app reads is what the test reads. Without an app, or for a
// type with no plan, it falls back to encoding/json, where the app's wire names are its naming
// policy's (ADR 0011) and matching without case, underscores and dashes finds every field under
// any of them. An Opt, a Null and a Date are read as the app writes them there too.
func decodeJSON(app *bites.App, b []byte, v any) error {
	if app != nil {
		if ok, err := testcodec.Decode(app, b, v); ok {
			return err
		}
	}
	return json.Unmarshal(b, v, json.MatchCaseInsensitiveNames(true), wire.Unmarshalers)
}

// Text is the body as a string.
func (r Response) Text() string { return string(r.Body) }

// Get returns the first value of a header, by any case of its name.
func (r Response) Get(name string) string {
	if v := r.Header[strings.ToLower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Do sends r on a new connection, reads one response and closes the connection. A response that
// does not arrive in five real seconds fails the test. The response is read while the request is
// written, so an answer the app gives before it has read the whole body (a 413 for a body over
// MaxBody, a 401 from a middleware) is returned as any other, though the app closed the connection
// on the rest of the body.
func (s *Server) Do(r Request) Response {
	s.tb.Helper()
	c := s.Dial(remoteOf(r))
	defer c.Close()
	req := requestBytes(r)
	written := make(chan error, 1)
	go func() {
		_, err := c.Write(req)
		written <- err
	}()
	resp, err := s.read(c, r.Method == "HEAD")
	if err != nil {
		select {
		case werr := <-written:
			if werr != nil {
				s.tb.Fatalf("bitetest: writing the request: %v", werr)
			}
		default:
		}
		s.tb.Fatalf("bitetest: %v", s.explain(err))
	}
	return resp
}

// Read reads one response from c. head says the request was HEAD, so the body is not read.
func (s *Server) Read(c net.Conn, head bool) Response {
	s.tb.Helper()
	r, err := s.read(c, head)
	if err != nil {
		s.tb.Fatalf("bitetest: %v", s.explain(err))
	}
	return r
}

// read reads one response from c within five real seconds.
func (s *Server) read(c net.Conn, head bool) (Response, error) {
	type result struct {
		r   Response
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := readResponse(bufio.NewReader(c), head)
		ch <- result{r, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return Response{}, fmt.Errorf("reading the response: %w", res.err)
		}
		res.r.app = s.app
		return res.r, nil
	case <-time.After(5 * time.Second):
		return Response{}, errors.New("no response in 5 s")
	}
}

func readResponse(br *bufio.Reader, head bool) (Response, error) {
	r, err := readHead(br)
	if err != nil {
		return Response{}, err
	}
	return readBody(br, r, head)
}

// readHead reads a response's status line and header fields.
func readHead(br *bufio.Reader) (Response, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return Response{}, err
	}
	f := strings.Fields(line)
	if len(f) < 2 {
		return Response{}, io.ErrUnexpectedEOF
	}
	r := Response{Header: map[string][]string{}}
	r.Status, _ = strconv.Atoi(f[1])
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			return Response{}, err
		}
		l = strings.TrimRight(l, "\r\n")
		if l == "" {
			return r, nil
		}
		k, v, _ := strings.Cut(l, ": ")
		k = strings.ToLower(k)
		r.Header[k] = append(r.Header[k], v)
	}
}

// readBody reads the body of the response whose head is r. head says the request was HEAD.
func readBody(br *bufio.Reader, r Response, head bool) (Response, error) {
	hasBody := !head && r.Status >= 200 && r.Status != 204 && r.Status != 304
	if te := r.Header["transfer-encoding"]; hasBody && len(te) > 0 && strings.EqualFold(te[0], "chunked") {
		// A stream (ADR 0030): the chunks are joined; a connection cut before the terminating chunk
		// is an error, which is how a test sees a stream that was cut.
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return Response{}, err
			}
			n, err := strconv.ParseInt(strings.TrimSpace(l), 16, 64)
			if err != nil {
				return Response{}, err
			}
			if n == 0 {
				if _, err := br.ReadString('\n'); err != nil {
					return Response{}, err
				}
				return r, nil
			}
			off := len(r.Body)
			r.Body = append(r.Body, make([]byte, n)...)
			if _, err := io.ReadFull(br, r.Body[off:]); err != nil {
				return Response{}, err
			}
			if _, err := br.Discard(2); err != nil {
				return Response{}, err
			}
		}
	}
	if _, ok := r.Header["content-length"]; !ok && hasBody && len(r.Header["connection"]) > 0 && r.Header["connection"][0] == "close" {
		// An HTTP/1.0 stream: the body ends when the connection does.
		b, err := io.ReadAll(br)
		if err != nil {
			return Response{}, err
		}
		r.Body = b
		return r, nil
	}
	if n, ok := r.Header["content-length"]; ok && !head && r.Status >= 200 && r.Status != 204 && r.Status != 304 {
		cl, _ := strconv.Atoi(n[0])
		r.Body = make([]byte, cl)
		if _, err := io.ReadFull(br, r.Body); err != nil {
			return Response{}, err
		}
	}
	return r, nil
}
