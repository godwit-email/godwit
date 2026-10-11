package app

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strconv"
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
	defModel, smallModel string     // the 用途 selects' values; follow a.settings between frames
	judge                string
	open                 bool // the add/edit dialog shows form
	loaded               bool // the list has been read once

	checkService func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error) // tests swap these
	testService  func(ctx context.Context, id int64, in core.ModelServiceInput, c string) (core.CapStatus, error)
}

// modelForm edits one service (id 0: a new one). A result for a form that
// has been replaced since leaves the new one alone.
type modelForm struct {
	id                       int64
	title, slug              string // as saved
	savedKind, savedURL      string // as saved: where a stored key may go
	preset                   string // the provider tile chosen ("custom": 其他服务)
	defURL                   string // the preset's address, for 恢复默认
	confirmDel               bool   // the footer asks to confirm the removal
	keyFocus                 bool
	fetchErr                 string // why the last automatic /models fetch failed
	fetched, noListStatus    int    // how many models that fetch listed; the status that said it lists none
	probeConn                string // conn() of the probe under way
	showKey, refetch         bool   // the typed key is shown; the key or address changed during a fetch, which asks for another
	helpURL                  string // the preset's API key page
	keyOptional              bool   // the preset needs no key
	kind, name, baseURL, key string
	provider                 string // as saved; "" for a new service: core infers it
	judgeFormat, judgeModel  string // as saved: kept until the dialog edits judge
	hasKey, noKey            bool   // noKey: the user chose no key for a changed address
	manual                   string // a model ID being typed
	models, offered          []string
	busy, testing, probing   bool
	checked                  string // conn() of the last probe taken; "" from a probe's start until it is taken
	reprobe                  bool   // a probe was not taken: saving probes again
	edits, connEdits         uint64 // bumped by every edit edited sees; by every change of conn() among them
	seen, connSeen           string // the form, and conn(), as edited last saw them
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
	if u, err := core.NormalizeModelBaseURL(raw); err == nil {
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
	return (f.reprobe || f.id == 0 || f.moved() || f.keyAction() != core.KeyKeep) && f.checked != f.conn()
}

// edited counts the form's edits: every change to what it would save, or
// to the model ID being typed, since the last call is one.
func (f *modelForm) edited() uint64 {
	if c := f.conn(); c != f.connSeen {
		f.connSeen, f.connEdits = c, f.connEdits+1
	}
	if p := strings.Join(append([]string{f.kind, f.name, f.baseURL, f.key, strconv.FormatBool(f.noKey), f.manual}, f.models...), "\n"); p != f.seen {
		f.seen, f.edits = p, f.edits+1
	}
	return f.edits
}

// input is the form as a service input, with an explicit key action.
func (f *modelForm) input() core.ModelServiceInput {
	return core.ModelServiceInput{Name: strings.TrimSpace(f.name), Kind: f.kind, BaseURL: strings.TrimSpace(f.baseURL),
		APIKey: f.key, KeyAction: f.keyAction(), Models: slices.Clone(f.models), Provider: f.provider, JudgeFormat: f.judgeFormat, JudgeModel: f.judgeModel}
}

// normalize rewrites the address as it will be saved, or says why it can't be.
func (f *modelForm) normalize() {
	raw := strings.TrimSpace(f.baseURL)
	if raw == "" {
		f.urlErr = ""
		return
	}
	u, err := core.NormalizeModelBaseURL(raw)
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
	m.defModel, m.smallModel, m.judge = a.settings.DefaultModel, a.settings.SmallModel, a.settings.JudgeService
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
		m.services, m.loaded = list, true
	})
}

func (m *modelsView) edit(s core.ModelService) {
	m.form = &modelForm{id: s.ID, title: s.Name, slug: s.Slug, savedKind: s.Kind, savedURL: s.BaseURL, kind: s.Kind, name: s.Name,
		baseURL: s.BaseURL, hasKey: s.HasKey, models: slices.Clone(s.Models), offered: slices.Clone(s.Models),
		provider: s.Provider, judgeFormat: s.JudgeFormat, judgeModel: s.JudgeModel}
}

// openEdit shows the service in the dialog and lists its models.
func (m *modelsView) openEdit(s core.ModelService) {
	m.edit(s)
	m.open = true
	m.fetch(m.form)
}

func (m *modelsView) add() {
	m.form, m.open = &modelForm{kind: ai.KindOpenAI, preset: presetCustom}, true
}

// addLocal opens the dialog for a local gateway: 其他服务 with a loopback address to complete.
func (m *modelsView) addLocal() {
	m.add()
	m.form.baseURL = "http://localhost:"
}

const presetCustom = "custom"

