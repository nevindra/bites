# The WebSocket reader and writer under fuzzing

**Run:** `FuzzReader` three times of 10 minutes and `FuzzRoundTrip` three times of 5 minutes, as the compiled test binary of `internal/ws` (`go test -c`, Go 1.27.3, kept outside the repository) pinned with `taskset -c 0,1,8,9` and `-test.parallel 4`, on a Ryzen 7 9700X, at commit `6b086e4` (the worktree had no uncommitted change to `internal/ws`), on 2026-11-01. Cores 0, 1, 8 and 9 are two physical cores; other agents were building and running unit tests on the other cores during the whole run, so the rates are a little noisy (the spread below is small). The runs were sequential and shared one cache of generated inputs (`-test.fuzzcachedir`, kept between runs), so each run starts from what the earlier ones found. Executions are the last progress line of each run; the rate is executions divided by the run time, because Go's own final line prints `0/sec`.

| fuzzer | run | executions | rate | result |
|---|---|---|---|---|
| `FuzzReader` | 1 | 229.6M | 383k/s | no failure |
| `FuzzReader` | 2 | 227.0M | 378k/s | no failure |
| `FuzzReader` | 3 | 226.3M | 377k/s | no failure |
| `FuzzRoundTrip` | 1 | 61.5M | 205k/s | no failure |
| `FuzzRoundTrip` | 2 | 61.7M | 206k/s | no failure |
| `FuzzRoundTrip` | 3 | 61.9M | 206k/s | no failure |

About 683M reader executions and 185M round-trip executions in all.

**What "no failure" means.** `FuzzReader` gives arbitrary bytes to the frame reader as one whole read and again cut into pieces of 1 byte, of two fuzzer-chosen sizes and of 3 bytes, under small limits (message 1024, 8 fragments, control payload 125, 4 pings per second), and requires that every cut gives exactly the whole read's outcome: the same messages, the same count of pings and pongs, the same close code and the same failure code. A reader whose verdict depends on how the bytes arrive is a bug of its own (a limit or a UTF-8 or masking check that fires only on some cuts). The seeds cover fragmented messages with a ping between, close frames with valid and reserved codes, invalid UTF-8 whole and split inside a rune, an unmasked client frame, 16-bit and 64-bit lengths (including the high bit set and a non-minimal length), a reserved bit, and control frames over 125 bytes. `FuzzRoundTrip` builds a valid masked client message of fuzzer-chosen size, fragmentation, opcode and mask keys, with pings interleaved, and requires that the reader returns exactly that message, whatever the read size. So a run without failure says: no input found that makes the reader panic, hang, or disagree with itself across cuts, and no valid message that the reader mangles or refuses, as far as these two properties and the seeds reach.

**Failures found.** None. No `testdata/fuzz` input was written, so nothing was fixed and no unit test was added by this run. The sequence was run once from the start and not repeated.

**Known weaknesses.** The fuzzers check self-consistency (the same verdict for every cut) and the round trip of valid messages; there is no second implementation to compare against, as net/http is for `internal/h1`, so a rule the reader gets wrong the same way on every cut is not caught by `FuzzReader` unless a unit test pins it. Per-message and per-fragment limits are exercised at 1024 and 8 in `FuzzReader` and at 65536 in `FuzzRoundTrip`, not at the configured defaults. Compression is not fuzzed (the compression bit is a seed that must be refused). The rate of new interesting inputs per run was not recorded, so how saturated the search was is not claimed.
