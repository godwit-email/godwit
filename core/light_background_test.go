package core

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A round of the inbox fetches it and stops there: the folders it skips come
// with the next full round.
func TestFetchInboxOnly(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", eml("in@x", "Bob <bob@example.com>", "alice@example.com", "inbox", "Mon, 05 Oct 2026 09:00:00 +0000", "first"))
	e.put(t, "Archive", eml("ar@x", "Bob <bob@example.com>", "alice@example.com", "archived", "Mon, 05 Oct 2026 09:05:00 +0000", "older"))
	recv := e.receiver(t)
	_, err := mailsync.Sync(bg, e.s.db, recv, e.acc.ID, e.s.bus) // the folders are known
	require.NoError(t, err)

	e.put(t, "INBOX", eml("in2@x", "Bob <bob@example.com>", "alice@example.com", "inbox again", "Mon, 05 Oct 2026 10:00:00 +0000", "newer"))
	e.put(t, "Archive", eml("ar2@x", "Bob <bob@example.com>", "alice@example.com>", "archived again", "Mon, 05 Oct 2026 10:05:00 +0000", "older"))

	_, err = mailsync.Fetch(bg, e.s.db, recv, e.acc.ID, e.s.bus, []backend.FolderRef{"INBOX"})
	require.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'in2@x'"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'ar2@x'"))

	_, err = mailsync.Sync(bg, e.s.db, recv, e.acc.ID, e.s.bus)
	require.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'ar2@x'"))
}

// SyncNow stays whole while the background is light: a sync the user asks for
// is not narrowed.
func TestLightBackgroundKeepsSyncNowWhole(t *testing.T) {
	e := newSyncEnv(t)
	e.s.SetLightBackground(true)
	defer e.s.SetLightBackground(false)
	e.put(t, "Archive", eml("ar@x", "Bob <bob@example.com>", "alice@example.com", "archived", "Mon, 05 Oct 2026 09:05:00 +0000", "older"))

	e.sync(t)

	assert.True(t, e.s.LightBackground())
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'ar@x'"))
}

// The worker's rounds follow the mode: light, they carry the inbox only, and
// turning it off wakes them into a full round.
func TestWorkerLightBackgroundSyncsInboxOnly(t *testing.T) {
	e, ch := newWorkerEnv(t)
	e.s.SetLightBackground(true)
	e.put(t, "INBOX", eml("in@x", "Bob <bob@example.com>", "alice@example.com", "inbox", "Mon, 05 Oct 2026 09:00:00 +0000", "new mail"))
	e.put(t, "Archive", eml("ar@x", "Bob <bob@example.com>", "alice@example.com>", "archived", "Mon, 05 Oct 2026 09:05:00 +0000", "older mail"))

	e.s.wakeWorker(e.acc.ID)
	waitEvent(t, ch, events.SyncFinished, 5*time.Second)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'in@x'"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'ar@x'"))

	e.s.SetLightBackground(false)
	waitEvent(t, ch, events.SyncFinished, 5*time.Second)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'ar@x'"))
}

