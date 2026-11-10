package coretest_test

import (
	"strconv"
	"testing"

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

// A move in Godwit moves the item on the server, where it gets a new
// ItemId; the next sync keeps it as the same one message, in the new folder.
func TestEWSMove(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	setEWS(t, e, true)
	in, _ := e.StartEWSServers(t, "alice@example.com")
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	srv := e.EWS(acc.ID)
	id := srv.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Move me", "body", coretest.MessageID("move@x")))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Move", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	archive := folderID(t, e, acc.ID, "Archive")

	require.NoError(t, e.Svc.Move(t.Context(), []int64{ms[0].ID}, archive))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Empty(t, srv.Items("INBOX"), "the item left the inbox on the server")
	moved := srv.Items("Archive")
	require.Len(t, moved, 1, "the item is in Archive on the server")
	assert.NotEqual(t, id, moved[0], "under a new ItemId")
	ms, err = e.Svc.Search(t.Context(), acc.ID, "Move", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1, "one message after the sync")
	assert.Equal(t, archive, ms[0].FolderID)
}
