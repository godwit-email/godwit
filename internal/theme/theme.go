// Package theme turns Godwit's settings into semantic colors and sizes drawn
// from internal/palette, and installs them as the mygo theme.
package theme

import (
	"embed"
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	p "github.com/godwit-email/godwit/internal/palette"
)

// Tokens are Godwit's semantic colors and sizes. Attention is the amber of
// status dots that ask for a look (amber-500).
type Tokens struct {
	Dark                                                            bool
	Window, Sidebar, Background, Foreground, Muted, Border, Primary ui.Color
	PrimaryText, SidebarSelected, RailSelected, Hover, Success      ui.Color
	WarningBg, WarningBorder, WarningText, Danger, DangerBg         ui.Color
	InsBg, InsText, DelBg, DelText                                  ui.Color
	Attention                                                       ui.Color
	FontSize, BodyFontSize, Scale                                   float32
}

// status are the colors both appearances share.
func status(t Tokens) Tokens {
	t.Success = p.Green600.Color()
	t.WarningBg, t.WarningBorder, t.WarningText = p.Amber50.Color(), p.Amber300.Color(), p.Amber800.Color()
	t.Danger, t.DangerBg, t.Attention = p.Red600.Color(), p.Red50.Color(), p.Amber500.Color()
	t.InsBg, t.InsText, t.DelBg, t.DelText = p.Green100.Color(), p.Green800.Color(), p.Red50.Color(), p.Red700.Color()
	t.FontSize, t.BodyFontSize, t.Scale = 13, 14, 1
	return t
}

// Light is the light appearance (spec 7.0).
func Light() Tokens {
	return status(Tokens{
		Window: p.Mid(p.Neutral100, p.Neutral200).Color(), Sidebar: p.Neutral50.Color(),
		Background: p.White.Color(), Foreground: p.Neutral950.Color(), Muted: p.Neutral500.Color(),
		Border: p.Neutral200.Color(), Primary: p.Neutral900.Color(), PrimaryText: p.White.Color(),
		SidebarSelected: p.Neutral100.Color(), RailSelected: p.Neutral200.Color(), Hover: p.Neutral100.Color(),
	})
}

// Dark is the dark appearance: shadcn neutral's dark values.
func Dark() Tokens {
	return status(Tokens{
		Dark:   true,
		Window: p.Mid(p.Neutral900, p.Neutral800).Color(), Sidebar: p.Neutral900.Color(),
		Background: p.Neutral950.Color(), Foreground: p.Neutral50.Color(), Muted: p.Neutral400.Color(),
		Border: p.Neutral800.Color(), Primary: p.Neutral200.Color(), PrimaryText: p.Neutral900.Color(),
		SidebarSelected: p.Neutral800.Color(), RailSelected: p.Neutral700.Color(), Hover: p.Neutral800.Color(),
	})
}

// FromSettings picks the appearance (Theme "system" follows systemDark), the
// font sizes, and the density: comfortable rows and paddings are 1.25×.
func FromSettings(st core.Settings, systemDark bool) Tokens {
	t := Light()
	if st.Theme == "dark" || st.Theme == "system" && systemDark {
		t = Dark()
	}
	if st.FontSize > 0 {
		t.FontSize = float32(st.FontSize)
	}
	if st.BodyFontSize > 0 {
		t.BodyFontSize = float32(st.BodyFontSize)
	}
	if st.Density == "comfortable" {
		t.Scale = 1.25
	}
	return t
}

var (
	mu     sync.Mutex
	themes = map[Tokens]*ui.Theme{} // one per Tokens, so a frame's theme leads back to them
	tokens = map[*ui.Theme]Tokens{}
)

// Apply makes the frame draw with t: mygo's theme for the appearance with
// t's colors, Geist and t's font size. Of(c) returns t afterwards.
func Apply(c *ui.Context, t Tokens) {
	mu.Lock()
	th, ok := themes[t]
	if !ok {
		th = mygoTheme(t)
		themes[t], tokens[th] = th, t
	}
	mu.Unlock()
	c.SetTheme(th)
}

func mygoTheme(t Tokens) *ui.Theme {
	th := *ui.LightTheme()
	if t.Dark {
		th = *ui.DarkTheme()
	}
	th.Background, th.Surface, th.SurfaceHover, th.SurfacePressed = t.Background, t.Background, t.Hover, t.Border
	th.Border, th.Text, th.TextMuted = t.Border, t.Foreground, t.Muted
	th.Accent, th.AccentText = t.Primary, t.PrimaryText
	th.AccentHover, th.AccentPressed = t.Primary.Mix(t.Background, 0.1), t.Primary.Mix(t.Background, 0.2)
	th.Danger, th.Warning, th.Success = t.Danger, t.Attention, t.Success
	th.Selection, th.Focus, th.Scrollbar = t.Primary.Alpha(0.15), t.Muted.Alpha(0.6), t.Muted.Alpha(0.5)
	th.Inverse, th.InverseText = t.Foreground, t.Background
	th.Radius, th.Spacing = 8, 4*t.Scale
	th.Font, th.FontSize = "Geist", t.FontSize
	return &th
}

// Of returns the tokens Apply gave this frame, or, without Apply, the
// default ones for the window's appearance.
func Of(c *ui.Context) Tokens {
	th := c.Theme()
	mu.Lock()
	t, ok := tokens[th]
	mu.Unlock()
	if ok {
		return t
	}
	if th.Dark {
		return Dark()
	}
	return Light()
}

//go:embed fonts/*.ttf
var fonts embed.FS

// RegisterFonts adds Geist (400, 500, 600) and Geist Mono (500). Chinese and
// other scripts Geist lacks fall back to the system's fonts.
func RegisterFonts() error { return registerFonts() }

var registerFonts = sync.OnceValue(func() error {
	for file, family := range map[string]string{
		"Geist-Regular.ttf": "Geist", "Geist-Medium.ttf": "Geist", "Geist-SemiBold.ttf": "Geist",
		"GeistMono-Medium.ttf": "Geist Mono",
	} {
		b, err := fonts.ReadFile("fonts/" + file)
		if err != nil {
			return err
		}
		if err := ui.RegisterFont(b, family); err != nil {
			return fmt.Errorf("register %s: %w", file, err)
		}
	}
	return nil
})
