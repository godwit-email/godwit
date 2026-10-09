package app

import (
	"context"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// modelsView is the Model services page: the services, and a form for the
// chosen one or a new one. All model service writes chain on "models".
type modelsView struct {
	a        *App
	services []core.ModelService
	listGen  uint64     // bumped per list read; only the newest applies
	form     *modelForm // nil until a service is chosen or added
	defModel string     // the default model select's value; follows a.settings between frames
}

// modelForm edits one service (id 0: a new one). A result for a form that
// has been replaced since leaves the new one alone.
type modelForm struct {
	id                       int64
	title                    string
	kind, name, baseURL, key string
	hasKey                   bool
	models, offered          []string // checked; shown as choices (saved and fetched)
	busy, testing, fetching  bool
	testMsg                  string
	testOK                   bool
}

func (m *modelsView) load() {
	a := m.a
	m.listGen++
	m.defModel = a.settings.DefaultModel
	gen := m.listGen
	var list []core.ModelService
	a.query(func(ctx context.Context) (err error) {
		list, err = a.svc.ListModelServices(ctx)
		return err
	}, func(err error) {
		if gen != m.listGen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		m.services = list
		if m.form == nil && len(list) > 0 {
			m.edit(list[0])
		}
	})
}

func (m *modelsView) edit(s core.ModelService) {
	m.form = &modelForm{id: s.ID, title: s.Name, kind: s.Kind, name: s.Name, baseURL: s.BaseURL, hasKey: s.HasKey,
		models: slices.Clone(s.Models), offered: slices.Clone(s.Models)}
}

func (m *modelsView) add() { m.form = &modelForm{kind: "openai"} }

func (m *modelsView) save(f *modelForm) {
	a := m.a
	in := core.ModelServiceInput{Name: strings.TrimSpace(f.name), Kind: f.kind, BaseURL: strings.TrimSpace(f.baseURL),
		APIKey: f.key, Models: slices.Clone(f.models)}
	id := f.id
	f.busy = true
	var saved core.ModelService
	a.writes.Chain("models", func(ctx context.Context) (err error) {
		if id == 0 {
			saved, err = a.svc.AddModelService(ctx, in)
		} else {
			saved, err = a.svc.UpdateModelService(ctx, id, in)
		}
		return err
	}, func(err error) {
		f.busy = false
		if err != nil {
			a.toastErr(err)
		} else {
			if m.form == f {
				m.edit(saved)
			}
			a.toast(i18n.T("settings.saved"))
		}
		m.load()
	})
}

func (m *modelsView) remove(f *modelForm) {
	a, id := m.a, f.id
	f.busy = true
	a.writes.Chain("models", func(ctx context.Context) error { return a.svc.RemoveModelService(ctx, id) }, func(err error) {
		f.busy = false
		a.toastErr(err)
		if err == nil && m.form == f {
			m.form = nil
		}
		m.load()
	})
}

func (m *modelsView) fetch(f *modelForm) {
	a, id := m.a, f.id
	f.fetching = true
	var got []string
	a.query(func(ctx context.Context) (err error) {
		got, err = a.svc.FetchModels(ctx, id)
		return err
	}, func(err error) {
		f.fetching = false
		if err != nil {
			a.toastErr(err)
			return
		}
		for _, g := range got {
			if !slices.Contains(f.offered, g) {
				f.offered = append(f.offered, g)
			}
		}
	})
}

// test runs the saved service and shows how long it took.
func (m *modelsView) test(f *modelForm) {
	a, id := m.a, f.id
	f.testing, f.testMsg = true, ""
	start := a.clock.Now()
	a.query(func(ctx context.Context) error { return a.svc.TestModelService(ctx, id) }, func(err error) {
		f.testing, f.testOK = false, err == nil
		if err != nil {
			f.testMsg = humanize(err)
			return
		}
		f.testMsg = i18n.Tf("settings.models.test.ok", a.clock.Now().Sub(start).Milliseconds())
	})
}

// setDefault saves Settings.DefaultModel through the settings write queue.
func (m *modelsView) setDefault(v string) {
	st := m.a.settings
	st.NoTranslate = slices.Clone(st.NoTranslate)
	st.DefaultModel = v
	m.a.changeSettings(st)
}

func kindLabel(kind string) string { return cmpTr("settings.models.kind."+kind, kind) }

// badge is a service's two-letter mark.
func badge(name string) string {
	r := []rune(strings.ToUpper(strings.TrimSpace(name)))
	return string(r[:min(2, len(r))])
}

func (m *modelsView) view(c *ui.Context) {
	a := m.a
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Start).Gap(th.Space(4)).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() { pageHeading(c, i18n.T("settings.models"), i18n.T("settings.models.sub")) })
		kit.Button(c, i18n.T("settings.models.add"), kit.ButtonOpts{}).OnClick(m.add)
	})
	if len(m.services) > 0 {
		listCard(c, func() {
			for i, s := range m.services {
				on := m.form != nil && m.form.id == s.ID
				listRow(c.Key(s.ID), i, on, func() {
					ui.Box(c).Size(th.Space(9), th.Space(8)).Shrink(0).Radius(t.Radius.Md).Background(t.Hover).Center().Children(func() {
						ui.Text(c, badge(s.Name)).Font("Geist Mono").FontSize(t.Text.Sm)
					})
					ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
						ui.Text(c, s.Name+" · "+kindLabel(s.Kind)).FontWeight(500).SingleLine()
						ui.Text(c, s.BaseURL).Font("Geist Mono").FontSize(t.Text.Sm).TextColor(t.Muted).SingleLine()
					})
					ui.Text(c, i18n.Plural("settings.models.count", len(s.Models))).TextColor(t.Muted)
					if strings.HasPrefix(a.settings.DefaultModel, s.Name+"/") {
						kit.Badge(c, i18n.T("settings.models.default_badge"))
					}
				}).OnClick(func() { m.edit(s) })
			}
		})
	}
	if f := m.form; f != nil {
		m.formView(c, f)
	}
}

