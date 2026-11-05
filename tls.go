package bites

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// VerifiedChains is the client certificate of an mTLS connection, as crypto/tls verified it
// against the ClientCAs of the *tls.Config the app serves with (ADR 0066): one chain or more, each
// starting with the client's own certificate and ending at a CA of ClientCAs. It is nil on a plain
// connection, when the client sent no certificate (ClientAuth VerifyClientCertIfGiven), when the
// config asks for a certificate without verifying it (RequestClientCert, RequireAnyClientCert),
// and after the request was answered. Only verified certificates are ever returned, so a Provider
// that reads the client's identity from chains[0][0] reads one a CA vouched for.
//
// The chains belong to the connection and every request on it sees the same ones: they are not
// pooled and nothing else writes them, but they are shared, so read them and do not change them.
func (r Request) VerifiedChains() [][]*x509.Certificate {
	if !r.live() {
		return nil
	}
	return r.run.ex.VerifiedChains()
}

// Certs is a server certificate that can be replaced while the app serves, without a restart and
// without dropping a connection (ADR 0067). Give its GetCertificate to the *tls.Config, with
// Certificates left empty:
//
//	certs, err := bites.LoadCerts("tls.crt", "tls.key")
//	app.ListenTLS(":443", &tls.Config{GetCertificate: certs.GetCertificate})
//
// and call Reload when the files change (on SIGHUP, or from a timer: bites installs no signal
// handler and watches no file). Every handshake reads the certificate in force with one atomic
// load, so handshakes running during a Reload finish with the old one and the next start with the
// new one; connections already open keep theirs. A Reload that fails (a file missing or half
// written, a key that does not match, a certificate already expired) returns its error and
// changes nothing: the certificate in force goes on serving.
//
// The zero Certs holds no certificate: Set gives it one, and GetCertificate fails every handshake
// until then. A Certs is safe for concurrent use.
type Certs struct {
	cur  atomic.Pointer[tls.Certificate]
	mu   sync.Mutex // serialises Reload and Set, so an older read is never put in force after a newer one
	cert string
	key  string
}

// LoadCerts reads a PEM certificate chain and its key, as tls.LoadX509KeyPair does, and returns a
// Certs holding it, or the error that stopped it.
func LoadCerts(certFile, keyFile string) (*Certs, error) {
	c := &Certs{cert: certFile, key: keyFile}
	if err := c.Reload(); err != nil {
		return nil, err
	}
	return c, nil
}

// ErrNoCertFiles is returned by Reload on a Certs that was not made by LoadCerts.
var ErrNoCertFiles = errors.New("bites: Certs has no files to reload; it was not made by LoadCerts")

// Reload reads the files LoadCerts was given again and puts the certificate in force. On any
// error it returns it and the certificate in force is unchanged.
func (c *Certs) Reload() error {
	if c.cert == "" {
		return ErrNoCertFiles
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cert, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		return fmt.Errorf("bites: reloading %s and %s: %w (the certificate in force is unchanged)", c.cert, c.key, err)
	}
	return c.setLocked(cert)
}

// Set puts cert in force, for a certificate that comes from somewhere other than two files (a
// secret store). It is refused, and the certificate in force is unchanged, when cert has no
// certificate or no key, when its key is not the certificate's, or when it has expired.
func (c *Certs) Set(cert tls.Certificate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setLocked(cert)
}

func (c *Certs) setLocked(cert tls.Certificate) error {
	if len(cert.Certificate) == 0 || cert.PrivateKey == nil {
		return errors.New("bites: the certificate has no certificate or no private key (the certificate in force is unchanged)")
	}
	leaf := cert.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return fmt.Errorf("bites: the certificate does not parse: %w (the certificate in force is unchanged)", err)
		}
		cert.Leaf = leaf
	}
	if pub, ok := cert.PrivateKey.(interface{ Public() crypto.PublicKey }); ok {
		if eq, ok := pub.Public().(interface{ Equal(crypto.PublicKey) bool }); ok && !eq.Equal(leaf.PublicKey) {
			return errors.New("bites: the private key is not the certificate's (the certificate in force is unchanged)")
		}
	}
	if now := time.Now(); now.After(leaf.NotAfter) {
		return fmt.Errorf("bites: the certificate expired at %s (the certificate in force is unchanged)", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	c.cur.Store(&cert)
	return nil
}

// GetCertificate is the function a *tls.Config calls for each handshake: it returns the
// certificate in force.
func (c *Certs) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if cert := c.cur.Load(); cert != nil {
		return cert, nil
	}
	return nil, errors.New("bites: Certs holds no certificate yet")
}
