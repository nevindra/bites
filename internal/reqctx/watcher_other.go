//go:build !linux

package reqctx

import (
	"errors"
	"syscall"
)

// Watcher is only implemented on Linux; elsewhere the engine falls back to a reader goroutine.
type Watcher struct{}

func NewWatcher() (*Watcher, error) {
	return nil, errors.New("reqctx: the epoll watcher needs Linux")
}

func (w *Watcher) Watch(rc syscall.RawConn, onHangup func()) (uint64, error) {
	return 0, errors.New("reqctx: the epoll watcher needs Linux")
}

func (w *Watcher) Unwatch(id uint64) {}

func (w *Watcher) Close() error { return nil }
