package webui

import (
	"cmp"
	"context"
	"errors"
	"log"
	netmail "net/mail"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/richtext"
)

// WitStream carries the Wit events of a compose window's draft, emitted to
// that window only. A window follows one conversation at a time: a run or
// Follow in another conversation replaces what it followed.
var WitStream = mygo.NewEvent[WitEvent]("wit")

// WitEvent is one event of a Wit run, as core.WitEvent.
type WitEvent struct {
	// Conversation and Run are the conversation and the run it belongs to.
	Conversation int64 `json:"conversation"`
	Run          int64 `json:"run"`
	// Kind is context, text, tool_call, tool_result, interrupted, done or error.
	Kind string `json:"kind"`
	// Text is a text delta.
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

// Citation is a message Wit read, numbered N from 1 in the conversation.
type Citation struct {
	N         int    `json:"n"`
	MessageID int64  `json:"messageId"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	// Date is in Unix milliseconds, 0 for none.
	Date int64 `json:"date"`
	// Suspect means it seems to hold instructions (wit.suspect).
	Suspect bool `json:"suspect"`
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
}

// WitInfo is what the Wit panel offers for the draft.
type WitInfo struct {
	// Enabled means a model is configured: the panel shows.
	Enabled bool `json:"enabled"`
	// Skills are the 调整 menu's; Languages the 翻译 menu's, the reading
	// language first.
	Skills    []Skill    `json:"skills"`
	Languages []Language `json:"languages"`
	// Chips name what Wit knows of the draft.
	Chips []string `json:"chips"`
}

// Skill is a 调整 menu item.
type Skill struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Language is a 翻译 menu item.
type Language struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
}

// adjustSkills are the 调整 menu's skills, in its order.
var adjustSkills = []string{"tone-formal", "tone-friendly", "shorter", "longer", "proofread"}

// languages are the 翻译 menu's common targets (app/settings_general.go).
var languages = []Language{{"zh-CN", "简体中文"}, {"zh-TW", "繁體中文"}, {"en", "English"}, {"ja", "日本語"}}

// langName is a language tag's name in the menu.
func langName(tag string) string {
	switch t := strings.ToLower(tag); {
	case strings.HasPrefix(t, "zh-tw"), strings.HasPrefix(t, "zh-hant"), strings.HasPrefix(t, "zh-hk"):
		return "繁體中文"
	case strings.HasPrefix(t, "zh"):
		return "简体中文"
	case strings.HasPrefix(t, "ja"):
		return "日本語"
	}
	return "English"
}

// startHook, when set, runs in a run's start before the window subscribes;
// followHook in Follow after its reads (tests).
var startHook, followHook func()

// follow is a window's subscription to a conversation. It lasts until the
// window follows another conversation, or is detached or released: a run
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
func (f *follow) release(win int, emit func(int, WitEvent)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.buf {
		if !f.stopped && emit != nil {
			emit(win, ev)
		}
	}
	f.buf, f.held, f.ended = nil, false, nil
}

// follow makes window win, whose session is s, follow conversation conv:
// its subscription to conv if it has one, else a new one in place of what
// it followed. Every event goes to emit. A session gone (Release, Detach)
// gets ErrNoDraft: those end the subscription after removing the session,
// both under c.mu, so none is left behind.
func (c *Compose) follow(win int, s *session, svc *core.Service, conv int64, emit func(int, WitEvent)) (*follow, error) {
	c.mu.Lock()
	if c.sessions[win] != s {
		c.mu.Unlock()
		return nil, ErrNoDraft
	}
	old := c.follows[win]
	if old != nil && old.conv == conv && !old.direct {
		c.mu.Unlock()
		return old, nil
	}
	ch, stop := svc.SubscribeConversation(conv)
	f := &follow{conv: conv, stop: stop}
	c.follows[win] = f
	c.mu.Unlock()
	if old != nil {
		old.end()
	}
	go f.pump(win, ch, emit)
	return f, nil
}

// followRun makes window win follow run's own events ch, of conversation
// conv, in place of what it followed: RewriteSelection makes its
// conversation, so nothing could subscribe to it before the run. A
// decision or Follow later subscribes to the conversation.
func (c *Compose) followRun(win int, s *session, conv int64, ch <-chan core.WitEvent, emit func(int, WitEvent)) error {
	c.mu.Lock()
	if c.sessions[win] != s {
		c.mu.Unlock()
		return ErrNoDraft
	}
	old := c.follows[win]
	f := &follow{conv: conv, stop: func() {}, direct: true}
	c.follows[win] = f
	c.mu.Unlock()
	if old != nil {
		old.end()
	}
	go f.pump(win, ch, emit)
	return nil
}

// pump sends ch's events to window win until ch closes.
func (f *follow) pump(win int, ch <-chan core.WitEvent, emit func(int, WitEvent)) {
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
			emit(win, we)
		}
		f.mu.Unlock()
	}
}

// unfollow ends window win's subscription.
func (c *Compose) unfollow(win int) {
	c.mu.Lock()
	f := c.follows[win]
	delete(c.follows, win)
	c.mu.Unlock()
	if f != nil {
		f.end()
	}
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

// WitInfo is what the Wit panel offers for the calling window's draft.
func (p *ComposePage) WitInfo(ctx context.Context) (WitInfo, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return WitInfo{}, err
	}
	defer s.mu.Unlock()
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return WitInfo{}, err
	}
	return witInfo(ctx, svc, d)
}

// witInfo is as compose_wit.go's menus and chips: the email replied to, the
// attachments, and how much mail with the recipients the draft's
// conversation read.
func witInfo(ctx context.Context, svc *core.Service, d core.Draft) (WitInfo, error) {
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return WitInfo{}, err
	}
	info := WitInfo{Enabled: st.DefaultModel != "", Chips: []string{}}
	for _, id := range adjustSkills {
		info.Skills = append(info.Skills, Skill{ID: id, Label: i18n.T("wit.skill." + id)})
	}
	read := svc.ReadingLanguage(ctx)
	info.Languages = []Language{{read, langName(read)}}
	for _, l := range languages {
		if !slices.ContainsFunc(info.Languages, func(x Language) bool { return x.Name == l.Name }) {
			info.Languages = append(info.Languages, l)
		}
	}
	if d.SourceMessageID != 0 {
		info.Chips = append(info.Chips, i18n.T("wit.compose.source"))
	}
	for _, a := range d.Attachments {
		info.Chips = append(info.Chips, a.Filename)
	}
	conv, err := openConversation(ctx, svc, d.ID)
	if err != nil || conv == 0 || len(d.To) == 0 {
		return info, err
	}
	c, err := svc.GetConversation(ctx, conv)
	if err != nil {
		return WitInfo{}, err
	}
	var seen []int64
	for _, ct := range c.Citations {
		if ct.CallID == "" && ct.MessageID != d.SourceMessageID && !slices.Contains(seen, ct.MessageID) {
			seen = append(seen, ct.MessageID)
		}
	}
	if len(seen) > 0 {
		who := d.To[0]
		if a, err := netmail.ParseAddress(who); err == nil {
			who = cmp.Or(a.Name, a.Address)
		}
		info.Chips = append(info.Chips, i18n.Tf("wit.compose.history", who, len(seen)))
	}
	return info, nil
}

// draftConversations are the draft's assist conversations, oldest first.
// The Assist one has no title; RewriteSelection titles its own.
func draftConversations(ctx context.Context, svc *core.Service, draftID int64) ([]core.Conversation, error) {
	cs, err := svc.ListConversations(ctx, "assist")
	cs = slices.DeleteFunc(cs, func(c core.Conversation) bool { return c.DraftID != draftID })
	slices.SortFunc(cs, func(a, b core.Conversation) int { return cmp.Compare(a.ID, b.ID) })
	return cs, err
}

// openConversation is the draft's conversation to show: the newest with an
// edit waiting, else its Assist conversation, else 0.
func openConversation(ctx context.Context, svc *core.Service, draftID int64) (int64, error) {
	cs, err := draftConversations(ctx, svc, draftID)
	if err != nil {
		return 0, err
	}
	for _, c := range slices.Backward(cs) {
		as, err := svc.ListApprovals(ctx, c.ID)
		if err != nil {
			return 0, err
		}
		if slices.ContainsFunc(as, func(a core.Approval) bool { return isDraftEdit(a.Tool) && a.State == "pending" }) {
			return c.ID, nil
		}
	}
	if i := slices.IndexFunc(cs, func(c core.Conversation) bool { return c.Title == "" }); i >= 0 {
		return cs[i].ID, nil
	}
	return 0, nil
}

func isDraftEdit(tool string) bool { return tool == "update_draft" || tool == "update_selection" }

// Ask asks Wit about the calling window's draft (core Assist) and returns the
// run's ID; its events go to the window on WitStream.
func (p *ComposePage) Ask(ctx context.Context, input string) (int64, error) {
	return p.start(ctx, func(ctx context.Context, svc *core.Service, id int64) (<-chan core.WitEvent, error) {
		return svc.Assist(ctx, id, input)
	})
}

// Skill runs skill id on the calling window's draft (core AssistSkill);
// translate-draft writes in language ("" for the reading language). It
// returns the run's ID; its events go to the window on WitStream.
func (p *ComposePage) Skill(ctx context.Context, id, language string) (int64, error) {
	return p.start(ctx, func(ctx context.Context, svc *core.Service, draftID int64) (<-chan core.WitEvent, error) {
		return svc.AssistSkill(ctx, draftID, id, core.SkillOptions{TargetLanguage: language})
	})
}

// start runs run in the draft's Assist conversation, which the window
// follows from before the run starts: a first generation is applied at
// once and the run resumes. Only Stop cancels the run.
func (p *ComposePage) start(ctx context.Context, run func(context.Context, *core.Service, int64) (<-chan core.WitEvent, error)) (int64, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return 0, keyed(err)
	}
	defer s.mu.Unlock()
	cs, err := draftConversations(ctx, svc, s.draftID)
	if err != nil {
		return 0, keyed(err)
	}
	var conv int64
	if i := slices.IndexFunc(cs, func(c core.Conversation) bool { return c.Title == "" }); i >= 0 {
		conv = cs[i].ID
	} else {
		c, err := svc.NewConversation(ctx, "assist", s.draftID)
		if err != nil {
			return 0, keyed(err)
		}
		conv = c.ID
	}
	if startHook != nil {
		startHook()
	}
	if _, err := p.c.follow(win, s, svc, conv, p.d.Emit); err != nil {
		return 0, keyed(err)
	}
	ch, err := run(context.WithoutCancel(ctx), svc, s.draftID)
	if err != nil { // ErrRunActive: the active run goes on being followed
		return 0, keyed(err)
	}
	c, err := svc.GetConversation(ctx, conv)
	id := c.ActiveRunID // claimed before Assist returned
	if err != nil || id == 0 {
		id = (<-ch).RunID // over already: its events are queued
	}
	go func() { // the subscription shows the same events
		for range ch {
		}
	}()
	return id, nil
}

// Stop cancels run, which must be one of the calling window's draft.
func (p *ComposePage) Stop(ctx context.Context, run int64) error {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return keyed(err)
	}
	draftID := s.draftID
	s.mu.Unlock() // CancelRun waits for the run to end
	cs, err := draftConversations(ctx, svc, draftID)
	if err != nil {
		return keyed(err)
	}
	for _, c := range cs {
		c, err := svc.GetConversation(ctx, c.ID)
		if err != nil {
			return keyed(err)
		}
		if c.ActiveRunID == run || slices.ContainsFunc(c.Runs, func(r core.RunSpan) bool { return r.ID == run }) {
			return keyed(svc.CancelRun(context.WithoutCancel(ctx), run))
		}
	}
	return keyed(ErrNoDraft)
}

// Follow makes the calling window follow its draft's conversation (an edit
// waiting, else the Assist one) and returns its history as read after
// subscribing. Events that come during the reads are held back and sent
// after: a run seen ending meanwhile shows as ended, read again. A run that
// ends later shows as Running: its end reaches the window after the reply,
// on the subscription, which goes on.
func (p *ComposePage) Follow(ctx context.Context) (WitHistory, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return WitHistory{}, keyed(err)
	}
	defer s.mu.Unlock()
	conv, err := openConversation(ctx, svc, s.draftID)
	if err != nil || conv == 0 {
		p.c.unfollow(win)
		return WitHistory{Messages: []WitMessage{}, Approvals: []Approval{}}, keyed(err)
	}
	f, err := p.c.follow(win, s, svc, conv, p.d.Emit)
	if err != nil {
		return WitHistory{}, keyed(err)
	}
	f.hold()
	defer f.release(win, p.d.Emit)
	h, err := history(ctx, svc, conv)
	if followHook != nil {
		followHook()
	}
	if ended := h.Running; err == nil && ended != 0 && f.endedWhileHeld(ended) {
		h, err = history(ctx, svc, conv)
		if h.Running == ended { // its end is in what is held back; a run started since stays
			h.Running = 0
		}
	}
	if err != nil {
		return WitHistory{}, keyed(err)
	}
	return h, nil
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
	h := WitHistory{Conversation: conv, Messages: witMessages(c), Approvals: []Approval{}}
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
	out := Citation{MessageID: c.MessageID, Subject: c.Subject, From: c.From, Suspect: c.Suspect}
	if !c.Date.IsZero() {
		out.Date = c.Date.UnixMilli()
	}
	return out
}

func approvalOf(a core.Approval) Approval {
	return Approval{ID: a.ID, Run: a.RunID, CallID: a.CallID, Tool: a.Tool, Summary: a.Summary, State: a.State,
		Error: a.Error, Destinations: nz(a.Destinations)}
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

// BodyChange is the draft after a Wit action, read again: Body is its
// editable HTML as stored, which the page adopts with the Change at once.
type BodyChange struct {
	Change
	// Body is the editable HTML as stored.
	Body string `json:"body"`
}

// RunStart is a ⌘K run started, or Conflict: the draft is not at the
// revision, or its text is not the text the page sent. Revision is the
// draft's stored revision.
type RunStart struct {
	Run      int64 `json:"run"`
	Revision int64 `json:"revision"`
	Conflict bool  `json:"conflict"`
}

var (
	// ErrBadDecisions is returned for decisions that are not one 1 (accept)
	// or -1 (reject) per change of an inline review.
	ErrBadDecisions = errors.New("one decision, 1 or -1, per change of an inline review")
	// ErrBadSelection is returned for an empty selection or one outside the text.
	ErrBadSelection = errors.New("the selection is empty or outside the text")
	// ErrBadLink is returned for a link that is not http, https or mailto.
	ErrBadLink = errors.New("only http, https and mailto links open")
)

// pendingApproval is approval id of the draft's conversations, waiting for
// a decision; else core.ErrInvalidState.
func pendingApproval(ctx context.Context, svc *core.Service, draftID, id int64) (core.Approval, error) {
	cs, err := draftConversations(ctx, svc, draftID)
	if err != nil {
		return core.Approval{}, err
	}
	for _, c := range cs {
		as, err := svc.ListApprovals(ctx, c.ID)
		if err != nil {
			return core.Approval{}, err
		}
		for _, a := range as {
			if a.ID == id && a.State == "pending" {
				return a, nil
			}
		}
	}
	return core.Approval{}, core.ErrInvalidState
}

// pendingEdit is pendingApproval for a draft edit only.
func pendingEdit(ctx context.Context, svc *core.Service, draftID, id int64) (core.Approval, error) {
	a, err := pendingApproval(ctx, svc, draftID, id)
	if err == nil && !isDraftEdit(a.Tool) {
		err = core.ErrInvalidState
	}
	return a, err
}

// Review is approval, a draft edit of the calling window's draft waiting
// for a decision, as the page reviews it.
func (p *ComposePage) Review(ctx context.Context, approval int64) (Review, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return Review{}, keyed(err)
	}
	defer s.mu.Unlock()
	a, err := pendingEdit(ctx, svc, s.draftID, approval)
	if err != nil {
		return Review{}, keyed(err)
	}
	return newProposal(a).review(), nil
}

// Decide accepts or rejects approval, one of the calling window's draft.
// The window follows its conversation first, so the run it resumes streams
// on WitStream. A draft changed since the edit was proposed is Conflict.
func (p *ComposePage) Decide(ctx context.Context, approval int64, accept bool) (BodyChange, error) {
	return p.decide(ctx, approval, func(svc *core.Service, a core.Approval) error {
		return svc.ResolveApproval(context.WithoutCancel(ctx), a.ID, accept)
	})
}

// Finish settles approval, an inline draft edit, with a decision per change
// (Review.Changes; 1 accepts, -1 rejects): all accepted or all rejected
// decides it; a mix applies the text the decisions make. As Decide.
func (p *ComposePage) Finish(ctx context.Context, approval int64, decisions []int) (BodyChange, error) {
	return p.decide(ctx, approval, func(svc *core.Service, a core.Approval) error {
		r := newProposal(a)
		if !r.inline() || len(decisions) != len(r.changes) || slices.ContainsFunc(decisions, func(d int) bool { return d != 1 && d != -1 }) {
			return ErrBadDecisions
		}
		ctx := context.WithoutCancel(ctx)
		if accept := !slices.Contains(decisions, -1); accept || !slices.Contains(decisions, 1) {
			return svc.ResolveApproval(ctx, a.ID, accept)
		}
		return svc.ApplyEditedProposal(ctx, a.ID, richtext.ToHTML(r.result(decisions)))
	})
}

// decide runs act on pending approval of the calling window's draft, the
// window following its conversation first, and reads the draft after.
func (p *ComposePage) decide(ctx context.Context, approval int64, act func(*core.Service, core.Approval) error) (BodyChange, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	defer s.mu.Unlock()
	a, err := pendingApproval(ctx, svc, s.draftID, approval)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	if _, err := p.c.follow(win, s, svc, a.ConversationID, p.d.Emit); err != nil {
		return BodyChange{}, keyed(err)
	}
	return bodyChange(ctx, svc, s, act(svc, a))
}

// bodyChange is the draft as stored after an action that ended in err:
// ErrStaleDraft is a Conflict.
func bodyChange(ctx context.Context, svc *core.Service, s *session, err error) (BodyChange, error) {
	if errors.Is(err, core.ErrStaleDraft) {
		return BodyChange{Change: conflict}, nil
	}
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	return BodyChange{Change: changeOf(d), Body: s.adopt(d)}, nil
}

// Undo takes back the latest edit Wit applied to the calling window's
// draft (core UndoLastAssist).
func (p *ComposePage) Undo(ctx context.Context) (BodyChange, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	defer s.mu.Unlock()
	return bodyChange(ctx, svc, s, svc.UndoLastAssist(context.WithoutCancel(ctx), s.draftID))
}

// Rewrite rewrites [start, end) of the calling window's draft as
// instruction says (⌘K, core RewriteSelection). The page saved revision
// and sends its text and the selection in it in UTF-16 offsets (the
// plainText contract); a draft not at revision, or with another text, is
// Conflict. The run's events go to the window on WitStream.
func (p *ComposePage) Rewrite(ctx context.Context, revision int64, text string, start, end int, instruction string) (RunStart, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return RunStart{}, keyed(err)
	}
	defer s.mu.Unlock()
	d, err := svc.GetDraft(ctx, s.draftID)
	if err != nil {
		return RunStart{}, keyed(err)
	}
	editable, _, _ := mail.SplitDraft(d.HTML)
	stored, offs := plainText(editable)
	if d.Revision != revision || stored != text {
		return RunStart{Revision: d.Revision, Conflict: true}, nil
	}
	if start < 0 || start >= end || end >= len(offs) || offs[start] < 0 || offs[end] < 0 {
		return RunStart{}, keyed(ErrBadSelection)
	}
	sel := core.Selection{Revision: revision, Start: offs[start], End: offs[end]}
	ch, err := svc.RewriteSelection(context.WithoutCancel(ctx), s.draftID, sel, instruction)
	if errors.Is(err, core.ErrStaleDraft) {
		return RunStart{Revision: d.Revision, Conflict: true}, nil
	}
	if err != nil {
		return RunStart{}, keyed(err)
	}
	cs, err := draftConversations(ctx, svc, s.draftID)
	var c core.Conversation
	if err == nil && len(cs) > 0 { // the newest: the run's, made before it returned
		c, err = svc.GetConversation(ctx, cs[len(cs)-1].ID)
	}
	if err == nil {
		err = p.c.followRun(win, s, c.ID, ch, p.d.Emit)
	}
	if err != nil {
		go func() {
			for range ch {
			}
		}()
		return RunStart{}, keyed(err)
	}
	run := c.ActiveRunID
	if run == 0 && len(c.Runs) > 0 { // over already
		run = c.Runs[len(c.Runs)-1].ID
	}
	return RunStart{Run: run, Revision: d.Revision}, nil
}

// OpenLink opens a link from Wit's answer: an http or https one in the
// browser; a mailto one as a new draft to its addresses, from the calling
// window's account, in a compose window. Any other is ErrBadLink.
func (p *ComposePage) OpenLink(ctx context.Context, link string) error {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return keyed(err)
	}
	draftID := s.draftID
	s.mu.Unlock()
	u, err := url.Parse(link)
	if err != nil {
		return keyed(ErrBadLink)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return keyed(p.d.OpenURL(link))
	case "mailto":
	default:
		return keyed(ErrBadLink)
	}
	addrs, _ := url.PathUnescape(u.Opaque)
	var to []string
	for _, a := range strings.Split(addrs, ",") {
		if a = strings.TrimSpace(a); a != "" {
			to = append(to, a)
		}
	}
	from, err := svc.GetDraft(ctx, draftID)
	if err != nil {
		return keyed(err)
	}
	d, err := svc.NewDraft(ctx, from.AccountID, "new", 0)
	if err == nil {
		d.To = to
		d, err = svc.SaveDraft(ctx, d)
	}
	if err != nil {
		return keyed(err)
	}
	p.d.OpenCompose(d.ID)
	return nil
}
