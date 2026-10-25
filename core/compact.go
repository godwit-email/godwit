package core

// Compacting a conversation (ai.Summary): once a run leaves more history than
// its model's budget, or when the user asks (CompactConversation), what comes
// before its newest messages (compactKeep of the budget, or the last two
// turns) is summarized with the conversation's own model, as pi's compaction
// does: a summary may end inside a turn, whose first part is then summarized
// apart (a split turn). Later runs send the summary in its place, and Send
// omits turns only when that is not enough.

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

const compactSystem = `You condense the earlier part of a conversation between a user and an email assistant, so the conversation can go on without it. ` +
	`Write a concise summary in the language the user writes in: what the user asked for and wants, decisions made, ` +
	`what was found in mail (senders, dates and subjects where they matter), actions taken or still pending, and open questions. ` +
	`Keep names, addresses, numbers and dates exact. Write only the summary.` + "\n\n" + ai.UntrustedSystemNote

// turnPrefixSystem condenses the first part of a turn a summary ends inside
// (pi's turn prefix): the rest of the turn goes on after it.
const turnPrefixSystem = `You condense the first part of the user's latest request to an email assistant, still under way, so the work can go on without it. ` +
	`Write a concise summary in the language the user writes in: the request as the user asked it, what was found in mail so far ` +
	`(senders, dates and subjects where they matter), actions taken, and what is left to do. ` +
	`Keep names, addresses, numbers and dates exact. Write only the summary.` + "\n\n" + ai.UntrustedSystemNote

// splitTurnNote heads a split turn's summary, after the turns' before it.
const splitTurnNote = "The latest request, still under way, so far:"

// compactKeep is the runes of its newest messages a compaction keeps whole
// (pi's keepRecentTokens, about a tenth of the window): an eighth of the budget.
func compactKeep(budget int) int { return budget / 8 }

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
	c, err := s.conversation(ctx, id)
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
	return s.compact(ctx, id, model, true)
}

// convSummary is conversation id's summary and its epoch (bumped by a rewind).
func (s *Service) convSummary(ctx context.Context, id int64) (ai.Summary, int64, error) {
	rows, err := s.db.Query(ctx, "SELECT summary, summary_upto, summary_epoch FROM conversations WHERE id = ?", id)
	if err != nil || len(rows.Values) == 0 {
		return ai.Summary{}, 0, err
	}
	r := rows.Values[0]
	return ai.Summary{Text: store.Str(r[0]), Upto: store.Int(r[1])}, store.Int(r[2]), nil
}

// compactLater compacts conversation id in the background when h, all of
// its history, is past model's history budget with its summary standing in;
// one compaction a conversation at a time.
func (s *Service) compactLater(id int64, model ai.ResolvedModel, h *ai.History) {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	// Under budget, nothing to do: CompactConversation is not kept waiting for it.
	sum, _, err := s.convSummary(ctx, id)
	if err == nil {
		var size int
		if size, err = h.CompactedSize(sum); err == nil && size <= s.historyBudget(model.BaseURL, model.Model) {
			end()
			return
		}
	}
	if _, busy := s.compacting.LoadOrStore(id, true); busy {
		end()
		return
	}
	go func() {
		defer end()
		defer s.compacting.Delete(id)
		if err := s.compact(ctx, id, model, false); err != nil {
			slog.Default().Warn("could not compact a conversation", "conversation", id, "err", err)
		}
	}()
}

// compact summarizes conversation id's history before its newest messages
// (CompactPoint), the summary so far included, when it is past the history
// budget, or, forced, whatever its size.
func (s *Service) compact(ctx context.Context, id int64, model ai.ResolvedModel, force bool) error {
	sum, epoch, err := s.convSummary(ctx, id)
	if err != nil {
		return err
	}
	// The history is read after the epoch: a rewind committed in between bumps it, and the summary is not saved.
	h, err := s.history(ctx, id)
	if err != nil {
		return err
	}
	budget := s.historyBudget(model.BaseURL, model.Model)
	keep := compactKeep(budget)
	if !force {
		size, err := h.CompactedSize(sum)
		if err != nil || size <= budget {
			return err
		}
	}
	if h.CompactPoint(keep) <= sum.Upto {
		if force {
			return ErrNothingToCompact
		}
		return nil
	}
	// One stretch at a time, each prompt within the budget, so a stretch
	// larger than the model's window still moves the summary on.
	for {
		upto, prompt, err := compactPrompt(h, sum, budget, keep)
		if err != nil || upto == 0 {
			return err
		}
		var text string
		if start := h.SplitTurn(upto); start > 0 {
			text, err = s.condenseSplit(ctx, id, model, h, sum, start, upto, budget)
		} else {
			text, err = s.condense(ctx, id, model, compactSystem, prompt)
		}
		if err != nil {
			return err
		}
		if err := s.saveSummary(ctx, id, text, sum, epoch, upto); err != nil {
			return err
		}
		next, nextEpoch, err := s.convSummary(ctx, id)
		if err != nil || nextEpoch != epoch || next.Upto != upto {
			return err // rewound or compacted elsewhere meanwhile
		}
		sum = next
	}
}

