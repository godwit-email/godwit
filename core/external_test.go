package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// extEnv adds test descriptors to an approvalEnv: ext_write (a plugin write
// tool run externally), ext_read (an MCP read tool with egress) and taint (a
// read tool that marks the run as having read mail).
type extEnv struct {
	*approvalEnv
	runs, inRun atomic.Int32
	binding     atomic.Value                           // string
	block       chan struct{}                          // ext_write's Run waits for it when set
	started     chan struct{}                          // ext_write's Run signals here when set
	outcome     func(ctx context.Context) (any, error) // ext_write's Run result when set
}

// setTimeout changes ext_write's timeout.
func (e *extEnv) setTimeout(d time.Duration) {
	e.s.toolsMu.Lock()
	defer e.s.toolsMu.Unlock()
	t := e.s.tools["ext_write"]
	t.Timeout = d
	e.s.tools["ext_write"] = t
}

const leakyDest = "https://user:hunter2-secret@api.example.com:8443/v1/x?token=tok-secret-9"

func newExtEnv(t *testing.T) *extEnv {
	t.Helper()
	e := &extEnv{approvalEnv: newApprovalEnv(t)}
	e.binding.Store("v1")
	e.registerExt(t, e.s)
	return e
}

func (e *extEnv) registerExt(t *testing.T, s *Service) {
	t.Helper()
	pass := func(_ context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
		return prepared{Args: args, Summary: "Ext call"}, nil
	}
	for _, d := range extDescsForTest(e, pass) {
		require.NoError(t, s.registerTool(d))
	}
}

func (e *extEnv) restartExt(t *testing.T) {
	t.Helper()
	e.restart(t)
	e.registerExt(t, e.s)
}

var extTools = []string{"taint", "ext_read", "ext_write", "fake_draft"}

// run scripts turns and starts a run offering extTools; it returns its events.
func (e *extEnv) run(t *testing.T, conv Conversation, turns ...testllm.Turn) []WitEvent {
	t.Helper()
	e.srv.Script(turns...)
	ch, err := e.s.startRun(t.Context(), conv, "go", e.s.registered(extTools...))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.NotEmpty(t, evs)
	return evs
}

func (e *extEnv) interruptExt(t *testing.T, conv Conversation, turns ...testllm.Turn) WitEvent {
	t.Helper()
	evs := e.run(t, conv, turns...)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	return last
}

func callTurn(name string) testllm.Turn {
	return testllm.Turn{Calls: []testllm.Call{{Name: name, Args: `{"x":1}`}}}
}

func TestReadToolNeedingApproval(t *testing.T) {
	e := newExtEnv(t)
	// No mail read: the read tool runs in the run.
	evs := e.run(t, e.newConv(t, "ask", 0), callTurn("ext_read"), testllm.Turn{Text: []string{"Read."}})
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	assert.Equal(t, int32(1), e.inRun.Load())
	assert.Zero(t, e.count(t, "SELECT COUNT(*) FROM approvals"))

	// After mail was read, its egress needs an approval, then runs once.
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("taint"), callTurn("ext_read"))
	require.Len(t, in.Approvals, 1)
	a := in.Approvals[0]
	assert.Equal(t, "ext_read", a.Tool)
	assert.Equal(t, []string{"api.example.com:8443"}, a.Destinations)
	assert.Equal(t, int32(1), e.inRun.Load(), "not run in the run")
	assert.Zero(t, e.runs.Load(), "not run before approval")

	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrInvalidState)
	e.waitRun(t, in.RunID, "done")
	assert.Equal(t, int32(1), e.runs.Load())
	assert.Equal(t, "done", e.approval(t, a.ID).State)
	reqs := e.srv.Requests()
	assert.Contains(t, string(reqs[len(reqs)-1].Messages), "read approved")
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM approvals WHERE id = ? AND exec_mode = 'external'", a.ID))
}

