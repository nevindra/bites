package main

import (
	"engines/common"
	"flag"
	"time"

	"github.com/valyala/fasthttp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	flag.Parse()
	common.StartStats()
	panic(fasthttp.ListenAndServe(*addr, func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Path()) == "/block" {
			time.Sleep(10 * time.Millisecond)
		}
		ctx.SetBodyString("hello")
	}))
}
