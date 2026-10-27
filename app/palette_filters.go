package app

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
)

// palFilter is a filter the palette offers, and once chosen its chip: set
// puts it into searchFilters. A kind holds one filter at a time.
type palFilter struct {
	kind, label string
	set         func(*searchFilters)
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
		{"attach", i18n.T("mail.search.has_attachments"), func(f *searchFilters) { f.attach = true }},
		{"unread", i18n.T("mail.filter.unread"), func(f *searchFilters) { f.unread = true }},
		{"starred", i18n.T("palette.filter.starred"), func(f *searchFilters) { f.starred = true }},
	}
	for _, acc := range a.accounts {
		id := acc.ID
		out = append(out, palFilter{"account", i18n.Tf("palette.filter.account", acc.Email), func(f *searchFilters) { f.acc, f.accSet = id, true }})
	}
	for _, l := range a.labels {
		id := l.ID
		out = append(out, palFilter{"label", i18n.Tf("palette.filter.label", l.Name), func(f *searchFilters) { f.labelID = id }})
	}
	for i, key := range searchSpans[1:] {
		out = append(out, palFilter{"span", i18n.Tf("palette.filter.time", i18n.T(key)), func(f *searchFilters) { f.span = i + 1 }})
	}
	if from := strings.TrimSpace(p.query); from != "" {
		out = append(out, palFilter{"from", i18n.Tf("palette.filter.from", from), func(f *searchFilters) { f.from = from }})
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
// closes the list only.
func (p *palette) filterKey(k ui.TextKey) bool {
	opts := p.filterOpts()
	n := len(opts)
	switch {
	case k.Mods == 0 && (k.Key == ui.KeyDown || k.Key == ui.KeyUp):
		p.fsel = wrapStep(p.fsel, n, k.Key == ui.KeyUp)
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.addFilter(opts[min(p.fsel, n-1)], func() { k.SetText("") })
	case k.Mods == 0 && k.Key == ui.KeyEscape:
		p.filtering = false
	default:
		return false
	}
	return true
}

// chips are the filters chosen, before the search box.
func (p *palette) chips(c *ui.Context) {
	for i, f := range p.filters {
		kit.PaletteChip(c.Key(f.kind), f.label, i18n.Tf("palette.filter.remove", f.label)).OnClick(func() { p.removeFilter(i) })
	}
}

// filterList is the palette's body while / shows the filters.
func (p *palette) filterList(c *ui.Context) {
	th := c.Theme()
	opts := p.filterOpts()
	p.fsel = min(p.fsel, len(opts)-1)
	for i, f := range opts {
		kit.PaletteRow(c.Key(f.label), i == p.fsel).Label(f.label).Gap(th.Space(2.5)).OnClick(func() { p.addFilter(f, func() {}) }).Children(func() { ui.Text(c, f.label).SingleLine() })
	}
}
