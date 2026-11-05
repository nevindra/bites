// The server of the WebSocket idle-memory measurement (ADR 0039): one typed WebSocket route that
// replies to a message with a message, and a stats dump on SIGUSR1 (as is) or SIGHUP (after two
// garbage collections), written as JSON to $STATS.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"syscall"

	nilogo "github.com/nevindra/bites"
)

type In struct{ Path struct{ ID int64 } }
type Msg struct{ Text string }
type Out struct {
	Text string `json:"text"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:19101", "")
	flag.Parse()
	app := nilogo.New(nilogo.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.WebSocket("/ws/{id}", func(_ context.Context, _ In, s *nilogo.Socket[Out]) (nilogo.Inbox[Msg], error) {
		return nilogo.Inbox[Msg]{OnMessage: func(_ context.Context, m Msg) error { return s.Send(Out{Text: m.Text}) }}, nil
	})
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	path := os.Getenv("STATS")
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1, syscall.SIGHUP)
	go func() {
		for sg := range ch {
			if sg == syscall.SIGHUP {
				runtime.GC()
				runtime.GC()
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			sm := []metrics.Sample{{Name: "/gc/scan/stack:bytes"}, {Name: "/memory/classes/heap/stacks:bytes"}, {Name: "/memory/classes/heap/objects:bytes"}}
			metrics.Read(sm)
			out := map[string]any{
				"goroutines": runtime.NumGoroutine(), "heap_inuse": m.HeapInuse, "stack_inuse": m.StackInuse, "sys": m.Sys,
				"scan_stack": sm[0].Value.Uint64(), "m_stacks": sm[1].Value.Uint64(), "m_objects": sm[2].Value.Uint64(),
				"mallocs": m.Mallocs, "num_gc": m.NumGC,
			}
			b, _ := json.Marshal(out)
			sfx := ""
			if sg == syscall.SIGHUP {
				sfx = ".gc"
			}
			os.WriteFile(path+sfx+".tmp", b, 0o644)
			os.Rename(path+sfx+".tmp", path+sfx)
		}
	}()
	app.Serve(ln)
}
