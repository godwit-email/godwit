package backend

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoCoreDependency: the backend packages depend on neither core nor its
// store, directly or not (ADR 0037), so they can be split out of the repo.
func TestNoCoreDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-test", "./...").Output()
	if err != nil {
		t.Fatal(err)
	}
	const core = "github.com/godwit-email/godwit/core"
	checked := false
	for _, p := range strings.Fields(string(out)) {
		checked = checked || p == "github.com/godwit-email/godwit/backend/backendtest"
		if p == core || strings.HasPrefix(p, core+"/") {
			t.Errorf("a backend package depends on %s", p)
		}
	}
	if !checked {
		t.Fatal("go list did not reach backendtest")
	}
}
