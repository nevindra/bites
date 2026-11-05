package proxyproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

func v2(cmd, fam byte, addr []byte, tlv []byte) []byte {
	b := append([]byte(nil), sigV2[:]...)
	b = append(b, 0x20|cmd, fam, 0, 0)
	binary.BigEndian.PutUint16(b[14:], uint16(len(addr)+len(tlv)))
	b = append(b, addr...)
	return append(b, tlv...)
}

func tcp4(src, dst [4]byte, sp, dp uint16) []byte {
	a := append(src[:], dst[:]...)
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(a, sp), dp)
}

func tcp6(src, dst netip.Addr, sp, dp uint16) []byte {
	s, d := src.As16(), dst.As16()
	a := append(s[:], d[:]...)
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(a, sp), dp)
}

func TestAVersion1HeaderNamesTheClientAndItsLengthEndsAtTheCRLF(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"PROXY TCP4 192.0.2.7 198.51.100.1 56324 443\r\nGET / HTTP/1.1\r\n", "192.0.2.7:56324"},
		{"PROXY TCP6 2001:db8::7 2001:db8::1 65535 0\r\n", "[2001:db8::7]:65535"},
		{"PROXY TCP4 0.0.0.0 0.0.0.0 0 0\r\n", "0.0.0.0:0"},
	} {
		h, err := Parse([]byte(tc.in))
		if err != nil || !h.Named || h.Version != 1 || h.Source.String() != tc.want {
			t.Fatalf("%q: %+v %v, want %s", tc.in, h, err, tc.want)
		}
		if line := tc.in[:h.Len]; line[len(line)-2:] != "\r\n" || bytes.Count([]byte(line), []byte("\n")) != 1 {
			t.Fatalf("%q: Len %d does not end at the first CRLF", tc.in, h.Len)
		}
	}
}

func TestAVersion1UnknownKeepsTheRealAddressesAndIgnoresWhatFollowsIt(t *testing.T) {
	for _, in := range []string{"PROXY UNKNOWN\r\n", "PROXY UNKNOWN ffff::1 ffff::2 1 2\r\n", "PROXY UNKNOWN anything at all\r\n"} {
		h, err := Parse([]byte(in))
		if err != nil || h.Named || h.Len != len(in) {
			t.Fatalf("%q: %+v %v", in, h, err)
		}
	}
}

func TestAVersion2ProxyHeaderNamesTheClientAndItsTLVsAreSkipped(t *testing.T) {
	a6 := netip.MustParseAddr("2001:db8::7")
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"TCP4", v2(1, 0x11, tcp4([4]byte{192, 0, 2, 7}, [4]byte{10, 0, 0, 1}, 56324, 443), nil), "192.0.2.7:56324"},
		{"TCP6", v2(1, 0x21, tcp6(a6, netip.IPv6Loopback(), 443, 8080), nil), "[2001:db8::7]:443"},
		{"TCP4 with an AWS endpoint TLV", v2(1, 0x11, tcp4([4]byte{192, 0, 2, 7}, [4]byte{10, 0, 0, 1}, 1, 2), []byte("\xea\x00\x09\x01vpce-0abc")), "192.0.2.7:1"},
	} {
		h, err := Parse(append(tc.in, "GET / HTTP/1.1\r\n"...))
		if err != nil || !h.Named || h.Version != 2 || h.Len != len(tc.in) || h.Source.String() != tc.want {
			t.Fatalf("%s: %+v %v, want %s and Len %d", tc.name, h, err, tc.want, len(tc.in))
		}
	}
}

func TestAVersion2LocalUnspecOrUnixHeaderKeepsTheRealAddresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"LOCAL with no address", v2(0, 0x00, nil, nil)},
		{"LOCAL with a TCP4 block, ignored", v2(0, 0x11, tcp4([4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, 1, 2), nil)},
		{"PROXY with UNSPEC", v2(1, 0x00, nil, nil)},
		{"PROXY with a Unix stream source", v2(1, 0x31, make([]byte, 216), nil)},
	} {
		h, err := Parse(tc.in)
		if err != nil || h.Named || h.Len != len(tc.in) {
			t.Fatalf("%s: %+v %v", tc.name, h, err)
		}
	}
}

func TestEveryPrefixOfAValidHeaderAsksForMoreBytes(t *testing.T) {
	for _, full := range [][]byte{
		[]byte("PROXY TCP4 192.0.2.7 198.51.100.1 56324 443\r\n"),
		v2(1, 0x11, tcp4([4]byte{192, 0, 2, 7}, [4]byte{10, 0, 0, 1}, 1, 2), []byte("\x04\x00\x01x")),
	} {
		for i := range full {
			if _, err := Parse(full[:i]); err != ErrNeedMore {
				t.Fatalf("%q[:%d]: %v, want ErrNeedMore", full, i, err)
			}
		}
	}
}

func TestBytesThatAreNotAPROXYHeaderAreToldApartFromABrokenOne(t *testing.T) {
	for _, in := range []string{"GET / HTTP/1.1\r\n", "\x16\x03\x01\x02\x00", "POST / HTTP/1.1\r\n", "PROXZ", "\r\n\r\nX"} {
		if _, err := Parse([]byte(in)); err != ErrNotProxy {
			t.Fatalf("%q: %v, want ErrNotProxy", in, err)
		}
	}
}

