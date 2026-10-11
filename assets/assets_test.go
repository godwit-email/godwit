package assets

import (
	"bytes"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLogoParses(t *testing.T) {
	if _, err := ui.ParseSVG(Logo); err != nil {
		t.Fatal(err)
	}
}

// AppIcon.icon keeps a copy of the logo (actool follows no link), which
// scripts/icons.sh refreshes.
func TestAppIconLogo(t *testing.T) {
	b, err := os.ReadFile("AppIcon.icon/Assets/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(Logo)) {
		t.Error("AppIcon.icon/Assets/logo.svg differs from logo.svg; run scripts/icons.sh")
	}
}
