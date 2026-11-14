package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// extView is the Extensions page: skills, plugins and MCP servers in three
// tabs. Writes chain per kind ("skills", "plugins", "mcp"); a result for a
// form replaced since leaves the new form alone.
type extView struct {
	a               *App
	tab             int    // 0 skills, 1 plugins (the first shown); MCP is on the Intelligence page
	list            latest // the plugins and MCP servers; only the newest read applies
	skillsRead      latest // a skill write drops it: an old read cannot replace a toggle
	skills          []core.SkillInfo
	plugins         []core.PluginInfo
	servers         []core.MCPServer
	plugin          string                 // the installed plugin open; "" none
	cfgs            map[string]*pluginForm // each plugin's settings form, read once; absent while it loads
	cfgBusy         map[string]bool        // a form read is in flight
	mcp             int64                  // the server shown
	mform           *mcpForm               // the add or edit form; nil when closed
	later           map[string]bool        // pending plugins collapsed with 暂不启用, this session only
	confirm         string                 // the open confirmation: "plugin", "mcp", "login"; "" none
	reg             core.RegistryView      // the plugin directory
	onDirectory     bool                   // the plugin directory's subpage shows
	detail          string                 // the directory entry whose install dialog shows; "" none
	detailOpen      bool                   // that dialog shows
	readmes         map[string]*pluginReadme
	open            bool   // the confirmation shows
	installing      string // ":folder" for a local install, otherwise the registry plugin name; "" when idle
	installProgress core.PluginInstallProgress
	logins          map[string]core.PluginLogin // what each sign-in plugin answered
	loginFlow       string                      // the plugin signing in; "" none
	loginCancel     context.CancelFunc          // gives up on that sign-in
	loginPage       string                      // the page awaiting the user's yes; "" none
	loginPageOpen   bool                        // its dialog shows
	loginAnswer     chan bool                   // receives that yes or no
}

// extChange: the event changes what the page lists.
func extChange(ev events.Event) bool {
	return ev.Type == events.PluginsChanged || ev.Type == events.MCPChanged
}

func (e *extView) loadSkills() {
	if len(e.a.writes.chains["skills"]) > 0 {
		e.skillsRead.Stop()
		return // 等待队列中所有开关写回完成，再读取最终状态。
	}
	var skills []core.SkillInfo
	e.skillsRead.Run(e.a, func(ctx context.Context) (err error) {
		skills, err = e.a.svc.ListSkills(ctx)
		return err
	}, e.a.here(func(err error) {
		if err != nil {
			e.a.toastErr(err)
			return
		}
		e.skills = skills
	}))
}

func (e *extView) load() {
	e.loadSkills()
	a := e.a
	var pl []core.PluginInfo
	var ms []core.MCPServer
	var rv core.RegistryView
	e.list.Run(a, func(ctx context.Context) (err error) {
		if rv, err = a.svc.ListPluginRegistry(ctx); err != nil { // a cache that fails its signature: say so, list nothing
			rv, err = core.RegistryView{On: true, Stale: err.Error()}, nil
		}
		if pl, err = a.svc.ListPlugins(ctx); err != nil {
			return err
		}
		ms, err = a.svc.ListMCPServers(ctx)
		return err
	}, a.here(func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		e.plugins, e.servers, e.reg = pl, ms, rv
		e.loadLogins()
		for _, p := range pl {
			if !p.Builtin { // no config, nothing installed
				e.loadCfg(p.Name)
			}
		}
		for n := range e.cfgs { // uninstalled
			if !slices.ContainsFunc(pl, func(p core.PluginInfo) bool { return p.Name == n }) {
				delete(e.cfgs, n)
			}
		}
		if _, ok := e.pluginInfo(); !ok {
			e.plugin = ""
		}
		if _, ok := e.server(); !ok && len(ms) > 0 {
			e.mcp = ms[0].ID
		}
	}))
}

// signsIn reports whether p offers a sign-in now.
func signsIn(p core.PluginInfo) bool {
	return p.Enabled && !p.NeedsGrant && slices.Contains(p.Capabilities, "login")
}

// loadLogins asks each sign-in plugin apart from the list: its process may
// have to start. One that cannot answer offers nothing, and shows its error.
// The next read of the list drops the answers.
func (e *extView) loadLogins() {
	a := e.a
	for _, p := range e.plugins {
		if !signsIn(p) {
			continue
		}
		name := p.Name
		var st core.PluginLogin
		e.list.Also(a, func(ctx context.Context) (err error) {
			st, err = a.svc.PluginLoginStatus(ctx, name)
			return err
		}, func(err error) {
			if err != nil {
				delete(e.logins, name)
				return
			}
			if e.logins == nil {
				e.logins = map[string]core.PluginLogin{}
			}
			e.logins[name] = st
		})
	}
}

