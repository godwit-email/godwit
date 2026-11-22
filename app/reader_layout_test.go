package app

import (
	"context"
	"fmt"
	"math"
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
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	// Its own count would be 1; the list's other thread has a "2 封往来" badge,
	// shown once the list has loaded, so look for this message's count only.
	if h.tt.HasText(i18n.Tf("reader.thread_count", 1)) {
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
	if len(h.tt.Menu()) == 0 {
		t.Fatal("no sender's actions")
	}
	if !r.open[msgs[0].ID] {
		t.Fatal("the sender folded its message")
	}
	h.tt.CloseMenu()
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
	llm.Script(
		coretest.Turn{Text: []string{`{"replies":[{"label":"同意","html":"<p>Yes, Friday works.</p>"},{"html":"<p>Can we do Monday?</p>"}]}`}},
	)
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
	h.see("Can we do Monday?")
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

// A quick reply without an explicit label shows the extracted core clause of
// its preview text.
func TestQuickLabel(t *testing.T) {
	for _, c := range []struct {
		q    core.QuickReply
		want string
	}{
		{core.QuickReply{Label: "同意", Preview: "Yes, Friday works."}, "同意"},
		{core.QuickReply{Preview: "OK"}, "OK"},
		{core.QuickReply{Preview: "Can we do Monday?"}, "Can we do Monday?"},
		{core.QuickReply{Preview: "I can't sign in, could you please reset my password?"}, "I can't sign in"},
	} {
		if got := quickLabel(c.q); got != c.want {
			t.Errorf("quickLabel(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestFitQuickLabels(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		replies := []core.QuickReply{
			{Preview: "Accept now"},                // 10 chars
			{Preview: "Review changes carefully?"}, // 25 chars
			{Preview: "Send details"},              // 12 chars
		}

		// When space is ample, all are shown in full (zero truncation)
		wide := fitQuickLabels(c, replies, 1200)
		require.Len(t, wide, 3)
		assert.Equal(t, "Accept now", wide[0])
		assert.Equal(t, "Review changes carefully?", wide[1])
		assert.Equal(t, "Send details", wide[2])

		// Dynamically compute constrained width based on actual shaped text metrics,
		// ensuring test assertions are reliable across all OS font renderers.
		t0, th := theme.Of(c), c.Theme()
		f := ui.Font{Size: t0.Text.Xs}
		w0, _ := ui.ShapeText("Accept now", f, 0).Size()
		w1, _ := ui.ShapeText("Review changes carefully?", f, 0).Size()
		w2, _ := ui.ShapeText("Send details", f, 0).Size()
		overhead := float32(3)*2*th.Space(2) + float32(2)*th.Space(1)

		// Constrain space so reply 1 (25 runes) must truncate, while short replies
		// reply 0 (10 runes) and reply 2 (12 runes) stay whole.
		narrowW := overhead + w0 + w2 + w1*0.6
		narrow := fitQuickLabels(c, replies, narrowW)
		require.Len(t, narrow, 3)
		assert.Equal(t, "Accept now", narrow[0], "short reply 1 is NOT truncated")
		assert.True(t, strings.HasSuffix(narrow[1], "…"), "longest reply 2 is truncated with ellipsis: %s", narrow[1])
		assert.Equal(t, "Send details", narrow[2], "short reply 3 is NOT truncated")

		// Independent allocation: two replies of equal length (18 and 18 runes).
		// When space is slightly tight, only one shrinks, the other remains full.
		pairReplies := []core.QuickReply{
			{Preview: "Short text"},         // 10 chars
			{Preview: "Review changes now"}, // 18 chars
			{Preview: "Submit details now"}, // 18 chars
		}
		wp0, _ := ui.ShapeText("Short text", f, 0).Size()
		wp1, _ := ui.ShapeText("Review changes now", f, 0).Size()
		wp2, _ := ui.ShapeText("Submit details now", f, 0).Size()
		medW := overhead + wp0 + wp1 + wp2 - 15
		med := fitQuickLabels(c, pairReplies, medW)
		require.Len(t, med, 3)
		assert.Equal(t, "Short text", med[0])
		assert.True(t, med[1] == "Review changes now" || med[2] == "Submit details now", "one of the two long replies stays full")
		assert.True(t, med[1] != med[2], "independent allocation: equal length replies do not share uniform cap")
	}, 800, 600)
	tt.Frame()
}

// A folded message shows its own avatar, read while it was open, else one
// the thread shows for its sender; none for another sender.
func TestShownAvatar(t *testing.T) {
	own, theirs := &ui.Bitmap{}, &ui.Bitmap{}
	msg := func(id int64, from string) core.MessageSummary { return core.MessageSummary{ID: id, From: from} }
	r := &reader{readerPane: &readerPane{}, session: &session{bodies: map[int64]*msgBody{
		1: {msg: core.Message{MessageSummary: msg(1, "Google <no-reply@accounts.google.com>")}, avatar: theirs},
		2: {msg: core.Message{MessageSummary: msg(2, "no-reply@accounts.google.com")}, avatar: own},
	}}}
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

// A message's avatar stays horizontally aligned as the message folds and opens;
// an open borderless message adds 6 px of top breathing room over its folded card.
func TestReaderAvatarStill(t *testing.T) {
	h, acc, _ := aiHarness(t, false, noAuto)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	h.openThreadWith(msgs[0].ThreadID, msgs[0].ID)
	h.seeBody(msgs[1].ID)
	initial := func() ui.Rect { // bob's: the first message's avatar
		h.t.Helper()
		box, ok := h.tt.Find("bob@example.com") // its avatar's box: the list's avatar has no label
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
	if folded := initial(); math.Abs(float64(folded.X-open.X)) > 1 || math.Abs(float64(open.Y-folded.Y-6)) > 1 {
		t.Fatalf("avatar at %v folded, %v open", folded, open)
	}
	h.click("first words")
	h.tt.Frame()
	if opening := initial(); math.Abs(float64(opening.X-open.X)) > 1 || math.Abs(float64(opening.Y-open.Y)) > 1 {
		t.Fatalf("avatar at %v opening, %v open", opening, open)
	}
}

// The header column shares the message's 12 px side inset so the subject left
// edge lines up directly with the message avatar.
func TestReaderMessageAligned(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.resize(2400, 900) // the pane wider than its column, which centers in it
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Plan", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.tt.Advance(2 * theme.MotionBase)
	subject, _ := h.tt.Find("Plan")
	avatar, ok := h.tt.Find("bob@example.com") // the avatar first, its pressable labelled by the address
	if !ok || math.Abs(float64(avatar.X-subject.X)) > 1 {
		t.Fatalf("avatar at x %v, subject at x %v: want them aligned", avatar.X, subject.X)
	}
}

// Scrolling smoothly collapses the reading header from its full height down to
// the 44 px slim bar, shrinking the subject, hiding the label/AI row, and
// bringing the thread count up beside the subject; scrolling back expands it.
func TestReaderStickyHeader(t *testing.T) {
	h, acc, _ := aiHarness(t, false, noAuto)
	msgs := h.thread2(acc)
	r := h.app.mail.reader
	h.openThread(msgs[0].ThreadID)
	v := h.seeBody(msgs[1].ID)
	h.app.BodyHeight(v, msgs[1].ID, 2000)
	h.tt.Frame()
	h.tt.Advance(2 * theme.MotionBase)
	fullH := r.headerH
	if fullH <= 44 || r.barTop != fullH {
		t.Fatalf("at top: barTop=%v headerH=%v", r.barTop, fullH)
	}
	subjFull, ok := h.tt.Find("Plan")
	if !ok {
		t.Fatal("subject not found at top")
	}
	// Touchpad scrolling while the pointer is directly over the sticky header scrolls the pane.
	h.tt.Scroll(subjFull.X+4, subjFull.Y+4, 0, 20)
	h.tt.Frame()
	if r.scroll.Y <= 0 {
		t.Fatalf("scrolling over sticky header did not scroll: scroll.Y=%v", r.scroll.Y)
	}
	// Halfway through the collapse distance: barTop and subject height are smoothly mid-transition.
	r.scroll.Y = (fullH - 44) / 2
	h.tt.Frame()
	h.tt.Frame()
	wantMid := (fullH + 44) / 2
	if math.Abs(float64(r.barTop-wantMid)) > 2 {
		t.Fatalf("halfway scrolled: barTop=%v, want ~%v", r.barTop, wantMid)
	}
	subjMid, ok := h.tt.Find("Plan")
	if !ok {
		t.Fatal("subject not found halfway")
	}
	// Fully collapsed past the collapse distance: pinned at 44 px with thread count beside subject.
	r.scroll.Y = fullH + 40
	h.tt.Frame()
	h.tt.Frame()
	if math.Abs(float64(r.barTop-44)) > 1 {
		t.Fatalf("scrolled past header: barTop=%v, want 44", r.barTop)
	}
	subjSlim, ok := h.tt.Find("Plan")
	if !ok {
		t.Fatal("subject not found collapsed")
	}
	if !(subjFull.H > subjMid.H && subjMid.H > subjSlim.H) {
		t.Fatalf("subject height did not shrink continuously: full=%v mid=%v slim=%v", subjFull.H, subjMid.H, subjSlim.H)
	}
	if !h.tt.HasText("2 封往来") || h.tt.HasText("添加标签") {
		t.Fatalf("collapsed bar state: %q", h.tt.Texts())
	}
	// Back at top: expanded again.
	r.scroll.Y = 0
	h.tt.Frame()
	h.tt.Frame()
	if math.Abs(float64(r.barTop-fullH)) > 1 {
		t.Fatalf("back at top: barTop=%v, want %v", r.barTop, fullH)
	}
}

// Folded messages are papers of a height: one after the other they are that
// and the gap apart, with no hairline between; a click opens one into its
// paper, which the fold button folds again.
func TestReaderFoldedPapers(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	h.setSettings(func(s *core.Settings) { s.ThreadCollapse = false })
	base := h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "first words", coretest.MessageID("a@x"), at(base, 3)))
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "second words", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("dave@example.com", acc.Email, "Re: Plan", "third words", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(acc)
	h.waitMail("the thread", func() bool { return len(h.items()) == 1 })
	msgs := h.messages(h.items()[0].ID)
	r := h.app.mail.reader
	h.openThread(msgs[0].ThreadID)
	h.seeBody(msgs[2].ID)
	h.tt.Advance(2 * theme.MotionBase)
	first, _ := h.tt.Find("first words")
	second, _ := h.tt.Find("second words")
	const folded, gap = 44, 8 // Space(11), Space(2)
	if got := second.Y - first.Y; math.Abs(float64(got-(folded+gap))) > 2 {
		t.Fatalf("folded rows %v apart, want %d", got, folded+gap)
	}
	if r.open[msgs[0].ID] || r.open[msgs[1].ID] || !r.open[msgs[2].ID] {
		t.Fatalf("open %v: want only the newest", r.open)
	}
	h.click("first words")
	h.until("the first open", func() bool { return r.open[msgs[0].ID] })
	h.tt.Advance(2 * theme.MotionBase)
	h.click("收起")
	h.until("the first folded again", func() bool { return !r.open[msgs[0].ID] })
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

// Switching from a short single-line subject to a very long subject wraps the
// long title across multiple lines in the expanded header (never truncated to
// one line), and attaches a hover tooltip on the truncated 44 px slim bar once
// scrolled.
func TestReaderLongSubjectWrapsAndSlimTooltip(t *testing.T) {
	h, acc, _ := aiHarness(t, false, noAuto)
	h.tt.SetSize(1600, 820) // wide enough that the 720 px reading column is horizontally centered
	shortMsg := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Short title", "body"))
	longSubj := "Quarterly engineering architecture review and cross-team release readiness checklist with extended deployment milestones and post-launch follow-up action items"
	longMsg := h.deliverOne(acc, coretest.Msg("carol@example.com", acc.Email, longSubj, "body"))

	h.openThread(shortMsg.ThreadID)
	h.seeBody(shortMsg.ID)
	h.tt.Frame()
	shortH := h.app.mail.reader.headerH

	h.openThread(longMsg.ThreadID)
	v := h.seeBody(longMsg.ID)
	h.app.BodyHeight(v, longMsg.ID, 2000)
	h.tt.Frame()
	h.tt.Frame()
	r := h.app.mail.reader
	if r.headerH <= shortH+20 {
		t.Fatalf("long subject headerH = %.1f, shortH = %.1f: want expanded subject wrapped onto multiple lines", r.headerH, shortH)
	}
	leftmostInk := func(y0, y1 float32) int {
		img := h.tt.Image()
		for x := int(r.pane.X + 10); x < int(r.pane.X+r.pane.W-10); x++ {
			for y := int(y0); y < int(y1); y++ {
				px := img.RGBAAt(x, y)
				if px.R < 180 || px.G < 180 || px.B < 180 {
					return x
				}
			}
		}
		return -1
	}
	expandedInkX := leftmostInk(r.pane.Y+24, r.pane.Y+54)
	if expandedInkX < int(r.pane.X+60) {
		t.Fatalf("expected centered expanded subject ink in wide pane (pane.X=%.1f, pane.W=%.1f), got x=%d", r.pane.X, r.pane.W, expandedInkX)
	}

	// Scroll past collapse range: slim 44 px bar shows truncated single-line subject
	// aligned to the exact same centered X as the expanded title, with full tooltip on hover.
	r.scroll.Y = 300
	h.tt.Frame()
	h.tt.Frame()
	if math.Abs(float64(r.barTop-44)) > 1 {
		t.Fatalf("barTop = %.1f when scrolled, want 44", r.barTop)
	}
	slimInkX := leftmostInk(r.pane.Y+6, r.pane.Y+38)
	if math.Abs(float64(slimInkX-expandedInkX)) > 3 {
		t.Fatalf("slim bar ink starts at x=%d, expanded title starts at x=%d (pane.X=%.1f): slim bar is not centered with expanded title", slimInkX, expandedInkX, r.pane.X)
	}
	// Hover the slim bar title at its centered X to verify the full-title tooltip appears.
	before := 0
	for _, s := range h.tt.Texts() {
		if s == longSubj {
			before++
		}
	}
	h.tt.Move(float32(slimInkX+40), r.pane.Y+22)
	h.tt.Advance(time.Second)
	after := 0
	for _, s := range h.tt.Texts() {
		if s == longSubj {
			after++
		}
	}
	if after <= before {
		t.Fatalf("expected hover tooltip on truncated slim bar at centered x=%d (occurrences before=%d, after=%d)", slimInkX+40, before, after)
	}
}
