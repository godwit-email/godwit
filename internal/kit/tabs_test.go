package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

var tabsLooks = map[string]TabsOpts{
	"segmented": {},
	"fill":      {Fill: true},
	"underline": {Look: TabsUnderline},
	"pill":      {Look: TabsPill},
}

// TestTabsKeyboard: a list of any look is one Tab stop, and the arrow keys,
// Home and End move its choice.
func TestTabsKeyboard(t *testing.T) {
	for name, o := range tabsLooks {
		t.Run(name, func(t *testing.T) {
			sel := 0
			tt := ui.NewTester(themed(func(c *ui.Context) {
				ui.Column(c).Width(300).Children(func() {
					Button(c, "before", ButtonOpts{})
					Tabs(c, &sel, o, TabLabels("One", "Two", "Three"))
					Button(c, "after", ButtonOpts{})
				})
			}), 400, 200)
			tt.Key(0, ui.KeyTab)
			tt.Key(0, ui.KeyTab)
			if !tt.Focused("One") {
				t.Fatal("the second Tab stop is not the list")
			}
			for _, k := range []struct {
				key  ui.Key
				want int
			}{{ui.KeyRight, 1}, {ui.KeyEnd, 2}, {ui.KeyRight, 0}, {ui.KeyLeft, 2}, {ui.KeyHome, 0}} {
				tt.Key(0, k.key)
				tt.Frame()
				if sel != k.want {
					t.Fatalf("after key %v: sel = %d, want %d", k.key, sel, k.want)
				}
			}
			tt.Key(0, ui.KeyTab)
			if !tt.Focused("after") || sel != 0 {
				t.Errorf("Tab left the list for after = %v, sel = %d: want one stop for all three tabs", tt.Focused("after"), sel)
			}
		})
	}
}

// TestTabsDisabled: a disabled list takes neither clicks nor keys.
func TestTabsDisabled(t *testing.T) {
	sel := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		Tabs(c, &sel, TabsOpts{}, TabLabels("One", "Two")).Disabled(true)
	}), 300, 100)
	_ = tt.Click("Two")
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyRight)
	if sel != 0 {
		t.Errorf("sel = %d, want a disabled list to stay", sel)
	}
}

// TestTabsPillLook: a pill list's chosen tab is the hover tint at radius-md
// on no track, in the foreground; the others bare, muted.
func TestTabsPillLook(t *testing.T) {
	for _, sel := range []int{0, 1} {
		var tk theme.Tokens
		tt := ui.NewTester(themed(func(c *ui.Context) {
			tk = theme.Of(c)
			ui.Column(c).Fill().Background(tk.Background).Children(func() {
				Tabs(c, &sel, TabsOpts{Look: TabsPill}, TabLabels("One", "Two"))
			})
		}), 300, 100)
		tt.Frame()
		tt.Advance(2 * theme.MotionBase)
		img := tt.Image()
		// the first tab spans the origin, 28 high; its corner is cut away, its padding filled while chosen
		if corner := img.RGBAAt(0, 0); !near(corner, tk.Background) {
			t.Errorf("sel=%d: the first tab's corner pixel is %v: its corners are square", sel, corner)
		}
		want := tk.Background
		if sel == 0 {
			want = tk.HoverTint.Over(tk.Background)
		}
		if got := img.RGBAAt(4, 14); !near(got, want) {
			t.Errorf("sel=%d: the first tab's fill is %v, want %v", sel, got, want)
		}
		one, _ := tt.Find("One")
		two, _ := tt.Find("Two")
		dark := func(r ui.Rect) uint8 { // the darkest pixel of a label: its text's color
			lowest := uint8(255)
			for y := int(r.Y); y < int(r.Y+r.H); y++ {
				for x := int(r.X); x < int(r.X+r.W); x++ {
					lowest = min(lowest, img.RGBAAt(x, y).R)
				}
			}
			return lowest
		}
		chosen, other := dark(one), dark(two)
		if sel == 1 {
			chosen, other = other, chosen
		}
		if chosen >= other {
			t.Errorf("sel=%d: text darkness chosen %d, other %d: want the chosen one the foreground, the other muted", sel, chosen, other)
		}
	}
}

