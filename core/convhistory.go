package core

// A conversation's history is its messages one row each (ADR 0021):
// conversation_messages by seq, never reused; conversations.next_seq is the
// next. ai.History reads and changes it; these save it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

// errUnreadableHistory: the conversation's history is still the old one-array
// state, which backfillHistories could not read; the next start tries again.
var errUnreadableHistory = errors.New("the conversation's history could not be read")

// history is conversation id's history as its runs and the user see it: a
// question being asked again (runs.rewind_from) hides what it replaces while
// its run is active.
func (s *Service) history(ctx context.Context, id int64) (*ai.History, error) {
	rows, err := s.db.Query(ctx, `SELECT c.next_seq, c.state IS NOT NULL AND length(c.state) > 0, COALESCE(r.rewind_from, 0), COALESCE(r.rewind_to, 0)
		FROM conversations c LEFT JOIN runs r ON r.conversation_id = c.id AND r.state IN ('running', 'interrupted', 'resuming') WHERE c.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("conversation %d not found", id)
	}
	r := rows.Values[0]
	if store.Int(r[1]) != 0 {
		return nil, errUnreadableHistory
	}
	msgs, err := s.db.Query(ctx, "SELECT seq, data FROM conversation_messages WHERE conversation_id = ? AND NOT (seq >= ? AND seq < ?) ORDER BY seq",
		id, store.Int(r[2]), store.Int(r[3]))
	if err != nil {
		return nil, err
	}
	saved := make([]ai.StoredMessage, len(msgs.Values))
	for i, m := range msgs.Values {
		data, _ := m[1].([]byte)
		saved[i] = ai.StoredMessage{Seq: store.Int(m[0]), Data: data}
	}
	return ai.LoadHistory(saved, store.Int(r[0]))
}

// historyStmts save ch, a change to conversation id's history.
func historyStmts(id int64, ch ai.HistoryChanges) []sqlite.Statement {
	var out []sqlite.Statement
	if ch.DropFrom > 0 {
		out = append(out, sqlite.Statement{SQL: "DELETE FROM conversation_messages WHERE conversation_id = ? AND seq >= ?", Args: []any{id, ch.DropFrom}})
	}
	for _, m := range ch.Put {
		out = append(out, sqlite.Statement{SQL: `INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, ?, ?)
			ON CONFLICT(conversation_id, seq) DO UPDATE SET data = excluded.data`, Args: []any{id, m.Seq, m.Data}})
	}
	return append(out, sqlite.Statement{SQL: "UPDATE conversations SET next_seq = MAX(next_seq, ?), updated_at = ? WHERE id = ?",
		Args: []any{ch.Next, time.Now().Unix(), id}})
}

// pendingRewind is a question asked again: the run replaces conversation
// conv's messages [from, to) (its question and what followed it), and the
// summary if drop. first: it was the conversation's first question.
type pendingRewind struct {
	from, to    int64
	dropSummary bool
	first       bool
}

// claimRewind records that runID asks again in place of rw.from: the
// replaced messages are hidden while it is active (history).
func claimRewind(runID int64, rw pendingRewind) sqlite.Statement {
	sum := 0
	if rw.dropSummary {
		sum = 1
	}
	return sqlite.Statement{SQL: "UPDATE runs SET rewind_from = ?, rewind_to = ?, rewind_summary = ? WHERE id = ?", Args: []any{rw.from, rw.to, sum, runID}}
}

// runRewind is runID's question asked again, if it is one.
func (s *Service) runRewind(ctx context.Context, runID int64) (*pendingRewind, error) {
	rows, err := s.db.Query(ctx, `SELECT r.rewind_from, r.rewind_to, r.rewind_summary, NOT EXISTS (SELECT 1 FROM conversation_messages m
		WHERE m.conversation_id = r.conversation_id AND m.seq < r.rewind_from) FROM runs r WHERE r.id = ? AND r.rewind_from IS NOT NULL`, runID)
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	r := rows.Values[0]
	return &pendingRewind{from: store.Int(r[0]), to: store.Int(r[1]), dropSummary: store.Int(r[2]) != 0, first: store.Int(r[3]) != 0}, nil
}

// commitRewind drops what runID's question asked again replaces, now that
// the run is done: the messages, the runs that added them (what they took
// stays counted), the summary if it reached them, and the title if it was the
// conversation's first question, which question (the run's) then titles.
// A compaction begun before is not saved (the epoch).
func commitRewind(convID, runID int64, rw pendingRewind, question string) []sqlite.Statement {
	const dropped = "FROM runs WHERE conversation_id = ? AND id != ? AND hist_from >= ? AND hist_from < ?"
	args := []any{convID, runID, rw.from, rw.to}
	stmts := []sqlite.Statement{
		{SQL: "DELETE FROM conversation_messages WHERE conversation_id = ? AND seq >= ? AND seq < ?", Args: []any{convID, rw.from, rw.to}},
		{SQL: `UPDATE conversations SET spent_input = spent_input + (SELECT COALESCE(SUM(input_tokens), 0) ` + dropped + `),
			spent_output = spent_output + (SELECT COALESCE(SUM(output_tokens), 0) ` + dropped + `),
			spent_cached = spent_cached + (SELECT COALESCE(SUM(cached_tokens), 0) ` + dropped + `) WHERE id = ?`,
			Args: append(append(append(append([]any{}, args...), args...), args...), convID)},
		{SQL: "DELETE " + dropped, Args: args},
		{SQL: "UPDATE runs SET " + noRewind + " WHERE id = ?", Args: []any{runID}},
		{SQL: "UPDATE conversations SET summary_epoch = summary_epoch + 1 WHERE id = ?", Args: []any{convID}},
	}
	if rw.dropSummary {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE conversations SET summary = '', summary_upto = 0 WHERE id = ?", Args: []any{convID}})
	}
	if rw.first {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE conversations SET title = ? WHERE id = ?", Args: []any{titleOf(question), convID}})
	}
	return stmts
}

// noRewind clears a run's question asked again.
const noRewind = "rewind_from = NULL, rewind_to = NULL, rewind_summary = 0"

// discardRewind ends runID's question asked again without replacing
// anything: what the run added goes, so the conversation is as before it.
func discardRewind(convID, runID int64, rw pendingRewind) []sqlite.Statement {
	return []sqlite.Statement{
		{SQL: "DELETE FROM conversation_messages WHERE conversation_id = ? AND seq >= ?", Args: []any{convID, rw.to}},
		{SQL: "DELETE FROM run_citations WHERE run_id = ?", Args: []any{runID}},
		{SQL: "UPDATE runs SET hist_from = NULL, hist_to = NULL, " + noRewind + " WHERE id = ?", Args: []any{runID}},
	}
}

func titleOf(question string) string {
	if r := []rune(question); len(r) > titleRunes {
		return string(r[:titleRunes])
	}
	return question
}

// backfillHistories moves each conversation still saved as one array
// (conversations.state) into its message rows, a transaction each: the runs'
// spans and the summary's reach become seqs, and a skill run's command (a
// question before ADR 0021 kept none of) is put where the run ran. A state
// it cannot read stays for the next start, and its conversation fails to load
// meanwhile (errUnreadableHistory).
func (s *Service) backfillHistories(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT id, kind, draft_id, state, summary_upto FROM conversations WHERE state IS NOT NULL AND length(state) > 0")
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		id := store.Int(r[0])
		state, _ := r[3].([]byte)
		if err := s.backfillHistory(ctx, id, store.Int(r[2]), state, int(store.Int(r[4]))); err != nil {
			slog.Default().Warn("could not move a conversation's history", "conversation", id, "err", err)
		}
	}
	return nil
}

func (s *Service) backfillHistory(ctx context.Context, id, draftID int64, state []byte, summaryUpto int) error {
	runs, err := s.db.Query(ctx, "SELECT id, hist_from, hist_to, snapshot, created_at FROM runs WHERE conversation_id = ? ORDER BY id", id)
	if err != nil {
		return err
	}
	var cmds []ai.PlacedCommand
	var skills []int64
	for _, r := range runs.Values {
		var snap snapshot
		if json.Unmarshal(asBytes(r[3]), &snap) != nil || !snap.Host || r[2] == nil {
			continue
		}
		// A skill run left the history as it was: hist_to is its length then.
		skill, err := s.legacySkill(ctx, store.Int(r[0]), draftID, store.Int(r[4]))
		if err != nil {
			return err
		}
		cmds = append(cmds, ai.PlacedCommand{At: int(store.Int(r[2])), Command: skill})
		skills = append(skills, store.Int(r[0]))
	}
	placedAt := map[int]int{}
	for _, c := range cmds {
		placedAt[c.At]++
	}
	h, seqOf, cmdSeqs, err := ai.LegacyHistory(state, cmds)
	if err != nil {
		return err
	}
	saved, err := h.Rows()
	if err != nil {
		return err
	}
	stmts := make([]sqlite.Statement, 0, len(saved)+len(runs.Values)+2)
	for _, m := range saved {
		stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, ?, ?)", Args: []any{id, m.Seq, m.Data}})
	}
	for _, r := range runs.Values {
		run := store.Int(r[0])
		if i := slices.Index(skills, run); i >= 0 {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE runs SET hist_from = ?, hist_to = ? WHERE id = ?", Args: []any{cmdSeqs[i], cmdSeqs[i] + 1, run}})
		} else if r[1] != nil {
			// a span ends before the commands put where it ends
			to := int(store.Int(r[2]))
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE runs SET hist_from = ?, hist_to = ? WHERE id = ?",
				Args: []any{seqOf(int(store.Int(r[1]))), seqOf(to) - int64(placedAt[to]), run}})
		}
	}
	upto := int64(0)
	if summaryUpto > 0 {
		upto = seqOf(summaryUpto)
	}
	stmts = append(stmts, sqlite.Statement{SQL: "UPDATE conversations SET state = NULL, next_seq = ?, summary_upto = ? WHERE id = ? AND state IS NOT NULL",
		Args: []any{h.Next(), upto, id}})
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// backfillPi converts each conversation still saved as Genkit messages to pi
// messages (#234, ai.History.PiHistory), a transaction each: the seqs the
// conversation, its runs and their snapshots keep move with the messages. One
// it cannot convert keeps its messages, which runs on pi read as they are;
// the next start tries again.
func (s *Service) backfillPi(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT c.id, (SELECT m.data FROM conversation_messages m WHERE m.conversation_id = c.id ORDER BY m.seq LIMIT 1)
		FROM conversations c WHERE c.state IS NULL OR length(c.state) = 0`)
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		id := store.Int(r[0])
		if first := asBytes(r[1]); first == nil || ai.IsPiMessage(first) {
			continue
		}
		if err := s.backfillPiHistory(ctx, id); err != nil {
			slog.Default().Warn("could not convert a conversation to pi messages", "conversation", id, "err", err)
		}
	}
	return nil
}

