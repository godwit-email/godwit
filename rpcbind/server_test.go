package rpcbind_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	coretest.ServeMCPIfChild()
	os.Exit(m.Run())
}

// listen serves svc on a fresh short directory (Unix socket paths are short).
func listen(t *testing.T, svc *core.Service) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := rpcbind.Listen(dir, svc, "test")
	require.NoError(t, err)
	go s.Serve(context.Background())
	t.Cleanup(func() { s.Close() })
	return dir
}

func dial(t *testing.T, dir, role string) *rpc.Client {
	t.Helper()
	c, err := rpc.Dial(dir, role, "rpcbind-test", nil) // no codec: errors stay *rpc.WireError
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

func auditRows(t *testing.T, dataDir string) [][]any {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dataDir, "godwit.db"))
	require.NoError(t, err)
	defer db.Close()
	r, err := db.Query(context.Background(), "SELECT source, tool, target_id, COALESCE(extension, '') || COALESCE(destination, '') || COALESCE(reason, '') FROM audit_log WHERE source IN ('local_api', 'local_mcp', 'ui') ORDER BY id")
	require.NoError(t, err)
	return r.Values
}

func cacheFiles(dataDir string) int {
	n := 0
	filepath.WalkDir(filepath.Join(dataDir, "cache"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// The mcp role's whole surface, compared with every method of *core.Service.
func TestMCPAllowlistContents(t *testing.T) {
	want := []string{"GetDraft", "GetMessageText", "GetThread", "ListAccounts", "ListDrafts", "ListFolders",
		"ListMessages", "ListThreads", "NewDraft", "RequestMCPSend", "SaveDraft", "Search"}
	newMode := []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"new"`), json.RawMessage(`0`)}
	var got []string
	for m := range reflect.TypeFor[*core.Service]().Methods() {
		if rpc.MCPAllowed(m.Name, newMode) {
			got = append(got, m.Name)
		}
	}
	sort.Strings(got)
	assert.Equal(t, want, got)
	for _, m := range want {
		assert.True(t, rpcbind.Expose(m), "%s must be callable at all", m)
	}
	assert.False(t, rpc.MCPAllowed("NewDraft", []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"forward"`), json.RawMessage(`2`)}))
	assert.True(t, rpcbind.Expose("CancelOutbox"), "full may undo a send")
	// An injected agent must not make the user's machine POST anywhere (#218); full may.
	for _, m := range []string{"Unsubscribe", "UnsubscribeMethod", "NewMailtoDraft"} {
		assert.False(t, rpc.MCPAllowed(m, []json.RawMessage{json.RawMessage(`1`)}), m)
		assert.True(t, rpcbind.Expose(m), m)
	}
}

