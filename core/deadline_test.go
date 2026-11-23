package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/store"
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

// setThreadDeadline is SetThreadDeadline's error.
func (e *sendEnv) setThreadDeadline(ctx context.Context, threadID int64, d Deadline) error {
	_, err := e.s.SetThreadDeadline(ctx, threadID, d)
	return err
}

// day is the local calendar day d days after the clock's now.
func (e *sendEnv) day(d int) string { return e.clock.Now().AddDate(0, 0, d).Format("2006-01-02") }

func TestDeadlineProgramSetsAndReads(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(0.5), "Please confirm the quote by Friday."))
	e.sync(t)
	require.Nil(t, e.deadlineOf(t, "q@x"))

	in := Deadline{Date: e.day(3), Time: "15:00", MessageID: e.idOf(t, "q@x"), Quote: "confirm the  quote\nby Friday"}
	require.NoError(t, e.setThreadDeadline(bg, e.threadOf(t, "q@x"), in))
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
	set := func(d Deadline) error { return e.setThreadDeadline(bg, tid, d) }

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
		assert.ErrorIs(t, e.setThreadDeadline(bg, c.thread, c.d), c.want, c.name)
	}
	assert.Zero(t, e.count(t, "SELECT count(*) FROM thread_deadlines"), "nothing was written")

	// The user's own date, and the marker they leave removing another's, keep programs out.
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, set(Deadline{Date: e.day(2), MessageID: q}))
	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{}), "the user removes the program's date")
	assert.Nil(t, e.deadlineOf(t, "q@x"))
	assert.ErrorIs(t, set(Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked, "clear, then write")
	assert.ErrorIs(t, set(Deadline{MessageID: q}), ErrDeadlineOutranked)
	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{Date: e.day(4)}))
	assert.Equal(t, &Deadline{Date: e.day(4), Source: DeadlineManual}, e.deadlineOf(t, "q@x"))
	assert.ErrorIs(t, set(Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked, "overwriting the user's")
	assert.ErrorIs(t, set(Deadline{MessageID: q}), ErrDeadlineOutranked, "clearing the user's")
	assert.Equal(t, &Deadline{Date: e.day(4), Source: DeadlineManual}, e.deadlineOf(t, "q@x"))

	// The user removing their own date leaves nothing behind.
	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{}))
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
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(3), MessageID: q}), "program replaces wit")
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
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q}))
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
	assert.ErrorIs(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q}), ErrDeadlineNotWaiting, "the old mail was answered")
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: e.idOf(t, "q2@x")}))
	require.NotNil(t, e.deadlineOf(t, "q@x"))

	// Archived on another device: its mail leaves the inbox for Archive.
	e.put(t, "Archive", eml("p@x", "dan@a.example", "alice@example.com", "phone", e.viewAt(1), "Reply by Friday."))
	e.put(t, "INBOX", eml("p2@x", "dan@a.example", "alice@example.com", "Re: phone", e.viewAt(0.5), "And?", "In-Reply-To: <p@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "p@x"), e.threadOf(t, "p2@x"))
	assert.ErrorIs(t, e.setThreadDeadline(bg, e.threadOf(t, "p@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "p@x")}), ErrDeadlineNotWaiting)
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

// A merge keeps the date that is still in effect: A's date ended when the user
// replied, B's is on mail newer than that reply, so B's survives though A's
// would win on source and date alone.
func TestDeadlineMergeKeepsTheOneInEffect(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "a", e.viewAt(5), "Reply by Monday."))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "b", e.viewAt(1), "Reply by Thursday."))
	e.sync(t)
	require.NoError(t, e.s.setDeadline(bg, DeadlineProgram, e.threadOf(t, "a@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "a@x")}))
	require.NoError(t, e.s.setDeadline(bg, DeadlineProgram, e.threadOf(t, "b@x"), Deadline{Date: e.day(4), MessageID: e.idOf(t, "b@x")}))
	e.put(t, "INBOX", eml("r@x", "alice@example.com", "bob@acme.example", "Re: a", e.viewAt(3), "Will do.", "In-Reply-To: <a@x>", "References: <a@x>"))
	e.sync(t)
	require.Nil(t, e.deadlineOf(t, "a@x"), "A's date ended with the reply")

	e.put(t, "INBOX", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(2), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "a@x"), e.threadOf(t, "b@x"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	got := e.deadlineOf(t, "b@x")
	require.NotNil(t, got, "B's date is still in effect after the merge")
	assert.Equal(t, e.day(4), got.Date)
}

// A merge keeps the newest archived mail's mark, for 回复状态, with the latest
// archive among the merged ones: B archived, then A's own date set, then A
// archived; B's mail is newer, and A's date stays ended.
func TestDeadlineMergeKeepsTheLatestArchive(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "a", e.viewAt(5), "Reply by Monday."))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "b", e.viewAt(1), "Thanks."))
	e.sync(t)
	_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "b@x")})
	require.NoError(t, err)
	e.clock.Advance(time.Minute)
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), e.threadOf(t, "a@x"), Deadline{Date: e.day(4)}))
	e.clock.Advance(time.Minute)
	_, err = e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "a@x")})
	require.NoError(t, err)
	require.Nil(t, e.deadlineOf(t, "a@x"), "archiving A ended its date")
	latest := e.rows(t, "SELECT max(archived_version), max(archived_at) FROM archive_marks")[0]

	e.put(t, "Archive", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(3), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "a@x"), e.threadOf(t, "b@x"))
	assert.Nil(t, e.deadlineOf(t, "a@x"), "the merge keeps A's archive, which came after the date")
	assert.Equal(t, [][]any{{"b@x", latest[0], latest[1]}}, e.rows(t, "SELECT message_id_hdr, archived_version, archived_at FROM archive_marks"),
		"B's newer mail, A's later archive")
}

