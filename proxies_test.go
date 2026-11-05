package bites_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// whoIs answers with the ClientAddr and Scheme the app sees, as response headers.
func whoIs(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	res := next.Call(ctx)
	res.AddHeader("X-Client", req.ClientAddr().String())
	res.AddHeader("X-Scheme", req.Scheme())
	return res, nil
}

func proxied(t *testing.T, opts ...bites.Option) *bitetest.Server {
	t.Helper()
	return startWith(t, func(app *bites.App) {
		app.Use(whoIs)
		app.Get("/users/{id}", getUser)
	}, opts...)
}

var balancers = bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:ff::/48"),
}})

func fromPeer(peer string, h ...string) bitetest.Request {
	r := bitetest.Request{Method: "GET", Target: "/users/7", Remote: peer, Header: map[string][]string{}}
	for i := 0; i+1 < len(h); i += 2 {
		r.Header[h[i]] = append(r.Header[h[i]], h[i+1])
	}
	return r
}

func TestWithNoTrustedProxyTheClientAddrIsThePeerAndForwardingHeadersAreIgnored(t *testing.T) {
	srv := proxied(t)
	r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-For", "198.51.100.9", "X-Forwarded-Proto", "https"))
	if r.Get("x-client") != "10.0.0.5" || r.Get("x-scheme") != "http" {
		t.Fatalf("%v", r.Header)
	}
}

func TestAClientTalkingDirectlyCannotNameAnotherAddressWhateverItSends(t *testing.T) {
	srv := proxied(t, balancers)
	r := srv.Do(fromPeer("203.0.113.9:4000", "X-Forwarded-For", "10.0.0.1", "X-Forwarded-Proto", "https"))
	if r.Get("x-client") != "203.0.113.9" || r.Get("x-scheme") != "http" {
		t.Fatalf("an untrusted peer named its own address or scheme: %v", r.Header)
	}
}

func TestBehindATrustedBalancerTheClientAddrIsTheRightmostUntrustedElementNotTheLeftmostLie(t *testing.T) {
	srv := proxied(t, balancers)
	for _, tc := range []struct{ xff, want string }{
		{"1.2.3.4, 198.51.100.9", "198.51.100.9"},
		{"1.2.3.4, 198.51.100.9, 10.1.1.1", "198.51.100.9"},
		{"198.51.100.9:5123, 10.1.1.1", "198.51.100.9"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"2001:db8::7, 2001:db8:ff::2", "2001:db8::7"},
		{"::ffff:198.51.100.9", "198.51.100.9"},
		{"10.2.2.2, 10.1.1.1", "10.2.2.2"},
		{"198.51.100.9, unknown, 10.1.1.1", "10.1.1.1"},
		{"198.51.100.9, not-an-ip", "10.0.0.5"},
		{"198.51.100.9,, 10.1.1.1", "198.51.100.9"},
	} {
		r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-For", tc.xff))
		if got := r.Get("x-client"); got != tc.want {
			t.Errorf("X-Forwarded-For %q: ClientAddr %s, want %s", tc.xff, got, tc.want)
		}
	}
}

func TestEveryLineOfARepeatedForwardedForIsReadWithTheLastLineRightmost(t *testing.T) {
	srv := proxied(t, balancers)
	c := srv.Dial("10.0.0.5:4000")
	defer c.Close()
	c.Write([]byte("GET /users/7 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 198.51.100.1\r\nX-Forwarded-For: 198.51.100.2, 10.3.3.3\r\n\r\n"))
	if r := srv.Read(c, false); r.Get("x-client") != "198.51.100.2" {
		t.Fatalf("%v", r.Header)
	}
	c.Write([]byte("GET /users/7 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 198.51.100.1\r\nX-Forwarded-For: 10.3.3.3\r\n\r\n"))
	if r := srv.Read(c, false); r.Get("x-client") != "198.51.100.1" {
		t.Fatalf("the walk did not go on to the earlier line: %v", r.Header)
	}
}

func TestATrustedPeerWithNoForwardingHeaderIsItsOwnClient(t *testing.T) {
	srv := proxied(t, balancers)
	if r := srv.Do(fromPeer("10.0.0.5:4000")); r.Get("x-client") != "10.0.0.5" {
		t.Fatalf("%v", r.Header)
	}
}

