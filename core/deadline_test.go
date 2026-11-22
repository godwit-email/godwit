package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadlineOf is the reply date a view's row shows for the conversation of mid
// (nil: none in effect).
func (e *sendEnv) deadlineOf(t *testing.T, mid string) *Deadline {
	t.Helper()
	tid := e.threadOf(t, mid)
	got, err := e.s.PreviewView(bg, SavedViewDoc{Match: MatchAll}, 0, 200)
	require.NoError(t, err)
	for _, g := range got.Groups {
		for _, r := range g.Items {
			if r.ID == tid {
				return r.Deadline
			}
		}
	}
	t.Fatalf("%s is in no group", mid)
	return nil
}

// day is the local calendar day d days after the clock's now.
func (e *sendEnv) day(d int) string { return e.clock.Now().AddDate(0, 0, d).Format("2006-01-02") }

func TestDeadlineProgramSetsAndReads(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	require.Nil(t, e.deadlineOf(t, "q@x"))

	in := Deadline{Date: e.day(3), Time: "15:00", MessageID: e.idOf(t, "q@x"), Quote: "confirm the  quote\nby Friday"}
	require.NoError(t, e.s.SetThreadDeadline(bg, e.threadOf(t, "q@x"), in))
	want := in
	want.Source = DeadlineProgram
	assert.Equal(t, &want, e.deadlineOf(t, "q@x"))
}

