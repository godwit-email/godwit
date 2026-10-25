package plugins

// Models a plugin offers (capability "models"): HTTP models Godwit runs with
// the SDK it already uses for the model's protocol, letting the plugin adjust
// each encoded request, and judge models the plugin answers itself.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Calcium-Ion/moejs"
)

// Protocols of HTTP models: the first three generate, the last two judge.
var (
	GenerateProtocols = []string{"openai-completions", "openai-responses", "anthropic-message"}
	JudgeProtocols    = []string{"typesafe-systemone", "openai-decisions"}
)

// ProviderModel is one model of a plugin. An "http" model has Protocol and
// BaseURL; a "plugin" model has Operation "judge" and, for a native plugin,
// Assets: registry asset group to variant ID. Description is an optional
// line about the model on this computer ("slow here"), shown in settings.
type ProviderModel struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Transport   string            `json:"transport"`
	Protocol    string            `json:"protocol,omitempty"`
	BaseURL     string            `json:"baseURL,omitempty"`
	Operation   string            `json:"operation,omitempty"`
	Assets      map[string]string `json:"assets,omitempty"`
	Description Text              `json:"description"`
}

// Judges reports whether m answers judge questions rather than generating.
func (m ProviderModel) Judges() bool {
	return m.Transport == "plugin" || slices.Contains(JudgeProtocols, m.Protocol)
}

type ModelsResult struct {
	Models []ProviderModel `json:"models"`
}

// ModelRequest is an HTTP request an SDK encoded for a plugin's model: the
// plugin's prepareRequest gets it and returns it with its URL, headers or
// body changed. Body is the request's JSON text.
type ModelRequest struct {
	ModelID string            `json:"modelId"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// JudgeModelRequest is a plugin judge call: one question in practice.
type JudgeModelRequest struct {
	ModelID   string     `json:"modelId"`
	Input     string     `json:"input"`
	Questions []Question `json:"questions"`
}

type JudgeModelResult struct {
	Answers []Answer `json:"answers"`
}

const maxModels = 200

// ValidateModels checks a plugin's model list. native: the plugin may name
// assets and plain-http base URLs (a local server), as a configured service may.
func ValidateModels(r ModelsResult, native bool) error {
	if len(r.Models) > maxModels {
		return fmt.Errorf("more than %d models", maxModels)
	}
	seen := map[string]bool{}
	for _, m := range r.Models {
		bad := func(why string) error { return fmt.Errorf("model %q: %s", m.ID, why) }
		if m.ID == "" || len(m.ID) > 200 || strings.ContainsAny(m.ID, "\r\n") || seen[m.ID] {
			return bad("the id is empty, too long, multi-line or repeated")
		}
		seen[m.ID] = true
		if strings.TrimSpace(m.Name) == "" {
			return bad("no name")
		}
		switch m.Transport {
		case "http":
			u, err := url.Parse(m.BaseURL)
			switch {
			case !slices.Contains(GenerateProtocols, m.Protocol) && !slices.Contains(JudgeProtocols, m.Protocol):
				return bad("unknown protocol " + m.Protocol)
			case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
				u.Scheme != "https" && !(native && u.Scheme == "http"):
				return bad("baseURL must be an https URL without credentials, query or fragment")
			case m.Operation != "" || m.Assets != nil:
				return bad("an http model has no operation or assets")
			}
		case "plugin":
			switch {
			case m.Operation != "judge":
				return bad(`a plugin model's operation must be "judge"`)
			case m.Protocol != "" || m.BaseURL != "":
				return bad("a plugin model has no protocol or baseURL")
			case m.Assets != nil && (!native || len(m.Assets) == 0):
				return bad("only a native plugin's model may name assets")
			}
		default:
			return bad(`transport must be "http" or "plugin"`)
		}
	}
	return nil
}

// modelProvider is what ctx.models.register registered; prepare and judge
// are optional (zero: none).
type modelProvider struct{ list, prepare, judge moejs.Value }

func (p *Plugin) registerModels(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	const fn = "models.register"
	if err := duringInit(c); err != nil {
		return undef, err
	}
	if !slices.Contains(p.m.Capabilities, "models") {
		return undef, typeError(fn + ": plugin.json does not declare the models capability")
	}
	o := moejs.Arg(args, 0)
	list, err := c.function(fn, o, "listModels")
	if err != nil {
		return undef, err
	}
	mp := &modelProvider{list: list}
	for k, dst := range map[string]*moejs.Value{"prepareRequest": &mp.prepare, "judge": &mp.judge} {
		v, err := c.vm.rt.Get(o, k)
		if err == nil && !v.IsUndefined() {
			*dst, err = c.function(fn, o, k)
		}
		if err != nil {
			return undef, err
		}
	}
	if p.models != nil {
		return undef, errors.New("models are already registered")
	}
	p.models = mp
	return undef, nil
}

// ListModels runs the registered listModels under inv.
func (p *Plugin) ListModels(ctx context.Context, inv Invocation) (any, error) {
	return p.call(ctx, inv, p.models.list, false)
}

// Prepares reports whether the plugin adjusts its models' requests.
func (p *Plugin) Prepares() bool { return p.models != nil && isFunction(p.models.prepare) }

// PrepareRequest runs the registered prepareRequest on req under inv.
func (p *Plugin) PrepareRequest(ctx context.Context, inv Invocation, req ModelRequest) (any, error) {
	if !p.Prepares() {
		return nil, fmt.Errorf("plugin %s has no prepareRequest", p.Name)
	}
	return p.call(ctx, inv, p.models.prepare, false, req)
}

// JudgeModel runs the registered judge on req under inv.
func (p *Plugin) JudgeModel(ctx context.Context, inv Invocation, req JudgeModelRequest) (any, error) {
	if p.models == nil || !isFunction(p.models.judge) {
		return nil, fmt.Errorf("plugin %s has no judge", p.Name)
	}
	return p.call(ctx, inv, p.models.judge, false, req)
}