// compactPrompt is the prompt that summarizes h's next stretch after sum, the
// summary so far included, and the Upto it reaches: the furthest compact
// point whose prompt fits budget runes, or the next one cut to fit; 0 when
// sum reaches CompactPoint.
func compactPrompt(h *ai.History, sum ai.Summary, budget, keep int) (int64, string, error) {
	var points []int64
	for _, p := range h.CompactPoints(keep) {
		if p > sum.Upto {
			points = append(points, p)
		}
	}
	if len(points) == 0 {
		return 0, "", nil
	}
	from := max(sum.Upto, h.Messages()[0].Seq) // the first message is always sent
	build := func(upto int64) (string, error) {
		prompt, err := h.Transcript(from, upto)
		if err == nil && sum.Text != "" {
			prompt = "Summary of the conversation before this part:\n" + sum.Text + "\n\nThe conversation since:\n" + prompt
		}
		return prompt, err
	}
	var err error
	// prompts grow with upto: the first point past the budget, less one
	n := sort.Search(len(points), func(i int) bool {
		p, e := build(points[i])
		err = errors.Join(err, e)
		return utf8.RuneCountInString(p) > budget
	})
	if err != nil {
		return 0, "", err
	}
	upto := points[max(n-1, 0)]
	prompt, err := build(upto)
	return upto, fit(prompt, budget), err
}

// fit is prompt cut to budget runes: one stretch alone may be past it.
func fit(prompt string, budget int) string {
	if rs := []rune(prompt); len(rs) > budget {
		return string(rs[:budget]) + " [...]"
	}
	return prompt
}

// turnsBetween reports whether h has messages the model sees after seq from
// and before seq start: seqs are not reused, so a gap (a turn asked again)
// may hold none.
func turnsBetween(h *ai.History, from, start int64) bool {
	return slices.ContainsFunc(h.Messages(), func(m ai.Message) bool {
		return m.Seq > from && m.Seq < start && m.Command == nil
	})
}

// condenseSplit summarizes h through upto, which ends inside the turn of the
// question at start (pi's split turn): the turns before it with the summary
// so far, as ever, and the turn's first part apart, under splitTurnNote.
func (s *Service) condenseSplit(ctx context.Context, id int64, model ai.ResolvedModel, h *ai.History, sum ai.Summary, start, upto int64, budget int) (string, error) {
	from := max(sum.Upto, h.Messages()[0].Seq) // the first message is always sent
	head := sum.Text
	if turnsBetween(h, from, start) { // whole turns since the summary, before the split one
		prompt, err := h.Transcript(from, start-1)
		if err != nil {
			return "", err
		}
		if sum.Text != "" {
			prompt = "Summary of the conversation before this part:\n" + sum.Text + "\n\nThe conversation since:\n" + prompt
		}
		if head, err = s.condense(ctx, id, model, compactSystem, fit(prompt, budget)); err != nil {
			return "", err
		}
	}
	prompt, err := h.Transcript(max(start-1, from), upto)
	if err != nil {
		return "", err
	}
	part, err := s.condense(ctx, id, model, turnPrefixSystem, fit(prompt, budget))
	if err != nil || head == "" {
		return part, err
	}
	return head + "\n\n" + splitTurnNote + "\n" + part, nil
}

// condense has the model write what system asks of prompt (a summary); the
// conversation takes its tokens, as a rewind's runs (spent_*).
func (s *Service) condense(ctx context.Context, id int64, model ai.ResolvedModel, system, prompt string) (string, error) {
	e, err := s.aiEngine()
	if err != nil {
		return "", err
	}
	events, wait := e.Run(ctx, ai.Request{Model: model, System: system, Prompt: prompt, MaxTurns: 1})
	var text strings.Builder
	for ev := range events {
		if ev.Kind == "text" {
			text.WriteString(ev.Text)
		}
	}
	res := wait()
	s.runRejected(model, res.Err)
	spent := sqlite.Statement{SQL: `UPDATE conversations SET spent_input = spent_input + ?, spent_output = spent_output + ? WHERE id = ?`,
		Args: []any{res.Usage.Input, res.Usage.Output, id}}
	if res.Err != nil {
		return "", errors.Join(res.Err, s.exec(ctx, spent))
	}
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{spent}); err != nil {
		return "", err
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", errors.New("the model wrote no summary")
	}
	return strings.TrimSpace(text.String()), nil
}

// saveSummary saves text as conversation id's summary through upto, unless its
// summary is no longer sum of epoch.
func (s *Service) saveSummary(ctx context.Context, id int64, text string, sum ai.Summary, epoch, upto int64) error {
	_, err := s.db.Transaction(ctx, []sqlite.Statement{{SQL: "UPDATE conversations SET summary = ?, summary_upto = ? WHERE id = ? AND summary_epoch = ? AND summary_upto = ?",
		Args: []any{text, upto, id, epoch, sum.Upto}}})
	return err
}
