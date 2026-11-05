package engine

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/reqctx"
)

// TLS in the engine (ADR 0017). Certificates are made here with crypto/x509 (ecdsa P-256), no
// files. The handshake runs over memconn on the fake clock where it can, so a stalled one is
// expired by Advance and not waited for.

const testHost = "bites.test"

// testCerts returns a server config and a client config that trusts it.
func testCerts(t testing.TB) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: testHost},
		DNSNames:              []string{testHost},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	server = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	client = &tls.Config{RootCAs: pool, ServerName: testHost}
	return server, client
}

// newTLSRig is newRig serving TLS on the memconn listener.
func newTLSRig(t *testing.T, h handlerFunc, mod func(*Options)) (*rig, *tls.Config) {
	t.Helper()
	scfg, ccfg := testCerts(t)
	clk := clock.NewFake(epoch)
	o := Options{Clock: clk, Logger: quietLogger(), HeadTimeout: 7 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 30 * time.Second}
	if mod != nil {
		mod(&o)
	}
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	r.srv = New(h, o)
	go func() { r.done <- r.srv.ServeTLS(r.ln, scfg) }()
	t.Cleanup(func() { r.srv.Close() })
	return r, ccfg
}

// dialTLS dials and completes the handshake, failing the test if it does not.
func (r *rig) dialTLS(ccfg *tls.Config) *client {
	r.t.Helper()
	c, err := r.tryDialTLS(ccfg)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) tryDialTLS(ccfg *tls.Config) (*client, error) {
	raw, err := r.ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
	if err != nil {
		return nil, err
	}
	tc := tls.Client(raw, ccfg)
	res := make(chan error, 1)
	go func() { res <- tc.Handshake() }()
	select {
	case err := <-res:
		if err != nil {
			raw.Close()
			return nil, err
		}
	case <-time.After(5 * time.Second):
		raw.Close()
		return nil, context.DeadlineExceeded
	}
	return &client{t: r.t, c: tc, br: bufio.NewReader(tc)}, nil
}

func TestAHandshakeThatStallsIsClosedAtTheHandshakeTimeoutOnTheFakeClock(t *testing.T) {
	r, _ := newTLSRig(t, okHandler("x"), nil)
	raw, err := r.ln.Dial(nil) // connects, then says nothing
	if err != nil {
		t.Fatal(err)
	}
	waitNext(t, r.clk, epoch.Add(10*time.Second))
	r.clk.Advance(10*time.Second - time.Millisecond)
	select {
	case <-raw.(*memconn.Conn).PeerClosed():
		t.Fatal("closed before the handshake timeout")
	case <-time.After(30 * time.Millisecond):
	}
	r.clk.Advance(time.Millisecond)
	select {
	case <-raw.(*memconn.Conn).PeerClosed():
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled handshake was not closed at the handshake timeout")
	}
}

func TestAHandshakeTimeoutFromTheOptionsReplacesTheDefault(t *testing.T) {
	r, _ := newTLSRig(t, okHandler("x"), func(o *Options) { o.HandshakeTimeout = 3 * time.Second })
	raw, _ := r.ln.Dial(nil)
	waitNext(t, r.clk, epoch.Add(3*time.Second))
	r.clk.Advance(3 * time.Second)
	select {
	case <-raw.(*memconn.Conn).PeerClosed():
	case <-time.After(5 * time.Second):
		t.Fatal("not closed at 3 s")
	}
}

func TestAStalledHandshakeCountsAgainstMaxConnsUntilItIsClosed(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), func(o *Options) { o.MaxConns = 1 })
	stalled, _ := r.ln.Dial(nil)
	waitNext(t, r.clk, epoch.Add(10*time.Second))
	got := make(chan *client, 1)
	go func() {
		c, err := r.tryDialTLS(ccfg)
		if err != nil {
			t.Error(err)
		}
		got <- c
	}()
	select {
	case <-got:
		t.Fatal("a second connection was served while the first held the only slot in its handshake")
	case <-time.After(50 * time.Millisecond):
	}
	r.clk.Advance(10 * time.Second)
	<-stalled.(*memconn.Conn).PeerClosed()
	select {
	case c := <-got:
		if c == nil {
			return
		}
		c.send(get)
		if rp := c.read(); rp.status != 200 {
			t.Fatalf("got %+v", rp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slot was not freed when the stalled handshake closed")
	}
}

func TestAPlainHTTPRequestToATLSPortIsClosedWithoutAnAnswer(t *testing.T) {
	called := false
	r, _ := newTLSRig(t, func(ex *Exchange) { called = true }, nil)
	c := r.dial()
	c.send(get)
	c.expectClosed()
	if called {
		t.Fatal("the handler ran")
	}
}

func TestATLSClientThatOffersOnlyH2GetsNoConnection(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), nil)
	cfg := ccfg.Clone()
	cfg.NextProtos = []string{"h2"}
	if c, err := r.tryDialTLS(cfg); err == nil {
		t.Fatalf("handshake succeeded, negotiated %q", c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol)
	}
}

