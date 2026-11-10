package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/h1"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/reqctx"
)

const get = "GET / HTTP/1.1\r\nHost: x\r\n\r\n"

func TestSeveralRequestsShareOneKeepAliveConnection(t *testing.T) {
	var n atomic.Int32
	r := newRig(t, func(ex *Exchange) {
		okHandler(string(rune('a' + n.Add(1) - 1)))(ex)
	}, nil)
	c := r.dial()
	for _, want := range []string{"a", "b", "c"} {
		c.send(get)
		rp := c.read()
		if rp.status != 200 || rp.body != want || rp.headers["connection"] != "" {
			t.Fatalf("got %+v, want body %q", rp, want)
		}
		if rp.headers["date"] == "" || rp.headers["content-length"] != "1" {
			t.Fatalf("missing engine headers: %+v", rp.headers)
		}
	}
}

func TestPipelinedRequestsAreAnsweredInOrder(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		okHandler(string(ex.Head().Path))(ex)
	}, nil)
	c := r.dial()
	c.send("GET /1 HTTP/1.1\r\nHost: x\r\n\r\nGET /2 HTTP/1.1\r\nHost: x\r\n\r\nGET /3 HTTP/1.1\r\nHost: x\r\n\r\n")
	for _, want := range []string{"/1", "/2", "/3"} {
		if rp := c.read(); rp.body != want {
			t.Fatalf("got %q, want %q", rp.body, want)
		}
	}
}

func TestARefusedHeadGetsItsStatusAndProblemBodyThenTheConnectionCloses(t *testing.T) {
	called := false
	r := newRig(t, func(ex *Exchange) { called = true }, nil)
	for _, tc := range []struct {
		req    string
		status int
	}{
		{"GET / HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\nTransfer-Encoding: chunked\r\n\r\n", 400},
		{"GET / HTTP/1.1\r\n\r\n", 400}, // no Host: the parser would allow keeping it, ADR 0045 closes
		{"GET / HTTP/2.0\r\nHost: x\r\n\r\n", 505},
		{"GET / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: gzip\r\n\r\n", 501},
	} {
		c := r.dial()
		c.send(tc.req)
		rp := c.read()
		if rp.status != tc.status || rp.headers["connection"] != "close" ||
			rp.headers["content-type"] != "application/problem+json" ||
			!strings.HasPrefix(rp.body, `{"title":"`) || !strings.Contains(rp.body, `"status":`) {
			t.Fatalf("%q: got %+v", tc.req, rp)
		}
		c.expectClosed()
	}
	if called {
		t.Fatal("the handler ran for a refused head")
	}
}

func TestAHeadLargerThanTheLimitIsA431AndCloses(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHost: x\r\nX: " + strings.Repeat("a", 9000) + "\r\n\r\n")
	if rp := c.read(); rp.status != 431 {
		t.Fatalf("status %d", rp.status)
	}
	c.expectClosed()
}

