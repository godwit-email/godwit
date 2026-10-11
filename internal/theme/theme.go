// Package theme turns Godwit's settings into semantic colors and sizes drawn
// from internal/palette, and installs them as the mygo theme.
package theme

import (
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	p "github.com/godwit-email/godwit/internal/palette"
)

// Tokens are Godwit's semantic colors and sizes, shadcn's neutral ones:
// Border outlines panels and cards, Input the controls (a step stronger in
// the dark), Ring the keyboard focus (at half opacity) and a hovered divider.
// Attention is the amber of
// status dots that ask for a look (amber-500); Unread, the blue of the dot
// beside unread mail (blue-500).
type Tokens struct {
	Dark                                                            bool
	Window, Sidebar, Background, Foreground, Muted, Border, Primary ui.Color
	PrimaryText, SidebarSelected, RailSelected, Hover, Success      ui.Color
	// HoverTint, like RailSelected, is a tint of the foreground for what
	// sits on the window's material (the rail, the top bar's ghost buttons),
	// which a solid gray would cover; over white it is Hover's gray.
	HoverTint                                                       ui.Color
	WarningBg, WarningBorder, WarningText, Danger, DangerBg         ui.Color
	SuccessBg, SuccessBorder, SuccessText, DangerBorder, DangerText ui.Color
	InsBg, InsText, DelBg, DelText                                  ui.Color
	Attention, Unread, Input, Ring                                  ui.Color
	// Outline is the faint edge of a panel over the window material: the
	// foreground at 5%, lighter than Border, which divides inside it.
	Outline ui.Color
	// FloatInset is the color of a floating input's inner edge in the dark
	// appearance: white at 20%, drawn inset with no offset or spread and a
	// 1 px blur (FloatInsetBlur), rgba(255 255 255 / 0.2) 0 0 1px 0 inset.
	FloatInset ui.Color
	// DividerHover is a draggable divider hovered or dragged: a step darker
	// than Border.
	DividerHover ui.Color
	// MarkTile is the tile under a provider's logo: white in either
	// appearance, as logos are drawn for white.
	MarkTile ui.Color
	// OnSolid is text on a solid status fill (AlertAction): white in either appearance.
	OnSolid ui.Color
	// Highlight marks matched text (search hits) in either appearance, with
	// HighlightText on it.
	Highlight, HighlightText ui.Color
	// Scale is the density (1.25 comfortable); outside this package sizes
	// come from c.Theme().Space(n), which it scales.
	FontSize, BodyFontSize, Scale float32
	Radius                        RadiusScale
	Text                          TextScale
}

// RadiusScale are corner radii, Tailwind v4's rounded-* steps (xs 2 … 4xl 32);
// they do not follow the density. Full rounds a box's short sides into half
// circles (renderers fit radii as CSS does).
type RadiusScale struct{ Xs, Sm, Md, Lg, Xl, X2l, X3l, X4l, Full float32 }

// TextScale are the interface's text sizes, steps from FontSize. Mail bodies
// use BodyFontSize. Body is FontSize itself (the user's setting, 14 by
// default); Base is the denser 13 under it. Title is dialog titles, Xl page
// headings, X2l brand text and Display the onboarding headline.
type TextScale struct{ Xs, Sm, Base, Body, Lg, Title, Xl, X2l, Display float32 }

// Layout sizes that do not follow the density.
const (
	Hairline           = 1   // dividers
	RailWidth          = 56  // the icon rail
	SidebarWidth       = 248 // the page sidebars, until the user drags them
	SidebarMinWidth    = 240 // the narrowest and widest a drag makes them
	SidebarMaxWidth    = 480
	ListWidth          = 340  // the mail list column
	TopBarHeight       = 44   // the top bar's least height
	SheetWidth         = 560  // dialogs and the add-account sheet
	DialogWideWidth    = 640  // DialogWide
	SheetMaxHeight     = 760  // the add-account sheet
	AsideWidth         = 440  // onboarding's left panel
	FormMaxWidth       = 520  // onboarding's form column
	ContentMaxWidth    = 1040 // settings pages
	ControlWidth       = 220  // settings' wide buttons
	SettingsMaxWidth   = 760  // a settings page's column
	SettingsSelectMin  = 200  // a select in a settings row
	SettingsSelectMax  = 280
	SettingsInputWidth = 260 // an input in a settings row
	// SignatureEditorHeight is the signature page: its toolbar (4+28+4 and a
	// hairline) over the editor's least 96; the editor scrolls inside.
	SignatureEditorHeight = 133
	HeadingGap            = 6  // a page heading to its subtitle
	SectionGap            = 10 // a section's title to its card
	PortWidth             = 90 // port fields
	SelectMinWidth        = 120
	ToastMaxWidth         = 420
	ToastMax              = 3   // toasts shown at once
	TitleButtonHeight     = 24  // buttons in the top bar
	PopupOffset           = 4   // room between a popup and its anchor
	SelectedBorder        = 2   // the border of a chosen tile
	QuoteBar              = 2   // the bar left of a quote in a mail body
	ImagePlaceholder      = 64  // an image not loaded, when its size is unknown
	ReaderMaxWidth        = 720 // the reading pane's text column
	AttachmentWidth       = 260 // an attachment in the reading pane
	WitMaxWidth           = 760 // the Ask page's conversation column
	CitationsWidth        = 320 // the Ask page's cited mail column
)

