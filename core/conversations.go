package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Conversation is a Wit chat: kind "ask" (the mailbox panel) or "assist" (one
// draft's compose panel, DraftID set). Turns are its questions and answers;
// ActiveRunID is its run that is running, resuming or waiting for approvals
// (0: none). ListConversations leaves both empty.
type Conversation struct {
	ID                 int64
	Kind, Title, Model string
	Effort             string // the reasoning effort its runs ask for; "" sends none
	DraftID            int64
	Turns              []Turn
	ActiveRunID        int64
	UpdatedAt          time.Time
	// Usage sums its runs' tokens; Last is the latest run's that reported any.
	// HistoryUse is how full the history budget is, its summary standing in
	// (from 0; past 1, its oldest turns are summarized or left out). ListConversations
	// leaves both empty. Compactable: CompactConversation has turns to
	// summarize.
	Usage      ai.Usage
	HistoryUse float64
	// SummaryUse is the part of HistoryUse its summary takes (0: none).
	SummaryUse  float64
	Compactable bool
	// Answering means a run of it is running or resuming (or an allowed
	// call of it executes); Awaiting that one waits for the user to decide
	// its calls.
	Answering, Awaiting bool
}

// WitEvent is one event of a Wit run of conversation ConversationID, for
// its turn Turn (Turn.ID). A run that starts (not a resume) sends "start"
// first; an Assist run then a "context" event whose Citations are the mail
// its context shows the model. An interrupted event carries the approvals
// it recorded, in call order. Ends: the run is over for now, done, failed,
// or waiting on the user (an interrupt whose calls were all decided at
// once goes on in a resume instead).
type WitEvent struct {
	ai.Event
	ConversationID int64
	RunID          int64
	Turn           string
	Ends           bool
	Citations      []Citation
	Approvals      []Approval
}

// Citation is a message a run's tools showed the model: RunID's call CallID
// (its tool_result), or when CallID is "" what Source says: mail the user
// quoted (citeQuote) or an assist run's context (citeContext). N is the message's
// citation number in the conversation (from 1, by first citation): the
// model knows the mail by it alone (its id) and cites it as <mail id="N"/>.
type Citation struct {
	N             int
	MessageID     int64
	Subject, From string
	Date          time.Time
	Suspect       bool
	Quote         string // the suspect text, from where it starts (ai.InjectionQuote): show as plain text
	Snippet       string // the message's snippet, for the card that shows it
	Gone          bool   // the message was deleted since: nothing to open or quote
	RunID         int64
	CallID        string
	Source        string
}

// Citation sources with no call. citeUnknown is an assist run's saved before
// sources were: either, so it counts as both.
const (
	citeQuote   = "quote"
	citeContext = "context"
	citeUnknown = "unknown"
)

// ErrRunActive: the conversation has a run that is running, waiting for
// approvals or resuming.
var ErrRunActive = errors.New("the conversation already has an active run")

// NewConversation starts an empty conversation: kind "ask" without a draft,
// or "assist" on draftID.
func (s *Service) NewConversation(ctx context.Context, kind string, draftID int64) (Conversation, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Conversation{}, err
	}
	defer end()
	if (kind != "ask" || draftID != 0) && (kind != "assist" || draftID == 0) {
		return Conversation{}, fmt.Errorf("invalid conversation: kind %q, draft %d", kind, draftID)
	}
	if err := s.checkWitDraft(ctx, draftID); err != nil {
		return Conversation{}, err
	}
	now := time.Now().Unix()
	r, err := s.db.Execute(ctx, "INSERT INTO conversations(kind, draft_id, title, model, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)",
		kind, nullID(draftID), now, now)
	if err != nil {
		return Conversation{}, err
	}
	return s.conversation(ctx, r.LastInsertID)
}

const convCols = "id, kind, title, model, draft_id, updated_at, effort FROM conversations"

func scanConversation(r []any) Conversation {
	return Conversation{ID: store.Int(r[0]), Kind: store.Str(r[1]), Title: store.Str(r[2]), Model: store.Str(r[3]),
		DraftID: store.Int(r[4]), UpdatedAt: time.Unix(store.Int(r[5]), 0), Effort: store.Str(r[6])}
}

// conversation returns the conversation without its history (history).
func (s *Service) conversation(ctx context.Context, id int64) (Conversation, error) {
	rows, err := s.db.Query(ctx, "SELECT "+convCols+" WHERE id = ?", id)
	if err != nil {
		return Conversation{}, err
	}
	if len(rows.Values) == 0 {
		return Conversation{}, fmt.Errorf("conversation %d not found", id)
	}
	return scanConversation(rows.Values[0]), nil
}

// ListConversations returns the conversations of kind, most recent first.
func (s *Service) ListConversations(ctx context.Context, kind string) ([]Conversation, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT "+convCols+" WHERE kind = ? ORDER BY updated_at DESC, id DESC", kind)
	if err != nil {
		return nil, err
	}
	out := make([]Conversation, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, scanConversation(r))
	}
	acts, err := s.activity(ctx, "c.kind = ?", kind)
	if err != nil {
		return nil, err
	}
	for _, a := range acts {
		if i := slices.IndexFunc(out, func(c Conversation) bool { return c.ID == a.conv }); i >= 0 {
			out[i].ActiveRunID, out[i].Answering, out[i].Awaiting = a.run, a.answering, a.awaiting
		}
	}
	return out, nil
}

// active is a conversation's active run and what it does.
type active struct {
	conv, run           int64
	answering, awaiting bool
}

// activity is the active runs of the conversations where holds (on c):
// under way, waiting on pending calls, or interrupted while an allowed call
// executes (an external one, up to its timeout).
func (s *Service) activity(ctx context.Context, where string, args ...any) ([]active, error) {
	rows, err := s.db.Query(ctx, `SELECT r.conversation_id, r.id, r.state, EXISTS(SELECT 1 FROM approvals a WHERE a.run_id = r.id AND a.state = 'pending'),
		EXISTS(SELECT 1 FROM approvals a WHERE a.run_id = r.id AND a.state = 'executing')
		FROM runs r JOIN conversations c ON c.id = r.conversation_id WHERE `+where+` AND r.state IN ('running', 'resuming', 'interrupted')`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]active, 0, len(rows.Values))
	for _, r := range rows.Values {
		a := active{conv: store.Int(r[0]), run: store.Int(r[1]), answering: true}
		if store.Str(r[2]) == "interrupted" {
			a.awaiting = store.Int(r[3]) != 0
			a.answering = !a.awaiting && store.Int(r[4]) != 0
		}
		out = append(out, a)
	}
	return out, nil
}

