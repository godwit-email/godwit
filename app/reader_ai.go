package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// The AI of the reader, per message: every expanded message gets its
// summary and plugin cards and can be translated; the toolbar's Summary,
// Translate and skill menu act on the newest one, which alone gets quick
// replies. ponytail: r.ai keeps every message read this session; evict the
// oldest if memory ever matters.

// aiRun is a skill's or action's output, streamed in on the UI thread, with
// the model and target language it ran with.
type aiRun struct {
	text, model, lang string
	doc               richtext.Doc // a plugin action's HTML output
	markdown          *markdown.Options
	translations      map[string]string
	err               error
	running, done     bool
}

// fetch is a read for the reader: done once it succeeded; a failure keeps
// err for a retry. gen is the reader gen of the last attempt.
type fetch[T any] struct {
	v             T
	done, loading bool
	err           error
	gen           uint64
	seq           uint64 // bumped per read and by restart; only the newest read lands
}

// fetchInto reads into f unless it is read, being read, or (without force)
// was tried during this visit. failed, if set, hears a failure.
// restart forgets what was read and drops a read in flight, so the next
// fetchInto reads afresh.
func (f *fetch[T]) restart() {
	f.seq++
	f.done, f.loading = false, false
}

func fetchInto[T any](r *reader, f *fetch[T], force bool, get func(context.Context) (T, error), failed func(error)) {
	if f.done || f.loading && f.gen == r.gen || !force && f.gen == r.gen {
		return
	}
	a, gen, ctx := r.p.a, r.gen, r.aiContext()
	f.seq++
	seq := f.seq
	f.gen, f.loading, f.err = gen, true, nil
	var v T
	a.do(func(context.Context) (err error) {
		v, err = get(ctx)
		return err
	}, func(err error) {
		if gen != r.gen || seq != f.seq {
			return
		}
		f.loading = false
		if err != nil {
			f.err = err
			if failed != nil {
				failed(err)
			}
			return
		}
		f.v, f.done = v, true
	})
}

// msgAI is the AI state of one message.
type msgAI struct {
	summary     *aiRun
	sumShown    *bool // the Summary action's choice; nil follows Settings.Summary
	translation *aiRun
	showTr      bool // the translation was asked to show
	result      *aiResult
	cards       fetch[[]core.Card]
	tags        fetch[[]core.Tag]
	quick       fetch[[]core.QuickReply]
	gist        fetch[string]
}

// aiResult is the closable card of a skill or action run from the menu.
type aiResult struct {
	title string
	run   *aiRun
}

// egressAsk is an action whose plugin wants to send data out.
type egressAsk struct {
	id, title string
	msgID     int64
	plugin    string
	hosts     []string
}

func (r *reader) state(id int64) *msgAI {
	s := r.ai[id]
	if s == nil {
		s = &msgAI{}
		r.ai[id] = s
	}
	return s
}

// aiReady reports whether a default model is set: the skill menu's skills need it.
func (r *reader) aiReady() bool { return r.p.a.settings.DefaultModel != "" }

// taskReady reports whether core resolves a model for the reader task:
// "summarize", "translate" or "" for quick replies (App.taskModels).
func (r *reader) taskReady(task string) bool { return r.p.a.taskModels[task] }

// modelOf is a model reference (service/model) without its service.
func modelOf(ref string) string {
	if _, m, ok := strings.Cut(ref, "/"); ok {
		return m
	}
	return ref
}

// modelLabel drops the empty model slot that ends a label until the run
// reports its model.
func modelLabel(s string) string { return strings.TrimSuffix(s, " · ") }

// readingLang is the language mail is translated into.
func (r *reader) readingLang() string {
	st := r.p.a.settings
	return cmp.Or(st.ReadingLanguage, st.UILanguage, r.p.a.sysLang, "en")
}

// shortLang names a reading language in the UI's language, briefly: 中文, not 简体中文.
func shortLang(tag string) string {
	base, _, _ := strings.Cut(strings.ToLower(tag), "-")
	switch base {
	case "de", "en", "es", "fr", "it", "ja", "ko", "nl", "pt", "ru", "tr", "vi", "zh":
		return i18n.T("reader.lang." + base)
	}
	return i18n.T("reader.lang.en")
}

