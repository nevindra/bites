package bitetest

import (
	"testing"
	"time"
)

func TestTheWaitIsFiveSecondsUnsetAndTheDurationGivenOtherwise(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 5 * time.Second, "30s": 30 * time.Second, "1m30s": 90 * time.Second, "250ms": 250 * time.Millisecond} {
		got, err := parseWait(v)
		if err != nil || got != want {
			t.Errorf("parseWait(%q) = %v, %v; want %v", v, got, err, want)
		}
	}
}

func TestAWaitNamedInAFailureSaysHowToChangeItWhenItIsTheDefault(t *testing.T) {
	t.Setenv(WaitEnv, "")
	if got := waitText(5 * time.Second); got != "5 s; a slower machine can set BITETEST_WAIT=30s" {
		t.Errorf("default: %q", got)
	}
	t.Setenv(WaitEnv, "30s")
	if got := waitText(30 * time.Second); got != "30 s (BITETEST_WAIT)" {
		t.Errorf("set: %q", got)
	}
}
