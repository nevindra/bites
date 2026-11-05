// Package stdhttp mounts a plain net/http handler on a bites route, and serves a whole app as an
// http.Handler: the slow paths of ADR 0019, for pprof, expvar, a third-party handler or an
// http.FileServer one way, and for a net/http server or an httptest test the other. It is the
// only package of the module that imports net/http, so an application that uses neither links
// nothing of it.
//
//	stdhttp.Mount(app, "/debug/pprof/", http.DefaultServeMux)
//	mux.Handle("/v2/", http.StripPrefix("/v2", stdhttp.Handler(app)))
//
// What follows is about Mount; Handler says what it does in its own comment.
//
// Each request builds an *http.Request and an http.ResponseWriter over the engine's exchange and
// runs the handler on a goroutine of its own, which is what lets a handler stream and flush while
// the engine keeps the connection. These routes are outside the allocation budget of ADR 0003;
// a hot route does not belong here. What is supported, and what is not:
//
//   - Method, path (decoded, with RawPath when it differs), query, every header, Host, RemoteAddr
//     and the request's context, which is cancelled when the client leaves (ADR 0016).
//   - The request body is read whole before the handler runs, up to the route's MaxBody, for POST,
//     PUT, PATCH and OPTIONS; GET, HEAD and DELETE routes read none (ADR 0044). Request.TLS is nil.
//   - Write, WriteHeader, Header, Flush (also http.NewResponseController(w).Flush), and a handler
//     that panics, which is a 500 before the first flush and a cut connection after it.
//   - A response the handler finishes before its first Flush and before 32 KiB is written is
//     buffered and sent with a Content-Length; a longer or flushed one is streamed, chunked
//     (ADR 0030). HEAD is served by the GET mount with the body dropped.
//   - Not supported: Hijack (the engine owns the connection, ADR 0008), trailers (dropped), 1xx
//     informational responses (dropped), HTTP/2 push, per-request deadlines through
//     http.ResponseController, and a Content-Length the handler sets (the engine's own is sent).
package stdhttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"sync"

	"github.com/nevindra/bites"
)

// call is the request a mounted handler serves, built from the engine's view of the head by a
// Provider (ADR 0023) that Mount registers for each route.
type call struct{ req *http.Request }

type headOnly struct{ Call call }
type withBody struct {
	Call call
	Body []byte
}

// Methods is the set Mount registers: the methods whose requests a mounted handler may serve. A
// HEAD request is served by the GET route.
var Methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// Mount serves h for every method of Methods on pattern, a path with no method and, to cover a
// subtree, a trailing slash ("/debug/pprof/"). The request the handler sees has the full path;
// wrap h in http.StripPrefix to hand it the rest. Options are the route options of the typed
// routes (MaxBody, Use, Provide). The mounted routes are left out of the app's API document
// (ADR 0069), since a handler's types do not say what it serves; a Summary or Description given
// here is refused for that reason. A registration mistake panics at the line that called Mount,
// like any other (ADR 0007).
func Mount(app *bites.App, pattern string, h http.Handler, opts ...bites.RouteOption) {
	MountMethods(app, Methods, pattern, h, opts...)
}

// MountMethods is Mount for the methods given. A method that is not a standard one is allowed,
// and so is a subset: "GET" alone makes a read-only mount whose other methods are a 405.
func MountMethods(app *bites.App, methods []string, pattern string, h http.Handler, opts ...bites.RouteOption) {
	if h == nil {
		panic("stdhttp: Mount was given a nil handler")
	}
	opts = withAnswers(append([]bites.RouteOption{bites.Provide(provider)}, opts...))
	for _, m := range methods {
		if hasBody(m) {
			app.Handle(m, pattern, serveBody(h), opts...)
		} else {
			app.Handle(m, pattern, serveHead(h), opts...)
		}
	}
}

// MountGroup is Mount on a group; the pattern is relative to the group's prefix.
func MountGroup(g *bites.Group, pattern string, h http.Handler, opts ...bites.RouteOption) {
	if h == nil {
		panic("stdhttp: MountGroup was given a nil handler")
	}
	opts = withAnswers(append([]bites.RouteOption{bites.Provide(provider)}, opts...))
	for _, m := range Methods {
		if hasBody(m) {
			g.Handle(m, pattern, serveBody(h), opts...)
		} else {
			g.Handle(m, pattern, serveHead(h), opts...)
		}
	}
}

func hasBody(m string) bool { return m != "GET" && m != "HEAD" && m != "DELETE" }

