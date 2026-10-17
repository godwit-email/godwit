package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginModelsStaleRefresh(t *testing.T) {
	e := newApprovalEnv(t)
	startTestPlugin(t, e.s, "catalog", `{},"capabilities":["models"]`, `export default function(ctx){ctx.models.register({async listModels(){await ctx.storage.get("barrier");return {models:[{id:"m",name:"M",transport:"http",protocol:"openai-responses",baseURL:"https://example.com"}]}}})}`)
	beforePluginStorageGet = func(name string) { require.NoError(t, e.s.SetPluginEnabled(bg, name, false)) }
	t.Cleanup(func() { beforePluginStorageGet = nil })
	models, err := e.s.ListModels(bg)
	require.NoError(t, err)
	for _, m := range models {
		require.False(t, strings.HasPrefix(m.Ref, "catalog/"))
	}
	e.s.pluginModelsMu.Lock()
	defer e.s.pluginModelsMu.Unlock()
	require.Empty(t, e.s.pluginModels["catalog"].snap.models)
}
