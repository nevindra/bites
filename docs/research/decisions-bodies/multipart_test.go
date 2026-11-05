package bodies

import (
	"fmt"
	"io"
	"mime/multipart"
	"runtime"
	"strings"
	"testing"
)

// genReader produces a multipart body lazily: head bytes, then n filler bytes, then tail.
type genReader struct {
	head, tail string
	n          int64
	state      int
	off        int
	sent       int64
}

func (g *genReader) Read(p []byte) (int, error) {
	switch g.state {
	case 0:
		c := copy(p, g.head[g.off:])
		g.off += c
		if g.off == len(g.head) {
			g.state, g.off = 1, 0
		}
		return c, nil
	case 1:
		m := int64(len(p))
		if g.n-g.sent < m {
			m = g.n - g.sent
		}
		for i := int64(0); i < m; i++ {
			p[i] = 'a'
		}
		g.sent += m
		if g.sent == g.n {
			g.state = 2
		}
		return int(m), nil
	case 2:
		c := copy(p, g.tail[g.off:])
		g.off += c
		if g.off == len(g.tail) {
			return c, io.EOF
		}
		return c, nil
	}
	return 0, io.EOF
}

func mem() (uint64, uint64) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc, m.Mallocs
}

// A 32 MiB file part streamed with NextPart and io.Copy to Discard.
func TestMultipartStreamedPart(t *testing.T) {
	g := &genReader{
		head: "--B\r\nContent-Disposition: form-data; name=\"f\"; filename=\"a.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n",
		tail: "\r\n--B--\r\n", n: 32 << 20,
	}
	a0, m0 := mem()
	r := multipart.NewReader(g, "B")
	p, err := r.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, p)
	a1, m1 := mem()
	t.Logf("32 MiB part streamed: %d bytes read, %d B allocated total (%d mallocs); body/alloc = %d", n, a1-a0, m1-m0, n/int64(a1-a0))
}

// A part header of hdrBytes: what does NextPart allocate before the handler sees the part?
func TestMultipartHugeHeader(t *testing.T) {
	for _, hb := range []int{1 << 10, 64 << 10, 1 << 20, 8 << 20} {
		var sb strings.Builder
		sb.WriteString("--B\r\nContent-Disposition: form-data; name=\"f\"\r\n")
		line := "X-Pad: " + strings.Repeat("z", 990) + "\r\n"
		for sb.Len() < hb {
			sb.WriteString(line[:7] + strings.Repeat("z", 990) + "\r\n")
		}
		sb.WriteString("\r\nx\r\n--B--\r\n")
		a0, m0 := mem()
		r := multipart.NewReader(strings.NewReader(sb.String()), "B")
		_, err := r.NextPart()
		a1, m1 := mem()
		t.Logf("part header of %7d B (single repeated field name): err=%v allocated %d B in %d mallocs", hb, err, a1-a0, m1-m0)
	}
	// one giant line
	big := "--B\r\nX: " + strings.Repeat("z", 9<<20) + "\r\n\r\nx\r\n--B--\r\n"
	a0, _ := mem()
	r := multipart.NewReader(strings.NewReader(big), "B")
	_, err := r.NextPart()
	a1, _ := mem()
	t.Logf("one 9 MiB header line: err=%v allocated %d B", err, a1-a0)
}

// Many tiny parts: allocations per NextPart, and whether the count is limited.
func TestMultipartManyParts(t *testing.T) {
	const N = 50000
	var sb strings.Builder
	for i := 0; i < N; i++ {
		fmt.Fprintf(&sb, "--B\r\nContent-Disposition: form-data; name=\"f%d\"\r\n\r\nv\r\n", i)
	}
	sb.WriteString("--B--\r\n")
	a0, m0 := mem()
	r := multipart.NewReader(strings.NewReader(sb.String()), "B")
	cnt := 0
	for {
		p, err := r.NextPart()
		if err != nil {
			t.Logf("stopped after %d parts: %v", cnt, err)
			break
		}
		io.Copy(io.Discard, p)
		cnt++
	}
	a1, m1 := mem()
	t.Logf("NextPart on %d parts: %.0f B and %.1f mallocs per part, no part-count limit in NextPart", cnt, float64(a1-a0)/float64(cnt), float64(m1-m0)/float64(cnt))
}

// What NewReader itself costs, per upload.
func BenchmarkMultipartNewReaderFirstPart(b *testing.B) {
	body := "--B\r\nContent-Disposition: form-data; name=\"f\"; filename=\"a.bin\"\r\nContent-Type: application/octet-stream\r\n\r\nhello\r\n--B--\r\n"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := multipart.NewReader(strings.NewReader(body), "B")
		p, _ := r.NextPart()
		io.Copy(io.Discard, p)
	}
}
