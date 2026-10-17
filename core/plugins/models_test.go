package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadModelsTest(t *testing.T, caps []string, src string) (*Plugin, error) {
	t.Helper()
	return Load(context.Background(), newTestVM(t, `var plugin = {default(ctx){`+src+`}}`, nil), Manifest{Name: "test", Capabilities: caps, Permissions: Permissions{LLM: true, Net: []string{"api.example.com"}}}, &fakeHost{}, testConfig)
}

func TestModelsRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		caps      []string
		want      string
	}{
		{"missing", ``, []string{"models"}, "registered no model provider"},
		{"undeclared", `ctx.models.register({listModels:()=>({models:[]})})`, nil, "models capability"},
		{"duplicate", `const p={listModels:()=>({models:[]})}; ctx.models.register(p);ctx.models.register(p)`, []string{"models"}, "already"},
		{"required", `ctx.models.register({})`, []string{"models"}, "listModels"},
		{"bad optional", `ctx.models.register({listModels:()=>({models:[]}),prepareRequest:42})`, []string{"models"}, "prepareRequest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadModelsTest(t, tc.caps, tc.src)
			require.ErrorContains(t, err, tc.want)
		})
	}
	p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[]})});globalThis.ctx=ctx`)
	require.NoError(t, err)
	got, err := p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
	require.NoError(t, err)
	require.Empty(t, got.Models)
	require.Contains(t, try(t, p, Invocation{Kind: KindAction}, `ctx.models.register({listModels:()=>({models:[]})})`), "initialization")
	hosts, approval := EgressFor(KindInit, p.m, true, false, false)
	require.Empty(t, hosts)
	require.False(t, approval)
	hosts, approval = EgressFor(KindModelsList, p.m, true, true, true)
	require.Equal(t, []string{"api.example.com"}, hosts)
	require.False(t, approval)
}

func TestModelsDescriptionValidation(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		judge        bool
	}{
		{"duplicate", `[{id:"a",name:"A",transport:"http",protocol:"openai-completions",baseURL:"https://api.example.com"},{id:"a",name:"A",transport:"plugin",operation:"judge"}]`, true},
		{"protocol", `[{id:"a",name:"A",transport:"http",protocol:"other",baseURL:"https://api.example.com"}]`, false},
		{"url", `[{id:"a",name:"A",transport:"http",protocol:"openai-completions",baseURL:"https://u:p@api.example.com"}]`, false},
		{"mixed", `[{id:"a",name:"A",transport:"plugin",operation:"judge",baseURL:"https://api.example.com"}]`, true},
		{"empty mixed", `[{id:"a",name:"A",transport:"plugin",operation:"judge",protocol:""}]`, true},
		{"assets", `[{id:"a",name:"A",transport:"plugin",operation:"judge",assets:{model:"4b"}}]`, true},
		{"missing judge", `[{id:"a",name:"A",transport:"plugin",operation:"judge"}]`, false},
		{"unknown field", `[{id:"a",name:"A",transport:"plugin",operation:"judge",secret:"oops"}]`, true},
		{"null list", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := ""
			if tc.judge {
				cb = `,judge:()=>({answers:[]})`
			}
			p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:`+tc.result+`})`+cb+`})`)
			require.NoError(t, err)
			_, err = p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
			require.Error(t, err)
		})
	}
	p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[{id:"org/a",name:"A",transport:"http",protocol:"anthropic-message",baseURL:"https://api.example.com/v1"}]})})`)
	require.NoError(t, err)
	got, err := p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
	require.NoError(t, err)
	require.Equal(t, "org/a", got.Models[0].ID)
	p, err = loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[],padding:"x".repeat(1<<20)})})`)
	require.NoError(t, err)
	_, err = p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestModelsCallbacks(t *testing.T) {
	req := PrepareModelRequest{Model: ProviderModel{ID: "a", Name: "A", Transport: "http", Protocol: "openai-completions", BaseURL: "https://api.example.com"}, Request: ModelHTTPRequest{URL: "https://api.example.com/chat/completions", Headers: map[string]string{}, Body: map[string]any{"model": "a", "stream": true}}}
	p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[]})})`)
	require.NoError(t, err)
	got, err := p.PrepareModel(context.Background(), Invocation{Kind: KindModelsPrepare}, req)
	require.NoError(t, err)
	require.Equal(t, req.Request, got)
	p, err = loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[]}),prepareRequest:()=>{throw {code:"rate_limited",message:"wait",retryable:true}}})`)
	require.NoError(t, err)
	_, err = p.PrepareModel(context.Background(), Invocation{Kind: KindModelsPrepare}, req)
	var business *NativeError
	require.ErrorAs(t, err, &business)
	require.Equal(t, "rate_limited", business.Code)
	p, err = loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>({models:[]}),prepareRequest:async()=>{await ctx.llm.complete("forbidden");return {}}})`)
	require.NoError(t, err)
	_, err = p.PrepareModel(context.Background(), Invocation{Kind: KindModelsPrepare}, req)
	require.ErrorContains(t, err, "permission denied")
	req.Request.Body["prompt"] = strings.Repeat("x", 1<<20)
	_, err = p.PrepareModel(context.Background(), Invocation{Kind: KindModelsPrepare}, req)
	require.True(t, errors.Is(err, ErrTooLarge))
}

func TestNativeModelsFake(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{"models": []map[string]any{{"id": "local", "name": "Local", "transport": "plugin", "operation": "judge"}}, "modelAnswers": []map[string]any{{"type": "predicate", "name": "yes", "probability": 0.9}}})
	n := newFake(t, lib, sha, 0, nil)
	raw, err := n.Call(context.Background(), "models.list", struct{}{})
	require.NoError(t, err)
	require.JSONEq(t, `{"models":[{"id":"local","name":"Local","transport":"plugin","operation":"judge"}]}`, string(raw))
	raw, err = n.Call(context.Background(), "models.prepare", PrepareModelRequest{Request: ModelHTTPRequest{URL: "https://example.com", Headers: map[string]string{}, Body: map[string]any{"model": "a"}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"https://example.com","headers":{},"body":{"model":"a"}}`, string(raw))
	raw, err = n.Call(context.Background(), "models.judge", JudgeModelRequest{ModelID: "local"})
	require.NoError(t, err)
	require.JSONEq(t, `{"answers":[{"type":"predicate","name":"yes","probability":0.9}]}`, string(raw))
}

