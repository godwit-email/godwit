package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/mail"
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
	run := p.wit.skill
	p.wit.skill = func(ctx context.Context, id int64, skill string, o core.SkillOptions) (<-chan core.WitEvent, error) {
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
