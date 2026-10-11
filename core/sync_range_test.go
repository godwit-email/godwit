package core

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// putAt appends msg(id) to folder with INTERNALDATE at, which SINCE matches.
func (e *syncEnv) putAt(t *testing.T, folder, id string, at time.Time) {
	t.Helper()
	_, err := e.im.User.Append(folder, strings.NewReader(msg(id)), &imap.AppendOptions{Time: at})
	require.NoError(t, err)
}

func (e *syncEnv) setRange(t *testing.T, r SyncRange) {
	t.Helper()
	got, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Sync: &r})
	require.NoError(t, err)
	require.Equal(t, r, got.Sync)
}

func (e *syncEnv) stored(t *testing.T, folder string) []string {
	t.Helper()
	var out []string
	for _, r := range e.rows(t, "SELECT message_id FROM messages WHERE server_folder_id = ? ORDER BY message_id", e.folderID(t, folder)) {
		out = append(out, r[0].(string))
	}
	return out
}

func TestSyncRangeValidation(t *testing.T) {
	e := newSyncEnv(t)
	assert.Equal(t, SyncRange{Limit: 2000}, e.acc.Sync, "the default")
	for _, bad := range []SyncRange{{Limit: 0, Months: -1}, {Limit: -1}, {Limit: 100001}, {Limit: 5, Months: 3}, {Months: 121}} {
		_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Sync: &bad})
		assert.Error(t, err, "%+v", bad)
	}
	e.setRange(t, SyncRange{Months: 3})
	e.setRange(t, SyncRange{}) // all
	e.setRange(t, SyncRange{Limit: 500})
}

// A months range first-syncs only mail with INTERNALDATE since the cutoff,
// whatever the UID order; growing it to 全部 fetches the rest, including the
// old message above the oldest stored UID; shrinking back deletes nothing.
func TestSyncRangeMonths(t *testing.T) {
	e := newSyncEnv(t)
	now := time.Now()
	e.setRange(t, SyncRange{Months: 3})
	e.putAt(t, "Archive", "new1@x", now.AddDate(0, -1, 0))
	e.putAt(t, "Archive", "old@x", now.AddDate(0, -6, 0)) // a higher UID than new1, but older
	e.putAt(t, "Archive", "new2@x", now.AddDate(0, 0, -2))
	e.sync(t)
	assert.Equal(t, []string{"new1@x", "new2@x"}, e.stored(t, "Archive"))
	before := e.idOf(t, "new1@x")

	e.putAt(t, "Archive", "late@x", now.AddDate(-2, 0, 0)) // above last_uid: new mail, whatever its date
	e.sync(t)
	assert.Equal(t, []string{"late@x", "new1@x", "new2@x"}, e.stored(t, "Archive"))

	e.setRange(t, SyncRange{})
	e.sync(t)
	assert.Equal(t, []string{"late@x", "new1@x", "new2@x", "old@x"}, e.stored(t, "Archive"))
	assert.Equal(t, before, e.idOf(t, "new1@x"), "stored rows keep their IDs")

	e.setRange(t, SyncRange{Months: 3})
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 4, "shrinking never deletes")
	e.setRange(t, SyncRange{Limit: 1})
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 4, "shrinking never deletes")
}

// Growing a count to a longer months range fetches the older mail it covers.
func TestSyncRangeCountToMonths(t *testing.T) {
	e := newSyncEnv(t)
	now := time.Now()
	e.setRange(t, SyncRange{Limit: 1})
	e.putAt(t, "Archive", "a@x", now.AddDate(0, -5, 0))
	e.putAt(t, "Archive", "b@x", now.AddDate(0, -2, 0))
	e.putAt(t, "Archive", "c@x", now.AddDate(0, 0, -1))
	e.sync(t)
	assert.Equal(t, []string{"c@x"}, e.stored(t, "Archive"))
	e.setRange(t, SyncRange{Months: 3})
	e.sync(t)
	assert.Equal(t, []string{"b@x", "c@x"}, e.stored(t, "Archive"))
	e.setRange(t, SyncRange{Months: 6})
	e.sync(t)
	assert.Equal(t, []string{"a@x", "b@x", "c@x"}, e.stored(t, "Archive"))
}

// 全部 from a count fetches everything, in pages, without a cap.
func TestSyncRangeCountToAll(t *testing.T) {
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{Limit: 2})
	for i := range 7 {
		e.put(t, "Archive", msg(string(rune('a'+i))+"@x"))
	}
	e.sync(t)
	require.Len(t, e.stored(t, "Archive"), 2)
	defer func(n int) { mailsync.BackfillPage = n }(mailsync.BackfillPage)
	mailsync.BackfillPage = 2
	e.setRange(t, SyncRange{})
	e.sync(t) // two pages a round (BackfillPages)
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 7)
}

// 全部 on a first sync takes the whole folder.
func TestSyncRangeAllFirstSync(t *testing.T) {
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{})
	for i := range 3 {
		e.put(t, "Archive", msg(string(rune('a'+i))+"@x"))
	}
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 3)
}

// The check counts the selectable folders; the account reports how many synced.
func TestFolderCounts(t *testing.T) {
	e := newSyncEnv(t)
	assert.Zero(t, e.acc.Folders, "before the first sync")
	in := internalInput(t, e.im.Addr, "127.0.0.1:1", "alice@example.com", false)
	r, _ := e.s.CheckAccount(bg, in)
	assert.Equal(t, 4, r.Folders, "INBOX, Sent, Trash, Archive")
	e.sync(t)
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 4, acc.Folders)
}

// A folder empty at its first sync: what arrives later is new mail (logged,
// so it notifies), whatever its date, under a months range and under 全部.
func TestSyncRangeEmptyFolderNewMail(t *testing.T) {
	for _, r := range []SyncRange{{Months: 3}, {}} {
		e := newSyncEnv(t)
		e.setRange(t, r)
		e.sync(t) // Archive is empty
		e.putAt(t, "Archive", "moved@x", time.Now().AddDate(-1, 0, 0))
		e.sync(t)
		assert.Equal(t, []string{"moved@x"}, e.stored(t, "Archive"), "%+v", r)
		assert.Equal(t, [][]any{{e.idOf(t, "moved@x")}}, e.rows(t, "SELECT message_id FROM events_log"), "%+v: new mail", r)
	}
}
