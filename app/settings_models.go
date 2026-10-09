package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// modelsView is the Model services page: the services, and a form for the
// chosen one or a new one. All model service writes chain on "models".
type modelsView struct {
	a                    *App
	services             []core.ModelService
	listGen              uint64     // bumped per list read; only the newest applies
	form                 *modelForm // nil until a service is chosen or added
	defModel, smallModel string     // the model selects' values; follow a.settings between frames

	checkService func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error) // tests swap these
	testService  func(ctx context.Context, id int64) error
}

// modelForm edits one service (id 0: a new one). A result for a form that
// has been replaced since leaves the new one alone.
type modelForm struct {
	id                       int64
	title, slug              string // as saved
	savedKind, savedURL      string // as saved: where a stored key may go
	picking                  bool   // a new service choosing its preset
	helpURL                  string // the preset's API key page
	keyOptional              bool   // the preset needs no key
	kind, name, baseURL, key string
	hasKey, noKey            bool   // noKey: the user chose no key for a changed address
	manual                   string // a model ID being typed
	models, offered          []string
	busy, testing, probing   bool
	checked                  string // conn() of the last probe taken
	noList                   bool   // that probe found no model list
	keyErr                   string // that service refused the key
	urlFocus                 bool
	urlErr, urlBad           string // why the address urlBad was refused when it lost focus
	testMsg                  string
	testOK                   bool
}

// url is the address as it will be saved, or as typed when it is not valid.
func (f *modelForm) url() string {
	raw := strings.TrimSpace(f.baseURL)
	if u, err := core.NormalizeModelBaseURL(f.kind, raw); err == nil {
		return u
	}
	return raw
}

// moved reports whether a saved service now points at another address or protocol.
func (f *modelForm) moved() bool {
	return f.id != 0 && (f.kind != f.savedKind || f.url() != f.savedURL)
}

// needsKeyDecision: the stored key may not follow a move, and the user has
// neither typed a key nor chosen none.
func (f *modelForm) needsKeyDecision() bool { return f.hasKey && f.key == "" && !f.noKey && f.moved() }

func (f *modelForm) keyAction() string {
	switch {
	case f.key != "":
		return core.KeyReplace
	case f.id == 0 || f.noKey && f.moved():
		return core.KeyClear
	}
	return core.KeyKeep
}

// conn is what a probe depends on: its result is taken only while it is the same.
func (f *modelForm) conn() string {
	return strings.Join([]string{f.kind, f.url(), f.keyAction(), f.key}, "\n")
}

// mustProbe reports whether saving probes first: a new service, or a new
// address, protocol or key.
func (f *modelForm) mustProbe() bool {
	return (f.id == 0 || f.moved() || f.keyAction() != core.KeyKeep) && f.checked != f.conn()
}

// input is the form as a service input, with an explicit key action.
func (f *modelForm) input() core.ModelServiceInput {
	return core.ModelServiceInput{Name: strings.TrimSpace(f.name), Kind: f.kind, BaseURL: strings.TrimSpace(f.baseURL),
		APIKey: f.key, KeyAction: f.keyAction(), Models: slices.Clone(f.models)}
}

// normalize rewrites the address as it will be saved, or says why it can't be.
func (f *modelForm) normalize() {
	raw := strings.TrimSpace(f.baseURL)
	if raw == "" {
		f.urlErr = ""
		return
	}
	u, err := core.NormalizeModelBaseURL(f.kind, raw)
	if err != nil {
		f.urlErr, f.urlBad = humanize(err), f.baseURL
		return
	}
	f.baseURL, f.urlErr = u, ""
}

// addManual offers and ticks the model ID typed.
func (f *modelForm) addManual() {
	id := strings.TrimSpace(f.manual)
	if id == "" {
		return
	}
	if !slices.Contains(f.offered, id) {
		f.offered = append(f.offered, id)
	}
	if !slices.Contains(f.models, id) {
		f.models = append(f.models, id)
	}
	f.manual = ""
}

