package bites

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/engine"
	"github.com/nevindra/bites/internal/jsoncodec"
)

// StreamWriter is the writer a Stream's function receives (ADR 0030). It is valid only while that
// function runs: once the function has returned, Write, WriteString and Flush fail with
// ErrStreamClosed and touch nothing, so a goroutine that kept the writer cannot write into
// whatever connection owns it next (ADR 0008). It is safe for concurrent use.
//
// Writes are buffered up to 64 KiB and sent as one chunk when the buffer fills, on Flush, and
// when the function returns. Nothing is sent before the first of those, not even the headers, so
// an error returned or a panic before it is still an ordinary 500. A write that fails (the client
// left, or took longer than the write timeout for one chunk) fails every later write too; the
// connection is closed when the function returns.
//
// Its methods (go doc does not list a method through an alias, so they are here):
//
//	Write(p []byte) (int, error)              buffers p, as io.Writer
//	WriteString(s string) (int, error)        buffers s, as io.StringWriter
//	ReadFrom(r io.Reader) (int64, error)      reads r into the buffer until it ends (at most the declared Length), as io.ReaderFrom
//	Flush() error                             sends what is buffered now, as one chunk
//	Live() bool                               whether the Stream was declared Live (a feed Shutdown cancels)
type StreamWriter = engine.StreamWriter

// ErrStreamClosed is returned by a StreamWriter after its function has returned.
var ErrStreamClosed = engine.ErrStreamClosed

// Stream is a response whose body is written over time (ADR 0030): a large download, NDJSON, a
// feed. The handler returns it with the status and headers fixed, and Func writes the body. The
// headers are fixed before the first byte, so they cannot be set late (ADR 0009).
//
// Func runs after the handler and after every middleware has returned, on the connection's
// goroutine, with the request's context, which is cancelled if the client leaves (ADR 0016). It
// ends the response by returning: nil ends it cleanly (the terminating chunk), and an error
// returned after the first byte, or a panic, closes the connection with no terminating chunk, so
// a client cannot mistake a cut stream for a complete one. An error or a panic before the first
// byte is the ordinary answer to an error or a panic.
//
// The body is chunked on HTTP/1.1; an HTTP/1.0 client gets no chunking and the body ends when the
// connection closes, with Connection: close. A HEAD request gets the head and no body, and Func
// does not run. Middleware sees a Stream as a Response with Streaming() true and Size() 0.
//
// Status is 200 when zero and must be a status that has a body: 1xx, 204 and 304 are a 500. A
// Func that is nil is a 500, logged. A Content-Type in Header is used; without one the stream is
// application/octet-stream.
type Stream struct {
	// Status is the response status; zero means 200. It must have a body, so 1xx, 204 and 304 are a 500.
	Status int
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
	// Header holds the response headers; a Content-Type in it is used, else application/octet-stream.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
	// Live declares a stream with no end of its own (an NDJSON feed): Shutdown cancels its context
	// with ErrServerClosed instead of waiting for it (ADR 0029, ADR 0030). SSE is live by
	// construction. A stream that is not Live (a download) is drained.
	Live bool
	// Func writes the body; it runs after the handler and its middleware return, and a nil Func is a 500.
	Func func(ctx context.Context, w *StreamWriter) error
}

// SSE is a Server-Sent Events response (ADR 0030): a Stream, live by construction, with the
// content type text/event-stream and Cache-Control: no-cache unless Header says otherwise, whose
// function writes events and not bytes. Its status is 200.
type SSE struct {
	// Header holds the response headers, with text/event-stream unless it says otherwise.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
	// Func writes the events; returning ends the stream.
	Func func(ctx context.Context, e EventWriter) error
}

// Event is one Server-Sent Event. Data may hold line breaks: each line is sent as its own data
// field and the browser joins them again. An event with an empty Data and a Name or an ID still
// sends one empty data field, which dispatches it; an event with only a Retry sends no data and
// dispatches nothing.
type Event struct {
	// ID becomes the Last-Event-ID the browser sends when it reconnects.
	ID string
	// Name is the event type; the browser's default, "message", when empty.
	Name string
	// Data is the text of the event, sent as one data field per line.
	Data string
	// Retry is how long the browser waits before reconnecting, in whole milliseconds; zero sends
	// nothing.
	Retry time.Duration
}

// ErrBadEvent is returned by EventWriter.Send, and by an EventSender, for an ID or Name with a
// line break (or a NUL in the ID), which would end the field and start another one of the
// sender's choosing. EventSender.SendEvent returns it wrapped for an Event that has Data of its
// own, since a typed event's data is the value sent.
var ErrBadEvent = errors.New("bites: an event id or name contains a line break")

// errEventData is what EventSender.SendEvent returns for an Event with its own Data: an
// ErrBadEvent to errors.Is, with a message that says which mistake it is.
var errEventData error = eventDataError{}