func TestMCPRoleAllowlist(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	l := e.LLM(t)
	acc := e.AddAccount(t, "alice@example.com")
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Plans", "Meet on Friday.",
		coretest.Attachment("a.txt", "text/plain", []byte("abc"))))
	page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	msg := page.Items[0]
	text, err := e.Svc.GetMessageText(ctx, msg.ID)
	require.NoError(t, err)
	require.Len(t, text.Attachments, 1)
	folders, err := e.Svc.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	var archive int64
	for _, f := range folders {
		if f.ID != msg.FolderID {
			archive = f.ID
		}
	}
	require.NotZero(t, archive)
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "out", "<p>hi</p>"
	d, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	q, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	q.To, q.Subject, q.HTML = []string{"dave@example.com"}, "queued", "<p>hi</p>"
	q, err = e.Svc.SaveDraft(ctx, q)
	require.NoError(t, err)
	require.NoError(t, e.Svc.Send(ctx, q.ID)) // in its undo window
	queued := must(t)(e.Svc.ListOutbox(ctx, acc.ID)).([]core.OutboxItem)
	require.Len(t, queued, 1)
	e.InstallPlugin(t, "config-demo")
	services, err := e.Svc.ListModelServices(ctx)
	require.NoError(t, err)
	require.Len(t, services, 1)
	conv, err := e.Svc.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	l.Script(coretest.Turn{Calls: []coretest.Call{{ID: "c1", Name: "create_draft", Args: `{"to":["x@example.com"],"subject":"s","html":"<p>x</p>"}`}}})
	evs, err := e.Svc.Ask(ctx, conv.ID, "draft one")
	require.NoError(t, err)
	for range evs {
	}
	approvals, err := e.Svc.ListApprovals(ctx, conv.ID)
	require.NoError(t, err)
	require.Len(t, approvals, 1)
	require.Equal(t, "pending", approvals[0].State)

	state := func() string {
		snap := map[string]any{"sent": len(e.Sent(acc.ID)), "cache": cacheFiles(e.DataDir),
			"outbox":       must(t)(e.Svc.ListOutbox(ctx, acc.ID)),
			"messages":     must(t)(e.Svc.ListMessages(ctx, core.View{FolderID: msg.FolderID}, "", 0)),
			"archive":      must(t)(e.Svc.ListMessages(ctx, core.View{FolderID: archive}, "", 0)),
			"settings":     must(t)(e.Svc.GetSettings(ctx)),
			"approvals":    must(t)(e.Svc.ListApprovals(ctx, conv.ID)),
			"plugins":      must(t)(e.Svc.ListPlugins(ctx)),
			"pluginConfig": must(t)(e.Svc.GetPluginConfig(ctx, "config-demo")),
			"accounts":     must(t)(e.Svc.ListAccounts(ctx)),
			"models":       must(t)(e.Svc.ListModelServices(ctx)),
			"drafts":       must(t)(e.Svc.ListDrafts(ctx, acc.ID)),
		}
		b, err := json.Marshal(snap)
		require.NoError(t, err)
		return string(b)
	}

	settings, err := e.Svc.GetSettings(ctx)
	require.NoError(t, err)
	settings.Theme = "dark"
	c := dial(t, listen(t, e.Svc), rpc.RoleMCP)
	for _, call := range []struct {
		method string
		args   []any
	}{
		{"Send", []any{d.ID}},
		{"SendAcknowledged", []any{d.ID, "digest"}},
		{"Delete", []any{[]int64{msg.ID}}},
		{"Move", []any{[]int64{msg.ID}, archive}},
		{"SetFlags", []any{[]int64{msg.ID}, []string{`\Seen`, `\Flagged`}, []string{}}},
		{"SetSettings", []any{settings}},
		{"ResolveApproval", []any{approvals[0].ID, true}},
		{"ResolveMCPSend", []any{1, true}},
		{"CancelOutbox", []any{queued[0].ID}},
		{"PendingMCPSends", nil},
		{"InstallPlugin", []any{coretest.PluginFixture(t, "card-demo")}},
		{"AddAccount", []any{core.AccountInput{Email: "eve@example.com", Username: "u", Password: "p", Receiving: core.Server{Host: "127.0.0.1", Port: 1}, Sending: core.Server{Host: "127.0.0.1", Port: 1}}}},
		{"ExportAccounts", nil},
		{"PreviewAccountImport", []any{[]byte(`{"version":1,"accounts":[]}`)}},
		{"ImportAccounts", []any{[]byte(`{"version":1,"accounts":[]}`)}},
		{"RemoveModelService", []any{services[0].ID}},
		{"SetPluginConfig", []any{"config-demo", map[string]any{"plan": "pro"}}},
		{"GetMessage", []any{msg.ID, true}},
		{"GetAttachment", []any{text.Attachments[0].ID}},
		{"NewDraft", []any{acc.ID, "forward", msg.ID}},
		{"Unsubscribe", []any{msg.ID}},
		{"UnsubscribeMethod", []any{msg.ID}},
		{"NewMailtoDraft", []any{acc.ID, "mailto:eve@example.com"}},
	} {
		before := state()
		err := c.Call(ctx, call.method, nil, call.args...)
		var we *rpc.WireError
		if assert.ErrorAs(t, err, &we, call.method) {
			assert.Equal(t, rpc.CodeForbidden, we.Code, call.method)
		}
		assert.Equal(t, before, state(), "%s changed something", call.method)
	}

	// The connection still works for what the role allows.
	var got core.MessageText
	require.NoError(t, c.Call(ctx, "GetMessageText", &got, msg.ID))
	assert.Contains(t, got.Text, "Meet on Friday.")
	assert.Zero(t, cacheFiles(e.DataDir))
}

// must(t)(f()) is f's value, failing t on its error.
func must(t *testing.T) func(v any, err error) any {
	return func(v any, err error) any {
		t.Helper()
		require.NoError(t, err)
		return v
	}
}

// Reading through the mcp role downloads nothing: no IMAP traffic for the
// message's attachment or inline image, nothing in the cache.
func TestMCPReadDownloadsNothing(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Chart", "see the chart",
		coretest.HTML(`<p>see <img src="cid:c1"></p>`), coretest.Attachment("r.pdf", "application/pdf", []byte("%PDF-1.4"))))
	page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	c := dial(t, listen(t, e.Svc), rpc.RoleMCP)

	before := e.IMAP(acc.ID).BytesSent()
	var m core.MessageText
	require.NoError(t, c.Call(ctx, "GetMessageText", &m, page.Items[0].ID))
	var th core.Thread
	require.NoError(t, c.Call(ctx, "GetThread", &th, page.Items[0].ThreadID))
	assert.Equal(t, before, e.IMAP(acc.ID).BytesSent(), "no IMAP traffic")
	assert.Zero(t, cacheFiles(e.DataDir))
	assert.Equal(t, "see", m.Text, "the HTML the reader shows, not the plain part")
	require.Len(t, m.Attachments, 1)
	assert.Equal(t, "r.pdf", m.Attachments[0].Filename)
}

