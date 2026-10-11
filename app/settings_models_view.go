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
	th := c.Theme()
	empty := m.loaded && len(m.services) == 0
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() { pageHeading(c, i18n.T("settings.models"), i18n.T("settings.models.sub")) })
		if !empty {
			kit.Button(c, i18n.T("settings.models.add"), kit.ButtonOpts{}).OnClick(m.add)
		}
	})
	if empty {
		kit.Empty(c, kit.EmptyOpts{Icon: "cpu", Title: i18n.T("settings.models.empty.title"), Desc: i18n.T("settings.models.empty.desc"), Dashed: true, Actions: func() {
			kit.Button(c, i18n.T("settings.models.add"), kit.ButtonOpts{}).OnClick(m.add)
			kit.Button(c, i18n.T("settings.models.local"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(m.addLocal)
		}})
	}
	if len(m.services) > 0 {
		m.list(c)
		m.modelSelects(c)
	}
	if m.open {
		m.dialog(c)
	}
}

// close drops the form with its dialog: its pending results are ignored.
func (m *modelsView) close() { m.open, m.form = false, nil }

// list is the services, one card row each.
func (m *modelsView) list(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	var rows []func()
	for _, s := range m.services {
		rows = append(rows, func() {
			listRow(c.Key(s.ID), 0, false, func() { m.openEdit(s) }, func() {
				ui.Box(c).Size(th.Space(9), th.Space(8)).Shrink(0).Radius(t.Radius.Lg).Background(t.Hover).Center().Children(func() {
					ui.Text(c, badge(s.Name)).Font(theme.Mono).FontSize(t.Text.Sm)
				})
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
						ui.Text(c, s.Name+" · "+kindLabel(s.Kind)).FontWeight(500).SingleLine().Shrink(1)
						statusChip(c, m.status[s.ID])
					})
					ui.Text(c, s.BaseURL).Font(theme.Mono).FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine()
				})
				capChip(c, i18n.Plural("settings.models.gen", len(s.Models)), false)
				if strings.HasPrefix(m.a.settings.DefaultModel, s.Slug+"/") {
					capChip(c, i18n.T("settings.models.default_badge"), true)
				}
				ui.Icon(c, icons.Get("chevron-right")).Size(theme.Icon, theme.Icon).TextColor(t.Muted).Shrink(0)
			}).Padding(th.Space(2), th.Space(4))
		})
	}
	settingsSection(c, i18n.T("settings.models.services"), i18n.T("settings.models.foot"), rows...)
}

func statusChip(c *ui.Context, s svcStatus) {
	t, th := theme.Of(c), c.Theme()
	bg, fg, dot, label := statusLook(t, s)
	ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Padding(th.Space(0.5), th.Space(2)).Radius(t.Radius.Full).Background(bg).Shrink(0).Children(func() {
		ui.Box(c).Size(theme.StatusDot, theme.StatusDot).Radius(t.Radius.Full).Background(dot)
		ui.Text(c, label).FontSize(t.Text.Sm).TextColor(fg)
	})
}

// capChip is a capability chip; the default one carries a check.
func capChip(c *ui.Context, text string, check bool) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).Height(th.Space(5)).Padding(0, th.Space(2)).Radius(t.Radius.Sm).Background(t.Hover).Shrink(0).Children(func() {
		if check {
			ui.Icon(c, icons.Get("check")).Size(theme.IconXs, theme.IconXs)
		}
		ui.Text(c, text).FontSize(t.Text.Sm)
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
			ui.Text(c, f.testMsg).FontSize(t.Text.Sm).TextColor(t.Success)
		case f.testMsg != "":
			kit.StatusDot(c, kit.Error)
			ui.Text(c, f.testMsg).FontSize(t.Text.Sm).TextColor(t.Danger).Shrink(1)
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
				ui.Text(c, i18n.Tf("settings.models.slug", f.slug)).FontSize(t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
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
				ui.Text(c, urlErr).FontSize(t.Text.Sm).TextColor(t.Danger)
			} else if u, err := core.NormalizeModelBaseURL(f.kind, strings.TrimSpace(f.baseURL)); err == nil {
				ui.Text(c, i18n.Tf("settings.models.endpoint", core.ModelEndpoint(f.kind, u))).FontSize(t.Text.Sm).TextColor(t.Muted)
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
							ui.Text(c, tl.cap).FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine()
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
		ui.Text(c, i18n.T("settings.models.common")).FontSize(t.Text.Sm).TextColor(t.Muted)
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
			b.Children(func() { ui.Text(c, p.Name).FontSize(t.Text.Sm).TextColor(fg) })
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
			b.Children(func() { ui.Text(c, name).Font(theme.Mono).FontSize(t.Text.Sm) })
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
			ui.Text(c, msg).FontSize(t.Text.Sm).TextColor(t.WarningText).Shrink(1)
			kit.Button(c, i18n.T("settings.models.retry"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Disabled: f.probing}).OnClick(func() { m.fetch(f) })
		})
	}
	switch {
	case f.probing:
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			kit.Spinner(c)
			ui.Text(c, i18n.T("settings.models.fetching")).FontSize(t.Text.Sm).TextColor(t.Muted)
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
		ui.Text(c, i18n.Tf("settings.models.fetched", f.fetched)).FontSize(t.Text.Sm).TextColor(t.Muted)
	default:
		ui.Text(c, i18n.T("settings.models.hint")).FontSize(t.Text.Sm).TextColor(t.Muted)
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
			ui.Text(c, f.keyErr).FontSize(t.Text.Sm).TextColor(t.Danger)
		} else {
			ui.Text(c, i18n.T("settings.models.api_key.help")).FontSize(t.Text.Sm).TextColor(t.Muted)
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

// modelSelects pick Settings.DefaultModel and SmallModel among the saved
// services' models, as rows of a 用途 section.
func (m *modelsView) modelSelects(c *ui.Context) {
	a := m.a
	defer func() { m.defModel, m.smallModel = a.settings.DefaultModel, a.settings.SmallModel }() // what the next frame shows
	opts := func(none string) []kit.Option[string] {
		o := []kit.Option[string]{{Value: "", Label: none}}
		for _, s := range m.services {
			for _, mod := range s.Models {
				o = append(o, kit.Option[string]{Value: s.Slug + "/" + mod, Label: s.Name + " / " + mod})
			}
		}
		return o
	}
	def := opts(i18n.T("settings.models.default.none"))
	label, small := i18n.T("settings.models.default"), i18n.T("settings.models.small")
	rows := []func(){func() {
		settingRow(c, label, "", func() {
			if settingSelect(c, label, &m.defModel, def).Changed() {
				v := m.defModel
				m.setModel(func(st *core.Settings) { st.DefaultModel = v })
			}
		})
	}}
	if dm := a.settings.DefaultModel; !slices.ContainsFunc(def, func(o kit.Option[string]) bool { return o.Value == dm }) {
		rows = append(rows, func() {
			settingBlock(c, func() { kit.Alert(c, kit.Warning, i18n.Tf("settings.models.default.missing", dm), nil) })
		})
	}
	rows = append(rows, func() {
		settingRow(c, small, "", func() {
			if settingSelect(c, small, &m.smallModel, opts(i18n.T("settings.models.small.same"))).Changed() {
				v := m.smallModel
				m.setModel(func(st *core.Settings) { st.SmallModel = v })
			}
		})
	})
	settingsSection(c, i18n.T("settings.models.usage"), "", rows...)
}
