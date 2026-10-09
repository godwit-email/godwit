package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// The AI of the reader acts on the newest message shown (the one replies
// answer): its summary, translation, skill and action results, plugin cards
// and quick replies. ponytail: r.ai keeps every message read this session;
// evict the oldest if memory ever matters.

// aiRun is a skill's or action's output, streamed in on the UI thread.
type aiRun struct {
	text          string
	doc           richtext.Doc // the output converted, once done
	err           error
	running, done bool
}

// msgAI is the AI state of one message.
type msgAI struct {
	summary, translation *aiRun
	showTr               bool // the body shows the translation
	result               *aiResult
	cards                []core.Card
	cardsDone            bool
	cardsGen             uint64 // the reader gen that asked for the cards
	quick                []core.QuickReply
	quickDone            bool
	quickGen             uint64
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

// aiReady reports whether a default model is set.
func (r *reader) aiReady() bool { return r.p.a.settings.DefaultModel != "" }

// modelName is the default model without its service.
func (r *reader) modelName() string {
	dm := r.p.a.settings.DefaultModel
	_, m, ok := strings.Cut(dm, "/")
	if !ok {
		return dm
	}
	return m
}

// aiContext is the context of the AI work for the key shown.
func (r *reader) aiContext() context.Context {
	if r.aiCtx == nil {
		r.aiCtx, r.aiStop = context.WithCancel(r.p.a.ctx)
	}
	return r.aiCtx
}

// stopAI cancels the AI work of the key shown and forgets unfinished runs,
// so coming back starts them again.
func (r *reader) stopAI() {
	if r.aiStop != nil {
		r.aiStop()
	}
	r.aiCtx, r.aiStop = nil, nil
	for _, s := range r.ai {
		if s.summary != nil && s.summary.running {
			s.summary = nil
		}
		if s.translation != nil && s.translation.running {
			s.translation, s.showTr = nil, false
		}
		if s.result != nil && s.result.run.running {
			s.result = nil
		}
	}
	r.egressDlg = false
}

// startAI starts what the newest message shows by itself: its summary,
// cards and quick replies, and loads the skill menu. Each runs once.
func (r *reader) startAI() {
	m, ok := r.last()
	if !ok {
		return
	}
	a, gen, ctx := r.p.a, r.gen, r.aiContext()
	st := r.state(m.ID)
	if r.aiReady() && a.settings.Summary && st.summary == nil {
		st.summary = r.stream(m.ID, "summarize")
	}
	if !st.cardsDone && st.cardsGen != gen {
		st.cardsGen = gen
		var cards []core.Card
		a.do(func(context.Context) (err error) {
			cards, err = a.svc.PluginCards(ctx, m.ID)
			return err
		}, func(err error) {
			if gen == r.gen {
				st.cards, st.cardsDone = cards, true // a failure shows no cards
			}
		})
	}
	if r.aiReady() && a.settings.QuickReplies && !st.quickDone && st.quickGen != gen {
		st.quickGen = gen
		var qs []core.QuickReply
		a.do(func(context.Context) (err error) {
			qs, err = a.svc.QuickReplies(ctx, m.ID)
			return err
		}, func(err error) {
			if gen == r.gen {
				st.quick, st.quickDone = qs, true // a failure shows none: they are a convenience
			}
		})
	}
	if r.menuGen != gen {
		r.menuGen = gen
		var sks []core.SkillInfo
		var acts []core.ActionInfo
		a.do(func(context.Context) (err error) {
			if sks, err = a.svc.ListSkills(ctx); err != nil {
				return err
			}
			acts, err = a.svc.ListActions(ctx, "message")
			return err
		}, func(err error) {
			if gen != r.gen || err != nil {
				return
			}
			r.skills = slices.DeleteFunc(sks, func(s core.SkillInfo) bool {
				return !s.Enabled || !slices.Contains(s.On, "message") || s.Name == "summarize" || s.Name == "translate"
			})
			r.actions = acts
		})
	}
}

// stream runs skill on a message; its text streams into the run returned.
func (r *reader) stream(id int64, skill string) *aiRun {
	a, ctx, start := r.p.a, r.aiContext(), r.runSkill
	run := &aiRun{running: true}
	var doc richtext.Doc
	a.do(func(context.Context) error {
		ch, err := start(ctx, skill, core.Target{Kind: "message", ID: id})
		if err != nil {
			return err
		}
		var all strings.Builder
		for ev := range ch {
			switch ev.Kind {
			case "text":
				all.WriteString(ev.Text)
				s := ev.Text
				a.onUI(func() { run.text += s })
			case "error":
				if ev.Err != nil {
					return ev.Err
				}
				return errors.New(skill + " failed")
			}
		}
		doc = richtext.FromMarkdown(all.String())
		return ctx.Err()
	}, func(err error) {
		run.running, run.err, run.done, run.doc = false, err, err == nil, doc
	})
	return run
}

// translate shows the newest message translated, or its original again.
func (r *reader) translate() {
	m, ok := r.last()
	if !ok {
		return
	}
	st := r.state(m.ID)
	if st.showTr = !st.showTr; st.showTr && (st.translation == nil || st.translation.err != nil) {
		st.translation = r.stream(m.ID, "translate")
	}
}

// runSkillCard runs a menu skill into the result card.
func (r *reader) runSkillCard(name string) {
	if m, ok := r.last(); ok {
		r.state(m.ID).result = &aiResult{title: name, run: r.stream(m.ID, name)}
	}
}

// runAction runs a plugin action on a message. A plugin that may send data
// out asks first; its result shows in the result card, a draft opens.
func (r *reader) runAction(id, title string, msgID int64, approve bool) {
	a, gen, ctx := r.p.a, r.gen, r.aiContext()
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
		if gen != r.gen || st.result != res {
			return
		}
		var ask *core.EgressApprovalRequired
		switch {
		case errors.As(err, &ask):
			st.result = nil
			r.egress, r.egressDlg = egressAsk{id: id, title: title, msgID: msgID, plugin: ask.Plugin, hosts: ask.Hosts}, true
		case err != nil:
			run.err = err
		case out.DraftID != 0:
			st.result = nil
			a.navigate(Route{Page: PageCompose, DraftID: out.DraftID})
		case out.Text == "" && out.HTML == "":
			st.result = nil
		default:
			run.text, run.doc, run.done = out.Text, doc, true
		}
	})
}

