package core

import (
	"net/url"
	"testing"

	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// A JMAP account with an API token (Bearer) receives and sends over JMAP
// without any SMTP server or separate sending password; legacy accounts stored
// with send_protocol='smtp' also send over JMAP once read.
func TestJMAPAPITokenSendsAndReceivesWithoutSMTP(t *testing.T) {
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
	jm.CreateMailbox("Drafts", mailbox.RoleDrafts)
	jm.CreateMailbox("Sent", mailbox.RoleSent)
	u, err := url.Parse(jm.URL())
	require.NoError(t, err)
	jh, jp := splitAddr(t, u.Host)
	in := AccountInput{
		Email:     "alice@example.com",
		Username:  "alice",
		Password:  "secret",
		Receiving: Server{Protocol: auth.JMAP, Host: jh, Port: jp, Security: SecurityNone, Auth: AuthAPIToken},
	}

	acc, err := s.AddAccount(bg, in)
	require.NoError(t, err)
	assert.Equal(t, AuthAPIToken, acc.Receiving.Auth)
	assert.Equal(t, auth.JMAP, acc.Sending.Protocol)
	assert.Equal(t, AuthAPIToken, acc.Sending.Auth)

	// Even if a legacy sendUser secret exists in the keychain from an older
	// version, JMAP sending still uses the API token as Bearer.
	require.NoError(t, keychainSet(s.sendUser(acc.ID), "legacy-smtp-pass"))

	recv, _, err := s.slotCredentials(bg, acc, false, "")
	require.NoError(t, err)
	assert.Equal(t, "secret", recv.Token, "the API token goes as a Bearer token")
	assert.Empty(t, recv.Password)
	send, _, err := s.slotCredentials(bg, acc, true, "")
	require.NoError(t, err)
	assert.Equal(t, "secret", send.Token, "JMAP sending uses the same Bearer token")
	assert.Empty(t, send.Password)

	jm.Deliver("INBOX", []byte("From: bob@example.com\r\nTo: alice@example.com\r\nSubject: hi\r\nMessage-ID: <hi@x>\r\n\r\nhello\r\n"))
	require.NoError(t, s.SyncNow(bg, acc.ID))
	ms, err := s.Search(bg, acc.ID, "hi", 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1)

	sendOne := func(subj string) {
		t.Helper()
		d, err := s.NewDraft(bg, acc.ID, "new", 0)
		require.NoError(t, err)
		d.To, d.Subject, d.HTML = []string{"bob@example.com"}, subj, "<p>hi</p>"
		d, err = s.SaveDraft(bg, d)
		require.NoError(t, err)
		require.NoError(t, s.Send(bg, d.ID))
		clock.Advance(UndoSendWindow)
		require.NoError(t, s.SyncNow(bg, acc.ID))
	}
	sendOne("first")
	require.Len(t, jm.Submissions(), 1)

	// Simulate an existing JMAP account row created before JMAP EmailSubmission
	// (send_protocol='smtp' pointing at a dead port, with stale saves_sent feature).
	_, err = s.db.Transaction(bg, []sqlite.Statement{{
		SQL: `UPDATE accounts SET send_protocol = 'smtp', send_host = '127.0.0.1', send_port = 1, send_auth = 'password',
			features = '{"identity":"stable","features":{"saves_sent":{"level":"unsupported","reason":"jmap.sent_via_smtp"}}}' WHERE id = ?`,
		Args: []any{acc.ID},
	}})
	require.NoError(t, err)
	reloaded, err := s.account(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, auth.JMAP, reloaded.Sending.Protocol)
	assert.Equal(t, AuthAPIToken, reloaded.Sending.Auth)
	assert.Equal(t, backend.LevelSupported, reloaded.Features[backend.FeatureSavesSent].Level)

	sendOne("migrated")
	require.Len(t, jm.Submissions(), 2)

	// Updating the JMAP API token checks against JMAP and replaces it cleanly.
	jm.SetPassword("new-token")
	require.Error(t, s.UpdatePassword(bg, acc.ID, "wrong"), "checked before it is kept")
	require.NoError(t, s.UpdatePassword(bg, acc.ID, "new-token"))
	sendOne("rotated")
	assert.Len(t, jm.Submissions(), 3)
}
