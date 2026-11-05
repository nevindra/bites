package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type lazyCtx struct {
	parent context.Context
	mu     sync.Mutex
	done   chan struct{}
	err    error
}

func (c *lazyCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *lazyCtx) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done == nil {
		c.done = make(chan struct{})
		if c.err != nil {
			close(c.done)
		}
	}
	return c.done
}
func (c *lazyCtx) Err() error          { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *lazyCtx) Value(k any) any     { return c.parent.Value(k) }
func (c *lazyCtx) cancel(e error) {
	c.mu.Lock()
	c.err = e
	if c.done != nil {
		close(c.done)
	}
	c.mu.Unlock()
}

var sinkCtx context.Context

func main() {
	bg := context.Background()
	fmt.Println("WithCancel(Background) + cancel:", testing.AllocsPerRun(1000, func() {
		c, cancel := context.WithCancel(bg)
		sinkCtx = c
		cancel()
	}))
	parent, pcancel := context.WithCancel(bg)
	fmt.Println("WithCancel(cancelCtx parent) + cancel:", testing.AllocsPerRun(1000, func() {
		c, cancel := context.WithCancel(parent)
		sinkCtx = c
		cancel()
	}))
	fmt.Println("WithCancel(cancelCtx parent), Done() called, cancel:", testing.AllocsPerRun(1000, func() {
		c, cancel := context.WithCancel(parent)
		sinkCtx = c
		_ = c.Done()
		cancel()
	}))
	fmt.Println("WithCancelCause(cancelCtx parent) + cancel:", testing.AllocsPerRun(1000, func() {
		c, cancel := context.WithCancelCause(parent)
		sinkCtx = c
		cancel(nil)
	}))
	fmt.Println("AfterFunc(cancelCtx parent) + stop:", testing.AllocsPerRun(1000, func() {
		stop := context.AfterFunc(parent, func() {})
		stop()
	}))
	fmt.Println("AfterFunc(Background) + stop:", testing.AllocsPerRun(1000, func() {
		stop := context.AfterFunc(bg, func() {})
		stop()
	}))
	fmt.Println("custom lazy ctx (alloc ctx only, no Done):", testing.AllocsPerRun(1000, func() {
		c := &lazyCtx{parent: bg}
		sinkCtx = c
		c.cancel(context.Canceled)
	}))
	fmt.Println("custom lazy ctx, Done() called:", testing.AllocsPerRun(1000, func() {
		c := &lazyCtx{parent: bg}
		sinkCtx = c
		_ = c.Done()
		c.cancel(context.Canceled)
	}))
	// Custom ctx as parent of WithCancel: how many allocs? (goroutine started for non-std parents)
	lc := &lazyCtx{parent: bg}
	fmt.Println("WithCancel(custom parent) + cancel:", testing.AllocsPerRun(1000, func() {
		c, cancel := context.WithCancel(lc)
		sinkCtx = c
		cancel()
	}))
	pcancel()
}
