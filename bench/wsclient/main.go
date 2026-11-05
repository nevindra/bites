// The WebSocket client of the harness, the WebSocket twin of client/: raw TCP, one goroutine a
// connection, the upgrade written and the 101 read by hand, and every message one masked text
// frame built once at start (the key is fixed, so masking costs the client nothing a message).
// Each message waits for the server's echo, whatever its opcode (bites answers a []byte message
// with a binary frame). Modes:
//
//	load   -c sockets for -warm then -dur: messages a second, p50/p99/p999 of the round trip,
//	       the server's CPU per message (from /proc/<pid>/stat) and its peak RSS
//	idle   open -n1 then -n2 sockets, each echoing one message, and snapshot the server's RSS
//	       and heap plus stack (SIGUSR1) after a settle
//	allocs one socket, -n messages, the server's mallocs delta over them
//
// The payload is a JSON object {"text":"xxx..."} of -size bytes, so one client serves both the
// raw echo routes and the JSON ones.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	mode   = flag.String("mode", "load", "load|idle|allocs")
	addr   = flag.String("addr", "127.0.0.1:19001", "server address")
	path   = flag.String("path", "/ws", "upgrade target")
	size   = flag.Int("size", 32, "payload bytes, at least 12")
	conns  = flag.Int("c", 256, "load sockets")
	warm   = flag.Duration("warm", 3*time.Second, "load warm-up, not measured")
	dur    = flag.Duration("dur", 10*time.Second, "load measuring time")
	pid    = flag.Int("pid", 0, "server pid")
	stats  = flag.String("stats", "", "file the server writes its stats to on SIGUSR1")
	n1     = flag.Int("n1", 10000, "idle: first socket count")
	n2     = flag.Int("n2", 20000, "idle: second socket count")
	settle = flag.Duration("settle", 5*time.Second, "idle: wait before each snapshot")
	n      = flag.Int("n", 50000, "allocs: measured messages")
)

func procStatus(key string) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", *pid))
	if err != nil {
		return -1
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key+":") {
			v, _ := strconv.ParseInt(strings.Fields(l)[1], 10, 64)
			return v * 1024
		}
	}
	return -1
}

func cpuTicks() int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", *pid))
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
	u, _ := strconv.ParseInt(f[11], 10, 64)
	s, _ := strconv.ParseInt(f[12], 10, 64)
	return u + s
}

func serverStats() map[string]any {
	if *stats == "" {
		return nil
	}
	os.Remove(*stats)
	syscall.Kill(*pid, syscall.SIGUSR1)
	for i := 0; i < 100; i++ {
		time.Sleep(50 * time.Millisecond)
		if b, err := os.ReadFile(*stats); err == nil {
			m := map[string]any{}
			if json.Unmarshal(b, &m) == nil {
				return m
			}
		}
	}
	return nil
}

// frame is the masked text frame every message sends, built once.
var frame []byte

func buildFrame() {
	if *size < 12 {
		*size = 12
	}
	p := []byte(`{"text":"` + strings.Repeat("x", *size-11) + `"}`)
	key := [4]byte{0x12, 0x34, 0x56, 0x78}
	f := []byte{0x81}
	switch l := len(p); {
	case l < 126:
		f = append(f, 0x80|byte(l))
	case l < 1<<16:
		f = append(f, 0x80|126, byte(l>>8), byte(l))
	default:
		f = append(f, 0x80|127)
		f = binary.BigEndian.AppendUint64(f, uint64(l))
	}
	f = append(f, key[:]...)
	for i, c := range p {
		f = append(f, c^key[i&3])
	}
	frame = f
}

type sock struct {
	c   net.Conn
	buf []byte
}

func dial(i int) (*sock, error) {
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}, Timeout: 10 * time.Second,
		Control: func(network, address string, rc syscall.RawConn) error {
			return rc.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, 24, 1) // IP_BIND_ADDRESS_NO_PORT
			})
		}}
	c, err := d.Dial("tcp", *addr)
	if err != nil {
		return nil, err
	}
	req := "GET " + *path + " HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: http://x\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		c.Close()
		return nil, err
	}
	s := &sock{c: c, buf: make([]byte, 0, 4096+*size)}
	b := s.buf[:cap(s.buf)]
	got := 0
	for {
		m, err := c.Read(b[got:])
		if err != nil {
			c.Close()
			return nil, err
		}
		got += m
		if he := bytes.Index(b[:got], []byte("\r\n\r\n")); he >= 0 {
			if !bytes.HasPrefix(b, []byte("HTTP/1.1 101")) {
				c.Close()
				return nil, fmt.Errorf("upgrade refused: %q", b[:got])
			}
			s.buf = append(s.buf[:0], b[he+4:got]...)
			return s, nil
		}
	}
}

// fill reads until s.buf holds at least k bytes.
func (s *sock) fill(k int) error {
	for len(s.buf) < k {
		if cap(s.buf) < k {
			nb := make([]byte, len(s.buf), k*2)
			copy(nb, s.buf)
			s.buf = nb
		}
		m, err := s.c.Read(s.buf[len(s.buf):cap(s.buf)])
		if err != nil {
			return err
		}
		s.buf = s.buf[:len(s.buf)+m]
	}
	return nil
}

