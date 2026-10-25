package core

import (
	"encoding/json"
	"github.com/Calcium-Ion/moejs"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockVM occupies the plugin's VM until the returned func is called.
func blockVM(t *testing.T, s *Service, name string) (release func()) {
	t.Helper()
	s.pluginsMu.RLock()
	vm := s.running[name].p.VM()
	s.pluginsMu.RUnlock()
	started, done := make(chan struct{}), make(chan struct{})
	go vm.Call(t.Context(), plugins.Invocation{ID: "blocker", Kind: plugins.KindHook}, func(*plugins.CallScope) (moejs.Value, error) {
		close(started)
		<-done
		return moejs.Undefined(), nil
	})
	<-started
	return sync.OnceFunc(func() { close(done) })
}

// An approved call runs with the config its approval was bound to: a config
// saved while the call waits for the VM makes it stale or leaves it on the
// approved snapshot, never on the new config.
func TestApprovedToolKeepsItsConfig(t *testing.T) {
	e := newApprovalEnv(t)
	startTestPlugin(t, e.s, "tagger", `{},"config":[{"key":"tag","type":"text","label":"Tag"}]`, `export default function (ctx: any) {
  ctx.tools.register({ name: "mark", description: "Marks.", schema: { type: "object" }, run: async () => "tag=" + ctx.config.tag })
}`)
	require.NoError(t, e.s.SetPluginConfig(t.Context(), "tagger", map[string]json.RawMessage{"tag": json.RawMessage(`"old"`)}))
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs("plugin_tagger_mark", `{}`), finished)
	in := evs[len(evs)-1]
	require.Equal(t, "interrupted", in.Kind, "%+v", in.Err)

	release := blockVM(t, e.s, "tagger")
	t.Cleanup(release)
	resolved := make(chan error, 1)
	go func() { resolved <- e.s.ResolveApproval(bg, in.Approvals[0].ID, true) }()
	require.Eventually(t, func() bool { return e.approval(t, in.Approvals[0].ID).State == "executing" }, 5*time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond) // the call is queued on the VM
	require.NoError(t, e.s.SetPluginConfig(t.Context(), "tagger", map[string]json.RawMessage{"tag": json.RawMessage(`"new"`)}))
	release()
	<-resolved
	e.waitRun(t, in.RunID, "done")
	a := e.approval(t, in.Approvals[0].ID)
	if a.State != "stale" {
		assert.Equal(t, "done", a.State)
		assert.Equal(t, 1, e.count(t, `SELECT COUNT(*) FROM approvals WHERE id = ? AND result = '"tag=old"'`, a.ID), "the approved snapshot")
		assert.Zero(t, e.count(t, `SELECT COUNT(*) FROM approvals WHERE result LIKE '%new%'`))
	}
}

// A beforeSend warning is bound to the checker's config: once the config
// changes (here to block), acknowledging the old warning checks again.
func TestBeforeSendAckBoundToConfig(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "policy", sendPerms+`,"config":[{"key":"mode","type":"text","label":"Mode","default":"warn"}]`,
		`export default function (ctx: any) {
  ctx.on("message.beforeSend", (d: any) => ctx.config.mode === "block" ? { block: "blocked" } : { warn: "careful" })
}`)
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	require.NoError(t, s.SetPluginConfig(t.Context(), "policy", map[string]json.RawMessage{"mode": json.RawMessage(`"block"`)}))
	var be *SendBlockedError
	require.ErrorAs(t, s.SendAcknowledged(t.Context(), d.ID, we.Check.Digest), &be)
	assert.Zero(t, outboxCount(t, s))
}

// beforeSend's 5 s budget starts before the call waits for the plugin's VM:
// a plugin busy with other work gets a no_response warning once it ends.
func TestBeforeSendBudgetIncludesQueue(t *testing.T) {
	clock := &heldClock{}
	testPluginClock = clock
	t.Cleanup(func() { testPluginClock = nil })
	s := newMailContextService(t)
	startTestPlugin(t, s, "busy", sendPerms, checker(`d => undefined`))
	d := sendDraft(t, s, "report")
	release := blockVM(t, s, "busy")
	t.Cleanup(release)
	before := clock.count()

	sent := make(chan error, 1)
	go func() { sent <- s.Send(bg, d.ID) }()
	require.Eventually(t, func() bool { return clock.count() == before+1 }, 5*time.Second, time.Millisecond,
		"the budget is armed while the call waits")
	clock.fire(before)
	var we *SendWarningsError
	select {
	case err := <-sent:
		require.ErrorAs(t, err, &we)
	case <-time.After(5 * time.Second):
		t.Fatal("Send waited for the busy VM")
	}
	assert.Equal(t, []SendWarning{{Plugin: "busy", Code: WarningNoResponse, Message: "plugin busy did not respond"}}, we.Check.Warnings)
	assert.Zero(t, calls(t, s, "busy"), "the handler never ran")
}
