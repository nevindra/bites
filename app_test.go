package bites_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type GetUser struct {
	Path  struct{ ID int64 }
	Query struct{ Expand bites.Opt[bool] }
}

type User struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Expand bool   `json:"expand"`
}

func getUser(_ context.Context, in GetUser) (User, error) {
	return User{ID: in.Path.ID, Name: "Ada", Expand: in.Query.Expand.Or(false)}, nil
}

func start(t *testing.T, register func(app *bites.App)) *bitetest.Server {
	t.Helper()
	return startWith(t, register)
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startWith serves an app whose log lines go to a buffer the test can read, with extra options.
func startWith(t *testing.T, register func(app *bites.App), opts ...bites.Option) *bitetest.Server {
	t.Helper()
	srv, _ := startLogged(t, register, opts...)
	return srv
}

func startLogged(t *testing.T, register func(app *bites.App), opts ...bites.Option) (*bitetest.Server, *logBuf) {
	t.Helper()
	lb := &logBuf{}
	all := append([]bites.Option{bites.WithClock(bitetest.NewClock()), bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil)))}, opts...)
	app := bites.New(all...)
	register(app)
	return bitetest.Start(t, app), lb
}

func TestAPlainValueIsA200WithItsJSON(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/42?expand=true"})
	if r.Status != 200 || r.Get("content-type") != "application/json" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
	if got, want := r.Text(), `{"id":42,"name":"Ada","expand":true}`; got != want {
		t.Fatalf("body %s, want %s", got, want)
	}
}

func TestAPathParamThatIsNotANumberIsA400NamingPathID(t *testing.T) {
	srv := start(t, func(app *bites.App) { app.Get("/users/{id}", getUser) })
	r := srv.Do(bitetest.Request{Method: "GET", Target: "/users/abc"})
	want := `{"title":"Bad Request","status":400,"errors":[{"location":"path.id","message":"expected an integer","rule":"type"}]}`
	if r.Status != 400 || r.Get("content-type") != "application/problem+json" || r.Text() != want {
		t.Fatalf("%d %s\n%s", r.Status, r.Get("content-type"), r.Text())
	}
}

// ---- TLS (ADR 0017) ----

func tlsConfigs(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bites.test"}, DNSNames: []string{"bites.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{RootCAs: pool, ServerName: "bites.test"}
}

func TestAnAppServesTypedRoutesOverTLSOnALoopbackListenerAndShutsDown(t *testing.T) {
	scfg, ccfg := tlsConfigs(t)
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/users/{id}", getUser)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.ServeTLS(ln, scfg) }()

	ccfg.NextProtos = []string{"h2", "http/1.1"}
	c, err := tls.Dial("tcp", ln.Addr().String(), ccfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if p := c.ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Fatalf("negotiated %q", p)
	}
	br := bufio.NewReader(c)
	for _, id := range []string{"1", "2"} {
		fmt.Fprintf(c, "GET /users/%s HTTP/1.1\r\nHost: x\r\n\r\n", id)
		status, _ := br.ReadString('\n')
		if !strings.Contains(status, " 200 ") {
			t.Fatalf("got %q", status)
		}
		for l, _ := br.ReadString('\n'); l != "\r\n"; l, _ = br.ReadString('\n') {
		}
		body := make([]byte, len(`{"id":1,"name":"Ada","expand":false}`))
		io.ReadFull(br, body)
		if !strings.Contains(string(body), `"id":`+id) {
			t.Fatalf("body %q", body)
		}
	}
	h2only := ccfg.Clone()
	h2only.NextProtos = []string{"h2"}
	if c2, err := tls.Dial("tcp", ln.Addr().String(), h2only); err == nil {
		c2.Close()
		t.Fatal("a client offering only h2 got a connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != bites.ErrServerClosed {
		t.Fatalf("ServeTLS returned %v", err)
	}
}

// ---- Several listeners and Unix sockets (ADR 0068) ----

func unixGet(t *testing.T, path, target string) (string, string) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", target)
	b, _ := io.ReadAll(c)
	head, body, _ := strings.Cut(string(b), "\r\n\r\n")
	status, _, _ := strings.Cut(head, "\r\n")
	return status, body
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %s", path)
}

