package webui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// ErrNotAsk is returned to the Wit page for a conversation that is not an
// ask one, or an approval or run not of the ask conversation it follows.
var ErrNotAsk = errors.New("the Wit page acts on its ask conversation only")

// errSuperseded ends a selection that a newer one replaced; a cancellation,
// so the page shows nothing for it.
var errSuperseded = fmt.Errorf("a newer conversation was opened: %w", context.Canceled)

// WitPageState is what the Wit page shows besides the conversation.
type WitPageState struct {
	// Conversation is the route's (0: a new one) as of Version, the
	// snapshot's: the page applies only newer ones (WitOpen).
	Conversation int64 `json:"conversation"`
	Version      int64 `json:"version"`
	// Model is the default model's label; "" for none.
	Model  string `json:"model"`
	Locale string `json:"locale"`
	Theme  Theme  `json:"theme"`
}

// WitOpenMsg tells the Wit page to show Conversation (0: a new one), the
// snapshot at Version. Created marks native taking the conversation the
// page's Ask made (SetCreated): the page's first question goes on in it,
// no switch.
type WitOpenMsg struct {
	Conversation int64 `json:"conversation"`
	Version      int64 `json:"version"`
	Created      bool  `json:"created"`
}

var (
	// WitOpen tells the Wit page the route's conversation changed.
	WitOpen = mygo.NewEvent[WitOpenMsg]("witOpen")
	// WitState tells the Wit page its model label, copy or theme changed
	// (the settings did); the conversation and its stream stay.
	WitState = mygo.NewEvent[WitPageState]("witState")
	// WitAsk hands the Wit page a question native collected (a search that
	// found nothing): the page asks it once, in the conversation it shows.
	WitAsk = mygo.NewEvent[string]("witAsk")
)

// WitPageDeps is what WitPage needs from the platform. The native callbacks
// post to the UI thread.
type WitPageDeps struct {
	// Emit sends a Wit event to the page (WitStream.EmitPage in production).
	Emit func(t Target, ev WitEvent)
	// Open and State send WitOpen and WitState to the page.
	Open  func(WitOpenMsg)
	State func(WitPageState)
	// Ask sends WitAsk to the page.
	Ask func(query string)
	// OpenMessage opens message id in the reader; OpenModels the model
	// settings; Navigate goes back (-1) or forward (1).
	OpenMessage func(id int64)
	OpenModels  func()
	Navigate    func(dir int)
	// ConversationCreated is told Ask made conversation id, asked under
	// snapshot version.
	ConversationCreated func(id, version int64)
	// OpenURL opens an http(s) link in the browser; OpenCompose opens draft
	// id in a compose window.
	OpenURL     func(url string) error
	OpenCompose func(draftID int64)
}

// Wit keeps the Wit page's state: the route's conversation as a versioned
// snapshot and the page's subscription. It is not bound: the page reaches
// it only through WitPage; native calls SetConversation and SettingsChanged.
type Wit struct {
	pages *Pages
	d     WitPageDeps

	decideMu sync.Mutex // decisions run one at a time, as they come

	mu      sync.Mutex
	svc     *core.Service
	conv    int64          // the snapshot: the route's conversation
	own     map[int64]bool // the runs the page's Ask started: Stop takes them wherever it is
	version int64          // bumped per SetConversation
	sel     uint64         // bumped per selection (Follow, Ask): only the newest subscribes
	f       *follow        // the page's subscription; nil for none
}

// NewWit is the Wit page's state for the pages of role "wit"; SetService
// opens it up.
func NewWit(pages *Pages, d WitPageDeps) *Wit { return &Wit{pages: pages, d: d} }

// SetService gives the page the service, once the data is open.
func (w *Wit) SetService(svc *core.Service) {
	w.mu.Lock()
	w.svc = svc
	w.mu.Unlock()
}

// WitPage is what the Wit page (role "wit", embedded in the main window)
// calls (mygo.Bind). It acts on ask conversations only.
type WitPage struct{ w *Wit }

// NewWitPage is the bound facade of w.
func NewWitPage(w *Wit) *WitPage { return &WitPage{w: w} }