// TestTabsIconOnly: a hugging segmented list whose items all have icons shows
// only the chosen one's name: the others are 28 wide, and choosing one works.
func TestTabsIconOnly(t *testing.T) {
	sel := 0
	var width float32
	tt := ui.NewTester(themed(func(c *ui.Context) {
		width = c.Theme().Space(7)
		Tabs(c, &sel, TabsOpts{}, []TabItem{{Icon: "mail", Label: "One"}, {Icon: "sparkles", Label: "Two"}, {Icon: "contacts", Label: "Three"}})
	}), 300, 100)
	if r, ok := tt.Find("Two"); !ok || r.W != width {
		t.Errorf("unchosen tab %+v, want %v wide", r, width)
	}
	if err := tt.Click("Three"); err != nil || sel != 2 {
		t.Fatalf("click: err=%v sel=%d, want 2", err, sel)
	}
	tt.Frame()
	tt.Advance(2 * theme.MotionBase)
	if r, ok := tt.Find("Two"); !ok || r.W != width {
		t.Errorf("unchosen tab %+v after the choice, want %v wide", r, width)
	}
}

// TestSidebarGroup: a group's title folds its rows, with its count at the
// row's end and its chevron after the title.
func TestSidebarGroup(t *testing.T) {
	open := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Width(240).Children(func() {
			SidebarItem(c, SidebarItemOpts{Muted: true, Open: &open, Count: "3",
				Title: func() { ui.Text(c, "Accounts") },
				Group: func() { SidebarItem(c, SidebarItemOpts{}).Children(func() { ui.Text(c, "Inbox") }) }})
		})
	}), 300, 200)
	if tt.HasText("Inbox") {
		t.Fatal("rows shown while folded")
	}
	title, ok1 := tt.Find("Accounts")
	count, ok2 := tt.Find("3")
	if !ok1 || !ok2 {
		t.Fatalf("title or count not found: %q", tt.Texts())
	}
	// 240 wide, padding 8: the count ends at 232, far from the title
	if end := count.X + count.W; end < 230 || end > 233 || count.X < title.X+title.W+40 {
		t.Errorf("count at %+v after the title %+v, want it at the row's end", count, title)
	}
	if err := tt.Click("Accounts"); err != nil {
		t.Fatal(err)
	}
	if !open || !tt.HasText("Inbox") {
		t.Fatalf("open=%v rows %q after a click, want them shown", open, tt.Texts())
	}
	_ = tt.Click("Accounts")
	tt.Advance(2 * theme.MotionBase)
	if open || tt.HasText("Inbox") {
		t.Errorf("open=%v rows %q after a second click, want them folded", open, tt.Texts())
	}
}

// TestChevronTurns: a Chevron points right shut and down open, turning over
// MotionBase to where it is, and starts there without a turn.
func TestChevronTurns(t *testing.T) {
	open := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Width(100).Children(func() { Chevron(c, open, theme.IconXs).Label("chev") })
	}), 200, 100)
	// span is the chevron's ink as wide and as high: a right arrow is higher
	// than wide, a down arrow wider than high.
	span := func(tt *ui.Tester) (wide, high int) {
		r, ok := tt.Find("chev")
		if !ok {
			t.Fatal("no chevron")
		}
		img := tt.Image()
		x0, x1, y0, y1 := int(r.X+r.W), 0, int(r.Y+r.H), 0
		for y := int(r.Y); y < int(r.Y+r.H); y++ {
			for x := int(r.X); x < int(r.X+r.W); x++ {
				if img.RGBAAt(x, y).R < 220 {
					x0, x1, y0, y1 = min(x0, x), max(x1, x), min(y0, y), max(y1, y)
				}
			}
		}
		return x1 - x0, y1 - y0
	}
	if wide, high := span(tt); wide >= high {
		t.Errorf("shut: ink %d wide, %d high, want a right arrow", wide, high)
	}
	open = true
	tt.Frame()
	tt.Advance(2 * theme.MotionBase)
	if wide, high := span(tt); wide <= high {
		t.Errorf("open: ink %d wide, %d high, want a down arrow", wide, high)
	}
	opened := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Width(100).Children(func() { Chevron(c, true, theme.IconXs).Label("chev") })
	}), 200, 100)
	if wide, high := span(opened); wide <= high {
		t.Errorf("built open: ink %d wide, %d high, want it down at once", wide, high)
	}
}