// SetConversationModel makes the conversation's next runs use ref ("slug/model"
// or a plugin's; "" goes back to Settings.DefaultModel) and ask for effort
// ("low", "medium" or "high"; "" asks for none, as non-reasoning models need).
// Both are checked before either is kept: a ref that does not resolve or an
// unknown effort changes nothing. A run going on keeps what it started with.
func (s *Service) SetConversationModel(ctx context.Context, id int64, ref, effort string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if !slices.Contains([]string{"", "low", "medium", "high"}, effort) {
		return fmt.Errorf("unknown reasoning effort %q", effort)
	}
	if ref != "" {
		if _, ref, err = s.resolveRef(ctx, ref); err != nil {
			return err
		}
	}
	r, err := s.db.Execute(ctx, "UPDATE conversations SET model = ?, effort = ? WHERE id = ?", ref, effort, id)
	if err == nil && r.Changes == 0 {
		err = fmt.Errorf("conversation %d not found", id)
	}
	return err
}

// GetConversation returns the conversation with its messages.
func (s *Service) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Conversation{}, err
	}
	defer end()
	c, err := s.conversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	h, err := s.history(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	acts, err := s.activity(ctx, "c.id = ?", id)
	if err != nil {
		return Conversation{}, err
	}
	if len(acts) > 0 {
		c.ActiveRunID, c.Answering, c.Awaiting = acts[0].run, acts[0].answering, acts[0].awaiting
	}
	runs, err := s.conversationRuns(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	as, err := s.approvals(ctx, "r.conversation_id = ?", id)
	if err != nil {
		return Conversation{}, err
	}
	cs, err := s.citationsOf(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	expand := func(m ai.Message) ai.Message { // the saved history keeps the references the model wrote
		x := s.linkExpander() // per message, as each was expanded live: history grows past maxExpandMessages
		m.Text, m.Reasoning = x.text(ctx, m.Text), x.text(ctx, m.Reasoning)
		m.Calls = slices.Clone(m.Calls)
		for j, call := range m.Calls {
			m.Calls[j].Args, _ = x.args(ctx, call.Args)
		}
		return m
	}
	c.Turns = turnsOf(h, runs, as, cs, c.Kind == "ask" && c.ActiveRunID == 0, expand)
	rows, err := s.db.Query(ctx, `SELECT COALESCE(SUM(input_tokens), 0) + (SELECT spent_input FROM conversations WHERE id = ?),
		COALESCE(SUM(output_tokens), 0) + (SELECT spent_output FROM conversations WHERE id = ?),
		COALESCE((SELECT context_tokens FROM runs WHERE conversation_id = ? AND context_tokens > 0 ORDER BY id DESC LIMIT 1), 0)
		FROM runs WHERE conversation_id = ?`, id, id, id, id)
	if err != nil {
		return Conversation{}, err
	}
	if len(rows.Values) > 0 {
		r := rows.Values[0]
		c.Usage = ai.Usage{Input: int(store.Int(r[0])), Output: int(store.Int(r[1])), Last: int(store.Int(r[2]))}
	}
	sum, _, err := s.convSummary(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	size, err := h.CompactedSize(sum)
	if err != nil {
		return Conversation{}, err
	}
	budget := s.refHistoryBudget(ctx, c.Model)
	c.Compactable = h.Compactable(sum, compactKeep(budget))
	c.HistoryUse = float64(size) / float64(budget)
	if sum.Text != "" && sum.Upto > 0 {
		c.SummaryUse = min(float64(utf8.RuneCountInString(sum.Text))/float64(budget), c.HistoryUse)
	}
	return c, nil
}

// runUsage adds res's tokens to runID's.
func runUsage(runID int64, u ai.Usage) sqlite.Statement {
	return sqlite.Statement{SQL: `UPDATE runs SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
		context_tokens = CASE WHEN ? > 0 THEN ? ELSE context_tokens END WHERE id = ?`, Args: []any{u.Input, u.Output, u.Last, u.Last, runID}}
}

// citationsOf is conversation id's saved citations, numbered.
func (s *Service) citationsOf(ctx context.Context, id int64) ([]Citation, error) {
	rows, err := s.db.Query(ctx, `SELECT rc.message_id, rc.subject, rc.from_addr, rc.date, rc.suspect, rc.run_id, rc.call_id, rc.quote, m.snippet, m.id IS NULL, rc.source
		FROM run_citations rc JOIN runs r ON r.id = rc.run_id LEFT JOIN messages m ON m.id = rc.message_id
		WHERE r.conversation_id = ? ORDER BY rc.id`, id)
	if err != nil {
		return nil, err
	}
	var out []Citation
	for _, r := range rows.Values {
		ct := Citation{MessageID: store.Int(r[0]), Subject: store.Str(r[1]), From: store.Str(r[2]), Suspect: store.Int(r[4]) != 0,
			RunID: store.Int(r[5]), CallID: store.Str(r[6]), Quote: store.Str(r[7]), Snippet: gomail.Snippet(store.Str(r[8])), Gone: store.Int(r[9]) != 0,
			Source: store.Str(r[10])}
		if d := store.Int(r[3]); d != 0 {
			ct.Date = time.Unix(d, 0)
		}
		out = append(out, ct)
	}
	numberCitations(out, map[int64]int{})
	return out, nil
}

// numberCitations sets each N from refs (message ID -> N), giving a message
// not in it the next number. The one numbering of citations: a run numbers
// new ones the same way and saves them at once, so the saved order agrees.
func numberCitations(cs []Citation, refs map[int64]int) {
	for i := range cs {
		n, ok := refs[cs[i].MessageID]
		if !ok {
			n = len(refs) + 1
			refs[cs[i].MessageID] = n
		}
		cs[i].N = n
	}
}

// keptRefs numbers job's citations as the conversation will have them once
// asked again in place of the question with seq at (commitRewind): only the
// runs it keeps count, and job's own (a resume's citations so far).
func (s *Service) keptRefs(ctx context.Context, job *runJob, at int64) error {
	rows, err := s.db.Query(ctx, "SELECT id FROM runs WHERE conversation_id = ? AND (hist_from IS NULL OR hist_from < ? OR id = ?)", job.conv.ID, at, job.id)
	if err != nil {
		return err
	}
	kept := map[int64]bool{}
	for _, r := range rows.Values {
		kept[store.Int(r[0])] = true
	}
	saved, err := s.citationsOf(ctx, job.conv.ID)
	if err != nil {
		return err
	}
	saved = slices.DeleteFunc(saved, func(c Citation) bool { return !kept[c.RunID] })
	job.refs = map[int64]int{}
	numberCitations(saved, job.refs)
	return nil
}

// number numbers cs, which job's call shows the model (call "": source
// does, citeQuote or citeContext), and saves them.
func (s *Service) number(ctx context.Context, job *runJob, call, source string, cs []Citation) []Citation {
	ctx = context.WithoutCancel(ctx)
	job.refsMu.Lock()
	defer job.refsMu.Unlock()
	s.loadRefs(ctx, job)
	for i := range cs {
		cs[i].RunID, cs[i].CallID, cs[i].Source = job.id, call, source
	}
	numberCitations(cs, job.refs)
	if len(cs) > 0 {
		s.saveCitations(ctx, job.id, cs)
		s.snippets(ctx, cs)
	}
	return cs
}

// loadRefs reads job's conversation's citation numbers on first use; job.refsMu is held.
func (s *Service) loadRefs(ctx context.Context, job *runJob) {
	if job.refs != nil {
		return
	}
	if job.rewind != nil { // a question asked again, resumed: number as its launch did, without what it replaces
		err := s.keptRefs(ctx, job, job.rewind.from)
		if err == nil {
			return
		}
		slog.Default().Warn("could not read a conversation's citations", "conversation", job.conv.ID, "err", err)
	}
	job.refs = map[int64]int{}
	saved, err := s.citationsOf(ctx, job.conv.ID)
	if err != nil {
		slog.Default().Warn("could not read a conversation's citations", "conversation", job.conv.ID, "err", err)
	}
	numberCitations(saved, job.refs)
}

// mailOf is the message the model knows as mail n in job's conversation: the
// mail its tools showed it with that id (citation number).
func (s *Service) mailOf(ctx context.Context, job *runJob, n int) (int64, error) {
	job.refsMu.Lock()
	defer job.refsMu.Unlock()
	s.loadRefs(context.WithoutCancel(ctx), job)
	if _, ok := job.refs[int64(n)]; ok && job.oldIDs { // a message ID the run's tools showed it before mail ids
		return int64(n), nil
	}
	for id, k := range job.refs {
		if k == n {
			return id, nil
		}
	}
	return 0, fmt.Errorf("mail %d %w: use the id of a mail the tools returned", n, errMailNotFound)
}

// contextCites numbers the mail an assist run's context shows (citeContext),
// as its prompt names it, and saves what is not saved yet: all of it for a
// run's start; for a resume, whose first drive saved the rest, only mail the
// draft brought since.
func (s *Service) contextCites(ctx context.Context, job *runJob, cs []Citation) []Citation {
	if !job.resumed {
		return s.number(ctx, job, "", citeContext, cs)
	}
	job.refsMu.Lock()
	s.loadRefs(context.WithoutCancel(ctx), job)
	var fresh []Citation
	for i := range cs {
		if n, ok := job.refs[cs[i].MessageID]; ok {
			cs[i].N = n
		} else {
			fresh = append(fresh, cs[i])
		}
	}
	job.refsMu.Unlock()
	for _, f := range s.number(ctx, job, "", citeContext, fresh) {
		cs[slices.IndexFunc(cs, func(c Citation) bool { return c.MessageID == f.MessageID })] = f
	}
	return cs
}

// snippets fills in cs's messages' snippets (citationsOf reads them the same way).
func (s *Service) snippets(ctx context.Context, cs []Citation) {
	ids := make([]any, len(cs))
	for i, c := range cs {
		ids[i] = c.MessageID
	}
	rows, err := s.db.Query(ctx, `SELECT id, snippet FROM messages WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, ids...)
	if err != nil {
		slog.Default().Warn("could not read cited messages' snippets", "err", err)
		return
	}
	by := map[int64]string{}
	for _, r := range rows.Values {
		by[store.Int(r[0])] = gomail.Snippet(store.Str(r[1]))
	}
	for i := range cs {
		snip, ok := by[cs[i].MessageID]
		cs[i].Snippet, cs[i].Gone = snip, !ok
	}
}

// saveCitations records what a run's event cites, for GetConversation.
func (s *Service) saveCitations(ctx context.Context, runID int64, cs []Citation) {
	stmts := make([]sqlite.Statement, 0, len(cs))
	for _, c := range cs {
		var date any // NULL without one
		if !c.Date.IsZero() {
			date = c.Date.Unix()
		}
		suspect := 0
		if c.Suspect {
			suspect = 1
		}
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO run_citations(run_id, call_id, message_id, subject, from_addr, date, suspect, quote, source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, Args: []any{runID, c.CallID, c.MessageID, c.Subject, c.From, date, suspect, c.Quote, c.Source}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		slog.Default().Warn("could not save a run's citations", "run", runID, "err", err)
	}
}

// RunConversation is the conversation runID is a run of.
func (s *Service) RunConversation(ctx context.Context, runID int64) (int64, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT conversation_id FROM runs WHERE id = ?", runID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("run %d not found", runID)
	}
	return store.Int(rows.Values[0][0]), nil
}

// DeleteConversation deletes the conversation with its runs and approvals. A
// run in flight or an approval executing makes it ErrRunActive; cancel first.
func (s *Service) DeleteConversation(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	r, err := s.db.Execute(ctx, `DELETE FROM conversations WHERE id = ? AND NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id = ?
		AND (state IN ('running', 'resuming') OR EXISTS (SELECT 1 FROM approvals WHERE run_id = runs.id AND state = 'executing')))`, id, id)
	if err != nil {
		return err
	}
	if r.Changes > 0 {
		s.dropGrants(id)
		s.reclaimSoon()
		return nil
	}
	if _, err := s.conversation(ctx, id); err != nil {
		return err
	}
	return ErrRunActive
}

// witStream delivers a run's events in order through ch. A reader that falls
// behind loses the oldest text-like events once more than witBuffer wait, but
// never an event that ends a run: there is one per run, so they are kept.
// Delivery ends early only on stop (unsubscribe) or when the Service closes
// (end): the rest is discarded and ch is closed, so a stalled reader holds the
// stream at most until then.
type witStream struct {
	ch      chan WitEvent
	end     <-chan struct{} // the Service's close; nil for none
	mu      sync.Mutex
	q       []WitEvent
	waiting int                  // events in q that do not end a run
	closing bool                 // no more pushes; ch closes once q is delivered
	held    bool                 // q waits for release (FollowConversation)
	skip    func(run int64) bool // runs whose events are not pushed (nil: none)
	wake    chan struct{}        // 1 slot: q, closing or held changed
	quit    chan struct{}        // closed by stop
	stopped sync.Once
}

const witBuffer = 64

func newWitStream() *witStream { return startStream(nil, nil) }

func startStream(end <-chan struct{}, wg *sync.WaitGroup) *witStream {
	w := &witStream{ch: make(chan WitEvent), end: end, wake: make(chan struct{}, 1), quit: make(chan struct{})}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		w.pump()
	}()
	return w
}

// newStream is a stream that the Service's Close ends. The caller holds subsMu
// or a begin registration, so it never starts while Close waits for streams.
func (s *Service) newStream() *witStream { return startStream(s.streamsEnd, &s.streams) }

func terminal(ev WitEvent) bool {
	return ev.Kind == "done" || ev.Kind == "error" || ev.Kind == "interrupted"
}

func (w *witStream) push(ev WitEvent) {
	w.mu.Lock()
	if w.closing || w.skip != nil && w.skip(ev.RunID) {
		w.mu.Unlock()
		return
	}
	w.q = append(w.q, ev)
	if !terminal(ev) {
		if w.waiting++; w.waiting > witBuffer {
			i := slices.IndexFunc(w.q, func(e WitEvent) bool { return !terminal(e) })
			w.q = slices.Delete(w.q, i, i+1)
			w.waiting--
		}
	}
	w.mu.Unlock()
	w.signal()
}

func (w *witStream) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// close delivers what is queued, then closes ch.
func (w *witStream) close() {
	w.mu.Lock()
	w.closing = true
	w.mu.Unlock()
	w.signal()
}

// stop drops what is queued and closes ch now.
func (w *witStream) stop() { w.stopped.Do(func() { close(w.quit) }) }

// release delivers what keep keeps of what was held back, then the rest but
// the events of runs skip drops.
func (w *witStream) release(keep func([]WitEvent) []WitEvent, skip func(run int64) bool) {
	w.mu.Lock()
	w.q, w.skip = keep(w.q), skip
	w.waiting = 0
	for _, ev := range w.q {
		if !terminal(ev) {
			w.waiting++
		}
	}
	w.held = false
	w.mu.Unlock()
	w.signal()
}

func (w *witStream) pump() {
	defer close(w.ch)
	for {
		w.mu.Lock()
		if len(w.q) == 0 || w.held {
			closing := w.closing && !w.held
			w.mu.Unlock()
			if closing {
				return
			}
			select {
			case <-w.wake:
			case <-w.quit:
				return
			case <-w.end:
				return
			}
			continue
		}
		ev := w.q[0] // taken out first, so push never drops the event in flight
		w.q = w.q[1:]
		if !terminal(ev) {
			w.waiting--
		}
		w.mu.Unlock()
		select {
		case w.ch <- ev:
		case <-w.quit:
			return
		case <-w.end:
			return
		}
	}
}

// SubscribeConversation streams the events of the conversation's runs,
// including resumes after approvals, until the returned func is called or the
// Service closes; either closes the channel and discards what is still queued.
// A slow reader loses the oldest text but never an event that ends a run.
// After Close the channel is closed.
func (s *Service) SubscribeConversation(id int64) (<-chan WitEvent, func()) {
	w, stop := s.subscribe(id, false)
	return w.ch, stop
}

// subscribe is SubscribeConversation's stream, held back if held.
func (s *Service) subscribe(id int64, held bool) (*witStream, func()) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	if s.subsClosed {
		w := &witStream{ch: make(chan WitEvent)}
		close(w.ch)
		return w, func() {}
	}
	w := s.newStream()
	w.held = held
	if s.subs == nil {
		s.subs = map[int64]map[*witStream]bool{}
	}
	if s.subs[id] == nil {
		s.subs[id] = map[*witStream]bool{}
	}
	s.subs[id][w] = true
	return w, func() {
		s.subsMu.Lock()
		delete(s.subs[id], w)
		s.subsMu.Unlock()
		w.stop()
	}
}

// FollowConversation is the conversation as it is now (GetConversation) and
// its runs' events from then on, none missed and none twice: a run whose
// saved history is in what was read sends nothing more, and one waiting on
// the user sends what comes after its interrupt. stop ends the events, as
// SubscribeConversation's.
func (s *Service) FollowConversation(ctx context.Context, id int64) (Conversation, <-chan WitEvent, func(), error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Conversation{}, nil, nil, err
	}
	defer end()
	w, stop := s.subscribe(id, true)
	// A read is kept only if no run of the conversation moved meanwhile:
	// otherwise what it saved may be half in it. Runs move a few times each,
	// and a new one waits for the user: the reads settle.
	var c Conversation
	for try := 0; ; try++ {
		before, err := s.runStates(ctx, id)
		if err == nil && try == 0 && beforeFollowRead != nil {
			beforeFollowRead()
		}
		if err == nil {
			c, err = s.GetConversation(ctx, id)
		}
		var after string
		if err == nil {
			after, err = s.runStates(ctx, id)
		}
		if err != nil {
			stop()
			return Conversation{}, nil, nil, err
		}
		if after == before {
			break
		}
		if err := ctx.Err(); err != nil {
			stop()
			return Conversation{}, nil, nil, err
		}
	}
	read := map[int64]bool{} // runs whose saved history is in c
	for _, t := range c.Turns {
		for _, r := range t.Runs {
			read[r] = true
		}
	}
	w.release(func(q []WitEvent) []WitEvent {
		// a run waiting on the user was read with its interrupt: what it sent up to that is in c
		last := -1
		for i, ev := range q {
			if c.Awaiting && ev.RunID == c.ActiveRunID && ev.Kind == "interrupted" {
				last = i
			}
		}
		out := q[:0]
		for i, ev := range q {
			if ev.RunID == c.ActiveRunID && i > last || ev.RunID != c.ActiveRunID && !read[ev.RunID] {
				out = append(out, ev)
			}
		}
		return out
	}, func(run int64) bool { return run != c.ActiveRunID && read[run] })
	return c, w.ch, stop, nil
}

// runStates is where conversation id's runs stand, to tell whether one
// moved between two reads.
func (s *Service) runStates(ctx context.Context, id int64) (string, error) {
	rows, err := s.db.Query(ctx, "SELECT id, state, COALESCE(hist_to, 0), updated_at FROM runs WHERE conversation_id = ? ORDER BY id", id)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(rows.Values), nil
}

// endStreams ends every stream, subscriptions and startRun channels alike,
// once no run is left to publish, and waits for their goroutines, which never
// wait for a reader once ended.
func (s *Service) endStreams() {
	s.subsMu.Lock()
	s.subsClosed = true
	s.subs = nil
	close(s.streamsEnd)
	s.subsMu.Unlock()
	s.streams.Wait()
}

func (s *Service) publishWit(convID int64, ev WitEvent) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for w := range s.subs[convID] {
		w.push(ev)
	}
}

// toolDesc is a tool Wit runs can offer. Every call goes through the gateway
// as Source (empty: wit) and Extension, with Egress(scope) as its
// destinations. A call that needs no approval runs in the run (Spec.Run). A
// call that does (Write, Spec.NeedsApproval or egress after mail was read)
// becomes an approval, prepared and, once accepted, executed by Mode:
//   - tx (empty): Exec, whose statements commit with the approval (plan 3);
//   - external: Prepare, then Run outside the approval lock within Timeout
//     (default externalTimeout), its outcome recorded afterwards. Binding,
//     if set, is recorded when proposed and checked before Run: a change
//     makes the approval stale.
type toolDesc struct {
	Spec      ai.ToolSpec
	Source    string
	Extension string
	Write     bool
	Egress    func(scope *gateway.Scope) []string
	Mode      string
	Exec      *executor
	Prepare   func(ctx context.Context, conv Conversation, args json.RawMessage) (prepared, error)
	Run       func(ctx context.Context, a approvalRow) (result any, err error)
	Binding   func(ctx context.Context) (string, error)
	Timeout   time.Duration
}

const modeExternal = "external"

// registerTool offers t to Wit runs; a name already registered is an error.
func (s *Service) registerTool(t toolDesc) error {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if _, ok := s.tools[t.Spec.Name]; ok {
		return fmt.Errorf("tool %s is already registered", t.Spec.Name)
	}
	if s.tools == nil {
		s.tools = map[string]toolDesc{}
	}
	s.tools[t.Spec.Name] = t
	return nil
}

// mustRegister registers a built-in tool, whose name cannot collide.
func (s *Service) mustRegister(t toolDesc) {
	if err := s.registerTool(t); err != nil {
		panic(err)
	}
}

// registered returns the named tools. A name no longer registered becomes a
// write tool without an executor, whose calls are denied, so a resumed run
// can still answer the calls made to it.
func (s *Service) registered(names ...string) []toolDesc {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	out := make([]toolDesc, 0, len(names))
	for _, n := range names {
		t, ok := s.tools[n]
		if !ok {
			t = unavailable(n)
		}
		out = append(out, t)
	}
	return out
}

// unavailable is a tool no longer offered: a write tool without an executor,
// whose calls are denied.
func unavailable(name string) toolDesc {
	return toolDesc{Spec: ai.ToolSpec{Name: name, Description: "No longer available."}, Write: true}
}

// offered applies a conversation kind's tool policy, the start's (Ask,
// Assist) as well as a resume's: Assist offers no plugin write tool (ADR
// 0003) and no untrusted MCP tool, so one that became one since is unavailable.
func offered(kind string, tools []toolDesc) []toolDesc {
	for i, t := range tools {
		if kind == "assist" && (t.Source == gateway.SourcePlugin || t.Source == gateway.SourceMCP) && t.Write {
			tools[i] = unavailable(t.Spec.Name)
		}
	}
	return tools
}

func (s *Service) desc(name string) (toolDesc, bool) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	t, ok := s.tools[name]
	return t, ok
}

// runJob is a run in flight, started or resumed.
type runJob struct {
	id       int64
	conv     Conversation
	scope    *gateway.Scope
	round    int      // seq of the first approval the next interrupt records
	prepared sync.Map // call ID -> prepared, stored by write tools' gates
	cites    sync.Map // call ID -> []Citation, stored by read tools
	refsMu   sync.Mutex
	refs     map[int64]int // message ID -> citation number, read on first use (number)
	// oldIDs: a resumed run begun before mail ids (snapshot.MailIDs): calls it
	// held back may name mail by message ID (mailOf)
	oldIDs  bool
	clued   sync.Map    // recipients create_draft has shown language clues for
	base    *ai.History // the history the run started from (its question asked again: before it)
	after   *ai.History // what the run ended with (drive), and the changes that save it
	changes ai.HistoryChanges
	resumed bool   // a resume: the run's context was shown and cited already
	turn    string // the turn its question starts (Turn.ID)
	// host, set for a host-made proposal (AssistSkill), turns the finished
	// tool-less run into an interrupt on the proposal; it is not resumed. Its
	// command is the question the history keeps for it.
	host    func(ctx context.Context, res ai.Result) ai.Result
	command ai.Command
	// rewind, for a question asked in place, is what the run replaces once it
	// is done (commitRewind); one that ends otherwise replaces nothing.
	rewind *pendingRewind
	// summary is what its first request sent in place of the earlier turns:
	// a resume sends it again, whatever a compaction stored since.
	summary *ai.Summary
	cited   []Citation // the mail the run's context shows the model, sent first
	quoted  []Citation // the mail the user quoted, numbered (quote): sent first too
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

// snapshot is runs.snapshot: what a resume needs besides the conversation state.
type snapshot struct {
	ReadMail bool        `json:"read_mail"`
	Round    int         `json:"round"`             // seq of the current interrupt's first approval
	Host     bool        `json:"host,omitempty"`    // a host-made proposal: nothing to resume
	Effort   string      `json:"effort,omitempty"`  // the run's reasoning effort
	Summary  *ai.Summary `json:"summary,omitempty"` // runJob.summary; none from before it was kept
	// MailIDs: the run's tools know a mail by its citation number, its id (blocks);
	// a run from before took message IDs
	MailIDs bool `json:"mail_ids,omitempty"`
}

type runKey struct{}

// runOf is the run whose tool is given ctx.
func runOf(ctx context.Context) *runJob { return ctx.Value(runKey{}).(*runJob) }

// witMaxTurns is the tool-call rounds of one drive of a Wit run (its start,
// or a resume after an approval or a question); at it the model answers from
// what it found.
const witMaxTurns = 20

// request builds the run's request on h, its summary (for a resume, the one
// its first request sent) standing in and the whole fitted to historyBudget
// (ai.History.Send); the job keeps h, which the request's view restores into
// once the run is done.
func (s *Service) request(ctx context.Context, job *runJob, model ai.ResolvedModel, h *ai.History, tools []toolDesc) (ai.Request, error) {
	system, err := s.witSystem(ctx, job, tools)
	if err != nil {
		return ai.Request{}, err
	}
	if job.summary == nil {
		sum, _, err := s.convSummary(ctx, job.conv.ID)
		if err != nil {
			return ai.Request{}, err
		}
		if job.rewind != nil && sum.Upto >= job.rewind.from { // it condenses what the question asked again replaces
			sum = ai.Summary{}
		}
		job.summary = &sum
	}
	sent, err := h.Send(s.historyBudget(model.BaseURL, model.Model), *job.summary, job.scope.ReadMail())
	if err != nil {
		return ai.Request{}, err
	}
	job.base = h
	return ai.Request{Model: model, System: system, History: sent, Tools: s.specs(job, tools), MaxTurns: witMaxTurns, Reasoning: true, Effort: job.conv.Effort,
		Args: s.linkExpander().toolArgs}, nil // link references expanded before a call is validated, gated, prepared or run
}

// track registers a run in flight. The caller holds runMu.
func (s *Service) track(ctx context.Context, id int64, conv Conversation, scope *gateway.Scope, round int) *runJob {
	ctx, cancel := context.WithCancel(ctx)
	job := &runJob{id: id, conv: conv, scope: scope, round: round, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if s.active == nil {
		s.active = map[int64]*runJob{}
	}
	s.active[id] = job
	return job
}

// untrack ends tracking job. The caller holds runMu.
func (s *Service) untrack(job *runJob) {
	if s.active[job.id] == job { // a resume may already track this run ID
		delete(s.active, job.id)
	}
	job.cancel()
	close(job.done)
}

// startRun runs prompt in conv with tools. Its events also go to the
// conversation's subscribers; the channel ends with done, error or interrupted
// (then the run waits for its approvals and resumes in the background).
func (s *Service) startRun(ctx context.Context, conv Conversation, prompt string, tools []toolDesc) (<-chan WitEvent, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	w, err := s.launch(ctx, end, conv, prompt, tools, nil, 0)
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

// launch claims a run and hands end to it. The time note of an ask or assist
// run (timeNote) and the quoted mail (checked IDs, quote) go before the
// prompt. at, unless 0, asks the prompt in place of the question with that
// seq: the run starts on the history before it, which it replaces once it is
// done (commitRewind); till then the conversation shows it without what it
// replaces. claimed, if set, are saved once the run is claimed and before
// the hand-off, not when asking in place; their error fails the run and
// launch.
func (s *Service) launch(ctx context.Context, end func(), conv Conversation, prompt string, tools []toolDesc,
	quoted []int64, at int64, claimed ...sqlite.Statement) (*witStream, error) {
	conv, err := s.conversation(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	model, ref, err := s.resolveRef(ctx, conv.Model) // the run stores the slug form
	if err != nil {
		return nil, err
	}
	e, err := s.aiEngine()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Spec.Name)
	}

	if beforeRunClaim != nil {
		beforeRunClaim(ctx)
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	job, h, err := s.claimRun(ctx, conv, ref, names)
	if err != nil {
		return nil, err
	}
	if at > 0 {
		var sum ai.Summary
		var ch ai.HistoryChanges
		var kept *ai.History
		if sum, _, err = s.convSummary(ctx, conv.ID); err == nil {
			kept, ch, err = h.Rewind(at, sum)
		}
		if err == nil {
			job.rewind = &pendingRewind{from: at, to: h.Next(), dropSummary: ch.DropSummary, first: kept.Len() == 0}
			h = kept
			err = s.keptRefs(ctx, job, at)
		}
	}
	var req ai.Request
	if err == nil {
		req, err = s.request(ctx, job, model, h, tools)
	}
	var note, quote string
	if err == nil && (conv.Kind == "ask" || conv.Kind == "assist") {
		note = timeNote(req.History.Contexts(), time.Now())
	}
	if err == nil && len(quoted) > 0 {
		quote, err = s.quote(ctx, job, quoted)
	}
	if err == nil && job.rewind != nil {
		if err = s.exec(ctx, claimRewind(job.id, *job.rewind)); err == nil { // claimed: only then are they set aside
			s.setGrantsAside(conv.ID, job.id)
		}
	} else if err == nil && len(claimed) > 0 {
		_, err = s.db.Transaction(ctx, claimed)
	}
	if err != nil {
		s.untrack(job)
		return nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), runState(job.id, "failed")))
	}
	req.History = req.History.Ask(prompt, strings.Join(slices.DeleteFunc([]string{note, quote}, func(c string) bool { return c == "" }), "\n\n"))
	out := s.newStream()
	go s.drive(job, end, e.Run, req, out)
	return out, nil
}

// claimRun records a running run of conv unless the conversation has an
// active one (ErrRunActive) and tracks it; h is the conversation's history
// as of the claim. A draft in the outbox gets none (ErrStaleDraft). The caller
// holds runMu.
func (s *Service) claimRun(ctx context.Context, conv Conversation, ref string, tools []string) (*runJob, *ai.History, error) {
	if err := s.checkWitDraft(ctx, conv.DraftID); err != nil {
		return nil, nil, err
	}
	toolsJSON, _ := json.Marshal(tools)
	snap, _ := json.Marshal(snapshot{MailIDs: true})
	now := time.Now().Unix()
	r, err := s.db.Execute(ctx, `INSERT INTO runs(conversation_id, state, model, tools, snapshot, created_at, updated_at)
		SELECT ?, 'running', ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id = ?
		AND state IN ('running', 'interrupted', 'resuming'))`, conv.ID, ref, string(toolsJSON), snap, now, now, conv.ID)
	if err != nil {
		return nil, nil, err
	}
	if r.Changes == 0 {
		return nil, nil, ErrRunActive
	}
	// Read the history only now: a previous run may have ended after the caller's read.
	h, err := s.history(ctx, conv.ID)
	if err == nil { // no run is active: open calls are left by one that failed
		h, err = s.closeOrphans(ctx, conv.ID, h)
	}
	var scope *gateway.Scope
	if err == nil {
		scope, err = s.convScope(ctx, conv.ID, false)
	}
	if err != nil {
		return nil, nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), runState(r.LastInsertID, "failed")))
	}
	return s.track(ctx, r.LastInsertID, conv, scope, 0), h, nil
}

// closeOrphans closes the open calls h ends with, which a run that failed
// without closing the state (its outcome could not be saved) left behind:
// each takes its approval's result where that was recorded, else
// msgRecovery. No run of conversation id may be active.
func (s *Service) closeOrphans(ctx context.Context, id int64, h *ai.History) (*ai.History, error) {
	calls := h.OpenCalls()
	if len(calls) == 0 {
		return h, nil
	}
	as, err := s.approvalRows(ctx, "r.conversation_id = ? AND a.state != 'pending'", id)
	if err != nil {
		return nil, err
	}
	recorded := map[string]ai.ToolResult{} // by call ID, the latest run's
	for _, a := range as {
		if a.State == "executing" { // an external run whose outcome could not be recorded
			a.result = msgUncertain
		}
		recorded[a.CallID] = a.toolResult()
	}
	for i, c := range calls {
		if rec, ok := recorded[c.CallID]; ok && rec.Name == c.Name {
			calls[i] = rec
		} else {
			calls[i].Error = msgRecovery
		}
	}
	closed, ch, err := h.Close(calls)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Transaction(ctx, historyStmts(id, ch))
	return closed, err
}

// convScope is a new scope for a run of conversation id: it has read mail if
// readMail or once any earlier run of the conversation did (its history may
// hold that mail).
func (s *Service) convScope(ctx context.Context, id int64, readMail bool) (*gateway.Scope, error) {
	rows, err := s.db.Query(ctx, "SELECT read_mail FROM conversations WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	scope := &gateway.Scope{}
	if readMail || len(rows.Values) > 0 && store.Int(rows.Values[0][0]) != 0 {
		scope.MarkReadMail()
	}
	return scope, nil
}

// markConvReadMail records that mail content entered conversation id.
func markConvReadMail(id int64) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE conversations SET read_mail = 1 WHERE id = ? AND read_mail = 0", Args: []any{id}}
}

// specs wraps the tools for one run: every call goes through the gateway as
// its tool's source, and a call it sends for approval is prepared first.
// Prepare fixes the final arguments the approval records; a call it rejects
// goes back to the model as an error, without an approval.
func (s *Service) specs(job *runJob, tools []toolDesc) []ai.ToolSpec {
	g := s.gateway()
	out := make([]ai.ToolSpec, 0, len(tools))
	for _, t := range tools {
		t.Source = cmp.Or(t.Source, gateway.SourceWit)
		if t.Source == gateway.SourcePlugin {
			t.Spec.Description = s.pluginToolDescription(t.Extension, t.Spec.Description)
		}
		spec := g.Wrap(job.scope, gateway.Call{Source: t.Source, Extension: t.Extension, Write: t.Write}, t.Spec, t.Egress)
		if run := spec.Run; run != nil {
			spec.Run = func(ctx context.Context, args json.RawMessage) (any, error) {
				return run(context.WithValue(ctx, runKey{}, job), args)
			}
		}
		prepare := t.Prepare
		if t.Mode != modeExternal {
			prepare = nil
			if t.Exec != nil {
				prepare = t.Exec.Prepare
			}
		}
		gate := spec.Gate
		spec.Gate = func(ctx context.Context, args json.RawMessage) (ai.Decision, error) {
			d, err := gate(ctx, args)
			if err != nil || d != ai.Approve {
				return d, err
			}
			if prepare == nil {
				return ai.Deny, errors.New("this call needs an approval that this tool cannot take")
			}
			ctx = context.WithValue(ctx, runKey{}, job)
			var binding string
			if t.Binding != nil { // before Prepare: a change in between makes the approval stale, never fresher
				if binding, err = t.Binding(ctx); err != nil {
					return ai.Deny, err
				}
			}
			p, err := prepare(ctx, job.conv, args)
			if err != nil {
				return ai.Deny, err
			}
			p.Binding, p.source, p.extension, p.Destinations = binding, t.Source, t.Extension, nil
			if t.Egress != nil {
				for _, h := range t.Egress(job.scope) {
					p.Destinations = append(p.Destinations, gateway.Host(h))
				}
			}
			job.prepared.Store(ai.CallID(ctx), p)
			return ai.Approve, nil
		}
		out = append(out, spec)
	}
	return out
}

// drive runs req, forwards its events and records how it ended before
// sending the event that ends it.
// runFunc runs a request: the engine's Run, or a stand-in that answers
// without a model (a plugin translating a draft).
type runFunc func(ctx context.Context, req ai.Request) (<-chan ai.Event, func() ai.Result)

// worked adds the time since start to run's time at work: its drives and its
// approved calls, not the user's wait to decide them.
func (s *Service) worked(ctx context.Context, run int64, start time.Time) {
	if err := s.exec(ctx, sqlite.Statement{SQL: "UPDATE runs SET active_ms = active_ms + ? WHERE id = ?",
		Args: []any{time.Since(start).Milliseconds(), run}}); err != nil {
		slog.Default().Warn("could not record a run's time", "run", run, "err", err)
	}
}

func (s *Service) drive(job *runJob, end func(), run runFunc, req ai.Request, out *witStream) {
	defer end()
	start := time.Now()
	// The Wit sidebar shows which ask conversations are answering: it re-reads as their runs
	// start and end (a compose window's runs change nothing it shows).
	if job.conv.Kind == "ask" {
		s.bus.Publish(events.Event{Type: events.RunsChanged, ConversationID: job.conv.ID})
		defer s.bus.Publish(events.Event{Type: events.RunsChanged, ConversationID: job.conv.ID})
	}
	defer func() {
		if beforeUntrack != nil {
			beforeUntrack(job.id)
		}
		s.runMu.Lock()
		s.untrack(job)
		s.runMu.Unlock()
	}()
	if job.turn == "" { // the question the run asks takes the next seq
		job.turn = strconv.FormatInt(job.base.Next(), 10)
	}
	emit := func(ev WitEvent) {
		// its Citations are numbered and saved (number): a reader that re-reads after it finds them
		ev.ConversationID, ev.RunID, ev.Turn = job.conv.ID, job.id, job.turn
		if out != nil {
			out.push(ev)
		}
		s.publishWit(job.conv.ID, ev)
	}
	if !job.resumed {
		emit(WitEvent{Event: ai.Event{Kind: "start"}})
	}
	cs := job.quoted
	if len(job.cited) > 0 && !job.resumed { // numbered and saved with the prompt (contextCites)
		cs = append(cs, job.cited...)
	}
	if len(cs) > 0 {
		emit(WitEvent{Event: ai.Event{Kind: "context"}, Citations: cs})
	}
	events, wait := run(job.ctx, req)
	for ev := range s.expandEvents(job.ctx, events) {
		if !terminal(WitEvent{Event: ev}) {
			wev := WitEvent{Event: ev}
			if ev.Kind == "tool_result" {
				if c, ok := job.cites.LoadAndDelete(ev.Result.CallID); ok {
					wev.Citations = c.([]Citation)
				}
			}
			emit(wev)
		}
	}
	ctx := context.WithoutCancel(job.ctx) // the outcome is recorded even when cancelled
	res := wait()
	s.worked(ctx, job.id, start) // each drive of it (a resume is another), before the outcome is told
	s.runRejected(req.Model, res.Err)
	if res.Err == nil {
		used := res.Usage // whatever res becomes, the model calls took these
		var err error
		if job.host != nil { // the history keeps the command, the proposal is the answer
			if res = job.host(ctx, res); res.Err == nil {
				job.after, job.changes, err = job.base.AddCommand(job.command)
			}
		} else { // the whole history, not just what this run was sent
			job.after, job.changes, err = req.History.Restore(res)
		}
		if err != nil {
			res = ai.Result{Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}
		}
		res.Usage = used
	}
	fin, err := s.settleRun(ctx, job, req, res)
	applied := false
	if err == nil && fin.Kind == "interrupted" {
		fin.Approvals, applied = s.applyInitial(ctx, fin.Approvals)
	}
	if err != nil {
		if _, ferr := s.db.Execute(ctx, "UPDATE runs SET state = 'failed', updated_at = ? WHERE id = ? AND state IN ('running', 'resuming')",
			time.Now().Unix(), job.id); ferr != nil {
			slog.Default().Warn("could not record a run's failure", "run", job.id, "err", ferr)
		}
		fin = WitEvent{Event: ai.Event{Kind: "error", Err: &ai.Error{Code: ai.CodeInternal, Msg: "could not save the run: " + err.Error()}}}
	}
	fin.Ends = !applied
	emit(fin)
	if out != nil {
		out.close()
	}
	if applied { // after the interrupt is out, so the resume's events follow it
		s.runMu.Lock()
		if err := s.resumeRun(ctx, job.id); err != nil {
			slog.Default().Warn("could not resume after a first generation", "run", job.id, "err", err)
		}
		s.runMu.Unlock()
	}
}

func (s *Service) exec(ctx context.Context, st sqlite.Statement) error {
	_, err := s.db.Execute(ctx, st.SQL, st.Args...)
	return err
}

// runSpan records that runID added the history's messages from seq from
// (its first save keeps its start) up to to.
func runSpan(runID, from, to int64) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE runs SET hist_from = COALESCE(hist_from, ?), hist_to = ? WHERE id = ?",
		Args: []any{from, to, runID}}
}

func runState(runID int64, state string) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE runs SET state = ?, updated_at = ? WHERE id = ?", Args: []any{state, time.Now().Unix(), runID}}
}

// settleRun records res: done saves the state; interrupted saves it with one
// pending approval per call, in one transaction; a failure keeps the saved
// state, closed with the approvals' results if the run was a resume.
func (s *Service) settleRun(ctx context.Context, job *runJob, req ai.Request, res ai.Result) (WitEvent, error) {
	// First, whatever the outcome: the history about to be saved may hold mail.
	if job.scope.ReadMail() {
		if err := s.exec(ctx, markConvReadMail(job.conv.ID)); err != nil {
			return WitEvent{}, err
		}
	}
	switch {
	case res.Err != nil:
		if job.rewind != nil && len(req.Responses) == 0 { // it replaced nothing: the grants set aside come back
			s.putGrantsBack(job.id)
		}
		state := "failed"
		if res.Err.Code == ai.CodeCanceled {
			state = "cancelled"
		}
		var err error
		if len(req.Responses) > 0 {
			_, err = s.closeRun(ctx, job.id, state, msgCancelled)
		} else {
			err = s.exec(ctx, runState(job.id, state))
		}
		// a failed or cut-off call may still have taken tokens
		err = errors.Join(err, s.exec(ctx, runUsage(job.id, res.Usage)))
		return WitEvent{Event: ai.Event{Kind: "error", Err: res.Err}}, err
	case len(res.Interrupts) == 0:
		stmts := append(historyStmts(job.conv.ID, job.changes), runState(job.id, "done"),
			runSpan(job.id, job.base.Next(), job.after.Next()), runUsage(job.id, res.Usage))
		if job.rewind != nil {
			stmts = append(stmts, commitRewind(job.conv.ID, job.id, *job.rewind, questionOf(job.after, job.rewind.to))...)
		}
		_, err := s.db.Transaction(ctx, stmts)
		if err == nil {
			s.forgetGrants(job.id)
			s.compactLater(job.conv.ID, req.Model, job.after)
		}
		return WitEvent{Event: ai.Event{Kind: "done"}}, err
	}
	now := time.Now().Unix()
	snap, _ := json.Marshal(snapshot{ReadMail: job.scope.ReadMail(), Round: job.round, Host: job.host != nil, Effort: req.Effort, Summary: job.summary, MailIDs: !job.oldIDs})
	stmts := append(historyStmts(job.conv.ID, job.changes), runSpan(job.id, job.base.Next(), job.after.Next()), runUsage(job.id, res.Usage),
		sqlite.Statement{SQL: "UPDATE runs SET state = 'interrupted', snapshot = ?, updated_at = ? WHERE id = ?", Args: []any{snap, now, job.id}})
	for i, c := range res.Interrupts {
		v, ok := job.prepared.Load(c.ID)
		if !ok {
			return WitEvent{}, fmt.Errorf("tool call %s was not prepared", c.Name)
		}
		p := v.(prepared)
		dests := []byte("[]")
		if len(p.Destinations) > 0 {
			dests, _ = json.Marshal(p.Destinations)
		}
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO approvals(run_id, seq, call_id, tool, args, summary, target_kind,
			target_id, target_version, state, destinations, binding, source, extension, input, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?)`,
			Args: []any{job.id, job.round + i, c.ID, c.Name, string(p.Args), p.Summary, p.TargetKind, nullID(p.TargetID), p.TargetVersion,
				string(dests), p.Binding, cmp.Or(p.source, gateway.SourceWit), p.extension, nullText(p.Input), now, now}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return WitEvent{}, err
	}
	s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: job.conv.ID})
	as, err := s.approvals(ctx, "a.run_id = ? AND a.seq >= ?", job.id, job.round)
	return WitEvent{Event: ai.Event{Kind: "interrupted", Interrupts: res.Interrupts}, Approvals: as}, err
}

