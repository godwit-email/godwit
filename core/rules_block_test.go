package core

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuleBlockFailureDoesNotStopConsumer(t *testing.T) {
	e := newRulesEnv(t)
	blocked := make([]string, maxBlockedSenders)
	for idx := range blocked {
		blocked[idx] = fmt.Sprintf("billing@sender%d.example", idx)
	}
	require.NoError(t, e.s.writeList(bg, blockedSenders, blocked))
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.saveRule(t, rule(RuleAction{Kind: ActionStar}), true)
	e.bill(t, "full@x", "billing@evil.example", "Invoice")
	e.bill(t, "next@x", blocked[0], "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	assert.Equal(t, 4, e.count(t, "SELECT count(*) FROM rule_runs"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM rule_action_results WHERE kind = 'block' AND state = 'failed'"))
	assert.Contains(t, e.flagsOf(t, "next@x"), `\Flagged`)
}

func TestRuleBlockInvalidSenderRecordsFailure(t *testing.T) {
	e := newRulesEnv(t)
	e.bill(t, "a@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	msgID := e.idOf(t, "a@x")
	_, err := e.s.db.Execute(bg, "UPDATE messages SET from_addr = 'invalid' WHERE id = ?", msgID)
	require.NoError(t, err)
	doc := rule(RuleAction{Kind: ActionBlock}, RuleAction{Kind: ActionStar})
	id := e.saveRule(t, doc, true)
	require.NoError(t, e.s.runRule(bg, Rule{ID: id, Doc: doc}, msgID, TriggerExisting))
	assert.Equal(t, []string{"block failed", "star not_run"}, e.results(t, e.runOf(t, "a@x")))
}

func TestRuleBlockCommitFailureRollsBack(t *testing.T) {
	e := newRulesEnv(t)
	e.bill(t, "a@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	doc := rule(RuleAction{Kind: ActionBlock})
	id := e.saveRule(t, doc, true)
	_, err := e.s.db.Execute(bg, `CREATE TRIGGER fail_block_result BEFORE INSERT ON rule_action_results
		WHEN NEW.kind = 'block' BEGIN SELECT RAISE(ABORT, 'injected result failure'); END`)
	require.NoError(t, err)
	require.Error(t, e.s.runRule(bg, Rule{ID: id, Doc: doc}, e.idOf(t, "a@x"), TriggerExisting))
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Empty(t, blocked)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM rule_action_results"))
	_, err = e.s.db.Execute(bg, "DROP TRIGGER fail_block_result")
	require.NoError(t, err)
	e.reopen(t)
	e.waitRuns(t, 1)
	kept, err := e.s.UndoRuleRun(bg, e.runOf(t, "a@x"))
	require.NoError(t, err)
	assert.Zero(t, kept)
	blocked, err = e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Empty(t, blocked)
}

func TestRuleBlockUndoCommitFailureRollsBack(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.bill(t, "a@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	runID := e.runOf(t, "a@x")
	_, err := e.s.db.Execute(bg, `CREATE TRIGGER fail_block_undo BEFORE UPDATE OF state ON rule_action_results
		WHEN NEW.kind = 'block' BEGIN SELECT RAISE(ABORT, 'injected undo failure'); END`)
	require.NoError(t, err)
	_, err = e.s.UndoRuleRun(bg, runID)
	require.Error(t, err)
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Contains(t, blocked, "billing@evil.example")
	assert.Equal(t, []string{"block done"}, e.results(t, runID))
	_, err = e.s.db.Execute(bg, "DROP TRIGGER fail_block_undo")
	require.NoError(t, err)
	kept, err := e.s.UndoRuleRun(bg, runID)
	require.NoError(t, err)
	assert.Zero(t, kept)
	blocked, err = e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Empty(t, blocked)
}

func TestRuleBlockUndoAfterMessageDeleted(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.bill(t, "deleted@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	runID := e.runOf(t, "deleted@x")
	_, err := e.s.db.Execute(bg, "DELETE FROM messages WHERE id = ?", e.idOf(t, "deleted@x"))
	require.NoError(t, err)
	kept, err := e.s.UndoRuleRun(bg, runID)
	require.NoError(t, err)
	assert.Zero(t, kept)
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Empty(t, blocked)
	assert.Equal(t, []string{"block undone"}, e.results(t, runID))
}

func TestRuleBlockUndoPreservesManualReblock(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.bill(t, "manual@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	runID := e.runOf(t, "manual@x")
	require.NoError(t, e.s.UnblockSender(bg, "billing@evil.example"))
	require.NoError(t, e.s.BlockSender(bg, "billing@evil.example"))
	_, err := e.s.UndoRuleRun(bg, runID)
	require.NoError(t, err)
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Contains(t, blocked, "billing@evil.example")
	assert.Equal(t, []string{"block kept"}, e.results(t, runID))
}

func TestRuleBlockUndoPreservesExistingBlock(t *testing.T) {
	e := newRulesEnv(t)
	require.NoError(t, e.s.BlockSender(bg, "billing@evil.example"))
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}), true)
	e.bill(t, "existing@x", "billing@evil.example", "Invoice")
	e.sync(t)
	e.rulesIdle(t)
	kept, err := e.s.UndoRuleRun(bg, e.runOf(t, "existing@x"))
	require.NoError(t, err)
	assert.Equal(t, 1, kept)
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Contains(t, blocked, "billing@evil.example")
}

func TestRuleBlockRestartPreservesManualUnblock(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, rule(RuleAction{Kind: ActionBlock}, RuleAction{Kind: ActionStar}), true)
	stopped := stopRulesAt(t, "rules.action")
	e.bill(t, "a@x", "billing@evil.example", "Invoice")
	e.sync(t)
	<-stopped
	require.NoError(t, e.s.UnblockSender(bg, "billing@evil.example"))
	rulesHook = nil
	e.reopen(t)
	e.waitRuns(t, 1)
	blocked, err := e.s.BlockedSenders(bg)
	require.NoError(t, err)
	assert.Empty(t, blocked)
	assert.Contains(t, e.flagsOf(t, "a@x"), `\Flagged`)
}
