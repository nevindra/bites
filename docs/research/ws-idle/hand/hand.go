// Package hand: minimal goroutine-per-connection WebSocket server. GET /ws upgrade, text/binary echo,
// masking, no fragmentation, no compression. The idle goroutine blocks on a 2-byte header read and holds
// no payload buffer; a 4 KiB buffer is taken from a sync.Pool only while a frame's payload is read.
package hand

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const bufSize = 4096 + 16

var pool = sync.Pool{New: func() any { poolNew.Add(1); b := make([]byte, bufSize); return &b }}

// Pool counters (atomic, cheap): the broadcast experiment reads them.
var poolGets, poolPuts, poolNew atomic.Int64

func PoolStats() (gets, puts, news int64) { return poolGets.Load(), poolPuts.Load(), poolNew.Load() }

func get() *[]byte  { poolGets.Add(1); return pool.Get().(*[]byte) }
func put(b *[]byte) { poolPuts.Add(1); pool.Put(b) }

// Registry for broadcast (only when Push is true; adds a map entry per connection).
var Push bool
var regMu sync.Mutex
var reg = map[net.Conn]struct{}{}

func Conns() []net.Conn {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]net.Conn, 0, len(reg))
	for c := range reg {
		out = append(out, c)
	}
	return out
}

// Serve accepts connections. variant "b" uses net.Conn reads; "b2" uses the raw fd read.
func Serve(ln net.Listener, variant string) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		if variant == "b2" {
			go serveRaw(c)
		} else if variant == "park" {
			go servePark(c)
		} else {
			go serve(c)
		}
	}
}

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// handshake reads the HTTP request with a pooled buffer and answers 101. Returns false on failure.
func handshake(c net.Conn) bool {
	bp := get()
	defer put(bp)
	buf := *bp
	n := 0
	for {
		m, err := c.Read(buf[n:])
		if err != nil {
			return false
		}
		n += m
		if bytes.Contains(buf[:n], []byte("\r\n\r\n")) {
			break
		}
		if n == len(buf) {
			return false
		}
	}
	i := bytes.Index(buf[:n], []byte("Sec-WebSocket-Key:"))
	if i < 0 {
		return false
	}
	i += len("Sec-WebSocket-Key:")
	for buf[i] == ' ' {
		i++
	}
	j := bytes.IndexByte(buf[i:n], '\r')
	h := sha1.New()
	h.Write(buf[i : i+j])
	h.Write([]byte(guid))
	var sum [20]byte
	var acc [28]byte
	base64.StdEncoding.Encode(acc[:], h.Sum(sum[:0]))
	out := append(buf[:0], "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: "...)
	out = append(out, acc[:]...)
	out = append(out, "\r\n\r\n"...)
	_, err := c.Write(out)
	return err == nil
}

// reader abstracts how the idle read blocks.
type reader func(p []byte) error

func serve(c net.Conn) {
	defer c.Close()
	if !handshake(c) {
		return
	}
	if Push {
		regMu.Lock()
		reg[c] = struct{}{}
		regMu.Unlock()
		defer func() { regMu.Lock(); delete(reg, c); regMu.Unlock() }()
	}
	frames(c, func(p []byte) error { _, err := io.ReadFull(c, p); return err })
}

var never chan struct{}

// servePark is an ablation: handshake and echo the first frame like serve, then park the goroutine on a
// channel instead of the socket. It measures goroutine + net objects with no read in flight.
func servePark(c net.Conn) {
	defer c.Close()
	if !handshake(c) {
		return
	}
	var hdr [2]byte
	rd := func(p []byte) error { _, err := io.ReadFull(c, p); return err }
	if rd(hdr[:]) != nil {
		return
	}
	var ext [4]byte
	if rd(ext[:]) != nil {
		return
	}
	ln := int(hdr[1] & 0x7f)
	bp := get()
	p := (*bp)[10 : 10+ln]
	if rd(p) != nil {
		put(bp)
		return
	}
	for i := range p {
		p[i] ^= ext[i&3]
	}
	buf := *bp
	buf[8], buf[9] = 0x81, byte(ln)
	c.Write(buf[8 : 10+ln])
	put(bp)
	<-never
}

