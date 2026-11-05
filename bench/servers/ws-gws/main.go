// The gws WebSocket server of the harness (github.com/lxzan/gws) on net/http, with gws's default
// ServerOption (no compression, one read goroutine a socket with ReadLoop). GET /ws echoes each
// message with its own opcode; GET /wsjson decodes each message into a struct with goccy/go-json
// and writes the struct back, the fast JSON a gws application would pick. The message's buffer
// goes back to gws's pool with message.Close, as gws documents.
package main

import (
	"flag"
	"net/http"

	gojson "github.com/goccy/go-json"
	"github.com/lxzan/gws"

	"github.com/nevindra/bites/bench/common"
)

type echo struct {
	gws.BuiltinEventHandler
	json bool
}

func (e *echo) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if !e.json {
		_ = socket.WriteMessage(message.Opcode, message.Bytes())
		return
	}
	var m common.Said
	if gojson.Unmarshal(message.Bytes(), &m) != nil {
		return
	}
	b, _ := gojson.Marshal(m)
	_ = socket.WriteMessage(gws.OpcodeText, b)
}

func serve(json bool) http.HandlerFunc {
	up := gws.NewUpgrader(&echo{json: json}, &gws.ServerOption{})
	return func(w http.ResponseWriter, r *http.Request) {
		socket, err := up.Upgrade(w, r)
		if err != nil {
			return
		}
		go socket.ReadLoop()
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
