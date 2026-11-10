package coretest_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	gojmap "git.sr.ht/~rockorager/go-jmap"
	"git.sr.ht/~rockorager/go-jmap/mail/mailbox"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/jmap"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setJMAP(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalJMAP = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

// A JMAP account is added only with the experiment on; its check names the
// session's host, found through /.well-known/jmap.
func TestJMAPAddAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	in := e.StartJMAPServers(t, "alice@example.com")
	_, err := e.Svc.AddAccount(t.Context(), in)
	require.ErrorIs(t, err, core.ErrJMAPOff)

	setJMAP(t, e, true)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	assert.Equal(t, "jmap", r.Steps[1].Name)
	assert.Equal(t, in.Receiving.Host+":"+strconv.Itoa(in.Receiving.Port), r.Host)
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "jmap", acc.Receiving.Protocol)
	assert.Equal(t, "smtp", acc.Sending.Protocol)

	// Turned off, the account still syncs.
	setJMAP(t, e, false)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hello JMAP", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Hello", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	assert.Equal(t, "Hello JMAP", ms[0].Subject)
}

// A session URL typed in, with its own path, works as well as discovery.
func TestJMAPSessionURL(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	setJMAP(t, e, true)
	in := e.StartJMAPServers(t, "alice@example.com")
	in.Receiving.Path = "/jmap/session"
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "/jmap/session", acc.Receiving.Path)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Typed", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Typed", 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1)
}

func folderID(t *testing.T, e *coretest.Env, accountID int64, name string) int64 {
	t.Helper()
	fs, err := e.Svc.ListFolders(t.Context(), accountID)
	require.NoError(t, err)
	i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Name == name })
	require.GreaterOrEqual(t, i, 0, "no folder %q in %+v", name, fs)
	return fs[i].ID
}

// other is another JMAP client of the account's server.
func other(t *testing.T, e *coretest.Env, accountID int64) backend.Receiver {
	r := jmap.New(jmap.Config{SessionURL: e.JMAP(accountID).SessionURL(), Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "alice", Password: "secret"}, nil
	}})
	t.Cleanup(func() { r.Close() })
	return r
}

func at(e *coretest.Env, accountID int64, folder string, id gojmap.ID) backend.Location {
	return backend.Location{Folder: backend.FolderRef(e.JMAP(accountID).Mailbox(folder)), Ref: backend.MessageRef(id), ServerID: string(id)}
}

// A move in Godwit moves the Email on the server; a flag change and a
// removal made elsewhere come back as the server's changes.
func TestJMAPMoveAndChangesElsewhere(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.Mailbox("Archive")
	id := srv.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Move me", "body", coretest.MessageID("move@x")))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Move", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	archive := folderID(t, e, acc.ID, "Archive")

	require.NoError(t, e.Svc.Move(t.Context(), []int64{ms[0].ID}, archive))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	o := other(t, e, acc.ID)
	ref, err := o.Find(t.Context(), backend.FolderRef(srv.Mailbox("Archive")), "move@x", 0)
	require.NoError(t, err)
	assert.Equal(t, []backend.MessageRef{backend.MessageRef(id)}, ref, "the Email is in Archive on the server")

	res, err := o.Apply(t.Context(), backend.SetFlags{At: at(e, acc.ID, "Archive", id), Add: backend.Flags{Seen: true, Flagged: true}})
	require.NoError(t, err)
	require.Equal(t, backend.Done, res.Outcome)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	m, err := e.Svc.GetMessageText(t.Context(), ms[0].ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{`\Seen`, `\Flagged`}, m.Flags, "flags changed elsewhere")

	res, err = o.Apply(t.Context(), backend.Purge{At: at(e, acc.ID, "Archive", id)})
	require.NoError(t, err)
	require.Equal(t, backend.Done, res.Outcome)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err = e.Svc.Search(t.Context(), acc.ID, "Move", 10)
	require.NoError(t, err)
	assert.Empty(t, ms, "deleted elsewhere")
}

// The server's push wakes the account's sync: mail arrives with no SyncNow.
func TestJMAPPush(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	e.JMAP(acc.ID).Deliver("INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Pushed", "body"))
	require.Eventually(t, func() bool {
		ms, err := e.Svc.Search(t.Context(), acc.ID, "Pushed", 10)
		return err == nil && len(ms) == 1
	}, 10*time.Second, 50*time.Millisecond)
}

