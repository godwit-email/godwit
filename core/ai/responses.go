package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"

	gai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/core/status"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// responsesPlugin speaks the OpenAI Responses API. It resolves any model ID
// on first use (IDs may hold "/"). Every request is stateless: store is false,
// the whole history goes along, and reasoning comes back encrypted, kept in
// the history as reasoning parts and returned in its place.
type responsesPlugin struct {
	svc   responses.ResponseService
	codex bool // ChatGPT's Codex backend: its own headers and usage codes, and instructions must not be empty
}

// codexBase ends the address of ChatGPT's Codex backend (a Codex sign-in's service).
const codexBase = "/backend-api/codex/"

// newResponsesPlugin reads nothing from the environment: only these options.
func newResponsesPlugin(baseURL, key string, c *http.Client) *responsesPlugin {
	opts := []option.RequestOption{option.WithBaseURL(baseURL), option.WithHTTPClient(c)}
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	codex := strings.HasSuffix(baseURL, codexBase)
	if codex { // as the Codex CLI sends: the account the token is for, and who is asking
		opts = append(opts, option.WithHeader("chatgpt-account-id", codexAccountID(key)),
			option.WithHeader("originator", "godwit"), option.WithHeader("OpenAI-Beta", "responses=experimental"))
	}
	return &responsesPlugin{svc: responses.NewResponseService(opts...), codex: codex}
}

// codexAccountID is the ChatGPT account a Codex access token (a JWT) is for.
func codexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	json.Unmarshal(b, &c)
	return c.Auth.AccountID
}

func (p *responsesPlugin) Name() string                                 { return "godwit-responses" }
func (p *responsesPlugin) Init(context.Context) []api.Action            { return nil }
func (p *responsesPlugin) ListActions(context.Context) []api.ActionDesc { return nil }

func (p *responsesPlugin) ResolveAction(t api.ActionType, id string) api.Action {
	if t != api.ActionTypeModel {
		return nil
	}
	opts := &gai.ModelOptions{Supports: &gai.ModelSupports{Multiturn: true, Tools: true, SystemRole: true}}
	return gai.NewModelAction(api.NewName(p.Name(), id), opts,
		func(ctx context.Context, req *gai.ModelRequest, cfg any, cb gai.ModelStreamCallback) (*gai.ModelResponse, error) {
			return p.generate(ctx, id, req, cfg, cb)
		})
}

// Safe, fixed error texts: never a body, key or Location.
var (
	errRespFailed    = status.Errorf(status.ErrInternal, "the model service failed the reply")
	errRespTruncated = status.Errorf(status.ErrInternal, "the model service ended the reply early")
	errUsageLimit    = status.Errorf(status.ErrResourceExhausted, "the ChatGPT plan's usage is exhausted or unavailable")
	errNotEligible   = status.Errorf(status.ErrPermissionDenied, "the ChatGPT account is not eligible for subscription sharing")
)