func TestAHeadLargerThanOneBufferButWithinTheLimitIsServed(t *testing.T) {
	r := newRig(t, okHandler("big"), nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHost: x\r\nX: " + strings.Repeat("a", 6000) + "\r\n\r\n")
	if rp := c.read(); rp.status != 200 || rp.body != "big" {
		t.Fatalf("got %+v", rp)
	}
	c.send(get)
	if rp := c.read(); rp.status != 200 {
		t.Fatalf("the next request on the same connection: %+v", rp)
	}
}

func echoBody(max int64) handlerFunc {
	return func(ex *Exchange) {
		b, err := ex.ReadBody(max, 5*time.Second)
		if err != nil {
			var he *h1.Error
			errors.As(err, &he)
			r := ex.Respond(he.Status)
			*r.Body() = append(*r.Body(), he.Reason...)
			return
		}
		r := ex.Respond(200)
		*r.Body() = append(*r.Body(), b...)
	}
}

func TestAContentLengthBodyIsReadIntoTheSlabAndAnotherRequestFollows(t *testing.T) {
	r := newRig(t, echoBody(1<<20), nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\n\r\nhello world")
	if rp := c.read(); rp.status != 200 || rp.body != "hello world" {
		t.Fatalf("got %+v", rp)
	}
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\n")
	c.send("hi")
	if rp := c.read(); rp.body != "hi" {
		t.Fatalf("got %+v", rp)
	}
}

func TestAChunkedBodyIsDecodedAndPipelinedBytesAfterItSurvive(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		if ex.Head().Method[0] == 'P' {
			echoBody(1 << 20)(ex)
			return
		}
		okHandler("next")(ex)
	}, nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6;ext=1\r\n world\r\n0\r\nTrailer: v\r\n\r\n" + get)
	if rp := c.read(); rp.status != 200 || rp.body != "hello world" {
		t.Fatalf("got %+v", rp)
	}
	if rp := c.read(); rp.body != "next" {
		t.Fatalf("pipelined request after the chunked body: %+v", rp)
	}
}

func TestAChunkedBodyOverTheLimitIsA413(t *testing.T) {
	r := newRig(t, echoBody(8), nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n" + "10\r\n0123456789abcdef\r\n0\r\n\r\n")
	if rp := c.read(); rp.status != 413 || rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
}

func TestExpectContinueSendsAHundredThenReadsTheBody(t *testing.T) {
	r := newRig(t, echoBody(1<<20), nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nExpect: 100-continue\r\n\r\n")
	if rp := c.read(); rp.status != 100 {
		t.Fatalf("want 100, got %+v", rp)
	}
	c.send("abcd")
	if rp := c.read(); rp.status != 200 || rp.body != "abcd" {
		t.Fatalf("got %+v", rp)
	}
}

func TestExpectContinueOverTheLimitIsA413WithoutReadingOrSendingAHundred(t *testing.T) {
	r := newRig(t, echoBody(10), nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 11\r\nExpect: 100-continue\r\n\r\n")
	rp := c.read()
	if rp.status != 413 || rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
}

func TestABodyThatTakesLongerThanTheTimeoutIsA408AndCloses(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		b, err := ex.ReadBody(100, 9*time.Second)
		if err != nil {
			var he *h1.Error
			errors.As(err, &he)
			ex.Respond(he.Status)
			return
		}
		ex.Respond(200).Body()
		_ = b
	}, nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nabc")
	waitNext(t, r.clk, epoch.Add(9*time.Second))
	r.clk.Advance(9 * time.Second)
	if rp := c.read(); rp.status != 408 || rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
}

func TestAClientThatNeverFinishesItsHeadIsClosedAtTheHeadTimeout(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHo")
	waitFor(t, func() bool { return c.c.(*memconn.Conn).Unconsumed() == 0 }, "the server to read the partial head")
	waitNext(t, r.clk, epoch.Add(7*time.Second))
	r.clk.Advance(7 * time.Second)
	c.expectClosed()
}

func TestAnIdleConnectionIsClosedAtTheIdleTimeoutAndNotBefore(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter)) // the first wait is short
	r.clk.Advance(moveAfter)                 // the wait moves to a fresh goroutine (ADR 0005)
	waitNext(t, r.clk, epoch.Add(60*time.Second))
	r.clk.Advance(59*time.Second - moveAfter)
	c.send(get) // still open: this also restarts the idle clock
	c.read()
	waitNext(t, r.clk, epoch.Add(59*time.Second+moveAfter))
	r.clk.Advance(moveAfter)
	waitNext(t, r.clk, epoch.Add(59*time.Second+60*time.Second))
	r.clk.Advance(60*time.Second - moveAfter)
	c.expectClosed()
}

func TestAnIdleTimeoutShorterThanTheMoveIsKeptAndTheWaitNeverMoves(t *testing.T) {
	r := newRig(t, okHandler("x"), func(o *Options) { o.IdleTimeout = moveAfter / 2 })
	c := r.dial()
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter/2))
	r.clk.Advance(moveAfter / 2)
	c.expectClosed()
}

