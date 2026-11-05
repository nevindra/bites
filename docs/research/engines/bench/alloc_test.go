package bench

import (
	"bytes"
	"engines/hand"
	"net"
	"net/http"
	"testing"

	"github.com/valyala/fasthttp"
)

// Client side is zero-alloc (preallocated buffers, TCPConn Read/Write), so process-wide
// Mallocs deltas per op are the server's allocations per request.
func drive(b *testing.B, ln net.Listener) {
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	req := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := make([]byte, 2048)
	one := func() {
		c.Write(req)
		n := 0
		for {
			m, err := c.Read(buf[n:])
			if err != nil {
				b.Fatal(err)
			}
			n += m
			if bytes.HasSuffix(buf[:n], []byte("hello")) {
				return
			}
		}
	}
	for i := 0; i < 2000; i++ {
		one()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		one()
	}
}

func BenchmarkStdHTTP(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	go http.Serve(ln, mux)
	defer ln.Close()
	drive(b, ln)
}
func BenchmarkFastHTTP(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go fasthttp.Serve(ln, func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("hello") })
	defer ln.Close()
	drive(b, ln)
}
func BenchmarkHandA(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go hand.Serve(ln, "a")
	defer ln.Close()
	drive(b, ln)
}
func BenchmarkHandB(b *testing.B) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go hand.Serve(ln, "b")
	defer ln.Close()
	drive(b, ln)
}