// quickReply opens a reply draft starting with r in the composer.
func (r *reader) quickReply(msgID int64, q core.QuickReply) {
	a, gen := r.p.a, r.gen
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
		if gen == r.gen {
			a.navigate(Route{Page: PageCompose, DraftID: id})
		}
	})
}

// aiTools are the toolbar's Translate and skill menu.
func (r *reader) aiTools(c *ui.Context) {
	out := kit.ButtonOpts{Variant: kit.Outline, Small: true}
	if r.aiReady() {
		o := out
		if m, ok := r.last(); ok && r.state(m.ID).showTr {
			o.Variant = kit.Primary
		}
		kit.Button(c, i18n.T("reader.ai.translate"), o).OnClick(r.translate)
	}
	skills := r.skills
	if !r.aiReady() {
		skills = nil
	}
	if len(skills) == 0 && len(r.actions) == 0 {
		return
	}
	m, _ := r.last()
	kit.Button(c, i18n.T("reader.ai.more_skills"), out).Menu(func(menu *ui.Menu) {
		for _, sk := range skills {
			if menu.Item(sk.Name).Chosen() {
				r.runSkillCard(sk.Name)
			}
		}
		if len(skills) > 0 && len(r.actions) > 0 {
			menu.Separator()
		}
		for _, act := range r.actions {
			label := cmp.Or(act.Label.For(i18n.Language()), act.ID)
			if menu.Item(label).Chosen() {
				r.runAction(act.ID, label, m.ID, false)
			}
		}
	})
}

// aiArea shows above the newest message's body: its summary, the menu's
// result and the translation's notice.
func (r *reader) aiArea(c *ui.Context, m core.MessageSummary) {
	last, _ := r.last()
	st := r.ai[m.ID]
	if m.ID != last.ID || st == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	if st.summary != nil {
		r.panel(c, i18n.Tf("reader.ai.summary", r.modelName()), nil, func() {
			ui.Column(c).Gap(th.Space(1)).Children(func() {
				for _, line := range points(st.summary.text) {
					ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Start).Children(func() {
						ui.Text(c, "•").TextColor(t.Muted)
						ui.Text(c, line).Grow(1).Shrink(1)
					})
				}
			})
			r.runState(c, st.summary, func() { st.summary = r.stream(m.ID, "summarize") })
		})
	}
	if res := st.result; res != nil {
		r.panel(c, res.title, func() { st.result = nil }, func() {
			r.output(c, res.run)
			r.runState(c, res.run, nil)
		})
	}
	if st.showTr {
		ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
			lang := cmp.Or(r.p.a.settings.ReadingLanguage, r.p.a.settings.UILanguage, r.p.a.sysLang, "en")
			ui.Text(c, i18n.Tf("reader.ai.translated", langName(lang), r.modelName())).FontSize(t.Text.Sm).TextColor(t.Muted)
			kit.Button(c, i18n.T("reader.ai.show_original"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).OnClick(r.translate)
		})
	}
}

// translated draws the translation in place of the body; false when the
// original shows.
func (r *reader) translated(c *ui.Context, m core.MessageSummary) bool {
	st := r.ai[m.ID]
	if st == nil || !st.showTr || st.translation == nil {
		return false
	}
	tr := st.translation
	if tr.done && strings.TrimSpace(tr.text) == "" { // it is in the reading language already
		lang := cmp.Or(r.p.a.settings.ReadingLanguage, r.p.a.settings.UILanguage, r.p.a.sysLang, "en")
		ui.Text(c, i18n.Tf("reader.ai.no_translation", langName(lang))).FontSize(theme.Of(c).Text.Sm).TextColor(theme.Of(c).Muted)
		return false
	}
	r.output(c, tr)
	r.runState(c, tr, func() { st.translation = r.stream(m.ID, "translate") })
	return true
}