type eventDataError struct{}

func (eventDataError) Error() string {
	return "bites: an Event given to SendEvent has Data; a typed event's data is the value sent"
}

func (eventDataError) Is(target error) bool { return target == ErrBadEvent }

// EventWriter writes the events of an SSE response. It is valid only while the SSE function runs,
// like the StreamWriter under it. Each Send and Comment goes out at once.
type EventWriter struct{ w *StreamWriter }

// Send writes ev and flushes it. It fails with ErrBadEvent for an ID or Name that cannot be one,
// and with the writer's error once the client is gone or too slow.
func (e EventWriter) Send(ev Event) error {
	if e.w == nil {
		return ErrStreamClosed
	}
	if badEventHead(ev.ID, ev.Name) {
		return ErrBadEvent
	}
	var arr [1024]byte // an event this size or smaller allocates nothing
	b := appendEventHead(arr[:0], ev.ID, ev.Name, ev.Retry)
	if ev.Data != "" || ev.ID != "" || ev.Name != "" || ev.Retry <= 0 {
		b = appendLines(b, "data: ", ev.Data)
	}
	b = append(b, '\n')
	return e.send(b)
}

// badEventHead reports an ID or a name that would end its field early.
func badEventHead(id, name string) bool {
	return strings.ContainsAny(name, "\r\n") || strings.ContainsAny(id, "\r\n\x00")
}

// appendEventHead appends the id, event and retry fields an event has, in that order.
func appendEventHead(b []byte, id, name string, retry time.Duration) []byte {
	if id != "" {
		b = append(append(append(b, "id: "...), id...), '\n')
	}
	if name != "" {
		b = append(append(append(b, "event: "...), name...), '\n')
	}
	if ms := retry.Milliseconds(); ms > 0 {
		b = strconv.AppendInt(append(b, "retry: "...), ms, 10)
		b = append(b, '\n')
	}
	return b
}

// Comment writes a comment line, which the browser ignores: the heartbeat that keeps a proxy
// from closing an idle stream. Line breaks in text start further comment lines.
func (e EventWriter) Comment(text string) error {
	if e.w == nil {
		return ErrStreamClosed
	}
	var arr [256]byte
	b := appendLines(arr[:0], ": ", text)
	return e.send(append(b, '\n'))
}

func (e EventWriter) send(b []byte) error {
	if _, err := e.w.Write(b); err != nil {
		return err
	}
	return e.w.Flush()
}

// appendLines appends s as one line per line of s, each starting with prefix: the line breaks of
// the SSE specification are LF, CR and CRLF. An empty s is one empty line.
func appendLines(b []byte, prefix, s string) []byte {
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			return append(append(append(b, prefix...), s...), '\n')
		}
		b = append(append(append(b, prefix...), s[:i]...), '\n')
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		s = s[i+1:]
	}
}

// Events is a Server-Sent Events response whose events carry a T, written as JSON (ADR 0030): an
// SSE whose function sends values and not strings. T is known at Registration, so its encoder is
// compiled there with the framework's codec, a T that cannot be written as JSON is refused there,
// and the API document states T's schema as the data of every event. Like an SSE it is live, it is
// text/event-stream with Cache-Control: no-cache unless Header says otherwise, and its status is
// 200. A Func that is nil is a 500, logged.
type Events[T any] struct {
	// Header holds the response headers, with text/event-stream unless it says otherwise.
	Header Header
	// Cookies are the cookies the response sets.
	Cookies []Cookie
	// Func sends the values; returning ends the stream, and a nil Func is a 500, logged.
	Func func(ctx context.Context, e EventSender[T]) error
}

// EventSender sends the events of an Events response. It is valid only while the Events function
// runs, like the StreamWriter under it, and it is safe for concurrent use. Each Send, SendEvent
// and Comment goes out at once.
type EventSender[T any] struct {
	w *StreamWriter
	s *eventScratch
}

// Send writes v as the data of one event of type name ("" is the browser's default, "message")
// and flushes it. v is one line of JSON, so it is one data field. It fails with ErrBadEvent for a
// name with a line break, with the encoder's error for a value JSON cannot hold (a NaN), and with
// the writer's error once the client is gone or too slow.
func (e EventSender[T]) Send(name string, v T) error {
	return e.s.send(e.w, "", name, 0, noescapeP(unsafe.Pointer(&v)))
}

// SendEvent is Send with the ID, Name and Retry of ev: the ID becomes the Last-Event-ID the browser
// sends when it reconnects. ev.Data must be empty, since the data is v; an Event with Data is an
// ErrBadEvent and nothing is sent.
func (e EventSender[T]) SendEvent(ev Event, v T) error {
	if ev.Data != "" {
		return errEventData
	}
	return e.s.send(e.w, ev.ID, ev.Name, ev.Retry, noescapeP(unsafe.Pointer(&v)))
}