// caller is the calling page, if it is the Wit page, and the service.
func (w *Wit) caller(ctx context.Context) (Target, *core.Service, error) {
	id, err := w.pages.Caller(ctx, "wit")
	if err != nil {
		return Target{}, nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.svc == nil {
		return Target{}, nil, ErrNotReady
	}
	return Target{Page: id}, w.svc, nil
}

// SetConversation is the route's conversation (0: a new one), as native
// navigates: a new snapshot version, sent to the page (WitOpen) and returned.
func (w *Wit) SetConversation(id int64) int64 { return w.open(id, false) }

// SetCreated is SetConversation for native taking the conversation the
// page's Ask made (ConversationCreated): WitOpen says Created, so the
// page's first question goes on in it.
func (w *Wit) SetCreated(id int64) int64 { return w.open(id, true) }

func (w *Wit) open(id int64, created bool) int64 {
	w.mu.Lock()
	w.version++
	m := WitOpenMsg{Conversation: id, Version: w.version, Created: created}
	w.conv = id
	w.mu.Unlock()
	w.d.Open(m)
	return m.Version
}

// Handoff gives the page a question to ask (WitAsk): the page starts the run
// through WitPage.Ask, so it follows it and Stop works. Native navigates to a
// new conversation first.
func (w *Wit) Handoff(query string) {
	if strings.TrimSpace(query) != "" {
		w.d.Ask(query)
	}
}

// SettingsChanged sends the page its state for st (WitState).
func (w *Wit) SettingsChanged(st core.Settings) { w.d.State(w.state(st)) }

func (w *Wit) state(st core.Settings) WitPageState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return WitPageState{Conversation: w.conv, Version: w.version, Model: modelLabel(st.DefaultModel), Locale: i18n.Language(), Theme: ThemeOf(st)}
}

// modelLabel is a default model's label: its name without the provider.
func modelLabel(model string) string {
	if _, m, ok := strings.Cut(model, "/"); ok {
		return m
	}
	return model
}

// Load is the page's state: the snapshot, the model, the copy and the theme.
func (p *WitPage) Load(ctx context.Context) (WitPageState, error) {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return WitPageState{}, err
	}
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return WitPageState{}, err
	}
	return p.w.state(st), nil
}

// selection starts a selection: an older one no longer subscribes.
func (w *Wit) selection() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sel++
	return w.sel
}

// followConv makes the page follow conv for selection sel: its
// subscription if it follows conv, else a new one in place of it. A newer
// selection since gets errSuperseded.
func (w *Wit) followConv(t Target, svc *core.Service, conv int64, sel uint64) (*follow, error) {
	w.mu.Lock()
	if sel != w.sel {
		w.mu.Unlock()
		return nil, errSuperseded
	}
	old := w.f
	if old != nil && old.conv == conv {
		w.mu.Unlock()
		return old, nil
	}
	var f *follow
	var ch <-chan core.WitEvent
	if conv != 0 {
		var stop func()
		ch, stop = svc.SubscribeConversation(conv)
		f = &follow{conv: conv, stop: stop}
	}
	w.f = f
	w.mu.Unlock()
	if old != nil {
		old.end()
	}
	if f != nil {
		go f.pump(t, ch, w.d.Emit)
	}
	return f, nil
}

// followed is the conversation the page follows; 0 for none.
func (w *Wit) followed() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0
	}
	return w.f.conv
}

// askConv checks conv is an ask conversation.
func askConv(ctx context.Context, svc *core.Service, conv int64) error {
	c, err := svc.GetConversation(ctx, conv)
	if err == nil && c.Kind != "ask" {
		err = ErrNotAsk
	}
	return err
}

// Follow makes the page follow conv, an ask conversation (0: none), and
// returns its history as read after subscribing (follow.read). The newest
// selection wins: an older Follow or Ask no longer subscribes.
func (p *WitPage) Follow(ctx context.Context, conv int64) (WitHistory, error) {
	t, svc, err := p.w.caller(ctx)
	if err != nil {
		return WitHistory{}, keyed(err)
	}
	sel := p.w.selection()
	if conv != 0 {
		if err := askConv(ctx, svc, conv); err != nil {
			return WitHistory{}, keyed(err)
		}
	}
	f, err := p.w.followConv(t, svc, conv, sel)
	if err != nil || f == nil {
		return WitHistory{Messages: []WitMessage{}, Approvals: []Approval{}}, keyed(err)
	}
	h, err := f.read(ctx, svc, t, p.w.d.Emit)
	return h, keyed(err)
}

