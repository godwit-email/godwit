package core

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billsRule labels mail from billing@ whose subject has "invoice" with Bills (u1).
func billsRule() RuleDoc {
	return RuleDoc{Version: RuleVersion, Name: "Bills", Trigger: TriggerReceive, Match: MatchAll,
		Conditions: []RuleCondition{{Field: FieldFrom, Op: OpContains, Value: "billing@"}, {Field: FieldSubject, Op: OpContains, Value: "Invoice"}},
		Actions:    []RuleAction{{Kind: ActionLabel, Label: "u1"}}}
}

func (e *syncEnv) saveRule(t *testing.T, d RuleDoc, enabled bool) int64 {
	t.Helper()
	id, err := e.s.SaveRule(bg, Rule{Enabled: enabled, Doc: d})
	require.NoError(t, err)
	return id
}

// newRulesEnv is newSyncedEnv with the label Bills (slug u1).
func newRulesEnv(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncedEnv(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	return e
}

// labelOn reports the message's Bills row: its source, or "" for none on.
func (e *syncEnv) labelOn(t *testing.T, msgID int64) string {
	t.Helper()
	rows := e.rows(t, `SELECT ml.source FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		WHERE ml.message_id = ? AND l.slug = 'u1' AND ml.active = 1`, msgID)
	if len(rows) == 0 {
		return ""
	}
	return rows[0][0].(string)
}

func (e *syncEnv) waitRuns(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM rule_runs WHERE state != 'running'") >= n
	}, 10*time.Second, 10*time.Millisecond)
}

func (e *syncEnv) bill(t *testing.T, id, from, subject string) {
	t.Helper()
	e.put(t, "INBOX", eml(id, from, "alice@example.com", subject, day, "hello"))
}

func TestRuleLabelsMatchingNewMail(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	e.bill(t, "hit@x", "AWS <billing@aws.example>", "Your invoice is ready")
	e.bill(t, "miss@x", "Bob <bob@example.com>", "Your invoice is ready")
	e.sync(t)
	e.waitRuns(t, 1)

	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
	assert.Empty(t, e.labelOn(t, e.idOf(t, "miss@x")))
	assert.Equal(t, [][]any{{e.idOf(t, "hit@x"), "receive", "done"}}, e.rows(t, "SELECT message_id, trigger, state FROM rule_runs"))
	assert.Equal(t, [][]any{{int64(0), "label", "done"}}, e.rows(t, "SELECT idx, kind, state FROM rule_action_results"))
}

// setRulesHook installs f as rulesHook for the test.
func setRulesHook(t *testing.T, f func(stage string) error) {
	t.Helper()
	rulesHook = f
	t.Cleanup(func() { rulesHook = nil })
}

// rulesIdle waits for the rule consumer to pass every logged event.
func (e *syncEnv) rulesIdle(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM events_log WHERE seq > (SELECT seq FROM event_cursors WHERE consumer = 'rules')") == 0
	}, 10*time.Second, 10*time.Millisecond)
}

// stopRulesAt makes the consumer stop at stage, without moving its cursor,
// as a crash would; the returned channel gets each stop.
func stopRulesAt(t *testing.T, stage string) chan struct{} {
	t.Helper()
	stopped := make(chan struct{}, 1)
	setRulesHook(t, func(s string) error {
		if s != stage {
			return nil
		}
		stopped <- struct{}{}
		return errStopConsumer
	})
	return stopped
}

// A crash after the rule ran but before the cursor moved delivers the
// message again: the action is applied once and there is one run.
func TestRuleOncePerMessage(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	stopped := stopRulesAt(t, "rules.ran")
	e.bill(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	<-stopped
	rulesHook = nil
	e.reopen(t)
	e.rulesIdle(t)

	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM rule_runs"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM rule_action_results"))
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
}

