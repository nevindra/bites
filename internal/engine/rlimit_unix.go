//go:build unix

package engine

import "syscall"

// fileLimit is the soft RLIMIT_NOFILE, or 0 if it cannot be read.
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