// Both dates still in effect after a merge: the higher source wins, as a
// write would, though the lower one is on newer mail.
func TestDeadlineMergeKeepsSourceOrderAmongEffective(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "a", e.viewAt(3), "Reply by Monday."))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "b", e.viewAt(1), "Reply by Thursday."))
	e.sync(t)
	require.NoError(t, e.s.setDeadline(bg, DeadlineProgram, e.threadOf(t, "a@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "a@x")}))
	require.NoError(t, e.s.setDeadline(bg, DeadlineAI, e.threadOf(t, "b@x"), Deadline{Date: e.day(4), MessageID: e.idOf(t, "b@x")}))

	e.put(t, "INBOX", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(2), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "a@x"), e.threadOf(t, "b@x"))
	got := e.deadlineOf(t, "b@x")
	require.NotNil(t, got)
	assert.Equal(t, DeadlineProgram, got.Source, "an app's date outranks the summary's")
	assert.Equal(t, e.day(2), got.Date)
}

// Two of the user's own dates: A's ended when A was archived, B's was set
// after that. The merge keeps B's, though A's is earlier.
func TestDeadlineMergeKeepsTheManualOneInEffect(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "a", e.viewAt(5), "Reply by Monday."))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "b", e.viewAt(1), "Reply by Thursday."))
	e.sync(t)
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "a@x"), Deadline{Date: e.day(2)}))
	e.clock.Advance(time.Minute)
	_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "a@x")})
	require.NoError(t, err)
	require.Nil(t, e.deadlineOf(t, "a@x"), "archiving A ended its date")
	e.clock.Advance(time.Minute)
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "b@x"), Deadline{Date: e.day(4)}))

	e.put(t, "INBOX", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(0.5), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "a@x"), e.threadOf(t, "b@x"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	assert.Equal(t, &Deadline{Date: e.day(4), Source: DeadlineManual}, e.deadlineOf(t, "b@x"), "B's date is still in effect after the merge")
}

