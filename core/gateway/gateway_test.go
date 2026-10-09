package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
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
		{clean, Call{Source: "skill", Tool: "fetch", Egress: []string{"example.com"}}, Deny},
		{clean, Call{Source: "wit", Tool: "create_draft", Write: true}, Approve},
		{clean, Call{Source: "plugin", Tool: "post", Egress: []string{"example.com"}}, Allow},
		{tainted, Call{Source: "mcp", Tool: "post", Egress: []string{"example.com"}}, Approve},
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
	run := func(context.Context, json.RawMessage) (any, error) { return "ok", nil }
	post := g.Wrap(scope, Call{Source: "mcp"}, ai.ToolSpec{Name: "post", Run: run}, egressTo("example.com"))
	require.False(t, post.NeedsApproval)
	d, err := post.Gate(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, Allow, d)
	scope.MarkReadMail() // tool A read mail
	d, err = post.Gate(t.Context(), nil)
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
			_, err := g.Check(context.Background(), s2, Call{Source: "wit", Tool: "post", Egress: []string{"example.com"}})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	require.True(t, s2.ReadMail())
}

func engineRun(t *testing.T, script []testllm.Turn, tools []ai.ToolSpec) []ai.Event {
	t.Helper()
	srv := testllm.Start(t, "openai")
	srv.Script(script...)
	e, err := ai.NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)
	t.Cleanup(e.Close)
	events, wait := e.Run(t.Context(), ai.Request{Model: ai.ResolvedModel{ServiceID: 1, Version: 1, Kind: "openai", BaseURL: srv.URL, Model: "m", APIKey: "k"},
		Prompt: "go", Tools: tools})
	var evs []ai.Event
	for ev := range events {
		evs = append(evs, ev)
	}
	require.Nil(t, wait().Err)
	return evs
}

func TestWrappedWriteInterruptsAndAudits(t *testing.T) {
	g, db := newGateway(t)
	ran := false
	w := g.Wrap(&Scope{}, Call{Source: "wit", Write: true}, ai.ToolSpec{Name: "create_draft", Run: func(context.Context, json.RawMessage) (any, error) { ran = true; return nil, nil }}, nil)
	evs := engineRun(t, []testllm.Turn{{Calls: []testllm.Call{{ID: "c1", Name: "create_draft"}}}}, []ai.ToolSpec{w})
	end := evs[len(evs)-1]
	require.Equal(t, "interrupted", end.Kind)
	require.False(t, ran)
	rows, err := db.Query(t.Context(), "SELECT source, tool, decision FROM audit_log")
	require.NoError(t, err)
	require.Equal(t, [][]any{{"wit", "create_draft", "approve"}}, rows.Values)
}

func TestEgressAfterReadInterrupts(t *testing.T) {
	g, _ := newGateway(t)
	scope := &Scope{}
	posted := false
	read := g.Wrap(scope, Call{Source: "wit"}, ai.ToolSpec{Name: "read_message", Run: func(context.Context, json.RawMessage) (any, error) {
		scope.MarkReadMail()
		return "mail", nil
	}}, nil)
	post := g.Wrap(scope, Call{Source: "mcp"}, ai.ToolSpec{Name: "post", Run: func(context.Context, json.RawMessage) (any, error) { posted = true; return nil, nil }}, egressTo("example.com"))
	evs := engineRun(t, []testllm.Turn{
		{Calls: []testllm.Call{{ID: "r1", Name: "read_message"}}},
		{Calls: []testllm.Call{{ID: "p1", Name: "post"}}},
	}, []ai.ToolSpec{read, post})
	end := evs[len(evs)-1]
	require.Equal(t, "interrupted", end.Kind)
	require.Len(t, end.Interrupts, 1)
	require.Equal(t, "p1", end.Interrupts[0].ID)
	require.False(t, posted)
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
	// Structured fields only: no arguments, request bodies, URLs or secrets (migration 7 adds the extension ones).
	require.Equal(t, []string{"id", "at", "source", "tool", "target_kind", "target_id", "decision",
		"extension", "call_id", "destination", "reason"}, names)

	rows, err := db.Query(t.Context(), "SELECT at, source, tool, target_kind, target_id, decision FROM audit_log ORDER BY id")
	require.NoError(t, err)
	require.Len(t, rows.Values, 2)
	r := rows.Values[0]
	require.NotZero(t, store.Int(r[0]))
	require.Equal(t, []any{"wit", "create_draft", "draft", int64(7), "approve"}, []any{store.Str(r[1]), store.Str(r[2]), store.Str(r[3]), store.Int(r[4]), store.Str(r[5])})
	r = rows.Values[1]
	require.Equal(t, []any{"skill", "search", "", nil, "deny"}, []any{store.Str(r[1]), store.Str(r[2]), store.Str(r[3]), r[4], store.Str(r[5])})
}

