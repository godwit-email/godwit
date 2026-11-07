package core

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewAt is a date d days before the clock's now.
func (e *sendEnv) viewAt(d float64) string {
	return e.clock.Now().Add(-time.Duration(d * 24 * float64(time.Hour))).Format(time.RFC1123Z)
}

func cond(field, op, value string) RuleCondition {
	return RuleCondition{Field: field, Op: op, Value: value}
}

var (
	condUnreplied = cond(FieldReply, OpIs, ViewReplyUnreplied)
)

func flatDoc(match string, c ...RuleCondition) SavedViewDoc {
	return SavedViewDoc{Match: match, Conditions: c}
}

// shown is the subjects a document lists, by group id.
func (e *sendEnv) shown(t *testing.T, d SavedViewDoc) map[string][]string {
	t.Helper()
	got, err := e.s.PreviewView(bg, d, 0, 200)
	require.NoError(t, err)
	return subjectsOf(got)
}

func subjectsOf(c ViewContents) map[string][]string {
	out := map[string][]string{}
	for _, g := range c.Groups {
		for _, r := range g.Items {
			out[g.ID] = append(out[g.ID], r.Subject)
		}
		slices.Sort(out[g.ID])
	}
	return out
}

func (e *sendEnv) replyOf(t *testing.T, mid string) replyStatus {
	t.Helper()
	got, err := e.s.replyStatuses(bg, e.clock.Now().Unix())
	require.NoError(t, err)
	rows := e.rows(t, "SELECT DISTINCT thread_id FROM messages WHERE message_id = ?", mid) // it may have copies
	require.Len(t, rows, 1, mid)
	return got[rows[0][0].(int64)]
}

func TestSavedViewMembership(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "Anna <anna@acme.example>", "alice@example.com", "order", e.viewAt(1), "broken"))
	e.put(t, "INBOX", eml("b@x", "Ben <ben@other.example>", "alice@example.com", "lunch", e.viewAt(400), "tomorrow"))
	e.put(t, "Archive", eml("c@x", "Cora <cora@acme.example>", "alice@example.com", "old deal", e.viewAt(400), "done"))
	e.put(t, "Trash", eml("d@x", "Dan <dan@acme.example>", "alice@example.com", "binned", e.viewAt(1), "gone"))
	e.sync(t)

	got := e.shown(t, flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme.example")))
	assert.Equal(t, []string{"old deal", "order"}, got[ViewGroupOther], "old mail counts, trash doesn't")

	got = e.shown(t, flatDoc(MatchAny, cond(FieldFrom, OpContains, "ben@"), cond(FieldSubject, OpContains, "order")))
	assert.Equal(t, []string{"lunch", "order"}, got[ViewGroupOther])

	got = e.shown(t, flatDoc(MatchAll, cond(FieldAny, OpContains, "tomorrow")))
	assert.Equal(t, []string{"lunch"}, got[ViewGroupOther], "any content reads the body too")

	d := flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme.example"))
	d.Account = e.acc.ID + 1
	_, err := e.s.PreviewView(bg, d, 0, 50)
	assert.ErrorIs(t, err, ErrInvalidView, "an account that doesn't exist")
}

func TestSavedViewConditionsAcrossAConversation(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("t1@x", "bob@acme.example", "alice@example.com", "first", e.viewAt(3), "hello"))
	e.put(t, "INBOX", eml("t2@x", "carol@other.example", "alice@example.com", "Re: first with invoice", e.viewAt(2), "see", "In-Reply-To: <t1@x>"))
	e.put(t, "INBOX", eml("n1@x", "news@acme.example", "alice@example.com", "digest", e.viewAt(3), "news"))
	e.put(t, "Sent", eml("n2@x", "alice@example.com", "news@acme.example", "Re: digest", e.viewAt(2), "stop", "In-Reply-To: <n1@x>"))
	e.sync(t)

	spread := e.shown(t, flatDoc(MatchAll, cond(FieldFrom, OpContains, "bob@"), cond(FieldSubject, OpContains, "invoice")))
	assert.Empty(t, spread, "conditions spread over two messages don't meet 全部满足")

	any := e.shown(t, flatDoc(MatchAny, cond(FieldFrom, OpContains, "bob@"), cond(FieldSubject, OpContains, "invoice")))
	assert.Len(t, any[ViewGroupOther], 1)

	not := e.shown(t, flatDoc(MatchAll, cond(FieldTo, OpContains, "@"), cond(FieldFrom, OpNotContains, "news@")))
	assert.Equal(t, []string{"first"}, not[ViewGroupOther], "the user's own reply doesn't make 不包含 hold")
}

func TestViewGroupPages(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("u@x", "anna@acme.example", "alice@example.com", "needs me", e.viewAt(1), "?"))
	e.put(t, "Archive", eml("o@x", "kim@acme.example", "alice@example.com", "filed", e.viewAt(1), "ok"))
	e.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "o@x")}, []string{`\Seen`}, nil))

	unread := cond(FieldRead, OpIs, ViewReadUnread)
	d := SavedViewDoc{Match: MatchAll, Conditions: []RuleCondition{cond(FieldFrom, OpContains, "acme.example")}, Groups: []ViewGroup{
		{ID: "a", Name: "A", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}},
		{ID: "b", Name: "B", Match: MatchAll, Conditions: []RuleCondition{unread}},
		{ID: "empty", Name: "Empty", Match: MatchAll, Conditions: []RuleCondition{cond(FieldSubject, OpContains, "nothing here")}},
	}}
	got, err := e.s.PreviewView(bg, d, 0, 50)
	require.NoError(t, err)
	require.Len(t, got.Groups, 2, "an empty group is left out")
	assert.Equal(t, "a", got.Groups[0].ID, "it goes to the first group it meets")
	assert.Equal(t, 1, got.Groups[0].Total)
	assert.Equal(t, ReplyStatusUnreplied, got.Groups[0].Items[0].Status)
	assert.Equal(t, ViewGroupOther, got.Groups[1].ID, "the rest is in 其他, last")

	d.Groups[0], d.Groups[1] = d.Groups[1], d.Groups[0]
	assert.Equal(t, []string{"needs me"}, e.shown(t, d)["b"], "order decides")

	// No conditions is met by all, here as in a rule.
	d.Conditions = nil
	assert.Equal(t, []string{"filed"}, e.shown(t, d)[ViewGroupOther])
}

