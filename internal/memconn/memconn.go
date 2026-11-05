// Package memconn is an in-memory net.Conn pair and net.Listener (ADR 0027).
//
// The engine takes its listener as an interface, so a test can put the whole application
// behind this one and exercise the path a real connection takes, minus the kernel: deadlines,
// a half-close, a client that leaves mid-request, a remote address the test chooses.
//
// # Why not net.Pipe
//
// net.Pipe is synchronous: a Write blocks until the peer reads, so a server that writes its
// response before the test client reads would deadlock, and nothing in it models a half-close
// or a bounded socket buffer. Here each direction has a buffer of BufferSize bytes: a writer
// does not need a concurrent reader until it is full, and then blocks, as a socket does.
//
// # Deadlines
//
// Deadlines are measured on the Clock given to Listen, so a fake clock's Advance expires
// them. A deadline owns one timer, re-armed in place when the clock's timers allow it
// (clock.Resetter), and an expiry only sets a flag and wakes the waiter. That keeps a
// SetReadDeadline per request allocation-free, which the engine's allocation test depends on
// (ADR 0003).
//
// # Learning that the peer left
//
// PeerClosed returns a channel closed when the other end closes or half-closes. The engine
// uses it as the disconnect watch for these connections (ADR 0016), where a real socket
// would use epoll.
package memconn

import (
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/nevindra/bites/internal/clock"
)

// BufferSize is the bytes one direction holds before a writer blocks.
const BufferSize = 64 << 10

// Addr is the address of a memconn endpoint.
type Addr struct{ Name string }

func (a Addr) Network() string { return "memconn" }
func (a Addr) String() string  { return a.Name }

// ErrListenerClosed is returned by Accept and Dial on a closed Listener.
var ErrListenerClosed = net.ErrClosed

// Listener is a net.Listener whose connections are made by Dial.
type Listener struct {
	clk   clock.Clock
	queue chan *Conn
	done  chan struct{}
	once  sync.Once

	// The server ends dialled and not yet closed, for Settled (ADR 0027).
	mu      sync.Mutex
	servers map[*Conn]struct{}
	changed chan struct{} // cap 1: a server end settled
}

// Listen returns a listener whose connections measure deadlines on clk.
func Listen(clk clock.Clock) *Listener {
	if clk == nil {
		clk = clock.Real()
	}
	return &Listener{clk: clk, queue: make(chan *Conn, 1024), done: make(chan struct{}),
		servers: map[*Conn]struct{}{}, changed: make(chan struct{}, 1)}
}

// Dial returns the client end of a new connection and queues the server end for Accept.
// The server end's RemoteAddr is remote (a default if nil).
func (l *Listener) Dial(remote net.Addr) (net.Conn, error) {
	if remote == nil {
		remote = Addr{"client"}
	}
	select {
	case <-l.done:
		return nil, &net.OpError{Op: "dial", Net: "memconn", Err: net.ErrClosed}
	default:
	}
	server, client := newPair(l.clk, l.Addr(), remote)
	server.ln = l
	l.mu.Lock()
	l.servers[server] = struct{}{}
	l.mu.Unlock()
	select {
	case l.queue <- server:
		// Close may have run between the first check and the send, and drained the queue before
		// this end was in it: nobody would ever accept or close it, and the client would wait
		// for an answer forever. Seen closed now, the end is closed here instead.
		select {
		case <-l.done:
			server.Close()
			return nil, &net.OpError{Op: "dial", Net: "memconn", Err: net.ErrClosed}
		default:
		}
		return client, nil
	case <-l.done:
		server.Close()
		return nil, &net.OpError{Op: "dial", Net: "memconn", Err: net.ErrClosed}
	}
}

// Settled waits, in real time, until every server end this listener made is settled: closed, or
// blocked in a Read with nothing to read and its peer still there. A server end is neither
// while it holds a request it has not finished with, and the engine's connection reads again
// only after the Observers' End of the request it just answered (ADR 0051), so a listener whose
// server ends are all settled has seen every End of the requests it received whole. A request the
// peer has only partly sent is not waited for: its connection is blocked in Read, as an idle one
// is. It reports whether that happened within d.
func (l *Listener) Settled(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		if l.allSettled() {
			return true
		}
		select {
		case <-l.changed:
		case <-t.C:
			return l.allSettled()
		}
	}
}

