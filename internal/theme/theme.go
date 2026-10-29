// Package theme turns Godwit's settings into semantic colors and sizes drawn
// from internal/palette, and installs them as the mygo theme.
package theme

import (
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	p "github.com/godwit-email/godwit/internal/palette"
)

// Tokens are Godwit's semantic colors and sizes, shadcn's neutral ones:
// Border outlines panels and cards, Input the controls (a step stronger in
// the dark), Ring the keyboard focus (at half opacity) and a hovered divider.
// Attention is the amber of
// status dots for a trouble that clears by itself, as a sync being retried
// (amber-500); Danger's red marks one that needs the user. Unread, the blue
// of the dot beside unread mail (blue-500).
type Tokens struct {
	Dark                                                            bool
	Window, Sidebar, Background, Foreground, Muted, Border, Primary ui.Color
	PrimaryText, SidebarSelected, Hover, Success                    ui.Color
	// HoverTint is a tint of the foreground for what sits on the window's
	// material (the page switch's track, the top bar's ghost buttons), which
	// a solid gray would cover; over white it is Hover's gray.
	HoverTint                                                       ui.Color
	WarningBg, WarningBorder, WarningText, Danger, DangerBg         ui.Color
	SuccessBg, SuccessBorder, SuccessText, DangerBorder, DangerText ui.Color
	InsBg, InsText, DelBg, DelText                                  ui.Color
	Attention, Unread, Input, Ring                                  ui.Color
	// Idle is the grey of a status dot with nothing to report (neutral-400).
	Idle ui.Color
	// Outline is the faint edge of a mark's tile: the foreground at 5%,
	// lighter than Border.
	Outline ui.Color
	// FloatInset is the color of a floating input's inner edge in the dark
	// appearance: white at 20%, drawn inset with no offset or spread and a
	// 1 px blur (FloatInsetBlur), rgba(255 255 255 / 0.2) 0 0 1px 0 inset.
	FloatInset ui.Color
	// DividerHover is a draggable divider hovered or dragged: a step darker
	// than Border.
	DividerHover ui.Color
	// Raised is a dialog's panel: Background in the light, a step up
	// (neutral-900) in the dark, so it stands off the window behind.
	Raised ui.Color
	// Scrim is a dialog's backdrop: black at 20% in either appearance, as
	// macOS dims a window under a sheet. AlertScrim is an alert's: macOS's
	// gray at 60% (neutral-900 in the dark).
	Scrim, AlertScrim ui.Color
	// Card is a settings section's fill: a borderless block of rows a step
	// off Background (neutral-50; neutral-900 in the dark).
	Card ui.Color
	// MarkTile is the tile under a provider's logo: white in either
	// appearance, as logos are drawn for white.
	MarkTile ui.Color
	// OnSolid is text on a solid status fill (AlertAction): white in either appearance.
	OnSolid ui.Color
	// Highlight marks matched text (search hits) in either appearance, with
	// HighlightText on it.
	Highlight, HighlightText ui.Color
	// Labels are the mail labels' colors in LabelNames' order; Label looks
	// one up by name. An array keeps Tokens comparable.
	Labels [len(LabelNames)]LabelColor
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

// Line heights, multipliers of a text's own size, one per TextScale step:
// tokens.json's, but Base and Body are 1.3, the font's normal line height,
// which the design's screens render (its 1.5 and 1.6 make list rows 13 px
// taller than the design's). Reading is mail bodies, translations and the
// drafts pane; Summary a summary's points; Prose onboarding's and empty
// states' paragraphs; Lede onboarding's lede; Tight text in a box of fixed
// height.
const (
	LineXs, LineSm, LineBase, LineBody, LineLg = 1.4, 1.45, 1.3, 1.3, 1.5
	LineTitle, LineXl, LineX2l, LineDisplay    = 1.4, 1.35, 1.3, 1.25
	LineReading, LineSummary, LineProse        = 1.75, 1.65, 1.6
	LineLede, LineTight                        = 1.7, 1
	LineCJK                                    = 1.4  // a CJK line at the fallback font's normal height (the drafts pane's fields, an AI panel's title, the remote-image notice)
	LineSubject                                = 1.35 // a mail's subject over the reader and the drafts pane
	LineRow                                    = 1.45 // settings' labels, subtitles and section titles with CJK in them (LineFor)
)

// LineFor is the line height of a settings label, subtitle or section title
// s: LineRow with CJK in it, whose fallback font has the taller normal line
// (1.448), else Body's, as a browser lays out the design's settings pages.
func LineFor(s string) float32 {
	if strings.ContainsFunc(s, func(r rune) bool {
		return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
	}) {
		return LineRow
	}
	return LineBody
}

// Line returns the line height of the step whose size is size, Body's for
// a size between steps.
func (s TextScale) Line(size float32) float32 {
	switch size {
	case s.Xs:
		return LineXs
	case s.Sm:
		return LineSm
	case s.Base:
		return LineBase
	case s.Lg:
		return LineLg
	case s.Title:
		return LineTitle
	case s.Xl:
		return LineXl
	case s.X2l:
		return LineX2l
	case s.Display:
		return LineDisplay
	}
	return LineBody
}

// Layout sizes that do not follow the density.
const (
	// Stroke is a control's border (inputs, buttons, checks), which their
	// heights count, and the room of one: a separator's, a row gap.
	Stroke       = 1
	MaterialTint = 0.6 // the light Window's opacity over the window material: lighter, as the design, yet still see-through (its 95% is over a bare wallpaper, not a material)
	// Hairline is a border one device pixel wide at any scale, for cards,
	// panels and dividers: MyGo rounds border widths to whole device pixels,
	// at least one. Borders only; a box or line this thin rounds away.
	Hairline        = 0.01
	SidebarWidth    = 248 // the page sidebars, until the user drags them
	SidebarMinWidth = 240 // the narrowest and widest a drag makes them
	SidebarMaxWidth = 480
	// SidebarCollapsePull is how far past SidebarMinWidth a drag goes on
	// before it folds the sidebar away.
	SidebarCollapsePull = 80
	ListWidth           = 340 // the mail list column, until the user drags it
	ListMinWidth        = 280 // the narrowest and widest a drag makes it
	ListMaxWidth        = 560
	TopBarHeight        = 44   // the top bar's least height
	DialogSmWidth       = 440  // confirmations
	BackdropBlur        = 4    // a frosted panel blurs what is behind it 4× this much
	DialogLift          = 0.2  // a dialog's backdrop pads its bottom by this share of its height: the panel centred at 40%
	DialogMinTop        = 32   // yet a dialog stays at least this far below the window's top
	DialogTop           = 64   // a Top dialog's distance from the window's top
	SheetWidth          = 560  // DialogMd: forms, the add-account dialog
	DialogWideWidth     = 640  // DialogLg
	DialogWideMax       = 788  // DialogLg's tallest
	SheetMaxHeight      = 760  // DialogSm's and DialogMd's tallest
	AsideWidth          = 440  // onboarding's left panel
	FormMaxWidth        = 520  // onboarding's form column
	ContentMaxWidth     = 1040 // settings pages
	ControlWidth        = 220  // settings' wide buttons
	SettingsMaxWidth    = 760  // a settings page's column
	SettingsSelectMin   = 200  // a select in a settings row
	SettingsSelectMax   = 280
	UsageSelectMin      = 240 // a select in the model services' 用途 card
	SettingsInputWidth  = 260 // an input in a settings row
	// SignatureEditorHeight is a signature's editor, the same in both modes so
	// switching keeps the card's size: the page's toolbar over its editor, or
	// the native toolbar over MyGo's code editor; either scrolls inside.
	SignatureEditorHeight = 257
	HeadingGap            = 6  // a page heading to its subtitle
	SectionGap            = 10 // a section's title to its card
	PortWidth             = 90 // port fields
	SelectMinWidth        = 120
	FilterMenuMin         = 200 // a search filter chip's menu
	LabelMenuWidth        = 240 // the reader's label menu
	CiteCardWidth         = 320 // the mail a summary's cite shows on hover, as Wit's
	CiteChip              = 16  // a summary's cite: its height, and least width
	DetailsWidth          = 400 // a message's details popover: who it went to, its date
	DetailsLabel          = 72  // its labels' column
	SyncMenuWidth         = 320 // the top bar's sync status list: compact, fitted popover
	LabelSwatch           = 32  // a label color's button: a 24 dot, a 2 ring when chosen
	LabelSwatchDot        = 24
	LabelSwatchRing       = 2
	LabelIconCell         = 36 // a cell of the label icon grid
	ToastMaxWidth         = 420
	ToastMax              = 3  // toasts shown at once
	TitleButtonHeight     = 24 // buttons in the top bar
	PopupOffset           = 4  // room between a popup and its anchor
	SelectedBorder        = 2  // the border of a chosen tile
	GripWidth             = 3  // a resize grip's line, down the middle of the panels' gap
	GripHeight            = 56
	RingWidth             = 3   // shadcn's focused box ring, outside its border
	QuoteBar              = 2   // the bar left of a quote in a mail body
	ImagePlaceholder      = 64  // an image not loaded, when its size is unknown
	ReaderMaxWidth        = 720 // the reading pane's text column
	AttachmentWidth       = 282 // an attachment in the reading pane, with its download button
	DraftAttachmentWidth  = 286 // an attachment in the drafts pane
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
	AboutBird                                        = 150            // the About page's 3D bird
	BrandLogo                                        = 48             // onboarding's brand mark, beside the name
	MarkSm, MarkMd, MarkLg, MarkXl                   = 16, 28, 40, 48 // provider marks
	ServiceMarkOffset                                = 4              // optical alignment with a service name's first line
	CloseButton                                      = 20             // a toast's close button
	LabelChip, LabelChipX, LabelChipXIcon            = 20, 16, 10     // a mail label chip, its × and the ×'s glyph
	Control                                          = 16             // check boxes and radio buttons
	RadioDot, StatusDot                              = 8, 6           // a chosen radio's dot; status dots
	StatusBadge, StatusBadgeRing                     = 9, 1.5         // a status dot on a mark's corner: the 6 dot in a ring of the background
	SwitchWidth, SwitchHeight, SwitchThumb           = 32, 18, 14     // a switch's track and thumb
	SwitchInset                                      = (SwitchHeight - SwitchThumb) / 2
	StepDot, PendingDot, DoneBadge                   = 20, 10, 36     // onboarding's check steps, OAuth wait, done mark
	ThemeTileWidth, ThemeTileHeight, ThemeTileGap    = 76, 48, 10     // settings' theme pictures and the space between them
	TileRail, TileSide, TileInset, TileGap, TileLine = 8, 18, 6, 4, 3 // the parts of a theme picture
	SkeletonShort                                    = 160            // a placeholder row's shorter line
	OlderRowHeight                                   = 36             // the list's row while older mail loads from the server
	EmptyIcon, EmptyDescMax                          = 40, 400        // an empty state's icon tile; its text's widest
	TabUnderline                                     = 2              // the selected underline tab's bottom border
	Watermark                                        = 96             // the empty reader pane's bird
	FormLabelWidth, ModelIDInputWidth                = 96, 132        // a dialog form's label column; the add-model-ID field
	SummaryIndent, CardTracking                      = 18, 3          // a summary's bullets; a card value's letter spacing
	SummaryDot                                       = 5              // a summary point's disc
)

// ShadowLayer is one layer of a drop shadow: its offset down, blur and
// spread (negative tucks it under the box), and its opacity of the ink.
type ShadowLayer struct{ Y, Blur, Spread, Alpha float32 }

// Elevation is a drop shadow of one or more layers, drawn in order.
type Elevation []ShadowLayer

// Shadows, Tailwind v4's as shadcn uses them: Xs under inputs, selects,
// checkboxes, outline buttons and the page panel; Sm under cards and the selected tab; Md
// under popovers and select lists; Lg under toasts and floating cards; Xl
// for anything above those; X2l under dialogs and the search palette.
var (
	ShadowXs  = Elevation{{1, 2, 0, 0.05}}
	ShadowSm  = Elevation{{1, 3, 0, 0.1}, {1, 2, -1, 0.1}}
	ShadowMd  = Elevation{{4, 6, -1, 0.1}, {2, 4, -2, 0.1}}
	ShadowLg  = Elevation{{10, 15, -3, 0.1}, {4, 6, -4, 0.1}}
	ShadowXl  = Elevation{{20, 25, -5, 0.1}, {8, 10, -6, 0.1}}
	ShadowX2l = Elevation{{25, 50, -12, 0.25}}
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

// LabelColor is one label color: Dot on the color picker, Fill under a label
// set by hand, Edge around one the model set, Text for its name and icon.
type LabelColor struct{ Dot, Fill, Edge, Text ui.Color }

// labelSteps are the label colors' Tailwind steps: 500, 100, 200, 700, 950, 800, 300.
var labelSteps = map[string][7]p.OKLCH{
	"red":    {p.Red500, p.Red100, p.Red200, p.Red700, p.Red950, p.Red800, p.Red300},
	"orange": {p.Orange500, p.Orange100, p.Orange200, p.Orange700, p.Orange950, p.Orange800, p.Orange300},
	"amber":  {p.Amber500, p.Amber100, p.Amber200, p.Amber700, p.Amber950, p.Amber800, p.Amber300},
	"yellow": {p.Yellow500, p.Yellow100, p.Yellow200, p.Yellow700, p.Yellow950, p.Yellow800, p.Yellow300},
	"lime":   {p.Lime500, p.Lime100, p.Lime200, p.Lime700, p.Lime950, p.Lime800, p.Lime300},
	"green":  {p.Green500, p.Green100, p.Green200, p.Green700, p.Green950, p.Green800, p.Green300},
	"teal":   {p.Teal500, p.Teal100, p.Teal200, p.Teal700, p.Teal950, p.Teal800, p.Teal300},
	"sky":    {p.Sky500, p.Sky100, p.Sky200, p.Sky700, p.Sky950, p.Sky800, p.Sky300},
	"blue":   {p.Blue500, p.Blue100, p.Blue200, p.Blue700, p.Blue950, p.Blue800, p.Blue300},
	"violet": {p.Violet500, p.Violet100, p.Violet200, p.Violet700, p.Violet950, p.Violet800, p.Violet300},
	"pink":   {p.Pink500, p.Pink100, p.Pink200, p.Pink700, p.Pink950, p.Pink800, p.Pink300},
}

// LabelNames are the label colors' names, in the picker's order.
var LabelNames = [...]string{"gray", "red", "orange", "amber", "yellow", "lime", "green", "teal", "sky", "blue", "violet", "pink"}

// Label is the label color of a name; an unknown name is gray.
func (t Tokens) Label(name string) LabelColor {
	for i, n := range LabelNames {
		if n == name {
			return t.Labels[i]
		}
	}
	return t.Labels[0]
}

// labels: light -100 fill, -200 edge, -700 text; dark -950, -800, -300.
// Gray is neutral, its dot the primary.
func labels(t Tokens) (out [len(LabelNames)]LabelColor) {
	for i, name := range LabelNames {
		switch s, ok := labelSteps[name]; {
		case !ok && t.Dark:
			out[i] = LabelColor{t.Primary, p.Neutral800.Color(), p.Neutral700.Color(), p.Neutral300.Color()}
		case !ok:
			out[i] = LabelColor{t.Primary, p.Neutral100.Color(), p.Neutral200.Color(), p.Neutral700.Color()}
		case t.Dark:
			out[i] = LabelColor{s[0].Color(), s[4].Color(), s[5].Color(), s[6].Color()}
		default:
			out[i] = LabelColor{s[0].Color(), s[1].Color(), s[2].Color(), s[3].Color()}
		}
	}
	return out
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
	if t.Dark { // Tailwind's dark pairing: -950 fill, -800 edge, -300 text
		t.WarningBg, t.WarningBorder, t.WarningText = p.Amber950.Color(), p.Amber800.Color(), p.Amber300.Color()
		t.DangerBg, t.DangerBorder, t.DangerText = p.Red950.Color(), p.Red800.Color(), p.Red300.Color()
		t.SuccessBg, t.SuccessBorder, t.SuccessText = p.Green950.Color(), p.Green800.Color(), p.Green300.Color()
		t.InsBg, t.InsText, t.DelBg, t.DelText = p.Green950.Color(), p.Green300.Color(), p.Red950.Color(), p.Red300.Color()
		t.Highlight, t.HighlightText = p.Yellow900.Color(), p.Yellow100.Color()
	}
	t.Labels = labels(t)
	t.FontSize, t.BodyFontSize, t.Scale = 14, 14, 1
	return sized(t)
}

// Light is the light appearance.
func Light() Tokens {
	return status(Tokens{
		Window: p.Neutral50.Color(), Sidebar: p.Neutral50.Color(),
		Background: p.White.Color(), Foreground: p.Neutral950.Color(), Muted: p.Neutral500.Color(),
		Border: p.Neutral200.Color(), Input: p.Neutral200.Color(), Ring: p.Neutral400.Color(), Idle: p.Neutral400.Color(), Outline: p.Neutral950.Color().Alpha(0.05), DividerHover: p.Neutral300.Color(), MarkTile: p.White.Color(), OnSolid: p.White.Color(), Raised: p.White.Color(), Card: p.Neutral50.Color(), Scrim: p.Black.Color().Alpha(0.2), AlertScrim: p.MacAlertGray.Color().Alpha(0.6),
		Primary: p.Neutral900.Color(), PrimaryText: p.White.Color(),
		SidebarSelected: p.Neutral100.Color(), HoverTint: p.Neutral950.Color().Alpha(0.04),
		Hover: p.Neutral100.Color(),
	})
}

// Dark is the dark appearance: shadcn neutral's dark values.
func Dark() Tokens {
	return status(Tokens{
		Dark:   true,
		Window: p.Mid(p.Neutral900, p.Neutral800).Color(), Sidebar: p.Neutral900.Color(),
		Background: p.Neutral950.Color(), Foreground: p.Neutral50.Color(), Muted: p.Neutral400.Color(),
		Border: p.White.Color().Alpha(0.1), Input: p.White.Color().Alpha(0.15), Ring: p.Neutral500.Color(), Idle: p.Neutral500.Color(), Outline: p.White.Color().Alpha(0.05), DividerHover: p.Neutral700.Color(), MarkTile: p.White.Color(), OnSolid: p.White.Color(), FloatInset: p.White.Color().Alpha(0.2), Raised: p.Neutral900.Color(), Card: p.Neutral900.Color(), Scrim: p.Black.Color().Alpha(0.2), AlertScrim: p.Neutral900.Color().Alpha(0.6),
		Primary: p.Neutral200.Color(), PrimaryText: p.Neutral900.Color(),
		SidebarSelected: p.Neutral800.Color(), HoverTint: p.White.Color().Alpha(0.08),
		Hover: p.Neutral800.Color(),
	})
}

// FromSettings picks the appearance (Theme "system" follows systemDark), the
// font sizes, and the density: comfortable rows and paddings are 1.25×.
func FromSettings(st core.Settings, systemDark bool) Tokens {
	t := Light()
	if st.Theme == "dark" || (st.Theme == "system" || st.Theme == "") && systemDark {
		t = Dark()
	}
	if st.FontSize > 0 {
		t.FontSize = float32(st.FontSize)
	}
	t.BodyFontSize = t.FontSize // 0 follows the interface
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
