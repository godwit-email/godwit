package core

import (
	"context"
	"encoding/base64"
	"github.com/godwit-email/godwit/core/store"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func (e *syncEnv) status(t *testing.T) (string, string) {
	t.Helper()
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	return a.Status, a.StatusDetail
}

func storedSecret(t *testing.T, s *Service, id int64) string {
	t.Helper()
	v, err := keyring.Get(keyringService, s.keyringUser(id))
	require.NoError(t, err)
	return v
}

func TestUpdatePasswordResumes(t *testing.T) {
	e, _ := newWorkerEnv(t)
	e.im.SetPassword("new")
	e.smtp.SetPassword("new")
	e.put(t, "INBOX", msg("p1@x")) // the next round's login is refused
	waitSignal(t, e.parked, "worker parking", 5*time.Second)
	st, _ := e.status(t)
	require.Equal(t, "reauth_required", st)

	require.NoError(t, e.s.UpdatePassword(bg, e.acc.ID, "new"))
	assert.Equal(t, "new", storedSecret(t, e.s, e.acc.ID))
	workerResumed(t, e, "p2@x")
	st, d := e.status(t)
	assert.Equal(t, []string{"ok", ""}, []string{st, d})
}

// A login refused for the old password after UpdatePassword saved the new one
// must not mark the account reauth_required again.
func TestStaleRefusalAfterUpdatePassword(t *testing.T) {
	e, _ := newWorkerEnv(t)
	e.im.SetPassword("new")
	e.smtp.SetPassword("new")
	entered, release := e.im.BlockLogin()
	t.Cleanup(release)
	e.put(t, "INBOX", msg("b1@x")) // the worker's next dial still uses "secret"
	waitSignal(t, entered, "old-password login", 5*time.Second)
	require.NoError(t, e.s.UpdatePassword(bg, e.acc.ID, "new"))
	release() // the refusal of the old password arrives now
	workerResumed(t, e, "b2@x")
	st, d := e.status(t)
	assert.Equal(t, []string{"ok", ""}, []string{st, d})
}

// A refusal of an older access token that arrives after another caller
// refreshed to a newer one must not mark the account reauth_required.
func TestStaleTokenRefusalAfterRefresh(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.oa.RevokeAccess() // A's first token T0 is refused; A refreshes to T1
	entered, release := e.im.BlockLoginAfter(1)
	t.Cleanup(release)
	errA := make(chan error, 1)
	go func() { errA <- e.s.SyncNow(bg, e.acc.ID) }()
	waitSignal(t, entered, "A's login with T1", 5*time.Second)
	t1, _ := e.cached()
	e.oa.RevokeAccess()                                    // T1 goes too
	_, t2, _, err := e.s.credentials(bg, e.acc, t1.access) // B refreshes to T2
	require.NoError(t, err)
	require.NotEqual(t, t1.access, t2)
	release() // A's refusal of T1 arrives now
	require.Error(t, <-errA)
	st, d := e.status(t)
	assert.Equal(t, []string{"ok", ""}, []string{st, d})
	assert.Zero(t, e.count(t, "SELECT count(*) FROM accounts WHERE smtp_reauth = 1"))

	n := e.oa.TokenRequests()
	e.put(t, "INBOX", msg("t2@x"))
	e.sync(t) // with T2, no further refresh
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 't2@x'"))
	assert.Equal(t, n, e.oa.TokenRequests())
}

func TestUpdatePasswordWrong(t *testing.T) {
	e := newSendEnv(t, false)
	err := e.s.UpdatePassword(bg, e.acc.ID, "wrong")
	var ce *CheckError
	require.ErrorAs(t, err, &ce)
	assert.False(t, ce.Report.OK())
	assert.Equal(t, "secret", storedSecret(t, e.s, e.acc.ID))
	assert.Error(t, e.s.Reauthorize(bg, e.acc.ID, "flow"), "not an OAuth account")
}

func TestSMTPReauthOutboxResumes(t *testing.T) {
	e, _ := newWorkerEnv(t)
	e.smtp.RejectAuth(true)
	e.sendNew(t)
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM accounts WHERE smtp_reauth = 1") == 1
	}, 5*time.Second, 10*time.Millisecond)
	e.smtp.RejectAuth(false)       // the user fixed it on the provider's side
	e.put(t, "INBOX", msg("s1@x")) // worker rounds go on, the outbox stays paused
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, "queued", e.state(t))
	assert.Empty(t, e.smtp.Messages())

	require.NoError(t, e.s.UpdatePassword(bg, e.acc.ID, "secret"))
	require.Eventually(t, func() bool { return len(e.smtp.Messages()) == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, e.smtp.Messages(), 1)
	st, _ := e.status(t)
	assert.Equal(t, "ok", st)
}