// summaryShown reports whether a message's summary shows: with a model,
// as the Summary action chose, else as Settings.Summary says.
func (r *reader) summaryShown(st *msgAI) bool {
	if !r.taskReady("summarize") {
		return false
	}
	if st.sumShown != nil {
		return *st.sumShown
	}
	return r.p.a.settings.Summary
}

// trShown reports whether a message shows its translation: asked for, and
// into the current reading language.
func (r *reader) trShown(st *msgAI) bool {
	return st.showTr && st.translation != nil && st.translation.lang == r.readingLang()
}

// aiContext is the context of the AI work for the key shown.
func (r *reader) aiContext() context.Context {
	if r.aiCtx == nil {
		r.aiCtx, r.aiStop = context.WithCancel(r.p.a.ctx)
	}
	return r.aiCtx
}

// stopAI cancels the AI work of the key shown and forgets unfinished runs,
// so coming back starts them again; the menu is read again then too.
func (r *reader) stopAI() {
	if r.aiStop != nil {
		r.aiStop()
	}
	r.aiCtx, r.aiStop = nil, nil
	for _, s := range r.ai {
		if s.summary != nil && s.summary.running {
			s.summary, s.sumShown = nil, nil // the next Summary click runs it again
		}
		if s.translation != nil && s.translation.running {
			s.translation, s.showTr = nil, false
		}
		if s.result != nil && s.result.run.running {
			s.result = nil
		}
	}
	r.skills.done, r.actions.done = false, false
	r.egressDlg = false
}

// startFor starts what an expanded message shows by itself: its summary
// (Settings.Summary with a model; the newest only, the one a thread shows)
// and its plugin cards. Each runs once.
func (r *reader) startFor(id int64) {
	st := r.state(id)
	if last, _ := r.last(); id == last.ID && r.taskReady("summarize") && r.p.a.settings.Summary && st.summary == nil {
		st.summary = r.stream(id, "summarize")
	}
	r.loadCards(id, false)
	r.loadTags(id, false)
}

func (r *reader) loadCards(id int64, force bool) {
	get := r.cardsFn
	fetchInto(r, &r.state(id).cards, force, func(ctx context.Context) ([]core.Card, error) { return get(ctx, id) }, nil)
}

func (r *reader) loadTags(id int64, force bool) {
	get := r.tagsFn
	fetchInto(r, &r.state(id).tags, force, func(ctx context.Context) ([]core.Tag, error) { return get(ctx, id) }, nil)
}

// reloadPluginParts reads the open messages' cards and tags again, as a
// plugin was switched, installed or removed.
func (r *reader) reloadPluginParts() {
	ids := maps.Clone(r.open)
	if m, ok := r.last(); ok {
		ids[m.ID] = true // its tags follow the subject even while it is folded
	}
	for id := range ids {
		if st := r.ai[id]; st != nil {
			st.cards.restart()
			st.tags.restart()
		}
		r.loadCards(id, true)
		r.loadTags(id, true)
	}
}

// gist is a folded message's one-line gist, asked for as its row shows (the
// small model, under Settings.Summary): "" until it comes, and when it
// fails or there is no model, so the row keeps the start of its text.
func (r *reader) gist(id int64) string {
	if !r.taskReady("") || !r.p.a.settings.Summary {
		return ""
	}
	st, get := r.state(id), r.gistFn
	fetchInto(r, &st.gist, false, func(ctx context.Context) (string, error) { return get(ctx, id) }, nil)
	return st.gist.v
}

func (r *reader) loadQuick(id int64, force bool) {
	get := r.quickFn
	fetchInto(r, &r.state(id).quick, force, func(ctx context.Context) ([]core.QuickReply, error) { return get(ctx, id) }, nil)
}

// startAI, once the thread is read, starts the AI of the expanded messages,
// the newest one's quick replies, and reads the skill menu's two sources.
func (r *reader) startAI() {
	m, ok := r.last()
	if !ok {
		return
	}
	for id := range r.open {
		r.startFor(id)
	}
	if r.taskReady("") && r.p.a.settings.QuickReplies {
		r.loadQuick(m.ID, false)
	}
	a, skills, actions := r.p.a, r.skillsFn, r.actionsFn
	fetchInto(r, &r.skills, false, func(ctx context.Context) ([]core.SkillInfo, error) {
		sks, err := skills(ctx)
		return slices.DeleteFunc(sks, func(s core.SkillInfo) bool {
			return !s.Enabled || !slices.Contains(s.On, "message") || s.Name == "summarize" || s.Name == "translate"
		}), err
	}, a.toastErr) // read again on the next visit
	fetchInto(r, &r.actions, false, func(ctx context.Context) ([]core.ActionInfo, error) {
		return actions(ctx, "message")
	}, a.toastErr)
}