// Mail logged while the consumer could not run is handled when it starts.
func TestRuleCatchesUpAfterRestart(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	stopped := stopRulesAt(t, "rules.deliver")
	e.bill(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	<-stopped
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs"))
	rulesHook = nil
	e.reopen(t)
	e.waitRuns(t, 1)
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
}

// An account's first sync is no new mail: no rule runs on it.
func TestRuleNotOnInitialSync(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	e.saveRule(t, billsRule(), true)
	e.bill(t, "old@x", "billing@aws.example", "Invoice 0")
	e.sync(t)
	e.bill(t, "new@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, [][]any{{e.idOf(t, "new@x")}}, e.rows(t, "SELECT message_id FROM rule_runs"))
	assert.Empty(t, e.labelOn(t, e.idOf(t, "old@x")))
}

// Disabled rules don't run; Sent mail is not received mail.
func TestRuleSkipsDisabledAndSent(t *testing.T) {
	e := newRulesEnv(t)
	e.put(t, "Sent", eml("sent0@x", "billing@aws.example", "bob@example.com", "Invoice", day, "b"))
	e.sync(t) // Sent synced: what lands there next is logged
	off := e.saveRule(t, billsRule(), false)
	e.bill(t, "a@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.rulesIdle(t) // a@x is handled before the enabled rule exists
	e.saveRule(t, billsRule(), true)
	e.put(t, "Sent", eml("sent@x", "billing@aws.example", "bob@example.com", "Invoice 2", day, "b"))
	e.bill(t, "b@x", "billing@aws.example", "Invoice 3")
	e.sync(t)
	e.rulesIdle(t)
	assert.Equal(t, [][]any{{e.idOf(t, "b@x")}}, e.rows(t, "SELECT message_id FROM rule_runs"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs WHERE rule_id = ?", off))
	assert.Empty(t, e.labelOn(t, e.idOf(t, "a@x")))
}

// A rule's label stays on through archiving the message.
func TestRuleLabelSurvivesArchive(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	e.bill(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.waitRuns(t, 1)
	require.NoError(t, e.s.MoveToRole(bg, []int64{e.idOf(t, "hit@x")}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{"hit@x"}, e.onServer(t, "Archive"))
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
}

// newMoveEnv has INBOX and Archive synced once, so mail another client
// moves into Archive arrives there as new mail, and the label Bills.
func newMoveEnv(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncEnv(t)
	e.newMail(t, "old@x", "before")
	e.put(t, "Archive", eml("old-archive@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	return e
}

// serverMove moves the message from INBOX to Archive on the server, as
// another client would: the copy here is dropped and imported again.
func (e *syncEnv) serverMove(t *testing.T, id, from, subject string) {
	t.Helper()
	e.put(t, "Archive", eml(id, from, "alice@example.com", subject, day, "hello"))
	e.expungeAll(t, "INBOX")
	e.sync(t)
}

// A rule's label outlives a re-import, as a hand-set one does.
func TestRuleLabelSurvivesReimport(t *testing.T) {
	e := newMoveEnv(t)
	id := e.saveRule(t, billsRule(), true)
	e.bill(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.waitRuns(t, 1)
	require.NoError(t, e.s.SetRuleEnabled(bg, id, false)) // the new copy gets it only from the stash
	e.serverMove(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.rulesIdle(t)
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
}

// A label taken off by hand stays off: the copy imported again keeps the
// choice, and the rule running on it does not put the label back.
func TestRuleKeepsManualRemoval(t *testing.T) {
	e := newMoveEnv(t)
	e.saveRule(t, billsRule(), true)
	e.bill(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.waitRuns(t, 1)
	bills := store.Int(e.rows(t, "SELECT id FROM labels WHERE slug = 'u1'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "hit@x")}, bills, false))
	e.serverMove(t, "hit@x", "billing@aws.example", "Invoice 1")
	e.waitRuns(t, 2)
	assert.Empty(t, e.labelOn(t, e.idOf(t, "hit@x")))
	assert.Equal(t, [][]any{{"manual", int64(0)}}, e.rows(t, "SELECT source, active FROM message_labels WHERE message_id = ?", e.idOf(t, "hit@x")))
}

// The stored rule holds ids and slugs only, never a display string.
func TestRuleStoresStableIDs(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	assert.JSONEq(t, `{"version":1,"name":"Bills","trigger":"receive","match":"all",`+
		`"conditions":[{"field":"from","op":"contains","value":"billing@"},{"field":"subject","op":"contains","value":"Invoice"}],`+
		`"actions":[{"kind":"label","label":"u1"}]}`, store.Str(e.rows(t, "SELECT doc FROM rules")[0][0]))
	for _, d := range []RuleDoc{
		{Version: RuleVersion, Trigger: "收到新邮件", Match: MatchAll, Actions: []RuleAction{{Kind: ActionLabel, Label: "u1"}}},
		{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Actions: []RuleAction{{Kind: ActionLabel, Label: "Bills"}}},
		{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{{FieldAge, OpOlderThan, "1.5"}}, Actions: []RuleAction{{Kind: ActionRead}}},
		{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{{FieldAge, OpOlderThan, "0"}}, Actions: []RuleAction{{Kind: ActionRead}}},
		{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{{FieldAge, OpContains, "3"}}, Actions: []RuleAction{{Kind: ActionRead}}},
	} {
		_, err := e.s.SaveRule(bg, Rule{Doc: d})
		assert.ErrorIs(t, err, ErrInvalidRule)
	}
}

// A rule for one account runs on that account's mail only; one naming an
// account there is not is refused.
func TestRuleAccountScope(t *testing.T) {
	e := newRulesEnv(t)
	res, err := e.s.db.Execute(bg, "INSERT INTO accounts(email) VALUES ('other@example.com')")
	require.NoError(t, err)
	mine, other := billsRule(), rule(RuleAction{Kind: ActionStar})
	mine.Account, other.Account = e.acc.ID, res.LastInsertID
	e.saveRule(t, mine, true)
	e.saveRule(t, other, true)
	e.bill(t, "hit@x", "AWS <billing@aws.example>", "Your invoice is ready")
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM rule_runs"), "the other account's rule did not run")

	bad := billsRule()
	bad.Account = res.LastInsertID + 1
	_, err = e.s.SaveRule(bg, Rule{Doc: bad})
	assert.ErrorIs(t, err, ErrInvalidRule)
}

func TestRuleConditions(t *testing.T) {
	m := ruleMail{from: "AWS <billing@aws.example>", subject: "Your Invoice", to: []string{"Alice <alice@example.com>", "team@example.com"},
		body: "Amount due: $12", attachments: true, labels: []string{"u1"}, folderID: 7, folderRole: "inbox",
		date: time.Now().AddDate(0, 0, -10).Unix(), now: time.Now().Unix()}
	for _, tc := range []struct {
		field, op, value string
		want             bool
	}{
		{FieldFrom, OpContains, "BILLING@", true},
		{FieldFrom, OpNotContains, "billing@", false},
		{FieldFrom, OpIs, "billing@aws.example", true},
		{FieldFrom, OpIs, "aws", false},
		{FieldTo, OpContains, "team@", true},
		{FieldTo, OpIs, "alice@example.com", true},
		{FieldTo, OpNotContains, "bob@", true},
		{FieldSubject, OpContains, "invoice", true},
		{FieldSubject, OpIs, "your invoice", true},
		{FieldSubject, OpNotContains, "invoice", false},
		{FieldBody, OpContains, "amount due", true},
		{FieldBody, OpNotContains, "refund", true},
		{FieldBody, OpIs, "amount", false},
		{FieldAttachments, OpIs, RuleYes, true},
		{FieldAttachments, OpIs, RuleNo, false},
		{FieldLabel, OpIs, "u1", true},
		{FieldLabel, OpIs, "u2", false},
		{FieldLabel, OpNotContains, "u1", false},
		{FieldFolder, OpIs, "inbox", true},
		{FieldFolder, OpIs, "7", true},
		{FieldFolder, OpIs, "archive", false},
		{FieldFolder, OpNotContains, "archive", true},
		{FieldAge, OpOlderThan, "7", true},
		{FieldAge, OpOlderThan, "30", false},
	} {
		c := RuleCondition{Field: tc.field, Op: tc.op, Value: tc.value}
		assert.Equal(t, tc.want, c.matches(m), "%s %s %q", tc.field, tc.op, tc.value)
	}
	hit, miss := RuleCondition{Field: FieldSubject, Op: OpContains, Value: "invoice"}, RuleCondition{Field: FieldSubject, Op: OpContains, Value: "receipt"}
	for _, tc := range []struct {
		match string
		conds []RuleCondition
		want  bool
	}{
		{MatchAll, []RuleCondition{hit, hit}, true},
		{MatchAll, []RuleCondition{hit, miss}, false},
		{MatchAny, []RuleCondition{miss, hit}, true},
		{MatchAny, []RuleCondition{miss, miss}, false},
		{MatchAll, nil, true},
		{MatchAny, nil, true},
	} {
		assert.Equal(t, tc.want, RuleDoc{Match: tc.match, Conditions: tc.conds}.matches(m), "%s %v", tc.match, tc.conds)
	}
}

// rule is a receive rule on mail from billing@ doing acts.
func rule(acts ...RuleAction) RuleDoc {
	return RuleDoc{Version: RuleVersion, Name: "R", Trigger: TriggerReceive, Match: MatchAll,
		Conditions: []RuleCondition{{Field: FieldFrom, Op: OpContains, Value: "billing@"}}, Actions: acts}
}

// results lists the run's action results as "kind state".
func (e *syncEnv) results(t *testing.T, runID int64) []string {
	t.Helper()
	var out []string
	for _, v := range e.rows(t, "SELECT kind, state FROM rule_action_results WHERE run_id = ? ORDER BY idx", runID) {
		out = append(out, store.Str(v[0])+" "+store.Str(v[1]))
	}
	return out
}

func (e *syncEnv) runOf(t *testing.T, mid string) int64 {
	t.Helper()
	return store.Int(e.rows(t, "SELECT id FROM rule_runs WHERE message_id = ?", e.idOf(t, mid))[0][0])
}

func (e *syncEnv) flagsOf(t *testing.T, mid string) string {
	t.Helper()
	return store.Str(e.rows(t, "SELECT flags FROM messages WHERE id = ?", e.idOf(t, mid))[0][0])
}

func (e *syncEnv) folderOf(t *testing.T, mid string) int64 {
	t.Helper()
	return store.Int(e.rows(t, "SELECT folder_id FROM messages WHERE id = ?", e.idOf(t, mid))[0][0])
}

// The fields a condition reads come from the stored message.
func TestRuleMatchesStoredMessage(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{
		{Field: FieldTo, Op: OpIs, Value: "alice@example.com"}, {Field: FieldBody, Op: OpContains, Value: "due"},
		{Field: FieldAttachments, Op: OpIs, Value: RuleNo}, {Field: FieldFolder, Op: OpIs, Value: "inbox"},
		{Field: FieldLabel, Op: OpNotContains, Value: "u1"},
	}, Actions: []RuleAction{{Kind: ActionLabel, Label: "u1"}}}, true)
	e.put(t, "INBOX", eml("hit@x", "bob@example.com", "alice@example.com", "s", day, "amount due"))
	e.put(t, "INBOX", eml("miss@x", "bob@example.com", "alice@example.com", "s", day, "paid"))
	e.sync(t)
	e.rulesIdle(t)
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "hit@x")))
	assert.Empty(t, e.labelOn(t, e.idOf(t, "miss@x")))
}

// Each local action changes the message here, and its op takes the change to the server.
func TestRuleLocalActions(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}, RuleAction{Kind: ActionRead}, RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionArchive}), true)
	e.saveRule(t, RuleDoc{Version: RuleVersion, Trigger: TriggerReceive, Match: MatchAll,
		Conditions: []RuleCondition{{Field: FieldSubject, Op: OpIs, Value: "spam"}}, Actions: []RuleAction{{Kind: ActionTrash}}}, true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.bill(t, "b@x", "bob@example.com", "spam")
	e.sync(t)
	e.waitRuns(t, 2)
	e.sync(t) // pushes the ops
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "a@x")))
	assert.Equal(t, []string{`a@x \Flagged \Seen`}, e.onServer(t, "Archive"))
	assert.Equal(t, []string{"b@x"}, e.onServer(t, "Trash"))
}

func TestRuleUnlabelAndJunk(t *testing.T) {
	e := newRulesEnv(t)
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}), true)
	e.saveRule(t, rule(RuleAction{Kind: ActionUnlabel, Label: "u1"}, RuleAction{Kind: ActionJunk}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 2)
	e.sync(t)
	assert.Empty(t, e.labelOn(t, e.idOf(t, "a@x")))
	assert.Equal(t, []string{"a@x"}, e.onServer(t, "Junk"))
}

func TestRuleBlockActionAndUndo(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.bill(t, "a@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)

	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Contains(t, blocked, "billing@evil.example")
	assert.Equal(t, []string{"block done"}, e.results(t, e.runOf(t, "a@x")))

	kept, err := e.s.UndoRuleRun(bg, e.runOf(t, "a@x"))
	require.NoError(t, err)
	assert.Zero(t, kept)

	blocked, err = e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.NotContains(t, blocked, "billing@evil.example")
	assert.Equal(t, []string{"block undone"}, e.results(t, e.runOf(t, "a@x")))
}

// Rules run in list order; a stop action keeps the later ones off the message.
func TestRuleStopsLaterRules(t *testing.T) {
	e := newRulesEnv(t)
	first := e.saveRule(t, rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionStop}), true)
	e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	assert.Equal(t, [][]any{{first}}, e.rows(t, "SELECT rule_id FROM rule_runs"))
	assert.Empty(t, e.labelOn(t, e.idOf(t, "a@x")))
	assert.Equal(t, `\Flagged`, e.flagsOf(t, "a@x"))
}

