package core

import (
	"net/url"
	"testing"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// Fastmail's way: JMAP signs in with an API token (Bearer), SMTP with an app
// password of its own. Each slot keeps and uses its own credential, and the
// sending one can be replaced alone.
func TestJMAPAPITokenWithOwnSMTPPassword(t *testing.T) {
	keyring.MockInit()
	clock := useTestClock(t)
	s, err := New(bg, Options{DataDir: t.TempDir(), ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	st.ExperimentalJMAP = true
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	jm := testmail.StartJMAP(t)
	smtpAddr, inbox := testmail.StartSMTP(t)
	inbox.SetPassword("smtp-pass")
	u, err := url.Parse(jm.URL())
	require.NoError(t, err)
	jh, jp := splitAddr(t, u.Host)
	sh, sp := splitAddr(t, smtpAddr)
	in := AccountInput{Email: "alice@example.com", Username: "alice", Password: "secret", SendPassword: "smtp-pass",
		Receiving: Server{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Auth: AuthAPIToken},
		Sending:   Server{Host: sh, Port: sp, Security: SecurityNone}}

	acc, err := s.AddAccount(bg, in)
	require.NoError(t, err)
	assert.Equal(t, AuthAPIToken, acc.Receiving.Auth)
	assert.Equal(t, AuthPassword, acc.Sending.Auth)
	recv, _, err := s.slotCredentials(bg, acc, false, "")
	require.NoError(t, err)
	assert.Equal(t, "secret", recv.Token, "the API token goes as a Bearer token")
	assert.Empty(t, recv.Password)
	send, _, err := s.slotCredentials(bg, acc, true, "")
	require.NoError(t, err)
	assert.Equal(t, "smtp-pass", send.Password)

	jm.Deliver("INBOX", []byte("From: bob@example.com\r\nTo: alice@example.com\r\nSubject: hi\r\nMessage-ID: <hi@x>\r\n\r\nhello\r\n"))
	require.NoError(t, s.SyncNow(bg, acc.ID))
	ms, err := s.Search(bg, acc.ID, "hi", 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1)

	sendOne := func() {
		t.Helper()
		d, err := s.NewDraft(bg, acc.ID, "new", 0)
		require.NoError(t, err)
		d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "hello", "<p>hi</p>"
		d, err = s.SaveDraft(bg, d)
		require.NoError(t, err)
		require.NoError(t, s.Send(bg, d.ID))
		clock.Advance(UndoSendWindow)
		require.NoError(t, s.SyncNow(bg, acc.ID))
	}
	sendOne()
	assert.Len(t, inbox.Messages(), 1)

	inbox.SetPassword("new-pass")
	require.Error(t, s.UpdateSendPassword(bg, acc.ID, "wrong"), "checked before it is kept")
	require.NoError(t, s.UpdateSendPassword(bg, acc.ID, "new-pass"))
	sendOne()
	assert.Len(t, inbox.Messages(), 2)
	require.NoError(t, s.SyncNow(bg, acc.ID), "the token still signs in to JMAP")

	_, err = s.AddAccount(bg, AccountInput{Email: "bob@example.com", Username: "alice", Password: "secret",
		Receiving: in.Receiving, Sending: in.Sending})
	assert.ErrorContains(t, err, "sending password", "an API token does not sign in to SMTP")
}