// BoldWeight is the font weight of bold text in mail bodies and the editor.
const BoldWeight = 600

// HeadingScale is the size of mail body headings h1–h6 in body font sizes.
var HeadingScale = [6]float32{1.6, 1.4, 1.2, 1.1, 1, 1}

// Sizes of glyphs and small parts; they do not follow the density.
const (
	IconXs, IconSm, Icon, IconMd, IconLg, IconXl     = 12, 14, 16, 18, 20, 28
	Logo                                             = 64             // the About page's logo
	BrandLogo                                        = 40             // onboarding's brand mark, beside the name
	MarkSm, MarkMd, MarkLg, MarkXl                   = 20, 28, 40, 48 // provider marks
	RailButton                                       = 40             // the icon rail's buttons
	CloseButton                                      = 20             // a toast's close button
	Control                                          = 16             // check boxes and radio buttons
	RadioDot, StatusDot                              = 8, 6           // a chosen radio's dot; status dots
	SwitchWidth, SwitchHeight, SwitchThumb           = 32, 18, 14     // a switch's track and thumb
	SwitchInset                                      = (SwitchHeight - SwitchThumb) / 2
	StepDot, PendingDot, DoneBadge                   = 20, 10, 36     // onboarding's check steps, OAuth wait, done mark
	ThemeTileWidth, ThemeTileHeight, ThemeTileGap    = 76, 48, 10     // settings' theme pictures and the space between them
	TileRail, TileSide, TileInset, TileGap, TileLine = 8, 18, 6, 4, 3 // the parts of a theme picture
	SkeletonShort                                    = 160            // a placeholder row's shorter line
	EmptyIcon, EmptyDescMax                          = 40, 400        // an empty state's icon tile; its text's widest
	GroupHeight, GroupSepHeight                      = 32, 16         // a button group; its separators
	TabUnderline                                     = 2              // the selected underline tab's bottom border
	Watermark                                        = 96             // the empty reader pane's bird
	AIButtonHeight, SummaryIndent, CardTracking      = 30, 18, 3      // the reader's Summary/Translate buttons; a summary's bullets; a card value's letter spacing
)

// ShadowLayer is one layer of a drop shadow: its offset down, blur and
// spread (negative tucks it under the box), and its opacity of the ink.
type ShadowLayer struct{ Y, Blur, Spread, Alpha float32 }

// Elevation is a drop shadow of one or more layers, drawn in order.
type Elevation []ShadowLayer

// Shadows, Tailwind v4's as shadcn uses them: Xs under inputs, selects,
// checkboxes, outline buttons and the page panel; Sm under cards and the selected tab; Md
// under popovers and select lists; Lg under dialogs, sheets and toasts; Xl
// for anything above those.
var (
	ShadowXs = Elevation{{1, 2, 0, 0.05}}
	ShadowSm = Elevation{{1, 3, 0, 0.1}, {1, 2, -1, 0.1}}
	ShadowMd = Elevation{{4, 6, -1, 0.1}, {2, 4, -2, 0.1}}
	ShadowLg = Elevation{{10, 15, -3, 0.1}, {4, 6, -4, 0.1}}
	ShadowXl = Elevation{{20, 25, -5, 0.1}, {8, 10, -6, 0.1}}
)

// FloatInsetBlur is the blur of FloatInset's inner edge, in DIPs.
const FloatInsetBlur = 1

// ShadowFloat is a floating input's edge and lift in the light appearance: a
// 1 px ring of the ink at 4% (its outline), then two soft drops,
// 0 0 0 1px #0000000a, 0 2px 8px #0000000a, 0 4px 80px 8px #00000006. The dark
// appearance's is an inner edge, FloatInset (kit.Float).
var ShadowFloat = Elevation{{0, 0, 1, 0x0a / 255.0}, {2, 8, 0, 0x0a / 255.0}, {4, 80, 8, 0x06 / 255.0}}

