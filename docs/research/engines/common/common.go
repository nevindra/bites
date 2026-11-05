// Package common: SIGUSR1 stats dump and goroutine-peak sampler shared by all servers.
package common

import (
	"encoding/json"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
)

var maxG atomic.Int64

func StartStats() {
	path := os.Getenv("STATS")
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		t := time.NewTicker(25 * time.Millisecond)
		for {
			select {
			case <-t.C:
				if g := int64(runtime.NumGoroutine()); g > maxG.Load() {
					maxG.Store(g)
				}
			case <-ch:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				out := map[string]any{
					"gomaxprocs": runtime.GOMAXPROCS(0), "goroutines": runtime.NumGoroutine(), "max_goroutines": maxG.Load(),
					"heap_inuse": m.HeapInuse, "stack_inuse": m.StackInuse, "sys": m.Sys, "heap_alloc": m.HeapAlloc,
					"num_gc": m.NumGC, "mallocs": m.Mallocs, "total_alloc": m.TotalAlloc,
					"mspan_inuse": m.MSpanInuse, "gcsys": m.GCSys, "other_sys": m.OtherSys,
				}
				b, _ := json.Marshal(out)
				if path != "" {
					os.WriteFile(path+".tmp", b, 0o644)
					os.Rename(path+".tmp", path)
				}
			}
		}
	}()
}