// startLogin runs a sign-in to its end: a page opened once, then polling,
// until it ends or the user gives up.
func (e *extView) startLogin(name string) {
	a := e.a
	ctx, cancel := context.WithTimeout(a.ctx, auth.FlowTimeout)
	e.loginFlow, e.loginCancel = name, cancel
	a.do(func(context.Context) error {
		flow, err := a.svc.BeginPluginLogin(ctx, name)
		if err != nil {
			return err
		}
		finished, page := false, ""
		answer := make(chan bool, 1)
		defer func() {
			if !finished { // the plugin would otherwise go on waiting for the browser
				a.svc.CancelPluginLogin(context.WithoutCancel(ctx), name, flow)
			}
		}()
		for {
			st, err := a.svc.PollPluginLogin(ctx, name, flow)
			if err != nil {
				return err
			}
			switch st.State {
			case "open":
				if page == "" { // the page the user agrees to is the one that opens
					page = st.URL
					a.onUI(func() { e.loginPage, e.loginPageOpen, e.loginAnswer = page, true, answer })
				}
			case "done":
				finished = true
				return nil
			case "failed":
				finished = true
				return errors.New(st.Message)
			}
			// Polling goes on while the user reads: it keeps the plugin from
			// going idle. On the app's clock, as its other waits are.
			tick := make(chan struct{}, 1)
			stop := a.clock.AfterFunc(time.Second, func() { tick <- struct{}{} })
			select {
			case <-ctx.Done():
				stop()
				return ctx.Err()
			case yes := <-answer:
				stop()
				if !yes {
					return context.Canceled
				}
				if err := a.plat.OpenURL(page); err != nil {
					return err
				}
			case <-tick:
			}
		}
	}, func(err error) {
		cancel()
		e.loginFlow, e.loginCancel, e.loginPage, e.loginPageOpen, e.loginAnswer = "", nil, "", false, nil
		if !errors.Is(err, context.Canceled) {
			a.toastErr(err)
		}
		e.load()
	})
}

// refreshRegistry fetches the plugin directory in the background and lists
// again; a failure shows as the directory's stale note, not a toast.
func (e *extView) refreshRegistry() {
	a := e.a
	a.do(func(ctx context.Context) error {
		a.svc.RefreshPluginRegistry(ctx) // ErrRegistryOff at once when there is none
		return nil
	}, func(error) { e.load() })
}

func (e *extView) pluginInfo() (core.PluginInfo, bool) {
	i := slices.IndexFunc(e.plugins, func(p core.PluginInfo) bool { return p.Name == e.plugin })
	if i < 0 {
		return core.PluginInfo{}, false
	}
	return e.plugins[i], true
}

func (e *extView) server() (core.MCPServer, bool) {
	i := slices.IndexFunc(e.servers, func(s core.MCPServer) bool { return s.ID == e.mcp })
	if i < 0 {
		return core.MCPServer{}, false
	}
	return e.servers[i], true
}

// write chains fn on key, then shows its error and reads the lists again.
func (e *extView) write(key string, fn func(ctx context.Context) error, done func(err error)) {
	a := e.a
	if key == "skills" {
		e.skillsRead.Stop()
	}
	a.writes.Chain(key, fn, func(err error) {
		a.toastErr(err)
		if done != nil {
			done(err)
		}
		if key == "skills" {
			if len(a.writes.chains[key]) == 1 {
				a.onUI(e.loadSkills) // Chain 在当前回调之后才移除已完成的写入。
			}
		} else {
			e.load()
		}
	})
}

func (e *extView) view(c *ui.Context) {
	if e.onDirectory && e.reg.On {
		e.directoryView(c)
		return
	}
	pageHeading(c, i18n.T("settings.extensions"), i18n.T("settings.ext.sub"))
	kit.TabsUnderline(c, &e.tab, i18n.Tf("settings.ext.tab.skills", len(e.skills)), i18n.Tf("settings.ext.tab.plugins", len(e.plugins)))
	if e.tab == 0 {
		e.skillsView(c)
	} else {
		e.pluginsView(c)
	}
	e.confirmView(c)
	e.loginPageView(c)
}

// --- skills

var skillTargets = []string{"message", "thread", "selection", "draft"}

func (e *extView) skillsView(c *ui.Context) {
	rows := make([]func(), len(e.skills))
	for i, s := range e.skills {
		rows[i] = func() {
			desc := s.Description
			if s.Error != "" {
				desc = i18n.Tf("settings.ext.skill.error", s.Error)
			} else if desc == "" {
				desc = skillOn(s)
			}
			if s.Builtin {
				desc += " · " + i18n.T("settings.ext.builtin")
			}
			if s.ManagedBy != "" {
				desc += " · " + i18n.Tf("settings.managed_by", s.ManagedBy)
			}
			settingRow(c.Key(s.Name), s.Label(), desc, func() {
				on := s.Enabled
				if kit.Switch(c, &on).Label(i18n.Tf("settings.ext.skill.enable", s.Label())).Changed() {
					on = !s.Enabled // the switch may have written an older frame's copy
					e.skills[i].Enabled = on
					name := s.Name
					e.write("skills", func(ctx context.Context) error { return e.a.svc.SetSkillEnabled(ctx, name, on) }, nil)
				}
			})
		}
	}
	settingsSection(c, i18n.T("settings.ext.sec.skills"), "", rows...)
}

func skillOn(s core.SkillInfo) string {
	var on []string
	for _, k := range skillTargets {
		if slices.Contains(s.On, k) {
			on = append(on, i18n.T("settings.ext.skill.on."+k))
		}
	}
	if len(on) == 0 {
		return i18n.T("settings.ext.skill.wit_only")
	}
	return i18n.Tf("settings.ext.skill.on", strings.Join(on, i18n.T("mail.list_sep")))
}

