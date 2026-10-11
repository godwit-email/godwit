package app

import (
	"context"
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
	tab        int    // 0 skills, 1 plugins, 2 MCP
	gen        uint64 // bumped per list read; only the newest applies
	skills     []core.SkillInfo
	plugins    []core.PluginInfo
	servers    []core.MCPServer
	plugin     string          // the plugin shown
	cfg        *pluginForm     // its settings form; nil while it loads or has none
	mcp        int64           // the server shown
	mform      *mcpForm        // the add or edit form; nil when closed
	later      map[string]bool // pending plugins collapsed with 暂不启用, this session only
	confirm    string          // the open confirmation: "plugin", "mcp"; "" none
	open       bool            // the confirmation shows
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
	a.query(func(ctx context.Context) (err error) {
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
		e.skills, e.plugins, e.servers = sk, pl, ms
		if _, ok := e.pluginInfo(); !ok && len(pl) > 0 {
			e.showPlugin(pl[0].Name)
		}
		if _, ok := e.server(); !ok && len(ms) > 0 {
			e.mcp = ms[0].ID
		}
	})
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
	th := c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() { pageHeading(c, i18n.T("settings.extensions"), i18n.T("settings.ext.sub")) })
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
				e.showPlugin(info.Name)
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
	if !slices.Contains(pm.Mail, "write") {
		no = append(no, i18n.T("settings.ext.excl.write"))
	}
	if !pm.LLM {
		no = append(no, i18n.T("settings.ext.excl.llm"))
	}
	no = append(no, i18n.T("settings.ext.excl.send"))
	return i18n.Tf("settings.ext.excluded", strings.Join(no, i18n.T("mail.list_sep")))
}

// pluginRows: a row per plugin. The manifest has no description field yet, so
// the description is empty (ledgered); plugins carry no built-in flag either.
func (e *extView) pluginsView(c *ui.Context) {
	t := theme.Of(c)
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
			kit.Pressable(c, kit.PressableOpts{OnClick: func() {
				if p.NeedsGrant {
					if e.later == nil {
						e.later = map[string]bool{}
					}
					e.later[name] = open
				} else {
					e.showPlugin(name)
				}
			}}).Grow(1).Shrink(1).Children(func() {
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, p.Name).FontWeight(500)
					ui.Text(c, p.Version).FontSize(t.Text.Sm).TextColor(t.Muted)
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
	if p.NeedsGrant {
		e.showPlugin(name) // loads its config form
	}
	ui.Column(c).FillWidth().Gap(th.Space(4)).Padding(th.Space(1), th.Space(4), th.Space(4)).Children(func() {
		if p.Error != "" {
			kit.Alert(c, kit.Danger, p.Error, nil)
		} else if p.DisabledReason != "" && !p.Enabled {
			kit.Alert(c, kit.Warning, p.DisabledReason, nil)
		}
		ui.Column(c).FillWidth().Gap(th.Space(2)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Lg).Background(t.Sidebar).Children(func() {
			ui.Text(c, i18n.T("settings.ext.perms")).FontWeight(500)
			perm := func(tag, text string) {
				ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Padding(th.Space(0.5), th.Space(1.5)).Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
						ui.Text(c, tag).Font(theme.Mono).FontSize(t.Text.Sm)
					})
					ui.Text(c, text).Shrink(1)
				})
			}
			pm := p.Permissions
			if slices.Contains(pm.Mail, "read") {
				perm("mail:read", i18n.T("settings.ext.perm.mail_read"))
			}
			if slices.Contains(pm.Mail, "write") {
				perm("mail:write", i18n.T("settings.ext.perm.mail_write"))
			}
			if len(pm.Net) > 0 {
				perm("net", i18n.Tf("settings.ext.perm.net", strings.Join(pm.Net, i18n.T("mail.list_sep"))))
			}
			if pm.LLM {
				perm("llm", i18n.T("settings.ext.perm.llm"))
			}
			if len(pm.Mail) == 0 && len(pm.Net) == 0 && !pm.LLM {
				ui.Text(c, i18n.T("settings.ext.perm.none")).TextColor(t.Muted)
			}
			ui.Text(c, pluginExcluded(pm)).FontSize(t.Text.Sm).TextColor(t.Muted)
		})
		if f := e.cfg; f != nil && f.name == name && len(p.Config) > 0 {
			e.configView(c, f, p.Config)
		}
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			if p.NeedsGrant {
				kit.Button(c, i18n.T("settings.ext.allow"), kit.ButtonOpts{}).OnClick(func() {
					e.write("plugins", func(ctx context.Context) error {
						if err := e.a.svc.GrantPermission(ctx, name); err != nil {
							return err
						}
						return e.a.svc.SetPluginEnabled(ctx, name, true)
					}, nil)
				})
				kit.Button(c, i18n.T("settings.ext.not_now"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
					if e.later == nil {
						e.later = map[string]bool{}
					}
					e.later[name] = true
				})
			}
			kit.Button(c, i18n.T("settings.ext.uninstall"), kit.ButtonOpts{Variant: kit.Destructive}).
				OnClick(func() { e.plugin, e.confirm, e.open = name, "plugin", true })
		})
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
	b.Children(func() { ui.Text(c, text).FontSize(t.Text.Sm) })
}

