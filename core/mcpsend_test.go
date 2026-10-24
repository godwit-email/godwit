package core

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/events"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpSendEnv is newSyncEnv (no background sync: a queued mail stays in the
// outbox) with a draft to bob and 通过 MCP 发送 at level; window says whether
// a window can confirm (false: --headless).
func mcpSendEnv(t *testing.T, level string, window bool) (*syncEnv, Draft) {
	t.Helper()
	e := newSyncEnv(t)
	e.s.opts.ConfirmWindow = window
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.MCPSend = level
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Subject, d.HTML = []string{"bob@example.com"}, "hello", "<p>hi</p>"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	return e, d
}

func (e *syncEnv) outboxLen(t *testing.T) int {
	t.Helper()
	out, err := e.s.ListOutbox(bg, e.acc.ID)
	require.NoError(t, err)
	return len(out)
}

func (e *syncEnv) pendingSends(t *testing.T) []MCPSend {
	t.Helper()
	ps, err := e.s.PendingMCPSends(bg)
	require.NoError(t, err)
	return ps
}

func TestMCPSendOff(t *testing.T) {
	for _, c := range []struct {
		name   string
		level  string
		window bool
	}{{"off", MCPSendOff, true}, {"headless confirm", MCPSendConfirm, false}} {
		t.Run(c.name, func(t *testing.T) {
			e, d := mcpSendEnv(t, c.level, c.window)
			_, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
			require.ErrorIs(t, err, ErrMCPSendOff)
			assert.Empty(t, e.pendingSends(t))
			assert.Zero(t, e.outboxLen(t))
		})
	}
}

func TestMCPSendConfirm(t *testing.T) {
	e, d := mcpSendEnv(t, MCPSendConfirm, true)
	evs, stop := e.s.bus.Subscribe()
	defer stop()
	sent, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
	require.NoError(t, err)
	assert.False(t, sent)
	assert.Equal(t, events.MCPSendsChanged, (<-evs).Type)
	_, err = e.s.RequestMCPSend(bg, d.ID, d.Revision) // the card's button clicked twice
	require.NoError(t, err)
	ps := e.pendingSends(t)
	require.Len(t, ps, 1)
	assert.Equal(t, d.ID, ps[0].Draft.ID)
	assert.Equal(t, "hello", ps[0].Draft.Subject)
	assert.Equal(t, "alice@example.com", ps[0].From)
	assert.Equal(t, []string{"bob@example.com"}, ps[0].FirstTime)
	assert.Zero(t, e.outboxLen(t))

	require.NoError(t, e.s.ResolveMCPSend(bg, ps[0].ID, true))
	assert.Equal(t, 1, e.outboxLen(t))
	assert.ErrorIs(t, e.s.ResolveMCPSend(bg, ps[0].ID, true), ErrInvalidState)
	assert.Equal(t, 1, e.outboxLen(t))
	assert.Empty(t, e.pendingSends(t))
	assert.Equal(t, [][]any{{"local_mcp", "send_draft", d.ID, "executed"}},
		e.rows(t, "SELECT source, tool, target_id, decision FROM audit_log WHERE source = 'local_mcp'"))
}

// Only the addresses the account never wrote to are marked first-time; one
// that wrote to the account is still new.
func TestMCPSendFirstTime(t *testing.T) {
	e, d := mcpSendEnv(t, MCPSendConfirm, true)
	day := "Mon, 05 Oct 2026 10:00:00 +0000"
	e.put(t, "Sent", eml("a@x", "alice@example.com", "Bob <BOB@example.com>", "earlier", day, "hi"))
	e.put(t, "INBOX", eml("b@x", "carol@example.com", "alice@example.com", "from carol", day, "hi"))
	e.sync(t)
	d.To, d.Cc = []string{"bob@example.com"}, []string{"Carol <carol@example.com>", "dave@example.com"}
	d, err := e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	_, err = e.s.RequestMCPSend(bg, d.ID, d.Revision)
	require.NoError(t, err)
	assert.Equal(t, []string{"carol@example.com", "dave@example.com"}, e.pendingSends(t)[0].FirstTime)
}

