package app

import (
	"cmp"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// empty is whether there is nothing to list: no service, and no plugin
// with models (a plugin's are enough).
func (m *modelsView) empty() bool {
	return m.loaded && len(m.services) == 0 && !slices.ContainsFunc(m.plugins, func(p core.PluginModels) bool { return len(p.Models) > 0 })
}

// view is the 模型 page, under its heading (modelsPage).
func (m *modelsView) view(c *ui.Context) {
	if m.empty() {
		emptyCard(c, kit.EmptyOpts{Icon: "cpu", Title: i18n.T("settings.models.empty.title"), Desc: i18n.T("settings.models.empty.desc"), Actions: func() {
			kit.EmptyAction(c, i18n.T("settings.models.add"), kit.ButtonOpts{Icon: "plus"}).OnClick(m.add)
			kit.EmptyAction(c, i18n.T("settings.models.local"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(m.addLocal)
		}})
	}
	if st := m.a.settings; len(m.services) > 0 || len(m.plugins) > 0 || isPluginRef(st.DefaultModel) || isPluginRef(st.SmallModel) || isPluginRef(st.JudgeService) {
		u := deriveUsage(m.a.settings, m.services, m.plugins, m.pluginStatus)
		m.usage(c, u)
		m.list(c)
	}
	m.localSection(c)
	if m.form != nil {
		m.dialog(c)
	}
}

// close drops the form with its dialog: its pending results are ignored.
func (m *modelsView) close() {
	m.cancelForm()
	m.open, m.form = false, nil
}

// svcMark is a service's avatar: its provider's glyph, else the favicon of
// the site it talks to, else its initial, as a sender's in mail; a gateway on
// this Mac without a favicon is a router, as the design's 本地网关.
func (m *modelsView) svcMark(c *ui.Context, s core.ModelService) {
	t, th := theme.Of(c), c.Theme()
	glyph := icons.BrandIcon(s.Provider)
	if glyph == nil {
		m.loadAvatar(s)
		if u, err := url.Parse(s.BaseURL); m.avatars[s.BaseURL] == nil && err == nil && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()) {
			glyph = icons.Get("router")
		}
	}
	if glyph != nil {
		kit.Tile(c, th.Space(8), th.Space(8), kit.ToneTint).Label(s.Name).Children(func() {
			ui.Icon(c, glyph).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
		})
		return
	}
	kit.AvatarImage(c, s.Name, m.avatars[s.BaseURL])
}

// svcLine is a service's second line: where it talks to and what it does.
func svcLine(s core.ModelService) string {
	parts := []string{svcSubtitle(s)}
	if u, err := url.Parse(s.BaseURL); err == nil && u.Host != "" && core.ModelProviderLogin(s.Provider) != "oauth" {
		parts[0] = u.Host
	}
	if s.Kind != "" && len(s.Models) > 0 {
		parts = append(parts, i18n.Plural("settings.models.gen", len(s.Models)))
	}
	if s.JudgeFormat != "" && s.JudgeModel != "" {
		parts = append(parts, i18n.Tf("settings.models.judge_chip", s.JudgeModel))
	}
	return strings.Join(parts, " · ")
}

// list is the services, one card row each: its mark, name and line, how its
// last test went, and a chevron to its form.
func (m *modelsView) list(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	var rows []func()
	for _, s := range m.services {
		rows = append(rows, func() {
			edit := func() { m.openEdit(s) }
			if s.ManagedBy != "" { // its gateway's file is where it changes (ADR 0041)
				edit = nil
			}
			listRow(c.Key(s.ID), 0, false, edit, func() {
				m.svcMark(c, s)
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, s.Name).SingleLine().Shrink(1)
						if s.ManagedBy != "" {
							kit.BadgeWith(c, i18n.Tf("settings.managed_by", s.ManagedBy), kit.BadgeOpts{Tone: kit.BadgeSurface, Muted: true}).Shrink(0)
						}
					})
					kit.Text(c, svcLine(s), t.Text.Xs).TextColor(t.Muted).SingleLine()
				})
				l, label := svcLook(s)
				statusLine(c, line{label, l})
				if edit != nil {
					ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
				} else {
					ui.Box(c).Size(theme.Icon, theme.Icon).Shrink(0)
				}
			}).AlignItems(ui.Center).Padding(th.Space(2), th.Space(4)).ContextMenu(func(menu *ui.Menu) {
				if menu.Item(i18n.T("settings.models.test")).Chosen() {
					m.testRow(s)
				}
			})
		})
	}
	if i := slices.IndexFunc(m.plugins, func(p core.PluginModels) bool { return p.Plugin == core.AppleIntelligencePlugin }); i >= 0 {
		p := m.plugins[i]
		rows = append(rows, func() {
			// The system runs it, with nothing to edit: no chevron, just its menu's test.
			iconRowGap(c.Key("apple-intelligence"), 1, func() {
				ui.Icon(c, icons.BrandIcon("apple")).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
			}, "Apple Intelligence", strings.Join([]string{i18n.T("settings.models.apple_intelligence.sub"), i18n.T("settings.models.cap.gen"), i18n.T("settings.models.cap.judge")}, " · "), func() {
				statusLine(c, pluginLook(p, m.pluginStatus))
				ui.Box(c).Size(theme.Icon, theme.Icon).Shrink(0)
			}).ContextMenu(func(menu *ui.Menu) {
				if menu.Item(i18n.T("settings.models.test")).Chosen() {
					m.testPluginRow(p)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.models.services"), "", rows...)
}

// statusLine is a dot and a text in the look's colors.
func statusLine(c *ui.Context, l line) ui.Element {
	t, th := theme.Of(c), c.Theme()
	dot, fg := l.look.status(t)
	return ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Shrink(0).Children(func() {
		kit.StatusDot(c, dot)
		kit.Text(c, l.text, t.Text.Xs).TextColor(fg)
	})
}

// usage is the 用途 card: the generate, small-task and judge defaults with
// their last tests, an alert when the generator is broken, and a hint when
// generation answers the judge.
func (m *modelsView) usage(c *ui.Context, u usage) {
	a := m.a
	t, th := theme.Of(c), c.Theme()
	defer func() {
		m.defModel, m.smallModel, m.judge = a.settings.DefaultModel, a.settings.SmallModel, a.settings.JudgeService
	}() // what the next frame shows
	sel := func(label string, v *string, opts []kit.Option[string], set func(*core.Settings, string), managed bool) {
		s := kit.SelectWith(c, kit.SelectOpts{Flat: true}, v, opts).Width(theme.SettingsControlWidth).Label(label).Disabled(u.disabled || managed)
		if s.Changed() {
			val := *v
			m.setModel(func(st *core.Settings) { set(st, val) })
		}
	}
	gen, small, judge := i18n.T("settings.models.use.gen"), i18n.T("settings.models.use.small"), i18n.T("settings.models.use.judge")
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, i18n.T("settings.models.usage"))
		if u.alert != "" {
			svc := *u.genSvc
			kit.Alert(c, kit.ToneDanger, u.alert, func() {
				kit.Button(c, i18n.T("settings.models.use.fix"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Tone: kit.ToneDanger}).Shrink(0).OnClick(func() { m.openEdit(svc) })
			})
		}
		settingsCard(c,
			func() {
				desc, by := strings.TrimRight(i18n.T("settings.models.use.gen.desc"), "。."), a.settings.DefaultModelBy
				if by != "" { // its file chooses the default: changed there (ADR 0041)
					desc += " · " + i18n.Tf("settings.managed_by", by)
				}
				usageRow(c, gen, desc, &u.gen, func() {
					sel(gen, &m.defModel, u.genOpts, func(st *core.Settings, v string) { st.DefaultModel = v }, by != "")
				})
			},
			func() {
				usageRow(c, small, i18n.T("settings.models.use.small.desc"), &u.small, func() {
					sel(small, &m.smallModel, u.smallOpts, func(st *core.Settings, v string) { st.SmallModel = v }, false)
				})
			},
			func() {
				usageRow(c, judge, strings.TrimRight(i18n.T("settings.models.use.judge.desc"), "。."), &u.judge, func() {
					sel(judge, &m.judge, u.judgeOpts, func(st *core.Settings, v string) { st.JudgeService = v }, false)
				})
			},
			func() {
				help := i18n.T("settings.models.threshold.help")
				if u.judgeSvc == nil && !u.pluginJudge {
					help += " " + i18n.T("settings.models.threshold.binary")
				}
				settingRow(c, i18n.T("settings.models.threshold"), help, func() ui.Element {
					threshold := a.settings.JudgeThreshold
					var slider ui.Element
					ui.Row(c).Width(theme.SettingsControlWidth).Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
						slider = ui.Slider(c, &threshold, core.MinJudgeThreshold, core.MaxJudgeThreshold).Step(0.01).Grow(1).Shrink(1).Label(i18n.T("settings.models.threshold"))
						if slider.Changed() {
							m.setModel(func(st *core.Settings) { st.JudgeThreshold = threshold })
						}
						kit.Text(c, fmt.Sprintf("%.0f%%", threshold*100), t.Text.Xs).Width(th.Space(10)).TextAlign(ui.End).TextColor(t.Muted)
					})
					return slider
				})
			})
	})
	if u.suggest {
		ui.Row(c).Wrap().AlignItems(ui.Center).Children(func() {
			kit.Text(c, i18n.T("settings.models.use.suggest"), t.Text.Xs).TextColor(t.Muted)
			kit.Button(c, i18n.T("settings.models.add"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).OnClick(m.add)
		})
	}
}

// usageRow is a row of the 用途 card: label, description and, when its last
// test found something, a status line on the left, the select on the right.
func usageRow(c *ui.Context, label, desc string, l *line, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, label, t.Text.Sm)
			kit.Text(c, desc, t.Text.Xs).TextColor(t.Muted)
			if l != nil && l.look != lookOK && l.look != lookNone { // a passed test is the services' row to show
				statusLine(c, *l).Margin(th.Space(1), 0, 0, 0)
			}
		})
		control()
	})
}