// --- plugins

// install asks for a plugin folder off the UI thread, then installs it.
func (e *extView) install() {
	if e.installing != "" {
		return
	}
	a, plat := e.a, e.a.plat
	e.installing = ":folder"
	e.installProgress = core.PluginInstallProgress{}
	var dir string
	a.do(func(context.Context) (err error) {
		dir, err = plat.OpenDirectory()
		return err
	}, func(err error) {
		if err != nil || dir == "" {
			e.installing = ""
			a.toastErr(err)
			return
		}
		var info core.PluginInfo
		e.write("plugins", func(ctx context.Context) (err error) {
			info, err = a.svc.InstallPlugin(ctx, dir)
			return err
		}, func(err error) {
			e.installing = ""
			if err == nil {
				e.tab = 1
				e.plugin = info.Name
			}
		})
	})
}

// permissionList names everything a script plugin may do, then what it may not.
func permissionList(c *ui.Context, pm plugins.Permissions, capabilities []string) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(1.5)).Children(func() {
		perm := func(tag, text string, muted bool) {
			r := ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center)
			if muted {
				r.TextColor(t.Muted)
			}
			r.Children(func() {
				kit.BadgeWith(c, tag, kit.BadgeOpts{Mono: true})
				kit.Text(c, text, t.Text.Sm).LineHeight(theme.LineFor(text)).Shrink(1)
			})
		}
		if slices.Contains(pm.Mail, "read") {
			perm("mail:read", i18n.T("settings.ext.perm.mail_read"), false)
		}
		if slices.Contains(pm.Mail, "write") {
			perm("mail:write", i18n.T("settings.ext.perm.mail_write"), false)
		}
		if len(pm.Net) > 0 {
			perm("net", i18n.Tf("settings.ext.perm.net", strings.Join(pm.Net, i18n.T("mail.list_sep"))), false)
		}
		if slices.Contains(capabilities, "translate") { // it gets the mail the user translates, and may send it to its hosts unasked
			perm("translate", i18n.T("settings.ext.perm.translate"), false)
		}
		if pm.LLM {
			perm("llm", i18n.T("settings.ext.perm.llm"), false)
		}
		if len(pm.Mail) == 0 && len(pm.Net) == 0 && !pm.LLM {
			ui.Text(c, i18n.T("settings.ext.perm.none")).TextColor(t.Muted)
		}
		perm(i18n.T("settings.ext.excluded"), pluginExcluded(pm), true)
	})
}

// pluginExcluded is the 不包含 line: what the plugin did not ask for. Sending
// is always named: no plugin can send.
func pluginExcluded(pm plugins.Permissions) string {
	var no []string
	if !slices.Contains(pm.Mail, "read") {
		no = append(no, i18n.T("settings.ext.excl.read"))
	}
	if !slices.Contains(pm.Mail, "write") {
		no = append(no, i18n.T("settings.ext.excl.write"))
	}
	if len(pm.Net) == 0 {
		no = append(no, i18n.T("settings.ext.excl.net"))
	}
	if !pm.LLM {
		no = append(no, i18n.T("settings.ext.excl.llm"))
	}
	no = append(no, i18n.T("settings.ext.excl.send"))
	return strings.Join(no, i18n.T("mail.list_sep"))
}

// pluginsView: 已安装, a settings row per plugin (name, description, switch); a
// plugin awaiting consent opens to its permissions, config and 允许并启用,
// an installed one to its permissions, config and uninstall on a click. Then
// 添加插件: the plugin directory's subpage and a folder.
func (e *extView) pluginsView(c *ui.Context) {
	t := theme.Of(c)
	rows := make([]func(), len(e.plugins))
	for i, p := range e.plugins {
		rows[i] = func() { e.pluginRow(c.Key(p.Name), p) }
	}
	if len(rows) > 0 {
		settingsSection(c, i18n.T("settings.ext.sec.installed"), "", rows...)
	} else {
		ui.Text(c, i18n.T("settings.ext.plugins.empty")).TextColor(t.Muted)
	}
	var add []func()
	if e.reg.On {
		add = append(add, func() {
			kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { e.onDirectory = true }}).Label(i18n.T("settings.ext.browse")).FillWidth().Children(func() {
				settingRow(c, i18n.T("settings.ext.browse"), i18n.T("settings.ext.browse.hint"), func() {
					ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
				})
			})
		})
	}
	add = append(add, func() {
		settingRow(c, i18n.T("settings.ext.install"), i18n.T("settings.ext.install.hint"), func() {
			if e.installing == ":folder" {
				e.installProgressView(c)
				return
			}
			kit.Button(c, i18n.T("settings.ext.install.choose"), kit.ButtonOpts{Variant: kit.Outline, Disabled: e.installing != ""}).OnClick(e.install)
		})
	})
	settingsSection(c, i18n.T("settings.ext.sec.add"), i18n.T("settings.ext.no_send"), add...)
}

// entry is the directory's entry for an installed plugin.
func (e *extView) entry(name string) (core.RegistryEntry, bool) {
	i := slices.IndexFunc(e.reg.Entries, func(r core.RegistryEntry) bool { return r.Name == name })
	if i < 0 {
		return core.RegistryEntry{}, false
	}
	return e.reg.Entries[i], true
}

