package main

import (
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"fmt"
	"runtime"
)

type S struct {
	A, B, C string
	X, Y    int
}

func measure(name string, f func(doc []byte, s *S)) {
	const N = 10000
	docs := make([][]byte, N) // distinct values per document, built before measuring
	for i := range docs {
		docs[i] = []byte(fmt.Sprintf(`{"A":"alpha-%08d","B":"bravo-%08d","C":"charlie-%08d","X":1,"Y":2}`, i, i*7, i*13))
	}
	keep := make([]S, N)
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	for i := range keep {
		f(docs[i], &keep[i])
	}
	runtime.ReadMemStats(&m1)
	fmt.Printf("%s distinct strings: mallocs/op=%.2f bytes/op=%.1f\n", name, float64(m1.Mallocs-m0.Mallocs)/N, float64(m1.TotalAlloc-m0.TotalAlloc)/N)
	fmt.Println("  sample:", keep[5])
}

func main() {
	measure("v2", func(d []byte, s *S) { json.Unmarshal(d, s) })
	measure("v1", func(d []byte, s *S) { jsonv1.Unmarshal(d, s) })
}