// dialog is the add/edit form in a 640-wide dialog; closing it drops the form.
func (m *modelsView) dialog(c *ui.Context) {
	f := m.form
	if f == nil {
		m.open = false
		return
	}
	f.follow()
	f.edited()
	title, desc := i18n.T("settings.models.dialog.add"), i18n.T("settings.models.dialog.desc")
	if f.id != 0 {
		title, desc = i18n.Tf("settings.models.edit", f.name), svcProvider(f.provider)+" · "+cmp.Or(f.email, f.baseURL)
	}
	opts := kit.DialogOpts{Size: kit.DialogLg, Desc: desc, Danger: f.confirmDel, Close: i18n.T("common.close"), Top: true} // grows as a test reports
	kit.Dialog(c, &m.open, title, opts, func() { m.formView(c, f) }, func() { m.footer(c, f) })
	if !m.open { // closed by Escape or a click outside
		m.close()
	}
}

func (m *modelsView) footer(c *ui.Context, f *modelForm) {
	t := theme.Of(c)
	if f.confirmDel {
		ui.Row(c).Grow(1).Gap(c.Theme().Space(2)).AlignItems(ui.Center).Label(i18n.T("settings.models.del_title")).Children(func() {
			ui.Text(c, i18n.Tf("settings.models.del_confirm", f.name)).TextColor(t.DangerText).Grow(1).Shrink(1)
			footButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(func() { f.confirmDel = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Ghost, Tone: kit.ToneDanger, Disabled: f.busy}).OnClick(func() { m.remove(f) })
		})
		return
	}
	if f.id != 0 {
		kit.Button(c, i18n.T("settings.models.remove"), kit.ButtonOpts{Variant: kit.Ghost, Tone: kit.ToneDanger, Disabled: f.busy}).OnClick(func() { f.confirmDel = true })
	}
	kit.Text(c, f.hint(), t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
	label := i18n.T("settings.models.test")
	if f.testing() {
		label = i18n.T("settings.models.test.busy")
	}
	// Blocked buttons are disabled in the design's blocked colors, which mygo then paints at half opacity.
	test := footButton(c, label, kit.Outline, f.cantTest()).OnClick(func() { m.test(f) })
	if f.cantTest() {
		test.TextColor(t.Idle).Background(t.Background)
	}
	footButton(c, i18n.T("common.cancel"), kit.Outline, false).OnClick(m.close)
	save := i18n.T("common.save")
	if f.id == 0 {
		save = i18n.T("settings.models.confirm_add")
	}
	b := footButton(c, save, kit.Default, f.busy || f.blocked()).PaddingX(c.Theme().Space(4)).Description(f.hint()).OnClick(func() { m.save(f) })
	if f.blocked() {
		b.Background(t.Hover).TextColor(t.Muted)
	}
}

// footButton is a dialog footer's button: 12 across, flat.
func footButton(c *ui.Context, label string, v kit.Variant, disabled bool) ui.Element {
	return kit.Button(c, label, kit.ButtonOpts{Variant: v, Disabled: disabled, Flat: true})
}

// formRow is a label in its own 96 px column beside the control; the label
// focuses the control it returns.
func formRow(c *ui.Context, label string, control func() ui.Element, extra func()) {
	th := c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		var in ui.Element
		// 4 down: where the design's 32 px line box (Geist's metrics) puts the glyphs, a little above center
		l := ui.Text(c, label).Width(theme.FormLabelWidth).Shrink(0).Margin(th.Space(1), 0, 0, 0)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(1.5)).Children(func() {
			in = control().Label(label)
			if extra != nil {
				extra()
			}
		})
		if l.Clicked() {
			in.Focus()
		}
	})
}

