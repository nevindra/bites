# A certificate is reloaded through GetCertificate, swapped atomically, and a failed reload changes nothing

`bites.Certs` holds the server certificate in force behind one atomic pointer. Its `GetCertificate` method is what the `*tls.Config` is given, with `Certificates` left empty, and `Reload` (or `Set`) replaces the certificate while the app serves, without a restart and without closing a connection. Built 2026-11-01, milestone 3, track C, in `tls.go`.

```go
certs, err := bites.LoadCerts("/etc/tls/tls.crt", "/etc/tls/tls.key")
if err != nil {
	log.Fatal(err)
}
go app.ListenTLS(":443", &tls.Config{GetCertificate: certs.GetCertificate})
// on SIGHUP, or when the files change:
if err := certs.Reload(); err != nil {
	log.Printf("certificate not reloaded, the old one is still served: %v", err)
}
```

**The API.** `LoadCerts(certFile, keyFile)` reads a PEM pair as `tls.LoadX509KeyPair` does and fails as it does. `Reload()` reads the same two files again. `Set(cert)` puts a `tls.Certificate` from anywhere else in force (a secret store, a certificate manager's callback); the zero `Certs` is usable with `Set` and fails every handshake until then. `GetCertificate` returns the certificate in force with one atomic load, so it costs a handshake nothing measurable and holds no lock: a handshake running during a reload finishes with the certificate it loaded, the next one gets the new one, and a connection already open keeps its session. bites installs no signal handler ([ADR 0029](0029-shutdown-drains-and-then-closes.md)) and watches no file, so what triggers a reload is the application's: SIGHUP, a ticker, a Kubernetes secret's mounted file changing.

**A failed reload changes nothing.** `Reload` and `Set` check the new certificate before it is put in force, and on any failure return an error that says the certificate in force is unchanged: a file missing, unreadable or half written; PEM that does not parse; a key that is not the certificate's (checked by `LoadX509KeyPair`, and for `Set` by comparing the key's public half with the certificate's); a certificate that has already expired. The app logs the error and goes on serving the old certificate, which is better than serving none; a certificate that expires while a reload keeps failing is then the alert's job. Reloads and sets are serialised by a mutex, so a slow reload that read older files is never put in force after a quicker one with newer files.

**What it costs.** Nothing on the request path and nothing per idle connection: the certificate is the handshake's, not the connection's. A handshake pays one atomic load. A `Reload` reads and parses two files. Throughput is not measured yet (wave 2).

**Refusals.** None at Registration: a `Certs` is a value the application builds, and every mistake in it is an I/O error of `LoadCerts` or `Reload`, returned. A config with `GetCertificate` already passes `ServeTLS`'s check for a certificate source ([ADR 0017](0017-tls-now-http2-later.md)).

**Scenarios it was put against.**

- _A certificate renewed by cert-manager or certbot every 60 days_ on a long-running instance of the reference workload ([ADR 0002](0002-the-reference-workload.md)): the files change, a reload puts the new certificate in force, and no WebSocket is dropped for it.
- _Many handshakes while reloads run in a loop_: 80 requests over new TLS connections from 8 goroutines while another reloads between four certificates, written by rename as a manager does, under `-race`: every request is served (`TestHandshakesDuringReloadsEachGetAWholeCertificate`). The pair is renamed in two steps, so some reloads read a certificate with the other's key, fail, and change nothing, which is the rule working.
- _A half-written file, a mismatched key, an expired certificate_: each `Reload` returns its error and the served certificate's serial is still the first (`TestAReloadThatFailsReturnsItsErrorAndTheCertificateInForceGoesOnServing`).
- _A slow handshake_ (a client that stalls): it holds the certificate pointer it loaded, which is an immutable value; nothing waits for it.

**What it fails at.** A reload only happens when something calls it; an application that never wires SIGHUP or a timer keeps the old certificate until it expires. One certificate is served for every name: several certificates chosen by SNI are a `GetCertificate` of the application's own (or autocert's). Session tickets issued before a reload still resume after it, which is TLS's design and harmless (they do not carry the server's certificate). Session ticket keys are rotated by `crypto/tls` itself and are not touched here. The expiry check uses the machine's clock, not the app's `WithClock`, because a `Certs` belongs to no app.

**What was rejected.** Watching the files (inotify, or polling their times): a goroutine and a platform-specific path in the core for what one line of the application does, and it would reload a pair halfway through a non-atomic write. Replacing the whole `*tls.Config` while serving: `crypto/tls` reads the config on every handshake without a lock, so swapping fields of a live config is a data race, and swapping the config means a listener per config. Closing open connections on a reload so they pick up the new certificate: it drops every WebSocket for no security gain, since the old certificate was valid when they connected. Logging inside `Reload`: a `Certs` has no logger, and the error returned carries everything a log line would.
