package web

import (
	"bytes"
	"testing"
)

func TestRemixIconsEmbedded(t *testing.T) {
	for name, path := range RemixIcons {
		b, ok := RemixIcon(name)
		if !ok || !bytes.Contains(b, []byte("<svg")) {
			t.Errorf("%s (%s) not embedded", name, path)
		}
	}
	if len(RemixIcons) != 125 {
		t.Errorf("%d icons, want the 125 internal/icons has today", len(RemixIcons))
	}
}
