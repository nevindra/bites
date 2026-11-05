// The load, idle, allocation and wait client of the harness. It is deliberately plain: raw TCP,
// one goroutine a connection, a hand-written response reader that wants a Content-Length and a
// 200. Modes:
//
//	wait   poll until the server accepts a connection
//	load   keep-alive connections for -warm then -dur: req/s, p50/p99/p999, the server's CPU
//	       per request (from /proc/<pid>/stat) and its peak RSS
//	idle   open -n1 then -n2 connections, each served one request, and snapshot the server's
//	       RSS and (if it answers SIGUSR1) heap plus stack after a settle longer than 100 ms
//	       (ADR 0005 moves an idle connection to a fresh goroutine after 100 ms idle)
//	allocs one connection, -n requests, the server's mallocs delta over them
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
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
	mode   = flag.String("mode", "load", "wait|load|idle|allocs")
	addr   = flag.String("addr", "127.0.0.1:19001", "server address")
	method = flag.String("method", "GET", "GET or POST")
	path   = flag.String("path", "/users/7", "request target, query included")
	body   = flag.String("body", "", "request body (POST)")
	conns  = flag.Int("c", 256, "load connections")
	warm   = flag.Duration("warm", 3*time.Second, "load warm-up, not measured")
	dur    = flag.Duration("dur", 10*time.Second, "load measuring time")
	pid    = flag.Int("pid", 0, "server pid")
	stats  = flag.String("stats", "", "file the server writes its stats to on SIGUSR1 ($STATS of the server)")
	n1     = flag.Int("n1", 10000, "idle: first connection count")
	n2     = flag.Int("n2", 20000, "idle: second connection count")
	settle = flag.Duration("settle", 5*time.Second, "idle: wait before each snapshot")
	n      = flag.Int("n", 50000, "allocs: measured requests")
	useTLS = flag.Bool("tls", false, "speak TLS (the certificate is not verified); default off")
)

func procStatus(key string) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", *pid))
	if err != nil {
		return -1
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, key+":") {
			f := strings.Fields(l)
			v, _ := strconv.ParseInt(f[1], 10, 64)
			return v * 1024
		}
	}
	return -1
}

// cpuTicks is utime plus stime of the server in clock ticks (10 ms each).
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

// serverStats asks the server for its stats; nil when it has no stats hook (nilo, in Zig).
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

// dial binds the connection to 127.0.0.2 to 127.0.0.5 in turn so loopback does not run out of
// ports in TIME_WAIT.
func dial(i int) (net.Conn, error) {
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}, Timeout: 10 * time.Second,
		Control: func(network, address string, rc syscall.RawConn) error {
			return rc.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, 24, 1) // IP_BIND_ADDRESS_NO_PORT
			})
		}}
	nc, err := d.Dial("tcp", *addr)
	if err != nil || !*useTLS {
		return nc, err
	}
	tc := tls.Client(nc, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	if err := tc.Handshake(); err != nil {
		nc.Close()
		return nil, err
	}
	return tc, nil
}

// roundtrip sends req and reads one whole response; false on an error, a non-200 or a response
// without Content-Length.
func roundtrip(c net.Conn, req, buf []byte) bool {
	if _, err := c.Write(req); err != nil {
		return false
	}
	n := 0
	for {
		m, err := c.Read(buf[n:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			return false
		}
		n += m
		he := bytes.Index(buf[:n], []byte("\r\n\r\n"))
		if he < 0 {
			if n == len(buf) {
				return false
			}
			continue
		}
		if he < 12 || string(buf[9:12]) != "200" {
			fmt.Fprintf(os.Stderr, "unexpected response: %q\n", buf[:n])
			return false
		}
		cl := -1
		if i := bytes.Index(buf[:he], []byte("ength:")); i >= 0 {
			j := i + 6
			for buf[j] == ' ' {
				j++
			}
			k := j
			for k < he && buf[k] >= '0' && buf[k] <= '9' {
				k++
			}
			cl, _ = strconv.Atoi(string(buf[j:k]))
		}
		if cl < 0 {
			fmt.Fprintf(os.Stderr, "no content-length: %q\n", buf[:n])
			return false
		}
		if n >= he+4+cl {
			return true
		}
	}
}

// rawConn is the TCP connection under c, which is c itself unless -tls wrapped it.
func rawConn(c net.Conn) *net.TCPConn {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	return c.(*net.TCPConn)
}

func request() []byte {
	r := *method + " " + *path + " HTTP/1.1\r\nHost: x\r\n"
	if *method == "POST" {
		r += "Content-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(*body)) + "\r\n"
	}
	return []byte(r + "\r\n" + *body)
}

func main() {
	flag.Parse()
	switch *mode {
	case "wait":
		for i := 0; i < 200; i++ {
			if c, err := net.Dial("tcp", *addr); err == nil {
				c.Close()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		os.Exit(1)
	case "idle":
		runIdle()
	case "load":
		runLoad()
	case "allocs":
		runAllocs()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", *mode)
		os.Exit(2)
	}
}

func emit(out map[string]any) {
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func runAllocs() {
	c, err := dial(0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	req, buf := request(), make([]byte, 8192)
	for i := 0; i < 2000; i++ { // warm: first-use allocations, pools, caches
		if !roundtrip(c, req, buf) {
			os.Exit(1)
		}
	}
	time.Sleep(200 * time.Millisecond)
	a := serverStats()
	for i := 0; i < *n; i++ {
		if !roundtrip(c, req, buf) {
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
	out := map[string]any{"rss0": procStatus("VmRSS")}
	out["stats0"] = serverStats()
	req := request()
	var all []net.Conn
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
				c, err := dial(i)
				if err != nil {
					fails.Add(1)
					fmt.Fprintln(os.Stderr, "dial:", err)
					return
				}
				if !roundtrip(c, req, make([]byte, 8192)) {
					fails.Add(1)
					c.Close()
					return
				}
				mu.Lock()
				all = append(all, c)
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
	for _, c := range all {
		rawConn(c).SetLinger(0)
		c.Close()
	}
}

func runLoad() {
	req := request()
	var stop, measuring atomic.Bool
	lats := make([][]int32, *conns)
	var errs atomic.Int64
	var wg, ready sync.WaitGroup
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		ready.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := dial(i)
			ready.Done()
			if err != nil {
				errs.Add(1)
				return
			}
			defer c.Close()
			rawConn(c).SetLinger(0)
			buf := make([]byte, 8192)
			ls := make([]int32, 0, 1<<14)
			for !stop.Load() {
				t := time.Now()
				if !roundtrip(c, req, buf) {
					errs.Add(1)
					return
				}
				if measuring.Load() {
					ls = append(ls, int32(time.Since(t)/1000)) // microseconds
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
		cpu = float64(c1-c0) * 1e7 / float64(len(all)) // ns of server CPU per request
	}
	emit(map[string]any{"reqs": len(all), "rps": float64(len(all)) / el.Seconds(), "p50_us": q(.5), "p99_us": q(.99), "p999_us": q(.999),
		"cpu_ns_per_req": cpu, "errors": errs.Load(), "peak_rss_sampled": peakRSS, "vmhwm": hwm})
}
