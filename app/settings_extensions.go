package app

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// extView is the Extensions page: skills, plugins and MCP servers in three
// tabs. Writes chain per kind ("skills", "plugins", "mcp"); a result for a
// form replaced since leaves the new form alone.
type extView struct {
	a          *App
	tab        int    // 0 skills, 1 plugins (the first shown), 2 MCP
	gen        uint64 // bumped per list read; only the newest applies
	skills     []core.SkillInfo
	plugins    []core.PluginInfo
	servers    []core.MCPServer
	plugin     string                 // the installed plugin open; "" none
	cfgs       map[string]*pluginForm // each plugin's settings form, read once; absent while it loads
	cfgBusy    map[string]bool        // a form read is in flight
	mcp        int64                  // the server shown
	mform      *mcpForm               // the add or edit form; nil when closed
	later      map[string]bool        // pending plugins collapsed with 暂不启用, this session only
	confirm    string                 // the open confirmation: "plugin", "mcp", "registry"; "" none
	reg        core.RegistryView      // the plugin directory
	regName    string                 // the directory entry the "registry" confirmation installs
	open       bool                   // the confirmation shows
	installing bool
}

// extChange: the event changes what the page lists.
func extChange(ev events.Event) bool {
	return ev.Type == events.PluginsChanged || ev.Type == events.MCPChanged
}

