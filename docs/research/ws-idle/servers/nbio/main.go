package main

import (
	"flag"
	"net/http"
	"wsidle/common"

	"github.com/lesismal/nbio/nbhttp"
	"github.com/lesismal/nbio/nbhttp/websocket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	flag.Parse()
	common.StartStats()
	up := websocket.NewUpgrader()
	up.OnMessage(func(c *websocket.Conn, mt websocket.MessageType, data []byte) {
		_ = c.WriteMessage(mt, data)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if _, err := up.Upgrade(w, r, nil); err != nil {
			return
		}
	})
	eng := nbhttp.NewEngine(nbhttp.Config{Network: "tcp", Addrs: []string{*addr}, Handler: mux})
	if err := eng.Start(); err != nil {
		panic(err)
	}
	select {}
}
