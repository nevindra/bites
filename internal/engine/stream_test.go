package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/reqctx"
)

type fnRunner struct {
	run  func(w *StreamWriter) error
	fail func(ex *Exchange, err error)
}

func (f fnRunner) Run(w *StreamWriter) error { return f.run(w) }

func (f fnRunner) Fail(ex *Exchange, err error) {
	if f.fail != nil {
		f.fail(ex, err)
		return
	}
	r := ex.Respond(500)
	*r.Body() = append(*r.Body(), "failed: "+err.Error()...)
}

// streaming is a handler that answers 200 and streams with run.
func streaming(run func(w *StreamWriter) error) handlerFunc {
	return func(ex *Exchange) {
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		ex.Stream(false, fnRunner{run: run})
	}
}

// readChunks reads a chunked body to its terminating chunk and returns the size of each chunk and
// the data.
func (c *client) readChunks() (sizes []int, data string, err error) {
	var sb strings.Builder
	for {
		l, err := c.br.ReadString('\n')
		if err != nil {
			return sizes, sb.String(), err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(l), 16, 64)
		if err != nil {
			return sizes, sb.String(), err
		}
		if n == 0 {
			_, err = c.br.ReadString('\n')
			return sizes, sb.String(), err
		}
		b := make([]byte, n+2)
		if _, err := io.ReadFull(c.br, b); err != nil {
			return sizes, sb.String(), err
		}
		if string(b[n:]) != "\r\n" {
			return sizes, sb.String(), errors.New("chunk not ended by CRLF")
		}
		sizes = append(sizes, int(n))
		sb.Write(b[:n])
	}
}

const get11 = "GET /s HTTP/1.1\r\nHost: x\r\n\r\n"

func TestAStreamIsSentChunkedWithItsHeadersAndEndsWithTheTerminatingChunk(t *testing.T) {
	r := newRig(t, streaming(func(w *StreamWriter) error {
		w.WriteString("hello ")
		w.Flush()
		w.WriteString("world")
		return nil
	}), nil)
	c := r.dial()
	c.send(get11)
	rp := c.read()
	if rp.status != 200 || rp.headers["transfer-encoding"] != "chunked" || rp.headers["content-type"] != "text/plain" {
		t.Fatalf("head = %d %v", rp.status, rp.headers)
	}
	if _, has := rp.headers["content-length"]; has {
		t.Fatal("a stream must not carry Content-Length")
	}
	sizes, data, err := c.readChunks()
	if err != nil || data != "hello world" || len(sizes) != 2 {
		t.Fatalf("chunks %v data %q err %v, want two chunks of hello world", sizes, data, err)
	}
}

func TestAConnectionServesAnotherRequestAfterAStreamEnded(t *testing.T) {
	n := 0
	r := newRig(t, func(ex *Exchange) {
		if string(ex.Head().Path) == "/plain" {
			okHandler("plain")(ex)
			return
		}
		n++
		streaming(func(w *StreamWriter) error { w.WriteString("s"); return nil })(ex)
	}, nil)
	c := r.dial()
	for range 2 {
		c.send(get11)
		c.read()
		if _, data, err := c.readChunks(); err != nil || data != "s" {
			t.Fatalf("stream = %q, %v", data, err)
		}
	}
	c.send("GET /plain HTTP/1.1\r\nHost: x\r\n\r\n")
	if rp := c.read(); rp.body != "plain" || rp.headers["connection"] != "" {
		t.Fatalf("after two streams: %+v", rp)
	}
}

func TestAStreamThatWritesNothingIsAHeadAndAnEmptyBody(t *testing.T) {
	r := newRig(t, streaming(func(w *StreamWriter) error { return nil }), nil)
	c := r.dial()
	c.send(get11)
	rp := c.read()
	sizes, data, err := c.readChunks()
	if rp.headers["transfer-encoding"] != "chunked" || len(sizes) != 0 || data != "" || err != nil {
		t.Fatalf("%v %v %q %v", rp.headers, sizes, data, err)
	}
}

