package core

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplyEvalLive scores the reply dates summaries give against the cases
// in GODWIT_LIVE_REPLY_CASES (default testdata/replyeval, whose README has
// the format, the threshold and how to label a real set). Each small model
// runs only with its settings set, and keys are never logged:
//
//	GODWIT_LIVE_OPENAI_KEY=… GODWIT_LIVE_REPLY_MODELS=gpt-…,gpt-… \
//	GODWIT_LIVE_LOCAL_URL=http://localhost:11434/v1 GODWIT_LIVE_LOCAL_MODELS=qwen3:4b \
//	GODWIT_LIVE_REPLY_CASES=~/godwit-reply-cases GODWIT_LIVE_REPLY_SAMPLES=~/godwit-reply-samples \
//	  go test ./core -run TestReplyEvalLive -v -timeout 3h
func TestReplyEvalLive(t *testing.T) {
	var ways []replyWay
	var skipped []string
	add := func(prefix, env, base, key, models string) {
		if base == "" || models == "" {
			skipped = append(skipped, prefix+": "+env+" not set")
			return
		}
		for m := range strings.SplitSeq(models, ",") {
			ways = append(ways, replyModelWay(prefix+"-"+strings.TrimSpace(m), base, key, strings.TrimSpace(m)))
		}
	}
	if key := os.Getenv("GODWIT_LIVE_OPENAI_KEY"); key == "" {
		skipped = append(skipped, "openai: GODWIT_LIVE_OPENAI_KEY not set")
	} else {
		add("openai", "GODWIT_LIVE_REPLY_MODELS", "https://api.openai.com/v1", key, os.Getenv("GODWIT_LIVE_REPLY_MODELS"))
	}
	add("local", "GODWIT_LIVE_LOCAL_URL and GODWIT_LIVE_LOCAL_MODELS", os.Getenv("GODWIT_LIVE_LOCAL_URL"), "", os.Getenv("GODWIT_LIVE_LOCAL_MODELS"))
	if len(ways) == 0 {
		t.Skip("no GODWIT_LIVE_* model set")
	}
	cases, err := loadReplySuite(cmp.Or(os.Getenv("GODWIT_LIVE_REPLY_CASES"), "testdata/replyeval"))
	require.NoError(t, err, "no model was called")
	dir, sample := os.Getenv("GODWIT_LIVE_REPLY_SAMPLES"), 0
	if dir != "" {
		sample = replySampleEvery
	}
	runs := runReplyEval(t, cases, ways, sample)
	if dir != "" {
		require.NoError(t, writeReplySamples(dir, runs))
		skipped = append(skipped, "nothing: summaries sampled to "+dir)
	}
	t.Log("\n" + replyReport(runs, skipped))
}

// replyModelWay is a model service at base with the one model, as the
// default and small model.
func replyModelWay(name, base, key, model string) replyWay {
	return replyWay{name: name, setup: func(t *testing.T, s *Service) {
		ms, err := s.AddModelService(bg, ModelServiceInput{Name: name, Kind: ai.KindOpenAI, BaseURL: base, APIKey: key, Models: []string{model}})
		require.NoError(t, err)
		setEvalSettings(t, s, func(st *Settings) { st.DefaultModel, st.SmallModel = ms.Slug+"/"+model, ms.Slug+"/"+model })
	}}
}

// replyRaw is a plain-text message to alice; re, when set, is the Message-ID
// it answers.
func replyRaw(id, from, date, body, re string) string {
	refs := ""
	if re != "" {
		refs = "In-Reply-To: <" + re + "@example.com>\nReferences: <" + re + "@example.com>\n"
	}
	return "From: " + from + "\nTo: alice@example.com\nSubject: Quote for the fit-out\nDate: " + date + "\nMessage-ID: <" + id + "@example.com>\n" + refs +
		"MIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n" + body + "\n"
}

func replyCaseLine(id, mailbox, zone, expect string, messages ...string) map[string]any {
	return map[string]any{"id": id, "mailbox": mailbox, "zone": zone, "expect": expect, "messages": messages}
}

const (
	replyLena = "Lena <lena@studio.example>"
	replyFri  = "Fri, 20 Nov 2026 09:00:00 +0000"
	replyAsk  = "Could you confirm the quote by Friday?"
)