// output is a run's text: as plain text while it streams, converted once done.
func (r *reader) output(c *ui.Context, run *aiRun) {
	if run.done && len(run.doc.Blocks) > 0 {
		richtext.Render(c, run.doc, richtext.RenderOptions{OnLink: r.p.a.openLink})
	} else if run.text != "" {
		ui.Text(c, run.text)
	}
}

// runState shows a spinner while run runs, its error with Retry when it failed.
func (r *reader) runState(c *ui.Context, run *aiRun, retry func()) {
	switch {
	case run.running:
		kit.Spinner(c)
	case run.err != nil:
		kit.Alert(c, kit.Danger, humanize(run.err), func() {
			if retry != nil {
				kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).OnClick(retry)
			}
		})
	}
}

// panel is the AI's box: a sparkle, a title, an optional close button, the body.
func (r *reader) panel(c *ui.Context, title string, close func(), body func()) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2)).Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Lg).Background(t.Sidebar).Border(theme.Hairline, t.Border).Children(func() {
		ui.Row(c).Gap(th.Space(2)).Children(func() {
			ui.Text(c, title).FontWeight(600).Grow(1).Shrink(1)
			if close != nil {
				kit.IconButton(c, "x", i18n.T("reader.ai.close_result"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).OnClick(close)
			}
		})
		body()
	})
}

// points are a summary's lines without their list markers.
func points(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		for _, p := range []string{"- ", "* ", "• "} {
			l = strings.TrimPrefix(l, p)
		}
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// cards are the plugins' cards on the newest message.
func (r *reader) cards(c *ui.Context, m core.MessageSummary) {
	last, _ := r.last()
	st := r.ai[m.ID]
	if m.ID != last.ID || st == nil {
		return
	}
	t, th := theme.Of(c), c.Theme()
	for _, card := range st.cards {
		ui.Column(c).Gap(th.Space(2)).Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Children(func() {
			ui.Text(c, i18n.Tf("reader.ai.card_from", card.Title, card.Plugin)).FontSize(t.Text.Sm).TextColor(t.Muted)
			for _, f := range card.Fields {
				ui.Row(c).Gap(th.Space(2)).Children(func() {
					ui.Text(c, f.Label).TextColor(t.Muted).Shrink(0)
					ui.Text(c, f.Value).Grow(1).Shrink(1)
				})
			}
			if len(card.Buttons) == 0 {
				return
			}
			ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
				for i, b := range card.Buttons {
					o := kit.ButtonOpts{Variant: kit.Outline, Small: true}
					if i == 0 {
						o.Variant = kit.Primary
					}
					if !kit.Button(c, b.Label, o).Clicked() {
						continue
					}
					switch b.Kind {
					case "copy":
						c.WriteClipboard(b.Value)
						r.p.a.toast(i18n.T("reader.ai.copied"))
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

// quickReplies are up to three one-line replies under the newest message;
// Tab moves between them, a click or Enter opens one in the composer.
func (r *reader) quickReplies(c *ui.Context, m core.MessageSummary) {
	last, _ := r.last()
	st := r.ai[m.ID]
	if m.ID != last.ID || st == nil || len(st.quick) == 0 || !r.aiReady() || !r.p.a.settings.QuickReplies {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Gap(th.Space(2)).Margin(th.Space(2), 0, 0, 0).Padding(th.Space(4), 0, 0, 0).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, i18n.T("reader.ai.quick")).FontSize(t.Text.Sm).TextColor(t.Muted)
		for i, q := range st.quick {
			ui.ButtonBase(c).Label(q.Preview).Gap(th.Space(2.5)).Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).Border(theme.Hairline, t.Border).Justify(ui.Start).Children(func() {
				ui.Text(c, q.Preview).SingleLine().Grow(1).Shrink(1)
				if i == 0 {
					kit.Kbd(c, "Tab")
				}
			}).OnClick(func() { r.quickReply(m.ID, q) })
		}
	})
}

// egressDialog asks before an action's plugin sends data out: the plugin
// and its hosts; Allow once runs it approved.
func (r *reader) egressDialog(c *ui.Context) {
	if !r.egressDlg {
		return
	}
	ask := r.egress
	t, th := theme.Of(c), c.Theme()
	kit.Dialog(c, &r.egressDlg, i18n.T("reader.egress.title"), func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			ui.Text(c, i18n.Tf("reader.egress.body", ask.plugin))
			for _, h := range ask.hosts {
				ui.Text(c, h).Font("Geist Mono").FontSize(t.Text.Sm)
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
