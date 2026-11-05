# Small fixes and TLS: four recommendations stress-tested

Run on 2026-10-31, go1.27.2, linux/amd64, nilo-go working tree on master (uncommitted milestone 2 work on top of 79d0e58).
Probes are in this folder, each its own module: `hostpct/`, `neglimits/`, `tlsalpn/`, `hsfail/` (with `bench_test.go`).
Benchmarks ran pinned with `taskset -c 0,1,8,9`, three runs each.
Anything not marked "measured" is a claim.

## 1. A percent sign in a host

Verdict: MODIFY. Refuse `%` in a host everywhere, not only in the absolute-form target.

The design as stated (refuse in absolute-form only) is rejected for two reasons.

The premise was slightly off. Measured (`hostpct`): net/http does not percent-decode a host in general. It refuses a pct-encoded ASCII byte in an absolute-form host (`%41`, `%40`, `%2f`, `%zz` all give `invalid URL escape`), and decodes only pct-encoded non-ASCII (`%C3%BC` becomes `über.example`). For a Host header net/http keeps the value as sent. h1 today accepts all of these, including the malformed `%zz`, in both places, and the absolute-form host silently wins over the Host header (`Host: x` was ignored in every case).

Refusing in absolute-form only would leave the same bytes accepted in the Host header, so one reading of "the host" is checked and the other is not. Both come from the same `hostChar` table in `internal/h1/parse.go` (the target through `validAuthority`, the header through the loop at the end of `ParseHead`), so the consistent change is one character: remove `%` from the `hostChar` string. CONNECT's authority-form uses `validAuthority` too and is covered by the same edit.

Evidence:
- RFC 3986 3.2.2 allows `pct-encoded` in `reg-name`, and RFC 9110 `uri-host` is `reg-name`, so a well-formed `%41` is grammatical; `%zz` is not, and RFC 9112 3.2 says a Host with an invalid field value is a 400. The grammar alone therefore forces at least "refuse malformed escapes". Refusing every `%` goes beyond the grammar, justified by ADR 0045's own rule (refuse what two parties could read two ways: `a%41` and `aA` name the same DNS host, and h1 compares raw bytes).
- Real clients (measured): curl 8.23 through a proxy sends `GET http://xn--ber-goa.example/` for `http://%C3%BCber.example/` and `GET http://aA.example/` for `http://a%41.example/`; it sends nothing for `a%2fb`. So it decodes first. Browsers do the same by the WHATWG host parser (claim: percent-decode, then IDNA to punycode, and `%` is a forbidden host code point afterwards). No client was found that sends a `%` in a hostname.
- The one real sender is an IPv6 zone: curl via a proxy sends `GET http://[fe80::1%25lo]/` in the target while its Host header is `[fe80::1]` (measured). RFC 9110 `uri-host` has no zone, so this is outside the grammar. It is a client talking to a forward proxy, and nilo-go is not one.
- nginx and Envoy: not installed, so not measured. Claim from memory of their sources: nginx's Host validation and Envoy's authority check do not decode a pct-encoded host and do not clearly reject it either, so a proxy in front passes the bytes through. That is the case for refusing here: the proxy will not do it for us.
- Where Host is used today: only exposed as `head.Host`, nothing in the request path reads it. Its future user is the WebSocket Origin comparison of ADR 0046. A browser sends Origin as punycode, so a `%` Host would never equal it and the check fails closed. The risk this closes is therefore small; the argument is consistency and the ambiguity class, not an exploit.
- Fuzz allowed list: `internal/h1/fuzz_test.go` already covers the new difference. When net/http accepts and we refuse, `targetDifference` checks `validAuthority(theirs.URL.Host)`; net/http's decoded `über.example` has bytes above 0x7f, which `hostChar` has never accepted, so the reason string is produced. For the header, `errBadHost` has its row. The decode-then-compare branch for Host in `compare` becomes dead and can go. Claim: not run, since I may not edit code or tests.
- Cost: no allocation (a table lookup that already runs per byte), no axis moves. Claim, not benchmarked: the `%` entry leaves the table and nothing is added to the loop.

Best alternative: validate escape syntax only (`%` must be followed by two hex digits), in both places. It lost because it keeps `a%41` and `aA` as two readings, accepts a form no client emits, and needs a new scanning branch, while deleting `%` needs none.

What the chosen design fails at: a proxy-style client sending an IPv6 zone id or raw `%`-encoded UTF-8 in a Host header gets a 400 and the connection closes. ADR 0045 row 6 and its sentence "the host comes from the target" need an exception, and the table needs a new row ("a `%` in the Host header or the target's authority: 400, connection closed").

## 2. Every negative limit panics

Verdict: MODIFY the placement. Do the check in `New`, when the options are applied, not at start or in ServeTLS.

Measured (`neglimits`): today all ten fields of `Limits` are accepted by `New` with a negative value and silently replaced by the default, `HandshakeTimeout` included (its panic fires only in `ServeTLS` and `ListenTLS`, in `checkTLS`).