func TestSavedViewPaging(t *testing.T) {
	e := newSendEnv(t, false)
	for i := range 5 {
		id := string(rune('a'+i)) + "@x"
		e.put(t, "INBOX", eml(id, "anna@acme.example", "alice@example.com", "new "+id, e.viewAt(float64(i)+1), "x", "List-Id: <l.example>"))
	}
	e.put(t, "INBOX", eml("old@x", "anna@acme.example", "alice@example.com", "old one", e.viewAt(20), "direct"))
	e.sync(t)
	d := SavedViewDoc{Match: MatchAll, Conditions: []RuleCondition{cond(FieldFrom, OpContains, "acme.example")}, Groups: []ViewGroup{
		{ID: "reply", Name: "R", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}}}}
	id, err := e.s.SaveView(bg, SavedView{Name: "Acme", Icon: "team", Doc: d})
	require.NoError(t, err)

	got, err := e.s.ViewContents(bg, id, 2)
	require.NoError(t, err)
	require.Len(t, got.Groups, 2)
	assert.Equal(t, "old one", got.Groups[0].Items[0].Subject, "a match older than a whole page still shows in its group")

	other := got.Groups[1]
	assert.Equal(t, 5, other.Total)
	var seen []string
	page := other
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
	assert.Len(t, seen, 5)
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(seen))), 5, "pages don't repeat or skip")

	cursor := other.NextCursor
	_, err = e.s.SaveView(bg, SavedView{ID: id, Name: "Acme", Icon: "team", Doc: d})
	require.NoError(t, err)
	again, err := e.s.ViewGroupPage(bg, id, ViewGroupOther, cursor, 2)
	require.NoError(t, err)
	assert.Equal(t, other.Items[0].Subject, again.Items[0].Subject, "a cursor from before an edit starts over")
}

func TestReplyStatusDirectMail(t *testing.T) {
	e := newSendEnv(t, false)
	me := "alice@example.com"
	for _, c := range []struct {
		id, from, to string
		hdr          []string
		want         string
	}{
		{"to@x", "bob@a.example", me, nil, ReplyStatusUnreplied},
		{"other@x", "bob@a.example", "me@alias.example", nil, ""},
		{"plus@x", "bob@a.example", "alice+shop@example.com", nil, ReplyStatusUnreplied},
		{"bcc@x", "bob@a.example", "", nil, ReplyStatusUnreplied},
		{"cc@x", "bob@a.example", "carol@a.example", []string{"Cc: " + me}, ""},
		{"list@x", "bob@a.example", me, []string{"List-Id: <l.example>"}, ""},
		{"unsub@x", "bob@a.example", me, []string{"List-Unsubscribe: <https://x.example/u>"}, ""},
		{"auto@x", "bob@a.example", me, []string{"Auto-Submitted: auto-generated; x=1"}, ""},
		{"human@x", "bob@a.example", me, []string{"Auto-Submitted: no"}, ReplyStatusUnreplied},
		{"bulk@x", "bob@a.example", me, []string{"Precedence: Bulk"}, ""},
		{"plist@x", "bob@a.example", me, []string{"Precedence: list"}, ""},
		{"nr@x", "no-reply@a.example", me, nil, ""},
		{"nr2@x", "billing-noreply@a.example", me, nil, ""},
		{"rt@x", "bob@a.example", me, []string{"Reply-To: donotreply@a.example"}, ""},
		{"nr3@x", "cloud_no_reply@a.example", me, nil, ""},
		{"note@x", "notifications@a.example", me, nil, ""},
		{"news@x", "news@a.example", me, nil, ""},
		{"rcpt@x", "receipts+acct_1@a.example", me, nil, ""},
		{"role@x", "info@a.example", me, nil, ""},
		{"bulk2@x", "dana@email.a.example", me, nil, ""},
		{"edu@x", "dana@mail.uni.example", me, nil, ReplyStatusUnreplied},
	} {
		e.put(t, "INBOX", eml(c.id, c.from, c.to, "subject "+c.id, e.viewAt(1), "x", c.hdr...))
		defer func() { assert.Equal(t, c.want, e.replyOf(t, c.id).Kind, c.id) }()
	}
	e.sync(t)
}

// Mail to an alias counts once the user has sent from it, and what they wrote
// to or took part in is theirs to answer, from a role's address too.
func TestReplyStatusAliasesAndPeople(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("alias@x", "bob@a.example", "me@alias.example", "to my alias", e.viewAt(2), "?"))
	e.put(t, "INBOX", eml("stranger@x", "cold@a.example", "me@other.example", "to no alias", e.viewAt(2), "?"))
	e.put(t, "INBOX", eml("role@x", "support@known.example", "alice@example.com", "their answer", e.viewAt(2), "?"))
	e.put(t, "INBOX", eml("ask@x", "help@thread.example", "alice@example.com", "Re: my ticket", e.viewAt(2), "?", "In-Reply-To: <mine@x>"))
	e.put(t, "INBOX", eml("promo@x", "tom@a.example", "alice@example.com", "sale", e.viewAt(2), "Big sale. Unsubscribe here."))
	e.put(t, "INBOX", eml("friend@x", "pal@known.example", "alice@example.com", "fwd", e.viewAt(2), "see below: unsubscribe"))
	e.put(t, "Sent", eml("from-alias@x", "me@alias.example", "someone@a.example", "from my alias", e.viewAt(20), "hi"))
	e.put(t, "Sent", eml("wrote@x", "alice@example.com", "support@known.example, pal@known.example", "my question", e.viewAt(20), "hi"))
	e.put(t, "Sent", eml("mine@x", "alice@example.com", "desk@thread.example", "my ticket", e.viewAt(20), "hi"))
	e.sync(t)
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "alias@x").Kind, "an alias they sent from")
	assert.Empty(t, e.replyOf(t, "stranger@x").Kind, "an address they never used")
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "role@x").Kind, "a role they wrote to")
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "ask@x").Kind, "a role in a conversation they took part in")
	assert.Empty(t, e.replyOf(t, "promo@x").Kind, "a stranger's mail saying unsubscribe")
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "friend@x").Kind, "the word from someone they write to")
}

