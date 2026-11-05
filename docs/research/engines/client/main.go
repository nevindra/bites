// Load/idle client. Modes: wait, idle, load.
package main

import (
	"bytes"
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
	mode  = flag.String("mode", "load", "wait|idle|load")
	addr  = flag.String("addr", "127.0.0.1:9001", "")
	path  = flag.String("path", "/", "")
	conns = flag.Int("c", 256, "")
	warm  = flag.Duration("warm", 3*time.Second, "")
	dur   = flag.Duration("dur", 10*time.Second, "")
	pid   = flag.Int("pid", 0, "server pid")
	stats = flag.String("stats", "", "stats file written by server on SIGUSR1")
	n1    = flag.Int("n1", 10000, "")
	n2    = flag.Int("n2", 20000, "")
	settle = flag.Duration("settle", 5*time.Second, "")
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

func serverStats() map[string]any {
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

func dial(i int) (net.Conn, error) {
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}, Timeout: 10 * time.Second,
		Control: func(network, address string, rc syscall.RawConn) error {
			return rc.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, 24, 1) // IP_BIND_ADDRESS_NO_PORT
			})
		}}
	return d.Dial("tcp", *addr)
}

// roundtrip sends req and reads one response, returns false on error.
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
			fmt.Fprintln(os.Stderr, "no content-length:", string(buf[:n]))
			return false
		}
		if n >= he+4+cl {
			return true
		}
	}
}

func request(p string) []byte { return []byte("GET " + p + " HTTP/1.1\r\nHost: x\r\n\r\n") }

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
	}
}

func runIdle() {
	out := map[string]any{"rss0": procStatus("VmRSS")}
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
				buf := make([]byte, 2048)
				if !roundtrip(c, request("/"), buf) {
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
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	for _, c := range all {
		c.(*net.TCPConn).SetLinger(0)
		c.Close()
	}
}

func runLoad() {
	req := request(*path)
	var stop atomic.Bool
	var measuring atomic.Bool
	lats := make([][]int32, *conns)
	counts := make([]int64, *conns)
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
			c.(*net.TCPConn).SetLinger(0)
			buf := make([]byte, 2048)
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
			counts[i] = int64(len(ls))
		}(i)
	}
	ready.Wait()
	time.Sleep(*warm)
	var peakRSS int64
	measuring.Store(true)
	t0 := time.Now()
	end := t0.Add(*dur)
	for time.Now().Before(end) {
		time.Sleep(100 * time.Millisecond)
		if r := procStatus("VmRSS"); r > peakRSS {
			peakRSS = r
		}
	}
	measuring.Store(false)
	el := time.Since(t0)
	stop.Store(true)
	hwm := procStatus("VmHWM")
	var st map[string]any
	if *stats != "" {
		st = serverStats()
	}
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
	out := map[string]any{"reqs": len(all), "rps": float64(len(all)) / el.Seconds(), "p50_us": q(.5), "p99_us": q(.99), "p999_us": q(.999),
		"errors": errs.Load(), "peak_rss_sampled": peakRSS, "vmhwm": hwm, "stats": st}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}