// full's Send and mcp's SaveDraft each write one audit row with the method
// and target, and no content.
func TestAuditExternal(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	dir := listen(t, e.Svc)
	full, mcp := dial(t, dir, rpc.RoleFull), dial(t, dir, rpc.RoleMCP)

	var d core.Draft
	require.NoError(t, mcp.Call(ctx, "NewDraft", &d, acc.ID, "new", 0))
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "quarterly secret plan", "<p>the body text</p>"
	require.NoError(t, mcp.Call(ctx, "SaveDraft", &d, d))
	require.NoError(t, mcp.Call(ctx, "ListDrafts", nil, acc.ID)) // reads are not audited
	require.NoError(t, full.Call(ctx, "Send", nil, d.ID))
	require.NoError(t, full.Call(ctx, "ListAccounts", nil))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hi", "No list here."))
	page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	msg := page.Items[0].ID
	require.NoError(t, full.Call(ctx, "UnsubscribeMethod", nil, msg))
	assert.ErrorContains(t, full.Call(ctx, "Unsubscribe", nil, msg), core.ErrNoUnsubscribe.Error()) // audited all the same

	assert.Error(t, full.Call(ctx, "BeginPluginLogin", nil, "absent")) // audited all the same: the plugin may reach its hosts
	assert.Error(t, mcp.Call(ctx, "BeginPluginLogin", nil, "absent"))

	rows := auditRows(t, e.DataDir)
	assert.Equal(t, [][]any{
		{"local_mcp", "NewDraft", nil, ""},
		{"local_mcp", "SaveDraft", d.ID, ""},
		{"local_api", "Send", d.ID, ""},
		{"local_api", "Unsubscribe", msg, ""},
		{"local_api", "BeginPluginLogin", nil, ""},
	}, rows)
	b, _ := json.Marshal(rows)
	assert.NotContains(t, string(b), "secret plan")
	assert.NotContains(t, string(b), "body text")
}

// SendRevision sends as Send does, so it is audited as Send is.
func TestAuditSendRevision(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	full := dial(t, listen(t, e.Svc), rpc.RoleFull)
	var d core.Draft
	require.NoError(t, full.Call(ctx, "NewDraft", &d, acc.ID, "new", 0))
	d.To, d.Subject = []string{"dave@example.com"}, "hi"
	require.NoError(t, full.Call(ctx, "SaveDraft", &d, d))
	require.NoError(t, full.Call(ctx, "SendRevision", nil, d.ID, d.Revision))
	assert.Equal(t, [][]any{{"local_api", "SendRevision", d.ID, ""}}, auditRows(t, e.DataDir))
}

func TestDialFull(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	dir := listen(t, e.Svc)
	c, err := rpc.Dial(dir, rpc.RoleFull, "rpcbind-test", rpcbind.NewCodec())
	require.NoError(t, err)
	defer c.Close()
	var accs []core.Account
	require.NoError(t, c.Call(t.Context(), "ListAccounts", &accs))
	require.Len(t, accs, 1)
	assert.Equal(t, acc.ID, accs[0].ID)
	// Errors decode through the codec; AuditExternal is not on the wire.
	err = c.Call(t.Context(), "GetPluginConfig", nil, "nope")
	assert.ErrorIs(t, err, core.ErrPluginNotFound)
	var we *rpc.RemoteError
	require.ErrorAs(t, c.Call(t.Context(), "AuditExternal", nil, "mcp", "x", 1), &we)
	assert.Equal(t, rpc.CodeMethodNotFound, we.Code)
}

// Over the socket, full reaches exactly the pinned methods: each one gets past
// the method lookup (too many params, so none runs) and every other method of
// *core.Service is not found.
func TestFullSurfaceOverSocket(t *testing.T) {
	c := dial(t, listen(t, newService(t)), rpc.RoleFull)
	pinned := map[string]bool{}
	for _, m := range fullSurface(t) {
		pinned[m] = true
	}
	tooMany := make([]any, 16)
	for m := range reflect.TypeFor[*core.Service]().Methods() {
		var we *rpc.WireError
		require.ErrorAs(t, c.Call(t.Context(), m.Name, nil, tooMany...), &we, m.Name)
		want := rpc.CodeMethodNotFound
		if pinned[m.Name] {
			want = rpc.CodeInvalidParams
		}
		assert.Equal(t, want, we.Code, m.Name)
	}
}

