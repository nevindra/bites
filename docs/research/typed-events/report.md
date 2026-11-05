# Typed events: what Events[T] costs against SSE

Taken on 2026-11-02, Go 1.27.0 darwin/arm64, Apple M1 Pro, macOS. The code measured is `3460aab` plus the uncommitted tree that adds `Events[T]` (ADR 0030). This is a laptop over the in-memory connection, not the reference machine of ADR 0002 over TCP: the allocation counts carry over, the nanoseconds are a ratio within one run. No throughput or p99 over TCP was measured.

## What was run

`BenchmarkAnEvent` in the root package's `budget_test.go` serves one stream of 100 events a request over the in-memory connection, three ways: an `SSE` sending constant data, an `SSE` whose function marshals a `feedTick` with `json.Marshal` into the event's `Data` (what a handler wrote before `Events`), and an `Events[feedTick]` sending the value. The test binary was built once (`go test -c`) and each case run with `-test.bench 'BenchmarkAnEvent/<case>' -test.benchtime=5000x`, the three in turn, four rounds. `ns/event` is the request's time divided by 100.

## Results (four interleaved rounds)

| Case | ns/event | allocs/request (100 events) | B/request |
|---|---|---|---|
| `SSE`, constant data | 172.6, 172.9, 173.6, 183.3 | 4 | 394 to 396 |
| `SSE` and `json.Marshal` | 483.1, 488.5, 497.6, 501.7 | 394 | 15,807 to 15,821 |
| `Events[T]` | 214.3, 215.7, 216.7, 218.3 | 4 | 988 to 991 |

- An `Events` stream allocates what an `SSE` stream does, 4, and an event allocates nothing. `TestAnEventsStreamAllocatesWhatAnSSEStreamDoesAndATypedEventAllocatesNothing` holds the counts for 0, 1 and 100 events exactly.
- Encoding the value costs about 42 ns an event over constant data, and the whole event takes 44% of the time of the `json.Marshal` idiom it replaces, which allocates about 3.9 times an event.
- A live `Events` stream holds about 594 bytes more than an `SSE`: its runner (584 bytes, the 640-byte size class) holds a 512-byte buffer the event is encoded into, against the 40-byte runner (48-byte class) of an `SSE`.

## Why the event is encoded into the runner's buffer

`EventWriter` builds an event in an array on its stack. The typed sender cannot: `go build -gcflags=-m` reports `leaking param: dst` on the codec's `Encoder.Append`, so a stack array handed to it would move to the heap on every event. The buffer therefore lives in the stream's runner, allocated once a stream, under a lock so a sender used from several goroutines does not tear an event. A buffer grown past 512 bytes by a large event is kept up to 64 KiB and dropped back to 512 bytes above that.
