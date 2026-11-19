package kit

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// The popover is the design's card: 16 round and 16 in from its edge, 4
// below its anchor, its corner not filled.
func TestPopoverRadiusAndPadding(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Background(theme.Light().Foreground).Padding(40).Children(func() {
			b := Button(c, "Anchor", ButtonOpts{})
			Popover(c, b, &open, func() { ui.Box(c).Width(100).Height(40).Label("body") })
		})
	}), 400, 300)
	tt.Advance(2 * theme.MotionFast)
	anchor, _ := tt.Find("Anchor")
	body, ok := tt.Find("body")
	if !ok {
		t.Fatalf("texts %q", tt.Texts())
	}
	left, top := anchor.X, anchor.Y+anchor.H+theme.PopupOffset
	if dx, dy := body.X-left, body.Y-top; diff8(dx, 16) > 1 || diff8(dy, 16) > 1 {
		t.Errorf("body is %v, %v in from the popover's corner, want 16", dx, dy)
	}
	img := tt.Image()
	if p := img.RGBAAt(int(left)+3, int(top)+3); p.R > 14 {
		t.Errorf("pixel 3 in from the corner %v, want the backdrop: the radius is not 16", p)
	}
	if p := img.RGBAAt(int(left)+20, int(top)+20); p.R < 40 {
		t.Errorf("pixel 20 in from the corner %v, want the popover's fill", p)
	}
}

func diff8(a, b float32) float32 { return max(a-b, b-a) }

// firstFilled is the x of the first pixel on row y that is not the backdrop's.
func firstFilled(img *image.RGBA, y int, backdrop color.RGBA) int {
	for x := 0; x < img.Bounds().Dx(); x++ {
		if img.RGBAAt(x, y) != backdrop {
			return x
		}
	}
	return -1
}

// A dialog's and a palette's panel is 16 round, which its top rows' fill
// reaches in from its side by more than a 12 radius would.
func TestPanelRadius(t *testing.T) {
	for _, name := range []string{"dialog", "palette"} {
		open, query := true, ""
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Box(c).Fill().Background(theme.Of(c).Background)
			if name == "dialog" {
				Dialog(c, &open, "Panel", DialogOpts{Size: DialogSm, Close: "Close"}, nil, nil)
			} else {
				Palette(c, &open, &query, PaletteOpts{Label: "Panel", Placeholder: "Search", Close: "Close"}, nil, nil)
			}
		}), 800, 600)
		tt.SetDark(true)
		tt.Advance(2 * theme.MotionBase)
		var top float32
		if name == "dialog" {
			title, _ := tt.Find("Panel")
			top = title.Y - 16
		} else {
			info, _ := tt.Access("Panel")
			top = info.Bounds.Y
		}
		img := tt.Image()
		backdrop := img.RGBAAt(2, 2)
		side := firstFilled(img, int(top)+24, backdrop)
		round := firstFilled(img, int(top)+2, backdrop)
		if side < 0 || round-side < 6 {
			t.Errorf("%s: the top rows' fill starts %d in from the side, want the 16 radius's 6 or more", name, round-side)
		}
	}
}

// A dialog panel's edge is GlassBorder, not the plain border, over its fill.
func TestDialogEdgeIsGlassBorder(t *testing.T) {
	open := true
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Box(c).Fill().Background(theme.Of(c).Background)
		Dialog(c, &open, "Panel", DialogOpts{Size: DialogSm, Close: "Close"}, nil, nil)
	}), 800, 600)
	tt.SetDark(true)
	tt.Advance(2 * theme.MotionBase)
	title, _ := tt.Find("Panel")
	img := tt.Image()
	y := int(title.Y) + 8
	edge := img.RGBAAt(firstFilled(img, y, img.RGBAAt(2, 2)), y)
	d := theme.Dark()
	base := float32(d.Raised.R)
	want := uint8(base + float32(d.GlassBorder.A)/255*(255-base))
	if diff(edge.R, want) > 3 {
		t.Errorf("edge pixel %v, want GlassBorder over the panel's fill (%d)", edge, want)
	}
}

// A HoverCard shows after 600 ms of hover and at once on keyboard focus, and
// the pointer passes through it.
func TestHoverCardShows(t *testing.T) {
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Padding(20).Gap(4).Children(func() {
			anchor := Button(c, "Anchor", ButtonOpts{})
			HoverCard(c, anchor, theme.SecurityCardWidth, func() { ui.Text(c, "Card text") })
		})
	}), 500, 300)
	r, _ := tt.Find("Anchor")
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(500 * time.Millisecond)
	if tt.HasText("Card text") {
		t.Fatal("the card shows before 600 ms of hover")
	}
	tt.Advance(200 * time.Millisecond)
	if !tt.HasText("Card text") {
		t.Fatalf("no card after 700 ms of hover: %q", tt.Texts())
	}

	tt = ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Padding(20).Gap(4).Children(func() {
			anchor := Button(c, "Anchor", ButtonOpts{})
			HoverCard(c, anchor, theme.SecurityCardWidth, func() { ui.Text(c, "Card text") })
		})
	}), 500, 300)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.HasText("Card text") {
		t.Error("the card does not show on keyboard focus")
	}
}

func TestHoverCardPassesPointerThrough(t *testing.T) {
	n := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Padding(20).Gap(4).Children(func() {
			anchor := Button(c, "Anchor", ButtonOpts{})
			HoverCard(c, anchor, theme.SecurityCardWidth, func() { ui.Text(c, "Card text") })
			if Button(c, "Under", ButtonOpts{}).Clicked() {
				n++
			}
		})
	}), 500, 300)
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if !tt.HasText("Card text") {
		t.Fatal("the card does not show on keyboard focus")
	}
	card, _ := tt.Find("Card text")
	under, _ := tt.Find("Under")
	if card.Y > under.Y+under.H || card.Y+card.H < under.Y {
		t.Fatalf("the card (%+v) does not cover the button (%+v)", card, under)
	}
	tt.ClickAt(under.X+under.W/2, under.Y+under.H/2)
	tt.Frame()
	if n != 1 {
		t.Errorf("the button under the card was clicked %d times, want 1", n)
	}
}
