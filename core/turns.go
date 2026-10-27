package core

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

// Turn is one question of a conversation, a command included, and its
// answer: everything Wit did for it up to the next question.
type Turn struct {
	// ID is what AskAgain takes and a run's events carry (WitEvent.Turn);
	// it never changes and means nothing else.
	ID       string
	Question string
	Command  *ai.Command // a command's question: a skill run on the draft (Question is then "")
	Quoted   []Citation  // the mail the user quoted with the question
	// Text and Reasoning are the answer's, its messages' joined; Calls its
	// tool calls in order, each with its result and its approval, if any.
	Text, Reasoning string
	Calls           []TurnCall
	// Steps are the answer in the order it came: each message's reasoning,
	// its text, then its calls.
	Steps []TurnStep
	// Approvals are its runs' approvals that answer none of Calls (a skill's
	// proposal), in order.
	Approvals []Approval
	// Cited is each message its runs showed the model, by their calls or
	// their context, once, with its number in the conversation.
	Cited   []Cited
	Runs    []int64
	Elapsed time.Duration // how long its runs worked (Run.Active)
	// Masked is how many values 脱敏 masked in what its runs sent (已脱敏 N
	// 处), Warned how many it sent as they are (仅警告).
	Masked, Warned int
	// Asked is when its first run began, Answered when its last one last
	// moved on (zero: none).
	Asked, Answered time.Time
	// CanAskAgain: AskAgain takes it now.
	CanAskAgain bool
}

// TurnCall is a tool call of a turn's answer, with its result (nil: none
// yet) and the approval it waits on or waited on (nil: none).
type TurnCall struct {
	ai.ToolCall
	Result   *ai.ToolResult
	Approval *Approval
}

// TurnStep is a step of a turn's answer: its reasoning or text (Text), or a
// call (Call, its index in the turn's Calls).
type TurnStep struct {
	Kind string // "reasoning", "text" or "call"
	Text string
	Call int
}

// Cited is a message a turn's runs showed the model, as the conversation
// cites it (suspect once any citation is): every call of the turn that
// showed it, and whether its runs' context did (an assist run's).
type Cited struct {
	Citation
	CallIDs   []string
	InContext bool
}

// runInfo is a run of a conversation as its turns need it.
type runInfo struct {
	id             int64
	from, to       int64
	active         time.Duration
	began, moved   time.Time // created_at, updated_at
	masked, warned int
}

// turnsOf groups h's messages into turns with what its runs, approvals and
// citations add; expand expands a message's link references, as each was
// live. canAsk: questions may be asked again now (an ask conversation with no
// active run).
func turnsOf(h *ai.History, runs []runInfo, as []Approval, cs []Citation, canAsk bool, expand func(ai.Message) ai.Message) []Turn {
	mail := map[int64]Citation{} // each message as cited: its first citation, suspect once any is
	for _, c := range cs {
		m, ok := mail[c.MessageID]
		if !ok {
			c.RunID, c.CallID, c.Source = 0, "", "" // a citation of the message, not of the call or context that showed it first
			mail[c.MessageID] = c
			continue
		}
		m.Suspect = m.Suspect || c.Suspect
		m.Quote = cmp.Or(m.Quote, c.Quote)
		mail[c.MessageID] = m
	}
	out := []Turn{}
	for _, ht := range h.Turns() {
		t := Turn{ID: strconv.FormatInt(ht.Seq, 10), Question: ht.Question.Text, Command: ht.Question.Command, Quoted: []Citation{},
			Calls: []TurnCall{}, Steps: []TurnStep{}, Approvals: []Approval{}, Cited: []Cited{}, Runs: []int64{}, CanAskAgain: canAsk && ht.Question.Command == nil}
		for _, r := range runs {
			if r.from < ht.To && r.to > ht.Seq {
				if len(t.Runs) == 0 {
					t.Asked = r.began
				}
				t.Runs = append(t.Runs, r.id)
				t.Elapsed += r.active
				t.Answered = r.moved
				// a resume sends what the run before it sent, and more
				t.Masked, t.Warned = max(t.Masked, r.masked), max(t.Warned, r.warned)
			}
		}
		calls := map[string]int{} // call ID -> index in t.Calls
		for _, m := range ht.Answer {
			m = expand(m)
			if strings.TrimSpace(m.Reasoning) != "" {
				t.Steps = append(t.Steps, TurnStep{Kind: "reasoning", Text: m.Reasoning})
			}
			if m.Role != "tool" && strings.TrimSpace(m.Text) != "" {
				t.Text = join(t.Text, m.Text)
				t.Steps = append(t.Steps, TurnStep{Kind: "text", Text: m.Text})
			}
			t.Reasoning = join(t.Reasoning, m.Reasoning)
			for _, c := range m.Calls {
				calls[c.ID] = len(t.Calls)
				t.Steps = append(t.Steps, TurnStep{Kind: "call", Call: len(t.Calls)})
				t.Calls = append(t.Calls, TurnCall{ToolCall: c})
			}
			for _, r := range m.Results {
				if i, ok := calls[r.CallID]; ok {
					r := r
					t.Calls[i].Result = &r
				}
			}
		}
		for _, a := range as {
			if !slices.Contains(t.Runs, a.RunID) {
				continue
			}
			// a call's approval is its run's: a provider may reuse a call ID in another run
			if i, ok := calls[a.CallID]; ok && t.Calls[i].Approval == nil {
				a := a
				t.Calls[i].Approval = &a
				continue
			}
			t.Approvals = append(t.Approvals, a)
		}
		for _, c := range cs {
			if !slices.Contains(t.Runs, c.RunID) {
				continue
			}
			if c.CallID == "" && (c.Source == citeQuote || c.Source == citeUnknown) {
				t.Quoted = append(t.Quoted, c)
			}
			i := slices.IndexFunc(t.Cited, func(x Cited) bool { return x.MessageID == c.MessageID })
			if i < 0 {
				t.Cited = append(t.Cited, Cited{Citation: mail[c.MessageID], CallIDs: []string{}})
				i = len(t.Cited) - 1
			}
			if c.CallID != "" && !slices.Contains(t.Cited[i].CallIDs, c.CallID) {
				t.Cited[i].CallIDs = append(t.Cited[i].CallIDs, c.CallID)
			}
			t.Cited[i].InContext = t.Cited[i].InContext || c.CallID == "" && (c.Source == citeContext || c.Source == citeUnknown)
		}
		out = append(out, t)
	}
	return out
}

func join(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + "\n\n" + b
}

// conversationRuns are conversation id's runs that added any of its history,
// in run order.
func (s *Service) conversationRuns(ctx context.Context, id int64) ([]runInfo, error) {
	rows, err := s.db.Query(ctx, "SELECT id, hist_from, hist_to, active_ms, created_at, updated_at, masked, mask_warned FROM runs WHERE conversation_id = ? AND hist_from IS NOT NULL ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	out := make([]runInfo, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, runInfo{id: store.Int(r[0]), from: store.Int(r[1]), to: store.Int(r[2]), active: time.Duration(store.Int(r[3])) * time.Millisecond,
			began: time.Unix(store.Int(r[4]), 0), moved: time.Unix(store.Int(r[5]), 0), masked: int(store.Int(r[6])), warned: int(store.Int(r[7]))})
	}
	return out, nil
}
