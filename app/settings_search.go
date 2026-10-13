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

// settingsIndex is the sidebar search's hand-kept index: the catalog keys of
// each page's titles and rows ("account" is every account page).
// TestSettingsIndexCoversRows fails when a row is missing.
var settingsIndex = map[string][]string{
	"account":    {"settings.accounts", "settings.account.basic", "settings.account.name", "settings.account.name.help", "settings.account.alias", "settings.account.make_default", "settings.account.make_default.help", "settings.account.default", "settings.account.status.ok", "settings.account.signature", "settings.account.signature.help", "settings.account.signature.image", "settings.account.sync", "settings.account.sync.status", "settings.account.sync_now", "settings.account.sync.range", "settings.account.sync.2000", "settings.account.usage", "settings.account.usage.help", "settings.account.clear_cache", "settings.account.mailbox", "settings.account.mailbox.help", "settings.account.mailbox.none", "settings.account.mailbox.drafts.help", "settings.account.mailbox.trash.help", "settings.account.mailbox.archive.help", "mail.folder.drafts", "mail.folder.sent", "mail.folder.junk", "mail.folder.trash", "mail.folder.archive", "settings.account.purge.0", "settings.account.purge.junk", "settings.account.purge.junk.help", "settings.account.purge.trash", "settings.account.purge.trash.help", "settings.ext.skill.on.draft", "settings.account.login", "settings.account.login.method", "settings.account.servers", "settings.account.servers.help", "settings.account.edit_servers", "settings.ext.mcp.edit", "settings.account.remove", "settings.account.remove.button", "settings.account.remove.row"},
	"general":    {"settings.general", "settings.general.sub", "settings.appearance", "settings.theme", "settings.theme.help", "settings.theme.system", "settings.theme.light", "settings.theme.dark", "settings.density", "settings.density.compact", "settings.density.comfortable", "settings.font_size", "settings.body_font_size", "settings.body_font_size.help", "settings.body_font_size.ui", "settings.language", "settings.ui_language", "settings.ui_language.system", "settings.reading_language", "settings.reading_language.help", "settings.reading_language.ui", "settings.reply_language", "settings.reply_language.help", "settings.reply_language.match", "settings.reply_language.reading", "settings.no_translate", "settings.no_translate.help", "settings.no_translate.none", "settings.startup", "settings.launch_at_login", "settings.launch_at_login.help", "settings.keep_in_background", "settings.keep_in_background.help", "settings.unread_in_menu_bar", "settings.notifications", "settings.notify_new_mail", "settings.notify_preview", "settings.notify_preview.help", "settings.ai", "settings.summary", "settings.summary.help", "settings.quick_replies", "settings.quick_replies.help", "settings.mail", "settings.thread_view", "settings.thread_view.help"},
	"models":     {"settings.models", "settings.models.sub", "settings.models.add", "settings.models.usage", "settings.models.use.gen", "settings.models.use.gen.desc", "settings.models.use.small", "settings.models.use.small.desc", "settings.models.use.judge", "settings.models.use.judge.desc", "settings.models.services", "settings.models.foot", "settings.models.empty.title", "settings.models.empty.desc", "settings.models.local", "settings.models.test", "settings.models.api_key", "settings.models.base_url"},
	"labels":     {"settings.labels", "settings.labels.sub", "settings.labels.new", "settings.labels.list", "settings.labels.list.foot", "settings.labels.builtin", "settings.labels.no_desc", "settings.labels.auto", "settings.labels.auto.on", "settings.labels.auto.on.help", "settings.labels.model", "settings.labels.model.help", "settings.labels.model.value", "settings.labels.model.go", "settings.labels.rescan", "settings.labels.rescan.sub", "settings.labels.rescan.range", "settings.labels.rescan.all_range", "settings.labels.rescan.mode", "settings.labels.rescan.missing", "settings.labels.rescan.missing.help", "settings.labels.rescan.redo", "settings.labels.rescan.redo.help", "settings.labels.rescan.start", "settings.labels.rescan.stop", "settings.notifications"},
	"extensions": {"settings.extensions", "settings.ext.sub", "settings.ext.install", "settings.ext.plugins.empty", "settings.ext.tab.skills", "settings.ext.tab.plugins", "settings.ext.tab.mcp", "settings.ext.sec.skills", "settings.ext.sec.plugins", "settings.ext.mcp.add", "settings.ext.mcp.empty"},
	"privacy":    {"settings.privacy", "settings.privacy.sub", "settings.remote_images", "settings.remote_images.sub", "settings.remote_images.mode", "settings.remote_images.mode.help", "settings.remote_images.auto", "settings.remote_images.trusted", "settings.remote_images.ask", "settings.remote_images.cache", "settings.remote_images.cache.help", "settings.remote_images.clear", "settings.trusted", "settings.trusted.sub", "settings.trusted.none", "settings.local_api", "settings.local_api.on", "settings.local_api.help", "settings.keychain", "settings.keychain.checking"},
	"about":      {"settings.about", "settings.about.license"},
}