func (s *Service) backfillPiHistory(ctx context.Context, id int64) error {
	conv, err := s.db.Query(ctx, "SELECT next_seq, summary_upto FROM conversations WHERE id = ?", id)
	if err != nil || len(conv.Values) == 0 {
		return err
	}
	next, upto := store.Int(conv.Values[0][0]), store.Int(conv.Values[0][1])
	msgs, err := s.db.Query(ctx, "SELECT seq, data FROM conversation_messages WHERE conversation_id = ? ORDER BY seq", id)
	if err != nil {
		return err
	}
	saved := make([]ai.StoredMessage, len(msgs.Values))
	for i, m := range msgs.Values {
		saved[i] = ai.StoredMessage{Seq: store.Int(m[0]), Data: asBytes(m[1])}
	}
	h, err := ai.LoadHistory(saved, next)
	if err != nil {
		return err
	}
	p, seqOf, err := h.PiHistory()
	if err != nil {
		return err
	}
	through := func(seq int64) int64 { return seqOf(seq+1) - 1 } // the last row of the message with seq
	if upto > 0 {
		upto = through(upto)
	}
	conv, err = s.db.Query(ctx, "SELECT id, hist_from, hist_to, rewind_from, rewind_to, snapshot FROM runs WHERE conversation_id = ?", id)
	if err != nil {
		return err
	}
	rows, err := p.Rows()
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{{SQL: "DELETE FROM conversation_messages WHERE conversation_id = ?", Args: []any{id}}}
	for _, m := range rows {
		stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO conversation_messages(conversation_id, seq, data) VALUES (?, ?, ?)", Args: []any{id, m.Seq, m.Data}})
	}
	for _, r := range conv.Values {
		args := make([]any, 0, 6)
		for _, v := range r[1:5] {
			if v != nil {
				v = seqOf(store.Int(v))
			}
			args = append(args, v)
		}
		snap := r[5]
		var sn snapshot
		if json.Unmarshal(asBytes(snap), &sn) == nil && sn.Summary != nil && sn.Summary.Upto > 0 { // a resume sends it again
			sn.Summary.Upto = through(sn.Summary.Upto)
			if snap, err = json.Marshal(sn); err != nil {
				return err
			}
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE runs SET hist_from = ?, hist_to = ?, rewind_from = ?, rewind_to = ?, snapshot = ? WHERE id = ?",
			Args: append(args, snap, r[0])})
	}
	stmts = append(stmts, sqlite.Statement{SQL: "UPDATE conversations SET next_seq = ?, summary_upto = ? WHERE id = ?", Args: []any{p.Next(), upto, id}})
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