// stream runs skill on a message; its text streams into the run returned,
// labelled with the model the run reports in its context event.
func (r *reader) stream(id int64, skill string) *aiRun {
	a, ctx, start := r.p.a, r.aiContext(), r.runSkill
	run := &aiRun{running: true, lang: r.readingLang(), markdown: &markdown.Options{OnLink: a.openLink}}
	if skill == "translate" {
		run.markdown = nil
	}
	a.do(func(context.Context) error {
		ch, err := start(ctx, skill, core.Target{Kind: "message", ID: id, PreserveHTML: skill == "translate"})
		if err != nil {
			return err
		}
		for ev := range ch {
			switch ev.Kind {
			case "context":
				model := modelOf(ev.Model)
				a.onUI(func() { run.model = model })
			case "text":
				s := ev.Text
				translations := ev.Translations
				a.onUI(func() {
					run.text += s
					if translations != nil {
						run.translations = translations
					}
				})
			case "error":
				if ev.Err != nil {
					return ev.Err
				}
				return errors.New(skill + " failed")
			}
		}
		return ctx.Err()
	}, func(err error) {
		run.running, run.err, run.done = false, err, err == nil
	})
	return run
}

// toggleSummary shows or hides the newest message's summary, running it
// when it has none.
func (r *reader) toggleSummary() {
	m, ok := r.last()
	if !ok {
		return
	}
	st := r.state(m.ID)
	on := !r.summaryShown(st)
	st.sumShown = &on
	if on && (st.summary == nil || st.summary.err != nil) {
		st.summary = r.stream(m.ID, "summarize")
	}
}

// toggleTranslate shows a message translated into the reading language
// (translating it again when that changed), or its original again.
func (r *reader) toggleTranslate(id int64) {
	st := r.state(id)
	if r.trShown(st) {
		st.showTr = false
		return
	}
	st.showTr = true
	if tr := st.translation; tr == nil || tr.err != nil || tr.lang != r.readingLang() {
		st.translation = r.stream(id, "translate")
	}
}

// runSkillCard runs a menu skill into the result card.
func (r *reader) runSkillCard(name string) {
	if m, ok := r.last(); ok {
		r.state(m.ID).result = &aiResult{title: name, run: r.stream(m.ID, name)}
	}
}

// runAction runs a plugin action on a message. A plugin that may send data
// out asks first; its result shows in the result card. A draft it returns
// opens in a compose window even when the reader has moved on since: an
// action is a command, so switching mail does not cancel it as it does the
// reader's AI work.
func (r *reader) runAction(id, title string, msgID int64, approve bool) {
	a, gen, ctx := r.p.a, r.gen, r.p.a.ctx
	st := r.state(msgID)
	res := &aiResult{title: title, run: &aiRun{running: true}}
	st.result = res
	var out core.ActionResult
	var doc richtext.Doc
	a.do(func(context.Context) (err error) {
		out, err = a.svc.RunAction(ctx, id, core.Target{Kind: "message", ID: msgID}, core.RunActionOptions{ApproveEgress: approve})
		if err == nil && out.HTML != "" {
			doc, _ = richtext.FromMailHTML(out.HTML, richtext.DefaultLimits) // the Service sanitized it
		}
		return err
	}, func(err error) {
		run := res.run
		run.running = false
		current := gen == r.gen && st.result == res
		if err == nil && out.DraftID != 0 {
			if current {
				st.result = nil
			}
			a.plat.OpenCompose(out.DraftID)
			return
		}
		if !current {
			return
		}
		var ask *core.EgressApprovalRequired
		switch {
		case errors.As(err, &ask):
			st.result = nil
			r.egress, r.egressDlg = egressAsk{id: id, title: title, msgID: msgID, plugin: ask.Plugin, hosts: ask.Hosts}, true
		case err != nil:
			run.err = err
		case out.Text == "" && out.HTML == "":
			st.result = nil
		default:
			run.text, run.doc, run.done = out.Text, doc, true
		}
	})
}

