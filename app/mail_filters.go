package app

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// searchFilters are the chips under the search box. The zero value filters
// nothing and searches the view's account.
type searchFilters struct {
	acc       int64 // the account chosen, once accSet; 0 is every account
	accSet    bool
	from      string // a substring of the sender
	span      int    // a searchSpans index; 0 is any time
	attach    bool
	labelID   int64  // a label the mail must have; 0 is any
	fromDraft string // the sender box while its menu is open

	accOpen, fromOpen, spanOpen, labelOpen bool
}

// searchSpans are the time chip's options, by i18n key. The design names
// the chip only: the options are ours.
var searchSpans = []string{"mail.search.any_time", "mail.search.today", "mail.search.week", "mail.search.month", "mail.search.year"}

// spanStart is when span starts, in now's local days; zero for any time.
func spanStart(now time.Time, span int) time.Time {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch span {
	case 1:
		return today
	case 2:
		return today.AddDate(0, 0, -6)
	case 3:
		return today.AddDate(0, 0, -29)
	case 4:
		return time.Date(y, 1, 1, 0, 0, 0, 0, now.Location())
	}
	return time.Time{}
}

// searchAccount is the account searched: the chip's choice, else the view's.
func (p *mailPage) searchAccount() int64 {
	if p.sf.accSet {
		return p.sf.acc
	}
	return p.viewAccount(p.view)
}

// searchQuery is the box's text with the chips' filters.
func (p *mailPage) searchQuery() core.SearchQuery {
	return core.SearchQuery{AccountID: p.searchAccount(), Text: strings.TrimSpace(p.query), From: strings.TrimSpace(p.sf.from),
		Since: spanStart(p.a.clock.Now(), p.sf.span), HasAttachments: p.sf.attach, LabelID: p.sf.labelID}
}

// filterChips is the row of filters under the search box; a change searches
// again.
func (p *mailPage) filterChips(c *ui.Context) {
	th := c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(1)).Role(ui.RoleGroup).Label(i18n.T("mail.search.filters")).Children(func() {
		scope := i18n.T("mail.search.all_accounts")
		if acc := p.searchAccount(); acc != 0 {
			if a, ok := p.a.accountByID(acc); ok {
				scope = a.Email
			}
		}
		acc := filterChip(c, scope, false, &p.sf.accOpen, nil)
		kit.Popover(c, acc, &p.sf.accOpen, func() {
			cur := p.searchAccount()
			filterItem(c, i18n.T("mail.search.all_accounts"), cur == 0, func() { p.setFilter(func(f *searchFilters) { f.acc, f.accSet = 0, true }) })
			for _, a := range p.a.accounts {
				id := a.ID
				filterItem(c.Key(id), a.Email, cur == id, func() { p.setFilter(func(f *searchFilters) { f.acc, f.accSet = id, true }) })
			}
		})

		from := i18n.T("mail.search.from")
		if p.sf.from != "" {
			from = p.sf.from
		}
		sender := filterChip(c, from, p.sf.from != "", &p.sf.fromOpen, func() { p.sf.fromDraft = p.sf.from })
		kit.Popover(c, sender, &p.sf.fromOpen, func() {
			in := kit.Input(c, &p.sf.fromDraft, kit.InputOpts{Placeholder: i18n.T("mail.search.from_placeholder")}).Width(theme.FilterMenuMin).AutoFocus().Label(i18n.T("mail.search.from"))
			if in.Submitted() {
				d := strings.TrimSpace(p.sf.fromDraft)
				p.setFilter(func(f *searchFilters) { f.from = d })
			}
			filterItem(c, i18n.T("mail.search.any_sender"), p.sf.from == "", func() { p.setFilter(func(f *searchFilters) { f.from = "" }) })
		})

		span := i18n.T("mail.search.time")
		if p.sf.span != 0 {
			span = i18n.T(searchSpans[p.sf.span])
		}
		when := filterChip(c, span, p.sf.span != 0, &p.sf.spanOpen, nil)
		kit.Popover(c, when, &p.sf.spanOpen, func() {
			for i, key := range searchSpans {
				filterItem(c.Key(key), i18n.T(key), p.sf.span == i, func() { p.setFilter(func(f *searchFilters) { f.span = i }) })
			}
		})

		filterChip(c, i18n.T("mail.search.has_attachments"), p.sf.attach, nil, func() {
			p.setFilter(func(f *searchFilters) { f.attach = !f.attach })
		}).Checked(p.sf.attach)
		p.labelFilter(c)
	})
}

