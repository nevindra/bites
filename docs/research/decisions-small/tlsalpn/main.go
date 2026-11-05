// Probe: what a tls-alpn-01 style client (offers only "acme-tls/1") gets from a server whose
// NextProtos is [http/1.1] (what the engine forces today) and from [http/1.1 acme-tls/1], and
// which protocol is picked when a client offers both and the server lists them in either order.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

func cert() tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	t := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x.test"}, DNSNames: []string{"x.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, t, t, &k.PublicKey, k)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func run(server []string, client []string) string {
	c := cert()
	sc := &tls.Config{NextProtos: server}
	called := false
	sc.GetCertificate = func(h *tls.ClientHelloInfo) (*tls.Certificate, error) { called = true; return &c, nil }
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	srvErr := make(chan string, 1)
	go func() {
		nc, _ := ln.Accept()
		tc := tls.Server(nc, sc)
		err := tc.Handshake()
		if err != nil {
			srvErr <- "server err: " + err.Error()
		} else {
			srvErr <- "server ok, negotiated " + fmt.Sprintf("%q", tc.ConnectionState().NegotiatedProtocol)
		}
		nc.Close()
	}()
	nc, _ := net.Dial("tcp", ln.Addr().String())
	cc := tls.Client(nc, &tls.Config{InsecureSkipVerify: true, ServerName: "x.test", NextProtos: client})
	err := cc.Handshake()
	res := "client ok " + fmt.Sprintf("%q", cc.ConnectionState().NegotiatedProtocol)
	if err != nil {
		res = "client err: " + err.Error()
	}
	nc.Close()
	return fmt.Sprintf("server=%v client=%v -> GetCertificate called=%v; %s; %s", server, client, called, res, <-srvErr)
}

func main() {
	fmt.Println(run([]string{"http/1.1"}, []string{"acme-tls/1"}))
	fmt.Println(run([]string{"http/1.1", "acme-tls/1"}, []string{"acme-tls/1"}))
	fmt.Println(run([]string{"http/1.1", "acme-tls/1"}, []string{"acme-tls/1", "http/1.1"}))
	fmt.Println(run([]string{"acme-tls/1", "http/1.1"}, []string{"http/1.1", "acme-tls/1"}))
	fmt.Println(run([]string{"http/1.1", "acme-tls/1"}, []string{"http/1.1", "acme-tls/1"}))
	fmt.Println(run([]string{"http/1.1"}, []string{"h2", "http/1.1"}))
	fmt.Println(run([]string{"http/1.1"}, nil))
}
