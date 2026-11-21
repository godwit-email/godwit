package kit

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// A ChoiceCard is drawn the one way a choice is: the background in the
// border's outline, chosen in the hover tint and a primary outline, on a
// card in the card gray with no outline.
func TestChoiceCardStates(t *testing.T) {
	for _, dark := range []bool{false, true} {
		tok := theme.Light()
		if dark {
			tok = theme.Dark()
		}
		tt := ui.NewTester(func(c *ui.Context) {
			c.DisableAnimations()
			theme.Apply(c, tok)
			ui.Column(c).Fill().Padding(20).Gap(20).Background(tok.Window).Children(func() {
				for _, k := range []struct {
					label string
					o     ChoiceCardOpts
				}{{"plain", ChoiceCardOpts{}}, {"chosen", ChoiceCardOpts{Selected: true}}, {"card", ChoiceCardOpts{Look: ChoiceOnCard}}} {
					ChoiceCard(c, Pressable(c, PressableOpts{Bare: true}).Label(k.label).Height(40), k.o, func() { ui.Text(c, k.label).Role(ui.RoleNone) })
				}
			})
		}, 300, 300)
		for _, k := range []struct {
			label      string
			edge, fill ui.Color
		}{{"plain", tok.Border, tok.Background}, {"chosen", tok.Primary, tint(tok, 1)}, {"card", tok.Card, tok.Card}} {
			k.fill = k.fill.Over(tok.Window)
			k.edge = k.edge.Over(k.fill)
			r, ok := tt.Find(k.label)
			if !ok {
				t.Fatalf("no %q card in %q", k.label, tt.Texts())
			}
			img := tt.Image()
			if got := img.RGBAAt(int(r.X), int(r.Y+r.H/2)); !near(got, k.edge) {
				t.Errorf("dark=%v %s: edge %v, want %v", dark, k.label, got, k.edge.SRGB())
			}
			if got := img.RGBAAt(int(r.X+r.W-8), int(r.Y+r.H/2)); !near(got, k.fill) {
				t.Errorf("dark=%v %s: fill %v, want %v", dark, k.label, got, k.fill.SRGB())
			}
		}
	}
}

// A card that acts on a click does so, and a dashed one is not chosen by it.
func TestChoiceCardClicks(t *testing.T) {
	clicks := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			ChoiceCard(c, Pressable(c, PressableOpts{Bare: true, OnClick: func() { clicks++ }}).Label("tile"), ChoiceCardOpts{Look: ChoiceDashed}, func() { ui.Text(c, "Tile") })
		})
	}), 200, 100)
	if err := tt.Click("tile"); err != nil || clicks != 1 {
		t.Fatalf("click: %v, clicked %d times", err, clicks)
	}
}

// A radio card is chosen by a click anywhere on it, its help included, and
// its dot follows the choice.
func TestChoiceCardRadioClicks(t *testing.T) {
	bg := false
	tt := ui.NewTester(themed(func(c *ui.Context) {
		c.DisableAnimations()
		ui.Column(c).Fill().Padding(10).Gap(8).Children(func() {
			for _, k := range []struct {
				on          bool
				label, help string
			}{{false, "Window", "Only while the window is open."}, {true, "Background", "Keeps running after the window closes."}} {
				ChoiceCard(c, ui.RadioBase(c.Key(k.label), &bg, k.on).Label(k.label+" card"), ChoiceCardOpts{Selected: bg == k.on, Look: ChoiceOnCard, Radio: true}, func() { ChoiceText(c, k.label, k.help) })
			}
		})
	}), 320, 200)
	r, ok := tt.Find("Keeps running after the window closes.")
	if !ok {
		t.Fatalf("no help text: %q", tt.Texts())
	}
	tt.Move(r.X+r.W-4, r.Y+r.H/2)
	tt.Press(r.X+r.W-4, r.Y+r.H/2)
	tt.Release(r.X+r.W-4, r.Y+r.H/2)
	tt.Frame()
	if !bg {
		t.Fatal("a click on the help did not choose the card")
	}
	if card, _ := tt.Find("Background card"); !near(tt.Image().RGBAAt(int(card.X), int(card.Y+card.H/2)), theme.Light().Primary) {
		t.Error("the chosen card has no primary outline")
	}
}
