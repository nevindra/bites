package wire

import "testing"

func TestUnescapeInPlaceDecodesPercentAndPlusInTheBytesItIsGiven(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"a+b", "a b"},
		{"ada%40example.com", "ada@example.com"},
		{"%41%42+%43", "AB C"},
		{"%e2%82%ac", "€"},
	} {
		b := []byte(tc.in)
		got, err := UnescapeInPlace(b, true)
		if err != nil || got != tc.want {
			t.Errorf("%q: %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	got, _ := UnescapeInPlace([]byte("a+b"), false)
	if got != "a+b" {
		t.Errorf("a plus is kept when plus is off: %q", got)
	}
}

func TestUnescapeInPlaceRefusesABadEscapeAndLeavesTheBytesAlone(t *testing.T) {
	for _, in := range []string{"%", "%4", "%zz", "ab%4", "a%4g"} {
		b := []byte(in)
		if _, err := UnescapeInPlace(b, true); err != ErrBadEscape || string(b) != in {
			t.Errorf("%q: err %v, bytes now %q", in, err, b)
		}
	}
}
