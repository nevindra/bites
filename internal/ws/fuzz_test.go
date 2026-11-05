package ws

import (
	"bytes"
	"math/rand"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzReader feeds arbitrary bytes to a Reader in pieces of several sizes and checks what must
// hold whatever the bytes are: no panic; no more bytes taken than given; every chunk within the
// limits of ADR 0046; a message that ends is valid UTF-8 if it is text; control payloads are at
// most 125 bytes; a failure is the one a Reader gives however the input was cut; and the events,
// joined into messages, do not depend on how the input was cut.
func FuzzReader(f *testing.F) {
	seeds := [][]byte{
		frame(true, OpText, "hello"),
		cat(frame(false, OpText, "he"), frame(true, OpPing, "p"), frame(true, OpContinuation, "llo")),
		frame(true, OpClose, closePayload(1000, "bye")),
		frame(true, OpClose, closePayload(1005, "")),
		frame(true, OpText, "\xff"),
		cat(frame(false, OpText, "\xe2\x82"), frame(true, OpContinuation, "\xac")),
		AppendFrame(nil, OpText, []byte("unmasked")),
		{0x82, 0x80 | 126, 0, 5, 1, 2, 3, 4},
		{0x82, 0x80 | 127, 0x80, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4},
		{0xC1, 0x80, 1, 2, 3, 4},
		frame(true, OpPing, string(make([]byte, 126))),
		frame(true, OpBinary, string(make([]byte, 300))),
	}
	for _, s := range seeds {
		f.Add(s, uint8(0), uint8(7))
	}
	lim := Limits{MaxMessage: 1024, MaxFragments: 8, MaxControl: 125, PingsPerSecond: 4, CloseTimeout: time.Second}
	f.Fuzz(func(t *testing.T, data []byte, step1, step2 uint8) {
		whole := run(t, &lim, data, 0)
		for _, step := range []int{1, int(step1), int(step2), 3} {
			if step <= 0 {
				continue
			}
			cut := run(t, &lim, data, step)
			if cut.fail != whole.fail || cut.closed != whole.closed || !equalStrings(cut.msgs, whole.msgs) || cut.pings != whole.pings || cut.pongs != whole.pongs {
				t.Fatalf("step %d: %+v differs from whole read %+v", step, cut, whole)
			}
		}
	})
}

type outcome struct {
	msgs         []string
	pings, pongs int
	closed       int // close code + 1, or 0
	fail         uint16
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func run(t *testing.T, lim *Limits, data []byte, step int) outcome {
	t.Helper()
	var r Reader
	r.Reset(lim)
	var out outcome
	now := time.Unix(1e9, 0)
	var cur []byte
	var curOp Opcode
	if step <= 0 {
		step = len(data) + 1
	}
	for off := 0; off < len(data); {
		piece := append([]byte(nil), data[off:min(off+step, len(data))]...)
		off += len(piece)
		for len(piece) > 0 {
			n, e := r.Feed(piece, now)
			if n < 0 || n > len(piece) {
				t.Fatalf("Feed took %d of %d bytes", n, len(piece))
			}
			piece = piece[n:]
			switch e.Kind {
			case KindNeedMore:
				if n == 0 {
					piece = nil
				}
			case KindData:
				if e.Start {
					cur, curOp = cur[:0], e.Opcode
				}
				if e.Opcode != curOp || (e.Opcode != OpText && e.Opcode != OpBinary) {
					t.Fatalf("opcode %d in a %d message", e.Opcode, curOp)
				}
				cur = append(cur, e.Payload...)
				if len(cur) > lim.MaxMessage {
					t.Fatalf("delivered %d bytes of a message limited to %d", len(cur), lim.MaxMessage)
				}
				if e.End {
					if e.Opcode == OpText && !utf8.Valid(cur) {
						t.Fatalf("text message ended invalid: %q", cur)
					}
					out.msgs = append(out.msgs, string(cur))
				}
			case KindPing, KindPong:
				if len(e.Payload) > 125 {
					t.Fatalf("control payload of %d bytes", len(e.Payload))
				}
				if e.Kind == KindPing {
					out.pings++
				} else {
					out.pongs++
				}
			case KindClose:
				if len(e.Payload) > 123 || !utf8.Valid(e.Payload) || (e.Code != CloseNoStatus && !ClientCloseCode(e.Code)) {
					t.Fatalf("bad close event %+v", e)
				}
				out.closed = int(e.Code) + 1
			case KindFail:
				if e.Err == nil {
					t.Fatal("failure without an error")
				}
				switch e.Err.Code {
				case 1002, 1007, 1008, 1009:
				default:
					t.Fatalf("close status %d", e.Err.Code)
				}
				out.fail = e.Err.Code
				// A failure is permanent.
				if _, again := r.Feed([]byte{0x81, 0x80, 0, 0, 0, 0}, now); again.Kind != KindFail || again.Err != e.Err {
					t.Fatal("failure not repeated")
				}
				return out
			}
		}
	}
	return out
}

// FuzzRoundTrip writes messages with the client writer, cut into fragments and read with the
// reader in pieces, and expects them back, with the control frames between.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello world"), uint8(3), uint8(5), true, int64(1))
	f.Add([]byte("héllo € \U0001F600"), uint8(1), uint8(2), true, int64(2))
	f.Add(bytes.Repeat([]byte{0xff, 0x00}, 100), uint8(40), uint8(9), false, int64(3))
	lim := Limits{MaxMessage: 1 << 16, MaxFragments: 1 << 17, MaxControl: 125, PingsPerSecond: 1 << 20, CloseTimeout: time.Second}
	f.Fuzz(func(t *testing.T, msg []byte, fragSize, readSize uint8, text bool, seed int64) {
		if len(msg) > 1<<16 || fragSize == 0 || readSize == 0 {
			return
		}
		op := OpBinary
		if text {
			op = OpText
			if !utf8.Valid(msg) {
				return
			}
		}
		rng := rand.New(rand.NewSource(seed))
		var wire []byte
		var key [4]byte
		sent := 0
		first := true
		for sent < len(msg) || first {
			end := min(sent+int(fragSize), len(msg))
			// A text message cut inside a rune is fine: only the whole must be valid.
			rng.Read(key[:])
			o := OpContinuation
			if first {
				o = op
			}
			wire = AppendClientFrame(wire, end == len(msg), o, msg[sent:end], key)
			if rng.Intn(3) == 0 {
				rng.Read(key[:])
				wire = AppendClientFrame(wire, true, OpPing, []byte{byte(sent)}, key)
			}
			first = false
			sent = end
			if end == len(msg) {
				break
			}
		}
		out := run(t, &lim, wire, int(readSize))
		if out.fail != 0 || len(out.msgs) != 1 || out.msgs[0] != string(msg) {
			t.Fatalf("did not round trip: %+v", out)
		}
	})
}