func TestASingleAddressHeaderIsReadOnlyFromATrustedPeer(t *testing.T) {
	srv := proxied(t, bites.WithTrustedProxies(bites.TrustedProxies{
		Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, Header: "CF-Connecting-IP",
	}))
	for _, tc := range []struct{ peer, value, want string }{
		{"10.0.0.5:4000", "198.51.100.9", "198.51.100.9"},
		{"10.0.0.5:4000", "garbage", "10.0.0.5"},
		{"203.0.113.9:4000", "198.51.100.9", "203.0.113.9"},
	} {
		r := srv.Do(fromPeer(tc.peer, "CF-Connecting-IP", tc.value, "X-Forwarded-For", "192.0.2.1"))
		if got := r.Get("x-client"); got != tc.want {
			t.Errorf("peer %s, CF-Connecting-IP %q: %s, want %s", tc.peer, tc.value, got, tc.want)
		}
	}
}

func TestTheSchemeIsTheForwardedProtoOfATrustedPeerAndHttpOtherwise(t *testing.T) {
	srv := proxied(t, balancers)
	for _, tc := range []struct{ peer, proto, want string }{
		{"10.0.0.5:4000", "https", "https"},
		{"10.0.0.5:4000", "HTTPS", "https"},
		{"10.0.0.5:4000", "http", "http"},
		{"10.0.0.5:4000", "http, https", "https"},
		{"10.0.0.5:4000", "gopher", "http"},
		{"203.0.113.9:4000", "https", "http"},
	} {
		r := srv.Do(fromPeer(tc.peer, "X-Forwarded-Proto", tc.proto))
		if got := r.Get("x-scheme"); got != tc.want {
			t.Errorf("peer %s, X-Forwarded-Proto %q: %s, want %s", tc.peer, tc.proto, got, tc.want)
		}
	}
}

func TestAPrefixWithHostBitsSetIsMaskedNotRefused(t *testing.T) {
	srv := proxied(t, bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.9.9.9/8")}}))
	if r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-For", "198.51.100.9")); r.Get("x-client") != "198.51.100.9" {
		t.Fatalf("%v", r.Header)
	}
}

func TestARequestKeptPastItsAnswerHasNoClientAddrAndNoScheme(t *testing.T) {
	var kept bites.Request
	srv := startWith(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			kept = req
			return next.Call(ctx), nil
		})
		app.Get("/users/{id}", getUser)
	}, balancers)
	srv.Do(fromPeer("10.0.0.5:4000"))
	if kept.ClientAddr().IsValid() || kept.Scheme() != "" {
		t.Fatalf("a kept Request still answers: %v %q", kept.ClientAddr(), kept.Scheme())
	}
}

func TestTheSchemeOfARequestOverTLSIsHttpsWhateverAForwardedProtoSays(t *testing.T) {
	scfg, ccfg := tlsConfigs(t)
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}))
	app.Use(whoIs)
	app.Get("/users/{id}", getUser)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	go app.ServeTLS(ln, scfg)
	defer app.Close()
	c, err := tls.Dial("tcp", ln.Addr().String(), ccfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /users/1 HTTP/1.1\r\nHost: x\r\nX-Forwarded-Proto: http\r\n\r\n")
	br := bufio.NewReader(c)
	var scheme string
	for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
		if v, ok := strings.CutPrefix(strings.ToLower(l), "x-scheme: "); ok {
			scheme = strings.TrimSpace(v)
		}
	}
	if scheme != "https" {
		t.Fatalf("Scheme over TLS = %q, want https", scheme)
	}
}

// rawAnswer writes send and reads one answer's status line and the headers whoIs added.
func rawAnswer(t *testing.T, c net.Conn, send string) (client, scheme, status string) {
	t.Helper()
	if _, err := io.WriteString(c, send); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	status = strings.TrimSpace(line)
	for l, _ := br.ReadString('\n'); l != "\r\n" && l != ""; l, _ = br.ReadString('\n') {
		if v, ok := strings.CutPrefix(strings.ToLower(l), "x-client: "); ok {
			client = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(strings.ToLower(l), "x-scheme: "); ok {
			scheme = strings.TrimSpace(v)
		}
	}
	return client, scheme, status
}

func TestBehindAPROXYProtocolBalancerTheClientItNamesIsThePeerAndItsOwnForwardingHeadersAreNotBelieved(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}))
	app.Use(whoIs)
	app.Get("/users/{id}", getUser)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	go app.Serve(ln, bites.ProxyProtocol())
	defer app.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// An L4 balancer forwards the client's bytes untouched: the X-Forwarded-For below is the
	// client's own, and the client is not a trusted proxy, so it is not read (ADR 0025).
	client, scheme, status := rawAnswer(t, c, "PROXY TCP4 198.51.100.9 127.0.0.1 5123 443\r\nGET /users/1 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 10.9.9.9\r\nX-Forwarded-Proto: https\r\n\r\n")
	if status != "HTTP/1.1 200 OK" || client != "198.51.100.9" || scheme != "http" {
		t.Fatalf("%s: ClientAddr %q, Scheme %q; want the named client and http", status, client, scheme)
	}
}

