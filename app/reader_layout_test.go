package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
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
	h.openThread(msgs[0].ThreadID)
	h.seeBody(msgs[1].ID)
	h.see("2 封往来")
	if !r.open[msgs[0].ID] { // both unread: both open
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
