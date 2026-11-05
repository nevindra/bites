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
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/bites"
)

// mTLS (ADR 0066) and certificate reload (ADR 0067), over loopback TCP with certificates made
// here.

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var serial atomic.Int64

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// issue makes a leaf for cn, a server's (with bites.test as its name) or a client's, valid until
// notAfter, and returns it with its PEM files' contents.
func (ca *testCA) issue(t *testing.T, cn string, server bool, notAfter time.Time) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial.Add(1)), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if server {
		tmpl.ExtKeyUsage, tmpl.DNSNames = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []string{"bites.test"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return c, certPEM, keyPEM
}

// serveTLSApp serves app over loopback TLS and stops it when the test ends.
func serveTLSApp(t *testing.T, app *bites.App, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	go app.ServeTLS(ln, cfg)
	t.Cleanup(func() { app.Close() })
	return ln.Addr().String()
}

// tlsGet sends one GET over a new TLS connection and returns the status line and body.
func tlsGet(addr string, cfg *tls.Config, path string) (string, string, error) {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", path)
	b, err := io.ReadAll(bufio.NewReader(c))
	if err != nil && len(b) == 0 {
		return "", "", err
	}
	head, body, _ := strings.Cut(string(b), "\r\n\r\n")
	status, _, _ := strings.Cut(head, "\r\n")
	return status, body, nil
}

type Peer struct{ CN string }

type WhoIn struct{ Peer Peer }

func peerProvider(_ context.Context, req bites.Request, _ struct{}) (Peer, error) {
	chains := req.VerifiedChains()
	if len(chains) == 0 {
		return Peer{}, bites.Unauthorized("a client certificate is required")
	}
	return Peer{CN: chains[0][0].Subject.CommonName}, nil
}

func mtlsApp(t *testing.T) *bites.App {
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Provide(peerProvider)
	app.Get("/who", func(_ context.Context, in WhoIn) (bites.Text, error) { return bites.Text{Body: in.Peer.CN}, nil })
	return app
}

func TestAProviderReadsTheVerifiedClientCertificateOfAnMTLSConnection(t *testing.T) {
	serverCA, clientCA, otherCA := newCA(t, "server CA"), newCA(t, "client CA"), newCA(t, "other CA")
	srvCert, _, _ := serverCA.issue(t, "server", true, time.Now().Add(time.Hour))
	alice, _, _ := clientCA.issue(t, "alice", false, time.Now().Add(time.Hour))
	mallory, _, _ := otherCA.issue(t, "mallory", false, time.Now().Add(time.Hour))
	addr := serveTLSApp(t, mtlsApp(t), &tls.Config{
		Certificates: []tls.Certificate{srvCert}, ClientCAs: clientCA.pool(), ClientAuth: tls.VerifyClientCertIfGiven,
	})
	client := func(certs ...tls.Certificate) *tls.Config {
		return &tls.Config{RootCAs: serverCA.pool(), ServerName: "bites.test", Certificates: certs}
	}
	if status, body, err := tlsGet(addr, client(alice), "/who"); err != nil || !strings.Contains(status, " 200 ") || body != "alice" {
		t.Fatalf("alice: %q %q %v", status, body, err)
	}
	if status, _, err := tlsGet(addr, client(), "/who"); err != nil || !strings.Contains(status, " 401 ") {
		t.Fatalf("no certificate: %q %v", status, err)
	}
	// A client sends only a certificate from a CA the server names, so mallory's is forced on it.
	forced := client()
	forced.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &mallory, nil }
	if status, _, err := tlsGet(addr, forced, "/who"); err == nil && strings.Contains(status, " 200 ") {
		t.Fatalf("a certificate from a CA not in ClientCAs was accepted: %q", status)
	}
}

