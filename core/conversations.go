package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
)

// Conversation is a Wit chat: kind "ask" (the mailbox panel) or "assist" (one
// draft's compose panel, DraftID set). Messages is decoded from the saved
// model state, for display; Citations are the mail its runs showed the
// model, in the order they came, each with the run and tool call that showed
// it (a message shown twice is there twice); Runs are the parts of Messages
// each run added, in run order; ActiveRunID is its run that is
// running, resuming or waiting for approvals (0: none). ListConversations
// leaves those four empty.
type Conversation struct {
	ID                 int64
	Kind, Title, Model string
	Effort             string // the reasoning effort its runs ask for; "" sends none
	DraftID            int64
	Messages           []ai.Message
	Citations          []Citation
	Runs               []RunSpan
	ActiveRunID        int64
	UpdatedAt          time.Time
	// Usage sums its runs' tokens; Last is the latest run's that reported any.
	// HistoryUse is how full the history budget is (from 0; past 1, the
	// oldest turns are left out of what the model is shown). ListConversations
	// leaves both empty.
	Usage      ai.Usage
	HistoryUse float64
}

// WitEvent is one event of a Wit run. An interrupted event carries the
// approvals it recorded, in call order. An Assist run starts with a "context"
// event whose Citations are the mail its context shows the model.
type WitEvent struct {
	ai.Event
	RunID     int64
	Citations []Citation
	Approvals []Approval
}

// RunSpan is the part of a conversation's Messages that run ID added, its
// question included: Messages[From:To]. A resume extends its run's span; a
// run that saved nothing (failed, cancelled while running) has none.
type RunSpan struct {
	ID       int64
	From, To int
}

// Citation is a message a run's tools showed the model: RunID's call CallID
// (its tool_result), or its context when CallID is "". N is the message's
// citation number in the conversation (from 1, by first citation): the
// model sees it as the mail's "ref" and cites it as [N].
type Citation struct {
	N             int
	MessageID     int64
	Subject, From string
	Date          time.Time
	Suspect       bool
	Quote         string // the suspect text, from where it starts (ai.InjectionQuote): show as plain text
	RunID         int64
	CallID        string
}

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
	now := time.Now().Unix()
	r, err := s.db.Execute(ctx, "INSERT INTO conversations(kind, draft_id, title, model, created_at, updated_at) VALUES (?, ?, '', '', ?, ?)",
		kind, nullID(draftID), now, now)
	if err != nil {
		return Conversation{}, err
	}
	c, _, err := s.conversation(ctx, r.LastInsertID)
	return c, err
}

const convCols = "id, kind, title, model, draft_id, updated_at, state, effort FROM conversations"

func scanConversation(r []any) Conversation {
	return Conversation{ID: store.Int(r[0]), Kind: store.Str(r[1]), Title: store.Str(r[2]), Model: store.Str(r[3]),
		DraftID: store.Int(r[4]), UpdatedAt: time.Unix(store.Int(r[5]), 0), Effort: store.Str(r[7])}
}

// conversation returns the conversation (without Messages) and its model state.
func (s *Service) conversation(ctx context.Context, id int64) (Conversation, []byte, error) {
	rows, err := s.db.Query(ctx, "SELECT "+convCols+" WHERE id = ?", id)
	if err != nil {
		return Conversation{}, nil, err
	}
	if len(rows.Values) == 0 {
		return Conversation{}, nil, fmt.Errorf("conversation %d not found", id)
	}
	state, _ := rows.Values[0][6].([]byte)
	return scanConversation(rows.Values[0]), state, nil
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

// RewindConversation takes the conversation back to before its question at
// Messages[at]: that question, everything after it and the runs that added
// them (their approvals and citations with them) are gone, so the question
// can be asked again, as it was or edited. What its tools did stays done, so
// the conversation's grants (本次对话都允许) go too: asked again, a write
// waits for the user rather than running a second time unseen. An active run
// makes it ErrRunActive.
func (s *Service) RewindConversation(ctx context.Context, id int64, at int) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.runMu.Lock() // no run starts meanwhile
	defer s.runMu.Unlock()
	return s.rewind(ctx, id, at)
}

