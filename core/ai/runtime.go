package ai

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"testing/fstest"
	"time"

	antsdk "github.com/anthropics/anthropic-sdk-go"
	ant "github.com/anthropics/anthropic-sdk-go/option"
	gai "github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/core/logger"
	"github.com/firebase/genkit/go/core/status"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"github.com/openai/openai-go"
	oai "github.com/openai/openai-go/option"
	oaishared "github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/v3/shared"
)

// Engine runs models through Genkit: one Genkit instance per model service
// configuration, created on first use. Genkit types never leave this package.
type Engine struct {
	warn    *slog.Logger // schema normalization warnings: tool name only
	log     *slog.Logger // what Genkit logs through: Error and above, redacted
	secrets func() []string
	client  *http.Client // never follows redirects; Close drops its connections

	mu      sync.Mutex
	insts   map[int64]*instance
	retired map[*instance]bool // replaced instances still running; Close cancels them too
	closed  bool

	keysMu sync.Mutex
	keys   map[string]bool // every API key handed to Genkit, redacted from its logs

	inits atomic.Int64 // genkit.Init calls, for tests

	// Current, if set, returns the service's stored version (an error when it
	// is gone). A run whose model has another version fails with CodeStale.
	// Set it before the first Run; it is called holding the engine's lock.
	Current func(ctx context.Context, serviceID int64) (int64, error)
}

type instance struct {
	cfg      ResolvedModel // Model cleared
	g        *genkit.Genkit
	provider string
	ctx      context.Context // ends when the instance is dropped; so do its runs
	cancel   context.CancelCauseFunc
	runs     int  // runs using it (Engine.mu)
	retired  bool // replaced for a new key: cancelled once runs reaches 0
}

// release ends one run's use of in; a retired instance is cancelled after its last run.
func (e *Engine) release(in *instance) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in.runs--; in.retired && in.runs == 0 {
		in.cancel(errStale)
		delete(e.retired, in)
	}
}

// errStale ends the runs of an instance whose service changed or went away.
var errStale = errors.New("ai: the model service changed")

// noPrompts keeps Init from loading ./prompts from the working directory.
var noPrompts = fstest.MapFS{"prompts": {Mode: fs.ModeDir}}

var errDenied = errors.New("tool call denied")

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
	e.warn = l
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

// Forget drops the service's instance (the service changed or was removed);
// its runs in flight end with CodeStale.
func (e *Engine) Forget(serviceID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.insts[serviceID]; in != nil {
		in.cancel(errStale)
		delete(e.insts, serviceID)
	}
	for in := range e.retired { // its runs on an older key are stale too
		if in.cfg.ServiceID == serviceID {
			in.cancel(errStale)
			delete(e.retired, in)
		}
	}
}

// Close drops every instance; later runs fail and runs in flight are cancelled.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	for id, in := range e.insts {
		in.cancel(context.Canceled)
		delete(e.insts, id)
	}
	for in := range e.retired {
		in.cancel(context.Canceled)
	}
	e.retired = nil
	e.mu.Unlock()
	e.client.CloseIdleConnections()
}

