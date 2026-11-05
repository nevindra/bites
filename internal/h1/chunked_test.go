package h1

import (
	"errors"
	"strings"
	"testing"
)

// decodeAll feeds body in pieces of step bytes through an out buffer of outSize.
func decodeAll(t *testing.T, body string, step, outSize, maxOverhead int) (string, int, bool, error) {
	t.Helper()
	var d ChunkedDecoder
	d.Reset(maxOverhead)
	var got []byte
	out := make([]byte, outSize)
	used := 0
	in := []byte(body)
	for used < len(in) {
		end := min(used+step, len(in))
		chunk := in[used:end]
		for len(chunk) > 0 {
			c, n, done, err := d.Decode(chunk, out)
			got = append(got, out[:n]...)
			used += c
			if err != nil {
				return string(got), used, false, err
			}
			if done {
				return string(got), used, true, nil
			}
			if c == 0 && n == 0 {
				t.Fatal("no progress")
			}
			chunk = chunk[c:]
		}
	}
	return string(got), used, false, nil
}

func wantChunkErr(t *testing.T, body string, status int, maxOverhead int) {
	t.Helper()
	_, _, _, err := decodeAll(t, body, len(body), 64, maxOverhead)
	var he *Error
	if !errors.As(err, &he) || he.Status != status || !he.Close {
		t.Fatalf("%q: %v", body, err)
	}
}

