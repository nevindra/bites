package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/nevindra/bites/internal/reqctx"
)

// A streamed response (ADR 0030). The framework builds the status line and headers in the
// Response as for any answer and hands the engine a StreamRunner with Exchange.Stream; the
// function runs after Serve returns, on the connection's goroutine, so every middleware has
// decorated the head by then and the head is fixed before the first byte. The request's context
// stays live until the function has returned.

const (
	// StreamChunk is the most data one chunk carries, and so the most one Write to the socket
	// moves under one deadline: a Go deadline covers the whole Write, and 64 KiB at the default
	// 30 s needs 2.2 KiB/s of the client where 10 MiB would need 340 KiB/s (ADR 0030).
	StreamChunk = 64 << 10

	streamLowat = 16 << 10 // TCP_NOTSENT_LOWAT while a stream runs
	// streamReserve is the room kept in front of a chunk for "XXXXX\r\n", the chunk size in hex.
	streamReserve = 8
	terminator    = "0\r\n\r\n"
)

// ErrStreamClosed is what a write to a StreamWriter returns once the function it was given to has
// returned (ADR 0008): the writer is the connection's, and the connection moves on.
var ErrStreamClosed = errors.New("bites: write to a stream after its function returned")

// ErrStreamOverrun is what a write to a sized stream returns when it would pass the length the
// stream declared.
var ErrStreamOverrun = errors.New("bites: write past the length a sized stream declared")

// StreamRunner is what the framework gives the engine to run a stream. Both methods are called
// on the connection's goroutine, after Serve has returned.
type StreamRunner interface {
	// Run is the user's function. w is valid until Run returns.
	Run(w *StreamWriter) error
	// Fail answers err when Run failed before sending a byte; it must build a response with
	// Exchange.Respond, as a handler's error does.
	Fail(ex *Exchange, err error)
}

// Stream marks the response begun with Respond as a stream: its body is written by run after
// Serve returns, with chunked transfer encoding (HTTP/1.1) or until the connection closes
// (HTTP/1.0). live is the flag of ADR 0030: Shutdown cancels the context of a live stream with
// ErrServerClosed (ADR 0029). A runner that also has EndsCleanlyAtShutdown() bool, true for an SSE,
// gets the terminating chunk when its function returns an error after being cut that way.
func (ex *Exchange) Stream(live bool, run StreamRunner) {
	r := &ex.c.sc.resp
	r.stream, r.live = run, live
}

// StreamSized is Stream for a body whose length is known before the first byte (a File, ADR 0043):
// the head carries Content-Length n, the body is not chunked, and the connection stays open when
// the function wrote exactly n bytes. A function that writes more is refused with
// ErrStreamOverrun, and one that writes fewer closes the connection, as a cut stream does. A HEAD
// request gets the head with the same Content-Length and no body, and the function does not run.
func (ex *Exchange) StreamSized(n int64, run StreamRunner) {
	r := &ex.c.sc.resp
	r.stream, r.live, r.sized, r.length = run, false, true, n
}

// Streaming reports whether the response is a stream, whose size is not known.
func (r *Response) Streaming() bool { return r.stream != nil }

// Live reports whether the response is a stream declared live.
func (r *Response) Live() bool { return r.stream != nil && r.live }

// StreamWriter is the writer of a stream. It exists for the length of the stream's function and
// is safe for concurrent use. Writes are buffered up to 64 KiB, sent as one chunk when the buffer
// fills, on Flush, and when the function returns; a write after the function returned fails with
// ErrStreamClosed and touches nothing (ADR 0008). Nothing is sent, not even the headers, until
// the first Flush, a full buffer or the function's return, so a panic or an error before that is
// still an ordinary 500.
type StreamWriter struct {
	mu       sync.Mutex
	c        *conn // nil once the function has returned
	buf      []byte
	err      error // the first failed write, kept
	started  bool  // the head has been sent
	chunked  bool
	keep     bool
	live     bool
	deadline time.Time
	lowat    bool
	sized    bool  // Content-Length instead of chunks
	length   int64 // the declared length, when sized
	accepted int64 // bytes accepted so far, when sized
	sent     int64 // body bytes that reached the socket, without the chunk framing (ADR 0051)
}