// The user's reply from a +tag address, synced into the inbox rather than
// Sent, ends A's app date; B's summary date is on newer mail and survives.
func TestDeadlineMergeCountsTheUsersTaggedAddress(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "a", e.viewAt(5), "Reply by Monday."))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "b", e.viewAt(1), "Reply by Thursday."))
	e.sync(t)
	require.NoError(t, e.s.setDeadline(bg, DeadlineProgram, e.threadOf(t, "a@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "a@x")}))
	require.NoError(t, e.s.setDeadline(bg, DeadlineAI, e.threadOf(t, "b@x"), Deadline{Date: e.day(4), MessageID: e.idOf(t, "b@x")}))
	e.put(t, "INBOX", eml("r@x", "alice+work@example.com", "bob@acme.example", "Re: a", e.viewAt(3), "Will do.", "In-Reply-To: <a@x>", "References: <a@x>"))
	e.sync(t)
	require.Nil(t, e.deadlineOf(t, "a@x"), "A's date ended with the reply")

	e.put(t, "INBOX", eml("m@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(2), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	require.Equal(t, e.threadOf(t, "a@x"), e.threadOf(t, "b@x"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	got := e.deadlineOf(t, "b@x")
	require.NotNil(t, got, "B's date is still in effect after the merge")
	assert.Equal(t, DeadlineAI, got.Source)
	assert.Equal(t, e.day(4), got.Date)
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
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q, Quote: "Friday"}))
	removed, err := e.s.RemoveAppAndWitDeadlines(bg)
	require.NoError(t, err)
	require.NoError(t, e.s.UndoRemoveAppAndWitDeadlines(bg, removed))
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), tid, Deadline{}))
	assert.Equal(t, before, state())
}

func TestDeadlineOverdue(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(3), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(0), MessageID: q}))
	assert.False(t, e.deadlineOf(t, "q@x").Overdue, "due by the end of today")
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(-1), Time: "23:59", MessageID: q}))
	assert.True(t, e.deadlineOf(t, "q@x").Overdue)
}

// A reply date's day and time are the user's, in the interface's zone, as the
// summary that wrote it read them, not the process's.
func TestDeadlineInUserZone(t *testing.T) {
	e := newSendEnv(t, false)
	if _, off := e.clock.Now().Zone(); off > 12*3600 {
		t.Skip("needs a zone at least 2 hours ahead of the clock's")
	}
	e.s.SetTimeZone("Pacific/Kiritimati") // UTC+14
	zone, err := time.LoadLocation("Pacific/Kiritimati")
	require.NoError(t, err)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(3), "Reply by Friday."))
	e.sync(t)
	tid := e.threadOf(t, "q@x")
	at := e.clock.Now().In(zone).Add(-time.Hour)
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: at.Format(time.DateOnly), Time: at.Format("15:04"), MessageID: e.idOf(t, "q@x")}))
	assert.True(t, e.deadlineOf(t, "q@x").Overdue, "an hour ago, there")
	got, err := e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.True(t, got.Deadline.Overdue)
}

// order is the subjects of a document's 其他, in the order it lists them.
func (e *sendEnv) order(t *testing.T, d SavedViewDoc) []string {
	t.Helper()
	got, err := e.s.PreviewView(bg, d, 0, 200)
	require.NoError(t, err)
	var out []string
	for _, g := range got.Groups {
		for _, r := range g.Items {
			out = append(out, r.Subject)
		}
	}
	return out
}

// dated puts five unreplied conversations, a newest, and gives four a reply
// date: c in 3 days, b tomorrow at 09:00, e 2 days overdue, d 9 days overdue.
func dated(t *testing.T) *sendEnv {
	e := newSendEnv(t, false)
	for i, s := range []string{"a", "b", "c", "d", "e"} {
		e.put(t, "INBOX", eml(s+"@x", "bob@a.example", "alice@example.com", s, e.viewAt(float64(i)+0.5), "Reply soon."))
	}
	e.sync(t)
	for s, d := range map[string]Deadline{"c": {Date: e.day(3)}, "b": {Date: e.day(1), Time: "09:00"}, "e": {Date: e.day(-2)}, "d": {Date: e.day(-9)}} {
		d.MessageID = e.idOf(t, s+"@x")
		require.NoError(t, e.setThreadDeadline(bg, e.threadOf(t, s+"@x"), d), s)
	}
	return e
}

// Every view lists conversations with a reply date first, soonest (so
// overdue) first; one more than 7 days overdue goes back among the rest,
// newest first, and its row stops showing it.
func TestViewSortsByReplyDate(t *testing.T) {
	e := dated(t)
	assert.Equal(t, []string{"e", "b", "c", "a", "d"}, e.order(t, SavedViewDoc{Match: MatchAll}))
	assert.Equal(t, e.day(-2), e.deadlineOf(t, "e@x").Date)
	assert.True(t, e.deadlineOf(t, "e@x").Overdue)
	assert.Nil(t, e.deadlineOf(t, "d@x"), "more than 7 days overdue")
}

