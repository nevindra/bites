package main

import (
	"context"
	"flag"
	"net/http"
	"wsidle/common"

	"github.com/coder/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	flag.Parse()
	common.StartStats()
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := context.Background()
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			if c.Write(ctx, typ, b) != nil {
				return
			}
		}
	})
	panic(http.ListenAndServe(*addr, nil))
}