func asBytes(v any) []byte {
	switch b := v.(type) {
	case []byte:
		return b
	case string:
		return []byte(b)
	}
	return nil
}

// legacySkill is the command a skill run saved before ADR 0021 ran: its
// proposal says a translation, else the audit log names the skill the
// draft ran then; "" when neither does (shown as a generic adjustment).
func (s *Service) legacySkill(ctx context.Context, runID, draftID, at int64) (ai.Command, error) {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM approvals WHERE run_id = ? AND summary = ?", runID, kindTranslate)
	if err != nil {
		return ai.Command{}, err
	}
	if len(rows.Values) > 0 {
		return ai.Command{Skill: "translate-draft"}, nil // shortcut: its target language was never kept, so none
	}
	rows, err = s.db.Query(ctx, `SELECT tool FROM audit_log WHERE target_kind = 'draft' AND target_id = ? AND tool LIKE 'skill:%' AND at <= ?
		ORDER BY at DESC, id DESC LIMIT 1`, draftID, at+5)
	if err != nil || len(rows.Values) == 0 {
		return ai.Command{}, err
	}
	return ai.Command{Skill: strings.TrimPrefix(store.Str(rows.Values[0][0]), "skill:")}, nil
}

// reclaimEvery is how often the database gives free pages back (store.Reclaim).
const reclaimEvery = 10 * time.Minute

// startReclaim gives the database's free pages back now and then, and soon
// after a conversation is deleted (reclaimSoon).
func (s *Service) startReclaim() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		t := time.NewTicker(reclaimEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.reclaimWake:
			}
			if err := s.db.Reclaim(ctx); err != nil && ctx.Err() == nil {
				slog.Default().Warn("could not reclaim the database's free space", "err", err)
			}
		}
	}()
}

func (s *Service) reclaimSoon() {
	select {
	case s.reclaimWake <- struct{}{}:
	default:
	}
}
