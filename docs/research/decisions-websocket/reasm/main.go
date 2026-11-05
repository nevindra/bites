// Probe for R6: what the ws.Reader does with a 64 KiB frame, whether a decoded string aliases the
// buffer it came from, and what reassembly strategies cost. Run: go run ./reasm
package main

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/nevindra/bites/internal/jsoncodec"
	"github.com/nevindra/bites/internal/wire"
	"github.com/nevindra/bites/internal/ws"
)

type Msg struct {
	Room string `json:"room"`
	Text string `json:"text"`
}

func feedAll(r *ws.Reader, wire []byte, chunk int) (events, starts, ends int, aliased bool) {
	aliased = true
	now := time.Now()
	for off := 0; off < len(wire); {
		end := min(off+chunk, len(wire))
		in := wire[off:end]
		for len(in) > 0 {
			n, ev := r.Feed(in, now)
			if ev.Kind == ws.KindData {
				events++
				if ev.Start {
					starts++
				}
				if ev.End {
					ends++
				}
				if len(ev.Payload) > 0 {
					p := uintptr(unsafe.Pointer(&ev.Payload[0]))
					b := uintptr(unsafe.Pointer(&wire[0]))
					if p < b || p >= b+uintptr(len(wire)) {
						aliased = false
					}
				}
			}
			in = in[n:]
		}
		off = end
	}
	return
}

