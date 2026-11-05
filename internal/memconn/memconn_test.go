package memconn

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/nevindra/bites/internal/clock"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func pair(t *testing.T, clk clock.Clock) (client, server net.Conn) {
	t.Helper()
	l := Listen(clk)
	t.Cleanup(func() { l.Close() })
	c, err := l.Dial(Addr{"10.0.0.7:1234"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestBytesWrittenWithoutAReaderAreBufferedAndDelivered(t *testing.T) {
	c, s := pair(t, clock.Real())
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := s.Read(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("read %q %v", buf[:n], err)
	}
}

func TestTheServerEndSeesTheAddressDialWasGiven(t *testing.T) {
	_, s := pair(t, clock.Real())
	if s.RemoteAddr().String() != "10.0.0.7:1234" {
		t.Fatalf("remote %v", s.RemoteAddr())
	}
}

func TestAWriterBlocksWhenTheBufferIsFullAndResumesWhenTheReaderDrains(t *testing.T) {
	c, s := pair(t, clock.Real())
	done := make(chan error)
	go func() { _, err := c.Write(make([]byte, BufferSize+10)); done <- err }()
	select {
	case <-done:
		t.Fatal("write finished with nobody reading")
	case <-time.After(50 * time.Millisecond):
	}
	n, _ := io.ReadFull(s, make([]byte, BufferSize))
	if n != BufferSize {
		t.Fatalf("read %d", n)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClosingOneEndGivesTheOtherEOFAfterItDrainsWhatWasBuffered(t *testing.T) {
	c, s := pair(t, clock.Real())
	c.Write([]byte("bye"))
	c.Close()
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "bye" {
		t.Fatalf("got %q %v", b, err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Fatal("write to a closed peer must fail")
	}
}

func TestCloseWriteIsAHalfCloseTheOtherEndCanStillAnswer(t *testing.T) {
	c, s := pair(t, clock.Real())
	c.Write([]byte("req"))
	c.(*Conn).CloseWrite()
	b, _ := io.ReadAll(s)
	if string(b) != "req" {
		t.Fatalf("got %q", b)
	}
	if _, err := s.Write([]byte("resp")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	n, _ := c.Read(buf)
	if string(buf[:n]) != "resp" {
		t.Fatalf("got %q", buf[:n])
	}
}

func TestPeerClosedIsClosedWhenTheOtherEndCloses(t *testing.T) {
	c, s := pair(t, clock.Real())
	ch := s.(*Conn).PeerClosed()
	select {
	case <-ch:
		t.Fatal("closed early")
	default:
	}
	c.Close()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("PeerClosed never closed")
	}
}

func TestAReadDeadlineOnAFakeClockExpiresOnAdvanceWithATimeoutError(t *testing.T) {
	clk := clock.NewFake(epoch)
	_, s := pair(t, clk)
	s.SetReadDeadline(clk.Now().Add(5 * time.Second))
	errc := make(chan error)
	go func() { _, err := s.Read(make([]byte, 1)); errc <- err }()
	for clk.Pending() != 1 {
		time.Sleep(time.Millisecond)
	}
	clk.Advance(5 * time.Second)
	err := <-errc
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestAWriteDeadlineExpiresAWriterBlockedOnAFullBuffer(t *testing.T) {
	clk := clock.NewFake(epoch)
	c, _ := pair(t, clk)
	c.SetWriteDeadline(clk.Now().Add(time.Second))
	errc := make(chan error)
	go func() { _, err := c.Write(make([]byte, BufferSize*2)); errc <- err }()
	for clk.Pending() != 1 {
		time.Sleep(time.Millisecond)
	}
	clk.Advance(time.Second)
	if err := <-errc; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestResettingADeadlineMovesItAndClearingItStopsTheTimer(t *testing.T) {
	clk := clock.NewFake(epoch)
	_, s := pair(t, clk)
	s.SetReadDeadline(clk.Now().Add(time.Second))
	s.SetReadDeadline(clk.Now().Add(10 * time.Second))
	clk.Advance(5 * time.Second)
	s.SetReadDeadline(time.Time{})
	if clk.Pending() != 0 {
		t.Fatalf("pending %d", clk.Pending())
	}
	clk.Advance(time.Hour)
	s.(*Conn).in.data = append(s.(*Conn).in.data, 'x')
	if _, err := s.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
}

func TestAReadBlockedWhenThisEndClosesReturnsErrClosed(t *testing.T) {
	_, s := pair(t, clock.Real())
	errc := make(chan error)
	go func() { _, err := s.Read(make([]byte, 1)); errc <- err }()
	time.Sleep(10 * time.Millisecond)
	s.Close()
	if err := <-errc; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestAClosedListenerRefusesAcceptAndDial(t *testing.T) {
	l := Listen(clock.Real())
	l.Close()
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept: %v", err)
	}
	if _, err := l.Dial(nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("dial: %v", err)
	}
}

func TestSteadyStateReadsWritesAndDeadlinesDoNotAllocate(t *testing.T) {
	clk := clock.NewFake(epoch)
	c, s := pair(t, clk)
	msg, buf := []byte("0123456789"), make([]byte, 16)
	allocs := testing.AllocsPerRun(200, func() {
		s.SetReadDeadline(clk.Now().Add(time.Second))
		c.Write(msg)
		s.Read(buf)
	})
	if allocs != 0 {
		t.Fatalf("allocs = %v", allocs)
	}
}

func TestSettledIsFalseUntilTheServerEndBlocksInReadWithNothingToReadAndTrueOnceItCloses(t *testing.T) {
	l := Listen(clock.Real())
	defer l.Close()
	c, err := l.Dial(Addr{"10.0.0.7:1234"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := l.Accept()
	if l.Settled(20 * time.Millisecond) {
		t.Fatal("settled before the server end ever read")
	}
	c.Write([]byte("x"))
	buf := make([]byte, 8)
	s.Read(buf)
	if l.Settled(20 * time.Millisecond) {
		t.Fatal("settled while the server end was between reads")
	}
	go s.Read(buf)
	if !l.Settled(5 * time.Second) {
		t.Fatal("not settled with the server end blocked in Read")
	}
	s.Close()
	if !l.Settled(5 * time.Second) {
		t.Fatal("not settled after the server end closed")
	}
}

func TestADialRacingCloseEitherFailsOrGivesAConnectionThatEnds(t *testing.T) {
	for range 20000 {
		l := Listen(nil)
		go l.Close()
		c, err := l.Dial(nil)
		if err != nil {
			continue
		}
		// Nobody accepts, so the listener's Close must close the server end and the client
		// reads EOF; before the fix a Dial that lost the race waited here forever.
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("a connection queued as the listener closed: the read returned %v, not io.EOF", err)
		}
		c.Close()
	}
}