// quickReply opens a reply draft starting with q in a compose window, even
// when the reader has moved on since.
func (r *reader) quickReply(msgID int64, q core.QuickReply) {
	a := r.p.a
	var id int64
	a.do(func(ctx context.Context) error {
		d, err := a.svc.DraftFromQuickReply(ctx, msgID, q)
		id = d.ID
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.plat.OpenCompose(id)
	})
}

// aiTools are the toolbar's Summary, Translate and skill menu, for the
// newest message.
func (r *reader) aiTools(c *ui.Context) {
	out := kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}
	m, _ := r.last()
	st := r.state(m.ID)
	t := theme.Of(c)
	// aiButton is a ghost at the toolbar's icon button height; on, it keeps
	// the hover fill. It shows icon before label; a menu's shows a chevron after it.
	aiButton := func(icon, label string, on, menu bool) ui.Element {
		b := kit.ButtonFrame(c, out).Label(label).Height(c.Theme().Space(8))
		if on {
			b.Background(t.HoverTint)
		}
		return b.Children(func() {
			if icon != "" {
				ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm)
			}
			ui.Text(c, label)
			if menu {
				ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			}
		})
	}
	if r.taskReady("summarize") {
		aiButton("file-text", i18n.T("reader.ai.summarize"), r.summaryShown(st) && st.summary != nil, false).OnClick(r.toggleSummary)
	}
	if r.taskReady("translate") {
		aiButton("languages", i18n.T("reader.ai.translate"), r.trShown(st), false).OnClick(func() { r.toggleTranslate(m.ID) })
	}
	var skills []core.SkillInfo
	if r.aiReady() {
		skills = r.skills.v
	}
	actions := r.actions.v
	if len(skills) == 0 && len(actions) == 0 {
		return
	}
	aiButton("", i18n.T("reader.ai.more_skills"), false, true).Menu(func(menu *ui.Menu) {
		for _, sk := range skills {
			if menu.Item(sk.Name).Chosen() {
				r.runSkillCard(sk.Name)
			}
		}
		if len(skills) > 0 && len(actions) > 0 {
			menu.Separator()
		}
		for _, act := range actions {
			label := cmp.Or(act.Label.For(i18n.Language()), act.ID)
			if menu.Item(label).Chosen() {
				r.runAction(act.ID, label, m.ID, false)
			}
		}
	})
}

// aiArea shows above a message's body: its summary, the menu's result and
// the translation's notice.
func (r *reader) aiArea(c *ui.Context, m core.MessageSummary) {
	st := r.ai[m.ID]
	if st == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	if len(r.msgs) == 1 { // a thread's shows under its subject (summaryPanel)
		r.summaryPanel(c, m)
	}
	if res := st.result; res != nil {
		r.panel(c, res.title, "", func() { st.result = nil }, func() {
			r.output(c, res.run)
			r.runState(c, res.run, nil)
		})
	}
	if tr := st.translation; r.trShown(st) && !(tr.done && strings.TrimSpace(tr.text) == "") {
		ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Get("languages")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			kit.Text(c, modelLabel(i18n.Tf("reader.ai.translated", shortLang(tr.lang), tr.model)), t.Text.Sm).TextColor(t.Muted)
			kit.TextLink(c, i18n.T("reader.ai.show_original"), t.Foreground, t.Text.Sm).OnClick(func() { r.toggleTranslate(m.ID) })
		})
	}
}

// summaryPanel is m's summary while shown: under the sender of a message
// read alone, under the subject of a thread (which summarizes its newest).
func (r *reader) summaryPanel(c *ui.Context, m core.MessageSummary) {
	st := r.ai[m.ID]
	if st == nil {
		return
	}
	if sum := st.summary; sum != nil && r.summaryShown(st) {
		r.panel(c, i18n.T("reader.ai.summary"), sum.model, nil, func() {
			r.output(c, sum)
			r.runState(c, sum, func() { st.summary = r.stream(m.ID, "summarize") })
		})
	}
}

