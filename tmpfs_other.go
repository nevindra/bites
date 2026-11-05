//go:build !linux

package bites

// onTmpfs is false where the framework cannot tell: the start-up warning is a Linux one.
func onTmpfs(string) bool { return false }