// searchHit is what the sidebar shows for a query: the app pages that match,
// and whether the accounts group does.
type searchHit struct {
	apps     []string
	accounts bool
	email    string // set when only accounts' emails matched: the sidebar lists those
}

func hasQuery(keys []string, q string) bool {
	for _, k := range keys {
		if strings.Contains(strings.ToLower(i18n.T(k)), q) {
			return true
		}
	}
	return false
}

// accountOptions are the labels of the account page's selects, formatted
// (最近 3 个月), which the index's keys cannot match.
func accountOptions() []string {
	var out []string
	for _, o := range rangeOptions() {
		out = append(out, o.Label)
	}
	for _, o := range purgeOptions() {
		out = append(out, o.Label)
	}
	return out
}

func (p *settingsPage) matching(query string) (m searchHit) {
	q := strings.ToLower(strings.TrimSpace(query))
	m.accounts = q == "" || hasQuery(settingsIndex["account"], q) ||
		slices.ContainsFunc(accountOptions(), func(l string) bool { return strings.Contains(strings.ToLower(l), q) })
	if !m.accounts && p.accountByEmail(q) != nil {
		m.accounts, m.email = true, q
	}
	for _, sec := range appSections {
		if q == "" || hasQuery(settingsIndex[sec], q) {
			m.apps = append(m.apps, sec)
		}
	}
	return
}

// accountByEmail is the first account whose email holds q, or nil.
func (p *settingsPage) accountByEmail(q string) *core.Account {
	if q == "" {
		return nil
	}
	for i, acc := range p.a.accounts {
		if strings.Contains(strings.ToLower(acc.Email), q) {
			return &p.a.accounts[i]
		}
	}
	return nil
}

// searchField is the sidebar's 搜索设置 field; Enter opens the first match.
func (p *settingsPage) searchField(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Height(th.Space(8)).AlignItems(ui.Center).Gap(th.Space(2)).PaddingX(th.Space(2.5)).Margin(0, 0, th.Space(1.5), 0).Radius(t.Radius.Md).Background(t.Window).Children(func() {
		ui.Icon(c, icons.Get("search")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
		in := kit.Input(c, &p.query, kit.InputOpts{Placeholder: i18n.T("settings.search"), Bare: true}).FocusRing(false).Label(i18n.T("settings.search")).Grow(1)
		if in.Submitted() {
			m := p.matching(p.query)
			switch {
			case m.accounts && len(p.a.accounts) > 0:
				acc := &p.a.accounts[0]
				if m.email != "" {
					acc = p.accountByEmail(m.email)
				}
				p.a.navigate(Route{Page: PageSettings, Settings: accountRoute(acc.ID)})
			case len(m.apps) > 0:
				p.a.navigate(Route{Page: PageSettings, Settings: m.apps[0]})
			}
		}
	})
}
