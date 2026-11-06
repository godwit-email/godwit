package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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

// listRulesTool reads the user's rules, as 设置 › 规则 lists them; it
// changes none.
const listRulesTool = "list_rules"

// ErrRecipientUnconfirmed: a rule proposal that forwards is saved only once
// the user confirmed each recipient in Godwit (ResolveApprovalWithArgs).
var ErrRecipientUnconfirmed = errors.New("the forward recipient is not confirmed")

// msgToEditor is what the model was told when opening the editor ended a
// proposal; the web card still names past ones by it.
const msgToEditor = "the user opened the rule in the editor; it is saved only if they save it there"

// proposalShown is how many matching messages a proposal's card lists.
const proposalShown = 3

// ruleProposal is propose_rule's recorded arguments: the rule, and what its
// card shows: the forward recipients no account has written to, the
// existing mail it matches, the names of the labels it names and the
// display name of the account it is for ("" every account). Confirmed
// comes only from the user's decision (confirmedRuleArgs), never from Wit.
type ruleProposal struct {
	Rule        RuleDoc           `json:"rule"`
	Background  bool              `json:"background"`
	FirstTime   []string          `json:"first_time"`
	Matches     int               `json:"matches"`
	Mail        []proposedMail    `json:"mail"`
	Labels      map[string]string `json:"labels"`
	AccountName string            `json:"account_name,omitempty"`
	Confirmed   []string          `json:"confirmed,omitempty"`
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
			"account: an account_id to run on that account's mail only; leave it out for every account. " +
			"conditions (match all or any; none matches all mail): field from, to, subject or body with op contains, not_contains or is; " +
			"attachments is yes or no; label (a slug) or folder (inbox, archive, junk or trash) with op is or not_contains; age with op older_than (value: whole days, from 1). " +
			"actions: label or unlabel (label: a slug), read, star, archive, junk, trash, block (block the sender), stop (later rules skip the mail), forward (to: one address), " +
			"pdf (saves the mail as a PDF), eml (saves the original .eml), attachments (saves its attachments), " +
			"draft_reply (Wit drafts a reply; never sent), key_points (Wit writes the mail's key points as Markdown). " +
			"pdf, eml, attachments and key_points write into dir, an absolute folder path, and only create files, never replace one; " +
			"naming names the files, a template of text and the tokens {date} {subject} {from} {domain} {year} {month} {account} " +
			"(at least one; each / in it makes a folder level); subfolder true puts each mail's files in a folder of its own, named by naming. " +
			"Actions with one dir share naming and subfolder. " +
			"draft_reply and key_points send the mail to the user's model service and need the user's 「智能」 switch for rules. " +
			"background keeps the rule running after the window closes.",
		Schema: object(map[string]any{
			"name":        stringProp,
			"trigger":     map[string]any{"type": "string", "enum": []string{TriggerReceive, TriggerLabel, TriggerSecurity, TriggerDaily}},
			"trigger_arg": stringProp,
			"account":     intProp,
			"match":       map[string]any{"type": "string", "enum": []string{MatchAll, MatchAny}},
			"conditions": map[string]any{"type": "array", "maxItems": MaxRuleConditions, "items": object(map[string]any{
				"field": stringProp, "op": stringProp, "value": stringProp,
			}, "field", "op", "value")},
			"actions": map[string]any{"type": "array", "minItems": 1, "maxItems": MaxRuleActions, "items": object(map[string]any{
				"kind": stringProp, "label": stringProp, "to": stringProp, "dir": stringProp,
				"subfolder": map[string]any{"type": "boolean"}, "naming": stringProp,
			}, "kind")},
			"background": map[string]any{"type": "boolean"},
		}, "name", "trigger", "match", "actions")},
		Write: true, Exec: &executor{Prepare: s.prepareRule, Apply: s.applyRule}})
}

func (s *Service) registerListRulesTool() {
	s.mustRegister(toolDesc{Spec: ai.ToolSpec{Name: listRulesTool,
		Description: "List the user's mail rules, as 设置 › 规则 shows them, in the order they run (top first; a stop action skips the rules below): " +
			"each one's id, name, whether it is on, whether it runs in the background, does (what it does, in plain words, with the labels' and account's names), " +
			"its rule (trigger, account, conditions, actions, in propose_rule's terms) " +
			"and how many mails it handled in the last " + strconv.Itoa(RuleHitDays) + " days. labels names each label slug they use; account_name the account a rule is kept to. " +
			"Tell the user what a rule does from does, in their language; never show them the rule's ids (receive, archive, a label slug). " +
			"It changes nothing: to change a rule, the user edits it in 设置 › 规则; propose_rule proposes a new one.",
		Schema: object(map[string]any{}), Run: s.listRules}})
}

