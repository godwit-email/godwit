package coretest_test

import (
	"bytes"
	"net"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/internal/testoauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setGraph(t *testing.T, e *coretest.Env, on bool) {
	t.Helper()
	st, err := e.Svc.GetSettings(t.Context())
	require.NoError(t, err)
	st.ExperimentalGraph = on
	_, err = e.Svc.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

const outlookTestScopes = "openid email offline_access https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send"

func setupOutlookOAuth(t *testing.T) *testoauth.Server {
	t.Helper()
	oa := testoauth.Start(t)
	core.SetOAuthConfigForTest(t, func(provider string) (auth.OAuthConfig, error) {
		p, ok := auth.ByID(provider)
		if !ok || p.OAuth == nil {
			return auth.OAuthConfig{}, auth.ErrOAuthNotConfigured
		}
		cfg := oa.Config(append(slices.Clone(p.OAuth.Scopes), "offline_access")...)
		cfg.TokenParams = map[string]string{"scope": outlookTestScopes}
		cfg.GraphSend = p.OAuth.GraphSend
		cfg.GraphRead = p.OAuth.GraphRead
		return cfg, nil
	})
	return oa
}

func completeOAuth(t *testing.T, svc *core.Service, oa *testoauth.Server, email string) string {
	t.Helper()
	flowID, authURL, err := svc.BeginOAuth(t.Context(), "outlook", email, core.OAuthClient{})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := svc.AwaitOAuth(t.Context(), flowID)
		done <- err
	}()
	oa.Browse(t, authURL, email)
	require.NoError(t, <-done)
	return flowID
}

func graphInput(t *testing.T, srv *testmail.Graph, email, flowID string) core.AccountInput {
	t.Helper()
	u, err := url.Parse(srv.URL())
	require.NoError(t, err)
	host, portStr, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	slot := core.Server{
		Protocol: auth.Graph,
		Host:     host,
		Port:     port,
		Security: core.SecurityNone,
		Path:     u.Path,
	}
	return core.AccountInput{
		Email:       email,
		Provider:    "outlook",
		OAuthFlowID: flowID,
		Receiving:   slot,
	}
}

func graphFolderID(t *testing.T, e *coretest.Env, accountID int64, role string) int64 {
	t.Helper()
	fs, err := e.Svc.ListFolders(t.Context(), accountID)
	require.NoError(t, err)
	i := slices.IndexFunc(fs, func(f core.Folder) bool { return f.Role == role })
	require.GreaterOrEqual(t, i, 0, "no folder with role %q in %+v", role, fs)
	return fs[i].ID
}

// End-to-end Microsoft Graph account lifecycle: gated by ExperimentalGraph,
// initial sync + folder role mapping, mark read writeback, move across folders
// with stable ImmutableId, incremental new mail arrival, and sending via Graph
// without IMAP or SMTP.
func TestGraphEndToEnd(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	oa := setupOutlookOAuth(t)
	oa.GrantScope("Mail.ReadWrite Mail.Send offline_access")

	srv := testmail.StartGraph(t)
	srv.SetTokenValidator(oa.ValidAccess)
	firstID := srv.Deliver("INBOX", coretest.Msg("bob@example.com", "alice@outlook.com", "Welcome Graph", "hello over graph", coretest.MessageID("g1@example.com")))

	flow1 := completeOAuth(t, e.Svc, oa, "alice@outlook.com")
	in := graphInput(t, srv, "alice@outlook.com", flow1)

	// Refused while ExperimentalGraph is off.
	_, err := e.Svc.AddAccount(t.Context(), in)
	require.ErrorIs(t, err, core.ErrGraphOff)

	setGraph(t, e, true)
	r, err := e.Svc.CheckAccount(t.Context(), in)
	require.NoError(t, err)
	require.True(t, r.OK(), "%+v", r.Steps)
	require.Len(t, r.Steps, 2, "Graph account check has settings + graph only, no smtp step")
	assert.Equal(t, "graph", r.Steps[1].Name)

	acc, err := e.Svc.AddAccount(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, auth.Graph, acc.Receiving.Protocol)
	assert.Equal(t, auth.Graph, acc.Sending.Protocol)

	// Existing account cannot switch receiving protocols in place.
	swap := core.ServerSettings{
		Receiving: core.Server{Protocol: auth.IMAP, Host: "127.0.0.1", Port: 993, Security: core.SecurityNone},
		Sending:   core.Server{Protocol: auth.SMTP, Host: "127.0.0.1", Port: 587, Security: core.SecurityNone},
	}
	_, err = e.Svc.UpdateAccount(t.Context(), acc.ID, core.AccountPatch{Servers: &swap})
	require.ErrorContains(t, err, "receiving protocol cannot change")

	// Turning ExperimentalGraph off leaves already-added Graph accounts working.
	setGraph(t, e, false)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	fs, err := e.Svc.ListFolders(t.Context(), acc.ID)
	require.NoError(t, err)
	roles := map[string]string{}
	for _, f := range fs {
		roles[f.Name] = f.Role
	}
	assert.Equal(t, map[string]string{
		"Inbox":         "inbox",
		"Sent Items":    "sent",
		"Drafts":        "drafts",
		"Deleted Items": "trash",
		"Junk Email":    "junk",
		"Archive":       "archive",
	}, roles)

	ms, err := e.Svc.Search(t.Context(), acc.ID, "Welcome", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	msgID := ms[0].ID
	txt, err := e.Svc.GetMessageText(t.Context(), msgID)
	require.NoError(t, err)
	assert.Contains(t, txt.Text, "hello over graph")

	// Mark read writes back PATCH isRead=true.
	require.NoError(t, e.Svc.SetFlags(t.Context(), []int64{msgID}, []string{`\Seen`}, nil))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	_, read, _, _, ok := srv.MessageState(firstID)
	require.True(t, ok)
	assert.True(t, read, "isRead written back to Graph")

	// Move to Archive writes back /move and preserves stable local message ID.
	archiveFolderID := graphFolderID(t, e, acc.ID, "archive")
	require.NoError(t, e.Svc.Move(t.Context(), []int64{msgID}, archiveFolderID))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	folderRef, _, _, _, ok := srv.MessageState(firstID)
	require.True(t, ok)
	assert.Equal(t, srv.Folder("Archive"), folderRef)
	msAfterMove, err := e.Svc.Search(t.Context(), acc.ID, "Welcome", 10)
	require.NoError(t, err)
	require.Len(t, msAfterMove, 1)
	assert.Equal(t, msgID, msAfterMove[0].ID, "ImmutableId keeps local message ID stable across moves")

	// Receive new mail via incremental delta sync.
	srv.Deliver("INBOX", coretest.Msg("carol@example.com", "alice@outlook.com", "Second Arrival", "delta mail", coretest.MessageID("g2@example.com")))
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	msNew, err := e.Svc.Search(t.Context(), acc.ID, "Second Arrival", 10)
	require.NoError(t, err)
	require.Len(t, msNew, 1)

	// Send mail via Graph sendMail (auto-saved to Sent Items without SMTP).
	d, err := e.Svc.NewDraft(t.Context(), acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Bcc, d.Subject, d.HTML = []string{"dave@example.com"}, []string{"secret@example.com"}, "Sent via Graph", "<p>from graph</p>"
	_, err = e.Svc.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(t.Context(), d.ID))
	e.AdvanceClock(core.UndoSendWindow)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	require.Len(t, srv.Sent(), 1)
	assert.Contains(t, string(srv.Sent()[0].Raw), "secret@example.com", "Graph receives Bcc in submitted MIME")

	require.Eventually(t, func() bool {
		require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
		msSent, err := e.Svc.Search(t.Context(), acc.ID, "Sent via Graph", 10)
		return err == nil && len(msSent) == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.Zero(t, srv.NonImmutableRequests(), "every Graph request carried Prefer: IdType=\"ImmutableId\"")
}

// Existing IMAP Outlook accounts without Mail.ReadWrite consent keep syncing
// over IMAP undisturbed, while a Graph account missing Mail.ReadWrite consent
// enters reauth_required cleanly and recovers on Reauthorize.
func TestGraphMissingConsentEntersReauthAndExistingIMAPKeepsWorking(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	oa := setupOutlookOAuth(t)

	// 1. Existing Outlook account on IMAP with only outlook.office.com scopes (no Mail.ReadWrite).
	im := testmail.StartIMAP(t, testmail.WithXOAuth2Func(oa.ValidAccess))
	smtpAddr, _ := testmail.StartSMTP(t, testmail.WithXOAuth2Func(oa.ValidAccess))
	ih, ipStr, _ := net.SplitHostPort(im.Addr)
	ip, _ := strconv.Atoi(ipStr)
	sh, spStr, _ := net.SplitHostPort(smtpAddr)
	sp, _ := strconv.Atoi(spStr)

	oa.GrantScope(outlookTestScopes)
	imapFlow := completeOAuth(t, e.Svc, oa, "alice@example.com")
	imapAcc, err := e.Svc.AddAccount(t.Context(), core.AccountInput{
		Email:       "alice@example.com",
		Provider:    "outlook",
		OAuthFlowID: imapFlow,
		Receiving:   core.Server{Protocol: auth.IMAP, Host: ih, Port: ip, Security: core.SecurityNone},
		Sending:     core.Server{Protocol: auth.SMTP, Host: sh, Port: sp, Security: core.SecurityNone},
	})
	require.NoError(t, err)
	oa.RevokeAccess() // force refresh on next sync: refresh still asks only for outlookTestScopes
	rawIMAP := coretest.Msg("bob@example.com", "alice@example.com", "Legacy IMAP", "still works")
	_, err = im.User.Append("INBOX", bytes.NewReader(rawIMAP), &imap.AppendOptions{})
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), imapAcc.ID))
	imapMsgs, err := e.Svc.Search(t.Context(), imapAcc.ID, "Legacy IMAP", 10)
	require.NoError(t, err)
	require.Len(t, imapMsgs, 1)

	// 2. Graph account whose grant loses Mail.ReadWrite consent transitions to reauth_required,
	// and recovers after Reauthorize grants Mail.ReadWrite again.
	setGraph(t, e, true)
	srv := testmail.StartGraph(t)
	srv.SetTokenValidator(oa.ValidAccess)
	oa.GrantScope("Mail.ReadWrite Mail.Send offline_access")
	graphFlow := completeOAuth(t, e.Svc, oa, "graphuser@outlook.com")
	graphAcc, err := e.Svc.AddAccount(t.Context(), graphInput(t, srv, "graphuser@outlook.com", graphFlow))
	require.NoError(t, err)

	// Simulate missing Mail.ReadWrite consent on refresh.
	oa.RevokeAccess()
	oa.GrantScope(outlookTestScopes)
	err = e.Svc.SyncNow(t.Context(), graphAcc.ID)
	require.ErrorIs(t, err, backend.ErrAuthFailed)

	accs, err := e.Svc.ListAccounts(t.Context())
	require.NoError(t, err)
	idx := slices.IndexFunc(accs, func(a core.Account) bool { return a.ID == graphAcc.ID })
	require.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, "reauth_required", accs[idx].Status)

	// Reauthorize with Mail.ReadWrite consent restores status=ok and syncs cleanly.
	oa.GrantScope("Mail.ReadWrite Mail.Send offline_access")
	reauthFlow := completeOAuth(t, e.Svc, oa, "graphuser@outlook.com")
	require.NoError(t, e.Svc.Reauthorize(t.Context(), graphAcc.ID, reauthFlow))
	srv.Deliver("INBOX", coretest.Msg("bob@example.com", "graphuser@outlook.com", "Recovered", "after reauth"))
	require.NoError(t, e.Svc.SyncNow(t.Context(), graphAcc.ID))

	accs, err = e.Svc.ListAccounts(t.Context())
	require.NoError(t, err)
	idx = slices.IndexFunc(accs, func(a core.Account) bool { return a.ID == graphAcc.ID })
	assert.Equal(t, "ok", accs[idx].Status)
	recMsgs, err := e.Svc.Search(t.Context(), graphAcc.ID, "Recovered", 10)
	require.NoError(t, err)
	require.Len(t, recMsgs, 1)
}

