package coretest_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend/gmailapi/gmailapitest"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ownGmailClient = core.OAuthClient{ID: testoauth.ClientID, Secret: "own-secret"}

func setGmailAPI(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalGmailAPI = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

type gmailEnv struct {
	oa  *testoauth.Server
	api *gmailapitest.Server
}

func startGmailAPIEnv(t *testing.T, email string) *gmailEnv {
	t.Helper()
	oa := testoauth.Start(t)
	oa.Secret(ownGmailClient.Secret)
	api := gmailapitest.Start(t)
	api.SetEmail(email)
	api.SetTokenValidator(oa.ValidAccess)
	gmailProv, _ := auth.ByID("gmail")
	pubID := gmailProv.OAuth.Public.ID
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := oa.AuthURL
		if r.URL.Path == "/token" {
			target = oa.TokenURL
		}
		rawQ := bytes.ReplaceAll([]byte(r.URL.RawQuery), []byte(pubID), []byte(testoauth.ClientID))
		body, _ := io.ReadAll(r.Body)
		body = bytes.ReplaceAll(body, []byte(pubID), []byte(testoauth.ClientID))
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target+"?"+string(rawQ), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header = r.Header.Clone()
		hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := hc.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	core.SetOAuthConfigForTest(t.Cleanup, func(providerID string) (auth.OAuthConfig, error) {
		p, ok := auth.ByID(providerID)
		if !ok || p.OAuth == nil {
			return auth.OAuthConfig{}, auth.ErrOAuthNotConfigured
		}
		cfg := oa.Config("openid", "email", "https://mail.google.com/")
		cfg.AuthURL = proxy.URL + "/authorize"
		cfg.TokenURL = proxy.URL + "/token"
		cfg.ClientID = ""
		cfg.Public = p.OAuth.Public
		return cfg, nil
	})
	return &gmailEnv{oa: oa, api: api}
}

func (g *gmailEnv) signIn(t *testing.T, e *coretest.Env, email string, client core.OAuthClient) string {
	t.Helper()
	flowID, authURL, err := e.Svc.BeginOAuth(t.Context(), "gmail", email, client)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := e.Svc.AwaitOAuth(t.Context(), flowID)
		done <- err
	}()
	g.oa.Browse(t, authURL, email)
	require.NoError(t, <-done)
	return flowID
}

func (g *gmailEnv) input(email, flowID string) core.AccountInput {
	host, port := g.api.HostPort()
	return core.AccountInput{
		Email:       email,
		Provider:    "gmail",
		OAuthFlowID: flowID,
		Receiving: core.Server{
			Protocol: auth.GmailAPI,
			Host:     host,
			Port:     port,
			Security: core.SecurityNone,
		},
	}
}

func addGmailAPIAccount(t *testing.T, e *coretest.Env, email string) (core.Account, *gmailEnv) {
	t.Helper()
	setGmailAPI(t, e, true)
	g := startGmailAPIEnv(t, email)
	flowID := g.signIn(t, e, email, ownGmailClient)
	acc, err := e.Svc.AddAccount(t.Context(), g.input(email, flowID))
	require.NoError(t, err)
	return acc, g
}

