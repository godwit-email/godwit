package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
)

// Conversation is a Wit chat: kind "ask" (the mailbox panel) or "assist" (one
// draft's compose panel, DraftID set). Messages is decoded from the saved
// model state, for display; ListConversations leaves it empty.
type Conversation struct {
	ID                 int64
	Kind, Title, Model string
	DraftID            int64
	Messages           []ai.Message
	UpdatedAt          time.Time
}

// WitEvent is one event of a Wit run. An interrupted event carries the
// approvals it recorded, in call order.
type WitEvent struct {
	ai.Event
	RunID     int64
	Citations []Citation
	Approvals []Approval
}

// Citation is a message a run's tools showed the model.
type Citation struct {
	MessageID     int64
	Subject, From string
	Suspect       bool
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

const convCols = "id, kind, title, model, draft_id, updated_at, state FROM conversations"

func scanConversation(r []any) Conversation {
	return Conversation{ID: store.Int(r[0]), Kind: store.Str(r[1]), Title: store.Str(r[2]), Model: store.Str(r[3]),
		DraftID: store.Int(r[4]), UpdatedAt: time.Unix(store.Int(r[5]), 0)}
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
	c.Messages, err = ai.Messages(state)
	return c, err
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
	if err != nil || r.Changes > 0 {
		return err
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

// registeredTool is a tool Wit runs can offer. A write tool has an executor
// and no Run: its calls become approvals.
type registeredTool struct {
	Spec   ai.ToolSpec
	Write  bool
	Egress string
	Exec   *executor
}

func (s *Service) registerTool(t registeredTool) {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	if s.tools == nil {
		s.tools = map[string]registeredTool{}
	}
	s.tools[t.Spec.Name] = t
}

// registered returns the named tools. A name no longer registered becomes a
// write tool without an executor, whose calls are denied, so a resumed run
// can still answer the calls made to it.
func (s *Service) registered(names ...string) []registeredTool {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	out := make([]registeredTool, 0, len(names))
	for _, n := range names {
		t, ok := s.tools[n]
		if !ok {
			t = registeredTool{Spec: ai.ToolSpec{Name: n, Description: "No longer available."}, Write: true}
		}
		out = append(out, t)
	}
	return out
}

func (s *Service) executor(name string) *executor {
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	return s.tools[name].Exec
}

// runJob is a run in flight, started or resumed.
type runJob struct {
	id       int64
	conv     Conversation
	scope    *gateway.Scope
	round    int      // seq of the first approval the next interrupt records
	prepared sync.Map // call ID -> prepared, stored by write tools' gates
	cites    sync.Map // call ID -> []Citation, stored by read tools
	clued    sync.Map // recipients create_draft has shown language clues for
	full     []byte   // the conversation state before TrimState
	dropped  int      // messages TrimState left out of the request
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// snapshot is runs.snapshot: what a resume needs besides the conversation state.
type snapshot struct {
	ReadMail bool `json:"read_mail"`
	Round    int  `json:"round"` // seq of the current interrupt's first approval
}

type runKey struct{}

// runOf is the run whose tool is given ctx.
func runOf(ctx context.Context) *runJob { return ctx.Value(runKey{}).(*runJob) }

// historyBudget caps the runes of the conversation state sent to the model.
const historyBudget = 60000

// request builds the run's request on state, trimmed to historyBudget; the
// job keeps what drive needs to save the whole history.
func (s *Service) request(ctx context.Context, job *runJob, model ai.ResolvedModel, state []byte, tools []registeredTool) (ai.Request, error) {
	system, err := s.witSystem(ctx, job.conv)
	if err != nil {
		return ai.Request{}, err
	}
	sent, dropped, err := ai.TrimState(state, historyBudget)
	if err != nil {
		return ai.Request{}, err
	}
	job.full, job.dropped = state, dropped
	return ai.Request{Model: model, System: system, State: sent, Tools: s.specs(job, tools)}, nil
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
func (s *Service) startRun(ctx context.Context, conv Conversation, prompt string, tools []registeredTool) (<-chan WitEvent, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	w, err := s.launch(ctx, end, conv, prompt, tools)
	if err != nil {
		end()
		return nil, err
	}
	return w.ch, nil
}

func (s *Service) launch(ctx context.Context, end func(), conv Conversation, prompt string, tools []registeredTool) (*witStream, error) {
	conv, _, err := s.conversation(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	ref := conv.Model
	if ref == "" {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return nil, err
		}
		if ref = st.DefaultModel; ref == "" {
			return nil, ErrAINotConfigured
		}
	}
	model, err := s.resolveModel(ctx, ref)
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
	toolsJSON, _ := json.Marshal(names)
	snap, _ := json.Marshal(snapshot{})
	now := time.Now().Unix()

	if beforeRunClaim != nil {
		beforeRunClaim()
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	r, err := s.db.Execute(ctx, `INSERT INTO runs(conversation_id, state, model, tools, snapshot, created_at, updated_at)
		SELECT ?, 'running', ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM runs WHERE conversation_id = ?
		AND state IN ('running', 'interrupted', 'resuming'))`, conv.ID, ref, string(toolsJSON), snap, now, now, conv.ID)
	if err != nil {
		return nil, err
	}
	if r.Changes == 0 {
		return nil, ErrRunActive
	}
	// Read the history only now: a previous run may have ended after the read above.
	_, state, err := s.conversation(ctx, conv.ID)
	if err != nil {
		st := runState(r.LastInsertID, "failed")
		return nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), st))
	}
	job := s.track(ctx, r.LastInsertID, conv, &gateway.Scope{}, 0)
	req, err := s.request(ctx, job, model, state, tools)
	if err != nil {
		s.untrack(job)
		return nil, errors.Join(err, s.exec(context.WithoutCancel(ctx), runState(job.id, "failed")))
	}
	req.Prompt = prompt
	out := s.newStream()
	go s.drive(job, end, e, req, out)
	return out, nil
}

// specs wraps the tools for one run: every call goes through the gateway, and
// a write call it sends for approval is prepared first. Prepare fixes the
// final arguments the approval records; a call it rejects goes back to the
// model as an error, without an approval.
func (s *Service) specs(job *runJob, tools []registeredTool) []ai.ToolSpec {
	g := gateway.New(s.db)
	out := make([]ai.ToolSpec, 0, len(tools))
	for _, t := range tools {
		spec := g.Wrap(job.scope, gateway.SourceWit, t.Spec, t.Write, t.Egress)
		if run := spec.Run; run != nil {
			spec.Run = func(ctx context.Context, args json.RawMessage) (any, error) {
				return run(context.WithValue(ctx, runKey{}, job), args)
			}
		}
		gate, ex := spec.Gate, t.Exec
		spec.Gate = func(ctx context.Context, args json.RawMessage) (ai.Decision, error) {
			d, err := gate(ctx, args)
			if err != nil || d != ai.Approve {
				return d, err
			}
			if ex == nil {
				return ai.Deny, errors.New("this call needs an approval that this tool cannot take")
			}
			p, err := ex.Prepare(context.WithValue(ctx, runKey{}, job), job.conv, args)
			if err != nil {
				return ai.Deny, err
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
func (s *Service) drive(job *runJob, end func(), e *ai.Engine, req ai.Request, out *witStream) {
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
		ev.RunID = job.id
		if out != nil {
			out.push(ev)
		}
		s.publishWit(job.conv.ID, ev)
	}
	events, wait := e.Run(job.ctx, req)
	for ev := range events {
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
	if res.Err == nil {
		// Save the whole history, not just what this run was sent.
		if st, err := ai.RestoreState(job.full, res.State, job.dropped); err != nil {
			res = ai.Result{Err: &ai.Error{Code: ai.CodeInternal, Msg: err.Error()}}
		} else {
			res.State = st
		}
	}
	fin, err := s.settleRun(ctx, job, req, res)
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
}

func saveState(convID int64, state []byte) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE conversations SET state = ?, updated_at = ? WHERE id = ?", Args: []any{state, time.Now().Unix(), convID}}
}

func (s *Service) exec(ctx context.Context, st sqlite.Statement) error {
	_, err := s.db.Execute(ctx, st.SQL, st.Args...)
	return err
}

func runState(runID int64, state string) sqlite.Statement {
	return sqlite.Statement{SQL: "UPDATE runs SET state = ?, updated_at = ? WHERE id = ?", Args: []any{state, time.Now().Unix(), runID}}
}

// settleRun records res: done saves the state; interrupted saves it with one
// pending approval per call, in one transaction; a failure keeps the saved
// state, closed with the approvals' results if the run was a resume.
func (s *Service) settleRun(ctx context.Context, job *runJob, req ai.Request, res ai.Result) (WitEvent, error) {
	switch {
	case res.Err != nil:
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
		return WitEvent{Event: ai.Event{Kind: "error", Err: res.Err}}, err
	case len(res.Interrupts) == 0:
		_, err := s.db.Transaction(ctx, []sqlite.Statement{saveState(job.conv.ID, res.State), runState(job.id, "done")})
		return WitEvent{Event: ai.Event{Kind: "done"}}, err
	}
	now := time.Now().Unix()
	snap, _ := json.Marshal(snapshot{ReadMail: job.scope.ReadMail(), Round: job.round})
	stmts := []sqlite.Statement{saveState(job.conv.ID, res.State),
		{SQL: "UPDATE runs SET state = 'interrupted', snapshot = ?, updated_at = ? WHERE id = ?", Args: []any{snap, now, job.id}}}
	for i, c := range res.Interrupts {
		v, ok := job.prepared.Load(c.ID)
		if !ok {
			return WitEvent{}, fmt.Errorf("tool call %s was not prepared", c.Name)
		}
		p := v.(prepared)
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO approvals(run_id, seq, call_id, tool, args, summary, target_kind,
			target_id, target_version, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
			Args: []any{job.id, job.round + i, c.ID, c.Name, string(p.Args), p.Summary, p.TargetKind, nullID(p.TargetID), p.TargetVersion, now, now}})
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
	scope := &gateway.Scope{}
	if snap.ReadMail {
		scope.MarkReadMail()
	}
	job := s.track(ctx, runID, conv, scope, snap.Round+len(as))
	req, err := s.request(ctx, job, model, state, s.registered(names...))
	if err != nil {
		s.untrack(job)
		return err
	}
	req.Responses = results
	go s.drive(job, end, e, req, nil)
	return nil
}
