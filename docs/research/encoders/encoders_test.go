// Package encodersbench measures the levels ADR 0061 chose among: gzip at BestSpeed (ADR 0031),
// zstd at its fastest and default levels through EncodeAll, and brotli through its classic writer
// and its matchfinder writer, each reused as the engine and the modules reuse them, on the JSON
// body of docs/research/gzip.
package encodersbench

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/andybalholm/brotli/matchfinder"
	"github.com/klauspost/compress/zstd"
)

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

type sink struct{ b []byte }

func (s *sink) Write(p []byte) (int, error) { s.b = append(s.b, p...); return len(p), nil }

type resetter interface {
	io.WriteCloser
	Reset(io.Writer)
}

func streamEnc(w resetter) func(dst, src []byte) []byte {
	var out sink
	return func(dst, src []byte) []byte {
		out.b = dst
		w.Reset(&out)
		w.Write(src)
		w.Close()
		return out.b
	}
}

func encoders() []struct {
	name string
	enc  func(dst, src []byte) []byte
	dec  func([]byte) ([]byte, error)
} {
	gz, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	zf, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	zd, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	zl, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
	zdec, _ := zstd.NewReader(nil)
	gunzip := func(b []byte) ([]byte, error) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return io.ReadAll(r)
	}
	unbr := func(b []byte) ([]byte, error) { return io.ReadAll(brotli.NewReader(bytes.NewReader(b))) }
	unz := func(b []byte) ([]byte, error) { return zdec.DecodeAll(b, nil) }
	type row = struct {
		name string
		enc  func(dst, src []byte) []byte
		dec  func([]byte) ([]byte, error)
	}
	rows := []row{
		{"gzip-1", streamEnc(gz), gunzip},
		{"zstd-fastest", func(dst, src []byte) []byte { return zf.EncodeAll(src, dst) }, unz},
		{"zstd-default", func(dst, src []byte) []byte { return zd.EncodeAll(src, dst) }, unz},
		{"zstd-fastest-lowmem-1M", func(dst, src []byte) []byte { return zl.EncodeAll(src, dst) }, unz},
	}
	for _, l := range []int{0, 1, 4, 5} {
		rows = append(rows, row{fmt.Sprintf("br-classic-%d", l), streamEnc(brotli.NewWriterLevel(nil, l)), unbr})
	}
	for _, l := range []int{1, 4, 5} {
		w := brotli.NewWriterV2(nil, l)
		rows = append(rows, row{fmt.Sprintf("br-v2-%d", l), streamEnc(mfw{w}), unbr})
	}
	return rows
}

type mfw struct{ *matchfinder.Writer }

func (m mfw) Reset(w io.Writer) { m.Writer.Reset(w) }

func TestEveryEncoderRoundTrips(t *testing.T) {
	for _, e := range encoders() {
		for _, size := range sizes {
			body := jsonBody(size)
			out := e.enc(nil, body)
			back, err := e.dec(out)
			if err != nil || !bytes.Equal(back, body) {
				t.Fatalf("%s at %d: %v", e.name, size, err)
			}
			t.Logf("%s %d B -> %d B", e.name, len(body), len(out))
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	for _, e := range encoders() {
		for _, size := range sizes {
			body := jsonBody(size)
			b.Run(fmt.Sprintf("%s/%dB", e.name, len(body)), func(b *testing.B) {
				dst := make([]byte, 0, len(body))
				var out []byte
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					out = e.enc(dst[:0], body)
				}
				b.ReportMetric(float64(len(out)), "outB")
			})
		}
	}
}

// TestTheMemoryEachEncoderStateHolds is what one pooled state keeps after it compressed a 64 KiB
// body: the engine and the modules keep about GOMAXPROCS of them, since compression never blocks.
func TestTheMemoryEachEncoderStateHolds(t *testing.T) {
	body := jsonBody(65536)
	const n = 64
	for _, mk := range []struct {
		name string
		mk   func() func(dst, src []byte) []byte
	}{
		{"gzip-1", func() func(dst, src []byte) []byte {
			w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
			return streamEnc(w)
		}},
		{"zstd-fastest (one EncodeAll state)", func() func(dst, src []byte) []byte {
			z, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
			return func(dst, src []byte) []byte { return z.EncodeAll(src, dst) }
		}},
		{"zstd-fastest, 1 MiB window, lower memory", func() func(dst, src []byte) []byte {
			z, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true))
			return func(dst, src []byte) []byte { return z.EncodeAll(src, dst) }
		}},
		{"zstd-fastest, lower memory", func() func(dst, src []byte) []byte {
			z, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1), zstd.WithLowerEncoderMem(true))
			return func(dst, src []byte) []byte { return z.EncodeAll(src, dst) }
		}},
		{"br-v2-1", func() func(dst, src []byte) []byte { return streamEnc(mfw{brotli.NewWriterV2(nil, 1)}) }},
	} {
		keep := make([]func(dst, src []byte) []byte, n)
		before := heap()
		dst := make([]byte, 0, len(body))
		for i := range keep {
			keep[i] = mk.mk()
			keep[i](dst[:0], body)
		}
		after := heap()
		t.Logf("%s: %d bytes held per state", mk.name, (after-before)/n)
		runtime.KeepAlive(keep)
	}
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}