func TestATLSClientThatOffersH2AndHTTP11IsServedHTTP11(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), nil)
	cfg := ccfg.Clone()
	cfg.NextProtos = []string{"h2", "http/1.1"}
	c := r.dialTLS(cfg)
	if p := c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Fatalf("negotiated %q", p)
	}
	c.send(get)
	if rp := c.read(); rp.status != 200 || rp.body != "x" {
		t.Fatalf("got %+v", rp)
	}
}

func TestAConfigReturnedByGetConfigForClientCannotOfferH2EitherAndNoALPNIsServed(t *testing.T) {
	scfg, ccfg := testCerts(t)
	inner := scfg.Clone()
	inner.NextProtos = []string{"h2", "http/1.1"}
	scfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return inner, nil }
	clk := clock.NewFake(epoch)
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	r.srv = New(okHandler("x"), Options{Clock: clk, Logger: quietLogger()})
	go func() { r.done <- r.srv.ServeTLS(r.ln, scfg) }()
	t.Cleanup(func() { r.srv.Close() })
	h2 := ccfg.Clone()
	h2.NextProtos = []string{"h2", "http/1.1"}
	c := r.dialTLS(h2)
	if p := c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Fatalf("negotiated %q, the engine speaks HTTP/1.1 only", p)
	}
	none := r.dialTLS(ccfg) // a client with no ALPN at all
	none.send(get)
	if rp := none.read(); rp.status != 200 {
		t.Fatalf("got %+v", rp)
	}
}

func TestKeepAliveOverTLSServesSeveralRequestsAndPipelinedOnes(t *testing.T) {
	r, ccfg := newTLSRig(t, func(ex *Exchange) { okHandler(string(ex.Head().Path))(ex) }, nil)
	c := r.dialTLS(ccfg)
	for _, p := range []string{"/a", "/b", "/c"} {
		c.send("GET " + p + " HTTP/1.1\r\nHost: x\r\n\r\n")
		if rp := c.read(); rp.status != 200 || rp.body != p || rp.headers["connection"] != "" {
			t.Fatalf("got %+v, want %s", rp, p)
		}
	}
	c.send("GET /1 HTTP/1.1\r\nHost: x\r\n\r\nGET /2 HTTP/1.1\r\nHost: x\r\n\r\nGET /3 HTTP/1.1\r\nHost: x\r\n\r\n")
	for _, p := range []string{"/1", "/2", "/3"} {
		if rp := c.read(); rp.body != p {
			t.Fatalf("got %q, want %q", rp.body, p)
		}
	}
}

func TestABodyIsReadOverTLSAcrossRecords(t *testing.T) {
	r, ccfg := newTLSRig(t, echoBody(1<<20), nil)
	c := r.dialTLS(ccfg)
	body := strings.Repeat("0123456789", 5000) // 50 KB: several TLS records
	c.send("POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 50000\r\n\r\n" + body)
	if rp := c.read(); rp.body != body {
		t.Fatalf("got %d bytes, want %d", len(rp.body), len(body))
	}
}

func TestARefusedHeadOverTLSIsAnsweredBeforeTheClose(t *testing.T) {
	called := false
	r, ccfg := newTLSRig(t, func(ex *Exchange) { called = true }, nil)
	c := r.dialTLS(ccfg)
	c.send("GET / HTTP/1.1\r\n\r\n")
	rp := c.read()
	if rp.status != 400 || rp.headers["connection"] != "close" || rp.headers["content-type"] != "application/problem+json" {
		t.Fatalf("got %+v", rp)
	}
	c.expectClosed()
	if called {
		t.Fatal("the handler ran for a refused head")
	}
}

