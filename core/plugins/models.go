package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/dop251/goja"
)

// ProviderModel is the wire description shared by script and native providers.
// Only the fields belonging to Transport may be present.
type ProviderModel struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Protocol  string            `json:"protocol,omitempty"`
	BaseURL   string            `json:"baseURL,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Assets    map[string]string `json:"assets,omitempty"`
}

func (m *ProviderModel) UnmarshalJSON(b []byte) error {
	type wire ProviderModel
	var v wire
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return errors.New("invalid model description")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return errors.New("invalid model description")
	}
	forbidden := []string{"operation", "assets"}
	if v.Transport == "plugin" {
		forbidden = []string{"protocol", "baseURL"}
	}
	for _, key := range forbidden {
		if _, ok := fields[key]; ok {
			return errors.New("model fields do not match transport")
		}
	}
	*m = ProviderModel(v)
	return nil
}

type ModelsResult struct {
	Models []ProviderModel `json:"models"`
}
type ModelHTTPRequest struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    map[string]any    `json:"body"`
}
type PrepareModelRequest struct {
	Model            ProviderModel    `json:"model"`
	SessionID        string           `json:"sessionId"`
	SessionCreatedAt int64            `json:"sessionCreatedAt"`
	RequestID        string           `json:"requestId"`
	Request          ModelHTTPRequest `json:"request"`
}
type ModelLoadRequest struct {
	ModelID string            `json:"modelId"`
	Assets  map[string]string `json:"assets"`
}
type JudgeModelRequest struct {
	ModelID   string     `json:"modelId"`
	Input     string     `json:"input"`
	Questions []Question `json:"questions"`
}
type JudgeModelResult struct {
	Answers []Answer `json:"answers"`
}

func ProtocolPath(protocol string) string {
	switch protocol {
	case "openai-completions":
		return "/chat/completions"
	case "openai-responses":
		return "/responses"
	case "anthropic-message":
		return "/messages"
	case "typesafe-systemone":
		return "/systemone"
	case "openai-decisions":
		return "/decisions"
	}
	return ""
}

// ValidateModels checks a whole catalog before the caller publishes it.
func ValidateModels(r ModelsResult, native, hasJudge, hasLoad bool) error {
	if r.Models == nil {
		return errors.New("models must be an array")
	}
	seen := map[string]bool{}
	for _, m := range r.Models {
		if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Name) == "" || strings.ContainsAny(m.ID, "\x00\r\n") || seen[m.ID] {
			return errors.New("invalid or duplicate model identity")
		}
		seen[m.ID] = true
		switch m.Transport {
		case "http":
			u, err := url.Parse(m.BaseURL)
			if ProtocolPath(m.Protocol) == "" || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || m.Operation != "" || m.Assets != nil {
				return errors.New("invalid HTTP model")
			}
		case "plugin":
			if m.Operation != "judge" || !hasJudge || m.Protocol != "" || m.BaseURL != "" {
				return errors.New("invalid plugin judge model")
			}
			if m.Assets != nil && (!native || !hasLoad || len(m.Assets) == 0) {
				return errors.New("managed assets require native models.load")
			}
			for k, v := range m.Assets {
				if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
					return errors.New("invalid model asset reference")
				}
			}
		default:
			return errors.New("unknown model transport")
		}
	}
	return nil
}

func (p *Plugin) registerModels(c *CallScope, call goja.FunctionCall) goja.Value {
	const fn = "models.register"
	duringInit(c)
	if !slices.Contains(p.m.Capabilities, "models") {
		panic(c.Runtime().NewTypeError(fn + ": plugin.json does not declare the models capability"))
	}
	if p.modelsList != nil {
		panic(errors.New("a model provider is already registered"))
	}
	o := call.Argument(0).ToObject(c.Runtime())
	list := function(c, fn, o, "listModels")
	optionalFn := func(key string) goja.Callable {
		if optional(o.Get(key)) {
			return nil
		}
		return function(c, fn, o, key)
	}
	prepare, judge := optionalFn("prepareRequest"), optionalFn("judge")
	p.modelsList, p.modelsPrepare, p.modelsJudge = list, prepare, judge
	return goja.Undefined()
}

func (p *Plugin) callModel(ctx context.Context, inv Invocation, f goja.Callable, out any, args ...any) error {
	if f == nil {
		return errors.New("model method is not supported")
	}
	vals := make([]any, len(args))
	for i, a := range args {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if len(b) > MaxResultBytes {
			return ErrTooLarge
		}
		if err = json.Unmarshal(b, &vals[i]); err != nil {
			return err
		}
	}
	result, err := p.call(ctx, inv, f, false, vals...)
	if err != nil {
		return err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return errors.New("invalid model result")
	}
	if len(b) > MaxResultBytes {
		return ErrTooLarge
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return fmt.Errorf("invalid model result: %w", err)
	}
	return nil
}

func (p *Plugin) ListModels(ctx context.Context, inv Invocation) (ModelsResult, error) {
	var result ModelsResult
	if err := p.callModel(ctx, inv, p.modelsList, &result); err != nil {
		return ModelsResult{}, err
	}
	if err := ValidateModels(result, false, p.modelsJudge != nil, false); err != nil {
		return ModelsResult{}, err
	}
	return result, nil
}
func (p *Plugin) PrepareModel(ctx context.Context, inv Invocation, req PrepareModelRequest) (ModelHTTPRequest, error) {
	if p.modelsPrepare == nil {
		if _, err := normalize(req); err != nil {
			return ModelHTTPRequest{}, err
		}
		return req.Request, nil
	}
	var result ModelHTTPRequest
	err := p.callModel(ctx, inv, p.modelsPrepare, &result, req)
	return result, err
}
func (p *Plugin) JudgeModel(ctx context.Context, inv Invocation, req JudgeModelRequest) (JudgeModelResult, error) {
	var result JudgeModelResult
	err := p.callModel(ctx, inv, p.modelsJudge, &result, req)
	return result, err
}

func modelInvocation(kind InvocationKind) bool {
	return kind == KindModelsList || kind == KindModelsPrepare || kind == KindModelsJudge
}

func modelBusinessCode(code string) bool {
	return slices.Contains([]string{"invalid_request", "model_unavailable", "model_not_ready", "model_reentry", "rate_limited", "upstream", "internal"}, code)
}

// modelBusinessError preserves the small JSON error contract without exposing Go values.
func (v *VM) modelBusinessError(val goja.Value) *NativeError {
	if val == nil || goja.IsNull(val) || goja.IsUndefined(val) {
		return nil
	}
	s, err := v.stringify(goja.Undefined(), val)
	if err != nil || goja.IsUndefined(s) || len(s.String()) > MaxResultBytes {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(s.String()), &obj) != nil || len(obj) != 3 {
		return nil
	}
	code, codeOK := obj["code"].(string)
	message, messageOK := obj["message"].(string)
	retryable, retryableOK := obj["retryable"].(bool)
	if !codeOK || !messageOK || !retryableOK || !modelBusinessCode(code) {
		return nil
	}
	message, _ = cut(v.clean(message), 1024)
	return &NativeError{Code: code, Message: message, Retryable: retryable}
}

type pluginCallChainKey struct{}

// CheckModelReentry rejects a provider whose VM is already awaiting this call chain.
func CheckModelReentry(ctx context.Context, name string) error {
	chain, _ := ctx.Value(pluginCallChainKey{}).([]string)
	if name != "" && slices.Contains(chain, name) {
		return &NativeError{Code: "model_reentry", Message: "A model provider cannot reenter its own plugin", Retryable: false}
	}
	return nil
}
func withPluginCall(ctx context.Context, name string) context.Context {
	chain, _ := ctx.Value(pluginCallChainKey{}).([]string)
	next := append(slices.Clone(chain), name)
	return context.WithValue(ctx, pluginCallChainKey{}, next)
}
