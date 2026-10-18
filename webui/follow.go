package webui

import (
	"context"
	"errors"
	"log"
	"slices"
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
	// Conversation and Run are the conversation and the run it belongs to.
	Conversation int64 `json:"conversation"`
	Run          int64 `json:"run"`
	// Kind is context, text, reasoning, tool_call, tool_result, interrupted,
	// done or error.
	Kind string `json:"kind"`
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
	held    bool           // events wait in buf (Follow reading)
	buf     []WitEvent     // while held
	ended   map[int64]bool // runs seen ending while held
}

func (f *follow) end() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	f.stop()
}

// hold keeps events back until release, noting the runs that end meanwhile.
func (f *follow) hold() {
	f.mu.Lock()
	f.held, f.ended = true, map[int64]bool{}
	f.mu.Unlock()
}

// endedWhileHeld reports whether run ended since hold.
func (f *follow) endedWhileHeld(run int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended[run]
}

// release sends what was held back, then lets events through again.
func (f *follow) release(t Target, emit func(Target, WitEvent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.buf {
		if !f.stopped && emit != nil {
			emit(t, ev)
		}
	}
	f.buf, f.held, f.ended = nil, false, nil
}

// pump sends ch's events to t until ch closes.
func (f *follow) pump(t Target, ch <-chan core.WitEvent, emit func(Target, WitEvent)) {
	for ev := range ch {
		we := witEvent(f.conv, ev)
		f.mu.Lock()
		switch {
		case f.stopped:
		case f.held:
			f.buf = append(f.buf, we)
			if ends(ev) {
				f.ended[ev.RunID] = true
			}
		case emit != nil:
			emit(t, we)
		}
		f.mu.Unlock()
	}
}

// read is f's conversation as read now, events held back meanwhile and
// sent to t after: a run seen ending meanwhile shows as ended, read again. A
// run that ends later shows as Running: its end reaches t after the reply,
// on the subscription, which goes on.
func (f *follow) read(ctx context.Context, svc *core.Service, t Target, emit func(Target, WitEvent)) (WitHistory, error) {
	f.hold()
	defer f.release(t, emit)
	h, err := history(ctx, svc, f.conv)
	if followHook != nil {
		followHook()
	}
	if ended := h.Running; err == nil && ended != 0 && f.endedWhileHeld(ended) {
		h, err = history(ctx, svc, f.conv)
		if h.Running == ended { // its end is in what is held back; a run started since stays
			h.Running = 0
		}
	}
	if err != nil {
		return WitHistory{}, err
	}
	return h, nil
}

// ends reports whether ev ends the run followed: an interrupt whose
// approvals were all decided at once (a first generation) resumes it.
func ends(ev core.WitEvent) bool {
	switch ev.Kind {
	case "done", "error":
		return true
	case "interrupted":
		return len(ev.Approvals) == 0 || slices.ContainsFunc(ev.Approvals, func(a core.Approval) bool { return a.State == "pending" })
	}
	return false
}

// history is conversation conv as read now.
func history(ctx context.Context, svc *core.Service, conv int64) (WitHistory, error) {
	c, err := svc.GetConversation(ctx, conv)
	var as []core.Approval
	if err == nil {
		as, err = svc.ListApprovals(ctx, conv)
	}
	if err != nil {
		return WitHistory{}, err
	}
	h := WitHistory{Conversation: conv, Messages: witMessages(c), Approvals: []Approval{}, Model: c.Model,
		Usage: WitUsage{Input: c.Usage.Input, Output: c.Usage.Output, Context: c.Usage.Last, History: c.HistoryUse}}
	for _, a := range as {
		h.Approvals = append(h.Approvals, approvalOf(a))
	}
	waits := slices.ContainsFunc(as, func(a core.Approval) bool { return a.RunID == c.ActiveRunID && a.State == "pending" })
	if c.ActiveRunID != 0 && !waits {
		h.Running = c.ActiveRunID
	}
	return h, nil
}

// witEvent is ev of conversation conv for the page.
func witEvent(conv int64, ev core.WitEvent) WitEvent {
	out := WitEvent{Conversation: conv, Run: ev.RunID, Kind: ev.Kind, Text: ev.Text,
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
	out := Citation{N: c.N, MessageID: c.MessageID, Subject: c.Subject, From: c.From, Suspect: c.Suspect, Quote: c.Quote}
	if !c.Date.IsZero() {
		out.Date = c.Date.UnixMilli()
	}
	return out
}

func approvalOf(a core.Approval) Approval {
	return Approval{ID: a.ID, Run: a.RunID, CallID: a.CallID, Tool: a.Tool, Summary: a.Summary, State: a.State,
		Error: a.Error, Destinations: nz(a.Destinations), Args: string(a.Args), SessionAllow: a.SessionAllow}
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