func (e *extView) pluginRow(c *ui.Context, p core.PluginInfo) {
	t, th := theme.Of(c), c.Theme()
	name, title := p.Name, p.Name
	open := p.Name == e.plugin && !p.Builtin // a built-in one has nothing to open: no permissions, config or uninstall
	if p.NeedsGrant {
		open = !e.later[name]
	}
	if p.Builtin {
		title = p.Title.For(i18n.Language())
	}
	ui.Column(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
			kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() {
				switch {
				case p.NeedsGrant:
					if e.later == nil {
						e.later = map[string]bool{}
					}
					e.later[name] = open
				case open:
					e.plugin = ""
				default:
					e.plugin = name
				}
			}}).Label(title).Expanded(open).Grow(1).Shrink(1).Children(func() {
				ui.Column(c).Gap(th.Space(0.5)).Shrink(1).Children(func() {
					ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
						if p.Native {
							statusPill(c, i18n.T("settings.ext.native"), false)
						}
						if p.Builtin {
							statusPill(c, i18n.T("settings.ext.builtin"), false)
						}
						kit.Text(c, title, t.Text.Sm).LineHeight(theme.LineFor(title)).FontWeight(500)
						if p.NeedsGrant {
							kit.Text(c, p.Version, t.Text.Xs).TextColor(t.Muted)
						}
					})
					if d := p.Description.For(i18n.Language()); d != "" { // untrusted length: one line, all of it in the tooltip
						kit.Tooltip(c, kit.Text(c, d, t.Text.Xs).TextColor(t.Muted).SingleLine().Shrink(1), d)
					}
				})
			})
			if p.NeedsGrant {
				statusPill(c, i18n.T("settings.ext.status.pending"), true)
				return
			}
			if e.installing == name {
				e.installProgressView(c)
				return
			}
			if en, ok := e.entry(name); ok && en.Update {
				e.updateButton(c, en)
			}
			if st, ok := e.logins[name]; signsIn(p) && ok && !st.SignedIn {
				if e.loginFlow == name {
					kit.Button(c, i18n.T("settings.ext.login.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { e.loginCancel() })
				} else {
					kit.Button(c, i18n.T("settings.ext.login"), kit.ButtonOpts{Variant: kit.Outline, Disabled: e.loginFlow != ""}).
						OnClick(func() { e.plugin, e.confirm, e.open = name, "login", true })
				}
			}
			on := p.Enabled
			if kit.Switch(c, &on).Label(i18n.Tf("settings.ext.skill.enable", title)).Changed() {
				e.setPluginEnabled(name, !p.Enabled)
			}
		})
		if open {
			e.pluginBody(c, p)
		}
	})
}

func (e *extView) pluginBody(c *ui.Context, p core.PluginInfo) {
	t, th := theme.Of(c), c.Theme()
	name := p.Name
	f := e.cfgs[name]
	ui.Column(c).FillWidth().Gap(th.Space(4)).Padding(th.Space(1), th.Space(4), th.Space(4)).Children(func() {
		if p.Error != "" {
			kit.Alert(c, kit.Danger, p.Error, nil)
		} else if p.DisabledReason == "trust_revoked" && !p.Enabled {
			kit.Alert(c, kit.Warning, i18n.T("settings.ext.revoked"), nil)
		} else if p.DisabledReason != "" && !p.Enabled {
			kit.Alert(c, kit.Warning, p.DisabledReason, nil)
		}
		if en, ok := e.entry(name); ok && en.DataFlow != "" {
			dataFlowCard(c, en)
		}
		if p.Native {
			nativeCard(c, p)
		} else {
			kit.Block(c, ui.Column(c), kit.ToneRaised).FillWidth().Gap(th.Space(2)).Padding(th.Space(3), th.Space(4)).Children(func() {
				kit.Text(c, i18n.T("settings.ext.perms"), t.Text.Sm).LineHeight(theme.LineFor(i18n.T("settings.ext.perms"))).FontWeight(500)
				if p.Trust == "listed" {
					kit.Text(c, i18n.T("settings.ext.native.trust.listed"), t.Text.Xs).TextColor(t.Muted)
				}
				permissionList(c, p.Permissions, p.Capabilities)
			})
		}
		if st, ok := e.logins[name]; signsIn(p) && ok && st.SignedIn {
			ui.Row(c).FillWidth().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				label := i18n.Tf("settings.ext.signed_in", st.Account)
				kit.Text(c, label, t.Text.Sm).LineHeight(theme.LineFor(label)).Grow(1).Shrink(1)
				kit.Button(c, i18n.T("settings.ext.logout"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
					e.write("plugins", func(ctx context.Context) error { return e.a.svc.PluginLogout(ctx, name) }, nil)
				})
			})
		}
		if f != nil && len(p.Config) > 0 {
			e.configView(c, f, p.Config, !p.NeedsGrant)
		}
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			if !p.NeedsGrant {
				kit.Button(c, i18n.T("settings.ext.uninstall"), kit.ButtonOpts{Variant: kit.Destructive}).
					OnClick(func() { e.plugin, e.confirm, e.open = name, "plugin", true })
				return
			}
			allow, opts := i18n.T("settings.ext.allow"), kit.ButtonOpts{Disabled: f != nil && f.busy}
			if p.Native && p.Trust != "listed" { // consent to the risk of an unreviewed file
				allow, opts.Variant = i18n.T("settings.ext.native.allow"), kit.Destructive
			}
			kit.Button(c, allow, opts).OnClick(func() { e.allow(name, f, p.Config) })
			kit.Button(c, i18n.T("settings.ext.not_now"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
				if e.later == nil {
					e.later = map[string]bool{}
				}
				e.later[name] = true
			})
		})
	})
}