// A row with a reply date keeps its 回复状态 only where the view's or group's
// conditions ask for it; elsewhere it says the date alone.
func TestViewRowDateWithoutStatus(t *testing.T) {
	e := dated(t)
	status := func(d SavedViewDoc) map[string]string {
		got, err := e.s.PreviewView(bg, d, 0, 200)
		require.NoError(t, err)
		out := map[string]string{}
		for _, g := range got.Groups {
			for _, r := range g.Items {
				out[r.Subject] = r.Status
			}
		}
		return out
	}
	from := cond(FieldFrom, OpContains, "a.example")
	assert.Equal(t, map[string]string{"a": ReplyStatusUnreplied, "b": "", "c": "", "d": ReplyStatusUnreplied, "e": ""}, status(flatDoc(MatchAll, from)))
	assert.Equal(t, map[string]string{"a": ReplyStatusUnreplied, "b": ReplyStatusUnreplied, "c": ReplyStatusUnreplied, "d": ReplyStatusUnreplied, "e": ReplyStatusUnreplied},
		status(SavedViewDoc{Match: MatchAll, Conditions: []RuleCondition{from}, Groups: []ViewGroup{{ID: "r", Name: "R", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}}}}))
}

// The reader reads a conversation's reply date outside any view, more than 7
// days overdue too; a removed one reads as none.
func TestThreadDeadline(t *testing.T) {
	e := dated(t)
	got, err := e.s.ThreadDeadline(bg, e.threadOf(t, "d@x"))
	require.NoError(t, err)
	assert.Equal(t, &Deadline{Date: e.day(-9), MessageID: e.idOf(t, "d@x"), Source: DeadlineProgram, Overdue: true}, got.Deadline)

	got, err = e.s.ThreadDeadline(bg, e.threadOf(t, "a@x"))
	require.NoError(t, err)
	assert.Equal(t, ReaderDeadline{Unreplied: true}, got, "none, on a conversation waiting on the user")
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), e.threadOf(t, "c@x"), Deadline{}))
	got, err = e.s.ThreadDeadline(bg, e.threadOf(t, "c@x"))
	require.NoError(t, err)
	assert.Nil(t, got.Deadline, "removed by the user")
}

// The reader knows, outside any view, that a conversation waits on someone
// else's reply, and for how many days.
func TestThreadDeadlineWaiting(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "Sent", eml("sent@x", "alias@example.com", "bob@a.example", "sent", e.clock.Now().Format(time.RFC1123Z), "hi"))
	e.sync(t)
	e.clock.Advance(4*24*time.Hour + time.Hour)
	got, err := e.s.ThreadDeadline(bg, e.threadOf(t, "sent@x"))
	require.NoError(t, err)
	assert.Equal(t, ReaderDeadline{Waiting: true, WaitingDays: 4}, got)
}

// Pages follow the order; a reply date written mid-paging reorders, so the
// next page starts over instead of repeating or skipping a row.
func TestViewPagingAcrossAReplyDate(t *testing.T) {
	e := dated(t)
	id, err := e.s.SaveView(bg, SavedView{Name: "All", Icon: "team", Doc: SavedViewDoc{Match: MatchAll}})
	require.NoError(t, err)
	first, err := e.s.ViewContents(bg, id, 2)
	require.NoError(t, err)
	page := first.Groups[0]
	var seen []string
	for {
		for _, r := range page.Items {
			seen = append(seen, r.Subject)
		}
		if page.NextCursor == "" {
			break
		}
		page, err = e.s.ViewGroupPage(bg, id, ViewGroupOther, page.NextCursor, 2)
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"e", "b", "c", "a", "d"}, seen)

	require.NoError(t, e.setThreadDeadline(bg, e.threadOf(t, "a@x"), Deadline{Date: e.day(2), MessageID: e.idOf(t, "a@x")}))
	again, err := e.s.ViewGroupPage(bg, id, ViewGroupOther, first.Groups[0].NextCursor, 2)
	require.NoError(t, err)
	assert.Equal(t, "e", again.Items[0].Subject, "a cursor from before the write starts over")
	next, err := e.s.ViewGroupPage(bg, id, ViewGroupOther, again.NextCursor, 2)
	require.NoError(t, err)
	assert.Equal(t, "a", next.Items[0].Subject)
}

