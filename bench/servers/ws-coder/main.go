// The coder/websocket server of the harness (github.com/coder/websocket, formerly nhooyr) on
// net/http, compression off (its default), a read loop of Reader/Writer so no message is copied
// into a fresh slice. GET /ws echoes with the message's own type; GET /wsjson decodes each message
// into a struct with goccy/go-json and writes the struct back. Any origin is allowed.
package main

import (
	"context"
	"flag"
	"io"
	"net/http"

	"github.com/coder/websocket"
	gojson "github.com/goccy/go-json"

	"github.com/nevindra/bites/bench/common"
)

func serve(json bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		for {
			t, rd, err := c.Reader(ctx)
			if err != nil {
				return
			}
			wr, err := c.Writer(ctx, t)
			if err != nil {
				return
			}
			if !json {
				if _, err := io.Copy(wr, rd); err != nil {
					return
				}
			} else {
				var m common.Said
				if gojson.NewDecoder(rd).Decode(&m) != nil {
					return
				}
				if gojson.NewEncoder(wr).Encode(m) != nil {
					return
				}
			}
			if wr.Close() != nil {
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
