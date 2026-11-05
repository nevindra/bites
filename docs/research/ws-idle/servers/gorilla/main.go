package main

import (
	"flag"
	"net/http"
	"wsidle/common"

	"github.com/gorilla/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	flag.Parse()
	common.StartStats()
	up := websocket.Upgrader{}
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			if c.WriteMessage(mt, b) != nil {
				return
			}
		}
	})
	panic(http.ListenAndServe(*addr, nil))
}
