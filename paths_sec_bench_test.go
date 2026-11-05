package bites

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/memconn"
	"github.com/nevindra/bites/internal/proxyproto"
)

// Exploration benchmarks for the security track: CPU per request of each security middleware,
// Provider and endpoint over memory on the real clock (and TLS over loopback TCP). Throwaway.

// exploreRead reads one answer with a Content-Length body and returns its status code.
func exploreRead(br *bufio.Reader) (int, error) {
	line, err := br.ReadSlice('\n')
	if err != nil {
		return 0, err
	}
	code, _ := strconv.Atoi(string(line[9:12]))
	cl := 0
	for {
		line, err = br.ReadSlice('\n')
		if err != nil {
			return 0, err
		}
		if len(line) == 2 {
			break
		}
		if len(line) > 16 && (line[0] == 'C' || line[0] == 'c') && strings.EqualFold(string(line[:15]), "content-length:") {
			cl, _ = strconv.Atoi(strings.TrimSpace(string(line[15:])))
		}
	}
	_, err = br.Discard(cl)
	return code, err
}

func exploreGetHandler(_ context.Context, in struct{ Path struct{ ID int64 } }) (budgetOut, error) {
	return budgetOut{ID: in.Path.ID, Name: "Ada Lovelace", Email: "ada@example.com", Age: 36}, nil
}

func getReq(target, extra string) []byte {
	return []byte("GET " + target + " HTTP/1.1\r\nHost: x\r\n" + extra + "\r\n")
}

// exploreMem serves register over memory and times request on one connection a goroutine.
func exploreMem(b *testing.B, opts []Option, register func(*App), request []byte, wantStatus int) {
	app := New(append([]Option{WithClock(clock.Real()), WithLogger(quiet())}, opts...)...)
	register(app)
	ln := memconn.Listen(app.Clock())
	go app.Serve(ln)
	defer app.Close()
	var mu sync.Mutex
	dial := func() (net.Conn, *bufio.Reader) {
		mu.Lock()
		defer mu.Unlock()
		c, err := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		if err != nil {
			b.Fatal(err)
		}
		return c, bufio.NewReaderSize(c, 8192)
	}
	c, br := dial()
	c.Write(request)
	if code, err := exploreRead(br); err != nil || code != wantStatus {
		b.Fatalf("first answer: %d %v, want %d", code, err, wantStatus)
	}
	c.Close()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		c, br := dial()
		defer c.Close()
		for pb.Next() {
			c.Write(request)
			if _, err := exploreRead(br); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func exploreTLSConfig(b *testing.B) (*tls.Config, *x509.CertPool) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x.test"}, DNSNames: []string{"x.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		b.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func exploreProxyV2() []byte {
	b := []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A, 0x21, 0x11, 0, 0}
	addr := append([]byte{198, 51, 100, 9, 203, 0, 113, 1}, 0, 0, 0, 0)
	binary.BigEndian.PutUint16(addr[8:], 5123)
	binary.BigEndian.PutUint16(addr[10:], 443)
	binary.BigEndian.PutUint16(b[14:], uint16(len(addr)))
	return append(b, addr...)
}

type exploreCount struct{ n int }

func (w *exploreCount) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }

