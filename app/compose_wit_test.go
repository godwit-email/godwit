package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/richtext"
)

// witCompose opens a draft whose editable part is editable, on an App with a model.
func witCompose(t *testing.T, editable string) (*harness, *composer, *coretest.LLM) {
	t.Helper()
	h, acc, llm := aiHarness(t, true, noAuto)
	ctx := context.Background()
	d, err := h.env.Svc.NewDraft(ctx, acc.ID, "new", 0)
	if err != nil {
		t.Fatal(err)
	}
	if editable != "" {
		_, q, sig := mail.SplitDraft(d.HTML)
		d.HTML = mail.JoinDraft(editable, q, sig)
		if d, err = h.env.Svc.SaveDraft(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	h.app.navigate(Route{Page: PageCompose, DraftID: d.ID})
	p := h.composerLoaded()
	h.see("告诉 Wit 你想怎么回")
	return h, p, llm
}

// tellWit types s into the compose page's Wit input and presses Enter.
func (h *harness) tellWit(s string) {
	h.t.Helper()
	h.click("告诉 Wit 你想怎么回")
	h.typeText(s)
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	h.tt.Frame()
}

func (h *harness) editable(id int64) string {
	h.t.Helper()
	e, _, _ := mail.SplitDraft(h.draft(id).HTML)
	return e
}

func proposeTurn(tool, html string) coretest.Turn {
	b, _ := json.Marshal(map[string]string{"html": html})
	return coretest.Turn{Calls: []coretest.Call{{Name: tool, Args: string(b)}}}
}

// pointAt moves the pointer over change i of the proposal under review.
func (h *harness) pointAt(p *composer, i int) {
	h.t.Helper()
	h.until("the review", func() bool { return p.wit.review != nil && p.wit.reviewEd != nil })
	r, ok := h.tt.Find("正文修订")
	if !ok {
		h.t.Fatalf("no review; texts %q", h.tt.Texts())
	}
	b := p.wit.changeBounds(i)
	h.tt.Move(r.X+b.X+b.W/2, r.Y+b.Y+b.H/2)
	h.drain()
	h.tt.Frame()
	h.tt.Frame()
}

func TestFirstGenerationApplies(t *testing.T) {
	h, p, llm := witCompose(t, "")
	llm.Script(proposeTurn("update_draft", "<p>Thanks, Lena!</p>"), coretest.Turn{Text: []string{"已写好并放进正文。"}})
	h.tellWit("写一封感谢信")
	h.see("已写好并放进正文。")
	h.until("the body", func() bool { return strings.Contains(p.ed.HTML(), "Thanks, Lena!") })
	h.see("已应用到正文")
	if h.tt.HasText("全部接受") || p.wit.review != nil {
		t.Fatal("a first generation waits for review")
	}
	if !strings.Contains(h.editable(p.id), "Thanks, Lena!") {
		t.Fatalf("stored %q", h.editable(p.id))
	}
	h.click("撤销")
	h.until("undone", func() bool { return mail.Blank(h.editable(p.id)) && mail.Blank(p.ed.HTML()) })
	if h.tt.HasText("已应用到正文") || p.s.conflict {
		t.Fatalf("after undo: texts %q, conflict %v", h.tt.Texts(), p.s.conflict)
	}
}

func TestEditorLockedDuringRun(t *testing.T) {
	h, p, llm := witCompose(t, "<p>Hello</p>")
	llm.Script(coretest.Turn{Text: []string{"正在想"}, Hang: true})
	h.tellWit("改一下")
	h.see("助手正在写入正文，暂时不能编辑")
	before := p.ed.HTML()
	h.writeBody(" there")
	if got := p.ed.HTML(); got != before {
		t.Fatalf("typed into a locked body: %q", got)
	}
	h.see("先接受或拒绝助手的修改")
	h.click("停止")
	h.until("stopped", func() bool {
		return !p.wit.panel.running && !h.tt.HasText("助手正在写入正文，暂时不能编辑")
	})
	h.writeBody(" there")
	h.until("typed", func() bool { return strings.Contains(p.ed.HTML(), "Hello there") })
}

const inlineOld = `<p>Hello <b>Lena</b>, see <a href="https://cal.example/x">the calendar</a> for Monday.</p><p>Best regards</p>`
const inlineNew = `<p>Hello <b>Lena</b>, see <a href="https://cal.example/x">the calendar</a> for Tuesday.</p><p>Warm regards</p>`

func TestInlineProposalAcceptAll(t *testing.T) {
	h, p, llm := witCompose(t, inlineOld)
	llm.Script(proposeTurn("update_draft", inlineNew), coretest.Turn{Text: []string{"改了 2 处。"}})
	h.tellWit("改一下日期和落款")
	h.see("2 处修改待确认")
	if !p.wit.locked() || !p.wit.review.inline() {
		t.Fatal("the body is open, or the review is not inline")
	}
	h.click("全部接受")
	h.until("accepted", func() bool {
		e := h.editable(p.id)
		return strings.Contains(e, "Tuesday") && strings.Contains(e, "Warm regards") && strings.Contains(p.ed.HTML(), "Tuesday") && p.wit.review == nil
	})
	h.see("已应用到正文")
	if h.tt.HasText("2 处修改待确认") || p.wit.locked() {
		t.Fatal("still under review")
	}
}

func TestInlineProposalPartial(t *testing.T) {
	h, p, llm := witCompose(t, inlineOld)
	llm.Script(proposeTurn("update_draft", inlineNew), coretest.Turn{Text: []string{"改了 2 处。"}})
	h.tellWit("改一下日期和落款")
	h.see("2 处修改待确认")
	h.pointAt(p, 0)
	h.click("接受")
	h.see("1 处修改待确认")
	h.pointAt(p, 1)
	h.click("拒绝")
	h.until("applied", func() bool { return p.wit.review == nil && strings.Contains(p.ed.HTML(), "Tuesday") })
	e := h.editable(p.id)
	for _, want := range []string{"Tuesday", "Best regards", "<b>Lena</b>", `<a href="https://cal.example/x">the calendar</a>`} {
		if !strings.Contains(e, want) {
			t.Fatalf("stored %q, want %q in it", e, want)
		}
	}
	if strings.Contains(e, "Warm") || strings.Contains(e, "Monday") {
		t.Fatalf("stored %q", e)
	}
}

func TestLinkOnlyChangeShown(t *testing.T) {
	h, p, llm := witCompose(t, `<p>Read <a href="https://a.example/x">the doc</a> today.</p>`)
	llm.Script(proposeTurn("update_draft", `<p>Read <a href="https://b.example/y">the doc</a> today.</p>`), coretest.Turn{Text: []string{"换了链接。"}})
	h.tellWit("换成新链接")
	h.see("1 处修改待确认")
	if got := p.wit.review.view.PlainText(); !strings.Contains(got, "https://b.example/y") {
		t.Fatalf("the review shows %q, not the new address", got)
	}
	h.pointAt(p, 0)
	h.see("链接改为 https://b.example/y")
}

func TestRewriteProposalCompare(t *testing.T) {
	h, p, llm := witCompose(t, "<p>One two three four.</p>")
	llm.Script(proposeTurn("update_draft", "<p>Completely different words appear here.</p>"), coretest.Turn{Text: []string{"整封重写了。"}})
	h.tellWit("整封重写")
	h.until("the review", func() bool { return p.wit.review != nil })
	r := p.wit.review
	if r.inline() {
		t.Fatalf("ratio %v shown inline", r.ratio)
	}
	h.see(fmt.Sprintf("已整体重写，改动约 %d%%", int(math.Round(r.ratio*100))))
	h.click("对比原文")
	h.see("原文")
	h.see("新版本")
	h.see("返回新版本")
	h.click("全部拒绝")
	h.until("rejected", func() bool { return p.wit.review == nil && !p.wit.locked() })
	if e := h.editable(p.id); e != "<p>One two three four.</p>" {
		t.Fatalf("stored %q", e)
	}
}

func TestCmdKRewriteSelection(t *testing.T) {
	h, p, llm := witCompose(t, "<p>First para.</p><p>Second para.</p>")
	llm.Script(proposeTurn("update_selection", "Second paragraph, rewritten."), coretest.Turn{Text: []string{"改好了。"}})
	h.click("正文")
	h.tt.Key(ui.Cmd, ui.KeyDown)
	h.tt.Key(ui.Shift, ui.KeyHome)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.drain()
	h.tt.Frame()
	h.see("⌘K · 只修改选中的这一段")
	h.click("修改选中的内容")
	h.typeText("更具体一点")
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	h.tt.Frame()
	h.see("1 处修改待确认")
	r := p.wit.review
	for _, ch := range r.changes {
		if b := r.blocks[ch.block]; b.Old != 1 && b.New != 1 {
			t.Fatalf("a change outside the second paragraph: %+v", b)
		}
	}
	h.click("全部接受")
	h.until("accepted", func() bool {
		return h.editable(p.id) == "<p>First para.</p><p>Second paragraph, rewritten.</p>"
	})
}

func TestAdjustMenuRunsSkill(t *testing.T) {
	h, p, llm := witCompose(t, "<p>Hello there, this is a long note about many things.</p>")
	var mu sync.Mutex
	var skills []string
	run := p.ops.skill
	p.ops.skill = func(ctx context.Context, id int64, skill string, o core.SkillOptions) (<-chan core.WitEvent, error) {
		mu.Lock()
		skills = append(skills, skill)
		mu.Unlock()
		return run(ctx, id, skill, o)
	}
	llm.Script(coretest.Turn{Text: []string{"<p>Hello, a short note.</p>"}})
	h.click("调整 ▾")
	if err := h.tt.ChooseMenuItem("更短"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.until("the proposal", func() bool { return p.wit.review != nil })
	mu.Lock()
	defer mu.Unlock()
	if len(skills) != 1 || skills[0] != "shorter" {
		t.Fatalf("skills %q", skills)
	}
}

func TestAssistErrorRetry(t *testing.T) {
	h, p, llm := witCompose(t, "<p>Hello</p>")
	failed := coretest.Turn{Status: 500}
	llm.Script(failed, failed, failed) // the model client retries a server error twice
	h.tellWit("改一下")
	h.see("重试")
	h.until("the draft read again", func() bool { return !p.wit.syncing })
	if n := llm.Requests(); n != 3 {
		t.Fatalf("%d requests for a failed run", n)
	}
	if e := h.editable(p.id); e != "<p>Hello</p>" || p.ed.HTML() != "<p>Hello</p>" || p.wit.locked() {
		t.Fatalf("after an error: stored %q, shown %q, locked %v", e, p.ed.HTML(), p.wit.locked())
	}
	llm.Script(coretest.Turn{Text: []string{"这次好了。"}})
	h.click("重试")
	h.see("这次好了。")
}

// gateDrafts makes the composer's draft reads wait: each read sends its
// reply channel on the returned one; the test answers it.
func gateDrafts(w *composeWit) chan chan func() (core.Draft, error) {
	reads := make(chan chan func() (core.Draft, error), 8)
	w.getDraft = func(ctx context.Context, id int64) (core.Draft, error) {
		reply := make(chan func() (core.Draft, error))
		reads <- reply
		return (<-reply)()
	}
	return reads
}

// saveEditable stores editable as the draft's editable part, as Wit would.
func (h *harness) saveEditable(id int64, editable string) core.Draft {
	h.t.Helper()
	d := h.draft(id)
	_, q, sig := mail.SplitDraft(d.HTML)
	d.HTML = mail.JoinDraft(editable, q, sig)
	d, err := h.env.Svc.SaveDraft(context.Background(), d)
	if err != nil {
		h.t.Fatal(err)
	}
	return d
}

func TestReloadKeepsLocalEdit(t *testing.T) {
	h, p, llm := witCompose(t, "<p>Hello</p>")
	reads := gateDrafts(p.wit)
	llm.Script(coretest.Turn{Text: []string{"好的。"}})
	h.tellWit("看看")
	h.until("the run over and its reads asked", func() bool {
		return !p.wit.panel.running && p.wit.reloads > 0 && len(reads) == int(p.wit.reloads)
	})
	var reply chan func() (core.Draft, error)
	for len(reads) > 0 { // the reads before the last are dropped
		if reply != nil {
			reply <- func() (core.Draft, error) { return h.env.Svc.GetDraft(context.Background(), p.id) }
		}
		reply = <-reads
	}
	h.drain()
	h.tt.Frame()
	if !p.wit.locked() {
		t.Fatal("the body opens before the draft is read again")
	}
	before := p.ed.HTML()
	h.writeBody(" there")
	if p.ed.HTML() != before {
		t.Fatalf("typed while the draft is read again: %q", p.ed.HTML())
	}
	p.ed.Replace(p.ed.Doc()) // an edit that got in anyway
	h.saveEditable(p.id, "<p>From elsewhere</p>")
	reply <- func() (core.Draft, error) { return h.env.Svc.GetDraft(context.Background(), p.id) }
	h.until("the read done", func() bool { return !p.wit.locked() })
	if got := p.ed.HTML(); got != before {
		t.Fatalf("the read replaced a local edit: %q", got)
	}
}

func TestReloadNewestWins(t *testing.T) {
	h, p, _ := witCompose(t, "<p>Hello</p>")
	reads := gateDrafts(p.wit)
	older := h.saveEditable(p.id, "<p>Older</p>")
	newer := h.saveEditable(p.id, "<p>Newer</p>")
	p.wit.reload()
	first := <-reads
	p.wit.reload()
	second := <-reads
	second <- func() (core.Draft, error) { return newer, nil }
	h.until("the newer read", func() bool { return p.ed.HTML() == "<p>Newer</p>" })
	first <- func() (core.Draft, error) { return older, nil }
	h.queued("the older read", 1)
	h.drain()
	h.tt.Frame()
	if got := p.ed.HTML(); got != "<p>Newer</p>" || p.s.rev != newer.Revision {
		t.Fatalf("after the older read: %q at revision %d", got, p.s.rev)
	}
}

func TestReloadErrorRetry(t *testing.T) {
	h, p, _ := witCompose(t, "<p>Hello</p>")
	reads := gateDrafts(p.wit)
	d := h.saveEditable(p.id, "<p>Changed</p>")
	p.wit.reload()
	(<-reads) <- func() (core.Draft, error) { return core.Draft{}, core.ErrClosed }
	h.see("重试")
	h.click("重试")
	(<-reads) <- func() (core.Draft, error) { return d, nil }
	h.until("read again", func() bool { return p.ed.HTML() == "<p>Changed</p>" && !h.tt.HasText("重试") })
}

func TestFreeInputAfterCmdKRunsAssist(t *testing.T) {
	h, p, llm := witCompose(t, "<p>First para.</p><p>Second para.</p>")
	llm.Script(proposeTurn("update_selection", "Second, more concrete."), coretest.Turn{Text: []string{"改好了。"}})
	h.click("正文")
	h.tt.Key(ui.Cmd, ui.KeyDown)
	h.tt.Key(ui.Shift, ui.KeyHome)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.drain()
	h.tt.Frame()
	h.click("修改选中的内容")
	h.typeText("更具体一点")
	h.tt.Key(0, ui.KeyEnter)
	h.drain()
	h.see("1 处修改待确认")
	h.click("全部拒绝")
	h.until("rejected", func() bool { return p.wit.review == nil && !p.wit.locked() })
	llm.Script(coretest.Turn{Text: []string{"这是 Assist 的回答。"}})
	h.tellWit("更具体一点")
	h.see("这是 Assist 的回答。")
	cs, err := draftConversations(context.Background(), h.env.Svc, p.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Title != "" {
			continue
		}
		full, err := h.env.Svc.GetConversation(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range full.Messages {
			if m.Role == "user" && m.Text == "更具体一点" {
				return
			}
		}
	}
	t.Fatal("the free input did not run Assist in the draft's conversation")
}

// countOps wraps every draft-changing Service call of p with a counter.
func countOps(p *composer) map[string]*atomic.Int32 {
	n := map[string]*atomic.Int32{}
	for _, k := range []string{"assist", "skill", "rewrite", "resolve", "resolveAll", "apply", "undo", "addAtt", "removeAtt", "setAccount", "delete"} {
		n[k] = new(atomic.Int32)
	}
	o := p.ops
	p.ops = draftOps{
		assist: func(ctx context.Context, id int64, in string) (<-chan core.WitEvent, error) {
			n["assist"].Add(1)
			return o.assist(ctx, id, in)
		},
		skill: func(ctx context.Context, id int64, sk string, so core.SkillOptions) (<-chan core.WitEvent, error) {
			n["skill"].Add(1)
			return o.skill(ctx, id, sk, so)
		},
		rewrite: func(ctx context.Context, id int64, sel core.Selection, in string) (<-chan core.WitEvent, error) {
			n["rewrite"].Add(1)
			return o.rewrite(ctx, id, sel, in)
		},
		resolve: func(ctx context.Context, id int64, accept bool) error {
			n["resolve"].Add(1)
			return o.resolve(ctx, id, accept)
		},
		resolveAll: func(ctx context.Context, runID int64, accept bool) error {
			n["resolveAll"].Add(1)
			return o.resolveAll(ctx, runID, accept)
		},
		apply: func(ctx context.Context, id int64, html string) error {
			n["apply"].Add(1)
			return o.apply(ctx, id, html)
		},
		undo: func(ctx context.Context, id int64) error {
			n["undo"].Add(1)
			return o.undo(ctx, id)
		},
		addAtt: func(ctx context.Context, id int64, path string) (core.DraftAttachment, error) {
			n["addAtt"].Add(1)
			return o.addAtt(ctx, id, path)
		},
		removeAtt: func(ctx context.Context, id, att int64) error {
			n["removeAtt"].Add(1)
			return o.removeAtt(ctx, id, att)
		},
		setAccount: func(ctx context.Context, id, acc, rev int64) (core.Draft, error) {
			n["setAccount"].Add(1)
			return o.setAccount(ctx, id, acc, rev)
		},
		delete: func(ctx context.Context, id int64) error {
			n["delete"].Add(1)
			return o.delete(ctx, id)
		},
	}
	return n
}

func TestDraftChangesRefusedWhileSending(t *testing.T) {
	h, p, llm := witCompose(t, inlineOld)
	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.plat.mu.Lock()
	h.plat.files = []string{file}
	h.plat.mu.Unlock()
	p.attach()
	h.until("attached", func() bool { return len(p.atts) == 1 && !p.s.busy })
	att := p.atts[0].ID
	llm.Script(proposeTurn("update_draft", inlineNew), coretest.Turn{Text: []string{"改了 2 处。"}})
	h.tellWit("改一下日期和落款")
	h.see("2 处修改待确认")
	h.until("the review", func() bool { return p.wit.review != nil && !p.wit.panel.running && !p.s.busy })
	w := p.wit
	ap := w.review.ap
	n := countOps(p)

	started, release := make(chan struct{}), make(chan struct{})
	p.sendFn = func(context.Context, int64, string) error {
		close(started)
		<-release
		return &core.SendWarningsError{Check: core.SendCheck{Warnings: []core.SendWarning{{Plugin: "p", Message: "careful"}}, Digest: "d"}}
	}
	h.click("发送")
	<-started
	h.drain()

	// Every way to change the draft, as its control would.
	acts := []struct {
		op  string
		act func()
	}{
		{"addAtt", p.attach},
		{"removeAtt", func() { p.removeAttachment(att) }},
		{"setAccount", func() { p.switchAccount(p.acc) }},
		{"apply", func() { w.decideOne(0, true); w.decideOne(1, false) }},
		{"resolve", func() { w.decideAll(true); w.panel.decide(ap, true, false) }},
		{"resolveAll", func() { w.panel.decide(ap, true, true) }},
		{"undo", w.undo},
		{"assist", func() { w.panel.input = "再改一下"; w.panel.send(w.options(nil)) }},
		{"skill", func() { w.runSkill("更短", "shorter", "") }},
		{"rewrite", func() {
			w.selFrom, w.selTo, w.cmdkInput = richtext.Pos{}, richtext.Pos{Rune: 5}, "更具体一点"
			w.rewriteSelection()
		}},
		{"delete", p.discard},
	}
	for _, a := range acts {
		a.act()
	}
	h.drain()
	h.tt.Frame()
	calls := func() map[string]int32 {
		m := map[string]int32{}
		for k, v := range n {
			if v.Load() != 0 {
				m[k] = v.Load()
			}
		}
		return m
	}
	if m := calls(); len(m) != 0 {
		t.Errorf("Service calls while sending: %v", m)
	}
	if !h.tt.HasText("正在发送，暂时不能修改草稿") {
		t.Error("no hint that the draft is being sent")
	}
	close(release)
	h.see("careful")
	h.settle()
	h.drain()
	if m := calls(); len(m) != 0 {
		t.Errorf("Service calls after the send returned: %v", m)
	}
	if t.Failed() {
		return
	}

	// The send failed with a warning: each works again.
	for _, a := range acts {
		h.until("the panel idle", func() bool { return !w.panel.running && !p.s.busy && len(w.panel.deciding) == 0 })
		a.act()
		h.until(a.op+" called", func() bool { return n[a.op].Load() > 0 })
	}
}

// TestQueuedChangeRefusedOnceSending: a change that passed held before the
// send began is still refused right before its Service call.
func TestQueuedChangeRefusedOnceSending(t *testing.T) {
	_, p, _ := witCompose(t, "")
	p.frozen.Store(true)
	ran := false
	if err := p.call(func() error { ran = true; return nil }); !errors.Is(err, errDraftSending) || ran {
		t.Fatalf("call while sending: %v, ran %v", err, ran)
	}
	p.frozen.Store(false)
	if err := p.call(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("call after: %v, ran %v", err, ran)
	}
}
