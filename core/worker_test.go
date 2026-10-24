package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// setFor sets *v to x for the rest of the test.
func setFor[T any](t *testing.T, v *T, x T) {
	old := *v
	*v = x
	t.Cleanup(func() { *v = old })
}

// newWorkerEnv is newSendEnv with AutoSync on and a short backoff. The IDLE and poll
// intervals stay at 5 minutes unless a test lowers them first, so anything a test
// sees sooner came from IDLE or a wake.
func newWorkerEnv(t *testing.T, opts ...testmail.Option) (*workerEnv, <-chan events.Event) {
	t.Helper()
	parked, lockWait := make(chan struct{}, 64), make(chan struct{}, 64)
	signal := func(c chan struct{}) func(int64) {
		return func(int64) {
			select {
			case c <- struct{}{}:
			default:
			}
		}
	}
	setFor(t, &onParked, signal(parked)) // set before any worker starts
	setFor(t, &beforeLockWait, signal(lockWait))
	b0, b1 := backoffMin, backoffMax
	backoffMin, backoffMax = 20*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { backoffMin, backoffMax = b0, b1 }) // runs after the Close below
	keyring.MockInit()
	clock := useTestClock(t)
	dir := t.TempDir()
	s, err := New(bg, Options{DataDir: dir, AutoSync: true, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ch, unsub := s.Subscribe()
	t.Cleanup(unsub)
	smtpAddr, in := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t, opts...)
	acc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "alice@example.com", true))
	require.NoError(t, err)
	e := &workerEnv{sendEnv: &sendEnv{syncEnv: &syncEnv{s: s, im: im, acc: acc, dir: dir}, smtp: in, clock: clock},
		parked: parked, lockWait: lockWait}
	waitEvent(t, ch, events.SyncFinished, 5*time.Second)
	return e, ch
}

// workerEnv is a sendEnv whose worker reports, through test hooks, when it parks
// and when any lockAccount call starts waiting.
type workerEnv struct {
	*sendEnv
	parked, lockWait chan struct{}
}

// waitSignal fails the test unless c fires within d.
func waitSignal(t *testing.T, c <-chan struct{}, what string, d time.Duration) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(d):
		t.Fatalf("%s did not happen within %s", what, d)
	}
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
	s, err := New(bg, Options{DataDir: e.dir, AutoSync: true, ImageClient: loopbackImageClient()})
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
	da, _, err := e.s.addAttachment(bg, d.ID, -1, "k.txt", "text/plain", []byte("keep"))
	require.NoError(t, err)
	orphanInDraft := filepath.Join(filepath.Dir(store.Str(e.rows(t, "SELECT path FROM draft_attachments WHERE id = ?", da.ID)[0][0])), "9999")
	draftFile := store.Str(e.rows(t, "SELECT path FROM draft_attachments WHERE id = ?", da.ID)[0][0])
	e.sendNew(t) // stays queued: nothing syncs
	queued := store.Str(e.rows(t, "SELECT raw_path FROM outbox WHERE state = 'queued'")[0][0])

	orphans := []string{
		filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10), "999"),
		filepath.Join(e.dir, "cache", "77", "1"),
		filepath.Join(e.dir, "cache", strconv.FormatInt(e.acc.ID, 10), ".tmp-123"),
		filepath.Join(e.dir, "drafts", "9999", "1"),
		filepath.Join(e.dir, "outbox", "9999.eml"),
		orphanInDraft, // a live draft's directory does not shelter files no row names
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

// lockedWorkerEnv returns a worker env whose worker is blocked waiting for the
// account lock, which the test holds until it ends.
func lockedWorkerEnv(t *testing.T) *workerEnv {
	t.Helper()
	e, ch := newWorkerEnv(t)
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	t.Cleanup(unlock)
	e.put(t, "INBOX", msg("wait@x")) // IDLE ends; the next round waits for the lock
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, ofType(collect(ch), events.SyncStarted), "the round must be waiting for the lock")
	return e
}

// within fails the test unless f returns within d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

func TestCloseWhileAccountLocked(t *testing.T) {
	e := lockedWorkerEnv(t)
	within(t, 5*time.Second, "Close", func() { require.NoError(t, e.s.Close()) })
}