func TestAUnixSocketPeerIsATrustedProxyOnlyWithUnixSockets(t *testing.T) {
	for _, tc := range []struct {
		name string
		tp   bites.TrustedProxies
		want string
	}{
		{"UnixSockets set", bites.TrustedProxies{UnixSockets: true}, "198.51.100.9"},
		{"only Prefixes", bites.TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, "invalid ip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), bites.WithTrustedProxies(tc.tp))
			app.Use(whoIs)
			app.Get("/users/{id}", getUser)
			path := t.TempDir() + "/app.sock"
			go app.ListenUnix(path, 0o600)
			defer app.Close()
			var c net.Conn
			var err error
			for range 200 {
				if c, err = net.Dial("unix", path); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err != nil {
				t.Skip("no Unix socket:", err)
			}
			defer c.Close()
			client, _, _ := rawAnswer(t, c, "GET /users/1 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 198.51.100.9\r\n\r\n")
			if client != tc.want {
				t.Fatalf("ClientAddr %q, want %q", client, tc.want)
			}
		})
	}
}

func TestAPROXYHeaderOnATrustedUnixSocketNamesTheClient(t *testing.T) {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		bites.WithTrustedProxies(bites.TrustedProxies{UnixSockets: true}))
	app.Use(whoIs)
	app.Get("/users/{id}", getUser)
	path := t.TempDir() + "/app.sock"
	go app.ListenUnix(path, 0o600, bites.ProxyProtocol())
	defer app.Close()
	var c net.Conn
	var err error
	for range 200 {
		if c, err = net.Dial("unix", path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Skip("no Unix socket:", err)
	}
	defer c.Close()
	client, _, _ := rawAnswer(t, c, "PROXY TCP6 2001:db8::9 2001:db8::1 5123 443\r\nGET /users/1 HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 10.9.9.9\r\n\r\n")
	if client != "2001:db8::9" {
		t.Fatalf("ClientAddr %q, want the client the header named, whose own X-Forwarded-For is not believed", client)
	}
}

// whereIs answers with the PublicHost, PublicPrefix and PublicURL the app sees, as response
// headers.
func whereIs(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	res := next.Call(ctx)
	res.AddHeader("X-Host", req.PublicHost())
	res.AddHeader("X-Prefix", req.PublicPrefix())
	res.AddHeader("X-URL", req.PublicURL("/rooms/7"))
	return res, nil
}

func located(t *testing.T, opts ...bites.Option) *bitetest.Server {
	t.Helper()
	return startWith(t, func(app *bites.App) {
		app.Use(whereIs)
		app.Get("/users/{id}", getUser)
	}, opts...)
}

// rewriters are balancers that write X-Forwarded-Host and X-Forwarded-Prefix.
var rewriters = bites.WithTrustedProxies(bites.TrustedProxies{
	Prefixes:      []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	ForwardedHost: true, ForwardedPrefix: true,
})

func TestAClientTalkingDirectlyCannotNameThePublicHostOrPrefixWhateverItSends(t *testing.T) {
	for name, opts := range map[string][]bites.Option{"no trusted proxy": nil, "an untrusted peer": {rewriters}} {
		srv := located(t, opts...)
		r := srv.Do(fromPeer("203.0.113.9:4000", "X-Forwarded-Host", "evil.example", "X-Forwarded-Prefix", "/evil", "X-Forwarded-Proto", "https"))
		if r.Get("x-host") != "test" || r.Get("x-prefix") != "" || r.Get("x-url") != "http://test/rooms/7" {
			t.Errorf("%s: a client named its own host or prefix: %v", name, r.Header)
		}
	}
}

func TestAForwardedHostAndPrefixAreReadOnlyWhenTheAppSaysItsProxiesWriteThem(t *testing.T) {
	srv := located(t, balancers)
	r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Host", "evil.example", "X-Forwarded-Prefix", "/evil"))
	if r.Get("x-host") != "test" || r.Get("x-prefix") != "" {
		t.Fatalf("a balancer trusted for X-Forwarded-For only passed the client's host or prefix through: %v", r.Header)
	}
}