func (e *Engine) instance(ctx context.Context, m ResolvedModel) (in *instance, err error) {
	cfg := m
	cfg.Model = ""
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("ai: engine closed")
	}
	// Checked under the lock: a service changed after this check is
	// Forgotten after it too, which ends the run through the instance.
	if e.Current != nil && m.Client == nil {
		if v, err := e.Current(ctx, m.ServiceID); err != nil || v != m.Version {
			return nil, errStale
		}
	}
	if in := e.insts[m.ServiceID]; in != nil {
		if in.cfg == cfg {
			in.runs++
			return in, nil
		}
		same := in.cfg
		same.APIKey = cfg.APIKey
		switch {
		case same != cfg, in.runs == 0:
			in.cancel(errStale)
		default: // only the key rotated: its runs finish on the old one, then it goes
			in.retired = true
			if e.retired == nil {
				e.retired = map[*instance]bool{}
			}
			e.retired[in] = true
		}
		delete(e.insts, m.ServiceID)
	}
	if api.CurrentEnvironment() == api.EnvironmentDev {
		return nil, errors.New("ai: refusing to run with GENKIT_ENV=dev")
	}
	// The SDKs append a "/" to a base path without one and lose its escapes
	// (openai-go and anthropic-sdk-go WithBaseURL drop RawPath): given one,
	// they keep it as it is, so "%2F" reaches the service as previewed.
	base := m.BaseURL + "/" // normalized: it never ends in "/"
	client := cmp.Or(m.Client, e.client)
	var plugin api.Plugin
	switch m.Kind {
	case "openai":
		// compat_oai clears the identity the SDK reads from the environment.
		plugin = &compat_oai.OpenAICompatible{Provider: "godwit", APIKey: m.APIKey, BaseURL: base,
			Opts: []oai.RequestOption{oai.WithHTTPClient(client)}}
	case "anthropic":
		// The SDK also reads ANTHROPIC_AUTH_TOKEN and sends it as Authorization.
		key, opts := m.APIKey, []ant.RequestOption{ant.WithHTTPClient(client), ant.WithHeaderDel("Authorization")}
		if key == "" { // a local gateway: no auth at all
			key, opts = "godwit-no-key", append(opts, ant.WithHeaderDel("X-Api-Key"))
		}
		plugin = &anthropic.Anthropic{APIKey: key, BaseURL: AnthropicBase(m.BaseURL) + "/", Opts: opts}
	case KindResponses:
		plugin = newResponsesPlugin(base, m.APIKey, client)
	default:
		return nil, fmt.Errorf("ai: unknown service kind %q", m.Kind)
	}
	if m.APIKey != "" {
		e.keysMu.Lock()
		e.keys[m.APIKey] = true
		e.keysMu.Unlock()
	}
	// Init's signal.NotifyContext ends with this context.
	ictx, cancel := context.WithCancelCause(e.logCtx(context.Background()))
	defer func() {
		if r := recover(); r != nil {
			cancel(context.Canceled)
			in, err = nil, errors.New("ai: model service setup failed")
		}
	}()
	g := genkit.Init(ictx, genkit.WithPlugins(plugin), genkit.WithPromptFS(noPrompts))
	e.inits.Add(1)
	in = &instance{cfg: cfg, g: g, provider: plugin.Name(), ctx: ictx, cancel: cancel, runs: 1}
	e.insts[m.ServiceID] = in
	return in, nil
}