func (l *Listener) allSettled() bool {
	l.mu.Lock()
	cs := make([]*Conn, 0, len(l.servers))
	for c := range l.servers {
		cs = append(cs, c)
	}
	l.mu.Unlock() // never held with a Conn's mutex: Close takes them the other way round
	for _, c := range cs {
		c.mu.Lock()
		ok := c.settledLocked()
		c.mu.Unlock()
		if !ok {
			return false
		}
	}
	return true
}

func (l *Listener) wake() { signal(l.changed) }

// Accept returns the server end of the next dialled connection.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.queue:
		return c, nil
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: "memconn", Err: net.ErrClosed}
	}
}

// Close stops the listener and closes connections nobody accepted.
func (l *Listener) Close() error {
	l.once.Do(func() {
		close(l.done)
		for {
			select {
			case c := <-l.queue:
				c.Close()
			default:
				return
			}
		}
	})
	return nil
}

// Closed reports whether Close has run: the server stopped accepting, as Shutdown does.
func (l *Listener) Closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Addr is the listener's address.
func (l *Listener) Addr() net.Addr { return Addr{"memconn"} }

// pipe is one direction of a connection. All fields are guarded by the pair's mutex.
type pipe struct {
	data     []byte
	rpos     int
	wclosed  bool          // the writer is done: the reader sees EOF after draining
	rclosed  bool          // the reader is gone: a write fails
	eof      chan struct{} // closed with wclosed
	readable chan struct{} // cap 1: a wake-up for the reader
	writable chan struct{} // cap 1: a wake-up for the writer
}

func newPipe() *pipe {
	return &pipe{eof: make(chan struct{}), readable: make(chan struct{}, 1), writable: make(chan struct{}, 1)}
}

func (p *pipe) avail() int { return len(p.data) - p.rpos }

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Conn is one end of an in-memory connection.
type Conn struct {
	mu     *sync.Mutex // shared by both ends
	clk    clock.Clock
	in     *pipe
	out    *pipe
	local  net.Addr
	remote net.Addr
	done   chan struct{} // closed by Close of this end
	closed bool
	rd, wd deadline

	ln      *Listener // set on a server end, which Settled watches
	waiting bool      // blocked in Read, for Settled
}

// settledLocked: closed, or blocked in Read with nothing to read, the peer still there and no
// deadline expired. Caller holds the mutex.
func (c *Conn) settledLocked() bool {
	return c.closed || (c.waiting && c.in.avail() == 0 && !c.in.wclosed && !c.rd.expired)
}

func newPair(clk clock.Clock, serverAddr, clientAddr net.Addr) (server, client *Conn) {
	mu := new(sync.Mutex)
	a, b := newPipe(), newPipe() // a: client to server, b: server to client
	server = &Conn{mu: mu, clk: clk, in: a, out: b, local: serverAddr, remote: clientAddr, done: make(chan struct{})}
	client = &Conn{mu: mu, clk: clk, in: b, out: a, local: clientAddr, remote: serverAddr, done: make(chan struct{})}
	for _, c := range []*Conn{server, client} {
		c.rd.init(c, func() { signal(c.in.readable) })
		c.wd.init(c, func() { signal(c.out.writable) })
	}
	return
}

// deadline is one direction's deadline: a time, an expired flag, and a re-armable timer.
type deadline struct {
	c       *Conn
	wake    func()
	t       time.Time
	expired bool
	timer   clock.Timer
	fire    func()
}

func (d *deadline) init(c *Conn, wake func()) {
	d.c, d.wake = c, wake
	d.fire = func() {
		c.mu.Lock()
		// The check against t discards a firing that raced with a Reset or a clear.
		if !d.t.IsZero() && !c.clk.Now().Before(d.t) {
			d.expired = true
			d.wake()
		}
		c.mu.Unlock()
	}
}

// set installs t (zero clears). Caller holds the mutex.
func (d *deadline) set(t time.Time) {
	d.t, d.expired = t, false
	if t.IsZero() {
		if d.timer != nil {
			d.timer.Stop()
		}
		return
	}
	dur := t.Sub(d.c.clk.Now())
	if dur <= 0 {
		if d.timer != nil {
			d.timer.Stop()
		}
		d.expired = true
		d.wake()
		return
	}
	switch {
	case d.timer == nil:
		d.timer = d.c.clk.AfterFunc(dur, d.fire)
	default:
		if r, ok := d.timer.(clock.Resetter); ok {
			r.Reset(dur)
		} else {
			d.timer.Stop()
			d.timer = d.c.clk.AfterFunc(dur, d.fire)
		}
	}
}

