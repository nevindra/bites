//go:build !linux

package engine

// inliner has no inline write off Linux: every frame goes through the queue and the flusher,
// which is correct and costs a goroutine start per burst (ADR 0037).
type inliner struct{}

func (w *WS) canInline() bool { return false }

func (in *inliner) write(b []byte) (int, error) { return 0, nil }
