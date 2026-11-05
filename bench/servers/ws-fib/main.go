// The fib WebSocket server of the harness (github.com/lesismal/fib/websocket), on fib's event-loop
// engine with fib.DefaultConfig() and websocket.DefaultConfig(), any origin allowed (the client
// sends Origin: http://x). GET /ws echoes each message with its own opcode; GET /wsjson decodes
// each message into a struct with goccy/go-json and writes the struct back, as the gws server
// does. fib hands OnMessage a payload valid only for the callback (its doc comment), so the echo
// writes it before returning and keeps nothing.
package main

import (
	"flag"
	stdhttp "net/http"

	gojson "github.com/goccy/go-json"
	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/websocket"

	"github.com/nevindra/bites/bench/common"
)

func handler(json bool) *websocket.ServerHandler {
	cfg := websocket.DefaultConfig()
	cfg.CheckOrigin = func(*stdhttp.Request) bool { return true }
	return websocket.NewHandlerWithConfig(cfg, websocket.HandlerFuncs{
		Message: func(c *websocket.Connection, op websocket.Opcode, data []byte) {
			if !json {
				_ = c.WriteMessage(op, data)
				return
			}
			var m common.Said
			if gojson.Unmarshal(data, &m) != nil {
				return
			}
			b, _ := gojson.Marshal(m)
			_ = c.WriteMessage(websocket.Text, b)
		},
	})
}

func main() {
	flag.Parse()
	common.StartStats()
	raw, js := handler(false), handler(true)
	app := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		switch c.Request.URL.Path {
		case "/ws":
			_, _ = raw.Upgrade(c, nil)
		case "/wsjson":
			_, _ = js.Upgrade(c, nil)
		default:
			_ = c.Respond(stdhttp.StatusNotFound, "text/plain", []byte("not found\n"))
		}
	})
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Addr: *common.Addr, Handler: fibhttp.NewHandler(app)})
	if err != nil {
		panic(err)
	}
	defer engine.Close()
	panic(engine.Run())
}