// pick fills the new service's form from a preset.
func (f *modelForm) pick(p ai.Preset) {
	f.preset, f.kind, f.name, f.baseURL, f.defURL, f.keyOptional, f.helpURL = p.ID, p.Kind, p.Name, p.BaseURL, p.BaseURL, !p.KeyRequired, ""
	if p.KeyRequired {
		f.helpURL = p.HelpURL
	}
}

// pickCustom clears the form's provider for an address of the user's own.
func (f *modelForm) pickCustom() {
	f.preset, f.name, f.baseURL, f.defURL, f.keyOptional, f.helpURL = presetCustom, "", "", "", false, ""
}

// save probes the service first when its address, protocol or key is new,
// and submits once the probe is taken and a model is chosen.
func (m *modelsView) save(f *modelForm) {
	switch {
	case f.busy || f.probing || f.needsKeyDecision(): // the form says what is missing
	case f.mustProbe():
		m.probe(f, func() {
			if len(f.models) == 0 {
				m.a.toastAs(kit.ToastWarning, i18n.T("settings.models.pick"))
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
		a.refreshTasks()
		if err != nil {
			a.toastErr(err)
			m.load()
			return
		}
		switch {
		case m.form != f:
		case f.unchanged(snap):
			m.close()
		default:
			f.id, f.title, f.slug, f.hasKey, f.savedKind, f.savedURL = saved.ID, saved.Name, saved.Slug, saved.HasKey, saved.Kind, saved.BaseURL
			if f.key == snap.key {
				f.key = ""
			}
		}
		noDefault := a.settings.DefaultModel == ""
		if id != 0 {
			m.dropRefs(saved)
		}
		if noDefault && saved.Kind != "" && len(saved.Models) > 0 { // this service's first model becomes it: Wit needs one
			m.setModel(func(st *core.Settings) { st.DefaultModel = saved.Slug + "/" + saved.Models[0] })
		}
		a.toastAs(kit.ToastSuccess, i18n.T("settings.saved"))
		m.load()
	})
}

func (f *modelForm) unchanged(snap modelForm) bool {
	return f.kind == snap.kind && f.name == snap.name && f.baseURL == snap.baseURL && f.key == snap.key && f.noKey == snap.noKey &&
		f.manual == snap.manual && slices.Equal(f.models, snap.models)
}

// dropRefs clears the model settings that point at what service svc no
// longer offers (a removed service: its ID and slug only). A rename keeps
// the slug, so references need no rewrite.
func (m *modelsView) dropRefs(svc core.ModelService) {
	if st, changed := dropRefs(m.a.settings, svc, m.services); changed {
		st.NoTranslate = slices.Clone(st.NoTranslate)
		m.a.changeSettings(st)
	}
}

// dropRefs clears st's references to models svc doesn't generate with (all
// of them with generate off) and to its judge when that is off. A
// DefaultModel cleared moves to alt: the first other service that
// generates, its first model, as the dialog's warning promises.
func dropRefs(st core.Settings, svc core.ModelService, services []core.ModelService) (core.Settings, bool) {
	keep, changed := svc.Models, false
	if svc.Kind == "" {
		keep = nil
	}
	for _, ref := range []*string{&st.DefaultModel, &st.SmallModel} {
		if model, ok := strings.CutPrefix(*ref, svc.Slug+"/"); ok && !slices.Contains(keep, model) {
			*ref, changed = "", true
		}
	}
	if st.JudgeService == svc.Slug && svc.JudgeFormat == "" {
		st.JudgeService, changed = "", true
	}
	if changed && st.DefaultModel == "" {
		for _, o := range services {
			if o.ID != svc.ID && o.Kind != "" && len(o.Models) > 0 {
				st.DefaultModel = o.Slug + "/" + o.Models[0]
				break
			}
		}
	}
	return st, changed
}

func (m *modelsView) remove(f *modelForm) {
	a, id, slug := m.a, f.id, f.slug
	f.busy = true
	a.writes.Chain("models", func(ctx context.Context) error { return a.svc.RemoveModelService(ctx, id) }, func(err error) {
		f.busy = false
		a.refreshTasks()
		a.toastErr(err)
		if err == nil {
			m.dropRefs(core.ModelService{ID: id, Slug: slug})
			if m.form == f {
				m.form, m.open = nil, false
			}
		}
		m.load()
	})
}

// probe checks the service as the form has it, saved or not, and offers its
// models; then runs after a probe that is taken. Until one is taken, saving
// probes again. The result is taken only on the form and page visit that
// asked, with no edit since it started; a refused key shows on the key field.
func (m *modelsView) probe(f *modelForm, then func()) { m.probeWith(f, then, false) }

// fetch lists the models when the key field loses focus and the edit dialog
// opens. Unlike a save's probe it survives edits to the name and models; it
// is dropped once the dialog has closed or the address or key changed.
func (m *modelsView) fetch(f *modelForm) {
	if f.probing { // that result is dropped if the key or address changed since it started: ask again then
		f.refetch = f.conn() != f.probeConn
		return
	}
	if _, err := core.NormalizeModelBaseURL(strings.TrimSpace(f.baseURL)); err == nil {
		m.probeWith(f, nil, true)
	}
}

func (m *modelsView) probeWith(f *modelForm, then func(), auto bool) {
	if f.probing || f.needsKeyDecision() {
		return
	}
	a, id, in, check, gen, conn, edits := m.a, f.id, f.input(), m.checkService, m.a.gen, f.conn(), f.edited()
	connEdits := f.connEdits
	f.probing, f.keyErr, f.fetchErr, f.checked, f.reprobe, f.refetch, f.probeConn = true, "", "", "", true, false, conn
	var res core.ServiceCheck
	a.do(func(ctx context.Context) (err error) {
		res, err = check(ctx, id, in)
		return err
	}, func(err error) {
		f.probing = false
		defer func() {
			if auto && f.refetch && m.form == f && gen == a.gen {
				m.fetch(f)
			}
		}()
		edited := f.edited() != edits // edited since: the user saves again
		if auto {
			edited = f.connEdits != connEdits // the address, protocol or key changed since, even back again
		}
		if m.form != f || gen != a.gen || edited {
			return
		}
		switch {
		case errors.Is(err, core.ErrModelAuth):
			f.keyErr = humanize(err)
			return
		case err != nil && then == nil:
			f.fetchErr = fetchFailure(err)
			return
		case err != nil:
			a.toastErr(err)
			return
		}
		f.checked, f.reprobe, f.noList, f.fetched = conn, false, !res.ListSupported || len(res.Models) == 0, len(res.Models)
		f.noListStatus = res.NoListStatus
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

// test runs the saved service and shows how long it took, as probe does;
// core stores the status, which the list reads again.
func (m *modelsView) test(f *modelForm) {
	a, id, in, test, gen := m.a, f.id, f.input(), m.testService, m.a.gen
	f.testing, f.testMsg = true, ""
	start := a.clock.Now()
	a.do(func(ctx context.Context) error { _, err := test(ctx, id, in, core.CapGen); return err }, func(err error) {
		f.testing = false
		if gen != a.gen {
			return
		}
		m.load()
		if m.form != f {
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

// look is a status line's dot and text color: what a test found.
type look int

const (
	lookNone     look = iota // nothing to test: grey
	lookOK                   // passed
	lookUntested             // not tested since it changed: amber
	lookFail                 // failed: red, red text
	lookSlow                 // the judge falls back to generation: amber
)

// colors are the look's dot and text colors.
func (l look) colors(t theme.Tokens) (dot, fg ui.Color) {
	switch l {
	case lookOK:
		return t.Success, t.Muted
	case lookUntested, lookSlow:
		return t.Attention, t.Muted
	case lookFail:
		return t.Danger, t.DangerText
	}
	return t.Idle, t.Muted
}

// line is a status line: a dot and its text.
type line struct {
	text string
	look look
}

// usage is the 用途 card as the settings and the services' tests make it.
type usage struct {
	genOpts, smallOpts, judgeOpts []kit.Option[string]
	disabled                      bool // no generate model: the selects say 先添加一个服务
	gen, judge                    line
	genSvc, judgeSvc              *core.ModelService // the defaults that resolve; judgeSvc nil: generation answers
	alert                         string             // the default generator failed its test
	suggest                       bool               // a judge service would be faster
}

// deriveUsage is the 用途 card for st and the services.
func deriveUsage(st core.Settings, services []core.ModelService) usage {
	var u usage
	var genModel string
	for i, s := range services {
		if s.Kind != "" {
			for _, mod := range s.Models {
				ref := s.Slug + "/" + mod
				u.genOpts = append(u.genOpts, kit.Option[string]{Value: ref, Label: s.Name + " / " + mod})
				if ref == st.DefaultModel {
					u.genSvc, genModel = &services[i], u.genOpts[len(u.genOpts)-1].Label
				}
			}
		}
		if s.JudgeFormat != "" && s.Slug == st.JudgeService {
			u.judgeSvc = &services[i]
		}
	}
	if len(u.genOpts) == 0 {
		none := []kit.Option[string]{{Value: "", Label: i18n.T("settings.models.use.add_first")}}
		u.genOpts, u.smallOpts, u.judgeOpts, u.disabled = none, none, none, true
		u.gen = line{i18n.T("settings.models.use.gen.none"), lookNone}
		u.judge = line{i18n.T("settings.models.use.judge.none"), lookNone}
		return u
	}
	u.smallOpts = append([]kit.Option[string]{{Value: "", Label: i18n.T("settings.models.small.same")}}, u.genOpts...)
	u.judgeOpts = []kit.Option[string]{{Value: "", Label: i18n.T("settings.models.use.judge.by_gen")}}
	for _, s := range services {
		if s.JudgeFormat != "" {
			u.judgeOpts = append(u.judgeOpts, kit.Option[string]{Value: s.Slug, Label: s.Name + " / " + s.JudgeModel})
		}
	}
	if u.genSvc == nil {
		u.genOpts = append([]kit.Option[string]{{Value: st.DefaultModel, Label: i18n.T("settings.models.default.none")}}, u.genOpts...)
		u.gen, u.judge = line{i18n.T("settings.models.use.gen.none"), lookNone}, line{i18n.T("settings.models.use.judge.none"), lookNone}
		if st.DefaultModel != "" {
			u.gen = line{i18n.Tf("settings.models.default.missing", st.DefaultModel), lookFail}
		}
		return u
	}
	u.gen = capLine(u.genSvc, core.CapGen)
	if u.judgeSvc != nil {
		u.judge = capLine(u.judgeSvc, core.CapJudge)
	} else {
		u.judge, u.suggest = line{i18n.Tf("settings.models.use.judge.slow", genModel), lookSlow}, true
	}
	if u.gen.look == lookFail {
		end := i18n.T("settings.models.use.alert.end")
		if u.judge.look == lookFail || u.judgeSvc == nil {
			end = i18n.T("settings.models.use.alert.judge_too")
		}
		u.alert = i18n.Tf("settings.models.use.alert", u.genSvc.Name, failReason(u.genSvc.Status[core.CapGen])) + end
	}
	return u
}

// capLine is a default's status line: its last test of capability c.
func capLine(s *core.ModelService, c string) line {
	st, ok := s.Status[c]
	switch {
	case !ok:
		return line{s.Name + " · " + i18n.T("settings.models.status.untested"), lookUntested}
	case st.State == "ok":
		return line{s.Name + " · " + i18n.Tf("settings.models.test.ok", st.LatencyMs), lookOK}
	}
	return line{s.Name + " · " + failReason(st), lookFail}
}

// failReason is why a test failed: the HTTP status, when known, and the code.
func failReason(st core.CapStatus) string {
	r := cmpTr("settings.models.fail."+st.Code, i18n.T("settings.models.fail.error"))
	if st.HTTPStatus > 0 {
		return strconv.Itoa(st.HTTPStatus) + " · " + r
	}
	return r
}

// svcLook is a service's status chip: Key 失效 when a capability's key was
// refused, 连接失败 when one failed otherwise, 未测试 while one is untested.
func svcLook(s core.ModelService) (look, string) {
	var caps []string
	if s.Kind != "" {
		caps = append(caps, core.CapGen)
	}
	if s.JudgeFormat != "" {
		caps = append(caps, core.CapJudge)
	}
	l := lookOK
	for _, c := range caps {
		st, ok := s.Status[c]
		switch {
		case ok && st.State != "ok" && st.Code == "auth":
			return lookFail, i18n.T("settings.models.status.bad")
		case ok && st.State != "ok":
			l = lookFail
		case !ok && l == lookOK:
			l = lookUntested
		}
	}
	switch l {
	case lookFail:
		return l, i18n.T("settings.models.fail.error")
	case lookUntested:
		return l, i18n.T("settings.models.status.untested")
	}
	return l, i18n.T("settings.models.status.ok")
}

var httpStatus = regexp.MustCompile(`HTTP \d{3}`)

// fetchFailure is why a fetch failed: the status the service answered when the error carries one.
func fetchFailure(err error) string {
	if st := httpStatus.FindString(err.Error()); st != "" {
		return st
	}
	return humanize(err)
}

func kindLabel(kind string) string { return cmpTr("settings.models.kind."+kind, kind) }

// badge is a service's two-letter mark: its provider's.
func badge(provider string) string {
	switch provider {
	case core.ProviderOpenAI:
		return "AI"
	case core.ProviderAnthropic:
		return "AN"
	case core.ProviderTypeSafe:
		return "TS"
	}
	return "GW" // other: a gateway or compatible service
}

// listCard is a bordered list of rows.
func listCard(c *ui.Context, rows func()) {
	t := theme.Of(c)
	ui.Column(c).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border).Background(t.Background).Clip().Children(rows)
}

// listRow is a clickable row of a listCard; i > 0 draws a divider above it.
// Selected (on), it does not click.
func listRow(c *ui.Context, i int, on bool, onClick func(), children func()) ui.Element {
	t, th := theme.Of(c), c.Theme()
	b := kit.Pressable(c, kit.PressableOpts{Selected: on, OnClick: onClick}).Radius(0).Gap(th.Space(3)).Padding(th.Space(3), th.Space(4))
	if i > 0 {
		b.BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border)
	}
	return b.Children(children)
}