// withAnswers adds the types a mounted handler's answer may be, and leaves the routes out of the
// API document: a net/http handler's types say nothing about what it reads or answers
// (ADR 0019, ADR 0069).
func withAnswers(opts []bites.RouteOption) []bites.RouteOption {
	return append(opts, bites.Undocumented(),
		bites.Returns[bites.Bytes](), bites.Returns[bites.Stream](),
		bites.Returns[bites.NoContent](), bites.Returns[bites.NotModified]())
}

func serveHead(h http.Handler) func(context.Context, headOnly) (any, error) {
	return func(ctx context.Context, in headOnly) (any, error) { return run(ctx, h, in.Call.req, nil) }
}

func serveBody(h http.Handler) func(context.Context, withBody) (any, error) {
	return func(ctx context.Context, in withBody) (any, error) { return run(ctx, h, in.Call.req, in.Body) }
}

// provider builds the *http.Request from the engine's view of the head.
func provider(_ context.Context, r bites.Request, _ struct{}) (call, error) {
	path, err := url.PathUnescape(r.Path())
	if err != nil {
		return call{}, bites.BadRequest("the path is not valid percent-encoding")
	}
	u := &url.URL{Path: path, RawQuery: r.RawQuery()}
	if raw := r.Path(); raw != path {
		u.RawPath = raw
	}
	uri := r.Path()
	if q := r.RawQuery(); q != "" {
		uri += "?" + q
	}
	req := &http.Request{
		Method:     r.Method(),
		URL:        u,
		RequestURI: uri,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:     http.Header{},
		Host:       r.Host(),
		RemoteAddr: r.RemoteAddr().String(),
	}
	for k, v := range r.Headers() {
		k = textproto.CanonicalMIMEHeaderKey(k)
		switch k {
		case "Host":
			continue
		case "Transfer-Encoding":
			req.TransferEncoding = append(req.TransferEncoding, v)
			continue
		}
		req.Header[k] = append(req.Header[k], v)
	}
	return call{req}, nil
}

const commitSize = 32 << 10

// chunk is bytes for the stream, or a flush.
type chunk struct {
	data  []byte
	flush bool
}

// writer is the ResponseWriter. The handler runs on its own goroutine; the first of a flush, a
// write past commitSize and the handler's return decides how the response is sent, and until then
// everything is held here.
type writer struct {
	mu        sync.Mutex
	header    http.Header
	status    int
	snapshot  http.Header // the header as it was at WriteHeader
	buf       bytes.Buffer
	committed bool // the response is a stream: the head and buf go out now, the rest as chunks
	ended     bool // the handler returned
	panicked  any
	head      bool          // a HEAD request: flushes and sizes do not commit
	decided   chan struct{} // closed by the first of commit and end
	isDecided bool
	chunks    chan chunk    // unbuffered: a write returns when the stream took it
	fin       chan struct{} // closed when the handler returned
	gone      chan struct{} // closed when nothing will take chunks any more
	goneOnce  sync.Once
}

func (w *writer) Header() http.Header { return w.header }

func (w *writer) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(code)
}

func (w *writer) writeHeaderLocked(code int) {
	if w.status != 0 || code < 200 { // 1xx are dropped; the real status comes later
		return
	}
	if code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code))
	}
	w.status = code
	w.snapshot = w.header.Clone()
}

func (w *writer) decideLocked() {
	if !w.isDecided {
		w.isDecided = true
		close(w.decided)
	}
}