// A failed action stops the rule and the rest are not run; a retry finishes
// them in the same run without doing the done ones again.
func TestRuleFailureAndRetry(t *testing.T) {
	e := newRulesEnv(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionJunk}, RuleAction{Kind: ActionRead}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	run := e.runOf(t, "a@x")
	assert.Equal(t, []string{"star done", "junk failed", "read not_run"}, e.results(t, run))
	hits, err := e.s.RuleHits(bg, id)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, RuleRunFailed, hits[0].State)
	assert.Equal(t, RuleFailNoFolder, hits[0].Actions[1].Failure)

	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	require.NoError(t, e.s.RetryRuleRun(bg, run))
	assert.Equal(t, []string{"star done", "junk done", "read done"}, e.results(t, run))
	assert.Equal(t, [][]any{{run, "done"}}, e.rows(t, "SELECT id, state FROM rule_runs"))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'flags'"))
	e.sync(t)
	assert.Equal(t, []string{`a@x \Flagged \Seen`}, e.onServer(t, "Junk"))
}

// A disabled rule's failed run is not retried.
func TestRuleRetryNotWhenDisabled(t *testing.T) {
	e := newRulesEnv(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionJunk}, RuleAction{Kind: ActionRead}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	require.NoError(t, e.s.SetRuleEnabled(bg, id, false))
	run := e.runOf(t, "a@x")
	assert.Error(t, e.s.RetryRuleRun(bg, run))
	assert.Equal(t, []string{"junk failed", "read not_run"}, e.results(t, run))
}

