package main

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

var lg = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
var errX = errors.New("tls: first record does not look like a TLS handshake")
var addr net.Addr = net.TCPAddrFromAddrPort(netip.MustParseAddrPort("203.0.113.7:51234"))

// today: Debug call with remote string built eagerly.
func BenchmarkTodayDebugDisabled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lg.Debug("nilogo: TLS handshake failed", "err", errX, "remote", addr.String())
	}
}

type gate struct {
	failed atomic.Uint64
	next   atomic.Int64 // unix nanos before which no Warn is written
	supp   atomic.Uint64
}

func (g *gate) fail(now time.Time, err error, a net.Addr) {
	g.failed.Add(1)
	n := now.UnixNano()
	nx := g.next.Load()
	if n < nx || !g.next.CompareAndSwap(nx, n+int64(time.Minute)) {
		g.supp.Add(1)
		return
	}
	lg.Warn("nilogo: TLS handshakes are failing", "err", err, "remote", a.String(), "suppressed", g.supp.Swap(0))
}

// proposed: counter plus a once-a-minute Warn; the remote string is built only when it is written.
func BenchmarkGateSuppressed(b *testing.B) {
	var g gate
	g.fail(time.Now(), errX, addr) // first one writes
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		g.fail(now, errX, addr)
	}
}

func BenchmarkGateSuppressedParallel(b *testing.B) {
	var g gate
	g.fail(time.Now(), errX, addr)
	now := time.Now()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			g.fail(now, errX, addr)
		}
	})
}

// the cost of an actual Warn line, once a minute.
func BenchmarkWarnWritten(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		lg.Warn("nilogo: TLS handshakes are failing", "err", errX, "remote", addr.String(), "suppressed", uint64(5))
	}
}