func TestRecord(t *testing.T) {
	g, db := newGateway(t)
	require.NoError(t, g.Record(t.Context(), Entry{Source: "skill", Tool: "skill:summarize", TargetKind: "thread", TargetID: 3, Decision: "run"}))
	require.NoError(t, g.Record(t.Context(), Entry{Source: "skill", Tool: "skill:translate", Decision: "run"}))
	rows, err := db.Query(t.Context(), "SELECT source, tool, target_kind, target_id, decision FROM audit_log ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{{"skill", "skill:summarize", "thread", int64(3), "run"}, {"skill", "skill:translate", "", nil, "run"}}, rows.Values)
}

func TestWrapDenyForSkill(t *testing.T) {
	g, _ := newGateway(t)
	run := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	for _, needs := range []bool{false, true} {
		for _, write := range []bool{false, true} {
			for _, src := range []string{"skill", "other"} {
				w := g.Wrap(&Scope{}, Call{Source: src, Write: write}, ai.ToolSpec{Name: "x", Run: run, NeedsApproval: needs}, nil)
				require.False(t, w.NeedsApproval, "deny beats a NeedsApproval flag")
				d, err := w.Gate(t.Context(), nil)
				require.NoError(t, err)
				require.Equal(t, Deny, d)
			}
		}
	}
	for _, c := range []struct {
		needs, write bool
		want         Decision
	}{{false, false, Allow}, {false, true, Approve}, {true, false, Approve}} {
		w := g.Wrap(&Scope{}, Call{Source: "wit", Write: c.write}, ai.ToolSpec{Name: "y", Run: run, NeedsApproval: c.needs}, nil)
		d, err := w.Gate(t.Context(), nil)
		require.NoError(t, err)
		require.Equal(t, c.want, d, "%+v", c)
	}
}

func egressTo(hosts ...string) func(*Scope) []string { return func(*Scope) []string { return hosts } }

func TestNilScopeReadNoMail(t *testing.T) {
	g, _ := newGateway(t)
	d, err := g.Check(t.Context(), nil, Call{Source: "plugin", Tool: "post", Egress: []string{"example.com"}})
	require.NoError(t, err)
	require.Equal(t, Allow, d)
}

func TestWrapComposesOwnGate(t *testing.T) {
	g, db := newGateway(t)
	run := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	for _, c := range []struct {
		source    string
		own, want Decision
	}{{"wit", Allow, Allow}, {"wit", Approve, Approve}, {"wit", Deny, Deny}, {"skill", Allow, Deny}, {"skill", Approve, Deny}} {
		own := c.own
		w := g.Wrap(nil, Call{Source: c.source}, ai.ToolSpec{Name: "z", Run: run,
			Gate: func(context.Context, json.RawMessage) (Decision, error) { return own, nil }}, nil)
		d, err := w.Gate(t.Context(), nil)
		require.NoError(t, err)
		require.Equal(t, c.want, d, "%+v", c)
	}
	rows, err := db.Query(t.Context(), "SELECT decision FROM audit_log WHERE tool = 'z' ORDER BY id")
	require.NoError(t, err)
	require.Equal(t, [][]any{{"allow"}, {"approve"}, {"deny"}, {"deny"}, {"deny"}}, rows.Values, "the final decision is audited")
}

func TestHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@api.example.com:8443/v1?token=x": "api.example.com:8443",
		"api.example.com":            "api.example.com",
		"api.example.com:444/path?q": "api.example.com:444",
		"u:p@host.example":           "host.example",
		"mcp:My Server":              "mcp:My Server",
	} {
		require.Equal(t, want, Host(in), in)
	}
}