func TestUISurfaceOverSocket(t *testing.T) {
	dir := listen(t, newService(t))
	ui := dial(t, dir, rpc.RoleUI)
	full := dial(t, dir, rpc.RoleFull)
	mcp := dial(t, dir, rpc.RoleMCP)

	allowed := map[string]bool{}
	for _, m := range fullSurface(t) {
		allowed[m] = true
	}
	uiOnly := uiSurface(t)
	for _, m := range uiOnly {
		allowed[m] = true
	}

	tooMany := make([]any, 16)
	for m := range reflect.TypeFor[*core.Service]().Methods() {
		var we *rpc.WireError
		require.ErrorAs(t, ui.Call(t.Context(), m.Name, nil, tooMany...), &we, m.Name)
		want := rpc.CodeMethodNotFound
		if allowed[m.Name] {
			want = rpc.CodeInvalidParams
		}
		assert.Equal(t, want, we.Code, m.Name)
	}

	for _, name := range uiOnly {
		var fwe *rpc.WireError
		require.ErrorAs(t, full.Call(t.Context(), name, nil, tooMany...), &fwe, name)
		assert.Equal(t, rpc.CodeMethodNotFound, fwe.Code, "full on %s", name)

		var mwe *rpc.WireError
		require.ErrorAs(t, mcp.Call(t.Context(), name, nil), &mwe, name)
		assert.Equal(t, rpc.CodeForbidden, mwe.Code, "mcp on %s", name)
	}
}

// A same-user program can present ui-token (ADR 0002), so RoleUI's sends, rule
// forwards, grants and MCPSend widening are audited as full's are, under the
// ui source; its other calls, drafts included, are not.
func TestUIRoleAudited(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	ui := dial(t, listen(t, e.Svc), rpc.RoleUI)

	require.NoError(t, ui.Call(ctx, "SetUIForeground", nil, true))
	require.NoError(t, ui.Call(ctx, "SetComposeOpen", nil, []int64{1}))
	require.NoError(t, ui.Call(ctx, "SetInboxOnly", nil, true))
	var inboxOnly bool
	require.NoError(t, ui.Call(ctx, "InboxOnly", &inboxOnly))
	assert.True(t, inboxOnly)

	var d core.Draft
	require.NoError(t, ui.Call(ctx, "NewDraft", &d, acc.ID, "new", 0))
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "hi", "<p>hi</p>"
	require.NoError(t, ui.Call(ctx, "SaveDraft", &d, d))
	require.NoError(t, ui.Call(ctx, "Send", nil, d.ID))

	var st core.Settings
	require.NoError(t, ui.Call(ctx, "GetSettings", &st))
	st.MCPSend = core.MCPSendOff
	require.NoError(t, ui.Call(ctx, "SetSettings", &st, st))
	st.MCPSend = core.MCPSendDirect
	require.NoError(t, ui.Call(ctx, "SetSettings", &st, st))

	fwd := core.Rule{
		Enabled: true,
		Doc: core.RuleDoc{
			Version:    core.RuleVersion,
			Name:       "R",
			Trigger:    core.TriggerReceive,
			Match:      core.MatchAll,
			Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "billing@"}},
			Actions:    []core.RuleAction{{Kind: core.ActionForward, To: "carol@example.org"}},
		},
		Confirmed: []string{"carol@example.org"},
	}
	var fwdID int64
	require.NoError(t, ui.Call(ctx, "SaveRule", &fwdID, fwd))
	require.NoError(t, ui.Call(ctx, "InstallPlugin", nil, coretest.PluginFixture(t, "card-demo")))
	require.NoError(t, ui.Call(ctx, "GrantPermission", nil, "card-demo"))

	cmd, args, env := coretest.MCPCommand(t)
	var srv core.MCPServer
	require.NoError(t, ui.Call(ctx, "AddMCPServer", &srv, core.MCPServerInput{Name: "fake", Transport: "stdio", Command: cmd, Args: args, Env: env}))
	require.NoError(t, ui.Call(ctx, "GrantMCPServer", nil, srv.ID))
	require.NoError(t, ui.Call(ctx, "SetMCPServerEnabled", nil, srv.ID, true))
	require.NoError(t, ui.Call(ctx, "SetMCPToolTrusted", nil, srv.ID, "lookup", true))

	assert.Equal(t, [][]any{
		{"ui", "Send", d.ID, ""},
		{"ui", "SetSettings", nil, ""},
		{"ui", "SaveRule", nil, "example.org"},
		{"ui", "GrantPermission", nil, "card-demo"},
		{"ui", "GrantMCPServer", srv.ID, ""},
		{"ui", "SetMCPToolTrusted", srv.ID, "lookup"},
	}, auditRows(t, e.DataDir))
}

