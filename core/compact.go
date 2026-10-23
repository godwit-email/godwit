package core

// Compacting a conversation (ai.Summary): once a run leaves more history than
// its model's budget, or when the user asks (CompactConversation), the turns
// before its last two are summarized with the conversation's own model; later
// runs send the summary in their place, and TrimState omits turns only when
// that is not enough.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

const compactSystem = `You condense the earlier part of a conversation between a user and an email assistant, so the conversation can go on without it. ` +
	`Write a concise summary in the language the user writes in: what the user asked for and wants, decisions made, ` +
	`what was found in mail (senders, dates and subjects where they matter), actions taken or still pending, and open questions. ` +
	`Keep names, addresses, numbers and dates exact. Write only the summary.` + "\n\n" + ai.UntrustedSystemNote

var (
	ErrNothingToCompact = errors.New("the conversation has nothing to compact")
	ErrCompacting       = errors.New("the conversation is being compacted")
)

// CompactConversation summarizes conversation id's turns before its last two
// now, whatever its size: ErrNothingToCompact when its summary already reaches
// them (Conversation.Compactable false), ErrCompacting while one is under way.
func (s *Service) CompactConversation(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	c, state, err := s.conversation(ctx, id)
	if err != nil {
		return err
	}
	model, _, err := s.resolveRef(ctx, c.Model)
	if err != nil {
		return err
	}
	if _, busy := s.compacting.LoadOrStore(id, true); busy {
		return ErrCompacting
	}
	defer s.compacting.Delete(id)
	return s.compact(ctx, id, model, state, true)
}

// convSummary is conversation id's summary and its epoch (bumped by a rewind).
func (s *Service) convSummary(ctx context.Context, id int64) (ai.Summary, int64, error) {
	rows, err := s.db.Query(ctx, "SELECT summary, summary_upto, summary_epoch FROM conversations WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return ai.Summary{}, 0, err
	}
	r := rows.Values[0]
	return ai.Summary{Text: store.Str(r[0]), Upto: int(store.Int(r[1]))}, store.Int(r[2]), nil
}

// compactLater compacts conversation id in the background when state, all of
// its history, is past model's history budget with its summary standing in;
// one compaction a conversation at a time.
func (s *Service) compactLater(id int64, model ai.ResolvedModel, state []byte) {
	if _, busy := s.compacting.LoadOrStore(id, true); busy {
		return
	}
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		s.compacting.Delete(id)
		return
	}
	go func() {
		defer end()
		defer s.compacting.Delete(id)
		if err := s.compact(ctx, id, model, state, false); err != nil {
			slog.Default().Warn("could not compact a conversation", "conversation", id, "err", err)
		}
	}()
}

// compact summarizes state's turns before its last two, the summary so far
// included, when it is past the history budget, or, forced, whatever its size.
func (s *Service) compact(ctx context.Context, id int64, model ai.ResolvedModel, state []byte, force bool) error {
	sum, epoch, err := s.convSummary(ctx, id)
	if err != nil {
		return err
	}
	if !force {
		size, err := ai.CompactedSize(state, sum)
		if err != nil || size <= s.historyBudget(model.BaseURL, model.Model) {
			return err
		}
	}
	upto, err := ai.CompactPoint(state)
	if err != nil {
		return err
	}
	if upto <= sum.Upto {
		if force {
			return ErrNothingToCompact
		}
		return nil
	}
	prompt, err := ai.Transcript(state, sum.Upto+1, upto)
	if err != nil {
		return err
	}
	if sum.Text != "" {
		prompt = "Summary of the conversation before this part:\n" + sum.Text + "\n\nThe conversation since:\n" + prompt
	}
	e, err := s.aiEngine()
	if err != nil {
		return err
	}
	events, wait := e.Run(ctx, ai.Request{Model: model, System: compactSystem, Prompt: prompt, MaxTurns: 1})
	var text strings.Builder
	for ev := range events {
		if ev.Kind == "text" {
			text.WriteString(ev.Text)
		}
	}
	res := wait()
	s.runRejected(model, res.Err)
	// the conversation took these tokens, as a rewind's runs (spent_*)
	spent := `UPDATE conversations SET spent_input = spent_input + ?, spent_output = spent_output + ? WHERE id = ?`
	if res.Err != nil {
		return errors.Join(res.Err, s.exec(ctx, sqlite.Statement{SQL: spent, Args: []any{res.Usage.Input, res.Usage.Output, id}}))
	}
	if strings.TrimSpace(text.String()) == "" {
		return errors.New("the model wrote no summary")
	}
	_, err = s.db.Transaction(ctx, []sqlite.Statement{{SQL: spent, Args: []any{res.Usage.Input, res.Usage.Output, id}},
		{SQL: "UPDATE conversations SET summary = ?, summary_upto = ? WHERE id = ? AND summary_epoch = ? AND summary_upto = ?",
			Args: []any{strings.TrimSpace(text.String()), upto, id, epoch, sum.Upto}}})
	return err
}
