package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
)

// proposeRuleTool is Wit's rule tool. It only proposes (ADR 0030): the
// approval is the preview card, and the rule is saved by the user's 保存规则
// on it, with a forward's recipient confirmed there, or from the editor.
const proposeRuleTool = "propose_rule"

// ErrRecipientUnconfirmed: a rule proposal that forwards is saved only once
// the user confirmed each recipient in Godwit (ResolveApprovalWithArgs).
var ErrRecipientUnconfirmed = errors.New("the forward recipient is not confirmed")

// msgToEditor is what the model is told when the user took its proposal to
// the editor (EditProposedRule).
const msgToEditor = "the user opened the rule in the editor; it is saved only if they save it there"

// proposalShown is how many matching messages a proposal's card lists.
const proposalShown = 3

// ruleProposal is propose_rule's recorded arguments: the rule, and what its
// card shows: the forward recipients no account has written to, the
// existing mail it matches and the names of the labels it names. Confirmed
// comes only from the user's decision (confirmedRuleArgs), never from Wit.
type ruleProposal struct {
	Rule       RuleDoc           `json:"rule"`
	Background bool              `json:"background"`
	FirstTime  []string          `json:"first_time"`
	Matches    int               `json:"matches"`
	Mail       []proposedMail    `json:"mail"`
	Labels     map[string]string `json:"labels"`
	Confirmed  []string          `json:"confirmed,omitempty"`
}

type proposedMail struct {
	ID      int64     `json:"id"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

func (s *Service) registerRuleTool() {
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: proposeRuleTool,
		Description: "Propose a mail rule for the user to review: when (trigger), if (conditions), then (actions, in order). " +
			"It saves nothing: the user sees it as a preview card and saves it themselves, or opens it in the rule editor; " +
			"a forward's recipient is confirmed by the user in Godwit, never by you. " +
			"trigger: receive (new mail), label (trigger_arg: a label slug), security (trigger_arg: suspicious or dangerous), daily. " +
			"conditions (match all or any; none matches all mail): field from, to, subject or body with op contains, not_contains or is; " +
			"attachments is yes or no; label (a slug) or folder (inbox, archive, junk or trash) with op is or not_contains. " +
			"actions: label or unlabel (label: a slug), read, star, archive, junk, trash, stop (later rules skip the mail), forward (to: one address), " +
			"draft_reply (Wit drafts a reply; never sent), key_points (Wit writes the mail's key points to dir, an absolute folder path). " +
			"draft_reply and key_points need the user's 「智能」 switch for rules. background keeps the rule running after the window closes.",
		Schema: object(map[string]any{
			"name":        stringProp,
			"trigger":     map[string]any{"type": "string", "enum": []string{TriggerReceive, TriggerLabel, TriggerSecurity, TriggerDaily}},
			"trigger_arg": stringProp,
			"match":       map[string]any{"type": "string", "enum": []string{MatchAll, MatchAny}},
			"conditions": map[string]any{"type": "array", "maxItems": MaxRuleConditions, "items": object(map[string]any{
				"field": stringProp, "op": stringProp, "value": stringProp,
			}, "field", "op", "value")},
			"actions": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxRuleActions, "items": object(map[string]any{
				"kind": stringProp, "label": stringProp, "to": stringProp, "dir": stringProp,
			}, "kind")},
			"background": map[string]any{"type": "boolean"},
		}, "name", "trigger", "match", "actions")},
		Write: true, Exec: &executor{Prepare: s.prepareRule, Apply: s.applyRule}})
}

// prepareRule checks the proposed rule as SaveRule would, and records it
// with what its card shows. Its destinations are the forward recipients.
func (s *Service) prepareRule(ctx context.Context, _ Conversation, raw json.RawMessage) (prepared, error) {
	var in struct {
		RuleDoc
		Background bool `json:"background"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return prepared{}, err
	}
	d := in.RuleDoc
	d.Version, d.Name = RuleVersion, strings.TrimSpace(d.Name)
	if d.Conditions == nil {
		d.Conditions = []RuleCondition{}
	}
	if err := s.validRule(ctx, d); err != nil {
		return prepared{}, err
	}
	rcpts := forwardRecipients(d)
	first, err := s.FirstTimeRecipients(ctx, rcpts)
	if err != nil {
		return prepared{}, err
	}
	prev, err := s.PreviewRule(ctx, d, proposalShown)
	if err != nil {
		return prepared{}, err
	}
	p := ruleProposal{Rule: d, Background: in.Background, FirstTime: first, Matches: prev.Count, Labels: map[string]string{}}
	for _, m := range prev.Messages {
		p.Mail = append(p.Mail, proposedMail{ID: m.ID, From: m.From, Subject: m.Subject, Date: m.Date})
	}
	ls, err := s.listLabels(ctx)
	if err != nil {
		return prepared{}, err
	}
	for _, l := range ls {
		p.Labels[l.Slug] = l.Name
	}
	b, err := json.Marshal(p)
	if err != nil {
		return prepared{}, err
	}
	return prepared{Args: b, Summary: "Create rule: " + d.Name, TargetKind: "rule", Destinations: rcpts}, nil
}