func TestAWriteAfterTheStreamFunctionReturnedFailsAndSendsNothing(t *testing.T) {
	var kept *StreamWriter
	r := newRig(t, streaming(func(w *StreamWriter) error {
		kept = w
		w.WriteString("a")
		return nil
	}), nil)
	c := r.dial()
	c.send(get11)
	c.read()
	if _, data, err := c.readChunks(); err != nil || data != "a" {
		t.Fatal(data, err)
	}
	// The connection is back at its next request; the kept writer must not reach it.
	_, err1 := kept.Write([]byte("evil"))
	_, err2 := kept.WriteString("evil")
	err3 := kept.Flush()
	for i, err := range []error{err1, err2, err3} {
		if !errors.Is(err, ErrStreamClosed) {
			t.Errorf("write %d after return: %v, want ErrStreamClosed", i, err)
		}
	}
	c.send("GET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	rp := c.read()
	if strings.Contains(rp.body, "evil") {
		t.Fatalf("a late write reached the connection: %q", rp.body)
	}
}

func TestATenMiBFlushIsSplitIntoChunksOfAtMost64KiB(t *testing.T) {
	const total = 10 << 20
	payload := strings.Repeat("0123456789abcdef", total/16)
	r := newRig(t, streaming(func(w *StreamWriter) error {
		_, err := w.WriteString(payload)
		return err
	}), nil)
	c := r.dial()
	c.send(get11)
	c.read()
	sizes, data, err := c.readChunks()
	if err != nil || data != payload {
		t.Fatalf("data differs or err %v (got %d bytes)", err, len(data))
	}
	for i, n := range sizes {
		if n > StreamChunk {
			t.Fatalf("chunk %d is %d bytes, over %d", i, n, StreamChunk)
		}
	}
	if len(sizes) != total/StreamChunk {
		t.Fatalf("%d chunks, want %d", len(sizes), total/StreamChunk)
	}
}

func TestAReaderThatNeverReadsIsCutByThePerChunkWriteDeadline(t *testing.T) {
	werr := make(chan error, 1)
	r := newRig(t, streaming(func(w *StreamWriter) error {
		// 1 MiB cannot fit in the connection's 64 KiB buffer, so the write blocks on the client.
		_, err := w.WriteString(strings.Repeat("x", 1<<20))
		werr <- err
		return err
	}), nil)
	c := r.dial()
	c.send(get11)
	waitNext(t, r.clk, epoch.Add(30*time.Second))
	r.clk.Advance(30 * time.Second)
	select {
	case err := <-werr:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write error = %v, want a deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write was not cut by the deadline")
	}
	// What was accepted arrives, but never the terminating chunk: the stream is cut, not complete.
	c.read()
	if _, _, err := c.readChunks(); err == nil {
		t.Fatal("the cut stream ended with a terminating chunk")
	}
}

func TestTheWriteDeadlineIsRearmedOnlyWhenLessThanHalfOfTheTimeoutRemains(t *testing.T) {
	step := make(chan struct{})
	ack := make(chan struct{})
	r := newRig(t, streaming(func(w *StreamWriter) error {
		for {
			w.WriteString("e")
			w.Flush()
			ack <- struct{}{}
			if _, ok := <-step; !ok {
				return nil
			}
		}
	}), nil)
	c := r.dial()
	c.send(get11)
	<-ack
	waitNext(t, r.clk, epoch.Add(30*time.Second)) // armed by the first flush
	r.clk.Advance(10 * time.Second)
	step <- struct{}{}
	<-ack
	if n, _ := r.clk.Next(); !n.Equal(epoch.Add(30 * time.Second)) {
		t.Fatalf("with 20 s of 30 s left the deadline moved to %v", n.Sub(epoch))
	}
	r.clk.Advance(10 * time.Second)
	step <- struct{}{}
	<-ack
	waitNext(t, r.clk, epoch.Add(50*time.Second)) // 10 s left, under half: re-armed to now+30 s
	close(step)
	c.read()
	if _, data, err := c.readChunks(); err != nil || data != "eee" {
		t.Fatalf("data %q err %v", data, err)
	}
	// Cleared once at the end: the 50 s deadline is gone, only the idle wait of the connection remains.
	waitFor(t, func() bool { n, ok := r.clk.Next(); return ok && !n.Equal(epoch.Add(50*time.Second)) }, "the write deadline to be cleared")
}

func TestHTTP10GetsNoChunkingAndConnectionCloseAndTheBodyEndsWithTheConnection(t *testing.T) {
	r := newRig(t, streaming(func(w *StreamWriter) error {
		w.WriteString("one,")
		w.Flush()
		w.WriteString("two")
		return nil
	}), nil)
	c := r.dial()
	c.send("GET /s HTTP/1.0\r\nHost: x\r\nConnection: keep-alive\r\n\r\n")
	rp := c.read()
	if _, has := rp.headers["transfer-encoding"]; has || rp.headers["connection"] != "close" {
		t.Fatalf("headers %v: want no chunking and Connection: close", rp.headers)
	}
	b, err := io.ReadAll(c.br)
	if err != nil || string(b) != "one,two" {
		t.Fatalf("body %q err %v", b, err)
	}
}

func TestAPanicAfterTheFirstByteClosesTheConnectionWithoutAnAnswerOrATerminatingChunk(t *testing.T) {
	r := newRig(t, streaming(func(w *StreamWriter) error {
		w.WriteString("partial")
		w.Flush()
		panic("boom")
	}), func(o *Options) { o.Logger = quietLogger() })
	c := r.dial()
	c.send(get11)
	rp := c.read()
	if rp.status != 200 {
		t.Fatalf("status %d", rp.status)
	}
	sizes, data, err := c.readChunks()
	if data != "partial" || len(sizes) != 1 || err == nil {
		t.Fatalf("got %v %q err %v: want the partial chunk and then a closed connection", sizes, data, err)
	}
	c.expectClosed()
}

func TestAPanicBeforeTheFirstByteIsTheOrdinary500(t *testing.T) {
	r := newRig(t, streaming(func(w *StreamWriter) error {
		w.WriteString("buffered, never sent")
		panic("boom")
	}), nil)
	c := r.dial()
	c.send(get11)
	rp := c.read()
	if rp.status != 500 || strings.Contains(rp.body, "buffered") || rp.headers["connection"] != "close" {
		t.Fatalf("%+v", rp)
	}
}

func TestAnErrorBeforeTheFirstByteIsAnsweredAndAfterItClosesTheConnection(t *testing.T) {
	boom := errors.New("boom")
	r := newRig(t, func(ex *Exchange) {
		first := string(ex.Head().Path) == "/early"
		ex.Respond(200)
		ex.Stream(false, fnRunner{run: func(w *StreamWriter) error {
			if first {
				return boom
			}
			w.WriteString("x")
			w.Flush()
			return boom
		}})
	}, nil)
	c := r.dial()
	c.send("GET /early HTTP/1.1\r\nHost: x\r\n\r\n")
	if rp := c.read(); rp.status != 500 || rp.body != "failed: boom" {
		t.Fatalf("%+v", rp)
	}
	c = r.dial()
	c.send("GET /late HTTP/1.1\r\nHost: x\r\n\r\n")
	c.read()
	if _, _, err := c.readChunks(); err == nil {
		t.Fatal("an error after the first byte must not end with a terminating chunk")
	}
}

func TestTheClientLeavingCancelsTheStreamFunctionsContext(t *testing.T) {
	cause := make(chan error, 1)
	started := make(chan struct{})
	r := newRig(t, func(ex *Exchange) {
		ex.Respond(200)
		ex.Stream(false, fnRunner{run: func(w *StreamWriter) error {
			w.WriteString("hi")
			w.Flush()
			close(started)
			<-ex.Context().Done()
			cause <- ex.Context().Cause()
			return ex.Context().Err()
		}})
	}, nil)
	c := r.dial()
	c.send(get11)
	c.read()
	<-started
	c.c.Close()
	select {
	case got := <-cause:
		if got != reqctx.ErrClientGone {
			t.Fatalf("cause %v, want ErrClientGone", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the context was not cancelled when the client left")
	}
}

func TestAHEADRequestGetsTheHeadOfAStreamAndItsFunctionDoesNotRun(t *testing.T) {
	var ran atomic.Bool
	r := newRig(t, streaming(func(w *StreamWriter) error { ran.Store(true); return nil }), nil)
	c := r.dial()
	c.isHead = true
	c.send("HEAD /s HTTP/1.1\r\nHost: x\r\n\r\n")
	rp := c.read()
	if rp.status != 200 || rp.headers["transfer-encoding"] != "chunked" {
		t.Fatalf("%+v", rp)
	}
	c.send("GET /s HTTP/1.1\r\nHost: x\r\n\r\n") // the connection is still in step
	c.isHead = false
	c.read()
	if ran.Load() == false {
		t.Fatal("the GET after the HEAD did not stream")
	}
}

func TestAStreamWithAStatusThatHasNoBodyIsSentAsAPlainAnswer(t *testing.T) {
	var ran atomic.Bool
	r := newRig(t, func(ex *Exchange) {
		ex.Respond(204)
		ex.Stream(false, fnRunner{run: func(w *StreamWriter) error { ran.Store(true); return nil }})
	}, nil)
	c := r.dial()
	c.send(get11)
	rp := c.read()
	if rp.status != 204 || ran.Load() {
		t.Fatalf("status %d, ran %v", rp.status, ran.Load())
	}
}

func TestAStreamAsksTheClosedSocketForNothingOnAnInMemoryConnectionAndSetsTCPNotsentLowatOnRealTCP(t *testing.T) {
	lowat := make(chan int, 1)
	var cc *conn
	run := func(w *StreamWriter) error {
		cc = w.c
		w.WriteString("x")
		w.Flush()
		v, _ := getLowat(w.c.raw())
		lowat <- v
		return nil
	}
	// In memory: no socket, no failure.
	r := newRig(t, streaming(run), nil)
	c := r.dial()
	c.send(get11)
	c.read()
	if _, data, err := c.readChunks(); err != nil || data != "x" {
		t.Fatal(data, err)
	}
	if v := <-lowat; v != -1 {
		t.Fatalf("in memory the option read back %d, want it unsupported", v)
	}
	// Over TCP: 16 KiB while the stream runs, and back to 0 after it for the connection's next answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	srv := New(streaming(run), Options{Logger: quietLogger()})
	go srv.Serve(ln)
	defer srv.Close()
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.Write([]byte(get11))
	buf := make([]byte, 4096)
	var got string
	for !strings.HasSuffix(got, "0\r\n\r\n") {
		n, err := nc.Read(buf)
		if err != nil {
			t.Fatal(err, got)
		}
		got += string(buf[:n])
	}
	if runtime.GOOS != "linux" {
		return // the option is set on Linux only (stream_linux.go)
	}
	if v := <-lowat; v != streamLowat {
		t.Fatalf("TCP_NOTSENT_LOWAT while streaming = %d, want %d", v, streamLowat)
	}
	waitFor(t, func() bool { v, _ := getLowat(cc.raw()); return v == 0 }, "TCP_NOTSENT_LOWAT to be restored after the stream")
}

// sized is a handler that answers 200 and streams n declared bytes with run.
func sized(n int64, run func(w *StreamWriter) error) handlerFunc {
	return func(ex *Exchange) {
		r := ex.Respond(200)
		r.Header("Content-Type", "application/octet-stream")
		ex.StreamSized(n, fnRunner{run: run})
	}
}

func TestASizedStreamIsSentWithContentLengthNotChunkedAndKeepsTheConnection(t *testing.T) {
	data := strings.Repeat("0123456789", 20000) // 200 000 bytes, over three chunks of the writer
	r := newRig(t, sized(int64(len(data)), func(w *StreamWriter) error {
		n, err := w.ReadFrom(strings.NewReader(data))
		if n != int64(len(data)) {
			t.Errorf("ReadFrom read %d", n)
		}
		return err
	}), nil)
	c := r.dial()
	for range 2 {
		c.send(get11)
		rp := c.read()
		if rp.status != 200 || rp.headers["content-length"] != strconv.Itoa(len(data)) || rp.headers["transfer-encoding"] != "" || rp.body != data {
			t.Fatalf("%d %v len=%d", rp.status, rp.headers, len(rp.body))
		}
	}
}

func TestASizedStreamThatWritesLessThanItDeclaredClosesTheConnectionAndOneThatWritesMoreIsRefused(t *testing.T) {
	var overErr error
	r := newRig(t, sized(10, func(w *StreamWriter) error {
		_, err := w.WriteString("0123456789")
		_, overErr = w.WriteString("x")
		return err
	}), nil)
	c := r.dial()
	c.send(get11)
	if rp := c.read(); rp.body != "0123456789" {
		t.Fatalf("%q", rp.body)
	}
	if overErr != ErrStreamOverrun {
		t.Fatalf("a write past the declared length returned %v", overErr)
	}
	short := newRig(t, sized(10, func(w *StreamWriter) error {
		_, err := w.WriteString("01234")
		return err
	}), nil)
	cs := short.dial()
	cs.send(get11)
	cs.expectClosed()
}

func TestAHEADRequestToASizedStreamGetsTheContentLengthAndTheFunctionDoesNotRun(t *testing.T) {
	var ran atomic.Bool
	r := newRig(t, sized(1234, func(w *StreamWriter) error { ran.Store(true); return nil }), nil)
	c := r.dial()
	c.isHead = true
	c.send("HEAD /s HTTP/1.1\r\nHost: x\r\n\r\n")
	rp := c.read()
	if rp.status != 200 || rp.headers["content-length"] != "1234" || ran.Load() {
		t.Fatalf("%d %v ran=%v", rp.status, rp.headers, ran.Load())
	}
}

// ---- Shutdown (ADR 0029) ----

type sseRunner struct{ fnRunner }

func (sseRunner) EndsCleanlyAtShutdown() bool { return true }

// following is a handler that streams "a", then waits for its context and returns its error.
// causes receives the cancel cause once the context is done.
func following(live, sse bool, started chan<- struct{}, causes chan<- error) handlerFunc {
	return func(ex *Exchange) {
		ctx := ex.Context()
		r := ex.Respond(200)
		r.Header("Content-Type", "text/plain")
		run := fnRunner{run: func(w *StreamWriter) error {
			w.WriteString("a")
			w.Flush()
			started <- struct{}{}
			select {
			case <-ctx.Done():
				causes <- ctx.Cause()
				return ctx.Err()
			case <-time.After(5 * time.Second):
				causes <- errors.New("not cancelled")
				return nil
			}
		}}
		if sse {
			ex.Stream(live, sseRunner{run})
		} else {
			ex.Stream(live, run)
		}
	}
}

func TestALiveStreamIsEndedWithErrServerClosedWhenShutdownStarts(t *testing.T) {
	started, causes := make(chan struct{}, 1), make(chan error, 1)
	r := newRig(t, following(true, false, started, causes), nil)
	c := r.dial()
	c.send(get11)
	<-started
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	if cause := <-causes; cause != ErrServerClosed {
		t.Fatalf("cause = %v, want ErrServerClosed", cause)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v: the stream ended, so nothing was left to wait for", err)
	}
	c.read()
	if _, data, err := c.readChunks(); err == nil {
		t.Fatalf("a live stream that is not SSE and was cut ended with a terminating chunk (%q)", data)
	}
}

func TestALiveSSEStreamCutByShutdownEndsWithItsTerminatingChunk(t *testing.T) {
	started, causes := make(chan struct{}, 1), make(chan error, 1)
	r := newRig(t, following(true, true, started, causes), nil)
	c := r.dial()
	c.send(get11)
	<-started
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	if cause := <-causes; cause != ErrServerClosed {
		t.Fatalf("cause = %v", cause)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	c.read()
	if _, data, err := c.readChunks(); err != nil || data != "a" {
		t.Fatalf("data %q, err %v, want a clean end after the first event", data, err)
	}
	c.expectClosed()
}

func TestAPlainStreamIsNotCancelledByShutdownAndShutdownWaitsForIt(t *testing.T) {
	started, causes := make(chan struct{}, 1), make(chan error, 1)
	release := make(chan struct{})
	r := newRig(t, func(ex *Exchange) {
		rp := ex.Respond(200)
		rp.Header("Content-Type", "text/plain")
		ex.Stream(false, fnRunner{run: func(w *StreamWriter) error {
			w.WriteString("a")
			w.Flush()
			started <- struct{}{}
			<-release
			causes <- ex.Context().Cause()
			w.WriteString("b")
			return nil
		}})
	}, nil)
	c := r.dial()
	c.send(get11)
	<-started
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v while a plain stream was running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if cause := <-causes; cause != nil {
		t.Fatalf("a plain stream's context was cancelled: %v", cause)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
	c.read()
	if _, data, err := c.readChunks(); err != nil || data != "ab" {
		t.Fatalf("data %q, err %v, want the whole body", data, err)
	}
}

func TestALiveStreamThatStartsDuringShutdownIsCancelledToo(t *testing.T) {
	// The request is in flight (stActive, no stream yet) when Shutdown walks, so only the stream's
	// own read of the closing flag can cancel it.
	entered, release := make(chan struct{}, 1), make(chan struct{})
	started, causes := make(chan struct{}, 1), make(chan error, 1)
	inner := following(true, false, started, causes)
	r := newRig(t, func(ex *Exchange) {
		entered <- struct{}{}
		<-release
		inner(ex)
	}, nil)
	c := r.dial()
	c.send(get11)
	<-entered
	done := make(chan error, 1)
	go func() { done <- r.srv.Shutdown(context.Background()) }()
	waitFor(t, func() bool { return r.srv.shutting.Load() }, "Shutdown to start")
	close(release)
	if cause := <-causes; cause != ErrServerClosed {
		t.Fatalf("cause = %v, want ErrServerClosed", cause)
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown returned %v", err)
	}
}

func TestAPlainRequestNeverTakesTheLiveStateOfAConnection(t *testing.T) {
	var st atomic.Int32
	r := newRig(t, func(ex *Exchange) {
		st.Store(ex.c.state.Load())
		okHandler("x")(ex)
	}, nil)
	c := r.dial()
	c.send(get11)
	c.read()
	if st.Load() != stActive {
		t.Fatalf("a plain request ran in state %d, want stActive", st.Load())
	}
}
