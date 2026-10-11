package app

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

func (m *modelsView) view(c *ui.Context) {
	empty := m.loaded && len(m.services) == 0
	var add []func()
	if !empty {
		add = append(add, func() { settingButton(c, i18n.T("settings.models.add"), kit.Default, false).OnClick(m.add) })
	}
	pageHeading(c, i18n.T("settings.models"), i18n.T("settings.models.sub"), add...)
	if empty {
		kit.Empty(c, kit.EmptyOpts{Icon: "cpu", Title: i18n.T("settings.models.empty.title"), Desc: i18n.T("settings.models.empty.desc"), Dashed: true, Actions: func() {
			kit.EmptyAction(c, i18n.T("settings.models.add"), kit.ButtonOpts{}).OnClick(m.add)
			kit.EmptyAction(c, i18n.T("settings.models.local"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(m.addLocal)
		}})
	}
	if len(m.services) > 0 {
		u := deriveUsage(m.a.settings, m.services)
		m.usage(c, u)
		m.list(c, u)
	}
	if m.open {
		m.dialog(c)
	}
}

// close drops the form with its dialog: its pending results are ignored.
func (m *modelsView) close() { m.open, m.form = false, nil }

// list is the services, one card row each.
func (m *modelsView) list(c *ui.Context, u usage) {
	t, th := theme.Of(c), c.Theme()
	var rows []func()
	for _, s := range m.services {
		rows = append(rows, func() {
			listRow(c.Key(s.ID), 0, false, func() { m.openEdit(s) }, func() {
				ui.Box(c).Size(th.Space(9), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Background(t.Hover).Center().Children(func() {
					kit.Text(c, badge(s.Provider), t.Text.Sm).Font(theme.Mono)
				})
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, s.Name).FontWeight(500).LineHeight(theme.LineFor(s.Name)).SingleLine().Shrink(1)
						l, label := svcLook(s)
						statusLine(c, line{label, l})
					})
					kit.Text(c, s.BaseURL, t.Text.Sm).Font(theme.Mono).LineHeight(theme.LineBase).TextColor(t.Muted).SingleLine()
					ui.Row(c).Wrap().Gap(th.Space(1.5)).Children(func() {
						if s.Kind != "" && len(s.Models) > 0 {
							capChip(c, i18n.Plural("settings.models.gen", len(s.Models)), u.genSvc != nil && u.genSvc.ID == s.ID)
						}
						if s.JudgeFormat != "" && s.JudgeModel != "" {
							capChip(c, i18n.Tf("settings.models.judge_chip", s.JudgeModel), u.judgeSvc != nil && u.judgeSvc.ID == s.ID)
						}
					})
				})
				ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
			}).Padding(th.Space(2), th.Space(4))
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
		kit.Text(c, l.text, t.Text.Sm).TextColor(fg)
	})
}

// capChip is a capability chip; the one in use as the default carries a check.
func capChip(c *ui.Context, text string, check bool) {
	t, th := theme.Of(c), c.Theme()
	fg := t.Muted
	if check {
		fg = t.Foreground
	}
	ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Height(th.Space(5)).Padding(0, th.Space(1.5)).Radius(t.Radius.Sm).Background(t.Hover).TextColor(fg).Shrink(0).Children(func() {
		if check {
			ui.Icon(c, icons.Get("check")).Size(theme.IconXs, theme.IconXs).Label(i18n.T("settings.models.default_badge"))
		}
		kit.Text(c, text, t.Text.Sm).LineHeight(theme.LineTight)
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
	sel := func(label string, v *string, opts []kit.Option[string], set func(*core.Settings, string)) {
		s := kit.SelectWith(c, kit.SelectOpts{Radius: t.Radius.Lg, MinWidth: theme.UsageSelectMin, MaxWidth: theme.SettingsSelectMax, Flat: true}, v, opts).Label(label).Disabled(u.disabled)
		if u.disabled {
			s.Opacity(0.5)
		}
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
				settingButton(c, i18n.T("settings.models.use.fix"), kit.Default, false).Background(t.DangerText).TextColor(t.OnSolid).FontWeight(400).OnClick(func() { m.openEdit(svc) })
			})
		}
		settingsCard(c,
			func() {
				usageRow(c, gen, i18n.T("settings.models.use.gen.desc"), &u.gen, func() {
					sel(gen, &m.defModel, u.genOpts, func(st *core.Settings, v string) { st.DefaultModel = v })
				})
			},
			func() {
				usageRow(c, small, i18n.T("settings.models.use.small.desc"), nil, func() {
					sel(small, &m.smallModel, u.smallOpts, func(st *core.Settings, v string) { st.SmallModel = v })
				})
			},
			func() {
				usageRow(c, judge, i18n.T("settings.models.use.judge.desc"), &u.judge, func() {
					sel(judge, &m.judge, u.judgeOpts, func(st *core.Settings, v string) { st.JudgeService = v })
				})
			})
	})
	if u.suggest {
		ui.Row(c).Wrap().AlignItems(ui.Center).Children(func() {
			kit.Text(c, i18n.T("settings.models.use.suggest"), t.Text.Sm).TextColor(t.Muted)
			kit.TextLink(c, i18n.T("settings.models.add"), t.Foreground, t.Text.Sm).OnClick(m.add)
		})
	}
}