// Gmail API accounts require ExperimentalGmailAPI, an OAuth sign-in, and the
// user's own Google Cloud OAuth client (refusing app passwords, Thunderbird's
// public client, and in-place protocol switching).
func TestGmailAPIAddAccountGates(t *testing.T) {
	const email = "alice@gmail.com"
	e := coretest.New(t, coretest.WithoutAutoSync())
	g := startGmailAPIEnv(t, email)

	// 1. Refused while ExperimentalGmailAPI is off.
	flowOff := g.signIn(t, e, email, ownGmailClient)
	_, err := e.Svc.AddAccount(t.Context(), g.input(email, flowOff))
	require.ErrorContains(t, err, "Gmail API is experimental")

	setGmailAPI(t, e, true)

	// 2. Refused with an app password (no OAuth flow).
	pwIn := g.input(email, "")
	pwIn.Password = "app-password"
	_, err = e.Svc.AddAccount(t.Context(), pwIn)
	require.ErrorContains(t, err, "OAuth")

	// 3. Refused when signed in with Thunderbird's public client.
	gmailProv, _ := auth.ByID("gmail")
	pub := gmailProv.OAuth.Public
	require.NotNil(t, pub)
	g.oa.Secret(pub.Secret)
	pubFlow := g.signIn(t, e, email, core.OAuthClient{ID: pub.ID, Secret: pub.Secret})
	rep, err := e.Svc.CheckAccount(t.Context(), g.input(email, pubFlow))
	require.NoError(t, err)
	assert.False(t, rep.OK())
	assert.Contains(t, rep.Steps[0].Detail, "own Google Cloud OAuth client")
	_, err = e.Svc.AddAccount(t.Context(), g.input(email, pubFlow))
	require.ErrorContains(t, err, "own Google Cloud OAuth client")

	// 4. Existing IMAP accounts cannot switch in place to Gmail API.
	imapAcc := e.AddAccount(t, "existing@gmail.com")
	host, port := g.api.HostPort()
	_, err = e.Svc.UpdateAccount(t.Context(), imapAcc.ID, core.AccountPatch{
		Servers: &core.ServerSettings{
			Receiving: core.Server{Protocol: auth.GmailAPI, Host: host, Port: port, Security: core.SecurityNone},
		},
	})
	require.ErrorContains(t, err, "receiving protocol cannot change")

	// 5. Succeeds with user's own OAuth client; turning the experiment off leaves it working.
	g.oa.Secret(ownGmailClient.Secret)
	flowOK := g.signIn(t, e, email, ownGmailClient)
	in := g.input(email, flowOK)
	rep, err = e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, rep.OK(), "%+v", rep.Steps)
	require.Len(t, rep.Steps, 2)
	assert.Equal(t, auth.GmailAPI, rep.Steps[1].Name)
	assert.Equal(t, host+":"+strconv.Itoa(port), rep.Host)

	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, auth.GmailAPI, acc.Receiving.Protocol)
	assert.Equal(t, auth.GmailAPI, acc.Sending.Protocol)
	assert.Equal(t, ownGmailClient.ID, acc.OAuthClientID)

	// Reauthorizing with Thunderbird's public client is also refused.
	g.oa.Secret(pub.Secret)
	reauthPub := g.signIn(t, e, email, core.OAuthClient{ID: pub.ID, Secret: pub.Secret})
	require.ErrorContains(t, e.Svc.Reauthorize(t.Context(), acc.ID, reauthPub), "own Google Cloud OAuth client")
	g.oa.Secret(ownGmailClient.Secret)

	setGmailAPI(t, e, false)
	g.api.Deliver("INBOX", coretest.Msg("bob@example.com", email, "StillSyncing", "hello after disabling lab"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "StillSyncing", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
}

func gmailFolderByName(t *testing.T, e *coretest.Env, accountID int64, name string) core.Folder {
	t.Helper()
	fs, err := e.Svc.ListFolders(t.Context(), accountID)
	require.NoError(t, err)
	i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Name == name })
	require.GreaterOrEqual(t, i, 0, "no folder %q in %+v", name, fs)
	return fs[i]
}

