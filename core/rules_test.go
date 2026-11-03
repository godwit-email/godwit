package core

import (
	"context"
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
	} {
		_, err := e.s.SaveRule(bg, Rule{Doc: d})
		assert.ErrorIs(t, err, ErrInvalidRule)
	}
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
