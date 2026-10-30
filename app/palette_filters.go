package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// palFilter is a filter the palette offers, and once chosen its chip: set
// puts it into searchFilters. A kind holds one filter at a time.
type palFilter struct {
	kind, label string
	icon        string
	iconColor   func(theme.Tokens) ui.Color
	renderIcon  func(c *ui.Context, selected bool)
	set         func(*searchFilters)
}

func labelIcon(icon string) string {
	if slices.Contains(core.LabelIcons, icon) {
		return "label-" + icon
	}
	return "tag"
}

func (p *palette) renderAccountIcon(c *ui.Context, acc core.Account, selected bool) {
	if strings.HasPrefix(acc.Icon, "image:") {
		if img := p.a.accountImage(acc); img != nil {
			markTile(c, img, theme.IconSm)
			return
		}
	}
	if id, ok := strings.CutPrefix(acc.Icon, "brand:"); ok {
		if b := icons.Brand(id); b != nil {
			markTile(c, b, theme.IconSm)
			return
		}
	}
	prov := accountProvider(p.a, acc)
	if acc.Icon == "" && prov.ID != "" && prov.ID != "other" {
		if b := icons.Brand(prov.ID); b != nil {
			markTile(c, b, theme.IconSm)
			return
		}
	}
	t := theme.Of(c)
	if name, ok := strings.CutPrefix(acc.Icon, "label:"); ok && slices.Contains(core.LabelIcons, name) {
		svg := icons.Get("label-" + name)
		if selected {
			svg = icons.FillOr("label-" + name)
		}
		ui.Icon(c, svg).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		return
	}
	svg := icons.Get("at")
	if selected {
		svg = icons.FillOr("at")
	}
	ui.Icon(c, svg).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
}

// sf is the chips as the list's search filters, which 查看全部 carries over.
func (p *palette) sf() searchFilters {
	var f searchFilters
	for _, x := range p.filters {
		x.set(&f)
	}
	return f
}

// filterOpts are the filters / offers: the sender is the text typed.
func (p *palette) filterOpts() []palFilter {
	a := p.a
	out := []palFilter{
		{kind: "attach", label: i18n.T("mail.search.has_attachments"), icon: "paperclip", set: func(f *searchFilters) { f.attach = true }},
		{kind: "unread", label: i18n.T("mail.filter.unread"), icon: "mail-unread", set: func(f *searchFilters) { f.unread = true }},
		{kind: "starred", label: i18n.T("palette.filter.starred"), icon: "star", set: func(f *searchFilters) { f.starred = true }},
	}
	for _, acc := range a.accounts {
		id := acc.ID
		name := accountName(acc)
		account := acc
		out = append(out, palFilter{
			kind:  "account",
			label: name,
			icon:  "at",
			renderIcon: func(c *ui.Context, selected bool) {
				p.renderAccountIcon(c, account, selected)
			},
			set: func(f *searchFilters) { f.acc, f.accSet = id, true },
		})
	}
	for _, l := range a.labels {
		id := l.ID
		color := l.Color
		out = append(out, palFilter{
			kind:      "label",
			label:     l.Name,
			icon:      labelIcon(l.Icon),
			iconColor: func(t theme.Tokens) ui.Color { return t.Label(color).Text },
			set:       func(f *searchFilters) { f.labelID = id },
		})
	}
	for i, key := range searchSpans[1:] {
		span := i + 1
		out = append(out, palFilter{
			kind:  "span",
			label: i18n.T(key),
			icon:  "time",
			set:   func(f *searchFilters) { f.span = span },
		})
	}
	if from := strings.TrimSpace(p.query); from != "" {
		out = append(out, palFilter{
			kind:  "from",
			label: i18n.Tf("palette.filter.from", from),
			icon:  "user",
			set:   func(f *searchFilters) { f.from = from },
		})
	}
	return out
}