// rewind is RewindConversation; the caller holds runMu.
func (s *Service) rewind(ctx context.Context, id int64, at int) error {
	_, state, err := s.conversation(ctx, id)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, "SELECT 1 FROM runs WHERE conversation_id = ? AND state IN ('running', 'interrupted', 'resuming')", id)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return ErrRunActive
	}
	kept, err := rewoundState(state, at)
	if err != nil {
		return fmt.Errorf("conversation %d: %w", id, err)
	}
	return s.saveRewind(ctx, id, at, kept)
}

// rewoundState is state before its question at Messages[at].
func rewoundState(state []byte, at int) ([]byte, error) {
	var msgs []json.RawMessage // the state's messages, as Messages has them
	if len(state) > 0 {
		if err := json.Unmarshal(state, &msgs); err != nil {
			return nil, err
		}
	}
	shown, err := ai.Messages(state)
	if err != nil {
		return nil, err
	}
	if at < 0 || at >= len(msgs) || len(shown) != len(msgs) || shown[at].Role != "user" {
		return nil, fmt.Errorf("no question at %d", at)
	}
	return json.Marshal(msgs[:at])
}

// saveRewind saves conversation id rewound to kept (rewoundState of at),
// and drops its grants.
func (s *Service) saveRewind(ctx context.Context, id int64, at int, kept []byte) error {
	_, err := s.db.Transaction(ctx, append([]sqlite.Statement{saveState(id, kept)}, rewindStmts(id, at)...))
	if err == nil {
		s.dropGrants(id)
	}
	return err
}

// rewindStmts drop what conversation id's runs added from its question at
// Messages[at] on (runs with no span stay); the conversation's state is
// saved apart.
func rewindStmts(id int64, at int) []sqlite.Statement {
	const dropped = "FROM runs WHERE conversation_id = ? AND hist_from >= ?"
	stmts := []sqlite.Statement{
		// what they took stays counted
		{SQL: `UPDATE conversations SET spent_input = spent_input + (SELECT COALESCE(SUM(input_tokens), 0) ` + dropped + `),
			spent_output = spent_output + (SELECT COALESCE(SUM(output_tokens), 0) ` + dropped + `) WHERE id = ?`, Args: []any{id, at, id, at, id}},
		{SQL: "DELETE " + dropped, Args: []any{id, at}},
	}
	if at == 0 { // the first question titled it: the one asked in its place does
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE conversations SET title = '' WHERE id = ?", Args: []any{id}})
	}
	return stmts
}

