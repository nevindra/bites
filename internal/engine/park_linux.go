//go:build linux

package engine

import (
	"errors"
	"sync"
	"syscall"
)

// parkEvents is what a parked connection is woken by: bytes, the peer's half-close, and (always
// reported) a hangup or an error. One-shot: one event, then silence until the next park re-arms.
const parkEvents = syscall.EPOLLIN | syscall.EPOLLRDHUP | syscall.EPOLLONESHOT

const pollWake = -1 // the Fd of the event of the poller's own wake pipe

var errPollerClosed = errors.New("bites: the poller is closed")

// poller is the engine's parking epoll (park.go): one instance and one goroutine blocked in
// epoll_wait, made on the first park. It is beside the disconnect watcher of ADR 0016 and not
// inside it, because the watcher's registrations are slots of about 150 bytes each, while a
// parked connection costs the poller one pointer in a table indexed by descriptor.
type poller struct {
	epfd         int
	wakeR, wakeW int

	mu     sync.Mutex
	conns  []*conn // by descriptor: the connection parked on it, or the last one that was
	closed bool
	done   chan struct{}
}

// pollerState is the part of a parkState that only Linux has.
type pollerState struct {
	p     *poller
	ctl   func(fd uintptr) // ps.control, made once
	fd    int              // the socket's descriptor, known from the first park
	gen   uint32           // which park an event belongs to; written under p.mu
	added bool             // the descriptor is in the poller's epoll; later parks re-arm it
	del   bool             // control removes the descriptor instead of arming it
	err   error
}

func (ps *parkState) init() { ps.ctl = ps.control }

// getPoller makes the poller on the first park; nil means parking is not available, and the
// connection moves its wait instead (ADR 0005).
func (s *Server) getPoller() *poller {
	s.pollOnce.Do(func() {
		if p, err := newPoller(); err == nil {
			s.poll.Store(p)
		}
	})
	return s.poll.Load()
}

func newPoller() (*poller, error) {
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	var fds [2]int
	if err := syscall.Pipe2(fds[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		syscall.Close(epfd)
		return nil, err
	}
	ev := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: pollWake}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, fds[0], &ev); err != nil {
		syscall.Close(epfd)
		syscall.Close(fds[0])
		syscall.Close(fds[1])
		return nil, err
	}
	p := &poller{epfd: epfd, wakeR: fds[0], wakeW: fds[1], conns: make([]*conn, 1024), done: make(chan struct{})}
	go p.loop()
	return p, nil
}

// arm registers the connection's socket for one event, or re-arms it, and records the connection
// under its descriptor. Both happen inside rc.Control, where the descriptor is certain to be this
// connection's: never a number the kernel has given to another connection since this one's
// socket was closed by another goroutine (Shutdown, a WebSocket's close deadline).
func (p *poller) arm(ps *parkState) error {
	ps.p, ps.del, ps.err = p, false, nil
	if err := ps.rc.Control(ps.ctl); err != nil {
		return err // the socket is closed
	}
	return ps.err
}

// drop removes the connection's socket from the epoll for good, while it is still open, and forgets
// the connection, so the table does not keep an ended connection alive.
func (p *poller) drop(ps *parkState) {
	if !ps.added {
		return
	}
	ps.p, ps.del = p, true
	ps.rc.Control(ps.ctl) // a closed socket has left the epoll already
	p.mu.Lock()
	if ps.fd < len(p.conns) && p.conns[ps.fd] == ps.c {
		p.conns[ps.fd] = nil
	}
	p.mu.Unlock()
	ps.added = false
}

// control runs inside rc.Control: fd is the connection's own open descriptor.
func (ps *parkState) control(fd uintptr) {
	p := ps.p
	n := int(fd)
	if ps.del {
		syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_DEL, n, nil)
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		ps.err = errPollerClosed
		return
	}
	if n >= len(p.conns) {
		t := make([]*conn, max(2*len(p.conns), n+1))
		copy(t, p.conns)
		p.conns = t
	}
	p.conns[n] = ps.c
	ps.gen++
	ev := syscall.EpollEvent{Events: parkEvents, Fd: int32(n), Pad: int32(ps.gen)}
	p.mu.Unlock()
	ps.fd = n
	op := syscall.EPOLL_CTL_MOD
	if !ps.added {
		op = syscall.EPOLL_CTL_ADD
	}
	err := syscall.EpollCtl(p.epfd, op, n, &ev)
	switch {
	case err == syscall.ENOENT && op == syscall.EPOLL_CTL_MOD:
		err = syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_ADD, n, &ev)
	case err == syscall.EEXIST && op == syscall.EPOLL_CTL_ADD:
		err = syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_MOD, n, &ev)
	}
	ps.added = err == nil
	ps.err = err
}

func (p *poller) loop() {
	defer close(p.done)
	var evs [128]syscall.EpollEvent
	ready := make([]*conn, 0, len(evs))
	for {
		n, err := syscall.EpollWait(p.epfd, evs[:], -1)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		for i := range n {
			fd := int(evs[i].Fd)
			if fd < 0 || fd >= len(p.conns) {
				continue
			}
			// An event of an earlier park (queued before that park was woken some other way, and
			// the connection parked again since) carries an older generation and is dropped.
			if c := p.conns[fd]; c != nil && c.ps.gen == uint32(evs[i].Pad) {
				ready = append(ready, c)
			}
		}
		p.mu.Unlock()
		for _, c := range ready {
			c.wake()
		}
		clear(ready)
		ready = ready[:0]
	}
}

// close stops the poller. The engine closes it only when no connection is left, so none is
// stranded parked.
func (p *poller) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	syscall.Write(p.wakeW, []byte{1})
	<-p.done
	syscall.Close(p.epfd)
	syscall.Close(p.wakeR)
	syscall.Close(p.wakeW)
}