func TestOwnKey(t *testing.T) {
	assert.Equal(t, "wangbaran@gmail.com", ownKey("Baran <Wang.Baran+paypal@gmail.com>"))
	assert.Equal(t, "wangbaran@gmail.com", ownKey("wangbaran@googlemail.com"))
	assert.Equal(t, "a.b@example.com", ownKey("a.b+x@example.com"))
}

func TestReplyStatusReplying(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "question", e.viewAt(1), "?"))
	e.sync(t)
	require.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind)

	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "q@x"))
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "q@x").Kind, "it counts once queued, before any Sent copy")

	items, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, e.s.CancelOutbox(bg, items[0].ID))
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind, "undoing the send brings it back")

	// A later reply syncing in leaves it waiting.
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	require.Eventually(t, func() bool { return len(e.smtp.Messages()) == 1 }, 5*time.Second, 10*time.Millisecond)
	e.sync(t)
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "q@x").Kind)

	// Flagged \Answered elsewhere, with no Sent copy here.
	e.put(t, "INBOX", eml("elsewhere@x", "carol@a.example", "alice@example.com", "other", e.viewAt(1), "?"))
	e.sync(t)
	require.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "elsewhere@x").Kind)
	_, err = e.s.db.Execute(bg, `UPDATE messages SET flags = flags || ' \Answered' WHERE id = ?`, e.idOf(t, "elsewhere@x"))
	require.NoError(t, err)
	assert.Empty(t, e.replyOf(t, "elsewhere@x").Kind)
}

func TestReplyStatusFailedSendTakesItBack(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "question", e.viewAt(1), "?"))
	e.sync(t)
	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "q@x"))
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	_, err = e.s.db.Execute(bg, "UPDATE outbox SET state = 'failed'")
	require.NoError(t, err)
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "q@x").Kind)
}

func TestReplyStatusTime(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("in@x", "bob@a.example", "alice@example.com", "in", e.viewAt(29), "?"))
	e.put(t, "INBOX", eml("out@x", "bob@a.example", "alice@example.com", "out", e.viewAt(31), "?"))
	e.put(t, "Sent", eml("sent@x", "alias@example.com", "bob@a.example", "sent", e.clock.Now().Format(time.RFC1123Z), "hi"))
	e.sync(t)
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "in@x").Kind)
	assert.Empty(t, e.replyOf(t, "out@x").Kind, "older than the 30 days it reads")
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "sent@x").Kind)

	d := flatDoc(MatchAll, cond(FieldReply, OpOlderThan, "3"))
	assert.Empty(t, e.shown(t, d))
	e.clock.Advance(3*24*time.Hour - time.Minute)
	assert.Empty(t, e.shown(t, d), "not before N days")
	e.clock.Advance(time.Minute)
	assert.Equal(t, []string{"sent"}, e.shown(t, d)[ViewGroupOther], "at exactly N days")

	got, err := e.s.PreviewView(bg, d, 0, 50)
	require.NoError(t, err)
	assert.Equal(t, ReplyStatusWaiting, got.Groups[0].Items[0].Status)
	assert.Equal(t, 3, got.Groups[0].Items[0].WaitingDays)
	assert.Equal(t, []string{"bob@a.example"}, got.Groups[0].Items[0].Participants, "a row of only the user's mail names who it went to")

	// Elsewhere, what the user just sent carries no status.
	flat, err := e.s.PreviewView(bg, flatDoc(MatchAll, cond(FieldTo, OpContains, "bob@")), 0, 50)
	require.NoError(t, err)
	assert.Empty(t, flat.Groups[0].Items[0].Status)

	_, err = e.s.PreviewView(bg, flatDoc(MatchAll, cond(FieldReply, OpOlderThan, "30")), 0, 50)
	assert.ErrorIs(t, err, ErrInvalidView, "N stays under the 30 days read")
}

func TestReplyStatusArchiving(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("in@x", "bob@a.example", "alice@example.com", "ask", e.viewAt(1), "?"))
	e.put(t, "Sent", eml("only@x", "alice@example.com", "bob@a.example", "sent only", e.viewAt(5), "hi"))
	e.sync(t)
	inThread, onlyThread := e.threadOf(t, "in@x"), e.threadOf(t, "only@x")

	undo, err := e.s.ArchiveThreads(bg, []int64{inThread, onlyThread})
	require.NoError(t, err)
	assert.Empty(t, e.replyOf(t, "in@x").Kind, "archiving clears it")
	assert.Empty(t, e.replyOf(t, "only@x").Kind, "of only sent mail too")
	assert.Len(t, undo.Moved, 1, "Sent copies don't move")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages m JOIN message_locations l ON l.message_id = m.id AND l.shown JOIN folders f ON f.id = l.folder_id WHERE m.message_id = 'only@x' AND f.role = 'sent'"))

	require.NoError(t, e.s.UndoArchiveThreads(bg, undo))
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "in@x").Kind, "undo restores the folder and the mark")
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "only@x").Kind)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM archive_marks"))

	_, err = e.s.ArchiveThreads(bg, []int64{inThread})
	require.NoError(t, err)
	e.put(t, "INBOX", eml("again@x", "bob@a.example", "alice@example.com", "Re: ask", e.viewAt(0), "!", "In-Reply-To: <in@x>"))
	e.sync(t)
	st := e.replyOf(t, "in@x")
	assert.Equal(t, ReplyStatusUnreplied, st.Kind)
	assert.True(t, st.NewReply, "a newer message brings it back as a new reply")

	// Archived on another device, and filtered to a folder of its own.
	e.put(t, "Archive", eml("phone@x", "dan@a.example", "alice@example.com", "archived there", e.viewAt(1), "x"))
	require.NoError(t, e.im.User.Create("Clients", nil))
	e.put(t, "Clients", eml("filed@x", "eve@a.example", "alice@example.com", "filed here", e.viewAt(1), "x"))
	e.sync(t)
	assert.Empty(t, e.replyOf(t, "phone@x").Kind)
	assert.Equal(t, ReplyStatusUnreplied, e.replyOf(t, "filed@x").Kind, "a custom folder is not archive")
}