// Write buffers p. It returns the number of bytes accepted, and an error once any write of the
// stream has failed (the client is gone or too slow).
func (w *StreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.c == nil {
		return 0, ErrStreamClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.sized && w.accepted+int64(len(p)) > w.length {
		return 0, ErrStreamOverrun
	}
	w.accepted += int64(len(p))
	n := 0
	for len(p) > 0 {
		k := min(StreamChunk-(len(w.buf)-streamReserve), len(p))
		w.buf = append(w.buf, p[:k]...)
		p, n = p[k:], n+k
		if len(w.buf)-streamReserve >= StreamChunk {
			if err := w.sendLocked(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// WriteString is Write for a string.
func (w *StreamWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.c == nil {
		return 0, ErrStreamClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.sized && w.accepted+int64(len(s)) > w.length {
		return 0, ErrStreamOverrun
	}
	w.accepted += int64(len(s))
	n := 0
	for len(s) > 0 {
		k := min(StreamChunk-(len(w.buf)-streamReserve), len(s))
		w.buf = append(w.buf, s[:k]...)
		s, n = s[k:], n+k
		if len(w.buf)-streamReserve >= StreamChunk {
			if err := w.sendLocked(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// ReadFrom reads r into the writer's buffer until r ends, so io.Copy into a StreamWriter does not
// copy twice. On a sized stream it reads at most what is left of the declared length and then
// returns, without waiting to see r end.
func (w *StreamWriter) ReadFrom(r io.Reader) (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.c == nil {
		return 0, ErrStreamClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	var total int64
	for {
		space := StreamChunk - (len(w.buf) - streamReserve)
		if w.sized {
			rest := w.length - w.accepted
			if rest <= 0 {
				return total, nil
			}
			space = int(min(int64(space), rest))
		}
		w.buf = slices.Grow(w.buf, space)
		n, err := r.Read(w.buf[len(w.buf) : len(w.buf)+space])
		w.buf = w.buf[:len(w.buf)+n]
		total += int64(n)
		w.accepted += int64(n)
		if len(w.buf)-streamReserve >= StreamChunk {
			if serr := w.sendLocked(false); serr != nil {
				return total, serr
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Flush sends what is buffered, and the headers if nothing has been sent yet. It is how a
// stream puts an event on the wire.
func (w *StreamWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.c == nil {
		return ErrStreamClosed
	}
	if w.err != nil {
		return w.err
	}
	return w.sendLocked(false)
}

// Live reports whether the stream was declared live.
func (w *StreamWriter) Live() bool { return w.live }

// sendLocked sends the buffered data (at most StreamChunk) as one chunk, with the head before it
// if the head has not gone. final also appends the terminating chunk. Callers hold w.mu and have
// checked w.err.
func (w *StreamWriter) sendLocked(final bool) error {
	data := len(w.buf) - streamReserve
	if data == 0 && w.started && !final {
		return nil
	}
	c := w.c
	start := streamReserve
	if w.chunked && data > 0 {
		w.buf = append(w.buf, '\r', '\n')
		i := streamReserve - 2
		w.buf[i], w.buf[i+1] = '\r', '\n'
		for n := data; ; n >>= 4 {
			i--
			w.buf[i] = "0123456789abcdef"[n&15]
			if n < 16 {
				break
			}
		}
		start = i
	}
	if final && w.chunked {
		w.buf = append(w.buf, terminator...)
	}
	frame := w.buf[start:]
	s := c.srv
	var head []byte
	if !w.started {
		w.started = true
		head = c.streamHead(w.chunked, w.keep, w.sized, w.length)
		w.lowat = setNotsentLowat(c.raw(), streamLowat)
	} else if len(frame) == 0 {
		w.buf = w.buf[:streamReserve]
		return nil
	}
	if now := s.now(); w.deadline.IsZero() || w.deadline.Sub(now) < s.o.WriteTimeout/2 {
		w.deadline = now.Add(s.o.WriteTimeout)
		c.setWriteDeadline(w.deadline)
	}
	var err error
	switch {
	case head == nil:
		_, err = c.nc.Write(frame)
	case len(head)+len(frame) <= 32<<10:
		head = append(head, frame...)
		_, err = c.nc.Write(head)
	default:
		if _, err = c.nc.Write(head); err == nil {
			_, err = c.nc.Write(frame)
		}
	}
	if head != nil {
		c.sc.resp.head = head[:0]
	}
	w.buf = w.buf[:streamReserve]
	if err != nil {
		w.err = err
	} else {
		w.sent += int64(data)
	}
	return err
}

// streamHead completes the head of a stream's response (the status line and headers the
// framework and the middleware built) and returns it, to be sent with the first chunk.
func (c *conn) streamHead(chunked, keep, sized bool, length int64) []byte {
	head := c.sc.resp.head
	if len(c.srv.coders) > 0 && !scanHead(head).varyCovers {
		head = append(head, varyLine...) // a stream is never compressed, and a cache must still know the choice was made (ADR 0031)
	}
	head = c.sc.resp.appendLate(head)
	head = append(head, c.srv.dateLine(c.srv.now())...)
	if chunked {
		head = append(head, "Transfer-Encoding: chunked\r\n"...)
	}
	if sized {
		head = append(head, "Content-Length: "...)
		head = appendInt(head, int(length))
		head = append(head, '\r', '\n')
	}
	if !keep {
		head = append(head, "Connection: close\r\n"...)
	}
	return append(head, '\r', '\n')
}

// close ends the writer: when clean it sends what is buffered and the terminating chunk; it then
// gives the buffer back to the response and makes every later write fail. It reports whether the
// response reached the client whole.
func (w *StreamWriter) close(clean bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.c
	if c == nil {
		return false
	}
	ok := clean && w.err == nil && (!w.sized || w.accepted == w.length)
	if ok {
		ok = w.sendLocked(true) == nil
	}
	if w.started {
		c.setWriteDeadline(time.Time{}) // cleared once, when the stream ends
		if w.lowat {
			setNotsentLowat(c.raw(), 0) // a kept-alive connection's plain answers are as before
		}
	}
	c.sc.resp.body = w.buf[:0]
	c.ex.wrote, c.ex.writeFailed = w.sent, w.err != nil
	w.c, w.buf = nil, nil
	return ok
}

// What runStream decided.
const (
	streamFallback = iota // nothing was sent: write the response in the buffer as usual
	streamAbort           // bytes went out and the stream did not end cleanly: close the connection
	streamKeep            // the stream ended cleanly and the connection continues
	streamClose           // the stream ended cleanly and the connection closes
)

// runStream sends a stream's response and runs its function. The connection's goroutine is
// where the stream waits (ADR 0005): a stream waits for its client in no other place, and a
// connection running a stream is not idle, so it never parks.
func (c *conn) runStream(f *fault) (outcome int, panicked bool) {
	s := c.srv
	ex := &c.ex
	r := &c.sc.resp
	req := &c.sc.req
	run, live := r.stream, r.live
	sized, length := r.sized, r.length
	r.stream, r.live, r.sized = nil, false, false
	if r.status < 200 || r.status == 204 || r.status == 304 {
		return streamFallback, false // such an answer has no body to stream
	}
	chunked := req.Minor >= 1 && !sized
	// A request body not read is not drained for a stream, and HTTP/1.0 ends the body by closing,
	// except for a sized one, which says where it ends.
	keep := req.KeepAlive && req.Minor >= 1 && !ex.closeAfter && !s.shutting.Load() && ex.bodyState != bodyUnread
	if bytes.Equal(req.Method, headMethod) {
		head := c.streamHead(chunked, keep, sized, length)
		c.setWriteDeadline(s.now().Add(s.o.WriteTimeout))
		_, err := c.nc.Write(head)
		c.setWriteDeadline(time.Time{})
		r.head = head[:0]
		c.ex.writeFailed = err != nil
		return endOf(err == nil, keep), false
	}
	w := &StreamWriter{c: c, chunked: chunked, keep: keep, live: live, sized: sized, length: length}
	w.buf = r.body[:0]
	if cap(w.buf) < streamReserve {
		w.buf = make([]byte, 0, 512)
	}
	w.buf = w.buf[:streamReserve]
	// A live stream is findable by Shutdown (ADR 0029): the state is set and then the closing flag
	// is read, and Shutdown sets the flag and then walks the connections, so a stream that starts
	// while Shutdown does is cancelled by one side or the other (or both, which is harmless). A
	// plain stream stores nothing.
	if live {
		c.state.Store(stLive)
		if s.shutting.Load() {
			ex.ctx.Cancel(ErrServerClosed)
		}
	}
	err, panicked := c.callStream(run, w, f)
	if live {
		c.state.Store(stActive)
	}
	// A live SSE stream cut by Shutdown ends cleanly, with its terminating chunk: EventSource
	// reconnects either way, and the client is told the server ended it. Any other live stream
	// that returns an error ends like a cut stream, with no terminating chunk, so a client cannot
	// take it for a complete body. A panic is still a panic.
	if live && !panicked && err != nil && ex.ctx.Cause() == ErrServerClosed {
		if e, ok := run.(interface{ EndsCleanlyAtShutdown() bool }); ok && e.EndsCleanlyAtShutdown() {
			w.mu.Lock()
			werr := w.err
			if !w.started {
				w.keep = false
			}
			w.mu.Unlock()
			if werr == nil {
				return endOf(w.close(true), false), false
			}
		}
	}
	w.mu.Lock()
	started, werr := w.started, w.err
	w.mu.Unlock()
	switch {
	case panicked:
		w.close(false)
		if !started {
			return streamFallback, true
		}
		return streamAbort, true
	case err != nil && !started:
		w.close(false)
		run.Fail(ex, err)
		return streamFallback, false
	case err != nil:
		if ex.ctx.Cause() != reqctx.ErrClientGone && !errors.Is(err, context.Canceled) {
			f.err = err
		}
		if werr == nil && ex.ctx.Cause() != reqctx.ErrClientGone && !errors.Is(err, context.Canceled) {
			s.o.Logger.Error("a stream function returned an error after its first byte; the connection is closed",
				"err", err, "method", string(req.Method), "target", string(req.Target), "remote", c.nc.RemoteAddr().String())
		}
		w.close(false)
		return streamAbort, false
	}
	return endOf(w.close(true), keep), false
}

func endOf(ok, keep bool) int {
	switch {
	case !ok:
		return streamAbort
	case keep:
		return streamKeep
	}
	return streamClose
}

// callStream runs the user's function with the engine's recovery around it (ADR 0028).
func (c *conn) callStream(run StreamRunner, w *StreamWriter, f *fault) (err error, panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			panicked = true
			st := debug.Stack()
			f.val, f.stack = v, st
			req := &c.sc.req
			c.srv.o.Logger.Error("panic in a stream function", "panic", v, "stack", string(st),
				"method", string(req.Method), "target", string(req.Target), "remote", c.nc.RemoteAddr().String())
		}
	}()
	return run.Run(w), false
}