func TestRemoveAccountWhileAccountLocked(t *testing.T) {
	e := lockedWorkerEnv(t)
	within(t, 5*time.Second, "RemoveAccount", func() { require.NoError(t, e.s.RemoveAccount(bg, e.acc.ID)) })
}

// slowExit makes every worker take 300ms to exit; call it before newWorkerEnv.
func slowExit(t *testing.T) {
	setFor(t, &beforeWorkerExit, func() { time.Sleep(300 * time.Millisecond) })
}

func workerOf(t *testing.T, e *workerEnv) *worker {
	t.Helper()
	e.s.workersMu.Lock()
	defer e.s.workersMu.Unlock()
	require.NotNil(t, e.s.workers[e.acc.ID])
	return e.s.workers[e.acc.ID]
}

func exited(w *worker) bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func TestConcurrentClose(t *testing.T) {
	slowExit(t)
	e, _ := newWorkerEnv(t)
	w := workerOf(t, e)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, e.s.Close())
			assert.True(t, exited(w), "Close returned before the worker exited")
		}()
	}
	within(t, 5*time.Second, "Close", wg.Wait)
}

func TestRemoveAccountRacingClose(t *testing.T) {
	slowExit(t)
	e, _ := newWorkerEnv(t)
	w := workerOf(t, e)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.s.RemoveAccount(bg, e.acc.ID) }() // may lose to Close: the DB is closed
	go func() {
		defer wg.Done()
		assert.NoError(t, e.s.Close())
		assert.True(t, exited(w), "Close returned before the removed account's worker exited")
	}()
	within(t, 5*time.Second, "RemoveAccount and Close", wg.Wait)
}

// parkWorker makes the worker's next round fail to log in and waits until the
// account is reauth_required. The keychain holds a wrong password afterwards.
func parkWorker(t *testing.T, e *workerEnv, ch <-chan events.Event) {
	t.Helper()
	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "wrong"))
	e.put(t, "INBOX", msg("r1@x")) // the next round fails to log in
	waitEvent(t, ch, events.SyncFailed, 5*time.Second)
	require.Eventually(t, func() bool {
		a, err := e.s.account(bg, e.acc.ID)
		return err == nil && a.Status == "reauth_required"
	}, 5*time.Second, 10*time.Millisecond)
	waitSignal(t, e.parked, "worker parking", 5*time.Second) // only a reauth_required status parks it
}

// workerResumed waits until the worker imports a newly arrived message.
func workerResumed(t *testing.T, e *workerEnv, id string) {
	t.Helper()
	e.put(t, "INBOX", msg(id))
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = ?", id) == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestWorkerParksWhileReauthRequired(t *testing.T) {
	e, ch := newWorkerEnv(t)
	parkWorker(t, e, ch)
	n := e.im.Logins()
	e.s.wakeWorker(e.acc.ID) // a local change must not log in either
	time.Sleep(time.Second)  // backoff alone would retry about 7 times
	assert.Equal(t, n, e.im.Logins(), "no logins while reauth_required")

	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "secret"))
	require.NoError(t, e.s.SyncNow(bg, e.acc.ID)) // resumes the worker
	workerResumed(t, e, "r2@x")
}

func TestReauthResumesOnLoginEvenIfSyncFails(t *testing.T) {
	e, ch := newWorkerEnv(t)
	parkWorker(t, e, ch)
	n := e.im.Logins() // parked: nothing logs in until SyncNow
	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "secret"))
	e.im.DropNextFetch() // LOGIN succeeds, the sync then fails
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status, "a successful LOGIN clears reauth_required")
	// SyncNow's own LOGIN plus at least one by the resumed worker.
	require.Eventually(t, func() bool { return e.im.Logins() >= n+2 }, 5*time.Second, 10*time.Millisecond, "worker resumes")
	workerResumed(t, e, "r2@x")
}

func TestSyncNowQueuedBeforeParkStillResumes(t *testing.T) {
	e, ch := newWorkerEnv(t)
	unlock, err := e.s.lockAccount(bg, e.acc.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- e.s.SyncNow(bg, e.acc.ID) }() // starts while the account is fine
	waitSignal(t, e.lockWait, "SyncNow waiting for the lock", 5*time.Second)
	parkWorker(t, e, ch) // the worker's IDLE login fails without needing the lock; it parks
	require.NoError(t, keyring.Set(keyringService, e.s.keyringUser(e.acc.ID), "secret"))
	unlock()
	require.NoError(t, <-done)
	workerResumed(t, e, "r2@x")
}

