// Probe: what does a connection hold while it declares a large Content-Length and sends nothing
// (docs/research/body-reserve/report.md)? It starts the server in this process, opens -n
// connections that each send a head declaring -cl bytes of body and then nothing, and reports
// the live heap and the resident set per connection, for a route that reads its body and one
// that does not. Run it from a copy of the tree to compare two commits: the replace directive
// in go.mod points two levels up from docs/research/.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	nilogo "github.com/nevindra/bites"
)

type readIn struct {
	Body struct{ Name string }
}

type noReadIn struct {
	Query struct{ Q nilogo.Opt[string] }
}

type out struct{ OK bool }

func rss() uint64 {
	b, _ := os.ReadFile("/proc/self/statm")
	f := strings.Fields(string(b))
	pages, _ := strconv.ParseUint(f[1], 10, 64)
	return pages * uint64(os.Getpagesize())
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func main() {
	n := flag.Int("n", 1000, "connections")
	cl := flag.Int("cl", 1<<20, "declared Content-Length")
	route := flag.String("route", "read", "read: a route with a JSON body; noread: a route without one")
	flag.Parse()

	app := nilogo.New(nilogo.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Post("/read", func(context.Context, readIn) (out, error) { return out{true}, nil })
	app.Post("/noread", func(context.Context, noReadIn) (out, error) { return out{true}, nil })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go app.Serve(ln)
	time.Sleep(200 * time.Millisecond)

	// Warm: one full request, so the pools and the router are in place before the baseline.
	w, _ := net.Dial("tcp", ln.Addr().String())
	fmt.Fprintf(w, "POST /%s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 15\r\n\r\n{\"Name\":\"ada\"} ", *route)
	buf := make([]byte, 4096)
	w.Read(buf)
	w.Close()
	time.Sleep(200 * time.Millisecond)

	h0, r0 := heap(), rss()
	var conns []net.Conn
	for i := 0; i < *n; i++ {
		// Spread over 127.0.0.2 to 127.0.0.5 so loopback does not run out of ports.
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, byte(2+i%4))}}
		c, err := d.Dial("tcp", ln.Addr().String())
		if err != nil {
			panic(err)
		}
		fmt.Fprintf(c, "POST /%s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", *route, *cl)
		conns = append(conns, c)
	}
	time.Sleep(2 * time.Second)
	h1, r1 := heap(), rss()
	fmt.Printf("route=%s n=%d cl=%d heap_per_conn=%d rss_per_conn=%d\n", *route, *n, *cl, (h1-h0)/uint64(*n), (r1-r0)/uint64(*n))
	for _, c := range conns {
		c.Close()
	}
	app.Close()
}