// A run resumed after a crash goes on with the actions the rule had when
// it started, though they were reordered meanwhile.
func TestRuleResumesWithRuleAsItStarted(t *testing.T) {
	e := newRulesEnv(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionRead}, RuleAction{Kind: ActionLabel, Label: "u1"}), true)
	stopped := stopRulesAt(t, "rules.action")
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	<-stopped
	rulesHook = nil
	_, err := e.s.SaveRule(bg, Rule{ID: id, Enabled: true, Doc: rule(RuleAction{Kind: ActionLabel, Label: "u1"}, RuleAction{Kind: ActionRead}, RuleAction{Kind: ActionStar})})
	require.NoError(t, err)
	e.reopen(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"star done", "read done", "label done"}, e.results(t, e.runOf(t, "a@x")))
	assert.Equal(t, "rule", e.labelOn(t, e.idOf(t, "a@x")))
}

// A crash between two actions resumes with the unfinished one only.
func TestRuleResumesAfterCrashBetweenActions(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionRead}), true)
	stopped := stopRulesAt(t, "rules.action")
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	<-stopped
	rulesHook = nil
	assert.Equal(t, []string{"star done"}, e.results(t, e.runOf(t, "a@x")))
	e.reopen(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"star done", "read done"}, e.results(t, e.runOf(t, "a@x")))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'flags'"))
}

