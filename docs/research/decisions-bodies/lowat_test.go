package bodies

import (
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func setInt(c net.Conn, level, opt, v int) error {
	rc, _ := c.(*net.TCPConn).SyscallConn()
	var e error
	rc.Control(func(fd uintptr) { e = syscall.SetsockoptInt(int(fd), level, opt, v) })
	return e
}

// The same never-reading client with TCP_NOTSENT_LOWAT (25) and with SO_SNDBUF set on the
// server side: how much does the kernel accept before a flush blocks?
func TestNeverReadingWithLowat(t *testing.T) {
	cases := []struct {
		name string
		set  func(net.Conn) error
	}{
		{"default", func(net.Conn) error { return nil }},
		{"NOTSENT_LOWAT=16KiB", func(c net.Conn) error { return setInt(c, syscall.IPPROTO_TCP, 25, 16<<10) }},
		{"SO_SNDBUF=64KiB", func(c net.Conn) error { return setInt(c, syscall.SOL_SOCKET, syscall.SO_SNDBUF, 64<<10) }},
	}
	msg := []byte(strings.Repeat("x", 100))
	for _, cs := range cases {
		s, c := pair(t)
		if err := cs.set(s); err != nil {
			t.Fatal(err)
		}
		sent := 0
		for {
			s.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
			if _, err := s.Write(msg); err != nil {
				break
			}
			sent++
		}
		t.Logf("%-22s never-reading client: %6d events = %5d KiB accepted before a flush blocked", cs.name, sent, sent*100/1024)
		s.Close()
		c.Close()
	}
}

func BenchmarkFlushLowat(b *testing.B) {
	for _, low := range []bool{false, true} {
		name := "default"
		if low {
			name = "lowat16K"
		}
		b.Run(name, func(b *testing.B) {
			s, c := pair(b)
			defer s.Close()
			defer c.Close()
			if low {
				setInt(s, syscall.IPPROTO_TCP, 25, 16<<10)
			}
			go func() {
				buf := make([]byte, 256<<10)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
			msg := make([]byte, 64<<10)
			b.SetBytes(int64(len(msg)))
			for i := 0; i < b.N; i++ {
				if _, err := s.Write(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
