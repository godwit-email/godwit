package core

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// age moves every purge clock of the account (entered_at, role_since, the
// policies' since and the folders' last purge) days into the past.
func (e *syncEnv) age(t *testing.T, days int) {
	t.Helper()
	d := days * 86400
	for _, sql := range []string{
		"UPDATE messages SET entered_at = entered_at - ?",
		"UPDATE folders SET role_since = role_since - ?, purged_at = purged_at - ?",
		"UPDATE purge_policies SET since = since - ?",
	} {
		args := []any{d, d}[:strings.Count(sql, "?")]
		_, err := e.s.db.Execute(bg, sql, args...)
		require.NoError(t, err)
	}
}

func (e *syncEnv) roles(t *testing.T) map[string]string {
	t.Helper()
	fs, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range fs {
		if f.Role != "" {
			out[f.Role] = f.Name
		}
	}
	return out
}

func (e *syncEnv) audits(t *testing.T) int {
	t.Helper()
	return e.count(t, "SELECT count(*) FROM audit_log WHERE source = 'purge'")
}

func TestPurgeDefaultIsNever(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("old@x"))
	e.sync(t)
	p, err := e.s.Purges(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]Purge{"junk": {}, "trash": {}}, p)
	e.age(t, 400)
	e.sync(t)
	assert.Equal(t, []string{"old@x"}, e.onServer(t, "Trash"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM ops"))
}

func TestPurgeExpungesOldTrashDaily(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("old@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 7))
	e.age(t, 8)
	e.putFlagged(t, "Trash", msg("other-client@x"), `\Deleted`)
	e.put(t, "Trash", msg("new@x"))
	e.sync(t)

	assert.Equal(t, []string{`other-client@x \Deleted`, "new@x"}, e.onServer(t, "Trash"), "UID EXPUNGE leaves other \\Deleted mail")
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'old@x'"), "the fetch after the push drops the row")
	assert.Equal(t, 1, e.audits(t))
	reason := store.Str(e.rows(t, "SELECT reason FROM audit_log WHERE source = 'purge'")[0][0])
	assert.Contains(t, reason, "1 message(s)")
	assert.Contains(t, reason, "7 day(s)")

	// new@x ages, but the folder was purged today.
	_, err := e.s.db.Execute(bg, "UPDATE messages SET entered_at = entered_at - 30 * 86400 WHERE message_id = 'new@x'")
	require.NoError(t, err)
	e.sync(t)
	assert.Contains(t, e.onServer(t, "Trash"), "new@x")
	_, err = e.s.db.Execute(bg, "UPDATE folders SET purged_at = purged_at - 86400")
	require.NoError(t, err)
	e.sync(t)
	assert.Equal(t, []string{`other-client@x \Deleted`}, e.onServer(t, "Trash"))
	assert.Equal(t, 2, e.audits(t))
}

func TestPurgeSparesJustMovedOldMessage(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("m@x")) // its Date header is long past
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 30)
	require.NoError(t, e.s.Delete(bg, []int64{e.idOf(t, "m@x")}))
	e.sync(t)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"))
	assert.Zero(t, e.audits(t))
}

