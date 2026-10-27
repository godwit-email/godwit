package app

import (
	"strings"

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
	if strings.TrimSpace(p.query) == "" && sf == (searchFilters{}) {
		return // nothing to search
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
	mp.closeSearch()
	mp.searchOpen, mp.query, mp.sf = true, p.query, sf
	mp.runSearch()
}

// viewAllItem is the row after the mail hits when some are left out.
func (p *palette) viewAllItem(total int) palItem {
	return palItem{icon: "list", title: i18n.Tf("palette.view_all", total), open: p.viewAll}
}