// resume continues runID, which the caller claimed (interrupted → resuming)
// holding runMu, answering its calls with the approvals' results by seq. It
// runs in the background, registered with begin: Close cancels and waits.
// A resume that cannot start fails the run (failRun); the error is only a
// database error from recording that.
func (s *Service) resume(runID int64) error {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil { // closing: the next start resumes it
		_, err := s.db.Execute(context.Background(), "UPDATE runs SET state = 'interrupted' WHERE id = ? AND state = 'resuming'", runID)
		return err
	}
	if err := s.startResume(ctx, end, runID); err != nil {
		turn := s.runTurn(ctx, runID)
		convID, ferr := s.failRun(ctx, runID, err)
		end()
		s.publishWit(convID, WitEvent{ConversationID: convID, RunID: runID, Turn: turn, Ends: true,
			Event: ai.Event{Kind: "error", Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}})
		return ferr
	}
	return nil
}

func (s *Service) startResume(ctx context.Context, end func(), runID int64) error {
	rows, err := s.db.Query(ctx, "SELECT conversation_id, model, tools, snapshot FROM runs WHERE id = ?", runID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("run %d not found", runID)
	}
	r := rows.Values[0]
	var names []string
	var snap snapshot
	if err := errors.Join(json.Unmarshal([]byte(store.Str(r[2])), &names), json.Unmarshal([]byte(store.Str(r[3])), &snap)); err != nil {
		return err
	}
	if snap.Host { // decided: the proposal was the whole run
		if err := s.exec(ctx, runState(runID, "done")); err != nil {
			return err
		}
		end()
		s.publishWit(store.Int(r[0]), WitEvent{ConversationID: store.Int(r[0]), RunID: runID, Turn: s.runTurn(ctx, runID), Ends: true, Event: ai.Event{Kind: "done"}})
		return nil
	}
	conv, err := s.conversation(ctx, store.Int(r[0]))
	if err == nil {
		err = s.checkWitDraft(ctx, conv.DraftID) // queued while the run waited: it fails, as it would outlive the draft
	}
	if err != nil {
		return err
	}
	h, err := s.history(ctx, conv.ID)
	if err != nil {
		return err
	}
	rw, err := s.runRewind(ctx, runID)
	if err != nil {
		return err
	}
	as, err := s.approvalRows(ctx, "a.run_id = ? AND a.seq >= ?", runID, snap.Round)
	if err != nil {
		return err
	}
	results := make([]ai.ToolResult, 0, len(as))
	for _, a := range as {
		results = append(results, a.toolResult())
	}
	model, err := s.resolveModel(ctx, store.Str(r[1]))
	if err != nil {
		return err
	}
	e, err := s.aiEngine()
	if err != nil {
		return err
	}
	scope, err := s.convScope(ctx, conv.ID, snap.ReadMail)
	if err != nil {
		return err
	}
	job := s.track(ctx, runID, conv, scope, snap.Round+len(as))
	job.resumed, job.rewind, job.turn, job.summary, job.oldIDs = true, rw, s.runTurn(ctx, runID), snap.Summary, !snap.MailIDs
	req, err := s.request(ctx, job, model, h, offered(conv.Kind, s.registered(names...)))
	if err != nil {
		s.untrack(job)
		return err
	}
	req.Responses = results
	req.Effort = snap.Effort // as the run began: Anthropic refuses thinking switched on mid-turn
	go s.drive(job, end, e.Run, req, nil)
	return nil
}

// questionOf is the text of h's question with seq seq ("" for none).
func questionOf(h *ai.History, seq int64) string {
	for _, t := range h.Turns() {
		if t.Seq == seq {
			return t.Question.Text
		}
	}
	return ""
}

// runTurn is the turn runID's question starts: its span's start ("" for a
// run that saved none).
func (s *Service) runTurn(ctx context.Context, runID int64) string {
	rows, err := s.db.Query(ctx, "SELECT hist_from FROM runs WHERE id = ? AND hist_from IS NOT NULL", runID)
	if err != nil || len(rows.Values) == 0 {
		return ""
	}
	return strconv.FormatInt(store.Int(rows.Values[0][0]), 10)
}
