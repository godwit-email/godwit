package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"testing/fstest"

	ant "github.com/anthropics/anthropic-sdk-go/option"
	gai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/core/logger"
	"github.com/firebase/genkit/go/core/status"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	oai "github.com/openai/openai-go/option"
)

// Engine runs models through Genkit: one Genkit instance per model service
// configuration, created on first use. Genkit types never leave this package.
type Engine struct {
	log     *slog.Logger // what Genkit logs through: Error and above, redacted
	secrets func() []string
	client  *http.Client // never follows redirects; Close drops its connections

	mu     sync.Mutex
	insts  map[int64]*instance
	closed bool

	keysMu sync.Mutex
	keys   map[string]bool // every API key handed to Genkit, redacted from its logs

	inits atomic.Int64 // genkit.Init calls, for tests
}

type instance struct {
	cfg      ResolvedModel // Model cleared
	g        *genkit.Genkit
	provider string
	cancel   context.CancelFunc
}

// noPrompts keeps Init from loading ./prompts from the working directory.
var noPrompts = fstest.MapFS{"prompts": {Mode: fs.ModeDir}}

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// NewEngine refuses GENKIT_ENV=dev, which would start Genkit's reflection
// server. logger receives Genkit's error logs with every key from secrets (and
// every key the engine has used) redacted.
func NewEngine(l *slog.Logger, secrets func() []string) (*Engine, error) {
	if api.CurrentEnvironment() == api.EnvironmentDev {
		return nil, errors.New("ai: refusing to run with GENKIT_ENV=dev")
	}
	if l == nil {
		l = slog.Default()
	}
	e := &Engine{
		secrets: secrets,
		client:  &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: NoRedirectClient.CheckRedirect},
		insts:   map[int64]*instance{},
		keys:    map[string]bool{},
	}
	e.log = slog.New(&redactor{l.Handler(), e.allSecrets})
	return e, nil
}

func (e *Engine) allSecrets() []string {
	var out []string
	if e.secrets != nil {
		out = e.secrets()
	}
	e.keysMu.Lock()
	defer e.keysMu.Unlock()
	for k := range e.keys {
		out = append(out, k)
	}
	return out
}

func (e *Engine) logCtx(ctx context.Context) context.Context { return logger.WithContext(ctx, e.log) }

// Forget drops the service's instance (the service changed or was removed).
func (e *Engine) Forget(serviceID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.insts[serviceID]; in != nil {
		in.cancel()
		delete(e.insts, serviceID)
	}
}

// Close drops every instance; later runs fail. Runs in flight end with their ctx.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	for id, in := range e.insts {
		in.cancel()
		delete(e.insts, id)
	}
	e.mu.Unlock()
	e.client.CloseIdleConnections()
}

func (e *Engine) instance(m ResolvedModel) (in *instance, err error) {
	cfg := m
	cfg.Model = ""
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("ai: engine closed")
	}
	if in := e.insts[m.ServiceID]; in != nil {
		if in.cfg == cfg {
			return in, nil
		}
		in.cancel()
		delete(e.insts, m.ServiceID)
	}
	if api.CurrentEnvironment() == api.EnvironmentDev {
		return nil, errors.New("ai: refusing to run with GENKIT_ENV=dev")
	}
	var plugin api.Plugin
	switch m.Kind {
	case "openai":
		// compat_oai clears the identity the SDK reads from the environment.
		plugin = &compat_oai.OpenAICompatible{Provider: "godwit", APIKey: m.APIKey, BaseURL: m.BaseURL,
			Opts: []oai.RequestOption{oai.WithHTTPClient(e.client)}}
	case "anthropic":
		// The SDK also reads ANTHROPIC_AUTH_TOKEN and sends it as Authorization.
		key, opts := m.APIKey, []ant.RequestOption{ant.WithHTTPClient(e.client), ant.WithHeaderDel("Authorization")}
		if key == "" { // a local gateway: no auth at all
			key, opts = "godwit-no-key", append(opts, ant.WithHeaderDel("X-Api-Key"))
		}
		plugin = &anthropic.Anthropic{APIKey: key, BaseURL: m.BaseURL, Opts: opts}
	default:
		return nil, fmt.Errorf("ai: unknown service kind %q", m.Kind)
	}
	if m.APIKey != "" {
		e.keysMu.Lock()
		e.keys[m.APIKey] = true
		e.keysMu.Unlock()
	}
	// Init's signal.NotifyContext ends with this context.
	ctx, cancel := context.WithCancel(e.logCtx(context.Background()))
	defer func() {
		if r := recover(); r != nil {
			cancel()
			in, err = nil, errors.New("ai: model service setup failed")
		}
	}()
	g := genkit.Init(ctx, genkit.WithPlugins(plugin), genkit.WithPromptFS(noPrompts))
	e.inits.Add(1)
	in = &instance{cfg: cfg, g: g, provider: plugin.Name(), cancel: cancel}
	e.insts[m.ServiceID] = in
	return in, nil
}

