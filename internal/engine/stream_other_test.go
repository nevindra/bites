//go:build !linux

package engine

import "net"

func getLowat(net.Conn) (int, error) { return -1, nil }
