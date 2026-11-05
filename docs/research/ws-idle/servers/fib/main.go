package main

import (
	"flag"
	stdhttp "net/http"
	"wsidle/common"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	flag.Parse()
	common.StartStats()
	ws := websocket.NewHandler(websocket.HandlerFuncs{
		Message: func(c *websocket.Connection, op websocket.Opcode, data []byte) {
			_ = c.WriteMessage(op, data)
		},
	})
	app := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.Request.URL.Path == "/ws" {
			_, _ = ws.Upgrade(c, nil)
			return
		}
		_ = c.Respond(stdhttp.StatusOK, "text/plain", []byte("hello\n"))
	})
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Addr: *addr, Handler: fibhttp.NewHandler(app)})
	if err != nil {
		panic(err)
	}
	defer engine.Close()
	panic(engine.Run())
}