func (m *modelsView) formView(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	kit.Card(c, func() {
		title := i18n.T("settings.models.add")
		if f.id != 0 {
			title = i18n.Tf("settings.models.edit", f.title)
		}
		ui.Text(c, title).FontSize(t.Text.Lg).FontWeight(600)
		ui.Row(c).Gap(th.Space(4)).Children(func() {
			for _, k := range []string{"openai", "anthropic"} {
				kit.Radio(c.Key(k), &f.kind, k, kindLabel(k))
			}
		})
		ui.Field(c, i18n.T("settings.models.name"), func() { kit.Input(c, &f.name, kit.InputOpts{}) })
		ui.Field(c, i18n.T("settings.models.base_url"), func() { kit.Input(c, &f.baseURL, kit.InputOpts{Mono: true}) })
		ph := ""
		if f.hasKey {
			ph = i18n.T("settings.secret_kept")
		}
		ui.Field(c, i18n.T("settings.models.api_key"), func() { kit.Input(c, &f.key, kit.InputOpts{Password: true, Placeholder: ph}) }).
			Description(i18n.T("settings.models.api_key.help"))
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			ui.Text(c, i18n.T("settings.models.models")).FontWeight(500).Grow(1)
			kit.Button(c, i18n.T("settings.models.fetch"), kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: f.id == 0 || f.fetching}).
				OnClick(func() { m.fetch(f) })
		})
		if len(f.offered) == 0 {
			ui.Text(c, i18n.T("settings.models.none")).TextColor(t.Muted)
		}
		ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
			for _, name := range f.offered {
				on := slices.Contains(f.models, name)
				ui.Box(c.Key(name)).Padding(th.Space(2), th.Space(2.5)).Radius(t.Radius.Sm).Border(theme.Hairline, t.Border).Children(func() {
					if kit.Checkbox(c, &on, name).Changed() {
						if !slices.Contains(f.models, name) { // the box may have written an older frame's copy
							f.models = append(f.models, name)
						} else {
							f.models = slices.DeleteFunc(f.models, func(s string) bool { return s == name })
						}
					}
				})
			}
		})
		m.defaultSelect(c)
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			kit.Button(c, i18n.T("common.save"), kit.ButtonOpts{Disabled: f.busy}).OnClick(func() { m.save(f) })
			kit.Button(c, i18n.T("settings.models.test"), kit.ButtonOpts{Variant: kit.Outline, Disabled: f.id == 0 || f.testing}).
				OnClick(func() { m.test(f) })
			switch {
			case f.testing:
				kit.Spinner(c)
			case f.testMsg != "" && f.testOK:
				kit.StatusDot(c, kit.OK)
				ui.Text(c, f.testMsg).TextColor(t.Success)
			case f.testMsg != "":
				kit.StatusDot(c, kit.Error)
				ui.Text(c, f.testMsg).TextColor(t.Danger).Shrink(1)
			}
			ui.Spacer(c)
			if f.id != 0 {
				kit.Button(c, i18n.T("settings.models.remove"), kit.ButtonOpts{Variant: kit.Destructive, Disabled: f.busy}).
					OnClick(func() { m.remove(f) })
			}
		})
	})
}

// defaultSelect picks Settings.DefaultModel among the saved services' models.
func (m *modelsView) defaultSelect(c *ui.Context) {
	a := m.a
	cur := &m.defModel
	defer func() { *cur = a.settings.DefaultModel }() // what the next frame shows
	opts := []kit.Option[string]{{Value: "", Label: i18n.T("settings.models.default.none")}}
	for _, s := range m.services {
		for _, mod := range s.Models {
			opts = append(opts, kit.Option[string]{Value: s.Name + "/" + mod, Label: s.Name + " / " + mod})
		}
	}
	if !slices.ContainsFunc(opts, func(o kit.Option[string]) bool { return o.Value == *cur }) {
		opts = append(opts, kit.Option[string]{Value: *cur, Label: *cur})
	}
	label := i18n.T("settings.models.default")
	ui.Field(c, label, func() {
		if kit.Select(c, cur, opts).Label(label).Changed() {
			m.setDefault(*cur)
		}
	})
}

// listCard is a bordered list of rows.
func listCard(c *ui.Context, rows func()) {
	t := theme.Of(c)
	ui.Column(c).Radius(t.Radius.Panel).Border(theme.Hairline, t.Border).Clip().Children(rows)
}

// listRow is a clickable row of a listCard; i > 0 draws a divider above it.
func listRow(c *ui.Context, i int, on bool, children func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := ui.ButtonBase(c).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4)).Background(t.Background)
	if i > 0 {
		b.BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border)
	}
	switch {
	case on:
		b.Background(t.SidebarSelected)
	case b.Hovered():
		b.Background(t.Hover)
	}
	return b.Children(children)
}