// If another client moves a cached message from a not-yet-polled folder
// (Archive, fld-6) into an already-polled folder (Inbox, fld-1) mid-round,
// reconcileLost re-attaches the message via req.Local.Lost before dropOrphans
// runs, preserving its local row ID and local-only labels.
func TestGraphMidSyncMovePreservesMessageAndLocalLabels(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	setGraph(t, e, true)
	oa := setupOutlookOAuth(t)
	oa.GrantScope("Mail.ReadWrite Mail.Send offline_access")

	srv := testmail.StartGraph(t)
	srv.SetTokenValidator(oa.ValidAccess)
	immID := srv.Deliver("Archive", coretest.Msg("bob@example.com", "alice@outlook.com", "Mid-round Move", "keep local state", coretest.MessageID("mid@example.com")))

	flow := completeOAuth(t, e.Svc, oa, "alice@outlook.com")
	acc, err := e.Svc.AddAccount(t.Context(), graphInput(t, srv, "alice@outlook.com", flow))
	require.NoError(t, err)
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))

	ms, err := e.Svc.Search(t.Context(), acc.ID, "Mid-round Move", 10)
	require.NoError(t, err)
	require.Len(t, ms, 1)
	origLocalID := ms[0].ID

	lbl, err := e.Svc.CreateLabel(t.Context(), core.LabelInput{
		Name:  "KeepLocal",
		Color: core.LabelColors[0],
		Icon:  core.LabelIcons[0],
	})
	require.NoError(t, err)
	require.NoError(t, e.Svc.SetLabel(t.Context(), []int64{origLocalID}, lbl.ID, true))

	// Right after Inbox (fld-1) finishes its delta poll — before Archive (fld-6)
	// is polled — another client moves the message from Archive into Inbox.
	movedOnce := false
	srv.SetAfterFolderDelta(func(folderName string) {
		if folderName == "Inbox" && !movedOnce {
			movedOnce = true
			srv.MoveMessage(immID, "INBOX")
		}
	})
	require.NoError(t, e.Svc.SyncNow(t.Context(), acc.ID))
	srv.SetAfterFolderDelta(nil)
	require.True(t, movedOnce)

	msAfter, err := e.Svc.Search(t.Context(), acc.ID, "Mid-round Move", 10)
	require.NoError(t, err)
	require.Len(t, msAfter, 1)
	assert.Equal(t, origLocalID, msAfter[0].ID, "local message row preserved rather than dropped and re-imported")

	inboxID := graphFolderID(t, e, acc.ID, "inbox")
	inboxPage, err := e.Svc.ListMessages(t.Context(), core.View{AccountID: acc.ID, FolderID: inboxID}, "", 10)
	require.NoError(t, err)
	require.Len(t, inboxPage.Items, 1)
	assert.Equal(t, origLocalID, inboxPage.Items[0].ID, "message reconciled into Inbox in the same round")

	applied, err := e.Svc.MessageLabels(t.Context(), []int64{origLocalID})
	require.NoError(t, err)
	require.Len(t, applied[origLocalID], 1)
	assert.Equal(t, lbl.ID, applied[origLocalID][0].LabelID, "local-only label preserved")
}
