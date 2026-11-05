// Package common: SIGUSR1 stats dump and goroutine-peak sampler shared by all servers.
package common

import (
	"encoding/json"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"sync/atomic"
	"syscall"
	"time"
)

var maxG atomic.Int64

// Extra is called on SIGUSR1 to add server-specific fields.
var Extra func(map[string]any)

func StartStats() {
	path := os.Getenv("STATS")
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGHUP)
	go func() {
		t := time.NewTicker(25 * time.Millisecond)
		for {
			select {
			case <-t.C:
				if g := int64(runtime.NumGoroutine()); g > maxG.Load() {
					maxG.Store(g)
				}
			case sg := <-ch:
				if sg == syscall.SIGHUP {
					runtime.GC()
					runtime.GC()
				}
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				out := map[string]any{
					"gomaxprocs": runtime.GOMAXPROCS(0), "goroutines": runtime.NumGoroutine(), "max_goroutines": maxG.Load(),
					"heap_inuse": m.HeapInuse, "stack_inuse": m.StackInuse, "sys": m.Sys, "heap_alloc": m.HeapAlloc,
					"num_gc": m.NumGC, "mallocs": m.Mallocs, "total_alloc": m.TotalAlloc,
					"mspan_inuse": m.MSpanInuse, "gcsys": m.GCSys, "other_sys": m.OtherSys,
				}
				sm := []metrics.Sample{{Name: "/gc/scan/stack:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}, {Name: "/memory/classes/total:bytes"}}
				metrics.Read(sm)
				out["scan_stack"] = sm[0].Value.Uint64()
				out["m_stacks"] = sm[1].Value.Uint64()
				out["m_total"] = sm[2].Value.Uint64()
				if Extra != nil {
					Extra(out)
				}
				b, _ := json.Marshal(out)
				if path != "" {
					sfx := ""
					if sg == syscall.SIGHUP {
						sfx = ".gc"
					}
					os.WriteFile(path+sfx+".tmp", b, 0o644)
					os.Rename(path+sfx+".tmp", path+sfx)
				}
			}
		}
	}()
}
