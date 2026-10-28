package app

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
)

// askWit closes the palette and asks Wit its text, as ↵ does while asking.
func (p *palette) askWit() {
	if strings.TrimSpace(p.query) == "" {
		return
	}
	p.open = false
	p.a.askWit(p.query)
}

// toggleAsk turns the box between the search and a question to Wit, the
// text kept, as Tab does.
func (p *palette) toggleAsk() {
	p.asking, p.m.open, p.filtering = !p.asking, false, false
}

// askKey is the box's while asking: ↵ asks, ⇧↵ breaks the line, Tab goes
// back to the search.
func (p *palette) askKey(k ui.TextKey) bool {
	switch {
	case k.Mods == 0 && k.Key == ui.KeyEnter:
		p.askWit()
	case k.Mods == 0 && k.Key == ui.KeyTab:
		p.toggleAsk()
	default:
		return false
	}
	return true
}

// viewAll closes the palette on the list's search for its text and filters,
// as ⌘↵ does: every mail hit, in every account as the palette searched.
func (p *palette) viewAll() {
	sf := p.sf()
	// the list's own test: an account alone is nothing to search by there
	if q := sf.query(p.a.clock.Now(), sf.acc, p.query); q.Text == "" && !q.Filtered() {
		return
	}
	if !sf.accSet {
		sf.acc, sf.accSet = 0, true
	}
	p.open = false
	p.a.mail.searchFor(p.query, sf)
}

// viewAllItem is the row after the mail hits when some are left out.
func (p *palette) viewAllItem(total int) palItem {
	return palItem{icon: "list", title: i18n.Tf("palette.view_all", total), kbd: "⌘↵", open: p.viewAll}
}
