package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

func newGateway(t *testing.T) (*Gateway, *store.DB) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "g.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return New(db), db
}

func TestGatewayDecisions(t *testing.T) {
	g, _ := newGateway(t)
	clean, tainted := &Scope{}, &Scope{}
	tainted.MarkReadMail()
	for _, c := range []struct {
		scope *Scope
		call  Call
		want  Decision
	}{
		{clean, Call{Source: "wit", Tool: "search"}, Allow},
		{clean, Call{Source: "skill", Tool: "search"}, Deny},
		{clean, Call{Source: "skill", Tool: "fetch", Egress: "example.com"}, Deny},
		{clean, Call{Source: "wit", Tool: "create_draft", Write: true}, Approve},
		{clean, Call{Source: "plugin", Tool: "post", Egress: "example.com"}, Allow},
		{tainted, Call{Source: "mcp", Tool: "post", Egress: "example.com"}, Approve},
		{tainted, Call{Source: "hook", Tool: "read"}, Allow},
		{clean, Call{Source: "other", Tool: "x"}, Deny},
	} {
		d, _ := g.Check(t.Context(), c.scope, c.call)
		require.Equal(t, c.want, d, "%+v", c.call)
	}
}

func TestGatewayScopeAcrossTools(t *testing.T) {
	g, _ := newGateway(t)
	scope := &Scope{}
	var mu sync.Mutex
	sent := 0
	readTool := g.Wrap(scope, "wit", ai.ToolSpec{Name: "read_message", Run: func(context.Context, json.RawMessage) (any, error) {
		scope.MarkReadMail()
		return "ok", nil
	}}, false, "")
	postTool := g.Wrap(scope, "mcp", ai.ToolSpec{Name: "post", Run: func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		sent++
		mu.Unlock()
		return "sent", nil
	}}, false, "example.com")
	require.False(t, postTool.NeedsApproval)

	_, err := postTool.Run(t.Context(), nil)
	require.NoError(t, err)
	_, err = readTool.Run(t.Context(), nil)
	require.NoError(t, err)
	_, err = postTool.Run(t.Context(), nil)
	require.ErrorIs(t, err, ErrApprovalRequired)
	require.Equal(t, 1, sent)
	d, err := g.Check(t.Context(), scope, Call{Source: "mcp", Tool: "post", Egress: "example.com"})
	require.NoError(t, err)
	require.Equal(t, Approve, d)

	// Concurrent marks and checks on one scope race-free.
	s2 := &Scope{}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				s2.MarkReadMail()
			}
			_, err := g.Check(context.Background(), s2, Call{Source: "wit", Tool: "post", Egress: "example.com"})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.True(t, s2.ReadMail())
}

func TestAuditLogStructured(t *testing.T) {
	g, db := newGateway(t)
	_, err := g.Check(t.Context(), &Scope{}, Call{Source: "wit", Tool: "create_draft", Write: true, TargetKind: "draft", TargetID: 7})
	require.NoError(t, err)
	_, err = g.Check(t.Context(), &Scope{}, Call{Source: "skill", Tool: "search"})
	require.NoError(t, err)

	cols, err := db.Query(t.Context(), "SELECT name FROM pragma_table_info('audit_log') ORDER BY cid")
	require.NoError(t, err)
	var names []string
	for _, r := range cols.Values {
		names = append(names, store.Str(r[0]))
	}
	require.Equal(t, []string{"id", "at", "source", "tool", "target_kind", "target_id", "decision"}, names)

	rows, err := db.Query(t.Context(), "SELECT at, source, tool, target_kind, target_id, decision FROM audit_log ORDER BY id")
	require.NoError(t, err)
	require.Len(t, rows.Values, 2)
	r := rows.Values[0]
	require.NotZero(t, store.Int(r[0]))
	require.Equal(t, []any{"wit", "create_draft", "draft", int64(7), "approve"}, []any{store.Str(r[1]), store.Str(r[2]), store.Str(r[3]), store.Int(r[4]), store.Str(r[5])})
	r = rows.Values[1]
	require.Equal(t, []any{"skill", "search", "", nil, "deny"}, []any{store.Str(r[1]), store.Str(r[2]), store.Str(r[3]), r[4], store.Str(r[5])})
}

func TestWrapDenyForSkill(t *testing.T) {
	g, _ := newGateway(t)
	ran := false
	run := func(context.Context, json.RawMessage) (any, error) { ran = true; return nil, nil }
	for _, write := range []bool{false, true} {
		w := g.Wrap(&Scope{}, "skill", ai.ToolSpec{Name: "x", Run: run}, write, "")
		require.False(t, w.NeedsApproval) // denied, never offered for approval
		_, err := w.Run(t.Context(), nil)
		require.True(t, errors.Is(err, ErrDenied), "%v", err)
	}
	require.False(t, ran)

	w := g.Wrap(&Scope{}, "wit", ai.ToolSpec{Name: "create_draft", Run: run}, true, "")
	require.True(t, w.NeedsApproval)
	w = g.Wrap(&Scope{}, "wit", ai.ToolSpec{Name: "search", Run: run}, false, "")
	require.False(t, w.NeedsApproval)
	_, err := w.Run(t.Context(), nil)
	require.NoError(t, err)
	require.True(t, ran)
}
