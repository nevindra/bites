// The client of the parking measurement (docs/research/parking), for what bench/client does not
// do. Modes:
//
//	wsidle  open -n1 then -n2 WebSockets (each upgrades, sends one message and reads the echo),
//	        snapshot the server's RSS and heap plus stack after -settle each time (as ws-gate)
//	wake    -c connections, each sending one request (or a WebSocket message) every -gap,
//	        jittered by 10%, so that with a gap past 100 ms every request finds its connection
//	        idle (parked, or moved): latency p50/p99/p999, the server's CPU, allocations and bytes
//	        a request, over -dur after -warm
//	wsload  -c WebSockets, each sending a message and waiting for its echo in a loop: messages a
//	        second and latency
//
// Connections are spread over source addresses 127.0.0.2 to 127.0.0.5.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
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
	mode   = flag.String("mode", "wake", "wsidle|wake|wsload")
	addr   = flag.String("addr", "127.0.0.1:19201", "")
	pid    = flag.Int("pid", 0, "server pid")
	stats  = flag.String("stats", "", "the server's $STATS")
	n1     = flag.Int("n1", 10000, "")
	n2     = flag.Int("n2", 20000, "")
	settle = flag.Duration("settle", 5*time.Second, "")
	conns  = flag.Int("c", 2000, "wake, wsload: connections")
	gap    = flag.Duration("gap", 300*time.Millisecond, "wake: time between two requests of a connection")
	warm   = flag.Duration("warm", 3*time.Second, "")
	dur    = flag.Duration("dur", 15*time.Second, "")
	useWS  = flag.Bool("ws", false, "wake: WebSocket messages instead of GETs")
	useTLS = flag.Bool("tls", false, "speak TLS (the certificate is not verified)")
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
	os.Remove(*stats)
	syscall.Kill(*pid, syscall.SIGUSR1)
	for i := 0; i < 200; i++ {
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

func rawConn(c net.Conn) *net.TCPConn {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	return c.(*net.TCPConn)
}

var getReq = []byte("GET /users/7 HTTP/1.1\r\nHost: x\r\n\r\n")

// roundtrip sends req and reads one whole 200 response with a Content-Length.
func roundtrip(c net.Conn, req, buf []byte) bool {
	if _, err := c.Write(req); err != nil {
		return false
	}
	n := 0
	for {
		m, err := c.Read(buf[n:])
		if err != nil {
			return false
		}
		n += m
		he := bytes.Index(buf[:n], []byte("\r\n\r\n"))
		if he < 0 {
			continue
		}
		if string(buf[9:12]) != "200" {
			fmt.Fprintf(os.Stderr, "unexpected response: %q\n", buf[:n])
			return false
		}
		i := bytes.Index(buf[:he], []byte("ength: "))
		if i < 0 {
			return false
		}
		j := i + 7
		k := j
		for buf[k] >= '0' && buf[k] <= '9' {
			k++
		}
		cl, _ := strconv.Atoi(string(buf[j:k]))
		if n >= he+4+cl {
			return true
		}
	}
}

var upgrade = []byte("GET /ws/7 HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")

func maskedMsg() []byte {
	mask := []byte{1, 2, 3, 4}
	p := []byte(`{"text":"abcdef"}`)
	out := []byte{0x81, 0x80 | byte(len(p))}
	out = append(out, mask...)
	for i, b := range p {
		out = append(out, b^mask[i&3])
	}
	return out
}

var wsMsg = maskedMsg()

// upgradeWS upgrades c; leftover bytes after the 101 are returned.
func upgradeWS(c net.Conn) ([]byte, bool) {
	if _, err := c.Write(upgrade); err != nil {
		return nil, false
	}
	buf := make([]byte, 1024)
	n, he := 0, -1
	for he < 0 {
		m, err := c.Read(buf[n:])
		if err != nil {
			return nil, false
		}
		n += m
		he = bytes.Index(buf[:n], []byte("\r\n\r\n"))
	}
	if !bytes.HasPrefix(buf, []byte("HTTP/1.1 101")) {
		fmt.Fprintln(os.Stderr, "bad upgrade:", string(buf[:n]))
		return nil, false
	}
	return append([]byte(nil), buf[he+4:n]...), true
}

// wsRoundtrip sends a message and reads one server frame (the echo; a ping is answered and skipped).
func wsRoundtrip(c net.Conn, have []byte, buf []byte) ([]byte, bool) {
	if _, err := c.Write(wsMsg); err != nil {
		return nil, false
	}
	for {
		for len(have) < 2 || len(have) < 2+int(have[1]&0x7f) {
			m, err := c.Read(buf)
			if err != nil {
				return nil, false
			}
			have = append(have, buf[:m]...)
		}
		n := 2 + int(have[1]&0x7f)
		op := have[0] & 0x0f
		have = have[n:]
		if op == 0x9 { // ping: pong it
			c.Write([]byte{0x8a, 0x80, 0, 0, 0, 0})
			continue
		}
		return have, op == 0x1
	}
}

func emit(m map[string]any) {
	b, _ := json.Marshal(m)
	fmt.Println(string(b))
}

func quantiles(all []int32) map[string]any {
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	q := func(p float64) int32 {
		if len(all) == 0 {
			return 0
		}
		return all[int(float64(len(all)-1)*p)]
	}
	return map[string]any{"p50_us": q(.5), "p90_us": q(.9), "p99_us": q(.99), "p999_us": q(.999)}
}

func runWSIdle() {
	out := map[string]any{"rss0": procStatus("VmRSS"), "fails": int64(0)}
	out["stats0"] = serverStats()
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
				have, ok := upgradeWS(c)
				if ok {
					_, ok = wsRoundtrip(c, have, make([]byte, 256))
				}
				if !ok {
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
		out["fails"] = out["fails"].(int64) + fails.Load()
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

// runWake is the scenario parking makes costlier: every request finds its connection idle.
func runWake() {
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
			var have []byte
			ok := err == nil
			if ok && *useWS {
				have, ok = upgradeWS(c)
			}
			ready.Done()
			if !ok {
				errs.Add(1)
				return
			}
			defer c.Close()
			rawConn(c).SetLinger(0)
			buf := make([]byte, 8192)
			ls := make([]int32, 0, 1024)
			r := rand.New(rand.NewPCG(uint64(i), 7))
			time.Sleep(time.Duration(r.Int64N(int64(*gap)))) // spread the first requests over a gap
			for !stop.Load() {
				t := time.Now()
				if *useWS {
					have, ok = wsRoundtrip(c, have, buf)
				} else {
					ok = roundtrip(c, getReq, buf)
				}
				if !ok {
					errs.Add(1)
					return
				}
				if measuring.Load() {
					ls = append(ls, int32(time.Since(t)/1000))
				}
				time.Sleep(*gap*9/10 + time.Duration(r.Int64N(int64(*gap/5)+1)))
			}
			lats[i] = ls
		}(i)
	}
	ready.Wait()
	time.Sleep(*warm)
	s0 := serverStats()
	c0 := cpuTicks()
	measuring.Store(true)
	t0 := time.Now()
	time.Sleep(*dur)
	measuring.Store(false)
	el := time.Since(t0)
	c1 := cpuTicks()
	s1 := serverStats()
	stop.Store(true)
	wg.Wait()
	var all []int32
	for _, l := range lats {
		all = append(all, l...)
	}
	n := float64(len(all))
	out := quantiles(all)
	out["reqs"], out["rps"], out["errors"] = len(all), n/el.Seconds(), errs.Load()
	if n > 0 {
		out["cpu_ns_per_req"] = float64(c1-c0) * 1e7 / n
		if s0 != nil && s1 != nil {
			// Two SIGUSR1 collections fall inside the window; their allocations are the stats
			// handler's own and are a few hundred in all, under 0.01 a request at these counts.
			out["allocs_per_req"] = (s1["mallocs"].(float64) - s0["mallocs"].(float64)) / n
			out["bytes_per_req"] = (s1["total_alloc"].(float64) - s0["total_alloc"].(float64)) / n
			out["goroutines_end"] = s1["goroutines"]
		}
	}
	out["vmhwm"] = procStatus("VmHWM")
	emit(out)
}

func runWSLoad() {
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
			var have []byte
			ok := err == nil
			if ok {
				have, ok = upgradeWS(c)
			}
			ready.Done()
			if !ok {
				errs.Add(1)
				return
			}
			defer c.Close()
			rawConn(c).SetLinger(0)
			buf := make([]byte, 8192)
			ls := make([]int32, 0, 1<<14)
			for !stop.Load() {
				t := time.Now()
				if have, ok = wsRoundtrip(c, have, buf); !ok {
					errs.Add(1)
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
	c0 := cpuTicks()
	measuring.Store(true)
	t0 := time.Now()
	time.Sleep(*dur)
	measuring.Store(false)
	el := time.Since(t0)
	c1 := cpuTicks()
	stop.Store(true)
	wg.Wait()
	var all []int32
	for _, l := range lats {
		all = append(all, l...)
	}
	out := quantiles(all)
	out["msgs"], out["mps"], out["errors"] = len(all), float64(len(all))/el.Seconds(), errs.Load()
	if len(all) > 0 {
		out["cpu_ns_per_msg"] = float64(c1-c0) * 1e7 / float64(len(all))
	}
	emit(out)
}

func main() {
	flag.Parse()
	switch *mode {
	case "wsidle":
		runWSIdle()
	case "wake":
		runWake()
	case "wsload":
		runWSLoad()
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", *mode)
		os.Exit(2)
	}
}
