package jsoncodec

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"
)

// plainRunSlow is the scan a byte at a time that plainRun must agree with.
func plainRunSlow(b []byte, i int) int {
	for ; i < len(b); i++ {
		if c := b[i]; c < 0x20 || c >= 0x80 || c == '"' || c == '\\' {
			return i
		}
	}
	return i
}

func checkString(t *testing.T, s string) {
	t.Helper()
	b := []byte(s)
	for i := 0; i <= len(b); i++ {
		if got, want := plainRun(b, i), plainRunSlow(b, i); got != want {
			t.Fatalf("plainRun(%q, %d) = %d, want %d", s, i, got, want)
		}
	}
	want, wantErr := jsontext.AppendQuote([]byte("x"), s)
	got, gotErr := appendString([]byte("x"), s)
	if (gotErr != nil) != (wantErr != nil) || gotErr == nil && !bytes.Equal(got, want) {
		t.Fatalf("appendString(%q) = %q, %v; jsontext.AppendQuote = %q, %v", s, got, gotErr, want, wantErr)
	}
}

func TestTheEightByteScanAndTheStringEncoderAgreeWithTheByteLoopAndJsontext(t *testing.T) {
	special := []string{`"`, `\`, "\x00", "\x1f", "\x20", "\x7f", "\x80", "\xff", "é", "€", "\U0001F600", " ", "<", ">", "&", "\xed\xa0\x80", "\xc3"}
	for _, sp := range special {
		for pos := 0; pos < 20; pos++ {
			checkString(t, strings.Repeat("a", pos)+sp+strings.Repeat("b", 19-pos))
			checkString(t, strings.Repeat("a", pos)+sp)
		}
	}
	checkString(t, "")
	checkString(t, strings.Repeat("plain ascii text ", 100))
	checkString(t, strings.Repeat("héllo wörld ✓ ", 100))
}

func FuzzTheStringEncoderAgreesWithJsontext(f *testing.F) {
	for _, s := range []string{"", "ada", `a"b`, `a\b`, "a\nb", "héllo", "\xff", "\xed\xa0\x80", "<&>", strings.Repeat("x", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { checkString(t, s) })
}

func plainStringByteLoop(b []byte, i int) (int, bool) {
	for ; i < len(b); i++ {
		c := b[i]
		if c == '"' {
			return i, true
		}
		if c < 0x20 || c >= 0x80 || c == '\\' {
			return 0, false
		}
	}
	return 0, false
}

var sinkInt int

func BenchmarkPlainStringByLength(b *testing.B) {
	for _, n := range []int{3, 7, 15, 40, 200, 4000} {
		body := []byte(`"` + strings.Repeat("a", n) + `","next":1}`)
		b.Run(fmt.Sprintf("loop_%d", n), func(b *testing.B) {
			for b.Loop() {
				j, _ := plainStringByteLoop(body, 1)
				sinkInt += j
			}
		})
		b.Run(fmt.Sprintf("swar_%d", n), func(b *testing.B) {
			for b.Loop() {
				j, _ := plainString(body, 1)
				sinkInt += j
			}
		})
	}
}
