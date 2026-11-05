// Probe: the server-side error string of each way a TLS handshake fails, to see which ones point at
// the operator's certificate setup and which are scanner noise.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

func mk(cn string, nb, na time.Time) tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	t := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn}, NotBefore: nb, NotAfter: na}
	der, _ := x509.CreateCertificate(rand.Reader, t, t, &k.PublicKey, k)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func serve(cfg *tls.Config, client func(addr string)) string {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	res := make(chan string, 1)
	go func() {
		nc, _ := ln.Accept()
		nc.SetDeadline(time.Now().Add(700 * time.Millisecond))
		err := tls.Server(nc, cfg).Handshake()
		var oe *net.OpError
		kind := ""
		if errors.As(err, &oe) && oe.Op == "remote error" {
			kind = " [client alert]"
		}
		res <- fmt.Sprintf("%v%s", err, kind)
		nc.Close()
	}()
	client(ln.Addr().String())
	return <-res
}

func main() {
	now := time.Now()
	good := mk("x.test", now.Add(-time.Hour), now.Add(time.Hour))
	expired := mk("x.test", now.Add(-2*time.Hour), now.Add(-time.Hour))
	srv := func(c tls.Certificate) *tls.Config { return &tls.Config{Certificates: []tls.Certificate{c}} }
	verify := func(addr string, name string) { // verifying client, self-signed server cert = unknown authority
		nc, _ := net.Dial("tcp", addr)
		tls.Client(nc, &tls.Config{ServerName: name}).Handshake()
		nc.Close()
	}
	type row struct {
		name string
		cfg  *tls.Config
		cl   func(string)
	}
	rows := []row{
		{"plain HTTP sent to the TLS port", srv(good), func(a string) { nc, _ := net.Dial("tcp", a); nc.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); time.Sleep(100 * time.Millisecond); nc.Close() }},
		{"connect and close (port scan)", srv(good), func(a string) { nc, _ := net.Dial("tcp", a); nc.Close() }},
		{"connect and say nothing (timeout)", srv(good), func(a string) { nc, _ := net.Dial("tcp", a); time.Sleep(900 * time.Millisecond); nc.Close() }},
		{"client rejects our cert: untrusted (self-signed/private CA)", srv(good), func(a string) { verify(a, "x.test") }},
		{"client rejects our cert: expired", srv(expired), func(a string) { verify(a, "x.test") }},
		{"client rejects our cert: wrong name", srv(good), func(a string) { verify(a, "other.test") }},
		{"GetCertificate returns an error (no cert for SNI)", &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("no cert for host") }},
			func(a string) { nc, _ := net.Dial("tcp", a); tls.Client(nc, &tls.Config{InsecureSkipVerify: true, ServerName: "x.test"}).Handshake(); nc.Close() }},
		{"TLS 1.0 only client", srv(good), func(a string) { nc, _ := net.Dial("tcp", a); tls.Client(nc, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS10, MinVersion: tls.VersionTLS10}).Handshake(); nc.Close() }},
	}
	for _, r := range rows {
		fmt.Printf("%-62s -> %s\n", r.name, serve(r.cfg, r.cl))
	}
}
