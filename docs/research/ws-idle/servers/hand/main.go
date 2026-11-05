package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"wsidle/common"
	"wsidle/hand"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19001", "")
	variant := flag.String("variant", "b", "b|b2")
	push := flag.Bool("push", false, "registry + broadcast on SIGUSR2")
	flag.Parse()
	hand.Push = *push
	common.Extra = func(m map[string]any) {
		g, p, n := hand.PoolStats()
		m["pool_gets"], m["pool_puts"], m["pool_new"] = g, p, n
	}
	common.StartStats()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	if *push {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR2)
		go func() {
			<-ch
			frame := hand.Frame([]byte("push!!"))
			for s := 0; s < 10; s++ {
				t0 := time.Now()
				conns := hand.Conns()
				// 4 writer goroutines over the registry
				var wg sync.WaitGroup
				for w := 0; w < 4; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						for i := w; i < len(conns); i += 4 {
							conns[i].Write(frame)
						}
					}(w)
				}
				wg.Wait()
				fmt.Fprintf(os.Stderr, "tick %d: %d conns in %v\n", s, len(conns), time.Since(t0))
				time.Sleep(time.Second - time.Since(t0))
			}
			fmt.Fprintln(os.Stderr, "broadcast done")
		}()
	}
	panic(hand.Serve(ln, *variant))
}