// cfg is a shared.ReasoningParam (effortConfig), or nil for no reasoning params.
func (p *responsesPlugin) generate(ctx context.Context, model string, req *gai.ModelRequest, cfg any, cb gai.ModelStreamCallback) (*gai.ModelResponse, error) {
	params, err := responsesParams(model, req)
	if err != nil {
		return nil, err
	}
	if r, ok := cfg.(shared.ReasoningParam); ok {
		params.Reasoning = r
	}
	if p.codex && !params.Instructions.Valid() {
		params.Instructions = param.NewOpt("You are a helpful assistant.") // the backend rejects none, as pi fills in
	}
	stream := p.svc.NewStreaming(ctx, params)
	defer stream.Close()

	parts := map[int64]*gai.Part{} // by output index
	text := map[int64]string{}
	args := map[string]string{} // argument deltas by item ID
	refused := false            // a refusal is a withheld reply, as a chat content_filter
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			d := ev.Delta
			text[ev.OutputIndex] += d
			if cb != nil && d != "" {
				if err := cb(ctx, &gai.ModelResponseChunk{Role: gai.RoleModel, Content: []*gai.Part{gai.NewTextPart(d)}}); err != nil {
					return nil, err
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_summary_part.added":
			d := ev.Delta
			if ev.Type == "response.reasoning_summary_part.added" {
				d = ""
				if ev.SummaryIndex > 0 {
					d = "\n\n" // between a reasoning item's summaries, as the history joins them
				}
			}
			if cb != nil && d != "" {
				if err := cb(ctx, &gai.ModelResponseChunk{Role: gai.RoleModel, Content: []*gai.Part{gai.NewReasoningPart(d, nil)}}); err != nil {
					return nil, err
				}
			}
		case "response.refusal.delta", "response.refusal.done":
			refused = true
		case "response.function_call_arguments.delta":
			args[ev.ItemID] += ev.Delta
		case "response.output_item.done":
			it := ev.Item
			switch it.Type {
			case "message":
				for _, c := range it.Content {
					refused = refused || c.Type == "refusal"
				}
			case "function_call":
				a := it.Arguments.OfString
				if a == "" {
					a = args[it.ID]
				}
				var in any
				if a != "" {
					if json.Unmarshal([]byte(a), &in) != nil {
						return nil, status.Errorf(status.ErrInternal, "the model sent unreadable tool arguments")
					}
				}
				parts[ev.OutputIndex] = gai.NewToolRequestPart(&gai.ToolRequest{Name: it.Name, Ref: it.CallID, Input: in})
			case "reasoning":
				var sum []string
				for _, s := range it.Summary {
					sum = append(sum, s.Text)
				}
				r := gai.NewReasoningPart(strings.Join(sum, "\n\n"), []byte(it.EncryptedContent))
				if r.Metadata == nil {
					r.Metadata = map[string]any{}
				}
				r.Metadata["id"] = it.ID
				parts[ev.OutputIndex] = r
			}
		case "response.completed":
			if refused { // its text and tool requests are not used either
				return &gai.ModelResponse{Request: req, Message: &gai.Message{Role: gai.RoleModel}, FinishReason: gai.FinishReasonBlocked,
					Usage: usageOf(ev.Response.Usage)}, nil
			}
			for i, t := range text {
				if t != "" {
					parts[i] = gai.NewTextPart(t)
				}
			}
			msg := &gai.Message{Role: gai.RoleModel}
			for _, i := range slices.Sorted(maps.Keys(parts)) {
				msg.Content = append(msg.Content, parts[i])
			}
			return &gai.ModelResponse{Request: req, Message: msg, FinishReason: gai.FinishReasonStop, Usage: usageOf(ev.Response.Usage)}, nil
		case "response.incomplete":
			// Cut off: no tool request survives, and the runtime keeps nothing.
			reason := gai.FinishReasonOther
			switch ev.Response.IncompleteDetails.Reason {
			case "max_output_tokens":
				reason = gai.FinishReasonLength
			case "content_filter":
				reason = gai.FinishReasonBlocked
			}
			return &gai.ModelResponse{Request: req, Message: &gai.Message{Role: gai.RoleModel}, FinishReason: reason, Usage: usageOf(ev.Response.Usage)}, nil
		case "response.failed", "error":
			switch ev.Response.Error.Code {
			case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable":
				return nil, errUsageLimit
			}
			return nil, errRespFailed
		}
	}
	if err := stream.Err(); err != nil {
		return nil, respError(ctx, err, p.codex)
	}
	return nil, errRespTruncated // the stream ended with no terminal event
}

// respError classifies a transport or HTTP error without keeping its text.
// codex: the Codex backend's usage errors are the ChatGPT plan's usage limit.
func respError(ctx context.Context, err error, codex bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *openai.Error
	if errors.As(err, &ae) {
		// Sign in with ChatGPT's own codes come as HTTP errors too, not only as response.failed.
		switch ae.Code {
		case "subscription_sharing_user_not_eligible":
			return errNotEligible
		case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable":
			return errUsageLimit
		case "usage_limit_reached", "usage_not_included", "rate_limit_exceeded":
			if codex {
				return errUsageLimit
			}
		}
		if codex && ae.StatusCode == http.StatusTooManyRequests {
			return errUsageLimit
		}
		switch c := ae.StatusCode; {
		case c == http.StatusUnauthorized || c == http.StatusForbidden:
			return status.Errorf(status.ErrUnauthenticated, "the model service rejected the API key")
		case c == http.StatusTooManyRequests:
			return status.Errorf(status.ErrResourceExhausted, "the model service is rate limiting requests")
		case c >= 500:
			return status.Errorf(status.ErrUnavailable, "the model service is unavailable")
		}
		return status.Errorf(status.ErrInternal, "the model service refused the request (HTTP %d)", ae.StatusCode)
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return status.Errorf(status.ErrUnavailable, "could not reach the model service")
	}
	return status.Errorf(status.ErrInternal, "the model service returned an unusable response")
}

