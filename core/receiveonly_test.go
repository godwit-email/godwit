package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"

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
	assert.True(t, ce.Report.OnlySendingFailed())

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
	deleted, err := e.s.DeleteDraftIfEmpty(bg, d.ID, d.Revision)
	require.NoError(t, err)
	assert.True(t, deleted, "an untouched reply is dropped like any other")

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

// A rule never forwards a receive-only account's mail through another account.
func TestReceiveOnlyRuleForwardFails(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	smtpAddr, sent := testmail.StartSMTP(t)
	_, err := e.s.AddAccount(bg, internalInput(t, e.im.Addr, smtpAddr, "carol@example.com", false))
	require.NoError(t, err)
	e.newMail(t, "old@x", "before")
	e.sync(t)
	e.saveForward(t, forwardRule(carol), carol)
	e.put(t, "INBOX", billWithPDF("bill@x"))
	e.sync(t)
	e.waitRuns(t, 1)

	assert.NotEqual(t, []string{"forward done"}, e.results(t, e.runOf(t, "bill@x")))
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM outbox"))
	assert.Empty(t, sent.Messages())
}

// A forward that would be skipped anyway lets the rule's other actions run.
func TestReceiveOnlyRuleSkippedForwardGoesOn(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	e.newMail(t, "old@x", "before")
	e.sync(t)
	e.saveForward(t, forwardRule(carol, RuleAction{Kind: ActionRead}), carol)
	e.put(t, "INBOX", billWithPDF("fwd@x", mail.RuleHeader+": 7"))
	e.sync(t)
	e.waitRuns(t, 1)
	assert.Equal(t, []string{"forward skipped", "read done"}, e.results(t, e.runOf(t, "fwd@x")))
}

func TestRecheckSending(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	r, err := e.s.RecheckSending(bg, e.acc.ID)
	require.NoError(t, err)
	assert.True(t, r.OnlySendingFailed())
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly, "still failing: stays receive-only")

	// A new password goes in although SMTP still refuses it.
	require.NoError(t, e.s.UpdatePassword(bg, e.acc.ID, "secret"))
	acc, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly)

	e.smtp.RejectAuth(false)
	r, err = e.s.RecheckSending(bg, e.acc.ID)
	require.NoError(t, err)
	assert.True(t, r.OK())
	acc, err = e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.False(t, acc.ReceiveOnly)
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.smtp.Messages(), 1)
}

func TestReceiveOnlyPasswordResumes(t *testing.T) {
	e := newReceiveOnlyEnv(t)
	e.smtp.RejectAuth(false)
	require.NoError(t, e.s.UpdatePassword(bg, e.acc.ID, "secret"))
	acc, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.False(t, acc.ReceiveOnly)
}

// An OAuth account has no servers to change: signing in again rechecks SMTP.
func TestReceiveOnlyReauthorizeResumes(t *testing.T) {
	e := newOAuthEnv(t, false)
	e.smtp.RejectAuth(true)
	in := e.input(t, "alice@example.com", e.signIn(t, "alice@example.com"))
	in.ReceiveOnly = true
	acc, err := e.s.AddAccount(bg, in)
	require.NoError(t, err)
	require.True(t, acc.ReceiveOnly)

	require.NoError(t, e.s.Reauthorize(bg, acc.ID, e.signIn(t, "alice@example.com")))
	acc, err = e.s.account(bg, acc.ID)
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly, "SMTP still refuses: no error, still receive-only")

	e.smtp.RejectAuth(false)
	require.NoError(t, e.s.Reauthorize(bg, acc.ID, e.signIn(t, "alice@example.com")))
	acc, err = e.s.account(bg, acc.ID)
	require.NoError(t, err)
	assert.False(t, acc.ReceiveOnly)
}

// An Outlook mailbox without SMTP AUTH or Graph consent is added receive-only;
// once Graph's Mail.Send is granted, the recheck makes it send through Graph.
func TestRecheckSendingThroughGraph(t *testing.T) {
	e := newGraphEnv(t)
	e.oa.GrantScope("openid email")
	in := e.input(t, "alice@example.com", e.signIn(t, "alice@example.com"))
	in.ReceiveOnly = true
	acc, err := e.s.AddAccount(bg, in)
	require.NoError(t, err)
	require.True(t, acc.ReceiveOnly)
	e.acc = acc

	e.oa.GrantScope("Mail.Send offline_access")
	r, err := e.s.RecheckSending(bg, acc.ID)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	acc, err = e.s.account(bg, acc.ID)
	require.NoError(t, err)
	assert.False(t, acc.ReceiveOnly)
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.graphSent(), 1)
}