// A message carrying multiple Gmail labels (plus STARRED, IMPORTANT and All Mail)
// merges into one local message row shown in each folder, matching Gmail IMAP (ADR 0034).
func TestGmailAPIMultiLabelMerge(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, g := addGmailAPIAccount(t, e, "alice@gmail.com")

	workID := g.api.EnsureLabel("Work")
	projID := g.api.EnsureLabel("Work/Urgent")
	g.api.AddMessage(
		coretest.Msg("bob@example.com", "alice@gmail.com", "Quarterly Plan", "multi-label body", coretest.MessageID("qplan@x")),
		"INBOX", workID, projID, "STARRED", "IMPORTANT", "UNREAD",
	)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	ms, err := e.Svc.Search(t.Context(), acc.ID, "Quarterly", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1, "locations sharing message.id merge into one local message")

	full, err := e.Svc.GetMessageText(t.Context(), ms[0].ID)
	require.NoError(t, err)
	assert.Contains(t, full.Text, "multi-label body")
	assert.Contains(t, full.Flags, `\Flagged`)
	assert.NotContains(t, full.Flags, `\Seen`)

	for _, name := range []string{"INBOX", "Work", "Work/Urgent", "IMPORTANT", "[Gmail]/All Mail"} {
		f := gmailFolderByName(t, e, acc.ID, name)
		assert.Equal(t, 1, f.Total, "folder %s total", name)
		assert.Equal(t, 1, f.Unread, "folder %s unread", name)
	}
	assert.Equal(t, "flagged", gmailFolderByName(t, e, acc.ID, "STARRED").Role)
	assert.Empty(t, gmailFolderByName(t, e, acc.ID, "IMPORTANT").Role)
	assert.Equal(t, "archive", gmailFolderByName(t, e, acc.ID, "[Gmail]/All Mail").Role)

	byAcc, totalUnreadStarred, err := e.Svc.StarredCounts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, byAcc[acc.ID])
	assert.Equal(t, 1, totalUnreadStarred)
}

// Local flag edits, archiving (removing INBOX while keeping user labels), undo
// archive, folder moves and trashing write back via messages.modify / trash,
// and remote label changes sync incrementally via history.list.
func TestGmailAPILabelMutationsAndIncrementalSync(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, g := addGmailAPIAccount(t, e, "alice@gmail.com")

	workID := g.api.EnsureLabel("Work")
	doneID := g.api.EnsureLabel("Done")
	msgID := g.api.AddMessage(
		coretest.Msg("bob@example.com", "alice@gmail.com", "Action Item", "handle this", coretest.MessageID("act@x")),
		"INBOX", workID, "UNREAD",
	)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	ms, err := e.Svc.Search(t.Context(), acc.ID, "Action", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	localID := ms[0].ID

	// 1. Mark read and starred locally -> removes UNREAD and adds STARRED on server.
	require.NoError(t, e.Svc.SetFlags(t.Context(), []int64{localID}, []string{`\Seen`, `\Flagged`}, nil))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.ElementsMatch(t, []string{"INBOX", workID, "STARRED"}, g.api.MessageLabels(msgID))

	// 2. Archive removes INBOX only, keeping Work and All Mail immediately.
	undo, err := e.Svc.ArchiveMessages(t.Context(), []int64{localID}, 0)
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.ElementsMatch(t, []string{workID, "STARRED"}, g.api.MessageLabels(msgID))
	assert.Equal(t, 0, gmailFolderByName(t, e, acc.ID, "INBOX").Total)
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "Work").Total)
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "[Gmail]/All Mail").Total)

	// 3. Undo archive restores INBOX without dropping Work.
	require.NoError(t, e.Svc.UndoArchiveThreads(t.Context(), undo))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.ElementsMatch(t, []string{"INBOX", workID, "STARRED"}, g.api.MessageLabels(msgID))
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "INBOX").Total)

	// 4. Move from Work to Done swaps Work -> Done while keeping INBOX.
	workFolder := gmailFolderByName(t, e, acc.ID, "Work")
	doneFolder := gmailFolderByName(t, e, acc.ID, "Done")
	require.NoError(t, e.Svc.MoveFrom(t.Context(), []int64{localID}, workFolder.ID, doneFolder.ID))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.ElementsMatch(t, []string{"INBOX", doneID, "STARRED"}, g.api.MessageLabels(msgID))
	assert.Equal(t, 0, gmailFolderByName(t, e, acc.ID, "Work").Total)
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "Done").Total)

	// 5. Remote webmail adds Work back and unstars; incremental history.list updates local state.
	g.api.ModifyLabels(msgID, []string{workID, "UNREAD"}, []string{"STARRED"})
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "Work").Total)
	full, err := e.Svc.GetMessageText(t.Context(), localID)
	require.NoError(t, err)
	assert.NotContains(t, full.Flags, `\Flagged`)
	assert.NotContains(t, full.Flags, `\Seen`)

	// 6. Delete moves to Trash via /trash endpoint.
	require.NoError(t, e.Svc.Delete(t.Context(), []int64{localID}))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Contains(t, g.api.MessageLabels(msgID), "TRASH")
	assert.NotContains(t, g.api.MessageLabels(msgID), "INBOX")
}

