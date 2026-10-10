// Package theme turns Godwit's settings into semantic colors and sizes drawn
// from internal/palette, and installs them as the mygo theme.
package theme

import (
	"embed"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	p "github.com/godwit-email/godwit/internal/palette"
)

// Tokens are Godwit's semantic colors and sizes. Attention is the amber of
// status dots that ask for a look (amber-500); Unread, the blue of the dot
// beside unread mail (blue-500).
type Tokens struct {
	Dark                                                            bool
	Window, Sidebar, Background, Foreground, Muted, Border, Primary ui.Color
	PrimaryText, SidebarSelected, RailSelected, Hover, Success      ui.Color
	WarningBg, WarningBorder, WarningText, Danger, DangerBg         ui.Color
	InsBg, InsText, DelBg, DelText                                  ui.Color
	Attention, Unread                                               ui.Color
	// Scale is the density (1.25 comfortable); outside this package sizes
	// come from c.Theme().Space(n), which it scales.
	FontSize, BodyFontSize, Scale float32
	Radius                        RadiusScale
	Text                          TextScale
}

// RadiusScale are corner radii; they do not follow the density. Xs (4) and
// Panel (12, cards) are the off-scale radii in use; Full rounds a box's
// short sides into half circles (renderers fit radii as CSS does).
type RadiusScale struct{ Xs, Sm, Md, Lg, Xl, Panel, Full float32 }

// TextScale are the interface's text sizes, steps from FontSize. Mail bodies
// use BodyFontSize. Title (dialog titles), Brand, Heading (page headings)
// and Display (the onboarding headline) are the off-scale sizes in use.
type TextScale struct{ Xs, Sm, Base, Lg, Xl, X2l, Title, Brand, Heading, Display float32 }

// Layout sizes that do not follow the density.
const (
	Hairline          = 1    // dividers
	RailWidth         = 56   // the icon rail
	SidebarWidth      = 248  // the mail and settings sidebars
	ListWidth         = 360  // the mail list column
	TopBarHeight      = 44   // the top bar's least height
	SheetWidth        = 560  // dialogs and the add-account sheet
	SheetMaxHeight    = 760  // the add-account sheet
	AsideWidth        = 440  // onboarding's left panel
	FormMaxWidth      = 520  // onboarding's form column
	ContentMaxWidth   = 1040 // settings pages
	ControlWidth      = 220  // settings' selects and buttons
	PortWidth         = 90   // port fields
	SelectMinWidth    = 120
	ToastMaxWidth     = 420
	TitleButtonHeight = 24  // buttons in the top bar
	PopupOffset       = 4   // room between a popup and its anchor
	SelectedBorder    = 2   // the border of a chosen tile
	QuoteBar          = 2   // the bar left of a quote in a mail body
	ImagePlaceholder  = 64  // an image not loaded, when its size is unknown
	ReaderMaxWidth    = 720 // the reading pane's text column
	AttachmentWidth   = 260 // an attachment in the reading pane
	WitMaxWidth       = 760 // the Ask page's conversation column
	CitationsWidth    = 320 // the Ask page's cited mail column
	WitPanelWidth     = 360 // the compose page's Wit panel
)

// BoldWeight is the font weight of bold text in mail bodies and the editor.
const BoldWeight = 600

// HeadingScale is the size of mail body headings h1–h6 in body font sizes.
var HeadingScale = [6]float32{1.6, 1.4, 1.2, 1.1, 1, 1}

// Sizes of glyphs and small parts; they do not follow the density.
const (
	IconXs, IconSm, Icon, IconMd, IconLg, IconXl = 12, 14, 16, 18, 20, 28
	Logo                                         = 64         // the About page's logo
	BrandLogo                                    = 40         // onboarding's brand mark, beside the name
	MarkSm, MarkMd, MarkLg                       = 20, 28, 40 // provider marks
	RailButton                                   = 40         // the icon rail's buttons
	CloseButton                                  = 20         // a toast's close button
	Control                                      = 16         // check boxes and radio buttons
	RadioDot, StatusDot                          = 8, 6       // a chosen radio's dot; status dots
	SwitchWidth, SwitchHeight, SwitchThumb       = 32, 18, 14 // a switch's track and thumb
	SwitchInset                                  = (SwitchHeight - SwitchThumb) / 2
	StepDot, PendingDot, DoneBadge               = 20, 10, 36   // onboarding's check steps, OAuth wait, done mark
	ThemeTileWidth, ThemeTileHeight              = 132, 84      // settings' theme pictures
	TileRail, TileInset, TileGap, TileLine       = 14, 10, 6, 5 // the parts of a theme picture
)

// Shadow is a drop shadow's offset down and its blur.
type Shadow struct{ Y, Blur float32 }

// Shadows: Sm under thumbs and tabs, Md under popups and toasts, Lg under
// dialogs, Xl under the add-account sheet.
var ShadowSm, ShadowMd, ShadowLg, ShadowXl = Shadow{1, 2}, Shadow{4, 16}, Shadow{16, 48}, Shadow{24, 64}

// sized fills the size tokens from the font size.
func sized(t Tokens) Tokens {
	f := t.FontSize
	t.Radius = RadiusScale{Xs: 4, Sm: 6, Md: 8, Lg: 10, Xl: 14, Panel: 12, Full: 9999}
	t.Text = TextScale{Xs: f - 2, Sm: f - 1, Base: f, Lg: f + 2, Xl: f + 5, X2l: f + 7,
		Title: f + 3, Brand: f + 4, Heading: f + 6, Display: f + 16}
	return t
}

// status are the colors both appearances share.
func status(t Tokens) Tokens {
	t.Success = p.Green600.Color()
	t.WarningBg, t.WarningBorder, t.WarningText = p.Amber50.Color(), p.Amber300.Color(), p.Amber800.Color()
	t.Danger, t.DangerBg, t.Attention, t.Unread = p.Red600.Color(), p.Red50.Color(), p.Amber500.Color(), p.Blue500.Color()
	t.InsBg, t.InsText, t.DelBg, t.DelText = p.Green100.Color(), p.Green800.Color(), p.Red50.Color(), p.Red700.Color()
	t.FontSize, t.BodyFontSize, t.Scale = 13, 14, 1
	return sized(t)
}

// Light is the light appearance (spec 7.0).
func Light() Tokens {
	return status(Tokens{
		Window: p.Mid(p.Neutral100, p.Neutral200).Color(), Sidebar: p.Neutral50.Color(),
		Background: p.White.Color(), Foreground: p.Neutral950.Color(), Muted: p.Neutral500.Color(),
		Border: p.Neutral200.Color(), Primary: p.Neutral900.Color(), PrimaryText: p.White.Color(),
		SidebarSelected: p.Mid(p.Neutral100, p.Neutral200).Color(), RailSelected: p.Neutral200.Color(), Hover: p.Neutral100.Color(),
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
	return sized(t)
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
	th.Radius, th.Spacing = t.Radius.Md, 4*t.Scale // Space(n) is n Tailwind units
	th.Font, th.FontSize = "Geist", t.Text.Base
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

// Component transitions: MotionFast for hover and selection, MotionBase for moves.
const (
	MotionFast = 120 * time.Millisecond
	MotionBase = 200 * time.Millisecond
)
