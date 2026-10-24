package webui

import (
	"cmp"
	"context"
	"errors"
	"log"
	"sync"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/internal/i18n"
)

// WitStream carries Wit events to the window or page following them only
// (a Target). Each follows one conversation at a time: a run or Follow in
// another conversation replaces what it followed.
var WitStream = mygo.NewEvent[WitEvent]("wit")

// WitEvent is one event of a Wit run, as core.WitEvent.
type WitEvent struct {
	// Conversation and Run are the conversation and the run it belongs to;
	// Turn the turn it answers (WitTurn.ID).
	Conversation int64  `json:"conversation"`
	Run          int64  `json:"run"`
	Turn         string `json:"turn"`
	// Kind is start (a run's first event), context, text, reasoning,
	// tool_call, tool_result, interrupted, done or error.
	Kind string `json:"kind"`
	// Ends: the run is over for now (done, failed, or waiting on the user).
	Ends bool `json:"ends"`
	// Text is a text or reasoning delta.
	Text string `json:"text"`
	// Call is a tool_call's call; Result a tool_result's result.
	Call   *WitCall   `json:"call"`
	Result *WitResult `json:"result"`
	// Interrupts are the calls an interrupt waits on; a skill's has ID
	// "skill" and no tool_call before it.
	Interrupts []WitCall `json:"interrupts"`
	// Citations are the mail shown to the model (N is 0: the page numbers
	// them after the history's); Approvals an interrupt's, in call order.
	Citations []Citation `json:"citations"`
	Approvals []Approval `json:"approvals"`
	// Error is an error's i18n key; "" for a stop.
	Error string `json:"error"`
}

// WitCall is a tool call; Args is its JSON text.
type WitCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// WitResult is a tool call's result: Output JSON text, or Error.
type WitResult struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

// Citation is a message Wit read, numbered N from 1 in the conversation (core.Citation.N).
type Citation struct {
	N         int    `json:"n"`
	MessageID int64  `json:"messageId"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	// Date is in Unix milliseconds, 0 for none.
	Date int64 `json:"date"`
	// Suspect means it seems to hold instructions (wit.suspect).
	Suspect bool `json:"suspect"`
	// Quote is the suspect text from where it starts, at most 40 runes; mail text, shown as plain text.
	Quote string `json:"quote"`
	// Snippet is the message's snippet, for its card; Gone means the message
	// was deleted since (nothing to open or quote).
	Snippet string `json:"snippet"`
	Gone    bool   `json:"gone"`
	// CallIDs are the tool calls of its answer that showed the mail (none:
	// only the run's context did).
	CallIDs []string `json:"callIds"`
}

// Approval is a call waiting for the user, or decided.
type Approval struct {
	ID  int64 `json:"id"`
	Run int64 `json:"run"`
	// CallID is the tool call it answers ("skill" for a skill's edit).
	CallID  string `json:"callId"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
	// State is pending, executing, done, failed, rejected or stale.
	State        string   `json:"state"`
	Error        string   `json:"error"`
	Destinations []string `json:"destinations"`
	// Args is the call's arguments, JSON text ("" for none).
	Args string `json:"args"`
	// SessionAllow: 本次对话都允许 can decide it (WitPage.DecideSession).
	SessionAllow bool `json:"sessionAllow"`
	// Draft is the draft an allowed create_draft made, while it exists (0: none).
	Draft int64 `json:"draft"`
	// DraftEdit: it edits the conversation's draft (shown in the compose window).
	DraftEdit bool `json:"draftEdit"`
	// Form is the form an MCP server asks the user to fill, JSON text (absent
	// for none): its server, message, questions and, once answered, answers.
	// Answered as a question (WitPage.Answer).
	Form string `json:"form,omitempty"`
}

// follow is a target's subscription to a conversation. It lasts until the
// target follows another conversation, or is let go of: a run
// ending does not end it, so a new run in the same conversation loses
// nothing and shows nothing twice.
type follow struct {
	conv    int64
	direct  bool // a ⌘K run's own events, no subscription: it ends with the run
	stop    func()
	mu      sync.Mutex // held while emitting, so nothing is emitted once ended
	stopped bool
}

func (f *follow) end() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	f.stop()
}

// pump sends ch's events to t until ch closes.
func (f *follow) pump(t Target, ch <-chan core.WitEvent, emit func(Target, WitEvent)) {
	for ev := range ch {
		we := witEvent(f.conv, ev)
		f.mu.Lock()
		if !f.stopped && emit != nil {
			emit(t, we)
		}
		f.mu.Unlock()
	}
}

// history is c for the panel.
func history(c core.Conversation) WitHistory {
	h := WitHistory{Conversation: c.ID, Turns: witTurns(c), Model: c.Model, Effort: c.Effort,
		Usage: WitUsage{Input: c.Usage.Input, Output: c.Usage.Output, Context: c.Usage.Last, History: c.HistoryUse, Compactable: c.Compactable}}
	if !c.Awaiting {
		h.Running = c.ActiveRunID
	}
	return h
}

