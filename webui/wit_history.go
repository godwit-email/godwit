package webui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core"
)

// WitHistory is a conversation as the Wit panel shows it.
type WitHistory struct {
	// Conversation is 0 when the draft has none yet.
	Conversation int64        `json:"conversation"`
	Messages     []WitMessage `json:"messages"`
	// Running is the run going on (not one waiting for a decision), else 0.
	Running int64 `json:"running"`
	// Approvals are every approval of the conversation, in any state.
	Approvals []Approval `json:"approvals"`
	// Model is the conversation's model (a ModelChoice's Ref); "" for the default.
	Model string `json:"model"`
}

// WitMessage is a question, or the answer to one: everything the model said
// and called up to the next question.
type WitMessage struct {
	ID string `json:"id"`
	// Role is user or assistant; an answer's Text is Markdown.
	Role string `json:"role"`
	Text string `json:"text"`
	// Reasoning is what the model showed of its reasoning (an answer only).
	Reasoning string      `json:"reasoning"`
	Calls     []WitCall   `json:"calls"`
	Results   []WitResult `json:"results"`
	Citations []Citation  `json:"citations"`
	// Runs are the runs that added it (a question: the run it started).
	Runs []int64 `json:"runs"`
}

// witMessages groups c's messages into questions and answers, as
// app/wit_run.go's witMsgs. An answer cites the mail the runs that added
// it read, with their numbers in the conversation (cited).
func witMessages(c core.Conversation) []WitMessage {
	out := []WitMessage{}
	var spans [][2]int // out[i] is c.Messages[from:to]
	for i, m := range c.Messages {
		if m.Role == "user" {
			out = append(out, WitMessage{ID: fmt.Sprintf("q%d", i), Role: "user", Text: m.Text,
				Calls: []WitCall{}, Results: []WitResult{}, Citations: []Citation{}})
			spans = append(spans, [2]int{i, i + 1})
			continue
		}
		if n := len(out); n == 0 || out[n-1].Role == "user" {
			out = append(out, WitMessage{ID: fmt.Sprintf("a%d", i), Role: "assistant",
				Calls: []WitCall{}, Results: []WitResult{}, Citations: []Citation{}})
			spans = append(spans, [2]int{i, i})
		}
		ans := &out[len(out)-1]
		spans[len(spans)-1][1] = i + 1
		if m.Role != "tool" && strings.TrimSpace(m.Text) != "" {
			if ans.Text != "" {
				ans.Text += "\n\n"
			}
			ans.Text += m.Text
		}
		if m.Reasoning != "" {
			if ans.Reasoning != "" {
				ans.Reasoning += "\n\n"
			}
			ans.Reasoning += m.Reasoning
		}
		for _, call := range m.Calls {
			ans.Calls = append(ans.Calls, callOf(call))
		}
		for _, r := range m.Results {
			ans.Results = append(ans.Results, resultOf(r))
		}
	}
	all := cited(c.Citations)
	for i := range out {
		out[i].Runs = []int64{}
		for _, r := range c.Runs {
			if r.From < spans[i][1] && r.To > spans[i][0] {
				out[i].Runs = append(out[i].Runs, r.ID)
			}
		}
	}
	for i := range out {
		if out[i].Role == "user" {
			continue
		}
		ids := citedBy(c, spans[i][0], spans[i][1])
		for _, ct := range all {
			if slices.Contains(ids, ct.MessageID) {
				out[i].Citations = append(out[i].Citations, ct)
			}
		}
	}
	return out
}

// cited is each cited message once, with core's number; suspect once any
// citation of it is.
func cited(cs []core.Citation) []Citation {
	var out []Citation
	for _, c := range cs {
		if i := slices.IndexFunc(out, func(x Citation) bool { return x.MessageID == c.MessageID }); i >= 0 {
			out[i].Suspect = out[i].Suspect || c.Suspect
			out[i].Quote = cmp.Or(out[i].Quote, c.Quote)
			continue
		}
		out = append(out, citationOf(c))
	}
	return out
}

// citedBy is the messages cited by the runs that added any of
// c.Messages[from:to], by their calls or their context.
func citedBy(c core.Conversation, from, to int) []int64 {
	var ids []int64
	for _, r := range c.Runs {
		if r.From >= to || r.To <= from {
			continue
		}
		for _, ct := range c.Citations {
			if ct.RunID == r.ID {
				ids = append(ids, ct.MessageID)
			}
		}
	}
	return ids
}
