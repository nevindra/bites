package bites

import "unsafe"

// noescape hides p from escape analysis. The framework fills an Input that lives on the stack of
// a generic function, through decoders it holds as function values; the compiler cannot see that
// none of them keeps the pointer and would move the Input to the heap, which is an allocation a
// request (ADR 0003). The lie is true: every decoder reached through a noescape pointer writes
// to it for the length of the call and keeps nothing. The one place a user's method could keep a
// receiver, UnmarshalText, is given a fresh value instead (internal/wire). The same device is in
// internal/jsoncodec.
//
// It is never inlined. Inlined, the conversion to uintptr is an ordinary value the compiler may
// compute once and keep across a call, in a slot the runtime does not treat as a pointer; when
// the goroutine's stack is moved during that call, the pointer made back from it still names the
// old stack, and writes through it are lost. A list in a JSON body nested two objects deep came
// back as a nil slice and a 500 for about two requests in three on a fresh connection (found in
// milestone 2 step 8; the test is in input_test.go). As a call, the conversion and its reverse
// happen inside this function with no call between them, where the stack cannot move, and the
// argument is a pointer the runtime adjusts.
//
//go:noinline
func noescape[T any](p *T) *T {
	x := uintptr(unsafe.Pointer(p))
	return (*T)(*(*unsafe.Pointer)(unsafe.Pointer(&x)))
}

// noescapeP is noescape for an unsafe.Pointer, never inlined for the same reason.
//
//go:noinline
func noescapeP(p unsafe.Pointer) unsafe.Pointer {
	x := uintptr(p)
	return *(*unsafe.Pointer)(unsafe.Pointer(&x))
}