// logs captures slog output until the test ends.
func logs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var b strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return b.Write(p)
	}), nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() string { mu.Lock(); defer mu.Unlock(); return b.String() }
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The current API token is redacted like any secret: an mcp SaveDraft whose
// Mode is the token gets an error without it, and the log has none.
func TestTokenNotLogged(t *testing.T) {
	logged := logs(t)
	e := coretest.New(t, coretest.WithoutAutoSync())
	acc := e.AddAccount(t, "alice@example.com")
	dir := listen(t, e.Svc)
	tok, err := os.ReadFile(filepath.Join(dir, rpc.TokenName))
	require.NoError(t, err)
	c := dial(t, dir, rpc.RoleMCP)
	err = c.Call(t.Context(), "SaveDraft", nil, core.Draft{AccountID: acc.ID, Mode: string(tok)})
	var we *rpc.WireError
	require.ErrorAs(t, err, &we)
	b, _ := json.Marshal(we)
	assert.NotContains(t, string(b), string(tok))
	assert.Contains(t, logged(), "unknown draft mode")
	assert.NotContains(t, logged(), string(tok))
}

// An audit that cannot be written stops the call: "internal error" on the
// wire, the redacted cause in the log, nothing sent.
func TestAuditFailureStopsCall(t *testing.T) {
	logged := logs(t)
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"dave@example.com"}, "out", "<p>hi</p>"
	_, err = e.Svc.SaveDraft(ctx, d)
	require.NoError(t, err)
	db, err := store.Open(ctx, filepath.Join(e.DataDir, "godwit.db"))
	require.NoError(t, err)
	_, err = db.Execute(ctx, "DROP TABLE audit_log")
	require.NoError(t, err)
	db.Close()

	c := dial(t, listen(t, e.Svc), rpc.RoleFull)
	err = c.Call(ctx, "Send", nil, d.ID)
	var we *rpc.WireError
	require.ErrorAs(t, err, &we)
	assert.Equal(t, rpc.CodeInternal, we.Code)
	assert.Equal(t, "internal error", we.Message)
	assert.Contains(t, logged(), "audit_log")
	drafts, err := e.Svc.ListDrafts(ctx, acc.ID)
	require.NoError(t, err)
	assert.Len(t, drafts, 1, "the draft was not sent")
	outbox, err := e.Svc.ListOutbox(ctx, acc.ID)
	require.NoError(t, err)
	assert.Empty(t, outbox)
	assert.Empty(t, e.Sent(acc.ID))
}

// failedOutbox adds an outbox item of acc that failed to send; its id.
func failedOutbox(t *testing.T, dataDir string, acc int64) int64 {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dataDir, "godwit.db"))
	require.NoError(t, err)
	defer db.Close()
	r, err := db.Execute(context.Background(), "INSERT INTO outbox(account_id, state) VALUES (?, 'failed')", acc)
	require.NoError(t, err)
	return r.LastInsertID
}

func outboxState(t *testing.T, svc *core.Service, acc, id int64) string {
	t.Helper()
	items, err := svc.ListOutbox(t.Context(), acc)
	require.NoError(t, err)
	for _, it := range items {
		if it.ID == id {
			return it.State
		}
	}
	return ""
}

// full's ResolveOutbox "resend" sends again, so it is audited like Send;
// confirm_sent and discard send nothing and are not.
func TestAuditResend(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	resend, confirm := failedOutbox(t, e.DataDir, acc.ID), failedOutbox(t, e.DataDir, acc.ID)
	full := dial(t, listen(t, e.Svc), rpc.RoleFull)
	require.NoError(t, full.Call(ctx, "ResolveOutbox", nil, confirm, "confirm_sent"))
	require.NoError(t, full.Call(ctx, "ResolveOutbox", nil, resend, "resend"))
	assert.Equal(t, [][]any{{"local_api", "ResolveOutbox", resend, ""}}, auditRows(t, e.DataDir))
}

