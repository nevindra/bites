package bites

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/router"
	"github.com/nevindra/bites/internal/wire"
)

// Middleware wraps the rest of the chain (ADR 0022). It sees a read-only view of the request,
// calls next to run what is inside it, and returns the Response it wants sent. It short-circuits
// by returning without calling next, and passes a value inward by calling next with a wrapped
// context. An error it returns is answered as a problem+json like a handler's (ADR 0049), and
// the middleware outside it then sees that answer as a Response.
//
// Order (ADR 0048): app middleware (App.Use) runs before the route is matched, so it sees 404,
// 405 and a preflight; group and route middleware run after the match, outermost group first.
type Middleware func(ctx context.Context, req Request, next Next) (Response, error)

// Next runs the rest of the chain. It is a small value, not a function, because a function value
// made for each request and layer is an allocation a request that the budget of ADR 0003 does
// not have. It is valid for the length of the call that received it: Call after the request has
// been answered returns a 500 Response and logs, and never runs another request's handler.
type Next struct {
	run  *chainRun
	gen  uint64
	step int32
}

// Call runs the rest of the chain with ctx and returns what it produced. It never returns an
// error: an error from the handler, a Provider or an inner middleware is already a problem+json
// Response here, so the middleware outside always sees the final status (ADR 0022).
func (n Next) Call(ctx context.Context) Response {
	r := n.run
	if r == nil || r.gen.Load() != n.gen {
		if r != nil {
			r.a.log.Error("bites: Next was called after its request was answered")
		}
		return Response{kind: kindProblem, status: 500}
	}
	return r.step(ctx, int(n.step))
}

// Request is a read-only view of the request head (ADR 0022). Every string in it is a copy made
// for this request, never the engine's read buffer (ADR 0008), so a middleware that keeps one
// keeps that request's own bytes and no other request's. The body is not part of the view.
//
// The methods that reach the request itself (RemoteAddr, RequestID, SetRequestID) check that the
// request is still the one running and do nothing, or answer with a zero value, after it has been
// answered: a Request kept past the call holds no pooled object it can read. The time on the
// app's clock is read from the context beside it, with Now(ctx).
type Request struct {
	run                 *chainRun
	gen                 uint64
	method, path, query string
	headers             string // the header lines as received, "Name: value\r\n" each, copied
}

// Method is the request method, an interned constant for the nine standard ones.
func (r Request) Method() string { return r.method }

// Path is the raw request path, still percent-encoded.
func (r Request) Path() string { return r.path }

// RawQuery is the query after the '?', still percent-encoded; empty when there is none.
func (r Request) RawQuery() string { return r.query }

// Host is the Host header.
func (r Request) Host() string { return r.Header("host") }

// Header returns the first value of the header called name (matched without regard to case), or
// "" when the request has none. It scans the copy of the head line by line without allocating:
// quick for one or two lookups.
func (r Request) Header(name string) string {
	b := r.headers
	n := len(name)
	for len(b) > 0 {
		i := strings.IndexByte(b, '\n')
		if i < 0 {
			return ""
		}
		line := b[:i]
		b = b[i+1:]
		if len(line) <= 1 { // the blank line that ends the block
			return ""
		}
		if len(line) <= n || line[n] != ':' || !foldPrefix(line, name) {
			continue
		}
		v := line[n+1:]
		for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
			v = v[1:]
		}
		for len(v) > 0 && (v[len(v)-1] == '\r' || v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
			v = v[:len(v)-1]
		}
		return v
	}
	return ""
}

// Cookie returns the value of the first cookie called name (matched exactly: cookie names are
// case-sensitive) across the request's Cookie header fields, in the order the client sent them,
// and whether there was one. One pair of double quotes around the value is removed and the value
// is not percent-decoded. A pair with no '=' or an empty name is skipped, and an empty name
// returns "", false. The parsing is the Cookie section's own, so a handler's Cookie section and
// this method read the same header the same way (ADR 0043). The value is a substring of the
// request's copy of the head and nothing is allocated; like Header it keeps working on a Request
// kept past the call, and returns what that request sent. A handler reads its cookies through its
// Input's Cookie section instead; this is for a middleware that decides before the Input exists.
func (r Request) Cookie(name string) (value string, ok bool) {
	if name == "" {
		return "", false
	}
	b := r.headers
	for len(b) > 0 {
		i := strings.IndexByte(b, '\n')
		if i < 0 {
			return "", false
		}
		line := b[:i]
		b = b[i+1:]
		if len(line) <= 1 { // the blank line that ends the block
			return "", false
		}
		const field = "cookie"
		if len(line) <= len(field) || line[len(field)] != ':' || !foldPrefix(line, field) {
			continue
		}
		v := line[len(field)+1:]
		for len(v) > 0 {
			var n, val string
			var good bool
			n, val, v, good = nextCookie(v)
			if good && n == name {
				return val, true
			}
		}
	}
	return "", false
}

