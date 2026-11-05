// WebSocket idle client. Modes: wait, idle (10k then 20k, snapshots), bcast (20k, then SIGUSR2 push for 10s, optional -ack).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	mode   = flag.String("mode", "idle", "wait|idle|bcast")
	addr   = flag.String("addr", "127.0.0.1:19001", "")
	pid    = flag.Int("pid", 0, "server pid")
	stats  = flag.String("stats", "", "stats file written by server on SIGUSR1")
	n1     = flag.Int("n1", 10000, "")
	n2     = flag.Int("n2", 20000, "")
	settle = flag.Duration("settle", 5*time.Second, "")
	ack    = flag.Bool("ack", false, "bcast: reply to every push with a 6-byte message")
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

// gcStats forces two GCs in the server (SIGHUP) and returns its stats: stacks shrink, scan_stack is current.
func gcStats() map[string]any {
	os.Remove(*stats + ".gc")
	syscall.Kill(*pid, syscall.SIGHUP)
	for i := 0; i < 200; i++ {
		time.Sleep(50 * time.Millisecond)
		if b, err := os.ReadFile(*stats + ".gc"); err == nil {
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

var upgrade = []byte("GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")

func maskedMsg() []byte {
	mask := []byte{1, 2, 3, 4}
	p := []byte("abcdef")
	out := []byte{0x81, 0x80 | 6}
	out = append(out, mask...)
	for i, b := range p {
		out = append(out, b^mask[i&3])
	}
	return out
}

// open upgrades, sends one 6-byte text message, reads the 8-byte echo. Returns true on success.
func open(c net.Conn) bool {
	if _, err := c.Write(upgrade); err != nil {
		return false
	}
	buf := make([]byte, 1024)
	n := 0
	var he int
	for {
		m, err := c.Read(buf[n:])
		if err != nil {
			return false
		}
		n += m
		if he = bytes.Index(buf[:n], []byte("\r\n\r\n")); he >= 0 {
			break
		}
	}
	if !bytes.HasPrefix(buf, []byte("HTTP/1.1 101")) {
		fmt.Fprintln(os.Stderr, "bad upgrade:", string(buf[:n]))
		return false
	}
	if _, err := c.Write(maskedMsg()); err != nil {
		return false
	}
	have := n - (he + 4)
	if have < 8 {
		if _, err := io.ReadFull(c, buf[:8-have]); err != nil {
			return false
		}
	}
	return true
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
	default:
		run()
	}
}

func run() {
	out := map[string]any{"rss0": procStatus("VmRSS")}
	out["stats0"] = serverStats()
	var all []net.Conn
	var mu sync.Mutex
	openN := func(from, to int) {
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
				if !open(c) {
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
			out["fails"] = out["fails"].(int64) + f
		}
	}
	out["fails"] = int64(0)
	snap := func(tag string) {
		time.Sleep(*settle)
		out["rss_"+tag] = procStatus("VmRSS")
		out["stats_"+tag] = serverStats()
		out["conns_"+tag] = len(all)
	}
	if *mode == "idle" {
		openN(0, *n1)
		snap("n1")
		openN(*n1, *n2)
		snap("n2")
		out["rss_gc_before"] = procStatus("VmRSS")
		out["stats_gc"] = gcStats()
		time.Sleep(time.Second)
		out["rss_gc"] = procStatus("VmRSS")
	} else {
		openN(0, *n2)
		snap("n2")
		var got atomic.Int64
		var wg sync.WaitGroup
		for _, c := range all {
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				var b [8]byte
				msg := maskedMsg()
				for {
					if _, err := io.ReadFull(c, b[:]); err != nil {
						return
					}
					got.Add(1)
					if *ack && string(b[2:8]) == "push!!" {
						if _, err := c.Write(msg); err != nil {
							return
						}
					}
				}
			}(c)
		}
		syscall.Kill(*pid, syscall.SIGUSR2)
		var peak int64
		t0 := time.Now()
		var series []int64
		for time.Since(t0) < 13*time.Second {
			time.Sleep(250 * time.Millisecond)
			r := procStatus("VmRSS")
			series = append(series, r)
			if r > peak {
				peak = r
			}
		}
		out["bcast_peak_rss_sampled"] = peak
		out["bcast_vmhwm"] = procStatus("VmHWM")
		out["bcast_rss_end"] = procStatus("VmRSS")
		out["bcast_received"] = got.Load()
		out["stats_end"] = serverStats()
		out["stats_end_gc"] = gcStats()
		out["rss_end_gc"] = procStatus("VmRSS")
		_ = series
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	for _, c := range all {
		c.(*net.TCPConn).SetLinger(0)
		c.Close()
	}
}
