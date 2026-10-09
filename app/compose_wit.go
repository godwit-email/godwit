package app

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"math"
	netmail "net/mail"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/editor"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/godwit-email/godwit/internal/worddiff"
)

// adjustSkills are the 调整 menu's skills, in its order.
var adjustSkills = []string{"tone-formal", "tone-friendly", "shorter", "longer", "proofread"}

// composeWit is Wit on the compose page: the panel beside the body, the
// banner above it, the review of a proposed edit in place of the editor,
// ⌘K over a selection and the undo of the latest edit applied. The body
// is read-only while a run goes on or an edit waits for review. UI thread
// only.
type composeWit struct {
	p     *composer
	panel *witPanel
	jobs  map[string]witJob // by the question they show as; replaced, never changed, as Start reads it off the UI thread

	review   *proposal // the edit under review; nil for none
	building int64     // the approval a review is being made for
	reviewEd *editor.Editor
	active   int  // the change whose actions show; -1 for none
	compare  bool // a rewrite shows the old text beside the new
	applied  bool // the latest edit Wit made is in the body: Undo can take it back
	told     bool // the user was told the body is locked, this time

	cmdk           bool // ⌘K is open over the selection
	cmdkInput      string
	selFrom, selTo richtext.Pos

	skill func(ctx context.Context, draftID int64, skill string, o core.SkillOptions) (<-chan core.WitEvent, error) // tests swap it
}

// witJob is a run a question in the panel stands for: a skill, a ⌘K
// rewrite (in a conversation of its own) or, for free text, Assist.
type witJob struct {
	run       func(ctx context.Context) (<-chan core.WitEvent, error)
	selection bool
}

func newComposeWit(p *composer) *composeWit {
	return &composeWit{p: p, panel: newWitPanel(p.a), active: -1, skill: p.a.svc.AssistSkill}
}

// openConversation shows the draft's conversation with an edit waiting, or
// its Assist conversation.
func (w *composeWit) openConversation() {
	svc, id := w.p.a.svc, w.p.id
	var conv int64
	w.p.a.do(func(ctx context.Context) error {
		cs, err := draftConversations(ctx, svc, id)
		if err != nil {
			return err
		}
		for _, c := range slices.Backward(cs) {
			as, err := svc.ListApprovals(ctx, c.ID)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(as, func(a core.Approval) bool { return isDraftEdit(a.Tool) && a.State == "pending" }) {
				conv = c.ID
				return nil
			}
		}
		if i := slices.IndexFunc(cs, func(c core.Conversation) bool { return c.Title == "" }); i >= 0 {
			conv = cs[i].ID
		}
		return nil
	}, func(err error) {
		if err != nil {
			w.p.a.toastErr(err)
		}
		w.panel.open(conv)
	})
}

// draftConversations are the draft's assist conversations, oldest first.
// The Assist one has no title; RewriteSelection titles its own.
func draftConversations(ctx context.Context, svc *core.Service, draftID int64) ([]core.Conversation, error) {
	cs, err := svc.ListConversations(ctx, "assist")
	cs = slices.DeleteFunc(cs, func(c core.Conversation) bool { return c.DraftID != draftID })
	slices.SortFunc(cs, func(a, b core.Conversation) int { return cmp.Compare(a.ID, b.ID) })
	return cs, err
}

// options are the panel's: every run is flushed first; free text runs Assist.
func (w *composeWit) options() witOptions {
	return witOptions{
		Placeholder: i18n.T("wit.compose.placeholder"),
		Label:       i18n.T("wit.compose.input"),
		Start:       w.start(w.jobs),
		Changed:     w.reload,
		Gate:        w.gate,
		Card:        w.card,
		Tools:       w.tools,
		After:       w.after,
	}
}

// gate saves the draft, then starts the run.
func (w *composeWit) gate(run func()) {
	w.applied = false
	w.p.s.Flush(func(err error) {
		if err != nil {
			if !errors.Is(err, core.ErrStaleDraft) { // the conflict notice tells
				w.p.a.toastErr(err)
			}
			return
		}
		run()
	})
}