// listRules is list_rules: every rule, with the names its card would show.
func (s *Service) listRules(ctx context.Context, _ json.RawMessage) (any, error) {
	rs, err := s.Rules(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.RuleHitCounts(ctx)
	if err != nil {
		return nil, err
	}
	ls, err := s.listLabels(ctx)
	if err != nil {
		return nil, err
	}
	labels := map[string]string{}
	for _, l := range ls {
		labels[l.Slug] = l.Name
	}
	type listed struct {
		ID          int64   `json:"id"`
		Enabled     bool    `json:"enabled"`
		Background  bool    `json:"background"`
		Does        string  `json:"does"`
		Rule        RuleDoc `json:"rule"`
		AccountName string  `json:"account_name,omitempty"`
		Handled     int     `json:"handled_last_days"`
	}
	out := make([]listed, 0, len(rs))
	for _, r := range rs {
		l := listed{ID: r.ID, Enabled: r.Enabled, Background: r.Background, Rule: r.Doc, Handled: counts[r.ID]}
		if r.Doc.Account != 0 {
			if acc, err := s.account(ctx, r.Doc.Account); err == nil {
				l.AccountName = accountLabel(acc)
			}
		}
		l.Does = ruleDoes(r.Doc, labels, l.AccountName)
		out = append(out, l)
	}
	return map[string]any{"rules": out, "labels": labels}, nil
}

// ruleDoes says what rule d does, in English the model puts in the user's
// language: "When new mail arrives, if the subject contains "invoice" and it
// has attachments: label it 账单, then archive it." labels names the slugs.
func ruleDoes(d RuleDoc, labels map[string]string, account string) string {
	label := func(slug string) string { return cmp.Or(labels[slug], slug) }
	var when string
	switch d.Trigger {
	case TriggerLabel:
		when = "When mail is labeled " + label(d.TriggerArg)
	case TriggerSecurity:
		when = "When the security check flags mail as dangerous"
		if d.TriggerArg == SecuritySuspicious {
			when = "When the security check flags mail as suspicious or dangerous"
		}
	case TriggerDaily:
		when = "Once a day, at the day's first sync, for each mail"
	default:
		when = "When new mail arrives"
	}
	if account != "" {
		when += " in " + account
	}
	conds := make([]string, len(d.Conditions))
	for i, c := range d.Conditions {
		switch c.Field {
		case FieldAttachments:
			conds[i] = "it has attachments"
			if c.Value == RuleNo {
				conds[i] = "it has no attachments"
			}
		case FieldAge:
			conds[i] = "it is older than " + c.Value + " days"
		case FieldLabel, FieldFolder:
			what := "labeled " + label(c.Value)
			if c.Field == FieldFolder {
				what = "in " + folderWords(c.Value)
			}
			conds[i] = "it is " + what
			if c.Op == OpNotContains {
				conds[i] = "it is not " + what
			}
		default:
			field := map[string]string{FieldFrom: "the sender", FieldTo: "a recipient (To or Cc)", FieldSubject: "the subject", FieldBody: "the body"}[c.Field]
			op := map[string]string{OpContains: "contains", OpNotContains: "does not contain", OpIs: "is"}[c.Op]
			conds[i] = fmt.Sprintf("%s %s %q", cmp.Or(field, c.Field), cmp.Or(op, c.Op), c.Value)
		}
	}
	acts := make([]string, len(d.Actions))
	for i, a := range d.Actions {
		switch a.Kind {
		case ActionLabel:
			acts[i] = "label it " + label(a.Label)
		case ActionUnlabel:
			acts[i] = "take the label " + label(a.Label) + " off"
		case ActionForward:
			acts[i] = "forward it to " + a.To
		case ActionPDF, ActionEML, ActionAttachments:
			what := map[string]string{ActionPDF: "save it as a PDF", ActionEML: "save the original .eml", ActionAttachments: "save its attachments"}[a.Kind]
			acts[i] = what + " into the folder " + a.Dir
			if a.Subfolder {
				acts[i] += ", one subfolder per mail"
			}
		default:
			acts[i] = cmp.Or(map[string]string{
				ActionRead: "mark it read", ActionStar: "star it", ActionArchive: "archive it", ActionJunk: "move it to Junk",
				ActionTrash: "move it to Trash", ActionBlock: "block the sender", ActionStop: "skip the rules below it",
				ActionDraftReply: "have Wit draft a reply (never sent)", ActionKeyPoints: "have Wit write its key points",
			}[a.Kind], a.Kind)
		}
		if a.Kind == ActionKeyPoints && a.Dir != "" {
			acts[i] += " into the folder " + a.Dir
		}
	}
	join := " and "
	if d.Match == MatchAny {
		join = " or "
	}
	if len(conds) == 0 {
		return when + ": " + strings.Join(acts, ", then ") + "."
	}
	return when + ", if " + strings.Join(conds, join) + ": " + strings.Join(acts, ", then ") + "."
}

// folderWords names a folder condition's value: a role, or a folder by id.
// shortcut: a folder by id is not named; name it if rules come to use them.
func folderWords(v string) string {
	if r := map[string]string{"inbox": "the inbox", "archive": "the archive", "junk": "Junk", "trash": "Trash"}[v]; r != "" {
		return r
	}
	return "folder " + v
}

// prepareRule checks the proposed rule as SaveRule would, and records it
// with what its card shows. Its destinations are the forward recipients and,
// with a Wit action, the host of the model service it sends mail to.
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
	if d.Account != 0 {
		acc, err := s.account(ctx, d.Account)
		if err != nil {
			return prepared{}, err
		}
		p.AccountName = accountLabel(acc)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return prepared{}, err
	}
	dests := rcpts
	if slices.ContainsFunc(d.Actions, func(a RuleAction) bool { return slices.Contains(RuleAIActions, a.Kind) }) {
		if h := s.ruleAIHost(ctx); h != "" {
			dests = append(slices.Clone(rcpts), h)
		}
	}
	return prepared{Args: b, Summary: "Create rule: " + d.Name, TargetKind: "rule", Destinations: dests}, nil
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

// EditProposedRule returns pending propose_rule approval id's rule for the
// editor (在编辑器里修改), turned on. The proposal stays pending, its card
// in the conversation too: the rule is saved only if the user saves it in
// the editor, confirming any forward recipient (ProposedRuleSaved), or
// back in the conversation from its card.
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
	if a.Tool != proposeRuleTool {
		return Rule{}, fmt.Errorf("approval %d is not a rule proposal", id)
	}
	if a.State != "pending" {
		return Rule{}, ErrInvalidState
	}
	var p ruleProposal
	if err := json.Unmarshal(a.Args, &p); err != nil {
		return Rule{}, err
	}
	return Rule{Enabled: true, Background: p.Background, Doc: p.Rule}, nil
}

