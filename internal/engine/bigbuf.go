package engine

import (
	"math/bits"
	"sync"
	"unsafe"
)

// Body buffers past maxPooled. A scratch keeps only a buffer of at most maxPooled, so one large
// answer does not stay with a scratch that then serves small ones; a larger body buffer goes back
// to one of these pools by its size class instead of to the garbage collector, and Reserve takes
// one for an answer whose size the framework expects. A sync.Pool lets go of what was not taken
// again within two collections, so the pools hold what the current load uses and no more.
//
// The pools hold the address of the buffer's first byte, not a slice: an unsafe.Pointer goes into
// an interface without an allocation, and the class gives back the capacity.
const (
	bigMinShift = 17 // the smallest class, 128 KiB: twice maxPooled
	bigMaxShift = 23 // the largest class, 8 MiB; a larger buffer is not kept
)

var bigBufs [bigMaxShift - bigMinShift + 1]sync.Pool

// takeBig returns an empty buffer whose capacity is at least n.
func takeBig(n int) []byte {
	k := max(bits.Len(uint(n-1)), bigMinShift)
	if k > bigMaxShift {
		return make([]byte, 0, n)
	}
	if p, ok := bigBufs[k-bigMinShift].Get().(unsafe.Pointer); ok {
		return unsafe.Slice((*byte)(p), 1<<k)[:0]
	}
	return make([]byte, 0, 1<<k)
}

// putBig gives b back to the class its capacity fills; a buffer below the smallest class or
// above the largest is left to the collector.
func putBig(b []byte) {
	k := bits.Len(uint(cap(b))) - 1 // the largest class b can hold
	if k < bigMinShift || k > bigMaxShift {
		return
	}
	bigBufs[k-bigMinShift].Put(unsafe.Pointer(unsafe.SliceData(b[:1])))
}