func TestArchivedUnreadStaysInItsGroup(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("in@x", "anna@acme.example", "alice@example.com", "ask", e.viewAt(1), "?"))
	e.sync(t)
	d := SavedViewDoc{Match: MatchAll, Conditions: []RuleCondition{cond(FieldFrom, OpContains, "acme")}, Groups: []ViewGroup{
		{ID: "r", Name: "R", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}},
		{ID: "u", Name: "U", Match: MatchAll, Conditions: []RuleCondition{cond(FieldRead, OpIs, ViewReadUnread)}},
	}}
	assert.Contains(t, e.shown(t, d), "r")
	_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "in@x")})
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"u": {"ask"}}, e.shown(t, d), "it matches the groups again")
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "in@x")}, []string{`\Seen`}, nil))
	assert.Equal(t, map[string][]string{ViewGroupOther: {"ask"}}, e.shown(t, d), "and falls to 其他 only when none fits")
}

func TestViewStateSurvivesAThreadMerge(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@acme.example", "alice@example.com", "one", e.viewAt(3), "x"))
	e.put(t, "INBOX", eml("b@x", "bob@acme.example", "alice@example.com", "two", e.viewAt(2), "y"))
	e.sync(t)
	ta, tb := e.threadOf(t, "a@x"), e.threadOf(t, "b@x")
	require.NotEqual(t, ta, tb)
	id, err := e.s.SaveView(bg, SavedView{Name: "Acme", Icon: "team", Doc: flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme"))})
	require.NoError(t, err)
	require.NoError(t, e.s.SetViewException(bg, id, ta, ViewExceptionAdded))
	require.NoError(t, e.s.SetViewException(bg, id, tb, ViewExceptionRemoved))
	_, err = e.s.ArchiveThreads(bg, []int64{tb})
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "INSERT INTO reply_records(thread_id, outbox_id, at) VALUES (?, 0, ?)", tb, e.clock.Now().Unix())
	require.NoError(t, err)

	e.put(t, "INBOX", eml("c@x", "bob@acme.example", "alice@example.com", "bridge", e.viewAt(1), "z", "References: <a@x> <b@x>"))
	e.sync(t)
	merged := e.threadOf(t, "c@x")
	require.Equal(t, merged, e.threadOf(t, "a@x"))
	require.Equal(t, merged, e.threadOf(t, "b@x"))

	got, err := e.s.ViewExceptions(bg, id)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, merged, got[0].ThreadID)
	assert.Equal(t, ViewExceptionRemoved, got[0].Kind, "removed wins a conflict")
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM archive_marks WHERE thread_id = ?", merged))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM reply_records WHERE thread_id = ?", merged))
}

func TestViewExceptions(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "in", e.viewAt(1), "x"))
	e.put(t, "INBOX", eml("b@x", "ben@other.example", "alice@example.com", "out", e.viewAt(1), "y"))
	e.sync(t)
	d := flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme"))
	id, err := e.s.SaveView(bg, SavedView{Name: "Acme", Icon: "team", Doc: d})
	require.NoError(t, err)
	contents := func() map[string][]string {
		got, err := e.s.ViewContents(bg, id, 50)
		require.NoError(t, err)
		return subjectsOf(got)
	}
	ta, tb := e.threadOf(t, "a@x"), e.threadOf(t, "b@x")
	require.NoError(t, e.s.SetViewException(bg, id, tb, ViewExceptionAdded))
	assert.Equal(t, []string{"in", "out"}, contents()[ViewGroupOther])

	preview, err := e.s.PreviewView(bg, d, id, 50)
	require.NoError(t, err)
	assert.Equal(t, contents(), subjectsOf(preview), "preview with the view applies its exceptions")

	require.NoError(t, e.s.SetViewException(bg, id, ta, ViewExceptionRemoved))
	assert.Equal(t, []string{"out"}, contents()[ViewGroupOther])
	assert.Zero(t, e.count(t, "SELECT count(*) FROM message_labels ml JOIN messages m ON m.id = ml.message_id WHERE m.thread_id = ?", ta))

	require.NoError(t, e.s.SetViewException(bg, id, ta, ""))
	require.NoError(t, e.s.SetViewException(bg, id, tb, ""))
	assert.Equal(t, []string{"in"}, contents()[ViewGroupOther])

}

func TestSavedViewToHandle(t *testing.T) {
	e := newSendEnv(t, false) // no language known
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "needs a reply", e.viewAt(1), "?"))
	e.put(t, "INBOX", eml("n@x", "news@a.example", "alice@example.com", "newsletter", e.viewAt(1), "x"))
	e.put(t, "Sent", eml("s@x", "alice@example.com", "bob@a.example", "asked", e.viewAt(4), "?"))
	e.sync(t)
	boxes, err := e.s.SavedViews(bg)
	require.NoError(t, err)
	require.Len(t, boxes, 1, "it is there from the first run, whatever the language")
	toHandle := boxes[0]
	assert.Equal(t, "chat-3", toHandle.Icon)

	// A view like any other: its conditions say what is in it, so nothing falls to 其他.
	got, err := e.s.ViewContents(bg, toHandle.ID, 50)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"reply": {"needs a reply"}, "wait": {"asked"}}, subjectsOf(got))

	// Added by hand, it shows in 其他, as anywhere.
	require.NoError(t, e.s.SetViewException(bg, toHandle.ID, e.threadOf(t, "n@x"), ViewExceptionAdded))
	got, err = e.s.ViewContents(bg, toHandle.ID, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"newsletter"}, subjectsOf(got)[ViewGroupOther])

	// It is seeded once: edits stay, and one deleted stays deleted.
	toHandle.Name = "Mine"
	_, err = e.s.SaveView(bg, toHandle)
	require.NoError(t, err)
	require.NoError(t, e.s.seedSavedViews(bg))
	boxes, err = e.s.SavedViews(bg)
	require.NoError(t, err)
	require.Len(t, boxes, 1)
	assert.Equal(t, "Mine", boxes[0].Name)
	require.NoError(t, e.s.DeleteView(bg, toHandle.ID))
	require.NoError(t, e.s.seedSavedViews(bg))
	boxes, err = e.s.SavedViews(bg)
	require.NoError(t, err)
	assert.Empty(t, boxes)

	tpls, err := e.s.ViewTemplates(bg)
	require.NoError(t, err)
	at := slices.IndexFunc(tpls, func(t ViewTemplate) bool { return t.ID == "to_handle" })
	require.GreaterOrEqual(t, at, 0, "it comes back from a template")
	id, err := e.s.SaveView(bg, SavedView{Name: tpls[at].Name, Icon: tpls[at].Icon, Doc: tpls[at].Doc})
	require.NoError(t, err)
	got, err = e.s.ViewContents(bg, id, 50)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{"reply": {"needs a reply"}, "wait": {"asked"}}, subjectsOf(got))
}

