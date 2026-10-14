package app

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
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
	saveService  func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ModelService, error)
}

// modelForm edits one service (id 0: a new one). A result for a form that
// has been replaced since leaves the new one alone.
type modelForm struct {
	id                       int64
	title, slug              string // as saved
	savedKind, savedJudge    string // as saved: where a stored key may go
	savedURL                 string // as saved, normalized
	provider                 string // its tile; fixed once added
	email, flowID, authURL   string // this session's OAuth sign-in; authURL for Reopen
	loggingIn                bool
	loginErr                 string
	confirmDel               bool   // the footer asks to confirm the removal
	keyFocus, urlFocus       bool
	fetch, fetchErr          string // "ok" or "fail" once the key's /models fetch is back; why it failed
	fetched                  int    // how many models that fetch listed
	probeConn                string // conn() of the fetch under way
	showKey, refetch         bool   // the typed key is shown; the key or address changed during a fetch, which asks for another
	kind, name, baseURL, key string // kind: the generate format, kept while generate is off
	genOn, judgeOn           bool
	judgeFormat, judgeModel  string   // kept while judge is off
	judgeModels              []string // the judge models the last fetch listed
	hasKey, noKey            bool     // noKey: the user chose no key for a changed address
	nameAuto                 bool     // other: the name follows the address's host until the user types one
	autoName, autoURL        string   // the name nameAuto set last, and the address it set it for
	manual                   string   // a model ID being typed
	models, offered          []string
	busy, probing            bool
	gen, judge               capTest // the dialog's tests; cleared when conn() changes
	tests                    int     // bumped by each test: only the newest one's results show
	connEdits                uint64  // bumped by every change of conn() edited sees
	connSeen                 string
	keyErr                   string // that service refused the key
	urlErr, urlBad           string // why the address urlBad was refused when it lost focus
}

// capTest is a capability's test in the dialog: state "", "testing", "ok" or
// "fail". status is what core found (State "" for an input error), basis
// the form's capBasis when the test started.
type capTest struct {
	state, text string
	status      core.CapStatus
	basis       string
	usage       bool
}

// providerURL is each provider's address (the design's PROVIDERS).
var providerURL = map[string]string{core.ProviderOpenAI: "https://api.openai.com/v1", core.ProviderAnthropic: "https://api.anthropic.com/v1",
	core.ProviderTypeSafe: "https://api.typesafe.ai/v1", core.ProviderOther: "",
	core.ProviderChatGPT: core.ModelProviderURL(core.ProviderChatGPT), core.ProviderGrok: core.ModelProviderURL(core.ProviderGrok)}

var providerOrder = []string{core.ProviderOpenAI, core.ProviderAnthropic, core.ProviderTypeSafe, core.ProviderOther, core.ProviderChatGPT, core.ProviderGrok}

func svcProvider(p string) string {
	switch p {
	case core.ProviderOpenAI:
		return "OpenAI"
	case core.ProviderAnthropic:
		return "Anthropic"
	case core.ProviderTypeSafe:
		return "TypeSafe"
	case core.ProviderChatGPT:
		return i18n.T("settings.models.preset.chatgpt")
	case core.ProviderGrok:
		return i18n.T("settings.models.preset.grok")
	}
	return i18n.T("settings.models.preset.custom")
}

// formats are the generate and judge formats' labels, and their API's name in an error.
var formats = map[string]struct{ label, api string }{
	ai.KindResponses: {"OpenAI Responses", "Responses"}, ai.KindOpenAI: {"Chat Completions", "Chat Completions"}, ai.KindAnthropic: {"Anthropic Messages", "Messages"},
	string(ai.FormatDecisions): {"OpenAI Decisions", "Decisions"}, string(ai.FormatSystemOne): {"TypeSafe System One", "System One"},
}

// localGateway is the 本地网关 preset: the port and path are the user's to type.
const localGateway = "http://127.0.0.1:"