// A rule that forwards is mail leaving later, unattended (ADR 0003): full's
// calls that save it, turn it on or rerun it are audited with its recipients'
// domains; a rule without a forward and turning one off are not, and mcp
// cannot reach any of them.
func TestAuditRuleForward(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	dir := listen(t, e.Svc)
	full, mcp := dial(t, dir, rpc.RoleFull), dial(t, dir, rpc.RoleMCP)
	doc := func(acts ...core.RuleAction) core.RuleDoc {
		return core.RuleDoc{Version: core.RuleVersion, Name: "R", Trigger: core.TriggerReceive, Match: core.MatchAll,
			Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "billing@"}}, Actions: acts}
	}
	fwd := core.Rule{Enabled: true, Doc: doc(core.RuleAction{Kind: core.ActionRead}, core.RuleAction{Kind: core.ActionForward, To: "Carol <carol@example.org>"},
		core.RuleAction{Kind: core.ActionForward, To: "billing/ops@example.net"}), Confirmed: []string{"carol@example.org", "billing/ops@example.net"}}
	local := core.Rule{Enabled: true, Doc: doc(core.RuleAction{Kind: core.ActionRead})}

	var fwdID, localID int64
	require.NoError(t, full.Call(ctx, "SaveRule", &fwdID, fwd))
	require.NoError(t, full.Call(ctx, "SaveRule", &localID, local))
	bad := core.Rule{Doc: doc(core.RuleAction{Kind: core.ActionForward, To: "Alice Smith"})}
	assert.ErrorContains(t, full.Call(ctx, "SaveRule", nil, bad), "invalid rule") // refused, and its text kept off the log
	fwd.ID = fwdID
	require.NoError(t, full.Call(ctx, "SaveRule", nil, fwd))
	require.NoError(t, full.Call(ctx, "SetRuleEnabled", nil, fwdID, false))
	require.NoError(t, full.Call(ctx, "SetRuleEnabled", nil, fwdID, true))
	require.NoError(t, full.Call(ctx, "SetRuleEnabled", nil, localID, true))
	assert.Error(t, full.Call(ctx, "RetryRuleRun", nil, 7)) // audited all the same
	require.NoError(t, full.Call(ctx, "RunRuleOnExisting", nil, fwdID))

	for _, m := range []string{"SaveRule", "SetRuleEnabled", "RetryRuleRun", "RunRuleOnExisting", "ProposedRuleSaved"} {
		var we *rpc.WireError
		require.ErrorAs(t, mcp.Call(ctx, m, nil), &we, m)
		assert.Equal(t, rpc.CodeForbidden, we.Code, m)
	}
	assert.Equal(t, [][]any{
		{"local_api", "SaveRule", nil, "example.org,example.net"}, // new: no id yet
		{"local_api", "SaveRule", fwdID, "example.org,example.net"},
		{"local_api", "SetRuleEnabled", fwdID, "example.org,example.net"},
		{"local_api", "RetryRuleRun", int64(7), ""},
	}, auditRows(t, e.DataDir))
}

// A resend whose audit cannot be written re-queues nothing.
func TestAuditResendFailureStopsCall(t *testing.T) {
	logs(t)
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	id := failedOutbox(t, e.DataDir, acc.ID)
	db, err := store.Open(ctx, filepath.Join(e.DataDir, "godwit.db"))
	require.NoError(t, err)
	_, err = db.Execute(ctx, "DROP TABLE audit_log")
	require.NoError(t, err)
	db.Close()

	err = dial(t, listen(t, e.Svc), rpc.RoleFull).Call(ctx, "ResolveOutbox", nil, id, "resend")
	var we *rpc.WireError
	require.ErrorAs(t, err, &we)
	assert.Equal(t, rpc.CodeInternal, we.Code)
	assert.Equal(t, "failed", outboxState(t, e.Svc, acc.ID, id), "not re-queued")
}

// Widening Settings.MCPSend and granting extensions or MCP tools without
// approval are persistent egress state (ADR 0003): full's calls are audited,
// while ordinary settings changes and tightening are not.
func TestAuditSendSettingsAndGrants(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	dir := listen(t, e.Svc)
	full, mcp := dial(t, dir, rpc.RoleFull), dial(t, dir, rpc.RoleMCP)

	var st core.Settings
	require.NoError(t, full.Call(ctx, "GetSettings", &st))
	st.Theme = "dark"
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // ordinary setting: not audited
	st.MCPSend = core.MCPSendOff
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // confirm -> off: tightening
	st.Theme = "light"
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // stays off: not audited
	st.MCPSend = ""
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // off -> "" (confirm): audited
	st.MCPSend = core.MCPSendDirect
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // confirm -> direct: audited
	st.Theme = "dark"
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // stays direct: not audited
	st.MCPSend = core.MCPSendConfirm
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st)) // direct -> confirm: tightening

	cmd, args, env := coretest.MCPCommand(t)
	var srv core.MCPServer
	require.NoError(t, full.Call(ctx, "AddMCPServer", &srv, core.MCPServerInput{Name: "fake", Transport: "stdio", Command: cmd, Args: args, Env: env}))
	require.NoError(t, full.Call(ctx, "GrantMCPServer", nil, srv.ID))
	require.NoError(t, full.Call(ctx, "SetMCPServerEnabled", nil, srv.ID, true))
	require.NoError(t, full.Call(ctx, "SetMCPToolTrusted", nil, srv.ID, "lookup", true))
	require.NoError(t, full.Call(ctx, "SetMCPToolTrusted", nil, srv.ID, "lookup", false)) // untrusting: tightening

	require.NoError(t, full.Call(ctx, "InstallPlugin", nil, coretest.PluginFixture(t, "card-demo")))
	require.NoError(t, full.Call(ctx, "GrantPermission", nil, "card-demo"))

	for _, m := range []string{"SetSettings", "GrantMCPServer", "SetMCPToolTrusted", "GrantPermission"} {
		var we *rpc.WireError
		require.ErrorAs(t, mcp.Call(ctx, m, nil), &we, m)
		assert.Equal(t, rpc.CodeForbidden, we.Code, m)
	}
	assert.Equal(t, [][]any{
		{"local_api", "SetSettings", nil, ""},
		{"local_api", "SetSettings", nil, ""},
		{"local_api", "GrantMCPServer", srv.ID, ""},
		{"local_api", "SetMCPToolTrusted", srv.ID, "lookup"},
		{"local_api", "GrantPermission", nil, "card-demo"},
	}, auditRows(t, e.DataDir))
}

