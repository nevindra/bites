// The client of the WebSocket idle-memory measurement: opens 10k and then 20k WebSockets (each
// upgrades, optionally sends one message and reads the reply), spread over source addresses
// 127.0.0.2 to 127.0.0.5, and snapshots the server's RSS and runtime stats after each step.
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
	mode   = flag.String("mode", "idle", "wait|idle")
	addr   = flag.String("addr", "127.0.0.1:19101", "")
	pid    = flag.Int("pid", 0, "server pid")
	stats  = flag.String("stats", "", "stats file the server writes on SIGUSR1")
	n1     = flag.Int("n1", 10000, "")
	n2     = flag.Int("n2", 20000, "")
	settle = flag.Duration("settle", 5*time.Second, "")
	msg    = flag.Bool("msg", true, "send one message per connection and read the reply")
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

func serverStats(sig syscall.Signal, file string) map[string]any {
	os.Remove(file)
	syscall.Kill(*pid, sig)
	for i := 0; i < 200; i++ {
		time.Sleep(50 * time.Millisecond)
		if b, err := os.ReadFile(file); err == nil {
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

func open(c net.Conn) bool {
	if _, err := c.Write(upgrade); err != nil {
		return false
	}
	buf := make([]byte, 1024)
	n := 0
	he := -1
	for he < 0 {
		m, err := c.Read(buf[n:])
		if err != nil {
			return false
		}
		n += m
		he = bytes.Index(buf[:n], []byte("\r\n\r\n"))
	}
	if !bytes.HasPrefix(buf, []byte("HTTP/1.1 101")) {
		fmt.Fprintln(os.Stderr, "bad upgrade:", string(buf[:n]))
		return false
	}
	if !*msg {
		return true
	}
	if _, err := c.Write(maskedMsg()); err != nil {
		return false
	}
	have := buf[he+4 : n]
	for len(have) < 2 || len(have) < 2+int(have[1]&0x7f) {
		var b [64]byte
		m, err := c.Read(b[:])
		if err != nil {
			return false
		}
		have = append(have, b[:m]...)
	}
	return true
}

func main() {
	flag.Parse()
	if *mode == "wait" {
		for i := 0; i < 200; i++ {
			if c, err := net.Dial("tcp", *addr); err == nil {
				c.Close()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		os.Exit(1)
	}
	out := map[string]any{"rss0": procStatus("VmRSS"), "fails": int64(0)}
	out["stats0"] = serverStats(syscall.SIGUSR1, *stats)
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
		out["fails"] = out["fails"].(int64) + fails.Load()
	}
	snap := func(tag string) {
		time.Sleep(*settle)
		out["rss_"+tag] = procStatus("VmRSS")
		out["stats_"+tag] = serverStats(syscall.SIGUSR1, *stats)
		out["conns_"+tag] = len(all)
	}
	openN(0, *n1)
	snap("n1")
	openN(*n1, *n2)
	snap("n2")
	out["stats_gc"] = serverStats(syscall.SIGHUP, *stats+".gc")
	time.Sleep(time.Second)
	out["rss_gc"] = procStatus("VmRSS")
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	for _, c := range all {
		c.(*net.TCPConn).SetLinger(0)
		c.Close()
	}
	_ = io.Discard
}