// witOn gives e's service a default model on a fake openai service, for Wit.
func (e *sendEnv) witOn(t *testing.T) *testllm.Server {
	t.Helper()
	srv := testllm.Start(t, "openai")
	_, err := e.s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: srv.URL, APIKey: "k", Models: []string{"m1"}})
	require.NoError(t, err)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.DefaultModel = "x/m1"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	return srv
}

// Wit sets a reply date, source wit, once the user approves; 本次对话都允许
// approves its later calls, which still cannot replace or clear the user's own
// date. A call the checks refuse goes back to the model with no approval.
func TestDeadlineWit(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm the quote by Friday."))
	e.sync(t)
	srv := e.witOn(t)
	conv, err := e.s.NewConversation(bg, "ask", 0)
	require.NoError(t, err)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	set := func(args string) testllm.Turn {
		return testllm.Turn{Calls: []testllm.Call{{Name: "set_reply_deadline", Args: args}}}
	}
	ask := func(turns ...testllm.Turn) WitEvent {
		srv.Script(append(turns, testllm.Turn{Text: []string{"ok"}})...)
		ch, err := e.s.Ask(bg, conv.ID, "Bob needs a reply by Friday")
		require.NoError(t, err)
		evs := drainWit(t, ch)
		return evs[len(evs)-1]
	}

	last := ask(found("quote"), set(`{"id":1,"date":"`+e.day(3)+`","time":"15:00","quote":"by  Friday"}`))
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	a := last.Approvals[0]
	assert.True(t, a.SessionAllow, "no destination: grantable for the conversation")
	assert.Contains(t, a.Summary, "quote", "names the conversation")
	assert.Contains(t, a.Summary, "15:00")
	assert.Nil(t, e.deadlineOf(t, "q@x"), "nothing before the approval")

	require.NoError(t, e.s.ResolveApprovalForConversation(bg, a.ID))
	for waitTerminal(t, sub).Kind != "done" {
	}
	q := e.idOf(t, "q@x")
	assert.Equal(t, &Deadline{Date: e.day(3), Time: "15:00", MessageID: q, Quote: "by  Friday", Source: DeadlineWit}, e.deadlineOf(t, "q@x"))

	// Granted: a later call applies at once, and the run goes on.
	last = ask(set(`{"id":1,"date":"` + e.day(4) + `"}`))
	require.Len(t, last.Approvals, 1)
	assert.Equal(t, "done", last.Approvals[0].State)
	for waitTerminal(t, sub).Kind != "done" {
	}
	assert.Equal(t, &Deadline{Date: e.day(4), MessageID: q, Source: DeadlineWit}, e.deadlineOf(t, "q@x"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM audit_log WHERE tool = 'set_reply_deadline' AND decision = ?", decisionSession))

	// Refused by the checks, or by the user's own date, granted or not: no approval, no change.
	assert.Equal(t, "done", ask(set(`{"id":1,"date":"`+e.day(4)+`","quote":"by Monday"}`)).Kind)
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), e.threadOf(t, "q@x"), Deadline{Date: e.day(5)}))
	assert.Equal(t, "done", ask(set(`{"id":1,"date":"`+e.day(6)+`"}`), set(`{"id":1,"date":""}`)).Kind)
	assert.Equal(t, &Deadline{Date: e.day(5), Source: DeadlineManual}, e.deadlineOf(t, "q@x"))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM approvals WHERE tool = 'set_reply_deadline'"))
	reqs := srv.Requests()
	assert.Contains(t, string(reqs[len(reqs)-1].Messages), ErrDeadlineOutranked.Error(), "the model is told why")
}

