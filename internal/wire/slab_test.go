package wire

import (
	"errors"
	"testing"
	"unsafe"
)

func TestSlabCopyReturnsAStringOverTheSlabAndUsesItUp(t *testing.T) {
	s := NewSlab(8)
	a := s.Copy([]byte("abc"))
	b := s.Copy([]byte("defgh"))
	if a != "abc" || b != "defgh" {
		t.Fatalf("got %q %q", a, b)
	}
	if s.Remaining() != 0 || s.Fallbacks() != 0 {
		t.Errorf("remaining %d fallbacks %d", s.Remaining(), s.Fallbacks())
	}
	if s.Copy(nil) != "" || s.Fallbacks() != 0 {
		t.Error("an empty copy must be free")
	}
}

func TestSlabStringsAreNeverChangedByLaterCopies(t *testing.T) {
	s := NewSlab(64)
	first := s.Copy([]byte("first"))
	src := []byte("second")
	second := s.Copy(src)
	src[0] = 'X' // the caller's buffer is not the slab
	if first != "first" || second != "second" {
		t.Errorf("got %q %q", first, second)
	}
}

func TestSlabFallsBackToAnAllocationWhenFullAndCountsIt(t *testing.T) {
	s := NewSlab(4)
	if got := s.Copy([]byte("toolong")); got != "toolong" {
		t.Errorf("got %q", got)
	}
	if s.Fallbacks() != 1 || s.Remaining() != 4 {
		t.Errorf("fallbacks %d remaining %d", s.Fallbacks(), s.Remaining())
	}
	if got, err := s.Unescape([]byte("a%20bcdef"), false); err != nil || got != "a bcdef" {
		t.Errorf("got %q, %v", got, err)
	}
	if s.Fallbacks() != 2 {
		t.Errorf("fallbacks %d", s.Fallbacks())
	}
	if got := s.Copy([]byte("ok")); got != "ok" || s.Fallbacks() != 2 {
		t.Error("a string that fits must still use the slab after a fallback")
	}
}

func TestUnescapeDecodesPercentEscapesAndPlusOnRequest(t *testing.T) {
	cases := []struct {
		in   string
		plus bool
		want string
	}{
		{"plain", false, "plain"},
		{"a%20b", false, "a b"},
		{"a+b", true, "a b"},
		{"a+b", false, "a+b"},
		{"%2B+%2b", true, "+ +"},
		{"%C3%A9", false, "é"},
		{"%00%7F", false, "\x00\x7f"},
		{"%25", false, "%"},
		{"", true, ""},
		{"+", true, " "},
	}
	for _, c := range cases {
		s := NewSlab(32)
		got, err := s.Unescape([]byte(c.in), c.plus)
		if err != nil || got != c.want {
			t.Errorf("Unescape(%q, %v) = %q, %v; want %q", c.in, c.plus, got, err, c.want)
		}
		if s.Fallbacks() != 0 {
			t.Errorf("Unescape(%q) fell back", c.in)
		}
	}
}

func TestUnescapeRefusesInvalidEscapesAndReservesNothing(t *testing.T) {
	for _, in := range []string{"%", "%2", "%zz", "a%2", "%G0", "ok%0", "%%41"} {
		s := NewSlab(16)
		if _, err := s.Unescape([]byte(in), true); !errors.Is(err, ErrBadEscape) {
			t.Errorf("Unescape(%q) err = %v", in, err)
		}
		if s.Remaining() != 16 {
			t.Errorf("Unescape(%q) used the slab on error", in)
		}
	}
}

func TestSlabCopyAndUnescapeAllocateNothingWhileTheyFit(t *testing.T) {
	s := NewSlab(1 << 20)
	b := []byte("hello%20world+x")
	if n := testing.AllocsPerRun(100, func() {
		_ = s.Copy(b)
		_, _ = s.Unescape(b, true)
	}); n != 0 {
		t.Errorf("allocs = %v", n)
	}
}

func TestReserveHandsOutRoomInTheSlabAndAllocatesWhenItRunsOut(t *testing.T) {
	var s Slab
	s.Init(8)
	b := s.Reserve(5)
	copy(b, "hello")
	if len(b) != 5 || cap(b) != 5 || s.Remaining() != 3 || s.Fallbacks() != 0 {
		t.Fatalf("len=%d cap=%d remaining=%d", len(b), cap(b), s.Remaining())
	}
	if c := s.Reserve(4); len(c) != 4 || s.Fallbacks() != 1 || s.Remaining() != 3 {
		t.Fatalf("an oversized Reserve must allocate and count a fallback")
	}
	if s.Reserve(0) != nil {
		t.Fatal("Reserve(0) is nil")
	}
}

func TestAnAliasedCopyAnswersWithASubstringOfTheCopyAndOtherBytesAreCopiedAsBefore(t *testing.T) {
	src := []byte("GET /a?x=1 HTTP/1.1\r\nHost: h\r\n\r\n")
	s := NewSlab(8)
	dup := string(src)
	s.Alias(src, dup)
	got := s.Copy(src[21:29])
	if got != "Host: h\r" {
		t.Fatalf("%q", got)
	}
	if unsafe.StringData(got) != unsafe.StringData(dup[21:]) {
		t.Fatal("the string is a copy, not a substring of the aliased copy")
	}
	if s.Remaining() != 8 {
		t.Fatalf("an aliased copy used %d bytes of the slab", 8-s.Remaining())
	}
	other := []byte("elsewhere")
	if s.Copy(other[:4]) != "else" || s.Remaining() != 4 {
		t.Fatalf("bytes outside the head are copied into the slab: %d left", s.Remaining())
	}
	other[0] = 'X'
	if s.Copy(other[:1]) != "X" {
		t.Fatal("a later copy reads stale bytes")
	}
}