// start runs a question's job off the UI thread. Assist and skills run in
// the draft's Assist conversation, followed through a subscription made
// first: a first generation is applied at once and its run resumes.
func (w *composeWit) start(jobs map[string]witJob) func(ctx context.Context, in string) (int64, <-chan core.WitEvent, func(), error) {
	svc, id := w.p.a.svc, w.p.id
	return func(ctx context.Context, in string) (int64, <-chan core.WitEvent, func(), error) {
		job, ok := jobs[in]
		if !ok {
			job.run = func(ctx context.Context) (<-chan core.WitEvent, error) { return svc.Assist(ctx, id, in) }
		}
		if job.selection {
			ch, err := job.run(ctx)
			if err != nil {
				return 0, nil, nil, err
			}
			cs, err := draftConversations(ctx, svc, id)
			if err != nil || len(cs) == 0 {
				go drainWit(ch)
				return 0, nil, nil, cmp.Or(err, errors.New("the rewrite has no conversation"))
			}
			return cs[len(cs)-1].ID, ch, nil, nil // the conversation it just made
		}
		cs, err := draftConversations(ctx, svc, id)
		if err != nil {
			return 0, nil, nil, err
		}
		var conv int64
		if i := slices.IndexFunc(cs, func(c core.Conversation) bool { return c.Title == "" }); i >= 0 {
			conv = cs[i].ID
		} else {
			c, err := svc.NewConversation(ctx, "assist", id)
			if err != nil {
				return 0, nil, nil, err
			}
			conv = c.ID
		}
		sub, stop := svc.SubscribeConversation(conv)
		ch, err := job.run(ctx)
		if err != nil {
			stop()
			return conv, nil, nil, err
		}
		go drainWit(ch) // the subscription shows the same events
		return conv, sub, stop, nil
	}
}

func drainWit(ch <-chan core.WitEvent) {
	for range ch {
	}
}

// ask runs job as the question in.
func (w *composeWit) ask(in string, job witJob) {
	jobs := maps.Clone(w.jobs)
	if jobs == nil {
		jobs = map[string]witJob{}
	}
	jobs[in] = job
	w.jobs = jobs
	w.panel.ask(w.options(), in)
}

func (w *composeWit) runSkill(label, skill, lang string) {
	run, id := w.skill, w.p.id
	w.ask(label, witJob{run: func(ctx context.Context) (<-chan core.WitEvent, error) {
		return run(ctx, id, skill, core.SkillOptions{TargetLanguage: lang})
	}})
}

// rewriteSelection sends ⌘K's instruction for the selection: after the
// flush, the selection in the editable HTML saved.
func (w *composeWit) rewriteSelection() {
	in := strings.TrimSpace(w.cmdkInput)
	if in == "" {
		return
	}
	p, from, to := w.p, w.selFrom, w.selTo
	w.cmdk, w.cmdkInput, w.applied = false, "", false
	p.s.Flush(func(err error) {
		if err != nil {
			if !errors.Is(err, core.ErrStaleDraft) {
				p.a.toastErr(err)
			}
			return
		}
		_, off := richtext.ToHTMLOffsets(p.ed.Doc())
		sel := core.Selection{Revision: p.s.rev, Start: off(from), End: off(to)}
		svc, id := p.a.svc, p.id
		w.ask(in, witJob{selection: true, run: func(ctx context.Context) (<-chan core.WitEvent, error) {
			return svc.RewriteSelection(ctx, id, sel, in)
		}})
	})
}

// pending is the draft edit waiting for the user, if any.
func (w *composeWit) pending() *core.Approval {
	for i, ap := range w.panel.approvals {
		if isDraftEdit(ap.Tool) && ap.State == "pending" {
			return &w.panel.approvals[i]
		}
	}
	return nil
}

// locked reports whether the body is read-only: Wit writes it, or an edit
// waits for review.
func (w *composeWit) locked() bool { return w.panel.running || w.pending() != nil }

func (w *composeWit) lockedInput() {
	if !w.told {
		w.told = true
		w.p.a.toast(i18n.T("wit.compose.locked"))
	}
}

// sync follows the pending edit: its review is made off the UI thread.
func (w *composeWit) sync() {
	if !w.locked() {
		w.told = false
	}
	ap := w.pending()
	if ap == nil {
		w.review, w.reviewEd, w.building, w.compare, w.active = nil, nil, 0, false, -1
		return
	}
	if ap.ID == w.building {
		return
	}
	w.building = ap.ID
	a, cp := w.p.a, *ap
	var r *proposal
	a.do(func(context.Context) error { r = newProposal(cp); return nil }, func(error) {
		if w.building != cp.ID {
			return
		}
		w.review, w.active, w.compare = r, -1, false
		w.reviewEd = editor.New(r.view, a.clock.Now)
	})
}

