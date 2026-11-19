package kit

import (
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// An InputButton is as high as an Input of its size, and a click on it is its action's.
func TestInputButton(t *testing.T) {
	clicked := 0
	tt := ui.NewTester(themed(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(10).Children(func() {
			InputButton(c, InputOpts{Size: SizeSm, Flat: true, Tint: true}, func() { clicked++ }).Label("search").Children(func() {
				ui.Text(c, "search").Role(ui.RoleNone)
			})
		})
	}), 200, 100)
	r, ok := tt.Find("search")
	if !ok || r.H != 28 {
		t.Fatalf("the field at %+v (found %v), want 28 high", r, ok)
	}
	if err := tt.Click("search"); err != nil || clicked != 1 {
		t.Fatalf("click: %v, clicked %d times", err, clicked)
	}
}

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
							Input(c, &value, InputOpts{Flat: true, Leading: func() { InputIcon(c, "search") }, Box: &frame}).Label("input")
							frame.Label("frame")
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
				tt.Advance(2 * theme.MotionFast)
				focused := pixel()
				if !fading || focused == rest {
					t.Errorf("focus did not fade: fading %v, rest %v, focused %v", fading, rest, focused)
				}
				fading = false
				if err := tt.Click("blur"); err != nil {
					t.Fatal(err)
				}
				tt.Advance(2 * theme.MotionFast)
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
			tt.Advance(2 * theme.MotionBase)
			if err := tt.Click("input"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			r, _ := tt.Find("input")
			img := tt.Image()
			want := img.RGBAAt(int(r.X)-2, int(r.Y+r.H/2))
			// Sample inside the ring where the painter puts its top: edges round to
			// the nearest pixel (a Hairline border leaves r.Y a hair past one).
			top := int(math.Round(float64(r.Y)))
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
							var box ui.Element
							slots := InputOpts{Box: &box}
							if kind == "group" {
								slots.Password, slots.Trailing = true, func() { IconButton(c, "eye", "show", ButtonOpts{Variant: Ghost, Size: SizeXs}) }
							} else {
								slots.Autocomplete, slots.Suggest, slots.Leading = true, []string{"a"}, func() { InputIcon(c, "search") }
							}
							Input(c, &value, slots).Label("input")
							box.Label("frame")
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

// An input with slots is the same height as one without, returns its text
// field, and the frame it is drawn in is the one it hands back.
func TestInputSlots(t *testing.T) {
	for _, size := range []Size{SizeSm, SizeDefault, SizeLg} {
		var plain, slotted, query string
		var box ui.Element
		clicks, submits := 0, 0
		tt := ui.NewTester(themed(func(c *ui.Context) {
			ui.Column(c).Fill().Padding(10).Gap(10).Children(func() {
				Input(c, &plain, InputOpts{Size: size}).Label("plain")
				in := Input(c, &slotted, InputOpts{Size: size, Box: &box,
					Leading: func() { InputIcon(c, "search") },
					Trailing: func() {
						IconButton(c, "x", "clear", ButtonOpts{Variant: Ghost, Size: SizeXs}).OnClick(func() { clicks++ })
					},
				}).Label("slotted")
				if in.Submitted() {
					submits++
				}
				box.Label("box")
				Input(c, &query, InputOpts{Size: size, Leading: func() { InputIcon(c, "search") }}).Label("leading only")
			})
		}), 300, 300)
		plainRect, _ := tt.Find("plain")
		boxRect, ok := tt.Find("box")
		if !ok || boxRect.H != plainRect.H || boxRect.W != plainRect.W {
			t.Errorf("size %v: frame %+v, plain input %+v", size, boxRect, plainRect)
		}
		field, _ := tt.Find("slotted")
		if field.X <= boxRect.X+16 || field.X+field.W >= boxRect.X+boxRect.W-24 {
			t.Errorf("size %v: the field %+v does not sit between the slots of %+v", size, field, boxRect)
		}
		if err := tt.Click("clear"); err != nil || clicks != 1 {
			t.Fatalf("clear: %v, clicked %d times", err, clicks)
		}
		if err := tt.Click("slotted"); err != nil {
			t.Fatal(err)
		}
		tt.Type("hi")
		tt.Key(0, ui.KeyEnter)
		if slotted != "hi" || submits != 1 {
			t.Errorf("size %v: typed %q, submitted %d times", size, slotted, submits)
		}
		leading, _ := tt.Find("leading only")
		want := plainRect.X + plainRect.W - map[Size]float32{SizeSm: 8, SizeDefault: 10, SizeLg: 12}[size] - theme.Stroke
		if got := leading.X + leading.W; math.Abs(float64(got-want)) > 0.5 {
			t.Errorf("size %v: a leading-only field ends at %v, want %v: the text is cut at the side padding", size, got, want)
		}
	}
}

// An autocompleting input with a slot is one field whose suggestions come and
// go as the user types: it keeps its focus and text when the first one shows,
// and its frame rings the whole row, the slot included.
func TestInputAutocompleteWithSlotKeepsFocusAndRing(t *testing.T) {
	var value string
	var tok theme.Tokens
	var box ui.Element
	tt := ui.NewTester(themed(func(c *ui.Context) {
		c.DisableAnimations()
		tok = theme.Of(c)
		var suggestions []string
		if value != "" && value[len(value)-1] == '@' {
			suggestions = []string{value + "example.com"}
		}
		ui.Column(c).Fill().Padding(10).Children(func() {
			Input(c, &value, InputOpts{Size: SizeLg, Autocomplete: true, Suggest: suggestions, Box: &box,
				Trailing: func() { Badge(c, "Detected").Margin(0, 8, 0, 0) },
			}).Label("address")
			box.Label("box")
		})
	}), 400, 200)
	if err := tt.Click("address"); err != nil {
		t.Fatal(err)
	}
	tt.Type("me@")
	tt.Frame()
	if !tt.HasText("me@example.com") {
		t.Fatalf("no suggestion for %q: %q", value, tt.Texts())
	}
	tt.Type("x")
	tt.Frame()
	if value != "me@x" {
		t.Fatalf("typing went on as %q: the field lost its focus when suggestions appeared", value)
	}
	badge, _ := tt.Find("Detected")
	frame, _ := tt.Find("box")
	img, found := tt.Image(), false
	y := int(badge.Y + badge.H/2)
	for x := int(badge.X + badge.W); x < int(frame.X+frame.W)+2 && !found; x++ {
		found = near(img.RGBAAt(x, y), tok.Ring)
	}
	if !found {
		t.Error("no ring-coloured border right of the badge: the frame does not ring the whole row")
	}
	x := int(frame.X)
	found = false
	for ; x > int(frame.X)-4 && !found; x-- {
		found = near(img.RGBAAt(x, int(frame.Y+frame.H/2)), tok.Ring)
	}
	if !found {
		t.Error("no ring-coloured border left of the row")
	}
}

// A Select of any comparable type writes the option chosen from its menu.
func TestKitSelectWritesAnyComparable(t *testing.T) {
	type id int64
	sel, changed := id(0), 0
	opts := []Option[id]{{Value: 0, Label: "All"}, {Value: 7, Label: "Seven"}}
	tt := ui.NewTester(themed(func(c *ui.Context) {
		if Select(c, &sel, opts).Label("Scope").Changed() {
			changed++
		}
	}), 300, 300)
	if err := tt.Click("Scope"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Seven"); err != nil || sel != 7 {
		t.Fatalf("choose: %v, selected %d", err, sel)
	}
	tt.Frame()
	if changed != 1 || !tt.HasText("Seven") {
		t.Errorf("changed %d times, texts %q", changed, tt.Texts())
	}
}
