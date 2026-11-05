package ws

import (
	"bytes"
	"math/rand"
	"testing"
	"unicode/utf8"
)

func validIn(parts ...[]byte) bool {
	var u utf8State
	for _, p := range parts {
		if !u.write(p) {
			return false
		}
	}
	return u.complete()
}

func TestUTF8SplitAtAnyPointAgreesWithUTF8Valid(t *testing.T) {
	samples := []string{
		"", "ascii", "héllo", "€", "\U0001F600", "a\U0001F600b",
		"\xff", "\xc0\x80", "\xe0\x80\x80", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xe2\x82", "\xf0\x9f\x98",
		"\xf0\x9f\x98\x80\xf0", "ok\x80", "\xc3", "\xc3\xa9\xc3",
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		b := make([]byte, rng.Intn(12))
		for j := range b {
			b[j] = []byte{0x41, 0xc3, 0xa9, 0xe2, 0x82, 0xac, 0xf0, 0x9f, 0x98, 0x80, 0xff, 0x80}[rng.Intn(12)]
		}
		samples = append(samples, string(b))
	}
	for _, s := range samples {
		b := []byte(s)
		want := utf8.Valid(b)
		for i := 0; i <= len(b); i++ {
			for j := i; j <= len(b); j++ {
				if got := validIn(b[:i], b[i:j], b[j:]); got != want {
					t.Fatalf("%q split at %d,%d: got %v, want %v", s, i, j, got, want)
				}
			}
		}
		parts := make([][]byte, len(b))
		for i := range b {
			parts[i] = b[i : i+1]
		}
		if got := validIn(parts...); got != want {
			t.Fatalf("%q byte by byte: got %v, want %v", s, got, want)
		}
	}
}

func BenchmarkUTF8Write(b *testing.B) {
	for _, v := range []struct {
		name string
		p    []byte
	}{
		{"ascii_32B", bytes.Repeat([]byte("x"), 32)},
		{"ascii_4KiB", bytes.Repeat([]byte("x"), 4096)},
		{"ascii_64KiB", bytes.Repeat([]byte("x"), 65536)},
		{"mixed_4KiB", bytes.Repeat([]byte("héllo wörld ✓ "), 256)},
	} {
		b.Run(v.name, func(b *testing.B) {
			b.SetBytes(int64(len(v.p)))
			var u utf8State
			for b.Loop() {
				if !u.write(v.p) {
					b.Fatal("invalid")
				}
			}
		})
	}
}