// translationState keeps the original body visible while translating or on failure.
func (r *reader) translationState(c *ui.Context, m core.MessageSummary) {
	st := r.ai[m.ID]
	b := r.bodies[m.ID]
	var shown *aiRun
	if st != nil && r.trShown(st) && st.translation.done && len(st.translation.translations) > 0 {
		shown = st.translation
	}
	if b != nil && b.view != nil && b.translation != shown {
		var texts map[string]string
		if shown != nil {
			texts = shown.translations
		}
		b.translation = shown
		r.p.a.plat.TranslateBody(m.ID, texts)
	}
	if st == nil || !r.trShown(st) {
		return
	}
	tr := st.translation
	if tr.done && strings.TrimSpace(tr.text) == "" { // it is in the reading language already
		kit.Text(c, i18n.Tf("reader.ai.no_translation", shortLang(tr.lang)), theme.Of(c).Text.Sm).TextColor(theme.Of(c).Muted)
		return
	}
	r.runState(c, tr, func() { st.translation = r.stream(m.ID, "translate") })
}

// output renders skills as Markdown and keeps plugin action HTML and text.
func (r *reader) output(c *ui.Context, run *aiRun) {
	if run.done && len(run.doc.Blocks) > 0 {
		richtext.Render(c, run.doc, richtext.RenderOptions{OnLink: r.p.a.openLink})
	} else if run.markdown != nil && run.text != "" {
		run.markdown.Theme = &markdown.Theme{FontSize: theme.Of(c).BodyFontSize, CodeFont: theme.Mono, LineHeight: theme.LineReading}
		if run.running {
			markdown.Stream(c, run.text, run.markdown)
		} else {
			markdown.View(c, run.text, run.markdown)
		}
	} else if run.text != "" {
		ui.Text(c, run.text).LineHeight(theme.LineReading)
	}
}

const chatGPTUsageURL = "https://chatgpt.com/settings/usage"

func isChatGPTUsage(err error) bool {
	var aiErr *ai.Error
	return errors.As(err, &aiErr) && aiErr.Detail == ai.DetailChatGPTUsage
}

// runState shows a spinner while run runs, its error with Retry when it failed.
func (r *reader) runState(c *ui.Context, run *aiRun, retry func()) {
	switch {
	case run.running:
		kit.Spinner(c)
	case run.err != nil:
		kit.Alert(c, kit.Danger, humanize(run.err), func() {
			if isChatGPTUsage(run.err) {
				a := r.p.a
				kit.Button(c, i18n.T("settings.models.manage_usage"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(chatGPTUsageURL) }, a.toastErr) })
			}
			if retry != nil {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(retry)
			}
		})
	}
}

// fetchErr shows a failed read with Retry.
func (r *reader) fetchErr(c *ui.Context, err error, loading bool, retry func()) {
	if msg := humanize(err); msg != "" && !loading {
		kit.Alert(c, kit.Danger, msg, func() {
			if isChatGPTUsage(err) {
				a := r.p.a
				kit.Button(c, i18n.T("settings.models.manage_usage"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { a.do(func(context.Context) error { return a.plat.OpenURL(chatGPTUsageURL) }, a.toastErr) })
			}
			kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(retry)
		})
	}
}

// panel is the AI's box: a sparkle, a title, an optional close button, the body.
func (r *reader) panel(c *ui.Context, title, meta string, close func(), body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2)).Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Xl).Background(t.Sidebar).Border(theme.Hairline, t.Border).Children(func() {
		ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Get("sparkles")).Size(theme.IconSm, theme.IconSm).TextColor(t.Foreground)
			ui.Text(c, title).FontWeight(600).LineHeight(theme.LineCJK).Shrink(1)
			kit.Text(c, meta, t.Text.Sm).TextColor(t.Muted).Grow(1).Shrink(1)
			if close != nil {
				kit.IconButton(c, "x", i18n.T("reader.ai.close_result"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(close)
			}
		})
		body()
	})
}