func (m *modelsView) formView(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	if f.id == 0 {
		access := i18n.T("settings.models.access.api")
		if m.group == 0 {
			access = i18n.T("settings.models.access.sub")
		}
		ui.Column(c).Gap(th.Space(3)).Children(func() {
			formRow(c, i18n.T("settings.models.access"), func() ui.Element {
				return ui.Row(c).Children(func() { // a row, so the tabs hug their labels instead of stretching
					kit.Tabs(c, &m.group, kit.TabsOpts{}, kit.TabLabels(i18n.T("settings.models.group.sub"), i18n.T("settings.models.group.api")))
				})
			}, func() { kit.Text(c, access, t.Text.Xs).TextColor(t.Muted) })
			if providerGroup(f.provider) != m.group { // another group starts the form over on its first tile
				m.cancelForm()
				f = newForm(providerGroups[m.group][0])
				m.form = f
			}
			formRow(c, i18n.T("settings.models.provider"), func() ui.Element { return m.tiles(c, f) }, nil)
		})
	}
	ui.Column(c).Gap(th.Space(3)).Children(func() {
		formRow(c, i18n.T("settings.models.name"), func() ui.Element {
			return kit.Input(c, &f.name, kit.InputOpts{Placeholder: i18n.T("settings.models.name.ph"), Flat: true})
		}, nil)
		if f.oauth() {
			m.accountRow(c, f)
		} else {
			var urlErr string
			if f.urlErr != "" && f.baseURL == f.urlBad {
				urlErr = f.urlErr
			}
			formRow(c, i18n.T("settings.models.base_url"), func() ui.Element {
				var in ui.Element
				ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
					in = kit.Input(c, &f.baseURL, kit.InputOpts{Mono: true, Invalid: urlErr != "", Placeholder: i18n.T("settings.models.url.ph"), Flat: true}).Grow(1).Shrink(1)
					focused := in.Focused()
					if f.urlFocus && !focused {
						f.normalize()
					}
					f.urlFocus = focused
					if f.canResetURL() {
						kit.Button(c, i18n.T("settings.models.reset_url"), kit.ButtonOpts{Variant: kit.Ghost}).TextColor(t.Muted).
							OnClick(func() { f.baseURL = providerURL[f.provider] })
					}
				})
				return in
			}, func() {
				if f.custom() {
					m.gatewayChips(c, f)
				}
				if urlErr != "" {
					kit.Text(c, urlErr, t.Text.Xs).TextColor(t.Danger)
				}
			})
			m.keyField(c, f)
		}
	})
	kit.Separator(c, false)
	ui.Column(c).Gap(th.Space(4)).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
			caps := i18n.T("settings.models.caps")
			if core.ModelProviderLogin(f.provider) != "oauth" { // a subscription has no key, and no judge to share it with
				caps += " · " + i18n.T("settings.models.caps.note")
			}
			kit.Text(c, caps, t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
			m.refreshButton(c, f)
		})
		genWarn, judgeWarn := offWarns(f, m.a.settings, m.services)
		gen, judge := core.ProviderFormats(f.provider)
		capCard(c, i18n.T("settings.models.use.gen"), i18n.T("settings.models.use.gen.desc"), i18n.Tf("settings.models.gen.unsupported", svcProvider(f.provider)),
			i18n.T("settings.models.gen.switch"), len(gen) > 0, &f.genOn, genWarn, func() {
				formatSegments(c, i18n.T("settings.models.gen.formats"), gen, &f.kind)
				reqLine(c, gen, f.kind, core.ModelEndpoint(f.kind, f.url()))
				m.modelPicker(c, f, f.genPicker())
				m.capStatus(c, f.gen)
			})
		capCard(c, i18n.T("settings.models.use.judge"), i18n.T("settings.models.judge.desc"), i18n.Tf("settings.models.judge.unsupported", svcProvider(f.provider)),
			i18n.T("settings.models.judge.switch"), len(judge) > 0, &f.judgeOn, judgeWarn, func() {
				if formatSegments(c, i18n.T("settings.models.judge.formats"), judge, &f.judgeFormat) {
					f.setJudgeFormat()
					if f.custom() || f.anyKey() { // the other format's models
						m.fetch(f)
					}
				}
				reqLine(c, judge, f.judgeFormat, ai.JudgeURL(ai.JudgeFormat(f.judgeFormat), f.url()))
				m.modelPicker(c, f, f.judgePicker())
				m.capStatus(c, f.judge)
			})
	})
}