// gateways are the 常用 presets of other; "" names the local gateway.
var gateways = []struct{ name, url string }{
	{"OpenRouter", "https://openrouter.ai/api/v1"}, {"Vercel AI Gateway", "https://ai-gateway.vercel.sh/v1"}, {"LiteLLM", "http://127.0.0.1:4000"}, {"", localGateway},
}

// hosts name an other service after its address (the design's HOSTS); "" is the local gateway.
var hosts = map[string]string{"openrouter.ai": "OpenRouter", "ai-gateway.vercel.sh": "Vercel AI Gateway", "127.0.0.1": "", "localhost": ""}

var hostOf = regexp.MustCompile(`(?i)^[a-z]+://([^/:]+)`)

// hostName is the name an other service at u gets: its host's, else the host.
func hostName(u string) string {
	m := hostOf.FindStringSubmatch(strings.TrimSpace(u))
	if m == nil {
		return ""
	}
	n, ok := hosts[m[1]]
	switch {
	case !ok:
		return m[1]
	case n == "":
		return i18n.T("settings.models.local.name")
	}
	return n
}

// newForm is a new service of provider p as the design's blank() makes it.
func newForm(p string) *modelForm {
	gen, judge := core.ProviderFormats(p)
	f := &modelForm{provider: p, baseURL: providerURL[p], nameAuto: true, genOn: len(gen) > 0, judgeOn: len(judge) > 0 && p != core.ProviderOther}
	if p != core.ProviderOther {
		f.name = svcProvider(p)
	}
	f.kind, f.judgeFormat = first(gen), first(judge)
	f.judgeModel = first(ai.JudgeFormat(f.judgeFormat).Models())
	return f
}

func first(s []string) string { return append(s, "")[0] }

// url is the address as it will be saved, or as typed when it is not valid.
func (f *modelForm) url() string {
	raw := strings.TrimSpace(f.baseURL)
	if u, err := core.NormalizeModelBaseURL(raw); err == nil {
		return u
	}
	return raw
}

func (f *modelForm) custom() bool { return f.provider == core.ProviderOther }
func (f *modelForm) oauth() bool  { return core.ModelProviderLogin(f.provider) == "oauth" }