// Every view follows one rule: no conditions is met by all, its groups take
// what they meet, and the rest is in 其他.
func TestSavedViewNoConditionsMeansAll(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "one", e.viewAt(1), "x"))
	e.put(t, "Archive", eml("b@x", "ben@other.example", "alice@example.com", "two", e.viewAt(1), "y"))
	e.put(t, "Trash", eml("c@x", "cora@other.example", "alice@example.com", "binned", e.viewAt(1), "z"))
	e.sync(t)
	assert.Equal(t, map[string][]string{ViewGroupOther: {"one", "two"}}, e.shown(t, SavedViewDoc{Match: MatchAll}), "all mail, as a flat list")

	byFrom := ViewGroup{ID: "a", Name: "A", Match: MatchAll, Conditions: []RuleCondition{cond(FieldFrom, OpContains, "anna")}}
	assert.Equal(t, map[string][]string{"a": {"one"}, ViewGroupOther: {"two"}}, e.shown(t, SavedViewDoc{Match: MatchAll, Groups: []ViewGroup{byFrom}}))

	rest := ViewGroup{ID: "rest", Name: "Rest", Match: MatchAll}
	assert.Equal(t, map[string][]string{"a": {"one"}, "rest": {"two"}}, e.shown(t, SavedViewDoc{Match: MatchAll, Groups: []ViewGroup{byFrom, rest}}), "a group with no conditions takes the rest")
	assert.Equal(t, map[string][]string{"rest": {"one", "two"}}, e.shown(t, SavedViewDoc{Match: MatchAll, Groups: []ViewGroup{rest, byFrom}}), "order decides")
}

func TestSavedViewValidation(t *testing.T) {
	e := newSendEnv(t, false)
	group := func(g ViewGroup) SavedViewDoc {
		return SavedViewDoc{Match: MatchAll, Groups: []ViewGroup{g}}
	}
	for name, d := range map[string]SavedViewDoc{
		"unnamed group":     group(ViewGroup{ID: "a", Name: " ", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}}),
		"days out of range": flatDoc(MatchAll, cond(FieldReply, OpOlderThan, "0")),
		"huge value":        flatDoc(MatchAll, cond(FieldSubject, OpContains, strings.Repeat("x", MaxRuleValue+1))),
		"unknown field":     flatDoc(MatchAll, cond("nope", OpIs, "x")),
		"age":               flatDoc(MatchAll, cond(FieldAge, OpOlderThan, "3")),
	} {
		_, err := e.s.SaveView(bg, SavedView{Name: "X", Icon: "team", Doc: d})
		assert.ErrorIs(t, err, ErrInvalidView, name)
	}
	_, err := e.s.SaveView(bg, SavedView{Name: " ", Icon: "team", Doc: flatDoc(MatchAll, cond(FieldSubject, OpContains, "x"))})
	assert.ErrorIs(t, err, ErrInvalidView, "no name")
	for name, d := range map[string]SavedViewDoc{
		"no conditions": {Match: MatchAll},
		"state in view": flatDoc(MatchAny, condUnreplied, cond(FieldRead, OpIs, ViewReadUnread)),
		"empty group":   group(ViewGroup{ID: "a", Name: "A", Match: MatchAll}),
	} {
		_, err := e.s.SaveView(bg, SavedView{Name: "X", Icon: "team", Doc: d})
		assert.NoError(t, err, name)
	}
}

