package bitetest_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// runWithWait runs one test of this package in a new process with BITETEST_WAIT set to wait,
// since the wait is read once per process, and returns its verbose output and how long it took.
func runWithWait(t *testing.T, name, wait string) (string, time.Duration) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), "BITETEST_WAIT_CHILD=1", bitetest.WaitEnv+"="+wait)
	start := time.Now()
	out, _ := cmd.CombinedOutput()
	return string(out), time.Since(start)
}

// TestChildSlowAnswer asks a route that answers after 300 ms of real time.
func TestChildSlowAnswer(t *testing.T) {
	if os.Getenv("BITETEST_WAIT_CHILD") == "" {
		t.Skip("run by its parent")
	}
	app := bites.New()
	app.Get("/slow", func(context.Context, none) (none, error) {
		time.Sleep(300 * time.Millisecond)
		return none{}, nil
	})
	srv := bitetest.Start(t, app)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/slow"}); r.Status != 200 {
		t.Fatalf("status %d", r.Status)
	}
}

func TestAWaitShorterThanTheAnswerFailsTheTestNamingTheWaitAndItsVariable(t *testing.T) {
	out, took := runWithWait(t, "TestChildSlowAnswer", "50ms")
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "no response in 50ms (BITETEST_WAIT)") {
		t.Fatalf("a 50ms wait did not fail the 300 ms answer naming the wait:\n%s", out)
	}
	if took > 4*time.Second {
		t.Fatalf("the child took %v: the wait was not the one set", took)
	}
}

func TestAWaitLongerThanTheAnswerLetsTheTestPass(t *testing.T) {
	out, _ := runWithWait(t, "TestChildSlowAnswer", "10s")
	if !strings.Contains(out, "--- PASS: TestChildSlowAnswer") {
		t.Fatalf("a 10s wait did not let the 300 ms answer through:\n%s", out)
	}
}

func TestAWaitThatIsNoDurationFailsTheTestNamingTheValue(t *testing.T) {
	for _, v := range []string{"thirty", "0", "-1s"} {
		out, _ := runWithWait(t, "TestChildSlowAnswer", v)
		if !strings.Contains(out, "FAIL") || !strings.Contains(out, `BITETEST_WAIT="`+v+`" is not a positive Go duration`) {
			t.Fatalf("BITETEST_WAIT=%s did not fail the test naming the value:\n%s", v, out)
		}
	}
}
