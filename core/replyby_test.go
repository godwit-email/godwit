package core

import (
	"encoding/json"
	"fmt"
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

// The eval's lines go after a message's and a thread's summary prompt, and
// are cached apart; with none set, summarize asks as before.
func TestSummarizeAppendsReplyByLines(t *testing.T) {
	e := newSkillEnv(t)
	e.srv.Script(testllm.Turn{Text: []string{"- a"}}, testllm.Turn{Text: []string{"PROGRESS\n- x <mail id=\"1\"/>"}}, testllm.Turn{Text: []string{"- b"}})
	e.s.summaryAppend = fmt.Sprintf(replyByFormat, "Asia/Shanghai")
	e.run(t, "summarize", Target{Kind: "message", ID: e.msg})
	e.run(t, "summarize", Target{Kind: "thread", ID: 1})
	e.s.summaryAppend = ""
	e.run(t, "summarize", Target{Kind: "message", ID: e.msg})
	require.Len(t, e.srv.Requests(), 3, "no summary from the cache")
	var system [3]string
	for i, r := range e.srv.Requests() {
		require.NoError(t, json.Unmarshal(r.System, &system[i]))
	}
	assert.Contains(t, system[0], "REPLY BY: YYYY-MM-DD [HH:MM] | none\nQUOTE: ")
	assert.Contains(t, system[0], "Asia/Shanghai")
	assert.Contains(t, system[1], "PROGRESS")
	assert.Contains(t, system[1], "REPLY BY:")
	assert.NotContains(t, system[2], "REPLY BY")
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