// Ask asks input in conv, an ask conversation, with the mail the user quoted
// (message IDs; core AskWith), on model (a ModelChoice's Ref; "": the
// default), which the conversation keeps; conv 0 makes one first, which
// ConversationCreated hears of. rewind, unless -1, asks it in place of the
// question at that index (a question's ID is "q<rewind>") and what followed
// (core AskAgain): a refused question leaves the conversation as it was. The page follows conv from before the run
// starts, unless a newer selection came meanwhile. Only Stop cancels the
// run: a page reload does not.
func (p *WitPage) Ask(ctx context.Context, conv int64, input string, quoted []int64, model string, rewind int) (RunStart, error) {
	t, svc, err := p.w.caller(ctx)
	if err != nil {
		return RunStart{}, keyed(err)
	}
	if strings.TrimSpace(input) == "" {
		return RunStart{}, keyed(errors.New("the question is empty"))
	}
	sel := p.w.selection()
	p.w.mu.Lock()
	version := p.w.version
	p.w.mu.Unlock()
	made := conv == 0
	if made {
		c, err := svc.NewConversation(ctx, "ask", 0)
		if err != nil {
			return RunStart{}, keyed(err)
		}
		conv = c.ID
	} else if err := askConv(ctx, svc, conv); err != nil {
		return RunStart{}, keyed(err)
	}
	if err := svc.SetConversationModel(ctx, conv, model); err != nil {
		if made { // nothing was asked in it: no empty conversation is left behind
			_ = svc.DeleteConversation(context.WithoutCancel(ctx), conv)
			return RunStart{}, keyed(err)
		}
		return RunStart{Conversation: conv}, keyed(err)
	}
	if startHook != nil {
		startHook()
	}
	if _, err := p.w.followConv(t, svc, conv, sel); err != nil && !errors.Is(err, errSuperseded) {
		return RunStart{}, keyed(err)
	}
	var ch <-chan core.WitEvent
	if rewind >= 0 && !made {
		ch, err = svc.AskAgain(context.WithoutCancel(ctx), conv, rewind, input, quoted)
	} else {
		ch, err = svc.AskWith(context.WithoutCancel(ctx), conv, input, quoted)
	}
	if made { // titled by now, unless Ask failed
		p.w.created(conv, version)
	}
	if err != nil { // ErrRunActive: the active run goes on being followed
		return RunStart{Conversation: conv}, keyed(err)
	}
	c, err := svc.GetConversation(ctx, conv)
	id := c.ActiveRunID // claimed before Ask returned
	if err != nil || id == 0 {
		id = (<-ch).RunID // over already: its events are queued
	}
	go func() { // the subscription shows the same events
		for range ch {
		}
	}()
	p.w.mu.Lock()
	if p.w.own == nil {
		p.w.own = map[int64]bool{}
	}
	p.w.own[id] = true // ponytail: kept for the session; a few IDs per question
	p.w.mu.Unlock()
	return RunStart{Run: id, Conversation: conv}, nil
}

// created notes that Ask made conv under snapshot version: the snapshot
// takes it while it is still that version's new conversation.
func (w *Wit) created(conv, version int64) {
	w.mu.Lock()
	if w.version == version && w.conv == 0 {
		w.conv = conv
	}
	w.mu.Unlock()
	w.d.ConversationCreated(conv, version)
}

// Stop cancels run: one the page's Ask started (a Stop pressed before Ask
// returned may come after a switch), or one of the conversation it follows.
func (p *WitPage) Stop(ctx context.Context, run int64) error {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return keyed(err)
	}
	p.w.mu.Lock()
	own := p.w.own[run]
	p.w.mu.Unlock()
	if own {
		return keyed(svc.CancelRun(context.WithoutCancel(ctx), run))
	}
	conv := p.w.followed()
	if conv == 0 {
		return keyed(ErrNotAsk)
	}
	c, err := svc.GetConversation(ctx, conv)
	if err != nil {
		return keyed(err)
	}
	if c.ActiveRunID != run && !slices.ContainsFunc(c.Runs, func(r core.RunSpan) bool { return r.ID == run }) {
		return keyed(ErrNotAsk)
	}
	return keyed(svc.CancelRun(context.WithoutCancel(ctx), run))
}

// Decide accepts or rejects approval, one of the conversation the page
// follows: the run it resumes streams on that subscription.
func (p *WitPage) Decide(ctx context.Context, approval int64, accept bool) error {
	return p.decide(ctx, func(a core.Approval) bool { return a.ID == approval }, func(svc *core.Service) error {
		return svc.ResolveApproval(context.WithoutCancel(ctx), approval, accept)
	})
}

// DecideAll accepts or rejects every pending approval of run, one of the
// conversation the page follows (全部允许). As Decide.
func (p *WitPage) DecideAll(ctx context.Context, run int64, accept bool) error {
	return p.decide(ctx, func(a core.Approval) bool { return a.RunID == run }, func(svc *core.Service) error {
		return svc.ResolveAll(context.WithoutCancel(ctx), run, accept)
	})
}