func TestAClientThatLeavesMidHandlerCancelsTheContextWithErrClientGoneOverTLSOnMemconn(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	r, ccfg := newTLSRig(t, causeHandler(started, cause), nil)
	c := r.dialTLS(ccfg)
	c.send(get)
	<-started
	c.c.Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

// tlsTCPPair is tcpPair for TLS: a loopback listener and a client that has completed the handshake.
func tlsTCPPair(t *testing.T, fallback bool, h handlerFunc) (*Server, *tls.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	scfg, ccfg := testCerts(t)
	srv := New(h, Options{Logger: quietLogger()})
	srv.forceFallback = fallback
	go srv.ServeTLS(ln, scfg)
	t.Cleanup(func() { srv.Close() })
	c, err := tls.Dial("tcp", ln.Addr().String(), ccfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return srv, c
}

func TestAClientThatLeavesMidHandlerCancelsTheContextOverLoopbackTLSWithTheWatcher(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	srv, c := tlsTCPPair(t, false, causeHandler(started, cause))
	c.Write([]byte(get))
	<-started
	c.NetConn().Close() // the peer's socket goes, as a crashed client's would
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
	if runtime.GOOS == "linux" && srv.watcher.Load() == nil { // elsewhere the reader goroutine watches (conn.go)
		t.Fatal("the epoll watcher was not used for the TLS connection")
	}
}

func TestAClientThatSendsCloseNotifyAndLeavesMidHandlerCancelsTheContextOverLoopbackTLS(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	_, c := tlsTCPPair(t, false, causeHandler(started, cause))
	c.Write([]byte(get))
	<-started
	c.Close() // close_notify, then the socket
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestAClientThatLeavesMidHandlerCancelsTheContextOverLoopbackTLSThroughTheReaderGoroutineFallback(t *testing.T) {
	started, cause := make(chan struct{}), make(chan error, 1)
	_, c := tlsTCPPair(t, true, causeHandler(started, cause))
	c.Write([]byte(get))
	<-started
	c.NetConn().Close()
	if err := <-cause; err != reqctx.ErrClientGone {
		t.Fatalf("cause = %v", err)
	}
}

func TestTheReaderGoroutineFallbackKeepsAPipelinedByteForTheNextRequestOverTLS(t *testing.T) {
	n := 0
	_, c := tlsTCPPair(t, true, func(ex *Exchange) {
		n++
		if n == 1 {
			ex.Context().Done() // arms the reader, which reads through the *tls.Conn
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

func TestARefusedHeadOverLoopbackTLSIsAnsweredAndTheClientReadsItBeforeTheClose(t *testing.T) {
	_, c := tlsTCPPair(t, false, okHandler("x"))
	// The head is refused while the client is still sending: the linger drain must keep the
	// socket from resetting the answer away.
	c.Write([]byte("GET / HTTP/1.1\r\n\r\n" + strings.Repeat("z", 20000)))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, " 400 ") {
		t.Fatalf("got %q, %v", line, err)
	}
}

func TestAnIdleTLSConnectionMovesItsWaitAndStillServesAndStillTimesOut(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), nil)
	c := r.dialTLS(ccfg)
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter))
	r.clk.Advance(moveAfter)
	waitNext(t, r.clk, epoch.Add(60*time.Second)) // moved, waiting out the rest
	c.send(get)                                   // a read that follows a deadline timeout must still work
	if rp := c.read(); rp.status != 200 {
		t.Fatalf("got %+v", rp)
	}
	waitNext(t, r.clk, epoch.Add(moveAfter).Add(moveAfter)) // the next idle wait began at the same fake instant
	r.clk.Advance(moveAfter)
	waitNext(t, r.clk, epoch.Add(moveAfter+60*time.Second))
	r.clk.Advance(60 * time.Second)
	c.expectClosed()
}

func TestShutdownClosesAnIdleTLSConnection(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), nil)
	c := r.dialTLS(ccfg)
	c.send(get)
	c.read()
	waitNext(t, r.clk, epoch.Add(moveAfter))
	r.clk.Advance(moveAfter)
	waitNext(t, r.clk, epoch.Add(60*time.Second))
	if err := r.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.expectClosed()
	if err := <-r.done; err != ErrServerClosed {
		t.Fatalf("Serve returned %v", err)
	}
}