func TestMCPSendReject(t *testing.T) {
	e, d := mcpSendEnv(t, MCPSendConfirm, true)
	_, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
	require.NoError(t, err)
	id := e.pendingSends(t)[0].ID
	require.NoError(t, e.s.ResolveMCPSend(bg, id, false))
	assert.ErrorIs(t, e.s.ResolveMCPSend(bg, id, true), ErrInvalidState)
	assert.Zero(t, e.outboxLen(t))
	_, err = e.s.GetDraft(bg, d.ID)
	assert.NoError(t, err, "the draft stays")
}

// A request goes stale when its draft changes or after ten minutes; then
// confirming it sends nothing.
func TestMCPSendStale(t *testing.T) {
	t.Run("draft changed", func(t *testing.T) {
		e, d := mcpSendEnv(t, MCPSendConfirm, true)
		_, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
		require.NoError(t, err)
		id := e.pendingSends(t)[0].ID
		d.Subject = "changed"
		_, err = e.s.SaveDraft(bg, d)
		require.NoError(t, err)
		assert.ErrorIs(t, e.s.ResolveMCPSend(bg, id, true), ErrStaleDraft)
		assert.Zero(t, e.outboxLen(t))
		assert.Empty(t, e.pendingSends(t))
	})
	t.Run("listed after the change", func(t *testing.T) {
		e, d := mcpSendEnv(t, MCPSendConfirm, true)
		_, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
		require.NoError(t, err)
		id := e.pendingSends(t)[0].ID
		d.Subject = "changed"
		_, err = e.s.SaveDraft(bg, d)
		require.NoError(t, err)
		assert.Empty(t, e.pendingSends(t))
		assert.ErrorIs(t, e.s.ResolveMCPSend(bg, id, true), ErrInvalidState)
		assert.Zero(t, e.outboxLen(t))
	})
	t.Run("ten minutes", func(t *testing.T) {
		e, d := mcpSendEnv(t, MCPSendConfirm, true)
		_, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
		require.NoError(t, err)
		id := e.pendingSends(t)[0].ID
		e.s.sendReqs[id].created = time.Now().Add(-mcpSendTTL)
		assert.ErrorIs(t, e.s.ResolveMCPSend(bg, id, true), ErrStaleDraft)
		assert.Zero(t, e.outboxLen(t))
		assert.Equal(t, [][]any{{"stale"}}, e.rows(t, "SELECT decision FROM audit_log WHERE source = 'local_mcp'"))
	})
}

func TestMCPSendWrongRevision(t *testing.T) {
	for _, level := range []string{MCPSendConfirm, MCPSendDirect} {
		t.Run(level, func(t *testing.T) {
			e, d := mcpSendEnv(t, level, true)
			for _, rev := range []int64{d.Revision - 1, d.Revision + 1, -1} {
				_, err := e.s.RequestMCPSend(bg, d.ID, rev)
				assert.ErrorIs(t, err, ErrStaleDraft, "revision %d", rev)
			}
			assert.Empty(t, e.pendingSends(t))
			assert.Zero(t, e.outboxLen(t))
		})
	}
}

func TestMCPSendDirect(t *testing.T) {
	e, d := mcpSendEnv(t, MCPSendDirect, false) // headless too
	evs, stop := e.s.bus.Subscribe()
	defer stop()
	sent, err := e.s.RequestMCPSend(bg, d.ID, d.Revision)
	require.NoError(t, err)
	assert.True(t, sent)
	assert.Equal(t, 1, e.outboxLen(t))
	assert.Empty(t, e.pendingSends(t))
	for len(evs) > 0 {
		assert.NotEqual(t, events.MCPSendsChanged, (<-evs).Type, "no approval")
	}
	assert.Equal(t, [][]any{{"local_mcp", "send_draft", d.ID, "direct"}},
		e.rows(t, "SELECT source, tool, target_id, decision FROM audit_log WHERE source = 'local_mcp'"))
}