func TestSavedViewUnreadAndMarkRead(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "one", e.viewAt(1), "x"))
	e.put(t, "INBOX", eml("b@x", "anna@acme.example", "alice@example.com", "two", e.viewAt(1), "y"))
	e.put(t, "Trash", eml("c@x", "anna@acme.example", "alice@example.com", "Re: two", e.viewAt(0), "z", "In-Reply-To: <b@x>"))
	e.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "b@x")}, []string{`\Seen`}, nil))
	id, err := e.s.SaveView(bg, SavedView{Name: "Acme", Icon: "team", Doc: flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme"))})
	require.NoError(t, err)
	unread := func() int {
		boxes, err := e.s.SavedViews(bg)
		require.NoError(t, err)
		return boxes[slices.IndexFunc(boxes, func(b SavedView) bool { return b.ID == id })].Unread
	}
	assert.Equal(t, 1, unread(), "conversations with unread mail, trash aside")
	n, err := e.s.MarkViewRead(bg, id)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Zero(t, unread())
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM messages WHERE message_id = 'c@x' AND instr(flags, '\Seen') = 0`), "trash stays as it was")
}

func TestViewWhy(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "ask", e.viewAt(1), "?"))
	e.put(t, "INBOX", eml("b@x", "anna@acme.example", "alice@example.com", "fyi", e.viewAt(1), "x", "List-Id: <l.example>"))
	e.sync(t)
	byFrom := cond(FieldFrom, OpContains, "acme")
	id, err := e.s.SaveView(bg, SavedView{Name: "Acme", Icon: "team", Doc: SavedViewDoc{Match: MatchAll, Conditions: []RuleCondition{byFrom},
		Groups: []ViewGroup{{ID: "r", Name: "R", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}}}}})
	require.NoError(t, err)
	why, err := e.s.ViewWhy(bg, id, e.threadOf(t, "a@x"))
	require.NoError(t, err)
	assert.Equal(t, "R", why.GroupName)
	assert.Equal(t, condUnreplied, why.Condition)
	assert.Equal(t, ReplyStatusUnreplied, why.Status)

	why, err = e.s.ViewWhy(bg, id, e.threadOf(t, "b@x"))
	require.NoError(t, err)
	assert.Equal(t, ViewGroupOther, why.GroupID)
	assert.Equal(t, byFrom, why.Condition)
}

func TestFolderConditionUsesEffectiveRoles(t *testing.T) {
	e := newSendEnv(t, false)
	require.NoError(t, e.im.User.Create("Filed", nil))
	e.put(t, "Filed", eml("f@x", "bob@a.example", "alice@example.com", "filed", e.viewAt(1), "x"))
	e.put(t, "INBOX", eml("i@x", "bob@a.example", "alice@example.com", "inboxed", e.viewAt(1), "x"))
	e.sync(t)
	_, err := e.s.db.Execute(bg, "UPDATE folders SET role = '' WHERE role = 'archive'")
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "UPDATE folders SET role_override = 'archive' WHERE name = 'Filed'")
	require.NoError(t, err)

	// A rule reads the role the user gave the folder, as the app does.
	doc := RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{cond(FieldFolder, OpIs, "archive")}, Actions: []RuleAction{{Kind: ActionRead}}}
	p, err := e.s.PreviewRule(bg, doc, 50)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Count)
	m, ok, err := e.s.ruleMail(bg, e.idOf(t, "f@x"))
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, doc.matches(m))

	assert.Equal(t, []string{"filed"}, e.shown(t, flatDoc(MatchAll, cond(FieldFolder, OpIs, "archive")))[ViewGroupOther])

	// Gmail: All Mail stands in for archive only after INBOX is removed.
	g := newSendEnv(t, false, testmail.Gmail())
	g.im.AddGmail(eml("i@x", "bob@a.example", "alice@example.com", "inboxed", g.viewAt(1), "x"), nil, "INBOX", testmail.GmailAllMail)
	g.im.AddGmail(eml("g@x", "bob@a.example", "alice@example.com", "only all mail", g.viewAt(1), "x"), nil, testmail.GmailAllMail)
	g.sync(t)
	got := g.shown(t, flatDoc(MatchAll, cond(FieldFolder, OpIs, "archive")))
	assert.Equal(t, []string{"only all mail"}, got[ViewGroupOther])
	assert.Equal(t, ReplyStatusUnreplied, g.replyOf(t, "i@x").Kind, "one message's memberships count once")
}

func TestReplyStatusGmailCopies(t *testing.T) {
	e := newSendEnv(t, false, testmail.Gmail())
	require.NoError(t, e.im.User.Create("Label", nil))
	day := e.viewAt(1)
	// The Sent membership identifies mail sent from an alias as the user's.
	e.im.AddGmail(eml("s@x", "alias@else.example", "bob@a.example", "from alias", day, "x"), nil, testmail.GmailSent, "Label")
	e.sync(t)
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "s@x").Kind)

	// Discovering another membership after an archive isn't a new reply.
	m := e.im.AddGmail(eml("c@x", "bob@a.example", "alice@example.com", "ask", day, "?"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "c@x")})
	require.NoError(t, err)
	m.Label("Label")
	e.sync(t)
	assert.Empty(t, e.replyOf(t, "c@x").Kind)
}

func TestSavedSearchMatchesTheSearch(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "bob@a.example", "alice@example.com", "subject match", e.viewAt(1), "nothing"))
	e.put(t, "INBOX", eml("b@x", "bob@a.example", "alice@example.com", "other", e.viewAt(1), "match in the body"))
	e.put(t, "INBOX", eml("c@x", "match@a.example", "alice@example.com", "third", e.viewAt(1), "z"))
	e.put(t, "INBOX", eml("d@x", "eve@a.example", "alice@example.com", "fourth", e.viewAt(1), "z"))
	e.sync(t)
	hits, err := e.s.SearchWith(bg, SearchQuery{Text: "match"}, 200)
	require.NoError(t, err)
	var want []string
	for _, h := range hits {
		want = append(want, h.Subject)
	}
	slices.Sort(want)
	assert.Equal(t, want, e.shown(t, flatDoc(MatchAll, cond(FieldAny, OpContains, "match")))[ViewGroupOther])
}

func TestSavedViewNegativeLabelAndFolder(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "labelled", e.viewAt(1), "x"))
	e.put(t, "Archive", eml("b@x", "anna@acme.example", "alice@example.com", "archived", e.viewAt(1), "y"))
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "News", Color: "blue", Icon: "newspaper"})
	require.NoError(t, err)
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "a@x")}, l.ID, true))
	acme := cond(FieldFrom, OpContains, "acme")
	assert.Equal(t, []string{"archived"}, e.shown(t, flatDoc(MatchAll, acme, cond(FieldLabel, OpNotContains, l.Slug)))[ViewGroupOther])
	assert.Equal(t, []string{"labelled"}, e.shown(t, flatDoc(MatchAll, acme, cond(FieldFolder, OpNotContains, "archive")))[ViewGroupOther])
}

func TestReplyStatusWaitingBoundaries(t *testing.T) {
	for _, n := range []int{1, 29} {
		e := newSendEnv(t, false)
		e.put(t, "Sent", eml("s@x", "alice@example.com", "bob@a.example", "sent", e.clock.Now().Format(time.RFC1123Z), "x"))
		e.sync(t)
		d := flatDoc(MatchAll, cond(FieldReply, OpOlderThan, strconv.Itoa(n)))
		e.clock.Advance(time.Duration(n)*24*time.Hour - time.Minute)
		assert.Empty(t, e.shown(t, d), "before %d days", n)
		e.clock.Advance(time.Minute)
		assert.Equal(t, []string{"sent"}, e.shown(t, d)[ViewGroupOther], "at %d days", n)
	}
}

func TestReplyStatusCopiesInEitherOrder(t *testing.T) {
	for _, folders := range [][2]string{{"Sent", "Label"}, {"Label", "Sent"}} {
		e := newSendEnv(t, false)
		require.NoError(t, e.im.User.Create("Label", nil))
		day := e.viewAt(1)
		e.put(t, folders[0], eml("s@x", "alias@else.example", "bob@a.example", "from alias", day, "x"))
		e.sync(t)
		e.put(t, folders[1], eml("s@x", "alias@else.example", "bob@a.example", "from alias", day, "x"))
		e.sync(t)
		assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "s@x").Kind, "%v", folders)
	}
}

func TestReplyFromAnotherAccountClearsUnreplied(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "question", e.viewAt(1), "?"))
	e.sync(t)
	tid := e.threadOf(t, "q@x")
	// A reply queued from an account with no Sent copy here: only its record says so.
	_, err := e.s.db.Execute(bg, "INSERT INTO reply_records(thread_id, outbox_id, at) VALUES (?, 0, ?)", tid, e.clock.Now().Unix())
	require.NoError(t, err)
	assert.Equal(t, ReplyStatusWaiting, e.replyOf(t, "q@x").Kind)
}

// A saved search shows what the search does, for each filter it maps.
func TestSavedSearchEachFilter(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "Anna <anna@acme.example>", "alice@example.com", "one", e.viewAt(1), "x"))
	e.put(t, "INBOX", eml("b@x", "ben@other.example", "alice@example.com", "two", e.viewAt(1), "y"))
	e.put(t, "INBOX", attachmentEml("c@x", "cora@other.example", "three"))
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "News", Color: "blue", Icon: "newspaper"})
	require.NoError(t, err)
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "b@x")}, l.ID, true))
	for name, c := range map[string]struct {
		q SearchQuery
		d SavedViewDoc
	}{
		"from part": {SearchQuery{From: "acme"}, flatDoc(MatchAll, cond(FieldFrom, OpContains, "acme"))},
		"from addr": {SearchQuery{FromAddr: "anna@acme.example"}, flatDoc(MatchAll, cond(FieldFrom, OpIs, "anna@acme.example"))},
		"label":     {SearchQuery{LabelID: l.ID}, flatDoc(MatchAll, cond(FieldLabel, OpIs, l.Slug))},
		"attached":  {SearchQuery{HasAttachments: true}, flatDoc(MatchAll, cond(FieldAttachments, OpIs, RuleYes))},
	} {
		hits, err := e.s.SearchWith(bg, c.q, 200)
		require.NoError(t, err, name)
		var want []string
		for _, h := range hits {
			want = append(want, h.Subject)
		}
		assert.Equal(t, want, e.shown(t, c.d)[ViewGroupOther], name)
		assert.Len(t, want, 1, name)
	}
}

func attachmentEml(id, from, subject string) string {
	return "Message-ID: <" + id + ">\r\nFrom: " + from + "\r\nTo: alice@example.com\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nx\r\n--B--\r\n"
}

// How long the sidebar and a list take on a large view. A number to read,
// not a pass or fail: set GODWIT_VIEW_BENCH to run it.
func TestSavedViewLargeViewTimes(t *testing.T) {
	if os.Getenv("GODWIT_VIEW_BENCH") == "" {
		t.Skip("GODWIT_VIEW_BENCH not set")
	}
	const threads, perThread = 50000, 3
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("seed@x", "seed@acme.example", "alice@example.com", "seed", e.viewAt(1), "x"))
	e.sync(t)
	folder := e.rows(t, "SELECT "+shownCol+", account_id FROM messages LIMIT 1")[0]
	now := e.clock.Now().Unix()
	const batch = 500
	for start := 0; start < threads; start += batch {
		var stmts []sqlite.Statement
		for i := start; i < start+batch; i++ {
			date := now - int64(i)*600 // every 10 minutes: most of it years old
			from := fmt.Sprintf("p%d@other%d.example", i%50, i%200)
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO threads(id, account_id, subject, last_date) VALUES (?, ?, ?, ?)",
				Args: []any{1000 + i, folder[1], fmt.Sprintf("subject %d", i), date}})
			for j := range perThread {
				stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO messages(account_id, thread_id, message_id, from_addr, to_addrs, cc_addrs, subject, date, flags, snippet, body_text)
					VALUES (?, ?, ?, ?, '["alice@example.com"]', '[]', ?, ?, '\Seen', 'snippet', 'body text')`,
					Args: []any{folder[1], 1000 + i, fmt.Sprintf("m%d-%d@x", i, j), from, fmt.Sprintf("subject %d", i), date - int64(j)}},
					sqlite.Statement{SQL: `INSERT INTO message_locations(message_id, folder_id, shown, uid, entered_at, date)
						VALUES (last_insert_rowid(), ?, 1, ?, 0, ?)`, Args: []any{folder[0], 1000 + i*10 + j, date - int64(j)}})
			}
		}
		_, err := e.s.db.Transaction(bg, stmts)
		require.NoError(t, err)
	}
	_, err := e.s.SaveView(bg, SavedView{Name: "Text", Icon: "team", Doc: SavedViewDoc{Match: MatchAll,
		Conditions: []RuleCondition{cond(FieldFrom, OpContains, "other7.example")},
		Groups:     []ViewGroup{{ID: "r", Name: "R", Match: MatchAll, Conditions: []RuleCondition{condUnreplied}}}}})
	require.NoError(t, err)
	_, err = e.s.SaveView(bg, SavedView{Name: "Any", Icon: "team", Doc: flatDoc(MatchAny, cond(FieldAny, OpContains, "subject 77"), cond(FieldTo, OpContains, "nobody"))})
	require.NoError(t, err)
	timed := func(name string, f func() error) {
		start := time.Now()
		require.NoError(t, f())
		t.Logf("%-28s %v", name, time.Since(start))
	}
	boxes, err := e.s.SavedViews(bg)
	require.NoError(t, err)
	timed("sidebar, 3 views", func() error { _, err := e.s.SavedViews(bg); return err })
	timed("replyStatuses", func() error { _, err := e.s.replyStatuses(bg, now); return err })
	for _, b := range boxes {
		timed("contents of "+b.Name, func() error { _, err := e.s.ViewContents(bg, b.ID, 50); return err })
	}
}

