package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// quiet holds, for each theme, its copy whose accent is DividerHover.
var quiet sync.Map // *ui.Theme → *ui.Theme

// Split is ui.Split, first beside second with a divider the user drags (or
// moves with the arrows once focused) to set *size: hovered or dragged, the
// divider turns DividerHover, a step darker than the border, not the
// accent's near-black. mygo's Split draws its divider from the theme it
// starts with, so it starts with that copy and gives first and second back
// the frame's own. c.SetTheme also resets the root's background to the
// theme's: a view that sets the root's background sets it after.
func Split(c *ui.Context, size *float32, first, second func()) ui.Element {
	base := c.Theme()
	q, ok := quiet.Load(base)
	if !ok {
		th := *base
		th.Accent = theme.Of(c).DividerHover
		q, _ = quiet.LoadOrStore(base, &th)
	}
	c.SetTheme(q.(*ui.Theme))
	defer c.SetTheme(base)
	return ui.Split(c, size, func() { c.SetTheme(base); first() }, func() { c.SetTheme(base); second() })
}
