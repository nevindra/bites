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
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The idle-memory gate of ADR 0033, through the whole app: the engine's own test holds a bare
// handler, this one holds a typed route, so anything Registration or the framework adds to a
// connection (a field, a pool, a goroutine) shows here.

type Ping struct {
	Path  struct{ ID int64 }
	Query struct{ Q Opt[string] }
}

type Pong struct {
	ID int64 `json:"id"`
}

const idleRequest = "GET /ping/7?q=x HTTP/1.1\r\nHost: x\r\n\r\n"

var lastStacks uint64

func heapAndStack() uint64 {
	runtime.GC()
	runtime.GC()
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}}
	metrics.Read(s)
	lastStacks = s[1].Value.Uint64()
	return s[0].Value.Uint64() + s[1].Value.Uint64()
}

// settle is how long the test waits after opening connections: past the engine's 100 ms, so every
// idle connection has parked, or with WithoutParking waits on a fresh goroutine's stack (ADR 0005).
// tlsIdleCeiling is 20% over the 11.9 KB measured in docs/research/tls-idle (ADR 0017), for a
// connection kept on a goroutine (WithoutParking).
const tlsIdleCeiling uint64 = 14500

// The ceilings of a parked connection (ADR 0005): 20% over what this test measured when parking
// was built (1,170 B plain and 7,606 B over TLS, docs/research/parking).
const (
	parkedIdleCeiling    uint64 = 1400
	parkedTLSIdleCeiling uint64 = 9100
)

const settle = 300 * time.Millisecond

// TestIdleClientHelper is the client half, run as a child process so the client's own sockets are
// not counted against the server.
func TestIdleClientHelper(t *testing.T) {
	addr := os.Getenv("BITES_IDLE_ADDR")
	if addr == "" {
		t.Skip("helper for TestTheMarginalHeapAndStackOfAnIdleConnectionThroughTheWholeApp")
	}
	var conns []net.Conn
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		n, _ := strconv.Atoi(strings.TrimPrefix(in.Text(), "open "))
		for range n {
			var c net.Conn
			var err error
			if os.Getenv("BITES_IDLE_TLS") != "" {
				c, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
			} else {
				c, err = net.Dial("tcp", addr)
			}
			if err != nil {
				fmt.Println("error", err)
				return
			}
			if os.Getenv("BITES_IDLE_PROXY") != "" {
				c.Write([]byte("PROXY TCP4 198.51.100.9 127.0.0.1 5123 443\r\n" + idleRequest))
			} else {
				c.Write([]byte(idleRequest))
			}
			b := make([]byte, 1024)
			got := 0
			for !strings.HasSuffix(string(b[:got]), "}") {
				m, err := c.Read(b[got:])
				if err != nil {
					fmt.Println("error", err)
					return
				}
				got += m
			}
			conns = append(conns, c)
		}
		fmt.Println("ready")
	}
}

func TestTheMarginalHeapAndStackOfAnIdleConnectionThroughTheWholeAppStaysUnderItsCeiling(t *testing.T) {
	t.Run("parked", func(t *testing.T) { idleMarginal(t, false, false, false, parkedIdleCeiling) })
	t.Run("without parking", func(t *testing.T) { idleMarginal(t, false, true, false, 4500) })
}

// The same gate for a connection whose listener reads the PROXY protocol (ADR 0058): what it adds
// is the 80-byte proxyConn, within the same ceiling.
func TestTheMarginalHeapAndStackOfAnIdlePROXYProtocolConnectionStaysUnderItsCeiling(t *testing.T) {
	t.Run("parked", func(t *testing.T) { idleMarginal(t, false, false, true, parkedIdleCeiling) })
}

// The same gate over TLS (ADR 0017): every connection has done its handshake and been served one
// request, so what is measured is what a TLS connection holds while idle, crypto/tls state included.
func TestTheMarginalHeapAndStackOfAnIdleTLSConnectionStaysUnderItsCeiling(t *testing.T) {
	t.Run("parked", func(t *testing.T) { idleMarginal(t, true, false, false, parkedTLSIdleCeiling) })
	t.Run("without parking", func(t *testing.T) { idleMarginal(t, true, true, false, tlsIdleCeiling) })
}

func idleMarginal(t *testing.T, useTLS, noPark, proxy bool, ceiling uint64) {
	if runtime.GOOS != "linux" || testing.Short() || raceEnabled {
		t.Skip("Linux, not -short, not -race")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	opts := []Option{WithLogger(quiet())}
	if noPark {
		opts = append(opts, WithoutParking())
	}
	var lopts []ListenOption
	if proxy {
		opts = append(opts, WithTrustedProxies(TrustedProxies{Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}))
		lopts = append(lopts, ProxyProtocol())
	}
	app := New(opts...)
	app.Get("/ping/{id}", func(_ context.Context, in Ping) (Pong, error) { return Pong{ID: in.Path.ID}, nil })
	if useTLS {
		cfg := serverTLSConfig(t)
		go app.ServeTLS(ln, cfg, lopts...)
	} else {
		go app.Serve(ln, lopts...)
	}
	defer app.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestIdleClientHelper$")
	cmd.Env = append(os.Environ(), "BITES_IDLE_ADDR="+ln.Addr().String())
	if useTLS {
		cmd.Env = append(cmd.Env, "BITES_IDLE_TLS=1")
	}
	if proxy {
		cmd.Env = append(cmd.Env, "BITES_IDLE_PROXY=1")
	}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	out := bufio.NewScanner(stdout)
	open := func(n int) {
		fmt.Fprintf(stdin, "open %d\n", n)
		for out.Scan() {
			if out.Text() == "ready" {
				return
			}
			if strings.HasPrefix(out.Text(), "error") {
				t.Fatal(out.Text())
			}
		}
		t.Fatal("client died")
	}
	open(200) // warm the pool, the poller and the maps
	time.Sleep(settle)
	a := heapAndStack()
	open(1000)
	time.Sleep(settle)
	b := heapAndStack()
	sb := lastStacks
	open(1000)
	time.Sleep(settle)
	c := heapAndStack()
	t.Logf("stacks alone: %d per connection (marginal 1200 to 2200)", (int64(lastStacks)-int64(sb))/1000)
	t.Logf("heap+stack at 200, 1200, 2200 connections: %d, %d, %d bytes", a, b, c)
	t.Logf("marginal bytes per idle connection through the whole app: %d (200 to 1200), %d (1200 to 2200); ADR 0005: 3.1 to 3.5 KB for the bare engine",
		(b-a)/1000, (c-b)/1000)
	if per := (c - b) / 1000; per > ceiling {
		t.Fatalf("an idle connection costs %d B heap+stack, over the %d B ceiling", per, ceiling)
	}
}

// serverTLSConfig is a self-signed ecdsa P-256 certificate made for the test; the client helper
// does not verify it, so the handshake costs what a real one does and nothing more.
func serverTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bites.test"}, DNSNames: []string{"bites.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}