func TestACertificateAskedForButNotVerifiedIsNeverReturned(t *testing.T) {
	serverCA, clientCA := newCA(t, "server CA"), newCA(t, "client CA")
	srvCert, _, _ := serverCA.issue(t, "server", true, time.Now().Add(time.Hour))
	alice, _, _ := clientCA.issue(t, "alice", false, time.Now().Add(time.Hour))
	addr := serveTLSApp(t, mtlsApp(t), &tls.Config{Certificates: []tls.Certificate{srvCert}, ClientAuth: tls.RequireAnyClientCert})
	status, _, err := tlsGet(addr, &tls.Config{RootCAs: serverCA.pool(), ServerName: "bites.test", Certificates: []tls.Certificate{alice}}, "/who")
	if err != nil || !strings.Contains(status, " 401 ") {
		t.Fatalf("an unverified certificate: %q %v", status, err)
	}
}

func TestVerifiedChainsIsNilOnAPlainConnectionAndAfterTheRequestWasAnswered(t *testing.T) {
	var kept bites.Request
	srv := start(t, func(a *bites.App) {
		a.Use(func(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
			if req.VerifiedChains() != nil {
				t.Error("chains on a plain connection")
			}
			kept = req
			return next.Call(ctx), nil
		})
		a.Get("/x", ok[Empty])
	})
	get(t, srv, "/x")
	if kept.VerifiedChains() != nil {
		t.Fatal("chains after the request was answered")
	}
}

func writeCertFiles(t *testing.T, dir string, certPEM, keyPEM []byte) (string, string) {
	t.Helper()
	cf, kf := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	os.WriteFile(cf, certPEM, 0o600)
	os.WriteFile(kf, keyPEM, 0o600)
	return cf, kf
}

func servedSerial(t *testing.T, addr string, roots *x509.CertPool) *big.Int {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "bites.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].SerialNumber
}

func TestCertsReloadPutsTheNewCertificateInForceForTheNextHandshake(t *testing.T) {
	ca := newCA(t, "CA")
	first, c1, k1 := ca.issue(t, "server", true, time.Now().Add(time.Hour))
	second, c2, k2 := ca.issue(t, "server", true, time.Now().Add(2*time.Hour))
	cf, kf := writeCertFiles(t, t.TempDir(), c1, k1)
	certs, err := bites.LoadCerts(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/x", ok[Empty])
	addr := serveTLSApp(t, app, &tls.Config{GetCertificate: certs.GetCertificate})
	if got := servedSerial(t, addr, ca.pool()); got.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatalf("serial %v", got)
	}
	os.WriteFile(cf, c2, 0o600)
	os.WriteFile(kf, k2, 0o600)
	if err := certs.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := servedSerial(t, addr, ca.pool()); got.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Fatalf("after Reload, serial %v", got)
	}
}

