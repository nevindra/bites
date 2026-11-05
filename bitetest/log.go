package bitetest

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// sink writes each log line into tb.Log until it is stopped. testing panics when Log is called
// after a test has finished, and a connection's goroutine can log after its handler returned, so
// stop (called by Server.Close, which runs as a cleanup, before the test finishes) makes the sink
// drop lines, under the lock a write holds.
type sink struct {
	tb   testing.TB
	mu   sync.Mutex
	done bool
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.done {
		s.tb.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

func (s *sink) stop() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
}

// testLog is the slog.Handler Start gives an App that has no logger of its own: the text handler
// without the time, over a sink.
type testLog struct {
	slog.Handler
	s *sink
}

func newTestLog(tb testing.TB) *testLog {
	s := &sink{tb: tb}
	h := slog.NewTextHandler(s, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return a
	}})
	return &testLog{h, s}
}

func (l *testLog) WithAttrs(as []slog.Attr) slog.Handler {
	return &testLog{l.Handler.WithAttrs(as), l.s}
}

func (l *testLog) WithGroup(name string) slog.Handler {
	return &testLog{l.Handler.WithGroup(name), l.s}
}

func (l *testLog) stop() { l.s.stop() }