// echo sends the frame and reads one whole message back: its frames up to the one with FIN
// (a streaming writer, as gorilla's and coder's, may end with an empty continuation frame).
func (s *sock) echo() error {
	if _, err := s.c.Write(frame); err != nil {
		return err
	}
	total := 0
	for first := true; ; first = false {
		if err := s.fill(2); err != nil {
			return err
		}
		fin, op, l, h := s.buf[0]&0x80 != 0, s.buf[0]&0x0f, int(s.buf[1]&0x7f), 2
		if first && op != 1 && op != 2 || !first && op != 0 {
			return fmt.Errorf("unexpected opcode %d: %q", op, s.buf)
		}
		switch l {
		case 126:
			if err := s.fill(4); err != nil {
				return err
			}
			l, h = int(binary.BigEndian.Uint16(s.buf[2:])), 4
		case 127:
			if err := s.fill(10); err != nil {
				return err
			}
			l, h = int(binary.BigEndian.Uint64(s.buf[2:])), 10
		}
		if err := s.fill(h + l); err != nil {
			return err
		}
		total += l
		s.buf = s.buf[:copy(s.buf, s.buf[h+l:])]
		if fin {
			break
		}
	}
	if total != *size && total != *size+1 { // +1: a JSON encoder that ends with a newline
		return fmt.Errorf("echo of %d bytes, sent %d", total, *size)
	}
	return nil
}

func (s *sock) close() {
	s.c.(*net.TCPConn).SetLinger(0)
	s.c.Close()
}

func emit(out map[string]any) {
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func main() {
	flag.Parse()
	buildFrame()
	switch *mode {
	case "load":
		runLoad()
	case "idle":
		runIdle()
	case "allocs":
		runAllocs()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", *mode)
		os.Exit(2)
	}
}

func runAllocs() {
	s, err := dial(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer s.close()
	for i := 0; i < 2000; i++ {
		if err := s.echo(); err != nil {
			fmt.Fprintln(os.Stderr, "echo:", err)
			os.Exit(1)
		}
	}
	time.Sleep(200 * time.Millisecond)
	a := serverStats()
	for i := 0; i < *n; i++ {
		if err := s.echo(); err != nil {
			fmt.Fprintln(os.Stderr, "echo:", err)
			os.Exit(1)
		}
	}
	b := serverStats()
	if a == nil || b == nil {
		emit(map[string]any{"error": "no server stats"})
		os.Exit(1)
	}
	d := func(k string) float64 { return (b[k].(float64) - a[k].(float64)) / float64(*n) }
	emit(map[string]any{"n": *n, "allocs_per_req": d("mallocs"), "bytes_per_req": d("total_alloc"), "num_gc": b["num_gc"].(float64) - a["num_gc"].(float64)})
}

func runIdle() {
	out := map[string]any{"rss0": procStatus("VmRSS"), "stats0": serverStats()}
	var all []*sock
	var mu sync.Mutex
	open := func(from, to int) {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 64)
		var fails atomic.Int64
		for i := from; i < to; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				s, err := dial(i)
				if err == nil {
					err = s.echo()
				}
				if err != nil {
					fails.Add(1)
					fmt.Fprintln(os.Stderr, "socket:", err)
					if s != nil {
						s.close()
					}
					return
				}
				mu.Lock()
				all = append(all, s)
				mu.Unlock()
			}(i)
		}
		wg.Wait()
		if f := fails.Load(); f > 0 {
			out["fails"] = f
		}
	}
	snap := func(tag string) {
		time.Sleep(*settle)
		out["rss_"+tag] = procStatus("VmRSS")
		out["stats_"+tag] = serverStats()
		out["conns_"+tag] = len(all)
	}
	open(0, *n1)
	snap("n1")
	open(*n1, *n2)
	snap("n2")
	emit(out)
	for _, s := range all {
		s.close()
	}
}

func runLoad() {
	var stop, measuring atomic.Bool
	lats := make([][]int32, *conns)
	var errs atomic.Int64
	var wg, ready sync.WaitGroup
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		ready.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := dial(i)
			ready.Done()
			if err != nil {
				errs.Add(1)
				fmt.Fprintln(os.Stderr, "dial:", err)
				return
			}
			defer s.close()
			ls := make([]int32, 0, 1<<14)
			for !stop.Load() {
				t := time.Now()
				if err := s.echo(); err != nil {
					errs.Add(1)
					fmt.Fprintln(os.Stderr, "echo:", err)
					return
				}
				if measuring.Load() {
					ls = append(ls, int32(time.Since(t)/1000))
				}
			}
			lats[i] = ls
		}(i)
	}
	ready.Wait()
	time.Sleep(*warm)
	var peakRSS int64
	c0 := cpuTicks()
	measuring.Store(true)
	t0 := time.Now()
	for end := t0.Add(*dur); time.Now().Before(end); {
		time.Sleep(100 * time.Millisecond)
		if r := procStatus("VmRSS"); r > peakRSS {
			peakRSS = r
		}
	}
	measuring.Store(false)
	el := time.Since(t0)
	c1 := cpuTicks()
	stop.Store(true)
	hwm := procStatus("VmHWM")
	wg.Wait()
	var all []int32
	for _, l := range lats {
		all = append(all, l...)
	}
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	q := func(p float64) int32 {
		if len(all) == 0 {
			return 0
		}
		return all[int(float64(len(all)-1)*p)]
	}
	cpu := 0.0
	if len(all) > 0 {
		cpu = float64(c1-c0) * 1e7 / float64(len(all))
	}
	emit(map[string]any{"reqs": len(all), "rps": float64(len(all)) / el.Seconds(), "p50_us": q(.5), "p99_us": q(.99), "p999_us": q(.999),
		"cpu_ns_per_req": cpu, "errors": errs.Load(), "peak_rss_sampled": peakRSS, "vmhwm": hwm})
}
