package main

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"testing"
)

var sink S

func BenchmarkV2Unmarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var s S
		if err := json.Unmarshal(doc, &s); err != nil {
			b.Fatal(err)
		}
		sink = s
	}
}
func BenchmarkV1Unmarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var s S
		if err := jsonv1.Unmarshal(doc, &s); err != nil {
			b.Fatal(err)
		}
		sink = s
	}
}
func BenchmarkV2MarshalWrite(b *testing.B) {
	b.ReportAllocs()
	v := S{"alpha", "bravo", "charlie", 1, 2}
	var buf bytes.Buffer
	for i := 0; i < b.N; i++ {
		buf.Reset()
		json.MarshalWrite(&buf, &v)
	}
}
func BenchmarkV1Marshal(b *testing.B) {
	b.ReportAllocs()
	v := S{"alpha", "bravo", "charlie", 1, 2}
	for i := 0; i < b.N; i++ {
		jsonv1.Marshal(&v)
	}
}

var doc2 = []byte(`{"A":"alpha-----------------------------------------","B":"bravo-----------------------------------------","C":"charlie---------------------------------------","X":1,"Y":2}`)

func BenchmarkV2UnmarshalLongStrings(b *testing.B) {
	b.ReportAllocs()
	var s S // reused, so the struct itself is not counted
	for i := 0; i < b.N; i++ {
		s = S{}
		json.Unmarshal(doc2, &s)
	}
	sink = s
}
func BenchmarkV1UnmarshalLongStrings(b *testing.B) {
	b.ReportAllocs()
	var s S
	for i := 0; i < b.N; i++ {
		s = S{}
		jsonv1.Unmarshal(doc2, &s)
	}
	sink = s
}
func BenchmarkV2UnmarshalShortReusedStruct(b *testing.B) {
	b.ReportAllocs()
	var s S
	for i := 0; i < b.N; i++ {
		s = S{}
		json.Unmarshal(doc, &s)
	}
	sink = s
}