func TestShutdownClosesAHandshakeInProgress(t *testing.T) {
	r, _ := newTLSRig(t, okHandler("x"), nil)
	raw, _ := r.ln.Dial(nil)
	waitNext(t, r.clk, epoch.Add(10*time.Second))
	if err := r.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-raw.(*memconn.Conn).PeerClosed():
	case <-time.After(5 * time.Second):
		t.Fatal("the handshake in progress was not closed")
	}
}

// logRecorder is a slog handler that keeps every record at or above its level.
type logRecorder struct {
	mu    sync.Mutex
	level slog.Level
	recs  []slog.Record
}

func (h *logRecorder) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }
func (h *logRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r)
	h.mu.Unlock()
	return nil
}
func (h *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *logRecorder) WithGroup(string) slog.Handler      { return h }

func (h *logRecorder) at(l slog.Level) (n int, last slog.Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.Level == l {
			n++
			last = r
		}
	}
	return n, last
}

func (h *logRecorder) above(l slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.recs {
		if r.Level > l {
			n++
		}
	}
	return n
}

func attrInt(r slog.Record, key string) int64 {
	var v int64 = -1
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.Int64()
			return false
		}
		return true
	})
	return v
}

func TestAClientThatRejectsTheCertificateIsLoggedAsAWarningAtMostOnceAMinute(t *testing.T) {
	rec := &logRecorder{level: slog.LevelDebug}
	r, _ := newTLSRig(t, okHandler("x"), func(o *Options) { o.Logger = slog.New(rec) })
	distrust := &tls.Config{ServerName: testHost} // no RootCAs: the client refuses the certificate
	reject := func(done func() bool) {
		if c, err := r.tryDialTLS(distrust); err == nil {
			c.c.Close()
			t.Fatal("the handshake succeeded")
		}
		waitFor(t, done, "the server to log the failure")
	}
	warns := func() int { n, _ := rec.at(slog.LevelWarn); return n }
	reject(func() bool { return warns() == 1 })
	if _, w := rec.at(slog.LevelWarn); attrInt(w, "suppressed") != 0 {
		t.Fatalf("the first warning says suppressed=%d", attrInt(w, "suppressed"))
	}
	reject(func() bool { return r.srv.hsSuppressed.Load() == 1 })
	reject(func() bool { return r.srv.hsSuppressed.Load() == 2 })
	if n := warns(); n != 1 {
		t.Fatalf("two more rejections inside the minute made %d warnings in all", n)
	}
	r.clk.Advance(61 * time.Second)
	reject(func() bool { return warns() == 2 })
	if _, w := rec.at(slog.LevelWarn); attrInt(w, "suppressed") != 2 {
		t.Fatalf("the second warning says suppressed=%d, want 2", attrInt(w, "suppressed"))
	}
}

func TestAPortScanIsNotLoggedAboveDebug(t *testing.T) {
	rec := &logRecorder{level: slog.LevelDebug}
	r, _ := newTLSRig(t, okHandler("x"), func(o *Options) { o.Logger = slog.New(rec) })
	debugs := func() int { n, _ := rec.at(slog.LevelDebug); return n }
	r.dial().c.Close() // connect and close
	waitFor(t, func() bool { return debugs() == 1 }, "the first failure")
	c := r.dial()
	c.send(get) // plain HTTP to the TLS port
	c.expectClosed()
	waitFor(t, func() bool { return debugs() == 2 }, "the second failure")
	if n := rec.above(slog.LevelDebug); n != 0 {
		t.Fatalf("%d records above debug", n)
	}
	if n, _ := rec.at(slog.LevelDebug); n != 2 {
		t.Fatalf("%d debug records, want one for each failed handshake", n)
	}
}

func TestAFailedHandshakeAllocatesNothingWhenDebugIsOff(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	s := New(okHandler("x"), Options{Clock: clock.NewFake(epoch), Logger: quietLogger()})
	c := &conn{srv: s} // nc is nil: the address is read only when a line is written
	scan, rejected := io.EOF, error(&net.OpError{Op: "remote error", Err: tls.AlertError(48)})
	s.hsWarnAt.Store(epoch.UnixNano()) // a Warn was just written, so a rejection is only counted
	if got := testing.AllocsPerRun(1000, func() { c.handshakeFailed(scan) }); got != 0 {
		t.Fatalf("a scan allocates %v times", got)
	}
	if got := testing.AllocsPerRun(1000, func() { c.handshakeFailed(rejected) }); got != 0 {
		t.Fatalf("a suppressed rejection allocates %v times", got)
	}
}

