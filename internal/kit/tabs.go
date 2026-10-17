package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Tabs is a segmented tab list choosing *sel.
func Tabs(c *ui.Context, sel *int, labels ...string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(labels))
	return tabs.List.Padding(th.Space(0.5)).Radius(t.Radius.Lg).Background(t.Hover).Gap(th.Space(0.5)).Children(func() {
		for i, l := range labels {
			// ponytail: backgrounds fade; a sliding indicator needs the tabs' rects from the last frame
			tab := tabs.Tab(i).Height(th.Space(6.5)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).FontSize(t.Text.Sm).FontWeight(400).TextColor(t.Muted).Transition(colors(theme.MotionFast))
			if i == *sel {
				Elevate(c, tab.Background(t.Background).TextColor(t.Foreground), theme.ShadowSm)
			}
			tab.Children(func() { ui.Text(c, l) })
		}
	})
}

// TabsUnderline is Tabs as a row over a hairline, in Base type: Muted, the
// chosen tab Foreground and 600 over a Primary bottom border.
func TabsUnderline(c *ui.Context, sel *int, labels ...string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(labels))
	return tabs.List.Gap(th.Space(1)).BorderColor(t.Border).BorderWidth(0, 0, theme.DeviceHairline, 0).Children(func() {
		for i, l := range labels {
			// every tab keeps the 2 px border (transparent unless chosen) so choosing one shifts nothing
			tab := tabs.Tab(i).Height(th.Space(8)).PaddingX(th.Space(3)).FontSize(t.Text.Base).TextColor(t.Muted).BorderColor(t.Border.Alpha(0)).BorderWidth(0, 0, theme.TabUnderline, 0).Transition(colors(theme.MotionFast))
			if i == *sel {
				tab.FontWeight(600).TextColor(t.Foreground).BorderColor(t.Primary)
			}
			tab.Children(func() { ui.Text(c, l) })
		}
	})
}

// ToggleGroup groups the buttons fn builds: one Tab stop, the arrow keys move between them.
func ToggleGroup(c *ui.Context, label string, fn func()) ui.Element {
	return ui.ToggleGroup(c, fn).Label(label).Gap(c.Theme().Space(0.5))
}