func TestExternalRunOutsideLock(t *testing.T) {
	e := newExtEnv(t)
	e.block, e.started = make(chan struct{}), make(chan struct{}, 1)
	convA := e.newConv(t, "ask", 0)
	inA := e.interruptExt(t, convA, callTurn("ext_write"))
	resolved := make(chan error, 1)
	go func() { resolved <- e.s.ResolveApproval(context.Background(), inA.Approvals[0].ID, true) }()
	select {
	case <-e.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not start")
	}
	assert.Equal(t, "executing", e.approval(t, inA.Approvals[0].ID).State)

	// While it runs, another conversation starts a run and decides an approval.
	convB := e.newConv(t, "ask", 0)
	var inB WitEvent
	within(t, 10*time.Second, "starting a run", func() {
		inB = e.interruptExt(t, convB, testllm.Turn{Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"B"}`}}})
	})
	within(t, 10*time.Second, "approving", func() {
		assert.NoError(t, e.s.ResolveApproval(t.Context(), inB.Approvals[0].ID, true))
	})
	e.waitRun(t, inB.RunID, "done")
	select {
	case err := <-resolved:
		t.Fatalf("resolved before Run returned: %v", err)
	default:
	}

	close(e.block)
	require.NoError(t, <-resolved)
	e.waitRun(t, inA.RunID, "done")
	assert.Equal(t, int32(1), e.runs.Load())
	assert.Equal(t, "done", e.approval(t, inA.Approvals[0].ID).State)
}

func TestExternalCrashUncertain(t *testing.T) {
	e := newExtEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("ext_write"))
	a := in.Approvals[0]
	// Run succeeds, recording it fails: the approval stays executing, as after a crash.
	_, err := e.s.db.Execute(t.Context(), `CREATE TRIGGER fail_done BEFORE UPDATE OF state ON approvals WHEN NEW.state = 'done'
		BEGIN SELECT RAISE(ABORT, 'injected commit failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, e.s.ResolveApproval(t.Context(), a.ID, true), "injected commit failure")
	assert.Equal(t, int32(1), e.runs.Load())
	assert.Equal(t, "executing", e.approval(t, a.ID).State)
	e.waitRun(t, in.RunID, "interrupted")
	_, err = e.s.db.Execute(t.Context(), "DROP TRIGGER fail_done")
	require.NoError(t, err)

	e.restartExt(t)
	e.waitRun(t, in.RunID, "done")
	got := e.approval(t, a.ID)
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, msgUncertain, got.Error)
	assert.Equal(t, int32(1), e.runs.Load(), "never run again")
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'ext_write' AND decision = 'uncertain'"))
	reqs := e.srv.Requests()
	assert.Contains(t, string(reqs[len(reqs)-1].Messages), msgUncertain)
}

func TestExternalBindingStale(t *testing.T) {
	e := newExtEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("ext_write"))
	a := in.Approvals[0]
	e.binding.Store("v2") // e.g. the plugin was updated
	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrStaleApproval)
	e.waitRun(t, in.RunID, "done")
	got := e.approval(t, a.ID)
	assert.Equal(t, "stale", got.State)
	assert.Equal(t, msgStale, got.Error)
	assert.Zero(t, e.runs.Load())
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'ext_write' AND decision = 'stale' AND source = 'plugin'"))
}

func TestDuplicateToolName(t *testing.T) {
	e := newExtEnv(t)
	require.Error(t, e.s.registerTool(toolDesc{Spec: e.s.registered("create_draft")[0].Spec, Source: gateway.SourcePlugin}))
	require.Error(t, e.s.registerTool(toolDesc{Spec: e.s.registered("ext_write")[0].Spec}))
	d := e.s.registered("ext_write")[0]
	assert.Equal(t, gateway.SourcePlugin, d.Source, "the first registration is kept")
	assert.Equal(t, gateway.SourceWit, cmpSource(e.s.registered("create_draft")[0]))
}

func cmpSource(d toolDesc) string {
	if d.Source == "" {
		return gateway.SourceWit
	}
	return d.Source
}

func TestSourcePreserved(t *testing.T) {
	e := newExtEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("taint"),
		testllm.Turn{Calls: []testllm.Call{{Name: "ext_write", Args: `{}`}, {Name: "ext_read", Args: `{}`}}})
	require.Len(t, in.Approvals, 2)
	w, r := in.Approvals[0], in.Approvals[1]
	require.NoError(t, e.s.ResolveApproval(t.Context(), w.ID, true))
	require.NoError(t, e.s.ResolveApproval(t.Context(), r.ID, false))
	e.waitRun(t, in.RunID, "done")

	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'taint' AND source = 'wit'"))
	for _, c := range []struct{ tool, source, ext, decision string }{
		{"ext_write", "plugin", "demo", "approve"}, {"ext_write", "plugin", "demo", "executed"},
		{"ext_read", "mcp", "srv1", "approve"}, {"ext_read", "mcp", "srv1", "rejected"},
	} {
		assert.Equal(t, 1, e.count(t, `SELECT COUNT(*) FROM audit_log WHERE tool = ? AND source = ? AND extension = ? AND decision = ?
			AND call_id <> ''`, c.tool, c.source, c.ext, c.decision), "%+v", c)
	}
	rows, err := e.s.db.Query(t.Context(), "SELECT DISTINCT call_id FROM audit_log WHERE tool = 'ext_write'")
	require.NoError(t, err)
	assert.Len(t, rows.Values, 1, "the gateway and the execution rows share the call ID")
}

