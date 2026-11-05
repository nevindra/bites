# Client certificates are verified by the app's tls.Config, and the Request offers only the chains it verified

mTLS is `crypto/tls`'s, configured on the `*tls.Config` the app is given ([ADR 0017](0017-tls-now-http2-later.md)): `ClientAuth` says whether a client certificate is asked for and whether one is required, and `ClientCAs` says which CAs may issue one. bites adds no verifier of its own and no option of its own. What it adds is the reading side: `Request.VerifiedChains()` returns the chains `crypto/tls` verified during the handshake, to a Provider and to a middleware ([ADR 0022](0022-middleware-sees-a-request-view-and-returns-a-response-value.md), [ADR 0023](0023-provided-sections-are-checked-at-registration.md)). Built 2026-11-01, milestone 3, track C.

**Only verified chains.** `VerifiedChains` is the `VerifiedChains` of the connection's `tls.ConnectionState`: each chain starts with the client's own certificate and ends at a CA of `ClientCAs`. It is nil on a plain connection, when the client sent no certificate (`VerifyClientCertIfGiven`), when the config asks for a certificate without verifying it (`RequestClientCert`, `RequireAnyClientCert`, whose certificates are in `PeerCertificates` and are deliberately not offered), and on a `Request` kept past its answer. So a Provider that takes the client's identity from `chains[0][0]` takes one a CA the operator chose vouched for, and the one way to get an unverified certificate, a config that does not verify, gives it nothing. A handler reads it through a Provider, as it reads any identity:

```go
func clientIdentity(_ context.Context, req bites.Request, _ struct{}) (Client, error) {
	chains := req.VerifiedChains()
	if len(chains) == 0 {
		return Client{}, bites.Unauthorized("a client certificate is required")
	}
	return Client{Name: chains[0][0].Subject.CommonName}, nil
}
```

**Nothing pooled reaches the user** ([ADR 0008](0008-no-pooled-object-reaches-the-user.md)). The slices and certificates belong to the `*tls.Conn`, which `crypto/tls` never reuses for another connection and the engine never pools; they live as long as the connection. Every request on one connection sees the same ones, so they are shared and must be read, not changed; that is said on the method. The method checks that its request is still the one running, as `RemoteAddr` does.

**A refusal.** A config whose `ClientAuth` verifies (`VerifyClientCertIfGiven` or `RequireAndVerifyClientCert`) and that has no `ClientCAs` (and no `GetConfigForClient`, which could supply them) panics in `ServeTLS` and `ListenTLS`: `crypto/tls` would then verify against the system's roots, which accepts a certificate from any public CA, which is never what an operator asking for mTLS means.

**What it costs.** Nothing on any request that does not call it: no field was added to a connection or a request, and the allocation rows of [ADR 0003](0003-the-trade-budget.md) are unchanged. A call builds a `tls.ConnectionState` by value on the caller's stack (not measured; a claim). The handshake of a client that sends a certificate costs what `crypto/tls`'s verification costs; TLS throughput is not measured yet, and waits for the serial run of wave 2.

**Scenarios it was put against.**

- _A service-to-service call inside the reference workload's cluster_ ([ADR 0002](0002-the-reference-workload.md)), each caller with a certificate from the cluster's CA: the Provider reads the caller's name, and a route that needs one answers 401 without it (`TestAProviderReadsTheVerifiedClientCertificateOfAnMTLSConnection`).
- _A hostile client with a certificate from another CA_: with `RequireAndVerifyClientCert` or `VerifyClientCertIfGiven` the handshake fails; it is never served with that certificate.
- _A config that only requests a certificate_ (`RequireAnyClientCert`): the client's certificate reaches the server and `VerifiedChains` is still nil (`TestACertificateAskedForButNotVerifiedIsNeverReturned`).
- _TLS terminated at a load balancer_: the app sees no certificate at all; a balancer that forwards the client certificate in a header is a trusted-proxy question for [ADR 0025](0025-no-proxy-is-trusted-by-default.md), not this.

**What it fails at.** Revocation is not checked: `crypto/tls` does not fetch CRLs or OCSP, and a revoked client certificate passes until it expires, unless the config's `VerifyPeerCertificate` checks it. A certificate verified at the handshake stays the connection's identity for its whole life, hours on a kept-alive connection, even after a `ClientCAs` change. HTTP/1.1 has no renegotiation here, so a route cannot ask for a certificate the handshake did not request: an app that wants mTLS for some routes asks for it optionally (`VerifyClientCertIfGiven`) and refuses in a Provider.

**What was rejected.** Offering `PeerCertificates` (the unverified list) or the whole `tls.ConnectionState`: the first is a trap that looks like an identity, the second exposes what HTTP/2 will answer differently ([ADR 0017](0017-tls-now-http2-later.md)) and invites the same mistake. A client-certificate option of bites' own (`WithClientCAs`): it would be a second place to say what `tls.Config` already says. Copying the chains for each call: an allocation for each read of a value nothing in the framework writes.