// Expired startHistoryId (HTTP 404) reconciles via FolderSnapshot without losing
// local state or duplicating existing messages.
func TestGmailAPIHistoryExpiryReconciles(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, g := addGmailAPIAccount(t, e, "alice@gmail.com")

	workID := g.api.EnsureLabel("Work")
	keepID := g.api.AddMessage(coretest.Msg("bob@example.com", "alice@gmail.com", "KeepMe", "body1"), "INBOX", workID, "UNREAD")
	dropID := g.api.AddMessage(coretest.Msg("bob@example.com", "alice@gmail.com", "DropMe", "body2"), "INBOX", "UNREAD")
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	keptBefore, err := e.Svc.Search(t.Context(), acc.ID, "KeepMe", 10)
	require.NoError(t, err)
	require.Len(t, keptBefore, 1)

	// Mutate server state and expire historyId (forcing 404 -> FolderSnapshot).
	g.api.ModifyLabels(keepID, []string{"STARRED"}, []string{"INBOX", "UNREAD"})
	g.api.DeleteMessage(dropID)
	g.api.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@gmail.com", "FreshAfter404", "body3"))
	g.api.ExpireHistory()

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	keptAfter, err := e.Svc.Search(t.Context(), acc.ID, "KeepMe", 10)
	require.NoError(t, err)
	require.Len(t, keptAfter, 1)
	assert.Equal(t, keptBefore[0].ID, keptAfter[0].ID, "local message ID preserved across 404 snapshot reconciliation")
	assert.Equal(t, 0, gmailFolderByName(t, e, acc.ID, "INBOX").Total-1, "only FreshAfter404 is in INBOX")
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "Work").Total)

	dropped, err := e.Svc.Search(t.Context(), acc.ID, "DropMe", 10)
	require.NoError(t, err)
	assert.Empty(t, dropped)

	fresh, err := e.Svc.Search(t.Context(), acc.ID, "FreshAfter404", 10)
	require.NoError(t, err)
	assert.Len(t, fresh, 1)
}

// Sending from a Gmail API account delivers through users.messages.send
// (including Bcc) and relies on Gmail's own SENT copy (SavesSent == true).
func TestGmailAPISendAndAttachment(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc, g := addGmailAPIAccount(t, e, "alice@gmail.com")
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To = []string{"dave@example.com"}
	d.Bcc = []string{"secret@example.com"}
	d.Subject = "Sent via Gmail API"
	d.HTML = "<p>hello from gmailapi</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	require.Eventually(t, func() bool { return len(g.api.Accepted()) == 1 }, 5*time.Second, 20*time.Millisecond)
	got := g.api.Accepted()[0]
	assert.ElementsMatch(t, []string{"dave@example.com", "secret@example.com"}, got.Rcpts)

	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	assert.Equal(t, 1, gmailFolderByName(t, e, acc.ID, "SENT").Total, "only the server's single SENT copy exists")

	// Verify attachment download on demand via format=raw + opaque PartRef.
	g.api.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@gmail.com", "WithAttachment", "attached",
		coretest.Attachment("note.txt", "text/plain", []byte("attachment payload"))))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	ms, err := e.Svc.Search(t.Context(), acc.ID, "WithAttachment", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	full, err := e.Svc.GetMessageText(t.Context(), ms[0].ID)
	require.NoError(t, err)
	require.Len(t, full.Attachments, 1)
	path, _, err := e.Svc.GetAttachment(t.Context(), full.Attachments[0].ID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "attachment payload", string(data))
}
