package app

import (
	"cmp"
	"fmt"
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

// view is the Intelligence page's 模型 tab, under the page's heading (aiView).
func (m *modelsView) view(c *ui.Context) {
	if m.empty() {
		kit.Empty(c, kit.EmptyOpts{Icon: "cpu", Title: i18n.T("settings.models.empty.title"), Desc: i18n.T("settings.models.empty.desc"), Dashed: true, Actions: func() {
			kit.EmptyAction(c, i18n.T("settings.models.add"), kit.ButtonOpts{}).OnClick(m.add)
			kit.EmptyAction(c, i18n.T("settings.models.local"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(m.addLocal)
		}})
	}
	if st := m.a.settings; len(m.services) > 0 || len(m.plugins) > 0 || isPluginRef(st.DefaultModel) || isPluginRef(st.SmallModel) || isPluginRef(st.JudgeService) {
		u := deriveUsage(m.a.settings, m.services, m.plugins, m.pluginStatus)
		m.usage(c, u)
		m.list(c, u)
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
// the site it talks to, else its initial, as a sender's in mail.
func (m *modelsView) svcMark(c *ui.Context, s core.ModelService) {
	t, th := theme.Of(c), c.Theme()
	if glyph := icons.BrandIcon(s.Provider); glyph != nil {
		ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).AlignSelf(ui.Start).Margin(theme.ServiceMarkOffset, 0, 0, 0).Radius(t.Radius.Lg).Background(t.Background).Center().Label(s.Name).Children(func() {
			ui.Icon(c, glyph).Size(th.Space(5), th.Space(5)).TextColor(t.Foreground)
		})
		return
	}
	m.loadAvatar(s)
	kit.AvatarImage(c, s.Name, m.avatars[s.BaseURL]).AlignSelf(ui.Start).Margin(theme.ServiceMarkOffset, 0, 0, 0)
}

// list is the services, one card row each.
func (m *modelsView) list(c *ui.Context, u usage) {
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
						ui.Text(c, s.Name).FontWeight(500).LineHeight(theme.LineFor(s.Name)).SingleLine().Shrink(1)
						l, label := svcLook(s)
						statusLine(c, line{label, l})
					})
					kit.Text(c, svcSubtitle(s), t.Text.Xs).Font(theme.Mono).LineHeight(theme.LineSm).TextColor(t.Muted).SingleLine()
					ui.Row(c).Wrap().Gap(th.Space(1.5)).Children(func() {
						if s.Kind != "" && len(s.Models) > 0 {
							capChip(c, i18n.Plural("settings.models.gen", len(s.Models)), u.genSvc != nil && u.genSvc.ID == s.ID)
						}
						if s.JudgeFormat != "" && s.JudgeModel != "" {
							capChip(c, i18n.Tf("settings.models.judge_chip", s.JudgeModel), u.judgeSvc != nil && u.judgeSvc.ID == s.ID)
						}
						if s.ManagedBy != "" {
							capChip(c, i18n.Tf("settings.managed_by", s.ManagedBy), false)
						}
					})
				})
				if edit != nil {
					ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
				}
			}).Padding(th.Space(2), th.Space(4)).ContextMenu(func(menu *ui.Menu) {
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
			ui.Row(c.Key("apple-intelligence")).Gap(th.Space(3)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(4)).Children(func() {
				ui.Box(c).Size(th.Space(8), th.Space(8)).Shrink(0).AlignSelf(ui.Start).Margin(theme.ServiceMarkOffset, 0, 0, 0).Radius(t.Radius.Lg).Background(t.Background).Center().Label("Apple Intelligence").Children(func() {
					ui.Icon(c, icons.Get("sparkles")).Size(th.Space(5), th.Space(5)).TextColor(t.Foreground)
				})
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "Apple Intelligence").FontWeight(500).LineHeight(theme.LineFor("Apple Intelligence")).SingleLine().Shrink(1)
						statusLine(c, pluginLook(p, m.pluginStatus))
					})
					kit.Text(c, i18n.T("settings.models.apple_intelligence.sub"), t.Text.Xs).Font(theme.Mono).LineHeight(theme.LineSm).TextColor(t.Muted).SingleLine()
					ui.Row(c).Wrap().Gap(th.Space(1.5)).Children(func() {
						st := m.a.settings
						var genOn, judgeOn bool
						for _, mod := range p.Models {
							ref := pluginRef(p.Plugin, mod.ID)
							judgeOn = judgeOn || mod.Judges() && st.JudgeService == ref
							genOn = genOn || !mod.Judges() && (st.DefaultModel == ref || st.SmallModel == ref)
						}
						capChip(c, i18n.Plural("settings.models.gen", 1), genOn)
						capChip(c, i18n.Tf("settings.models.judge_chip", "Apple Intelligence"), judgeOn)
					})
				})
			}).ContextMenu(func(menu *ui.Menu) {
				if menu.Item(i18n.T("settings.models.test")).Chosen() {
					m.testPluginRow(p)
				}
			})
		})
	}
	settingsSection(c, i18n.T("settings.models.services"), i18n.T("settings.models.foot"), rows...)
}

