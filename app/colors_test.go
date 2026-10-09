package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoColorLiterals: colors in app/ and internal/kit/ come from the palette
// through theme tokens, never from literals.
func TestNoColorLiterals(t *testing.T) {
	for _, dir := range []string{".", "../internal/kit"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s: %v", dir, err)
		}
		for _, f := range files {
			if filepath.Base(f) == "colors_test.go" {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, lit := range []string{"ui.Hex(", "ui.RGB(", "ui.RGBA(", "ui.Color{"} {
				if strings.Contains(string(b), lit) {
					t.Errorf("%s uses %s", f, lit)
				}
			}
		}
	}
}