func (e *extView) load() {
	a := e.a
	e.gen++
	gen := e.gen
	var sk []core.SkillInfo
	var pl []core.PluginInfo
	var ms []core.MCPServer
	var rv core.RegistryView
	a.query(func(ctx context.Context) (err error) {
		if rv, err = a.svc.ListPluginRegistry(ctx); err != nil { // a cache that fails its signature: say so, list nothing
			rv, err = core.RegistryView{On: true, Stale: err.Error()}, nil
		}
		if sk, err = a.svc.ListSkills(ctx); err != nil {
			return err
		}
		if pl, err = a.svc.ListPlugins(ctx); err != nil {
			return err
		}
		ms, err = a.svc.ListMCPServers(ctx)
		return err
	}, func(err error) {
		if gen != e.gen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		e.skills, e.plugins, e.servers, e.reg = sk, pl, ms, rv
		for _, p := range pl {
			e.loadCfg(p.Name)
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
	a.writes.Chain(key, fn, func(err error) {
		a.toastErr(err)
		if done != nil {
			done(err)
		}
		e.load()
	})
}

func (e *extView) view(c *ui.Context) {
	pageHeading(c, i18n.T("settings.extensions"), i18n.T("settings.ext.sub"), func() {
		kit.Button(c, i18n.T("settings.ext.install"), kit.ButtonOpts{Disabled: e.installing}).OnClick(e.install)
	})
	kit.TabsUnderline(c, &e.tab, i18n.Tf("settings.ext.tab.skills", len(e.skills)), i18n.Tf("settings.ext.tab.plugins", len(e.plugins)),
		i18n.Tf("settings.ext.tab.mcp", len(e.servers)))
	switch e.tab {
	case 0:
		e.skillsView(c)
	case 1:
		e.pluginsView(c)
	default:
		e.mcpView(c)
	}
	e.confirmView(c)
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
			settingRow(c.Key(s.Name), s.Name, desc, func() {
				on := s.Enabled
				if kit.Switch(c, &on).Label(i18n.Tf("settings.ext.skill.enable", s.Name)).Changed() {
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
	a, plat := e.a, e.a.plat
	e.installing = true
	var dir string
	a.do(func(context.Context) (err error) {
		dir, err = plat.OpenDirectory()
		return err
	}, func(err error) {
		if err != nil || dir == "" {
			e.installing = false
			a.toastErr(err)
			return
		}
		var info core.PluginInfo
		e.write("plugins", func(ctx context.Context) (err error) {
			info, err = a.svc.InstallPlugin(ctx, dir)
			return err
		}, func(err error) {
			e.installing = false
			if err == nil {
				e.tab = 1
				e.plugin = info.Name
			}
		})
	})
}

func pluginStatus(p core.PluginInfo) string {
	switch {
	case p.Error != "":
		return i18n.T("settings.ext.status.error")
	case p.NeedsGrant:
		return i18n.T("settings.ext.status.needs_grant")
	case p.Enabled:
		return i18n.T("settings.ext.status.enabled")
	}
	return i18n.T("settings.ext.status.disabled")
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

// pluginsView: a settings row per plugin (name, description, switch); a
// plugin awaiting consent opens to its permissions, config and 允许并启用,
// an installed one to its permissions, config and uninstall on a click.
func (e *extView) pluginsView(c *ui.Context) {
	t := theme.Of(c)
	if e.reg.On {
		e.registryView(c)
	}
	if len(e.plugins) == 0 {
		ui.Text(c, i18n.T("settings.ext.plugins.empty")).TextColor(t.Muted)
		return
	}
	rows := make([]func(), len(e.plugins))
	for i, p := range e.plugins {
		rows[i] = func() { e.pluginRow(c.Key(p.Name), p) }
	}
	settingsSection(c, i18n.T("settings.ext.sec.plugins"), i18n.T("settings.ext.no_send"), rows...)
}

func (e *extView) pluginRow(c *ui.Context, p core.PluginInfo) {
	t, th := theme.Of(c), c.Theme()
	name := p.Name
	open := p.Name == e.plugin
	if p.NeedsGrant {
		open = !e.later[name]
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
			}}).Label(name).Expanded(open).Grow(1).Shrink(1).Children(func() {
				ui.Column(c).Gap(th.Space(0.5)).Shrink(1).Children(func() {
					ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
						kit.Text(c, p.Name, t.Text.Body).LineHeight(theme.LineFor(p.Name)).FontWeight(500)
						if p.NeedsGrant {
							kit.Text(c, p.Version, t.Text.Sm).TextColor(t.Muted)
						}
						if p.Native {
							statusPill(c, i18n.T("settings.ext.native"), false)
						}
					})
					if d := p.Description.For(i18n.Language()); d != "" { // untrusted length: one line, all of it in the tooltip
						kit.Tooltip(c, kit.Text(c, d, t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(1), d)
					}
				})
			})
			if p.NeedsGrant {
				statusPill(c, i18n.T("settings.ext.status.pending"), true)
				return
			}
			on := p.Enabled
			if kit.Switch(c, &on).Label(i18n.Tf("settings.ext.skill.enable", name)).Changed() {
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
		if p.Native {
			nativeCard(c, p)
		} else {
			ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
				kit.Text(c, i18n.T("settings.ext.perms"), t.Text.Body).LineHeight(theme.LineFor(i18n.T("settings.ext.perms"))).FontWeight(500)
				if p.Trust == "listed" {
					kit.Text(c, i18n.T("settings.ext.native.trust.listed"), t.Text.Sm).TextColor(t.Muted)
				}
				ui.Column(c).Gap(th.Space(1.5)).Children(func() {
					perm := func(tag, text string, muted bool) {
						r := ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center)
						if muted {
							r.TextColor(t.Muted)
						}
						r.Children(func() {
							ui.Box(c).Padding(th.Space(0.5), th.Space(1.5)).Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
								kit.Text(c, tag, t.Text.Sm).Font(theme.Mono)
							})
							kit.Text(c, text, t.Text.Body).LineHeight(theme.LineFor(text)).Shrink(1)
						})
					}
					pm := p.Permissions
					if slices.Contains(pm.Mail, "read") {
						perm("mail:read", i18n.T("settings.ext.perm.mail_read"), false)
					}
					if slices.Contains(pm.Mail, "write") {
						perm("mail:write", i18n.T("settings.ext.perm.mail_write"), false)
					}
					if len(pm.Net) > 0 {
						perm("net", i18n.Tf("settings.ext.perm.net", strings.Join(pm.Net, i18n.T("mail.list_sep"))), false)
					}
					if pm.LLM {
						perm("llm", i18n.T("settings.ext.perm.llm"), false)
					}
					if len(pm.Mail) == 0 && len(pm.Net) == 0 && !pm.LLM {
						ui.Text(c, i18n.T("settings.ext.perm.none")).TextColor(t.Muted)
					}
					perm(i18n.T("settings.ext.excluded"), pluginExcluded(pm), true)
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
			if p.Native { // consent to the risk, not to permissions
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
		trust = i18n.T("settings.ext.native.trust.listed")
	}
	h := p.LibraryHash
	if len(h) > 12 {
		h = h[:8] + "…" + h[len(h)-4:]
	}
	ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
		title := i18n.T("settings.ext.native.risk.title")
		kit.Text(c, title, t.Text.Body).LineHeight(theme.LineFor(title)).FontWeight(500)
		for _, s := range []string{i18n.T("settings.ext.native.risk.body"), trust} {
			kit.Text(c, s, t.Text.Body).LineHeight(theme.LineFor(s))
		}
		kit.Text(c, i18n.Tf("settings.ext.native.file", plugins.LibraryName(), h), t.Text.Sm).Font(theme.Mono).TextColor(t.Muted)
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
	t, th := theme.Of(c), c.Theme()
	b := ui.Box(c).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Sm).Background(t.Hover).TextColor(t.Foreground)
	if attention {
		b.Background(t.WarningBg).TextColor(t.WarningText)
	}
	b.Children(func() { kit.Text(c, text, t.Text.Sm) })
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
							kit.Text(cc, s.Transport+" · "+i18n.Plural("settings.ext.mcp.tools", len(s.Tools)), t.Text.Sm).TextColor(t.Muted)
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

// confirmView asks before an uninstall or a removal.
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
	case "registry":
		i := slices.IndexFunc(e.reg.Entries, func(r core.RegistryEntry) bool { return r.Name == e.regName })
		if i < 0 {
			e.confirm = ""
			return
		}
		en := e.reg.Entries[i]
		title, body, label = i18n.Tf("settings.ext.registry.confirm.title", entryTitle(en), en.Version), i18n.T("settings.ext.registry.confirm.body"), i18n.T("settings.ext.registry.confirm.ok")
		if en.Installed != "" {
			body += i18n.Tf("settings.ext.registry.confirm.replace", en.Name)
		}
		run = func() { e.installFromRegistry(en.Name) }
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
	kit.Dialog(c, &e.open, title, kit.DialogOpts{Size: kit.DialogSm, Desc: body, Close: i18n.T("common.close")}, nil, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(dismiss)
		kit.Button(c, label, kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() { dismiss(); run() })
	})
}

// registryView is the plugin directory (spec 3.1): what it lists for this
// computer, and for each entry install, update, replace, or why not.
func (e *extView) registryView(c *ui.Context) {
	t := theme.Of(c)
	foot := ""
	if e.reg.Stale != "" {
		foot = i18n.T("settings.ext.registry.stale")
	}
	title := i18n.T("settings.ext.registry")
	if len(e.reg.Entries) == 0 {
		ui.Column(c).Gap(theme.SectionGap).Children(func() {
			sectionTitle(c, title)
			ui.Text(c, i18n.T("settings.ext.registry.empty")).TextColor(t.Muted)
			if foot != "" {
				kit.Text(c, foot, t.Text.Sm).TextColor(t.Muted)
			}
		})
		return
	}
	rows := make([]func(), len(e.reg.Entries))
	for i, en := range e.reg.Entries {
		rows[i] = func() { e.registryRow(c.Key("registry:"+en.Name), en) }
	}
	settingsSection(c, title, foot, rows...)
}

func entryTitle(en core.RegistryEntry) string {
	if s := en.Title.For(i18n.Language()); s != "" {
		return s
	}
	return en.Name
}

func (e *extView) registryRow(c *ui.Context, en core.RegistryEntry) {
	t, th := theme.Of(c), c.Theme()
	settingRow(c, entryTitle(en), en.Summary.For(i18n.Language()), func() {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kind := i18n.T("settings.ext.script")
			if en.Native {
				kind = i18n.T("settings.ext.native")
			}
			statusPill(c, kind, false)
			if v := cmp.Or(en.Version, en.Installed); v != "" {
				kit.Text(c, v, t.Text.Sm).TextColor(t.Muted)
			}
			act := func(text string, confirm bool) {
				kit.Button(c, text, kit.ButtonOpts{Variant: kit.Outline}).Label(i18n.Tf("settings.ext.registry.named", text, en.Name)).OnClick(func() {
					if confirm && en.Native {
						e.regName, e.confirm, e.open = en.Name, "registry", true
						return
					}
					e.installFromRegistry(en.Name)
				})
			}
			switch {
			case en.Version == "":
				kit.Text(c, unavailable(en.Unavailable), t.Text.Sm).TextColor(t.Muted)
			case en.Installed == "":
				act(i18n.T("settings.ext.registry.install"), true)
			case en.Update:
				act(i18n.Tf("settings.ext.registry.update", en.Version), false) // reviewed, and its risk was confirmed at install
			case en.InstalledTrust != "listed":
				act(i18n.T("settings.ext.registry.replace"), true)
			default:
				kit.Text(c, i18n.T("settings.ext.registry.installed"), t.Text.Sm).TextColor(t.Muted)
			}
		})
	})
}

func unavailable(reason string) string {
	if min, ok := strings.CutPrefix(reason, "godwit:"); ok {
		return i18n.Tf("settings.ext.registry.na.godwit", min)
	}
	if reason == "abi" {
		return i18n.T("settings.ext.registry.na.abi")
	}
	return i18n.T("settings.ext.registry.na.platform")
}

// installFromRegistry installs or updates a directory plugin and opens its row.
func (e *extView) installFromRegistry(name string) {
	a := e.a
	e.write("plugins", func(ctx context.Context) error {
		_, err := a.svc.InstallPluginFromRegistry(ctx, name)
		return err
	}, func(err error) {
		if err == nil {
			e.tab, e.plugin = 1, name
		}
	})
}
