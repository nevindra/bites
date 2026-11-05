// Package common holds what every Go server of the harness shares: the flags, and the SIGUSR1
// stats dump that the client reads for allocations and for heap plus stack per idle connection.
package common

import (
	"encoding/json"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"syscall"
	"time"
)

// Addr is the listen address, the one flag every server takes.
var Addr = flag.String("addr", "127.0.0.1:19001", "listen address")

// Wire shapes shared by the Go servers. The JSON names are the wire contract of the
// reference route, and match budgetIn and budgetOut of budget_test.go in the root package.
type (
	// NewUser is the body of POST /users/{id}.
	NewUser struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Age   int    `json:"age"`
	}
	// Created is the answer of POST /users/{id}.
	Created struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Age   int    `json:"age"`
		Page  int    `json:"page"`
	}
	// Blocked is the answer of GET /block, the stand-in for a handler that waits on a database.
	Blocked struct {
		OK bool `json:"ok"`
	}
	// User is the answer of GET /users/{id}.
	User struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Age   int    `json:"age"`
	}
)

// BlockDelay is how long GET /block sleeps in every server: 10 ms of time.Sleep, the stand-in
// for a database call (the "block" experiment, as in docs/research/engines/run1.sh).
const BlockDelay = 10 * time.Millisecond

// StartStats installs the SIGUSR1 handler: on the signal the process runs two collections, then
// writes heap plus stack from runtime/metrics (heap objects and goroutine stacks, the measure of
// idle_test.go and ADR 0005), its MemStats counters and its goroutine count as JSON to the file
// named by $STATS, by rename so a reader never sees half.
func StartStats() {
	path := os.Getenv("STATS")
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			runtime.GC()
			runtime.GC()
			s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}}
			metrics.Read(s)
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			out := map[string]any{
				"gomaxprocs": runtime.GOMAXPROCS(0), "goroutines": runtime.NumGoroutine(),
				"heap_objects": s[0].Value.Uint64(), "stacks": s[1].Value.Uint64(),
				"heap_inuse": m.HeapInuse, "stack_inuse": m.StackInuse, "heap_alloc": m.HeapAlloc,
				"sys": m.Sys, "num_gc": m.NumGC, "mallocs": m.Mallocs, "total_alloc": m.TotalAlloc,
			}
			b, _ := json.Marshal(out)
			if path != "" {
				os.WriteFile(path+".tmp", b, 0o644)
				os.Rename(path+".tmp", path)
			}
		}
	}()
}