func main() {
	lim := ws.DefaultLimits()
	payload := make([]byte, 64<<10-1)
	for i := range payload {
		payload[i] = 'a'
	}
	frame := ws.AppendClientFrame(nil, true, ws.OpBinary, payload, [4]byte{1, 2, 3, 4})
	fmt.Printf("frame bytes for payload 65535: %d (header %d + mask 4)\n", len(frame), ws.HeaderLen(65535))

	for _, chunk := range []int{4096, 65536, len(frame)} {
		var r ws.Reader
		r.Reset(&lim)
		f := append([]byte(nil), frame...)
		ev, st, en, al := feedAll(&r, f, chunk)
		fmt.Printf("single frame fed in %6d byte reads: data events %3d, with Start %d, with End %d, payload always inside fed slice: %v\n", chunk, ev, st, en, al)
	}
	// A 3000 byte message, one frame, split in two reads at 1500.
	p3 := make([]byte, 3000)
	for i := range p3 {
		p3[i] = 'b'
	}
	f3 := ws.AppendClientFrame(nil, true, ws.OpBinary, p3, [4]byte{9, 9, 9, 9})
	var r ws.Reader
	r.Reset(&lim)
	ev, st, en, _ := feedAll(&r, f3, 1500)
	fmt.Printf("3000 byte single frame in two reads: data events %d, Start %d, End %d (so a non-fragmented message can still need assembly)\n", ev, st, en)

	// Does a decoded string alias the buffer? (ADR 0008)
	d, err := jsoncodec.CompileDecoder(reflect.TypeFor[Msg](), jsoncodec.Options{})
	if err != nil {
		panic(err)
	}
	body := []byte(`{"room":"general","text":"hello world"}`)
	readbuf := append([]byte(nil), body...)
	var m Msg
	var errs jsoncodec.Errors
	if err := d.Decode(readbuf, unsafe.Pointer(&m), wire.NewSlab(256), &errs); err != nil {
		panic(err)
	}
	for i := range readbuf { // the engine reuses the read buffer for the next frame
		readbuf[i] = 'X'
	}
	fmt.Printf("decode straight from the read buffer, then buffer reused: Text = %q (ADR 0008 broken if not the original)\n", m.Text)

	// Decode from a fresh per-message slab (copy then decode).
	readbuf = append(readbuf[:0], body...)
	var m2 Msg
	slab := wire.NewSlab(len(readbuf))
	cp := slab.Reserve(len(readbuf))
	copy(cp, readbuf)
	if err := d.Decode(cp, unsafe.Pointer(&m2), slab, &errs); err != nil {
		panic(err)
	}
	for i := range readbuf {
		readbuf[i] = 'X'
	}
	fmt.Printf("decode from a fresh slab copy, then buffer reused: Text = %q\n", m2.Text)

	runtime.GC()
	a0 := testing.AllocsPerRun(1000, func() {
		var m Msg
		var errs jsoncodec.Errors
		s := wire.NewSlab(len(body)) // NewSlab is 2 allocs; Init on a stack Slab is 1
		_ = s
		cp := make([]byte, len(body))
		copy(cp, body)
		_ = d.Decode(cp, unsafe.Pointer(&m), s, &errs)
		sink = m
	})
	a1 := testing.AllocsPerRun(1000, func() {
		var m Msg
		var errs jsoncodec.Errors
		var s wire.Slab
		s.Init(len(body))
		cp := s.Reserve(len(body))
		copy(cp, body)
		_ = d.Decode(cp, unsafe.Pointer(&m), &s, &errs)
		sink = m
	})
	a2 := testing.AllocsPerRun(1000, func() {
		var m Msg
		var errs jsoncodec.Errors
		var s wire.Slab // zero slab, body aliased from the reusable buffer: strings copied? no, they alias
		_ = d.Decode(readbuf2, unsafe.Pointer(&m), &s, &errs)
		sink = m
	})
	fmt.Printf("allocs per message (%d byte body): copy to fresh slab then decode = %.0f (Init on stack Slab), %.0f (NewSlab+extra copy); decode in place from reused buffer = %.0f\n", len(body), a1, a0, a2)
	ns := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var m Msg
			var errs jsoncodec.Errors
			var s wire.Slab
			s.Init(len(body))
			cp := s.Reserve(len(body))
			copy(cp, body)
			_ = d.Decode(cp, unsafe.Pointer(&m), &s, &errs)
			sink = m
		}
	})
	ns2 := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var m Msg
			var errs jsoncodec.Errors
			var s wire.Slab
			_ = d.Decode(readbuf2, unsafe.Pointer(&m), &s, &errs)
			sink = m
		}
	})
	fmt.Printf("ns per message: fresh slab copy %d ns (%d B/op, %d allocs); in place %d ns (%d B/op, %d allocs)\n", ns.NsPerOp(), ns.AllocedBytesPerOp(), ns.AllocsPerOp(), ns2.NsPerOp(), ns2.AllocedBytesPerOp(), ns2.AllocsPerOp())

	// 20,000 sockets each holding 64 KiB less one byte of an unfinished message.
	for _, strat := range []string{"exact", "doubling-append", "class-4/16/64K"} {
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		bufs := make([][]byte, 20000)
		chunk := make([]byte, 4096)
		for i := range bufs {
			switch strat {
			case "exact":
				bufs[i] = make([]byte, 0, 65535)
				bufs[i] = append(bufs[i], make([]byte, 65535)...)
			case "doubling-append":
				var b []byte
				for len(b) < 65535 {
					b = append(b, chunk[:min(4096, 65535-len(b))]...)
				}
				bufs[i] = b
			case "class-4/16/64K":
				b := make([]byte, 0, 4096)
				for len(b) < 65535 {
					if len(b)+4096 > cap(b) {
						nb := make([]byte, len(b), min(cap(b)*4, 65536))
						copy(nb, b)
						b = nb
					}
					b = append(b, chunk[:min(4096, 65535-len(b))]...)
				}
				bufs[i] = b
			}
		}
		runtime.ReadMemStats(&m1)
		fmt.Printf("%-16s 20000 x (64 KiB-1) held: HeapInuse +%d MiB, TotalAlloc +%d MiB, mallocs per socket %.1f, cap %d\n", strat, (m1.HeapInuse-m0.HeapInuse)>>20, (m1.TotalAlloc-m0.TotalAlloc)>>20, float64(m1.Mallocs-m0.Mallocs)/20000, cap(bufs[0]))
		runtime.KeepAlive(bufs)
		bufs = nil
	}
}

var sink Msg
var readbuf2 = []byte(`{"room":"general","text":"hello world"}`)
