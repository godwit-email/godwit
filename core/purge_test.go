package core

import (
	"strings"
	"testing"
	"time"

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
	// Its UIDVALIDITY is set to Trash's, so only the server-folder guard holds it.
	_, err := e.s.db.Execute(bg, "UPDATE messages SET folder_id = ?, server_uidvalidity = (SELECT uidvalidity FROM folders WHERE id = ?) WHERE message_id = 'stray@x'",
		e.folderID(t, "Trash"), e.folderID(t, "Trash"))
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
	assert.Zero(t, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'purge'"), "none even queued")
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
		_, err := e.s.db.Execute(bg, "UPDATE folders SET purged_at = 0")
		require.NoError(t, err)
		e.sync(t)
		assert.Equal(t, []string{"trash@x"}, e.onServer(t, "Trash"), "and holds on the next round")
		e.age(t, 2)
		e.sync(t)
		assert.Equal(t, []string{}, e.onServer(t, "Trash"))
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
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "Sent")), "a folder holds one role")
	assert.Error(t, e.s.SetRoleFolder(bg, e.acc.ID, "drafts", e.folderID(t, "Trash")))
	assert.Equal(t, "Sent", e.roles(t)["sent"])
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

// Gmail has no Archive: its All Mail is the archive, unless one is detected.
func TestAllMailIsArchive(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("All Mail", nil))
	e.sync(t)
	_, err := e.s.db.Execute(bg, "UPDATE folders SET role = CASE name WHEN 'All Mail' THEN 'all' ELSE '' END WHERE name IN ('Archive', 'All Mail')")
	require.NoError(t, err)
	assert.Equal(t, "All Mail", e.roles(t)["archive"])
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", 0))
	assert.NotContains(t, e.roles(t), "archive", "不使用 turns All Mail off too")
	require.NoError(t, e.s.SetRoleFolder(bg, e.acc.ID, "archive", e.folderID(t, "All Mail")))
	assert.Equal(t, "All Mail", e.roles(t)["archive"])
	_, err = e.s.db.Execute(bg, "UPDATE folders SET role_override = ''")
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "UPDATE folders SET role = 'archive' WHERE name = 'Archive'")
	require.NoError(t, err)
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

func TestPurgeClockStartsWhenTurnedOn(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.put(t, "Trash", msg("unknown@x"))
	e.sync(t)
	e.age(t, 30) // a month in Trash with the purge off
	_, err := e.s.db.Execute(bg, "UPDATE messages SET entered_at = 0 WHERE message_id = 'unknown@x'")
	require.NoError(t, err)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.sync(t)
	assert.Equal(t, []string{"m@x", "unknown@x"}, e.onServer(t, "Trash"), "only mail a day past turning it on")
	e.age(t, 2)
	e.sync(t)
	assert.Equal(t, []string{"unknown@x"}, e.onServer(t, "Trash"), "an unknown entered_at is never old")
}

func TestPurgeTurnedOffBeforePush(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 2)
	require.NoError(t, mailsync.QueuePurge(bg, e.s.db, e.dial(t), e.acc.ID))
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 0))
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"))
	assert.Equal(t, []string{"cancelled"}, e.opStates(t))
}

func TestPurgeWaitsForPendingFlags(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 2)
	e.im.Stop()
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "m@x")}, []string{`\Seen`}, nil))
	e.im.Restart()
	e.sync(t)
	assert.Equal(t, []string{`m@x \Seen`}, e.onServer(t, "Trash"))
}

// interruptPurge leaves m@x in Trash with \Deleted set and its purge pending:
// the connection drops at UID EXPUNGE.
func interruptPurge(t *testing.T, e *syncEnv) {
	t.Helper()
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 2)
	e.im.DropOnNextUIDExpunge()
	_ = e.s.SyncNow(bg, e.acc.ID)
	require.Equal(t, []string{`m@x \Deleted`}, e.onServer(t, "Trash"))
	require.Equal(t, 1, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'purge' AND state = 'pending' AND attempts = 1"))
}

