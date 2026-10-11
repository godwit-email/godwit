package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/editor"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/richtext"
)

// composeWit is Wit on the compose page: the panel beside the body, the
// banner above it, the review of a proposed edit in place of the editor,
// ⌘K over a selection and the undo of the latest edit applied. The body
// is read-only while a run goes on or an edit waits for review. UI thread
// only.
type composeWit struct {
	p     *composer
	panel *witPanel

	review   *proposal // the edit under review; nil for none
	building int64     // the approval a review is being made for
	reviewEd *editor.Editor
	active   int  // the change whose actions show; -1 for none
	compare  bool // a rewrite shows the old text beside the new
	applied  bool // the latest edit Wit made is in the body: Undo can take it back
	told     bool // the user was told the body is locked, this time

	reloads   uint64 // bumped per read of the draft after Wit: only the newest applies
	syncing   bool   // that read is on its way: the body stays locked
	reloadErr error  // it failed: Retry reads again

	cmdk           bool // ⌘K is open over the selection
	cmdkInput      string
	selFrom, selTo richtext.Pos

	getDraft func(ctx context.Context, id int64) (core.Draft, error) // tests swap it
}

// witJob is the run a question in the panel stands for, when it is not
// Assist on free text: a skill, or a ⌘K rewrite in a conversation of its own.
type witJob struct {
	run       func(ctx context.Context) (<-chan core.WitEvent, error)
	selection bool
}

func newComposeWit(p *composer) *composeWit {
	w := &composeWit{p: p, panel: newWitPanel(p.a), active: -1, getDraft: p.a.svc.GetDraft}
	w.panel.resolve = func(ctx context.Context, id int64, accept bool) error {
		return p.call(func() error { return p.ops.resolve(ctx, id, accept) })
	}
	w.panel.resolveAll = func(ctx context.Context, runID int64, accept bool) error {
		return p.call(func() error { return p.ops.resolveAll(ctx, runID, accept) })
	}
	return w
}

// openConversation shows the draft's conversation with an edit waiting, or
// its Assist conversation.
func (w *composeWit) openConversation() {
	svc, id, tok := w.p.a.svc, w.p.id, w.panel.follows
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
		if w.panel.follows != tok {
			return // asked meanwhile: the panel shows that run, not a conversation read before it
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

// options are the panel's: every run is flushed first; free text runs
// Assist, job (pinned to its turn, so Retry runs it again) anything else.
func (w *composeWit) options(job *witJob) witOptions {
	return witOptions{
		Placeholder: i18n.T("wit.compose.placeholder"),
		Label:       i18n.T("wit.compose.input"),
		Start:       w.start(job),
		Pinned:      job != nil,
		Changed:     w.reload,
		Gate:        w.gate,
		Hold:        w.p.held,
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
func (w *composeWit) start(pinned *witJob) func(ctx context.Context, in string) (int64, <-chan core.WitEvent, func(), error) {
	svc, p, id := w.p.a.svc, w.p, w.p.id
	var job witJob
	if pinned != nil {
		job = *pinned
	}
	return func(ctx context.Context, in string) (int64, <-chan core.WitEvent, func(), error) {
		job := job
		if job.run == nil {
			job.run = func(ctx context.Context) (<-chan core.WitEvent, error) { return p.ops.assist(ctx, id, in) }
		}
		run := job.run
		job.run = func(ctx context.Context) (ch <-chan core.WitEvent, err error) {
			err = p.call(func() (err error) { ch, err = run(ctx); return err })
			return ch, err
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

// ask runs job, shown as the question in.
func (w *composeWit) ask(in string, job witJob) { w.panel.ask(w.options(&job), in) }

func (w *composeWit) runSkill(label, skill, lang string) {
	run, id := w.p.ops.skill, w.p.id
	w.ask(label, witJob{run: func(ctx context.Context) (<-chan core.WitEvent, error) {
		return run(ctx, id, skill, core.SkillOptions{TargetLanguage: lang})
	}})
}

// rewriteSelection sends ⌘K's instruction for the selection: after the
// flush, the selection in the editable HTML saved.
func (w *composeWit) rewriteSelection() {
	if w.p.held() {
		return
	}
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
		rewrite, id := p.ops.rewrite, p.id
		w.ask(in, witJob{selection: true, run: func(ctx context.Context) (<-chan core.WitEvent, error) {
			return rewrite(ctx, id, sel, in)
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

// locked reports whether the body is read-only: Wit writes it, an edit
// waits for review, the draft Wit left is being read, or it is being sent.
func (w *composeWit) locked() bool {
	return w.panel.running || w.pending() != nil || w.syncing || w.p.frozen.Load()
}

func (w *composeWit) lockedInput() {
	if !w.told {
		w.told = true
		if w.p.frozen.Load() {
			w.p.a.toast(i18n.T("compose.sending.locked"))
		} else {
			w.p.a.toast(i18n.T("wit.compose.locked"))
		}
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
	if w.p.held() {
		return
	}
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
	if w.p.held() {
		return
	}
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
	h, id, p, apply := richtext.ToHTML(r.result()), r.ap.ID, w.p, w.p.ops.apply
	w.panel.settle(r.ap, []int64{id}, func(ctx context.Context) error { return p.call(func() error { return apply(ctx, id, h) }) })
}

// reload reads the draft after a run or a decision, the body locked
// meanwhile: a newer revision the saver did not write is Wit's edit, which
// the body then shows. Only the newest read applies, and only to the body
// it was asked for: a local edit since stays, for the saver to reconcile.
func (w *composeWit) reload() {
	p := w.p
	w.reloads++
	n, ver, get, id := w.reloads, p.ed.Version(), w.getDraft, p.id
	w.syncing, w.reloadErr = true, nil
	var pd prepared
	p.a.do(func(ctx context.Context) error {
		d, err := get(ctx, id)
		pd = prepare(d)
		return err
	}, func(err error) {
		if n != w.reloads {
			return // a newer read decides
		}
		w.syncing = false
		if err != nil {
			w.reloadErr = err
			return
		}
		if !p.loaded || p.s.busy || p.s.conflict || pd.d.Revision <= p.s.rev || p.ed.Version() != ver {
			return // a save in flight that this read saw, a conflict or a local edit is the saver's
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
	if w.p.held() {
		return
	}
	svc, p, id := w.p.a.svc, w.p, w.p.id
	var pd prepared
	w.p.s.Run(func(ctx context.Context, _ int64) (core.Draft, error) {
		if err := p.call(func() error { return p.ops.undo(ctx, id) }); err != nil {
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
