package main

import (
	"context"
	"engines/common"
	"flag"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	flag.Parse()
	common.StartStats()
	h := server.New(server.WithHostPorts(*addr), server.WithDisablePrintRoute(true))
	h.GET("/", func(ctx context.Context, c *app.RequestContext) { c.String(200, "hello") })
	h.GET("/block", func(ctx context.Context, c *app.RequestContext) { time.Sleep(10 * time.Millisecond); c.String(200, "hello") })
	h.Spin()
}
