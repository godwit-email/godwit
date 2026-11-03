package core

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const carol = "carol@example.com"

// newForwardEnv is a sendEnv whose INBOX synced once, so later mail is new.
func newForwardEnv(t *testing.T) *sendEnv {
	t.Helper()
	e := newSendEnv(t, false)
	e.newMail(t, "old@x", "before")
	e.sync(t)
	return e
}

// forwardRule forwards mail from billing@ to to.
func forwardRule(to string, more ...RuleAction) RuleDoc {
	return rule(append([]RuleAction{{Kind: ActionForward, To: to}}, more...)...)
}

func (e *sendEnv) saveForward(t *testing.T, d RuleDoc, confirmed ...string) int64 {
	t.Helper()
	id, err := e.s.SaveRule(bg, Rule{Enabled: true, Doc: d, Confirmed: confirmed})
	require.NoError(t, err)
	return id
}

// billWithPDF is mail from billing@ with the attachment f0.pdf; hdr are extra header lines.
func billWithPDF(id string, hdr ...string) string {
	raw := strings.Replace(withAttachments(id, "your bill", 1), "From: bob@example.com", "From: billing@shop.example", 1)
	for _, h := range hdr {
		raw = h + "\r\n" + raw
	}
	return raw
}

// release lets the undo window pass and syncs, sending what is queued.
func (e *sendEnv) release(t *testing.T) []mail.Parsed {
	t.Helper()
	e.clock.Advance(UndoSendWindow)
	e.sync(t)
	var out []mail.Parsed
	for _, m := range e.smtp.Messages() {
		p, err := mail.Parse(m.Data)
		require.NoError(t, err)
		out = append(out, *p)
	}
	return out
}

// A forward goes through the outbox, with the attachments, to the confirmed
// recipient, marked as a rule's and audited as the rule's send.
func TestRuleForwardSends(t *testing.T) {
	e := newForwardEnv(t)
	id := e.saveForward(t, forwardRule(carol), carol)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	e.waitRuns(t, 1)

	assert.Equal(t, []string{"forward done"}, e.results(t, e.runOf(t, "bill@x")))
	assert.Equal(t, 1, e.outboxLen(t), "in the outbox, undoable")
	got := e.release(t)
	require.Len(t, got, 1)
	assert.Equal(t, []string{carol}, got[0].To)
	assert.True(t, got[0].RuleSent)
	require.Len(t, got[0].Attachments, 1)
	assert.Equal(t, "f0.pdf", got[0].Attachments[0].Filename)
	assert.Equal(t, [][]any{{AuditRule, "forward", "rule", id, "example.com"}},
		e.rows(t, "SELECT source, tool, target_kind, target_id, destination FROM audit_log WHERE source = ?", AuditRule))
}

// Retrying a failed run forwards to the recipient the rule confirmed.
func TestRuleRetryForwards(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, rule(RuleAction{Kind: ActionJunk}, RuleAction{Kind: ActionForward, To: carol}), carol)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	e.waitRuns(t, 1)
	run := e.runOf(t, "bill@x")
	require.Equal(t, []string{"junk failed", "forward not_run"}, e.results(t, run))
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	require.NoError(t, e.s.RetryRuleRun(bg, run))
	assert.Equal(t, []string{"junk done", "forward done"}, e.results(t, run))
	assert.Len(t, e.release(t), 1)
}

// Two retries of one failed run at once (a double click) run it once: one
// claims it, the other finds nothing to retry, and one mail is forwarded.
func TestRuleRetryOnce(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, rule(RuleAction{Kind: ActionJunk}, RuleAction{Kind: ActionForward, To: carol}), carol)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	e.waitRuns(t, 1)
	run := e.runOf(t, "bill@x")
	require.NoError(t, e.im.User.Create("Junk", nil))
	e.sync(t)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- e.s.RetryRuleRun(bg, run) }()
	}
	failed := 0
	for range 2 {
		if <-errs != nil {
			failed++
		}
	}
	assert.Equal(t, 1, failed, "one of the two finds nothing to retry")
	assert.Equal(t, []string{"junk done", "forward done"}, e.results(t, run))
	assert.Len(t, e.release(t), 1)
}

