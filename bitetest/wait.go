package bitetest

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// WaitEnv is the environment variable that sets how long bitetest waits in real time for the
// server before it fails the test: a Go duration such as 30s. Unset, the wait is five seconds. It
// is for a machine slower than the one the tests were written on (a shared CI runner), where an
// answer that took two seconds on a laptop takes ten; the server's own timeouts run on the App's
// clock and are not changed by it. A value that is not a positive duration fails every test that
// waits, naming the value.
const WaitEnv = "BITETEST_WAIT"

const defaultWait = 5 * time.Second

// waitFromEnv is read once per test process: the environment of a test binary does not change
// while it runs, and every wait of every test should be the same one.
var waitFromEnv = sync.OnceValues(func() (time.Duration, error) { return parseWait(os.Getenv(WaitEnv)) })

// parseWait reads the value of WaitEnv: empty is the default, anything else must be a positive
// Go duration.
func parseWait(v string) (time.Duration, error) {
	if v == "" {
		return defaultWait, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bitetest: %s=%q is not a positive Go duration; write one such as 30s, or unset it for five seconds", WaitEnv, v)
	}
	return d, nil
}

// wait is how long a real-time wait of bitetest lasts, failing tb when WaitEnv is not a positive
// duration.
func wait(tb testing.TB) time.Duration {
	tb.Helper()
	d, err := waitFromEnv()
	if err != nil {
		tb.Fatal(err)
	}
	return d
}

// waitText names a wait in a failure: "5 s; a slower machine can set BITETEST_WAIT=30s" for the
// default, so the message says what to change, and "30 s (BITETEST_WAIT)" for a wait it set.
func waitText(d time.Duration) string {
	n := d.String()
	if d%time.Second == 0 {
		n = strconv.FormatInt(int64(d/time.Second), 10) + " s"
	}
	if os.Getenv(WaitEnv) == "" {
		return n + "; a slower machine can set " + WaitEnv + "=30s"
	}
	return n + " (" + WaitEnv + ")"
}
