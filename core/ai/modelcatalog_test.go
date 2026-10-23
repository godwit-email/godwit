package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modelsDevFixture = `{
"anthropic": {"name": "Anthropic", "models": {"claude-opus-5-5": {"id": "claude-opus-5-5", "name": "Claude Opus 5.5", "limit": {"context": 1000000, "output": 128000}}}},
"openrouter": {"api": "https://openrouter.ai/api/v1", "models": {"anthropic/claude-haiku-4.5": {"name": "Claude Haiku 4.5", "limit": {"context": 200000}}}},
"xai": {"models": {"grok-4.7": {"name": "Grok 4.7", "limit": {"context": 500000}}}},
"zhipuai": {"api": "https://open.bigmodel.cn/api/paas/v4", "models": {"glm-5": {"name": "GLM-5", "limit": {"context": 200000}}}},
"zhipuai-coding-plan": {"api": "https://open.bigmodel.cn/api/coding/paas/v4", "models": {"glm-5": {"limit": {"context": 128000}}}}
}`

func TestModelCatalog(t *testing.T) {
	c, trimmed, err := ParseModelCatalog([]byte(modelsDevFixture))
	require.NoError(t, err)
	assert.NotContains(t, string(trimmed), `"Anthropic"`, "only what Model reads is kept")
	again, _, err := ParseModelCatalog(trimmed)
	require.NoError(t, err)
	assert.Equal(t, c, again, "the trimmed copy parses the same")

	opus := CatalogModel{"Claude Opus 5.5", 1000000}
	for _, tc := range []struct {
		why, baseURL, model string
		want                CatalogModel
	}{
		{"by the service's host", "https://api.x.ai/v1", "grok-4.7", CatalogModel{"Grok 4.7", 500000}},
		{"a host's own ID", "https://openrouter.ai/api/v1", "anthropic/claude-haiku-4.5", CatalogModel{"Claude Haiku 4.5", 200000}},
		{"a shared host is the first provider's", "https://open.bigmodel.cn/api/paas/v4", "glm-5", CatalogModel{"GLM-5", 200000}},
		{"a provider/model ID", "https://gw.example.com/v1", "anthropic/claude-opus-5-5", opus},
		{"by the ID's prefix", "https://gw.example.com/v1", "claude-opus-5-5", opus},
		{"a plugin model", "", "claude-opus-5-5", opus},
	} {
		m, ok := c.Model(tc.baseURL, tc.model)
		assert.True(t, ok, tc.why)
		assert.Equal(t, tc.want, m, tc.why)
	}
	for _, tc := range [][2]string{{"https://gw.example.com/v1", "my-model"}, {"https://api.x.ai/v1", "grok-unknown"}} {
		_, ok := c.Model(tc[0], tc[1])
		assert.False(t, ok, tc[1])
	}
	_, ok := (*ModelCatalog)(nil).Model("https://api.x.ai/v1", "grok-4.7")
	assert.False(t, ok, "none fetched yet")
}