func TestAuditDestinationNoSecrets(t *testing.T) {
	e := newExtEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("taint"), callTurn("ext_read"))
	a := in.Approvals[0]
	assert.NotContains(t, a.Summary, "api.example.com", "destinations are a field, not part of the summary")
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	e.waitRun(t, in.RunID, "done")

	rows, err := e.s.db.Query(t.Context(), "SELECT destination FROM audit_log WHERE tool = 'ext_read'")
	require.NoError(t, err)
	require.Len(t, rows.Values, 2) // approve, executed
	for _, r := range rows.Values {
		assert.Equal(t, "api.example.com:8443", store.Str(r[0]))
	}
	all, err := e.s.db.Query(t.Context(), "SELECT * FROM audit_log")
	require.NoError(t, err)
	for _, r := range all.Values {
		for _, v := range r {
			s, _ := v.(string)
			for _, bad := range []string{"hunter2", "tok-secret", "/v1", "token="} {
				assert.NotContains(t, s, bad)
			}
		}
	}
	got := e.approval(t, a.ID)
	assert.Equal(t, []string{"api.example.com:8443"}, got.Destinations)
	assert.False(t, strings.Contains(string(got.Args), "hunter2"))
}

func TestMixedTxAndExternalResume(t *testing.T) {
	e := newExtEnv(t)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, testllm.Turn{Calls: []testllm.Call{
		{Name: "fake_draft", Args: `{"subject":"A"}`}, {Name: "ext_write", Args: `{}`}}})
	require.Len(t, in.Approvals, 2)
	sub, unsub := e.s.SubscribeConversation(conv.ID)
	defer unsub()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, a := range in.Approvals {
		wg.Go(func() { <-start; assert.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true)) })
	}
	close(start)
	wg.Wait()
	assert.Equal(t, "done", waitTerminal(t, sub).Kind)
	e.waitRun(t, in.RunID, "done")
	time.Sleep(50 * time.Millisecond) // a second resume would have asked by now
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2, "resumed exactly once")
	calls, results := toolPairs(t, reqs[1])
	assert.Len(t, results, 2)
	assert.Equal(t, calls, results)
	assert.Contains(t, string(reqs[1].Messages), "created")
	assert.Contains(t, string(reqs[1].Messages), "wrote")
	assert.Equal(t, int32(1), e.applied.Load())
	assert.Equal(t, int32(1), e.runs.Load())
}