// An approved set_reply_deadline is checked again when it runs and commits with
// the approval (ADR 0004): replied to since it was proposed, it fails, writes
// nothing, and is not recorded as having maybe acted outside.
func TestDeadlineWitRefusedOnApply(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm the quote by Friday."))
	e.sync(t)
	srv := e.witOn(t)
	conv, err := e.s.NewConversation(bg, "ask", 0)
	require.NoError(t, err)
	srv.Script(found("quote"), testllm.Turn{Calls: []testllm.Call{{Name: "set_reply_deadline", Args: `{"id":1,"date":"` + e.day(3) + `"}`}}}, testllm.Turn{Text: []string{"ok"}})
	ch, err := e.s.Ask(bg, conv.ID, "Bob needs a reply by Friday")
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.Len(t, evs[len(evs)-1].Approvals, 1)
	a := evs[len(evs)-1].Approvals[0]

	// The user sets their own date between the check and the commit: the commit fails.
	stmts, _, _, err := e.s.applyWitDeadline(bg, Approval{ID: a.ID, Args: json.RawMessage(store.Str(e.rows(t, "SELECT args FROM approvals WHERE id = ?", a.ID)[0][0]))})
	require.NoError(t, err)
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "q@x"), Deadline{Date: e.day(5)}))
	_, err = e.s.db.Transaction(bg, stmts)
	require.Error(t, err)
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "q@x"), Deadline{}))

	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "q@x"))
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	assert.ErrorIs(t, e.s.ResolveApproval(bg, a.ID, true), ErrDeadlineNotWaiting)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM thread_deadlines"))
	assert.Equal(t, [][]any{{"failed", ErrDeadlineNotWaiting.Error(), "tx"}}, e.rows(t, "SELECT state, result, exec_mode FROM approvals WHERE id = ?", a.ID))
}

// 全部去掉 in settings takes away every date other apps and Wit set, in
// effect or not, and nothing else; undoing it puts them back, but not over a
// date the user set since. Its count is of the dates in effect, as shown.
func TestRemoveAppAndWitDeadlines(t *testing.T) {
	e := newSendEnv(t, false)
	for _, id := range []string{"p", "w", "a", "m", "x"} {
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
	set(DeadlineProgram, "x@x")
	_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "x@x")})
	require.NoError(t, err, "x's date is no longer in effect")
	program := e.deadlineOf(t, "p@x")
	count := func() int {
		n, err := e.s.CountAppAndWitDeadlines(bg)
		require.NoError(t, err)
		return n
	}
	assert.Equal(t, 2, count())
	version := func() int64 {
		return int64(e.count(t, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'reply_dates_version'), 0)"))
	}
	v := version()

	removed, err := e.s.RemoveAppAndWitDeadlines(bg)
	require.NoError(t, err)
	assert.Len(t, removed, 3)
	assert.Nil(t, e.deadlineOf(t, "p@x"))
	assert.Nil(t, e.deadlineOf(t, "w@x"))
	assert.Equal(t, DeadlineAI, e.deadlineOf(t, "a@x").Source)
	assert.Equal(t, DeadlineManual, e.deadlineOf(t, "m@x").Source)
	assert.Zero(t, count())
	assert.Greater(t, version(), v, "paging cursors start over")
	v = version()

	// Meanwhile the user sets their own date on one: undo leaves it.
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), e.threadOf(t, "w@x"), Deadline{Date: e.day(5)}))
	v = version()
	require.NoError(t, e.s.UndoRemoveAppAndWitDeadlines(bg, removed))
	assert.Greater(t, version(), v, "paging cursors start over")
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
		require.NoError(t, e.setThreadDeadline(bg, e.threadOf(t, mid+"@x"), d), mid)
	}
	set("soon", Deadline{Date: e.day(2)})
	set("today", Deadline{Date: e.day(0)})
	set("passed", Deadline{Date: e.day(0), Time: "00:00"})
	set("later", Deadline{Date: e.day(3)})
	set("overdue", Deadline{Date: e.day(-1)})
	set("removed", Deadline{Date: e.day(1)})
	ui := WithUISession(t.Context(), e.s)
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "removed@x"), Deadline{}), "the user removes it")
	require.NoError(t, e.setThreadDeadline(ui, e.threadOf(t, "mine@x"), Deadline{Date: e.day(1)}), "the user's own date, on a conversation with no 回复状态")

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

// The user's own date outlives their reply ("收到，周五前给你") and ends when
// the conversation is archived after it was set; undoing the archive brings
// it back. An archive from before the date was set leaves it.
func TestManualDeadlineEndsOnArchive(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	ui := WithUISession(t.Context(), e.s)
	_, err := e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	e.clock.Advance(time.Minute)
	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{Date: e.day(4)}))
	want := &Deadline{Date: e.day(4), Source: DeadlineManual}
	got, err := e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.Equal(t, want, got.Deadline, "an archive before the date leaves it")

	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", q)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	got, err = e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.Equal(t, want, got.Deadline, "the user's reply leaves it")

	e.clock.Advance(time.Minute)
	undo, err := e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	got, err = e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.Nil(t, got.Deadline, "archiving ends it")
	require.NoError(t, e.s.UndoArchiveThreads(bg, undo))
	got, err = e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.Equal(t, want, got.Deadline, "undoing the archive brings it back")

	// A mark from before archive times were kept counts as older than any date.
	_, err = e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "UPDATE archive_marks SET archived_at = 0, archived_version = 0")
	require.NoError(t, err)
	got, err = e.s.ThreadDeadline(bg, tid)
	require.NoError(t, err)
	assert.Equal(t, want, got.Deadline)
}