func (m *modelsView) load() {
	a := m.a
	m.listGen++
	m.defModel, m.smallModel = a.settings.DefaultModel, a.settings.SmallModel
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
	m.form = &modelForm{id: s.ID, title: s.Name, slug: s.Slug, savedKind: s.Kind, savedURL: s.BaseURL, kind: s.Kind, name: s.Name,
		baseURL: s.BaseURL, hasKey: s.HasKey, models: slices.Clone(s.Models), offered: slices.Clone(s.Models)}
}

func (m *modelsView) add() { m.form = &modelForm{picking: true, kind: ai.KindOpenAI} }

// pick fills the new service's form from a preset.
func (f *modelForm) pick(p ai.Preset) {
	f.picking, f.kind, f.name, f.baseURL, f.keyOptional = false, p.Kind, p.Name, p.BaseURL, !p.KeyRequired
	if p.KeyRequired {
		f.helpURL = p.HelpURL
	}
}

// save probes the service first when its address, protocol or key is new,
// and submits once the probe is taken and a model is chosen.
func (m *modelsView) save(f *modelForm) {
	switch {
	case f.busy || f.probing || f.needsKeyDecision(): // the form says what is missing
	case f.mustProbe():
		m.probe(f, func() {
			if len(f.models) == 0 {
				m.a.toast(i18n.T("settings.models.pick"))
				return
			}
			m.submit(f)
		})
	default:
		m.submit(f)
	}
}

// submit writes the form as it is now. On success an unedited form shows the
// saved service; one edited meanwhile keeps the edits and takes only the
// service's ID, name, address and key state. References to models it no
// longer has are cleared.
func (m *modelsView) submit(f *modelForm) {
	a := m.a
	snap := *f
	snap.models = slices.Clone(f.models)
	in, id := f.input(), f.id
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
			m.load()
			return
		}
		switch {
		case m.form != f:
		case f.unchanged(snap):
			m.edit(saved)
			m.form.offered = append(m.form.offered, slices.DeleteFunc(slices.Clone(f.offered), func(s string) bool { return slices.Contains(saved.Models, s) })...)
		default:
			f.id, f.title, f.slug, f.hasKey, f.savedKind, f.savedURL = saved.ID, saved.Name, saved.Slug, saved.HasKey, saved.Kind, saved.BaseURL
			if f.key == snap.key {
				f.key = ""
			}
		}
		if id != 0 {
			m.dropRefs(saved.Slug, saved.Models)
		}
		a.toast(i18n.T("settings.saved"))
		m.load()
	})
}

func (f *modelForm) unchanged(snap modelForm) bool {
	return f.kind == snap.kind && f.name == snap.name && f.baseURL == snap.baseURL && f.key == snap.key && f.noKey == snap.noKey &&
		slices.Equal(f.models, snap.models)
}

// dropRefs clears the model settings that point at service slug's models
// other than keep (all of them after a removal, keep nil). A rename keeps the
// slug, so references need no rewrite.
func (m *modelsView) dropRefs(slug string, keep []string) {
	st := m.a.settings
	changed := false
	for _, ref := range []*string{&st.DefaultModel, &st.SmallModel} {
		if model, ok := strings.CutPrefix(*ref, slug+"/"); ok && !slices.Contains(keep, model) {
			*ref, changed = "", true
		}
	}
	if changed {
		st.NoTranslate = slices.Clone(st.NoTranslate)
		m.a.changeSettings(st)
	}
}

func (m *modelsView) remove(f *modelForm) {
	a, id, slug := m.a, f.id, f.slug
	f.busy = true
	a.writes.Chain("models", func(ctx context.Context) error { return a.svc.RemoveModelService(ctx, id) }, func(err error) {
		f.busy = false
		a.toastErr(err)
		if err == nil {
			m.dropRefs(slug, nil)
			if m.form == f {
				m.form = nil
			}
		}
		m.load()
	})
}

