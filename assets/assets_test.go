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

// The app icon's layer is the logo: actool cannot compile it through a link, so
// AppIcon.icon keeps a copy, which must not drift.
func TestAppIconLogo(t *testing.T) {
	b, err := os.ReadFile("AppIcon.icon/Assets/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(Logo)) {
		t.Error("AppIcon.icon/Assets/logo.svg differs from logo.svg; copy logo.svg over it and run scripts/appicon.sh")
	}
}