func (w *writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.status == 0 {
		w.writeHeaderLocked(200)
	}
	if w.status == 204 || w.status == 304 {
		w.mu.Unlock()
		return 0, http.ErrBodyNotAllowed
	}
	if !w.committed {
		w.buf.Write(p)
		if w.buf.Len() > commitSize && !w.head {
			w.committed = true
			w.decideLocked()
		}
		w.mu.Unlock()
		return len(p), nil
	}
	w.mu.Unlock()
	if w.head {
		return len(p), nil
	}
	if err := w.send(chunk{data: bytes.Clone(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *writer) send(c chunk) error {
	select {
	case w.chunks <- c:
		return nil
	case <-w.gone:
		return context.Canceled
	}
}

// FlushError is what http.NewResponseController(w).Flush calls.
func (w *writer) FlushError() error {
	w.mu.Lock()
	if w.status == 0 {
		w.writeHeaderLocked(200)
	}
	if w.head || w.status == 204 || w.status == 304 {
		w.mu.Unlock()
		return nil
	}
	if !w.committed {
		w.committed = true
		w.decideLocked()
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	return w.send(chunk{flush: true})
}

// Flush implements http.Flusher.
func (w *writer) Flush() { _ = w.FlushError() }

func (w *writer) cut() { w.goneOnce.Do(func() { close(w.gone) }) }

// run serves one request with h and returns the bites response: Bytes, NoContent, NotModified or
// a Stream.
func run(ctx context.Context, h http.Handler, req *http.Request, body []byte) (any, error) {
	r := req.WithContext(ctx)
	if len(body) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	} else {
		r.Body = http.NoBody
	}
	if len(r.TransferEncoding) > 0 {
		r.ContentLength = -1
	}
	w := &writer{
		header: http.Header{}, head: r.Method == "HEAD",
		decided: make(chan struct{}), chunks: make(chan chunk), fin: make(chan struct{}), gone: make(chan struct{}),
	}
	stop := context.AfterFunc(ctx, w.cut)
	go func() {
		defer func() {
			p := recover()
			w.mu.Lock()
			w.ended, w.panicked = true, p
			w.decideLocked()
			w.mu.Unlock()
			close(w.fin)
		}()
		h.ServeHTTP(w, r)
	}()
	select {
	case <-w.decided:
	case <-ctx.Done():
		w.cut()
		<-w.fin
		stop()
		return nil, context.Cause(ctx)
	}
	w.mu.Lock()
	status, hdr, committed, ended, panicked := w.status, w.snapshot, w.committed, w.ended, w.panicked
	if status == 0 { // the handler wrote nothing at all
		status, hdr = 200, w.header.Clone()
	}
	prefix := w.buf.Bytes()
	w.mu.Unlock()
	if !committed {
		stop()
		if !ended {
			// cannot happen: decided without commit means the handler ended
			<-w.fin
		}
		if panicked != nil {
			return nil, fmt.Errorf("stdhttp: the handler panicked: %v", panicked)
		}
		return buffered(status, hdr, prefix), nil
	}
	out := bites.Stream{Status: status, Header: convert(hdr, true, prefix)}
	out.Func = func(ctx context.Context, sw *bites.StreamWriter) error {
		defer stop()
		err := pump(ctx, w, sw, prefix)
		if err != nil {
			w.cut()
			<-w.fin // the handler may not use the request after the stream is gone
		}
		return err
	}
	return out, nil
}

func pump(ctx context.Context, w *writer, sw *bites.StreamWriter, prefix []byte) error {
	if len(prefix) > 0 {
		if _, err := sw.Write(prefix); err != nil {
			return err
		}
	}
	if err := sw.Flush(); err != nil { // the commit was a flush, or the buffer reached its size
		return err
	}
	for {
		select {
		case c := <-w.chunks:
			if len(c.data) > 0 {
				if _, err := sw.Write(c.data); err != nil {
					return err
				}
			}
			if c.flush {
				if err := sw.Flush(); err != nil {
					return err
				}
			}
		case <-w.fin:
			w.mu.Lock()
			p := w.panicked
			w.mu.Unlock()
			if p != nil {
				return fmt.Errorf("stdhttp: the handler panicked: %v", p)
			}
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// buffered is the answer of a handler that finished before it flushed.
func buffered(status int, h http.Header, body []byte) any {
	switch {
	case status == 204:
		return bites.NoContent{Header: convert(h, true, nil)}
	case status == 304:
		return bites.NotModified{Header: convert(h, true, nil)}
	case status == 200:
		ct := h.Get("Content-Type")
		if ct == "" {
			ct = sniff(body)
		}
		return bites.Bytes{ContentType: ct, Body: bytes.Clone(body), Header: convert(h, false, nil)}
	}
	data := bytes.Clone(body)
	return bites.Stream{Status: status, Header: convert(h, true, data), Func: func(_ context.Context, sw *bites.StreamWriter) error {
		_, err := sw.Write(data)
		return err
	}}
}

func sniff(body []byte) string {
	if len(body) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(body)
}

// convert copies h without the fields the engine writes itself. keepType keeps Content-Type,
// which a Bytes carries in its own field; for a stream without one, the type is sniffed from the
// first bytes as net/http does.
func convert(h http.Header, keepType bool, first []byte) bites.Header {
	out := make(bites.Header, len(h)+1)
	for k, v := range h {
		switch k {
		case "Content-Length", "Transfer-Encoding", "Connection", "Date", "Trailer":
			continue
		case "Content-Type":
			if !keepType {
				continue
			}
		}
		if len(k) > len(http.TrailerPrefix) && k[:len(http.TrailerPrefix)] == http.TrailerPrefix {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	if keepType && out["Content-Type"] == nil && len(first) > 0 {
		out["Content-Type"] = []string{http.DetectContentType(first)}
	}
	return out
}