func TestReplyEvalScores(t *testing.T) {
	dir := t.TempDir()
	one := func(id, body string) string { return replyRaw(id, replyLena, replyFri, body, "") }
	writeEvalCases(t, dir, "a.jsonl",
		replyCaseLine("right", "a", "UTC", "2026-11-27", one("right", replyAsk)),
		replyCaseLine("wrong", "a", "UTC", "2026-11-27", one("wrong", replyAsk)),
		replyCaseLine("false", "a", "UTC", "none", one("false", "We will ship the samples by Friday.")),
		replyCaseLine("missed", "a", "UTC", "2026-11-27", one("missed", replyAsk)),
		replyCaseLine("malformed", "b", "UTC", "none", one("malformed", "Thanks, all good.")),
		replyCaseLine("quote", "b", "UTC", "2026-11-27", one("quote", replyAsk)),
		replyCaseLine("thread", "b", "Asia/Shanghai", "2026-11-21 15:00",
			replyRaw("thread-1", "Alice <alice@example.com>", "Thu, 19 Nov 2026 02:00:00 +0000", "Here are the drawings.", ""),
			replyRaw("thread-2", replyLena, replyFri, "Thanks. Please reply by 15:00 tomorrow with your choice.", "thread-1")),
		replyCaseLine("old", "b", "UTC", "none",
			replyRaw("old-1", replyLena, "Mon, 02 Nov 2026 09:00:00 +0000", "Please reply by November 2.", ""),
			replyRaw("old-2", replyLena, replyFri, "Never mind, sorted it out.", "old-1")),
	)
	cases, err := loadReplySuite(dir)
	require.NoError(t, err)

	srv := testllm.Start(t, "openai")
	answer := func(lines string) testllm.Turn { return testllm.Turn{Text: []string{"- a point\n" + lines}} }
	srv.Script(
		answer("REPLY BY: 2026-11-27\nQUOTE: "+replyAsk),
		testllm.Turn{Text: []string{"- the summary as it was"}}, // right, sampled
		answer("REPLY BY: 2026-11-28\nQUOTE: "+replyAsk),
		answer("REPLY BY: 2026-11-27\nQUOTE: We will ship the samples by Friday."),
		answer("REPLY BY: none\nQUOTE:"),
		answer("REPLY BY: Friday\nQUOTE: Thanks"),
		testllm.Turn{Text: []string{"- as it was"}}, // malformed, sampled
		answer("REPLY BY: 2026-11-27\nQUOTE: Confirm the quote by Friday."),
		testllm.Turn{Text: []string{"PROGRESS\n- Lena asked <mail id=\"1\"/>\nREPLY BY: 2026-11-21 15:00\nQUOTE: Please reply by 15:00 tomorrow with your choice."}},
		answer("REPLY BY: 2026-11-02\nQUOTE: Please reply by November 2."),
	)
	runs := runReplyEval(t, cases, []replyWay{replyModelWay("fake", srv.URL, "k", "m")}, 4)
	require.Len(t, runs, 1)
	r := runs[0]
	require.NoError(t, r.err)

	assert.Equal(t, replyScore{n: 8, mailboxes: 2, right: 4, wrongDate: 1, falseDate: 1, missed: 2, dropped: 3}, r.score())
	assert.Equal(t, replyBy{Date: "2026-11-21", Time: "15:00", Quote: "Please reply by 15:00 tomorrow with your choice."}, r.got["thread"].r)
	assert.Equal(t, "PROGRESS\n- Lena asked <mail id=\"1\"/>", r.got["thread"].text, "the lines are out of the summary")
	assert.Contains(t, r.got["old"].why, "quote not in the newest message", "a date from an older email is dropped")
	assert.Equal(t, "- the summary as it was", r.got["right"].base)
	assert.Empty(t, r.got["wrong"].base, "every fourth case is sampled")

	reqs := srv.Requests()
	require.Len(t, reqs, 10)
	var system []string
	for _, q := range reqs {
		var s string
		require.NoError(t, json.Unmarshal(q.System, &s))
		system = append(system, s)
	}
	assert.Contains(t, system[0], "REPLY BY: YYYY-MM-DD [HH:MM] | none")
	assert.Contains(t, system[0], "calendar in UTC")
	assert.NotContains(t, system[1], "REPLY BY", "the sample is the summary as production asks it")
	assert.Contains(t, system[8], "PROGRESS", "a thread is summarized as a thread")
	assert.Contains(t, system[8], "calendar in Asia/Shanghai")

	report := replyReport(runs, []string{"local: GODWIT_LIVE_LOCAL_URL and GODWIT_LIVE_LOCAL_MODELS not set"})
	t.Log(report)
	for _, want := range []string{
		"skipped local: GODWIT_LIVE_LOCAL_URL",
		"8 cases from 2 mailboxes",
		"wrong reply dates 2 of 8 (25.0%)",
		"no verdict: it needs 200 cases from 2 mailboxes",
		"wrong date: wrong (2026-11-28, want 2026-11-27)",
		"date where none: false (2026-11-27)",
		"missed: missed, quote",
		"dropped: malformed (date \"Friday\"",
	} {
		assert.Contains(t, report, want)
	}
	for _, body := range []string{"confirm the quote", "ship the samples", "drawings"} {
		assert.NotContains(t, report, body, "no mail content")
	}

	out := t.TempDir()
	require.NoError(t, writeReplySamples(out, runs))
	b, err := os.ReadFile(filepath.Join(out, "fake.md"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "- the summary as it was")
	assert.Contains(t, string(b), "## malformed")
}

// The threshold, written down before the first run: a verdict needs 200
// cases from two mailboxes, and passes under 5% wrong reply dates.
func TestReplyEvalVerdict(t *testing.T) {
	run := func(n, mailboxes, wrong int) replyRun {
		r := replyRun{got: map[string]replyGot{}}
		for i := range n {
			id := fmt.Sprint(i)
			c := replyCase{ID: id, Mailbox: fmt.Sprint(i % mailboxes), want: replyBy{Date: "2026-11-27"}}
			r.cases = append(r.cases, c)
			r.got[id] = replyGot{r: c.want}
			if i < wrong {
				r.got[id] = replyGot{r: replyBy{Date: "2026-11-28"}}
			}
		}
		return r
	}
	assert.True(t, strings.HasPrefix(run(200, 2, 9).score().verdict(), "PASS"), "4.5%")
	assert.True(t, strings.HasPrefix(run(200, 2, 10).score().verdict(), "FAIL"), "5.0% is not under 5%")
	assert.True(t, strings.HasPrefix(run(199, 2, 0).score().verdict(), "no verdict"))
	assert.True(t, strings.HasPrefix(run(400, 1, 0).score().verdict(), "no verdict"))

	// A label's time has to match; a time the label has not is fine.
	r := run(2, 1, 0)
	r.cases[0].want.Time = "15:00"
	r.got["0"] = replyGot{r: replyBy{Date: "2026-11-27", Time: "17:00"}}
	r.got["1"] = replyGot{r: replyBy{Date: "2026-11-27", Time: "09:00"}}
	assert.Equal(t, replyScore{n: 2, mailboxes: 1, right: 1, wrongDate: 1}, r.score())
}

func TestReplyEvalRejectsBadCases(t *testing.T) {
	dir := t.TempDir()
	m := replyRaw("x", replyLena, replyFri, replyAsk, "")
	writeEvalCases(t, dir, "a.jsonl",
		replyCaseLine("ok", "a", "UTC", "none", m),
		replyCaseLine("ok", "a", "UTC", "none", m),
		replyCaseLine("zone", "a", "Mars/Olympus", "none", m),
		replyCaseLine("expect", "a", "UTC", "Friday", m),
		replyCaseLine("empty", "a", "UTC", "none"),
		replyCaseLine("mailbox", "", "UTC", "none", m),
		map[string]any{"id": "typo", "mailbox": "a", "zone": "UTC", "expect": "none", "mesages": []string{m}},
	)
	_, err := loadReplySuite(dir)
	for _, want := range []string{
		"a.jsonl:2 ok: id also used at a.jsonl:1 ok",
		`a.jsonl:3 zone: zone "Mars/Olympus"`,
		`a.jsonl:4 expect: expect "Friday"`,
		"a.jsonl:5 empty: no messages",
		"a.jsonl:6 mailbox: no mailbox",
		`a.jsonl:7: json: unknown field "mesages"`,
	} {
		require.ErrorContains(t, err, want)
	}
}

// The checked-in cases load and cover the hard cases the README names.
func TestReplyEvalSamples(t *testing.T) {
	cases, err := loadReplySuite("testdata/replyeval")
	require.NoError(t, err)
	zones, notes := map[string]bool{}, ""
	dated, thread := false, false
	for _, c := range cases {
		zones[c.Zone] = true
		notes += c.Note + "\n"
		dated = dated || c.want.Date != ""
		thread = thread || len(c.Messages) > 1
	}
	assert.True(t, dated && thread)
	assert.GreaterOrEqual(t, len(zones), 2)
	for _, kind := range []string{"relative", "sender's own", "pay", "quoted", "time zone"} {
		assert.Contains(t, notes, kind)
	}

	// Each syncs as one conversation; a model that answers "ok" gives no date.
	srv := testllm.Start(t, "openai")
	runs := runReplyEval(t, cases, []replyWay{replyModelWay("fake", srv.URL, "k", "m")}, 0)
	require.NoError(t, runs[0].err)
	sc := runs[0].score()
	assert.Equal(t, len(cases), sc.dropped, "no REPLY BY line")
	assert.Equal(t, sc.n, sc.right+sc.missed)
	report := replyReport(runs, nil)
	for _, c := range cases {
		for _, m := range c.Messages {
			_, body, _ := strings.Cut(m, "\r\n\r\n")
			assert.NotContains(t, report, strings.TrimSpace(body)[:12], "no mail content")
		}
	}
}
