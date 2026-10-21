package webui

import (
	"cmp"
	"context"
	"errors"
	netmail "net/mail"
	"net/url"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/richtext"
)

// WitInfo is what the Wit panel offers for the draft.
type WitInfo struct {
	// Enabled means the panel shows: a model is configured, or a plugin translates.
	Enabled bool `json:"enabled"`
	// TranslateOnly: no model, but a plugin translates. The panel offers its
	// 翻译 menu alone: no input, no 调整, no chips.
	TranslateOnly bool `json:"translateOnly"`
	// Skills are the 调整 menu's; Languages the 翻译 menu's, which the panel
	// orders: the app's language first.
	Skills    []Skill    `json:"skills"`
	Languages []Language `json:"languages"`
	// Chips name what Wit knows of the draft.
	Chips []string `json:"chips"`
	// Model is the default model's label, as the input's chip.
	Model string `json:"model"`
}

// Skill is a 调整 menu item.
type Skill struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Hint is what the skill changes, shown beside its label.
	Hint string `json:"hint"`
}

// Language is a 翻译 menu item.
type Language struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
}

// adjustSkills are the 调整 menu's skills, in its order.
var adjustSkills = []string{"tone-formal", "tone-friendly", "shorter", "longer", "proofread"}

// languages are the 翻译 menu's common targets (app/settings_general.go).
var languages = []Language{
	{"de", "Deutsch"}, {"zh-Hans", "简体中文"}, {"zh-Hant", "繁體中文"}, {"en", "English"}, {"ja", "日本語"},
	{"ko", "한국어"}, {"fr", "Français"}, {"es", "Español"}, {"pt", "Português"}, {"it", "Italiano"},
	{"ru", "Русский"}, {"nl", "Nederlands"}, {"tr", "Türkçe"}, {"vi", "Tiếng Việt"},
}

func isZhTW(tag string) bool {
	t, _, _ := strings.Cut(strings.ToLower(strings.ReplaceAll(tag, "_", "-")), ".")
	return strings.HasPrefix(t, "zh-") && (t == "zh-tw" || strings.HasSuffix(t, "-tw"))
}

// langName is a language tag's name in the menu.
func langName(tag string, zhTW bool) string {
	switch t := strings.ToLower(tag); {
	case strings.HasPrefix(t, "de"):
		return "Deutsch"
	case strings.HasPrefix(t, "en-gb"):
		return "English (UK)"
	case strings.HasPrefix(t, "en-us"):
		return "English (US)"
	case strings.HasPrefix(t, "en"):
		return "English"
	case strings.HasPrefix(t, "zh-tw"), strings.HasPrefix(t, "zh-hant"), strings.HasPrefix(t, "zh-hk"), strings.HasPrefix(t, "zh-mo"):
		if zhTW {
			return "正體中文"
		}
		return "繁體中文"
	case strings.HasPrefix(t, "zh"):
		return "简体中文"
	case strings.HasPrefix(t, "ja"):
		return "日本語"
	case strings.HasPrefix(t, "ko"):
		return "한국어"
	case strings.HasPrefix(t, "fr"):
		return "Français"
	case strings.HasPrefix(t, "es"):
		return "Español"
	case strings.HasPrefix(t, "pt"):
		return "Português"
	case strings.HasPrefix(t, "it"):
		return "Italiano"
	case strings.HasPrefix(t, "ru"):
		return "Русский"
	case strings.HasPrefix(t, "nl"):
		return "Nederlands"
	case strings.HasPrefix(t, "tr"):
		return "Türkçe"
	case strings.HasPrefix(t, "vi"):
		return "Tiếng Việt"
	case strings.HasPrefix(t, "uk"):
		return "Українська"
	case strings.HasPrefix(t, "pl"):
		return "Polski"
	case strings.HasPrefix(t, "ar"):
		return "العربية"
	case strings.HasPrefix(t, "hi"):
		return "हिन्दी"
	case strings.HasPrefix(t, "th"):
		return "ไทย"
	case strings.HasPrefix(t, "id"):
		return "Bahasa Indonesia"
	}
	return "English"
}

// startHook, when set, runs in a run's start before the window subscribes;
// followHook in Follow after its reads (tests).
var startHook, followHook func()

// follow makes window win, whose session is s, follow conversation conv:
// its subscription to conv if it has one, else a new one in place of what
// it followed. Every event goes to emit. A session gone (Release, Detach)
// gets ErrNoDraft: those end the subscription after removing the session,
// both under c.mu, so none is left behind.
func (c *Compose) follow(win int, s *session, svc *core.Service, conv int64, emit func(Target, WitEvent)) (*follow, error) {
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
	go f.pump(Target{Window: win}, ch, emit)
	return f, nil
}