// Comment writes a comment line, which the browser ignores, and flushes it: the heartbeat that
// keeps a proxy from closing an idle stream, as EventWriter.Comment.
func (e EventSender[T]) Comment(text string) error {
	if e.s == nil {
		return ErrStreamClosed
	}
	return EventWriter{e.w}.Comment(text)
}

const (
	// eventScratchSize is the buffer an Events stream encodes its events in, inside the runner,
	// so an event of this size or smaller allocates nothing.
	eventScratchSize = 512
	// eventScratchKept is the largest buffer a stream keeps after a large event.
	eventScratchKept = 64 << 10
)

// eventScratch is the per-stream state of an EventSender: the encoder of T and the buffer an event
// is built in, which the lock keeps to one event at a time. It lives in the stream's runner and is
// never pooled, so a sender kept past its function touches nothing another stream owns (ADR 0008).
type eventScratch struct {
	mu  sync.Mutex
	enc *jsoncodec.Encoder
	buf []byte
	arr [eventScratchSize]byte
}

func (s *eventScratch) send(w *StreamWriter, id, name string, retry time.Duration, v unsafe.Pointer) error {
	if s == nil || w == nil {
		return ErrStreamClosed
	}
	if badEventHead(id, name) {
		return ErrBadEvent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := append(appendEventHead(s.buf[:0], id, name, retry), "data: "...)
	start := len(b)
	b, err := s.enc.Append(b, v)
	if err != nil {
		return err
	}
	// JSON has a raw line break only as whitespace between tokens (one inside a string is
	// escaped), so a MarshalJSON that indents becomes one line with the same value.
	for i := start; i < len(b); i++ {
		if b[i] == '\n' || b[i] == '\r' {
			b[i] = ' '
		}
	}
	b = append(b, '\n', '\n')
	if cap(b) <= eventScratchKept {
		s.buf = b[:0]
	} else {
		s.buf = s.arr[:0]
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.Flush()
}

// eventsType is how Registration reaches the T of an Events[T] it holds only as a reflect.Type: a
// zero Events[T] boxed once at Registration answers for every response of the route.
type eventsType interface {
	eventType() reflect.Type
	newRun(a *App, ctx context.Context, src unsafe.Pointer, enc *jsoncodec.Encoder) engine.StreamRunner
}

func (Events[T]) eventType() reflect.Type { return reflect.TypeFor[T]() }

// newRun builds the runner of the Events at src, nil when its Func is nil. The runner is the one
// allocation the stream makes beyond a request's own and the writer, as streamRun is for an SSE.
func (Events[T]) newRun(a *App, ctx context.Context, src unsafe.Pointer, enc *jsoncodec.Encoder) engine.StreamRunner {
	fn := (*Events[T])(src).Func
	if fn == nil {
		return nil
	}
	r := &eventsRun[T]{a: a, ctx: ctx, fn: fn}
	r.s.enc = enc
	r.s.buf = r.s.arr[:0]
	return r
}

type eventsRun[T any] struct {
	a   *App
	ctx context.Context
	fn  func(context.Context, EventSender[T]) error
	s   eventScratch
}

func (r *eventsRun[T]) Run(w *StreamWriter) error {
	return r.fn(r.ctx, EventSender[T]{w: w, s: &r.s})
}

func (r *eventsRun[T]) Fail(ex *engine.Exchange, err error) { r.a.answerError(ex, err) }

// EndsCleanlyAtShutdown: an Events stream ends with its terminating chunk when Shutdown cut it, as
// an SSE does (EventSource reconnects either way).
func (r *eventsRun[T]) EndsCleanlyAtShutdown() bool { return true }

func isStreamType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == ownPkg && (t.Name() == "Stream" || t.Name() == "SSE" || strings.HasPrefix(t.Name(), "Events["))
}

// streamLabel names a stream type in a refusal: Events[Tick] rather than the instance's name,
// which spells out the package path of its argument.
func streamLabel(t reflect.Type) string {
	if et, ok := reflect.Zero(t).Interface().(eventsType); ok {
		return "Events[" + eventLabel(et.eventType()) + "]"
	}
	return t.Name()
}

// eventLabel is typeLabel, with an Events named by streamLabel.
func eventLabel(t reflect.Type) string {
	if isStreamType(t) {
		return streamLabel(t)
	}
	return typeLabel(t)
}

// isResponseType reports one of the framework's response types: a status wrapper, a File or a
// stream.
func isResponseType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.PkgPath() != ownPkg {
		return false
	}
	for _, w := range wrapperNames {
		if t.Name() == w.prefix || strings.HasSuffix(w.prefix, "[") && strings.HasPrefix(t.Name(), w.prefix) {
			return true
		}
	}
	return false
}

