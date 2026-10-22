package kit

import (
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// soft holds, for each theme, its copy whose accent is the sidebar's selected gray.
var soft sync.Map // *ui.Theme → *ui.Theme

// List is ui.List with the selected rows in the sidebar's selected gray and
// the foreground, not mygo's accent (the primary, near-black). mygo styles a
// row from the theme current when it builds it, so the rows' content gets the
// frame's own theme back. A reordered list that chooses no rows keeps the
// accent: the line showing where dragged rows go is drawn in it.
func List(c *ui.Context, s *ui.ListState, n int, row func(i int)) ui.Element {
	if s.Reorder != nil && s.Selected == nil && s.Selection == nil {
		return ui.List(c, s, n, row)
	}
	base := c.Theme()
	q, ok := soft.Load(base)
	if !ok {
		t, th := theme.Of(c), *base
		th.Accent, th.AccentText = t.SidebarSelected, t.Foreground
		q, _ = soft.LoadOrStore(base, &th)
	}
	c.SetTheme(q.(*ui.Theme))
	defer c.SetTheme(base)
	return ui.List(c, s, n, func(i int) {
		c.SetTheme(base)
		row(i)
		c.SetTheme(q.(*ui.Theme))
	})
}