// addFilter makes x a chip in place of its kind's and closes the list; the
// sender takes the text typed, which clear empties.
func (p *palette) addFilter(x palFilter, clear func()) {
	kept := p.filters[:0]
	for _, f := range p.filters {
		if f.kind != x.kind {
			kept = append(kept, f)
		}
	}
	p.filters, p.filtering, p.started = append(kept, x), false, false
	if x.kind == "from" {
		p.query = ""
		clear()
	}
}

// removeFilter takes chip i off.
func (p *palette) removeFilter(i int) {
	p.filters = append(p.filters[:i:i], p.filters[i+1:]...)
	p.started = false
}

// slashed reports whether now is was with a / typed where it opens the
// filters: first, or after a space.
// shortcut: compares the text frame to frame rather than at the caret, so
// typing / right after another / reads as the later one; upgrade when the
// palette's input exposes its caret to OnKey.
func slashed(was, now string) bool {
	if len(now) != len(was)+1 {
		return false
	}
	for i := len(now) - 1; i >= 0; i-- {
		if now[i] == '/' && now[:i]+now[i+1:] == was {
			return i == 0 || now[i-1] == ' '
		}
	}
	return false
}

// filterKey is key while the filter list shows: ↑↓ choose, ↵ adds, Esc
// closes the list only; the kind tabs stay put behind it.
func (p *palette) filterKey(k ui.TextKey) bool {
	opts := p.filterOpts()
	n := len(opts)
	switch {
	case k.Mods == 0 && (k.Key == ui.KeyDown || k.Key == ui.KeyUp):
		p.fsel, p.scroll = wrapStep(p.fsel, n, k.Key == ui.KeyUp), true
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.addFilter(opts[min(p.fsel, n-1)], func() { k.SetText("") })
	case k.Mods == 0 && k.Key == ui.KeyEscape:
		p.filtering = false
	case k.Mods == 0 && (k.Key == ui.KeyLeft || k.Key == ui.KeyRight) && k.Text() == "",
		k.Key == ui.KeyTab && (k.Mods == ui.Ctrl || k.Mods == ui.Ctrl|ui.Shift):
		// swallowed: switching the tabs behind the list reads as the list moving
	default:
		return false
	}
	return true
}

// filterGroup is the heading a filter of kind lists under; filterOpts keeps
// each group's filters together.
func filterGroup(kind string) string {
	switch kind {
	case "account":
		return i18n.T("mail.accounts")
	case "label":
		return i18n.T("mail.labels")
	case "span":
		return i18n.T("mail.search.time")
	case "from":
		return i18n.T("mail.search.from")
	}
	return i18n.T("palette.filter.status")
}

// chips are the filters chosen, before the search box.
func (p *palette) chips(c *ui.Context) {
	for i, f := range p.filters {
		kit.PaletteChip(c.Key(f.kind), f.label, i18n.Tf("palette.filter.remove", f.label)).OnClick(func() { p.removeFilter(i) })
	}
}

// filterList is the palette's body while / shows the filters.
func (p *palette) filterList(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	opts := p.filterOpts()
	p.fsel = min(p.fsel, len(opts)-1)
	for i, f := range opts {
		if g := filterGroup(f.kind); i == 0 || g != filterGroup(opts[i-1].kind) {
			palHead(c, g)
		}
		r := kit.PaletteRow(c.Key(f.kind+":"+f.label), i == p.fsel).Label(f.label).Gap(th.Space(2.5)).OnClick(func() { p.addFilter(f, func() {}) })
		if i == p.fsel && p.scroll {
			r.ScrollIntoView()
		}
		r.Children(func() {
			if f.renderIcon != nil {
				f.renderIcon(c, i == p.fsel)
			} else if f.icon != "" {
				svg, color := icons.Get(f.icon), t.Muted
				if f.iconColor != nil {
					color = f.iconColor(t)
				} else if i == p.fsel {
					color = t.Foreground
				}
				if i == p.fsel {
					svg = icons.FillOr(f.icon)
				}
				ui.Icon(c, svg).Size(theme.IconSm, theme.IconSm).TextColor(color)
			}
			ui.Text(c, f.label).SingleLine()
		})
	}
	p.scroll = false
}
