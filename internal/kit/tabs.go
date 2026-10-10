package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Tabs is a segmented tab list choosing *sel.
func Tabs(c *ui.Context, sel *int, labels ...string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(labels))
	return tabs.List.Padding(th.Space(0.75)).Radius(t.Radius.Md).Background(t.Hover).Gap(th.Space(0.5)).Children(func() {
		for i, l := range labels {
			// ponytail: backgrounds fade; a sliding indicator needs the tabs' rects from the last frame
			tab := tabs.Tab(i).Height(th.Space(6.5)).PaddingX(th.Space(2.5)).Radius(t.Radius.Sm).FontWeight(500).TextColor(t.Muted).Transition(colors(theme.MotionFast))
			if i == *sel {
				tab.Background(t.Background).TextColor(t.Foreground).Shadow(0, theme.ShadowSm.Y, theme.ShadowSm.Blur, 0, ink(t).Alpha(0.1))
			}
			tab.Children(func() { ui.Text(c, l) })
		}
	})
}

// ToggleGroup groups the buttons fn builds: one Tab stop, the arrow keys move between them.
func ToggleGroup(c *ui.Context, label string, fn func()) ui.Element {
	return ui.ToggleGroup(c, fn).Label(label).Gap(c.Theme().Space(0.5))
}