// decideAll decides every open change of the review; a rewrite as a whole.
func (w *composeWit) decideAll(accept bool) {
	r := w.review
	if !r.inline() {
		w.panel.decide(r.ap, accept, false)
		return
	}
	for i := range r.decided {
		if r.decided[i] == 0 {
			r.decided[i] = map[bool]int{true: 1, false: -1}[accept]
		}
	}
	w.finish()
}

// decideOne decides change i; with none left open, the edit is settled.
func (w *composeWit) decideOne(i int, accept bool) {
	r := w.review
	if w.panel.deciding[r.ap.ID] {
		return
	}
	r.decided[i] = map[bool]int{true: 1, false: -1}[accept]
	w.active = -1
	if r.open() == 0 {
		w.finish()
		return
	}
	r.render()
	w.reviewEd.Replace(r.view)
}

// finish settles the review: all accepted or all rejected decides the
// approval; a mix applies the text built from the decisions, each token in
// its own style.
func (w *composeWit) finish() {
	r := w.review
	all := !slices.Contains(r.decided, -1)
	if all || !slices.Contains(r.decided, 1) {
		w.panel.decide(r.ap, all, false)
		return
	}
	h, id, apply := richtext.ToHTML(r.result()), r.ap.ID, w.p.a.svc.ApplyEditedProposal
	w.panel.settle(r.ap, []int64{id}, func(ctx context.Context) error { return apply(ctx, id, h) })
}

// reload reads the draft after a run or a decision: a revision the saver
// did not write is Wit's edit, which the body then shows.
func (w *composeWit) reload() {
	p := w.p
	svc, id := p.a.svc, p.id
	var pd prepared
	p.a.do(func(ctx context.Context) error {
		d, err := svc.GetDraft(ctx, id)
		pd = prepare(d)
		return err
	}, func(err error) {
		if err != nil || !p.loaded || p.s.busy || p.s.conflict || pd.d.Revision == p.s.rev {
			return // a save in flight that this read saw, or a conflict, is the saver's
		}
		p.s.rev = pd.d.Revision
		w.setBody(pd)
		w.applied = true
	})
}

func (w *composeWit) setBody(pd prepared) {
	p := w.p
	p.ed.Replace(pd.doc)
	p.edVer = p.ed.Version() // Wit's edit is saved
	p.quote, p.sig = pd.quote, pd.sig
}

// undo takes back the latest edit Wit applied.
func (w *composeWit) undo() {
	svc, id := w.p.a.svc, w.p.id
	var pd prepared
	w.p.s.Run(func(ctx context.Context, _ int64) (core.Draft, error) {
		if err := svc.UndoLastAssist(ctx, id); err != nil {
			return core.Draft{}, err
		}
		d, err := svc.GetDraft(ctx, id)
		pd = prepare(d)
		return d, err
	}, func(err error) {
		if err != nil {
			w.p.a.toastErr(err)
			return
		}
		w.applied = false
		w.setBody(pd)
	})
}

// languages are the 翻译 menu's targets: the reading language, then the
// common ones.
func (w *composeWit) languages() []struct{ tag, name string } {
	st := w.p.a.settings
	read := cmp.Or(st.ReadingLanguage, st.UILanguage, w.p.a.sysLang, "en")
	out := []struct{ tag, name string }{{read, langName(read)}}
	for _, l := range languages {
		if !slices.ContainsFunc(out, func(x struct{ tag, name string }) bool { return x.name == l.name }) {
			out = append(out, l)
		}
	}
	return out
}

// view is the panel beside the compose column: what Wit knows of the
// draft, then the conversation.
func (w *composeWit) view(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Column(c).Width(theme.WitPanelWidth).Shrink(0).BorderWidth(0, 0, 0, theme.Hairline).BorderColor(t.Border).Children(func() {
		if chips := w.chips(); len(chips) > 0 {
			ui.Row(c).Wrap().Gap(th.Space(1.5)).Padding(th.Space(3), th.Space(4)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
				for _, s := range chips {
					ui.Text(c, s).FontSize(t.Text.Xs).TextColor(t.Muted).Padding(0, th.Space(2)).Radius(t.Radius.Full).Border(theme.Hairline, t.Border)
				}
			})
		}
		w.panel.View(c, w.options())
	})
}

