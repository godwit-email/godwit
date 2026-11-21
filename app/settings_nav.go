package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// settingsNav is the Settings sidebar's page groups under the accounts, in
// order: each group's title (a catalog key; "" for the last, which has none)
// and its pages' Route.Settings keys.
var settingsNav = []struct {
	title string
	pages []string
}{
	{"settings.app", []string{"general", "signatures", "labels", "rules"}},
	{"settings.group.ai", []string{"models", "features", "skills", "mcp", "plugins", "clients"}},
	{"settings.privacy", privacyPages},
	{"", []string{"labs", "about"}},
}

// privacyPages are the pages of 隐私与安全.
var privacyPages = []string{"masking", "security", "images", "blocked"}

func isPrivacyPage(sec string) bool { return slices.Contains(privacyPages, sec) }

// settingsPages are the pages' names (catalog keys) and sidebar icons, the
// line style of each; the selected row shows its fill style where it has one.
var settingsPages = map[string]struct{ title, icon string }{
	"general":    {"settings.general", "settings"},
	"signatures": {"settings.signatures", "quill-pen"},
	"labels":     {"settings.labels", "tag"},
	"rules":      {"settings.rules", "filter"},
	"models":     {"settings.ai.tab.models", "brain-4"},
	"features":   {"settings.ai", "sparkles"},
	"skills":     {"settings.ext.sec.skills", "file-pen"},
	"mcp":        {"settings.mcp", "cpu"},
	"plugins":    {"settings.plugins", "puzzle"},
	"clients":    {"settings.ai.tab.apps", "component"},
	"masking":    {"settings.masking", "eye-off"},
	"security":   {"settings.security", "shield-check"},
	"images":     {"settings.remote_images", "image-off"},
	"blocked":    {"settings.blocked", "mail-forbid"},
	"labs":       {"settings.labs", "flask"},
	"about":      {"settings.about", "info"},
}

// appSections are the app pages' keys, in the sidebar's order.
var appSections = func() (all []string) {
	for _, g := range settingsNav {
		all = append(all, g.pages...)
	}
	return all
}()

// legacySettingsRoutes are the keys of pages that were split into several: a
// route saved or linked with one opens the first of its pages.
var legacySettingsRoutes = map[string]string{"extensions": "skills", "privacy": "masking"}

// pageName is the name of the app page sec, as its sidebar row and title show it.
func pageName(sec string) string {
	if pg, ok := settingsPages[sec]; ok {
		return i18n.T(pg.title)
	}
	return i18n.T("settings." + sec)
}

// settingsParent is the sidebar page that the page sec belongs to: a
// signature is a page of 签名, 脱敏记录 one of 隐私脱敏.
func settingsParent(sec string) string {
	switch {
	case strings.HasPrefix(sec, "signature:"):
		return "signatures"
	case sec == "masklog":
		return "masking"
	}
	return sec
}

// showsSkills reports whether the page open lists the skills, which a skill
// event changes.
func (p *settingsPage) showsSkills() bool {
	sec, _ := p.section()
	return sec == "skills" || sec == "plugins"
}

// Breadcrumb names the sub-page open and the page it belongs to; ok is false
// on a page of the sidebar.
func (p *settingsPage) Breadcrumb() (parent, current string, ok bool) {
	sec, _ := p.section()
	switch {
	case sec == "masklog":
		return pageName("masking"), i18n.T("settings.masking.log"), true
	case sec == "plugins" && p.ext.onDirectory && p.ext.reg.On:
		return pageName("plugins"), i18n.T("settings.ext.directory"), true
	case sec == "rules" && p.rules.editing:
		return pageName("rules"), p.rules.editorTitle(), true
	}
	if sig, ok := p.shownSignature(sec); ok {
		return pageName("signatures"), sig.Name, true
	}
	return "", "", false
}

// BreadcrumbUp is the breadcrumb's parent, clicked: up from the subpage open.
// A signature or 脱敏记录 is a route of its own, and the history knows no way
// to its parent (a signature opened from a link, say), so this goes there; the
// plugin directory and the rule editor close in place.
func (p *settingsPage) BreadcrumbUp() {
	sec, _ := p.section()
	switch sec {
	case "plugins":
		p.ext.onDirectory = false
	case "rules":
		p.rules.closeEditor()
	default:
		p.a.navigate(Route{Page: PageSettings, Settings: settingsParent(sec)})
	}
}

