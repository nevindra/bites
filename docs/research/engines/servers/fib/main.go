package main

import (
	"engines/common"
	"flag"
	stdhttp "net/http"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9001", "")
	async := flag.Bool("async", false, "Retain/Release with sleep in a goroutine")
	flag.Parse()
	common.StartStats()
	app := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.Request.URL.Path == "/block" {
			if *async {
				c.Retain()
				go func() {
					time.Sleep(10 * time.Millisecond)
					_ = c.Respond(stdhttp.StatusOK, "text/plain", []byte("hello"))
					c.Release()
				}()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = c.Respond(stdhttp.StatusOK, "text/plain", []byte("hello"))
	})
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Addr: *addr, Handler: fibhttp.NewHandler(app)})
	if err != nil {
		panic(err)
	}
	defer engine.Close()
	panic(engine.Run())
}