// When audit_log cannot be written, widening MCPSend or granting an extension
// or MCP tool is refused before changing state.
func TestAuditGrantFailureStopsCall(t *testing.T) {
	logs(t)
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()

	st, err := e.Svc.GetSettings(ctx)
	require.NoError(t, err)
	st.MCPSend = core.MCPSendOff
	st, err = e.Svc.SetSettings(ctx, st)
	require.NoError(t, err)

	cmd, args, env := coretest.MCPCommand(t)
	ungranted, err := e.Svc.AddMCPServer(ctx, core.MCPServerInput{Name: "ungranted", Transport: "stdio", Command: cmd, Args: args, Env: env})
	require.NoError(t, err)
	started, err := e.Svc.AddMCPServer(ctx, core.MCPServerInput{Name: "started", Transport: "stdio", Command: cmd, Args: args, Env: env})
	require.NoError(t, err)
	require.NoError(t, e.Svc.GrantMCPServer(ctx, started.ID))
	require.NoError(t, e.Svc.SetMCPServerEnabled(ctx, started.ID, true))
	_, err = e.Svc.InstallPlugin(ctx, coretest.PluginFixture(t, "card-demo"))
	require.NoError(t, err)

	db, err := store.Open(ctx, filepath.Join(e.DataDir, "godwit.db"))
	require.NoError(t, err)
	_, err = db.Execute(ctx, "DROP TABLE audit_log")
	require.NoError(t, err)
	db.Close()

	full := dial(t, listen(t, e.Svc), rpc.RoleFull)
	st.Theme = "dark"
	require.NoError(t, full.Call(ctx, "SetSettings", &st, st), "ordinary settings still save")

	widen := st
	widen.MCPSend = core.MCPSendDirect
	for _, call := range []struct {
		method string
		args   []any
	}{
		{"SetSettings", []any{widen}},
		{"GrantMCPServer", []any{ungranted.ID}},
		{"SetMCPToolTrusted", []any{started.ID, "lookup", true}},
		{"GrantPermission", []any{"card-demo"}},
	} {
		var we *rpc.WireError
		require.ErrorAs(t, full.Call(ctx, call.method, nil, call.args...), &we, call.method)
		assert.Equal(t, rpc.CodeInternal, we.Code, call.method)
	}

	gotSt, err := e.Svc.GetSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, core.MCPSendOff, gotSt.MCPSend)

	srvs, err := e.Svc.ListMCPServers(ctx)
	require.NoError(t, err)
	require.Len(t, srvs, 2)
	assert.False(t, srvs[0].Granted)
	require.Len(t, srvs[1].Tools, 1)
	assert.False(t, srvs[1].Tools[0].Trusted)

	pls, err := e.Svc.ListPlugins(ctx)
	require.NoError(t, err)
	for _, p := range pls {
		if p.Name == "card-demo" {
			assert.True(t, p.NeedsGrant)
		}
	}
}

