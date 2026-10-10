package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// quiet holds, for each theme, its copy whose accent is the ring's gray.
var quiet sync.Map // *ui.Theme → *ui.Theme

// Split is ui.Split, first beside second with a divider the user drags (or
// moves with the arrows once focused) to set *size: hovered or dragged, the
// divider turns the ring's gray, as shadcn's sidebar rail does, not the
// accent's near-black. mygo's Split draws its divider from the theme it
// starts with, so it starts with that copy and gives first and second back
// the frame's own.
func Split(c *ui.Context, size *float32, first, second func()) ui.Element {
	base := c.Theme()
	q, ok := quiet.Load(base)
	if !ok {
		th := *base
		th.Accent = theme.Of(c).Ring
		q, _ = quiet.LoadOrStore(base, &th)
	}
	c.SetTheme(q.(*ui.Theme))
	defer c.SetTheme(base)
	return ui.Split(c, size, func() { c.SetTheme(base); first() }, func() { c.SetTheme(base); second() })
}
