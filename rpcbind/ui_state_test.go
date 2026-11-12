package rpcbind_test

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUISessionForegroundAndRoleIsolation(t *testing.T) {
	svc := newService(t)
	ctx := t.Context()
	dir := listen(t, svc)

	assert.False(t, svc.UIConnected())
	assert.False(t, svc.UIForeground())

	full := dial(t, dir, rpc.RoleFull)
	mcp := dial(t, dir, rpc.RoleMCP)

	// External roles neither count as a UI connection nor can call UI state methods.
	assert.False(t, svc.UIConnected())
	for _, call := range []struct {
		method string
		args   []any
	}{
		{"SetUIForeground", []any{true}},
		{"SetComposeOpen", []any{[]int64{1}}},
	} {
		var fwe *rpc.WireError
		require.ErrorAs(t, full.Call(ctx, call.method, nil, call.args...), &fwe, call.method)
		assert.Equal(t, rpc.CodeMethodNotFound, fwe.Code, "full on %s", call.method)

		var mwe *rpc.WireError
		require.ErrorAs(t, mcp.Call(ctx, call.method, nil, call.args...), &mwe, call.method)
		assert.Equal(t, rpc.CodeForbidden, mwe.Code, "mcp on %s", call.method)
	}
	assert.False(t, svc.UIForeground())

	// First UI connection connects: present, not foreground until reported.
	ui1 := dial(t, dir, rpc.RoleUI)
	assert.True(t, svc.UIConnected())
	assert.False(t, svc.UIForeground())

	require.NoError(t, ui1.Call(ctx, "SetUIForeground", nil, true))
	assert.True(t, svc.UIForeground())

	// Second UI connection reports background: union remains foreground while ui1 is foreground.
	ui2 := dial(t, dir, rpc.RoleUI)
	require.NoError(t, ui2.Call(ctx, "SetUIForeground", nil, false))
	assert.True(t, svc.UIConnected())
	assert.True(t, svc.UIForeground())

	// Disconnecting ui1 clears its foreground flag while ui2 stays connected.
	require.NoError(t, ui1.Close())
	require.Eventually(t, func() bool { return !svc.UIForeground() }, time.Second, time.Millisecond)
	assert.True(t, svc.UIConnected())

	// Disconnecting ui2 leaves zero UI connections.
	require.NoError(t, ui2.Close())
	require.Eventually(t, func() bool { return !svc.UIConnected() }, time.Second, time.Millisecond)
	assert.False(t, svc.UIForeground())
}

func TestUIForegroundNarrowsBackgroundSync(t *testing.T) {
	e := coretest.New(t)
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	require.NoError(t, e.Svc.SyncNow(ctx, acc.ID))

	folders, err := e.Svc.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	var archiveID int64
	for _, f := range folders {
		if f.Name == "Archive" {
			archiveID = f.ID
		}
	}
	require.NotZero(t, archiveID)
	archiveCount := func() int {
		page, err := e.Svc.ListMessages(ctx, core.View{FolderID: archiveID}, "", 0)
		require.NoError(t, err)
		return len(page.Items)
	}
	inboxCount := func() int {
		page, err := e.Svc.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
		require.NoError(t, err)
		return len(page.Items)
	}

	// A full round woken by widening may outlive the switch back, so the
	// narrowed check first waits for sync events to go quiet (rounds that
	// began before Subscribe make counting starts and ends unreliable).
	var last atomic.Int64 // unix nanos of the latest sync event
	last.Store(time.Now().UnixNano())
	evs, stopEvs := e.Svc.Subscribe()
	t.Cleanup(stopEvs)
	go func() {
		for ev := range evs {
			switch ev.Type {
			case events.SyncStarted, events.SyncFinished, events.SyncFailed:
				last.Store(time.Now().UnixNano())
			}
		}
	}()
	idle := func() {
		t.Helper()
		require.Eventually(t, func() bool { return time.Since(time.Unix(0, last.Load())) > 300*time.Millisecond },
			5*time.Second, 10*time.Millisecond, "sync events quiet")
	}

	dir := listen(t, e.Svc)
	ui1 := dial(t, dir, rpc.RoleUI)

	// Reporting background with default BackgroundSyncInbox narrows rounds to the inbox.
	require.NoError(t, ui1.Call(ctx, "SetUIForeground", nil, false))
	assert.True(t, e.Svc.InboxOnly())

	// Changing BackgroundSync to "all" while in background immediately widens sync and wakes workers.
	var st core.Settings
	require.NoError(t, ui1.Call(ctx, "GetSettings", &st))
	_, err = e.IMAP(acc.ID).User.Append("Archive", bytes.NewReader(coretest.Msg("bob@example.com", acc.Email, "Arch 1", "body")), &imap.AppendOptions{})
	require.NoError(t, err)
	st.BackgroundSync = core.BackgroundSyncAll
	require.NoError(t, ui1.Call(ctx, "SetSettings", &st, st))
	assert.False(t, e.Svc.InboxOnly())
	require.Eventually(t, func() bool { return archiveCount() == 1 }, 5*time.Second, 10*time.Millisecond,
		"widening BackgroundSync to all catches up Archive")

	// Switching BackgroundSync back to "inbox" while still in background narrows sync again.
	st.BackgroundSync = core.BackgroundSyncInbox
	require.NoError(t, ui1.Call(ctx, "SetSettings", &st, st))
	assert.True(t, e.Svc.InboxOnly())

	// While narrowed, an INBOX arrival syncs INBOX without pulling Archive; coming to foreground catches up Archive.
	// A first arrival lets any full round left from widening finish.
	_, err = e.IMAP(acc.ID).User.Append("INBOX", bytes.NewReader(coretest.Msg("bob@example.com", acc.Email, "Inbox 0", "body")), &imap.AppendOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return inboxCount() == 1 }, 5*time.Second, 10*time.Millisecond)
	idle()
	_, err = e.IMAP(acc.ID).User.Append("Archive", bytes.NewReader(coretest.Msg("bob@example.com", acc.Email, "Arch 2", "body")), &imap.AppendOptions{})
	require.NoError(t, err)
	_, err = e.IMAP(acc.ID).User.Append("INBOX", bytes.NewReader(coretest.Msg("bob@example.com", acc.Email, "Inbox 1", "body")), &imap.AppendOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return inboxCount() == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, archiveCount(), "Archive stays at 1 while narrowed")

	require.NoError(t, ui1.Call(ctx, "SetUIForeground", nil, true))
	assert.False(t, e.Svc.InboxOnly())
	require.Eventually(t, func() bool { return archiveCount() == 2 }, 5*time.Second, 10*time.Millisecond,
		"coming to foreground catches up Archive")

	// A second UI connection in background keeps aggregate foreground true; disconnecting ui1 drops back to narrowed.
	ui2 := dial(t, dir, rpc.RoleUI)
	require.NoError(t, ui2.Call(ctx, "SetUIForeground", nil, false))
	assert.False(t, e.Svc.InboxOnly())

	require.NoError(t, ui1.Close())
	require.Eventually(t, func() bool { return e.Svc.InboxOnly() }, time.Second, time.Millisecond)
}