func TestAReloadThatFailsReturnsItsErrorAndTheCertificateInForceGoesOnServing(t *testing.T) {
	ca := newCA(t, "CA")
	first, c1, k1 := ca.issue(t, "server", true, time.Now().Add(time.Hour))
	_, c2, _ := ca.issue(t, "server", true, time.Now().Add(time.Hour))
	_, cOld, kOld := ca.issue(t, "server", true, time.Now().Add(-time.Hour))
	dir := t.TempDir()
	cf, kf := writeCertFiles(t, dir, c1, k1)
	certs, err := bites.LoadCerts(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/x", ok[Empty])
	addr := serveTLSApp(t, app, &tls.Config{GetCertificate: certs.GetCertificate})
	for _, tc := range []struct {
		name       string
		cert, key  []byte
		wantInText string
	}{
		{"a half-written certificate", c1[:len(c1)/2], k1, "the certificate in force is unchanged"},
		{"a key that is not the certificate's", c2, k1, "the certificate in force is unchanged"},
		{"an expired certificate", cOld, kOld, "expired"},
	} {
		writeCertFiles(t, dir, tc.cert, tc.key)
		err := certs.Reload()
		if err == nil || !strings.Contains(err.Error(), tc.wantInText) {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got := servedSerial(t, addr, ca.pool()); got.Cmp(first.Leaf.SerialNumber) != 0 {
			t.Fatalf("%s: the certificate in force changed", tc.name)
		}
	}
	os.Remove(cf)
	if err := certs.Reload(); err == nil {
		t.Fatal("a missing file reloaded")
	}
}

func TestAZeroCertsFailsEveryHandshakeUntilSetAndCannotReload(t *testing.T) {
	var certs bites.Certs
	if _, err := certs.GetCertificate(nil); err == nil {
		t.Fatal("a certificate from the zero Certs")
	}
	if err := certs.Reload(); !errors.Is(err, bites.ErrNoCertFiles) {
		t.Fatalf("Reload: %v", err)
	}
	ca := newCA(t, "CA")
	c, _, _ := ca.issue(t, "server", true, time.Now().Add(time.Hour))
	if err := certs.Set(c); err != nil {
		t.Fatal(err)
	}
	if got, err := certs.GetCertificate(nil); err != nil || got.Leaf.SerialNumber.Cmp(c.Leaf.SerialNumber) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if err := certs.Set(tls.Certificate{}); err == nil {
		t.Fatal("an empty certificate was put in force")
	}
}

func TestHandshakesDuringReloadsEachGetAWholeCertificate(t *testing.T) {
	ca := newCA(t, "CA")
	pairs := make([][2][]byte, 4)
	for i := range pairs {
		_, c, k := ca.issue(t, "server", true, time.Now().Add(time.Hour))
		pairs[i] = [2][]byte{c, k}
	}
	dir := t.TempDir()
	cf, kf := writeCertFiles(t, dir, pairs[0][0], pairs[0][1])
	certs, err := bites.LoadCerts(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/x", ok[Empty])
	addr := serveTLSApp(t, app, &tls.Config{GetCertificate: certs.GetCertificate})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := pairs[i%len(pairs)]
			// Written to new files and renamed, as a certificate manager does, so a reload never
			// reads a half-written pair.
			os.WriteFile(cf+".new", p[0], 0o600)
			os.WriteFile(kf+".new", p[1], 0o600)
			os.Rename(cf+".new", cf)
			os.Rename(kf+".new", kf)
			certs.Reload()
		}
	}()
	var failed atomic.Int64
	var hs sync.WaitGroup
	for range 8 {
		hs.Add(1)
		go func() {
			defer hs.Done()
			for range 10 {
				if status, _, err := tlsGet(addr, &tls.Config{RootCAs: ca.pool(), ServerName: "bites.test"}, "/x"); err != nil || !strings.Contains(status, " 200 ") {
					failed.Add(1)
				}
			}
		}()
	}
	hs.Wait()
	close(stop)
	wg.Wait()
	if n := failed.Load(); n != 0 {
		t.Fatalf("%d of 80 requests failed while the certificate was reloaded", n)
	}
}

func TestAnAppWithAcmeTLSAndGetCertificateAnswersTheChallengeAndServesHTTP11(t *testing.T) {
	ca := newCA(t, "CA")
	certs := &bites.Certs{}
	c, _, _ := ca.issue(t, "server", true, time.Now().Add(time.Hour))
	certs.Set(c)
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Get("/x", ok[Empty])
	addr := serveTLSApp(t, app, &tls.Config{GetCertificate: certs.GetCertificate, NextProtos: []string{"http/1.1", "acme-tls/1"}})
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: ca.pool(), ServerName: "bites.test", NextProtos: []string{"acme-tls/1"}})
	if err != nil {
		t.Fatal(err)
	}
	if p := conn.ConnectionState().NegotiatedProtocol; p != "acme-tls/1" {
		t.Fatalf("negotiated %q", p)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("the challenge connection was not closed: %d %v", n, err)
	}
	conn.Close()
	if st, _, err := tlsGet(addr, &tls.Config{RootCAs: ca.pool(), ServerName: "bites.test", NextProtos: []string{"h2", "http/1.1"}}, "/x"); err != nil || !strings.Contains(st, " 200 ") {
		t.Fatalf("%q %v", st, err)
	}
}