// nativeCard stands in for the permissions card of a native plugin: no
// permission binds native code, so it states the risk, how the current
// file came to be allowed, and which file that is.
func nativeCard(c *ui.Context, p core.PluginInfo) {
	t, th := theme.Of(c), c.Theme()
	trust := i18n.T("settings.ext.native.trust.unlisted")
	switch p.Trust {
	case "user":
		trust = i18n.T("settings.ext.native.trust.user")
	case "listed":
		trust = ""
		if p.Error == "" {
			trust = i18n.T("settings.ext.native.trust.listed")
		}
	}
	h := p.LibraryHash
	if len(h) > 12 {
		h = h[:8] + "…" + h[len(h)-4:]
	}
	kit.Block(c, ui.Column(c), kit.ToneRaised).FillWidth().Gap(th.Space(2)).Padding(th.Space(3), th.Space(4)).Children(func() {
		title, body := i18n.T("settings.ext.native.risk.title"), i18n.T("settings.ext.native.risk.body")
		if p.Trust == "listed" && p.Error == "" {
			title, body, trust = trust, i18n.T("settings.ext.native.reviewed.body"), ""
		}
		kit.Text(c, title, t.Text.Sm).LineHeight(theme.LineFor(title)).FontWeight(500)
		for _, s := range []string{body, trust} {
			if s != "" {
				kit.Text(c, s, t.Text.Sm).LineHeight(theme.LineFor(s))
			}
		}
		kit.Text(c, i18n.Tf("settings.ext.native.file", plugins.LibraryName(), h), t.Text.Xs).Font(theme.Mono).TextColor(t.Muted)
	})
}

// allow saves what the pending plugin's form holds, grants its permissions
// and enables it, as one chained write (not one transaction: a failure keeps
// the steps before it); a config error stays on its field.
func (e *extView) allow(name string, f *pluginForm, fields []plugins.ConfigField) {
	a := e.a
	var vals map[string]json.RawMessage
	if f != nil {
		if f.busy {
			return
		}
		vals = f.values(fields)
		f.busy, f.err = true, ""
		for _, v := range f.vals {
			v.err = ""
		}
	}
	a.writes.Chain("plugins", func(ctx context.Context) error {
		if len(vals) > 0 {
			if err := a.svc.SetPluginConfig(ctx, name, vals); err != nil {
				return err
			}
		}
		if err := a.svc.GrantPermission(ctx, name); err != nil {
			return err
		}
		return a.svc.SetPluginEnabled(ctx, name, true)
	}, func(err error) {
		if f != nil {
			f.busy = false
		}
		switch {
		case err == nil:
			delete(e.cfgs, name) // read again: the saved values
		case f != nil && e.cfgs[name] == f:
			f.fieldError(err, fields)
		default:
			a.toastErr(err)
		}
		e.load()
	})
}

func (e *extView) setPluginEnabled(name string, on bool) {
	if i := slices.IndexFunc(e.plugins, func(p core.PluginInfo) bool { return p.Name == name }); i >= 0 {
		e.plugins[i].Enabled = on
	}
	e.write("plugins", func(ctx context.Context) error { return e.a.svc.SetPluginEnabled(ctx, name, on) }, nil)
}

// statusPill is a small status label; attention shows it amber.
func statusPill(c *ui.Context, text string, attention bool) {
	tone := kit.BadgeSurface
	if attention {
		tone = kit.BadgeWarning
	}
	kit.BadgeWith(c, text, kit.BadgeOpts{Tone: tone})
}

// loadCfg reads a plugin's settings form once; a form already read or being read stays.
func (e *extView) loadCfg(name string) {
	a := e.a
	if e.cfgs[name] != nil || e.cfgBusy[name] {
		return
	}
	if e.cfgBusy == nil {
		e.cfgBusy = map[string]bool{}
	}
	e.cfgBusy[name] = true
	var vals []core.ConfigValue
	a.query(func(ctx context.Context) (err error) {
		vals, err = a.svc.GetPluginConfig(ctx, name)
		return err
	}, func(err error) {
		delete(e.cfgBusy, name)
		if err != nil {
			a.toastErr(err)
			return
		}
		if e.cfgs == nil {
			e.cfgs = map[string]*pluginForm{}
		}
		if e.cfgs[name] == nil {
			e.cfgs[name] = newPluginForm(name, vals)
		}
	})
}

// --- MCP

// mcpForm adds (id 0) or edits a server. Environment variables and headers
// are lines of KEY=VALUE; editing shows the keys only, and an empty value
// keeps the stored one.
type mcpForm struct {
	id                       int64
	title                    string
	name, transport, command string
	args, env, url, headers  string
	busy                     bool
	err                      string
}