// responsesParams maps a Genkit request to a stateless Responses request.
func responsesParams(model string, req *gai.ModelRequest) (responses.ResponseNewParams, error) {
	var system []string
	var items responses.ResponseInputParam
	for _, m := range req.Messages {
		var buf strings.Builder
		flush := func() {
			if buf.Len() == 0 {
				return
			}
			role := responses.EasyInputMessageRoleUser
			if m.Role == gai.RoleModel {
				role = responses.EasyInputMessageRoleAssistant
			}
			items = append(items, responses.ResponseInputItemParamOfMessage(buf.String(), role))
			buf.Reset()
		}
		for _, p := range m.Content {
			switch {
			case m.Role == gai.RoleSystem:
				system = append(system, p.Text)
			case p.IsText():
				buf.WriteString(p.Text)
			case p.IsReasoning():
				flush()
				id, _ := p.Metadata["id"].(string)
				items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{
					ID: id, Summary: []responses.ResponseReasoningItemSummaryParam{}, EncryptedContent: param.NewOpt(signature(p))}})
			case p.IsToolRequest():
				flush()
				a, err := json.Marshal(p.ToolRequest.Input)
				if err != nil || p.ToolRequest.Input == nil {
					a = []byte("{}")
				}
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(string(a), p.ToolRequest.Ref, p.ToolRequest.Name))
			case p.IsToolResponse():
				flush()
				out, err := json.Marshal(p.ToolResponse.Output)
				if err != nil {
					return responses.ResponseNewParams{}, status.Errorf(status.ErrInternal, "ai: tool output is not JSON")
				}
				item := responses.ResponseInputItemParamOfFunctionCallOutput(string(out))
				item.OfFunctionCallOutput.CallID = param.NewOpt(p.ToolResponse.Ref)
				items = append(items, item)
			}
		}
		flush()
	}
	params := responses.ResponseNewParams{
		Model:   model,
		Store:   param.NewOpt(false),
		Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		Input:   responses.ResponseNewParamsInputUnion{OfInputItemList: items},
	}
	if len(system) > 0 {
		params.Instructions = param.NewOpt(strings.Join(system, "\n"))
	}
	for _, t := range req.Tools {
		strict, _ := t.Metadata["strict"].(bool) // plugin and MCP tools are not strict
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if strict {
			schema = strictSchema(schema)
		}
		params.Tools = append(params.Tools, responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
			Name: t.Name, Description: param.NewOpt(t.Description), Parameters: schema, Strict: param.NewOpt(strict)}})
	}
	return params, nil
}

// signature is a reasoning part's encrypted content: bytes as Genkit made
// them, or base64 once the state went through JSON.
func signature(p *gai.Part) string {
	switch s := p.Metadata["signature"].(type) {
	case []byte:
		return string(s)
	case string:
		b, _ := base64.StdEncoding.DecodeString(s)
		return string(b)
	}
	return ""
}

// usageOf is a Responses response's token usage under Genkit's convention:
// InputTokens is the whole prompt, cached tokens a part of it, and
// OutputTokens leaves out the reasoning, which ThoughtsTokens counts.
func usageOf(u responses.ResponseUsage) *gai.GenerationUsage {
	reasoning := int(u.OutputTokensDetails.ReasoningTokens)
	return &gai.GenerationUsage{InputTokens: int(u.InputTokens), OutputTokens: int(u.OutputTokens) - reasoning,
		ThoughtsTokens: reasoning, CachedContentTokens: int(u.InputTokensDetails.CachedTokens), TotalTokens: int(u.TotalTokens)}
}