// Run streams one run. The last event is exactly one of done, interrupted or
// error; wait returns the Result once the run has ended, even if nobody reads
// the events (sends give up when ctx ends).
func (e *Engine) Run(ctx context.Context, req Request) (<-chan Event, func() Result) {
	r := &runner{e: e, ctx: ctx, ch: make(chan Event, 64)}
	done := make(chan struct{})
	var res Result
	go func() {
		defer close(done)
		res = r.run(req)
		switch {
		case res.Err != nil:
			r.finish(Event{Kind: "error", Err: res.Err})
		case len(res.Interrupts) > 0:
			r.finish(Event{Kind: "interrupted", Interrupts: res.Interrupts})
		default:
			r.finish(Event{Kind: "done"})
		}
	}()
	return r.ch, func() Result { <-done; return res }
}

type runner struct {
	e   *Engine
	ctx context.Context
	ch  chan Event

	mu    sync.Mutex // sends, and the close after the last one
	ended bool
}

// send drops events after the last one: a tool Genkit abandoned after a failed
// round may still be running.
func (r *runner) send(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ended {
		r.sendLocked(ev)
	}
}

func (r *runner) finish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sendLocked(ev)
	r.ended = true
	close(r.ch)
}

func (r *runner) sendLocked(ev Event) {
	select {
	case r.ch <- ev:
		return
	default:
	}
	select {
	case r.ch <- ev:
	case <-r.ctx.Done():
	}
}

func (r *runner) run(req Request) Result {
	fail := func(err error) Result {
		return Result{State: req.State, Err: &Error{Code: CodeInternal, Msg: err.Error()}}
	}
	seen := map[string]bool{}
	for _, s := range req.Tools {
		if !toolName.MatchString(s.Name) || seen[s.Name] {
			return fail(fmt.Errorf("ai: invalid or duplicate tool name %q", s.Name))
		}
		if s.Run == nil && !s.NeedsApproval {
			return fail(fmt.Errorf("ai: tool %q has no Run", s.Name))
		}
		seen[s.Name] = true
	}
	if len(req.Responses) > 0 && req.Prompt != "" {
		return fail(errors.New("ai: a resumed run takes no prompt"))
	}
	history, err := decodeState(req.State)
	if err != nil {
		return fail(err)
	}
	in, err := r.e.instance(req.Model)
	if err != nil {
		return fail(err)
	}
	model := genkit.LookupModel(in.g, in.provider+"/"+req.Model.Model)
	if model == nil {
		return fail(fmt.Errorf("ai: model %q unavailable", req.Model.Model))
	}

	tools := map[string]*gai.ToolAction[any, any]{}
	var refs []gai.ToolRef
	for _, s := range req.Tools {
		tools[s.Name] = r.tool(s)
		refs = append(refs, tools[s.Name])
	}
	opts := []gai.GenerateOption{gai.WithModel(model), gai.WithStreaming(r.stream), gai.WithUse(callIDs{})}
	if len(refs) > 0 {
		opts = append(opts, gai.WithTools(refs...))
	}
	if req.System != "" {
		opts = append(opts, gai.WithSystem(req.System))
	}
	if len(history) > 0 {
		opts = append(opts, gai.WithMessages(history...))
	}
	if req.Prompt != "" {
		opts = append(opts, gai.WithPrompt(req.Prompt))
	}
	if len(req.Responses) > 0 {
		parts, err := respond(history, tools, req.Responses)
		if err != nil {
			return fail(err)
		}
		opts = append(opts, gai.WithToolResponses(parts...))
	}
	if req.MaxTurns > 0 {
		opts = append(opts, gai.WithMaxTurns(req.MaxTurns))
	}

	resp, err := genkit.Generate(r.e.logCtx(r.ctx), in.g, opts...)
	if err != nil {
		// The state stays as it was: a failed run leaves no half turn behind.
		return Result{State: req.State, Err: classify(r.ctx, err)}
	}
	var msgs []*gai.Message
	for _, m := range resp.History() {
		if m.Role != gai.RoleSystem { // the system prompt comes with each Request
			msgs = append(msgs, m)
		}
	}
	state, err := json.Marshal(msgs)
	if err != nil {
		return fail(err)
	}
	res := Result{State: state, Messages: display(msgs)}
	if resp.FinishReason == gai.FinishReasonInterrupted {
		for _, p := range resp.Interrupts() {
			res.Interrupts = append(res.Interrupts, toolCall(p.ToolRequest))
		}
	}
	return res
}