func TestUIComposeOpenBlocksDirectMCPSend(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	ctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")

	st, err := e.Svc.GetSettings(ctx)
	require.NoError(t, err)
	st.MCPSend = core.MCPSendDirect
	_, err = e.Svc.SetSettings(ctx, st)
	require.NoError(t, err)

	makeDraft := func(subject string) core.Draft {
		t.Helper()
		d, err := e.Svc.NewDraft(ctx, acc.ID, "new", 0)
		require.NoError(t, err)
		d.To, d.Subject, d.HTML = []string{"bob@example.com"}, subject, "<p>hi</p>"
		d, err = e.Svc.SaveDraft(ctx, d)
		require.NoError(t, err)
		return d
	}
	d1, d2 := makeDraft("one"), makeDraft("two")

	dir := listen(t, e.Svc)
	ui1 := dial(t, dir, rpc.RoleUI)
	ui2 := dial(t, dir, rpc.RoleUI)
	mcp, err := rpc.Dial(dir, rpc.RoleMCP, "mcp-test", rpcbind.NewCodec())
	require.NoError(t, err)
	t.Cleanup(func() { mcp.Close() })

	assert.False(t, e.Svc.IsComposeOpen(d1.ID))
	assert.False(t, e.Svc.IsComposeOpen(d2.ID))

	// Union across multiple RoleUI connections blocks direct MCP send for both drafts.
	require.NoError(t, ui1.Call(ctx, "SetComposeOpen", nil, []int64{d1.ID}))
	require.NoError(t, ui2.Call(ctx, "SetComposeOpen", nil, []int64{d2.ID}))
	assert.True(t, e.Svc.IsComposeOpen(d1.ID))
	assert.True(t, e.Svc.IsComposeOpen(d2.ID))

	var sent bool
	err = mcp.Call(ctx, "RequestMCPSend", &sent, d1.ID, d1.Revision)
	require.ErrorIs(t, err, core.ErrMCPSendComposing)
	err = mcp.Call(ctx, "RequestMCPSend", &sent, d2.ID, d2.Revision)
	require.ErrorIs(t, err, core.ErrMCPSendComposing)

	// Updating ui1's open list replaces its previous set without affecting ui2.
	require.NoError(t, ui1.Call(ctx, "SetComposeOpen", nil, []int64{}))
	assert.False(t, e.Svc.IsComposeOpen(d1.ID))
	assert.True(t, e.Svc.IsComposeOpen(d2.ID))

	// Re-open d1 on ui1, then drop ui1's connection: d1 auto-clears and can send, while d2 stays blocked.
	require.NoError(t, ui1.Call(ctx, "SetComposeOpen", nil, []int64{d1.ID}))
	require.NoError(t, ui1.Close())
	require.Eventually(t, func() bool { return !e.Svc.IsComposeOpen(d1.ID) }, time.Second, time.Millisecond)
	assert.True(t, e.Svc.IsComposeOpen(d2.ID))

	require.NoError(t, mcp.Call(ctx, "RequestMCPSend", &sent, d1.ID, d1.Revision))
	assert.True(t, sent)
	err = mcp.Call(ctx, "RequestMCPSend", &sent, d2.ID, d2.Revision)
	require.ErrorIs(t, err, core.ErrMCPSendComposing)

	// Dropping ui2 clears d2 as well.
	require.NoError(t, ui2.Close())
	require.Eventually(t, func() bool { return !e.Svc.IsComposeOpen(d2.ID) }, time.Second, time.Millisecond)
	require.NoError(t, mcp.Call(ctx, "RequestMCPSend", &sent, d2.ID, d2.Revision))
	assert.True(t, sent)

	outbox, err := e.Svc.ListOutbox(ctx, acc.ID)
	require.NoError(t, err)
	assert.Len(t, outbox, 2)
}
