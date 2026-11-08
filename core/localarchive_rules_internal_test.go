package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Imported mail is old mail, not received (ADR 0036): a receive rule runs on
// the next new mail and never on the import.
func TestImportEMLRunsNoReceiveRule(t *testing.T) {
	e := newRulesEnv(t)
	e.saveRule(t, billsRule(), true)
	acc, err := e.s.AddLocalArchive(bg, "旧邮件")
	require.NoError(t, err)
	inbox := e.count(t, "SELECT id FROM folders WHERE account_id = ? AND role = 'inbox'", acc.ID)
	path := filepath.Join(t.TempDir(), "old.eml")
	require.NoError(t, os.WriteFile(path, []byte(eml("old@x", "AWS <billing@aws.example>", "alice@example.com", "Old invoice", day, "b")), 0o644))
	sum, err := e.s.ImportEML(bg, acc.ID, int64(inbox), []string{path})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Imported)

	e.bill(t, "new@x", "AWS <billing@aws.example>", "New invoice")
	e.sync(t)
	e.waitRuns(t, 1)

	require.Equal(t, [][]any{{e.idOf(t, "new@x")}}, e.rows(t, "SELECT message_id FROM rule_runs"))
	old := e.rows(t, "SELECT id FROM messages WHERE account_id = ?", acc.ID)
	require.Len(t, old, 1)
	require.Empty(t, e.labelOn(t, old[0][0].(int64)))
}
