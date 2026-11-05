package main

import (
	"flag"
	"net/http"
	"wsidle/common"

	"github.com/lxzan/gws"
)

type H struct{ gws.BuiltinEventHandler }

func (H) OnMessage(c *gws.Conn, m *gws.Message) {
	defer m.Close()
	_ = c.WriteMessage(m.Opcode, m.Bytes())
}

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	flag.Parse()
	common.StartStats()
	up := gws.NewUpgrader(H{}, &gws.ServerOption{}) // defaults; ParallelEnabled false
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r)
		if err != nil {
			return
		}
		go c.ReadLoop()
	})
	panic(http.ListenAndServe(*addr, nil))
}