// moved reports whether a saved service now points at another address or
// format, which core won't send the stored key to.
func (f *modelForm) moved() bool {
	switched := func(was, is string, on bool) bool { return was != "" && on && was != is }
	return f.id != 0 && (f.url() != f.savedURL || switched(f.savedKind, f.kind, f.genOn) || switched(f.savedJudge, f.judgeFormat, f.judgeOn))
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

// conn is what a fetch or test depends on: the formats, address and key.
func (f *modelForm) conn() string {
	return strings.Join([]string{f.kind, f.judgeFormat, f.url(), f.keyAction(), f.key, f.flowID}, "\n")
}

// capBasis is what a test of capability c ran on: conn() and the model it asked.
func (f *modelForm) capBasis(c string) string {
	if c == core.CapGen {
		return f.conn() + "\n" + first(f.models)
	}
	return f.conn() + "\n" + f.judgeModel
}

// found is the dialog's finished tests that still describe the form: they
// become the saved service's status.
func (f *modelForm) found() map[string]core.CapStatus {
	out := map[string]core.CapStatus{}
	for c, t := range map[string]capTest{core.CapGen: f.gen, core.CapJudge: f.judge} {
		if on := c == core.CapGen && f.genOn || c == core.CapJudge && f.judgeOn; on && t.status.State != "" && t.basis == f.capBasis(c) {
			out[c] = t.status
		}
	}
	return out
}

// edited counts the changes of conn() it sees; each clears the tests, which
// said nothing about the new address, key or format.
func (f *modelForm) edited() {
	if c := f.conn(); c != f.connSeen {
		f.connSeen, f.connEdits = c, f.connEdits+1
		f.gen, f.judge = capTest{}, capTest{}
	}
}

// follow keeps an other service's name on its address's host until the
// user types a name.
func (f *modelForm) follow() {
	if f.name != f.autoName {
		f.nameAuto = false
	}
	if f.nameAuto && f.custom() && f.baseURL != f.autoURL {
		f.name = hostName(f.baseURL)
		f.autoName, f.autoURL = f.name, f.baseURL
	}
}

// pickGateway fills the address of a 常用 preset, and its name while the name follows the address.
func (f *modelForm) pickGateway(name, url string) {
	f.follow()
	f.baseURL = url
	if f.nameAuto {
		f.name, f.autoName, f.autoURL = name, name, url
	}
}

func (f *modelForm) anyCap() bool  { return f.genOn || f.judgeOn }
func (f *modelForm) anyKey() bool  { return f.key != "" || f.hasKey || f.flowID != "" && !f.loggingIn }
func (f *modelForm) testing() bool { return f.gen.state == "testing" || f.judge.state == "testing" }
func (f *modelForm) needKey() bool {
	if f.oauth() {
		return f.id == 0 && (f.flowID == "" || f.loggingIn)
	}
	return f.id == 0 && !f.custom() && f.key == ""
}
func (f *modelForm) needModel() bool { return f.genOn && len(f.models) == 0 }
func (f *modelForm) noURL() bool     { return strings.TrimSpace(f.baseURL) == "" }
func (f *modelForm) noName() bool    { return strings.TrimSpace(f.name) == "" }
func (f *modelForm) failed() bool    { return f.gen.state == "fail" || f.judge.state == "fail" }
func (f *modelForm) canResetURL() bool {
	return !f.custom() && !defaultURL(f.provider, strings.TrimSpace(f.baseURL))
}

// defaultURL: u is provider p's address. Older Godwits stored Anthropic's without /v1.
func defaultURL(p, u string) bool {
	return u == providerURL[p] || p == core.ProviderAnthropic && u == "https://api.anthropic.com"
}

// blocked is the design's rule for the primary button, and a generator needs a model.
func (f *modelForm) blocked() bool {
	return f.noName() || f.noURL() || !f.anyCap() || f.needKey() || f.needModel()
}

// hint is what the footer says is missing, in the design's order.
func (f *modelForm) hint() string {
	switch {
	case !f.anyCap():
		return i18n.T("settings.models.need.cap")
	case f.noURL():
		return i18n.T("settings.models.need.url")
	case f.needKey() && f.oauth():
		return i18n.T("settings.models.need.login")
	case f.needKey():
		return i18n.T("settings.models.need.key")
	case f.noName():
		return i18n.T("settings.models.need.name")
	case f.needModel():
		return i18n.T("settings.models.need.model")
	case f.failed() && f.id == 0:
		return i18n.T("settings.models.need.failed_add")
	case f.failed():
		return i18n.T("settings.models.need.failed_save")
	}
	return ""
}

func (f *modelForm) cantTest() bool {
	return !f.anyCap() || !f.custom() && !f.anyKey() || f.noURL() || f.testing()
}

// decList is the judge models to choose from: none until there is a key
// (other: the format's own); the fetched ones, else the format's.
func (f *modelForm) decList() []string {
	if f.judgeFormat == "" || !f.anyKey() && !f.custom() {
		return nil
	}
	list := ai.JudgeFormat(f.judgeFormat).Models()
	if !f.custom() && len(f.judgeModels) > 0 {
		list = slices.Clone(f.judgeModels)
	}
	if f.judgeModel != "" && !slices.Contains(list, f.judgeModel) {
		list = append([]string{f.judgeModel}, list...)
	}
	return list
}

// input is the form as a service input, with an explicit key action. The
// models are kept while generate is off, for when it is turned on again.
func (f *modelForm) input() core.ModelServiceInput {
	in := core.ModelServiceInput{Name: strings.TrimSpace(f.name), BaseURL: strings.TrimSpace(f.baseURL), APIKey: f.key, KeyAction: f.keyAction(),
		Models: slices.Clone(f.models), Provider: f.provider}
	if f.oauth() {
		in.OAuthFlowID, in.APIKey, in.KeyAction = f.flowID, "", ""
	}
	if f.genOn {
		in.Kind = f.kind
	}
	if f.judgeOn {
		in.JudgeFormat, in.JudgeModel = f.judgeFormat, f.judgeModel
	}
	return in
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

// offWarns are the warnings for turning off a capability of the saved
// service the defaults use: generate's names the service it moves to (alt).
func offWarns(f *modelForm, st core.Settings, services []core.ModelService) (gen, judge string) {
	if f.id == 0 {
		return
	}
	genF, judgeF := core.ProviderFormats(f.provider)
	if strings.HasPrefix(st.DefaultModel, f.slug+"/") && !f.genOn && len(genF) > 0 {
		gen = i18n.T("settings.models.off.gen.only")
		if alt := altGen(services, f.id); alt != nil {
			gen = i18n.Tf("settings.models.off.gen.alt", alt.Name+" / "+alt.Models[0])
		}
	}
	if st.JudgeService == f.slug && !f.judgeOn && len(judgeF) > 0 {
		judge = i18n.T("settings.models.off.judge")
	}
	return gen, judge
}

// altGen is where the default generator moves when service id stops
// generating: the first other service that generates, at its first model.
func altGen(services []core.ModelService, id int64) *core.ModelService {
	for i, o := range services {
		if o.ID != id && o.Kind != "" && len(o.Models) > 0 {
			return &services[i]
		}
	}
	return nil
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
	p := cmp.Or(s.Provider, core.ProviderOther)
	gen, judge := core.ProviderFormats(p)
	savedURL, _ := core.NormalizeModelBaseURL(s.BaseURL)
	f := &modelForm{id: s.ID, title: s.Name, slug: s.Slug, savedKind: s.Kind, savedJudge: s.JudgeFormat, savedURL: cmp.Or(savedURL, s.BaseURL), provider: p,
		email: s.Email, name: s.Name, baseURL: s.BaseURL, hasKey: s.HasKey, models: slices.Clone(s.Models), offered: slices.Clone(s.Models),
		kind: cmp.Or(s.Kind, first(gen)), genOn: s.Kind != "", judgeOn: s.JudgeFormat != "", judgeFormat: cmp.Or(s.JudgeFormat, first(judge)), judgeModel: s.JudgeModel}
	if f.judgeModel == "" {
		f.judgeModel = first(ai.JudgeFormat(f.judgeFormat).Models())
	}
	m.form = f
}

// svcSubtitle is the second line of a service card: the signed-in email for an OAuth provider, else its BaseURL.
func svcSubtitle(s core.ModelService) string {
	if core.ModelProviderLogin(s.Provider) == "oauth" {
		return s.Email
	}
	return s.BaseURL
}

func (m *modelsView) cancelFlow(id string) {
	if id == "" {
		return
	}
	oauth := m.a.oauth
	m.a.do(func(context.Context) error { oauth.Cancel(id); return nil }, nil)
}

func (m *modelsView) cancelForm() {
	if f := m.form; f != nil {
		if f.flowID != "" {
			m.cancelFlow(f.flowID)
			f.flowID = ""
		}
		f.loggingIn = false
	}
}

func (m *modelsView) openURL(url string) {
	plat, a := m.a.plat, m.a
	a.do(func(context.Context) error { return plat.OpenURL(url) }, a.toastErr)
}

func loginError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return ""
	case errors.Is(err, auth.ErrAccessDenied):
		return i18n.T("settings.models.login.denied")
	case errors.Is(err, auth.ErrFlowExpired):
		return i18n.T("settings.models.login.timeout")
	}
	return humanize(err)
}