// followRun makes window win follow run's own events ch, of conversation
// conv, in place of what it followed: RewriteSelection makes its
// conversation, so nothing could subscribe to it before the run. A
// decision or Follow later subscribes to the conversation.
func (c *Compose) followRun(win int, s *session, conv int64, ch <-chan core.WitEvent, emit func(Target, WitEvent)) error {
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
	go f.pump(Target{Window: win}, ch, emit)
	return nil
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
	ts, err := svc.ListTranslators(ctx)
	if err != nil {
		return WitInfo{}, err
	}
	// translate-draft runs on a running plugin without a model (core assistSkill)
	plugin := st.Translator != "" && slices.ContainsFunc(ts, func(t core.TranslatorInfo) bool { return t.ID == st.Translator && t.Running })
	info := WitInfo{Enabled: st.DefaultModel != "" || plugin, TranslateOnly: st.DefaultModel == "" && plugin, Chips: []string{}, Model: modelLabel(st.DefaultModel)}
	for _, id := range adjustSkills {
		info.Skills = append(info.Skills, Skill{ID: id, Label: i18n.T("wit.skill." + id), Hint: i18n.T("wit.skill." + id + ".hint")})
	}
	zhTW := isZhTW(svc.SystemLanguage())
	read := svc.ReadingLanguage(ctx)

	// The system's translation offers only what is downloaded: none leaves the menu its hint.
	if st.Translator == "plugin/"+core.SystemTranslationPlugin && svc.SystemTranslator() != nil {
		info.Languages = []Language{}
		for _, code := range svc.SystemTranslator().InstalledLanguages() {
			info.Languages = append(info.Languages, Language{Tag: code, Name: langName(code, zhTW)})
		}
	} else {
		info.Languages = []Language{{read, langName(read, zhTW)}}
		for _, l := range languages {
			if zhTW && l.Tag == "zh-Hant" {
				l.Name = "正體中文"
			}
			if !slices.ContainsFunc(info.Languages, func(x Language) bool { return x.Name == l.Name }) {
				info.Languages = append(info.Languages, l)
			}
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

// openConversation is the draft's conversation to show: the newest with a
// run going on (a ⌘K run outlives its window), else the newest with an edit
// waiting, else its Assist conversation, else 0.
func openConversation(ctx context.Context, svc *core.Service, draftID int64) (int64, error) {
	cs, err := draftConversations(ctx, svc, draftID)
	if err != nil {
		return 0, err
	}
	for _, c := range slices.Backward(cs) {
		c, err := svc.GetConversation(ctx, c.ID)
		if err != nil {
			return 0, err
		}
		if c.ActiveRunID != 0 {
			return c.ID, nil
		}
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

// Ask asks Wit about the calling window's draft, with the mail the user
// quoted (message IDs; core AssistWith), and returns the run's ID; its
// events go to the window on WitStream.
func (p *ComposePage) Ask(ctx context.Context, input string, quoted []int64) (int64, error) {
	return p.start(ctx, func(ctx context.Context, svc *core.Service, id int64) (<-chan core.WitEvent, error) {
		return svc.AssistWith(ctx, id, input, quoted)
	})
}

// SearchMail is the quote picker's search (core Search, every account).
func (p *ComposePage) SearchMail(ctx context.Context, query string) ([]MailHit, error) {
	_, svc, s, err := p.open(ctx)
	if err != nil {
		return nil, keyed(err)
	}
	s.mu.Unlock()
	return searchMail(ctx, svc, query)
}

// MailHit is a message the quote picker offers.
type MailHit struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	// Date is in Unix milliseconds; 0 for none.
	Date int64 `json:"date"`
}

// quoteHits caps the quote picker's results.
const quoteHits = 8

func searchMail(ctx context.Context, svc *core.Service, query string) ([]MailHit, error) {
	ms, err := svc.Search(ctx, 0, query, quoteHits)
	out := make([]MailHit, 0, len(ms))
	for _, m := range ms {
		h := MailHit{ID: m.ID, Subject: m.Subject, From: m.From}
		if !m.Date.IsZero() {
			h.Date = m.Date.UnixMilli()
		}
		out = append(out, h)
	}
	return out, keyed(err)
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

// Follow makes the calling window follow its draft's conversation (a run
// going on, else an edit waiting, else the Assist one) and returns its
// history as read after subscribing. Events that come during the reads are held back and sent
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
	h, err := f.read(ctx, svc, Target{Window: win}, p.d.Emit)
	return h, keyed(err)
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
// draft's stored revision. For WitPage.Ask, Conversation is the run's
// conversation (made for a new one).
type RunStart struct {
	Run          int64 `json:"run"`
	Conversation int64 `json:"conversation"`
	Revision     int64 `json:"revision"`
	Conflict     bool  `json:"conflict"`
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
	return pendingWhere(ctx, svc, draftID, func(a core.Approval) bool { return a.ID == id })
}

// pendingWhere is the first approval of the draft's conversations waiting
// for a decision that is; else core.ErrInvalidState.
func pendingWhere(ctx context.Context, svc *core.Service, draftID int64, is func(core.Approval) bool) (core.Approval, error) {
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
			if is(a) && a.State == "pending" {
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

// Decide accepts or rejects approval, any pending one of the calling
// window's draft (a draft edit or a tool's call). The window follows its
// conversation first, so the run it resumes streams on WitStream. A draft
// changed since the edit was proposed is Conflict.
func (p *ComposePage) Decide(ctx context.Context, approval int64, accept bool) (BodyChange, error) {
	return p.decide(ctx, func(a core.Approval) bool { return a.ID == approval }, func(svc *core.Service, a core.Approval) error {
		return svc.ResolveApproval(context.WithoutCancel(ctx), a.ID, accept)
	})
}

// DecideAll accepts or rejects every pending approval of run, one of the
// calling window's draft (core ResolveAll; 全部允许). As Decide.
func (p *ComposePage) DecideAll(ctx context.Context, run int64, accept bool) (BodyChange, error) {
	return p.decide(ctx, func(a core.Approval) bool { return a.RunID == run }, func(svc *core.Service, a core.Approval) error {
		return svc.ResolveAll(context.WithoutCancel(ctx), a.RunID, accept)
	})
}

// Finish settles approval, an inline draft edit, with a decision per change
// (Review.Changes; 1 accepts, -1 rejects): all accepted or all rejected
// decides it; a mix applies the text the decisions make. As Decide.
func (p *ComposePage) Finish(ctx context.Context, approval int64, decisions []int) (BodyChange, error) {
	return p.decide(ctx, func(a core.Approval) bool { return a.ID == approval }, func(svc *core.Service, a core.Approval) error {
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

// decide runs act on the pending approval of the calling window's draft
// that is, the window following its conversation first, and reads the
// draft after.
func (p *ComposePage) decide(ctx context.Context, is func(core.Approval) bool, act func(*core.Service, core.Approval) error) (BodyChange, error) {
	win, svc, s, err := p.open(ctx)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	defer s.mu.Unlock()
	a, err := pendingWhere(ctx, svc, s.draftID, is)
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
	ch, err := changeOf(ctx, svc, d)
	if err != nil {
		return BodyChange{}, keyed(err)
	}
	return BodyChange{Change: ch, Body: s.adopt(d)}, nil
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
	stored, lo, hi := plainText(editable)
	if d.Revision != revision || stored != text {
		return RunStart{Revision: d.Revision, Conflict: true}, nil
	}
	if start < 0 || start >= end || end >= len(lo) || lo[start] < 0 || lo[end] < 0 {
		return RunStart{}, keyed(ErrBadSelection)
	}
	sel := core.Selection{Revision: revision}
	sel.Start, sel.End = selectionRange(editable, lo, hi, start, end)
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

// OpenModels opens the model settings (the input's model chip).
func (p *ComposePage) OpenModels(ctx context.Context) error {
	_, _, s, err := p.open(ctx)
	if err != nil {
		return keyed(err)
	}
	s.mu.Unlock()
	if p.d.OpenModels != nil {
		p.d.OpenModels()
	}
	return nil
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
	return keyed(openLink(ctx, svc, link, func() (int64, error) {
		d, err := svc.GetDraft(ctx, draftID)
		return d.AccountID, err
	}, p.d.OpenURL, p.d.OpenCompose))
}

// openLink opens link as OpenLink does, a mailto one from account's account.
func openLink(ctx context.Context, svc *core.Service, link string, account func() (int64, error),
	openURL func(string) error, openCompose func(int64)) error {
	u, err := url.Parse(link)
	if err != nil {
		return ErrBadLink
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return openURL(link)
	case "mailto":
	default:
		return ErrBadLink
	}
	addrs, _ := url.PathUnescape(u.Opaque)
	var to []string
	for _, a := range strings.Split(addrs, ",") {
		if a = strings.TrimSpace(a); a != "" {
			to = append(to, a)
		}
	}
	acc, err := account()
	if err != nil {
		return err
	}
	d, err := svc.NewDraft(ctx, acc, "new", 0)
	if err == nil {
		d.To = to
		d, err = svc.SaveDraft(ctx, d)
	}
	if err != nil {
		return err
	}
	openCompose(d.ID)
	return nil
}
