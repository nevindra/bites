//go:build linux

package bites

import "syscall"

const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458f6
)

// onTmpfs reports whether dir is on tmpfs or ramfs.
func onTmpfs(dir string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return false
	}
	return int64(st.Type) == tmpfsMagic || int64(st.Type) == ramfsMagic
}
