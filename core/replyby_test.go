package core

import (
	"encoding/json"
	"github.com/emersion/go-imap/v2"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A summary's two machine lines come out of it; the summary stays as written.
func TestParseReplyBy(t *testing.T) {
	r, rest, err := parseReplyBy("- Lena wants the quote confirmed.\n- She sent the price list.\nREPLY BY: 2026-11-28 15:00\nQUOTE: “Please confirm the quote by Friday 3pm.”\n")
	require.NoError(t, err)
	assert.Equal(t, replyBy{Date: "2026-11-28", Time: "15:00", Quote: "Please confirm the quote by Friday 3pm."}, r)
	assert.Equal(t, "- Lena wants the quote confirmed.\n- She sent the price list.", rest)

	r, rest, err = parseReplyBy("PROGRESS\n- Bob shipped <mail id=\"3\"/>\n  reply by:  2026-12-01  \n quote: Can you reply by December 1?")
	require.NoError(t, err, "case and spacing around the lines do not matter")
	assert.Equal(t, replyBy{Date: "2026-12-01", Quote: "Can you reply by December 1?"}, r)
	assert.Equal(t, "PROGRESS\n- Bob shipped <mail id=\"3\"/>", rest)

	r, rest, err = parseReplyBy("- We ship by Friday.\nREPLY BY: none\nQUOTE: \n")
	require.NoError(t, err)
	assert.Zero(t, r, "none")
	assert.Equal(t, "- We ship by Friday.", rest)
}

// Lines that are there but do not read are no date, and say why; the
// summary still comes back without them.
func TestParseReplyByMalformed(t *testing.T) {
	for text, want := range map[string]string{
		"- a": "no REPLY BY line",
		"- a\nREPLY BY: 28/11/2026\nQUOTE: by Friday":                        `date "28/11/2026"`,
		"- a\nREPLY BY: 2026-11-31\nQUOTE: by Friday":                        `date "2026-11-31"`,
		"- a\nREPLY BY: 2026-11-28 3pm\nQUOTE: by Friday":                    `time "3pm"`,
		"- a\nREPLY BY: 2026-11-28 9:30\nQUOTE: by 9:30":                     `time "9:30"`,
		"- a\nREPLY BY: 2026-11-28 24:00\nQUOTE: by 24h":                     `time "24:00"`,
		"- a\nREPLY BY: 2026-11-28 (Friday)\nQUOTE: by F":                    `time "(Friday)"`,
		"- a\nREPLY BY: \nQUOTE: ":                                           "empty REPLY BY line",
		"- a\nREPLY BY:\t \nQUOTE: by Friday":                                "empty REPLY BY line",
		"- a\nREPLY BY: 2026-11-28":                                          "no QUOTE line",
		"- a\nREPLY BY: 2026-11-28\nQUOTE: “ ”":                              "no QUOTE line",
		"- a\nREPLY BY: 2026-11-28\nREPLY BY: 2026-11-29\nQUOTE: by Friday":  "more than one REPLY BY line",
		"- a\nREPLY BY: 2026-11-28\nQUOTE: by Friday\nQUOTE: or by Saturday": "more than one QUOTE line",
	} {
		r, rest, err := parseReplyBy(text)
		require.ErrorContains(t, err, want, text)
		assert.Zero(t, r, text)
		assert.Equal(t, "- a", rest, text)
	}
}

// summarize is the summary of tg as the reader gets it, in the user's time
// zone zone.
func (e *sendEnv) summarize(t *testing.T, zone string, tg Target) string {
	t.Helper()
	ch, err := e.s.RunSkill(WithTimeZone(bg, zone), "summarize", tg)
	require.NoError(t, err)
	var b strings.Builder
	for ev := range ch {
		require.NotEqual(t, "error", ev.Kind, "%v", ev.Err)
		if ev.Kind == "text" {
			assert.NotContains(t, ev.Text, "REP", "no part of a machine line streams out")
			b.WriteString(ev.Text)
		}
	}
	return b.String()
}

// A summary of a message or a thread asks for the reply date in the user's
// time zone; a valid one becomes the conversation's 期限 (source ai), and
// the two lines never reach the reader, streamed or from the cache. Only the
// model's run writes it: a cache hit does not put back a date removed since.
func TestSummaryCarriesReplyDate(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm the quote by Friday."))
	e.sync(t)
	srv := e.witOn(t)
	srv.Script(testllm.Turn{Text: []string{"- Bob wants the quote confirmed.\nREP", "LY BY: " + e.day(3) + " 15:00\nQU", "OTE: “Please confirm the quote by Friday.”\n"}})
	q, tid := e.idOf(t, "q@x"), e.threadOf(t, "q@x")

	text := e.summarize(t, "Asia/Shanghai", Target{Kind: "message", ID: q})
	assert.Equal(t, "- Bob wants the quote confirmed.", strings.TrimSpace(text))
	assert.Equal(t, &Deadline{Date: e.day(3), Time: "15:00", MessageID: q, Quote: "Please confirm the quote by Friday.", Source: DeadlineAI}, e.deadlineOf(t, "q@x"))
	var system string
	require.NoError(t, json.Unmarshal(srv.Requests()[0].System, &system))
	assert.Contains(t, system, "REPLY BY: YYYY-MM-DD [HH:MM] | none\nQUOTE: ")
	assert.Contains(t, system, "calendar in Asia/Shanghai")
	assert.Contains(t, system, replyByDateNote, "the skill's no-ISO rule names its exception")

	require.NoError(t, e.setThreadDeadline(bg, tid, Deadline{MessageID: q})) // an app clears it
	require.Nil(t, e.deadlineOf(t, "q@x"))
	assert.Equal(t, text, e.summarize(t, "Asia/Shanghai", Target{Kind: "message", ID: q}), "from the cache, the same")
	assert.Len(t, srv.Requests(), 1)
	assert.Nil(t, e.deadlineOf(t, "q@x"), "a cache hit writes no date")
}

// A newer reply draft synced from another device is not the conversation's
// newest mail: the incoming message's summary still writes its date.
func TestSummaryReplyDatePastADraft(t *testing.T) {
	e := newSendEnv(t, false)
	require.NoError(t, e.im.User.Create("Drafts", &imap.CreateOptions{SpecialUse: []imap.MailboxAttr{imap.MailboxAttrDrafts}}))
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please confirm the quote by Friday."))
	e.put(t, "Drafts", eml("d@x", "alice@example.com", "bob@a.example", "Re: quote", e.viewAt(0.5), "Will do.", "In-Reply-To: <q@x>"))
	e.sync(t)
	q := e.idOf(t, "q@x")
	require.Equal(t, e.threadOf(t, "q@x"), e.threadOf(t, "d@x"))
	srv := e.witOn(t)
	srv.Script(testllm.Turn{Text: []string{"- Bob wants the quote confirmed.\nREPLY BY: " + e.day(3) + "\nQUOTE: Please confirm the quote by Friday.\n"}})

	e.summarize(t, "Asia/Shanghai", Target{Kind: "message", ID: q})
	got := e.deadlineOf(t, "q@x")
	require.NotNil(t, got, "the draft is not the newest mail")
	assert.Equal(t, e.day(3), got.Date)
	assert.Equal(t, DeadlineAI, got.Source)

	// The thread's summary, the draft last among its mail, checks and writes against q too.
	require.NoError(t, e.setThreadDeadline(bg, e.threadOf(t, "q@x"), Deadline{MessageID: q}))
	require.Nil(t, e.deadlineOf(t, "q@x"))
	srv.Script(testllm.Turn{Text: []string{"PROGRESS\n- Bob wants the quote confirmed.\nREPLY BY: " + e.day(4) + "\nQUOTE: Please confirm the quote by Friday.\n"}})
	e.summarize(t, "Asia/Shanghai", Target{Kind: "thread", ID: e.threadOf(t, "q@x")})
	got = e.deadlineOf(t, "q@x")
	require.NotNil(t, got, "a thread summary with the draft last")
	assert.Equal(t, e.day(4), got.Date)
}

// A tracking parameter the model never saw (linkRefs dropped it) does not
// keep a quote with that link from checking against the stored message.
func TestSummaryReplyDateTrackedLink(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(1), "Please reply at https://a.example/x?utm_source=email&id=7 by Friday."))
	e.sync(t)
	q := e.idOf(t, "q@x")
	srv := e.witOn(t)
	srv.Script(testllm.Turn{Text: []string{"- Bob wants a reply.\nREPLY BY: " + e.day(3) + "\nQUOTE: Please reply at https://a.example/x?id=7 by Friday.\n"}})

	e.summarize(t, "Asia/Shanghai", Target{Kind: "message", ID: q})
	got := e.deadlineOf(t, "q@x")
	require.NotNil(t, got, "the quote is the message's, links as the model got them")
	assert.Equal(t, e.day(3), got.Date)
}