// labelFilter is the label chip and its menu: any label, or one.
func (p *mailPage) labelFilter(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	cur, set := p.a.label(p.sf.labelID)
	set = set && p.sf.labelID != 0
	b := kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { p.sf.labelOpen = !p.sf.labelOpen }}).
		Label(i18n.T("mail.filter.label.name")).Expanded(p.sf.labelOpen).Height(th.Space(6)).PaddingX(th.Space(2)).Gap(th.Space(1)).Radius(t.Radius.Md)
	if set {
		b.Background(t.SidebarSelected)
	}
	b.Children(func() {
		if set {
			ui.Icon(c, icons.Get("label-"+cur.Icon)).Size(theme.IconXs, theme.IconXs).TextColor(t.Label(cur.Color).Text)
			kit.Text(c, cur.Name, t.Text.Sm).SingleLine()
		} else {
			kit.Text(c, i18n.T("mail.filter.label"), t.Text.Sm).SingleLine()
		}
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted)
	})
	kit.Popover(c, b, &p.sf.labelOpen, func() {
		filterItem(c, i18n.T("mail.filter.label.any"), !set, func() { p.setFilter(func(f *searchFilters) { f.labelID = 0 }) })
		for _, l := range p.a.labels {
			id, on := l.ID, set && l.ID == cur.ID
			kit.Pressable(c.Key(id), kit.PressableOpts{Selected: on, OnClick: func() { p.setFilter(func(f *searchFilters) { f.labelID = id }) }}).
				Label(i18n.Tf("mail.filter.label.item", l.Name)).Checked(on).MinWidth(theme.FilterMenuMin).Padding(th.Space(1.5), th.Space(2)).
				Gap(th.Space(2)).AlignItems(ui.Center).Radius(t.Radius.Md).Children(func() {
				ui.Icon(c, icons.Get("label-"+l.Icon)).Size(theme.IconXs, theme.IconXs).TextColor(t.Label(l.Color).Text)
				kit.Text(c, l.Name, t.Text.Sm).SingleLine()
			})
		}
	})
}

// labelsLoaded drops a label filter whose label is gone, and searches again:
// kept, it would filter by nothing the chip shows and find nothing.
func (p *mailPage) labelsLoaded() {
	a := p.a
	gone := func(r Route) bool { return a.goneLabel(r) }
	a.history, a.ahead = slices.DeleteFunc(a.history, gone), slices.DeleteFunc(a.ahead, gone)
	if a.goneLabel(a.route) {
		a.setRoute(a.route) // to the inbox
	}
	if _, ok := a.label(p.sf.labelID); ok || p.sf.labelID == 0 {
		return
	}
	p.sf.labelID = 0
	if p.searched {
		p.runSearch()
	}
}

// setFilter changes the filters, closes their menus and searches again.
func (p *mailPage) setFilter(change func(*searchFilters)) {
	change(&p.sf)
	p.sf.accOpen, p.sf.fromOpen, p.sf.spanOpen, p.sf.labelOpen = false, false, false, false
	p.runSearch()
}

// filterChip is a chip: a toggle when open is nil, else the button of the
// menu *open shows, its label the value chosen. on marks a filter set.
func filterChip(c *ui.Context, label string, on bool, open *bool, click func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() {
		if open != nil {
			*open = !*open
		}
		if click != nil {
			click()
		}
	}}).Label(label).Height(th.Space(6)).PaddingX(th.Space(2)).Gap(th.Space(1)).Radius(t.Radius.Md)
	if on {
		b.Background(t.SidebarSelected)
	}
	if open != nil {
		b.Expanded(*open)
	}
	b.Children(func() {
		kit.Text(c, label, t.Text.Sm).SingleLine()
		if open != nil {
			ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted)
		}
	})
	return b
}

// filterItem is an option in a chip's menu; the chosen one is marked.
func filterItem(c *ui.Context, label string, on bool, click func()) {
	t, th := theme.Of(c), c.Theme()
	kit.Pressable(c, kit.PressableOpts{Selected: on, OnClick: click}).Label(label).Checked(on).
		MinWidth(theme.FilterMenuMin).Padding(th.Space(1.5), th.Space(2)).Radius(t.Radius.Md).Children(func() {
		kit.Text(c, label, t.Text.Sm).SingleLine()
	})
}
