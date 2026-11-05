//go:build race

package jsoncodec

// The race detector makes sync.Pool drop items at random and instruments allocation, so
// allocation counts mean nothing under it.
const raceEnabled = true