// holdRules installs, before the env starts its consumer (rulesHook is
// read there), a hook that holds the next delivery once armed, as a slow
// model's run would; held is closed then, release lets it go.
func holdRules(t *testing.T) (arm func(), held, release chan struct{}) {
	t.Helper()
	held, release = make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var once sync.Once
	setRulesHook(t, func(stage string) error {
		if stage == "rules.deliver" && armed.Load() {
			once.Do(func() { close(held); <-release })
		}
		return nil
	})
	return func() { armed.Store(true) }, held, release
}

// A rule saved while the consumer is behind runs only on mail logged after
// it: a bill that came before, still in the backlog, is not forwarded, as
// the confirmation says; one that comes after is.
func TestRuleForwardsOnlyMailAfterSaving(t *testing.T) {
	arm, held, release := holdRules(t)
	e := newForwardEnv(t)
	e.rulesIdle(t)
	arm()
	e.put(t, "INBOX", billWithPDF("before@x"))
	e.sync(t)
	<-held
	e.saveForward(t, forwardRule(carol), carol)
	close(release)
	e.rulesIdle(t)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs"), "the backlog's bill came before the rule")

	e.put(t, "INBOX", billWithPDF("after@x"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward done"}, e.results(t, e.runOf(t, "after@x")))
	assert.Len(t, e.release(t), 1)
}

// Turned off and on, a rule runs on what comes after, not on mail that came
// while it was off.
func TestRuleTurnedOnRunsOnLaterMail(t *testing.T) {
	arm, held, release := holdRules(t)
	e := newForwardEnv(t)
	id := e.saveForward(t, forwardRule(carol), carol)
	require.NoError(t, e.s.SetRuleEnabled(bg, id, false))
	e.rulesIdle(t)
	arm()
	e.put(t, "INBOX", billWithPDF("off@x"))
	e.sync(t)
	<-held
	require.NoError(t, e.s.SetRuleEnabled(bg, id, true))
	close(release)
	e.rulesIdle(t)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs"))
}

// The run over existing mail leaves out mail that came after saving, still
// in the consumer's backlog: that is new mail, which the consumer then runs
// with every action, the forward too.
func TestRuleExistingRunLeavesNewMail(t *testing.T) {
	arm, held, release := holdRules(t)
	e := newForwardEnv(t)
	e.rulesIdle(t)
	id := e.saveForward(t, forwardRule(carol, RuleAction{Kind: ActionStar}), carol)
	arm()
	e.put(t, "INBOX", billWithPDF("new@x"))
	e.sync(t)
	<-held
	n, err := e.s.RunRuleOnExisting(bg, id)
	require.NoError(t, err)
	assert.Zero(t, n, "the bill came after saving")
	close(release)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward done", "star done"}, e.results(t, e.runOf(t, "new@x")))
	assert.Len(t, e.release(t), 1)
}

// A rule turned off while an earlier rule's run on the mail is going starts
// no run when its turn comes: it is read as it is then, not as it was.
func TestRuleTurnedOffMidwayRunsNot(t *testing.T) {
	held, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var once sync.Once
	setRulesHook(t, func(stage string) error {
		if stage == "rules.action" && armed.Load() {
			once.Do(func() { close(held); <-release }) // the first rule's slow action, a PDF say
		}
		return nil
	})
	e := newForwardEnv(t)
	e.saveForward(t, rule(RuleAction{Kind: ActionStar}))
	later := e.saveForward(t, forwardRule(carol), carol)
	e.rulesIdle(t)
	armed.Store(true)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	<-held
	require.NoError(t, e.s.SetRuleEnabled(bg, later, false))
	close(release)
	e.rulesIdle(t)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM rule_runs"), "the first rule's only")
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_runs WHERE rule_id = ?", later))
	assert.Empty(t, e.release(t))
}

// A forward to a recipient not confirmed, saved so or edited into the
// stored rule, sends nothing and its hit says why.
func TestRuleForwardUnconfirmedNeverSends(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, forwardRule(carol, RuleAction{Kind: ActionRead}))
	e.put(t, "INBOX", billWithPDF("a@x"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward failed", "read not_run"}, e.results(t, e.runOf(t, "a@x")))

	id := e.saveForward(t, forwardRule(carol), carol)
	_, err := e.s.db.Execute(bg, `UPDATE rules SET doc = json_set(doc, '$.actions[0].to', 'mallory@evil.example') WHERE id = ?`, id)
	require.NoError(t, err)
	e.put(t, "INBOX", billWithPDF("b@x"))
	e.sync(t)
	e.waitRuns(t, 3)
	assert.Equal(t, 3, e.count(t, "SELECT count(*) FROM rule_action_results WHERE kind = 'forward' AND result = ?", RuleFailUnconfirmed))
	assert.Zero(t, e.outboxLen(t))
	assert.Empty(t, e.release(t))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM drafts"))
}

// SaveRule keeps only the confirmed recipients the rule forwards to.
func TestSaveRuleKeepsConfirmedForwardRecipients(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, forwardRule("Carol <Carol@Example.com>"), carol, "dave@example.com")
	rs, err := e.s.Rules(bg)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, []string{carol}, rs[0].Confirmed)
	_, err = e.s.SaveRule(bg, Rule{Doc: forwardRule("not an address")})
	assert.ErrorIs(t, err, ErrInvalidRule)
}

// The confirmation flags a recipient no account has written to.
func TestFirstTimeRecipients(t *testing.T) {
	e := newForwardEnv(t)
	e.put(t, "Sent", eml("s@x", "alice@example.com", "Bob <bob@example.com>", "hi", day, "b"))
	e.sync(t)
	got, err := e.s.FirstTimeRecipients(bg, []string{"BOB@example.com", carol})
	require.NoError(t, err)
	assert.Equal(t, []string{carol}, got)
}

// A crash after the forward was queued, before the run finished, resumes
// without forwarding again.
func TestRuleForwardOnceAcrossCrash(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, forwardRule(carol, RuleAction{Kind: ActionRead}), carol)
	stopped := stopRulesAt(t, "rules.action")
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	<-stopped
	rulesHook = nil
	e.reopen(t)
	e.waitRuns(t, 1)

	assert.Equal(t, []string{"forward done", "read done"}, e.results(t, e.runOf(t, "bill@x")))
	assert.Len(t, e.release(t), 1)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM audit_log WHERE source = ?", AuditRule))
}

