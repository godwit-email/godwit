package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
)

// aiHarness is a mail harness with one account; with llm a fake model
// service is the default model. set changes the settings before the App starts.
func aiHarness(t *testing.T, llm bool, set func(*core.Settings), setup ...func(*coretest.Env)) (*harness, core.Account, *coretest.LLM) {
	t.Helper()
	var acc core.Account
	var l *coretest.LLM
	h := newHarnessWith(t, nil, append([]func(*coretest.Env){func(e *coretest.Env) {
		acc = e.AddAccount(t, "alice@example.com")
		if llm {
			l = e.LLM(t)
			t.Cleanup(l.Release)
		}
		if set != nil {
			st, err := e.Svc.GetSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			set(&st)
			if _, err := e.Svc.SetSettings(context.Background(), st); err != nil {
				t.Fatal(err)
			}
		}
	}}, setup...)...)
	h.until("the mail page", func() bool { return h.app.loaded && h.app.route.Page == PageMail })
	return h, acc, l
}

func noQuick(st *core.Settings)   { st.QuickReplies = false }
func noSummary(st *core.Settings) { st.Summary = false }
func noAuto(st *core.Settings)    { st.QuickReplies, st.Summary = false, false }

func TestSummaryStreams(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noQuick)
	llm.Script(coretest.Turn{Text: []string{"- 第一点要点\n", "- 第二点要点\n"}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "the plan body"))
	other := h.deliverOne(acc, coretest.Msg("carol@example.com", acc.Email, "Other", "other body"))
	h.openThread(m.ThreadID)
	h.see("总结 · summarize · m1")
	h.see("第一点要点")
	h.see("第二点要点")
	if n := h.countTexts("点要点"); n != 2 {
		t.Fatalf("%d points: %q", n, h.tt.Texts())
	}
	h.until("the summary done", func() bool { s := h.app.mail.reader.ai[m.ID]; return s != nil && s.summary.done })
	n := llm.Requests()
	h.openThread(other.ThreadID)
	h.see("other body")
	h.until("the other summary done", func() bool { s := h.app.mail.reader.ai[other.ID]; return s != nil && s.summary.done })
	n2 := llm.Requests()
	if n2 != n+1 {
		t.Fatalf("requests %d after the other message, want %d", n2, n+1)
	}
	h.openThread(m.ThreadID)
	h.see("第二点要点")
	if got := llm.Requests(); got != n2 {
		t.Fatalf("requests %d after coming back, want %d", got, n2)
	}
}

func TestSummaryOffWithoutModel(t *testing.T) {
	h, acc, _ := aiHarness(t, false, nil)
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "the plan body"))
	h.openThread(m.ThreadID)
	h.see("the plan body")
	for range 20 {
		h.drain()
		h.tt.Frame()
	}
	if h.tt.HasText("summarize") || h.tt.HasText("翻译") || h.tt.HasText("即时回复") {
		t.Fatalf("AI shown without a model: %q", h.tt.Texts())
	}
	if s := h.app.mail.reader.ai[m.ID]; s != nil && s.summary != nil {
		t.Fatal("a summary started without a model")
	}
}

func TestTranslateToggle(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noAuto)
	llm.Script(coretest.Turn{Text: []string{"Hallo **Welt**, das ist die Übersetzung des Textes."}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "the original words"))
	h.openThread(m.ThreadID)
	h.see("the original words")
	h.click("翻译")
	h.see("已翻译为简体中文 · translate · m1")
	h.see("Welt")
	h.until("the original replaced", func() bool { return !h.tt.HasText("the original words") })
	h.click("显示原文")
	h.see("the original words")
	if h.tt.HasText("Welt") {
		t.Fatal("translation still shown")
	}
	h.click("翻译")
	h.see("Welt")
	if n := llm.Requests(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

// writeSkill adds a user skill on messages to the Env's skills directory.
func writeSkill(t *testing.T, e *coretest.Env, name string) {
	t.Helper()
	dir := filepath.Join(e.DataDir, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: Brief the email\nmetadata:\n  godwit-action-on: \"message\"\n  godwit-action-output: \"panel\"\n---\n\nBrief it.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skillMenu chooses item from the reader's skill menu.
func (h *harness) skillMenu(item string) {
	h.t.Helper()
	h.click("更多 skill ▾")
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.drain()
	h.tt.Frame()
}

func TestSkillMenuRuns(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noAuto, func(e *coretest.Env) { writeSkill(t, e, "brief") })
	llm.Script(coretest.Turn{Text: []string{"the skill output"}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "body words"))
	h.openThread(m.ThreadID)
	h.see("body words")
	h.see("更多 skill ▾")
	h.click("更多 skill ▾")
	menu := strings.Join(h.tt.Menu(), "|")
	h.tt.CloseMenu()
	if strings.Contains(menu, "summarize") || strings.Contains(menu, "translate") || strings.Contains(menu, "proofread") {
		t.Fatalf("menu %q", menu)
	}
	h.skillMenu("brief")
	h.see("the skill output")
	h.click("关闭结果")
	h.until("the card closed", func() bool { return !h.tt.HasText("the skill output") })
}

func TestActionEgressApproval(t *testing.T) {
	h, acc, _ := aiHarness(t, false, nil, func(e *coretest.Env) { e.InstallPlugin(t, "egress-action") })
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "body words"))
	h.openThread(m.ThreadID)
	h.see("body words")
	h.skillMenu("Ping")
	h.see("egress.godwit.test:80")
	h.see("egress-action")
	if h.tt.HasText("pong") {
		t.Fatal("ran before approval")
	}
	h.click("允许这一次")
	h.see("pong")
}