// usageRow is a row of the 用途 card: label, description and status line
// on the left, the select on the right.
func usageRow(c *ui.Context, label, desc string, l *line, control func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).FillWidth().Gap(th.Space(6)).Padding(th.Space(2), th.Space(4)).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, label, t.Text.Body).LineHeight(theme.LineFor(label)).FontWeight(500)
			kit.Text(c, desc, t.Text.Sm).TextColor(t.Muted)
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
	f.edited()
	title := i18n.T("settings.models.dialog.add")
	if f.id != 0 {
		title = i18n.Tf("settings.models.edit", f.title)
	}
	kit.DialogWide(c, &m.open, title, func() {
		ui.Scroll(c).MaxHeight(theme.DialogBodyMax).Children(func() { m.formView(c, f) })
	}, func() { m.footer(c, f) })
	if !m.open { // closed by Escape or a click outside
		m.close()
	}
}

func (m *modelsView) footer(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	if f.confirmDel {
		ui.Row(c).Grow(1).Gap(th.Space(3)).AlignItems(ui.Center).Padding(th.Space(3), th.Space(4)).Radius(t.Radius.Lg).Background(t.DangerBg).Border(theme.Hairline, t.DangerBorder).Children(func() {
			ui.Text(c, i18n.Tf("settings.models.del_confirm", f.title)).TextColor(t.DangerText).Grow(1).Shrink(1)
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { f.confirmDel = false })
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive, Disabled: f.busy}).OnClick(func() { m.remove(f) })
		})
		return
	}
	if f.id != 0 {
		kit.Button(c, i18n.T("settings.models.remove"), kit.ButtonOpts{Variant: kit.Ghost, Disabled: f.busy}).TextColor(t.Danger).OnClick(func() { f.confirmDel = true })
	}
	ui.Row(c).Grow(1).Shrink(1).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
		switch {
		case f.testing:
			kit.Spinner(c)
		case f.testMsg != "" && f.testOK:
			kit.StatusDot(c, kit.OK)
			kit.Text(c, f.testMsg, t.Text.Sm).TextColor(t.Success)
		case f.testMsg != "":
			kit.StatusDot(c, kit.Error)
			kit.Text(c, f.testMsg, t.Text.Sm).TextColor(t.Danger).Shrink(1)
		}
	})
	kit.Button(c, i18n.T("settings.models.test"), kit.ButtonOpts{Variant: kit.Outline, Disabled: f.id == 0 || f.testing}).OnClick(func() { m.test(f) })
	kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(m.close)
	save := i18n.T("common.save")
	if f.id == 0 {
		save = i18n.T("settings.models.confirm_add")
	}
	kit.Button(c, save, kit.ButtonOpts{Disabled: f.busy}).OnClick(func() { m.save(f) })
}