// cards are the plugins' cards on a message; a failure to read them shows
// with Retry.
func (r *reader) cards(c *ui.Context, m core.MessageSummary) {
	st := r.ai[m.ID]
	if st == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	r.fetchErr(c, st.cards.err, st.cards.loading, func() { r.loadCards(m.ID, true) })
	for _, card := range st.cards.v {
		row := ui.Row(c).Gap(th.Space(4)).AlignItems(ui.Center).Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Xl).Border(theme.Hairline, t.Border)
		kit.Elevate(c, row, theme.ShadowXs)
		row.Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, i18n.Tf("reader.ai.card_from", card.Title, card.Plugin), t.Text.Sm).TextColor(t.Muted)
				if len(card.Fields) == 0 {
					return
				}
				ui.Text(c, card.Fields[0].Value).Shrink(1).Font(theme.Mono).FontSize(t.Text.X2l).LineHeight(t.Text.Line(t.Text.X2l)).LetterSpacing(theme.CardTracking)
				if hint := cardHint(card.Fields[1:]); hint != "" {
					kit.Text(c, hint, t.Text.Sm).TextColor(t.Muted)
				}
			})
			if len(card.Buttons) == 0 {
				return
			}
			ui.Row(c).Wrap().Gap(th.Space(2)).Shrink(0).Children(func() {
				for i, b := range card.Buttons {
					o := kit.ButtonOpts{Variant: kit.Outline} // one height for all; the first stands out by colour
					if i == 0 {
						o = kit.ButtonOpts{Variant: kit.Default}
					}
					key := fmt.Sprintf("%d/%s/%d", m.ID, card.Plugin, i)
					label := b.Label
					if b.Kind == "copy" && r.copiedCard == key { // the click shows on the button itself, stepping back to secondary
						label, o.Icon, o.Variant = i18n.T("codes.copied"), "check", kit.Secondary
					}
					var alts []string // a copy button turns into Copied; size it for both
					if b.Kind == "copy" {
						alts = append(alts, i18n.T("codes.copied"))
					}
					if !kit.SwapButton(c, label, o, alts...).Clicked() {
						continue
					}
					switch b.Kind {
					case "copy":
						c.WriteClipboard(b.Value)
						r.copiedCard = key
						r.p.a.after(1500*time.Millisecond, func() {
							if r.copiedCard == key {
								r.copiedCard = ""
							}
						})
					case "open-url":
						r.p.a.openLink(b.Value)
					case "run-action":
						r.runAction(b.Value, b.Label, m.ID, false)
					}
				}
			})
		})
	}
}

// hasTags reports whether the newest message has plugin tags to show.
func (r *reader) hasTags() bool {
	m, ok := r.last()
	st := r.ai[m.ID]
	return ok && st != nil && len(st.tags.v) > 0
}

// tagChips are the plugins' tags for the newest message, as small buttons
// after the subject: a copy tag shows its text and turns into Copied in
// place; a link tag names what it opens and where, and asks first, as a
// link in the body does.
func (r *reader) tagChips(c *ui.Context) {
	m, ok := r.last()
	st := r.ai[m.ID]
	if !ok || st == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	a := r.p.a
	chip := func(e ui.Element) ui.Element {
		return e.Height(th.Space(6)).PaddingX(th.Space(2)).Radius(t.Radius.Md).FontSize(t.Text.Base).Shrink(0)
	}
	for i, tag := range st.tags.v {
		key := fmt.Sprintf("tag/%d/%s/%d", m.ID, tag.Plugin, i)
		switch tag.Kind {
		case "copy":
			label, o := tag.Text, kit.ButtonOpts{Variant: kit.Secondary, Icon: "copy"}
			if r.copiedCard == key {
				label, o.Icon = i18n.T("codes.copied"), "check"
			}
			e := chip(kit.SwapButton(c, label, o, tag.Text, i18n.T("codes.copied")))
			if r.copiedCard != key {
				e.Font(theme.Mono).FontWeight(500)
			}
			if e.Clicked() {
				c.WriteClipboard(tag.Value)
				r.copiedCard = key
				a.after(1500*time.Millisecond, func() {
					if r.copiedCard == key {
						r.copiedCard = ""
					}
				})
			}
		case "open-url":
			name, link := tag.Label, tag.Value
			if tag.Builtin {
				name = i18n.T("codes.card." + tag.Label)
			}
			e := chip(kit.ButtonFrame(c, kit.ButtonOpts{Variant: kit.Secondary})).Label(name).Children(func() {
				ui.Icon(c, icons.Get("external-link")).Size(theme.Icon, theme.Icon)
				ui.Text(c, name)
				ui.Text(c, tag.Text).TextColor(t.Muted) // where it goes, before it opens
			})
			if e.Clicked() {
				a.openLink(link)
			}
		}
	}
}

