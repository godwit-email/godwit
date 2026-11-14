package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Tabs is a segmented tab list choosing *sel, each tab as wide as its label:
// a hover-gray track 32 high at radius-lg, the tabs 28 at radius-md in sm at
// 500, muted, the chosen one on the background under ShadowSm. NavSwitch is
// the same control with icons.
func Tabs(c *ui.Context, sel *int, labels ...string) ui.Element {
	return segmented(c, sel, false, labels)
}

// TabsFill is Tabs across its row, every tab the same width: a choice
// between whole views, as Gmail's 应用专用密码 | 浏览器登录.
func TabsFill(c *ui.Context, sel *int, labels ...string) ui.Element {
	return segmented(c, sel, true, labels)
}

// tabPill is the raised background of a segmented tab list's chosen segment:
// one per list, told apart by its sel, so that it slides from the segment
// left to the one chosen, as a NavSwitch's pill does. tabLine is an
// underlined list's line, sliding the same way.
type (
	tabPill struct{ sel *int }
	tabLine struct{ sel *int }
)

func segmented(c *ui.Context, sel *int, fill bool, labels []string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(labels))
	// seg sizes a tab and the label drawn over it alike: 28 in the track's
	// Space(0.5), a 32 control, at 500 chosen or not, so that choosing one
	// moves nothing.
	seg := func(e ui.Element) ui.Element {
		e.Height(th.Space(7)).PaddingX(th.Space(2.5)).AlignItems(ui.Center).Justify(ui.Center).FontWeight(500)
		if fill {
			e.Grow(1).Basis(0)
		}
		return e
	}
	track := func(e ui.Element) ui.Element { return e.Padding(th.Space(0.5)).Gap(th.Space(0.5)) }
	return track(tabs.List).Radius(t.Radius.Lg).Background(t.Hover).Children(func() {
		for i, l := range labels {
			seg(tabs.Tab(i)).Label(l).Radius(t.Radius.Md).Children(func() { // its corner is the track's less that inset
				ui.Text(c, l).Role(ui.RoleNone).Opacity(0) // sizes the tab unseen
				if i == *sel {
					pill := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(t.Radius.Md).Background(t.Background).PassThrough()
					Elevate(c, pill, theme.ShadowSm).Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: tabPill{sel}})
				}
			})
		}
		// The labels are a row of their own over the tabs: an absolute element
		// paints over its siblings, and the pill sliding past would cover the
		// labels of the tabs before it.
		over := track(ui.Row(c).Absolute().Left(0).Top(0).Bottom(0)).PassThrough()
		if fill {
			over.Right(0)
		}
		over.Children(func() {
			for i, l := range labels {
				e := seg(ui.Row(c)).TextColor(t.Muted).PassThrough()
				if i == *sel {
					e.TextColor(t.Foreground)
				}
				e.Children(func() { ui.Text(c, l).PassThrough() })
			}
		})
	})
}

// TabsUnderline is Tabs as a row over a hairline, in sm: muted, the chosen
// tab in the foreground at 500 over a primary line that slides to it.
func TabsUnderline(c *ui.Context, sel *int, labels ...string) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(labels))
	return tabs.List.Gap(th.Space(1)).BorderColor(t.Border).BorderWidth(0, 0, theme.Hairline, 0).Children(func() {
		for i, l := range labels {
			// every tab keeps the 2 px border (transparent unless chosen) so choosing one shifts nothing
			tab := tabs.Tab(i).Height(th.Space(8)).PaddingX(th.Space(3)).FontSize(t.Text.Sm).TextColor(t.Muted).BorderColor(t.Border.Alpha(0)).BorderWidth(0, 0, theme.TabUnderline, 0)
			if i == *sel {
				tab.FontWeight(500).TextColor(t.Foreground)
			}
			tab.Children(func() {
				ui.Text(c, l)
				if i == *sel { // over the tab's own clear border, where the line was
					ui.Box(c).Absolute().Left(0).Right(0).Bottom(-theme.TabUnderline).Height(theme.TabUnderline).Background(t.Primary).PassThrough().
						Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: tabLine{sel}})
				}
			})
		}
	})
}

// ToggleGroup groups the buttons fn builds: one Tab stop, the arrow keys move between them.
func ToggleGroup(c *ui.Context, label string, fn func()) ui.Element {
	return ui.ToggleGroup(c, fn).Label(label).Gap(c.Theme().Space(0.5))
}