// ProposedRuleSaved ends pending propose_rule approval id as saved, with r,
// the rule the user saved for it in the editor (SaveRule): its card shows r
// saved, and the model is told. A proposal no longer pending, saved from
// its card meanwhile, say, is left as it is.
func (s *Service) ProposedRuleSaved(ctx context.Context, id int64, r Rule) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	a, err := s.approvalRow(ctx, id)
	if err != nil {
		return err
	}
	if a.Tool != proposeRuleTool {
		return fmt.Errorf("approval %d is not a rule proposal", id)
	}
	var p ruleProposal
	if err := json.Unmarshal(a.Args, &p); err != nil {
		return err
	}
	p.Rule, p.Background, p.Confirmed = r.Doc, r.Background, r.Confirmed
	args, err := json.Marshal(p)
	if err != nil {
		return err
	}
	res, err := json.Marshal(map[string]any{"saved": true, "name": r.Doc.Name, "edited_in_editor": true})
	if err != nil {
		return err
	}
	err = s.ledger.execute(ctx, id, a.RunID, func(ctx context.Context) error {
		err := s.ledger.finish(ctx, a, "done", string(res), string(args), nil, "executed")
		s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: a.ConversationID})
		return err
	}, true)
	if errors.Is(err, ErrInvalidState) {
		return nil
	}
	return s.cleanError(err)
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

// ruleAIHost is the host[:port] the rule AI actions send mail to: the
// default model service's; "" for none, or one on the device.
func (s *Service) ruleAIHost(ctx context.Context) string {
	m, _, err := s.resolveTask(ctx, "", false)
	if err != nil || m.OnDevice {
		return ""
	}
	return gateway.Host(m.BaseURL)
}

// ruleAsk sends the message to the default model, masked, for a rule's AI
// action kind, and returns the reply; failure is why it could not. Its
// audit names the model service's host.
func (s *Service) ruleAsk(ctx context.Context, kind, system string, msgID int64) (reply, string, error) {
	model, _, err := s.resolveTask(ctx, "", false)
	if err != nil {
		return reply{}, RuleFailModel, nil
	}
	items, err := s.mailContext(ctx, &gateway.Scope{}, []int64{msgID}, defaultMailBudget)
	if err != nil || len(items) == 0 {
		return reply{}, RuleFailGone, err
	}
	var dests []string
	if !model.OnDevice {
		dests = []string{model.BaseURL}
	}
	if err := s.gateway().Record(ctx, gateway.Entry{Source: AuditRule, Tool: kind, TargetKind: "message", TargetID: msgID, Destinations: dests, Decision: "run"}); err != nil {
		return reply{}, "", err
	}
	system += "\n\n" + ai.UntrustedSystemNote + "\n\nWrite in the language of the email."
	// A model that never answers would hold the rules (ruleRunMu) and an undo
	// waiting on them: past ruleAITimeout the action fails, to retry.
	mctx, cancel := context.WithTimeout(ctx, ruleAITimeout)
	defer cancel()
	r, err := s.complete(mctx, featureRules, ai.Request{Model: model, System: system, Prompt: items[0].Block, Raw: true}, 0)
	if ctx.Err() != nil { // closing, not the model: the run resumes
		return reply{}, "", ctx.Err()
	}
	if err != nil || strings.TrimSpace(r.Text) == "" {
		return reply{}, RuleFailModel, nil
	}
	return r, "", nil
}

// ruleAITimeout bounds a rule's ask of the model (a var for tests).
var ruleAITimeout = 2 * time.Minute

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
