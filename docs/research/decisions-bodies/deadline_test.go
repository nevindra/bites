package bodies

import (
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func pair(t testing.TB) (srv, cli net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	cli, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return <-ch, cli
}

// The cost of one SetWriteDeadline against one 100 byte write that a reader drains.
func BenchmarkSetWriteDeadline(b *testing.B) {
	s, c := pair(b)
	defer s.Close()
	defer c.Close()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
}

func BenchmarkFlushWithDeadline(b *testing.B) {
	for _, arm := range []bool{false, true} {
		name := "noarm"
		if arm {
			name = "arm"
		}
		b.Run(name, func(b *testing.B) {
			s, c := pair(b)
			defer s.Close()
			defer c.Close()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
			msg := []byte(strings.Repeat("x", 100))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if arm {
					s.SetWriteDeadline(time.Now().Add(30 * time.Second))
				}
				if _, err := s.Write(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// A client that never reads: how many 100 byte events does the server flush before a flush
// blocks, and how long after that does the per-flush deadline close it?
func TestNeverReadingClient(t *testing.T) {
	s, c := pair(t)
	defer s.Close()
	defer c.Close()
	msg := []byte(strings.Repeat("x", 100))
	sent := 0
	const to = 300 * time.Millisecond
	t0 := time.Now()
	for {
		s.SetWriteDeadline(time.Now().Add(to))
		t1 := time.Now()
		_, err := s.Write(msg)
		if err != nil {
			t.Logf("accepted %d events = %d KiB before a flush blocked; it failed %v later (os.ErrDeadlineExceeded=%v); total %v", sent, sent*100/1024, time.Since(t1).Round(10*time.Millisecond), os.IsTimeout(err), time.Since(t0).Round(10*time.Millisecond))
			return
		}
		sent++
	}
}

// A client that reads 1 byte per interval while the server flushes 4 KiB chunks and 64 KiB
// chunks, each with its own deadline: does the stall resolve inside the deadline?
func TestTrickleReader(t *testing.T) {
	for _, chunk := range []int{4 << 10, 64 << 10} {
		s, c := pair(t)
		stop := make(chan struct{})
		go func() {
			b := make([]byte, 1)
			for {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
					c.Read(b)
				}
			}
		}()
		msg := make([]byte, chunk)
		const to = 400 * time.Millisecond
		flushes := 0
		t0 := time.Now()
		var err error
		for err == nil {
			s.SetWriteDeadline(time.Now().Add(to))
			_, err = s.Write(msg)
			flushes++
		}
		t.Logf("chunk %d B, client 1 B / 50 ms: %d flushes (%d KiB) accepted, closed after %v with timeout=%v", chunk, flushes-1, (flushes-1)*chunk/1024, time.Since(t0).Round(10*time.Millisecond), os.IsTimeout(err))
		close(stop)
		s.Close()
		c.Close()
	}
}

// A declared Content-Length of 32 MiB, reserved up front by Exchange.readLength (make): what
// do 100 of them hold before one byte arrives?
func TestReserveUpFront(t *testing.T) {
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	keep := make([][]byte, 100)
	for i := range keep {
		keep[i] = make([]byte, 32<<20)
	}
	runtime.ReadMemStats(&m1)
	rss := func() string {
		b, _ := os.ReadFile("/proc/self/statm")
		f := strings.Fields(string(b))
		return f[1]
	}()
	t.Logf("100 x make([]byte, 32 MiB), untouched: HeapAlloc +%d MiB, HeapSys +%d MiB, resident pages=%s (x4 KiB)", (m1.HeapAlloc-m0.HeapAlloc)>>20, (m1.HeapSys-m0.HeapSys)>>20, rss)
	runtime.KeepAlive(keep)
}