func (r *runner) stream(_ context.Context, c *gai.ModelResponseChunk) error {
	if c.Role == gai.RoleModel || c.Role == "" {
		if t := c.Text(); t != "" {
			r.send(Event{Kind: "text", Text: t})
		}
	}
	return nil
}

type callIDKey struct{}

// callIDs hands each tool call's ID to the tool function through its context.
type callIDs struct{}

func (callIDs) Name() string { return "godwit_call_ids" }

func (callIDs) New(context.Context) (*gai.Hooks, error) {
	return &gai.Hooks{WrapTool: func(ctx context.Context, p *gai.ToolParams, next gai.ToolNext) (*gai.MultipartToolResponse, error) {
		return next(context.WithValue(ctx, callIDKey{}, p.Request.Ref), p)
	}}, nil
}

// tool is a dynamic tool for one run; Genkit registers it in a throwaway child
// registry. A tool error goes back to the model as {"error": ...}.
func (r *runner) tool(s ToolSpec) *gai.ToolAction[any, any] {
	schema := s.Schema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return gai.NewTool(s.Name, s.Description, func(tc *gai.ToolContext, input any) (any, error) {
		id, _ := tc.Value(callIDKey{}).(string)
		args := rawJSON(input)
		r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: s.Name, Args: args}})
		if s.NeedsApproval {
			return nil, tc.Interrupt(nil)
		}
		out, err := s.Run(tc, args)
		res := ToolResult{CallID: id, Name: s.Name}
		if err != nil {
			res.Error = err.Error()
			out = map[string]any{"error": res.Error}
		} else {
			res.Output = rawJSON(out)
		}
		r.send(Event{Kind: "tool_result", Result: &res})
		return out, nil
	}, gai.WithInputSchema(schema), gai.WithStrictSchema(s.Strict),
		// Out is any: without this, Genkit validates Respond outputs against
		// its multipart envelope schema and rejects them.
		gai.WithOutputSchema(map[string]any{}))
}

// respond answers the interrupted calls of history's last message.
func respond(history []*gai.Message, tools map[string]*gai.ToolAction[any, any], results []ToolResult) ([]*gai.Part, error) {
	var parts []*gai.Part
	for _, res := range results {
		p := pendingCall(history, res.CallID)
		if p == nil {
			return nil, fmt.Errorf("ai: no pending tool call %q", res.CallID)
		}
		t := tools[p.ToolRequest.Name]
		if t == nil {
			return nil, fmt.Errorf("ai: tool %q is not offered to this run", p.ToolRequest.Name)
		}
		out, err := resultOutput(res)
		if err != nil {
			return nil, err
		}
		parts = append(parts, t.Respond(p, out, nil))
	}
	return parts, nil
}

