package plugins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modelsPlugin = `var plugin = {default(ctx) {
	ctx.models.register({
		listModels: () => ({ models: [{ id: "m", name: "M", transport: "http", protocol: "openai-completions", baseURL: "https://api.example.com/v1" }] }),
		prepareRequest: (req) => ({ url: req.url, headers: { ...req.headers, "x-key": "k:" + req.modelId }, body: req.body.replace("hi", "hello") }),
		judge: (req) => ({ answers: req.questions.map((q) => ({ type: "predicate", name: q.name, probability: 0.75 })) }),
	})
}}`

func TestModelsRegistration(t *testing.T) {
	declares := Manifest{Name: "test", Capabilities: []string{"models"}}
	load := func(m Manifest, src string) (*Plugin, error) {
		return Load(context.Background(), newTestVM(t, src, VMOptions{}), m, &fakeHost{}, testConfig)
	}
	p, err := load(declares, modelsPlugin)
	require.NoError(t, err)
	inv := Invocation{ID: "m", Kind: KindModels, Config: testConfig}
	got, err := p.ListModels(context.Background(), inv)
	require.NoError(t, err)
	assert.Equal(t, "openai-completions", got.(map[string]any)["models"].([]any)[0].(map[string]any)["protocol"])

	require.True(t, p.Prepares())
	got, err = p.PrepareRequest(context.Background(), inv, ModelRequest{ModelID: "m", Method: "POST", URL: "https://api.example.com/v1/chat/completions",
		Headers: map[string]string{"content-type": "application/json"}, Body: `{"say":"hi"}`})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"url": "https://api.example.com/v1/chat/completions",
		"headers": map[string]any{"content-type": "application/json", "x-key": "k:m"}, "body": `{"say":"hello"}`}, got)

	got, err = p.JudgeModel(context.Background(), inv, JudgeModelRequest{ModelID: "j", Input: "x", Questions: []Question{{Name: "q", Type: "predicate", Instructions: "?"}}})
	require.NoError(t, err)
	assert.Equal(t, 0.75, got.(map[string]any)["answers"].([]any)[0].(map[string]any)["probability"])

	_, err = load(declares, `var plugin = {default(ctx) {}}`)
	assert.ErrorContains(t, err, "registered none")
	_, err = load(Manifest{Name: "test"}, modelsPlugin)
	assert.ErrorContains(t, err, "models capability")
	_, err = load(declares, `var plugin = {default(ctx) {
		const m = { listModels: () => ({ models: [] }) }
		ctx.models.register(m); ctx.models.register(m)
	}}`)
	assert.ErrorContains(t, err, "already")
	p, err = load(declares, `var plugin = {default(ctx) { ctx.models.register({ listModels: () => ({ models: [] }) }) }}`)
	require.NoError(t, err)
	assert.False(t, p.Prepares(), "prepareRequest is optional")
}

func TestValidateModels(t *testing.T) {
	http := func(mod func(*ProviderModel)) ModelsResult {
		m := ProviderModel{ID: "m", Name: "M", Transport: "http", Protocol: "anthropic-message", BaseURL: "https://api.example.com/v1"}
		mod(&m)
		return ModelsResult{Models: []ProviderModel{m}}
	}
	judge := ProviderModel{ID: "j", Name: "J", Transport: "plugin", Operation: "judge", Assets: map[string]string{"model": "4b"}}
	assert.NoError(t, ValidateModels(http(func(*ProviderModel) {}), false))
	assert.NoError(t, ValidateModels(ModelsResult{Models: []ProviderModel{judge}}, true))
	assert.NoError(t, ValidateModels(http(func(m *ProviderModel) { m.BaseURL = "http://127.0.0.1:8080/v1" }), true), "a native plugin's local server")
	for name, r := range map[string]ModelsResult{
		"no id":         http(func(m *ProviderModel) { m.ID = "" }),
		"no name":       http(func(m *ProviderModel) { m.Name = " " }),
		"protocol":      http(func(m *ProviderModel) { m.Protocol = "gemini" }),
		"http base":     http(func(m *ProviderModel) { m.BaseURL = "http://api.example.com/v1" }),
		"query":         http(func(m *ProviderModel) { m.BaseURL = "https://api.example.com/v1?k=1" }),
		"credentials":   http(func(m *ProviderModel) { m.BaseURL = "https://u:p@api.example.com/v1" }),
		"http assets":   http(func(m *ProviderModel) { m.Assets = map[string]string{"a": "b"} }),
		"transport":     http(func(m *ProviderModel) { m.Transport = "grpc" }),
		"script assets": {Models: []ProviderModel{judge}},
		"repeated":      {Models: []ProviderModel{judge, judge}},
		"operation":     {Models: []ProviderModel{{ID: "x", Name: "X", Transport: "plugin", Operation: "generate"}}},
	} {
		assert.Error(t, ValidateModels(r, name == "repeated"), name)
	}
	assert.True(t, judge.Judges())
	assert.True(t, ProviderModel{Transport: "http", Protocol: "openai-decisions"}.Judges())
	assert.False(t, ProviderModel{Transport: "http", Protocol: "openai-responses"}.Judges())
}