func (m *modelsView) accountRow(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	formRow(c, i18n.T("settings.models.account"), func() ui.Element {
		return ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).MinHeight(th.Space(8)).Children(func() {
			switch {
			case f.signIn.waiting():
				kit.Spinner(c)
				kit.Text(c, i18n.T("settings.models.login.opened"), t.Text.Xs).TextColor(t.Muted)
				if u := f.signIn.url; u != "" {
					kit.Button(c, i18n.T("settings.models.login.reopen"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { m.openURL(u) })
				}
				kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(m.cancelForm)
			case f.email != "":
				kit.StatusDot(c, kit.OK).Shrink(0)
				kit.Text(c, f.email, t.Text.Xs).Font(theme.Mono)
				kit.Button(c, i18n.T("settings.models.login.again"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Flat: true}).
					OnClick(func() { m.login(f) })
			default:
				kit.Button(c, i18n.T("settings.models.login."+f.provider), kit.ButtonOpts{Variant: kit.Outline, Flat: true}).
					OnClick(func() { m.login(f) })
			}
		})
	}, func() {
		if f.loginErr != "" {
			kit.Text(c, f.loginErr, t.Text.Xs).TextColor(t.Danger)
		}
	})
}

// capCard is a capability as a form row: its name in the label column, then
// its switch (or 不支持) beside what it does, and while on, body; warn says
// what turning it off does to the defaults.
func capCard(c *ui.Context, title, desc, unsupported, switchLabel string, supported bool, on *bool, warn string, body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		ui.Text(c, title).Width(theme.FormLabelWidth).Shrink(0).Margin(th.Space(1), 0, 0, 0)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(th.Space(1.5)).Children(func() {
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				if supported {
					kit.Switch(c, on).Label(switchLabel).Shrink(0)
				} else {
					kit.BadgeWith(c, i18n.T("settings.models.unsupported"), kit.BadgeOpts{Muted: true}).Shrink(0)
					desc = unsupported
				}
				kit.Text(c, desc, t.Text.Xs).TextColor(t.Muted).Grow(1).Shrink(1)
			})
			if supported && *on {
				body()
			}
			if warn != "" {
				kit.Alert(c, kit.ToneWarning, warn, nil)
			}
		})
	})
}

