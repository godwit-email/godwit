package core

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPreviewEnv is a mailbox with one message per kind of condition to
// tell apart, the label Bills (u1) on news@x, a message in Sent and one
// older than the preview looks.
func newPreviewEnv(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncEnv(t)
	recent := time.Now().Add(-2 * time.Hour).Format(time.RFC1123Z)
	att := func(id, from, subject, body string) string {
		return "Message-ID: <" + id + ">\r\nFrom: " + from + "\r\nTo: alice@example.com\r\nCc: team@example.com\r\nSubject: " + subject +
			"\r\nDate: " + recent + "\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n" +
			"--XX\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n" +
			"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"a.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--XX--\r\n"
	}
	e.put(t, "INBOX", att("aws@x", "AWS <billing@aws.example>", "Your Invoice is ready", "Amount due: $12"))
	e.put(t, "INBOX", eml("stripe@x", "invoice@stripe.com", "Alice <alice@example.com>", "Receipt #1234", recent, "Thanks for paying"))
	e.put(t, "Archive", eml("news@x", "Substack <news@substack.com>", "alice@example.com", "The Weekly Digest", recent, "invoice tips inside"))
	e.put(t, "INBOX", att("cn@x", "王律师 <lawyer@client.example>", "合同终稿，请查收", "附件是合同"))
	e.put(t, "INBOX", eml("short@x", "ab@cd.example", "alice@example.com", "Hi", time.Now().AddDate(0, 0, -2).Format(time.RFC1123Z), "ok"))
	e.put(t, "Sent", eml("sent@x", "billing@aws.example", "bob@example.com", "Invoice", recent, "b"))
	e.put(t, "INBOX", eml("old@x", "billing@aws.example", "alice@example.com", "Invoice", time.Now().AddDate(0, 0, -RulePreviewDays-5).Format(time.RFC1123Z), "b"))
	e.sync(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "news@x")}, e.labelID(t, "u1"), true))
	return e
}

func (e *syncEnv) labelID(t *testing.T, slug string) int64 {
	t.Helper()
	return e.rows(t, "SELECT id FROM labels WHERE slug = ?", slug)[0][0].(int64)
}

// evaluated is what the in-memory evaluator matches of the mail of the last
// 30 days, and with old@x too.
func (e *syncEnv) evaluated(t *testing.T, d RuleDoc, old ...string) []int64 {
	t.Helper()
	var out []int64
	for _, mid := range append([]string{"aws@x", "stripe@x", "news@x", "cn@x", "short@x"}, old...) {
		m, ok, err := e.s.ruleMail(bg, e.idOf(t, mid))
		require.NoError(t, err)
		if ok && d.matches(m) {
			out = append(out, e.idOf(t, mid))
		}
	}
	slices.Sort(out)
	return out
}

func (e *syncEnv) previewed(t *testing.T, d RuleDoc) []int64 {
	t.Helper()
	p, err := e.s.PreviewRule(bg, d, 200)
	require.NoError(t, err)
	var out []int64
	for _, m := range p.Messages {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	if d.Trigger == TriggerReceive { // the evaluator reads no trigger
		assert.Equal(t, len(e.evaluated(t, d, "old@x")), p.Count, "the count is of all the existing mail")
	}
	return out
}

// The preview, compiled to SQL and the full-text index, matches what the
// evaluator does on each kind of condition, within the last 30 days and
// never in Sent.
func TestRulePreviewAgreesWithEvaluator(t *testing.T) {
	e := newPreviewEnv(t)
	archive := strconv.FormatInt(e.folderID(t, "Archive"), 10)
	conds := []RuleCondition{
		{FieldFrom, OpContains, "billing@"}, {FieldFrom, OpContains, "BILLING"}, {FieldFrom, OpContains, "ab"},
		{FieldFrom, OpContains, "王律师"}, {FieldFrom, OpNotContains, "billing@"},
		{FieldFrom, OpIs, "billing@aws.example"}, {FieldFrom, OpIs, " AWS <billing@aws.example>"}, {FieldFrom, OpIs, "aws"},
		{FieldTo, OpContains, "team@"}, {FieldTo, OpContains, "al"}, {FieldTo, OpIs, "alice@example.com"}, {FieldTo, OpNotContains, "team@"},
		{FieldSubject, OpContains, "invoice"}, {FieldSubject, OpContains, "合同"}, {FieldSubject, OpContains, "hi"}, {FieldSubject, OpContains, "#12"},
		{FieldSubject, OpIs, "receipt #1234"}, {FieldSubject, OpNotContains, "invoice"}, {FieldSubject, OpContains, `"quoted"`},
		{FieldBody, OpContains, "amount due"}, {FieldBody, OpContains, "ok"}, {FieldBody, OpContains, "invoice"},
		{FieldBody, OpIs, "ok"}, {FieldBody, OpNotContains, "invoice"},
		{FieldAttachments, OpIs, RuleYes}, {FieldAttachments, OpIs, RuleNo},
		{FieldLabel, OpIs, "u1"}, {FieldLabel, OpNotContains, "u1"},
		{FieldFolder, OpIs, "inbox"}, {FieldFolder, OpIs, "archive"}, {FieldFolder, OpIs, archive}, {FieldFolder, OpNotContains, "inbox"},
		{FieldAge, OpOlderThan, "1"}, {FieldAge, OpOlderThan, "3"},
	}
	some := 0
	for _, c := range conds {
		d := RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{c}}
		want := e.evaluated(t, d)
		assert.Equal(t, want, e.previewed(t, d), "%s %s %q", c.Field, c.Op, c.Value)
		if len(want) > 0 && len(want) < 5 {
			some++
		}
	}
	assert.Greater(t, some, len(conds)/2, "the fixture tells too few conditions apart")
	two := []RuleCondition{{FieldFrom, OpContains, "billing@"}, {FieldBody, OpContains, "invoice"}}
	for _, d := range []RuleDoc{
		{Trigger: TriggerReceive, Match: MatchAll, Conditions: two},
		{Trigger: TriggerReceive, Match: MatchAny, Conditions: two},
		{Trigger: TriggerReceive, Match: MatchAll},
	} {
		assert.Equal(t, e.evaluated(t, d), e.previewed(t, d), "%s %v", d.Match, d.Conditions)
	}
	assert.Len(t, e.previewed(t, RuleDoc{Trigger: TriggerReceive, Match: MatchAny}), 5, "Sent and older mail are not listed")
	// An account rule runs on that account's mail only.
	assert.Equal(t, e.evaluated(t, RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Account: e.acc.ID}), e.previewed(t, RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Account: e.acc.ID}))
	assert.Empty(t, e.previewed(t, RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Account: e.acc.ID + 1}))
	// A label rule runs on mail with that label on.
	assert.Equal(t, []int64{e.idOf(t, "news@x")}, e.previewed(t, RuleDoc{Trigger: TriggerLabel, TriggerArg: "u1", Match: MatchAll}))

	p, err := e.s.PreviewRule(bg, RuleDoc{Trigger: TriggerReceive, Match: MatchAll}, 2)
	require.NoError(t, err)
	assert.Equal(t, 6, p.Count, "older mail counts, Sent does not")
	assert.Len(t, p.Messages, 2)
	_, err = e.s.PreviewRule(bg, RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{{FieldSubject, OpContains, " "}}}, 2)
	assert.ErrorIs(t, err, ErrInvalidRule)
}