// replyBar floats at the reading pane's foot: the newest message's quick
// replies as short labels, each a draft to edit, then a box that hands a
// question about the thread, quoting that message, to Wit.
func (r *reader) replyBar(c *ui.Context) ui.Element {
	t, th := theme.Of(c), c.Theme()
	last, _ := r.last()
	return ui.Column(c).Padding(th.Space(2.5), th.Space(7), th.Space(3)).Material(kit.Bar(t.Background)).
		BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
		r.column(c).Gap(th.Space(2)).Children(func() {
			if st := r.ai[last.ID]; st != nil && r.taskReady("") && r.p.a.settings.QuickReplies {
				r.fetchErr(c, st.quick.err, st.quick.loading, func() { r.loadQuick(last.ID, true) })
				if len(st.quick.v) > 0 {
					ui.Row(c).Wrap().Gap(th.Space(1)).Role(ui.RoleGroup).Label(i18n.T("reader.ai.quick")).Children(func() {
						for _, q := range st.quick.v {
							chip := kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.quickReply(last.ID, q) }}).
								Height(th.Space(6)).PaddingX(th.Space(2)).Radius(t.Radius.Md).Background(t.Hover).Center().Label(q.Preview)
							chip.Children(func() { kit.Text(c, quickLabel(q), t.Text.Sm).SingleLine() })
							kit.TooltipLabelled(c, chip, q.Preview)
						}
					})
				}
			}
			kit.InputGroup(c, kit.InputOpts{Flat: true}).Height(th.Space(10)).Padding(0, th.Space(1), 0, th.Space(3)).Gap(th.Space(1)).Children(func() {
				name, _ := sender(last.From)
				in := kit.Input(c, &r.replyAsk, kit.InputOpts{Bare: true, Placeholder: i18n.Tf("reader.reply_wit", name)}).Grow(1).Shrink(1).Label(i18n.T("reader.ask_wit"))
				if in.Submitted() {
					r.askReply()
				}
				kit.IconButton(c, "arrow-up", i18n.T("reader.ask_wit"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).OnClick(r.askReply)
			})
		})
	})
}

// quickLabel is a quick reply's short label: the model's, else (cached
// before labels were asked for) the start of its text.
func quickLabel(q core.QuickReply) string {
	if q.Label != "" {
		return q.Label
	}
	if p := []rune(q.Preview); len(p) > quickLabelRunes {
		return string(p[:quickLabelRunes]) + "…"
	}
	return q.Preview
}

// quickLabelRunes is as much of a quick reply's text as stands in for its label.
const quickLabelRunes = 12

// askReply hands the reply bar's question to Wit, quoting the newest message.
func (r *reader) askReply() {
	q := strings.TrimSpace(r.replyAsk)
	m, ok := r.last()
	if q == "" || !ok {
		return
	}
	r.replyAsk = ""
	r.p.a.askWit(q, m)
}

// egressDialog asks before an action's plugin sends data out: the plugin
// and its hosts; Allow once runs it approved.
func (r *reader) egressDialog(c *ui.Context) {
	if !r.egressDlg {
		return
	}
	ask := r.egress
	t, th := theme.Of(c), c.Theme()
	opts := kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.Tf("reader.egress.body", ask.plugin), Close: i18n.T("common.close")}
	kit.Dialog(c, &r.egressDlg, i18n.T("reader.egress.title"), opts, func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			for _, h := range ask.hosts {
				kit.Text(c, h, t.Text.Sm).Font(theme.Mono)
			}
		})
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			r.egressDlg = false
		}
		if kit.Button(c, i18n.T("reader.egress.allow"), kit.ButtonOpts{}).Clicked() {
			r.egressDlg = false
			r.runAction(ask.id, ask.title, ask.msgID, true)
		}
	})
}

// cardHint is a card's fields after the first on one line: "label value"
// (the value alone without a label), joined by " · ".
func cardHint(fs []core.CardField) string {
	var out []string
	for _, f := range fs {
		out = append(out, strings.TrimSpace(f.Label+" "+f.Value))
	}
	return strings.Join(out, " · ")
}