// (A or B) and C holds on one message, and 待处理 leaves out the labels nobody replies to.
func TestViewConditionGroups(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("a@x", "anna@acme.example", "alice@example.com", "invoice", e.viewAt(2), "x"))
	e.put(t, "INBOX", eml("b@x", "ben@other.example", "alice@example.com", "invoice too", e.viewAt(2), "y"))
	e.put(t, "INBOX", eml("c1@x", "anna@acme.example", "alice@example.com", "hello", e.viewAt(3), "z"))
	e.put(t, "INBOX", eml("c2@x", "cora@other.example", "alice@example.com", "Re: hello invoice", e.viewAt(2), "z", "In-Reply-To: <c1@x>"))
	e.sync(t)
	grouped := func(g ...RuleCondition) RuleCondition { return RuleCondition{Match: MatchAny, Group: g} }
	d := flatDoc(MatchAll, grouped(cond(FieldFrom, OpContains, "anna@"), cond(FieldFrom, OpContains, "dan@")), grouped(cond(FieldSubject, OpContains, "invoice")))
	assert.Equal(t, []string{"invoice"}, e.shown(t, d)[ViewGroupOther], "spread over two messages, it doesn't hold")

	for name, bad := range map[string]SavedViewDoc{
		"one group":    flatDoc(MatchAll, grouped(cond(FieldSubject, OpContains, "x"))),
		"mixed":        flatDoc(MatchAll, grouped(cond(FieldSubject, OpContains, "x")), cond(FieldSubject, OpContains, "y")),
		"nested":       flatDoc(MatchAll, grouped(grouped(cond(FieldSubject, OpContains, "x"))), grouped(cond(FieldSubject, OpContains, "y"))),
		"empty inside": flatDoc(MatchAll, grouped(cond(FieldSubject, OpContains, "")), grouped(cond(FieldSubject, OpContains, "y"))),
	} {
		_, err := e.s.PreviewView(bg, bad, 0, 50)
		assert.ErrorIs(t, err, ErrInvalidView, name)
	}
}

