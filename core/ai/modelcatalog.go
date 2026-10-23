package ai

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
)

// ModelsDevURL is models.dev's catalog of providers and their models.
const ModelsDevURL = "https://models.dev/api.json"

// ModelCatalog is what models.dev says of models: their display names and
// context windows, by provider, and its providers by API host. A nil one
// knows none.
type ModelCatalog struct {
	byHost map[string]string
	models map[string]map[string]CatalogModel
}

// CatalogModel is a model as models.dev lists it.
type CatalogModel struct {
	Name    string
	Context int // tokens; 0 unknown
}

// modelsDev is the part of models.dev's api.json ModelCatalog reads.
type modelsDev map[string]struct {
	API    string `json:"api,omitempty"`
	Models map[string]struct {
		Name  string `json:"name,omitempty"`
		Limit struct {
			Context int `json:"context"`
		} `json:"limit"`
	} `json:"models"`
}

// knownHosts are the API hosts of providers models.dev gives no "api" for.
var knownHosts = map[string][]string{
	"anthropic": {"api.anthropic.com"},
	"openai":    {"api.openai.com", "chatgpt.com"},
	"xai":       {"api.x.ai"},
	"google":    {"generativelanguage.googleapis.com"},
}

// ParseModelCatalog reads models.dev's api.json. trimmed is the part it read,
// as small a copy to cache and parse again.
func ParseModelCatalog(data []byte) (c *ModelCatalog, trimmed []byte, err error) {
	var md modelsDev
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, nil, err
	}
	c = &ModelCatalog{byHost: map[string]string{}, models: map[string]map[string]CatalogModel{}}
	ids := make([]string, 0, len(md))
	for id := range md {
		ids = append(ids, id)
	}
	slices.Sort(ids) // a host shared, as by a coding plan's provider, is the first's by ID
	for _, id := range ids {
		p := md[id]
		hosts := knownHosts[id]
		if u, err := url.Parse(p.API); err == nil && u.Host != "" {
			hosts = append(hosts, u.Host)
		}
		for _, h := range hosts {
			if _, ok := c.byHost[h]; !ok {
				c.byHost[h] = id
			}
		}
		c.models[id] = map[string]CatalogModel{}
		for m, v := range p.Models {
			c.models[id][m] = CatalogModel{Name: v.Name, Context: v.Limit.Context}
		}
	}
	trimmed, err = json.Marshal(md)
	return c, trimmed, err
}

// vendorPrefixes name the provider a model ID's prefix belongs to, for a
// service models.dev does not know, such as a proxy.
var vendorPrefixes = []struct{ prefix, provider string }{
	{"claude-", "anthropic"}, {"gpt-", "openai"}, {"grok-", "xai"}, {"gemini-", "google"},
	{"deepseek-", "deepseek"}, {"kimi-", "moonshotai"}, {"glm-", "zhipuai"}, {"qwen", "alibaba"}, {"MiniMax-", "minimax"},
}

// Model is model on the service at baseURL; ok false when unknown. A service
// models.dev does not list is taken for the provider a "provider/model" ID or
// the ID's prefix names.
func (c *ModelCatalog) Model(baseURL, model string) (m CatalogModel, ok bool) {
	if c == nil {
		return CatalogModel{}, false
	}
	if u, err := url.Parse(baseURL); err == nil {
		if m, ok := c.models[c.byHost[u.Host]][model]; ok {
			return m, true
		}
	}
	if vendor, id, cut := strings.Cut(model, "/"); cut {
		if m, ok := c.models[vendor][id]; ok {
			return m, true
		}
		model = id
	}
	for _, v := range vendorPrefixes {
		if strings.HasPrefix(model, v.prefix) {
			m, ok := c.models[v.provider][model]
			return m, ok
		}
	}
	return CatalogModel{}, false
}