// chips name the context of an Assist run: the email replied to, the
// attachments, and how much mail with the recipients its last run read.
func (w *composeWit) chips() []string {
	p := w.p
	var out []string
	if p.src != 0 {
		out = append(out, i18n.T("wit.compose.source"))
	}
	for _, att := range p.atts {
		out = append(out, att.Filename)
	}
	var seen []int64
	for _, ct := range w.panel.cites {
		if ct.CallID == "" && ct.MessageID != p.src && !slices.Contains(seen, ct.MessageID) {
			seen = append(seen, ct.MessageID)
		}
	}
	if len(seen) > 0 && len(p.to) > 0 {
		who := p.to[0]
		if a, err := netmail.ParseAddress(who); err == nil {
			who = cmp.Or(a.Name, a.Address)
		}
		out = append(out, i18n.Tf("wit.compose.history", who, len(seen)))
	}
	return out
}

// tools are the 调整 and 翻译 menus above the input.
func (w *composeWit) tools(c *ui.Context) {
	th := c.Theme()
	off := w.locked()
	pill := kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: off}
	ui.Row(c).Gap(th.Space(1.5)).Children(func() {
		kit.Button(c, i18n.T("wit.compose.adjust"), pill).Menu(func(m *ui.Menu) {
			for _, sk := range adjustSkills {
				if label := i18n.T("wit.skill." + sk); m.Item(label).Chosen() && !off {
					w.runSkill(label, sk, "")
				}
			}
		})
		kit.Button(c, i18n.T("wit.compose.translate"), pill).Menu(func(m *ui.Menu) {
			for _, l := range w.languages() {
				if m.Item(l.name).Chosen() && !off {
					w.runSkill(i18n.Tf("wit.compose.translate_to", l.name), "translate-draft", l.tag)
				}
			}
		})
	})
}

// after notes, once the latest edit is in the body, that it is, with Undo.
func (w *composeWit) after(c *ui.Context) {
	if !w.applied || w.locked() {
		return
	}
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Gap(th.Space(2)).FontSize(t.Text.Xs).TextColor(t.Muted).Children(func() {
		ui.Text(c, i18n.T("wit.compose.applied")).Padding(0, th.Space(1.5)).Radius(t.Radius.Xs).Background(t.Hover)
		ui.ButtonBase(c).TextColor(t.Foreground).Children(func() {
			ui.Text(c, i18n.T("wit.compose.undo")).Underline()
		}).OnClick(w.undo)
	})
}

// card is a draft edit's approval in the panel: what it changes, and
// Accept all / Reject all.
func (w *composeWit) card(c *ui.Context, ap core.Approval) bool {
	if !isDraftEdit(ap.Tool) {
		return false
	}
	t, th := theme.Of(c), c.Theme()
	r := w.review
	ui.Column(c).Radius(t.Radius.Lg).Border(theme.Hairline, t.Primary).Background(t.Background).Clip().Children(func() {
		ui.Row(c).Gap(th.Space(2)).Padding(th.Space(2), th.Space(3)).Background(t.Hover).Children(func() {
			ui.Text(c, i18n.T("wit.compose.review")).FontWeight(600)
			ui.Text(c, ap.Tool).Font("Geist Mono").FontSize(t.Text.Xs)
		})
		ui.Box(c).Padding(th.Space(2), th.Space(3)).Children(func() {
			switch {
			case r == nil || r.ap.ID != ap.ID:
				kit.Spinner(c)
			case r.inline():
				ui.Text(c, i18n.Plural("wit.compose.review.inline", len(r.changes)))
			default:
				ui.Text(c, w.rewriteText(true))
			}
		})
		if r != nil && r.ap.ID == ap.ID {
			ui.Row(c).Gap(th.Space(1.5)).Padding(th.Space(2), th.Space(3)).BorderWidth(theme.Hairline, 0, 0, 0).BorderColor(t.Border).Children(func() {
				w.decideButtons(c)
			})
		}
	})
	return true
}