func TestAMalformedHeaderIsRefusedAndNeverGuessedAt(t *testing.T) {
	long := "PROXY TCP4 192.0.2.7 198.51.100.1 56324 443" + string(bytes.Repeat([]byte(" "), 70))
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"v1 with no CRLF in 107 bytes", []byte(long)},
		{"v1 with a bare LF", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 56324 443\n")},
		{"v1 with a CR not before a LF", []byte("PROXY TCP4 192.0.2.7\r198.51.100.1 56324 443\r\n")},
		{"v1 with an unknown protocol", []byte("PROXY UDP4 192.0.2.7 198.51.100.1 1 2\r\n")},
		{"v1 TCP4 with an IPv6 address", []byte("PROXY TCP4 2001:db8::1 198.51.100.1 1 2\r\n")},
		{"v1 TCP6 with an IPv4 address", []byte("PROXY TCP6 192.0.2.7 2001:db8::1 1 2\r\n")},
		{"v1 with a zone", []byte("PROXY TCP6 fe80::1%eth0 fe80::2 1 2\r\n")},
		{"v1 with a port past 65535", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 65536 2\r\n")},
		{"v1 with a leading zero in a port", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 080 2\r\n")},
		{"v1 with a missing field", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 1\r\n")},
		{"v1 with an extra field", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 1 2 3\r\n")},
		{"v1 with two spaces", []byte("PROXY TCP4  192.0.2.7 198.51.100.1 1 2\r\n")},
		{"v1 with a control byte", []byte("PROXY TCP4 192.0.2.7 198.51.100.1 1 2\x00\r\n")},
		{"v2 with version 1", append(append([]byte(nil), sigV2[:]...), 0x11, 0x11, 0, 12)},
		{"v2 with command 2", v2(2, 0x11, tcp4([4]byte{}, [4]byte{}, 1, 2), nil)},
		{"v2 with an unknown family", v2(1, 0x41, nil, nil)},
		{"v2 with an unknown transport", v2(1, 0x13, tcp4([4]byte{}, [4]byte{}, 1, 2), nil)},
		{"v2 PROXY over UDP4", v2(1, 0x12, tcp4([4]byte{}, [4]byte{}, 1, 2), nil)},
		{"v2 TCP4 with a short address block", v2(1, 0x11, make([]byte, 11), nil)},
		{"v2 TCP6 with a short address block", v2(1, 0x21, make([]byte, 35), nil)},
		{"v2 Unix with a short address block", v2(1, 0x31, make([]byte, 100), nil)},
		{"v2 longer than MaxHeader", v2(0, 0x00, nil, make([]byte, MaxHeader))},
	} {
		if _, err := Parse(tc.in); err != ErrMalformed {
			t.Fatalf("%s: %v, want ErrMalformed", tc.name, err)
		}
	}
}

func TestAVersion2HeaderWithAnImpossibleLengthIsRefusedAtItsSixteenthByte(t *testing.T) {
	b := v2(1, 0x21, make([]byte, 10), nil) // says 10 bytes, which cannot hold two IPv6 addresses
	if _, err := Parse(b[:16]); err != ErrMalformed {
		t.Fatalf("%v, want ErrMalformed before the address block arrives", err)
	}
	big := append(append([]byte(nil), sigV2[:]...), 0x21, 0x11, 0xff, 0xff)
	if _, err := Parse(big); err != ErrMalformed {
		t.Fatalf("a length of 65535: %v, want ErrMalformed at once", err)
	}
}

// The corpus the fuzzer started from, kept as a test: each seed must parse to the same answer
// every time and never past its own bytes.
var seeds = [][]byte{
	[]byte("PROXY TCP4 192.0.2.7 198.51.100.1 56324 443\r\n"),
	[]byte("PROXY TCP6 2001:db8::7 2001:db8::1 65535 0\r\nGET / HTTP/1.1\r\n\r\n"),
	[]byte("PROXY UNKNOWN\r\n"),
	[]byte("PROXY TCP4 1.1.1.1 1.1.1.1 1 1\n"),
	[]byte("PROXY "),
	[]byte("GET / HTTP/1.1\r\n"),
	v2(1, 0x11, tcp4([4]byte{192, 0, 2, 7}, [4]byte{10, 0, 0, 1}, 56324, 443), nil),
	v2(1, 0x21, tcp6(netip.MustParseAddr("2001:db8::7"), netip.IPv6Loopback(), 1, 2), []byte("\x01\x00\x02h2")),
	v2(0, 0x00, nil, nil),
	v2(1, 0x31, make([]byte, 216), nil),
	v2(1, 0x12, tcp4([4]byte{}, [4]byte{}, 1, 2), nil),
	sigV2[:7],
}

func TestTheFuzzSeedsParseTheSameWayEveryTime(t *testing.T) {
	for _, s := range seeds {
		checkParse(t, s)
	}
}

func checkParse(t *testing.T, b []byte) {
	h, err := Parse(b)
	h2, err2 := Parse(append([]byte(nil), b...))
	if h != h2 || !errors.Is(err, err2) {
		t.Fatalf("%q: two parses disagree: %+v %v and %+v %v", b, h, err, h2, err2)
	}
	if err != nil {
		if h != (Header{}) {
			t.Fatalf("%q: an error with a header %+v", b, h)
		}
		if err != ErrNeedMore && err != ErrNotProxy && err != ErrMalformed {
			t.Fatalf("%q: unexpected error %v", b, err)
		}
		return
	}
	if h.Len <= 0 || h.Len > len(b) || h.Len > MaxHeader {
		t.Fatalf("%q: Len %d out of range", b, h.Len)
	}
	if h.Named != h.Source.IsValid() {
		t.Fatalf("%q: Named %v but Source %v", b, h.Named, h.Source)
	}
	// What follows the header never changes what the header says.
	h3, err3 := Parse(append(append([]byte(nil), b[:h.Len]...), "trailing bytes"...))
	if err3 != nil || h3 != h {
		t.Fatalf("%q: the header alone parses to %+v %v, with its tail to %+v", b, h3, err3, h)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) { checkParse(t, b) })
}