// formatSegments chooses *sel among list, when there is more than one, and
// reports a choice.
func formatSegments(c *ui.Context, label string, list []string, sel *string) (changed bool) {
	if len(list) < 2 {
		return false
	}
	labels := make([]string, len(list))
	for i, id := range list {
		labels[i] = formats[id].label
	}
	i := max(0, slices.Index(list, *sel))
	kit.Tabs(c, &i, kit.TabsOpts{}, kit.TabLabels(labels...)).Label(label).AlignSelf(ui.Start)
	if list[i] != *sel {
		*sel, changed = list[i], true
	}
	return changed
}

// reqLine is where requests go; a single format is named before it.
func reqLine(c *ui.Context, list []string, format, url string) {
	t, th := theme.Of(c), c.Theme()
	text := i18n.T("settings.models.req")
	if len(list) == 1 {
		text = formats[format].label + " · " + text
	}
	ui.Row(c).Wrap().AlignItems(ui.Center).Gap(th.Space(1)).Children(func() { // the gap is the design's space before the address
		kit.Text(c, text, t.Text.Xs).TextColor(t.Muted)
		kit.Text(c, url, t.Text.Xs).Font(theme.Mono).TextColor(t.Muted)
	})
}

// capStatus is a capability test's line: testing grey, ok green, failed red.
func (m *modelsView) capStatus(c *ui.Context, r capTest) {
	if r.state == "" {
		return
	}
	t, th := theme.Of(c), c.Theme()
	dot, fg, text := kit.Idle, t.Muted, i18n.T("settings.models.testing")
	switch r.state {
	case "ok":
		dot, text = kit.OK, r.text
	case "fail":
		dot, fg, text = kit.Error, t.DangerText, cmp.Or(r.text, i18n.T("settings.models.fail.error"))
	}
	ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
		kit.StatusDot(c, dot)
		kit.Text(c, text, t.Text.Xs).LineHeight(theme.LineProse).TextColor(fg) // CJK beside Latin digits: the design's line is that tall
		if r.usage {
			kit.Button(c, i18n.T("settings.models.manage_usage"), kit.ButtonOpts{Variant: kit.Link, Size: kit.SizeXs}).OnClick(func() { m.openURL(chatGPTUsageURL) })
		}
	})
}