// probe checks the service as the form has it, saved or not, and offers its
// models; then runs after a probe that is taken. The result is taken only on
// the form and page visit that asked, while what it probed is unchanged; a
// refused key shows on the key field.
func (m *modelsView) probe(f *modelForm, then func()) {
	if f.probing || f.needsKeyDecision() {
		return
	}
	a, id, in, check, gen, conn := m.a, f.id, f.input(), m.checkService, m.a.gen, f.conn()
	f.probing, f.keyErr = true, ""
	var res core.ServiceCheck
	a.do(func(ctx context.Context) (err error) {
		res, err = check(ctx, id, in)
		return err
	}, func(err error) {
		f.probing = false
		if m.form != f || gen != a.gen || f.conn() != conn {
			return
		}
		switch {
		case errors.Is(err, core.ErrModelAuth):
			f.keyErr = humanize(err)
			return
		case err != nil:
			a.toastErr(err)
			return
		}
		f.checked, f.noList = conn, !res.ListSupported || len(res.Models) == 0
		if res.BaseURL != "" {
			f.baseURL = res.BaseURL
		}
		for _, g := range res.Models {
			if !slices.Contains(f.offered, g) {
				f.offered = append(f.offered, g)
			}
		}
		if then != nil {
			then()
		}
	})
}

// test runs the saved service and shows how long it took, as probe does.
func (m *modelsView) test(f *modelForm) {
	a, id, test, gen := m.a, f.id, m.testService, m.a.gen
	f.testing, f.testMsg = true, ""
	start := a.clock.Now()
	a.do(func(ctx context.Context) error { return test(ctx, id) }, func(err error) {
		f.testing = false
		if m.form != f || gen != a.gen {
			return
		}
		f.testOK = err == nil
		if err != nil {
			f.testMsg = humanize(err)
			return
		}
		f.testMsg = i18n.Tf("settings.models.test.ok", a.clock.Now().Sub(start).Milliseconds())
	})
}

// setModel saves a model setting through the settings write queue.
func (m *modelsView) setModel(set func(*core.Settings)) {
	st := m.a.settings
	st.NoTranslate = slices.Clone(st.NoTranslate)
	set(&st)
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
					if strings.HasPrefix(a.settings.DefaultModel, s.Slug+"/") {
						kit.Badge(c, i18n.T("settings.models.default_badge"))
					}
				}).OnClick(func() { m.edit(s) })
			}
		})
	}
	m.modelSelects(c)
	switch f := m.form; {
	case f == nil:
	case f.picking:
		m.presetView(c, f)
	default:
		m.formView(c, f)
	}
}

// presetView is the first step of adding a service: a provider, or a custom address.
func (m *modelsView) presetView(c *ui.Context, f *modelForm) {
	t, th := theme.Of(c), c.Theme()
	kit.Card(c, func() {
		ui.Text(c, i18n.T("settings.models.add")).FontSize(t.Text.Lg).FontWeight(600)
		ui.Text(c, i18n.T("settings.models.preset")).TextColor(t.Muted)
		ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
			for _, p := range m.a.svc.ModelPresets() {
				kit.Button(c.Key(p.ID), p.Name, kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { f.pick(p) })
			}
			kit.Button(c, i18n.T("settings.models.preset.custom"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { f.picking = false })
		})
	})
}

