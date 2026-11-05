package main

import (
	"engines/common"
	"engines/hand"
	"flag"
	"net"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	variant := flag.String("variant", "a", "a|b")
	flag.Parse()
	common.StartStats()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	panic(hand.Serve(ln, *variant))
}
