//go:build linux

package engine

import (
	"net"
	"syscall"
)

// getLowat reads TCP_NOTSENT_LOWAT, or -1 when nc is not a TCP socket.
func getLowat(nc net.Conn) (int, error) {
	tc, ok := nc.(*net.TCPConn)
	if !ok {
		return -1, nil
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return -1, err
	}
	v := -1
	rc.Control(func(fd uintptr) { v, _ = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotsentLowat) })
	return v, nil
}
