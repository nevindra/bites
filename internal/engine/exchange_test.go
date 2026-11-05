package engine

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

func TestTheEightByteHeaderScanCleansWhatTheByteLoopCleans(t *testing.T) {
	byteLoop := func(s string) string {
		b := []byte(s)
		for i, ch := range b {
			if ch == '\r' || ch == '\n' || ch == 0 {
				b[i] = ' '
			}
		}
		return string(b)
	}
	alphabet := []byte("aZ09-;= /\t\r\n\x00\x0b\x0e\x7f\x80\xff")
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20000 {
		b := make([]byte, rng.IntN(40))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		if rng.IntN(2) == 0 { // mostly clean values, as real headers are
			for i := range b {
				if b[i] < 0x0e {
					b[i] = 'x'
				}
			}
		}
		s := string(b)
		if got, want := string(appendClean(nil, s)), byteLoop(s); got != want {
			t.Fatalf("appendClean(%q) = %q, want %q", s, got, want)
		}
	}
}

func BenchmarkAppendClean(b *testing.B) {
	buf := make([]byte, 0, 256)
	for _, s := range []string{"Content-Type", "application/json; charset=utf-8", "max-age=31536000; includeSubDomains"} {
		b.Run(fmt.Sprint(len(s)), func(b *testing.B) {
			for b.Loop() {
				buf = appendClean(buf[:0], s)
			}
		})
	}
}