func TestToHandleLeavesOutLabels(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "a question", e.viewAt(1), "?"))
	e.put(t, "INBOX", eml("o@x", "carol@a.example", "alice@example.com", "your code", e.viewAt(1), "123456"))
	e.sync(t)
	boxes, err := e.s.SavedViews(bg)
	require.NoError(t, err)
	before, err := e.s.ViewContents(bg, boxes[0].ID, 50)
	require.NoError(t, err)
	assert.Len(t, subjectsOf(before)["reply"], 2, "no language was known, so no labels to leave out")

	_, err = e.s.db.Execute(bg, "INSERT INTO labels(slug, name, description, color, icon, builtin, position) VALUES ('otp', 'OTP', '', 'red', 'bill', 1, 0)")
	require.NoError(t, err)
	l := e.rows(t, "SELECT id FROM labels WHERE slug = 'otp'")[0][0].(int64)
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "o@x")}, l, true))
	tpls, err := e.s.ViewTemplates(bg)
	require.NoError(t, err)
	toHandle := tpls[slices.IndexFunc(tpls, func(t ViewTemplate) bool { return t.ID == "to_handle" })]
	assert.Equal(t, map[string][]string{"reply": {"a question"}}, e.shown(t, toHandle.Doc), "the template leaves out the labels there are")
}

func TestOwnSentMailIsNotUnread(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "Sent", eml("s@x", "alice@example.com", "bob@a.example", "sent", e.viewAt(1), "x"))
	e.sync(t)
	assert.Empty(t, e.shown(t, flatDoc(MatchAll, cond(FieldRead, OpIs, ViewReadUnread))))
}

func TestViewDateRange(t *testing.T) {
	loc := time.FixedZone("x", 8*3600)
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, loc) // a Wednesday
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, loc) }
	for value, want := range map[string][2]time.Time{
		ViewDateToday:     {day(11, 4), day(11, 5)},
		ViewDateYesterday: {day(11, 3), day(11, 4)},
		ViewDateThisWeek:  {day(11, 2), day(11, 9)},
		ViewDateLastWeek:  {day(10, 26), day(11, 2)},
		ViewDateThisMonth: {day(11, 1), day(12, 1)},
		ViewDateLastMonth: {day(10, 1), day(11, 1)},
		"7":               {day(10, 29), day(11, 5)},
		"1":               {day(11, 4), day(11, 5)},
	} {
		start, end := viewDateRange(now, value)
		assert.True(t, start.Equal(want[0]) && end.Equal(want[1]), "%s: %v to %v", value, start, end)
	}
	sunday := time.Date(2026, 11, 8, 23, 0, 0, 0, loc)
	start, _ := viewDateRange(sunday, ViewDateThisWeek)
	assert.True(t, start.Equal(day(11, 2)), "a week starts on Monday, Sunday is its end")
}

func TestViewDateStarredAndWrote(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("new@x", "pal@a.example", "alice@example.com", "new", e.viewAt(0), "x"))
	e.put(t, "INBOX", eml("old@x", "stranger@a.example", "alice@example.com", "old", e.viewAt(40), "y"))
	e.put(t, "Sent", eml("me@x", "alice@example.com", "Pal <pal@a.example>", "mine", e.viewAt(90), "z"))
	e.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "old@x")}, []string{`\Flagged`}, nil))
	shown := func(c RuleCondition) []string { return e.shown(t, flatDoc(MatchAll, c))[ViewGroupOther] }

	assert.Equal(t, []string{"new"}, shown(cond(FieldDate, OpIs, "7")))
	assert.Equal(t, []string{"mine", "old"}, shown(cond(FieldDate, OpNotContains, "7")))
	assert.Equal(t, []string{"new"}, shown(cond(FieldDate, OpIs, ViewDateToday)))
	assert.Equal(t, []string{"old"}, shown(cond(FieldStarred, OpIs, RuleYes)))
	assert.Equal(t, []string{"mine", "new"}, shown(cond(FieldStarred, OpIs, RuleNo)))
	assert.Equal(t, []string{"mine", "new"}, shown(cond(FieldWrote, OpIs, RuleYes)), "who they wrote to, by name or not, and their own")
	assert.Equal(t, []string{"old"}, shown(cond(FieldWrote, OpIs, RuleNo)))

	d := flatDoc(MatchAll, cond(FieldFrom, OpContains, "stranger"), cond(FieldDate, OpIs, "7"))
	assert.Empty(t, e.shown(t, d), "on the same message, as other conditions")

	for name, c := range map[string]RuleCondition{
		"unknown date": cond(FieldDate, OpIs, "someday"),
		"no days":      cond(FieldDate, OpIs, "0"),
		"too many":     cond(FieldDate, OpIs, "3651"),
		"date op":      cond(FieldDate, OpContains, "7"),
		"star value":   cond(FieldStarred, OpIs, "maybe"),
		"wrote op":     cond(FieldWrote, OpContains, RuleYes),
	} {
		_, err := e.s.PreviewView(bg, flatDoc(MatchAll, c), 0, 50)
		assert.ErrorIs(t, err, ErrInvalidView, name)
	}
	_, err := e.s.SaveRule(bg, Rule{Doc: RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{cond(FieldDate, OpIs, "7")}, Actions: []RuleAction{{Kind: ActionRead}}}})
	assert.Error(t, err, "a rule runs once: 今天 means nothing to it")
}