// shortTempDir is a temporary directory whose path leaves room for a socket's name: macOS bounds a
// socket path at 104 bytes, and t.TempDir there is under /var/folders and named after the test.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nilo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestOneAppServesTCPTLSAndAUnixSocketAtOnceAndShutdownEndsThemAll(t *testing.T) {
	scfg, ccfg := tlsConfigs(t)
	lb := &logBuf{}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(lb, nil))))
	app.Get("/addr", func(context.Context, Empty) (bites.Text, error) { return bites.Text{Body: "ok"}, nil })
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		resp := next.Call(ctx)
		resp.AddHeader("X-Remote", req.RemoteAddr().String())
		return resp, nil
	})
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	secure, _ := net.Listen("tcp", "127.0.0.1:0")
	sock := filepath.Join(shortTempDir(t), "app.sock")
	done := make(chan error, 3)
	go func() { done <- app.Serve(plain) }()
	go func() { done <- app.ServeTLS(secure, scfg) }()
	go func() { done <- app.ListenUnix(sock, 0o660) }()
	waitForSocket(t, sock)

	if st, body := unixGet(t, sock, "/addr"); !strings.Contains(st, " 200 ") || body != "ok" {
		t.Fatalf("unix: %q %q", st, body)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o660 {
		t.Fatalf("the socket's permission: %v %v", fi.Mode(), err)
	}
	c, err := net.Dial("tcp", plain.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(c, "GET /addr HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	if b, _ := io.ReadAll(c); !strings.Contains(string(b), "X-Remote: 127.0.0.1:") {
		t.Fatalf("tcp: %q", b)
	}
	c.Close()
	if st, _, err := tlsGet(secure.Addr().String(), ccfg, "/addr"); err != nil || !strings.Contains(st, " 200 ") {
		t.Fatalf("tls: %q %v", st, err)
	}
	for _, want := range []string{
		`msg="bites: listening" network=tcp addr=` + plain.Addr().String() + " tls=false",
		`msg="bites: listening" network=tcp addr=` + secure.Addr().String() + " tls=true",
		`msg="bites: listening" network=unix addr=` + sock + " tls=false",
	} {
		if !strings.Contains(lb.String(), want) {
			t.Errorf("the start-up log has no %q:\n%s", want, lb.String())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := <-done; err != bites.ErrServerClosed {
			t.Fatalf("a listener returned %v", err)
		}
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("the socket file is still there: %v", err)
	}
}

func TestAUnixConnectionHasNoPeerAddress(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	var got atomic.Value
	app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
		got.Store(req.RemoteAddr())
		return next.Call(ctx), nil
	})
	app.Get("/x", ok[Empty])
	sock := filepath.Join(shortTempDir(t), "a.sock")
	go app.ListenUnix(sock, 0o600)
	t.Cleanup(func() { app.Close() })
	waitForSocket(t, sock)
	unixGet(t, sock, "/x")
	if ap := got.Load().(netip.AddrPort); ap.IsValid() {
		t.Fatalf("%v", ap)
	}
}

func TestListenUnixRemovesASocketNobodyAnswersOnAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Skip(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // a server killed without removing its socket
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/x", ok[Empty])
	t.Cleanup(func() { app.Close() })
	go app.ListenUnix(stale, 0o600)
	waitForSocket(t, stale)
	if st, _ := unixGet(t, stale, "/x"); !strings.Contains(st, " 200 ") {
		t.Fatalf("%q", st)
	}
	// The socket now in use belongs to the app: a second ListenUnix on it is an error.
	if err := app.ListenUnix(stale, 0o600); err == nil || !strings.Contains(err.Error(), "another process is listening") {
		t.Fatalf("%v", err)
	}
	regular := filepath.Join(dir, "regular")
	os.WriteFile(regular, []byte("keep me"), 0o600)
	if err := app.ListenUnix(regular, 0o600); err == nil {
		t.Fatal("listened over a regular file")
	}
	if b, _ := os.ReadFile(regular); string(b) != "keep me" {
		t.Fatal("a regular file at the path was touched")
	}
}

func TestNowIsTheAppsClockInAProviderAHandlerAndAContextKeptAfterTheRequest(t *testing.T) {
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	type seen struct{ At time.Time }
	var kept context.Context
	app.Provide(func(ctx context.Context, _ bites.Request, _ struct{}) (seen, error) { return seen{bites.Now(ctx)}, nil })
	app.Get("/now", func(ctx context.Context, in struct{ S seen }) (bites.NoContent, error) {
		kept = context.WithValue(ctx, struct{}{}, 1)
		if got := bites.Now(kept); !got.Equal(in.S.At) {
			return bites.NoContent{}, fmt.Errorf("the handler's derived context says %v and the Provider %v", got, in.S.At)
		}
		return bites.NoContent{}, nil
	})
	srv := bitetest.Start(t, app)
	clk.Advance(90 * time.Minute)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/now"}); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	want := clk.Now()
	if got := bites.Now(kept); !got.Equal(want) {
		t.Fatalf("a context kept after its request reads %v, not the app's clock at %v", got, want)
	}
	if got := bites.Now(context.Background()); got.Sub(time.Now()).Abs() > time.Minute {
		t.Fatalf("a context that is not a request's reads %v, not the real time", got)
	}
}