// compileEvents reads the T of an Events[T] (ADR 0030): any type that can be written as JSON,
// except []byte, which JSON writes as base64, and the framework's response types, which have a
// status and headers an event cannot carry.
func (a *App) compileEvents(p *outPlan, t reflect.Type) error {
	et := reflect.Zero(t).Interface().(eventsType)
	vt := et.eventType()
	switch {
	case vt == bytesType:
		return refusalf("the handler returns Events[[]byte]; an event's data is text, and []byte would be sent as base64 JSON: send bytes as text with an SSE and its EventWriter")
	case isResponseType(vt):
		return refusalf("the handler returns Events[%s]; %s is one of the framework's response types, and an event's data is a plain value, written as JSON, with no status or headers", eventLabel(vt), eventLabel(vt))
	}
	e, err := jsoncodec.CompileEncoder(vt, jsoncodec.Options{Names: a.naming, Formats: a.formats, JSONSchemas: a.jsonSchemas})
	if err != nil {
		return refusalf("the handler returns Events[%s], whose event type cannot be written as JSON: %v", eventLabel(vt), err)
	}
	p.events, p.enc = et, e
	return nil
}

// streamRun is what the engine runs after the handler and the middleware have returned. It is the
// one allocation a stream makes beyond a request's own, besides the writer.
type streamRun struct {
	a   *App
	ctx context.Context
	fn  func(context.Context, *StreamWriter) error
	sse func(context.Context, EventWriter) error
}

func (s *streamRun) Run(w *StreamWriter) error {
	if s.sse != nil {
		return s.sse(s.ctx, EventWriter{w})
	}
	return s.fn(s.ctx, w)
}

// EndsCleanlyAtShutdown is the engine's question when Shutdown cut this stream: an SSE ends with
// its terminating chunk (EventSource reconnects either way), any other live stream does not.
func (s *streamRun) EndsCleanlyAtShutdown() bool { return s.sse != nil }

func (s *streamRun) Fail(ex *engine.Exchange, err error) { s.a.answerError(ex, err) }

// writeStream builds the head of a Stream or SSE and hands the engine the function to run.
func (p *outPlan) writeStream(a *App, ex *engine.Exchange, src unsafe.Pointer, ctx context.Context) {
	status := 200
	sse := p.wk >= wrapSSE // an SSE or an Events
	var run engine.StreamRunner
	switch p.wk {
	case wrapEvents:
		if run = p.events.newRun(a, ctx, src, p.enc); run == nil {
			a.internalError(ex, refusalf("an Events has no Func"))
			return
		}
	case wrapSSE:
		r := &streamRun{a: a, ctx: ctx}
		r.sse = *(*func(context.Context, EventWriter) error)(unsafe.Add(src, p.funcOff))
		if r.sse == nil {
			a.internalError(ex, refusalf("an SSE has no Func"))
			return
		}
		run = r
	default:
		r := &streamRun{a: a, ctx: ctx}
		r.fn = *(*func(context.Context, *StreamWriter) error)(unsafe.Add(src, p.funcOff))
		run = r
		if r.fn == nil {
			a.internalError(ex, refusalf("a Stream has no Func"))
			return
		}
		if st := *(*int)(unsafe.Add(src, p.statOff)); st != 0 {
			status = st
		}
		if status < 200 || status > 599 || status == 204 || status == 304 {
			a.internalError(ex, refusalf("a Stream has status %d; it must be a status that has a body, 200 to 599 except 204 and 304", status))
			return
		}
	}
	r := ex.Respond(status)
	needType := true
	if err := writeHeaders(r, *(*Header)(unsafe.Add(src, p.hdrOff)), &needType); err != nil {
		a.internalError(ex, err)
		return
	}
	if p.hasAtt {
		writeAttachment(r, *(*Header)(unsafe.Add(src, p.hdrOff)), *(*string)(unsafe.Add(src, p.attOff)))
	}
	if needType {
		if sse {
			r.Header("Content-Type", "text/event-stream")
		} else {
			r.Header("Content-Type", "application/octet-stream")
		}
	}
	if sse && !hasHeader(*(*Header)(unsafe.Add(src, p.hdrOff)), "Cache-Control") {
		r.Header("Cache-Control", "no-cache")
	}
	for _, c := range *(*[]Cookie)(unsafe.Add(src, p.cookOff)) {
		v, err := appendSetCookie(nil, &c)
		if err != nil {
			a.internalError(ex, err)
			return
		}
		r.Header("Set-Cookie", string(v))
	}
	live := sse || *(*bool)(unsafe.Add(src, p.liveOff))
	ex.Stream(live, run)
}

func hasHeader(h Header, name string) bool {
	for n := range h {
		if foldEq(n, name) {
			return true
		}
	}
	return false
}
