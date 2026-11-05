package bites_test

import (
	"os/exec"
	"strings"
	"testing"
)

// ADR 0006 and ADR 0019: the core never imports net/http, and nothing outside the standard library.
func TestTheCoreNeverImportsNetHTTPOrAnythingOutsideTheStandardLibrary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	if err != nil {
		t.Skip(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if !strings.HasPrefix(p, "github.com/nevindra/bites") {
			t.Errorf("the core depends on %s", p)
		}
	}
	std, _ := exec.Command("go", "list", "-deps", ".").Output()
	for _, p := range strings.Fields(string(std)) {
		if p == "net/http" {
			t.Error("the core depends on net/http")
		}
	}
}