// pageIcon is a page row's icon: muted in the line style; selected, in the
// foreground's fill style (the line style where the icon has none).
func pageIcon(c *ui.Context, icon string, selected bool) {
	t := theme.Of(c)
	svg, color := icons.Get(icon), t.Muted
	if selected {
		svg, color = icons.FillOr(icon), t.Foreground
	}
	ui.Icon(c, svg).Size(theme.Icon, theme.Icon).TextColor(color)
}

// Sidebar: the accounts group (mail accounts and address books with no mail
// account, each with its mark, a status dot and what it syncs, and 添加账户
// in its title), then the pages, a group each. A search keeps the pages that
// match.
func (p *settingsPage) Sidebar(c *ui.Context) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	cur, _ := p.section()
	cur = settingsParent(cur)
	sidebarScroll(c, func() {
		if !splitChrome() {
			p.searchField(c)
		}
		hit := p.matching(p.query)
		if hit.accounts {
			p.accountsGroup(c, hit, cur)
		}
		for _, g := range settingsNav {
			pages := slices.DeleteFunc(slices.Clone(g.pages), func(sec string) bool { return !slices.Contains(hit.apps, sec) })
			if len(pages) == 0 {
				continue
			}
			column := ui.Column(c).FillWidth().Gap(th.Space(0.5))
			if g.title == "" {
				column.Padding(th.Space(2), 0, 0, 0)
			}
			column.Children(func() {
				if g.title != "" {
					kit.Text(c, i18n.T(g.title), t.Text.Xs).TextColor(t.Muted).Padding(th.Space(3), th.Space(2), th.Space(1), th.Space(2))
				}
				for _, sec := range pages {
					b := kit.SidebarItem(c, kit.SidebarItemOpts{Selected: sec == cur, OnClick: func() { a.navigate(Route{Page: PageSettings, Settings: sec}) }})
					b.Children(func() {
						pageIcon(c, settingsPages[sec].icon, sec == cur)
						ui.Text(c, pageName(sec))
					})
				}
			})
		}
	})
}

// accountsGroup is the sidebar's accounts: a title with 添加账户 at its end,
// then a row per account or address book, dragged to reorder.
func (p *settingsPage) accountsGroup(c *ui.Context, hit searchHit, cur string) {
	a := p.a
	t, th := theme.Of(c), c.Theme()
	gap := th.Space(0.5) // the sidebar column's
	ui.Column(c).FillWidth().Gap(gap).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Padding(gap, 0, gap, th.Space(2)).Children(func() { // the button's height makes the title's
			kit.Text(c, i18n.T("settings.accounts"), t.Text.Xs).TextColor(t.Muted).Grow(1)
			kit.IconButton(c, "plus", i18n.T("settings.add_account"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs}).OnClick(a.openAddAccount)
		})
		entries := a.accountEntries()
		entry := func(e accountEntry) {
			route := accountRoute(e.key().Account)
			if e.ab != nil {
				route = addressBookRoute(e.ab.ID)
			}
			p.entryRow(c, e, route == cur, route)
		}
		if n := len(entries); hit.email == "" && n > 1 { // dragged to reorder; not while a search hides some
			p.accountOrder.Key = func(i int) any { return entries[i].key() }
			p.accountOrder.Reorder = func(rows []int, to int) { p.moveEntry(rows[0], to) }
			kit.List(c, &p.accountOrder, n, func(i int) {
				var top float32
				if i > 0 {
					top = gap
				}
				ui.Column(c).FillWidth().Padding(top, 0, 0, 0).Children(func() { entry(entries[i]) })
			}).FillWidth().Height(float32(n)*(th.Space(11)+gap) - gap)
			return
		}
		for _, e := range entries {
			if hit.email == "" || e.acc != nil && accountMatches(*e.acc, hit.email) || e.ab != nil && strings.Contains(strings.ToLower(e.ab.Name), hit.email) {
				entry(e)
			}
		}
	})
}