// Every archive in Godwit, not only a list's or a view's, ends the user's own
// date and takes the conversation out of 回复状态; its undo brings both back.
func TestEveryArchiveEndsManualDeadline(t *testing.T) {
	for name, archive := range map[string]func(e *sendEnv, q int64) (undo func() error){
		"sender": func(e *sendEnv, _ int64) func() error {
			res, err := e.s.ArchiveSender(bg, "bob@a.example")
			require.NoError(t, err)
			require.Len(t, res.Moved, 1)
			return func() error { return e.s.UndoArchive(bg, res) }
		},
		"role": func(e *sendEnv, q int64) func() error { // Wit's archive_mail, a program's MoveToRole: no undo
			require.NoError(t, e.s.MoveToRole(bg, []int64{q}, "archive"))
			return nil
		},
		"rule": func(e *sendEnv, _ int64) func() error {
			id := e.saveRule(t, RuleDoc{Version: RuleVersion, Name: "archive", Trigger: TriggerReceive, Match: MatchAll,
				Conditions: []RuleCondition{cond(FieldFrom, OpContains, "bob@")}, Actions: []RuleAction{{Kind: ActionArchive}}}, true)
			n, err := e.s.RunRuleOnExisting(bg, id)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			return func() error {
				kept, err := e.s.UndoRuleRun(bg, e.runOf(t, "q@x"))
				assert.Zero(t, kept)
				return err
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSendEnv(t, false)
			e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday?"))
			e.sync(t)
			tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
			require.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind)
			require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), tid, Deadline{Date: e.day(4)}))
			want := &Deadline{Date: e.day(4), Source: DeadlineManual}
			deadline := func() *Deadline {
				got, err := e.s.ThreadDeadline(bg, tid)
				require.NoError(t, err)
				return got.Deadline
			}

			e.clock.Advance(time.Minute)
			undo := archive(e, q)
			assert.Nil(t, deadline(), "archiving ends it")
			assert.Empty(t, e.replyOf(t, "q@x").Kind, "and takes it out of 回复状态")
			if undo == nil {
				return
			}
			require.NoError(t, undo())
			assert.Equal(t, want, deadline(), "undoing the archive brings it back")
			assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind)
			assert.Zero(t, e.count(t, "SELECT count(*) FROM archive_marks"), "the mark it replaced: none")
		})
	}
}

// Removing a summary's or a program's date keeps programs out until the
// conversation is archived; after that, new mail can carry one again.
func TestRemovedDeadlineHoldsUntilArchive(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.s.setDeadline(bg, DeadlineAI, tid, Deadline{Date: e.day(2), MessageID: q}))
	require.NoError(t, e.setThreadDeadline(WithUISession(t.Context(), e.s), tid, Deadline{}), "去掉")
	assert.ErrorIs(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked)
	assert.ErrorIs(t, e.s.setDeadline(bg, DeadlineAI, tid, Deadline{Date: e.day(3), MessageID: q}), ErrDeadlineOutranked)

	e.clock.Advance(time.Minute)
	_, err := e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	e.put(t, "INBOX", eml("q2@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(0.5), "Now by Monday.", "In-Reply-To: <q@x>"))
	e.sync(t)
	require.Equal(t, tid, e.threadOf(t, "q2@x"))
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(4), MessageID: e.idOf(t, "q2@x")}), "archived since")
	assert.Equal(t, DeadlineProgram, e.deadlineOf(t, "q@x").Source)
}