func TestDeadlineProgramRejections(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm the quote by Friday."))
	e.put(t, "INBOX", eml("other@x", "carol@a.example", "alice@example.com", "other", e.viewAt(1), "Unrelated."))
	e.put(t, "INBOX", eml("news@x", "news@a.example", "alice@example.com", "news", e.viewAt(1), "Reply by Friday!", "List-Id: <news.a.example>"))
	e.put(t, "Sent", eml("mine@x", "alice@example.com", "dan@a.example", "mine", e.viewAt(1), "Tell me by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	set := func(d Deadline) error { return e.s.SetThreadDeadline(bg, tid, d) }

	for _, c := range []struct {
		name   string
		thread int64
		d      Deadline
		want   error
	}{
		{"no message", tid, Deadline{Date: e.day(2)}, ErrDeadlineMessage},
		{"no such message", tid, Deadline{Date: e.day(2), MessageID: 999999}, ErrDeadlineMessage},
		{"the user's own message", e.threadOf(t, "mine@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "mine@x")}, ErrDeadlineMessage},
		{"another conversation's message", tid, Deadline{Date: e.day(2), MessageID: e.idOf(t, "other@x")}, ErrDeadlineMessage},
		{"a conversation that needs no reply", e.threadOf(t, "news@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "news@x")}, ErrDeadlineNotWaiting},
		{"a quote not in the message", tid, Deadline{Date: e.day(2), MessageID: q, Quote: "by Monday"}, ErrDeadlineQuote},
		{"a bad date", tid, Deadline{Date: "Friday", MessageID: q}, ErrDeadlineInvalid},
		{"a bad time", tid, Deadline{Date: e.day(2), Time: "3pm", MessageID: q}, ErrDeadlineInvalid},
		{"a time with no date", tid, Deadline{Time: "15:00", MessageID: q}, ErrDeadlineInvalid},
		{"a long quote", tid, Deadline{Date: e.day(2), MessageID: q, Quote: strings.Repeat("x", MaxDeadlineQuote+1)}, ErrDeadlineInvalid},
	} {
		assert.ErrorIs(t, e.s.SetThreadDeadline(bg, c.thread, c.d), c.want, c.name)
	}
	assert.Zero(t, e.count(t, "SELECT count(*) FROM thread_deadlines"), "nothing was written")

	// The user's own date, and the marker they leave removing another's, keep programs out.
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, set(Deadline{Date: e.day(2), MessageID: q}))
	require.NoError(t, e.s.SetThreadDeadline(ui, tid, Deadline{}), "the user removes the program's date")
	assert.Nil(t, e.deadlineOf(t, "q@x"))
	assert.ErrorIs(t, set(Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked, "clear, then write")
	assert.ErrorIs(t, set(Deadline{MessageID: q}), ErrDeadlineOutranked)
	require.NoError(t, e.s.SetThreadDeadline(ui, tid, Deadline{Date: e.day(4)}))
	assert.Equal(t, &Deadline{Date: e.day(4), Source: DeadlineManual}, e.deadlineOf(t, "q@x"))
	assert.ErrorIs(t, set(Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked, "overwriting the user's")
	assert.ErrorIs(t, set(Deadline{MessageID: q}), ErrDeadlineOutranked, "clearing the user's")
	assert.Equal(t, &Deadline{Date: e.day(4), Source: DeadlineManual}, e.deadlineOf(t, "q@x"))

	// The user removing their own date leaves nothing behind.
	require.NoError(t, e.s.SetThreadDeadline(ui, tid, Deadline{}))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	require.NoError(t, set(Deadline{Date: e.day(3), MessageID: q}))
}

// Sources by precedence: the same level replaces, a lower one is refused.
func TestDeadlinePrecedence(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.s.setDeadline(bg, DeadlineAI, tid, Deadline{Date: e.day(1), MessageID: q}))
	require.NoError(t, e.s.setDeadline(bg, DeadlineWit, tid, Deadline{Date: e.day(2), MessageID: q}), "wit replaces ai")
	assert.ErrorIs(t, e.s.setDeadline(bg, DeadlineAI, tid, Deadline{Date: e.day(1), MessageID: q}), ErrDeadlineOutranked)
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(3), MessageID: q}), "program replaces wit")
	assert.Equal(t, DeadlineProgram, e.deadlineOf(t, "q@x").Source)
	assert.Equal(t, e.day(3), e.deadlineOf(t, "q@x").Date)
}

// A program's date follows 回复状态: replying or archiving, here or on another
// device or client, ends it; undoing the send or the archive brings it back.
func TestDeadlineEndsWithReplyAndArchive(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q}))
	require.NotNil(t, e.deadlineOf(t, "q@x"))

	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", q)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	assert.Nil(t, e.deadlineOf(t, "q@x"), "replying ends it")
	items, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	require.NoError(t, e.s.CancelOutbox(bg, items[0].ID))
	assert.NotNil(t, e.deadlineOf(t, "q@x"), "undoing the send brings it back")

	undo, err := e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	assert.Nil(t, e.deadlineOf(t, "q@x"), "archiving ends it")
	require.NoError(t, e.s.UndoArchiveThreads(bg, undo))
	assert.NotNil(t, e.deadlineOf(t, "q@x"), "undoing the archive brings it back")

	_, err = e.s.db.Execute(bg, `UPDATE messages SET flags = flags || ' \Answered' WHERE id = ?`, q)
	require.NoError(t, err)
	assert.Nil(t, e.deadlineOf(t, "q@x"), "answered in another client")
	_, err = e.s.db.Execute(bg, `UPDATE messages SET flags = replace(flags, ' \Answered', '') WHERE id = ?`, q)
	require.NoError(t, err)
	require.NotNil(t, e.deadlineOf(t, "q@x"))

	// A reply sent from another client syncs in, then Bob writes again: the old date stays ended.
	e.put(t, "Sent", eml("r@x", "alice@example.com", "bob@a.example", "Re: quote", e.viewAt(0.5), "Will do.", "In-Reply-To: <q@x>"))
	e.put(t, "INBOX", eml("q2@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(0.25), "Also this.", "In-Reply-To: <r@x>"))
	e.sync(t)
	require.Equal(t, tid, e.threadOf(t, "q2@x"))
	require.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q2@x").Kind)
	assert.Nil(t, e.deadlineOf(t, "q@x"), "a reply from another client, synced from Sent, ended it")
	assert.ErrorIs(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q}), ErrDeadlineNotWaiting, "the old mail was answered")
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: e.idOf(t, "q2@x")}))
	require.NotNil(t, e.deadlineOf(t, "q@x"))

	// Archived on another device: its mail leaves the inbox for Archive.
	e.put(t, "Archive", eml("p@x", "dan@a.example", "alice@example.com", "phone", e.viewAt(1), "Reply by Friday."))
	e.put(t, "INBOX", eml("p2@x", "dan@a.example", "alice@example.com", "Re: phone", e.viewAt(0.5), "And?", "In-Reply-To: <p@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "p@x"), e.threadOf(t, "p2@x"))
	assert.ErrorIs(t, e.s.SetThreadDeadline(bg, e.threadOf(t, "p@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "p@x")}), ErrDeadlineNotWaiting)
}

// Merging conversations keeps one date: the highest source's, then the earliest.
func TestDeadlineSurvivesAThreadMerge(t *testing.T) {
	e := newSendEnv(t, false)
	for _, id := range []string{"a", "b", "c"} {
		e.put(t, "INBOX", eml(id+"@x", "bob@acme.example", "alice@example.com", id, e.viewAt(3), "Reply by Friday."))
	}
	e.sync(t)
	set := func(source, mid string, day int) {
		require.NoError(t, e.s.setDeadline(bg, source, e.threadOf(t, mid), Deadline{Date: e.day(day), MessageID: e.idOf(t, mid)}))
	}
	set(DeadlineProgram, "a@x", 3)
	set(DeadlineWit, "b@x", 2)
	set(DeadlineAI, "c@x", 1)

	e.put(t, "INBOX", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(1), "z", "References: <a@x> <b@x> <c@x>"))
	e.sync(t)
	merged := e.threadOf(t, "m@x")
	require.Equal(t, merged, e.threadOf(t, "a@x"))
	require.Equal(t, merged, e.threadOf(t, "c@x"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	got := e.deadlineOf(t, "m@x")
	require.NotNil(t, got)
	assert.Equal(t, e.day(2), got.Date)
	assert.Equal(t, DeadlineWit, got.Source)
}

// A date never becomes state that leaves the machine later (ADR 0003): writing
// or clearing one adds no events_log row (so no rule run or plugin hook) and
// changes no flag or keyword the server would get.
func TestDeadlineStaysLocal(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	state := func() [][]any {
		return e.rows(t, "SELECT (SELECT count(*) FROM events_log), (SELECT count(*) FROM ops), (SELECT COALESCE(flags, '') FROM messages WHERE id = ?)", q)
	}
	before := state()
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q, Quote: "Friday"}))
	removed, err := e.s.RemoveAppAndWitDeadlines(bg)
	require.NoError(t, err)
	require.NoError(t, e.s.UndoRemoveAppAndWitDeadlines(bg, removed))
	require.NoError(t, e.s.SetThreadDeadline(WithUISession(t.Context(), e.s), tid, Deadline{}))
	assert.Equal(t, before, state())
}

func TestDeadlineOverdue(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(3), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(0), MessageID: q}))
	assert.False(t, e.deadlineOf(t, "q@x").Overdue, "due by the end of today")
	require.NoError(t, e.s.SetThreadDeadline(bg, tid, Deadline{Date: e.day(-1), Time: "23:59", MessageID: q}))
	assert.True(t, e.deadlineOf(t, "q@x").Overdue)
}