// TestCancelWaitsForExternal: cancelling a run whose approval executes
// outside the lock waits for it, so the history gets its real result.
func TestCancelWaitsForExternal(t *testing.T) {
	e := newExtEnv(t)
	e.block, e.started = make(chan struct{}), make(chan struct{}, 1)
	conv := e.newConv(t, "ask", 0)
	in := e.interruptExt(t, conv, callTurn("ext_write"))
	resolved := make(chan error, 1)
	go func() { resolved <- e.s.ResolveApproval(context.Background(), in.Approvals[0].ID, true) }()
	<-e.started
	cancelled := make(chan error, 1)
	go func() { cancelled <- e.s.CancelRun(context.Background(), in.RunID) }()
	select {
	case err := <-cancelled:
		t.Fatalf("CancelRun returned while the approval executes: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(e.block)
	require.NoError(t, <-resolved)
	require.NoError(t, <-cancelled)
	assert.Equal(t, "done", e.approval(t, in.Approvals[0].ID).State)
	require.Eventually(t, func() bool {
		rows, err := e.s.db.Query(t.Context(), "SELECT state FROM runs WHERE id = ?", in.RunID)
		st := store.Str(rows.Values[0][0])
		return err == nil && (st == "cancelled" || st == "done")
	}, 10*time.Second, 5*time.Millisecond)

	evs := e.run(t, conv, testllm.Turn{Text: []string{"Next."}})
	require.Equal(t, "done", evs[len(evs)-1].Kind)
	reqs := e.srv.Requests()
	calls, results := toolPairs(t, reqs[len(reqs)-1])
	assert.Equal(t, calls, results)
	assert.Contains(t, string(reqs[len(reqs)-1].Messages), "wrote")
}

func TestExternalRunErrorNotUncertain(t *testing.T) {
	e := newExtEnv(t)
	e.outcome = func(context.Context) (any, error) { return nil, errors.New("quota exceeded") }
	in := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	require.ErrorContains(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true), "quota exceeded")
	got := e.approval(t, in.Approvals[0].ID)
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, "quota exceeded", got.Error, "an ordinary error is not uncertain")
	e.waitRun(t, in.RunID, "done")

	// A result that cannot be encoded is an ordinary failure too.
	e.outcome = func(context.Context) (any, error) { return func() {}, nil }
	in = e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	require.Error(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	got = e.approval(t, in.Approvals[0].ID)
	assert.Equal(t, "failed", got.State)
	assert.NotEqual(t, msgUncertain, got.Error)
}

func TestExternalTimeoutUncertain(t *testing.T) {
	e := newExtEnv(t)
	e.setTimeout(20 * time.Millisecond)
	e.outcome = func(ctx context.Context) (any, error) { <-ctx.Done(); return nil, ctx.Err() }
	in := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	require.Error(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	got := e.approval(t, in.Approvals[0].ID)
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, msgUncertain, got.Error)
}

func TestExternalOutcomeSanitized(t *testing.T) {
	e := newExtEnv(t)
	e.s.secrets.Add("plugin:demo", "sekret-value-123")
	e.outcome = func(context.Context) (any, error) {
		return nil, errors.New("upstream said sekret-value-123 while reading /Users/someone/plugins/demo/main.js")
	}
	in := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	a := in.Approvals[0]
	err := e.s.ResolveApproval(t.Context(), a.ID, true)
	require.Error(t, err)
	got := e.approval(t, a.ID)
	assert.Contains(t, got.Error, "[redacted]")
	assert.Contains(t, got.Error, "<path>")
	e.waitRun(t, in.RunID, "done")

	e.outcome = func(context.Context) (any, error) {
		return map[string]string{"token": "sekret-value-123", "file": "/home/u/x"}, nil
	}
	in2 := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	require.NoError(t, e.s.ResolveApproval(t.Context(), in2.Approvals[0].ID, true))
	errAll := e.s.ResolveAll(t.Context(), in2.RunID, true)

	rows, qerr := e.s.db.Query(t.Context(), "SELECT result FROM approvals")
	require.NoError(t, qerr)
	texts := []string{err.Error(), fmt.Sprint(errAll)}
	for _, r := range rows.Values {
		texts = append(texts, store.Str(r[0]))
	}
	audit, qerr := e.s.db.Query(t.Context(), "SELECT * FROM audit_log")
	require.NoError(t, qerr)
	for _, r := range audit.Values {
		texts = append(texts, fmt.Sprint(r...))
	}
	for _, s := range texts {
		assert.NotContains(t, s, "sekret-value-123")
		assert.NotContains(t, s, "/Users/someone")
		assert.NotContains(t, s, "/home/u")
	}
	assert.True(t, json.Valid([]byte(e.approval(t, in2.Approvals[0].ID).Args)))
}

func TestExternalOutcomeEscapedSecret(t *testing.T) {
	e := newExtEnv(t)
	const secret = "<sekret-123>&" // characters JSON escapes
	e.s.secrets.Add("plugin:demo", secret)
	e.outcome = func(context.Context) (any, error) {
		return map[string]any{"note": "x " + secret + " y", secret: []any{secret}}, nil
	}
	in := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	require.NoError(t, e.s.ResolveApproval(t.Context(), in.Approvals[0].ID, true))
	e.waitRun(t, in.RunID, "done")

	e.outcome = func(context.Context) (any, error) { return nil, errors.New("bad " + secret) }
	in2 := e.interruptExt(t, e.newConv(t, "ask", 0), callTurn("ext_write"))
	errAll := e.s.ResolveAll(t.Context(), in2.RunID, true)
	require.Error(t, errAll, "ResolveAll fails with the Run's error")

	texts := []string{errAll.Error()}
	rows, err := e.s.db.Query(t.Context(), "SELECT result FROM approvals")
	require.NoError(t, err)
	for _, r := range rows.Values {
		texts = append(texts, store.Str(r[0]))
	}
	audit, err := e.s.db.Query(t.Context(), "SELECT * FROM audit_log")
	require.NoError(t, err)
	for _, r := range audit.Values {
		texts = append(texts, fmt.Sprint(r...))
	}
	for _, s := range texts {
		assert.NotContains(t, s, "sekret-123", "raw or escaped")
	}
	assert.True(t, json.Valid([]byte(store.Str(rows.Values[0][0]))))
}