// noHistory is the history of no conversation.
func noHistory() WitHistory { return WitHistory{Turns: []WitTurn{}} }

// started is the run a start event (a run's first, core.WitEvent) names.
func started(ch <-chan core.WitEvent) (core.WitEvent, bool) {
	ev, ok := <-ch
	return ev, ok && ev.Kind == "start"
}

// witEvent is ev of conversation conv for the page.
func witEvent(conv int64, ev core.WitEvent) WitEvent {
	out := WitEvent{Conversation: cmp.Or(ev.ConversationID, conv), Run: ev.RunID, Turn: ev.Turn, Kind: ev.Kind, Ends: ev.Ends, Text: ev.Text,
		Interrupts: []WitCall{}, Citations: []Citation{}, Approvals: []Approval{}}
	if ev.Call != nil {
		c := callOf(*ev.Call)
		out.Call = &c
	}
	if ev.Result != nil {
		r := resultOf(*ev.Result)
		out.Result = &r
	}
	for _, c := range ev.Interrupts {
		out.Interrupts = append(out.Interrupts, callOf(c))
	}
	for _, c := range ev.Citations {
		out.Citations = append(out.Citations, citationOf(c))
	}
	for _, a := range ev.Approvals {
		out.Approvals = append(out.Approvals, approvalOf(a))
	}
	if ev.Err != nil {
		out.Error = errorKey(ev.Err)
	}
	return out
}

func callOf(c ai.ToolCall) WitCall { return WitCall{ID: c.ID, Name: c.Name, Args: string(c.Args)} }

func resultOf(r ai.ToolResult) WitResult {
	return WitResult{CallID: r.CallID, Name: r.Name, Output: string(r.Output), Error: r.Error}
}

func citationOf(c core.Citation) Citation {
	out := Citation{N: c.N, MessageID: c.MessageID, Subject: c.Subject, From: c.From, Suspect: c.Suspect, Quote: c.Quote, Snippet: c.Snippet, Gone: c.Gone, CallIDs: []string{}}
	if c.CallID != "" {
		out.CallIDs = append(out.CallIDs, c.CallID)
	}
	if !c.Date.IsZero() {
		out.Date = c.Date.UnixMilli()
	}
	return out
}

func approvalOf(a core.Approval) Approval {
	return Approval{ID: a.ID, Run: a.RunID, CallID: a.CallID, Tool: a.Tool, Summary: a.Summary, State: a.State,
		Error: a.Error, Destinations: nz(a.Destinations), Args: string(a.Args), SessionAllow: a.SessionAllow, Draft: a.DraftID, DraftEdit: a.DraftEdit,
		Form: string(a.Form)}
}

// witErrorKeys are the core errors with a message of their own (app/errors.go).
var witErrorKeys = []struct {
	err error
	key string
}{
	{core.ErrStaleDraft, "error.stale_draft"},
	{core.ErrInvalidState, "error.invalid_state"},
	{core.ErrRunActive, "error.run_active"},
	{core.ErrStaleApproval, "error.stale_approval"},
	{core.ErrNeedsGrant, "error.needs_grant"},
	{core.ErrAINotConfigured, "error.ai_not_configured"},
	{core.ErrModelNotFound, "error.model_not_found"},
	{core.ErrModelAuth, "error.model_auth"},
	{core.ErrTooLarge, "error.too_large"},
	{core.ErrKeychainUnavailable, "error.keychain"},
	{core.ErrCompacting, "error.compacting"},
	{core.ErrNothingToCompact, "error.nothing_to_compact"},
	{ErrMailGone, "wit.mail.gone"},
	{core.ErrDraftGone, "wit.draft.gone"},
}

// errorKey is the i18n key of what err means to the user, as app's
// humanize: "" for a cancellation, which the user asked for.
func errorKey(err error) string {
	if err == nil || errors.Is(err, context.Canceled) { // a canceled *ai.Error is context.Canceled
		return ""
	}
	var aiErr *ai.Error
	if errors.As(err, &aiErr) {
		if key := "error.ai." + string(aiErr.Code); i18n.T(key) != key {
			return key
		}
	}
	for _, k := range witErrorKeys {
		if errors.Is(err, k.err) {
			return k.key
		}
	}
	return "error.unknown"
}

// keyError is an error the page shows by its i18n key, its text.
type keyError struct {
	key string
	err error
}

func (e *keyError) Error() string { return e.key }
func (e *keyError) Unwrap() error { return e.err }

// keyed is err with its i18n key as its text ("" for a cancellation:
// nothing to show); an unknown one is logged.
func keyed(err error) error {
	if err == nil {
		return nil
	}
	key := errorKey(err)
	if key == "error.unknown" {
		log.Printf("wit: %v", err)
	}
	return &keyError{key: key, err: err}
}
