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
func Resizer(c *ui.Context, size *float32, gap float32, label string, onClick func(), first, second func()) (changed, dragging bool) {
	return resizer(c, size, gap, false, label, onClick, first, second)
}

// ResizerOver is Resizer with no layout gap between first and second: first
// reaches second's edge (so a sidebar's scroll bar sits right along the
// content panel's border) and a grip of width gripWidth rides that edge.
func ResizerOver(c *ui.Context, size *float32, gripWidth float32, label string, onClick func(), first, second func()) (changed, dragging bool) {
	return resizer(c, size, gripWidth, true, label, onClick, first, second)
}

func resizer(c *ui.Context, size *float32, gap float32, over bool, label string, onClick func(), first, second func()) (changed, dragging bool) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		// ClipX, not Clip: the panes clip a sliding sidebar sideways; clipped below too, they would cut off a panel's ShadowXs.
		ui.Column(c).Width(*size).Shrink(0).ClipX().Children(first)
		buildGrip := func() {
			grip := ui.Box(c).Width(gap).Shrink(0).Center().Focusable().Role(ui.RoleSplitter).Label(label).Cursor(ui.CursorResizeEW)
			if over {
				grip.Absolute().Left(*size - gap/2).Top(0).Bottom(0)
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
		}
		if !over {
			buildGrip()
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).ClipX().Children(second)
		if over {
			buildGrip()
		}
	})
	return changed, dragging
}