func TestCardCopy(t *testing.T) {
	h, acc, _ := aiHarness(t, false, nil, func(e *coretest.Env) { e.InstallPlugin(t, "card-demo") })
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Invoice 42", "pay me"))
	h.openThread(m.ThreadID)
	h.see("Invoice · 来自插件 card-demo")
	h.see("From")
	h.click("Copy")
	if got := h.tt.Clipboard(); got != "INV-1" {
		t.Fatalf("clipboard %q", got)
	}
	h.see("已复制")
	h.click("Open")
	h.see("pay.example")
	if !h.app.linkOpen || h.app.link != "https://pay.example/inv" {
		t.Fatalf("link dialog %v %q", h.app.linkOpen, h.app.link)
	}
}

func TestQuickReplies(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noSummary)
	llm.Script(coretest.Turn{Text: []string{`{"replies":[{"html":"<p>Reply one</p>"},{"html":"<p>Reply two</p>"},{"html":"<p>Reply three</p>"}]}`}})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Question", "can you?"))
	h.openThread(m.ThreadID)
	h.see("即时回复 · 选一封，进入撰写页再改")
	h.see("Reply one")
	h.see("Reply two")
	h.see("Reply three")
	for range 200 {
		if h.tt.Focused("Reply two") {
			break
		}
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
	}
	if !h.tt.Focused("Reply two") {
		t.Fatal("Tab never reached the second reply")
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	h.until("the composer", func() bool { return h.app.route.Page == PageCompose && h.app.route.DraftID != 0 })
	d, err := h.env.Svc.GetDraft(context.Background(), h.app.route.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.HTML, "Reply two") || strings.Contains(d.HTML, "Reply one") || d.SourceMessageID != m.ID {
		t.Fatalf("draft %+v", d)
	}
}

func TestSwitchingCancelsRun(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noQuick)
	llm.Script(coretest.Turn{Hang: true})
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Slow", "slow body"))
	other := h.deliverOne(acc, coretest.Msg("carol@example.com", acc.Email, "Other", "other body"))
	r := h.app.mail.reader
	var mu sync.Mutex
	ctxs := map[int64]context.Context{}
	real := r.runSkill
	r.runSkill = func(ctx context.Context, name string, tg core.Target) (<-chan ai.Event, error) {
		mu.Lock()
		ctxs[tg.ID] = ctx
		mu.Unlock()
		return real(ctx, name, tg)
	}
	runCtx := func(id int64) context.Context { mu.Lock(); defer mu.Unlock(); return ctxs[id] }
	h.openThread(m.ThreadID)
	h.until("the summary started", func() bool { return runCtx(m.ID) != nil && llm.Requests() == 1 })
	h.see("summarize")
	h.openThread(other.ThreadID)
	h.see("other body")
	if runCtx(m.ID).Err() == nil {
		t.Fatal("the run of the message left goes on")
	}
	if s := r.ai[m.ID]; s != nil && s.summary != nil {
		t.Fatalf("a cancelled summary kept: %+v", s.summary)
	}
	h.until("the other summary done", func() bool { s := r.ai[other.ID]; return s != nil && s.summary != nil && s.summary.done })
	h.openThread(m.ThreadID)
	h.until("the summary run again", func() bool { s := r.ai[m.ID]; return s != nil && s.summary != nil && s.summary.done })
	if n := llm.Requests(); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}