// login begins a browser sign-in for f, opens it, awaits the result, and
// fetches the service's models once signed in.
func (m *modelsView) login(f *modelForm) {
	if f.flowID != "" {
		m.cancelFlow(f.flowID)
	}
	f.loggingIn, f.loginErr, f.flowID, f.authURL = true, "", "", ""
	a := m.a
	oauth, plat, prov, hint := a.oauth, a.plat, f.provider, strings.TrimSpace(f.email)
	var id, url string
	var openErr error
	a.do(func(ctx context.Context) error {
		var err error
		if id, url, err = oauth.Begin(ctx, prov, hint); err != nil {
			return err
		}
		openErr = plat.OpenURL(url)
		return nil
	}, func(err error) {
		if m.form != f || !f.loggingIn {
			if err == nil && id != "" {
				m.cancelFlow(id)
			}
			return
		}
		if err != nil {
			f.loggingIn = false
			f.loginErr = loginError(err)
			return
		}
		a.toastErr(openErr)
		f.flowID, f.authURL = id, url
		var email string
		a.do(func(ctx context.Context) error {
			var err error
			email, err = oauth.Await(ctx, id)
			return err
		}, func(err error) {
			if m.form != f || !f.loggingIn || f.flowID != id {
				return
			}
			f.loggingIn = false
			if errors.Is(err, context.Canceled) {
				f.flowID = ""
				return
			}
			a.plat.ShowWindow()
			if err != nil {
				f.flowID = ""
				f.loginErr = loginError(err)
				return
			}
			f.email, f.flowID = email, id
			m.fetch(f)
		})
	})
}