// Run streams one run. The last event is exactly one of done, interrupted or
// error; wait returns the Result once the run has ended, even if nobody reads
// the events (sends give up when ctx ends).
func (e *Engine) Run(ctx context.Context, req Request) (<-chan Event, func() Result) {
	// One slot more than the events use: the terminal event always fits.
	r := &runner{e: e, ctx: ctx, ch: make(chan Event, eventBuffer+1), key: req.Model.APIKey, reasoning: req.Reasoning, args: req.Args}
	done := make(chan struct{})
	var res Result
	go func() {
		defer close(done)
		res = r.run(req)
		r.mu.Lock()
		res.Usage = r.usage
		r.mu.Unlock()
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

const eventBuffer = 64

type runner struct {
	e   *Engine
	ctx context.Context
	ch  chan Event
	key string
	// reasoning: Request.Reasoning
	reasoning bool
	args      func(context.Context, json.RawMessage) (json.RawMessage, error) // Request.Args
	usage     Usage                                                           // so far, under mu

	mu    sync.Mutex // sends, and the close after the last one
	ended bool
}

// send drops events after the last one: a tool Genkit abandoned after a failed
// round may still be running.
// Events leave the last buffer slot free, waiting for the reader or ctx.
func (r *runner) send(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// ponytail: polls while the reader lags; a wake-up channel if it matters
	for !r.ended && len(r.ch) >= eventBuffer {
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !r.ended {
		r.ch <- ev // sends are serialized and readers only make room
	}
}

// finish sends the terminal event into the reserved slot and closes the stream.
func (r *runner) finish(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ch <- ev
	r.ended = true
	close(r.ch)
}

// redact replaces every known key in s.
func (r *runner) redact(s string) string { return Redact(s, append(r.e.allSecrets(), r.key)) }

func (r *runner) run(req Request) Result {
	fail := func(err error) Result {
		return Result{State: req.State, Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
	}
	seen := map[string]bool{}
	for _, s := range req.Tools {
		if !toolName.MatchString(s.Name) || seen[s.Name] {
			return fail(fmt.Errorf("ai: invalid or duplicate tool name %q", s.Name))
		}
		if s.Run == nil && !s.NeedsApproval && s.Gate == nil {
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
	in, err := r.e.instance(r.ctx, req.Model)
	if errors.Is(err, errStale) {
		return Result{State: req.State, Err: &Error{Code: CodeStale, Msg: "the model service changed; try again"}}
	}
	if err != nil {
		return fail(err)
	}
	defer r.e.release(in)
	// The run ends with its instance: a changed or removed service sends nothing more.
	ctx, cancel := context.WithCancelCause(r.ctx)
	defer cancel(nil)
	stop := context.AfterFunc(in.ctx, func() { cancel(context.Cause(in.ctx)) })
	defer stop()
	// Sends give up with it too, so a stalled reader cannot hold the run.
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
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
	opts := []gai.GenerateOption{gai.WithModel(model), gai.WithStreaming(r.stream), gai.WithUse(callIDs{r})}
	if len(refs) > 0 {
		opts = append(opts, gai.WithTools(refs...))
	}
	if req.System != "" {
		opts = append(opts, gai.WithSystem(req.System))
	}
	if len(history) > 0 {
		opts = append(opts, gai.WithMessages(history...))
	}
	switch {
	case req.Context != "" && req.Prompt != "":
		ctxPart := gai.NewTextPart(req.Context)
		ctxPart.Metadata = map[string]any{contextKey: true}
		opts = append(opts, gai.WithPromptParts(ctxPart, gai.NewTextPart(req.Prompt)))
	case req.Prompt != "":
		opts = append(opts, gai.WithPrompt(req.Prompt))
	}
	if len(req.Responses) == 0 && openCalls(history) {
		return fail(errors.New("ai: the conversation has unanswered tool calls; pass Responses or close the state"))
	}
	if len(req.Responses) > 0 {
		parts, err := respond(history, tools, req.Responses, r.redact)
		if err != nil {
			return fail(err)
		}
		opts = append(opts, gai.WithToolResponses(parts...))
	}
	if req.MaxTurns > 0 {
		opts = append(opts, gai.WithMaxTurns(req.MaxTurns))
	}
	if cfg := effortConfig(req.Model.Kind, req.Effort); cfg != nil {
		opts = append(opts, gai.WithConfig(cfg))
	}

	resp, err := genkit.Generate(r.e.logCtx(ctx), in.g, opts...)
	if err == nil && ctx.Err() != nil {
		err = context.Cause(ctx) // ended as Generate returned: events may have been dropped
	}
	if err != nil {
		// The state stays as it was: a failed run leaves no half turn behind.
		return Result{State: req.State, Err: classify(ctx, err)}
	}
	switch resp.FinishReason {
	case gai.FinishReasonStop, gai.FinishReasonInterrupted,
		gai.FinishReasonUnknown, "": // a reason Genkit does not map: a normal end, as Genkit treats it
	default:
		// Cut off, filtered or failed part-way: nothing of it is kept.
		return Result{State: req.State, Err: incomplete(resp.FinishReason)}
	}
	var msgs []*gai.Message
	for _, m := range resp.History() {
		// The system prompt comes with each Request; an empty reply carries nothing.
		if m.Role != gai.RoleSystem && len(m.Content) > 0 {
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

// thinkingBudget is the Anthropic thinking budget per effort; max_tokens is
// raised above it by the answer's own room, Genkit's default 4096.
var thinkingBudget = map[string]int64{"low": 2048, "medium": 8192, "high": 16384}

// effortConfig is the model config asking kind's API for effort; nil for "".
func effortConfig(kind, effort string) any {
	if effort == "" {
		return nil
	}
	switch kind {
	case "anthropic":
		b := thinkingBudget[effort]
		return antsdk.MessageNewParams{MaxTokens: b + 4096, Thinking: antsdk.ThinkingConfigParamOfEnabled(b)}
	case "openai":
		return openai.ChatCompletionNewParams{ReasoningEffort: oaishared.ReasoningEffort(effort)}
	case KindResponses:
		return shared.ReasoningParam{Effort: shared.ReasoningEffort(effort), Summary: shared.ReasoningSummaryAuto}
	}
	return nil
}

func (r *runner) stream(_ context.Context, c *gai.ModelResponseChunk) error {
	if c.Role == gai.RoleModel || c.Role == "" {
		for _, p := range c.Content {
			// an encrypted one (Responses) has no text
			if r.reasoning && p != nil && p.IsReasoning() && p.Text != "" {
				r.send(Event{Kind: "reasoning", Text: p.Text})
			}
		}
		if t := c.Text(); t != "" {
			r.send(Event{Kind: "text", Text: t})
		}
	}
	return nil
}

type callIDKey struct{}

// CallID is the ID of the tool call whose Gate or Run is given ctx.
func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// callIDs hands each tool call's ID to the tool function through its context,
// and turns arguments that fail the tool's schema into an error result for the
// model, which can then retry, instead of a failed run.
type callIDs struct{ r *runner }

func (callIDs) Name() string { return "godwit_call_ids" }

func (c callIDs) New(context.Context) (*gai.Hooks, error) {
	return &gai.Hooks{
		WrapTool: func(ctx context.Context, p *gai.ToolParams, next gai.ToolNext) (*gai.MultipartToolResponse, error) {
			// A call the tool never sees goes back to the model as an error result, which it can retry.
			refuse := func(msg string) (*gai.MultipartToolResponse, error) {
				call := toolCall(p.Request)
				res := ToolResult{CallID: call.ID, Name: call.Name, Error: c.r.redact(msg)}
				c.r.send(Event{Kind: "tool_call", Call: &call})
				c.r.send(Event{Kind: "tool_result", Result: &res})
				return &gai.MultipartToolResponse{Output: map[string]any{"error": res.Error}}, nil
			}
			if c.r.args != nil { // on a copy: the model's own request stays in the state
				in := rawJSON(p.Request.Input)
				out, err := c.r.args(ctx, in)
				if err != nil {
					return refuse(err.Error())
				}
				if !bytes.Equal(out, in) {
					dec := json.NewDecoder(bytes.NewReader(out))
					dec.UseNumber() // a large integer argument stays exact
					var v any
					if dec.Decode(&v) == nil {
						req, q := *p.Request, *p
						req.Input, q.Request = v, &req
						p = &q
					}
				}
			}
			resp, err := next(context.WithValue(ctx, callIDKey{}, p.Request.Ref), p)
			if err == nil || !errors.Is(err, status.ErrInvalidInput) {
				return resp, err
			}
			// Genkit validated the arguments before the tool function ran.
			return refuse("invalid arguments: " + err.Error())
		},
		// Results bind to call IDs, so a turn that repeats one is unusable.
		WrapModel: func(ctx context.Context, p *gai.ModelParams, next gai.ModelNext) (*gai.ModelResponse, error) {
			resp, err := next(ctx, p)
			if err == nil && resp.Usage != nil {
				c.r.mu.Lock()
				c.r.usage.Input += resp.Usage.InputTokens
				c.r.usage.Output += resp.Usage.OutputTokens
				c.r.usage.Last = resp.Usage.InputTokens
				c.r.mu.Unlock()
			}
			if err == nil && resp.Message != nil {
				seen := map[string]bool{}
				for _, part := range resp.Message.Content {
					if part != nil && part.IsToolRequest() && part.ToolRequest != nil && part.ToolRequest.Ref != "" {
						if seen[part.ToolRequest.Ref] {
							return nil, errDuplicateCallID
						}
						seen[part.ToolRequest.Ref] = true
					}
				}
			}
			return resp, err
		},
	}, nil
}

var errDuplicateCallID = errors.New("ai: the model repeated a tool call ID")

// tool is a dynamic tool for one run; Genkit registers it in a throwaway child
// registry. A tool error goes back to the model as {"error": ...}.
func (r *runner) tool(s ToolSpec) *gai.ToolAction[any, any] {
	schema := s.Schema
	if schema == nil {
		schema = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	desc := s.Description
	if !s.Strict {
		var descr string
		var warns []string
		schema, descr, warns = NormalizeSchema(schema)
		if len(warns) > 0 {
			r.e.warn.Warn("ai: tool schema could not be fully flattened", "tool", s.Name, "warnings", len(warns))
		}
		if descr != "" {
			desc += "\n\n" + descr
		}
	}
	return gai.NewTool(s.Name, desc, func(tc *gai.ToolContext, input any) (any, error) {
		id := CallID(tc)
		args := rawJSON(input)
		r.send(Event{Kind: "tool_call", Call: &ToolCall{ID: id, Name: s.Name, Args: args}})
		d := Allow
		var err error
		if s.Gate != nil {
			if d, err = s.Gate(tc, args); err == nil && d != Allow && d != Approve {
				err = errDenied
			}
		}
		if err == nil && (d == Approve || s.NeedsApproval) {
			return nil, tc.Interrupt(nil)
		}
		var out any
		switch {
		case err != nil:
		case s.Run == nil: // a gate allowed a tool that can only be approved
			err = errDenied
		default:
			out, err = s.Run(tc, args)
		}
		res := ToolResult{CallID: id, Name: s.Name}
		if err != nil {
			res.Error = r.redact(err.Error())
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
func respond(history []*gai.Message, tools map[string]*gai.ToolAction[any, any], results []ToolResult, redact func(string) string) ([]*gai.Part, error) {
	calls, byID, err := matchResults(history, results)
	if err != nil {
		return nil, err
	}
	var parts []*gai.Part
	for _, p := range calls {
		res, open := byID[p.ToolRequest.Ref]
		if !open {
			continue // ran before the interrupt; Genkit replays its result
		}
		t := tools[p.ToolRequest.Name]
		if t == nil {
			return nil, fmt.Errorf("ai: tool %q is not offered to this run", p.ToolRequest.Name)
		}
		res.Error = redact(res.Error)
		out, err := resultOutput(res)
		if err != nil {
			return nil, err
		}
		parts = append(parts, t.Respond(p, out, nil))
	}
	return parts, nil
}

// openCalls reports whether history ends with a model turn's tool calls, which
// only an interrupted run leaves behind.
func openCalls(history []*gai.Message) bool {
	if len(history) == 0 || history[len(history)-1].Role != gai.RoleModel {
		return false
	}
	return slices.ContainsFunc(history[len(history)-1].Content, (*gai.Part).IsToolRequest)
}

// matchResults returns the tool calls of history's last message, and results
// keyed by call ID once they answer the open calls (no pendingOutput) one to
// one by ID and name.
func matchResults(history []*gai.Message, results []ToolResult) ([]*gai.Part, map[string]ToolResult, error) {
	if len(history) == 0 || history[len(history)-1].Role != gai.RoleModel {
		return nil, nil, errors.New("ai: no open tool calls")
	}
	open := map[string]string{} // ID -> tool name
	var calls []*gai.Part
	for _, p := range history[len(history)-1].Content {
		if !p.IsToolRequest() {
			continue
		}
		calls = append(calls, p)
		if _, ran := p.Metadata["pendingOutput"]; !ran {
			if _, dup := open[p.ToolRequest.Ref]; dup {
				return nil, nil, fmt.Errorf("ai: tool call ID %q is repeated", p.ToolRequest.Ref)
			}
			open[p.ToolRequest.Ref] = p.ToolRequest.Name
		}
	}
	if len(open) == 0 {
		return nil, nil, errors.New("ai: no open tool calls")
	}
	byID := map[string]ToolResult{}
	for _, r := range results {
		name, ok := open[r.CallID]
		switch {
		case !ok:
			return nil, nil, fmt.Errorf("ai: no open tool call %q", r.CallID)
		case r.Name != name:
			return nil, nil, fmt.Errorf("ai: result for %q names tool %q, the call is to %q", r.CallID, r.Name, name)
		}
		if _, dup := byID[r.CallID]; dup {
			return nil, nil, fmt.Errorf("ai: more than one result for %q", r.CallID)
		}
		byID[r.CallID] = r
	}
	if len(byID) != len(open) {
		return nil, nil, errors.New("ai: every open tool call needs exactly one result")
	}
	return calls, byID, nil
}

func resultOutput(res ToolResult) (any, error) {
	if res.Error != "" {
		return map[string]any{"error": res.Error}, nil
	}
	var out any
	if len(res.Output) > 0 {
		dec := json.NewDecoder(bytes.NewReader(res.Output))
		dec.UseNumber() // large integers reach the model exactly
		if err := dec.Decode(&out); err != nil || dec.More() {
			return nil, fmt.Errorf("ai: tool result %q is not JSON", res.CallID)
		}
	}
	return out, nil
}

// CloseState answers every open call of state's last message, without asking
// the model: calls that already ran keep their result, the others take theirs
// from responses, which must match them one to one by ID and name. A cancelled
// run uses it so the conversation can go on.
func CloseState(state []byte, responses []ToolResult) ([]byte, error) {
	history, err := decodeState(state)
	if err != nil {
		return nil, err
	}
	calls, byID, err := matchResults(history, responses)
	if err != nil {
		return nil, err
	}
	toolMsg := &gai.Message{Role: gai.RoleTool}
	for _, p := range calls {
		out, ran := p.Metadata["pendingOutput"]
		if !ran {
			if out, err = resultOutput(byID[p.ToolRequest.Ref]); err != nil {
				return nil, err
			}
		}
		for _, k := range []string{"pendingOutput", "pendingMetadata", "pendingContent", "interrupt"} {
			delete(p.Metadata, k)
		}
		toolMsg.Content = append(toolMsg.Content, gai.NewResponseForToolRequest(p, out))
	}
	return json.Marshal(append(history, toolMsg))
}

// Messages decodes state for display.
func Messages(state []byte) ([]Message, error) {
	history, err := decodeState(state)
	if err != nil {
		return nil, err
	}
	return display(history), nil
}

// Contexts are the Request.Context texts state holds, oldest first.
func Contexts(state []byte) ([]string, error) {
	history, err := decodeState(state)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range history {
		for _, p := range m.Content {
			if p.IsText() && p.Metadata[contextKey] != nil {
				out = append(out, p.Text)
			}
		}
	}
	return out, nil
}

func decodeState(state []byte) ([]*gai.Message, error) {
	if len(state) == 0 {
		return nil, nil
	}
	bad := errors.New("ai: unreadable conversation state")
	var msgs []*gai.Message
	if err := json.Unmarshal(state, &msgs); err != nil {
		return nil, bad
	}
	for _, m := range msgs {
		if m == nil || len(m.Content) == 0 || (m.Role != gai.RoleUser && m.Role != gai.RoleModel && m.Role != gai.RoleTool) {
			return nil, bad
		}
		for _, p := range m.Content {
			switch {
			case p == nil,
				p.IsToolRequest() && (p.ToolRequest == nil || p.ToolRequest.Name == "" || p.ToolRequest.Ref == ""),
				p.IsToolResponse() && (p.ToolResponse == nil || p.ToolResponse.Name == "" || p.ToolResponse.Ref == ""):
				return nil, bad
			}
		}
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

// contextKey marks a Request.Context part in the state.
const contextKey = "godwit_context"

func display(msgs []*gai.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		d := Message{Role: string(m.Role)}
		for _, p := range m.Content {
			switch {
			case p.IsText() && p.Metadata[contextKey] == nil:
				d.Text += p.Text
			case p.IsReasoning():
				d.Reasoning += p.Text
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
	if errors.Is(err, errDuplicateCallID) {
		return &Error{Code: CodeBadResponse, Msg: "the model repeated a tool call ID"}
	}
	if errors.Is(context.Cause(ctx), errStale) {
		return &Error{Code: CodeStale, Msg: "the model service changed during the run"}
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return &Error{Code: CodeCanceled, Msg: "the run was cancelled"}
	}
	switch status.Of(err) {
	case status.Unauthenticated, status.PermissionDenied:
		if errors.Is(err, errNotEligible) {
			return &Error{Code: CodeUnauthorized, Msg: "the ChatGPT account is not eligible for subscription sharing", Detail: DetailChatGPTEligibility}
		}
		return &Error{Code: CodeUnauthorized, Msg: "the model service rejected the API key"}
	case status.ResourceExhausted:
		if errors.Is(err, errUsageLimit) {
			return &Error{Code: CodeRateLimited, Msg: "the ChatGPT plan's usage limit is reached", Detail: DetailChatGPTUsage}
		}
		return &Error{Code: CodeRateLimited, Msg: "the model service is rate limiting requests"}
	case status.Unavailable, status.DeadlineExceeded:
		return &Error{Code: CodeUnavailable, Msg: "the model service is unavailable"}
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return &Error{Code: CodeUnavailable, Msg: "could not reach the model service"}
	}
	return &Error{Code: CodeBadResponse, Msg: "the model service returned an unusable response"}
}

// incomplete is the error for a reply that did not finish normally. The
// service's own finish message is not used: it may repeat the request.
func incomplete(r gai.FinishReason) *Error {
	switch r {
	case gai.FinishReasonLength:
		return &Error{Code: CodeIncomplete, Msg: "the model's reply was cut off at its output limit"}
	case gai.FinishReasonBlocked:
		return &Error{Code: CodeIncomplete, Msg: "the model service withheld the reply"}
	}
	return &Error{Code: CodeIncomplete, Msg: "the model did not finish its reply"}
}
