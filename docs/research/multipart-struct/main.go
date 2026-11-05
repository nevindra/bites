// Probe for ADR 0044 (a multipart form decoded into a struct): what keeping a file part costs
// in memory against an unlinked temporary file, per size, and that an unlinked file is still
// readable (so a crash leaves nothing behind). Run: go run . ; nothing is built into the tree.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func mem(src []byte) (time.Duration, uint64) {
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t := time.Now()
	var b []byte
	r := bytes.NewReader(src)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		b = append(b, buf[:n]...)
		if err == io.EOF {
			break
		}
	}
	d := time.Since(t)
	runtime.ReadMemStats(&m1)
	_ = b
	return d, m1.Mallocs - m0.Mallocs
}

func spill(src []byte) (time.Duration, uint64) {
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	t := time.Now()
	f, err := os.CreateTemp("", "bites-part-*")
	if err != nil {
		panic(err)
	}
	os.Remove(f.Name()) // unlinked at once: nothing to clean after a crash
	r := bytes.NewReader(src)
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		f.Write(buf[:n])
		if err == io.EOF {
			break
		}
	}
	sr := io.NewSectionReader(f, 0, int64(len(src)))
	n, _ := io.Copy(io.Discard, sr) // still readable after the unlink
	if n != int64(len(src)) {
		panic("short")
	}
	d := time.Since(t)
	f.Close()
	runtime.ReadMemStats(&m1)
	return d, m1.Mallocs - m0.Mallocs
}

func main() {
	fmt.Println("dir:", os.TempDir())
	for _, sz := range []int{64 << 10, 256 << 10, 1 << 20, 8 << 20, 64 << 20} {
		src := make([]byte, sz)
		for i := 0; i < 3; i++ {
			dm, am := mem(src)
			ds, as := spill(src)
			fmt.Printf("%9d B  memory %10v %4d allocs   spill %10v %4d allocs\n", sz, dm, am, ds, as)
		}
	}
}