// openEdit shows the service in the dialog and quietly lists its models.
func (m *modelsView) openEdit(s core.ModelService) {
	m.edit(s)
	m.open = true
	m.fetchAs(m.form, true)
}

func (m *modelsView) add() { m.form, m.open = newForm(core.ProviderOpenAI), true }

// addLocal opens the dialog for a local gateway: 其他服务 with a loopback address to complete.
func (m *modelsView) addLocal() {
	f := newForm(core.ProviderOther)
	f.name, f.baseURL = i18n.T("settings.models.local.name"), localGateway
	f.autoName, f.autoURL = f.name, f.baseURL
	m.form, m.open = f, true
}

// save writes the form; a failed test doesn't stop it.
func (m *modelsView) save(f *modelForm) {
	if f.busy || f.blocked() || f.needsKeyDecision() { // the form says what is missing
		return
	}
	m.submit(f)
}

// submit writes the form as it is now. On success an unedited form shows the
// saved service; one edited meanwhile keeps the edits and takes only the
// service's ID, name, address and key state. References to what it no
// longer offers are cleared, the default generator moving to altGen.
func (m *modelsView) submit(f *modelForm) {
	a := m.a
	snap := *f
	snap.models = slices.Clone(f.models)
	in, id, found, save := f.input(), f.id, f.found(), m.saveService
	f.busy = true
	var saved core.ModelService
	a.writes.Chain("models", func(ctx context.Context) (err error) {
		switch {
		case save != nil:
			saved, err = save(ctx, id, in)
		case id == 0:
			saved, err = a.svc.AddModelService(ctx, in)
		default:
			saved, err = a.svc.UpdateModelService(ctx, id, in)
		}
		if err != nil {
			return err
		}
		// shortcut: a test still running at save time isn't kept; the user tests again.
		for c, st := range found {
			if rerr := a.svc.RecordCapStatus(ctx, saved.ID, saved.Version, c, st); rerr != nil {
				slog.Warn("model service status", "service", saved.ID, "err", rerr)
			}
		}
		return nil
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
			f.flowID = ""
			m.close()
		default:
			f.id, f.title, f.slug, f.hasKey, f.savedKind, f.savedJudge = saved.ID, saved.Name, saved.Slug, saved.HasKey, saved.Kind, saved.JudgeFormat
			f.email = cmp.Or(saved.Email, f.email)
			f.savedURL, _ = core.NormalizeModelBaseURL(saved.BaseURL)
			if f.key == snap.key {
				f.key = ""
			}
			if f.flowID == snap.flowID {
				f.flowID = ""
			}
		}
		noDefault := a.settings.DefaultModel == ""
		if id != 0 {
			m.dropRefs(saved)
		}
		fillGen := noDefault && saved.Kind != "" && len(saved.Models) > 0                              // this service's first model becomes it: Wit needs one
		fillJudge := a.settings.JudgeService == "" && saved.JudgeFormat != "" && snap.savedJudge == "" // judge just turned on answers the judge questions
		if fillGen || fillJudge {
			m.setModel(func(st *core.Settings) {
				if fillGen {
					st.DefaultModel = saved.Slug + "/" + saved.Models[0]
				}
				if fillJudge {
					st.JudgeService = saved.Slug
				}
			})
		}
		a.toastAs(kit.ToastSuccess, i18n.T("settings.saved"))
		m.load()
	})
}

