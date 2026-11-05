//go:build !unix

package bites

func fileLimit() int { return 0 }