func (e *extView) showPlugin(name string) {
	a := e.a
	e.plugin = name
	if e.cfg != nil && e.cfg.name == name {
		return
	}
	e.cfg = nil
	var vals []core.ConfigValue
	a.query(func(ctx context.Context) (err error) {
		vals, err = a.svc.GetPluginConfig(ctx, name)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if e.plugin == name && e.cfg == nil {
			e.cfg = newPluginForm(name, vals)
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
	t := theme.Of(c)
	kit.Button(c, i18n.T("settings.ext.mcp.add"), kit.ButtonOpts{Variant: kit.Outline, Icon: "plus"}).AlignSelf(ui.Start).
		OnClick(func() { e.mform = &mcpForm{transport: "stdio"} })
	if len(e.servers) == 0 && e.mform == nil {
		ui.Text(c, i18n.T("settings.ext.mcp.empty")).TextColor(t.Muted)
	}
	if len(e.servers) > 0 {
		listCard(c, func() {
			for i, s := range e.servers {
				listRow(c.Key(s.ID), i, s.ID == e.mcp && e.mform == nil, func() { e.mcp, e.mform = s.ID, nil }, func() {
					ui.Text(c, s.Name).FontWeight(500).Grow(1).Shrink(1)
					ui.Text(c, s.Transport).Font(theme.Mono).FontSize(t.Text.Sm).TextColor(t.Muted)
					ui.Text(c, i18n.Plural("settings.ext.mcp.tools", len(s.Tools))).TextColor(t.Muted)
					st, attention := mcpStatus(s)
					statusPill(c, st, attention)
				})
			}
		})
	}
	if f := e.mform; f != nil {
		e.mcpFormView(c, f)
	} else if s, ok := e.server(); ok {
		e.serverView(c, s)
	}
}

func (e *extView) mcpFormView(c *ui.Context, f *mcpForm) {
	t, th := theme.Of(c), c.Theme()
	kit.Card(c, func() {
		title := i18n.T("settings.ext.mcp.add")
		if f.id != 0 {
			title = i18n.Tf("settings.ext.mcp.edit.title", f.title)
		}
		ui.Text(c, title).FontSize(t.Text.Lg).FontWeight(600)
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
}

func (e *extView) serverView(c *ui.Context, s core.MCPServer) {
	a := e.a
	t, th := theme.Of(c), c.Theme()
	id := s.ID
	kit.Card(c, func() {
		st, _ := mcpStatus(s)
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			ui.Text(c, s.Name).FontSize(t.Text.Lg).FontWeight(600)
			ui.Text(c, s.Transport+" · "+st).TextColor(t.Muted)
		})
		if s.Error != "" {
			kit.Alert(c, kit.Danger, s.Error, nil)
		}
		if !s.Granted {
			ui.Text(c, i18n.T("settings.ext.mcp.grant.help")).TextColor(t.Muted)
			kit.Button(c, i18n.T("settings.ext.grant"), kit.ButtonOpts{}).AlignSelf(ui.Start).OnClick(func() {
				e.write("mcp", func(ctx context.Context) error {
					if err := a.svc.GrantMCPServer(ctx, id); err != nil {
						return err
					}
					return a.svc.SetMCPServerEnabled(ctx, id, true)
				}, nil)
			})
		} else {
			on := s.Enabled
			if kit.SwitchRow(c, &on, i18n.T("settings.ext.mcp.enable"), "").Padding(0).Changed() {
				on = !s.Enabled
				e.write("mcp", func(ctx context.Context) error { return a.svc.SetMCPServerEnabled(ctx, id, on) }, nil)
			}
		}
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			ui.Text(c, i18n.T("settings.ext.mcp.tools.title")).FontWeight(600).Grow(1)
			kit.Button(c, i18n.T("settings.ext.mcp.refresh"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: !s.Granted}).
				OnClick(func() { e.write("mcp", func(ctx context.Context) error { return a.svc.RefreshMCPTools(ctx, id) }, nil) })
		})
		if len(s.Tools) == 0 {
			ui.Text(c, i18n.T("settings.ext.mcp.no_tools")).TextColor(t.Muted)
		}
		for _, tool := range s.Tools {
			ui.Column(c.Key(tool.Name)).Gap(th.Space(1.5)).Padding(th.Space(3), 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				ui.Row(c).Gap(th.Space(2)).Children(func() {
					ui.Text(c, tool.Name).Font(theme.Mono).FontWeight(500)
					if tool.ReadOnlyHint {
						kit.Badge(c, i18n.T("settings.ext.mcp.read_only"))
					}
				})
				if tool.Description != "" {
					ui.Text(c, tool.Description).TextColor(t.Muted)
				}
				on, name := tool.Trusted, tool.Name
				if kit.SwitchRow(c, &on, i18n.T("settings.ext.mcp.trust"), i18n.T("settings.ext.mcp.trust.help")).Padding(0).Changed() {
					on = !tool.Trusted
					e.write("mcp", func(ctx context.Context) error { return a.svc.SetMCPToolTrusted(ctx, id, name, on) }, nil)
				}
			})
		}
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			kit.Button(c, i18n.T("settings.ext.mcp.edit"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { e.editServer(s) })
			kit.Button(c, i18n.T("settings.ext.mcp.remove"), kit.ButtonOpts{Variant: kit.Destructive}).
				OnClick(func() { e.confirm, e.open = "mcp", true })
		})
	})
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
	t := theme.Of(c)
	var title, body, label string
	var run func()
	switch e.confirm {
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
					e.plugin, e.cfg = "", nil
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
	kit.Dialog(c, &e.open, title, func() { ui.Text(c, body).TextColor(t.Muted) }, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(dismiss)
		kit.Button(c, label, kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() { dismiss(); run() })
	})
}
