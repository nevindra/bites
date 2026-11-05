package wire

import (
	"reflect"
	"testing"
)

func TestEachQuerySplitsPairsOnAmpersandAndTheFirstEquals(t *testing.T) {
	cases := []struct {
		raw  string
		want [][2]string
	}{
		{"", nil},
		{"a=1&b=2", [][2]string{{"a", "1"}, {"b", "2"}}},
		{"a=1&&b=2", [][2]string{{"a", "1"}, {"b", "2"}}},
		{"&&", nil},
		{"a=1&", [][2]string{{"a", "1"}}},
		{"&a=1", [][2]string{{"a", "1"}}},
		{"flag", [][2]string{{"flag", ""}}},
		{"flag&x=1", [][2]string{{"flag", ""}, {"x", "1"}}},
		{"a=", [][2]string{{"a", ""}}},
		{"=x", [][2]string{{"", "x"}}},
		{"=", [][2]string{{"", ""}}},
		{"a=b=c", [][2]string{{"a", "b=c"}}},
		{"a%20b=c%26d", [][2]string{{"a%20b", "c%26d"}}},
		{"t=a&t=b", [][2]string{{"t", "a"}, {"t", "b"}}},
	}
	for _, c := range cases {
		var got [][2]string
		EachQuery([]byte(c.raw), func(k, v []byte) bool {
			got = append(got, [2]string{string(k), string(v)})
			return true
		})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("EachQuery(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestEachQueryStopsWhenTheCallbackReturnsFalse(t *testing.T) {
	n := 0
	EachQuery([]byte("a=1&b=2&c=3"), func(k, v []byte) bool { n++; return n < 2 })
	if n != 2 {
		t.Errorf("calls = %d", n)
	}
}

func TestEachQueryAllocatesNothing(t *testing.T) {
	raw := []byte("page=2&tag=a&tag=b&flag&&q=x%20y")
	total := 0
	if n := testing.AllocsPerRun(100, func() {
		EachQuery(raw, func(k, v []byte) bool { total += len(k) + len(v); return true })
	}); n != 0 {
		t.Errorf("allocs = %v", n)
	}
}