func TestReauthorizeOAuth(t *testing.T) {
	e := newOAuthEnv(t, true)
	e.add(t)
	waitEvent(t, e.ch, events.SyncFinished, 5*time.Second)
	old := e.keychain(t)
	e.oa.RevokeAll()
	e.s.wakeWorker(e.acc.ID)
	waitSignal(t, e.parked, "worker parking", 5*time.Second)
	st, _ := e.status(t)
	require.Equal(t, "reauth_required", st)

	require.NoError(t, e.s.Reauthorize(bg, e.acc.ID, e.signIn(t, "alice@example.com")))
	assert.NotEqual(t, old, e.keychain(t))
	st, d := e.status(t)
	assert.Equal(t, []string{"ok", ""}, []string{st, d})
	e.put(t, "INBOX", msg("re@x"))
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = 're@x'") == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.ErrorIs(t, e.s.UpdatePassword(bg, e.acc.ID, "pw"), errNotPassword)
}

func TestReauthorizeWrongAccount(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	old := e.keychain(t)
	e.oa.RevokeAll()
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	st, d := e.status(t)
	require.Equal(t, []string{"reauth_required", "token_revoked"}, []string{st, d})

	flow := e.signIn(t, "bob@example.com")
	assert.ErrorIs(t, e.s.Reauthorize(bg, e.acc.ID, flow), ErrWrongAccount)
	assert.Equal(t, old, e.keychain(t))
	st2, d2 := e.status(t)
	assert.Equal(t, []string{st, d}, []string{st2, d2})
	_, ok := e.cached()
	assert.False(t, ok, "bob's access token is not cached")
	assert.ErrorIs(t, e.s.Reauthorize(bg, e.acc.ID, "nope"), ErrUnknownFlow)
}

// UpdateAccount checks new servers with a refreshed token when the cached one is refused.
func TestUpdateAccountOAuthRefreshes(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	e.oa.RevokeAccess()
	n := e.oa.TokenRequests()
	name := "Work"
	srv := e.acc.servers()
	a, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Name: &name, Servers: &srv})
	require.NoError(t, err)
	assert.Equal(t, "Work", a.Name)
	assert.Equal(t, n+1, e.oa.TokenRequests())
}

func TestUpdateAccountServersChecked(t *testing.T) {
	e, _ := newWorkerEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	srv := ServerSettings{Username: "alice", IMAPHost: e.acc.IMAPHost, IMAPPort: closed, IMAPSecurity: SecurityNone,
		SMTPHost: e.acc.SMTPHost, SMTPPort: e.acc.SMTPPort, SMTPSecurity: SecurityNone}
	name := "Changed"
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Name: &name, Servers: &srv})
	var ce *CheckError
	require.ErrorAs(t, err, &ce)
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, e.acc, a, "nothing saved")

	srv.IMAPPort = e.acc.IMAPPort
	im2 := testmail.StartIMAP(t) // a working server elsewhere
	host, port := splitAddr(t, im2.Addr)
	srv.IMAPHost, srv.IMAPPort = host, port
	a, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Name: &name, Servers: &srv})
	require.NoError(t, err)
	assert.Equal(t, "Changed", a.Name)
	assert.Equal(t, port, a.IMAPPort)
	// The worker moves to the new server by itself.
	(&syncEnv{im: im2}).put(t, "INBOX", msg("new-server@x"))
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'new-server@x'") == 1
	}, 5*time.Second, 20*time.Millisecond)
}

func TestUpdateAccountDefault(t *testing.T) {
	e := newSendEnv(t, false)
	smtpAddr, _ := testmail.StartSMTP(t)
	b, err := e.s.AddAccount(bg, internalInput(t, e.im.Addr, smtpAddr, "b@example.com", false))
	require.NoError(t, err)
	defaults := func() []int64 {
		var ids []int64
		for _, r := range e.rows(t, "SELECT id FROM accounts WHERE is_default = 1 ORDER BY id") {
			ids = append(ids, r[0].(int64))
		}
		return ids
	}
	yes, no := true, false
	got, err := e.s.UpdateAccount(bg, b.ID, AccountPatch{IsDefault: &yes})
	require.NoError(t, err)
	assert.True(t, got.IsDefault)
	assert.Equal(t, []int64{b.ID}, defaults())

	_, err = e.s.UpdateAccount(bg, b.ID, AccountPatch{IsDefault: &no})
	assert.Error(t, err, "the only default cannot be unset")
	assert.Equal(t, []int64{b.ID}, defaults())
	_, err = e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{IsDefault: &no}) // not the default: nothing to do
	assert.NoError(t, err)

	for _, bad := range []int{0, 100001} {
		_, err = e.s.UpdateAccount(bg, b.ID, AccountPatch{SyncLimit: &bad})
		assert.Error(t, err)
	}
	lim, sig := 500, "-- b"
	got, err = e.s.UpdateAccount(bg, b.ID, AccountPatch{SyncLimit: &lim, Signature: &sig})
	require.NoError(t, err)
	assert.Equal(t, 500, got.SyncLimit)
	assert.Equal(t, "-- b", got.Signature)
	_, err = e.s.UpdateAccount(bg, 999, AccountPatch{Signature: &sig})
	assert.Error(t, err)
}