func pendingCall(history []*gai.Message, id string) *gai.Part {
	if len(history) == 0 {
		return nil
	}
	for _, p := range history[len(history)-1].Content {
		if p.IsToolRequest() && p.ToolRequest.Ref == id {
			return p
		}
	}
	return nil
}

func resultOutput(res ToolResult) (any, error) {
	if res.Error != "" {
		return map[string]any{"error": res.Error}, nil
	}
	var out any
	if len(res.Output) > 0 {
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return nil, fmt.Errorf("ai: tool result %q is not JSON", res.CallID)
		}
	}
	return out, nil
}

// CloseState answers every open call of state's last message, without asking
// the model: calls that already ran keep their result, the rest take one from
// responses. A cancelled run uses it so the conversation can go on.
func CloseState(state []byte, responses []ToolResult) ([]byte, error) {
	history, err := decodeState(state)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 || history[len(history)-1].Role != gai.RoleModel {
		return nil, errors.New("ai: no open tool calls")
	}
	byID := map[string]ToolResult{}
	for _, r := range responses {
		byID[r.CallID] = r
	}
	toolMsg := &gai.Message{Role: gai.RoleTool}
	for _, p := range history[len(history)-1].Content {
		if !p.IsToolRequest() {
			continue
		}
		out, ok := p.Metadata["pendingOutput"]
		if !ok {
			res, found := byID[p.ToolRequest.Ref]
			if !found {
				return nil, fmt.Errorf("ai: tool call %q has no result", p.ToolRequest.Ref)
			}
			if out, err = resultOutput(res); err != nil {
				return nil, err
			}
		}
		for _, k := range []string{"pendingOutput", "pendingMetadata", "pendingContent", "interrupt"} {
			delete(p.Metadata, k)
		}
		toolMsg.Content = append(toolMsg.Content, gai.NewResponseForToolRequest(p, out))
	}
	if len(toolMsg.Content) == 0 {
		return nil, errors.New("ai: no open tool calls")
	}
	return json.Marshal(append(history, toolMsg))
}

func decodeState(state []byte) ([]*gai.Message, error) {
	if len(state) == 0 {
		return nil, nil
	}
	var msgs []*gai.Message
	if err := json.Unmarshal(state, &msgs); err != nil {
		return nil, errors.New("ai: unreadable conversation state")
	}
	return msgs, nil
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || v == nil {
		return json.RawMessage("{}")
	}
	return b
}

func toolCall(r *gai.ToolRequest) ToolCall {
	return ToolCall{ID: r.Ref, Name: r.Name, Args: rawJSON(r.Input)}
}

func display(msgs []*gai.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		d := Message{Role: string(m.Role)}
		for _, p := range m.Content {
			switch {
			case p.IsText():
				d.Text += p.Text
			case p.IsToolRequest():
				d.Calls = append(d.Calls, toolCall(p.ToolRequest))
			case p.IsToolResponse():
				d.Results = append(d.Results, ToolResult{CallID: p.ToolResponse.Ref, Name: p.ToolResponse.Name, Output: rawJSON(p.ToolResponse.Output)})
			}
		}
		out = append(out, d)
	}
	return out
}

// classify turns a Genkit error into an Error whose message never carries the
// key or a request or response body.
func classify(ctx context.Context, err error) *Error {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return &Error{CodeCanceled, "the run was cancelled"}
	}
	switch status.Of(err) {
	case status.Unauthenticated, status.PermissionDenied:
		return &Error{CodeUnauthorized, "the model service rejected the API key"}
	case status.ResourceExhausted:
		return &Error{CodeRateLimited, "the model service is rate limiting requests"}
	case status.Unavailable, status.DeadlineExceeded:
		return &Error{CodeUnavailable, "the model service is unavailable"}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return &Error{CodeUnavailable, "could not reach the model service"}
	}
	return &Error{CodeBadResponse, "the model service returned an unusable response"}
}
