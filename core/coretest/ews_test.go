package coretest_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setEWS(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalEWS = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

// An EWS account is added from a typed-in EWS URL only with the experiment
// on; its check names the EWS host. Its folders take their roles and its
// inbox's mail comes in whole and opens, the experiment turned off again.
func TestEWSAddAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	in, _ := e.StartEWSServers(t, "alice@example.com")
	_, err := e.Svc.AddAccount(t.Context(), in)
	require.ErrorIs(t, err, core.ErrEWSOff)

	setEWS(t, e, true)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	assert.Equal(t, "ews", r.Steps[1].Name)
	assert.Equal(t, in.Receiving.Host+":"+strconv.Itoa(in.Receiving.Port), r.Host)
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "ews", acc.Receiving.Protocol)
	assert.Equal(t, "ews", acc.Sending.Protocol)
	assert.Equal(t, "/EWS/Exchange.asmx", acc.Receiving.Path)

	setEWS(t, e, false)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hello EWS", "the body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Hello", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	m, err := e.Svc.GetMessageText(t.Context(), ms[0].ID)
	require.NoError(t, err)
	assert.Contains(t, m.Text, "the body")

	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	roles := map[string]string{}
	for _, f := range fs {
		roles[f.Name] = f.Role
	}
	assert.Equal(t, map[string]string{"Inbox": "inbox", "Sent Items": "sent", "Drafts": "drafts", "Deleted Items": "trash",
		"Junk Email": "junk", "Archive": "archive"}, roles)
}

// addEWS adds an EWS account and syncs it once.
func addEWS(t *testing.T, e *coretest.Env) (core.Account, *coretest.EWS) {
	t.Helper()
	setEWS(t, e, true)
	in, srv := e.StartEWSServers(t, "alice@example.com")
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	return acc, srv
}

// queueSend sends a new draft to dave, Bcc eve, past the undo window.
func queueSend(t *testing.T, e *coretest.Env, acc core.Account) {
	t.Helper()
	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Bcc, d.Subject, d.HTML = []string{"dave@example.com"}, []string{"eve@example.com"}, "Out by EWS", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
}

// Mail sent from an EWS account goes out through EWS to every recipient,
// Bcc ones unnamed in it, and lands in Sent Items once: the server's copy.
func TestEWSSendThroughOutbox(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, srv := addEWS(t, e)
	queueSend(t, e, acc)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.Eventually(t, func() bool { return len(srv.Sent()) == 1 }, 5*time.Second, 20*time.Millisecond)
	sent := srv.Sent()[0]
	assert.ElementsMatch(t, []string{"dave@example.com", "eve@example.com"}, sent.Rcpts)
	assert.NotContains(t, string(sent.Raw), "eve@")
	require.Eventually(t, func() bool {
		require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
		return folderTotal(t, e, acc.ID, "Sent Items") == 1
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, 1, srv.Count("Sent Items"))
}

// After EWS refused the credentials of a send, an explicit sync signs in
// to EWS again, never to SMTP, and the outbox resumes.
func TestEWSSendAuthFailedResumes(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, srv := addEWS(t, e)
	queueSend(t, e, acc)
	srv.RejectNextSendAuth()
	require.ErrorIs(t, e.Svc.SyncNow(t.Context(), acc.ID), backend.ErrAuthFailed)
	assert.Empty(t, srv.Sent())
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.Eventually(t, func() bool { return len(srv.Sent()) == 1 }, 5*time.Second, 20*time.Millisecond)
}

// A server older than Exchange 2016 fails the check with its reason.
func TestEWSServerTooOld(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	setEWS(t, e, true)
	in, srv := e.StartEWSServers(t, "alice@example.com")
	srv.SetVersion(15, 0)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.False(t, r.OK())
	assert.Equal(t, ews.ReasonServerTooOld, r.Steps[1].Detail)
	_, err = e.Svc.AddAccount(t.Context(), in)
	require.ErrorIs(t, err, ews.ErrServerTooOld)
}
