// Package prof adds one thing to the bench servers: on SIGUSR2 it writes a CPU profile of
// $PROF_SECS seconds (default 10) to $PROF_FILE, sampled at $PROF_HZ (default 500). Nothing else
// about a server changes.
package prof

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"syscall"
	"time"
)

// Start installs the SIGUSR2 handler; without $PROF_FILE it does nothing.
func Start() {
	path := os.Getenv("PROF_FILE")
	if path == "" {
		return
	}
	secs, hz := 10, 500
	if v, err := strconv.Atoi(os.Getenv("PROF_SECS")); err == nil && v > 0 {
		secs = v
	}
	if v, err := strconv.Atoi(os.Getenv("PROF_HZ")); err == nil && v > 0 {
		hz = v
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	go func() {
		<-ch
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "prof:", err)
			return
		}
		runtime.SetCPUProfileRate(hz) // before Start, so the runtime's own 100 Hz default is not used
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "prof:", err)
			return
		}
		time.Sleep(time.Duration(secs) * time.Second)
		pprof.StopCPUProfile()
		f.Close()
	}()
}