// The one-time run puts the rule's local actions on the existing mail it
// matches, once, and never forwards; the hit counts show it.
func TestRuleRunOnExisting(t *testing.T) {
	e := newPreviewEnv(t)
	d := rule(RuleAction{Kind: ActionForward, To: carol}, RuleAction{Kind: ActionLabel, Label: "u1"}, RuleAction{Kind: ActionRead})
	id, err := e.s.SaveRule(bg, Rule{Enabled: true, Doc: d, Confirmed: []string{carol}})
	require.NoError(t, err)
	n, err := e.s.RunRuleOnExisting(bg, id)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "all the existing mail, not only the last 30 days; Sent left out")
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "aws@x")))
	assert.Contains(t, e.flagsOf(t, "aws@x"), `\Seen`)
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "old@x")))
	assert.Equal(t, [][]any{{e.idOf(t, "aws@x"), TriggerExisting, RuleRunDone}, {e.idOf(t, "old@x"), TriggerExisting, RuleRunDone}},
		e.rows(t, "SELECT message_id, trigger, state FROM rule_runs ORDER BY message_id"))
	assert.Equal(t, []string{"label done", "read done"}, e.results(t, store.Int(e.rows(t, "SELECT id FROM rule_runs")[0][0])))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM outbox"))

	n, err = e.s.RunRuleOnExisting(bg, id)
	require.NoError(t, err)
	assert.Zero(t, n, "a message the rule ran on is not run again")
	counts, err := e.s.RuleHitCounts(bg)
	require.NoError(t, err)
	assert.Equal(t, map[int64]int{id: 2}, counts)
}

// Only local actions run on existing mail; any other kind (forward, Wit)
// waits for new mail unless it is listed.
func TestRuleActionsOnExisting(t *testing.T) {
	for _, k := range []string{ActionLabel, ActionUnlabel, ActionRead, ActionStar, ActionArchive, ActionJunk, ActionTrash, ActionBlock, ActionStop} {
		assert.True(t, RunsOnExisting(k), k)
	}
	assert.False(t, RunsOnExisting(ActionForward))
}

// A run over existing mail cut off between two actions goes on when Godwit
// opens again: no event brings it back as one does a new mail's.
func TestRuleRunOnExistingResumes(t *testing.T) {
	e := newPreviewEnv(t)
	id, err := e.s.SaveRule(bg, Rule{Enabled: true, Doc: rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionRead})})
	require.NoError(t, err)
	e.rulesIdle(t) // the consumer past the synced mail before the hook is set
	stopRulesAt(t, "rules.action")
	_, err = e.s.RunRuleOnExisting(bg, id)
	require.Error(t, err, "cut off")
	rulesHook = nil
	run := store.Int(e.rows(t, "SELECT id FROM rule_runs")[0][0])
	require.Equal(t, []string{"star done"}, e.results(t, run))
	e.reopen(t)
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM rule_runs WHERE id = ? AND state = ?", run, RuleRunDone) == 1
	},
		5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"star done", "read done"}, e.results(t, run))
}
