// Probe for R7: the cost of getting one framed message onto N loopback TCP sockets, by writer model.
// Both ends live in this process; the client ends never read (a frame is 16 bytes, the kernel holds it).
// Run: taskset -c 0,1,8,9 go run ./bcast -n 20000
package main

import (
	"flag"
	"fmt"
	"net"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type sock struct {
	c  net.Conn
	rc syscall.RawConn
	mu sync.Mutex
	q  [][]byte // queue of shared frames
	fl bool     // a flusher is running
	ch chan []byte
	wf []byte // H: the frame being written by the callback
	wn int
	we error
	cb func(fd uintptr) bool
}

var frame = append([]byte{0x81, 14}, []byte("hello, 20k chat")...)[:16]

func heapStack() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64() + s[1].Value.Uint64()
}

func (s *sock) send(wg *sync.WaitGroup) { // R7: queue, start a flusher only if none runs
	s.mu.Lock()
	s.q = append(s.q, frame)
	start := !s.fl
	s.fl = true
	s.mu.Unlock()
	if start {
		wg.Add(1)
		go s.flush(wg)
	}
}

func (s *sock) flush(wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		s.mu.Lock()
		if len(s.q) == 0 {
			s.fl = false
			s.mu.Unlock()
			return
		}
		f := s.q[0]
		s.q = s.q[1:]
		s.mu.Unlock()
		s.c.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)) // the deadline of ADR 0037 (200 ms here so the probe ends)
		s.c.Write(f)
	}
}

func (s *sock) nbWrite(f []byte) (queued bool) { // inline, never blocks: EAGAIN means queue
	var n int
	var err error
	s.rc.Write(func(fd uintptr) bool {
		n, err = syscall.Write(int(fd), f)
		return true
	})
	return err != nil || n < len(f)
}

// hybrid: write inline without blocking; if the kernel took less than the frame, queue the rest and
// start a flusher (which blocks, under a deadline) for this socket alone.
func (s *sock) hybrid(wg *sync.WaitGroup) {
	s.wf, s.wn, s.we = frame, 0, nil
	err := s.rc.Write(s.cb)
	if err == nil && s.we == nil && s.wn == len(frame) {
		return
	}
	s.send(wg)
}