func keyLines(keys []string) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// lines are s's lines verbatim, without the blank ones.
func lines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if l = strings.TrimRight(l, "\r\n"); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// keyValues reads KEY=VALUE lines; values stay verbatim.
func keyValues(s string) map[string]string {
	m := map[string]string{}
	for _, l := range lines(s) {
		k, v, _ := strings.Cut(l, "=")
		m[strings.TrimSpace(k)] = v
	}
	return m
}

func (f *mcpForm) input() core.MCPServerInput {
	in := core.MCPServerInput{Name: strings.TrimSpace(f.name), Transport: f.transport}
	if f.transport == "http" {
		in.URL, in.Headers = strings.TrimSpace(f.url), keyValues(f.headers)
	} else {
		in.Command, in.Args, in.Env = strings.TrimSpace(f.command), lines(f.args), keyValues(f.env)
	}
	return in
}

// fields are what the user edits.
func (f *mcpForm) fields() [7]string {
	return [...]string{f.name, f.transport, f.command, f.args, f.env, f.url, f.headers}
}

func (e *extView) editServer(s core.MCPServer) {
	e.mform = &mcpForm{id: s.ID, title: s.Name, name: s.Name, transport: s.Transport, command: s.Command,
		args: strings.Join(s.Args, "\n"), env: keyLines(s.EnvKeys), url: s.URL, headers: keyLines(s.HeaderKeys)}
}

func (e *extView) saveServer(f *mcpForm) {
	a, in, id := e.a, f.input(), f.id
	f.busy, f.err = true, ""
	snap := f.fields()
	var saved core.MCPServer
	a.writes.Chain("mcp", func(ctx context.Context) (err error) {
		if id == 0 {
			saved, err = a.svc.AddMCPServer(ctx, in)
		} else {
			saved, err = a.svc.UpdateMCPServer(ctx, id, in)
		}
		return err
	}, func(err error) {
		f.busy = false
		switch {
		case e.mform != f:
			a.toastErr(err)
		case err != nil:
			f.err = humanize(err)
		case f.fields() != snap: // edited while it saved: the form stays, now for the saved server
			f.id, f.title, e.mcp = saved.ID, saved.Name, saved.ID
		default:
			e.mform, e.mcp = nil, saved.ID
		}
		e.load()
	})
}

func mcpStatus(s core.MCPServer) (string, bool) {
	switch {
	case !s.Granted:
		return i18n.T("settings.ext.status.needs_grant"), true
	case s.Error != "":
		return i18n.T("settings.ext.status.error"), true
	case s.Enabled:
		return i18n.T("settings.ext.status.enabled"), false
	}
	return i18n.T("settings.ext.status.disabled"), false
}

func (e *extView) mcpView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	kit.Button(c, i18n.T("settings.ext.mcp.add"), kit.ButtonOpts{Variant: kit.Outline, Icon: "plus"}).AlignSelf(ui.Start).
		OnClick(func() { e.mform = &mcpForm{transport: "stdio"} })
	if len(e.servers) == 0 && e.mform == nil {
		ui.Text(c, i18n.T("settings.ext.mcp.empty")).TextColor(t.Muted)
	}
	if len(e.servers) > 0 {
		rows := make([]func(), len(e.servers))
		for i, s := range e.servers {
			rows[i] = func() {
				cc := c.Key(s.ID)
				ui.Row(cc).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center).Children(func() {
					kit.Pressable(cc, kit.PressableOpts{Selected: s.ID == e.mcp && e.mform == nil, OnClick: func() { e.mcp, e.mform = s.ID, nil }}).
						Grow(1).Shrink(1).Children(func() {
						ui.Column(cc).Gap(th.Space(0.5)).Children(func() {
							ui.Text(cc, s.Name).FontWeight(500)
							kit.Text(cc, s.Transport+" · "+i18n.Plural("settings.ext.mcp.tools", len(s.Tools)), t.Text.Xs).TextColor(t.Muted)
						})
					})
					st, attention := mcpStatus(s)
					statusPill(cc, st, attention)
				})
			}
		}
		settingsSection(c, "MCP", "", rows...)
	}
	if f := e.mform; f != nil {
		e.mcpFormView(c, f)
	} else if s, ok := e.server(); ok {
		e.serverView(c, s)
	}
}

