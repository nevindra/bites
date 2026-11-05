//go:build !linux

package engine

import (
	"net"
	"syscall"
)

// rawIO has no raw path off Linux: rawConnOf finds no RawConn, so every read and write goes
// through the net.Conn.
type rawIO struct {
	readFn, writeFn func(fd uintptr) bool
	b               []byte
	n               int
	err             error
	waited          bool
}

func (o *rawIO) init() {}

func rawConnOf(net.Conn) syscall.RawConn { return nil }