func main() {
	n := flag.Int("n", 20000, "sockets")
	bursts := flag.Int("bursts", 5, "bursts per mode")
	flag.Parse()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := make([]*sock, *n)
	var cli []net.Conn
	go func() {}()
	t0 := time.Now()
	for i := 0; i < *n; i++ {
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}}
		c, err := d.Dial("tcp", ln.Addr().String())
		if err != nil {
			panic(err)
		}
		cli = append(cli, c)
		sc, err := ln.Accept()
		if err != nil {
			panic(err)
		}
		s := &sock{c: sc}
		s.rc, _ = sc.(*net.TCPConn).SyscallConn()
		s.cb = func(fd uintptr) bool { s.wn, s.we = syscall.Write(int(fd), s.wf); return true }
		srv[i] = s
	}
	fmt.Printf("%d sockets up in %v, GOMAXPROCS %d\n", *n, time.Since(t0).Round(time.Millisecond), runtime.GOMAXPROCS(0))
	var ms0, ms1 runtime.MemStats

	run := func(name string, burst func()) {
		var ds []float64
		stop := make(chan struct{})
		peak := make(chan [2]uint64)
		go func() { // samples the stacks and the goroutine count every 200 us
			var mx [2]uint64
			sm := []metrics.Sample{{Name: "/memory/classes/heap/stacks:bytes"}, {Name: "/sched/goroutines:goroutines"}}
			for {
				select {
				case <-stop:
					peak <- mx
					return
				default:
				}
				metrics.Read(sm)
				mx[0], mx[1] = max(mx[0], sm[0].Value.Uint64()), max(mx[1], sm[1].Value.Uint64())
				time.Sleep(200 * time.Microsecond)
			}
		}()
		runtime.GC()
		runtime.ReadMemStats(&ms0)
		for b := 0; b < *bursts; b++ {
			t := time.Now()
			burst()
			ds = append(ds, float64(time.Since(t).Microseconds())/1000)
		}
		runtime.ReadMemStats(&ms1)
		close(stop)
		pk := <-peak
		sort.Float64s(ds)
		fmt.Printf("peak stacks %d MiB, goroutines %d | ", pk[0]>>20, pk[1])
		fmt.Printf("%-34s burst ms min %.1f med %.1f max %.1f | ns/socket med %.0f | mallocs/socket/burst %.2f | B/socket/burst %.0f\n", name, ds[0], ds[len(ds)/2], ds[len(ds)-1], ds[len(ds)/2]*1e6/float64(*n), float64(ms1.Mallocs-ms0.Mallocs)/float64(*n**bursts), float64(ms1.TotalAlloc-ms0.TotalAlloc)/float64(*n**bursts))
	}

	run("A inline blocking Write, 1 goroutine", func() {
		for _, s := range srv {
			s.c.Write(frame)
		}
	})
	run("A2 inline nonblocking (SyscallConn)", func() {
		for _, s := range srv {
			s.nbWrite(frame)
		}
	})
	run("B go func per socket per burst", func() {
		var wg sync.WaitGroup
		for _, s := range srv {
			wg.Add(1)
			go func() { defer wg.Done(); s.c.Write(frame) }()
		}
		wg.Wait()
	})
	run("D R7 queue + flusher if none runs", func() {
		var wg sync.WaitGroup
		for _, s := range srv {
			s.send(&wg)
		}
		wg.Wait()
	})
	// Same, with 4 publisher goroutines each handling a quarter, as a broadcast loop could.
	run("D4 R7 with 4 publishers", func() {
		var wg sync.WaitGroup
		var pw sync.WaitGroup
		q := *n / 4
		for p := 0; p < 4; p++ {
			pw.Add(1)
			go func() {
				defer pw.Done()
				for _, s := range srv[p*q : (p+1)*q] {
					s.send(&wg)
				}
			}()
		}
		pw.Wait()
		wg.Wait()
	})
	run("A4 inline blocking, 4 publishers", func() {
		var pw sync.WaitGroup
		q := *n / 4
		for p := 0; p < 4; p++ {
			pw.Add(1)
			go func() {
				defer pw.Done()
				for _, s := range srv[p*q : (p+1)*q] {
					s.c.Write(frame)
				}
			}()
		}
		pw.Wait()
	})

	for _, s := range srv {
		s.c.SetWriteDeadline(time.Time{})
	}
	runHybrid := func(workers int) func() {
		return func() {
			var wg, pw sync.WaitGroup
			q := *n / workers
			for p := 0; p < workers; p++ {
				pw.Add(1)
				go func() {
					defer pw.Done()
					for _, s := range srv[p*q : (p+1)*q] {
						s.hybrid(&wg)
					}
				}()
			}
			pw.Wait()
			wg.Wait()
		}
	}
	run("H hybrid, 4 workers inline nonblocking", runHybrid(4))
	run("H16 hybrid, 16 workers", runHybrid(16))

	// C: a persistent writer goroutine per socket.
	for _, s := range srv {
		s.c.SetWriteDeadline(time.Time{})
	}
	runtime.GC()
	before := heapStack()
	var done atomic.Int64
	for _, s := range srv {
		s.ch = make(chan []byte, 1)
		go func() {
			for f := range s.ch {
				s.c.Write(f)
				done.Add(1)
			}
		}()
	}
	for _, s := range srv { // one write each so the stacks have grown as they will
		s.ch <- frame
	}
	for done.Load() < int64(*n) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	after := heapStack()
	fmt.Printf("C persistent writer goroutine + chan(1): +%d B heap+stack per socket (measured, after one write each)\n", (after-before)/uint64(*n))
	run("C persistent writer per socket", func() {
		done.Store(0)
		for _, s := range srv {
			s.ch <- frame
		}
		for done.Load() < int64(*n) {
			runtime.Gosched()
		}
	})
	for _, s := range srv {
		close(s.ch)
	}

	// Stalled reader: socket 0 gets a tiny send buffer and is filled; clients never read.
	s0 := srv[0]
	cli[0].(*net.TCPConn).SetReadBuffer(2048)
	s0.c.(*net.TCPConn).SetWriteBuffer(4096)
	big := make([]byte, 1<<16)
	for {
		s0.c.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
		if _, err := s0.c.Write(big); err != nil {
			break
		}
	}
	for fails, it := 0, 0; fails < 5 && it < 200000; it++ { // until frame-sized writes keep timing out
		s0.c.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
		if _, err := s0.c.Write(frame); err != nil {
			fails++
		} else {
			fails = 0
		}
	}
	fmt.Println("socket 0 is now stalled (send buffer full)")
	deadline := 200 * time.Millisecond
	{
		s0.c.SetWriteDeadline(time.Now().Add(deadline))
		t1 := time.Now()
		k, err := s0.c.Write(frame)
		fmt.Printf("one write of 16 bytes to the stalled socket: %d bytes, err %v, after %v\n", k, err, time.Since(t1).Round(time.Millisecond))
	}
	t := time.Now()
	for _, s := range srv {
		s.c.SetWriteDeadline(time.Now().Add(deadline))
		s.c.Write(frame)
	}
	fmt.Printf("A inline blocking with a stalled socket first, write deadline %v: burst %.1f ms (one publisher goroutine held)\n", deadline, float64(time.Since(t).Microseconds())/1000)
	t = time.Now()
	for _, s := range srv {
		s.nbWrite(frame)
	}
	fmt.Printf("A2 inline nonblocking with the stalled socket: burst %.1f ms\n", float64(time.Since(t).Microseconds())/1000)
	t = time.Now()
	{
		var wgh sync.WaitGroup
		for _, s := range srv {
			s.hybrid(&wgh)
		}
		fmt.Printf("H hybrid with the stalled socket, one worker: publisher returned after %.1f ms; ", float64(time.Since(t).Microseconds())/1000)
		wgh.Wait()
		fmt.Printf("whole %.1f ms\n", float64(time.Since(t).Microseconds())/1000)
	}
	t = time.Now()
	var wg sync.WaitGroup
	for _, s := range srv {
		s.send(&wg)
	}
	enq := time.Since(t)
	fmt.Printf("D R7 with the stalled socket: publisher returned after %.1f ms (flusher of socket 0 is still blocked)\n", float64(enq.Microseconds())/1000)
	wg.Wait()
	fmt.Printf("D R7 whole burst incl. stalled flusher: %.1f ms\n", float64(time.Since(t).Microseconds())/1000)
	_ = cli
}