func TestPurgeInterruptedLeavesNoDeleted(t *testing.T) {
	t.Run("rescue", func(t *testing.T) {
		e := newSyncEnv(t)
		interruptPurge(t, e)
		require.NoError(t, e.s.Move(bg, []int64{e.idOf(t, "m@x")}, e.folderID(t, "INBOX")))
		e.sync(t)
		assert.Equal(t, []string{"m@x"}, e.onServer(t, "INBOX"), "rescued without \\Deleted")
		assert.Equal(t, []string{}, e.onServer(t, "Trash"))
	})
	t.Run("turned off", func(t *testing.T) {
		e := newSyncEnv(t)
		interruptPurge(t, e)
		require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 0))
		e.sync(t)
		assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"), "the cancel removes \\Deleted")
		assert.Contains(t, e.opStates(t), "cancelled")
	})
}

func TestPurgeRacingRescue(t *testing.T) {
	for _, step := range []string{"selected", "stored"} {
		t.Run(step, func(t *testing.T) {
			e := newSyncEnv(t)
			e.put(t, "Trash", msg("m@x"))
			e.sync(t)
			require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
			e.age(t, 2)
			id, inbox := e.idOf(t, "m@x"), e.folderID(t, "INBOX")
			mailsync.PurgeStep = func(s string) {
				if s == step {
					require.NoError(t, e.s.Move(bg, []int64{id}, inbox))
				}
			}
			t.Cleanup(func() { mailsync.PurgeStep = nil })
			e.sync(t)
			mailsync.PurgeStep = nil
			e.sync(t)
			assert.Equal(t, []string{"m@x"}, e.onServer(t, "INBOX"))
			assert.Zero(t, e.audits(t))
			assert.Equal(t, inbox, store.Int(e.rows(t, "SELECT server_folder_id FROM messages WHERE id = ?", id)[0][0]))
		})
	}
}

func TestDeleteForeverExpungesFromTrashOnly(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("gone@x"))
	e.putFlagged(t, "Trash", msg("other-client@x"), `\Deleted`)
	e.put(t, "INBOX", msg("kept@x"))
	e.sync(t)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "gone@x")}, []string{`\Seen`}, nil))
	require.NoError(t, e.s.DeleteForever(bg, []int64{e.idOf(t, "gone@x")}))
	require.NoError(t, e.s.DeleteForever(bg, []int64{e.idOf(t, "kept@x")}))
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{`other-client@x \Deleted`}, e.onServer(t, "Trash"), "UID EXPUNGE, after the pending flags change")
	assert.Equal(t, []string{"kept@x"}, e.onServer(t, "INBOX"), "mail outside Trash is not purged")
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'gone@x'"))
	assert.Empty(t, e.ops(t))
}

func TestDeleteForeverRescued(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	id := e.idOf(t, "m@x")
	require.NoError(t, e.s.DeleteForever(bg, []int64{id}))
	require.NoError(t, e.s.Move(bg, []int64{id}, e.folderID(t, "INBOX")))
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "INBOX"))
	assert.Equal(t, []string{}, e.onServer(t, "Trash"))
}

func TestDeleteForeverNeedsUIDPlus(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t, testmail.WithoutUIDPlus())
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	require.NoError(t, e.s.DeleteForever(bg, []int64{e.idOf(t, "m@x")}))
	clock.Advance(UndoPurgeWindow)
	_ = e.s.SyncNow(bg, e.acc.ID) // reports the failed op
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM ops WHERE kind = 'purge' AND state = 'failed'"), "a failure the user sees, not a silent cancel")
}