// statusLine is a dot and a text in the look's colors.
func statusLine(c *ui.Context, l line) ui.Element {
	t, th := theme.Of(c), c.Theme()
	dot, fg := l.look.colors(t)
	return ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Shrink(0).Radius(t.Radius.Full).Background(dot)
		kit.Text(c, l.text, t.Text.Xs).TextColor(fg)
	})
}

// capChip is a capability chip; the one in use as the default carries a check.
func capChip(c *ui.Context, text string, check bool) {
	o := kit.BadgeOpts{Tone: kit.BadgeSurface, Muted: !check}
	if check {
		o.Icon, o.IconLabel = "check", i18n.T("settings.models.default_badge")
	}
	kit.BadgeWith(c, text, o).Shrink(0)
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
		s := kit.SelectWith(c, kit.SelectOpts{Flat: true}, v, opts).Width(theme.UsageSelectMin).Label(label).Disabled(u.disabled || managed)
		if s.Changed() {
			val := *v
			m.setModel(func(st *core.Settings) { set(st, val) })
		}
	}
	gen, small, judge := i18n.T("settings.models.use.gen"), i18n.T("settings.models.use.small"), i18n.T("settings.models.use.judge")
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		sectionTitle(c, i18n.T("settings.models.usage"))
		if u.alert != "" {
			ui.Row(c).Wrap().AlignItems(ui.Center).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Xl).
				Border(theme.Hairline, t.DangerBorder).Background(t.DangerBg).Children(func() {
				ui.Text(c, u.alert).TextColor(t.DangerText).Grow(1).Shrink(1).MinWidth(theme.SidebarMinWidth)
				svc := *u.genSvc
				fixText := t.OnSolid
				if t.Dark {
					fixText = t.DangerBg
				}
				settingButton(c, i18n.T("settings.models.use.fix"), kit.Default, false).Background(t.DangerText).TextColor(fixText).OnClick(func() { m.openEdit(svc) })
			})
		}
		settingsCard(c,
			func() {
				desc, by := i18n.T("settings.models.use.gen.desc"), a.settings.DefaultModelBy
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
				usageRow(c, judge, i18n.T("settings.models.use.judge.desc"), &u.judge, func() {
					sel(judge, &m.judge, u.judgeOpts, func(st *core.Settings, v string) { st.JudgeService = v }, false)
				})
			},
			func() {
				help := i18n.T("settings.models.threshold.help")
				if u.judgeSvc == nil && !u.pluginJudge {
					help += " " + i18n.T("settings.models.threshold.binary")
				}
				settingRow(c, i18n.T("settings.models.threshold"), help, func() {
					threshold := a.settings.JudgeThreshold
					ui.Row(c).Width(theme.UsageSelectMin).Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
						slider := ui.Slider(c, &threshold, core.MinJudgeThreshold, core.MaxJudgeThreshold).Step(0.01).Grow(1).Shrink(1).Label(i18n.T("settings.models.threshold"))
						if slider.Changed() {
							m.setModel(func(st *core.Settings) { st.JudgeThreshold = threshold })
						}
						kit.Text(c, fmt.Sprintf("%.0f%%", threshold*100), t.Text.Xs).Width(th.Space(10)).TextAlign(ui.End).TextColor(t.Muted)
					})
				})
			})
	})
	if u.suggest {
		ui.Row(c).Wrap().AlignItems(ui.Center).Children(func() {
			kit.Text(c, i18n.T("settings.models.use.suggest"), t.Text.Xs).TextColor(t.Muted)
			kit.TextLink(c, i18n.T("settings.models.add"), t.Foreground, t.Text.Xs).OnClick(m.add)
		})
	}
}

