package core

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/internal/testmail"
	"github.com/godwitmail/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// newWorkerEnv is newSyncEnv with AutoSync on and a short backoff. The IDLE and poll
// intervals stay at 5 minutes, so anything a test sees sooner came from IDLE or a wake.
func newWorkerEnv(t *testing.T) (*syncEnv, <-chan events.Event) {
	t.Helper()
	b0, b1 := backoffMin, backoffMax
	backoffMin, backoffMax = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { backoffMin, backoffMax = b0, b1 }) // runs after the Close below
	keyring.MockInit()
	dir := t.TempDir()
	s, err := New(bg, Options{DataDir: dir, AutoSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ch, unsub := s.Subscribe()
	t.Cleanup(unsub)
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "alice@example.com", true))
	require.NoError(t, err)
	e := &syncEnv{s: s, im: im, acc: acc, dir: dir}
	waitEvent(t, ch, events.SyncFinished, 5*time.Second)
	return e, ch
}

// waitEvent returns the first event of type typ within d.
func waitEvent(t *testing.T, ch <-chan events.Event, typ string, d time.Duration) events.Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-ch:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s within %s", typ, d)
		}
	}
}

func TestWorkerPicksUpNewMail(t *testing.T) {
	e, ch := newWorkerEnv(t)
	e.put(t, "INBOX", msg("new@x"))
	ev := waitEvent(t, ch, events.MessageReceived, 5*time.Second)
	assert.Equal(t, e.acc.ID, ev.AccountID)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'new@x'"))
	time.Sleep(300 * time.Millisecond) // settled: back in IDLE, not syncing in a loop
	assert.LessOrEqual(t, len(ofType(collect(ch), events.SyncStarted)), 1)
}

func TestWorkerPushesLocalChangesPromptly(t *testing.T) {
	e, ch := newWorkerEnv(t)
	e.put(t, "INBOX", msg("a@x"))
	waitEvent(t, ch, events.MessageReceived, 5*time.Second)
	require.NoError(t, e.s.SetFlags(bg, []int64{e.idOf(t, "a@x")}, []string{"\\Flagged"}, nil))
	require.Eventually(t, func() bool {
		got := e.onServer(t, "INBOX")
		return len(got) == 1 && got[0] == "a@x \\Flagged"
	}, 5*time.Second, 20*time.Millisecond)
}

func TestWorkerReconnectsAfterServerRestart(t *testing.T) {
	e, ch := newWorkerEnv(t)
	e.im.Stop()
	waitEvent(t, ch, events.SyncFailed, 5*time.Second)
	time.Sleep(time.Second) // backoff 20ms doubling to 200ms: about 7 attempts, not a hot loop
	assert.LessOrEqual(t, len(ofType(collect(ch), events.SyncFailed)), 10)
	e.im.Restart()
	e.put(t, "INBOX", msg("back@x"))
	waitEvent(t, ch, events.MessageReceived, 30*time.Second)
}

func TestCloseStopsWorkers(t *testing.T) {
	e, ch := newWorkerEnv(t)
	// New starts a worker for an account that already exists.
	require.NoError(t, e.s.Close())
	s, err := New(bg, Options{DataDir: e.dir, AutoSync: true})
	require.NoError(t, err)
	ch, unsub := s.Subscribe()
	defer unsub()
	waitEvent(t, ch, events.SyncFinished, 5*time.Second) // now idling

	done := make(chan error)
	go func() { done <- s.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s")
	}
	assert.Empty(t, s.workers)
}

func TestRemoveAccountStopsWorker(t *testing.T) {
	e, _ := newWorkerEnv(t)
	done := make(chan error)
	go func() { done <- e.s.RemoveAccount(bg, e.acc.ID) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveAccount did not return within 5s")
	}
	e.s.workersMu.Lock()
	defer e.s.workersMu.Unlock()
	assert.Empty(t, e.s.workers)
}

func TestStartupCleansOrphans(t *testing.T) {
	e := newSendEnv(t, false)
	e.put(t, "INBOX", withAttachments("att@x", "files", 1))
	e.sync(t)
	att := store.Int(e.rows(t, "SELECT a.id FROM attachments a JOIN messages m ON m.id = a.message_id WHERE m.message_id = 'att@x'")[0][0])
	cached, _, err := e.s.GetAttachment(bg, att)
	require.NoError(t, err)

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	da, err := e.s.addAttachment(bg, d.ID, "k.txt", "text/plain", []byte("keep"))
	require.NoError(t, err)
	draftFile := store.Str(e.rows(t, "SELECT path FROM draft_attachments WHERE id = ?", da.ID)[0][0])
	e.sendNew(t) // stays queued: nothing syncs
	queued := store.Str(e.rows(t, "SELECT raw_path FROM outbox WHERE state = 'queued'")[0][0])

	orphans := []string{
		filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10), "999"),
		filepath.Join(e.dir, "cache", "77", "1"),
		filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10), ".tmp-123"),
		filepath.Join(e.dir, "drafts", "9999", "1"),
		filepath.Join(e.dir, "outbox", "9999.eml"),
	}
	for _, p := range orphans {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}

	e.reopen(t)
	for _, p := range orphans {
		assert.NoFileExists(t, p)
	}
	for _, p := range []string{cached, draftFile, queued} {
		assert.FileExists(t, p)
	}
}
