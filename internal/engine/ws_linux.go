//go:build linux

package engine

import (
	"net"
	"os"
	"syscall"
	"unsafe"
)

// inliner is the machinery of one non-blocking write on a plain TCP socket (ADR 0037). The
// callback is a method value made once, and the arguments and results are fields, because a
// closure made for each write would allocate on every message. The caller holds the WS's mutex,
// so there is one write at a time.
type inliner struct {
	rc  syscall.RawConn
	fn  func(fd uintptr) bool
	b   []byte
	n   int
	err error
}

// canInline reports whether the socket can take an inline write: a *net.TCPConn, not TLS (a
// tls.Conn cannot be written without blocking, and a write timeout corrupts it for good) and not
// an in-memory connection. The caller holds mu.
func (w *WS) canInline() bool {
	if !w.inTried {
		w.inTried = true
		// c.nc itself, or the TCP socket under a plain PROXY protocol connection (ADR 0058),
		// which buffers nothing on the way out.
		nc := w.c.nc
		if pc, ok := nc.(*proxyConn); ok {
			nc = pc.Conn
		}
		if tc, ok := nc.(*net.TCPConn); ok && !w.c.srv.noInline {
			if rc, err := tc.SyscallConn(); err == nil {
				in := &inliner{rc: rc}
				in.fn = in.run
				w.in = in
			}
		}
	}
	return w.in != nil
}

// run writes with RawSyscall, as rawIO does (rawio_linux.go): a write to a non-blocking socket
// never blocks, so the scheduler's bookkeeping around a system call is left out.
func (in *inliner) run(fd uintptr) bool {
	for in.n < len(in.b) {
		rest := in.b[in.n:]
		m, _, e := syscall.RawSyscall(syscall.SYS_WRITE, fd, uintptr(unsafe.Pointer(unsafe.SliceData(rest))), uintptr(len(rest)))
		switch e {
		case 0:
			in.n += int(m)
		case syscall.EINTR:
		case syscall.EAGAIN:
			return true // the kernel's buffer is full: what is left goes to the queue
		default:
			in.err = os.NewSyscallError("write", e)
			return true
		}
	}
	return true
}

// write writes as much of b as the kernel takes without waiting and reports how much. An error
// is a socket that cannot be written any more.
func (in *inliner) write(b []byte) (int, error) {
	in.b, in.n, in.err = b, 0, nil
	err := in.rc.Write(in.fn)
	in.b = nil
	if err != nil {
		return in.n, err
	}
	return in.n, in.err
}
