package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A range backfill takes at most BackfillPages pages a round, then yields:
// new mail elsewhere comes in meanwhile, and the next round goes on.
func TestSyncRangeBackfillBounded(t *testing.T) {
	defer func(n, p int) { mailsync.BackfillPage, mailsync.BackfillPages = n, p }(mailsync.BackfillPage, mailsync.BackfillPages)
	mailsync.BackfillPage, mailsync.BackfillPages = 2, 1
	e := newSyncEnv(t)
	e.setRange(t, SyncRange{Limit: 1})
	for i := range 7 {
		e.put(t, "Archive", msg(string(rune('a'+i))+"@x"))
	}
	e.sync(t)
	e.setRange(t, SyncRange{})
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 3, "one page of two")
	e.put(t, "INBOX", msg("new@x"))
	e.sync(t)
	assert.Equal(t, []string{"new@x"}, e.stored(t, "INBOX"))
	assert.Len(t, e.stored(t, "Archive"), 5)
	e.sync(t)
	e.sync(t)
	assert.Len(t, e.stored(t, "Archive"), 7)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM folders WHERE filled_since IS NOT NULL AND name = 'Archive'"))
}

// A bounded range backfill makes the worker sync again at once, not after
// the IDLE timeout.
func TestWorkerContinuesBackfill(t *testing.T) {
	defer func(n, p int) { mailsync.BackfillPage, mailsync.BackfillPages = n, p }(mailsync.BackfillPage, mailsync.BackfillPages)
	mailsync.BackfillPage, mailsync.BackfillPages = 2, 1
	e, _ := newWorkerEnv(t)
	lim := SyncRange{Limit: 1}
	_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Sync: &lim})
	require.NoError(t, err)
	for i := range 7 {
		e.put(t, "Archive", msg(fmt.Sprintf("a%d@x", i))) // not INBOX: no IDLE wake-up
	}
	e.sync(t)
	all := SyncRange{}
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Sync: &all})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(e.stored(t, "Archive")) == 7 }, 5*time.Second, 20*time.Millisecond)
}

// New mail above last_uid comes a page at a time, all of it new mail.
func TestFetchNewPaged(t *testing.T) {
	defer func(n int) { mailsync.BackfillPage = n }(mailsync.BackfillPage)
	mailsync.BackfillPage = 2
	e := newSyncEnv(t)
	e.sync(t)
	for i := range 5 {
		e.put(t, "INBOX", msg(fmt.Sprintf("n%d@x", i)))
	}
	ch, stop := e.s.Subscribe()
	defer stop()
	e.sync(t)
	assert.Len(t, e.stored(t, "INBOX"), 5)
	var got []int64
	for _, ev := range ofType(collect(ch), events.MessageReceived) {
		got = append(got, ev.MessageIDs...)
	}
	assert.Len(t, got, 5)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM folders WHERE name = 'INBOX' AND last_uid = 5 AND synced = 1"))
}
