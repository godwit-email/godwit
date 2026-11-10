package coretest_test

import (
	"os"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setPOP3(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalPOP3 = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

// A POP3 account is added only with the experiment on, checked over POP3,
// and comes with a local trash, archive and sent folder beside its inbox.
func TestPOP3AddAccount(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	in := e.StartPOP3Servers(t, "alice@example.com")
	_, err := e.Svc.AddAccount(t.Context(), in)
	require.ErrorContains(t, err, "POP3 is experimental")
	require.ErrorContains(t, e.Svc.TestAccount(t.Context(), in), `receiving protocol "pop3" is not supported`)

	setPOP3(t, e, true)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	assert.Equal(t, "pop3", r.Steps[1].Name)
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "pop3", acc.Receiving.Protocol)

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	roles := map[string]string{}
	for _, f := range fs {
		roles[f.Name] = f.Role
	}
	assert.Equal(t, map[string]string{"INBOX": "inbox", "Trash": "trash", "Archive": "archive", "Sent": "sent"}, roles)

	// Turned off, the account still syncs.
	setPOP3(t, e, false)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hello POP3", "body"))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Hello", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
}

// A sync round downloads the maildrop: what was there before the first
// round is existing mail, what came after is new mail a receive rule runs on.
func TestPOP3SyncArrives(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	l, err := e.Svc.CreateLabel(t.Context(), core.LabelInput{Name: "Invoices", Color: "gray", Icon: "folder-3"})
	require.NoError(t, err)
	_, err = e.Svc.SaveRule(t.Context(), core.Rule{Enabled: true, Doc: core.RuleDoc{Version: core.RuleVersion, Name: "invoices",
		Trigger: core.TriggerReceive, Match: core.MatchAll,
		Conditions: []core.RuleCondition{{Field: core.FieldSubject, Op: core.OpContains, Value: "invoice"}},
		Actions:    []core.RuleAction{{Kind: core.ActionLabel, Label: l.Slug}}}})
	require.NoError(t, err)
	e.POP3(acc.ID).Deliver(coretest.Msg("billing@example.com", "alice@example.com", "Old invoice", "b"))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@example.com", "alice@example.com", "New invoice", "b"))

	labeled := func(subject string) bool {
		ms, err := e.Svc.Search(t.Context(), acc.ID, subject, 10)
		require.NoError(t, err)
		require.Len(t, ms, 1)
		got, _ := e.Svc.MessageLabels(t.Context(), []int64{ms[0].ID})
		return len(got[ms[0].ID]) > 0
	}
	require.Eventually(t, func() bool { return labeled("New invoice") }, 5*time.Second, 20*time.Millisecond)
	assert.False(t, labeled("Old invoice"), "mail already there is not new")
}

func searchOne(t *testing.T, e *coretest.Env, accountID int64, q string) core.MessageSummary {
	t.Helper()
	ms, err := e.Svc.Search(t.Context(), accountID, q, 10)
	require.NoError(t, err)
	require.Len(t, ms, 1, q)
	return ms[0]
}

func noPendingOps(t *testing.T, e *coretest.Env, accountID int64) {
	t.Helper()
	ops, err := e.Svc.PendingOps(t.Context(), accountID)
	require.NoError(t, err)
	assert.Empty(t, ops)
}

// Deleting and archiving happen on this computer: the mail shows in the local
// Trash and Archive, stays on the server, and the next round does not bring
// it back to the inbox. No move waits to be pushed.
func TestPOP3MovesStayLocal(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Bin me", "body"))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Keep me", "body"))
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{searchOne(t, e, acc.ID, "Bin").ID}))
	require.NoError(t, e.Svc.MoveToRole(t.Context(), []int64{searchOne(t, e, acc.ID, "Keep").ID}, "archive"))
	noPendingOps(t, e, acc.ID)

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "INBOX"))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "Trash"))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "Archive"))
	assert.Len(t, e.POP3(acc.ID).UIDs(), 2, "still on the server")

	// Moved back, it is in the inbox again, still with nothing to push.
	require.NoError(t, e.Svc.MoveToRole(t.Context(), []int64{searchOne(t, e, acc.ID, "Keep").ID}, "inbox"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "INBOX"))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Archive"))
	noPendingOps(t, e, acc.ID)
}

