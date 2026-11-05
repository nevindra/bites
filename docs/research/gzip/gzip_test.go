package gzipbench

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	nilogo "github.com/nevindra/bites"
	nilotest "github.com/nevindra/bites/bitetest"
)

// jsonBody is the body of docs/research/decisions-bodies: a JSON list of about n bytes, more
// repetitive than real data, so its ratios are optimistic.
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

var sizes = []int{1024, 4096, 16384, 65536}

// BenchmarkCompress is the compression alone, as the engine does it: a pooled BestSpeed writer
// appending to a reused buffer. It is the number ADR 0031 quotes (3.5, 5.5, 13.4, 38.3 us).
func BenchmarkCompress(b *testing.B) {
	for _, size := range sizes {
		body := jsonBody(size)
		b.Run(fmt.Sprintf("%dB", len(body)), func(b *testing.B) {
			var out sink
			w, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out.b = out.b[:0]
				w.Reset(&out)
				w.Write(body)
				w.Close()
			}
			b.ReportMetric(float64(len(out.b)), "outB")
		})
	}
}

type sink struct{ b []byte }

func (s *sink) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }

// BenchmarkRequest is a whole request on one keep-alive in-memory connection, served by the
// engine, with the client reading the whole answer: "plain" has the client not accepting gzip,
// "gzip" has it accepting, against the same server with WithGzip. Their difference is what a
// compressed answer costs end to end (compression, plus the smaller write and read).
func BenchmarkRequest(b *testing.B) {
	for _, size := range sizes {
		body := jsonBody(size)
		for _, mode := range []string{"plain", "gzip"} {
			b.Run(fmt.Sprintf("%dB/%s", len(body), mode), func(b *testing.B) {
				app := nilogo.New(nilogo.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), nilogo.WithGzip(nilogo.Gzip{}))
				app.Get("/b", func(context.Context, struct{}) (nilogo.Bytes, error) {
					return nilogo.Bytes{ContentType: "application/json", Body: body}, nil
				})
				srv := nilotest.Start(b, app)
				c := srv.Dial("203.0.113.9:4000")
				defer c.Close()
				req := []byte("GET /b HTTP/1.1\r\nHost: x\r\n\r\n")
				if mode == "gzip" {
					req = []byte("GET /b HTTP/1.1\r\nHost: x\r\nAccept-Encoding: gzip\r\n\r\n")
				}
				buf := make([]byte, 128<<10)
				want := 0
				do := func() {
					c.Write(req)
					if want == 0 {
						want, _ = readFirst(c, buf)
						return
					}
					io.ReadFull(c, buf[:want])
				}
				do()
				if mode == "gzip" && !bytes.Contains(buf[:want], []byte("Content-Encoding: gzip")) {
					b.Fatal("the answer was not compressed")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					do()
				}
				b.ReportMetric(float64(want), "wireB")
			})
		}
	}
}

// readFirst reads the first answer whole: its head says its length.
func readFirst(c io.Reader, buf []byte) (int, error) {
	n := 0
	for {
		m, err := c.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
		i := bytes.Index(buf[:n], []byte("\r\n\r\n"))
		if i < 0 {
			continue
		}
		var cl int
		for _, l := range strings.Split(string(buf[:i]), "\r\n") {
			if v, ok := strings.CutPrefix(strings.ToLower(l), "content-length: "); ok {
				fmt.Sscan(v, &cl)
			}
		}
		if n >= i+4+cl {
			return i + 4 + cl, nil
		}
	}
}