// DecideSession accepts approval and allows its tool for the rest of the
// conversation (本次对话都允许; core ResolveApprovalForConversation). As Decide.
func (p *WitPage) DecideSession(ctx context.Context, approval int64) error {
	return p.decide(ctx, func(a core.Approval) bool { return a.ID == approval }, func(svc *core.Service) error {
		return svc.ResolveApprovalForConversation(context.WithoutCancel(ctx), approval)
	})
}

// SearchMail is the quote picker's search (core Search, every account).
func (p *WitPage) SearchMail(ctx context.Context, query string) ([]MailHit, error) {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return nil, keyed(err)
	}
	return searchMail(ctx, svc, query)
}

// decide runs act once an approval of the followed conversation is, one
// decision at a time.
func (p *WitPage) decide(ctx context.Context, is func(core.Approval) bool, act func(*core.Service) error) error {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return keyed(err)
	}
	p.w.decideMu.Lock()
	defer p.w.decideMu.Unlock()
	conv := p.w.followed()
	if conv == 0 {
		return keyed(ErrNotAsk)
	}
	as, err := svc.ListApprovals(ctx, conv)
	if err != nil {
		return keyed(err)
	}
	if !slices.ContainsFunc(as, is) {
		return keyed(ErrNotAsk)
	}
	return keyed(act(svc))
}

// OpenCited opens cited message id in the reader.
func (p *WitPage) OpenCited(ctx context.Context, message int64) error {
	if _, _, err := p.w.caller(ctx); err != nil {
		return keyed(err)
	}
	p.w.d.OpenMessage(message)
	return nil
}

// OpenLink opens a link from Wit's answer: an http or https one in the
// browser; a mailto one as a new draft to its addresses, from the default
// account, in a compose window. Any other is ErrBadLink.
func (p *WitPage) OpenLink(ctx context.Context, link string) error {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return keyed(err)
	}
	return keyed(openLink(ctx, svc, link, func() (int64, error) {
		accs, err := svc.ListAccounts(ctx)
		if err != nil || len(accs) == 0 {
			return 0, err
		}
		if i := slices.IndexFunc(accs, func(a core.Account) bool { return a.IsDefault }); i >= 0 {
			return accs[i].ID, nil
		}
		return accs[0].ID, nil
	}, p.w.d.OpenURL, p.w.d.OpenCompose))
}

// ModelChoice is a model Wit can answer with: Ref is what Ask takes, Label
// "service / model" as the settings show it.
type ModelChoice struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

// Models are the models a conversation can use: every generating service's,
// then the running plugins' (judges aside), as the settings' default model offers.
func (p *WitPage) Models(ctx context.Context) ([]ModelChoice, error) {
	_, svc, err := p.w.caller(ctx)
	if err != nil {
		return nil, keyed(err)
	}
	services, err := svc.ListModelServices(ctx)
	if err != nil {
		return nil, keyed(err)
	}
	plugins, err := svc.ListPluginModels(ctx)
	if err != nil {
		return nil, keyed(err)
	}
	out := []ModelChoice{}
	for _, s := range services {
		if s.Kind == "" {
			continue
		}
		for _, m := range s.Models {
			out = append(out, ModelChoice{Ref: s.Slug + "/" + m, Label: s.Name + " / " + m})
		}
	}
	for _, pm := range plugins {
		for _, m := range pm.Models {
			if !m.Judges() {
				out = append(out, ModelChoice{Ref: "plugin/" + pm.Plugin + "/" + m.ID, Label: pm.Plugin + " / " + m.Name})
			}
		}
	}
	return out, nil
}

// OpenModels opens the model settings.
func (p *WitPage) OpenModels(ctx context.Context) error {
	if _, _, err := p.w.caller(ctx); err != nil {
		return keyed(err)
	}
	p.w.d.OpenModels()
	return nil
}

// ErrBadDirection is returned for a Navigate that is not -1 or 1.
var ErrBadDirection = errors.New("navigate goes back (-1) or forward (1)")

// Navigate goes back (-1) or forward (1), as ⌘[ and ⌘] do natively.
func (p *WitPage) Navigate(ctx context.Context, dir int) error {
	if _, _, err := p.w.caller(ctx); err != nil {
		return keyed(err)
	}
	if dir != -1 && dir != 1 {
		return keyed(ErrBadDirection)
	}
	p.w.d.Navigate(dir)
	return nil
}