// sized fills the size tokens from the font size.
func sized(t Tokens) Tokens {
	f := t.FontSize
	t.Radius = RadiusScale{Xs: 2, Sm: 4, Md: 6, Lg: 8, Xl: 12, X2l: 16, X3l: 24, X4l: 32, Full: 9999} // Tailwind v4's
	t.Text = TextScale{Xs: f - 3, Sm: f - 2, Base: f - 1, Body: f, Lg: f + 1, Title: f + 2,
		Xl: f + 4, X2l: f + 6, Display: f + 15}
	return t
}

// status are the colors both appearances share.
func status(t Tokens) Tokens {
	t.Success = p.Green600.Color()
	t.WarningBg, t.WarningBorder, t.WarningText = p.Amber50.Color(), p.Amber300.Color(), p.Amber800.Color()
	t.Danger, t.DangerBg, t.Attention, t.Unread = p.Red600.Color(), p.Red50.Color(), p.Amber500.Color(), p.Blue500.Color()
	t.SuccessBg, t.SuccessBorder, t.SuccessText = p.Green50.Color(), p.Green200.Color(), p.Green700.Color()
	t.DangerBorder, t.DangerText = p.Red200.Color(), p.Red700.Color()
	t.InsBg, t.InsText, t.DelBg, t.DelText = p.Green100.Color(), p.Green800.Color(), p.Red50.Color(), p.Red700.Color()
	t.Highlight, t.HighlightText = p.Yellow200.Color(), p.Neutral950.Color()
	t.FontSize, t.BodyFontSize, t.Scale = 14, 14, 1
	return sized(t)
}

// Light is the light appearance (spec 7.0).
func Light() Tokens {
	return status(Tokens{
		Window: p.Mid(p.Neutral100, p.Neutral200).Color(), Sidebar: p.Neutral50.Color(),
		Background: p.White.Color(), Foreground: p.Neutral950.Color(), Muted: p.Neutral500.Color(),
		Border: p.Neutral200.Color(), Input: p.Neutral200.Color(), Ring: p.Neutral400.Color(), Outline: p.Neutral950.Color().Alpha(0.05), DividerHover: p.Neutral300.Color(), MarkTile: p.White.Color(), OnSolid: p.White.Color(),
		Primary: p.Neutral900.Color(), PrimaryText: p.White.Color(),
		SidebarSelected: p.Neutral100.Color(), RailSelected: p.Neutral950.Color().Alpha(0.04), HoverTint: p.Neutral950.Color().Alpha(0.04),
		Hover: p.Neutral100.Color(),
	})
}

// Dark is the dark appearance: shadcn neutral's dark values.
func Dark() Tokens {
	return status(Tokens{
		Dark:   true,
		Window: p.Mid(p.Neutral900, p.Neutral800).Color(), Sidebar: p.Neutral900.Color(),
		Background: p.Neutral950.Color(), Foreground: p.Neutral50.Color(), Muted: p.Neutral400.Color(),
		Border: p.White.Color().Alpha(0.1), Input: p.White.Color().Alpha(0.15), Ring: p.Neutral500.Color(), Outline: p.White.Color().Alpha(0.05), DividerHover: p.Neutral700.Color(), MarkTile: p.White.Color(), OnSolid: p.White.Color(), FloatInset: p.White.Color().Alpha(0.2),
		Primary: p.Neutral200.Color(), PrimaryText: p.Neutral900.Color(),
		SidebarSelected: p.Neutral800.Color(), RailSelected: p.White.Color().Alpha(0.1), HoverTint: p.White.Color().Alpha(0.08),
		Hover: p.Neutral800.Color(),
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

// Mono is the font for code-like text: the system's monospace font.
const Mono = "monospace"

// Apply makes the frame draw with t: mygo's theme for the appearance with
// t's colors, the system's font and t's font size. Of(c) returns t afterwards.
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
	th.Selection, th.Focus, th.Scrollbar = t.Primary.Alpha(0.15), t.Ring.Alpha(0.5), t.Muted.Alpha(0.5)
	th.Inverse, th.InverseText = t.Foreground, t.Background
	th.Radius, th.Spacing = t.Radius.Lg, 4*t.Scale // Space(n) is n Tailwind units
	th.Font, th.FontSize = "", t.Text.Body
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

// Component transitions: MotionFast for hover and selection, MotionBase for moves.
const (
	MotionFast = 120 * time.Millisecond
	MotionBase = 200 * time.Millisecond
	// MotionOffset is how far, in DIPs, an overlay rises as it comes in.
	MotionOffset = 4

	SkeletonPulse = 1500 * time.Millisecond // one dim-and-return of a Skeleton
)