// usageRow is a row of the 用途 card: label, description and status line
// on the left, the select on the right.
func usageRow(c *ui.Context, label, desc string, l *line, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, label, t.Text.Sm).LineHeight(theme.LineFor(label)).FontWeight(500)
			kit.Text(c, desc, t.Text.Xs).TextColor(t.Muted)
			if l != nil {
				statusLine(c, *l).Margin(th.Space(1.25), 0, 0, 0) // the design's 4, and the pixel its CJK line height adds
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
	if m.group == 0 {
		desc = i18n.T("settings.models.dialog.desc.sub")
	}
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
			footButton(c, i18n.T("common.delete"), kit.Default, f.busy).Background(t.Danger).TextColor(t.OnSolid).OnClick(func() { m.remove(f) })
		})
		return
	}
	if f.id != 0 {
		footButton(c, i18n.T("settings.models.remove"), kit.Ghost, f.busy).TextColor(t.Danger).OnClick(func() { f.confirmDel = true })
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
	return kit.Button(c, label, kit.ButtonOpts{Variant: v, Disabled: disabled, Flat: true}).PaddingX(c.Theme().Space(3))
}

// formRow is a label in its own 96 px column beside the control; the label
// focuses the control it returns.
func formRow(c *ui.Context, label string, control func() ui.Element, extra func()) {
	th := c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		var in ui.Element
		// 4 down: where the design's 32 px line box (Geist's metrics) puts the glyphs, a little above center
		l := ui.Text(c, label).Width(theme.FormLabelWidth).Shrink(0).LineHeight(theme.LineRow).Margin(th.Space(1), 0, 0, 0)
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
		ui.Row(c).Children(func() { // a row, so the tabs hug their labels instead of stretching
			kit.Tabs(c, &m.group, i18n.T("settings.models.group.sub"), i18n.T("settings.models.group.api"))
		})
		if providerGroup(f.provider) != m.group { // another group starts the form over on its first tile
			m.cancelForm()
			f = newForm(providerGroups[m.group][0])
			m.form = f
		}
		m.tiles(c, f)
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
						kit.Button(c, i18n.T("settings.models.reset_url"), kit.ButtonOpts{Variant: kit.Ghost}).PaddingX(th.Space(2.5)).TextColor(t.Muted).
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
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		ui.Row(c).Justify(ui.SpaceBetween).AlignItems(ui.End).Gap(th.Space(3)).Children(func() {
			ui.Text(c, i18n.T("settings.models.caps")).FontWeight(600).LineHeight(theme.LineFor(i18n.T("settings.models.caps")))
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				if core.ModelProviderLogin(f.provider) != "oauth" { // a subscription has no key, and no judge to share it with
					kit.Text(c, i18n.T("settings.models.caps.note"), t.Text.Xs).TextColor(t.Muted)
				}
				m.refreshButton(c, f)
			})
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

// capCard is a capability: its switch (or 不支持), and while on, body; warn
// says what turning it off does to the defaults.
func capCard(c *ui.Context, title, desc, unsupported, switchLabel string, supported bool, on *bool, warn string, body func()) {
	t, th := theme.Of(c), c.Theme()
	card := ui.Column(c).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Background(t.Background)
	if !supported {
		card.Background(t.Sidebar)
		desc = unsupported
	}
	card.Children(func() {
		ui.Row(c).AlignItems(ui.Start).Gap(th.Space(3)).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				ui.Text(c, title).FontWeight(600).LineHeight(theme.LineFor(title))
				kit.Text(c, desc, t.Text.Xs).TextColor(t.Muted)
			})
			if supported {
				kit.Switch(c, on).Label(switchLabel)
			} else {
				kit.BadgeWith(c, i18n.T("settings.models.unsupported"), kit.BadgeOpts{Muted: true}).Shrink(0)
			}
		})
		if supported && *on {
			body()
		}
		if warn != "" {
			ui.Box(c).Padding(th.Space(2), th.Space(2.5)).Radius(t.Radius.Lg).Border(theme.Hairline, t.WarningBorder).Background(t.WarningBg).Label(warn).Children(func() {
				kit.Text(c, warn, t.Text.Xs).TextColor(t.WarningText)
			})
		}
	})
}

