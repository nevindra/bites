package ws

import (
	"bytes"
	"fmt"
	"testing"
)

func TestUnmaskAgreesWithTheByteLoopAtEveryLengthAndOffset(t *testing.T) {
	key := [4]byte{0x37, 0xfa, 0x21, 0x3d}
	for n := 0; n <= 200; n++ {
		for pos := uint8(0); pos < 4; pos++ {
			src := make([]byte, n)
			for i := range src {
				src[i] = byte(i*7 + 3)
			}
			want := bytes.Clone(src)
			for i := range want {
				want[i] ^= key[(int(pos)+i)&3]
			}
			got := bytes.Clone(src)
			unmask(got, key, pos)
			if !bytes.Equal(got, want) {
				t.Fatalf("unmask of %d bytes at offset %d differs from the byte loop", n, pos)
			}
		}
	}
}

func BenchmarkUnmask(b *testing.B) {
	key := [4]byte{1, 2, 3, 4}
	for _, n := range []int{32, 4096} {
		buf := make([]byte, n)
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				unmask(buf, key, 1)
			}
		})
	}
}
