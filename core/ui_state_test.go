package core

import (
	"context"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Wit run asked over the Local API waits on the user's approvals, so it
// does not start while no interface is connected to take them (ADR 0038);
// with one connected it runs, and Godwit's own callers never need one.
func TestExternalWitRunNeedsUI(t *testing.T) {
	e := newAssistEnv(t)
	conv := e.askConv(t)
	d := e.replyDraft(t, "<p>Yes.</p>")
	ext := WithExternalSource(t.Context(), AuditLocalAPI)

	_, err := e.s.Ask(ext, conv.ID, "When do we meet?")
	require.ErrorIs(t, err, ErrNoUI)
	_, err = e.s.Assist(ext, d.ID, "shorter")
	require.ErrorIs(t, err, ErrNoUI)
	_, err = e.s.AssistSkill(ext, d.ID, "polish", SkillOptions{})
	require.ErrorIs(t, err, ErrNoUI)
	assert.Empty(t, e.srv.Requests(), "no run started")

	ui, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	WithUISession(ui, e.s)
	e.srv.Script(testllm.Turn{Text: []string{"Friday."}})
	ch, err := e.s.Ask(ext, conv.ID, "When do we meet?")
	require.NoError(t, err)
	evs := drainWit(t, ch)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	disconnect()
	require.Eventually(t, func() bool { return !e.s.UIConnected() }, time.Second, time.Millisecond)

	e.srv.Script(testllm.Turn{Text: []string{"Friday."}})
	evs = e.ask(t, conv.ID, "And where?") // in-process: Godwit's own caller
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
}

// The tray's 退出并停止收信 stays armed while the interface shows its
// unsaved-changes prompt (which brings it to the front): only an explicit
// cancel disarms it.
func TestTrayQuitStopSurvivesForeground(t *testing.T) {
	e := newSyncEnv(t)
	ui, disconnect := context.WithCancel(t.Context())
	defer disconnect()
	ui = WithUISession(ui, e.s)
	stopped := 0
	e.s.StopWhenUIQuits(func() { stopped++ })
	require.NoError(t, e.s.SetUIForeground(ui, true)) // the prompt shows the window
	require.NoError(t, e.s.StopUIBackend(ui))         // the user confirms the quit
	assert.Equal(t, 1, stopped, "the confirmed quit stops the backend")

	e.s.StopWhenUIQuits(func() { stopped++ })
	require.NoError(t, e.s.CancelTrayQuit(ui)) // the user went back
	disconnect()
	require.Eventually(t, func() bool { return !e.s.UIConnected() }, time.Second, time.Millisecond)
	assert.Equal(t, 1, stopped, "a cancelled quit leaves the backend running")
}