// formatSegments chooses *sel among list, when there is more than one, and
// reports a choice.
func formatSegments(c *ui.Context, label string, list []string, sel *string) (changed bool) {
	if len(list) < 2 {
		return false
	}
	t, th := theme.Of(c), c.Theme()
	ui.RadioGroup(c, func() {
		for _, id := range list {
			r := ui.RadioBase(c.Key(id), sel, id).Height(th.Space(7)).PaddingX(th.Space(2.5)).Radius(t.Radius.Md).TextColor(t.Muted)
			changed = changed || r.Changed()
			if *sel == id {
				kit.Elevate(c, r.Background(t.Background).TextColor(t.Foreground), theme.ShadowSm)
			}
			r.Children(func() { kit.Text(c, formats[id].label, t.Text.Xs) })
		}
	}).Row().Label(label).Wrap().Gap(th.Space(1)).Padding(th.Space(0.5)).Radius(t.Radius.Lg).Background(t.Hover).AlignSelf(ui.Start)
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
	dot, fg, text := t.Idle, t.Muted, i18n.T("settings.models.testing")
	switch r.state {
	case "ok":
		dot, fg, text = t.Success, t.SuccessText, r.text
	case "fail":
		dot, fg, text = t.Danger, t.DangerText, cmp.Or(r.text, i18n.T("settings.models.fail.error"))
	}
	ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Shrink(0).Radius(t.Radius.Full).Background(dot)
		kit.Text(c, text, t.Text.Xs).LineHeight(theme.LineProse).TextColor(fg) // CJK beside Latin digits: the design's line is that tall
		if r.usage {
			kit.TextLink(c, i18n.T("settings.models.manage_usage"), fg, t.Text.Xs).OnClick(func() { m.openURL(chatGPTUsageURL) })
		}
	})
}

