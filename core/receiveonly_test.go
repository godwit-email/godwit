package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// newReceiveOnlyEnv adds alice although her SMTP server refuses the sign-in.
func newReceiveOnlyEnv(t *testing.T) *sendEnv {
	t.Helper()
	keyring.MockInit()
	clock := useTestClock(t)
	dir := t.TempDir()
	s, err := New(bg, Options{DataDir: dir, ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	im := testmail.StartIMAP(t)
	smtpAddr, in := testmail.StartSMTP(t)
	in.RejectAuth(true)
	ai := internalInput(t, im.Addr, smtpAddr, "alice@example.com", true)

	_, err = s.AddAccount(bg, ai)
	var ce *CheckError
	require.ErrorAs(t, err, &ce, "SMTP still blocks a plain add")
	assert.True(t, ce.Report.OnlySMTPFailed())

	ai.ReceiveOnly = true
	acc, err := s.AddAccount(bg, ai)
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly)
	return &sendEnv{syncEnv: &syncEnv{s: s, im: im, acc: acc, dir: dir}, smtp: in, clock: clock}
}

func TestReceiveOnlyNeedsIMAP(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	ai := internalInput(t, e.im.Addr, e.im.Addr, "bob@example.com", false)
	ai.Password, ai.ReceiveOnly = "wrong", true
	_, err := e.s.AddAccount(bg, ai)
	var ce *CheckError
	require.ErrorAs(t, err, &ce, "a failing IMAP step is not let through")
}

func TestReceiveOnlyNeverSends(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	e.put(t, "INBOX", msg("in@x"))
	e.sync(t) // mail still comes in
	src := e.idOf(t, "in@x")

	_, err := e.s.NewDraft(bg, e.acc.ID, "reply", src)
	require.ErrorIs(t, err, ErrReceiveOnly, "no other account sends")
	_, err = e.s.draftAccount(bg, e.acc.ID)
	require.ErrorIs(t, err, ErrReceiveOnly)

	// A draft on it (none can be made now) is refused at the outbox's door.
	r, err := e.s.db.Execute(bg, `INSERT INTO drafts(account_id, mode, to_addrs, cc_addrs, bcc_addrs, reply_to, subject, body_html, updated_at)
		VALUES (?, 'new', '["bob@example.com"]', '[]', '[]', '', 'hi', '<p>hi</p>', 0)`, e.acc.ID)
	require.NoError(t, err)
	require.ErrorIs(t, e.s.Send(bg, r.LastInsertID), ErrReceiveOnly)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.MCPSend = MCPSendDirect
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	d, err := e.s.GetDraft(bg, r.LastInsertID)
	require.NoError(t, err)
	_, err = e.s.RequestMCPSend(bg, d.ID, d.Revision)
	require.ErrorIs(t, err, ErrReceiveOnly)
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM outbox"))

	useSMTP, err := e.s.checkSMTP(bg, e.acc.ID, true)
	require.NoError(t, err)
	assert.False(t, useSMTP, "no SMTP login, even on SyncNow")

	// Changing servers while SMTP still fails keeps it receive-only.
	acc, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Servers: new(e.acc.servers())})
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly)
}

func TestReceiveOnlyRepliesFromAnother(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	smtpAddr, _ := testmail.StartSMTP(t)
	other, err := e.s.AddAccount(bg, internalInput(t, e.im.Addr, smtpAddr, "carol@example.com", false))
	require.NoError(t, err)
	e.put(t, "INBOX", msg("in@x"))
	e.sync(t)

	d, err := e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "in@x"))
	require.NoError(t, err)
	assert.Equal(t, other.ID, d.AccountID)
	// Its own sent mail is still the user's: the reply follows up with Bob.
	e.put(t, "INBOX", eml("sent@x", "alice@example.com", "Bob <bob@example.com>", "plan", day, "the plan"))
	e.sync(t)
	d, err = e.s.NewDraft(bg, e.acc.ID, "reply", e.idOf(t, "sent@x"))
	require.NoError(t, err)
	assert.Equal(t, other.ID, d.AccountID)
	assert.Equal(t, []string{"Bob <bob@example.com>"}, d.To)

	d, err = e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	assert.Equal(t, other.ID, d.AccountID)
	_, err = e.s.SetDraftAccount(bg, d.ID, e.acc.ID, d.Revision)
	require.ErrorIs(t, err, ErrReceiveOnly)
	got, err := e.s.draftAccount(bg, 0)
	require.NoError(t, err)
	assert.Equal(t, other.ID, got.ID, "the default sender is never receive-only")
}

func TestReceiveOnlyResumesAfterServerFix(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	e.smtp.RejectAuth(false) // the user turned SMTP AUTH on
	acc, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Servers: new(e.acc.servers())})
	require.NoError(t, err)
	assert.False(t, acc.ReceiveOnly)

	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
}
