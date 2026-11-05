package engine

import (
	"crypto/tls"
	"crypto/x509"
)

// VerifiedChains is the client's certificate chains as crypto/tls verified them against the
// config's ClientCAs during the handshake (ADR 0066): nil on a plain connection, when the client
// sent no certificate, and when the config asked for one without verifying it
// (RequestClientCert, RequireAnyClientCert). Each chain starts with the client's own certificate.
// The slices belong to the TLS connection, which never reuses them, and live as long as it: they
// are not pooled (ADR 0008), and every request on the connection sees the same ones.
func (ex *Exchange) VerifiedChains() [][]*x509.Certificate {
	tc, ok := ex.c.nc.(*tls.Conn)
	if !ok {
		return nil
	}
	return tc.ConnectionState().VerifiedChains
}