// tiles are the providers, with what each can do.
func (m *modelsView) tiles(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	ui.RadioGroup(c, func() {
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
			b := ui.RadioBase(c.Key(p), &f.provider, p).Label(name).Grow(1).Basis(0).MinWidth(0).Radius(t.Radius.Xl).Padding(th.Space(2.5), th.Space(3)).Border(theme.Stroke, t.Border).Background(t.Background)
			if b.Changed() { // another provider starts the form over
				m.cancelForm()
				m.form = newForm(p)
			}
			if f.provider == p {
				b.Background(t.Hover).Border(theme.Stroke, t.Primary)
			}
			b.Children(func() {
				ui.Column(c).Gap(th.Space(0.5)).Children(func() {
					ui.Text(c, name).FontWeight(500).LineHeight(theme.LineRow).SingleLine() // the row's CJK caption sets its line
					kit.Text(c, sub, t.Text.Xs).LineHeight(theme.LineRow).TextColor(t.Muted).SingleLine()
				})
			})
		}
	}).Row().Label(i18n.T("settings.models.provider")).Gap(th.Space(2))
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
			b := kit.Pressable(c.Key(g.url), kit.PressableOpts{Bare: true, OnClick: func() { f.pickGateway(name, g.url) }}).
				Label(name+" "+i18n.T("settings.models.common")).Height(th.Space(6)).Padding(0, th.Space(2)).Radius(t.Radius.Md)
			fg := t.Muted
			if on {
				b.Background(t.Hover).Border(theme.Stroke, t.Primary)
				fg = t.Foreground
			} else {
				b.Border(theme.Stroke, t.Border).Background(t.Background)
			}
			b.Children(func() { kit.Text(c, name, t.Text.Xs).TextColor(fg) })
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
			kit.IconButton(c, "plus", i18n.T("settings.models.manual.add"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Flat: true}).OnClick(p.add)
		})
	}
	if many { // the filter, its toggle and the hand-typed ID stay above the scrolling chips
		ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ph := i18n.Tf("settings.models.filter", len(p.offered))
			kit.Input(c, &p.view.query, kit.InputOpts{Placeholder: ph, Flat: true, Size: kit.SizeSm}).Label(ph).Grow(1).MinWidth(theme.ModelIDInputWidth)
			label := i18n.T("settings.models.only_on")
			b := kit.Pressable(c, kit.PressableOpts{Bare: true, OnClick: func() { p.view.onlyOn = !p.view.onlyOn }}).Label(label).Height(th.Space(7)).Padding(0, th.Space(2.5)).Radius(t.Radius.Md)
			fg := t.Muted
			if p.view.onlyOn {
				b.Background(t.Hover).Border(theme.Stroke, t.Primary)
				fg = t.Foreground
			} else {
				b.Background(t.Background).Border(theme.Stroke, t.Border)
			}
			b.Children(func() { kit.Text(c, label, t.Text.Xs).TextColor(fg) })
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
				b := kit.Pressable(c.Key(name), kit.PressableOpts{Bare: true}).Label(label).Height(th.Space(7)).Padding(0, th.Space(2.5)).Radius(t.Radius.Md)
				fg := t.Foreground
				if on {
					b.Background(t.Hover).Border(theme.Stroke, t.Primary)
				} else {
					b.Background(t.Background).Border(theme.Stroke, t.DividerHover).BorderStyle(ui.BorderDashed) // the design's neutral-300 dash
					fg = t.Muted
				}
				r := f.modelTests[p.cap+"\n"+name]
				b.Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
					if r.state != "" { // its test from the menu
						dot := t.Idle
						switch r.state {
						case "ok":
							dot = t.Success
						case "fail":
							dot = t.Danger
						}
						ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Shrink(0).Radius(t.Radius.Full).Background(dot)
					}
					tx := kit.Text(c, label, t.Text.Xs).TextColor(fg)
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
		box := kit.InputGroup(c, kit.InputOpts{Invalid: f.keyErr != "", Flat: true}).Padding(0, th.Space(0.5), 0, 0)
		box.Children(func() {
			in = kit.Input(c, &f.key, kit.InputOpts{Password: !f.showKey, Placeholder: ph, Bare: true}).Grow(1).Shrink(1).PaddingX(th.Space(2.5)).OnSubmit(func() { m.fetch(f) })
			icon, tip := "eye", i18n.T("settings.models.key.show")
			if f.showKey {
				icon, tip = "eye-off", i18n.T("settings.models.key.hide")
			}
			// only what was typed this session: a stored key is never read into the form
			kit.IconButton(c, icon, tip, kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(func() { f.showKey = !f.showKey })
		})
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
					kit.Alert(c, kit.Warning, i18n.T("settings.models.key_moved"), nil)
				}
				kit.Checkbox(c, &f.noKey, i18n.T("settings.models.no_key"))
			})
		}
	})
}