func TestLightBackgroundKeepsForegroundCatchup(t *testing.T) {
	e, _ := newWorkerEnv(t)
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	unlock = sync.OnceFunc(unlock)
	t.Cleanup(unlock)
	e.put(t, "INBOX", msg("watch@x"))
	waitSignal(t, e.lockWait, "worker waiting for lock", 5*time.Second)
	e.put(t, "Archive", msg("catchup@x"))
	e.s.SetLightBackground(true)
	e.s.SetLightBackground(false)
	e.s.workersMu.Lock()
	wake := e.s.workers[e.acc.ID].wake
	e.s.workersMu.Unlock()
	// 节流可以消费普通唤醒，但不能丢掉尚未执行的前台补全请求。
	require.True(t, waitOrWake(bg, time.Second, wake))
	e.put(t, "INBOX", msg("watch-again@x"))
	unlock()
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'catchup@x'") == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestLightBackgroundClaimsMoves(t *testing.T) {
	for _, awaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("awaiting=%v", awaiting), func(t *testing.T) {
			e, _ := newWorkerEnv(t, testmail.WithoutUIDPlus())
			e.s.stopWorker(e.acc.ID)
			e.put(t, "INBOX", msg("move@x"))
			e.sync(t)
			id := e.idOf(t, "move@x")
			require.NoError(t, e.s.Move(bg, []int64{id}, e.folderID(t, "Archive")))
			require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Seen`}, nil))
			if awaiting {
				require.NoError(t, mailsync.PushOps(bg, e.s.db, e.receiver(t), e.acc.ID, e.s.clk().Now()))
				require.Equal(t, []string{"awaiting_claim", "pending"}, e.opStates(t))
			}
			e.s.SetLightBackground(true)
			e.s.startWorker(e.acc.ID)
			require.Eventually(t, func() bool {
				return e.count(t, "SELECT count(*) FROM ops WHERE state != 'done'") == 0
			}, 5*time.Second, 20*time.Millisecond)
			assert.Equal(t, []string{`move@x \Seen`}, e.onServer(t, "Archive"))
		})
	}
}

func TestLightBackgroundRecoversPendingGmailUndo(t *testing.T) {
	e, _ := newWorkerEnv(t, testmail.Gmail(), testmail.WithoutMove())
	e.s.stopWorker(e.acc.ID)
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	m := e.im.AddGmail(msg("undo@x"), nil, "INBOX")
	e.sync(t)
	id := e.idOf(t, "undo@x")
	u, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
	require.NoError(t, err)
	e.clock.Advance(UndoArchiveWindow)
	e.sync(t)
	require.Empty(t, m.Folders())
	require.NoError(t, e.s.UndoArchiveThreads(bg, u))
	e.sync(t)
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "pending", pending[0].State)

	require.NoError(t, e.im.User.Create(testmail.GmailAllMail, nil))
	m.Label(testmail.GmailAllMail)
	e.s.SetLightBackground(true)
	e.s.startWorker(e.acc.ID)
	require.Eventually(t, func() bool {
		pending, err := e.s.PendingOps(bg, e.acc.ID)
		return err == nil && len(pending) == 0
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"INBOX", testmail.GmailAllMail}, m.Folders())
	assert.Equal(t, []int64{id}, e.syncEnv.listed(t, "INBOX"))
}

func TestLightBackgroundFinishesPurges(t *testing.T) {
	for _, scenario := range []string{"manual", "automatic", "already-done", "gmail", "gmail-awaiting-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			var opts []testmail.Option
			if scenario == "gmail" || scenario == "gmail-awaiting-cleanup" {
				opts = append(opts, testmail.Gmail())
			}
			e, ch := newWorkerEnv(t, opts...)
			e.s.stopWorker(e.acc.ID)
			trash := e.roles(t)["trash"]
			require.NotEmpty(t, trash)
			e.put(t, trash, msg("purge@x"))
			e.sync(t)
			id := e.idOf(t, "purge@x")
			if scenario == "automatic" {
				require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 7))
				e.age(t, 8)
			} else {
				require.NoError(t, e.s.DeleteForever(bg, []int64{id}))
				e.clock.Advance(UndoPurgeWindow)
			}
			if scenario == "already-done" || scenario == "gmail-awaiting-cleanup" {
				recv := e.receiver(t)
				require.NoError(t, mailsync.PushOps(bg, e.s.db, recv, e.acc.ID, e.s.clk().Now()))
				require.Equal(t, []string{"done"}, e.opStates(t))
				if scenario == "gmail-awaiting-cleanup" {
					// 位置已经提交删除，但整轮收尾尚未执行。
					_, err := mailsync.Fetch(bg, e.s.db, recv, e.acc.ID, e.s.bus,
						[]backend.FolderRef{backend.FolderRef(trash), backend.FolderRef(e.roles(t)["all"])})
					require.NoError(t, err)
					require.Zero(t, e.count(t, "SELECT count(*) FROM message_locations WHERE message_id = ?", id))
					require.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE id = ?", id))
				}
			}
			e.s.SetLightBackground(true)
			collect(ch)
			e.s.startWorker(e.acc.ID)
			waitEvent(t, ch, events.SyncFinished, 5*time.Second)
			assert.Empty(t, e.onServer(t, trash))
			assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE id = ?", id))
			assert.Zero(t, e.count(t, "SELECT count(*) FROM message_locations WHERE message_id = ?", id))
		})
	}
}

func TestLightBackgroundContinuesExplicitBackfill(t *testing.T) {
	setFor(t, &mailsync.BackfillPage, 2)
	setFor(t, &mailsync.BackfillPages, 1)
	e, ch := newWorkerEnv(t)
	e.s.stopWorker(e.acc.ID)
	e.setRange(t, SyncRange{Limit: 1})
	for i := range 11 {
		e.put(t, "Archive", msg(fmt.Sprintf("backfill%d@x", i)))
	}
	e.sync(t)
	e.setRange(t, SyncRange{})
	e.s.SetLightBackground(true)
	collect(ch)
	e.s.startWorker(e.acc.ID)
	waitEvent(t, ch, events.SyncFinished, 5*time.Second)
	e.sync(t)
	require.Eventually(t, func() bool {
		return len(e.stored(t, "Archive")) == 11 && e.count(t, "SELECT count(*) FROM folders WHERE name = 'Archive' AND filled_since IS NOT NULL") == 1
	}, 5*time.Second, 20*time.Millisecond)
	e.put(t, "Archive", msg("after-backfill@x"))
	e.put(t, "INBOX", msg("inbox-after-backfill@x"))
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'inbox-after-backfill@x'") == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'after-backfill@x'"))
}

// roundCost is what one sync round took: wall time, bytes the server sent and
// bytes the process allocated.
type roundCost struct {
	took        time.Duration
	sent, alloc uint64
}

// A background round's cost is what a window behind another app pays every few
// minutes. The light round reads the inbox alone, so the server sends it a
// fraction of the bytes of a round that reads every folder.
func TestBackgroundRoundCost(t *testing.T) {
	const folders, perFolder = 20, 20
	e := newSyncEnv(t)
	for i := range folders {
		name := fmt.Sprintf("Projects/%02d", i+1)
		require.NoError(t, e.im.User.Create(name, &imap.CreateOptions{}))
		for j := range perFolder {
			e.put(t, name, eml(fmt.Sprintf("f%d-%d@x", i, j), "Bob <bob@example.com>", "alice@example.com",
				fmt.Sprintf("folder %d message %d", i, j), "Mon, 05 Oct 2026 09:00:00 +0000", "body"))
		}
	}
	for j := range perFolder {
		e.put(t, "INBOX", eml(fmt.Sprintf("in-%d@x", j), "Bob <bob@example.com>", "alice@example.com",
			fmt.Sprintf("inbox message %d", j), "Mon, 05 Oct 2026 09:00:00 +0000", "body"))
	}

	recv := e.receiver(t)
	round := func(only []backend.FolderRef) roundCost {
		t.Helper()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		sent := e.im.BytesSent()
		start := time.Now()
		if only == nil {
			_, err := mailsync.Sync(bg, e.s.db, recv, e.acc.ID, e.s.bus)
			require.NoError(t, err)
		} else {
			_, err := mailsync.Fetch(bg, e.s.db, recv, e.acc.ID, e.s.bus, only)
			require.NoError(t, err)
		}
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		return roundCost{took, uint64(e.im.BytesSent() - sent), after.TotalAlloc - before.TotalAlloc}
	}

	round(nil) // the first sync downloads every folder
	rows := e.rows(t, "SELECT server_ref FROM folders WHERE account_id = ? AND role = 'inbox'", e.acc.ID)
	require.Len(t, rows, 1)
	inbox := []backend.FolderRef{backend.FolderRef(store.Str(rows[0][0]))}
	full := round(nil)
	light := round(inbox)
	t.Logf("%d folders, %d messages: full round %v, %d KiB from the server, %d KiB allocated; inbox only %v, %d KiB, %d KiB",
		folders+1, (folders+1)*perFolder, full.took, full.sent>>10, full.alloc>>10, light.took, light.sent>>10, light.alloc>>10)
	assert.Less(t, light.sent*4, full.sent, "the light round sent %d bytes, the full round %d", light.sent, full.sent)
}