Why `New`, not start:
- `register.go:228` copies `a.limits.MaxBody` and `BodyTimeout` into the route config at Registration, long before `start()`. A check at start would be after the routes already took defaults.
- `start()` runs only when something serves. Claim: the in-memory path of `nilotest` does not necessarily reach it, so a test app with `MaxHead: -1` would never be told. `New` is the one place every app passes.
- The existing `HandshakeTimeout` check then moves out of `checkTLS`. An app that never serves TLS with `HandshakeTimeout: -1s` stops passing silently, which is the point of "every negative limit". The row in `refusal_test.go` (line 206) changes its text and its `ServeTLS:` prefix.

Design: after the options run in `New`, walk a small table of (field name, value < 0, what zero means) and panic with a `RegistrationError`, e.g. `Limits.MaxConns is -1, which is not a limit: it must be positive, or zero for the default (90% of the file descriptor limit)\n\tregistered at app_test.go:12`. `callerWhere` skips the package's own non-test files, so it reports the user's `New(...)` line (claim, same helper the other refusals use). Then `withDefaults` can test `== 0` and the `g.X > 0` marks in `logSettings` stay correct, so zero means default everywhere and the log says `default` for it.

The refusal exists to catch one real mistake: a value read from a config where `-1` meant "unlimited" (MaxConns, timeouts). The message must say there is no way to switch a limit off. ADR 0024 already says that for the per-route `MaxBody(0)`; the app-level zero meaning default and the route-level zero being refused stays an asymmetry, documented, not fixed here.

Alternative considered: panic at start/Serve like `HandshakeTimeout` today. Lost for the reasons above (routes, nilotest). A second alternative, clamping negatives to the default with a Warn at start, lost because the project's rule is refusal at Registration (ADR 0007).

Fails at: a user who builds `Limits` from a config file after calling `New` cannot; none, since the options are applied at `New`. A programmatically negative value from an unset sentinel (-1) now stops the process at boot, which is what is wanted. Axis: none (no request-path code). Needs one table row per field, ten rows, in `refusal_test.go`, and ADR 0024's sentence on the handshake timeout widened to all fields.

## 3. `acme-tls/1` beside `http/1.1`

Verdict: MODIFY. Allowing it in `checkTLS` alone is not enough and would do nothing; the engine's forced `NextProtos` has to follow, and there is one more refusal to add.

Measured (`tlsalpn`), server `NextProtos` against a client offering only `acme-tls/1`, which is what the CA's tls-alpn-01 validation does:
- server `[http/1.1]` (what `engine.ServeTLS` forces today, `internal/engine/server.go:192`): the handshake fails with `no application protocol` and `GetCertificate` is never called. So with the engine as it is, autocert cannot answer the challenge at all, whatever `checkTLS` allows.
- server `[http/1.1, acme-tls/1]`: `GetCertificate` is called, the handshake completes, negotiated `acme-tls/1`.
- a client offering `[acme-tls/1, http/1.1]` to `[http/1.1, acme-tls/1]` gets `http/1.1`; the server's order wins. With the order reversed a client offering both gets `acme-tls/1`. So `http/1.1` must be first.

How autocert uses it (read from `x/crypto@v0.58.0/acme/autocert/autocert.go`): `Manager.TLSConfig()` sets `NextProtos` to `h2, http/1.1, acme-tls/1`. `GetCertificate` recognises a challenge by `len(hello.SupportedProtos) == 1 && SupportedProtos[0] == acme-tls/1` and returns the token certificate only if one is pending or cached for that name; otherwise an error. The connection never carries HTTP. So allowing the protocol is harmless to the HTTP path: the CA validates the certificate in the handshake and closes.

Design:
1. Allowed set is `{http/1.1, acme-tls/1}`; `h2` and anything else still refused. The h2 message gets a clause: `autocert.Manager.TLSConfig()` lists h2, so build the config with `GetCertificate: m.GetCertificate` and `NextProtos: []string{"http/1.1", acme.ALPNProto}`.
2. The engine sets `NextProtos` to `[http/1.1]`, plus `acme-tls/1` only if the caller listed it, `http/1.1` first, and applies the same filter to the config `GetConfigForClient` returns.
3. New refusal: `acme-tls/1` listed with only static `Certificates` (no `GetCertificate`, no `GetConfigForClient`). A static certificate cannot carry a challenge, so the CA's validation would fail with the normal certificate. This is the "developer making the obvious mistake" case, and it is catchable before the first request.
4. In `conn.handshake`, a negotiated `acme-tls/1` is not an error: close the connection, log at Debug as a challenge answered, count nothing as a failure. Today that value would take the `errNotHTTP11` path, which is a "failed handshake" and would be wrong once failures are counted (section 4).

Hostile client: offers only `acme-tls/1` to an app that enabled it. It gets a handshake; autocert returns an error unless a challenge for that name is pending. It costs one handshake, the same as any scanner, bounded by `HandshakeTimeout` and counted against `MaxConns`. Claim, not measured; the handshake cost is the one in `docs/research/tls-idle/`. One existing exposure, independent of this change: autocert's `GetCertificate` runs under a 5 minute context (line 270), not the 10 s handshake deadline, which bounds I/O only; a hook that blocks holds its goroutine. Worth a line in ADR 0017 when milestone 3 wires autocert.

