// Probe for the ADR 0046 gaps: the ping bucket, a pong flood, and a client echoing the close status
// the server sent. Run: go run ./ctl
package main

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/ws"
)

func main() {
	lim := ws.DefaultLimits()
	key := [4]byte{1, 2, 3, 4}
	now := time.Unix(1000, 0)

	// Pings: how many in a burst before 1008, and the sustained rate.
	var r ws.Reader
	r.Reset(&lim)
	ping := ws.AppendClientFrame(nil, true, ws.OpPing, nil, key)
	n := 0
	for ; n < 100; n++ {
		_, ev := r.Feed(ping, now)
		if ev.Kind == ws.KindFail {
			fmt.Printf("burst: the ping number %d at the same instant fails with %d (%s)\n", n+1, ev.Err.Code, ev.Err.Reason)
			break
		}
	}
	r.Reset(&lim)
	ok := 0
	for i := 0; i < 3200; i++ { // 32 a second for 100 s, exactly on the refill rate
		_, ev := r.Feed(ping, now.Add(time.Duration(i)*time.Second/32))
		if ev.Kind == ws.KindFail {
			break
		}
		ok++
	}
	fmt.Printf("sustained 32/s for 100 s: %d of 3200 accepted\n", ok)

	// Pongs: unlimited today?
	r.Reset(&lim)
	pong := ws.AppendClientFrame(nil, true, ws.OpPong, nil, key)
	bad := 0
	for i := 0; i < 1_000_000; i++ {
		_, ev := r.Feed(pong, now)
		if ev.Kind == ws.KindFail {
			bad++
		}
	}
	fmt.Printf("1,000,000 pongs at one instant: %d refused (a pong flood is not limited as built)\n", bad)
	res := testing.Benchmark(func(b *testing.B) {
		var r ws.Reader
		r.Reset(&lim)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			r.Feed(pong, now)
		}
	})
	fmt.Printf("cost of a pong in the Reader: %d ns, %d allocs (6 bytes on the wire)\n", res.NsPerOp(), res.AllocsPerOp())

	// A client echoing the status the server closed with.
	for _, code := range []uint16{1000, 1001, 1008, 1009, 1011, 1012, 1013} {
		var rr ws.Reader
		rr.Reset(&lim)
		p := binary.BigEndian.AppendUint16(nil, code)
		_, ev := rr.Feed(ws.AppendClientFrame(nil, true, ws.OpClose, p, key), now)
		verdict := "accepted"
		if ev.Kind == ws.KindFail {
			verdict = fmt.Sprintf("FAIL %d %q", ev.Err.Code, ev.Err.Reason)
		}
		fmt.Printf("client Close echoing %d: %s\n", code, verdict)
	}
}
