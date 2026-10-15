package kit

import (
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

func TestInputFocusFades(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		for _, group := range []bool{false, true} {
			t.Run(map[bool]string{false: "input", true: "group"}[group]+map[bool]string{false: "/motion", true: "/reduced"}[reduce], func(t *testing.T) {
				var value string
				fading := false
				tt := ui.NewTester(themed(func(c *ui.Context) {
					ui.Column(c).Fill().Padding(10).Background(theme.Of(c).Background).Children(func() {
						var frame ui.Element
						if group {
							frame = InputGroup(c, InputOpts{Flat: true}).Label("frame").Children(func() {
								Input(c, &value, InputOpts{Bare: true}).Grow(1).Label("input")
							})
						} else {
							frame = Input(c, &value, InputOpts{Flat: true}).Label("input")
						}
						// Click may settle the whole fade before returning on a slow runner.
						fading = fading || !frame.Settled("input-ring")
						Button(c, "blur", ButtonOpts{})
					})
				}), 160, 100)
				tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
				label := "input"
				if group {
					label = "frame"
				}
				r, _ := tt.Find(label)
				pixel := func() color.RGBA { return tt.Image().RGBAAt(int(r.X)-2, int(r.Y+r.H/2)) }
				rest := pixel()
				if err := tt.Click("input"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * theme.MotionFast)
				tt.Frame()
				focused := pixel()
				if !fading || focused == rest {
					t.Errorf("focus did not fade: fading %v, rest %v, focused %v", fading, rest, focused)
				}
				fading = false
				if err := tt.Click("blur"); err != nil {
					t.Fatal(err)
				}
				time.Sleep(2 * theme.MotionFast)
				tt.Frame()
				if last := pixel(); !fading || last != rest {
					t.Errorf("blur did not fade: fading %v, last %v, want %v", fading, last, rest)
				}
			})
		}
	}
}

func TestDialogInputFocusIsNotClipped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dark   bool
		offset float32
	}{
		{"light", false, 0},
		{"dark", true, 0},
		{"light/fractional", false, 0.75},
		{"dark/fractional", true, 0.75},
	} {
		t.Run(tc.name, func(t *testing.T) {
			open, value := true, ""
			tt := ui.NewTester(themed(func(c *ui.Context) {
				c.DisableAnimations()
				Dialog(c, &open, "Account", DialogOpts{Desc: "Choose your provider", Close: "close"}, func() {
					Input(c, &value, InputOpts{}).Label("input").Margin(tc.offset, 0, 0, 0)
				}, nil)
			}), 800, 500)
			tt.SetDark(tc.dark)
			time.Sleep(2 * theme.MotionBase)
			tt.Frame()
			if err := tt.Click("input"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			r, _ := tt.Find("input")
			img := tt.Image()
			want := img.RGBAAt(int(r.X)-2, int(r.Y+r.H/2))
			// Sample inside the ring when platform font metrics put its top between pixels.
			top := int(math.Ceil(float64(r.Y)))
			for _, offset := range []int{1, 2, 3} {
				got := img.RGBAAt(int(r.X+r.W/2), top-offset)
				if max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) > 3 {
					t.Errorf("top focus ring clipped at %d px: got %v, side %v", offset, got, want)
				}
			}
		})
	}
}

func TestInputFocusAppearance(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for _, kind := range []string{"text", "password", "textarea", "invalid", "group", "autocomplete"} {
			t.Run(kind+map[bool]string{false: "/light", true: "/dark"}[dark], func(t *testing.T) {
				var value string
				tok := theme.Light()
				if dark {
					tok = theme.Dark()
				}
				tt := ui.NewTester(func(c *ui.Context) {
					c.DisableAnimations()
					theme.Apply(c, tok)
					ui.Column(c).Fill().Padding(20).Gap(20).Background(tok.Background).Children(func() {
						opts := InputOpts{Password: kind == "password", Invalid: kind == "invalid"}
						switch kind {
						case "group", "autocomplete":
							box := InputGroup(c, InputOpts{}).Label("frame")
							box.Children(func() {
								if kind == "group" {
									Input(c, &value, InputOpts{Password: true, Bare: true}).Label("input").Grow(1).Height(30)
									IconButton(c, "eye", "show", ButtonOpts{Variant: Ghost, Size: SizeSm})
								} else {
									ui.Autocomplete(c, &value, nil).Label("input").Grow(1).Height(30).Padding(0).Border(0, tok.Input).FocusRing(false)
								}
							})
						case "textarea":
							TextArea(c, &value, opts).Label("input")
						default:
							Input(c, &value, opts).Label("input")
						}
						Button(c, "blur", ButtonOpts{})
						ui.Box(c).Label("reference").Size(20, 20).Background(tok.Ring.Alpha(0.5))
					})
				}, 300, 260)
				if err := tt.Click("input"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				r, _ := tt.Find("input")
				if kind == "group" || kind == "autocomplete" {
					r, _ = tt.Find("frame")
				}
				ref, _ := tt.Find("reference")
				img := tt.Image()
				want := img.RGBAAt(int(ref.X+ref.W/2), int(ref.Y+ref.H/2))
				for _, offset := range []int{1, 2, 3} {
					got := img.RGBAAt(int(r.X)-offset, int(r.Y+r.H/2))
					if max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) > 3 {
						t.Errorf("ring at %d px: got %v, want %v", offset, got, want)
					}
				}
				border := tok.Ring
				if kind == "invalid" {
					border = tok.Danger
				}
				if got := img.RGBAAt(int(r.X), int(r.Y+r.H/2)); !near(got, border) {
					t.Errorf("focus border %v, want %v", got, border.SRGB())
				}
				if got := img.RGBAAt(int(r.X)-4, int(r.Y+r.H/2)); !near(got, tok.Background) {
					t.Errorf("ring exceeds 3 px: %v", got)
				}
				if err := tt.Click("blur"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if got := tt.Image().RGBAAt(int(r.X)-2, int(r.Y+r.H/2)); !near(got, tok.Background) {
					t.Errorf("ring remained after blur: %v", got)
				}
				tt.Key(ui.Shift, ui.KeyTab)
				tt.Frame()
				got := tt.Image().RGBAAt(int(r.X)-2, int(r.Y+r.H/2))
				if max(diff(got.R, want.R), diff(got.G, want.G), diff(got.B, want.B)) > 3 {
					t.Errorf("keyboard focus ring: got %v, want %v", got, want)
				}
			})
		}
	}
}