// Undo restores what each local action changed; one whose target changed
// since is kept as it is.
func TestRuleUndo(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}, RuleAction{Kind: ActionRead}, RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionArchive}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.bill(t, "b@x", "billing@aws.example", "Invoice 2")
	e.sync(t)
	e.waitRuns(t, 2)

	kept, err := e.s.UndoRuleRun(bg, e.runOf(t, "a@x"))
	require.NoError(t, err)
	assert.Zero(t, kept)
	assert.Empty(t, e.labelOn(t, e.idOf(t, "a@x")))
	assert.Empty(t, e.flagsOf(t, "a@x"))
	assert.Equal(t, e.folderID(t, "INBOX"), e.folderOf(t, "a@x"))
	assert.Equal(t, []string{"label undone", "read undone", "star undone", "archive undone"}, e.results(t, e.runOf(t, "a@x")))

	b := e.idOf(t, "b@x")
	require.NoError(t, e.s.MoveToRole(bg, []int64{b}, "trash"))
	require.NoError(t, e.s.SetFlags(bg, []int64{b}, nil, []string{`\Flagged`}))
	kept, err = e.s.UndoRuleRun(bg, e.runOf(t, "b@x"))
	require.NoError(t, err)
	assert.Equal(t, 2, kept)
	assert.Equal(t, []string{"label undone", "read undone", "star kept", "archive kept"}, e.results(t, e.runOf(t, "b@x")))
	assert.Equal(t, e.folderID(t, "Trash"), e.folderOf(t, "b@x"))
	e.sync(t)
	assert.Equal(t, []string{"b@x"}, e.onServer(t, "Trash"))
	inbox := e.onServer(t, "INBOX")
	slices.Sort(inbox)
	assert.Equal(t, []string{"a@x", "old@x"}, inbox)
}

// History is the last 7 days of hits, newest first.
func TestRuleHits(t *testing.T) {
	e := newRulesEnv(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionStar}), true)
	for i, mid := range []string{"a@x", "b@x", "c@x"} {
		e.bill(t, mid, "billing@aws.example", "Invoice "+mid)
		e.sync(t)
		e.waitRuns(t, i+1)
	}
	_, err := e.s.db.Execute(bg, "UPDATE rule_runs SET at = at - 8 * 86400 WHERE message_id = ?", e.idOf(t, "a@x"))
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "UPDATE rule_runs SET at = at - 60 WHERE message_id = ?", e.idOf(t, "b@x"))
	require.NoError(t, err)
	hits, err := e.s.RuleHits(bg, id)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, "Invoice c@x", hits[0].Subject)
	assert.Equal(t, "Invoice b@x", hits[1].Subject)
	assert.Equal(t, []RuleActionResult{{Kind: ActionStar, State: RuleActionDone, Action: RuleAction{Kind: ActionStar}}}, hits[0].Actions)
}