func TestPurgeSkipsPendingMoves(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("out@x"))
	e.put(t, "INBOX", msg("in@x"))
	e.put(t, "INBOX", msg("stray@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	// Shown in Trash but on the server in INBOX, with no move queued.
	_, err := e.s.db.Execute(bg, "UPDATE messages SET folder_id = ? WHERE message_id = 'stray@x'", e.folderID(t, "Trash"))
	require.NoError(t, err)
	e.im.Stop()
	require.NoError(t, e.s.Move(bg, []int64{e.idOf(t, "out@x")}, e.folderID(t, "INBOX")))
	require.NoError(t, e.s.Delete(bg, []int64{e.idOf(t, "in@x")}))
	e.age(t, 30) // even with old clocks: in@x is not on the server in Trash yet, out@x not shown there
	e.reopen(t)
	e.im.Restart()
	e.sync(t)
	assert.Equal(t, []string{"stray@x", "out@x"}, e.onServer(t, "INBOX"))
	assert.Equal(t, []string{"in@x"}, e.onServer(t, "Trash"))
	assert.Zero(t, e.audits(t))
}

func TestPurgeRescueCancelsQueuedPurge(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Bin", nil))
	e.put(t, "Trash", msg("m@x"))
	e.put(t, "Trash", msg("n@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 2)
	require.NoError(t, mailsync.QueuePurge(bg, e.s.db, e.dial(t), e.acc.ID))
	require.Equal(t, 2, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'purge' AND state = 'pending'"))

	require.NoError(t, e.s.Move(bg, []int64{e.idOf(t, "m@x")}, e.folderID(t, "INBOX")))
	assert.Equal(t, []any{"cancelled", nil}, e.rows(t, "SELECT o.state, m.depends_on FROM ops o JOIN ops m ON m.message_id = o.message_id AND m.kind = 'move' WHERE o.kind = 'purge' AND o.message_id = ?", e.idOf(t, "m@x"))[0],
		"the rescue cancels the purge at once and does not wait for it")
	// n@x's purge is checked again at push: its folder is no longer Trash.
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", e.folderID(t, "Bin")))
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "INBOX"))
	assert.Equal(t, []string{"n@x"}, e.onServer(t, "Trash"))
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'purge' AND state = 'cancelled'"))
	assert.Empty(t, e.ops(t), "a cancelled purge is not a problem to show")
}

func TestPurgeNoneRole(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", 0))
	e.age(t, 30)
	e.sync(t)
	e.age(t, 30)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"))
	assert.NotContains(t, e.roles(t), "trash")
	var nf *ErrNoRoleFolder
	assert.ErrorAs(t, e.s.Delete(bg, []int64{e.idOf(t, "m@x")}), &nf)
}

func TestPurgeRoleChangeRestartsClock(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		e := newSyncEnv(t)
		require.NoError(t, e.im.User.Create("Bin", nil))
		e.put(t, "Bin", msg("kept@x"))
		e.sync(t)
		require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
		e.age(t, 30)
		require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", e.folderID(t, "Bin")))
		e.sync(t)
		_, err := e.s.db.Execute(bg, "UPDATE folders SET purged_at = 0")
		require.NoError(t, err)
		e.sync(t)
		assert.Equal(t, []string{"kept@x"}, e.onServer(t, "Bin"), "Bin's mail is old, but Bin is Trash only from now")
		e.age(t, 2)
		e.sync(t)
		assert.Equal(t, []string{}, e.onServer(t, "Bin"))
	})
	t.Run("detected", func(t *testing.T) {
		e := newSyncEnv(t)
		e.put(t, "Trash", msg("kept@x"))
		e.sync(t)
		require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
		e.age(t, 30)
		for _, r := range []string{"", "trash"} { // detection changed it and back, between two purges
			_, err := e.s.db.Execute(bg, "UPDATE folders SET role = ? WHERE name = 'Trash'", r)
			require.NoError(t, err)
		}
		e.sync(t)
		assert.Equal(t, []string{"kept@x"}, e.onServer(t, "Trash"))
	})
	t.Run("server rename", func(t *testing.T) {
		e := newSyncEnv(t)
		require.NoError(t, e.im.User.Create("Bin", nil))
		e.put(t, "Bin", msg("bin@x"))
		e.put(t, "Trash", msg("trash@x"))
		e.sync(t)
		require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", e.folderID(t, "Bin")))
		require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
		e.age(t, 30)
		require.NoError(t, e.im.User.Rename("Bin", "Bin2", nil))
		e.sync(t)
		e.sync(t)
		assert.Equal(t, "Trash", e.roles(t)["trash"], "the role falls back to detection")
		assert.Equal(t, []string{"bin@x"}, e.onServer(t, "Bin2"))
		assert.Equal(t, []string{"trash@x"}, e.onServer(t, "Trash"), "the fallback's clock starts now")
	})
}

