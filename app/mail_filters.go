package app

import (
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
	fromDraft string // the sender box while its menu is open

	accOpen, fromOpen, spanOpen bool
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
		Since: spanStart(p.a.clock.Now(), p.sf.span), HasAttachments: p.sf.attach}
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
	})
}

// setFilter changes the filters, closes their menus and searches again.
func (p *mailPage) setFilter(change func(*searchFilters)) {
	change(&p.sf)
	p.sf.accOpen, p.sf.fromOpen, p.sf.spanOpen = false, false, false
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