// foldPrefix reports whether line starts with name, ignoring ASCII case.
func foldPrefix(line, name string) bool {
	for j := 0; j < len(name); j++ {
		a, b := line[j], name[j]
		if a == b {
			continue
		}
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

// RemoteAddr is the client's address as the connection reports it (the address of a proxy, when
// there is one in front). The zero AddrPort after the request was answered.
func (r Request) RemoteAddr() netip.AddrPort {
	if !r.live() {
		return netip.AddrPort{}
	}
	return r.run.ex.RemoteAddrPort()
}

// RequestID is the id a middleware gave this request with SetRequestID, or "". An Observer's End
// reads it too: the run keeps its own copy after the engine has let go of the answer.
func (r Request) RequestID() string {
	if !r.live() {
		return ""
	}
	return r.run.reqID
}

// SetRequestID gives the request an id. Every problem+json answer written afterwards, by the
// framework or the engine, carries it as request_id (ADR 0012). The request-ID middleware of
// ADR 0032 is its caller; it does not touch the response headers, which are the middleware's.
func (r Request) SetRequestID(id string) {
	if r.live() {
		r.run.reqID = id
		r.run.ex.SetRequestID(id)
		r.run.ex.Context().SetRequestID(id)
	}
}

// setRequestIDHeader tells the engine which header the id goes in, for the 500 it writes after a
// panic, which the request-ID middleware never sees (ADR 0028).
func (r Request) setRequestIDHeader(name string) {
	if r.live() {
		r.run.ex.SetRequestIDHeader(name)
	}
}

// AddLogAttrs adds attributes to this request's log record (ADR 0023). A Provider calls it with
// what it found, the user's ID for one, and the access log, an Observer, writes them when the
// request has ended (ADR 0051). The attributes are copied; nothing happens after
// the request was answered.
func (r Request) AddLogAttrs(attrs ...slog.Attr) {
	if r.live() {
		r.run.attrs = append(r.run.attrs, attrs...)
	}
}

// AppendLogAttrs appends the attributes added to this request so far to dst and returns the
// result, as append does. It copies them, so the slice it returns is the caller's own.
func (r Request) AppendLogAttrs(dst []slog.Attr) []slog.Attr {
	if !r.live() {
		return dst
	}
	return append(dst, r.run.attrs...)
}

func (r Request) live() bool { return r.run != nil && r.run.gen.Load() == r.gen }

type respKind uint8

const (
	kindNone    respKind = iota // the zero Response: not an answer
	kindInner                   // what the rest of the chain produced, already in the engine's buffer
	kindFresh                   // built by a middleware, written when the chain returns
	kindProblem                 // a fresh problem+json, written with the request id
)

type headerPair struct{ name, value string }

// Response is the answer as middleware sees it (ADR 0022): a status, a content type, the headers
// middleware added, and the size of the body. The body bytes are not part of it: they are
// written once into the engine's own buffer by the innermost step, so no pooled buffer reaches a
// middleware and nothing here can be used to read or rewrite them.
//
// The value is 80 bytes because the headers middleware added are not in it: those of an answer
// from next are kept in the chain's own list, and the Response holds the range that is its
// (this keeps what is copied across each layer small; a Response of 272 B with six pairs inline
// cost 45 ns a request). A fresh Response, which has no chain yet, holds its headers
// on the heap, which is the short-circuit path and not budgeted.
//
// Methods that change it take a pointer, so a middleware writes
//
//	res := next.Call(ctx)
//	res.AddHeader("X-Served-By", "a")
//	return res, nil
//
// A header the handler itself set (a wrapper's Header, Location, Set-Cookie) is not visible here
// and cannot be replaced; a middleware can add headers and change the status. To replace more,
// return a fresh Response (NewResponse, Problem), and the inner answer is dropped. A Response
// used after its request has been answered has no headers and ignores AddHeader.
type Response struct {
	status int32
	size   int32
	lo, hi int32 // the range of run.hdr that is this Response's headers
	kind   respKind
	stream bool // the answer is a Stream: its body is written after the chain returns, Size is 0
	gen    uint64
	run    *chainRun
	ctype  string
	text   string // a fresh body, or a problem's detail
	fresh  *[]headerPair
}

// NewResponse returns a fresh Response with status and no body, to return instead of calling
// next. The status must be 100 to 599; any other is answered as a 500 and logged.
func NewResponse(status int) Response { return Response{kind: kindFresh, status: int32(status)} }

// Problem returns a fresh problem+json Response with detail (ADR 0012), carrying the request id
// like every other error answer. It is how a middleware refuses a request without an error value.
func Problem(status int, detail string) Response {
	return Response{kind: kindProblem, status: int32(status), text: detail, ctype: problemType}
}

// Status is the status code. It is 0 for the zero Response and when nothing was answered.
func (r *Response) Status() int { return int(r.status) }

// SetStatus changes the status; the body and headers stay.
func (r *Response) SetStatus(status int) { r.status = int32(status) }

// ContentType is the Content-Type the answer was given, or "" when it has none.
func (r *Response) ContentType() string { return r.ctype }

// Size is the number of body bytes the answer has. It is 0 for a stream, whose body is written
// after every middleware has returned and whose size nobody knows yet: ask Streaming.
func (r *Response) Size() int { return int(r.size) }

// Streaming reports whether the answer is a Stream or an SSE (ADR 0030). A middleware sees its
// status, its Content-Type and the headers it adds, as for any answer; it can change the status
// and add headers, which go out with the head, before the first byte. It cannot see the body, as
// with any answer, and a stream answered by a fresh Response is dropped without its function
// running.
func (r *Response) Streaming() bool { return r.stream }

// live is the chain's list of headers when this Response is an answer from next whose request is
// still running, and nil otherwise.
func (r *Response) live() *chainRun {
	if r.kind == kindInner && r.run != nil && r.run.gen.Load() == r.gen {
		return r.run
	}
	return nil
}

// pairs is the headers this Response has.
func (r *Response) pairs() []headerPair {
	if run := r.live(); run != nil {
		return run.hdr[r.lo:r.hi]
	}
	if r.fresh != nil {
		return *r.fresh
	}
	return nil
}

// SetBody gives a fresh Response a body and its content type. It is for a short answer a
// middleware writes itself; the text is copied when the chain returns. On an answer from next it
// replaces the answer: the inner status, headers and body are dropped.
func (r *Response) SetBody(contentType, body string) {
	if r.kind != kindFresh {
		hs := append([]headerPair(nil), r.pairs()...) // the inner body is dropped, the headers middleware added stay
		*r = Response{kind: kindFresh, status: r.status}
		if len(hs) > 0 {
			r.fresh = &hs
		}
	}
	r.ctype, r.text, r.size = contentType, body, int32(len(body))
}

// Header returns the first value of a header a middleware set on this Response, matched without
// regard to case, and whether there is one.
func (r *Response) Header(name string) (string, bool) {
	for _, p := range r.pairs() {
		if foldEq(p.name, name) {
			return p.value, true
		}
	}
	return "", false
}

// AddHeader adds a header line, after any with the same name.
func (r *Response) AddHeader(name, value string) {
	p := headerPair{name, value}
	if run := r.live(); run != nil {
		if int(r.hi) != len(run.hdr) {
			// Something was added after this range, by another Response: move the range to the end.
			start := len(run.hdr)
			run.hdr = append(run.hdr, run.hdr[r.lo:r.hi]...)
			r.lo, r.hi = int32(start), int32(len(run.hdr))
		}
		run.hdr = append(run.hdr, p)
		r.hi++
		return
	}
	if r.kind == kindInner {
		return // the request was answered; there is nothing to add to
	}
	if r.fresh == nil {
		r.fresh = new([]headerPair)
	}
	*r.fresh = append(*r.fresh, p)
}

// SetHeader sets a header: it replaces the value of one this Response already carries from a
// middleware, and adds it otherwise.
func (r *Response) SetHeader(name, value string) {
	ps := r.pairs()
	for i := range ps {
		if foldEq(ps[i].name, name) {
			ps[i].value = value
			return
		}
	}
	r.AddHeader(name, value)
}

func foldEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// chainRun is the state of one request's walk through its middleware. It is pooled and never
// reaches user code: Next and Request hold a pointer to it and a generation, and every use checks
// the generation, which release moves on when the request is answered (ADR 0008).
type chainRun struct {
	a     *App
	ex    *engine.Exchange
	gen   atomic.Uint64
	rt    *route
	ps    router.Params
	req   Request
	hdr   []headerPair // the headers middleware added to the answers of this request, see Response
	attrs []slog.Attr  // AddLogAttrs, in the order they were added
	err   error        // the last error answered (NoteError), told to Observers at End
	reqID string       // SetRequestID, kept until End (the engine clears its own once the answer is built)

	// Observers (ADR 0051): when Start and End ran, and the context each Start returned, in order.
	began, ended time.Time
	ctxs         []context.Context
}

// runSeq gives each chainRun its own range of generations: a run the pool dropped and a new one
// must never hold the same number, or a Response kept from the dropped one would pass for the
// new one's.
var runSeq atomic.Uint64

func newRun(a *App) func() any {
	return func() any {
		r := &chainRun{a: a}
		r.gen.Store(runSeq.Add(1) << 32)
		return r
	}
}

// release ends the run: Next and Request values made for it stop working.
func (r *chainRun) release() {
	r.gen.Add(1)
	r.ex, r.rt, r.req, r.reqID, r.err = nil, nil, Request{}, "", nil
	clear(r.ctxs)
	r.ctxs = r.ctxs[:0]
	clear(r.ps.Values[:r.ps.N])
	r.ps.N = 0
	clear(r.hdr)
	r.hdr = r.hdr[:0]
	clear(r.attrs)
	r.attrs = r.attrs[:0]
	r.a.runs.Put(r)
}

// serveChain answers a request that has middleware or Observers: the app's (rt is nil, the route
// is matched inside the chain) or only its route's (rt and ps are the match). An observed request
// keeps its run until the engine calls End, after the last byte, which releases it.
func (a *App) serveChain(ex *engine.Exchange, rt *route, ps *router.Params) {
	r := a.runs.Get().(*chainRun)
	observed := len(a.obs) > 0
	if observed {
		ex.Observe(r) // first, so that End runs whatever panics below
	} else {
		defer r.release()
	}
	r.ex, r.rt = ex, rt
	start := 0
	if rt != nil {
		r.ps.N = ps.N // only the wildcards the match filled, not the whole array
		copy(r.ps.Names[:ps.N], ps.Names[:ps.N])
		copy(r.ps.Values[:ps.N], ps.Values[:ps.N])
		start = len(a.mw) + 1
	}
	// The Input reads its header, cookie and plain path and query values out of the copy of the
	// head (Slab.Alias), so the slab needs room only for what is decoded: at most the target, and
	// nothing when the target has no percent escape and no '+'.
	if t := ex.Head().Target; bytes.IndexByte(t, '%') >= 0 || bytes.IndexByte(t, '+') >= 0 {
		ex.AddSlab(len(t))
	}
	// Room for what the first-party middleware of this chain write into the slab, counted at
	// Registration (slabRoom): nothing for a chain without RequestID or a RateLimiter, since every
	// byte a request allocates brings the next collection nearer (ADR 0003).
	room := a.room
	if rt != nil {
		room = rt.room
	}
	ex.AddSlab(room)
	if a.traceOn {
		a.beginTrace(ex)
	}
	r.buildRequest()
	rc := ex.Context()
	rc.SetRequest(&a.info, rt.originFor(r.req.method))
	var ctx context.Context = rc
	if observed {
		ctx = r.observe(ctx)
	}
	r.finish(r.step(ctx, start))
}

// slabRoom is the room in a request's slab that the first-party middleware among mw write into:
// the id RequestID generates (ADR 0032) and the RateLimit header of each RateLimiter (ADR 0055),
// so that neither costs an allocation. Other middleware reserve nothing.
func slabRoom(mw []Middleware) int {
	id, limit := (*requestID)(nil).serve, (*RateLimiter)(nil).serve
	n := 0
	for _, m := range mw {
		switch {
		case sameCode(m, id):
			n += generatedIDLen
		case sameCode(m, limit):
			n += rateLimitRoom
		}
	}
	return n
}

// useOn gives rt its middleware and the slab room they need, and keeps the largest room of any
// route for a request matched inside the app's chain, whose route is not known when its slab is
// sized.
func (a *App) useOn(rt *route, mw []Middleware) {
	rt.mw = mw
	rt.room = slabRoom(mw)
	a.routeRoom = max(a.routeRoom, rt.room)
}

// step runs layer i of the chain: the app's middleware, then the match, then the route's
// middleware, then the route.
func (r *chainRun) step(ctx context.Context, i int) Response {
	na := len(r.a.mw)
	switch {
	case i < na:
		return r.call(r.a.mw[i], ctx, i)
	case i == na:
		return r.dispatch(ctx)
	}
	j := i - na - 1
	if j < len(r.rt.mw) {
		return r.call(r.rt.mw[j], ctx, i)
	}
	r.rt.serve(r.a, r.ex, &r.ps, ctx, &r.req)
	return r.capture()
}

// call runs one middleware. Its error becomes the answer here, so the caller above sees a Response.
func (r *chainRun) call(m Middleware, ctx context.Context, i int) Response {
	res, err := m(ctx, r.req, Next{run: r, gen: r.gen.Load(), step: int32(i + 1)})
	if err != nil {
		r.a.answerError(r.ex, err)
		return r.capture()
	}
	return res
}

// dispatch matches the route inside the app's chain, so a 404, 405 and a preflight are seen by
// the app's middleware.
func (r *chainRun) dispatch(ctx context.Context) Response {
	head := r.ex.Head()
	rt, res, allow := r.a.router.Lookup(head.Method, head.Path, &r.ps)
	if res != router.Found {
		r.a.unmatched(r.ex, res, allow)
		return r.capture()
	}
	r.rt = rt
	r.ex.Context().SetOrigin(rt.originFor(r.req.method))
	if len(rt.mw) == 0 {
		rt.serve(r.a, r.ex, &r.ps, ctx, &r.req)
		return r.capture()
	}
	return r.step(ctx, len(r.a.mw)+1)
}

// capture turns what the engine's buffer holds into the Response value.
func (r *chainRun) capture() Response {
	p := r.ex.Pending()
	return Response{
		kind: kindInner, gen: r.gen.Load(), run: r,
		lo: int32(len(r.hdr)), hi: int32(len(r.hdr)),
		status: int32(p.Status()), size: int32(p.BodyLen()),
		ctype: p.ContentType(), stream: p.Streaming(),
	}
}

// finish writes the Response the outermost layer returned into the engine's buffer.
func (r *chainRun) finish(res Response) {
	ex := r.ex
	st := int(res.status)
	switch res.kind {
	case kindInner:
		if res.gen != r.gen.Load() {
			r.a.log.Error("bites: a middleware returned a Response kept from another request")
			problem(ex, 500, "")
			return
		}
		if st != 0 {
			if badStatus(st) {
				r.a.log.Error("bites: a middleware set a status that is not a status", "status", st)
				problem(ex, 500, "")
				return
			}
			ex.Pending().SetStatus(st)
		}
	case kindFresh, kindProblem:
		if badStatus(st) {
			r.a.log.Error("bites: a middleware returned a Response with a status that is not a status", "status", st)
			problem(ex, 500, "")
			return
		}
		if res.kind == kindProblem {
			problem(ex, st, res.text)
			break
		}
		w := ex.Respond(st)
		if res.ctype != "" {
			w.Header("Content-Type", res.ctype)
		}
		if res.text != "" {
			b := w.Body()
			*b = append(*b, res.text...)
		}
	default:
		r.a.log.Error("bites: a middleware returned the zero Response and no error; return next.Call(ctx), a NewResponse or an error")
		problem(ex, 500, "")
		return
	}
	w := ex.Pending()
	for _, p := range res.pairs() {
		w.Header(p.name, p.value)
	}
}

func badStatus(s int) bool { return s < 100 || s > 599 }

// buildRequest copies the request line and the header block once into the slab (ADR 0022), the
// slab having been sized for it by AddSlab, as one copy of the bytes as received. The path and
// the query are substrings of that copy. Nothing in the Request points at the engine's read buffer.
func (r *chainRun) buildRequest() {
	ex := r.ex
	h := ex.Head()
	slab := ex.Slab()
	raw := ex.RawHead()
	all := slab.Copy(raw)
	slab.Alias(raw, all)
	var headers string
	if i := strings.IndexByte(all, '\n'); i >= 0 {
		headers = all[i+1:]
	}
	r.req = Request{
		run: r, gen: r.gen.Load(),
		method:  internMethod(h.Method, slab),
		path:    slab.Copy(h.Path),
		query:   slab.Copy(h.RawQuery),
		headers: headers,
	}
}

// internMethod gives the method as a string: a constant for the nine standard ones, else a copy
// in slab, or a string of its own when slab is nil.
func internMethod(m []byte, slab *wire.Slab) string {
	switch string(m) {
	case "GET":
		return "GET"
	case "HEAD":
		return "HEAD"
	case "POST":
		return "POST"
	case "PUT":
		return "PUT"
	case "DELETE":
		return "DELETE"
	case "CONNECT":
		return "CONNECT"
	case "OPTIONS":
		return "OPTIONS"
	case "TRACE":
		return "TRACE"
	case "PATCH":
		return "PATCH"
	}
	if slab == nil {
		return string(m)
	}
	return slab.Copy(m)
}
