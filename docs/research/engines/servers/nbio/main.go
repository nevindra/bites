package main

import (
	"engines/common"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lesismal/nbio/nbhttp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	flag.Parse()
	common.StartStats()
	mux := http.NewServeMux()
	mux.HandleFunc("/block", func(w http.ResponseWriter, r *http.Request) { time.Sleep(10 * time.Millisecond); w.Header().Set("Content-Length", "5"); w.Write([]byte("hello")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Content-Length", "5"); w.Write([]byte("hello")) })
	e := nbhttp.NewEngine(nbhttp.Config{Network: "tcp", Addrs: []string{*addr}, Handler: mux})
	if err := e.Start(); err != nil {
		panic(err)
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	e.Stop()
}