// formRow is a label in its own 96 px column beside the control; the label
// focuses the control it returns.
func formRow(c *ui.Context, label string, control func() ui.Element, extra func()) {
	th := c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		var in ui.Element
		l := ui.Text(c, label).FontWeight(500).Width(theme.FormLabelWidth).Shrink(0)
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
	a := m.a
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(4)).Children(func() {
		if f.id == 0 {
			m.tiles(c, f)
		}
		formRow(c, i18n.T("settings.models.kind"), func() ui.Element {
			return ui.Row(c).Wrap().Gap(th.Space(4)).Children(func() {
				for _, k := range []string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic} {
					kit.Radio(c.Key(k), &f.kind, k, kindLabel(k))
				}
			})
		}, nil)
		formRow(c, i18n.T("settings.models.name"), func() ui.Element { return kit.Input(c, &f.name, kit.InputOpts{}) }, func() {
			if f.slug == "" {
				return
			}
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				kit.Text(c, i18n.Tf("settings.models.slug", f.slug), t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
				if kit.Button(c, i18n.T("settings.models.slug.copy"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).Clicked() {
					c.WriteClipboard(f.slug)
					a.toast(i18n.T("link.copied"))
				}
			})
		})
		var urlErr string
		if f.urlErr != "" && f.baseURL == f.urlBad {
			urlErr = f.urlErr
		}
		formRow(c, i18n.T("settings.models.base_url"), func() ui.Element {
			var in ui.Element
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				in = kit.Input(c, &f.baseURL, kit.InputOpts{Mono: true, Invalid: urlErr != ""}).Grow(1).Shrink(1)
				focused := in.Focused()
				if f.urlFocus && !focused {
					f.normalize()
				}
				f.urlFocus = focused
				if f.defURL != "" && f.baseURL != f.defURL {
					kit.Button(c, i18n.T("settings.models.reset_url"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(func() { f.baseURL = f.defURL })
				}
			})
			return in
		}, func() {
			if f.preset == presetCustom {
				m.commonChips(c, f)
			}
			if urlErr != "" {
				kit.Text(c, urlErr, t.Text.Sm).TextColor(t.Danger)
			} else if u, err := core.NormalizeModelBaseURL(strings.TrimSpace(f.baseURL)); err == nil {
				kit.Text(c, i18n.Tf("settings.models.endpoint", core.ModelEndpoint(f.kind, u)), t.Text.Sm).TextColor(t.Muted)
			}
		})
		m.keyField(c, f)
		formRow(c, i18n.T("settings.models.models"), func() ui.Element { return m.modelChips(c, f) }, func() { m.modelNote(c, f) })
	})
}

// tiles are the providers, four to a row; the last is a service of the user's own.
func (m *modelsView) tiles(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	type tile struct {
		id, name, cap string
		pick          func()
	}
	var all []tile
	for _, p := range m.a.svc.ModelPresets() {
		all = append(all, tile{p.ID, p.Name, i18n.T("settings.models.cap.gen"), func() { f.pick(p) }})
	}
	all = append(all, tile{presetCustom, i18n.T("settings.models.preset.custom"), i18n.T("settings.models.cap.custom"), f.pickCustom})
	ui.Column(c).Gap(th.Space(2)).Children(func() {
		for row := range slices.Chunk(all, 4) {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				for _, tl := range row {
					on := f.preset == tl.id
					b := kit.Pressable(c.Key(tl.id), kit.PressableOpts{Bare: true, OnClick: tl.pick}).Label(tl.name).Grow(1).Basis(0).Radius(t.Radius.Xl).Padding(th.Space(2.5), th.Space(3)).
						Border(theme.Hairline, t.Border)
					if on {
						b.Background(t.Hover).Border(theme.Hairline, t.Primary)
					}
					b.Children(func() {
						ui.Column(c).Gap(th.Space(0.5)).Children(func() {
							ui.Text(c, tl.name).FontWeight(500).SingleLine()
							kit.Text(c, tl.cap, t.Text.Sm).TextColor(t.Muted).SingleLine()
						})
					})
				}
				for range 4 - len(row) { // keep the columns equal
					ui.Box(c).Grow(1).Basis(0)
				}
			})
		}
	})
}

// commonChips are the usual compatible services, to fill the name and address of 其他服务.
func (m *modelsView) commonChips(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
		kit.Text(c, i18n.T("settings.models.common"), t.Text.Sm).TextColor(t.Muted)
		for _, p := range m.a.svc.ModelPresets() {
			if p.Kind != ai.KindOpenAI {
				continue
			}
			on := f.baseURL == p.BaseURL
			b := kit.Pressable(c.Key(p.ID), kit.PressableOpts{Bare: true, OnClick: func() {
				f.name, f.baseURL, f.kind, f.keyOptional, f.helpURL = p.Name, p.BaseURL, p.Kind, !p.KeyRequired, ""
			}}).Label(p.Name+" "+i18n.T("settings.models.common")).Height(th.Space(6)).Padding(0, th.Space(2)).Radius(t.Radius.Md)
			fg := t.Muted
			if on {
				b.Background(t.Hover).Border(theme.Hairline, t.Primary)
				fg = t.Foreground
			} else {
				b.Border(theme.Hairline, t.Border).Background(t.Background)
			}
			b.Children(func() { kit.Text(c, p.Name, t.Text.Sm).TextColor(fg) })
		}
	})
}