func TestCancelledSyncIsNotAFailure(t *testing.T) {
	e := newSendEnv(t, false)
	e.sendNew(t)
	ch, unsub := e.s.Subscribe()
	defer unsub()
	ctx, cancel := context.WithCancel(bg)
	beforeOutboxItem = func(int64) { cancel() }
	t.Cleanup(func() { beforeOutboxItem = nil })
	require.Error(t, e.s.SyncNow(ctx, e.acc.ID))
	assert.Empty(t, ofType(collect(ch), events.SyncFailed))
}

func TestWorkerPollsWithoutIdle(t *testing.T) {
	setFor(t, &pollInterval, 200*time.Millisecond)
	e, ch := newWorkerEnv(t, testmail.WithoutIdle())
	e.put(t, "INBOX", msg("p@x"))
	waitEvent(t, ch, events.MessageReceived, 5*time.Second)
}

func TestWorkerFullRoundFindsOtherFolders(t *testing.T) {
	setFor(t, &idleMax, 200*time.Millisecond)
	e, ch := newWorkerEnv(t)
	e.put(t, "Archive", msg("arch@x"))
	waitEvent(t, ch, events.MessageReceived, 5*time.Second)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'arch@x' AND folder_id = ?", e.folderID(t, "Archive")))
}

func TestWorkerDeliversSend(t *testing.T) {
	e, _ := newWorkerEnv(t)
	mid := e.sendNew(t)
	require.Eventually(t, func() bool { return len(e.smtp.Messages()) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM outbox WHERE message_id_hdr = ? AND state = 'done'", mid) == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestLoginClearsReauthSetAfterDialReadStatus(t *testing.T) {
	e := newSyncEnv(t) // no worker: the hook below is only called by this test's SyncNow
	setFor(t, &afterAccountRead, func(id int64) {
		_, err := e.s.db.Execute(bg, "UPDATE accounts SET status = 'reauth_required' WHERE id = ?", id)
		require.NoError(t, err)
	})
	e.put(t, "INBOX", msg("f@x")) // something to FETCH
	e.im.DropNextFetch()          // LOGIN succeeds, the sync then fails, so only the LOGIN can clear it
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", a.Status)
}

// IMAP accepts the password but SMTP refuses it: only the outbox pauses. The
// worker keeps syncing mail without SMTP logins, the message waits unharmed,
// and only an SMTP login on an explicit SyncNow clears it.
func TestSMTPAuthFailurePausesOnlyOutbox(t *testing.T) {
	e, ch := newWorkerEnv(t)
	e.smtp.RejectAuth(true)
	base := e.smtp.AuthAttempts()
	e.sendNew(t) // wakes the worker
	ev := waitEvent(t, ch, events.SyncFailed, 5*time.Second)
	assert.Contains(t, ev.Error, "authentication failed")
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "reauth_required", acc.Status, "the user is asked for the password")
	assert.Equal(t, "auth_rejected", acc.StatusDetail)
	r := e.rows(t, "SELECT state, attempts FROM outbox")
	assert.Equal(t, []any{"queued", int64(0)}, r[0], "the item is not charged for the login")

	for i := range 3 {
		e.sendNew(t)                                 // wakes the worker for a round
		workerResumed(t, e, fmt.Sprintf("m%d@x", i)) // mail still arrives in the background
	}
	assert.Equal(t, 1, e.smtp.AuthAttempts()-base, "one SMTP login across the rounds")
	select {
	case <-e.parked:
		t.Fatal("an SMTP-only failure parked the worker")
	default:
	}

	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), conn.ErrAuthFailed)
	assert.Equal(t, 2, e.smtp.AuthAttempts()-base, "an explicit sync tries SMTP once")
	assert.Empty(t, e.smtp.Messages())

	e.smtp.RejectAuth(false)
	require.NoError(t, e.s.SyncNow(bg, e.acc.ID))
	acc, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "ok", acc.Status)
	assert.Empty(t, acc.StatusDetail)
	assert.Len(t, e.smtp.Messages(), 4)
}