// A JMAP server refusing the credentials marks the account for sign-in again,
// as IMAP does.
func TestJMAPCredentialsRevoked(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	e.JMAP(acc.ID).RejectNextRequest()
	require.ErrorIs(t, e.Svc.SyncNow(t.Context(), acc.ID), backend.ErrAuthFailed)
	as, err := e.Svc.ListAccounts(t.Context())
	require.NoError(t, err)
	require.Len(t, as, 1)
	assert.Equal(t, "reauth_required", as[0].Status)
}

// An attachment downloads on demand from the whole message's blob.
func TestJMAPAttachment(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Report", "see attached",
		coretest.Attachment("report.txt", "text/plain", []byte("the report"))))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Report", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	m, err := e.Svc.GetMessageText(t.Context(), ms[0].ID)
	require.NoError(t, err)
	assert.Contains(t, m.Text, "see attached")
	require.Len(t, m.Attachments, 1)
	path, _, err := e.Svc.GetAttachment(t.Context(), m.Attachments[0].ID)
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "the report", string(b))
}

// An Email in two mailboxes is one message, shown in both folders.
func TestJMAPOneMessageInTwoMailboxes(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.AddEmail([]gojmap.ID{srv.Inbox, srv.Mailbox("Work")}, coretest.Msg("bob@example.com", "alice@example.com", "Both", "body"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Both", 10)
	require.NoError(t, err)
	assert.Len(t, ms, 1)
	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	for _, f := range fs {
		if f.Name == "Inbox" || f.Name == "Work" {
			assert.Equal(t, 1, f.Total, f.Name)
		}
	}
}

// Mail sent from a JMAP account goes out by SMTP and its copy is stored in
// the server's Sent mailbox.
func TestJMAPSendSavesSent(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Sent", mailbox.RoleSent)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "Out by SMTP", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.Eventually(t, func() bool { return len(e.Sent(acc.ID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
		return folderTotal(t, e, acc.ID, "Sent") == 1
	}, 5*time.Second, 50*time.Millisecond)
}

// Archiving moves a JMAP message to the Archive mailbox, as on plain IMAP:
// JMAP's stable identity is not Gmail's labels over All Mail.
func TestJMAPArchiveMoves(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Archive", mailbox.RoleArchive)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Archive me", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Archive", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.NoError(t, e.Svc.MoveToRole(t.Context(), []int64{ms[0].ID}, "archive"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Inbox"))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "Archive"))
}

// Deleting for good from Trash destroys the Email on the server: a JMAP
// folder keeps no cursor of its own, so no folder generation holds it back.
func TestJMAPDeleteForever(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	trash := srv.CreateMailbox("Trash", mailbox.RoleTrash)
	srv.AddEmail([]gojmap.ID{trash}, coretest.Msg("bob@example.com", "alice@example.com", "Binned", "body", coretest.MessageID("binned@x")))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Binned", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.NoError(t, e.Svc.DeleteForever(t.Context(), []int64{ms[0].ID}))
	e.AdvanceClock(core.UndoPurgeWindow + time.Second)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	left, err := other(t, e, acc.ID).Find(t.Context(), backend.FolderRef(trash), "binned@x", 0)
	require.NoError(t, err)
	assert.Empty(t, left, "destroyed on the server")
}

// A JMAP account's servers change to another session URL, never to IMAP.
func TestJMAPChangeServers(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	ss := core.ServerSettings{Username: "alice", Receiving: acc.Receiving, Sending: acc.Sending}
	ss.Receiving.Path = "/jmap/session"
	saved, err := e.Svc.UpdateAccount(t.Context(), acc.ID, core.AccountPatch{Servers: &ss})
	require.NoError(t, err)
	assert.Equal(t, "/jmap/session", saved.Receiving.Path)
	assert.Equal(t, "jmap", saved.Receiving.Protocol)

	ss.Receiving.Protocol = "imap"
	_, err = e.Svc.UpdateAccount(t.Context(), acc.ID, core.AccountPatch{Servers: &ss})
	assert.Error(t, err, "switched to IMAP in place")
}

func folderTotal(t *testing.T, e *coretest.Env, accountID int64, name string) int {
	fs, err := e.Svc.ListFolders(t.Context(), accountID)
	require.NoError(t, err)
	for _, f := range fs {
		if f.Name == name {
			return f.Total
		}
	}
	return -1
}
