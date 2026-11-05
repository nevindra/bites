package engine

import (
	"bytes"
	"testing"
)

func TestReserveKeepsTheBodyAndSetsTheScratchsOwnBufferAside(t *testing.T) {
	s := &Server{}
	s.pool.New = func() any { return &scratch{buf: make([]byte, bufSize)} }
	sc := s.getScratch()
	small := make([]byte, 0, 4096)
	sc.resp.body = append(small, "head of the body"...)
	sc.resp.Reserve(200 << 10)
	if got := string(sc.resp.body); got != "head of the body" {
		t.Fatalf("the body after Reserve is %q, want the bytes it had", got)
	}
	if c := cap(sc.resp.body); c < 200<<10+16 {
		t.Fatalf("cap after Reserve(200 KiB) is %d, want room for it", c)
	}
	if &sc.resp.spare[:1][0] != &small[:1][0] {
		t.Fatal("the scratch's own buffer was not set aside")
	}
	s.putScratch(sc)
	if &sc.resp.body[:1][0] != &small[:1][0] || sc.resp.spare != nil {
		t.Fatal("putScratch did not give the scratch its own buffer back")
	}
}

func TestReserveWithRoomAlreadyThereChangesNothing(t *testing.T) {
	var r Response
	r.body = make([]byte, 3, 4096)
	before := &r.body[0]
	r.Reserve(100)
	if &r.body[0] != before || r.spare != nil {
		t.Fatal("Reserve moved a body that had room")
	}
}

func TestALargeBufferGoesBackToItsSizeClassAndComesBackEmpty(t *testing.T) {
	for _, n := range []int{1, 64 << 10, 128 << 10, 128<<10 + 1, 3 << 20, 8 << 20} {
		b := takeBig(n)
		if len(b) != 0 || cap(b) < n {
			t.Fatalf("takeBig(%d): len %d cap %d", n, len(b), cap(b))
		}
		b = append(b, bytes.Repeat([]byte("x"), n)...)
		putBig(b)
	}
	if b := takeBig(9 << 20); cap(b) != 9<<20 {
		t.Fatalf("a buffer above the largest class is made to its size, got cap %d", cap(b))
	}
	putBig(make([]byte, 0, 1000)) // below the smallest class: left to the collector, no panic
}