// tiles are the providers, with what each can do.
func (m *modelsView) tiles(c *ui.Context, f *modelForm) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.RadioGroup(c, func() {
		for _, p := range providerGroups[m.group] {
			sub := i18n.T("settings.models.tile." + p)
			if core.ModelProviderLogin(p) != "oauth" {
				gen, judge := core.ProviderFormats(p)
				var caps []string
				if len(gen) > 0 {
					caps = append(caps, i18n.T("settings.models.cap.gen"))
				}
				if len(judge) > 0 {
					caps = append(caps, i18n.T("settings.models.cap.judge"))
				}
				sub = strings.Join(caps, " · ")
			}
			name := svcProvider(p)
			b := kit.ChoiceCard(c, ui.RadioBase(c.Key(p), &f.provider, p).Label(name).Grow(1).Basis(0).MinWidth(0).Radius(t.Radius.Xl).Padding(th.Space(2.5), th.Space(3)), kit.ChoiceCardOpts{Selected: f.provider == p}, func() {
				ui.Column(c).Gap(th.Space(0.5)).Children(func() {
					ui.Text(c, name).FontWeight(500).SingleLine() // the row's CJK caption sets its line
					kit.Text(c, sub, t.Text.Xs).TextColor(t.Muted).SingleLine()
				})
			})
			if b.Changed() { // another provider starts the form over
				m.cancelForm()
				m.form = newForm(p)
			}
		}
	}).Row().Gap(th.Space(2))
}

// gatewayChips are the usual compatible services, to fill the address (and
// while it follows the address, the name) of 其他服务.
func (m *modelsView) gatewayChips(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		kit.Text(c, i18n.T("settings.models.common"), t.Text.Xs).TextColor(t.Muted).Margin(0, th.Space(0.5), 0, 0)
		for _, g := range gateways {
			name := cmp.Or(g.name, i18n.T("settings.models.local.name"))
			on := f.baseURL == g.url
			kit.ToggleChip(c.Key(g.url), name, on, kit.ChoiceDefault).Label(name + " " + i18n.T("settings.models.common")).
				OnClick(func() { f.pickGateway(name, g.url) })
		}
	})
}

// picker is one capability's models: generate ticks any number, judge chooses one.
type picker struct {
	offered  []string
	on       func(id string) bool
	pick     func(id string) // a chip was clicked
	manual   *string         // the ID being typed
	add      func()          // adds what manual holds
	list     listing         // how the fetch went
	hint     bool            // nothing fetched yet: say where the models will come from
	unlisted bool            // the format has no list: the offered are its usual IDs
	view     *pickerView     // the filter, once there are many
	cap      string          // the capability its chips' menu tests
}

