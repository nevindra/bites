//go:build linux

package reqctx

import (
	"errors"
	"sync"
	"syscall"
)

const (
	epollRDHUP  = 0x2000 // EPOLLRDHUP, absent from package syscall
	hangupMask  = epollRDHUP | syscall.EPOLLHUP | syscall.EPOLLERR
	wakeSlot    = -1 // Fd value of the event for the wake pipe
	initialSlot = 256
)

// Watcher owns one epoll instance and one goroutine blocked in epoll_wait; it reports read-side
// hangups (EPOLLRDHUP, EPOLLHUP, EPOLLERR) on watched descriptors without ever reading them, so
// the bytes of a pipelined next request stay in the socket (ADR 0016).
//
// Descriptors are registered for EPOLLRDHUP only, never EPOLLIN, so arriving data does not wake
// the goroutine. The epoll event carries a slot index and a generation, so an event that was
// already queued when its connection was unwatched (and the slot reused) is recognised as stale.
type Watcher struct {
	epfd  int
	wakeR int
	wakeW int

	mu     sync.Mutex
	slots  []*slot
	free   []int
	closed bool

	wg sync.WaitGroup
}

// slot is one registration. ctl is built once with the slot so rc.Control costs no allocation.
type slot struct {
	w        *Watcher
	idx      int
	gen      uint32
	active   bool
	fd       int
	ev       syscall.EpollEvent
	err      error
	onHangup func()
	ctl      func(fd uintptr)
}

// NewWatcher creates the epoll instance and starts its goroutine.
func NewWatcher() (*Watcher, error) {
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return nil, err
	}
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_NONBLOCK|syscall.O_CLOEXEC); err != nil {
		syscall.Close(epfd)
		return nil, err
	}
	w := &Watcher{epfd: epfd, wakeR: p[0], wakeW: p[1]}
	ev := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: wakeSlot}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, w.wakeR, &ev); err != nil {
		syscall.Close(epfd)
		syscall.Close(p[0])
		syscall.Close(p[1])
		return nil, err
	}
	w.slots = make([]*slot, 0, initialSlot)
	w.free = make([]int, 0, initialSlot)
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Watch registers the descriptor behind rc and calls onHangup at most once, from the watcher
// goroutine, if the peer hangs up while registered. onHangup runs under the watcher's lock so that
// it cannot run after Unwatch returns; it must not call Watch, Unwatch or Close. The returned id
// goes to Unwatch, which must be called before the descriptor is closed.
func (w *Watcher) Watch(rc syscall.RawConn, onHangup func()) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("reqctx: watcher closed")
	}
	var s *slot
	if n := len(w.free); n > 0 {
		s = w.slots[w.free[n-1]]
		w.free = w.free[:n-1]
	} else {
		s = &slot{w: w, idx: len(w.slots)}
		s.ctl = func(fd uintptr) {
			s.fd = int(fd)
			s.err = syscall.EpollCtl(s.w.epfd, syscall.EPOLL_CTL_ADD, s.fd, &s.ev)
		}
		w.slots = append(w.slots, s)
	}
	s.ev = syscall.EpollEvent{Events: epollRDHUP, Fd: int32(s.idx), Pad: int32(s.gen)}
	s.err = nil
	if err := rc.Control(s.ctl); err != nil {
		w.release(s)
		return 0, err
	}
	if s.err != nil {
		err := s.err
		w.release(s)
		return 0, err
	}
	if onHangup == nil {
		onHangup = noop
	}
	s.onHangup = onHangup
	s.active = true
	return uint64(s.gen)<<32 | uint64(s.idx), nil
}

// release returns a slot to the free list, invalidating every id and queued event it had.
func (w *Watcher) release(s *slot) {
	s.active = false
	s.onHangup = nil
	s.gen++
	w.free = append(w.free, s.idx)
}

// Unwatch removes the registration. A stale or repeated id is ignored. When it returns, onHangup
// is not running and will not run.
func (w *Watcher) Unwatch(id uint64) {
	idx, gen := int(uint32(id)), uint32(id>>32)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || idx >= len(w.slots) {
		return
	}
	s := w.slots[idx]
	if !s.active || s.gen != gen {
		return
	}
	if !s.fired() {
		syscall.EpollCtl(w.epfd, syscall.EPOLL_CTL_DEL, s.fd, nil)
	}
	w.release(s)
}

// fired reports whether the hangup was already delivered, which removed the descriptor from epoll.
func (s *slot) fired() bool { return s.onHangup == nil }

// Close stops the goroutine and releases the epoll instance. Registrations are dropped silently.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	syscall.Write(w.wakeW, []byte{1})
	w.wg.Wait()
	syscall.Close(w.epfd)
	syscall.Close(w.wakeR)
	syscall.Close(w.wakeW)
	return nil
}

func (w *Watcher) loop() {
	defer w.wg.Done()
	var evs [128]syscall.EpollEvent
	for {
		n, err := syscall.EpollWait(w.epfd, evs[:], -1)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return
		}
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		for i := 0; i < n; i++ {
			ev := &evs[i]
			if ev.Fd < 0 || int(ev.Fd) >= len(w.slots) || ev.Events&hangupMask == 0 {
				continue
			}
			s := w.slots[ev.Fd]
			if !s.active || s.gen != uint32(ev.Pad) || s.fired() {
				continue
			}
			// Level-triggered RDHUP would fire again at once: leave epoll, then deliver once.
			syscall.EpollCtl(w.epfd, syscall.EPOLL_CTL_DEL, s.fd, nil)
			f := s.onHangup
			s.onHangup = nil
			f()
		}
		w.mu.Unlock()
	}
}

func noop() {}
