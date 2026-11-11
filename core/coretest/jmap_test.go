package coretest_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
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
	require.Len(t, r.Steps, 3)
	assert.Equal(t, "jmap_send", r.Steps[2].Name)
	assert.Equal(t, "jmap", r.Steps[1].Name)
	assert.Equal(t, in.Receiving.Host+":"+strconv.Itoa(in.Receiving.Port), r.Host)
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "jmap", acc.Receiving.Protocol)
	assert.Equal(t, "jmap", acc.Sending.Protocol)

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

func TestJMAPLightBackgroundFinishesWholeAccountSync(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	id := srv.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@example.com", "RemoveCached", "body"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "RemoveCached", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	e.Svc.SetLightBackground(true)
	srv.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@example.com", "BackgroundReady", "body"))
	require.Eventually(t, func() bool {
		ms, err := e.Svc.Search(t.Context(), acc.ID, "BackgroundReady", 10)
		return err == nil && len(ms) == 1
	}, 5*time.Second, 20*time.Millisecond)

	res, err := other(t, e, acc.ID).Apply(t.Context(), backend.Purge{At: at(e, acc.ID, "INBOX", id)})
	require.NoError(t, err)
	require.Equal(t, backend.Done, res.Outcome)
	require.Eventually(t, func() bool {
		ms, err := e.Svc.Search(t.Context(), acc.ID, "RemoveCached", 10)
		return err == nil && len(ms) == 0
	}, 5*time.Second, 20*time.Millisecond)
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

// Mail sent from a JMAP account goes out over JMAP EmailSubmission/set (never
// dialing SMTP), records the full envelope including Bcc without leaking Bcc
// into headers, and leaves a single copy in Sent.
func TestJMAPSendSavesSent(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	assert.Nil(t, e.SMTP(acc.ID), "JMAP account has no SMTP server")

	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	sentBox := srv.CreateMailbox("Sent", mailbox.RoleSent)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"dave@example.com"}
	d.Cc = []string{"carol@example.com"}
	d.Bcc = []string{"secret@example.com"}
	d.Subject, d.HTML = "Out by JMAP", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	require.Eventually(t, func() bool { return len(srv.Submissions()) == 1 }, 5*time.Second, 20*time.Millisecond)
	sub := srv.Submissions()[0]
	assert.Equal(t, "alice@example.com", sub.MailFrom)
	assert.ElementsMatch(t, []string{"dave@example.com", "carol@example.com", "secret@example.com"}, sub.RcptTo)
	assert.NotContains(t, string(sub.Raw), "secret@example.com")

	require.Eventually(t, func() bool {
		require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
		return folderTotal(t, e, acc.ID, "Sent") == 1
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Drafts"))

	// Verify on the server directly that Sent holds exactly one Email.
	var sentCount int
	_, err = other(t, e, acc.ID).Sync(t.Context(), backend.SyncRequest{}, func(p backend.Page) error {
		for _, ch := range p.Changes {
			if ma, ok := ch.(backend.MessageAppeared); ok && ma.Location.Folder == backend.FolderRef(sentBox) {
				sentCount++
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, sentCount, "Sent mailbox on JMAP server must hold a single copy")
}

// When the user assigns Sent to a folder without the server's sent role via
// SetRoleFolder, JMAP sending preserves the sent copy in that folder instead of
// destroying the staged email.
func TestJMAPSendUsesRoleOverrideSentFolder(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	customSent := srv.CreateMailbox("FiledSent", "")
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	var filedID int64
	for _, f := range fs {
		if f.Name == "FiledSent" {
			filedID = f.ID
		}
	}
	require.NotZero(t, filedID)
	require.NoError(t, e.Svc.SetRoleFolder(t.Context(), acc.ID, "sent", filedID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "Custom Sent", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	require.Len(t, srv.Submissions(), 1)
	items, err := e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	assert.Empty(t, items)

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "FiledSent"))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Drafts"))

	var customCount int
	_, err = other(t, e, acc.ID).Sync(t.Context(), backend.SyncRequest{}, func(p backend.Page) error {
		for _, ch := range p.Changes {
			if ma, ok := ch.(backend.MessageAppeared); ok && ma.Location.Folder == backend.FolderRef(customSent) {
				customCount++
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, customCount)
}

// If EmailSubmission/set accepts the message but onSuccessUpdateEmail fails to
// move it to Sent, the outbox retains archival recovery (archive_uncertain) and
// archives the sent copy on a later round without resubmitting the email.
func TestJMAPSendArchivalRecoveryWhenOnSuccessUpdateFails(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	draftsBox := srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	sentBox := srv.CreateMailbox("Sent", mailbox.RoleSent)

	var breakSent atomic.Bool
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if breakSent.Load() && r.URL.Path == "/jmap/api" &&
			(bytes.Contains(body, []byte("EmailSubmission/set")) || bytes.Contains(body, []byte("Email/import"))) {
			body = bytes.ReplaceAll(body, []byte(`"`+string(sentBox)+`"`), []byte(`"missing-sent"`))
			body = bytes.ReplaceAll(body, []byte("mailboxIds/"+string(sentBox)), []byte("mailboxIds/missing-sent"))
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		out = bytes.ReplaceAll(out, []byte(srv.URL()), []byte(proxy.URL))
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	t.Cleanup(proxy.Close)

	pu, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	ph, ppStr, err := net.SplitHostPort(pu.Host)
	require.NoError(t, err)
	pp, err := strconv.Atoi(ppStr)
	require.NoError(t, err)
	ss := core.ServerSettings{
		Username:  acc.Username,
		Receiving: core.Server{Protocol: "jmap", Host: ph, Port: pp, Security: core.SecurityNone, Path: "/jmap/session"},
	}
	ss.Sending = ss.Receiving
	_, err = e.Svc.UpdateAccount(t.Context(), acc.ID, core.AccountPatch{Servers: &ss})
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "Recover Sent", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)

	breakSent.Store(true)
	_ = e.Svc.SyncNow(t.Context(), acc.ID)

	require.Len(t, srv.Submissions(), 1, "accepted once by EmailSubmission/set")
	items, err := e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "archive_uncertain", items[0].State, "retains archival recovery state instead of dropping item")

	// Staged copy in Drafts was cleaned up.
	var draftsLeft int
	_, err = other(t, e, acc.ID).Sync(t.Context(), backend.SyncRequest{}, func(p backend.Page) error {
		for _, ch := range p.Changes {
			if ma, ok := ch.(backend.MessageAppeared); ok && ma.Location.Folder == backend.FolderRef(draftsBox) {
				draftsLeft++
			}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Zero(t, draftsLeft, "no leftover staged draft in Drafts")

	// Once Sent is writable again, the next sync archives the sent copy without resubmitting.
	breakSent.Store(false)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Len(t, srv.Submissions(), 1, "must never resubmit an already accepted message")
	items, err = e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	assert.Empty(t, items, "outbox item finished after archival recovery")

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 1, folderTotal(t, e, acc.ID, "Sent"))
	assert.Equal(t, 0, folderTotal(t, e, acc.ID, "Drafts"))
}

// A rate-limited EmailSubmission/set creation keeps the outbox item queued for
// automatic backoff retry instead of failing it permanently.
func TestJMAPSendRateLimitRetriesWithBackoff(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	srv.CreateMailbox("Sent", mailbox.RoleSent)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "Rate limited", "<p>retry me</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)

	desc := "too many submissions per minute"
	srv.FailNextSubmission(&gojmap.SetError{Type: "rateLimit", Description: &desc})
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	assert.Empty(t, srv.Submissions())
	items, err := e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "queued", items[0].State, "rateLimit must stay queued for backoff retry")
	assert.Equal(t, 1, items[0].Attempts)

	// Advance past first retry backoff and verify automatic delivery succeeds.
	e.AdvanceClock(2 * time.Minute)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Len(t, srv.Submissions(), 1)
	items, err = e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	assert.Empty(t, items)
}

// A serverPartialFail method error on EmailSubmission/set moves the outbox item
// to send_uncertain instead of queued so it is never automatically resent.
func TestJMAPSendServerPartialFailEntersSendUncertainWithoutResending(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	srv.CreateMailbox("Sent", mailbox.RoleSent)

	var partialFail atomic.Bool
	var subCalls atomic.Int32
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/jmap/api" && bytes.Contains(body, []byte("EmailSubmission/set")) {
			subCalls.Add(1)
			if partialFail.CompareAndSwap(true, false) {
				// Simulate a server that returns serverPartialFail before reaching Sent.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"sessionState":"0","methodResponses":[["Email/import",{"accountId":"u","created":{"m":{"id":"e999"}}},"0"],["error",{"type":"serverPartialFail"},"1"]]}`))
				return
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, srv.URL()+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		out = bytes.ReplaceAll(out, []byte(srv.URL()), []byte(proxy.URL))
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	t.Cleanup(proxy.Close)

	pu, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	ph, ppStr, err := net.SplitHostPort(pu.Host)
	require.NoError(t, err)
	pp, err := strconv.Atoi(ppStr)
	require.NoError(t, err)
	ss := core.ServerSettings{
		Username:  acc.Username,
		Receiving: core.Server{Protocol: "jmap", Host: ph, Port: pp, Security: core.SecurityNone, Path: "/jmap/session"},
	}
	ss.Sending = ss.Receiving
	_, err = e.Svc.UpdateAccount(t.Context(), acc.ID, core.AccountPatch{Servers: &ss})
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "Partial Fail", "<p>uncertain</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)

	partialFail.Store(true)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, int32(1), subCalls.Load())

	items, err := e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "send_uncertain", items[0].State, "serverPartialFail must enter send_uncertain")

	// Later syncs must never automatically resend an item in send_uncertain.
	e.AdvanceClock(10 * time.Minute)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, int32(1), subCalls.Load(), "must not automatically retry after serverPartialFail")
}

// A JMAP session without urn:ietf:params:jmap:submission fails only the
// check's sending step: the account can be added receive-only.
func TestJMAPNoSubmissionAddsReceiveOnly(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	setJMAP(t, e, true)
	in := e.StartJMAPServers(t, "alice@example.com")
	seed, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	e.JMAP(seed.ID).DisableSubmission()

	in.Email = "other@example.com"
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, r.Steps, 3)
	assert.True(t, r.Steps[1].OK, "receiving still signs in")
	assert.Equal(t, "jmap_send", r.Steps[2].Name)
	assert.Equal(t, jmap.ReasonNoSubmission, r.Steps[2].Detail)
	assert.True(t, r.OnlySendingFailed())
	_, err = e.Svc.AddAccount(t.Context(), in)
	assert.ErrorIs(t, err, jmap.ErrNoSubmission, "not added as a sending account")

	in.ReceiveOnly = true
	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.True(t, acc.ReceiveOnly)
}

// An existing JMAP account whose sign-in may not send fails its message with
// the no-submission reason, and a new token without submission is still
// saved: the account becomes receive-only.
func TestJMAPNoSubmissionSendFailsAndTokenUpdateKeepsReceiving(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com", coretest.ViaJMAP())
	srv := e.JMAP(acc.ID)
	srv.CreateMailbox("Drafts", mailbox.RoleDrafts)
	srv.CreateMailbox("Sent", mailbox.RoleSent)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	srv.DisableSubmission()

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "No submission", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	items, err := e.Svc.ListOutbox(t.Context(), acc.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "failed", items[0].State)
	require.NotNil(t, items[0].Failure)
	assert.Equal(t, jmap.ReasonNoSubmission, items[0].Failure.Message)
	assert.Empty(t, srv.Submissions())

	require.NoError(t, e.Svc.UpdatePassword(t.Context(), acc.ID, "secret"))
	got, err := e.Svc.ListAccounts(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].ReceiveOnly, "saved, receiving only")
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