Better DX than waiting: yes. The alternative is to keep refusing it and let milestone 3 change `checkTLS`, `engine.ServeTLS` and `handshake` together under time pressure; doing it now is about 20 lines and four test rows (allowed, h2 still refused, static-certs refused, challenge handshake closes quietly), and there is nothing on the request path, so no axis moves (allocations per request unchanged, idle memory unchanged: the config is read per handshake, not stored per connection).

Fails at: it adds a protocol the engine will carry for ever, and the first real use needs milestone 3's automation to test against a real CA (Pebble); until then the test is a self-signed token certificate as in the probe. The existing row `ListenTLS with a config that offers another protocol` in `refusal_test.go` (line 204) flips from a refusal to an accepted value; its replacement should use another name (`spdy/3`).

## 4. Failed TLS handshakes

Verdict: MODIFY. A rate-limited Warn for the failures that point at the operator's certificate, Debug for the rest, and no public counter yet.

Measured (`hsfail`), the server-side error for each way a handshake ends:
- plain HTTP to the TLS port: `tls: first record does not look like a TLS handshake`
- connect and close (port scan): `EOF`
- connect and silence: `read tcp ...: i/o timeout`
- TLS 1.0 only client: `tls: client offered only unsupported versions: [301]`
- the client rejects our certificate, whether self-signed, expired or for another name: `remote error: tls: bad certificate` (a Go client; other stacks send `certificate_expired` or `unknown_ca`)
- `GetCertificate` returns an error: that error's text, unclassifiable.

What the operator who misconfigured sees is only the third-from-last group (the client alert), and today it is at Debug, invisible by default. Everything else is routine internet noise.

Design:
- Warn when the handshake ends in a received alert of `bad_certificate`, `unsupported_certificate`, `certificate_revoked`, `certificate_expired`, `certificate_unknown` or `unknown_ca` (typed, via `errors.As` to `tls.AlertError`, no string matching; claim that Go 1.27's `OpError` wraps it, to be confirmed in the change). Message: `nilogo: a client rejected the TLS certificate (expired, wrong name or untrusted?)` with the error, the remote address and `suppressed=N`.
- Rate limit: one Warn per interval (a minute), on the engine's clock so tests use the fake clock; the rest counted into `suppressed`.
- Errors from `GetCertificate` are NOT promoted: with autocert and a `HostPolicy`, a scanner connecting by IP or with a random SNI produces them all day, which would put the Warn back into the noise it must avoid.
- No public counter: there is no stats surface and the metrics hook is still on the "settled in a later milestone" list, so a counter nobody can read is the same as Debug. Keep the atomic internally (it feeds `suppressed`); expose it with the hook.

Cost (measured, pinned, three runs): the code today builds `tc.RemoteAddr().String()` before the Debug call, which is 4 allocations and about 50 ns (49.3 to 52.8 ns, 56 B) on every failure even with Debug off. A gate of two atomics and a compare-and-swap, with the address built only when a line is written, is 7.9 ns and 0 allocations single-threaded (7.87 to 8.07) and 18.4 ns under four-way contention (18.36 to 18.42). A written Warn line is about 850 ns and 5 allocations (849 to 1,010 ns), once a minute. A handshake itself allocates about 57 KB (ADR 0017), so all of this is below noise; it is off the request path, and per connection state is unchanged (the gate is three words on the server, not on a connection), so ADR 0005 and ADR 0003 do not move.

Hostile client: a flood of bad handshakes costs the server one Warn a minute, not one line each, and the handshake cost it already pays. A client that sends the alert deliberately can make one Warn per minute appear; that is the cap.

Alternatives: Warn on every failure (rejected: a scanner fills the log, an attacker controls the volume); a counter alone (rejected: no way to read it); Debug as now (rejected: this is the case the roadmap names, a misconfigured certificate and a silent log).

Fails at: a client that rejects the certificate and just closes, with no alert (some old browsers and library stacks), appears as `EOF` and stays invisible; this is a limit of what TLS tells the server. The Warn is also not proof of misconfiguration: one client with a stale trust store triggers it, hence the question mark in the message.

## Summary

| item | verdict | decisive evidence |
|---|---|---|
| host with a percent sign | MODIFY: refuse `%` in the Host header and the target's authority (delete `%` from `hostChar`) | measured: net/http refuses `%41`/`%40`/`%2f` hosts, h1 accepts `%zz`; curl decodes before sending; Host header and target share one table |
| negative limits | MODIFY: panic in `New`, not at start | measured: all 10 fields silently default today; routes copy limits at Registration, so a start check is too late |
| `acme-tls/1` | MODIFY: also change the engine's forced `NextProtos`, `http/1.1` first, refuse it with static certificates, close a negotiated challenge quietly | measured: with `[http/1.1]` the CA's hello fails before `GetCertificate`; server order decides the pick |
| failed handshakes | MODIFY: Warn once a minute on client certificate alerts, Debug otherwise, no public counter | measured: only the client alert signals a bad certificate; gate 7.9 ns 0 allocs against 50 ns 4 allocs today |
