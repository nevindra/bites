# The HTTP/1.1 parser refuses anything two servers could read two ways

The engine's parser is strict where a looser reading could make it and a proxy in front of it see different requests, which is how request smuggling works. Each case and its answer:

| request | answer |
|---|---|
| both `Transfer-Encoding` and `Content-Length` | 400, connection closed |
| more than one `Content-Length`, even with equal values | 400, connection closed |
| a `Transfer-Encoding` other than `chunked`, or `chunked` not last | 501, connection closed |
| a line folded onto the next (obs-fold), a bare CR, a bare LF as a line end, a NUL, space before a colon, a field name that is not a token | 400, connection closed |
| no `Host` in HTTP/1.1, or more than one in any version (`CONNECT` excepted) | 400, connection closed |
| absolute-form target (`GET http://host/path`) | accepted; the host comes from the target, as RFC 9112 says, kept as sent (never percent-decoded) |
| a `%` in the Host header or in the host of an absolute-form target or a CONNECT authority | 400, connection closed |
| `OPTIONS *` | 204 with the methods the app serves |
| a `#`, userinfo, or a scheme other than http or https in the target | 400, connection closed |
| a blank line before the request line | 400, connection closed |
| any control character but HTAB in a header value | 400, connection closed |
| a version other than HTTP/1.0 or HTTP/1.1 | 505, connection closed |
| `Transfer-Encoding` in an HTTP/1.0 request | 400, connection closed |
| an `Expect` other than `100-continue` (HTTP/1.1) | 417, connection closed |
| chunk extensions and trailers | accepted, counted against a budget of their own for the whole body (the head limit by default), trailers discarded; over it, 431 and connection closed |
| `Expect: 100-continue` with a `Content-Length` over the route's limit | 413 at once, body never read |
| `Expect: 100-continue` otherwise | `100 Continue` sent when the body is first read |
| a response sent before the body was read | up to 64 KiB of the rest drained briefly, otherwise the connection is closed |
| HTTP/1.0 | kept alive only with `Connection: keep-alive` |
| several requests in one write (pipelining) | served in order ([ADR 0016](0016-the-handler-context-is-cancelled-on-disconnect.md)) |

**Every refusal closes the connection**, a missing or repeated `Host` included, even though its framing is known (the parser first kept those connections open; the engine found the exception and the rule won). On TCP the close lingers: the engine shuts its write side and discards what the client still sends for up to 500 ms, so unread bytes do not turn the close into a reset that destroys the 400 before the client reads it. A response header value has CR, LF and NUL replaced by a space, so no value a handler returns can split a response.

**Why every refusal closes:** Once a head is refused, the framing of whatever follows it cannot be trusted, so the engine answers and closes rather than trying to skip a body it was told how to find by a request it just called malformed.

**The policy is this table, not the fuzzer.** Differential fuzzing against net/http ([ADR 0033](0033-the-rules-are-build-steps.md)) finds where the two parsers disagree; each disagreement is then settled against this table, and a corpus of known smuggling inputs is part of the run.

**What it costs:** a client or proxy that sends bare LF line ends, or duplicate identical `Content-Length` headers, gets a 400 from us where a lenient server would answer. RFC 9112 allows accepting a bare LF; it is refused here because the stricter reading cannot be smuggled through, and a broken client finds out at once.

**What was rejected (a percent sign in a host):** keeping the host as sent and accepting a `%` in it, including the malformed `%zz`, and the narrower fix of refusing it in an absolute-form target only. Both read the same bytes two ways: `a%41` and `aA` name one DNS host while this parser compares raw bytes, and the Host header and the target's authority share one character table, so refusing in one place left the other open. A `%` is now outside the host characters everywhere. Evidence ([`docs/research/decisions-small/`](../research/decisions-small/report.md), section 1, probe `hostpct`): net/http refuses a percent-encoded ASCII byte in an absolute-form host (`%41`, `%40`, `%2f`, `%zz`) and decodes only non-ASCII ones, curl and browsers decode before they send so none was found sending a `%` in a hostname, and the only real sender is an IPv6 zone id (`[fe80::1%25lo]`) from a client talking to a forward proxy, which bites is not. The difference from net/http is covered by this row in the fuzz run: `FuzzParseHead` ran 2 minutes pinned to `taskset -c 0,1,8,9` after the change, 5,306,055 executions, no disagreement. Validating the escape syntax only (a `%` followed by two hex digits) lost because it keeps the two readings and needs a scanning branch that deleting one character does not.
