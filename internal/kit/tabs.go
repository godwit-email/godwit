package kit

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// TabsLook is how a Tabs list is drawn.
type TabsLook uint8

const (
	TabsSegmented TabsLook = iota // a hover-tinted track, the chosen tab raised on the background
	TabsUnderline                 // a hairline under the tabs, the chosen one over a primary line
	TabsPill                      // tabs on no track, the chosen one tinted: a palette's kinds
)

// TabsOpts say how a Tabs list looks.
type TabsOpts struct {
	Look TabsLook
	Fill bool // segmented only: across its row, every tab the same width
}

// TabItem is a tab: its label, and the icon before it if Icon is set.
type TabItem struct{ Icon, Label string }

// TabLabels are the items of tabs that have labels alone.
func TabLabels(labels ...string) []TabItem {
	items := make([]TabItem, len(labels))
	for i, l := range labels {
		items[i].Label = l
	}
	return items
}

// tabPill is the raised background of a segmented tab list's chosen segment:
// one per list, told apart by its sel, so that it slides from the segment
// left to the one chosen. tabLine is an underlined list's line, sliding the
// same way; tabContent is the icon and name of a list's i-th tab where
// every one has an icon, which slide from where they were as the pill does.
type (
	tabPill    struct{ sel *int }
	tabLine    struct{ sel *int }
	tabContent int
)

// Tabs is a tab list choosing *sel among items, one Tab stop whose arrow keys,
// Home and End move the choice; OnChange on it reports a new one. A segmented
// list is a track 32 high at radius-lg, its tabs 28 at radius-md in 500, the
// chosen one on the background under ShadowSm. An underlined one is tabs 32
// high over a hairline, muted at 400, the chosen one in the foreground at 500
// over a 2 px primary line. A pill list's tabs are 28 high in xs, the chosen
// one tinted.
//
// An item's icon is muted line style before its label, the foreground when
// chosen (the fill style in a segmented list). A hugging segmented list whose
// items all have icons shows only the chosen one's label: the others are 28
// wide icon-only tabs named by a tooltip, in the foreground on hover.
//
// The labels of a segmented list are a row of their own over its tabs: an
// absolute element paints over its siblings, and the pill sliding past would
// cover the labels of the tabs before it. The tabs of an icon-only list take
// their new widths at once, so that what slides heads for where it ends.
func Tabs(c *ui.Context, sel *int, o TabsOpts, items []TabItem) ui.Element {
	t, th := theme.Of(c), c.Theme()
	tabs := ui.TabsBase(c, sel, len(items))
	segmented := o.Look == TabsSegmented
	iconOnly := segmented && !o.Fill && !slices.ContainsFunc(items, func(it TabItem) bool { return it.Icon == "" })
	gap := th.Space(1.5)

	// seg sizes a segmented tab and the label drawn over it alike: 28 in the
	// track's Space(0.5), a 32 control, at 500 chosen or not, so that
	// choosing one moves nothing.
	seg := func(e ui.Element, chosen bool) ui.Element {
		e.Height(th.Space(7)).PaddingX(th.Space(2.5)).AlignItems(ui.Center).Justify(ui.Center).FontWeight(500).Gap(gap)
		if o.Fill {
			e.Grow(1).Basis(0)
		}
		if iconOnly { // the icon in 28 unchosen, its name after it chosen
			right := gap
			if chosen {
				right = th.Space(2.5)
			}
			e.Padding(0, right, 0, gap)
		}
		return e
	}
	content := func(it TabItem, chosen, unseen bool) {
		if it.Icon != "" {
			svg := icons.Get(it.Icon)
			if chosen && segmented {
				svg = icons.Fill(it.Icon)
			}
			ui.Icon(c, svg).Size(theme.Icon, theme.Icon).Shrink(0).PassThrough()
		}
		if !iconOnly || chosen {
			label := ui.Text(c, it.Label).SingleLine().Shrink(0).PassThrough()
			if unseen {
				label.Role(ui.RoleNone)
			}
		}
	}

	list := tabs.List
	switch o.Look {
	case TabsSegmented:
		list.Padding(th.Space(0.5)).Gap(th.Space(0.5)).Radius(t.Radius.Lg).Background(tint(t, 1))
		if iconOnly {
			list.Clip().Transition(ui.ElementTransition{Duration: theme.MotionBase, Size: true})
		}
	case TabsUnderline:
		list.Gap(th.Space(1)).BorderColor(t.Border).BorderWidth(0, 0, theme.Hairline, 0)
	case TabsPill:
		list.Gap(th.Space(1))
	}
	return list.Children(func() {
		hovered := make([]bool, len(items))
		for i, it := range items {
			chosen := i == *sel
			tab := tabs.Tab(i).Label(it.Label).AlignItems(ui.Center).Justify(ui.Center).Gap(gap)
			hovered[i] = tab.Hovered()
			switch o.Look {
			case TabsSegmented:
				seg(tab, chosen).Radius(t.Radius.Md) // its corner is the track's less that inset
				if iconOnly && !chosen {
					Tooltip(c, tab, it.Label)
				}
				tab.Children(func() {
					ui.Row(c).Gap(gap).AlignItems(ui.Center).Opacity(0).Children(func() { content(it, chosen, true) }) // sizes the tab unseen
					if chosen {
						pill := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Radius(t.Radius.Md).Background(t.Background).PassThrough()
						Elevate(c, pill, theme.ShadowSm).Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: tabPill{sel}})
					}
				})
			case TabsUnderline:
				// every tab keeps the 2 px border (transparent unless chosen) so choosing one shifts nothing
				tab.Height(th.Space(8)).PaddingX(th.Space(3)).FontSize(t.Text.Sm).TextColor(t.Muted).BorderColor(t.Border.Alpha(0)).BorderWidth(0, 0, theme.TabUnderline, 0)
				if chosen {
					tab.FontWeight(500).TextColor(t.Foreground)
				}
				tab.Children(func() {
					content(it, chosen, false)
					if chosen { // over the tab's own clear border, where the line was
						ui.Box(c).Absolute().Left(0).Right(0).Bottom(-theme.TabUnderline).Height(theme.TabUnderline).Background(t.Primary).PassThrough().
							Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: tabLine{sel}})
					}
				})
			case TabsPill:
				tab.Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).FontSize(t.Text.Xs).TextColor(t.Muted).Transition(colors(theme.MotionFast))
				if chosen {
					tab.Background(t.HoverTint).TextColor(t.Foreground)
				}
				tab.Children(func() { content(it, chosen, false) })
			}
		}
		if !segmented {
			return
		}
		over := ui.Row(c).Absolute().Left(0).Top(0).Bottom(0).Padding(th.Space(0.5)).Gap(th.Space(0.5)).PassThrough()
		if o.Fill {
			over.Right(0)
		}
		over.Children(func() {
			for i, it := range items {
				chosen := i == *sel
				e := seg(ui.Row(c), chosen).TextColor(t.Muted).PassThrough()
				if chosen || iconOnly && hovered[i] {
					e.TextColor(t.Foreground)
				}
				if iconOnly { // keyed by the one chosen, so that a new one appears and slides from where its Match was, clipping the name as it widens
					e.Key([2]int{*sel, i}).Clip().Transition(ui.ElementTransition{Duration: theme.MotionBase, Match: tabContent(i)})
				}
				e.Children(func() { content(it, chosen, false) })
			}
		})
	})
}