// Deleting for good from the local Trash waits out the undo window, then
// DELEs the message by its inbox location and deletes it here.
func TestPOP3DeleteForever(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Gone for good", "body"))
	id := searchOne(t, e, acc.ID, "Gone").ID
	kept, err := e.Svc.ArchiveUsage(t.Context(), acc.ID)
	require.NoError(t, err)
	assert.Positive(t, kept, "its original is kept")
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{id}))
	require.NoError(t, e.Svc.DeleteForever(t.Context(), []int64{id}))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Len(t, e.POP3(acc.ID).UIDs(), 1, "not before the undo window ends")

	e.AdvanceClock(core.UndoPurgeWindow + time.Second)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Empty(t, e.POP3(acc.ID).UIDs())
	assert.Equal(t, 1, e.POP3(acc.ID).Deletes())
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Gone", 10)
	require.NoError(t, err)
	assert.Empty(t, ms)
	noPendingOps(t, e, acc.ID)
	kept, err = e.Svc.ArchiveUsage(t.Context(), acc.ID)
	require.NoError(t, err)
	assert.Zero(t, kept, "its original is deleted too")
}

// Mail the server no longer has is deleted for good here alone; mail the
// server refuses to delete goes back to the Trash, the delete failed.
func TestPOP3DeleteForeverGoneOrRefused(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	srv := e.POP3(acc.ID)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Already gone", "body"))
	gone := searchOne(t, e, acc.ID, "Already").ID
	srv.Remove(srv.UIDs()[0])
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{gone}))
	require.NoError(t, e.Svc.DeleteForever(t.Context(), []int64{gone}))
	e.AdvanceClock(core.UndoPurgeWindow + time.Second)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "Already", 10)
	require.NoError(t, err)
	assert.Empty(t, ms)
	assert.Equal(t, 0, srv.Deletes())

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Refused", "body"))
	refused := searchOne(t, e, acc.ID, "Refused").ID
	srv.RejectDele()
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{refused}))
	require.NoError(t, e.Svc.DeleteForever(t.Context(), []int64{refused}))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Trash"), "hidden while it waits")
	e.AdvanceClock(core.UndoPurgeWindow + time.Second)
	require.Eventually(t, func() bool { // retried as on IMAP, then given up
		_ = e.Svc.SyncNow(t.Context(), acc.ID) // reports the refusal
		ops, err := e.Svc.PendingOps(t.Context(), acc.ID)
		require.NoError(t, err)
		require.Len(t, ops, 1)
		return ops[0].State == "failed"
	}, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, srv.UIDs(), 1)
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "Trash"), "back where it was")
}

// Mail the server deleted stays here, and its body, attachment and original
// are read from the original kept on this computer, not from the server.
func TestPOP3KeptOffline(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
	srv := e.POP3(acc.ID)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Report", "see attached",
		coretest.Attachment("report.txt", "text/plain", []byte("the report"))))
	id := searchOne(t, e, acc.ID, "Report").ID
	srv.Remove(srv.UIDs()[0])
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	srv.RejectAuth() // any visit to the server now fails

	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "INBOX"))
	m, err := e.Svc.GetMessageText(t.Context(), id)
	require.NoError(t, err)
	assert.Contains(t, m.Text, "see attached")
	require.Len(t, m.Attachments, 1)
	path, _, err := e.Svc.GetAttachment(t.Context(), m.Attachments[0].ID)
	require.NoError(t, err)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "the report", string(b))
	raw, err := e.Svc.GetRawMessage(t.Context(), id)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Subject: Report")
}

// Mail sent from a POP3 account goes out by SMTP and its copy is kept in the
// local Sent folder, read and not counted as new mail.
func TestPOP3SendKeepsSent(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaPOP3())
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
	m := searchOne(t, e, acc.ID, "Out by SMTP")
	assert.Contains(t, m.Flags, `\Seen`)
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "INBOX"))
}