func (m *modelsView) formView(c *ui.Context, f *modelForm) {
	a := m.a
	t, th := theme.Of(c), c.Theme()
	kit.Card(c, func() {
		title := i18n.T("settings.models.add")
		if f.id != 0 {
			title = i18n.Tf("settings.models.edit", f.title)
		}
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(t.Text.Lg).FontWeight(600).Grow(1).Shrink(1)
			if f.id != 0 { // up here, clear of the toasts
				kit.Button(c, i18n.T("settings.models.remove"), kit.ButtonOpts{Variant: kit.Destructive, Small: true, Disabled: f.busy}).
					OnClick(func() { m.remove(f) })
			}
		})
		ui.Row(c).Wrap().Gap(th.Space(4)).Children(func() {
			for _, k := range []string{ai.KindOpenAI, ai.KindResponses, ai.KindAnthropic} {
				kit.Radio(c.Key(k), &f.kind, k, kindLabel(k))
			}
		})
		ui.Field(c, i18n.T("settings.models.name"), func() { kit.Input(c, &f.name, kit.InputOpts{}) })
		if f.slug != "" {
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.Tf("settings.models.slug", f.slug)).FontSize(t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
				if kit.Button(c, i18n.T("settings.models.slug.copy"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).Clicked() {
					c.WriteClipboard(f.slug)
					a.toast(i18n.T("link.copied"))
				}
			})
		}
		url := ui.Field(c, i18n.T("settings.models.base_url"), func() {
			focused := kit.Input(c, &f.baseURL, kit.InputOpts{Mono: true}).Focused()
			if f.urlFocus && !focused {
				f.normalize()
			}
			f.urlFocus = focused
		})
		if f.urlErr != "" && f.baseURL == f.urlBad {
			url.Error(f.urlErr)
		} else if u, err := core.NormalizeModelBaseURL(f.kind, strings.TrimSpace(f.baseURL)); err == nil {
			url.Description(i18n.Tf("settings.models.endpoint", core.ModelEndpoint(f.kind, u)))
		}
		m.keyField(c, f)
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, i18n.T("settings.models.models")).FontWeight(500).Grow(1)
			if f.probing {
				kit.Spinner(c)
			}
			kit.Button(c, i18n.T("settings.models.probe"), kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: f.probing}).
				OnClick(func() { m.probe(f, nil) })
		})
		switch {
		case f.noList && f.checked == f.conn():
			ui.Text(c, i18n.T("settings.models.no_list")).TextColor(t.Muted)
		case len(f.offered) == 0:
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
		ui.Field(c, i18n.T("settings.models.manual"), func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				kit.Input(c, &f.manual, kit.InputOpts{Mono: true}).Grow(1).OnSubmit(f.addManual)
				kit.Button(c, i18n.T("settings.models.manual.add"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(f.addManual)
			})
		})
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
		})
	})
}

// keyField is the API key, the preset's key page, and the decision a stored
// key needs once the address or protocol changes.
func (m *modelsView) keyField(c *ui.Context, f *modelForm) {
	a := m.a
	label, ph := i18n.T("settings.models.api_key"), ""
	if f.keyOptional {
		label = i18n.T("settings.models.api_key.optional")
	}
	if f.hasKey {
		ph = i18n.T("settings.secret_kept")
	}
	ui.Field(c, label, func() { kit.Input(c, &f.key, kit.InputOpts{Password: true, Placeholder: ph}) }).
		Description(i18n.T("settings.models.api_key.help")).Error(f.keyErr)
	if u := f.helpURL; u != "" {
		kit.Button(c, i18n.T("settings.models.api_key.get"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).
			OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(u) }, a.toastErr) })
	}
	if f.hasKey && f.key == "" && f.moved() {
		if !f.noKey {
			kit.Alert(c, kit.Warning, i18n.T("settings.models.key_moved"), nil)
		}
		kit.Checkbox(c, &f.noKey, i18n.T("settings.models.no_key"))
	}
}

// modelSelects pick Settings.DefaultModel and SmallModel among the saved
// services' models.
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
	kit.Card(c, func() {
		def := opts(i18n.T("settings.models.default.none"))
		label := i18n.T("settings.models.default")
		ui.Field(c, label, func() {
			if kit.Select(c, &m.defModel, def).Label(label).Changed() {
				v := m.defModel
				m.setModel(func(st *core.Settings) { st.DefaultModel = v })
			}
		})
		if dm := a.settings.DefaultModel; !slices.ContainsFunc(def, func(o kit.Option[string]) bool { return o.Value == dm }) {
			kit.Alert(c, kit.Warning, i18n.Tf("settings.models.default.missing", dm), nil)
		}
		small := i18n.T("settings.models.small")
		ui.Field(c, small, func() {
			if kit.Select(c, &m.smallModel, opts(i18n.T("settings.models.small.same"))).Label(small).Changed() {
				v := m.smallModel
				m.setModel(func(st *core.Settings) { st.SmallModel = v })
			}
		})
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