func (e *extView) mcpFormView(c *ui.Context, f *mcpForm) {
	th := c.Theme()
	title := i18n.T("settings.ext.mcp.add")
	if f.id != 0 {
		title = i18n.Tf("settings.ext.mcp.edit.title", f.title)
	}
	settingsSection(c, title, "", func() {
		settingBlock(c, func() {
			ui.Field(c, i18n.T("settings.ext.mcp.name"), func() { kit.Input(c, &f.name, kit.InputOpts{}) })
			ui.Field(c, i18n.T("settings.ext.mcp.transport"), func() {
				ui.Row(c).Gap(th.Space(4)).Children(func() {
					kit.Radio(c.Key("stdio"), &f.transport, "stdio", "stdio")
					kit.Radio(c.Key("http"), &f.transport, "http", "http")
				})
			})
			kv := func(label string, s *string) {
				ui.Field(c, label, func() { kit.TextArea(c, s, kit.InputOpts{Mono: true}).Height(th.Space(20)) }).
					Description(i18n.T("settings.ext.mcp.kv.help"))
			}
			if f.transport == "http" {
				ui.Field(c, i18n.T("settings.ext.mcp.url"), func() { kit.Input(c, &f.url, kit.InputOpts{Mono: true}) })
				kv(i18n.T("settings.ext.mcp.headers"), &f.headers)
			} else {
				ui.Field(c, i18n.T("settings.ext.mcp.command"), func() { kit.Input(c, &f.command, kit.InputOpts{Mono: true}) })
				ui.Field(c, i18n.T("settings.ext.mcp.args"), func() { kit.TextArea(c, &f.args, kit.InputOpts{Mono: true}).Height(th.Space(20)) }).
					Description(i18n.T("settings.ext.mcp.args.help"))
				kv(i18n.T("settings.ext.mcp.env"), &f.env)
			}
			if f.err != "" {
				kit.Alert(c, kit.Danger, f.err, nil)
			}
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				kit.Button(c, i18n.T("common.save"), kit.ButtonOpts{Disabled: f.busy}).OnClick(func() { e.saveServer(f) })
				kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { e.mform = nil })
			})
		})
	})
}

func (e *extView) serverView(c *ui.Context, s core.MCPServer) {
	a := e.a
	th := c.Theme()
	id := s.ID
	st, _ := mcpStatus(s)
	rows := []func(){func() {
		desc := s.Transport + " · " + st
		if !s.Granted {
			desc = i18n.T("settings.ext.mcp.grant.help")
		}
		if s.ManagedBy != "" {
			desc += " · " + i18n.Tf("settings.managed_by", s.ManagedBy)
		}
		if s.Error != "" {
			desc += " · " + s.Error
		}
		settingRow(c, s.Name, desc, func() {
			if !s.Granted {
				kit.Button(c, i18n.T("settings.ext.grant"), kit.ButtonOpts{}).OnClick(func() {
					e.write("mcp", func(ctx context.Context) error {
						if err := a.svc.GrantMCPServer(ctx, id); err != nil {
							return err
						}
						return a.svc.SetMCPServerEnabled(ctx, id, true)
					}, nil)
				})
				return
			}
			on := s.Enabled
			if kit.Switch(c, &on).Label(i18n.T("settings.ext.mcp.enable")).Changed() {
				on = !s.Enabled
				e.write("mcp", func(ctx context.Context) error { return a.svc.SetMCPServerEnabled(ctx, id, on) }, nil)
			}
		})
	}, func() {
		settingRow(c, i18n.T("settings.ext.mcp.tools.title"), "", func() {
			kit.Button(c, i18n.T("settings.ext.mcp.refresh"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: !s.Granted}).
				OnClick(func() { e.write("mcp", func(ctx context.Context) error { return a.svc.RefreshMCPTools(ctx, id) }, nil) })
		})
	}}
	if len(s.Tools) == 0 {
		rows = append(rows, func() {
			settingBlock(c, func() { ui.Text(c, i18n.T("settings.ext.mcp.no_tools")).TextColor(theme.Of(c).Muted) })
		})
	}
	for _, tool := range s.Tools {
		rows = append(rows, func() {
			desc := tool.Description
			if tool.ReadOnlyHint {
				desc = strings.TrimSuffix(i18n.T("settings.ext.mcp.read_only")+" · "+desc, " · ")
			}
			if desc != "" {
				desc += " · "
			}
			desc += i18n.T("settings.ext.mcp.trust.help")
			settingRow(c.Key(tool.Name), tool.Name, desc, func() {
				on, name := tool.Trusted, tool.Name
				if kit.Switch(c, &on).Label(i18n.T("settings.ext.mcp.trust")).Changed() {
					on = !tool.Trusted
					e.write("mcp", func(ctx context.Context) error { return a.svc.SetMCPToolTrusted(ctx, id, name, on) }, nil)
				}
			})
		})
	}
	rows = append(rows, func() {
		if s.ManagedBy != "" { // its gateway's file is where it changes (ADR 0041)
			return
		}
		settingBlock(c, func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				kit.Button(c, i18n.T("settings.ext.mcp.edit"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { e.editServer(s) })
				kit.Button(c, i18n.T("settings.ext.mcp.remove"), kit.ButtonOpts{Variant: kit.Destructive}).
					OnClick(func() { e.confirm, e.open = "mcp", true })
			})
		})
	})
	settingsSection(c, s.Name, "", rows...)
}

// loginPageView asks before a sign-in's page opens, naming its host (AGENTS.md:
// egress goes through a card that names its destination). Only now is it known.
func (e *extView) loginPageView(c *ui.Context) {
	if e.loginPage == "" {
		return
	}
	answer := func(yes bool) {
		e.loginAnswer <- yes
		e.loginPage, e.loginPageOpen = "", false
	}
	if !e.loginPageOpen { // Escape or a click outside
		answer(false)
		return
	}
	host := e.loginPage
	if u, err := url.Parse(e.loginPage); err == nil {
		host = u.Host
	}
	kit.Dialog(c, &e.loginPageOpen, i18n.T("settings.ext.login.open.title"), kit.DialogOpts{Size: kit.DialogSm, Alert: true,
		Desc: i18n.Tf("settings.ext.login.open.body", host), Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { answer(false) })
		kit.Button(c, i18n.T("settings.ext.login.open.confirm"), kit.ButtonOpts{}).OnClick(func() { answer(true) })
	})
}