func TestAPanicInTheHandlerIsA500ThenTheConnectionClosesAndTheStackIsLogged(t *testing.T) {
	var logs lockedBuf
	r := newRig(t, func(ex *Exchange) {
		ex.Respond(200)
		panic("boom")
	}, func(o *Options) { o.Logger = slogTo(&logs) })
	c := r.dial()
	c.send(get)
	rp := c.read()
	if rp.status != 500 || rp.headers["connection"] != "close" || !strings.Contains(rp.body, `"status":500`) {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
	if s := logs.String(); !strings.Contains(s, "boom") || !strings.Contains(s, "goroutine") {
		t.Fatalf("panic and stack not logged: %s", s)
	}
}

func TestAHandlerThatNeverRespondsGetsA500(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {}, nil)
	c := r.dial()
	c.send(get)
	if rp := c.read(); rp.status != 500 {
		t.Fatalf("got %+v", rp)
	}
	c.send(get) // a framework bug, not a broken connection: it stays open
	if rp := c.read(); rp.status != 500 {
		t.Fatalf("got %+v", rp)
	}
}

func TestAHeadResponseHasTheBodysContentLengthAndNoBody(t *testing.T) {
	r := newRig(t, okHandler("twelve bytes"), nil)
	c := r.dial()
	c.isHead = true
	c.send("HEAD / HTTP/1.1\r\nHost: x\r\n\r\n")
	rp := c.read()
	if rp.status != 200 || rp.headers["content-length"] != "12" {
		t.Fatalf("got %+v", rp)
	}
	c.isHead = false
	c.send(get) // proves no body bytes were left in the stream
	if rp := c.read(); rp.body != "twelve bytes" {
		t.Fatalf("got %+v", rp)
	}
}

func TestConnectionCloseIsHonouredAndAnswered(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send("GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	rp := c.read()
	if rp.status != 200 || rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
}

func TestHTTP10ClosesUnlessItAsksToKeepAlive(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send("GET / HTTP/1.0\r\n\r\n")
	if rp := c.read(); rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()

	c = r.dial()
	c.send("GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n")
	if rp := c.read(); rp.headers["connection"] != "keep-alive" {
		t.Fatalf("got %+v", rp)
	}
	c.send("GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n")
	c.read()
}

func TestAnUnreadBodyIsDrainedSoTheNextRequestStartsCleanly(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		if ex.Head().Method[0] == 'P' {
			ex.Respond(204)
			return
		}
		okHandler("after")(ex)
	}, nil)
	c := r.dial()
	body := strings.Repeat("z", 20000)
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 20000\r\n\r\n" + body[:100])
	if rp := c.read(); rp.status != 204 {
		t.Fatalf("got %+v", rp)
	}
	c.send(body[100:] + get)
	if rp := c.read(); rp.body != "after" {
		t.Fatalf("the request after the drained body: %+v", rp)
	}
}

func TestWithTheReaderFallbackAnUnreadBodyIsDrainedWithTheByteTheWatchTook(t *testing.T) {
	sent := make(chan struct{})
	_, c := tcpPair(t, true, func(ex *Exchange) {
		if ex.Head().Method[0] == 'P' {
			ex.Context().Done()
			<-sent
			time.Sleep(30 * time.Millisecond) // the watch takes the body's next byte
			ex.Respond(204)
			return
		}
		okHandler(string(ex.Head().Method))(ex) // a byte of the body before the next head would show here
	})
	body := strings.Repeat("z", 20000)
	c.Write([]byte("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 20000\r\n\r\n" + body[:100]))
	time.Sleep(30 * time.Millisecond)
	c.Write([]byte(body[100:101]))
	close(sent)
	time.Sleep(60 * time.Millisecond)
	c.Write([]byte(body[101:] + get))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var all strings.Builder
	buf := make([]byte, 4096)
	for strings.Count(all.String(), "HTTP/1.1 ") < 2 || !strings.HasSuffix(all.String(), "T") {
		m, err := c.Read(buf)
		if err != nil {
			t.Fatalf("%v; got %q", err, all.String())
		}
		all.Write(buf[:m])
	}
	if !strings.Contains(all.String(), " 204 ") || !strings.HasSuffix(all.String(), "\r\n\r\nGET") {
		t.Fatalf("got %q", all.String())
	}
}

