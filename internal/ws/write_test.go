package ws

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestAServerFrameIsUnmaskedWithTheMinimalLengthEncoding(t *testing.T) {
	cases := []struct {
		n    int
		want []byte
	}{
		{0, []byte{0x81, 0}},
		{125, []byte{0x81, 125}},
		{126, []byte{0x81, 126, 0, 126}},
		{65535, []byte{0x81, 126, 0xff, 0xff}},
		{65536, []byte{0x81, 127, 0, 0, 0, 0, 0, 1, 0, 0}},
	}
	for _, c := range cases {
		got := AppendFrame(nil, OpText, bytes.Repeat([]byte("a"), c.n))
		if !bytes.HasPrefix(got, c.want) || len(got) != len(c.want)+c.n || len(got)-c.n != HeaderLen(c.n) {
			t.Errorf("length %d: header % x, HeaderLen %d", c.n, got[:len(c.want)], HeaderLen(c.n))
		}
		if got[1]&0x80 != 0 {
			t.Errorf("length %d: a server frame is masked", c.n)
		}
	}
}

func TestFragmentsOfAServerMessageCarryContinuationAndFin(t *testing.T) {
	got := cat(AppendFragment(nil, false, OpText, []byte("ab")), AppendFragment(nil, false, OpContinuation, []byte("c")), AppendFragment(nil, true, OpContinuation, nil))
	want := []byte{0x01, 2, 'a', 'b', 0x00, 1, 'c', 0x80, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x", got)
	}
}

func TestAControlFrameOver125BytesIsNotWritten(t *testing.T) {
	if _, err := AppendPing(nil, make([]byte, 126)); err != ErrControlTooLong {
		t.Errorf("ping: %v", err)
	}
	if _, err := AppendPong(nil, make([]byte, 126)); err != ErrControlTooLong {
		t.Errorf("pong: %v", err)
	}
	if _, err := AppendClose(nil, 1000, strings.Repeat("a", 124)); err != ErrControlTooLong {
		t.Errorf("close: %v", err)
	}
	if got, err := AppendClose(nil, 1000, strings.Repeat("a", 123)); err != nil || len(got) != 2+125 {
		t.Errorf("close of 125 bytes: %v %d", err, len(got))
	}
}

func TestACloseFrameCarriesItsStatusAndReason(t *testing.T) {
	got, err := AppendClose(nil, 1009, "too big")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 0x88 || int(got[1]) != 2+len("too big") || binary.BigEndian.Uint16(got[2:]) != 1009 || string(got[4:]) != "too big" {
		t.Fatalf("got % x", got)
	}
}

func TestAStatusThatMayNotBeSentIsNotWritten(t *testing.T) {
	for _, code := range []uint16{0, 999, 1004, 1005, 1006, 1015, 2000, 5000} {
		if _, err := AppendClose(nil, code, ""); err != ErrCloseStatus {
			t.Errorf("code %d: %v", code, err)
		}
	}
	for _, code := range []uint16{1000, 1001, 1008, 1009, 1011, 1012, 1013, 3000, 4999} {
		if _, err := AppendClose(nil, code, ""); err != nil {
			t.Errorf("code %d: %v", code, err)
		}
	}
}

func TestWhatTheClientWriterWritesTheReaderReadsBack(t *testing.T) {
	for _, n := range []int{0, 1, 125, 126, 1000, 65535, 65536, 70000} {
		lim := DefaultLimits()
		lim.MaxMessage = 1 << 20
		payload := bytes.Repeat([]byte("xyz"), n/3+1)[:n]
		msgs, _ := messages(feed(newReader(lim), AppendClientFrame(nil, true, OpBinary, payload, testKey), 1000, t0))
		if len(msgs) != 1 || msgs[0] != string(payload) {
			t.Errorf("length %d did not round trip", n)
		}
	}
}

func TestWritingAFrameIntoASizedBufferAllocatesNothing(t *testing.T) {
	buf := make([]byte, 0, 4096)
	payload := bytes.Repeat([]byte("a"), 1000)
	reason := "going away"
	check := func(name string, f func()) {
		if a := testing.AllocsPerRun(200, f); a != 0 {
			t.Errorf("%s: %v allocations", name, a)
		}
	}
	check("frame", func() { _ = AppendFrame(buf[:0], OpText, payload) })
	check("fragment", func() { _ = AppendFragment(buf[:0], false, OpBinary, payload) })
	check("ping", func() { _, _ = AppendPing(buf[:0], payload[:100]) })
	check("close", func() { _, _ = AppendClose(buf[:0], 1001, reason) })
	check("accept", func() { _ = AppendAccept(buf[:0], []byte("dGhlIHNhbXBsZSBub25jZQ==")) })
}
