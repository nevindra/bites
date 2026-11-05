package bodies

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

// TCP_USER_TIMEOUT (18) instead of a per-flush deadline: no write deadline at all, the
// kernel gives up when buffered data stays untransmitted or unacknowledged for the timeout.
// The reader drains 1 byte per readEvery (0 means never reads).
func TestUserTimeout(t *testing.T) {
	for _, readEvery := range []time.Duration{0, 50 * time.Millisecond, 400 * time.Millisecond} {
		s, c := pair(t)
		if err := setInt(s, syscall.IPPROTO_TCP, 18, 1000); err != nil {
			t.Fatal(err)
		}
		setInt(s, syscall.IPPROTO_TCP, 25, 16<<10)
		stop := make(chan struct{})
		if readEvery > 0 {
			go func() {
				b := make([]byte, 1)
				for {
					select {
					case <-stop:
						return
					case <-time.After(readEvery):
						c.Read(b)
					}
				}
			}()
		}
		msg := []byte(strings.Repeat("x", 4096))
		t0 := time.Now()
		n := 0
		var err error
		done := make(chan struct{})
		go func() {
			for err == nil {
				_, err = s.Write(msg)
				n++
			}
			close(done)
		}()
		select {
		case <-done:
			t.Logf("reader 1 B / %-6v: write failed after %v (%d KiB accepted): %v", readEvery, time.Since(t0).Round(10*time.Millisecond), n*4, err)
		case <-time.After(8 * time.Second):
			t.Logf("reader 1 B / %-6v: still blocked or writing after 8 s (%d KiB accepted): USER_TIMEOUT did NOT close it", readEvery, n*4)
			s.Close()
			<-done
		}
		close(stop)
		s.Close()
		c.Close()
	}
}
