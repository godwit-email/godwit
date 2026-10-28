package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// Engine runs models on pi (pirun.go). It keeps one instance per model
// service configuration, created on first use, whose context ends the runs
// on it when the service changes or goes away.
type Engine struct {
	warn    *slog.Logger // schema normalization warnings: tool name only
	secrets func() []string
	client  *http.Client // never follows redirects; Close drops its connections

	mu     sync.Mutex
	insts  map[int64]*instance
	closed bool

	keysMu sync.Mutex
	keys   map[string]bool // every API key the engine has used, redacted from what a run reports

	inits atomic.Int64 // instances created, for tests

	// Current, if set, returns the service's stored version (an error when it
	// is gone). A run whose model has another version fails with CodeStale.
	// Set it before the first Run; it is called holding the engine's lock.
	Current func(ctx context.Context, serviceID int64) (int64, error)

	// Masker, if set, masks what runs send a model off this device while it
	// is on, and restores the placeholders in what every run gets back:
	// streamed text, tool calls, what a run ends with (docs/adr/0025).
	// Set it before the first Run.
	Masker *mask.Masker
}

type instance struct {
	cfg    ResolvedModel   // Model cleared
	ctx    context.Context // ends when the instance is dropped; so do its runs
	cancel context.CancelCauseFunc
}

// errStale ends the runs of an instance whose service changed or went away.
var errStale = errors.New("ai: the model service changed")

var errDenied = errors.New("tool call denied")

// ErrInterrupt, from a tool's Run, interrupts the call as if its gate had
// asked for approval: the caller records what the approval needs first.
var ErrInterrupt = errors.New("ai: the call waits for the user")

var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// NewEngine is an engine whose runs redact every key from secrets (and every
// key the engine has used) from what they report; l receives schema warnings.
func NewEngine(l *slog.Logger, secrets func() []string) (*Engine, error) {
	if l == nil {
		l = slog.Default()
	}
	return &Engine{
		warn:    l,
		secrets: secrets,
		client:  &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), CheckRedirect: NoRedirectClient.CheckRedirect},
		insts:   map[int64]*instance{},
		keys:    map[string]bool{},
	}, nil
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