func BenchmarkExploreSecurity(b *testing.B) {
	k := make([]byte, SessionKeyLen)
	for i := range k {
		k[i] = byte(i + 1)
	}
	carts := NewSessions[budgetCart](SessionOptions{Keys: [][]byte{k}})
	seed := Session[budgetCart]{s: carts, clk: clock.Real()}
	sc, err := seed.Save(context.Background(), budgetCart{Items: []string{"tea", "milk"}})
	if err != nil {
		b.Fatal(err)
	}
	cookie := "Cookie: " + sc.Name + "=" + sc.Value + "\r\n"
	const target = "/users/42?expand=true&page=3"
	const token = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhZGEifQ.c2lnbmF0dXJlLXNpZ25hdHVyZQ"
	const csrfTok = "q1w2e3r4t5y6u7i8o9p0a1s2d3f4g5h6j7k8l9z0x1c"
	csrfMW := CSRF(CSRFOptions{Origins: []string{"https://app.example.com"}})
	trusted := WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}})
	verifyBearer := func(_ context.Context, tok string) (budgetUser, error) {
		if tok != token {
			return budgetUser{}, ErrInvalidCredentials
		}
		return budgetUser{ID: "ada"}, nil
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("ada:secret"))
	m := NewMetrics()

	for _, v := range []struct {
		name    string
		opts    []Option
		reg     func(a *App)
		request []byte
	}{
		{"baseline_GET", nil, func(a *App) { a.Get("/users/{id}", exploreGetHandler) }, getReq("/users/42", "")},
		{"baseline_POST", nil, func(a *App) { a.Post("/users/{id}", budgetHandler) }, post(target, typedBody)},
		{"Sessions_read", nil, func(a *App) {
			a.Provide(carts.Load)
			a.Get("/c/{id}", func(_ context.Context, in budgetCartIn) (budgetCart, error) { return in.S.Value, nil })
		}, getReq("/c/1", cookie)},
		{"Sessions_save", nil, func(a *App) {
			a.Provide(carts.Load)
			a.Get("/c/{id}", func(ctx context.Context, in budgetCartIn) (OK[budgetCart], error) {
				c, err := in.S.Save(ctx, in.S.Value)
				return OK[budgetCart]{Body: in.S.Value, Cookies: []Cookie{c}}, err
			})
		}, getReq("/c/1", cookie)},
		{"CSRF_POST_token", nil, func(a *App) { a.Use(csrfMW); a.Post("/users/{id}", budgetHandler) },
			postWith(target, "Cookie: s=1; __Host-csrf="+csrfTok+"\r\nX-CSRF-Token: "+csrfTok+"\r\n", typedBody)},
		{"CSRF_POST_origin", nil, func(a *App) { a.Use(csrfMW); a.Post("/users/{id}", budgetHandler) },
			postWith(target, "Cookie: s=1\r\nSec-Fetch-Site: same-origin\r\n", typedBody)},
		{"CSRF_GET_issues", nil, func(a *App) { a.Use(csrfMW); a.Get("/users/{id}", exploreGetHandler) }, getReq("/users/42", "")},
		{"BasicAuth", nil, func(a *App) {
			a.Provide(BasicAuth(func(_ context.Context, u, p string) (budgetUser, error) {
				if u != "ada" || p != "secret" {
					return budgetUser{}, ErrInvalidCredentials
				}
				return budgetUser{ID: u}, nil
			}))
			a.Post("/users/{id}", budgetAuthedHandler)
		}, postWith(target, "Authorization: "+basic+"\r\n", typedBody)},
		{"BearerAuth", nil, func(a *App) { a.Provide(BearerAuth(verifyBearer)); a.Post("/users/{id}", budgetAuthedHandler) },
			postWith(target, "Authorization: Bearer "+token+"\r\n", typedBody)},
		{"APIKeyAuth", nil, func(a *App) {
			a.Provide(APIKeyAuth(func(_ context.Context, key string) (budgetUser, error) {
				if key != "k_live_0123456789abcdef" {
					return budgetUser{}, ErrInvalidCredentials
				}
				return budgetUser{ID: "svc"}, nil
			}))
			a.Post("/users/{id}", budgetAuthedHandler)
		}, postWith(target, "X-API-Key: k_live_0123456789abcdef\r\n", typedBody)},
		{"RateLimiter_1key", nil, func(a *App) {
			a.Use(NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second}).Middleware())
			a.Post("/users/{id}", budgetHandler)
		}, post(target, typedBody)},
		{"SecurityHeaders", nil, func(a *App) { a.Use(SecurityHeaders()); a.Post("/users/{id}", budgetHandler) }, post(target, typedBody)},
		{"CORS_preflight", nil, func(a *App) {
			a.Use(CORS(CORSOptions{Origins: []string{"https://app.example.com"}, Credentials: true}))
			a.Post("/users/{id}", budgetHandler)
		}, []byte("OPTIONS /users/42 HTTP/1.1\r\nHost: x\r\nOrigin: https://app.example.com\r\nAccess-Control-Request-Method: POST\r\nAccess-Control-Request-Headers: content-type\r\n\r\n")},
		{"Metrics_request", nil, func(a *App) { WithObserver(m)(a); a.Post("/users/{id}", budgetHandler) }, post(target, typedBody)},
		{"Health_livez", []Option{WithHealth(Health{Live: "/livez", Ready: "/readyz"})}, func(a *App) { a.Get("/users/{id}", exploreGetHandler) }, getReq("/livez", "")},
		{"TrustedProxy_XFF5", []Option{trusted}, func(a *App) { a.Use(readsClient); a.Post("/users/{id}", budgetHandler) },
			postWith(target, "X-Forwarded-For: 1.1.1.1, 2.2.2.2, 3.3.3.3, 198.51.100.9, 203.0.113.7\r\nX-Forwarded-Proto: https\r\n", typedBody)},
	} {
		code := 200
		if v.name == "CORS_preflight" {
			code = 204
		}
		b.Run(v.name, func(b *testing.B) { exploreMem(b, v.opts, v.reg, v.request, code) })
	}

	b.Run("RateLimiter_10000addrs", func(b *testing.B) {
		const n = 10000
		app := New(WithClock(clock.Real()), WithLogger(quiet()))
		app.Use(NewRateLimiter(RateLimitOptions{Limit: 1_000_000_000, Period: time.Second}).Middleware())
		app.Post("/users/{id}", budgetHandler)
		ln := memconn.Listen(app.Clock())
		go app.Serve(ln)
		defer app.Close()
		req := post(target, typedBody)
		conns := make([]net.Conn, n)
		brs := make([]*bufio.Reader, n)
		for i := range conns {
			c, err := ln.Dial(memconn.Addr{Name: fmt.Sprintf("10.%d.%d.%d:4000", i>>16&255, i>>8&255, i&255)})
			if err != nil {
				b.Fatal(err)
			}
			conns[i], brs[i] = c, bufio.NewReaderSize(c, 512)
			defer c.Close()
		}
		conns[0].Write(req)
		if code, err := exploreRead(brs[0]); err != nil || code != 200 {
			b.Fatalf("%d %v", code, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			j := i % n
			conns[j].Write(req)
			if _, err := exploreRead(brs[j]); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Metrics_scrape_50routes", func(b *testing.B) {
		mm := NewMetrics()
		app := New(WithClock(clock.Real()), WithLogger(quiet()), WithObserver(mm))
		app.ServeMetrics("/metrics", mm)
		for i := range 50 {
			app.Get("/r"+strconv.Itoa(i)+"/{id}", exploreGetHandler)
		}
		ln := memconn.Listen(app.Clock())
		go app.Serve(ln)
		defer app.Close()
		c, _ := ln.Dial(memconn.Addr{Name: "203.0.113.9:4000"})
		defer c.Close()
		br := bufio.NewReaderSize(c, 1<<16)
		for i := range 50 {
			c.Write(getReq("/r"+strconv.Itoa(i)+"/7", ""))
			if code, err := exploreRead(br); err != nil || code != 200 {
				b.Fatalf("%d %v", code, err)
			}
		}
		scrape := getReq("/metrics", "")
		c.Write(scrape)
		if code, err := exploreRead(br); err != nil || code != 200 {
			b.Fatalf("scrape: %d %v", code, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Write(scrape)
			if _, err := exploreRead(br); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		var w exploreCount
		mm.WriteTo(&w)
		b.ReportMetric(float64(w.n), "scrape_bytes")
	})

	b.Run("ProxyProto_v1_parse", func(b *testing.B) {
		in := []byte("PROXY TCP4 198.51.100.9 203.0.113.1 5123 443\r\nGET / HTTP/1.1\r\n")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if h, err := proxyproto.Parse(in); err != nil || !h.Named {
				b.Fatal(err)
			}
		}
	})
	b.Run("ProxyProto_v2_parse", func(b *testing.B) {
		in := exploreProxyV2()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if h, err := proxyproto.Parse(in); err != nil || !h.Named {
				b.Fatal(err)
			}
		}
	})

	tlsRig := func(b *testing.B) (string, *tls.Config) {
		cfg, pool := exploreTLSConfig(b)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			b.Skip(err)
		}
		app := New(WithLogger(quiet()))
		app.Post("/users/{id}", budgetHandler)
		go app.ServeTLS(ln, cfg)
		b.Cleanup(func() { app.Close() })
		return ln.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "x.test"}
	}
	b.Run("TLS_request_established", func(b *testing.B) {
		addr, ccfg := tlsRig(b)
		c, err := tls.Dial("tcp", addr, ccfg)
		if err != nil {
			b.Fatal(err)
		}
		defer c.Close()
		br := bufio.NewReaderSize(c, 8192)
		req := post(target, typedBody)
		c.Write(req)
		if code, err := exploreRead(br); err != nil || code != 200 {
			b.Fatalf("%d %v", code, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c.Write(req)
			if _, err := exploreRead(br); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, resume := range []bool{false, true} {
		name := "TLS_handshake_full"
		if resume {
			name = "TLS_handshake_resumed"
		}
		b.Run(name, func(b *testing.B) {
			addr, ccfg := tlsRig(b)
			if resume {
				ccfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
			}
			req := []byte(strings.Replace(string(post(target, typedBody)), "Host: x\r\n", "Host: x\r\nConnection: close\r\n", 1))
			one := func() {
				c, err := tls.Dial("tcp", addr, ccfg)
				if err != nil {
					b.Fatal(err)
				}
				c.Write(req)
				br := bufio.NewReaderSize(c, 4096)
				if code, err := exploreRead(br); err != nil || code != 200 {
					b.Fatalf("%d %v", code, err)
				}
				io.Copy(io.Discard, br)
				c.Close()
			}
			one()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				one()
			}
			b.StopTimer()
			if resume {
				c, err := tls.Dial("tcp", addr, ccfg)
				if err == nil {
					b.ReportMetric(map[bool]float64{true: 1, false: 0}[c.ConnectionState().DidResume], "resumed")
					c.Close()
				}
			}
		})
	}
}
