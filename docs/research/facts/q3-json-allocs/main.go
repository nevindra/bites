package main

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"fmt"
	"testing"
	"unsafe"
)

type S struct {
	A, B, C string
	X, Y    int
}

var doc = []byte(`{"A":"alpha","B":"bravo","C":"charlie","X":1,"Y":2}`)

func main() {
	var s S
	fmt.Println("v2 Unmarshal allocs:", testing.AllocsPerRun(1000, func() { s = S{}; json.Unmarshal(doc, &s) }))
	fmt.Println("v1 Unmarshal allocs:", testing.AllocsPerRun(1000, func() { s = S{}; jsonv1.Unmarshal(doc, &s) }))
	// do decoded strings alias the input?
	s = S{}
	json.Unmarshal(doc, &s)
	fmt.Println("v2 decoded string aliases input:", uintptr(unsafe.Pointer(unsafe.StringData(s.A))) >= uintptr(unsafe.Pointer(&doc[0])) && uintptr(unsafe.Pointer(unsafe.StringData(s.A))) < uintptr(unsafe.Pointer(&doc[0]))+uintptr(len(doc)))
	// per-string cost: doc with 0 strings
	type N struct{ X, Y int }
	var n N
	d2 := []byte(`{"X":1,"Y":2}`)
	fmt.Println("v2 Unmarshal 2 ints only:", testing.AllocsPerRun(1000, func() { n = N{}; json.Unmarshal(d2, &n) }))
	type O struct{ A string }
	var o O
	d3 := []byte(`{"A":"alpha"}`)
	fmt.Println("v2 Unmarshal 1 string:", testing.AllocsPerRun(1000, func() { o = O{}; json.Unmarshal(d3, &o) }))
	v := S{"alpha", "bravo", "charlie", 1, 2}
	var buf bytes.Buffer
	fmt.Println("v2 MarshalWrite reused buf:", testing.AllocsPerRun(1000, func() { buf.Reset(); json.MarshalWrite(&buf, &v) }))
	fmt.Println("v2 MarshalWrite reused buf (value):", testing.AllocsPerRun(1000, func() { buf.Reset(); json.MarshalWrite(&buf, v) }))
	fmt.Println("v1 Marshal:", testing.AllocsPerRun(1000, func() { jsonv1.Marshal(&v) }))
	fmt.Println("v1 NewEncoder reused buf:", testing.AllocsPerRun(1000, func() { buf.Reset(); jsonv1.NewEncoder(&buf).Encode(&v) }))
}
