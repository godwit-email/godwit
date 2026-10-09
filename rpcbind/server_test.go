package rpcbind_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	r, err := db.Query(context.Background(), "SELECT source, tool, target_id, COALESCE(extension, '') || COALESCE(destination, '') || COALESCE(reason, '') FROM audit_log WHERE source IN ('local_api', 'mcp') ORDER BY id")
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
		"ListMessages", "ListThreads", "NewDraft", "SaveDraft", "Search"}
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
	before := state()

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
		{"InstallPlugin", []any{coretest.PluginFixture(t, "card-demo")}},
		{"AddAccount", []any{core.AccountInput{Email: "eve@example.com", Username: "u", Password: "p", IMAPHost: "127.0.0.1", IMAPPort: 1, SMTPHost: "127.0.0.1", SMTPPort: 1}}},
		{"RemoveModelService", []any{services[0].ID}},
		{"SetPluginConfig", []any{"config-demo", map[string]any{"plan": "pro"}}},
		{"GetMessage", []any{msg.ID, true}},
		{"GetAttachment", []any{text.Attachments[0].ID}},
		{"NewDraft", []any{acc.ID, "forward", msg.ID}},
	} {
		err := c.Call(ctx, call.method, nil, call.args...)
		var we *rpc.WireError
		if assert.ErrorAs(t, err, &we, call.method) {
			assert.Equal(t, rpc.CodeForbidden, we.Code, call.method)
		}
	}
	assert.Equal(t, before, state(), "a forbidden call changed something")

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
	assert.Equal(t, "see the chart", m.Text)
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

	rows := auditRows(t, e.DataDir)
	assert.Equal(t, [][]any{
		{"mcp", "NewDraft", nil, ""},
		{"mcp", "SaveDraft", d.ID, ""},
		{"local_api", "Send", d.ID, ""},
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
