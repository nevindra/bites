//go:build linux

package engine

import (
	"io"
	"net"
	"os"
	"syscall"
	"unsafe"
)

// rawIO is one read or one write on a plain TCP socket through its syscall.RawConn, the system
// call made with syscall.RawSyscall. A busy connection's two calls a request are system calls of
// a microsecond on a non-blocking socket: RawSyscall leaves out the scheduler's bookkeeping around
// each (entersyscall, exitsyscall) and tells the caller whether the call had to wait, which a
// net.Conn hides. Waiting itself is the RawConn's: an EAGAIN returns false and the RawConn parks
// on the poller with the socket's deadline, as net.Conn does. The callbacks are method values made
// with the scratch, and the arguments and results are fields, so a call allocates nothing.
type rawIO struct {
	readFn, writeFn func(fd uintptr) bool
	b               []byte
	n               int
	err             error
	waited          bool // the write found the kernel's buffer full and waited for room
}

func (o *rawIO) init() {
	o.readFn = o.read
	o.writeFn = o.write
}

func (o *rawIO) read(fd uintptr) bool {
	for {
		m, _, e := syscall.RawSyscall(syscall.SYS_READ, fd, uintptr(unsafe.Pointer(unsafe.SliceData(o.b))), uintptr(len(o.b)))
		switch e {
		case 0:
			o.n = int(m)
			if m == 0 {
				o.err = io.EOF
			}
			return true
		case syscall.EINTR:
		case syscall.EAGAIN:
			return false
		default:
			o.err = os.NewSyscallError("read", e)
			return true
		}
	}
}

func (o *rawIO) write(fd uintptr) bool {
	for o.n < len(o.b) {
		rest := o.b[o.n:]
		m, _, e := syscall.RawSyscall(syscall.SYS_WRITE, fd, uintptr(unsafe.Pointer(unsafe.SliceData(rest))), uintptr(len(rest)))
		switch e {
		case 0:
			o.n += int(m)
		case syscall.EINTR:
		case syscall.EAGAIN:
			o.waited = true
			return false
		default:
			o.err = os.NewSyscallError("write", e)
			return true
		}
	}
	return true
}

// rawConnOf is the RawConn of a bare TCP connection, the only kind rawIO serves: TLS keeps its own
// records, an in-memory connection has no socket, and a PROXY protocol connection may hold bytes
// it read past its header.
func rawConnOf(nc net.Conn) syscall.RawConn {
	if tc, ok := nc.(*net.TCPConn); ok {
		if rc, err := tc.SyscallConn(); err == nil {
			return rc
		}
	}
	return nil
}
