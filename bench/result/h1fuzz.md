# The HTTP/1.1 parser against net/http

**Run:** `FuzzParseHead` three times of 10 minutes and `FuzzChunked` three times of 5 minutes, as the compiled test binary of `internal/h1` (`go test -c`, Go 1.27) pinned with `taskset -c 0,1,8,9` and `-test.parallel 4`, on a Ryzen 7 9700X, on the uncommitted milestone 1 tree of 2026-10-31. Cores 0, 1, 8 and 9 are two physical cores that no other work was pinned to, but another agent ran short smoke tests on cores 2 to 7 and 10 to 15 during part of it, so the rates are a little noisy (the spread below is small). The cache of generated inputs was kept between runs, so each run starts from what the earlier ones found.

| fuzzer | run | executions | rate | result |
|---|---|---|---|---|
| `FuzzParseHead` | 1 | 256.7M | 427k/s | no difference |
| `FuzzParseHead` | 2 | 257.3M | 430k/s | no difference |
| `FuzzParseHead` | 3 | 257.7M | 431k/s | no difference |
| `FuzzChunked` | 1 | 107.7M | 362k/s | no difference |
| `FuzzChunked` | 2 | 107.5M | 348k/s | no difference |
| `FuzzChunked` | 3 | 106.4M | 355k/s | no difference |

About 772M head executions and 322M chunked-body executions in all, from the final test code. The first sessions ended early on the failures listed below; once a difference was settled the whole sequence was started again (the head fuzzer's runs were not affected by the later changes to the chunked allowed list, and the three recorded chunked runs are from the last build).

**What each does.** `FuzzParseHead` gives the same bytes to `h1.ParseHead` and to `http.ReadRequest` and compares method, path, query, host, chunked, content length and minor version when both accept, and checks every disagreement of acceptance against `allowed`, a list in which each entry names its [ADR 0045](../../docs/adr/0045-http1-strictness.md) row. `FuzzChunked` (new) gives the same chunked body to `h1.ChunkedDecoder` and to net/http's chunked reader behind a fixed head, requires the same decoded bytes when both finish, and requires that feeding the decoder the whole body or one byte at a time gives the same bytes, the same count consumed and the same error. A decoder that depends on how the bytes arrive is a smuggling bug of its own.

**The smuggling corpus** is the seed list of `FuzzParseHead` (about 75 heads: Content-Length with Transfer-Encoding both ways, `chunked` with other codings in either order, Transfer-Encoding with a space before the colon, a tab, a leading space, a fold, an empty value, other case, a parameter, repeated, in HTTP/1.0; duplicate, equal and unequal, list, signed, hexadecimal, zero-led, empty, spaced, negative and overflowing Content-Length; Content-Length hidden behind a bare LF, a bare CR or a fold; bare LF and bare CR line ends; NUL and other control bytes in names and values; repeated Host and Host in other case; absolute-form with userinfo, fragment and odd schemes; a pipelined second request) and the seeds of `FuzzChunked` (extensions, space and tab before `;`, trailers with and without a colon, folded trailers, bare LF framing, signed, hexadecimal, zero-led and overlong sizes, a size over 2^63, a missing or doubled CRLF after data, NUL in a size line, a request after the last chunk). The seeds were widened during this run (about 30 head seeds and the whole chunked list were added); all pass.

**Differences found, and how each was settled**

| input | difference | settled as |
|---|---|---|
| `0 http://%80 HTTP/1.0` | the host of an absolute-form target: net/http percent-decodes it, we keep it as sent | deliberate, ADR 0045 row 6 (the host comes from the target); the comparison now decodes ours before comparing. Kept as `testdata/fuzz/FuzzParseHead/418e566a759be527`. Not a smuggling vector (the host is not used to frame the request), but it is a place where a proxy and we could name two hosts, so it is worth a line in ADR 0045 if the user wants it recorded there |
| `00\r\n1\r\n\r\n` | a trailer line with no colon: we discard trailers unread, net/http refuses with a MIME error | deliberate, the chunk row of ADR 0045 (trailers accepted, counted, discarded) |
| `5\t;x\r\nhello...` | whitespace before `;` in a chunk-size line: RFC 9112 allows it (BWS), net/http refuses | deliberate, the chunk row (extensions accepted); the allowed entry only matches hex digits, blanks, then `;` |
| a trailer line of about 4 KB | net/http refuses a trailer over its own fixed size (`suspiciously long trailer after chunked`) | deliberate, the chunk row: extensions and trailers have a budget of their own, the head limit by default. Kept as `testdata/fuzz/FuzzChunked/0efb16ad6b340387` |
| a chunk-size line of about 16 KB of extension | net/http refuses a line over its fixed cap (`header line too long`) | same, kept as `testdata/fuzz/FuzzChunked/2bd0c5dbf2a42abb` |

No difference was a bug in the parser or the decoder: none let a request be read two ways by us and by a conforming proxy, and none made the decoder depend on how bytes arrive. The other allowed entries of `FuzzParseHead` (versions, duplicate Content-Length, Transfer-Encoding rules, bare CR and LF, obs-fold, Host rules, unknown Expect, target forms, field names and values) were already in the list and each maps to a row of ADR 0045; the run found nothing outside them.

**Known weaknesses.** net/http's `ReadRequest` is not the whole server (the server adds the Host check and its own limits), so a rule enforced only by `http.Server` is not compared. Input is mutated bytes, so structured attacks that need a long valid prefix (a body of exact length after a crafted head) are reached through the seeds, not by the fuzzer's own search. The three recorded chunked runs are shorter (5 minutes) than the head runs. The rate of new interesting inputs per run was not recorded, so how saturated the search was is not claimed.