// Another server's UIDs are not this one's, even when UIDVALIDITY and UID match:
// after a server change, queued changes to old messages conflict instead of
// hitting the new server's UID, and downloads of old parts fail.
func TestUpdateAccountNewServerForgetsOldUIDs(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "INBOX", withAttachments("old@x", "old", 1))
	e.sync(t)
	old := e.idOf(t, "old@x")
	att := store.Int(e.rows(t, "SELECT id FROM attachments WHERE message_id = ?", old)[0][0])
	require.NoError(t, e.s.Move(bg, []int64{old}, e.folderID(t, "Archive")))

	im2 := testmail.StartIMAP(t) // same UIDVALIDITY, its UID 1 is another message
	e2 := &syncEnv{im: im2}
	e2.put(t, "INBOX", strings.ReplaceAll(withAttachments("new@x", "new", 1), "JVBERi0xLjQK",
		base64.StdEncoding.EncodeToString([]byte("new server"))))
	host, port := splitAddr(t, im2.Addr)
	srv := e.acc.servers()
	srv.IMAPHost, srv.IMAPPort = host, port
	_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Servers: &srv})
	require.NoError(t, err)

	if p, _, err := e.s.GetAttachment(bg, att); err == nil {
		b, _ := os.ReadFile(p)
		t.Fatalf("old attachment downloaded from the new server: %q", b)
	}
	e.s.SyncNow(bg, e.acc.ID) // reports the conflict
	assert.Equal(t, []string{"conflict"}, e.opStates(t))
	e.im = im2
	assert.Equal(t, []string{"new@x"}, e.onServer(t, "INBOX"), "the new server's UID 1 stays")
	assert.Empty(t, e.onServer(t, "Archive"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'new@x'"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'old@x'"))
}

// Fixing the servers is fixing the sign-in: a worker parked on reauth_required
// resumes and a paused outbox sends, with no SyncNow.
func TestUpdateAccountServersResumes(t *testing.T) {
	e, _ := newWorkerEnv(t)
	e.smtp.RejectAuth(true)
	e.sendNew(t)
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM accounts WHERE smtp_reauth = 1") == 1
	}, 5*time.Second, 10*time.Millisecond)
	e.im.SetPassword("other")
	e.put(t, "INBOX", msg("p1@x")) // the next round's login is refused
	waitSignal(t, e.parked, "worker parking", 5*time.Second)

	im2 := testmail.StartIMAP(t)
	smtp2, in2 := testmail.StartSMTP(t)
	srv := e.acc.servers()
	srv.IMAPHost, srv.IMAPPort = splitAddr(t, im2.Addr)
	srv.SMTPHost, srv.SMTPPort = splitAddr(t, smtp2)
	_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Servers: &srv})
	require.NoError(t, err)
	st, d := e.status(t)
	assert.Equal(t, []string{"ok", ""}, []string{st, d})
	(&syncEnv{im: im2}).put(t, "INBOX", msg("fixed@x"))
	require.Eventually(t, func() bool {
		return e.count(t, "SELECT count(*) FROM messages WHERE message_id = 'fixed@x'") == 1 && len(in2.Messages()) == 1
	}, 5*time.Second, 20*time.Millisecond)
}

// A token endpoint that never answers costs one refresh its deadline; the
// refresh lock is free afterwards for Reauthorize and later syncs.
func TestRefreshDeadline(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.add(t)
	setFor(t, &refreshTimeout, 200*time.Millisecond)
	release := e.oa.Hang(t)
	e.expire(t)
	within(t, 3*time.Second, "refresh", func() {
		_, _, _, err := e.s.credentials(bg, e.acc, "")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
	assert.Error(t, e.s.SyncNow(bg, e.acc.ID), "still hanging: this sync fails too")
	release()
	within(t, 3*time.Second, "Reauthorize", func() {
		require.NoError(t, e.s.Reauthorize(bg, e.acc.ID, e.signIn(t, "alice@example.com")))
	})
	e.expire(t)
	within(t, 3*time.Second, "SyncNow", func() { e.sync(t) }) // refreshes again
}