// Mail a rule forwarded carries the marker, and no rule forwards it again:
// two rules can't forward to each other in a loop.
func TestRuleNeverForwardsMarkedMail(t *testing.T) {
	e := newForwardEnv(t)
	e.saveForward(t, forwardRule(carol, RuleAction{Kind: ActionRead}), carol)
	e.put(t, "INBOX", billWithPDF("fwd@x", mail.RuleHeader+": 7"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward skipped", "read done"}, e.results(t, e.runOf(t, "fwd@x")))
	assert.Zero(t, e.outboxLen(t))
	assert.Empty(t, e.release(t))
}

// A run over the mail there is (here the daily one) never forwards.
func TestRuleForwardNotOverExistingMail(t *testing.T) {
	e := newForwardEnv(t)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	d := forwardRule(carol)
	d.Trigger = TriggerDaily
	e.saveForward(t, d, carol)
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward skipped"}, e.results(t, e.runOf(t, "bill@x")))
	assert.Zero(t, e.outboxLen(t))
	assert.Empty(t, e.release(t))
}

// Nobody is there to read a plugin's send warning: the forward fails with
// the reason, and its draft is gone.
func TestRuleForwardFailsOnSendWarning(t *testing.T) {
	e := newForwardEnv(t)
	startTestPlugin(t, e.s, "warner", sendPerms, checker(`d => ({ warn: "external recipient" })`))
	e.saveForward(t, forwardRule(carol), carol)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, [][]any{{RuleActionFailed, RuleFailSendCheck}}, e.rows(t, "SELECT state, result FROM rule_action_results"))
	assert.Zero(t, e.outboxLen(t))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM drafts"))
}