// pickerView is what a picker with many models shows: those matching query,
// by ID or name, and only the chosen ones while onlyOn.
type pickerView struct {
	query  string
	onlyOn bool
}

func (v *pickerView) filter(ids []string, names map[string]string, on func(string) bool) []string {
	q := strings.ToLower(strings.TrimSpace(v.query))
	var out []string
	for _, id := range ids {
		if v.onlyOn && !on(id) {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(id), q) || strings.Contains(strings.ToLower(names[id]), q) {
			out = append(out, id)
		}
	}
	return out
}

func (f *modelForm) genPicker() picker {
	return picker{offered: f.offered, manual: &f.manual, add: f.addManual, list: f.list, hint: len(f.offered) == 0, view: &f.genView, cap: core.CapGen,
		on: func(id string) bool { return slices.Contains(f.models, id) },
		pick: func(id string) {
			if slices.Contains(f.models, id) {
				f.models = slices.DeleteFunc(f.models, func(s string) bool { return s == id })
			} else {
				f.models = append(f.models, id)
			}
		}}
}

func (f *modelForm) judgePicker() picker {
	return picker{offered: f.decList(), manual: &f.judgeManual, add: f.addJudgeManual, list: f.judgeList, view: &f.judgeView, cap: core.CapJudge,
		hint: f.id == 0 && len(f.judgeModels) == 0, unlisted: !ai.JudgeFormat(f.judgeFormat).Lists(f.url()),
		on: func(id string) bool { return id == f.judgeModel }, pick: func(id string) { f.judgeModel = id }}
}

// manyModels is how many offered models bring the filter and its toggle.
const manyModels = 12

// modelPicker is p's models as chips in a box that scrolls past six rows,
// filtered once there are many, an inline field for a model ID by hand, and
// where the models came from with a way to fetch them again.
func (m *modelsView) modelPicker(c *ui.Context, f *modelForm, p picker) {
	t, th := theme.Of(c), c.Theme()
	many := len(p.offered) > manyModels
	shown := p.offered
	if many {
		shown = p.view.filter(p.offered, f.names, p.on)
	}
	manual := func() {
		ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
			kit.Input(c, p.manual, kit.InputOpts{Mono: true, Placeholder: i18n.T("settings.models.manual"), Flat: true, Size: kit.SizeSm}).Label(i18n.T("settings.models.manual")).
				Width(theme.ModelIDInputWidth).OnSubmit(p.add)
			kit.IconButton(c, "plus", i18n.T("settings.models.manual.add"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(p.add)
		})
	}
	if many { // the filter, its toggle and the hand-typed ID stay above the scrolling chips
		ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ph := i18n.Tf("settings.models.filter", len(p.offered))
			kit.Input(c, &p.view.query, kit.InputOpts{Placeholder: ph, Flat: true, Size: kit.SizeSm}).Label(ph).Grow(1).MinWidth(theme.ModelIDInputWidth)
			label := i18n.T("settings.models.only_on")
			kit.ToggleChip(c, label, p.view.onlyOn, kit.ChoiceDefault).OnClick(func() { p.view.onlyOn = !p.view.onlyOn })
			manual()
		})
	}
	chips := func() {
		ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			for _, name := range shown {
				on := p.on(name)
				label, font := name, theme.Mono
				if n := f.names[name]; n != "" {
					label, font = n, "" // a display name reads as prose, an ID as code
				}
				b := kit.ToggleChipFrame(c.Key(name), on, kit.ChoiceDefault).Label(label) // offered, plain until it is chosen: then tinted in a primary outline
				r := f.modelTests[p.cap+"\n"+name]
				b.Children(func() {
					if r.state != "" { // its test from the menu
						dot := kit.Idle
						switch r.state {
						case "ok":
							dot = kit.OK
						case "fail":
							dot = kit.Error
						}
						kit.StatusDot(c, dot)
					}
					tx := ui.Text(c, label)
					if font != "" {
						tx.Font(font)
					}
				})
				b.ContextMenu(func(menu *ui.Menu) {
					if menu.Item(i18n.T("settings.models.test_model")).Icon(menuIcon("play")).Disabled(f.cantTest()).Chosen() {
						m.testModel(f, p.cap, name)
					}
				})
				if b.Clicked() {
					p.pick(name)
				}
			}
			if !many {
				manual()
			}
		})
	}
	if many {
		ui.Column(c).FillWidth().MaxHeight(th.Space(50)).Clip().Children(func() { // six rows of chips
			ui.Scroll(c).Shrink(1).Children(func() {
				if len(shown) == 0 {
					kit.Text(c, i18n.T("settings.models.filter.none"), t.Text.Xs).TextColor(t.Muted)
				}
				chips()
			})
		})
	} else {
		chips()
	}
	if id := f.lastModel[p.cap]; id != "" { // the last chip menu test, named
		r := f.modelTests[p.cap+"\n"+id]
		if r.state != "testing" {
			r.text = cmp.Or(f.names[id], id) + " · " + cmp.Or(r.text, i18n.T("settings.models.fail.error"))
		}
		m.capStatus(c, r)
	}
	if p.unlisted {
		kit.Text(c, i18n.T("settings.models.suggested"), t.Text.Xs).TextColor(t.Muted)
		return
	}
	if f.probing {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kit.Spinner(c)
			kit.Text(c, i18n.T("settings.models.fetching"), t.Text.Xs).TextColor(t.Muted)
		})
		return
	}
	var msg string
	fg, fail := t.Muted, p.list.state == "fail"
	if fail {
		fg = t.WarningText
	}
	switch {
	case fail && p.list.err != "":
		msg = i18n.Tf("settings.models.fetch_failed", p.list.err)
	case fail:
		msg = i18n.T("settings.models.no_list")
	case p.list.state == "ok":
		msg = i18n.Tf("settings.models.fetched", p.list.n)
	case p.hint && f.oauth():
		msg = i18n.T("settings.models.hint.sub")
	case p.hint:
		msg = i18n.T("settings.models.hint")
	}
	if msg != "" {
		kit.Text(c, msg, t.Text.Xs).TextColor(fg)
	}
}