// confirmedRuleArgs is a proposal's recorded arguments with the recipients
// the user confirmed (edited: {"confirmed": [...]}, from the card), or
// ErrRecipientUnconfirmed while one it forwards to is not.
func confirmedRuleArgs(recorded, edited json.RawMessage) (json.RawMessage, error) {
	var p ruleProposal
	if err := json.Unmarshal(recorded, &p); err != nil {
		return nil, err
	}
	var ed struct {
		Confirmed []string `json:"confirmed"`
	}
	if edited != nil {
		if err := json.Unmarshal(edited, &ed); err != nil {
			return nil, err
		}
	}
	p.Confirmed = nil
	for _, k := range forwardRecipients(p.Rule) {
		if !slices.ContainsFunc(ed.Confirmed, func(c string) bool { return rcptKey(c) == k }) {
			return nil, ErrRecipientUnconfirmed
		}
		p.Confirmed = append(p.Confirmed, k)
	}
	return json.Marshal(p)
}

// applyRule saves the proposed rule, turned on, with the approval.
func (s *Service) applyRule(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var p ruleProposal
	if err := json.Unmarshal(a.Args, &p); err != nil {
		return nil, nil, nil, err
	}
	st, err := s.ruleWrite(ctx, Rule{Enabled: true, Background: p.Background, Doc: p.Rule, Confirmed: p.Confirmed})
	if err != nil {
		return nil, nil, nil, err
	}
	return []sqlite.Statement{st}, map[string]any{"saved": true, "name": p.Rule.Name}, nil, nil
}

// EditProposedRule ends pending propose_rule approval id unsaved and returns
// its rule for the editor (在编辑器里修改), turned on: the rule is saved
// only if the user saves it there, confirming any forward recipient.
func (s *Service) EditProposedRule(ctx context.Context, id int64) (Rule, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Rule{}, err
	}
	defer end()
	a, err := s.approvalRow(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	var p ruleProposal
	if a.Tool != proposeRuleTool {
		return Rule{}, fmt.Errorf("approval %d is not a rule proposal", id)
	}
	if err := json.Unmarshal(a.Args, &p); err != nil {
		return Rule{}, err
	}
	err = s.ledger.execute(ctx, id, a.RunID, func(ctx context.Context) error {
		err := s.ledger.finish(ctx, a, "rejected", msgToEditor, nil, nil, "rejected")
		s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: a.ConversationID})
		return err
	}, true)
	if err != nil {
		return Rule{}, s.cleanError(err)
	}
	return Rule{Enabled: true, Background: p.Background, Doc: p.Rule}, nil
}

// RuleAIActions are the rule actions Wit does: they send the message to the
// default model, masked (featureRules), only while Settings.RuleAI is on.
var RuleAIActions = []string{ActionDraftReply, ActionKeyPoints}

const (
	ruleReplySystem = `Write a reply the user could send to the email you are given, written as the user. ` +
		`Answer with the reply only, as simple HTML (paragraphs, line breaks): no greeting placeholders, no signature, no quoted text.`
	ruleKeyPointsSystem = `List the key points of the email you are given as a short Markdown list: facts, dates, amounts, requests and deadlines. ` +
		`Answer with the list only.`
)

func (s *Service) ruleAIOn(ctx context.Context) (bool, error) {
	st, err := s.loadSettings(ctx)
	return st.RuleAI, err
}

// ruleAsk sends the message to the default model, masked, for a rule's AI
// action kind, and returns the reply; failure is why it could not.
func (s *Service) ruleAsk(ctx context.Context, kind, system string, msgID int64) (reply, string, error) {
	model, _, err := s.resolveTask(ctx, "", false)
	if err != nil {
		return reply{}, RuleFailModel, nil
	}
	items, err := s.mailContext(ctx, &gateway.Scope{}, []int64{msgID}, defaultMailBudget)
	if err != nil || len(items) == 0 {
		return reply{}, RuleFailGone, err
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: AuditRule, Tool: kind, TargetKind: "message", TargetID: msgID, Decision: "run"}); err != nil {
		return reply{}, "", err
	}
	system += "\n\n" + ai.UntrustedSystemNote + "\n\nWrite in the language of the email."
	r, err := s.complete(ctx, featureRules, ai.Request{Model: model, System: system, Prompt: items[0].Block, Raw: true}, 0)
	if err != nil || strings.TrimSpace(r.Text) == "" {
		return reply{}, RuleFailModel, nil
	}
	return r, "", nil
}

// ruleDraftReply saves a reply draft to the message with the model's text:
// a draft only, which the user sends or not.
// shortcut: a crash between the draft and its result drafts again on resume; a spare draft harms nothing.
func (s *Service) ruleDraftReply(ctx context.Context, acc, msgID int64) (string, error) {
	r, failure, err := s.ruleAsk(ctx, ActionDraftReply, ruleReplySystem, msgID)
	if failure != "" || err != nil {
		return failure, err
	}
	d, err := s.NewDraft(ctx, acc, "reply", msgID)
	if err != nil {
		return RuleFailUnknown, nil
	}
	_, quote, sig := mail.SplitDraft(d.HTML)
	if d.HTML, err = s.sanitizeDraft(ctx, mail.JoinDraft(cleanHTML(r.RestoreHTML(r.Text)), quote, sig)); err == nil {
		_, err = s.SaveDraft(ctx, d)
	}
	if err != nil {
		return RuleFailUnknown, s.deleteDraft(context.WithoutCancel(ctx), d.ID)
	}
	return "", nil
}

// ruleKeyPoints is the message's key points, as Markdown, from the model.
func (s *Service) ruleKeyPoints(ctx context.Context, msgID int64) (string, string, error) {
	r, failure, err := s.ruleAsk(ctx, ActionKeyPoints, ruleKeyPointsSystem, msgID)
	if failure != "" || err != nil {
		return "", failure, err
	}
	return strings.TrimSpace(r.Restore(r.Text)) + "\n", "", nil
}