// ---- acme-tls/1 (ADR 0017, milestone 3) ----

// acmeRig serves TLS with acme-tls/1 listed, logging at Debug into rec.
func acmeRig(t *testing.T, scfg *tls.Config) (*rig, *logRecorder) {
	t.Helper()
	rec := &logRecorder{level: slog.LevelDebug}
	clk := clock.NewFake(epoch)
	r := &rig{t: t, clk: clk, ln: memconn.Listen(clk), done: make(chan error, 1)}
	r.srv = New(okHandler("x"), Options{Clock: clk, Logger: slog.New(rec)})
	go func() { r.done <- r.srv.ServeTLS(r.ln, scfg) }()
	t.Cleanup(func() { r.srv.Close() })
	return r, rec
}

func TestACertificateChallengeOnAcmeTLSIsAnsweredByTheHandshakeAndClosedQuietly(t *testing.T) {
	scfg, ccfg := testCerts(t)
	scfg.NextProtos = []string{"http/1.1", ACMEProto}
	var sawACME bool
	var mu sync.Mutex
	cert := scfg.Certificates[0]
	scfg.Certificates = nil
	scfg.GetCertificate = func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
		mu.Lock()
		sawACME = sawACME || slicesContains(hi.SupportedProtos, ACMEProto)
		mu.Unlock()
		return &cert, nil
	}
	r, rec := acmeRig(t, scfg)
	acme := ccfg.Clone()
	acme.NextProtos = []string{ACMEProto}
	c := r.dialTLS(acme)
	if p := c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != ACMEProto {
		t.Fatalf("negotiated %q", p)
	}
	c.expectClosed()
	mu.Lock()
	if !sawACME {
		t.Fatal("GetCertificate did not see the challenge's protocol")
	}
	mu.Unlock()
	if n, last := rec.at(slog.LevelDebug); n != 0 {
		t.Fatalf("a challenge is not a failed handshake, logged %q", last.Message)
	}
	// Real traffic still gets HTTP/1.1, also from a client that offers acme-tls/1 beside it.
	for _, protos := range [][]string{{"h2", "http/1.1"}, {ACMEProto, "http/1.1"}, nil} {
		cfg := ccfg.Clone()
		cfg.NextProtos = protos
		c := r.dialTLS(cfg)
		if p := c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != "http/1.1" && p != "" {
			t.Fatalf("%v: negotiated %q", protos, p)
		}
		c.send(get)
		if rp := c.read(); rp.status != 200 {
			t.Fatalf("%v: %+v", protos, rp)
		}
	}
}

func TestAcmeTLSIsNotOfferedUnlessTheConfigListsIt(t *testing.T) {
	r, ccfg := newTLSRig(t, okHandler("x"), nil)
	cfg := ccfg.Clone()
	cfg.NextProtos = []string{ACMEProto}
	if c, err := r.tryDialTLS(cfg); err == nil {
		t.Fatalf("handshake succeeded, negotiated %q", c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol)
	}
}

func TestAConfigFromGetConfigForClientKeepsAcmeTLSWhenItListsItAndNeverH2(t *testing.T) {
	scfg, ccfg := testCerts(t)
	inner := scfg.Clone()
	inner.NextProtos = []string{"h2", ACMEProto, "http/1.1"}
	scfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) { return inner, nil }
	r, _ := acmeRig(t, scfg)
	acme := ccfg.Clone()
	acme.NextProtos = []string{ACMEProto}
	c := r.dialTLS(acme)
	if p := c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol; p != ACMEProto {
		t.Fatalf("negotiated %q", p)
	}
	c.expectClosed()
	h2 := ccfg.Clone()
	h2.NextProtos = []string{"h2"}
	if c, err := r.tryDialTLS(h2); err == nil {
		t.Fatalf("h2 negotiated %q", c.c.(*tls.Conn).ConnectionState().NegotiatedProtocol)
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