func TestAnUnreadBodyTooBigToDrainClosesTheConnection(t *testing.T) {
	r := newRig(t, func(ex *Exchange) { ex.Respond(204) }, nil)
	c := r.dial()
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 1000000\r\n\r\n")
	if rp := c.read(); rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
}

func TestTheHandlerCannotInjectAHeaderLineThroughAValue(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		ex.Respond(200).Header("X-A", "v\r\nSet-Cookie: evil=1")
	}, nil)
	c := r.dial()
	c.send(get)
	rp := c.read()
	if _, bad := rp.headers["set-cookie"]; bad {
		t.Fatalf("header injected: %+v", rp.headers)
	}
}

func TestTheRemoteAddressIsWhatTheDialGave(t *testing.T) {
	got := make(chan string, 1)
	r := newRig(t, func(ex *Exchange) {
		got <- ex.RemoteAddr().String()
		ex.Respond(204)
	}, nil)
	c := r.dial()
	c.send(get)
	c.read()
	if a := <-got; a != "203.0.113.9:4000" {
		t.Fatalf("remote %q", a)
	}
}

func TestMaxConnsStopsAcceptingInsteadOfAcceptingAndClosing(t *testing.T) {
	r := newRig(t, okHandler("x"), func(o *Options) { o.MaxConns = 1 })
	a := r.dial()
	a.send(get)
	a.read()
	b := r.dial() // queued in the listener, not accepted
	b.send(get)
	time.Sleep(30 * time.Millisecond)
	if b.br.Buffered() != 0 {
		t.Fatal("served over the limit")
	}
	a.c.Close()
	if rp := b.read(); rp.status != 200 {
		t.Fatalf("the queued connection was not served once a slot freed: %+v", rp)
	}
}

// ---- disconnects (ADR 0016) ----

func causeHandler(started chan<- struct{}, cause chan<- error) handlerFunc {
	return func(ex *Exchange) {
		ctx := ex.Context()
		close(started)
		select {
		case <-ctx.Done():
			cause <- ctx.Cause()
		case <-time.After(5 * time.Second):
			cause <- errors.New("not cancelled")
		}
		ex.Respond(204)
	}
}

