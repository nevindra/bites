package main

import (
	"engines/common"
	"flag"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	flag.Parse()
	common.StartStats()
	mux := http.NewServeMux()
	mux.HandleFunc("/block", func(w http.ResponseWriter, r *http.Request) { time.Sleep(10 * time.Millisecond); w.Write([]byte("hello")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	panic(http.ListenAndServe(*addr, mux))
}
