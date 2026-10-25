package webui

import (
	"github.com/godwit-email/godwit/core"
)

// WitHistory is a conversation as the Wit panel shows it.
type WitHistory struct {
	// Conversation is 0 when the draft has none yet.
	Conversation int64     `json:"conversation"`
	Turns        []WitTurn `json:"turns"`
	// Running is the run going on (not one waiting for a decision), else 0.
	Running int64 `json:"running"`
	// Model is the conversation's model (a ModelChoice's Ref); "" for the default.
	Model string `json:"model"`
	// Effort is the conversation's reasoning effort: low, medium, high; "" for none.
	Effort string   `json:"effort"`
	Usage  WitUsage `json:"usage"`
}

// WitUsage is what a conversation took: its runs' input and output tokens,
// the latest run's last input (Context; 0 when the service reported none),
// how full its history budget is (History, from 0; past 1 the oldest turns
// are summarized or left out), and whether WitPage.Compact has turns to
// summarize (Compactable).
type WitUsage struct {
	Input       int     `json:"input"`
	Output      int     `json:"output"`
	Context     int     `json:"context"`
	History     float64 `json:"history"`
	Compactable bool    `json:"compactable"`
}

// WitTurn is a question and its answer (core.Turn): ID is what a run's
// events carry and Ask's rewind takes.
type WitTurn struct {
	ID string `json:"id"`
	// Question is the question asked; for a command (a skill run on the draft), Command.
	Question string      `json:"question"`
	Command  *WitCommand `json:"command"`
	// Quoted is the mail the question quoted.
	Quoted []MailHit `json:"quoted"`
	// Text is the answer, Markdown; Reasoning what the model showed of its reasoning.
	Text      string `json:"text"`
	Reasoning string `json:"reasoning"`
	// Calls are the answer's tool calls, each with its result and approval.
	Calls []WitTurnCall `json:"calls"`
	// Steps are the answer in the order it came (core.TurnStep).
	Steps []WitStep `json:"steps"`
	// Approvals are the turn's approvals that answer none of Calls (a skill's edit).
	Approvals []Approval `json:"approvals"`
	// Citations are the mail its runs showed the model, by their calls or their context.
	Citations []Citation `json:"citations"`
	Runs      []int64    `json:"runs"`
	// Elapsed is how long its runs worked, in milliseconds (0: not known, as
	// for a run before this was kept).
	Elapsed int64 `json:"elapsed"`
	// CanAskAgain: Ask's rewind takes it now.
	CanAskAgain bool `json:"canAskAgain"`
}

// WitStep is a step of an answer: its reasoning or text, or a call (Call, its
// index in Calls).
type WitStep struct {
	Kind string `json:"kind"` // "reasoning", "text" or "call"
	Text string `json:"text"`
	Call int    `json:"call"`
}

// WitCommand is a command's skill and, for translate-draft, its language tag.
type WitCommand struct {
	Skill string `json:"skill"`
	Lang  string `json:"lang"`
}

// WitTurnCall is a tool call of an answer with its result and the approval it
// waits or waited on (each nil: none).
type WitTurnCall struct {
	Call     WitCall    `json:"call"`
	Result   *WitResult `json:"result"`
	Approval *Approval  `json:"approval"`
}

// witTurns is c's turns for the panel.
func witTurns(c core.Conversation) []WitTurn {
	out := make([]WitTurn, 0, len(c.Turns))
	for _, t := range c.Turns {
		w := WitTurn{ID: t.ID, Question: t.Question, Quoted: []MailHit{}, Text: t.Text, Reasoning: t.Reasoning, Calls: []WitTurnCall{},
			Steps: make([]WitStep, 0, len(t.Steps)), Approvals: []Approval{}, Citations: []Citation{}, Runs: append([]int64{}, t.Runs...), Elapsed: t.Elapsed.Milliseconds(), CanAskAgain: t.CanAskAgain}
		if t.Command != nil {
			w.Command = &WitCommand{Skill: t.Command.Skill, Lang: t.Command.Lang}
		}
		for _, q := range t.Quoted {
			c := citationOf(q)
			w.Quoted = append(w.Quoted, MailHit{ID: c.MessageID, Subject: c.Subject, From: c.From, Date: c.Date})
		}
		for _, c := range t.Calls {
			tc := WitTurnCall{Call: callOf(c.ToolCall)}
			if c.Result != nil {
				r := resultOf(*c.Result)
				tc.Result = &r
			}
			if c.Approval != nil {
				a := approvalOf(*c.Approval)
				tc.Approval = &a
			}
			w.Calls = append(w.Calls, tc)
		}
		for _, s := range t.Steps {
			w.Steps = append(w.Steps, WitStep{Kind: s.Kind, Text: s.Text, Call: s.Call})
		}
		for _, a := range t.Approvals {
			w.Approvals = append(w.Approvals, approvalOf(a))
		}
		for _, ct := range t.Cited {
			c := citationOf(ct.Citation)
			c.CallIDs = ct.CallIDs
			w.Citations = append(w.Citations, c)
		}
		out = append(out, w)
	}
	return out
}