// 全部去掉 in settings takes away every date other apps and Wit set, and
// nothing else; undoing it puts them back, but not over a date the user set since.
func TestRemoveAppAndWitDeadlines(t *testing.T) {
	e := newSendEnv(t, false)
	for _, id := range []string{"p", "w", "a", "m"} {
		e.put(t, "INBOX", eml(id+"@x", "bob+"+id+"@a.example", "alice@example.com", id, e.viewAt(1), "Reply by Friday."))
	}
	e.sync(t)
	set := func(source, mid string) {
		require.NoError(t, e.s.setDeadline(bg, source, e.threadOf(t, mid), Deadline{Date: e.day(2), Time: "09:30", MessageID: e.idOf(t, mid), Quote: "Friday"}))
	}
	set(DeadlineProgram, "p@x")
	set(DeadlineWit, "w@x")
	set(DeadlineAI, "a@x")
	set(DeadlineManual, "m@x")
	program := e.deadlineOf(t, "p@x")
	count := func() int {
		n, err := e.s.CountAppAndWitDeadlines(bg)
		require.NoError(t, err)
		return n
	}
	assert.Equal(t, 2, count())

	removed, err := e.s.RemoveAppAndWitDeadlines(bg)
	require.NoError(t, err)
	assert.Len(t, removed, 2)
	assert.Nil(t, e.deadlineOf(t, "p@x"))
	assert.Nil(t, e.deadlineOf(t, "w@x"))
	assert.Equal(t, DeadlineAI, e.deadlineOf(t, "a@x").Source)
	assert.Equal(t, DeadlineManual, e.deadlineOf(t, "m@x").Source)
	assert.Zero(t, count())

	// Meanwhile the user sets their own date on one: undo leaves it.
	require.NoError(t, e.s.SetThreadDeadline(WithUISession(t.Context(), e.s), e.threadOf(t, "w@x"), Deadline{Date: e.day(5)}))
	require.NoError(t, e.s.UndoRemoveAppAndWitDeadlines(bg, removed))
	assert.Equal(t, program, e.deadlineOf(t, "p@x"))
	assert.Equal(t, &Deadline{Date: e.day(5), Source: DeadlineManual}, e.deadlineOf(t, "w@x"))
	assert.Equal(t, 1, count())

	// Undo writes back only app and Wit dates, never a manual or ai one.
	forged := []RemovedDeadline{{ThreadID: e.threadOf(t, "p@x"), Date: e.day(9), Source: DeadlineManual}}
	_, err = e.s.RemoveAppAndWitDeadlines(bg)
	require.NoError(t, err)
	require.NoError(t, e.s.UndoRemoveAppAndWitDeadlines(bg, forged))
	assert.Nil(t, e.deadlineOf(t, "p@x"))
}