// modelChips are the models as toggles, then an inline field for a model ID by hand.
func (m *modelsView) modelChips(c *ui.Context, f *modelForm) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
		for _, name := range f.offered {
			on := slices.Contains(f.models, name)
			b := kit.Pressable(c.Key(name), kit.PressableOpts{Bare: true}).Label(name).Height(th.Space(7)).Padding(0, th.Space(2.5)).Radius(t.Radius.Md)
			if on {
				b.Background(t.Hover).Border(theme.Hairline, t.Primary)
			} else {
				b.Border(theme.Hairline, t.Border).BorderStyle(ui.BorderDashed)
			}
			b.Children(func() { kit.Text(c, name, t.Text.Sm).Font(theme.Mono) })
			if b.Clicked() {
				if !on {
					f.models = append(f.models, name)
				} else {
					f.models = slices.DeleteFunc(f.models, func(s string) bool { return s == name })
				}
			}
		}
		ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
			kit.Input(c, &f.manual, kit.InputOpts{Mono: true, Placeholder: i18n.T("settings.models.manual")}).Label(i18n.T("settings.models.manual")).
				Height(th.Space(7)).Width(theme.ModelIDInputWidth).OnSubmit(f.addManual)
			kit.IconButton(c, "plus", i18n.T("settings.models.manual.add"), kit.ButtonOpts{Variant: kit.Outline}).Size(th.Space(7), th.Space(7)).OnClick(f.addManual)
		})
	})
}

// modelNote says where the models came from, or why the fetch failed.
func (m *modelsView) modelNote(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	retry := func(msg string) {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kit.Text(c, msg, t.Text.Sm).TextColor(t.WarningText).Shrink(1)
			kit.Button(c, i18n.T("settings.models.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: f.probing}).OnClick(func() { m.fetch(f) })
		})
	}
	switch {
	case f.probing:
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kit.Spinner(c)
			kit.Text(c, i18n.T("settings.models.fetching"), t.Text.Sm).TextColor(t.Muted)
		})
	case f.fetchErr != "":
		retry(i18n.Tf("settings.models.fetch_failed", f.fetchErr))
	case f.noList && f.checked == f.conn():
		if f.noListStatus > 0 {
			retry(i18n.Tf("settings.models.fetch_failed", "HTTP "+strconv.Itoa(f.noListStatus)))
		} else {
			retry(i18n.T("settings.models.no_list"))
		}
	case f.checked == f.conn() && f.fetched > 0:
		kit.Text(c, i18n.Tf("settings.models.fetched", f.fetched), t.Text.Sm).TextColor(t.Muted)
	default:
		kit.Text(c, i18n.T("settings.models.hint"), t.Text.Sm).TextColor(t.Muted)
	}
}

// keyField is the API key, the preset's key page, and the decision a stored
// key needs once the address or protocol changes. Leaving it lists the models.
func (m *modelsView) keyField(c *ui.Context, f *modelForm) {
	a := m.a
	t, th := theme.Of(c), c.Theme()
	label, ph := i18n.T("settings.models.api_key"), ""
	if f.keyOptional {
		label = i18n.T("settings.models.api_key.optional")
	}
	if f.hasKey {
		ph = i18n.T("settings.secret_kept")
	}
	formRow(c, label, func() ui.Element {
		var in ui.Element
		border := t.Input
		if f.keyErr != "" {
			border = t.Danger
		}
		ui.Row(c).AlignItems(ui.Center).Height(th.Space(8)).Radius(t.Radius.Md).Border(theme.Hairline, border).Background(t.Background).Padding(0, th.Space(0.5), 0, 0).Children(func() {
			in = kit.Input(c, &f.key, kit.InputOpts{Password: !f.showKey, Placeholder: ph, Bare: true}).Grow(1).Shrink(1).PaddingX(th.Space(2.5))
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
			kit.Text(c, f.keyErr, t.Text.Sm).TextColor(t.Danger)
		} else {
			kit.Text(c, i18n.T("settings.models.api_key.help"), t.Text.Sm).TextColor(t.Muted)
		}
		if u := f.helpURL; u != "" {
			kit.Button(c, i18n.T("settings.models.api_key.get"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(u) }, a.toastErr) })
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