// GetConversation returns the conversation with its messages.
func (s *Service) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Conversation{}, err
	}
	defer end()
	c, state, err := s.conversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if c.Messages, err = ai.Messages(state); err != nil {
		return Conversation{}, err
	}
	x := s.linkExpander()
	for i, m := range c.Messages { // the saved history keeps the references the model wrote
		c.Messages[i].Text, c.Messages[i].Reasoning = x.text(ctx, m.Text), x.text(ctx, m.Reasoning)
		for j, call := range m.Calls {
			m.Calls[j].Args = x.args(ctx, call.Args)
		}
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM runs WHERE conversation_id = ? AND state IN ('running', 'interrupted', 'resuming')", id)
	if err != nil {
		return Conversation{}, err
	}
	if len(rows.Values) > 0 {
		c.ActiveRunID = store.Int(rows.Values[0][0])
	}
	rows, err = s.db.Query(ctx, "SELECT id, hist_from, hist_to FROM runs WHERE conversation_id = ? AND hist_from IS NOT NULL ORDER BY id", id)
	if err != nil {
		return Conversation{}, err
	}
	for _, r := range rows.Values {
		c.Runs = append(c.Runs, RunSpan{ID: store.Int(r[0]), From: int(store.Int(r[1])), To: int(store.Int(r[2]))})
	}
	rows, err = s.db.Query(ctx, `SELECT COALESCE(SUM(input_tokens), 0) + (SELECT spent_input FROM conversations WHERE id = ?),
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
	c.HistoryUse = float64(utf8.RuneCount(state)) / historyBudget
	c.Citations, err = s.citationsOf(ctx, id)
	return c, err
}

// runUsage adds res's tokens to runID's.
func runUsage(runID int64, u ai.Usage) sqlite.Statement {
	return sqlite.Statement{SQL: `UPDATE runs SET input_tokens = input_tokens + ?, output_tokens = output_tokens + ?,
		context_tokens = CASE WHEN ? > 0 THEN ? ELSE context_tokens END WHERE id = ?`, Args: []any{u.Input, u.Output, u.Last, u.Last, runID}}
}

// citationsOf is conversation id's saved citations, numbered.
func (s *Service) citationsOf(ctx context.Context, id int64) ([]Citation, error) {
	rows, err := s.db.Query(ctx, `SELECT rc.message_id, rc.subject, rc.from_addr, rc.date, rc.suspect, rc.run_id, rc.call_id, rc.quote
		FROM run_citations rc JOIN runs r ON r.id = rc.run_id WHERE r.conversation_id = ? ORDER BY rc.id`, id)
	if err != nil {
		return nil, err
	}
	var out []Citation
	for _, r := range rows.Values {
		ct := Citation{MessageID: store.Int(r[0]), Subject: store.Str(r[1]), From: store.Str(r[2]), Suspect: store.Int(r[4]) != 0,
			RunID: store.Int(r[5]), CallID: store.Str(r[6]), Quote: store.Str(r[7])}
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
// rewound to at (saveRewind): only the runs it keeps count.
func (s *Service) keptRefs(ctx context.Context, job *runJob, at int) error {
	rows, err := s.db.Query(ctx, "SELECT id FROM runs WHERE conversation_id = ? AND (hist_from IS NULL OR hist_from < ?)", job.conv.ID, at)
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

// number numbers cs, which job's call (or its context, call "") shows the
// model, and saves them.
func (s *Service) number(ctx context.Context, job *runJob, call string, cs []Citation) []Citation {
	ctx = context.WithoutCancel(ctx)
	job.refsMu.Lock()
	defer job.refsMu.Unlock()
	if job.refs == nil {
		job.refs = map[int64]int{}
		saved, err := s.citationsOf(ctx, job.conv.ID)
		if err != nil {
			slog.Default().Warn("could not read a conversation's citations", "conversation", job.conv.ID, "err", err)
		}
		numberCitations(saved, job.refs)
	}
	for i := range cs {
		cs[i].RunID, cs[i].CallID = job.id, call
	}
	numberCitations(cs, job.refs)
	if len(cs) > 0 {
		s.saveCitations(ctx, job.id, cs)
	}
	return cs
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
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO run_citations(run_id, call_id, message_id, subject, from_addr, date, suspect, quote)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, Args: []any{runID, c.CallID, c.MessageID, c.Subject, c.From, date, suspect, c.Quote}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		slog.Default().Warn("could not save a run's citations", "run", runID, "err", err)
	}
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
		return nil
	}
	if _, _, err := s.conversation(ctx, id); err != nil {
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
	waiting int           // events in q that do not end a run
	closing bool          // no more pushes; ch closes once q is delivered
	wake    chan struct{} // 1 slot: q or closing changed
	quit    chan struct{} // closed by stop
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
	if w.closing {
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

func (w *witStream) pump() {
	defer close(w.ch)
	for {
		w.mu.Lock()
		if len(w.q) == 0 {
			closing := w.closing
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
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	if s.subsClosed {
		ch := make(chan WitEvent)
		close(ch)
		return ch, func() {}
	}
	w := s.newStream()
	if s.subs == nil {
		s.subs = map[int64]map[*witStream]bool{}
	}
	if s.subs[id] == nil {
		s.subs[id] = map[*witStream]bool{}
	}
	s.subs[id][w] = true
	return w.ch, func() {
		s.subsMu.Lock()
		delete(s.subs[id], w)
		s.subsMu.Unlock()
		w.stop()
	}
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
// Assist) as well as a resume's: Assist offers no plugin write tool (spec
// 7.4) and no untrusted MCP tool, so one that became one since is unavailable.
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
	clued    sync.Map      // recipients create_draft has shown language clues for
	full     []byte        // the conversation state before TrimState
	dropped  int           // messages TrimState left out of the request
	// host, set for a host-made proposal (AssistSkill), turns the finished
	// tool-less run into an interrupt on the proposal; it is not resumed.
	host func(ctx context.Context, res ai.Result) ai.Result
	// rewind, for a question asked in place, go with the run's first save
	// (settleRun): a run that saves nothing (failed, stopped) replaces nothing.
	rewind []sqlite.Statement
	grants []grantKey // the grants asking in place set aside: back if nothing is replaced
	cited  []Citation // the mail the run's context shows the model, sent first
	quoted []Citation // the mail the user quoted, numbered (quote): sent first too
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// snapshot is runs.snapshot: what a resume needs besides the conversation state.
type snapshot struct {
	ReadMail bool   `json:"read_mail"`
	Round    int    `json:"round"`            // seq of the current interrupt's first approval
	Host     bool   `json:"host,omitempty"`   // a host-made proposal: nothing to resume
	Effort   string `json:"effort,omitempty"` // the run's reasoning effort
}

type runKey struct{}

// runOf is the run whose tool is given ctx.
func runOf(ctx context.Context) *runJob { return ctx.Value(runKey{}).(*runJob) }

// historyBudget caps the runes of the conversation state sent to the model.
const historyBudget = 60000

// request builds the run's request on state, trimmed to historyBudget; the
// job keeps what drive needs to save the whole history.
func (s *Service) request(ctx context.Context, job *runJob, model ai.ResolvedModel, state []byte, tools []toolDesc) (ai.Request, error) {
	system, err := s.witSystem(ctx, job, tools)
	if err != nil {
		return ai.Request{}, err
	}
	sent, dropped, err := ai.TrimState(state, historyBudget)
	if err != nil {
		return ai.Request{}, err
	}
	job.full, job.dropped = state, dropped
	return ai.Request{Model: model, System: system, State: sent, Tools: s.specs(job, tools), Reasoning: true, Effort: job.conv.Effort,
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
	w, err := s.launch(ctx, end, conv, prompt, tools, nil, -1)
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

// launch claims a run and hands end to it. The quoted mail (checked IDs) goes
// before the prompt (quote). at, unless -1, asks the prompt in place of the
// question at Messages[at] (RewindConversation): the run starts on the
// history before it, and the rewind is saved with what the run saves, if it
// does. claimed, if set, are saved once the run is claimed and before the
// hand-off (asking in place: with the rewind); their error fails the run
// and launch.
func (s *Service) launch(ctx context.Context, end func(), conv Conversation, prompt string, tools []toolDesc,
	quoted []int64, at int, claimed ...sqlite.Statement) (*witStream, error) {
	conv, _, err := s.conversation(ctx, conv.ID)
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
	job, state, err := s.claimRun(ctx, conv, ref, names)
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		state, err = rewoundState(state, at)
	}
	if err == nil && at >= 0 {
		err = s.keptRefs(ctx, job, at)
	}
	var req ai.Request
	if err == nil {
		req, err = s.request(ctx, job, model, state, tools)
	}
	if err == nil && len(quoted) > 0 {
		req.Context, err = s.quote(ctx, job, quoted)
	}
	if err == nil && at >= 0 {
		job.rewind = append(rewindStmts(conv.ID, at), claimed...)
		job.grants = s.dropGrants(conv.ID) // asked again, a write waits for the user
	} else if err == nil && len(claimed) > 0 {
		_, err = s.db.Transaction(ctx, claimed)
	}
	if err != nil {
		s.untrack(job)
		return nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), runState(job.id, "failed")))
	}
	req.Prompt = prompt
	out := s.newStream()
	go s.drive(job, end, e.Run, req, out)
	return out, nil
}

// claimRun records a running run of conv unless the conversation has an
// active one (ErrRunActive) and tracks it; state is the conversation's history
// as of the claim. The caller holds runMu.
func (s *Service) claimRun(ctx context.Context, conv Conversation, ref string, tools []string) (*runJob, []byte, error) {
	toolsJSON, _ := json.Marshal(tools)
	snap, _ := json.Marshal(snapshot{})
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
	_, state, err := s.conversation(ctx, conv.ID)
	var scope *gateway.Scope
	if err == nil {
		scope, err = s.convScope(ctx, conv.ID, false)
	}
	if err != nil {
		return nil, nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), runState(r.LastInsertID, "failed")))
	}
	return s.track(ctx, r.LastInsertID, conv, scope, 0), state, nil
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

func (s *Service) drive(job *runJob, end func(), run runFunc, req ai.Request, out *witStream) {
	defer end()
	defer func() {
		if beforeUntrack != nil {
			beforeUntrack(job.id)
		}
		s.runMu.Lock()
		s.untrack(job)
		s.runMu.Unlock()
	}()
	emit := func(ev WitEvent) {
		ev.RunID = job.id // its Citations are numbered and saved (number): a reader that re-reads after it finds them
		if out != nil {
			out.push(ev)
		}
		s.publishWit(job.conv.ID, ev)
	}
	cs := job.quoted
	if len(job.cited) > 0 {
		cs = append(cs, s.number(job.ctx, job, "", job.cited)...)
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
	s.runRejected(req.Model, res.Err)
	if res.Err == nil {
		used := res.Usage // whatever res becomes, the model calls took these
		// Save the whole history, not just what this run was sent.
		if st, err := ai.RestoreState(job.full, res.State, job.dropped); err != nil {
			res = ai.Result{Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}
		} else {
			res.State = st
		}
		if job.host != nil {
			res = job.host(ctx, res)
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

func saveState(convID int64, state []byte) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE conversations SET state = ?, updated_at = ? WHERE id = ?", Args: []any{state, time.Now().Unix(), convID}}
}

func (s *Service) exec(ctx context.Context, st sqlite.Statement) error {
	_, err := s.db.Execute(ctx, st.SQL, st.Args...)
	return err
}

// runSpan records that runID's history runs from before (its first save
// keeps its start) to after.
func runSpan(runID int64, before, after []byte) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE runs SET hist_from = COALESCE(hist_from, ?), hist_to = ? WHERE id = ?",
		Args: []any{countMessages(before), countMessages(after), runID}}
}

func countMessages(state []byte) int {
	ms, _ := ai.Messages(state) // the state was just decoded or encoded
	return len(ms)
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
		if job.rewind != nil { // it replaced nothing: the grants set aside come back
			for _, g := range job.grants {
				s.sessionGrants.Store(g, true)
			}
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
		// the rewind first: its run's span starts where what it drops did
		_, err := s.db.Transaction(ctx, append(slices.Clone(job.rewind), saveState(job.conv.ID, res.State), runState(job.id, "done"),
			runSpan(job.id, job.full, res.State), runUsage(job.id, res.Usage)))
		if err == nil {
			job.rewind = nil
		}
		return WitEvent{Event: ai.Event{Kind: "done"}}, err
	}
	now := time.Now().Unix()
	snap, _ := json.Marshal(snapshot{ReadMail: job.scope.ReadMail(), Round: job.round, Host: job.host != nil, Effort: req.Effort})
	stmts := append(slices.Clone(job.rewind), saveState(job.conv.ID, res.State), runSpan(job.id, job.full, res.State), runUsage(job.id, res.Usage),
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
			target_id, target_version, state, destinations, binding, source, extension, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?)`,
			Args: []any{job.id, job.round + i, c.ID, c.Name, string(p.Args), p.Summary, p.TargetKind, nullID(p.TargetID), p.TargetVersion,
				string(dests), p.Binding, cmp.Or(p.source, gateway.SourceWit), p.extension, now, now}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return WitEvent{}, err
	}
	job.rewind = nil
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
		convID, ferr := s.failRun(ctx, runID, err)
		end()
		s.publishWit(convID, WitEvent{RunID: runID, Event: ai.Event{Kind: "error", Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}})
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
		s.publishWit(store.Int(r[0]), WitEvent{RunID: runID, Event: ai.Event{Kind: "done"}})
		return nil
	}
	conv, state, err := s.conversation(ctx, store.Int(r[0]))
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
	req, err := s.request(ctx, job, model, state, offered(conv.Kind, s.registered(names...)))
	if err != nil {
		s.untrack(job)
		return err
	}
	req.Responses = results
	req.Effort = snap.Effort // as the run began: Anthropic refuses thinking switched on mid-turn
	go s.drive(job, end, e.Run, req, nil)
	return nil
}