func TestEmptyFolder(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("trash1@x"))
	e.put(t, "Trash", msg("trash2@x"))
	e.put(t, "INBOX", msg("kept@x"))
	e.sync(t)
	trashID := e.folderID(t, "Trash")
	ids, err := e.s.EmptyFolder(bg, trashID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{e.idOf(t, "trash1@x"), e.idOf(t, "trash2@x")}, ids)
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{}, e.onServer(t, "Trash"))
	assert.Equal(t, []string{"kept@x"}, e.onServer(t, "INBOX"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE folder_id = ?", trashID))
}

// trashShown is what the Trash shows: its list, its folder count and a search.
func (e *syncEnv) trashShown(t *testing.T) (listed, total, found int) {
	t.Helper()
	trash := e.folderID(t, "Trash")
	page, err := e.s.ListMessages(bg, View{FolderID: trash}, "", 10)
	require.NoError(t, err)
	fs, err := e.s.ListFolders(bg, e.acc.ID)
	require.NoError(t, err)
	for _, f := range fs {
		if f.ID == trash {
			total = f.Total
		}
	}
	hits, err := e.s.Search(bg, 0, "body", 10)
	require.NoError(t, err)
	return len(page.Items), total, len(hits)
}

func TestDeleteForeverWaitsForUndo(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("m@x"))
	e.sync(t)
	id := e.idOf(t, "m@x")
	require.NoError(t, e.s.DeleteForever(bg, []int64{id}))
	l, n, f := e.trashShown(t)
	assert.Equal(t, [3]int{0, 0, 0}, [3]int{l, n, f}, "gone from the list, the count and search at once")

	clock.Advance(UndoPurgeWindow - time.Millisecond)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"), "the undo window is still open")
	gone, err := e.s.UndoPurge(bg, []int64{id})
	require.NoError(t, err)
	assert.Zero(t, gone)
	l, n, f = e.trashShown(t)
	assert.Equal(t, [3]int{1, 1, 1}, [3]int{l, n, f}, "back after the undo")
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{"m@x"}, e.onServer(t, "Trash"), "an undone purge never runs")

	require.NoError(t, e.s.DeleteForever(bg, []int64{id}))
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{}, e.onServer(t, "Trash"))
	_, err = e.s.UndoPurge(bg, []int64{id})
	assert.ErrorIs(t, err, ErrPurgeTooLate)
}

func TestUndoPurgeCountsWhatIsGone(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("early@x"))
	e.put(t, "Trash", msg("late@x"))
	e.sync(t)
	early, late := e.idOf(t, "early@x"), e.idOf(t, "late@x")
	require.NoError(t, e.s.DeleteForever(bg, []int64{early}))
	clock.Advance(UndoPurgeWindow / 2)
	require.NoError(t, e.s.DeleteForever(bg, []int64{late}))
	clock.Advance(UndoPurgeWindow / 2)
	require.NoError(t, e.s.SyncNow(bg, e.acc.ID)) // pushes early's purge; its fetch drops the row
	require.Equal(t, []string{"late@x"}, e.onServer(t, "Trash"))

	gone, err := e.s.UndoPurge(bg, []int64{early, late})
	require.NoError(t, err)
	assert.Equal(t, 1, gone, "early was expunged before the undo")
	l, _, _ := e.trashShown(t)
	assert.Equal(t, 1, l, "late is back")
}

func TestUndoEmptyFolderKeepsEarlierDelete(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t)
	e.put(t, "Trash", msg("deleted@x"))
	e.put(t, "Trash", msg("emptied@x"))
	e.sync(t)
	require.NoError(t, e.s.DeleteForever(bg, []int64{e.idOf(t, "deleted@x")}))
	ids, err := e.s.EmptyFolder(bg, e.folderID(t, "Trash"))
	require.NoError(t, err)
	assert.Equal(t, []int64{e.idOf(t, "emptied@x")}, ids, "mail already being deleted is not queued again")

	gone, err := e.s.UndoPurge(bg, ids)
	require.NoError(t, err)
	assert.Zero(t, gone)
	clock.Advance(UndoPurgeWindow)
	e.sync(t)
	assert.Equal(t, []string{"emptied@x"}, e.onServer(t, "Trash"), "the undo takes back the emptying, not the delete before it")
}