// Concurrent SetSettings calls decide and write the audit row under settingsMu
// in the same transaction as the save, so an off -> direct transition interleaved
// behind a direct snapshot is never left unaudited or allowed past a broken audit table.
func TestAuditSetSettingsConcurrentWidening(t *testing.T) {
	logs(t)
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	dir := listen(t, e.Svc)
	full := dial(t, dir, rpc.RoleFull)

	base, err := e.Svc.GetSettings(ctx)
	require.NoError(t, err)
	directSt, offSt := base, base
	directSt.MCPSend = core.MCPSendDirect
	offSt.MCPSend = core.MCPSendOff

	for range 30 {
		_, err = e.Svc.SetSettings(ctx, directSt)
		require.NoError(t, err)
		before := len(auditRows(t, e.DataDir))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			require.NoError(t, full.Call(ctx, "SetSettings", nil, directSt))
		}()
		go func() {
			defer wg.Done()
			_, err := e.Svc.SetSettings(ctx, offSt)
			require.NoError(t, err)
		}()
		wg.Wait()

		got, err := e.Svc.GetSettings(ctx)
		require.NoError(t, err)
		added := len(auditRows(t, e.DataDir)) - before
		if got.MCPSend == core.MCPSendDirect {
			assert.Equal(t, 1, added, "off ran first, so direct widened and must audit")
		} else {
			assert.Equal(t, core.MCPSendOff, got.MCPSend)
			assert.Zero(t, added, "direct ran first, then off tightened")
		}
	}

	_, err = e.Svc.SetSettings(ctx, directSt)
	require.NoError(t, err)
	db, err := store.Open(ctx, filepath.Join(e.DataDir, "godwit.db"))
	require.NoError(t, err)
	_, err = db.Execute(ctx, "DROP TABLE audit_log")
	require.NoError(t, err)
	db.Close()

	for range 20 {
		_, err = e.Svc.SetSettings(ctx, directSt)
		require.NoError(t, err)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = full.Call(ctx, "SetSettings", nil, directSt)
		}()
		go func() {
			defer wg.Done()
			_, err := e.Svc.SetSettings(ctx, offSt)
			require.NoError(t, err)
		}()
		wg.Wait()

		got, err := e.Svc.GetSettings(ctx)
		require.NoError(t, err)
		assert.Equal(t, core.MCPSendOff, got.MCPSend, "widening after off cannot commit without audit_log")
	}
}

func TestListenWithExternalToggle(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	dir, err := os.MkdirTemp("", "gw")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	srv, err := rpcbind.ListenWith(dir, e.Svc, rpcbind.ListenOptions{Version: "test"})
	require.NoError(t, err)
	go srv.Serve(context.Background())
	t.Cleanup(func() { srv.Close() })

	uiTokBytes, err := os.ReadFile(filepath.Join(dir, rpc.UITokenName))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, rpc.TokenName))
	require.ErrorIs(t, err, os.ErrNotExist)

	ui := dial(t, dir, rpc.RoleUI)
	require.NoError(t, ui.Call(ctx, "SetInboxOnly", nil, true))
	var inboxOnly bool
	require.NoError(t, ui.Call(ctx, "InboxOnly", &inboxOnly))
	assert.True(t, inboxOnly)
	uiEvents, uiCancel, err := ui.Stream(ctx, "Subscribe")
	require.NoError(t, err)
	defer uiCancel()

	dialRawToken := func(token, role string) error {
		conn, err := net.Dial("unix", filepath.Join(dir, rpc.SocketName))
		require.NoError(t, err)
		c := rpc.NewClient(conn)
		defer c.Close()
		return c.Call(ctx, "hello", nil, token, role, "raw-test")
	}

	for _, role := range []string{rpc.RoleFull, rpc.RoleMCP} {
		_, err := rpc.Dial(dir, role, "ext-test", nil)
		require.ErrorIs(t, err, os.ErrNotExist)
		var we *rpc.WireError
		require.ErrorAs(t, dialRawToken(string(uiTokBytes), role), &we)
		assert.Equal(t, rpc.CodeUnauthenticated, we.Code)
	}

	require.NoError(t, srv.SetExternal(true))
	apiTokBytes, err := os.ReadFile(filepath.Join(dir, rpc.TokenName))
	require.NoError(t, err)

	var we *rpc.WireError
	require.ErrorAs(t, dialRawToken(string(apiTokBytes), rpc.RoleUI), &we)
	assert.Equal(t, rpc.CodeUnauthenticated, we.Code)

	full, mcp := dial(t, dir, rpc.RoleFull), dial(t, dir, rpc.RoleMCP)
	var accs []core.Account
	require.NoError(t, full.Call(ctx, "ListAccounts", &accs))
	require.NoError(t, mcp.Call(ctx, "ListAccounts", &accs))

	require.NoError(t, srv.SetExternal(false))
	_, err = os.Stat(filepath.Join(dir, rpc.TokenName))
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, c := range []*rpc.Client{full, mcp} {
		select {
		case <-c.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("external connection not dropped")
		}
	}

	// RoleUI remains connected and receives events on its live Subscribe stream.
	var st core.Settings
	require.NoError(t, ui.Call(ctx, "GetSettings", &st))
	st.Theme = "dark"
	require.NoError(t, ui.Call(ctx, "SetSettings", &st, st))
	select {
	case _, ok := <-uiEvents:
		require.True(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("RoleUI Subscribe stream did not receive event")
	}
	assert.Empty(t, auditRows(t, e.DataDir))
}
