package core

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
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