func (w *composeWit) decideButtons(c *ui.Context) {
	busy := w.panel.deciding[w.review.ap.ID]
	kit.Button(c, i18n.T("wit.compose.reject_all"), kit.ButtonOpts{Variant: kit.Outline, Small: true, Disabled: busy}).OnClick(func() { w.decideAll(false) })
	kit.Button(c, i18n.T("wit.compose.accept_all"), kit.ButtonOpts{Small: true, Disabled: busy}).OnClick(func() { w.decideAll(true) })
}

// rewriteText tells how much a rewrite changed, as the banner or (card)
// the review in the panel says it.
func (w *composeWit) rewriteText(card bool) string {
	key := "wit.compose.rewrite"
	if card {
		key = "wit.compose.review.rewrite"
	}
	if !w.review.ok {
		return i18n.T(key + "_plain")
	}
	return i18n.Tf(key, int(math.Round(w.review.ratio*100)))
}

// banner is the bar above the body while Wit writes it or an edit waits.
func (w *composeWit) banner(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	r := w.review
	var text string
	switch {
	case w.panel.running:
		text = i18n.T("wit.compose.writing_banner")
	case r != nil && r.inline():
		text = i18n.Plural("wit.compose.pending", r.open())
	case r != nil:
		text = w.rewriteText(false)
	default:
		return
	}
	ui.Row(c).Gap(th.Space(2)).Padding(th.Space(2), th.Space(5)).Background(t.Hover).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Role(ui.RoleStatus).Children(func() {
		ui.Text(c, text).Grow(1).Shrink(1)
		small := kit.ButtonOpts{Variant: kit.Outline, Small: true}
		switch {
		case w.panel.running:
			kit.Button(c, i18n.T("wit.stop"), small).OnClick(w.panel.stopRun)
		default:
			if !r.inline() {
				label := i18n.T("wit.compose.compare")
				if w.compare {
					label = i18n.T("wit.compose.back")
				}
				kit.Button(c, label, small).OnClick(func() { w.compare = !w.compare })
			}
			w.decideButtons(c)
		}
	})
}

// body draws what takes the editor's place: the review of an edit, or
// while Wit writes an empty body, a placeholder. It reports whether it drew.
func (w *composeWit) body(c *ui.Context) bool {
	t, th := theme.Of(c), c.Theme()
	opts := richtext.RenderOptions{OnLink: w.p.a.openLink}
	switch r := w.review; {
	case r != nil && !r.inline() && w.compare:
		ui.Row(c).Gap(th.Space(5)).AlignItems(ui.Start).Children(func() {
			side := func(title string, d richtext.Doc, border float32) {
				ui.Column(c).Grow(1).Basis(0).Gap(th.Space(2.5)).Padding(0, 0, 0, border*th.Space(5)).BorderWidth(0, 0, 0, border*theme.Hairline).BorderColor(t.Border).Children(func() {
					ui.Text(c, title).FontSize(t.Text.Xs).FontWeight(600)
					richtext.Render(c, d, opts)
				})
			}
			side(i18n.T("wit.compose.original"), r.old, 0)
			side(i18n.T("wit.compose.new"), r.new, 1)
		})
	case r != nil && !r.inline():
		richtext.Render(c, r.new, opts)
	case r != nil && w.reviewEd != nil:
		w.reviewView(c)
	case w.panel.running && mail.Blank(w.p.ed.HTML()):
		ui.Column(c).Gap(th.Space(2.5)).Children(func() {
			ui.Text(c, i18n.T("wit.compose.writing")).TextColor(t.Muted)
			for _, pct := range []float32{90, 75, 50} {
				ui.Box(c).Height(th.Space(3)).MaxWidthPercent(pct).Radius(t.Radius.Sm).Background(t.Hover)
			}
		})
	default:
		return false
	}
	return true
}

