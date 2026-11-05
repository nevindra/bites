// The server of the parking measurement (docs/research/parking): the reference routes of
// bench/servers/nilogo (GET and POST /users/{id}, the same Inputs and answers) and the WebSocket
// route of docs/research/ws-gate (/ws/{id}, echoes a message), plain or TLS, with parking on or
// off (-park). On SIGUSR1 it runs two collections and writes heap plus stack from
// runtime/metrics, its MemStats counters and its goroutine count to $STATS, as bench/common does.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"syscall"
	"time"

	nilogo "github.com/nevindra/bites"
)

type postIn struct {
	Path  struct{ ID int64 }
	Query struct {
		Expand nilogo.Opt[bool]
		Page   int
	}
	Body struct {
		Name  string
		Email string
		Age   int
	}
}

type getIn struct {
	Path struct{ ID int64 }
}

type created struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
	Page  int    `json:"page"`
}

type user struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

type wsIn struct{ Path struct{ ID int64 } }
type wsMsg struct{ Text string }
type wsOut struct {
	Text string `json:"text"`
}

var (
	addr   = flag.String("addr", "127.0.0.1:19201", "listen address")
	useTLS = flag.Bool("tls", false, "serve TLS with a self-signed ecdsa P-256 certificate")
	park   = flag.String("park", "on", "on|off: parking of idle connections (WithoutParking when off)")
)

func selfSigned() *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bench.test"}, DNSNames: []string{"bench.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func startStats() {
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

func main() {
	flag.Parse()
	startStats()
	opts := append([]nilogo.Option{nilogo.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}, parkOptions(*park)...)
	app := nilogo.New(opts...)
	app.Post("/users/{id}", func(_ context.Context, in postIn) (created, error) {
		return created{ID: in.Path.ID, Name: in.Body.Name, Email: in.Body.Email, Age: in.Body.Age, Page: in.Query.Page}, nil
	})
	app.Get("/users/{id}", func(_ context.Context, in getIn) (user, error) {
		return user{ID: in.Path.ID, Name: "ada", Email: "ada@example.dev", Age: 36}, nil
	})
	app.WebSocket("/ws/{id}", func(_ context.Context, _ wsIn, s *nilogo.Socket[wsOut]) (nilogo.Inbox[wsMsg], error) {
		return nilogo.Inbox[wsMsg]{OnMessage: func(_ context.Context, m wsMsg) error { return s.Send(wsOut{Text: m.Text}) }}, nil
	})
	var err error
	if *useTLS {
		err = app.ListenTLS(*addr, selfSigned())
	} else {
		err = app.Listen(*addr)
	}
	if err != nil {
		panic(err)
	}
}