// refreshButton lists both capabilities' models again: one fetch serves both.
func (m *modelsView) refreshButton(c *ui.Context, f *modelForm) {
	t := theme.Of(c)
	if !f.anyCap() || !f.custom() && !f.anyKey() {
		return
	}
	label := i18n.T("settings.models.refresh")
	kit.Button(c, label, kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeXs, Flat: true}).TextColor(t.Muted).OnClick(func() { m.fetch(f) })
}

// keyField is the API key and the decision a stored key needs once the
// address or format changes. Entering it, or leaving it, lists the models.
func (m *modelsView) keyField(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	label, ph := i18n.T("settings.models.api_key"), i18n.T("settings.models.key.ph."+f.provider)
	if f.hasKey {
		ph = i18n.T("settings.secret_kept")
	}
	formRow(c, label, func() ui.Element {
		var in ui.Element
		icon, tip := "eye", i18n.T("settings.models.key.show")
		if f.showKey {
			icon, tip = "eye-off", i18n.T("settings.models.key.hide")
		}
		in = kit.Input(c, &f.key, kit.InputOpts{Invalid: f.keyErr != "", Flat: true, Password: !f.showKey, Placeholder: ph,
			// only what was typed this session: a stored key is never read into the form
			Trailing: func() {
				kit.IconButton(c, icon, tip, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeXs}).OnClick(func() { f.showKey = !f.showKey })
			},
		}).OnSubmit(func() { m.fetch(f) })
		focused := in.Focused()
		if f.keyFocus && !focused {
			m.fetch(f)
		}
		f.keyFocus = focused
		return in
	}, func() {
		if f.keyErr != "" {
			kit.Text(c, f.keyErr, t.Text.Xs).TextColor(t.Danger)
		} else {
			kit.Text(c, i18n.T("settings.models.api_key.help"), t.Text.Xs).TextColor(t.Muted)
		}
		if f.hasKey && f.key == "" && f.moved() {
			ui.Column(c).Gap(th.Space(2)).Children(func() {
				if !f.noKey {
					kit.Alert(c, kit.ToneWarning, i18n.T("settings.models.key_moved"), nil)
				}
				kit.Checkbox(c, &f.noKey, i18n.T("settings.models.no_key"))
			})
		}
	})
}
