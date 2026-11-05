// The probe behind ADR 0005's hand-off of an idle connection to a fresh goroutine: a nilo-go app
// serving a typed route, and a keep-alive load client that reports throughput, latency and the
// server's CPU per request. Built twice, against the tree with the hand-off and without it.
package main

import (
	"bytes"
	"context"
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
	"time"

	nilogo "github.com/nevindra/bites"
)

var (
	mode  = flag.String("mode", "server", "server|load")
	addr  = flag.String("addr", "127.0.0.1:19101", "")
	path  = flag.String("path", "/ping/7", "")
	conns = flag.Int("c", 256, "")
	warm  = flag.Duration("warm", 2*time.Second, "")
	dur   = flag.Duration("dur", 8*time.Second, "")
	pid   = flag.Int("pid", 0, "server pid, for its CPU time")
)

type Ping struct {
	Path  struct{ ID int64 }
	Query struct{ Q nilogo.Opt[string] }
}

type Pong struct {
	ID int64 `json:"id"`
}

// deep stands in for a handler that calls a driver or fmt: it uses about 6 KiB of stack, so the
// stack grows to 8 KiB on every request once the wait is on a fresh goroutine.
//
//go:noinline
func deep(n int) int64 {
	var pad [256]byte
	pad[n%256] = byte(n)
	if n == 0 {
		return int64(pad[0])
	}
	return deep(n-1) + int64(pad[n%256])
}

func server() {
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	app := nilogo.New()
	app.Get("/ping/{id}", func(_ context.Context, in Ping) (Pong, error) { return Pong{ID: in.Path.ID}, nil })
	app.Get("/deep/{id}", func(_ context.Context, in Ping) (Pong, error) { return Pong{ID: in.Path.ID + deep(20)}, nil })
	app.Serve(ln)
}

func cpuTicks() int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", *pid))
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
	u, _ := strconv.ParseInt(f[11], 10, 64)
	s, _ := strconv.ParseInt(f[12], 10, 64)
	return u + s // clock ticks of 10 ms
}

func dial(i int) (net.Conn, error) {
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}, Timeout: 10 * time.Second}
	return d.Dial("tcp", *addr)
}

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
		i := bytes.Index(buf[:he], []byte("Content-Length: "))
		if i < 0 {
			return false
		}
		j := i + len("Content-Length: ")
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

func load() {
	req := []byte("GET " + *path + " HTTP/1.1\r\nHost: x\r\n\r\n")
	var stop, measuring atomic.Bool
	lats := make([][]int32, *conns)
	var errs atomic.Int64
	var wg sync.WaitGroup
	for i := range *conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := dial(i)
			if err != nil {
				errs.Add(1)
				return
			}
			defer c.Close()
			buf := make([]byte, 2048)
			ls := make([]int32, 0, 1<<16)
			for !stop.Load() {
				t := time.Now()
				if !roundtrip(c, req, buf) {
					errs.Add(1)
					return
				}
				if measuring.Load() {
					ls = append(ls, int32(time.Since(t)/time.Microsecond))
				}
			}
			lats[i] = ls
		}()
	}
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
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	q := func(p float64) int32 { return all[int(float64(len(all)-1)*p)] }
	out := map[string]any{"rps": int(float64(len(all)) / el.Seconds()), "p50_us": q(.5), "p99_us": q(.99), "p999_us": q(.999),
		"cpu_ns_per_req": int(float64(c1-c0) * 1e7 / float64(len(all))), "errors": errs.Load()}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
}

func main() {
	flag.Parse()
	if *mode == "load" {
		load()
		return
	}
	server()
}