func TestRoleFolderRefusals(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Bin", nil))
	e.sync(t)
	smtpAddr, _ := testmail.StartSMTP(t)
	other, err := e.s.AddAccount(bg, internalInput(t, e.im.Addr, smtpAddr, "bob@example.com", false))
	require.NoError(t, err)
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", e.folderID(t, "INBOX")))
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "INBOX")))
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "junk", e.folderID(t, "Sent")))
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "trash", e.folderID(t, "Archive")))
	assert.Error(t, e.s.SetRoleFolder(bg, other.ID, "trash", e.folderID(t, "Bin")), "another account's folder")
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "inbox", e.folderID(t, "Bin")))
	assert.Error(t, e.s.SetPurge(bg, e.acc.ID, "trash", 3))
	assert.Error(t, e.s.SetPurge(bg, e.acc.ID, "archive", 1))
	assert.Equal(t, map[string]string{"inbox": "INBOX", "sent": "Sent", "trash": "Trash", "archive": "Archive"}, e.roles(t))
}

func TestPurgeDuplicateTrashFolders(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Deleted Items", nil))
	e.put(t, "Deleted Items", msg("dup@x"))
	e.put(t, "Trash", msg("t@x"))
	e.sync(t)
	assert.Equal(t, "Deleted Items", e.roles(t)["trash"], "one folder per role: the lowest id (LIST order)")
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 30)
	e.sync(t)
	assert.Equal(t, []string{}, e.onServer(t, "Deleted Items"))
	assert.Equal(t, []string{"t@x"}, e.onServer(t, "Trash"), "the other trash-named folder has no role")
}

func TestPurgeCountsFromFirstSeen(t *testing.T) {
	e := newSyncEnv(t)
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 30)
	e.put(t, "Trash", msg("elsewhere@x")) // another client moved old mail in
	e.sync(t)
	_, err := e.s.db.Execute(bg, "UPDATE folders SET purged_at = 0")
	require.NoError(t, err)
	e.sync(t)
	assert.Equal(t, []string{"elsewhere@x"}, e.onServer(t, "Trash"))
}

func TestPurgeNeedsUIDPlus(t *testing.T) {
	e := newSyncEnv(t, testmail.WithoutUIDPlus())
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 30)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"))
	p, err := e.s.Purges(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, Purge{Days: 1, Blocked: true}, p["trash"])
}

func TestRoleOverrideSurvivesSync(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Bin", nil))
	e.put(t, "INBOX", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "Bin")))
	e.sync(t)
	assert.Equal(t, "Bin", e.roles(t)["archive"])
	require.NoError(t, e.s.MoveToRole(bg, []int64{e.idOf(t, "m@x")}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Bin"))

	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", 0))
	e.sync(t)
	assert.NotContains(t, e.roles(t), "archive", "不使用 survives sync too")
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "Archive")))
	assert.Equal(t, "Archive", e.roles(t)["archive"])
}

func TestEnteredAtFollowsFolder(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", msg("m@x"))
	e.sync(t)
	id := e.idOf(t, "m@x")
	entered := func() int64 {
		return store.Int(e.rows(t, "SELECT unixepoch() - entered_at FROM messages WHERE id = ?", id)[0][0])
	}
	assert.Less(t, entered(), int64(5), "imported: first seen now")
	e.age(t, 10)
	require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Seen`}, nil))
	e.sync(t)
	assert.Greater(t, entered(), int64(86400), "a flag change keeps it")
	require.NoError(t, e.s.Delete(bg, []int64{id}))
	assert.Less(t, entered(), int64(5), "a move restarts it")
	e.age(t, 10)
	e.sync(t)
	assert.Greater(t, entered(), int64(86400), "the move's push keeps it")
}