func TestAClientThatLeavesMidHandlerCancelsTheContextWithErrClientGoneOverMemconn(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	r := newRig(t, causeHandler(started, cause), nil)
	c := r.dial()
	c.send(get)
	<-started
	c.c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestNothingIsWrittenToAClientThatLeftWhenTheHandlerSetNoAnswerOrPanicked(t *testing.T) {
	for _, panics := range []bool{false, true} {
		started := make(chan struct{})
		r := newRig(t, func(ex *Exchange) {
			ctx := ex.Context()
			close(started)
			<-ctx.Done()
			if panics {
				panic("after the client left")
			}
		}, nil)
		c := r.dial()
		c.send(get)
		<-started
		// A half-close is a disconnect to the watcher (ADR 0016) and leaves the client able to
		// read, so a 500 written by the engine would arrive here.
		c.c.(interface{ CloseWrite() error }).CloseWrite()
		c.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, err := io.ReadAll(c.c)
		if len(b) != 0 || err != nil {
			t.Fatalf("panics=%v: the client read %q (err %v), want nothing and EOF", panics, b, err)
		}
	}
}

func TestAnAnswerTheHandlerSetIsStillWrittenAfterTheClientHalfClosed(t *testing.T) {
	started := make(chan struct{})
	r := newRig(t, func(ex *Exchange) {
		ctx := ex.Context()
		close(started)
		<-ctx.Done()
		ex.Respond(204)
	}, nil)
	c := r.dial()
	c.send(get)
	<-started
	c.c.(interface{ CloseWrite() error }).CloseWrite()
	if rp := c.read(); rp.status != 204 {
		t.Fatalf("%+v", rp)
	}
}

func tcpPair(t *testing.T, fallback bool, h handlerFunc) (*Server, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	srv := New(h, Options{Logger: quietLogger()})
	srv.forceFallback = fallback
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return srv, c
}

func TestAClientThatLeavesMidHandlerCancelsTheContextOverLoopbackTCPWithTheWatcher(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	srv, c := tcpPair(t, false, causeHandler(started, cause))
	c.Write([]byte(get))
	<-started
	c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
	_ = srv
}

func TestAClientThatLeavesMidHandlerCancelsTheContextThroughTheReaderGoroutineFallback(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	_, c := tcpPair(t, true, causeHandler(started, cause))
	c.Write([]byte(get))
	<-started
	c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestTheReaderGoroutineFallbackKeepsAPipelinedByteForTheNextRequest(t *testing.T) {
	n := 0
	_, c := tcpPair(t, true, func(ex *Exchange) {
		n++
		if n == 1 {
			ex.Context().Done() // arms the reader
			time.Sleep(50 * time.Millisecond)
		}
		okHandler(string(ex.Head().Path))(ex)
	})
	c.Write([]byte("GET /one HTTP/1.1\r\nHost: x\r\n\r\n"))
	time.Sleep(20 * time.Millisecond)
	c.Write([]byte("GET /two HTTP/1.1\r\nHost: x\r\n\r\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	var all strings.Builder
	for !strings.Contains(all.String(), "/two") {
		m, err := c.Read(buf)
		if err != nil {
			t.Fatalf("%v; got %q", err, all.String())
		}
		all.Write(buf[:m])
	}
	if !strings.Contains(all.String(), "/one") {
		t.Fatalf("got %q", all.String())
	}
}

func TestAContextTheHandlerNeverLooksAtCostsNoWatching(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send(get)
	c.read()
	if r.srv.watcher.Load() != nil {
		t.Fatal("a watcher was created though no handler asked for Done")
	}
}

// ---- shutdown (ADR 0029) ----

func TestShutdownClosesIdleConnectionsNowAndReturnsWhenNothingIsLeft(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter)) // idle and waiting
	if err := r.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.expectClosed()
	if err := <-r.done; err != ErrServerClosed {
		t.Fatalf("Serve returned %v", err)
	}
}

func TestShutdownClosesAnIdleConnectionWhoseWaitHasMovedToAFreshGoroutine(t *testing.T) {
	r := newRig(t, okHandler("x"), nil)
	c := r.dial()
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter))
	r.clk.Advance(moveAfter)
	waitNext(t, r.clk, epoch.Add(60*time.Second)) // moved, and waiting out the rest
	if err := r.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.expectClosed()
	if err := <-r.done; err != ErrServerClosed {
		t.Fatalf("Serve returned %v", err)
	}
}

