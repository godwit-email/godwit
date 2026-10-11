package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretSet(t *testing.T) {
	var set secretSet
	assert.Empty(t, set.Values())
	set.Add("alpha-secret-1", "alpha-secret-2")
	set.Add("mcp-header-val")
	set.Add("alpha-secret-3", "alpha-secret-1")
	got := set.Values()
	slices.Sort(got)
	assert.Equal(t, []string{"alpha-secret-1", "alpha-secret-2", "alpha-secret-3", "mcp-header-val"}, got, "once each")
	assert.Equal(t, "key [redacted] and [redacted], short", set.Redact("key alpha-secret-2 and mcp-header-val, short"))

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 100 {
				set.Add(fmt.Sprint("value-", i, "-", j))
				_ = set.Values()
			}
		})
	}
	wg.Wait()
	assert.Len(t, set.Values(), 4+800)
}

// TestPluginSecretRedactedByEngine: a value added to the Service's secret set
// after the engine exists is redacted from what the engine hands back.
func TestPluginSecretRedactedByEngine(t *testing.T) {
	e := newSkillEnv(t)
	_, err := e.s.aiEngine()
	require.NoError(t, err)
	e.s.secrets.Add("plugin-sekret-42")
	e.s.registerTool(toolDesc{Spec: ai.ToolSpec{Name: "leaky", Description: "Leaks.", Schema: object(map[string]any{}),
		Run: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("upstream rejected plugin-sekret-42 and sekret-key-7")
		}}})
	e.srv.Script(testllm.Turn{Calls: []testllm.Call{{Name: "leaky", Args: `{}`}}}, testllm.Turn{Text: []string{"ok"}})
	ch, err := e.s.startRun(t.Context(), e.askConv(t), "go", e.s.registered("leaky"))
	require.NoError(t, err)
	evs := drainWit(t, ch)
	require.NotEmpty(t, evs)
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
	rs := witResults(evs, "leaky")
	require.Len(t, rs, 1)
	assert.Equal(t, "upstream rejected [redacted] and [redacted]", rs[0].Result.Error)
	reqs := e.srv.Requests()
	require.Len(t, reqs, 2)
	assert.NotContains(t, string(reqs[1].Messages), "plugin-sekret-42")
}