func (d *deadline) stop() {
	if d.timer != nil {
		d.timer.Stop()
	}
}

func (c *Conn) opErr(op string, err error) error {
	return &net.OpError{Op: op, Net: "memconn", Source: c.local, Addr: c.remote, Err: err}
}

// Read reads buffered bytes, or blocks until some arrive, the peer closes its write side
// (io.EOF once drained), the deadline passes or this end closes.
func (c *Conn) Read(b []byte) (int, error) {
	c.mu.Lock()
	for {
		switch {
		case c.closed:
			c.mu.Unlock()
			return 0, c.opErr("read", net.ErrClosed)
		case c.rd.expired:
			c.mu.Unlock()
			return 0, c.opErr("read", os.ErrDeadlineExceeded)
		case len(b) == 0:
			c.mu.Unlock()
			return 0, nil
		}
		p := c.in
		if n := p.avail(); n > 0 {
			n = copy(b, p.data[p.rpos:])
			p.rpos += n
			if p.avail() == 0 {
				p.data, p.rpos = p.data[:0], 0
			} else {
				signal(p.readable)
			}
			signal(p.writable)
			c.mu.Unlock()
			return n, nil
		}
		if p.wclosed {
			c.mu.Unlock()
			return 0, io.EOF
		}
		c.waiting = true
		c.mu.Unlock()
		if c.ln != nil {
			c.ln.wake()
		}
		select {
		case <-p.readable:
		case <-c.done:
		}
		c.mu.Lock()
		c.waiting = false
	}
}

// Write copies b into the peer's buffer, blocking while it is full.
func (c *Conn) Write(b []byte) (int, error) {
	total := 0
	c.mu.Lock()
	for {
		p := c.out
		switch {
		case c.closed:
			c.mu.Unlock()
			return total, c.opErr("write", net.ErrClosed)
		case p.wclosed:
			c.mu.Unlock()
			return total, c.opErr("write", io.ErrClosedPipe)
		case c.wd.expired:
			c.mu.Unlock()
			return total, c.opErr("write", os.ErrDeadlineExceeded)
		case p.rclosed:
			c.mu.Unlock()
			return total, c.opErr("write", io.ErrClosedPipe)
		}
		if len(b) == 0 {
			c.mu.Unlock()
			return total, nil
		}
		if room := BufferSize - p.avail(); room > 0 {
			n := min(room, len(b))
			p.data = append(p.data, b[:n]...)
			b = b[n:]
			total += n
			signal(p.readable)
			if len(b) == 0 {
				c.mu.Unlock()
				return total, nil
			}
		}
		c.mu.Unlock()
		select {
		case <-p.writable:
		case <-c.done:
		}
		c.mu.Lock()
	}
}

// CloseWrite is a half-close: the peer reads EOF once it has drained what is buffered.
func (c *Conn) CloseWrite() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeWriteLocked()
	return nil
}

func (c *Conn) closeWriteLocked() {
	if !c.out.wclosed {
		c.out.wclosed = true
		close(c.out.eof)
		signal(c.out.readable)
	}
}

// Close closes both directions. Operations blocked on this end return net.ErrClosed.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.opErr("close", net.ErrClosed)
	}
	c.closed = true
	close(c.done)
	c.closeWriteLocked()
	c.in.rclosed = true
	signal(c.in.writable)
	c.rd.stop()
	c.wd.stop()
	if c.ln != nil {
		c.ln.mu.Lock()
		delete(c.ln.servers, c)
		c.ln.mu.Unlock()
		c.ln.wake()
	}
	return nil
}

// PeerClosed is closed when the other end closes or half-closes, whether or not bytes it sent
// are still unread. It is how the engine learns a client left without reading (ADR 0016).
func (c *Conn) PeerClosed() <-chan struct{} { return c.in.eof }

// Unconsumed is the number of bytes this end has written that the peer has not read yet.
// Tests wait for it to reach zero to know the server has taken what they sent.
func (c *Conn) Unconsumed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.avail()
}

func (c *Conn) LocalAddr() net.Addr  { return c.local }
func (c *Conn) RemoteAddr() net.Addr { return c.remote }

func (c *Conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.opErr("set", net.ErrClosed)
	}
	c.rd.set(t)
	c.wd.set(t)
	return nil
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.opErr("set", net.ErrClosed)
	}
	c.rd.set(t)
	return nil
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.opErr("set", net.ErrClosed)
	}
	c.wd.set(t)
	return nil
}