func TestModelsBusinessErrorsValidateShape(t *testing.T) {
	for _, expr := range []string{`{code:"rate_limited",message:null,retryable:true}`, `{code:"rate_limited",message:"wait",retryable:null}`, `{code:"unsupported_platform",message:"wait",retryable:false}`, `new Error("bug")`} {
		p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:()=>{throw (`+expr+`)}})`)
		require.NoError(t, err)
		_, err = p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
		require.Error(t, err)
		var ne *NativeError
		require.False(t, errors.As(err, &ne))
	}
	p, err := loadModelsTest(t, []string{"models"}, `ctx.models.register({listModels:async()=>{throw {code:"upstream",message:"try again",retryable:true}}})`)
	require.NoError(t, err)
	_, err = p.ListModels(context.Background(), Invocation{Kind: KindModelsList})
	var ne *NativeError
	require.ErrorAs(t, err, &ne)
	require.Equal(t, "upstream", ne.Code)
}

type reentryHost struct {
	fakeHost
	target *Plugin
	action bool
}

func (h *reentryHost) LLMComplete(ctx context.Context, prompt string) (string, error) {
	if h.action {
		_, err := h.target.RunAction(ctx, Invocation{Kind: KindAction}, "call", map[string]any{})
		return "", err
	}
	_, err := h.target.ListModels(ctx, Invocation{Kind: KindModelsList})
	return "", err
}
func TestModelReentry(t *testing.T) {
	h := &reentryHost{}
	p, err := Load(t.Context(), newTestVM(t, `var plugin={default(ctx){ctx.models.register({listModels:()=>({models:[]})});ctx.actions.register({id:"call",label:"Call",on:["selection"],run:async()=>await ctx.llm.complete("again")})}}`, nil), Manifest{Name: "test", Capabilities: []string{"models"}, Permissions: Permissions{LLM: true}}, h, testConfig)
	require.NoError(t, err)
	h.target = p
	_, err = p.RunAction(t.Context(), Invocation{Kind: KindAction}, "call", map[string]any{})
	var business *NativeError
	require.ErrorAs(t, err, &business)
	require.Equal(t, "model_reentry", business.Code)
	_, err = p.ListModels(t.Context(), Invocation{Kind: KindModelsList})
	require.NoError(t, err)
}

func TestModelReentryTwoPlugins(t *testing.T) {
	aHost, bHost := &reentryHost{}, &reentryHost{}
	load := func(name string, h *reentryHost) *Plugin {
		p, err := Load(t.Context(), newTestVM(t, `var plugin={default(ctx){ctx.models.register({listModels:()=>({models:[]})});ctx.actions.register({id:"call",label:"Call",on:["selection"],run:async()=>await ctx.llm.complete("again")})}}`, nil), Manifest{Name: name, Capabilities: []string{"models"}, Permissions: Permissions{LLM: true}}, h, testConfig)
		require.NoError(t, err)
		return p
	}
	a, b := load("a", aHost), load("b", bHost)
	aHost.target = b
	aHost.action = true
	bHost.target = a
	_, err := a.RunAction(t.Context(), Invocation{Kind: KindAction}, "call", map[string]any{})
	var business *NativeError
	require.ErrorAs(t, err, &business)
	require.Equal(t, "model_reentry", business.Code)
}
