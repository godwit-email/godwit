package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/theme"
)

// A message read alone has no count of messages and no fold button; a
// thread has both, folds by the button, and its sender opens the sender's
// actions instead of folding.
func TestReaderThreadHeader(t *testing.T) {
	h, acc, _ := aiHarness(t, false, noAuto)
	msgs := h.thread2(acc) // first: it wants the only thread
	m := h.deliverOne(acc, coretest.Msg("dave@example.com", acc.Email, "Alone", "just one"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	if h.tt.HasText("封往来") {
		t.Fatalf("a message alone counts messages: %q", h.tt.Texts())
	}
	if _, ok := h.tt.Find("收起"); ok {
		t.Fatal("a message alone folds")
	}

	r := h.app.mail.reader
	h.openThreadWith(msgs[0].ThreadID, msgs[0].ID)
	h.seeBody(msgs[1].ID)
	h.see("2 封往来")
	if !r.open[msgs[0].ID] { // the older opened too
		t.Fatalf("open %v", r.open)
	}
	h.click("bob@example.com")
	h.until("the sender's actions", func() bool { return r.senderOpen && r.senderMsgID == msgs[0].ID })
	if !r.open[msgs[0].ID] {
		t.Fatal("the sender folded its message")
	}
	r.senderOpen = false
	h.tt.Frame()
	h.click("收起")
	h.until("the first folded", func() bool { return !r.open[msgs[0].ID] && r.open[msgs[1].ID] })
	h.see("first words") // its row: the start of its text
}

// The summary of a message read alone follows its sender; a thread's is
// under the subject, above its messages.
func TestReaderSummaryPlace(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.thread2(acc) // first: it wants the only thread
	m := h.deliverOne(acc, coretest.Msg("dave@example.com", acc.Email, "Alone", "just one"))
	h.openThread(m.ThreadID)
	h.see("m1")
	sum, _ := h.tt.Find("m1")
	to, _ := h.tt.Find("发给 我")
	if sum.Y <= to.Y {
		t.Fatalf("summary at %v, sender line at %v: want it under the sender", sum, to)
	}

	h.openThread(msgs[0].ThreadID)
	r := h.app.mail.reader
	h.until("the summary done", func() bool {
		s := r.ai[msgs[1].ID]
		return s != nil && s.summary != nil && s.summary.done
	})
	h.see("m1")
	sum, _ = h.tt.Find("m1")
	to, _ = h.tt.Find("发给 我")
	if sum.Y >= to.Y {
		t.Fatalf("summary at %v, first sender line at %v: want it above the messages", sum, to)
	}
}

// The reply bar shows each quick reply by its label (the start of its text
// without one), a draft on a click; its box hands a question to Wit quoting
// the newest message.
func TestReaderReplyBar(t *testing.T) {
	h, acc, llm := aiHarness(t, true, noSummary)
	llm.Script(coretest.Turn{Text: []string{`{"replies":[{"label":"同意","html":"<p>Yes, Friday works.</p>"},{"html":"<p>Can we do Monday instead?</p>"}]}`}})
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Question", "can you?"))
	var asks []string
	var quoted [][]int64
	h.app.SetWitAsk(func(q string, ms ...core.MessageSummary) {
		asks = append(asks, q)
		var ids []int64
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		quoted = append(quoted, ids)
	})
	h.openThread(m.ThreadID)
	h.see("同意")
	h.see("Can we do Mo…")
	h.click("同意")
	d, err := h.env.Svc.GetDraft(context.Background(), h.composeOpened(0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.HTML, "Yes, Friday works.") || d.SourceMessageID != m.ID {
		t.Fatalf("draft %+v", d)
	}

	h.click("交给 Wit")
	h.typeText("说周五可以")
	h.tt.Key(0, ui.KeyEnter)
	h.until("the Wit page", func() bool { return h.app.route.Page == PageWit })
	if !slices.Equal(asks, []string{"说周五可以"}) || len(quoted) != 1 || !slices.Equal(quoted[0], []int64{m.ID}) {
		t.Fatalf("asks %q quoting %v", asks, quoted)
	}
	if h.app.mail.reader.replyAsk != "" {
		t.Fatalf("the box kept %q", h.app.mail.reader.replyAsk)
	}
}

// A quick reply cached before labels were asked for shows the start of its
// text.
func TestQuickLabel(t *testing.T) {
	for _, c := range []struct {
		q    core.QuickReply
		want string
	}{
		{core.QuickReply{Label: "同意", Preview: "Yes, Friday works."}, "同意"},
		{core.QuickReply{Preview: "OK"}, "OK"},
		{core.QuickReply{Preview: "Can we do Monday instead?"}, "Can we do Mo…"},
	} {
		if got := quickLabel(c.q); got != c.want {
			t.Errorf("quickLabel(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

// A folded message shows its own avatar, read while it was open, else one
// the thread shows for its sender; none for another sender.
func TestShownAvatar(t *testing.T) {
	own, theirs := &ui.Bitmap{}, &ui.Bitmap{}
	msg := func(id int64, from string) core.MessageSummary { return core.MessageSummary{ID: id, From: from} }
	r := &reader{bodies: map[int64]*msgBody{
		1: {msg: core.Message{MessageSummary: msg(1, "Google <no-reply@accounts.google.com>")}, avatar: theirs},
		2: {msg: core.Message{MessageSummary: msg(2, "no-reply@accounts.google.com")}, avatar: own},
	}}
	if got := r.shownAvatar(msg(2, "no-reply@accounts.google.com")); got != own {
		t.Error("not its own avatar")
	}
	if got := r.shownAvatar(msg(3, "Google <No-Reply@accounts.google.com>")); got != theirs && got != own {
		t.Error("not its sender's avatar")
	}
	if got := r.shownAvatar(msg(4, "bob@example.com")); got != nil {
		t.Error("another sender's avatar")
	}
}

// A message's avatar stays where it is as the message folds and opens.
func TestReaderAvatarStill(t *testing.T) {
	h, acc, _ := aiHarness(t, false, noAuto)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	h.openThreadWith(msgs[0].ThreadID, msgs[0].ID)
	h.seeBody(msgs[1].ID)
	initial := func() ui.Rect { // bob's: the first message's avatar
		h.t.Helper()
		box, ok := h.tt.Find("B")
		if !ok {
			t.Fatalf("no avatar: %q", h.tt.Texts())
		}
		return box
	}
	open := initial()
	h.click("收起")
	h.tt.Advance(2 * theme.MotionBase)
	if r.open[msgs[0].ID] {
		t.Fatal("not folded")
	}
	if folded := initial(); folded.X != open.X || folded.Y != open.Y {
		t.Fatalf("avatar at %v folded, %v open", folded, open)
	}
	h.click("first words")
	h.tt.Frame()
	if opening := initial(); opening.X != open.X || opening.Y != open.Y {
		t.Fatalf("avatar at %v opening, %v open", opening, open)
	}
}

// A message's header keeps to the column its subject does: the avatar's
// left edge under the subject's.
func TestReaderMessageAligned(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.resize(2400, 900) // the pane wider than its column, which centers in it
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Plan", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.tt.Advance(2 * theme.MotionBase)
	subject, _ := h.tt.Find("Plan")
	avatar, ok := h.tt.Find("bob@example.com") // the avatar first, its pressable labelled by the address
	if !ok || avatar.X != subject.X {
		t.Fatalf("avatar at x %v, subject at x %v", avatar.X, subject.X)
	}
}

// A folded message shows its AI gist, marked, in place of the start of its
// text; the open one shows none.
func TestReaderFoldedGist(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	r.gistFn = func(_ context.Context, id int64) (string, error) { return fmt.Sprint("gist of ", id), nil }
	h.openThread(msgs[0].ThreadID)
	h.until("the folded row's gist", func() bool { return h.tt.HasText(fmt.Sprint("gist of ", msgs[0].ID)) })
	if _, ok := h.tt.Find("AI 要点"); !ok {
		t.Fatal("the gist is not marked the AI's")
	}
	if h.tt.HasText("first words") {
		t.Fatal("the row shows the start of its text beside its gist")
	}
	if h.tt.HasText(fmt.Sprint("gist of ", msgs[1].ID)) {
		t.Fatal("the open message shows a gist")
	}
}

// Without a model a folded row keeps the start of its text, unmarked, and
// asks for no gist.
func TestReaderFoldedGistNoModel(t *testing.T) {
	h, acc, _ := aiHarness(t, false, nil)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	var asked atomic.Bool // set off the UI thread
	r.gistFn = func(context.Context, int64) (string, error) { asked.Store(true); return "x", nil }
	h.openThread(msgs[0].ThreadID)
	h.see("first words")
	if _, ok := h.tt.Find("AI 要点"); ok || asked.Load() {
		t.Fatalf("a gist without a model (asked %v)", asked.Load())
	}
}

// digestSkill makes r's summaries of a thread answer text, and records the
// kinds of target its summaries ran on.
func digestSkill(r *reader, text func() string) (kinds func() []string) {
	var mu sync.Mutex
	var seen []string
	real := r.runSkill
	r.runSkill = func(ctx context.Context, name string, tg core.Target) (<-chan ai.Event, error) {
		mu.Lock()
		seen = append(seen, tg.Kind)
		mu.Unlock()
		if name != "summarize" || tg.Kind != "thread" {
			return real(ctx, name, tg)
		}
		out := make(chan ai.Event, 3)
		out <- ai.Event{Kind: "context", Model: "x/m1"}
		out <- ai.Event{Kind: "text", Text: text()}
		out <- ai.Event{Kind: "done"}
		close(out)
		return out, nil
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(seen) }
}

// A thread's summary shows its steps citing their messages by place in the
// thread, a cite not of the thread dropped, and what waits on the user; a
// cite shows its message's card on hover and opens it.
func TestReaderThreadDigest(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	digestSkill(r, func() string {
		return fmt.Sprintf("PROGRESS\n- Bob proposed a plan <mail id=\"%d\"/>\n- Carol replied <mail id=\"%d\"/>\n- Lost <mail id=\"999\"/>\nTODO: Reply to Carol\n",
			msgs[0].ID, msgs[1].ID)
	})
	h.openThread(msgs[0].ThreadID)
	h.see("Bob proposed a plan")
	h.see("等你处理")
	h.see("Reply to Carol")
	h.see("基于 2 封往来")
	if h.tt.HasText("PROGRESS") || h.tt.HasText("999") || h.tt.HasText("mail id") {
		t.Fatalf("the digest shows as written: %q", h.tt.Texts())
	}
	if r.open[msgs[0].ID] {
		t.Fatal("the older message opened before its source was clicked")
	}
	if h.tt.HasText("second words") {
		t.Fatal("the newest message's card shows before its cite is hovered")
	}
	b, ok := h.tt.Find(r.sourceLabel(msgs[1]))
	if !ok {
		t.Fatalf("no cite of the newest message: %q", h.tt.Texts())
	}
	h.tt.Move(b.X+b.W/2, b.Y+b.H/2)
	h.tt.Advance(900 * time.Millisecond)
	h.see("second words") // its card: sender, when, the start of its text
	h.click(r.sourceLabel(msgs[0]))
	h.until("the source's message open", func() bool { return r.open[msgs[0].ID] })
}

// A digest that cannot be read shows as written; a message alone keeps its
// own summary, not its thread's.
func TestReaderThreadDigestFallback(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	kinds := digestSkill(r, func() string { return "Just some prose." })
	h.openThread(msgs[0].ThreadID)
	h.see("Just some prose.")

	m := h.deliverOne(acc, coretest.Msg("dave@example.com", acc.Email, "Alone", "just one"))
	h.openThread(m.ThreadID)
	h.until("its summary run", func() bool { return slices.Contains(kinds(), "message") })
	if got := kinds(); !slices.Equal(got, []string{"thread", "message"}) {
		t.Fatalf("summaries ran on %q", got)
	}
}

// A summary kept from the newest message read alone does not stand for its
// thread's: opening the thread summarizes the thread.
func TestReaderSummaryFollowsTarget(t *testing.T) {
	h, acc, _ := aiHarness(t, true, noQuick)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	r.gistFn = func(context.Context, int64) (string, error) { return "", nil }
	var mu sync.Mutex
	var kinds []string
	r.runSkill = func(_ context.Context, name string, tg core.Target) (<-chan ai.Event, error) {
		mu.Lock()
		kinds = append(kinds, tg.Kind)
		mu.Unlock()
		out := make(chan ai.Event, 3)
		out <- ai.Event{Kind: "context", Model: "x/m1"}
		out <- ai.Event{Kind: "text", Text: "PROGRESS\n- Summary of the " + tg.Kind + "\n"}
		out <- ai.Event{Kind: "done"}
		close(out)
		return out, nil
	}
	h.app.OpenMessage(msgs[1].ID)
	h.see("PROGRESS") // a message's summary is no digest: as written
	h.openThread(msgs[0].ThreadID)
	h.see("Summary of the thread")
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(kinds, []string{"message", "thread"}) {
		t.Fatalf("summaries ran on %q", kinds)
	}
}