func TestChunkedDecoder(t *testing.T) {
	const body = "5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"

	t.Run("a chunked body is decoded and the bytes after it are left alone", func(t *testing.T) {
		got, used, done, err := decodeAll(t, body+"GET /next", len(body)+9, 64, 1024)
		if err != nil || !done || got != "hello world" || used != len(body) {
			t.Fatalf("%q %d %v %v", got, used, done, err)
		}
	})

	t.Run("a chunked body fed one byte at a time decodes the same", func(t *testing.T) {
		got, used, done, err := decodeAll(t, body, 1, 64, 1024)
		if err != nil || !done || got != "hello world" || used != len(body) {
			t.Fatalf("%q %d %v %v", got, used, done, err)
		}
	})

	t.Run("a small output buffer makes Decode stop and resume without losing bytes", func(t *testing.T) {
		got, _, done, err := decodeAll(t, body, len(body), 2, 1024)
		if err != nil || !done || got != "hello world" {
			t.Fatalf("%q %v %v", got, done, err)
		}
	})

	t.Run("hex sizes are case-insensitive", func(t *testing.T) {
		got, _, done, err := decodeAll(t, "A\r\n0123456789\r\na\r\nabcdefghij\r\n0\r\n\r\n", 3, 8, 1024)
		if err != nil || !done || got != "0123456789abcdefghij" {
			t.Fatalf("%q %v", got, err)
		}
	})

	t.Run("extensions and trailers are accepted and discarded", func(t *testing.T) {
		b := "5;name=value;x\r\nhello\r\n0 ;last\r\nX-Trailer: a\r\nY: b\r\n\r\n"
		got, used, done, err := decodeAll(t, b, 1, 64, 1024)
		if err != nil || !done || got != "hello" || used != len(b) {
			t.Fatalf("%q %d %v %v", got, used, done, err)
		}
	})

	t.Run("an empty body is just the last chunk", func(t *testing.T) {
		got, _, done, err := decodeAll(t, "0\r\n\r\n", 1, 8, 8)
		if err != nil || !done || got != "" {
			t.Fatal(got, done, err)
		}
	})

	t.Run("an incomplete body is not done", func(t *testing.T) {
		for i := 0; i < len(body); i++ {
			_, _, done, err := decodeAll(t, body[:i], 1, 64, 1024)
			if err != nil || done {
				t.Fatalf("prefix %d: %v %v", i, done, err)
			}
		}
	})

	t.Run("chunk extensions over the budget are a 431 and the connection closed", func(t *testing.T) {
		wantChunkErr(t, "5;"+strings.Repeat("e", 100)+"\r\nhello\r\n0\r\n\r\n", 431, 50)
	})

	t.Run("trailers over the budget are a 431 and the connection closed", func(t *testing.T) {
		wantChunkErr(t, "0\r\nX: "+strings.Repeat("t", 100)+"\r\n\r\n", 431, 50)
	})

	t.Run("the extension budget is shared by the whole body", func(t *testing.T) {
		wantChunkErr(t, strings.Repeat("1;aaaaaaaaaa\r\nx\r\n", 20)+"0\r\n\r\n", 431, 100)
	})

	t.Run("many small chunks cost nothing against the budget", func(t *testing.T) {
		b := strings.Repeat("1\r\nx\r\n", 5000) + "0\r\n\r\n"
		got, _, done, err := decodeAll(t, b, 7, 64, 0)
		if err != nil || !done || len(got) != 5000 {
			t.Fatal(len(got), done, err)
		}
	})

	t.Run("malformed framing is a 400 and the connection closed", func(t *testing.T) {
		for _, b := range []string{
			"\r\n",                                  // no size
			"g\r\n",                                 // not hex
			"5\nhello\r\n0\r\n\r\n",                 // bare LF after size
			"5\r\nhello\n0\r\n\r\n",                 // bare LF after data
			"5\r\nhelloXX0\r\n\r\n",                 // missing CRLF after data
			"5\r\nhello\r\n0\r\n\n",                 // bare LF ends the trailers
			"5;a\rb\r\nhello\r\n",                   // bare CR in extension
			"5;a\x00\r\nhello\r\n",                  // NUL in extension
			"5\r\nhello\r\n0\r\nX\x00: a\r\n\r\n",   // NUL in trailer
			"5\r\nhello\r\n0\r\nX: a\n\r\n",         // bare LF in trailer
			"5\r\nhello\r\n0\r\n folded: a\r\n\r\n", // obs-fold in trailer
			"5 x\r\n",                               // junk after size
			"-5\r\n",
			"0x5\r\n",
			"+5\r\n",
		} {
			wantChunkErr(t, b, 400, 1024)
		}
	})

	t.Run("a chunk size too large to represent is refused", func(t *testing.T) {
		wantChunkErr(t, "ffffffffffffffff\r\n", 400, 1024)
		wantChunkErr(t, "10000000000000000\r\n", 400, 1024)
		wantChunkErr(t, strings.Repeat("0", 17)+"1\r\n", 400, 1024)
		// the largest size that fits is accepted as a size; it just waits for data
		_, _, done, err := decodeAll(t, "7fffffffffffffff\r\n", 3, 8, 8)
		if err != nil || done {
			t.Fatal(done, err)
		}
	})

	t.Run("Decode after the end consumes nothing", func(t *testing.T) {
		var d ChunkedDecoder
		d.Reset(8)
		out := make([]byte, 8)
		d.Decode([]byte("0\r\n\r\n"), out)
		c, n, done, err := d.Decode([]byte("more"), out)
		if c != 0 || n != 0 || !done || err != nil {
			t.Fatal(c, n, done, err)
		}
	})

	t.Run("Reset readies a finished decoder for the next body", func(t *testing.T) {
		var d ChunkedDecoder
		out := make([]byte, 8)
		d.Reset(8)
		d.Decode([]byte("1\r\na\r\n0\r\n\r\n"), out)
		d.Reset(8)
		_, n, done, err := d.Decode([]byte("2\r\nbc\r\n0\r\n\r\n"), out)
		if err != nil || !done || string(out[:n]) != "bc" {
			t.Fatal(n, done, err)
		}
	})

	t.Run("decoding allocates nothing", func(t *testing.T) {
		in := []byte("5;e=1\r\nhello\r\n6\r\n world\r\n0\r\nT: 1\r\n\r\n")
		out := make([]byte, 64)
		var d ChunkedDecoder
		allocs := testing.AllocsPerRun(1000, func() {
			d.Reset(1024)
			if _, _, done, err := d.Decode(in, out); !done || err != nil {
				panic("bad")
			}
		})
		if allocs != 0 {
			t.Fatalf("%v allocations", allocs)
		}
	})
}
