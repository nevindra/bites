// Package h1 parses HTTP/1.1 request heads and decodes chunked bodies for the
// engine. It is engine-internal.
//
// # Why it exists
//
// ADR 0005 puts our own HTTP/1.1 parser under the framework, and an own
// parser is a request-smuggling surface. This package therefore refuses
// anything that a proxy in front of us could read another way. The policy is
// the table of ADR 0045, row by row; the differential fuzzer in this package
// finds where net/http disagrees, and every deliberate disagreement is named
// in an allow-list in the test that cites the row.
//
// # Borrowing
//
// Every slice in a Request points into the caller's read buffer (ADR 0008:
// the engine owns that buffer and never lends it to user code; what a
// handler sees is copied into a per-request slab later). A Request is valid
// until the buffer is reused. Request.Headers is the one thing kept across
// requests: ParseHead truncates and appends to it, so after warm-up a parse
// allocates nothing.
//
// # Errors
//
// A refusal is an *Error carrying the status to answer and whether the
// connection must be closed afterwards. When Close is false (a missing or
// repeated Host), ParseHead still returns the head's length and a filled
// Request, so the engine can answer 400 and skip the body by its framing.
// When Close is true nothing else can be trusted and n is 0. Errors are
// shared package-level values, so a hostile client costs no allocation.
//
// ErrNeedMore means the buffer holds no complete head yet. The head limit
// (ADR 0024) is judged on the bytes seen so far: a buffer of MaxHead bytes
// with no blank line is already a 431, so the engine never needs a buffer
// larger than MaxHead to decide.
//
// # Chunked bodies
//
// ChunkedDecoder is a byte-wise state machine fed whatever the engine has
// read. Decode writes decoded bytes into the start of out (it does not
// append) and may stop early when out is full, so the caller drives it in a
// loop and never needs a body-sized buffer. Chunk extensions and trailers
// are validated, discarded and counted against one cumulative budget
// (ADR 0045); the ordinary size lines and CRLFs of the chunks are not, so a
// long body in small chunks is not penalised.
package h1