func TestShutdownLetsARequestInFlightFinishWithConnectionClose(t *testing.T) {
	in, release := make(chan struct{}), make(chan struct{})
	r := newRig(t, func(ex *Exchange) {
		close(in)
		<-release
		okHandler("done")(ex)
	}, nil)
	c := r.dial()
	c.send(get)
	<-in
	res := make(chan error, 1)
	go func() { res <- r.srv.Shutdown(context.Background()) }()
	select {
	case err := <-res:
		t.Fatalf("Shutdown returned %v with a request in flight", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := r.ln.Dial(nil); err == nil {
		t.Fatal("the listener still accepts")
	}
	close(release)
	rp := c.read()
	if rp.body != "done" || rp.headers["connection"] != "close" {
		t.Fatalf("got %+v", rp)
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
	c.expectClosed()
}

func TestShutdownForceClosesWhenItsContextEndsAndCancelsTheLiveContext(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	r := newRig(t, causeHandler(started, cause), nil)
	c := r.dial()
	c.send(get)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown returned %v", err)
	}
	if err := <-cause; err != ErrServerClosed {
		t.Fatalf("cause = %v", err)
	}
	_ = io.EOF
}

// heapLive is the live heap after a collection.
func heapLive() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

const declare1MiB = "POST /p HTTP/1.1\r\nHost: x\r\nContent-Length: 1048576\r\n\r\n"

func TestASilentClientThatDeclaresA1MiBBodyHoldsAtMost64KiBWhileItWaits(t *testing.T) {
	const n = 40
	var reading atomic.Int32
	r := newRig(t, func(ex *Exchange) {
		reading.Add(1)
		echoBody(1 << 20)(ex)
	}, nil)
	before := heapLive()
	for i := 0; i < n; i++ {
		r.dial().send(declare1MiB)
	}
	waitFor(t, func() bool { return reading.Load() == n }, "every handler to be reading")
	// Past the point where ReadBody has made its region: a body region of 64 KiB is the most a
	// silent client may hold; give the runtime the time a region takes to appear, then measure.
	time.Sleep(100 * time.Millisecond)
	per := (heapLive() - before) / n
	t.Logf("held per silent connection declaring 1 MiB: %d B", per)
	if per > 64<<10+12<<10 {
		t.Fatalf("a silent connection holds %d B, over 64 KiB plus the connection's own", per)
	}
}

func TestARouteThatDoesNotReadItsBodyReservesNoBodyMemory(t *testing.T) {
	release := make(chan struct{})
	got := make(chan int, 1)
	r := newRig(t, func(ex *Exchange) {
		got <- ex.Slab().Remaining()
		<-release
		okHandler("x")(ex)
	}, nil)
	c := r.dial()
	c.send(declare1MiB)
	remaining := <-got
	close(release)
	if remaining > len(declare1MiB) {
		t.Fatalf("the slab has %d B free for a body that has not arrived, the head is %d B", remaining, len(declare1MiB))
	}
}

func TestABodyThatArrivesSlowlyIsAssembledCorrectlyAndItsStringsStayValidAfterTheNextRequest(t *testing.T) {
	const size = 300 << 10 // past firstBody, so the region grows more than once
	mk := func(seed byte) string {
		b := make([]byte, size)
		for i := range b {
			b[i] = 'a' + (byte(i)+seed)%26
		}
		return string(b)
	}
	var kept []string // what the handler saw: the head's copy, and a view of the body
	r := newRig(t, func(ex *Exchange) {
		path := ex.Slab().Copy(ex.Head().Path) // made before the body is read
		b, err := ex.ReadBody(1<<20, 5*time.Second)
		if err != nil {
			ex.Respond(500)
			return
		}
		kept = append(kept, path, unsafe.String(&b[0], len(b)))
		okHandler(strconv.Itoa(len(b)))(ex)
	}, nil)
	c := r.dial()
	for i, body := range []string{mk(1), mk(2)} {
		c.send("POST /req" + strconv.Itoa(i) + " HTTP/1.1\r\nHost: x\r\nContent-Length: " + strconv.Itoa(size) + "\r\n\r\n")
		for off := 0; off < size; off += 7001 {
			c.send(body[off:min(off+7001, size)])
			time.Sleep(50 * time.Microsecond)
		}
		if rp := c.read(); rp.status != 200 || rp.body != strconv.Itoa(size) {
			t.Fatalf("request %d: %+v", i, rp)
		}
	}
	if len(kept) != 4 || kept[0] != "/req0" || kept[2] != "/req1" || kept[1] != mk(1) || kept[3] != mk(2) {
		t.Fatalf("a string from the first request changed after the second (or the body was assembled wrongly)")
	}
}

func TestAChunkedBodyIsNotCopiedTwice(t *testing.T) {
	const size = 400_000
	body := strings.Repeat("x", size)
	var chunked strings.Builder
	for off := 0; off < size; off += 10_000 {
		chunked.WriteString("2710\r\n" + body[off:off+10_000] + "\r\n")
	}
	chunked.WriteString("0\r\n\r\n")
	req := []byte("POST /p HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n" + chunked.String())
	r := newRig(t, func(ex *Exchange) {
		b, err := ex.ReadBody(1<<20, 5*time.Second)
		if err != nil || len(b) != size {
			ex.Respond(500)
			return
		}
		okHandler("ok")(ex)
	}, nil)
	c := r.dial()
	do := func() {
		c.c.Write(req)
		if rp := c.read(); rp.body != "ok" {
			t.Fatalf("%+v", rp)
		}
	}
	do()
	const runs = 10
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	for i := 0; i < runs; i++ {
		do()
	}
	runtime.ReadMemStats(&b)
	per := float64(b.TotalAlloc-a.TotalAlloc) / runs / size
	t.Logf("bytes allocated per request, in units of the body: %.2f", per)
	// One region doubling from 4 KiB allocates about 2.6 times a 400 KB body. Staging the body
	// and then copying it into the slab allocated more than 4 times.
	if per > 3.2 {
		t.Fatalf("allocated %.2f times the body per request", per)
	}
}

func TestAChunkedBodyOverSeveralGrowthsIsDecodedAndAPipelinedRequestSurvives(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		if ex.Head().Method[0] == 'P' {
			b, err := ex.ReadBody(1<<20, 5*time.Second)
			if err != nil {
				ex.Respond(500)
				return
			}
			okHandler(strconv.Itoa(len(b)) + string(b[len(b)-3:]))(ex)
			return
		}
		okHandler("next")(ex)
	}, nil)
	c := r.dial()
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		sb.WriteString("1388\r\n" + strings.Repeat("y", 4999) + "z\r\n")
	}
	c.send("POST /p HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n" + sb.String() + "0\r\n\r\n" + get)
	if rp := c.read(); rp.body != "250000yyz" {
		t.Fatalf("%+v", rp)
	}
	if rp := c.read(); rp.body != "next" {
		t.Fatalf("pipelined request after the chunked body: %+v", rp)
	}
}

