//go:build !linux

package engine

import "net"

// setNotsentLowat is Linux only; elsewhere the per-chunk deadline alone bounds a slow reader.
func setNotsentLowat(net.Conn, int) bool { return false }