// Setting, changing and removing a date each undo to the row as it was:
// none, another source's date with its quote, or the "removed" marker.
func TestUndoSetThreadDeadline(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	ui := WithUISession(t.Context(), e.s)
	row := func() string {
		rows, err := e.s.db.Query(bg, "SELECT COALESCE(due_date, 'NULL') || ' ' || source || ' ' || quote FROM thread_deadlines WHERE thread_id = ?", tid)
		require.NoError(t, err)
		if len(rows.Values) == 0 {
			return ""
		}
		return rows.Values[0][0].(string)
	}

	before, err := e.s.SetThreadDeadline(ui, tid, Deadline{Date: e.day(2)})
	require.NoError(t, err)
	assert.Equal(t, RemovedDeadline{ThreadID: tid}, before)
	require.NoError(t, e.s.UndoSetThreadDeadline(ui, before))
	assert.Empty(t, row(), "undoing a set over none")

	program := Deadline{Date: e.day(3), MessageID: q, Quote: "confirm by Friday"}
	require.NoError(t, e.setThreadDeadline(bg, tid, program))
	want := e.day(3) + " program confirm by Friday"
	for _, d := range []Deadline{{Date: e.day(1), Time: "15:00"}, {}} { // change, then remove
		before, err = e.s.SetThreadDeadline(ui, tid, d)
		require.NoError(t, err)
		require.NotEqual(t, want, row())
		require.NoError(t, e.s.UndoSetThreadDeadline(ui, before))
		assert.Equal(t, want, row())
		assert.Equal(t, DeadlineProgram, e.deadlineOf(t, "q@x").Source)
	}

	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{}))
	before, err = e.s.SetThreadDeadline(ui, tid, Deadline{Date: e.day(5)})
	require.NoError(t, err)
	require.NoError(t, e.s.UndoSetThreadDeadline(ui, before))
	assert.Equal(t, "NULL manual ", row(), "the marker comes back")
	assert.ErrorIs(t, e.setThreadDeadline(bg, tid, program), ErrDeadlineOutranked)
}

// Setting a date and archiving in the same second keep their order: the
// clock stands still here between the two.
func TestManualDeadlineArchiveOrderWithinASecond(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid := e.threadOf(t, "q@x")
	ui := WithUISession(t.Context(), e.s)
	deadline := func() *Deadline {
		got, err := e.s.ThreadDeadline(bg, tid)
		require.NoError(t, err)
		return got.Deadline
	}
	want := &Deadline{Date: e.day(4), Source: DeadlineManual}

	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{Date: e.day(4)}))
	undo, err := e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	assert.Nil(t, deadline(), "set, then archived: ended")
	require.NoError(t, e.s.UndoArchiveThreads(bg, undo))
	assert.Equal(t, want, deadline(), "undoing the archive brings it back")

	_, err = e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	require.NoError(t, e.setThreadDeadline(ui, tid, Deadline{Date: e.day(4)}))
	assert.Equal(t, want, deadline(), "archived, then set: in effect")

	_, err = e.s.ArchiveThreads(bg, []int64{tid})
	require.NoError(t, err)
	require.Nil(t, deadline())
	removed, err := e.s.SetThreadDeadline(ui, tid, Deadline{Date: e.day(5)})
	require.NoError(t, err)
	require.NoError(t, e.s.UndoSetThreadDeadline(ui, removed))
	assert.Nil(t, deadline(), "undoing a change brings back the date as it was: archived since")
}

// A reply draft synced from another device, only in Drafts, is not a reply:
// the conversation stays 未回复 and keeps its date, and programs may still write one.
func TestDeadlineKeptPastADraftReply(t *testing.T) {
	e := newSendEnv(t, false)
	require.NoError(t, e.im.User.Create("Drafts", &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{imap.MailboxAttrDrafts}}))
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Reply by Friday."))
	e.sync(t)
	tid, q := e.threadOf(t, "q@x"), e.idOf(t, "q@x")
	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(2), MessageID: q}))

	e.put(t, "Drafts", eml("d@x", "alice@example.com", "bob@a.example", "Re: quote", e.viewAt(0.5), "Will do.", "In-Reply-To: <q@x>"))
	e.sync(t)
	require.Equal(t, tid, e.threadOf(t, "d@x"))
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind)
	assert.NotNil(t, e.deadlineOf(t, "q@x"), "a draft is no reply")
	assert.NoError(t, e.setThreadDeadline(bg, tid, Deadline{Date: e.day(3), MessageID: q}))
}