func TestBehindAProxyThatStripsAPrefixThePublicURLHasTheForwardedSchemeHostAndPrefix(t *testing.T) {
	srv := located(t, rewriters)
	r := srv.Do(fromPeer("10.0.0.5:4000", "Host", "chat.internal:8080", "X-Forwarded-Host", "api.example.com",
		"X-Forwarded-Prefix", "/api", "X-Forwarded-Proto", "https"))
	if r.Get("x-host") != "api.example.com" || r.Get("x-prefix") != "/api" || r.Get("x-url") != "https://api.example.com/api/rooms/7" {
		t.Fatalf("%v", r.Header)
	}
	r = srv.Do(fromPeer("10.0.0.5:4000", "Host", "chat.internal:8080"))
	if r.Get("x-host") != "chat.internal:8080" || r.Get("x-prefix") != "" || r.Get("x-url") != "http://chat.internal:8080/rooms/7" {
		t.Fatalf("a trusted proxy that writes neither header: %v", r.Header)
	}
}

func TestOfTwoProxiesTheForwardedHostAndPrefixAreTheElementsTheNearestWrote(t *testing.T) {
	srv := located(t, rewriters)
	r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Host", "evil.example, api.example.com", "X-Forwarded-Prefix", "/evil, /api/"))
	if r.Get("x-host") != "api.example.com" || r.Get("x-prefix") != "/api" {
		t.Fatalf("%v", r.Header)
	}
	c := srv.Dial("10.0.0.5:4000")
	defer c.Close()
	c.Write([]byte("GET /users/7 HTTP/1.1\r\nHost: x\r\nX-Forwarded-Host: evil.example\r\nX-Forwarded-Host: api.example.com\r\n\r\n"))
	if r := srv.Read(c, false); r.Get("x-host") != "api.example.com" {
		t.Fatalf("a repeated X-Forwarded-Host is read from its last line: %v", r.Header)
	}
}

func TestAnIPv6ForwardedHostKeepsItsBracketsAndPort(t *testing.T) {
	srv := located(t, rewriters)
	for _, h := range []string{"[2001:db8::1]", "[2001:db8::1]:8443", "192.0.2.7:8080", "API.Example.com"} {
		r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Host", h))
		if r.Get("x-host") != h || r.Get("x-url") != "http://"+h+"/rooms/7" {
			t.Errorf("X-Forwarded-Host %q: %v", h, r.Header)
		}
	}
}

func TestAForwardedHostThatIsNotAHostAnswersTheHostHeader(t *testing.T) {
	srv := located(t, rewriters)
	for _, h := range []string{
		"evil.example/path", "user@evil.example", "evil.example:99999", "evil.example:", "evil.example:80:80",
		"2001:db8::1", "[2001:db8::1", "[fe80::1%25eth0]", "[192.0.2.1]", "evil example", `evil.example\`, "",
	} {
		r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Host", h))
		if r.Get("x-host") != "test" {
			t.Errorf("X-Forwarded-Host %q was believed: %v", h, r.Header)
		}
	}
}

func TestAForwardedPrefixThatCouldNameAnotherHostOrClimbOutAnswersNoPrefix(t *testing.T) {
	srv := located(t, rewriters)
	for _, tc := range []struct{ prefix, want string }{
		{"/api", "/api"}, {"/api/", "/api"}, {"/", ""}, {"/a/b%20c", "/a/b%20c"},
		{"//evil.example", ""}, {`/\evil.example`, ""}, {"/a/../b", ""}, {"/a/%2e%2E", ""}, {"/a/.", ""},
		{"api", ""}, {"/a//b", ""}, {"/a?x=1", ""}, {"/a#x", ""}, {"/a%zz", ""}, {"/a b", ""}, {"http://evil.example", ""},
	} {
		r := srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Prefix", tc.prefix))
		if got := r.Get("x-prefix"); got != tc.want {
			t.Errorf("X-Forwarded-Prefix %q: PublicPrefix %q, want %q", tc.prefix, got, tc.want)
		}
	}
}

func TestARequestKeptPastItsAnswerHasNoPublicHostPrefixOrURL(t *testing.T) {
	var kept bites.Request
	srv := startWith(t, func(app *bites.App) {
		app.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			kept = req
			return next.Call(ctx), nil
		})
		app.Get("/users/{id}", getUser)
	}, rewriters)
	srv.Do(fromPeer("10.0.0.5:4000", "X-Forwarded-Host", "api.example.com", "X-Forwarded-Prefix", "/api"))
	if kept.PublicHost() != "" || kept.PublicPrefix() != "" || kept.PublicURL("/x") != "" {
		t.Fatalf("a kept Request still answers: %q %q %q", kept.PublicHost(), kept.PublicPrefix(), kept.PublicURL("/x"))
	}
}
