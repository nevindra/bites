// Package hand: minimal goroutine-per-connection HTTP/1.1 server (GET, no body, no pipelining).
package hand

import (
	"bytes"
	"net"
	"sync"
	"time"
)

var resp = []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 5\r\n\r\nhello")
var pool = sync.Pool{New: func() any { b := make([]byte, 4096); return &b }}

func Serve(ln net.Listener, variant string) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		if variant == "b" {
			go connB(c)
		} else {
			go connA(c)
		}
	}
}

func answer(c net.Conn, head []byte) bool {
	if bytes.HasPrefix(head, []byte("GET /block ")) {
		time.Sleep(10 * time.Millisecond)
	}
	_, err := c.Write(resp)
	return err == nil
}

var crlf2 = []byte("\r\n\r\n")

// (a) 4 KiB buffer allocated per connection and held for its life.
func connA(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 4096)
	for {
		n := 0
		for {
			m, err := c.Read(buf[n:])
			if err != nil {
				return
			}
			n += m
			if bytes.Contains(buf[:n], crlf2) {
				break
			}
			if n == len(buf) {
				return
			}
		}
		if !answer(c, buf[:n]) {
			return
		}
	}
}

// (b) block on a 1-byte read into a per-conn byte, take the 4 KiB buffer from the pool only
// while a request is in flight, return it before blocking again.
func connB(c net.Conn) {
	defer c.Close()
	one := make([]byte, 1)
	for {
		if _, err := c.Read(one); err != nil {
			return
		}
		bp := pool.Get().(*[]byte)
		buf := *bp
		buf[0] = one[0]
		n := 1
		ok := true
		for !bytes.Contains(buf[:n], crlf2) {
			if n == len(buf) {
				ok = false
				break
			}
			m, err := c.Read(buf[n:])
			if err != nil {
				ok = false
				break
			}
			n += m
		}
		if ok {
			ok = answer(c, buf[:n])
		}
		pool.Put(bp)
		if !ok {
			return
		}
	}
}
