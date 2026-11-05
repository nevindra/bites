//go:build unix

package bites

import "syscall"

// fileLimit is the soft RLIMIT_NOFILE, or 0 if it cannot be read. It is the value the engine
// computes its own default from, so the start-up log can show the effective MaxConns.
func fileLimit() int {
	var r syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r); err != nil || r.Cur == 0 {
		return 0
	}
	if r.Cur > 1<<30 {
		return 1 << 30
	}
	return int(r.Cur)
}
