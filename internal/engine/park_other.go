//go:build !linux

package engine

// Off Linux there is no poller: an idle connection moves its wait to a fresh goroutine after
// moveAfter and keeps that goroutine (ADR 0005), as before parking existed.

type poller struct{}

type pollerState struct{}

func (ps *parkState) init() {}

func (s *Server) getPoller() *poller { return nil }

func (p *poller) arm(*parkState) error { return errNoPoller }

func (p *poller) drop(*parkState) {}

func (p *poller) close() {}

var errNoPoller = errorString("bites: parking needs Linux")

type errorString string

func (e errorString) Error() string { return string(e) }
