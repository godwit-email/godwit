package kit

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

func near(got color.RGBA, want ui.Color) bool {
	w := want.SRGB()
	return max(diff(got.R, w.R), diff(got.G, w.G), diff(got.B, w.B)) <= 3
}

// TestKitLabelChipLooks: by hand a filled tint, by the model only an edge, in
// both appearances; both 20 high and the same size.
func TestKitLabelChipLooks(t *testing.T) {
	for _, dark := range []bool{false, true} {
		tt := ui.NewTester(themed(func(c *ui.Context) {
			t := theme.Of(c)
			ui.Column(c).Fill().Padding(20).Gap(20).AlignItems(ui.Start).Background(t.Background).Children(func() {
				ui.Box(c).Label("manual").Children(func() { LabelChip(c, LabelChipOpts{Text: "验证码", Icon: "shield-keyhole", Color: t.Label("red")}) })
				ui.Box(c).Label("auto").Children(func() {
					LabelChip(c, LabelChipOpts{Text: "验证码", Icon: "shield-keyhole", Color: t.Label("red"), Auto: true})
				})
			})
		}), 300, 200)
		tt.SetDark(dark)
		tt.Frame()
		tok := theme.Light()
		if dark {
			tok = theme.Dark()
		}
		red := tok.Label("red")
		m, ok1 := tt.Find("manual")
		a, ok2 := tt.Find("auto")
		if !ok1 || !ok2 {
			t.Fatalf("chips not found in %q", tt.Texts())
		}
		if m.H != 20 || a.H != 20 || m.W != a.W {
			t.Errorf("dark=%v: manual %vx%v, auto %vx%v, want both 20 high and as wide", dark, m.W, m.H, a.W, a.H)
		}
		img := tt.Image()
		mid := func(r ui.Rect) int { return int(r.Y + r.H/2) }
		if got := img.RGBAAt(int(m.X)+3, mid(m)); !near(got, red.Fill) {
			t.Errorf("dark=%v: manual inside %v, want the fill %v", dark, got, red.Fill.SRGB())
		}
		if got := img.RGBAAt(int(a.X)+3, mid(a)); !near(got, tok.Background) {
			t.Errorf("dark=%v: auto inside %v, want the background", dark, got)
		}
		if got := img.RGBAAt(int(a.X+a.W/2), int(a.Y)); !near(got, red.Edge) {
			t.Errorf("dark=%v: auto edge %v, want %v", dark, got, red.Edge.SRGB())
		}
		if got := img.RGBAAt(int(m.X+m.W/2), int(m.Y)); !near(got, red.Fill) {
			t.Errorf("dark=%v: manual edge %v, want the fill", dark, got)
		}
	}
}

func TestKitLabelChipRemove(t *testing.T) {
	removed, clicked := 0, 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		LabelChip(c, LabelChipOpts{Text: "验证码", Icon: "shield-keyhole", Color: theme.Of(c).Label("red"), Auto: true, Tip: "由模型标记",
			OnClick: func() { clicked++ }, OnRemove: func() { removed++ }, RemoveLabel: "去掉标签「验证码」"})
	}), 300, 200)
	r, _ := tt.Find("验证码")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(900 * time.Millisecond)
	if !tt.HasText("由模型标记") {
		t.Errorf("no tip on hover: %q", tt.Texts())
	}
	if err := tt.Click("去掉标签「验证码」"); err != nil {
		t.Fatal(err)
	}
	if removed != 1 || clicked != 0 {
		t.Errorf("removed %d clicked %d, want 1 and 0", removed, clicked)
	}
}

func TestKitLabelChipClick(t *testing.T) {
	clicked := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Color: theme.Of(c).Label("amber"), OnClick: func() { clicked++ }})
	}), 300, 200)
	if err := tt.Click("账单"); err != nil {
		t.Fatal(err)
	}
	if clicked != 1 {
		t.Errorf("clicked %d, want 1", clicked)
	}
	plain := ui.NewTester(themed(func(c *ui.Context) {
		LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Color: theme.Of(c).Label("amber")})
	}), 300, 200)
	plain.Key(0, ui.KeyTab)
	plain.Frame()
	if plain.Focused("账单") {
		t.Error("a chip with no OnClick takes focus")
	}
}

func TestKitLabelChipDisplay(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).AlignItems(ui.Start).Children(func() {
			ui.Box(c).Label("both").Children(func() {
				LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Display: "both", Color: theme.Of(c).Label("amber")})
			})
			ui.Box(c).Label("icon").Children(func() {
				LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Display: "icon", Color: theme.Of(c).Label("amber"), Tip: "由模型标记"})
			})
			ui.Box(c).Label("text").Children(func() {
				LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Display: "text", Color: theme.Of(c).Label("amber")})
			})
		})
	}), 300, 200)
	tt.Frame()
	both, _ := tt.Find("both")
	icon, _ := tt.Find("icon")
	text, _ := tt.Find("text")
	if icon.W >= both.W {
		t.Errorf("icon width %v >= both width %v", icon.W, both.W)
	}
	if text.W >= both.W {
		t.Errorf("text width %v >= both width %v", text.W, both.W)
	}
	// Hovering over icon-only chip shows the name plus tip
	tt.Move(icon.X+icon.W/2, icon.Y+icon.H/2)
	tt.Advance(900 * time.Millisecond)
	if !tt.HasText("账单 · 由模型标记") {
		t.Errorf("expected combined tooltip on icon-only chip, got %q", tt.Texts())
	}
}

// TestKitLabelChipIconOnly: an icon-only chip is named for screen readers and
// tips its name on hover; one with no icon falls back to its name.
func TestKitLabelChipIconOnly(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).AlignItems(ui.Start).Gap(20).Children(func() {
			LabelChip(c, LabelChipOpts{Text: "账单", Icon: "bill", Display: "icon", Color: theme.Of(c).Label("amber")})
			LabelChip(c, LabelChipOpts{Text: "旅行", Display: "icon", Color: theme.Of(c).Label("amber")})
		})
	}), 300, 200)
	tt.Frame()
	if !tt.HasText("旅行") {
		t.Errorf("a chip with no icon hides its name: %q", tt.Texts())
	}
	r, ok := tt.Find("账单")
	if !ok {
		t.Fatalf("an icon-only chip has no accessible name: %q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(900 * time.Millisecond)
	// the chip's label and the tip
	if n := strings.Count(strings.Join(tt.Texts(), "\n"), "账单"); n != 2 {
		t.Errorf("no name tip on hover: %q", tt.Texts())
	}
}