// reviewView is the edit word by word: deletions struck, insertions
// marked; pointing at a change, or the caret in it, offers its Accept and
// Reject.
func (w *composeWit) reviewView(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	r := w.review
	var hs []editor.Highlight
	for _, m := range r.marks {
		h := editor.Highlight{From: m.from, To: m.to, Bg: t.InsBg, Fg: t.InsText}
		if m.kind == worddiff.Del {
			h = editor.Highlight{From: m.from, To: m.to, Bg: t.DelBg, Fg: t.DelText, Strike: true}
		}
		hs = append(hs, h)
	}
	ui.Box(c).Children(func() {
		el := w.reviewEd.View(c, editor.Options{Label: i18n.T("wit.compose.review_body"), ReadOnly: true, Highlights: hs,
			OnReadOnlyInput: w.lockedInput}).FillWidth()
		over := false
		if k := w.active; k >= 0 && r.decided[k] == 0 {
			b := w.changeBounds(k)
			pop := ui.Row(c).Absolute().Top(b.Y+b.H+th.Space(1)).Left(b.X).Gap(th.Space(1)).Padding(th.Space(1)).
				Radius(t.Radius.Md).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
				if u := r.linkNote(k); u != "" {
					ui.Text(c, i18n.Tf("wit.compose.link", u)).FontSize(t.Text.Sm).Padding(0, th.Space(1))
				}
				kit.Button(c, i18n.T("wit.compose.accept"), kit.ButtonOpts{Small: true}).OnClick(func() { w.decideOne(k, true) })
				kit.Button(c, i18n.T("wit.compose.reject"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).OnClick(func() { w.decideOne(k, false) })
			})
			over = pop.Hovered() || pop.FocusWithin()
		}
		if over {
			return
		}
		if x, y, in := el.PointerPosition(); in {
			i, _ := w.reviewEd.IndexForPoint(ui.Point{X: x, Y: y})
			if k := r.changeAt(posAt(r.view, i)); k >= 0 {
				w.active = k
			}
		} else if el.Focused() {
			s, _ := w.reviewEd.DocumentSelection()
			if k := r.changeAt(s); k >= 0 {
				w.active = k
			}
		}
	})
}

// changeBounds is the box of change k's first line in the review, in the
// review's terms.
func (w *composeWit) changeBounds(k int) ui.Rect {
	r := w.review
	from, to, ok := r.span(k)
	if !ok {
		return ui.Rect{}
	}
	b, _, _ := w.reviewEd.BoundsForRange(ui.TextInputRange{Start: utf16At(r.view, from), End: utf16At(r.view, to)})
	return b
}

// editor is the body's editor, read-only while locked, with ⌘K's prompt
// below the selection.
func (w *composeWit) editor(c *ui.Context) {
	p := w.p
	t, th := theme.Of(c), c.Theme()
	locked := w.locked()
	o := editor.Options{Label: i18n.T("compose.body"), PasteHTML: p.a.plat.ClipboardHTML, ReadOnly: locked, OnReadOnlyInput: w.lockedInput}
	if !locked {
		o.Placeholder = i18n.T("wit.compose.empty")
	}
	if locked {
		w.cmdk = false
	}
	if from, to := p.ed.DocumentSelection(); !locked && from != to && p.a.settings.DefaultModel != "" && c.Shortcut(ui.Cmd, ui.KeyK) {
		w.cmdk, w.selFrom, w.selTo = true, from, to
	}
	ui.Box(c).Children(func() {
		p.ed.View(c, o).FillWidth()
		if !w.cmdk {
			return
		}
		b, _, _ := p.ed.BoundsForRange(p.ed.Selection().Range)
		box := ui.Column(c).Absolute().Top(b.Y+b.H+th.Space(1.5)).Left(0).Right(0).Gap(th.Space(1.5)).Padding(th.Space(2), th.Space(2.5)).
			Radius(t.Radius.Lg).Border(theme.Hairline, t.Border).Background(t.Background).Children(func() {
			ui.Row(c).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, icons.Get("sparkles")).Size(theme.IconSm, theme.IconSm)
				in := ui.TextInputBase(c, &w.cmdkInput).Grow(1).Label(i18n.T("wit.compose.cmdk.input")).AutoFocus()
				if in.Submitted() {
					w.rewriteSelection()
				}
			})
			ui.Row(c).Gap(th.Space(1.5)).FontSize(t.Text.Xs).TextColor(t.Muted).Children(func() {
				ui.Text(c, i18n.T("wit.compose.cmdk")).Grow(1).Shrink(1)
				kit.Button(c, i18n.T("wit.compose.cmdk.esc"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).OnClick(func() { w.cmdk = false })
				kit.Button(c, i18n.T("wit.compose.cmdk.go"), kit.ButtonOpts{Small: true}).OnClick(w.rewriteSelection)
			})
		})
		if box.Shortcut(0, ui.KeyEscape) {
			w.cmdk = false
		}
	})
}
