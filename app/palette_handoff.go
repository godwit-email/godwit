package app

import (
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// askWit closes the palette and hands its text to Wit, as Tab does.
func (p *palette) askWit() {
	p.open = false
	p.a.askWit(p.query)
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
	mp := p.a.mail
	v := mp.view
	if v == (core.View{}) {
		v = core.View{Unified: "inbox"} // the mail page not shown yet
	}
	p.a.navigate(Route{Page: PageMail, View: v})
	mp.searchFor(p.query, sf)
}

// viewAllItem is the row after the mail hits when some are left out.
func (p *palette) viewAllItem(total int) palItem {
	return palItem{icon: "list", title: i18n.Tf("palette.view_all", total), kbd: "⌘↵", open: p.viewAll}
}