// confirmView asks before an uninstall, a removal or a sign-in.
func (e *extView) confirmView(c *ui.Context) {
	if e.confirm == "" {
		return
	}
	if !e.open { // Escape or a click outside
		e.confirm = ""
		return
	}
	a := e.a
	var title, body, label string
	var run func()
	switch e.confirm {
	case "login":
		p, ok := e.pluginInfo()
		if !ok {
			e.confirm = ""
			return
		}
		name := p.Name
		body = cmp.Or(e.logins[name].Notice.For(i18n.Language()), i18n.T("settings.ext.login.body"))
		title, label = i18n.Tf("settings.ext.login.title", name), i18n.T("settings.ext.login.confirm")
		run = func() { e.startLogin(name) }
	case "plugin":
		p, ok := e.pluginInfo()
		if !ok {
			e.confirm = ""
			return
		}
		name := p.Name
		title, body, label = i18n.Tf("settings.ext.uninstall.title", name), i18n.T("settings.ext.uninstall.body"), i18n.T("settings.ext.uninstall.confirm")
		run = func() {
			e.write("plugins", func(ctx context.Context) error { return a.svc.UninstallPlugin(ctx, name) }, func(err error) {
				if err == nil && e.plugin == name {
					e.plugin = ""
					delete(e.cfgs, name)
				}
			})
		}
	default:
		s, ok := e.server()
		if !ok {
			e.confirm = ""
			return
		}
		id := s.ID
		title, body, label = i18n.Tf("settings.ext.mcp.remove.title", s.Name), i18n.T("settings.ext.mcp.remove.body"), i18n.T("common.delete")
		run = func() { e.write("mcp", func(ctx context.Context) error { return a.svc.RemoveMCPServer(ctx, id) }, nil) }
	}
	dismiss := func() { e.confirm, e.open = "", false }
	kit.Dialog(c, &e.open, title, kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: body, Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(dismiss)
		kit.Button(c, label, kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() { dismiss(); run() })
	})
}

func (e *extView) installProgressView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	p := e.installProgress
	value, label := float64(-1), i18n.T("settings.ext.installing")
	switch p.Stage {
	case "download":
		if p.Total > 0 {
			value = float64(p.Done) / float64(p.Total)
			label = i18n.Tf("settings.ext.downloading", p.Done*100/p.Total)
		}
	case "verify":
		label = i18n.T("settings.ext.verifying")
	}
	ui.Column(c).Width(th.Space(36)).Gap(th.Space(1)).Children(func() {
		kit.Text(c, label, t.Text.Xs).TextColor(t.Muted)
		name := label
		if e.installing != ":folder" {
			name = i18n.Tf("settings.ext.registry.named", label, e.installing)
		}
		ui.Progress(c, value).FillWidth().Label(name)
	})
}

func unavailable(reason string) string {
	if min, ok := strings.CutPrefix(reason, "godwit:"); ok {
		return i18n.Tf("settings.ext.registry.na.godwit", min)
	}
	if reason == "abi" || reason == "unsupported" {
		return i18n.T("settings.ext.registry.na.abi")
	}
	return i18n.T("settings.ext.registry.na.platform")
}

// installFromRegistry installs or updates a directory plugin and opens its row;
// approved, what the install dialog showed, also grants it and turns it on.
func (e *extView) installFromRegistry(name string, approved *core.RegistryEntry) {
	if e.installing != "" {
		return
	}
	e.installing = name
	e.installProgress = core.PluginInstallProgress{}
	a := e.a
	e.write("plugins", func(ctx context.Context) error {
		lastStage, lastPercent := "", int64(-1)
		info, err := a.svc.InstallPluginFromRegistryWithProgress(ctx, name, func(p core.PluginInstallProgress) {
			percent := int64(-1)
			if p.Total > 0 {
				percent = p.Done * 100 / p.Total
			}
			if p.Stage == lastStage && percent == lastPercent {
				return
			}
			lastStage, lastPercent = p.Stage, percent
			a.onUI(func() { e.installProgress = p })
		})
		// The directory may have refreshed since the dialog: what differs from
		// what the user read stays pending, for its row to ask again.
		if err != nil || approved == nil || !sameConsent(*approved, info) {
			return err
		}
		if err := a.svc.GrantPermission(ctx, name); err != nil {
			return err
		}
		return a.svc.SetPluginEnabled(ctx, name, true)
	}, func(err error) {
		e.installing = ""
		if err == nil {
			e.tab, e.plugin, e.onDirectory = 1, name, false
		}
	})
}

// sameConsent: the installed plugin is the release, kind and permissions the
// dialog showed. A native release with no permissions is no script's consent.
func sameConsent(en core.RegistryEntry, p core.PluginInfo) bool {
	return en.Native == p.Native && en.Version == p.Version && reflect.DeepEqual(en.Permissions, p.Permissions) && slices.Equal(en.Capabilities, p.Capabilities)
}
