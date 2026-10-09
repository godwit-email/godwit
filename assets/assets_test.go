package assets

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLogoParses(t *testing.T) {
	if _, err := ui.ParseSVG(Logo); err != nil {
		t.Fatal(err)
	}
}