// rawConn reads straight from the fd through syscall.RawConn: fewer frames between the loop and the park.
type rawConn struct {
	rc syscall.RawConn
	p  []byte
	n  int
	e  error
}

func (r *rawConn) cb(fd uintptr) bool {
	n, err := syscall.Read(int(fd), r.p)
	if err == syscall.EAGAIN {
		return false
	}
	r.n, r.e = n, err
	return true
}

func (r *rawConn) readFull(p []byte) error {
	for len(p) > 0 {
		r.p = p
		if err := r.rc.Read(r.cb); err != nil {
			return err
		}
		if r.e != nil {
			return r.e
		}
		if r.n == 0 {
			return io.EOF
		}
		p = p[r.n:]
	}
	return nil
}

func serveRaw(c net.Conn) {
	defer c.Close()
	if !handshake(c) {
		return
	}
	rc, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		return
	}
	r := &rawConn{rc: rc}
	if Push {
		regMu.Lock()
		reg[c] = struct{}{}
		regMu.Unlock()
		defer func() { regMu.Lock(); delete(reg, c); regMu.Unlock() }()
	}
	frames(c, r.readFull)
}

// frames is the read loop. Idle, it blocks in rd(hdr[:2]) holding nothing from the pool.
func frames(c net.Conn, rd reader) {
	var hdr [2]byte
	for {
		if rd(hdr[:]) != nil {
			return
		}
		op := hdr[0] & 0x0f
		masked := hdr[1]&0x80 != 0
		ln := uint64(hdr[1] & 0x7f)
		var ext [12]byte
		need := 0
		if ln == 126 {
			need = 2
		} else if ln == 127 {
			need = 8
		}
		if masked {
			need += 4
		}
		if need > 0 {
			if rd(ext[:need]) != nil {
				return
			}
		}
		k := 0
		if ln == 126 {
			ln = uint64(ext[0])<<8 | uint64(ext[1])
			k = 2
		} else if ln == 127 {
			ln = 0
			for i := 0; i < 8; i++ {
				ln = ln<<8 | uint64(ext[i])
			}
			k = 8
		}
		var mask [4]byte
		if masked {
			copy(mask[:], ext[k:k+4])
		}
		if hdr[0]&0x80 == 0 || ln > 4096 {
			return // no fragmentation, small frames only
		}
		if !payload(c, rd, op, masked, mask, int(ln)) {
			return
		}
	}
}

// payload takes a pooled buffer, reads the payload, answers, and returns the buffer.
func payload(c net.Conn, rd reader, op byte, masked bool, mask [4]byte, ln int) bool {
	bp := get()
	defer put(bp)
	buf := *bp
	const off = 10
	p := buf[off : off+ln]
	if ln > 0 && rd(p) != nil {
		return false
	}
	if masked {
		for i := range p {
			p[i] ^= mask[i&3]
		}
	}
	switch op {
	case 8:
		return false
	case 9:
		op = 10 // ping -> pong
	case 1, 2, 10:
	default:
		return false
	}
	if op == 10 && ln == 0 && false {
		return true
	}
	// header written right before the payload, one Write.
	var start int
	if ln < 126 {
		start = off - 2
		buf[start] = 0x80 | op
		buf[start+1] = byte(ln)
	} else {
		start = off - 4
		buf[start] = 0x80 | op
		buf[start+1] = 126
		buf[start+2] = byte(ln >> 8)
		buf[start+3] = byte(ln)
	}
	_, err := c.Write(buf[start : off+ln])
	return err == nil
}

// Frame builds an unmasked server text frame for ln<126.
func Frame(msg []byte) []byte {
	return append([]byte{0x81, byte(len(msg))}, msg...)
}

var _ = time.Second