func (f *modelForm) unchanged(snap modelForm) bool {
	return f.kind == snap.kind && f.name == snap.name && f.baseURL == snap.baseURL && f.key == snap.key && f.noKey == snap.noKey &&
		f.flowID == snap.flowID && f.manual == snap.manual && slices.Equal(f.models, snap.models) && f.genOn == snap.genOn && f.judgeOn == snap.judgeOn &&
		f.judgeFormat == snap.judgeFormat && f.judgeModel == snap.judgeModel
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
// DefaultModel cleared moves to altGen, as the dialog's warning promises.
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
	if alt := altGen(services, svc.ID); changed && st.DefaultModel == "" && alt != nil {
		st.DefaultModel = alt.Slug + "/" + alt.Models[0]
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

// fetch lists the models when the key is entered or loses focus. A result
// is dropped once the dialog has closed or the formats, address or key
// changed since it started; a key or address changed meanwhile asks again.
func (m *modelsView) fetch(f *modelForm) { m.fetchAs(f, false) }

// fetchAs is fetch; quiet (an edit dialog opening) offers what it lists
// without saying how the fetch went.
func (m *modelsView) fetchAs(f *modelForm, quiet bool) {
	if f.probing {
		f.refetch = f.conn() != f.probeConn
		return
	}
	if _, err := core.NormalizeModelBaseURL(strings.TrimSpace(f.baseURL)); err != nil || !f.anyCap() || f.needsKeyDecision() {
		return
	}
	f.edited()
	a, id, in, check, gen, conn, connEdits := m.a, f.id, f.input(), m.checkService, m.a.gen, f.conn(), f.connEdits
	in.Name = cmp.Or(in.Name, "-") // checked, never saved
	f.probing, f.keyErr, f.refetch, f.probeConn = true, "", false, conn
	if !quiet {
		f.fetch, f.fetchErr = "", ""
	}
	var res core.ServiceCheck
	a.do(func(ctx context.Context) (err error) {
		res, err = check(ctx, id, in)
		return err
	}, func(err error) {
		f.probing = false
		defer func() {
			if f.refetch && m.form == f && gen == a.gen {
				m.fetch(f)
			}
		}()
		if m.form != f || gen != a.gen || f.connEdits != connEdits {
			return
		}
		switch {
		case errors.Is(err, core.ErrModelAuth):
			f.keyErr = humanize(err)
			return
		case errors.Is(err, core.ErrModelReauth):
			f.loginErr = humanize(err)
			return
		case err != nil:
			if !quiet {
				f.fetch, f.fetchErr = "fail", fetchFailure(err)
			}
			return
		}
		f.judgeModels = res.JudgeModels
		if f.judgeModel == "" {
			f.judgeModel = first(f.decList())
		}
		if in.Kind == "" {
			return
		}
		fresh := len(f.offered) == 0
		for _, g := range res.Models {
			if !slices.Contains(f.offered, g) {
				f.offered = append(f.offered, g)
			}
		}
		if fresh && len(f.models) == 0 && len(res.Models) > 0 { // the first listed is chosen, as the design's onKey does
			f.models = []string{res.Models[0]}
		}
		switch {
		case quiet:
		case !res.ListSupported || len(res.Models) == 0:
			f.fetch, f.fetchErr = "fail", ""
			if res.NoListStatus > 0 {
				f.fetchErr = strconv.Itoa(res.NoListStatus)
			}
		default:
			f.fetch, f.fetched = "ok", len(res.Models)
		}
	})
}

// test tests each capability that is on, as the form has it, saved or not;
// core stores a saved service's status, which the list reads again. A result
// is dropped once conn() changed since it started.
func (m *modelsView) test(f *modelForm) {
	if f.cantTest() {
		return
	}
	f.edited()
	f.tests++
	a, id, in, test, gen, connEdits, run := m.a, f.id, f.input(), m.testService, m.a.gen, f.connEdits, f.tests
	for _, c := range []struct {
		cap, format string
		on          bool
		res         *capTest
	}{{core.CapGen, f.kind, f.genOn, &f.gen}, {core.CapJudge, f.judgeFormat, f.judgeOn, &f.judge}} {
		*c.res = capTest{}
		if !c.on {
			continue
		}
		*c.res = capTest{state: "testing"}
		basis := f.capBasis(c.cap)
		var st core.CapStatus
		a.do(func(ctx context.Context) (err error) {
			st, err = test(ctx, id, in, c.cap)
			return err
		}, func(err error) {
			if gen == a.gen {
				m.load()
			}
			switch {
			case m.form != f || f.tests != run: // a later test owns the lines
			case gen != a.gen || f.connEdits != connEdits:
				*c.res = capTest{}
			default:
				*c.res = capResult(c.format, st, err)
				c.res.status, c.res.basis = st, basis
			}
		})
	}
}

// capResult is a capability test's status line.
func capResult(format string, st core.CapStatus, err error) capTest {
	switch {
	case err == nil:
		return capTest{state: "ok", text: i18n.Tf("settings.models.test.ok", st.LatencyMs)}
	case isChatGPTUsage(err):
		return capTest{state: "fail", text: humanize(err), usage: true}
	case errors.Is(err, core.ErrModelReauth):
		return capTest{state: "fail", text: humanize(err)}
	case st.Code == "forbidden":
		return capTest{state: "fail", text: strconv.Itoa(st.HTTPStatus) + " · " + i18n.Tf("settings.models.fail.no_api", formats[format].api)}
	case st.State == "fail":
		return capTest{state: "fail", text: failReason(st)}
	}
	return capTest{state: "fail", text: humanize(err)}
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
	// A setting naming what isn't offered shows as 未设置, as core falls back past it.
	u.smallOpts, u.judgeOpts = unset(u.smallOpts, st.SmallModel), unset(u.judgeOpts, st.JudgeService)
	if u.genSvc == nil {
		u.genOpts = append([]kit.Option[string]{{Value: st.DefaultModel, Label: i18n.T("settings.models.default.none")}}, u.genOpts...)
		u.gen, u.judge = line{i18n.T("settings.models.use.gen.none"), lookNone}, line{i18n.T("settings.models.use.judge.none"), lookNone}
		if st.DefaultModel != "" {
			u.gen = line{i18n.Tf("settings.models.default.missing", st.DefaultModel), lookFail}
		}
		if u.judgeSvc != nil {
			u.judge = capLine(u.judgeSvc, core.CapJudge)
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

// unset puts v first as 未设置 when opts doesn't offer it.
func unset(opts []kit.Option[string], v string) []kit.Option[string] {
	if slices.ContainsFunc(opts, func(o kit.Option[string]) bool { return o.Value == v }) {
		return opts
	}
	return append([]kit.Option[string]{{Value: v, Label: i18n.T("settings.models.default.none")}}, opts...)
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
		return strings.TrimPrefix(st, "HTTP ")
	}
	return humanize(err)
}

// badge is a service's two-letter mark: its provider's.
func badge(provider string) string {
	switch provider {
	case core.ProviderOpenAI:
		return "AI"
	case core.ProviderAnthropic:
		return "AN"
	case core.ProviderTypeSafe:
		return "TS"
	case core.ProviderChatGPT:
		return "CG"
	case core.ProviderGrok:
		return "GK"
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
