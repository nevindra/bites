//go:build linux

package engine

import (
	"net"
	"syscall"
)

const tcpNotsentLowat = 25 // TCP_NOTSENT_LOWAT, not in package syscall

// setNotsentLowat sets TCP_NOTSENT_LOWAT on nc when it is a real TCP socket. It reports whether
// it did; an in-memory connection has no socket and is skipped without failing.
func setNotsentLowat(nc net.Conn, v int) bool {
	tc, ok := nc.(*net.TCPConn)
	if !ok {
		return false
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return false
	}
	done := false
	rc.Control(func(fd uintptr) {
		done = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotsentLowat, v) == nil
	})
	return done
}
