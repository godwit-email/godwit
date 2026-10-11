package core

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func smallOlderPage(t *testing.T, n int) {
	old := mailsync.OlderPage
	mailsync.OlderPage = n
	t.Cleanup(func() { mailsync.OlderPage = old })
}

// Pages load newest first below what the range kept, end with more false,
// never duplicate, and a later sync neither refetches nor deletes them.
func TestLoadOlderPages(t *testing.T) {
	smallOlderPage(t, 2)
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{Limit: 1})
	for i := range 5 {
		e.put(t, "Archive", msg(fmt.Sprintf("a%d@x", i)))
	}
	e.sync(t)
	require.Equal(t, []string{"a4@x"}, e.stored(t, "Archive"))
	f := e.folderID(t, "Archive")

	more, err := e.s.LoadOlder(bg, f)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Equal(t, []string{"a2@x", "a3@x", "a4@x"}, e.stored(t, "Archive"), "newest first")

	e.sync(t)
	assert.Equal(t, []string{"a2@x", "a3@x", "a4@x"}, e.stored(t, "Archive"), "the sync keeps them")

	more, err = e.s.LoadOlder(bg, f)
	require.NoError(t, err)
	assert.False(t, more, "the folder's end")
	assert.Len(t, e.stored(t, "Archive"), 5)

	more, err = e.s.LoadOlder(bg, f)
	require.NoError(t, err)
	assert.False(t, more)
	e.sync(t)
	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM messages WHERE server_folder_id = ?", f), "no duplicates")
}

// Under a months range the page also takes old mail above the oldest stored
// UID, and growing the range afterwards does not import it twice.
func TestLoadOlderMonths(t *testing.T) {
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{Months: 3})
	now := time.Now()
	e.putAt(t, "Archive", "new@x", now.AddDate(0, -1, 0))
	e.putAt(t, "Archive", "old@x", now.AddDate(0, -9, 0))
	e.sync(t)
	require.Equal(t, []string{"new@x"}, e.stored(t, "Archive"))
	more, err := e.s.LoadOlder(bg, e.folderID(t, "Archive"))
	require.NoError(t, err)
	assert.False(t, more)
	e.setRange(t, SyncRange{})
	e.sync(t)
	assert.Equal(t, []string{"new@x", "old@x"}, e.stored(t, "Archive"))
}

// A page and a sync at once: they take turns on the account, and nothing is
// stored twice.
func TestLoadOlderWithSync(t *testing.T) {
	smallOlderPage(t, 3)
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{Limit: 2})
	for i := range 8 {
		e.put(t, "Archive", msg(fmt.Sprintf("a%d@x", i)))
	}
	e.sync(t)
	f := e.folderID(t, "Archive")
	e.setRange(t, SyncRange{Limit: 4}) // the sync backfills while the page loads
	var wg sync.WaitGroup
	var olderErr, syncErr error
	wg.Add(2)
	go func() { defer wg.Done(); _, olderErr = e.s.LoadOlder(bg, f) }()
	go func() { defer wg.Done(); syncErr = e.s.SyncNow(bg, e.acc.ID) }()
	wg.Wait()
	require.NoError(t, olderErr)
	require.NoError(t, syncErr)
	n := e.count(t, "SELECT count(*) FROM messages WHERE server_folder_id = ?", f)
	assert.Equal(t, n, e.count(t, "SELECT count(DISTINCT message_id) FROM messages WHERE server_folder_id = ?", f))
	assert.GreaterOrEqual(t, n, 5)
	_, err := e.s.LoadOlder(bg, f)
	require.NoError(t, err)
	_, err = e.s.LoadOlder(bg, f)
	require.NoError(t, err)
	assert.Len(t, e.stored(t, "Archive"), 8)
}