// A hit names its action as the run had it: the rule's label changed since,
// the hit still says the one it put on.
func TestRuleHitsKeepTheirActions(t *testing.T) {
	e := newRulesEnv(t)
	e.addPaid(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	_, err := e.s.SaveRule(bg, Rule{ID: id, Enabled: true, Doc: rule(RuleAction{Kind: ActionLabel, Label: "u2"})})
	require.NoError(t, err)
	hits, err := e.s.RuleHits(bg, id)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, "u1", hits[0].Actions[0].Action.Label)
}

// Deleting a rule deletes its hits too.
func TestDeleteRule(t *testing.T) {
	e := newRulesEnv(t)
	id := e.saveRule(t, rule(RuleAction{Kind: ActionStar}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	require.NoError(t, e.s.DeleteRule(bg, id))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rules"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_action_results"))
	assert.Error(t, e.s.DeleteRule(bg, id))
}

// Rules come in the order the user put them in, which names each once.
func TestReorderRules(t *testing.T) {
	e := newRulesEnv(t)
	a := e.saveRule(t, rule(RuleAction{Kind: ActionStar}), true)
	b := e.saveRule(t, rule(RuleAction{Kind: ActionRead}), true)
	c := e.saveRule(t, rule(RuleAction{Kind: ActionArchive}), true)
	require.NoError(t, e.s.ReorderRules(bg, []int64{c, a, b}))
	rs, err := e.s.Rules(bg)
	require.NoError(t, err)
	var ids []int64
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []int64{c, a, b}, ids)
	assert.Error(t, e.s.ReorderRules(bg, []int64{a, b}))
	assert.Error(t, e.s.ReorderRules(bg, []int64{a, a, b}))
}

// paidRule puts Paid (u2) on the mail its trigger fires for.
func paidRule(trigger, arg string) RuleDoc {
	return RuleDoc{Version: RuleVersion, Name: "Paid", Trigger: trigger, TriggerArg: arg, Match: MatchAll,
		Actions: []RuleAction{{Kind: ActionLabel, Label: "u2"}}}
}

// paidOn reports whether the message has Paid (u2) on.
func (e *syncEnv) paidOn(t *testing.T, msgID int64) bool {
	t.Helper()
	return e.count(t, `SELECT count(*) FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		WHERE ml.message_id = ? AND l.slug = 'u2' AND ml.active = 1`, msgID) == 1
}

// addPaid creates the label Paid (slug u2) after Bills.
func (e *syncEnv) addPaid(t *testing.T) {
	t.Helper()
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Paid", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
}

