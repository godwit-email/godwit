package core

import (
	"net"
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

func TestUpdateAccountServersChecked(t *testing.T) {
	e := newSendEnv(t, false)
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
	n := im2.Logins()
	e.sync(t)
	assert.Greater(t, im2.Logins(), n, "syncs use the new server")
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
