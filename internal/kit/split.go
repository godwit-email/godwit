package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// Resizer is first, *size wide, beside second, with a gap of gap between
// them that the user drags (or, focused, moves with the arrows) to set
// *size, as Claude's: hovered, dragged or focused, a GripWidth line of the
// foreground at 20% shows
// down the gap's middle, and hovering that names it (label) beside it. It
// reports whether *size changed, and whether the grip is being dragged; the
// caller clamps *size. onClick, if any, is what a click on the grip does.
// With over, the panels touch (they differ enough to need no gap, as a
// sidebar and the content beside it): the grip, gap wide, rides second's
// edge (its Hairline border) down the middle, reaching as little into either.
// Gap is then GripWidth: first's scroll bar keeps further from its edge than
// the grip reaches, so the grip runs beside it, not over it.
func Resizer(c *ui.Context, size *float32, gap float32, over bool, label string, onClick func(), first, second func()) (changed, dragging bool) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		// ClipX, not Clip: the panes clip a sliding sidebar sideways; clipped below too, they would cut off a panel's ShadowXs.
		ui.Column(c).Width(*size).Shrink(0).ClipX().Children(first)
		grip := ui.Box(c).Width(gap).Shrink(0).Center().Focusable().Role(ui.RoleSplitter).Label(label).Cursor(ui.CursorResizeEW)
		if over {
			grip.Absolute().Left(*size + float32(theme.Hairline)/2 - gap/2).Top(0).Bottom(0)
		}
		move := func(d float32) {
			if d != 0 {
				*size += d
				changed = true
			}
		}
		dx, _, held := grip.Dragged()
		dragging = held
		move(dx)
		if onClick != nil && grip.Clicked() {
			onClick()
		}
		if grip.Shortcut(0, ui.KeyLeft) {
			move(-th.Space(2.5))
		}
		if grip.Shortcut(0, ui.KeyRight) {
			move(th.Space(2.5))
		}
		show := held || grip.Hovered() || grip.FocusVisible()
		grip.Children(func() {
			// The tip's anchor is the line's own span, not the gap's full height,
			// so the tip shows by the line rather than at the gap's top.
			span := ui.Box(c).Width(gap).Height(theme.GripHeight).Center()
			Tooltip(c, span, label)
			span.Children(func() {
				if show {
					ui.Box(c).Width(theme.GripWidth).Height(theme.GripHeight).Radius(t.Radius.Full).Background(t.Foreground.Alpha(0.2))
				}
			})
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).ClipX().Children(second)
	})
	return changed, dragging
}
