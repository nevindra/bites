package main

import (
	"fmt"
	"runtime"
	"unsafe"
)

var sink [][]byte

//go:noinline
func makeStr(want string) (string, *bool) {
	buf := make([]byte, 64)
	copy(buf, want)
	freed := new(bool)
	// finalizer on the backing array's owner: attach to &buf[0] (heap object start).
	runtime.SetFinalizer(&buf[0], func(*byte) { *freed = true })
	return unsafe.String(&buf[0], len(want)), freed
}

func churn() {
	for i := 0; i < 200000; i++ {
		b := make([]byte, 64)
		for j := range b {
			b[j] = 'Z'
		}
		if i%1000 == 0 {
			sink = append(sink, b)
		}
	}
}

func main() {
	strs := make([]string, 0, 100)
	flags := make([]*bool, 0, 100)
	for i := 0; i < 100; i++ {
		s, f := makeStr(fmt.Sprintf("request-%03d-payload", i))
		strs = append(strs, s)
		flags = append(flags, f)
	}
	for r := 0; r < 5; r++ {
		runtime.GC()
		churn()
		runtime.GC()
		time := 0
		_ = time
	}
	bad, freed := 0, 0
	for i, s := range strs {
		if s != fmt.Sprintf("request-%03d-payload", i) {
			bad++
		}
		if *flags[i] {
			freed++
		}
	}
	fmt.Printf("strings corrupted: %d/100, backing arrays finalized while string alive: %d/100\n", bad, freed)
	// Control: drop the strings, then GC: finalizers must run (shows the probe can detect collection).
	for i := range strs {
		strs[i] = ""
	}
	for r := 0; r < 5; r++ {
		runtime.GC()
		runtime.Gosched()
	}
	runtime.GC()
	freed = 0
	for _, f := range flags {
		if *f {
			freed++
		}
	}
	fmt.Printf("control: after dropping the strings, finalized: %d/100\n", freed)
	runtime.KeepAlive(strs)
}
