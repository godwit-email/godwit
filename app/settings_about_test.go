package app

import (
	"runtime"
	"testing"
)

// About stacks the name, the selectable version with its platform, and the
// update button, centered, over one card of links; the button checks for
// updates, or offers the newer version once one is known.
func TestSettingsAboutStates(t *testing.T) {
	for _, latest := range []string{"", "9.0.0"} {
		t.Run("latest="+latest, func(t *testing.T) {
			h, _ := withAccount(t)
			h.plat.mu.Lock()
			h.plat.latest = latest
			h.plat.mu.Unlock()
			h.openSettings("masking")
			h.openSettings("about")
			button, call := "检查更新", "CheckForUpdates"
			if latest != "" {
				button = "升级到 " + latest
			}
			version := "版本 " + Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
			h.seeExactly(version)
			h.seeExactly(button)
			name, _ := h.tt.Find("Godwit")
			ver, _ := h.tt.Find(version)
			btn, _ := h.tt.Find(button)
			license, _ := h.tt.Find("开源协议")
			if !(name.Y < ver.Y && ver.Y < btn.Y && btn.Y < license.Y) {
				t.Errorf("latest %q: name %v, version %v, button %v, license %v are not stacked in order", latest, name.Y, ver.Y, btn.Y, license.Y)
			}
			if d := (btn.X + btn.W/2) - (ver.X + ver.W/2); d < -1 || d > 1 {
				t.Errorf("latest %q: the button is %v off the version's center", latest, d)
			}
			if _, ok := h.tt.Find("链接"); ok {
				t.Errorf("latest %q: the links have a section title of their own", latest)
			}
			first, _ := h.tt.Find("开源协议")
			for _, row := range []string{"源代码", "官网", "更新日志", "反馈问题"} {
				r, ok := h.tt.Find(row)
				if !ok || r.X != first.X {
					t.Errorf("latest %q: row %q (found %v) is not in the license row's card", latest, row, ok)
				}
			}
			h.click(button)
			h.called(call)
		})
	}
}