// The 回复日期 condition reads the dates in effect: 有, 没有, due within N days
// (today included, overdue not) and 已过期, in groups and pages too.
func TestViewDeadlineCondition(t *testing.T) {
	e := newSendEnv(t, false)
	for _, mid := range []string{"soon", "today", "passed", "later", "overdue", "none", "removed"} {
		e.put(t, "INBOX", eml(mid+"@x", "bob@a.example", "alice@example.com", mid, e.viewAt(1), "Reply by Friday."))
	}
	e.put(t, "Sent", eml("mine@x", "alice@example.com", "dan@a.example", "mine", e.viewAt(1), "Tell me."))
	e.sync(t)
	set := func(mid string, d Deadline) {
		d.MessageID = e.idOf(t, mid+"@x")
		require.NoError(t, e.s.SetThreadDeadline(bg, e.threadOf(t, mid+"@x"), d), mid)
	}
	set("soon", Deadline{Date: e.day(2)})
	set("today", Deadline{Date: e.day(0)})
	set("passed", Deadline{Date: e.day(0), Time: "00:00"})
	set("later", Deadline{Date: e.day(3)})
	set("overdue", Deadline{Date: e.day(-1)})
	set("removed", Deadline{Date: e.day(1)})
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, e.s.SetThreadDeadline(ui, e.threadOf(t, "removed@x"), Deadline{}), "the user removes it")
	require.NoError(t, e.s.SetThreadDeadline(ui, e.threadOf(t, "mine@x"), Deadline{Date: e.day(1)}), "the user's own date, on a conversation with no 回复状态")

	for value, want := range map[string][]string{
		RuleYes:             {"later", "mine", "overdue", "passed", "soon", "today"},
		RuleNo:              {"none", "removed"},
		"3":                 {"mine", "soon", "today"},
		"1":                 {"today"},
		ViewDeadlineOverdue: {"overdue", "passed"},
	} {
		assert.Equal(t, map[string][]string{ViewGroupOther: want}, e.shown(t, flatDoc(MatchAll, cond(FieldDeadline, OpIs, value))), value)
	}

	id, err := e.s.SaveView(bg, SavedView{Name: "Due", Icon: "team", Doc: SavedViewDoc{Match: MatchAll, Groups: []ViewGroup{
		{ID: "due", Name: "Due", Match: MatchAll, Conditions: []RuleCondition{cond(FieldDeadline, OpIs, "3")}}}}})
	require.NoError(t, err)
	got, err := e.s.ViewContents(bg, id, 2)
	require.NoError(t, err)
	page := got.Groups[0]
	require.Equal(t, "due", page.ID)
	assert.Equal(t, 3, page.Total)
	seen := len(page.Items)
	page, err = e.s.ViewGroupPage(bg, id, "due", page.NextCursor, 2)
	require.NoError(t, err)
	assert.Equal(t, 3, seen+len(page.Items), "the next page has the rest")
	assert.Empty(t, page.NextCursor)
}

// 回复日期 is for views only: a date an app, Wit or the summary wrote must not
// set off a rule's actions.
func TestDeadlineConditionIsViewOnly(t *testing.T) {
	e := newSendEnv(t, false)
	save := func(c RuleCondition) error {
		_, err := e.s.SaveView(bg, SavedView{Name: "X", Icon: "team", Doc: flatDoc(MatchAll, c)})
		return err
	}
	for _, v := range []string{"0", "3651", "x", ""} {
		assert.ErrorIs(t, save(cond(FieldDeadline, OpIs, v)), ErrInvalidView, v)
	}
	assert.ErrorIs(t, save(cond(FieldDeadline, OpContains, RuleYes)), ErrInvalidView, "op")
	for _, v := range []string{RuleYes, RuleNo, ViewDeadlineOverdue, "1", "3650"} {
		assert.NoError(t, save(cond(FieldDeadline, OpIs, v)), v)
		_, err := e.s.SaveRule(bg, Rule{Doc: RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll,
			Conditions: []RuleCondition{cond(FieldDeadline, OpIs, v)}, Actions: []RuleAction{{Kind: ActionRead}}}})
		assert.ErrorIs(t, err, ErrInvalidRule, v)
	}
}