func TestAPanicAfterTheHandlerSetARequestIDCarriesItInTheEngines500(t *testing.T) {
	r := newRig(t, func(ex *Exchange) {
		ex.SetRequestID(`ab"c`)
		panic("boom")
	}, nil)
	c := r.dial()
	c.send(get)
	rp := c.read()
	if rp.status != 500 || rp.body != `{"title":"Internal Server Error","status":500,"request_id":"ab\"c"}` {
		t.Fatalf("got %+v", rp)
	}
}

func TestARequestIDDoesNotSurviveIntoTheNextRequestOnTheConnection(t *testing.T) {
	n := 0
	r := newRig(t, func(ex *Exchange) {
		n++
		if n == 1 {
			ex.SetRequestID("first")
		} else if ex.RequestID() != "" {
			panic("the id of the last request is still there")
		}
		ex.Respond(204)
	}, nil)
	c := r.dial()
	for range 2 {
		c.send(get)
		if rp := c.read(); rp.status != 204 {
			t.Fatalf("got %+v", rp)
		}
	}
}

func TestAddSlabGrowsTheSlabBeforeItsFirstUseAndIsIgnoredAfter(t *testing.T) {
	var before, after, headLen int
	r := newRig(t, func(ex *Exchange) {
		headLen = ex.HeadLen()
		ex.AddSlab(ex.HeadLen())
		before = ex.Slab().Remaining()
		ex.AddSlab(1000)
		after = ex.Slab().Remaining()
		ex.Respond(204)
	}, nil)
	c := r.dial()
	c.send(get)
	c.read()
	if before != 2*headLen || after != before {
		t.Fatalf("head %d, slab %d then %d", headLen, before, after)
	}
}

func TestAResponseReportsItsStatusSizeAndContentTypeAndTakesANewStatus(t *testing.T) {
	var gotStatus, gotLen int
	var ct string
	r := newRig(t, func(ex *Exchange) {
		w := ex.Respond(200)
		w.Header("content-type", "application/json")
		w.Header("X-A", "1")
		*w.Body() = append(*w.Body(), `{"a":1}`...)
		gotStatus, gotLen, ct = w.Status(), w.BodyLen(), w.ContentType()
		w.SetStatus(203)
	}, nil)
	c := r.dial()
	c.send(get)
	rp := c.read()
	if gotStatus != 200 || gotLen != 7 || ct != "application/json" {
		t.Fatalf("%d %d %q", gotStatus, gotLen, ct)
	}
	if rp.status != 203 || rp.headers["x-a"] != "1" || rp.body != `{"a":1}` {
		t.Fatalf("%+v", rp)
	}
}