// Forget drops the service's instance (the service changed or was removed);
// its runs in flight end with CodeStale.
func (e *Engine) Forget(serviceID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.insts[serviceID]; in != nil {
		in.cancel(errStale)
		delete(e.insts, serviceID)
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
	e.mu.Unlock()
	e.client.CloseIdleConnections()
}

func (e *Engine) instance(ctx context.Context, m ResolvedModel) (*instance, error) {
	cfg := m
	cfg.Model, cfg.APIKey = "", ""
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
	if m.APIKey != "" {
		e.keysMu.Lock()
		e.keys[m.APIKey] = true
		e.keysMu.Unlock()
	}
	if in := e.insts[m.ServiceID]; in != nil {
		if in.cfg == cfg {
			return in, nil
		}
		in.cancel(errStale)
		delete(e.insts, m.ServiceID)
	}
	if piAPI(m) == "" {
		return nil, fmt.Errorf("ai: unknown service kind %q", m.Kind)
	}
	ictx, cancel := context.WithCancelCause(context.Background())
	e.inits.Add(1)
	in := &instance{cfg: cfg, ctx: ictx, cancel: cancel}
	e.insts[m.ServiceID] = in
	return in, nil
}

// Run streams one run. The last event is exactly one of done, interrupted or
// error; wait returns the Result once the run has ended, even if nobody reads
// the events (sends give up when ctx ends).
func (e *Engine) Run(ctx context.Context, req Request) (<-chan Event, func() Result) {
	// One slot more than the events use: the terminal event always fits.
	r := &runner{e: e, ctx: ctx, ch: make(chan Event, eventBuffer+1), key: req.Model.APIKey, reasoning: req.Reasoning, raw: req.Raw, args: req.Args}
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
	raw       bool                                                            // Request.Raw
	args      func(context.Context, json.RawMessage) (json.RawMessage, error) // Request.Args
	usage     Usage                                                           // so far, under mu

	mu    sync.Mutex // sends, and the close after the last one
	ended bool
	// held is streamed text of heldKind not sent yet: it may end in a
	// placeholder cut between chunks
	held, heldKind string
}

// send drops events after the last one: a tool abandoned after a failed
// round may still be running.
// Events leave the last buffer slot free, waiting for the reader or ctx.
func (r *runner) send(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.restored(ev) {
		r.put(ev)
	}
}

func (r *runner) put(ev Event) {
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
	evs := r.restored(ev)
	for _, ev := range evs[:len(evs)-1] {
		r.put(ev)
	}
	r.ch <- evs[len(evs)-1]
	r.ended = true
	close(r.ch)
}

// restored is the events ev sends, the masker's placeholders in streamed
// text restored: text is held back while it may end in a placeholder cut
// between chunks, and sent before any other event, without a placeholder
// the stream left cut. Called holding mu.
func (r *runner) restored(ev Event) []Event {
	m := r.e.Masker
	if m == nil || r.raw {
		return []Event{ev}
	}
	var out []Event
	streamed := ev.Kind == "text" || ev.Kind == "reasoning"
	if r.held != "" && ev.Kind != r.heldKind {
		out = append(out, Event{Kind: r.heldKind, Text: m.Restore(m.DropCut(r.held))})
		r.held = ""
	}
	if !streamed {
		return append(out, ev)
	}
	text := r.held + ev.Text
	cut := m.Cut(text)
	if r.held, r.heldKind = text[cut:], ev.Kind; cut > 0 {
		ev.Text = m.Restore(text[:cut])
		out = append(out, ev)
	}
	return out
}

// redact replaces every known key in s.
func (r *runner) redact(s string) string { return Redact(s, append(r.e.allSecrets(), r.key)) }

// run checks req and runs it on pi. A one-off with tools runs as the first
// question of a new conversation.
func (r *runner) run(req Request) Result {
	fail := func(err error) Result {
		return Result{Err: &Error{Code: CodeInternal, Msg: r.redact(err.Error())}}
	}
	if err := checkTools(req.Tools); err != nil {
		return fail(err)
	}
	switch h := req.History; {
	case h != nil && req.Prompt != "":
		return fail(errors.New("ai: a run of a history asks its question with Sent.Ask"))
	case h != nil && len(req.Responses) > 0 && h.asked():
		return fail(errors.New("ai: a resumed run takes no question"))
	case h != nil && len(req.Responses) == 0 && !h.asked():
		return fail(errors.New("ai: a run of a history asks a question or resumes"))
	case h != nil && len(req.Responses) == 0 && openCalls(h.msgs[:len(h.msgs)-1]):
		return fail(errors.New("ai: the conversation has unanswered tool calls; pass Responses or close the state"))
	case len(req.Responses) > 0 && h == nil:
		return fail(errors.New("ai: a resumed run needs its history"))
	case h == nil && len(req.Tools) > 0:
		s, err := NewHistory().Send(0, Summary{}, false)
		if err != nil {
			return fail(err)
		}
		req.History, req.Prompt = s.Ask(req.Prompt, ""), ""
	}
	return r.runPi(req)
}

// limitNote ends the system prompt of the request a run makes at its MaxTurns.
const limitNote = "You have reached the tool-call limit for this request. Do not call any more tools. Answer the user from the tool results above, and say what is still missing and how the user can continue."

func errLimit() *Error {
	return &Error{Code: CodeIncomplete, Msg: "the model reached its tool-call limit without answering"}
}

// thinkingBudget is the Anthropic thinking budget per effort; max_tokens is
// raised above it by the answer's own room, Genkit's default 4096.
var thinkingBudget = map[string]int64{"low": 2048, "medium": 8192, "high": 16384}

type callIDKey struct{}

// CallID is the ID of the tool call whose Gate or Run is given ctx.
func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// schema is the input schema and description s is offered with: unless
// Strict, flattened ($defs inlined) as every service takes it, and what the
// flattening dropped described.
func (r *runner) schema(s ToolSpec) (map[string]any, string) {
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
	return schema, desc
}

// openCalls reports whether history ends with a model turn's tool calls, which
// only an interrupted run leaves behind.
func openCalls(history []*message) bool {
	if len(history) == 0 || history[len(history)-1].Role != roleModel {
		return false
	}
	return slices.ContainsFunc(history[len(history)-1].Content, (*part).IsToolRequest)
}

// matchResults returns the tool calls of history's last message, and results
// keyed by call ID once they answer the open calls (no pendingOutput) one to
// one by ID and name.
func matchResults(history []*message, results []ToolResult) ([]*part, map[string]ToolResult, error) {
	if len(history) == 0 || history[len(history)-1].Role != roleModel {
		return nil, nil, errors.New("ai: no open tool calls")
	}
	open := map[string]string{} // ID -> tool name
	var calls []*part
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

func decodeState(state []byte) ([]*message, error) {
	if len(state) == 0 {
		return nil, nil
	}
	bad := errors.New("ai: unreadable conversation state")
	var msgs []*message // tool inputs and outputs decode exactly (part.UnmarshalJSON)
	if err := json.Unmarshal(state, &msgs); err != nil {
		return nil, bad
	}
	for _, m := range msgs {
		if m == nil || len(m.Content) == 0 || (m.Role != roleUser && m.Role != roleModel && m.Role != roleTool) {
			return nil, bad
		}
		for _, p := range m.Content {
			if p == nil || p.IsToolRequest() && (p.ToolRequest.Name == "" || p.ToolRequest.Ref == "") ||
				p.IsToolResponse() && (p.ToolResponse.Name == "" || p.ToolResponse.Ref == "") {
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

func toolCall(r *toolRequest) ToolCall {
	return ToolCall{ID: r.Ref, Name: r.Name, Args: rawJSON(r.Input)}
}

// RestoreMessage is msg as the user sees it: the placeholders m made in its
// text, reasoning, calls' arguments and results restored.
func RestoreMessage(m *mask.Masker, msg Message) Message {
	msg.Text, msg.Reasoning = m.Restore(msg.Text), m.Restore(msg.Reasoning)
	msg.Calls = slices.Clone(msg.Calls)
	for i, c := range msg.Calls {
		msg.Calls[i].Args, _ = m.RestoreArgs(c.Args)
	}
	msg.Results = slices.Clone(msg.Results)
	for i, r := range msg.Results {
		msg.Results[i].Output, _ = m.RestoreArgs(r.Output)
		msg.Results[i].Error = m.Restore(r.Error)
	}
	return msg
}

func display(msgs []*message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		d := Message{Role: string(m.Role), Command: commandOf(m)}
		for _, p := range m.Content {
			switch {
			case d.Command != nil:
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

// classify is the Error of a run that failed as ctx ended, never carrying
// the key or a request or response body.
func classify(ctx context.Context) *Error {
	if errors.Is(context.Cause(ctx), errStale) {
		return &Error{Code: CodeStale, Msg: "the model service changed during the run"}
	}
	return &Error{Code: CodeCanceled, Msg: "the run was cancelled"}
}

// incomplete is the error for a reply that did not finish normally. The
// service's own finish message is not used: it may repeat the request.
func incomplete(stopReason string) *Error {
	switch stopReason {
	case "length":
		return &Error{Code: CodeIncomplete, Msg: "the model's reply was cut off at its output limit"}
	case "blocked":
		return &Error{Code: CodeIncomplete, Msg: "the model service withheld the reply"}
	}
	return &Error{Code: CodeIncomplete, Msg: "the model did not finish its reply"}
}