// A label put on by hand runs the label rule; one a rule put on does not.
func TestRuleLabelTrigger(t *testing.T) {
	e := newRulesEnv(t)
	e.addPaid(t)
	e.saveRule(t, billsRule(), true)
	e.saveRule(t, paidRule(TriggerLabel, "u1"), true)
	e.bill(t, "byrule@x", "billing@aws.example", "Invoice 1")
	e.bill(t, "byhand@x", "bob@example.com", "lunch")
	e.sync(t)
	e.waitRuns(t, 1)
	bills := store.Int(e.rows(t, "SELECT id FROM labels WHERE slug = 'u1'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "byhand@x")}, bills, true))
	e.waitRuns(t, 2)
	e.rulesIdle(t)

	assert.True(t, e.paidOn(t, e.idOf(t, "byhand@x")))
	assert.False(t, e.paidOn(t, e.idOf(t, "byrule@x")))
	assert.Equal(t, [][]any{{e.idOf(t, "byhand@x"), "label"}}, e.rows(t, "SELECT message_id, trigger FROM rule_runs WHERE trigger != 'receive'"))
}

// spoof is mail the header check flags at import; scam is freemail only the
// model flags, after the import.
func spoof(id string) string {
	return eml(id, `"PayPal" <phish@attacker.com>`, "alice@example.com", "Account Alert", day, "Please login")
}

func scam(id string) string {
	return eml(id, `"Richard" <richard277@yahoo.com>`, "alice@example.com", "Erbschaft", day, "18,7 Millionen US-Dollar",
		"Authentication-Results: mx.example.com; dkim=pass header.i=@yahoo.com")
}

// New mail flagged at import or by the scanner runs the security rule;
// re-scanning old mail does not.
func TestRuleSecurityTrigger(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", scam("old@x"))
	e.sync(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	e.addPaid(t)
	st, err := e.s.loadSettings(bg)
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	e.s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{p: map[string]float64{"phishing_fraud": 0.95}}, nil
	}
	e.saveRule(t, paidRule(TriggerSecurity, SecuritySuspicious), true)

	e.put(t, "INBOX", spoof("spoof@x"))
	e.put(t, "INBOX", scam("scam@x"))
	e.sync(t)
	e.scanAll(t)
	_, err = e.s.RescanSecurity(bg, 0, true)
	require.NoError(t, err)
	e.scanAll(t)
	e.waitRuns(t, 2)
	e.rulesIdle(t)

	assert.Equal(t, "warning", store.Str(e.rows(t, "SELECT security_status FROM messages WHERE id = ?", e.idOf(t, "old@x"))[0][0]))
	assert.False(t, e.paidOn(t, e.idOf(t, "old@x")))
	assert.True(t, e.paidOn(t, e.idOf(t, "spoof@x")))
	assert.True(t, e.paidOn(t, e.idOf(t, "scam@x")))
}

// scanAll runs the security scanner until nothing is queued.
func (e *syncEnv) scanAll(t *testing.T) {
	t.Helper()
	for {
		n, err := e.s.scanSecurityBatch(bg)
		require.NoError(t, err)
		if n == 0 {
			return
		}
	}
}

// dayClock is the real clock with Now set by the test.
type dayClock struct {
	realClock
	mu  sync.Mutex
	now time.Time
}

func (c *dayClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *dayClock) set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }

// The daily rule runs over existing mail on the first sync of each local
// day, however many syncs that day has.
func TestRuleDailyTrigger(t *testing.T) {
	clock := &dayClock{now: time.Date(2026, 11, 2, 8, 0, 0, 0, time.Local)}
	setFor(t, &testClock, Clock(clock))
	e := newRulesEnv(t)
	e.addPaid(t)
	d := paidRule(TriggerDaily, "")
	d.Conditions = []RuleCondition{{Field: FieldSubject, Op: OpContains, Value: "invoice"}}
	e.saveRule(t, d, true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice 1")
	clock.set(time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local))
	e.sync(t) // the first sync today
	e.waitRuns(t, 1)
	e.bill(t, "b@x", "billing@aws.example", "Invoice 2")
	clock.set(time.Date(2026, 11, 3, 23, 59, 0, 0, time.Local))
	e.sync(t)
	e.rulesIdle(t)
	assert.True(t, e.paidOn(t, e.idOf(t, "a@x")))
	assert.False(t, e.paidOn(t, e.idOf(t, "b@x")))
	assert.False(t, e.paidOn(t, e.idOf(t, "old@x")))

	clock.set(time.Date(2026, 11, 4, 0, 1, 0, 0, time.Local))
	e.sync(t)
	e.waitRuns(t, 2)
	e.rulesIdle(t)
	assert.True(t, e.paidOn(t, e.idOf(t, "b@x")))
	assert.Equal(t, [][]any{{"daily", int64(2)}}, e.rows(t, "SELECT trigger, count(*) FROM rule_runs GROUP BY trigger"))
}

// Only enabled rules set to run in the background keep the app running.
func TestRulesInBackground(t *testing.T) {
	e := newRulesEnv(t)
	inBackground := func() bool {
		on, err := e.s.RulesInBackground(bg)
		require.NoError(t, err)
		return on
	}
	assert.False(t, inBackground())
	id := e.saveRule(t, billsRule(), true)
	assert.False(t, inBackground())
	_, err := e.s.SaveRule(bg, Rule{ID: id, Enabled: true, Background: true, Doc: billsRule()})
	require.NoError(t, err)
	assert.True(t, inBackground())
	rules, err := e.s.Rules(bg)
	require.NoError(t, err)
	assert.True(t, rules[0].Background)
	require.NoError(t, e.s.SetRuleEnabled(bg, id, false))
	assert.False(t, inBackground())
}

// classify has the classifier answer for the Inbox mail it has not yet.
func (e *syncEnv) classify(t *testing.T) {
	t.Helper()
	// As new mail is classified (labeled_at NULL): auto-labelling on, the labels
	// seeded. A rescan's target (labeled_at 0) runs no label rule.
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	if !st.AutoLabel {
		st.AutoLabel = true
		_, err = e.s.SetSettings(bg, st)
		require.NoError(t, err)
	}
	_, err = e.s.db.Execute(bg, "INSERT OR IGNORE INTO meta(key, value) VALUES ('labels_seeded', '1')")
	require.NoError(t, err)
	_, err = e.s.classifyBatch(bg)
	require.NoError(t, err)
}

// The classifier labelling mail of the first sync runs no label rule; it
// labelling new mail does.
func TestRuleLabelTriggerNotOnInitialSync(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.CreateLabel(bg, LabelInput{Name: "Bills", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	e.addPaid(t)
	e.saveRule(t, paidRule(TriggerLabel, "u1"), true)
	useLabelJudge(e.s, &fakeJudge{p: map[string]float64{"u1": 1}})
	e.bill(t, "old@x", "billing@aws.example", "Invoice 0")
	e.sync(t)
	e.classify(t)
	require.Equal(t, "auto", e.labelOn(t, e.idOf(t, "old@x")))

	e.bill(t, "new@x", "billing@aws.example", "Invoice 1")
	e.sync(t)
	e.classify(t)
	e.waitRuns(t, 1)
	e.rulesIdle(t)
	assert.Equal(t, [][]any{{e.idOf(t, "new@x"), "label"}}, e.rows(t, "SELECT message_id, trigger FROM rule_runs"))
	assert.False(t, e.paidOn(t, e.idOf(t, "old@x")))
}

// A daily run cut off after an action that took the mail off its
// conditions (archived, out of the inbox) goes on with the rest the next
// time: the run there is resumes before the conditions are read again.
func TestRuleDailyResumes(t *testing.T) {
	clock := &dayClock{now: time.Date(2026, 11, 2, 8, 0, 0, 0, time.Local)}
	setFor(t, &testClock, Clock(clock))
	e := newRulesEnv(t)
	d := paidRule(TriggerDaily, "")
	d.Conditions = []RuleCondition{{Field: FieldFolder, Op: OpIs, Value: "inbox"}, {Field: FieldSubject, Op: OpContains, Value: "invoice"}}
	d.Actions = []RuleAction{{Kind: ActionArchive}, {Kind: ActionStar}}
	e.saveRule(t, d, true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice 1")
	stopped := stopRulesAt(t, "rules.action")
	clock.set(time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local))
	e.sync(t)
	<-stopped
	rulesHook = nil
	run := e.runOf(t, "a@x")
	require.Equal(t, []string{"archive done"}, e.results(t, run))
	e.reopen(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"archive done", "star done"}, e.results(t, run))
}

// Undo pressed while the run is still going waits for it: undone in the
// middle, the actions after would still be applied.
func TestRuleUndoWaitsForTheRun(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionStar}, RuleAction{Kind: ActionRead}), true)
	e.rulesIdle(t)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	setRulesHook(t, func(stage string) error {
		if stage == "rules.action" {
			once.Do(func() { close(held); <-release }) // after the star, before the read
		}
		return nil
	})
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	<-held
	run := e.runOf(t, "a@x")
	undone := make(chan error, 1)
	go func() { _, err := e.s.UndoRuleRun(bg, run); undone <- err }()
	select {
	case err := <-undone:
		close(release) // let the run end, or Close waits on it
		t.Fatalf("undo did not wait for the run: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-undone)
	assert.Equal(t, []string{"star undone", "read undone"}, e.results(t, run))
	assert.Empty(t, e.flagsOf(t, "a@x"))
	assert.Equal(t, [][]any{{RuleRunUndone}}, e.rows(t, "SELECT state FROM rule_runs WHERE id = ?", run))
}

// A retry cut off between two actions goes on when Godwit opens again: it
// ran outside the consumer, whose event for the mail is long past.
func TestRuleRetryResumes(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionJunk}, RuleAction{Kind: ActionStar}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	e.rulesIdle(t)
	run := e.runOf(t, "a@x")
	require.Equal(t, []string{"junk failed", "star not_run"}, e.results(t, run))
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	e.rulesIdle(t)
	stopRulesAt(t, "rules.action")
	require.Error(t, e.s.RetryRuleRun(bg, run), "cut off after the junk")
	rulesHook = nil
	require.Equal(t, [][]any{{RuleRunRunning}}, e.rows(t, "SELECT state FROM rule_runs WHERE id = ?", run))
	e.reopen(t)
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM rule_runs WHERE id = ? AND state = ?", run, RuleRunDone) == 1
	},
		5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"junk done", "star done"}, e.results(t, run))
}

// A label the user set by hand after the rule's stays: undo checks the row
// in its own write, and counts the label as kept.
func TestRuleUndoKeepsALabelSetByHand(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionLabel, Label: "u1"}), true)
	e.bill(t, "a@x", "billing@aws.example", "Invoice")
	e.sync(t)
	e.waitRuns(t, 1)
	id := e.idOf(t, "a@x")
	require.Equal(t, "rule", e.labelOn(t, id))
	bills := store.Int(e.rows(t, "SELECT id FROM labels WHERE slug = 'u1'")[0][0])
	require.NoError(t, e.s.SetLabel(bg, []int64{id}, bills, true))
	require.Equal(t, "manual", e.labelOn(t, id))

	run := e.runOf(t, "a@x")
	kept, err := e.s.UndoRuleRun(bg, run)
	require.NoError(t, err)
	assert.Equal(t, 1, kept)
	assert.Equal(t, "manual", e.labelOn(t, id))
	assert.Equal(t, []string{"label kept"}, e.results(t, run))
}
