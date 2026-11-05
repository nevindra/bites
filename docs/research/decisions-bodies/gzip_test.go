package bodies

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// jsonBody builds a realistic JSON list body of about n bytes.
func jsonBody(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"items":[`)
	for i := 0; sb.Len() < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"item number %d","status":"active","created_at":"2026-10-31T12:%02d:%02dZ","price":%d.%02d,"tags":["a","b"]}`, 1000+i*7, i, i%60, (i*13)%60, i*3%500, i%100)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())[:min(sb.Len(), n+200)]
}

func BenchmarkGzipNewWriter(b *testing.B) {
	for _, lvl := range []int{gzip.DefaultCompression, gzip.BestSpeed} {
		b.Run(fmt.Sprint("level", lvl), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w, _ := gzip.NewWriterLevel(io.Discard, lvl)
				w.Close()
			}
		})
	}
}

func BenchmarkGzipPoolReset(b *testing.B) {
	for _, lvl := range []int{gzip.DefaultCompression, gzip.BestSpeed} {
		b.Run(fmt.Sprint("level", lvl), func(b *testing.B) {
			p := sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, lvl); return w }}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				w := p.Get().(*gzip.Writer)
				w.Reset(io.Discard)
				w.Close()
				p.Put(w)
			}
		})
	}
}

func BenchmarkGzipCompress(b *testing.B) {
	for _, size := range []int{512, 1024, 4096, 16384, 65536} {
		body := jsonBody(size)
		for _, lvl := range []int{gzip.DefaultCompression, gzip.BestSpeed} {
			b.Run(fmt.Sprintf("%dB/level%d", len(body), lvl), func(b *testing.B) {
				w, _ := gzip.NewWriterLevel(io.Discard, lvl)
				var out bytes.Buffer
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					out.Reset()
					w.Reset(&out)
					w.Write(body)
					w.Close()
				}
				b.ReportMetric(float64(out.Len())/float64(len(body)), "ratio")
				b.ReportMetric(float64(out.Len()), "outB")
			})
		}
	}
}

// Heap held by one writer, per level.
func TestGzipWriterBytes(t *testing.T) {
	for _, lvl := range []int{gzip.DefaultCompression, gzip.BestSpeed, gzip.HuffmanOnly} {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		w, _ := gzip.NewWriterLevel(io.Discard, lvl)
		w.Write([]byte("x"))
		w.Close()
		runtime.ReadMemStats(&m1)
		t.Logf("level %d: NewWriter+first use allocated %d B in %d mallocs", lvl, m1.TotalAlloc-m0.TotalAlloc, m1.Mallocs-m0.Mallocs)
		runtime.KeepAlive(w)
	}
}
