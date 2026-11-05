// Micro costs for R7: start+exit of a goroutine, SetWriteDeadline, a wheel Add, a mutex pair.
package main

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
	"github.com/nevindra/bites/internal/wheel"
)

type owner struct{ n wheel.Node }

func main() {
	for r := 0; r < 3; r++ {
		g := testing.Benchmark(func(b *testing.B) {
			var wg sync.WaitGroup
			for i := 0; i < b.N; i++ {
				wg.Add(1)
				go func() { wg.Done() }()
				wg.Wait()
			}
		})
		g2 := testing.Benchmark(func(b *testing.B) { // fan-out: 1000 goroutines at a time, as a broadcast does
			var wg sync.WaitGroup
			for i := 0; i < b.N; i += 1000 {
				for j := 0; j < 1000; j++ {
					wg.Add(1)
					go func() { wg.Done() }()
				}
				wg.Wait()
			}
		})
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		c, _ := net.Dial("tcp", ln.Addr().String())
		defer c.Close()
		d := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			}
		})
		w := wheel.New(clock.Real(), wheel.Config{})
		var o owner
		o.n.Init(func() {})
		a := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				w.Add(&o.n, 10*time.Second)
			}
		})
		fmt.Printf("run %d: goroutine start+exit+wait %d ns (%d allocs); in bursts of 1000 %d ns; SetWriteDeadline %d ns; wheel.Add %d ns (%d allocs)\n", r, g.NsPerOp(), g.AllocsPerOp(), g2.NsPerOp(), d.NsPerOp(), a.NsPerOp(), a.AllocsPerOp())
	}
}
