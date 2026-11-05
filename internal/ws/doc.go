// Package ws is the WebSocket codec of ADR 0035 to ADR 0040 and ADR 0046: the frame reader and
// writer, the handshake check and accept key, close codes, and UTF-8 checked across fragments.
// It is engine-internal and is not called by the engine yet (milestone 2, step 2).
//
// # What it decides and what it leaves
//
// The codec enforces the protocol (RFC 6455) and the inbound limits of ADR 0046, row by row,
// and when it refuses it names the close status ADR 0046 gives and the limit that was hit.
// It reports opcodes and facts; it decides no policy. Binary frames, subprotocols, the Origin
// comparison, a reassembly deadline, an app-wide reassembly budget and who writes to a socket
// are open in docs/roadmap.md and belong to the engine step that settles them.
//
// # Ownership (ADR 0008)
//
// The engine owns every buffer. Reader.Feed unmasks in place in the slice it is given and
// returns payloads that alias it, valid until the engine reuses that slice; nothing here keeps
// a pointer to caller memory past a call, and nothing here is pooled. The one buffer a Reader
// owns is a 125 byte one for a control frame split across reads, allocated the first time that
// happens. A Reader is about 150 bytes (see TestAReaderStaysSmall), because it is held for every
// idle socket (ADR 0003).
//
// # Errors
//
// A refusal is a shared *Error carrying the close status and the Limit that was hit, so a
// hostile client costs no allocation. After a failure a Reader repeats it. After a Close frame
// it discards input.
//
// # Writing
//
// The Append functions add to a caller's slice and allocate only if it must grow, so the
// engine frames a message once into a buffer it owns (ADR 0038). Server frames are never masked.
package ws