// Lines that do not read or do not check give no date, and the summary as
// it was.
func TestSummaryReplyDateRefused(t *testing.T) {
	e := newSendEnv(t, false)
	answers := [][2]string{
		{"quote", "REPLY BY: " + e.day(3) + "\nQUOTE: Please confirm the price by Friday."},
		{"range", "REPLY BY: " + e.day(-30) + "\nQUOTE: Please confirm the quote by Friday."},
		{"none", "REPLY BY: none\nQUOTE:"},
		{"bad", "REPLY BY: Friday\nQUOTE: Please confirm the quote by Friday."},
	}
	var turns []testllm.Turn
	for _, a := range answers {
		e.put(t, "INBOX", eml(a[0]+"@x", "bob@a.example", "alice@example.com", a[0], e.viewAt(1), "Please confirm the quote by Friday."))
		turns = append(turns, testllm.Turn{Text: []string{"- Bob asks.\n" + a[1]}})
	}
	e.sync(t)
	e.witOn(t).Script(turns...)
	for _, a := range answers {
		assert.Equal(t, "- Bob asks.", strings.TrimSpace(e.summarize(t, "UTC", Target{Kind: "message", ID: e.idOf(t, a[0]+"@x")})), a[0])
		assert.Nil(t, e.deadlineOf(t, a[0]+"@x"), a[0])
	}
}

