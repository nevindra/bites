// The gorilla/websocket server of the harness on net/http, with the write buffer pool gorilla
// documents for many idle connections (Upgrader.WriteBufferPool) and a read loop of
// NextReader/NextWriter, so no message is copied into a fresh slice. GET /ws echoes with the
// message's own type; GET /wsjson decodes each message into a struct with goccy/go-json and writes
// the struct back. Any origin is allowed.
package main

import (
	"flag"
	"io"
	"net/http"
	"sync"

	gojson "github.com/goccy/go-json"
	"github.com/gorilla/websocket"

	"github.com/nevindra/bites/bench/common"
)

var up = websocket.Upgrader{WriteBufferPool: &sync.Pool{}, CheckOrigin: func(*http.Request) bool { return true }}

func serve(json bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			t, rd, err := c.NextReader()
			if err != nil {
				return
			}
			if !json {
				wr, err := c.NextWriter(t)
				if err != nil {
					return
				}
				if _, err := io.Copy(wr, rd); err != nil || wr.Close() != nil {
					return
				}
				continue
			}
			var m common.Said
			if gojson.NewDecoder(rd).Decode(&m) != nil {
				return
			}
			if c.WriteJSON(m) != nil {
				return
			}
		}
	}
}

func main() {
	flag.Parse()
	common.StartStats()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", serve(false))
	mux.HandleFunc("GET /wsjson", serve(true))
	if err := http.ListenAndServe(*common.Addr, mux); err != nil {
		panic(err)
	}
}
