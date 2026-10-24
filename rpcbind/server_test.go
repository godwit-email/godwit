package rpcbind_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	r, err := db.Query(context.Background(), "SELECT source, tool, target_id, COALESCE(extension, '') || COALESCE(destination, '') || COALESCE(reason, '') FROM audit_log WHERE source IN ('local_api', 'local_mcp') ORDER BY id")
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
		{"AddAccount", []any{core.AccountInput{Email: "eve@example.com", Username: "u", Password: "p", IMAPHost: "127.0.0.1", IMAPPort: 1, SMTPHost: "127.0.0.1", SMTPPort: 1}}},
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

	rows := auditRows(t, e.DataDir)
	assert.Equal(t, [][]any{
		{"local_mcp", "NewDraft", nil, ""},
		{"local_mcp", "SaveDraft", d.ID, ""},
		{"local_api", "Send", d.ID, ""},
		{"local_api", "Unsubscribe", msg, ""},
	}, rows)
	b, _ := json.Marshal(rows)
	assert.NotContains(t, string(b), "secret plan")
	assert.NotContains(t, string(b), "body text")
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
