package bodies

import (
	"encoding/json/jsontext"
	"testing"
)

var vals3 = []string{"pending", "active", "closed"}
var vals8 = []string{"pending", "active", "closed", "paused", "failed", "queued", "retired", "unknown"}
var vals32 = func() []string {
	v := make([]string, 32)
	for i := range v {
		v[i] = "value_number_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	v[31] = "pending"
	return v
}()
var sink []byte

func scan(vals []string, s string) bool {
	for _, v := range vals {
		if s == v {
			return true
		}
	}
	return false
}

// encode of a string field: the existing cost.
func BenchmarkEncodeString(b *testing.B) {
	buf := make([]byte, 0, 64)
	s := "pending"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf, _ = jsontext.AppendQuote(buf[:0], s)
	}
	sink = buf
}

func benchScan(b *testing.B, vals []string, s string) {
	buf := make([]byte, 0, 64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !scan(vals, s) {
			b.Fatal("x")
		}
		buf, _ = jsontext.AppendQuote(buf[:0], s)
	}
	sink = buf
}
func BenchmarkEncodeEnumScan3(b *testing.B)  { benchScan(b, vals3, "closed") }
func BenchmarkEncodeEnumScan8(b *testing.B)  { benchScan(b, vals8, "unknown") }
func BenchmarkEncodeEnumScan32(b *testing.B) { benchScan(b, vals32, "pending") }

func BenchmarkEncodeEnumMap32(b *testing.B) {
	m := map[string]struct{}{}
	for _, v := range vals32 {
		m[v] = struct{}{}
	}
	buf := make([]byte, 0, 64)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok := m["pending"]; !ok {
			b.Fatal("x")
		}
		buf, _ = jsontext.AppendQuote(buf[:0], "pending")
	}
	sink = buf
}