// A newer message's date replaces the older one's; a follow-up asking for
// no date keeps it, and so does a summary of the older message alone.
func TestSummaryReplyDateAcrossMessages(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", eml("q1@x", "bob@a.example", "alice@example.com", "quote", e.viewAt(3), "Please confirm the quote by Friday."))
	e.sync(t)
	srv := e.witOn(t)
	answer := func(lines string) testllm.Turn {
		return testllm.Turn{Text: []string{"PROGRESS\n- Bob asked <mail id=\"1\"/>\nTODO: confirm\n" + lines}}
	}
	srv.Script(answer("REPLY BY: " + e.day(3) + "\nQUOTE: Please confirm the quote by Friday."))
	e.summarize(t, "UTC", Target{Kind: "message", ID: e.idOf(t, "q1@x")})
	q1 := e.idOf(t, "q1@x")
	require.Equal(t, &Deadline{Date: e.day(3), MessageID: q1, Quote: "Please confirm the quote by Friday.", Source: DeadlineAI}, e.deadlineOf(t, "q1@x"))

	e.put(t, "INBOX", eml("q2@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(2), "Just a reminder.", "In-Reply-To: <q1@x>", "References: <q1@x>"))
	e.sync(t)
	tid := e.threadOf(t, "q1@x")
	require.Equal(t, tid, e.threadOf(t, "q2@x"))
	srv.Script(answer("REPLY BY: none\nQUOTE:"))
	assert.Equal(t, "PROGRESS\n- Bob asked <mail id=\"1\"/>\nTODO: confirm", strings.TrimSpace(e.summarize(t, "UTC", Target{Kind: "thread", ID: tid})))
	assert.Equal(t, q1, e.deadlineOf(t, "q1@x").MessageID, "a reminder keeps the date")

	e.put(t, "INBOX", eml("q3@x", "bob@a.example", "alice@example.com", "Re: quote", e.viewAt(1), "Change of plan: please confirm by Monday.", "In-Reply-To: <q2@x>", "References: <q1@x> <q2@x>"))
	e.sync(t)
	srv.Script(answer("REPLY BY: "+e.day(5)+"\nQUOTE: please confirm by Monday."),
		answer("REPLY BY: "+e.day(7)+"\nQUOTE: Just a reminder."))
	e.summarize(t, "UTC", Target{Kind: "thread", ID: tid})
	q3 := e.idOf(t, "q3@x")
	assert.Equal(t, &Deadline{Date: e.day(5), MessageID: q3, Quote: "please confirm by Monday.", Source: DeadlineAI}, e.deadlineOf(t, "q1@x"))
	e.summarize(t, "UTC", Target{Kind: "message", ID: e.idOf(t, "q2@x")})
	assert.Len(t, srv.Requests(), 4)
	assert.Equal(t, q3, e.deadlineOf(t, "q1@x").MessageID, "an older message's summary does not replace a newer one's date")
}

// The quote has to be in the newest message as the model got it, however its
// spaces fall; the date between the day before that message's date and a
// year after, on the user's calendar.
func TestReplyByCheck(t *testing.T) {
	long := "https://portal.example.com/quotes/2026/confirm?token=abcdefghijklmnopqrstuvwxyz0123456789&ref=mail"
	body := "Hi Alice,\n\nCould you confirm\nthe quote by tomorrow? Details: " + long + "\n\n> On Monday Bob wrote:\n> reply by November 2"
	// 2026-11-20 23:00 UTC: the 21st in Shanghai, the 20th in Los Angeles.
	newest := mailItem{MessageID: 5, Date: time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC),
		Block: ai.Untrusted("message:5", linkRefs("Subject: Quote\nFrom: Lena <lena@example.com>\nDate: 2026-11-20T23:00:00Z\n", 5)+"\n"+linkRefs(body, 5), false)}
	require.NotContains(t, newest.Block, "token=", "the long link went as a reference")
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)

	quote := "Could you confirm the quote by tomorrow?"
	for _, c := range []struct {
		r    replyBy
		loc  *time.Location
		want string // "" is valid
	}{
		{replyBy{Date: "2026-11-22", Quote: quote}, shanghai, ""}, // tomorrow, in Shanghai
		{replyBy{Date: "2026-11-21", Quote: quote}, la, ""},       // tomorrow, in Los Angeles
		{replyBy{Date: "2026-11-20", Quote: quote}, shanghai, ""}, // the day before
		{replyBy{Date: "2026-11-19", Quote: quote}, shanghai, "out of range"},
		{replyBy{Date: "2026-11-19", Quote: quote}, la, ""},
		{replyBy{Date: "2026-11-18", Quote: quote}, la, "out of range"},
		{replyBy{Date: "2027-11-21", Quote: quote}, shanghai, ""},
		{replyBy{Date: "2027-11-22", Quote: quote}, shanghai, "out of range"},
		{replyBy{Date: "2026-11-22", Quote: "Could  you confirm the quote\tby tomorrow?"}, shanghai, ""},
		{replyBy{Date: "2026-11-22", Quote: "Details: " + long}, shanghai, ""}, // the link as the reply has it, expanded
		{replyBy{Date: "2026-11-22", Quote: "Please confirm the quote by tomorrow."}, shanghai, "quote"},
		{replyBy{Date: "2026-11-02", Quote: "reply by November 2"}, shanghai, "out of range"}, // an old date in the quoted reply
	} {
		err := c.r.check(newest, c.loc)
		if c.want == "" {
			assert.NoError(t, err, "%+v in %s", c.r, c.loc)
		} else {
			assert.ErrorContains(t, err, c.want, "%+v in %s", c.r, c.loc)
		}
	}
}

// The two lines are held back however the stream is cut; an ordinary line cut
// across events stays whole, even when its next part starts like one of them.
func TestReplyByHoldStream(t *testing.T) {
	for name, events := range map[string][]string{
		"continued line":   {"- Please review the ", "quote: confirm the price by Friday.\n", "REPLY BY: none\nQUOTE: \n"},
		"lines cut midway": {"- a\nREP", "LY BY: 2026-11-28\nQU", "OTE: by Friday\n- b"},
		"one event":        {"- a\nREPLY BY: 2026-11-28\nQUOTE: by Friday\n- b"},
		"continued twice":  {"- x ", "rep", "ly by: tomorrow\n- y"},
	} {
		var h replyByHold
		var got string
		for _, e := range events {
			got += h.feed(e)
		}
		got += h.flush()
		assert.Equal(t, stripReplyBy(strings.Join(events, "")), got, name)
		assert.NotContains(t, got, "REPLY BY:", name)
	}
}
