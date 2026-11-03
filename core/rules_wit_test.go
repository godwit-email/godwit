package core

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	archiveProposal = `{"name":"Stripe","trigger":"receive","match":"all","conditions":[{"field":"from","op":"contains","value":"stripe.com"}],"actions":[{"kind":"archive"}]}`
	// forwardProposal also claims its recipient confirmed: only Godwit's UI can.
	forwardProposal = `{"name":"To accounting","trigger":"receive","match":"all","conditions":[{"field":"subject","op":"contains","value":"invoice"}],` +
		`"actions":[{"kind":"forward","to":"acct@company.example"},{"kind":"archive"}],"confirmed":["acct@company.example"]}`
)

// propose has Wit call propose_rule with args and returns its approval.
func (e *approvalEnv) propose(t *testing.T, args string) Approval {
	t.Helper()
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(proposeRuleTool, args), finished)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	return last.Approvals[0]
}

func (e *approvalEnv) savedRules(t *testing.T) []Rule {
	t.Helper()
	rs, err := e.s.Rules(t.Context())
	require.NoError(t, err)
	return rs
}

// Wit's proposal is a card waiting for the user: nothing is saved.
func TestProposeRuleSavesNothing(t *testing.T) {
	e := newApprovalEnv(t)
	a := e.propose(t, archiveProposal)
	assert.Equal(t, "pending", a.State)
	assert.Empty(t, a.Destinations)
	assert.Empty(t, e.savedRules(t))

	var p ruleProposal
	require.NoError(t, json.Unmarshal(a.Args, &p))
	assert.Equal(t, RuleVersion, p.Rule.Version)
	assert.Equal(t, "Stripe", p.Rule.Name)
}

// A proposal for one account names it for the card.
func TestProposeRuleNamesItsAccount(t *testing.T) {
	e := newApprovalEnv(t)
	res, err := e.s.db.Execute(t.Context(), "INSERT INTO accounts(email, alias) VALUES ('work@company.example', 'Work')")
	require.NoError(t, err)
	a := e.propose(t, `{"name":"Stripe","trigger":"receive","account":`+strconv.FormatInt(res.LastInsertID, 10)+`,"match":"all","actions":[{"kind":"archive"}]}`)
	var p ruleProposal
	require.NoError(t, json.Unmarshal(a.Args, &p))
	assert.Equal(t, res.LastInsertID, p.Rule.Account)
	assert.Equal(t, "Work", p.AccountName)
}

// propose_rule says all it takes: the file actions, with their folder,
// subfolder and naming, and its schema has those fields.
func TestProposeRuleNamesEverythingItTakes(t *testing.T) {
	e := newApprovalEnv(t)
	spec := e.s.tools[proposeRuleTool].Spec
	for _, w := range []string{"pdf", "eml", "attachments", "key_points", "dir", "subfolder", "naming", "{subject}", "age", "older_than"} {
		assert.Contains(t, spec.Description, w)
	}
	item := spec.Schema["properties"].(map[string]any)["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, k := range []string{"dir", "subfolder", "naming"} {
		assert.Contains(t, item, k)
	}
	a := e.propose(t, `{"name":"Bills","trigger":"receive","match":"all","actions":[{"kind":"pdf","dir":"`+t.TempDir()+`","subfolder":true,"naming":"{from}/{date} {subject}"}]}`)
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))
	rs := e.savedRules(t)
	require.Len(t, rs, 1)
	assert.True(t, rs[0].Doc.Actions[0].Subfolder)
	assert.Equal(t, "{from}/{date} {subject}", rs[0].Doc.Actions[0].Naming)
}

// The user's 保存规则 saves the rule as proposed, turned on.
func TestAcceptingAProposalSavesTheRule(t *testing.T) {
	e := newApprovalEnv(t)
	a := e.propose(t, archiveProposal)
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, true))

	rs := e.savedRules(t)
	require.Len(t, rs, 1)
	assert.True(t, rs[0].Enabled)
	assert.Equal(t, "Stripe", rs[0].Doc.Name)
	assert.Equal(t, []RuleAction{{Kind: ActionArchive}}, rs[0].Doc.Actions)
}

// A proposal that forwards names its recipient on the card, and is saved
// only with the recipient confirmed in Godwit: never by what Wit claims.
func TestAForwardProposalNeedsTheRecipientConfirmed(t *testing.T) {
	e := newApprovalEnv(t)
	a := e.propose(t, forwardProposal)
	assert.Equal(t, []string{"acct@company.example"}, a.Destinations)
	var p ruleProposal
	require.NoError(t, json.Unmarshal(a.Args, &p))
	assert.Equal(t, []string{"acct@company.example"}, p.FirstTime)

	require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrRecipientUnconfirmed)
	require.ErrorIs(t, e.s.ResolveApprovalWithArgs(t.Context(), a.ID, true, json.RawMessage(`{"confirmed":["other@x.example"]}`)), ErrRecipientUnconfirmed)
	require.ErrorIs(t, e.s.ResolveAll(t.Context(), a.RunID, true), ErrRecipientUnconfirmed)
	assert.Equal(t, "pending", e.approval(t, a.ID).State)
	assert.Empty(t, e.savedRules(t))

	require.NoError(t, e.s.ResolveApprovalWithArgs(t.Context(), a.ID, true, json.RawMessage(`{"confirmed":["ACCT@company.example"]}`)))
	rs := e.savedRules(t)
	require.Len(t, rs, 1)
	assert.Equal(t, []string{"acct@company.example"}, rs[0].Confirmed)
	assert.Equal(t, "To accounting", rs[0].Doc.Name, "the edit confirms; it does not change the rule")
}

// 拒绝 saves nothing.
func TestRejectingAProposalSavesNothing(t *testing.T) {
	e := newApprovalEnv(t)
	a := e.propose(t, forwardProposal)
	require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, false))
	assert.Equal(t, "rejected", e.approval(t, a.ID).State)
	assert.Empty(t, e.savedRules(t))
}

// 在编辑器里修改 hands the proposed rule to the editor and ends the card
// unsaved: the rule is saved only if the user saves it there.
// A proposal with a Wit action names, besides the forward recipients, the
// host of the model service its actions would send mail to.
func TestAProposalWithWitActionsNamesTheModelHost(t *testing.T) {
	e := newApprovalEnv(t)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.RuleAI = true
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	u, err := url.Parse(e.srv.URL)
	require.NoError(t, err)
	a := e.propose(t, `{"name":"Reply","trigger":"receive","match":"all","actions":[{"kind":"draft_reply"},{"kind":"forward","to":"Acct <acct@company.example>"}]}`)
	assert.Equal(t, []string{"acct@company.example", u.Host}, a.Destinations)
}

func TestEditingAProposalHandsItToTheEditor(t *testing.T) {
	e := newApprovalEnv(t)
	a := e.propose(t, forwardProposal)
	d, err := e.s.EditProposedRule(t.Context(), a.ID)
	require.NoError(t, err)
	assert.Equal(t, "To accounting", d.Doc.Name)
	assert.Equal(t, "acct@company.example", d.Doc.Actions[0].To)
	assert.Empty(t, d.Confirmed)
	got := e.approval(t, a.ID)
	assert.Equal(t, "rejected", got.State)
	assert.Equal(t, msgToEditor, got.Error)
	assert.Empty(t, e.savedRules(t))

	_, err = e.s.EditProposedRule(t.Context(), a.ID)
	require.Error(t, err, "once")
}

// An invalid rule goes back to Wit as an error, with no card.
func TestAnInvalidProposalIsRefused(t *testing.T) {
	e := newApprovalEnv(t)
	evs := e.ask(t, e.newConv(t, "ask", 0), callArgs(proposeRuleTool, `{"name":"x","trigger":"receive","match":"all","actions":[{"kind":"delete_forever"}]}`), finished)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM approvals"))
	assert.Empty(t, e.savedRules(t))
}

// aiRules gives the env a default model on a fake service, with the
// classifier off (it would ask the model too), and 「智能」's rule switch as on.
func (e *syncEnv) aiRules(t *testing.T, on bool) *testllm.Server {
	t.Helper()
	srv := testllm.Start(t, "openai")
	_, err := e.s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: srv.URL, APIKey: "k", Models: []string{"m1"}})
	require.NoError(t, err)
	e.setRuleAI(t, on)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.DefaultModel, st.AutoLabel = "x/m1", false
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	return srv
}

func (e *syncEnv) setRuleAI(t *testing.T, on bool) {
	t.Helper()
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.RuleAI = on
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
}

func draftReplyRule() RuleDoc {
	return RuleDoc{Version: RuleVersion, Name: "Reply", Trigger: TriggerReceive, Match: MatchAll,
		Conditions: []RuleCondition{{Field: FieldSubject, Op: OpContains, Value: "card"}},
		Actions:    []RuleAction{{Kind: ActionDraftReply}}}
}

const cardNumber = "4111 1111 1111 1111"

// Rule AI actions are off until the user turns them on in 「智能」: no rule
// takes one, and a rule that has one fails it, sending nothing to the model.
func TestRuleAIActionsAreRefusedWhileTheSwitchIsOff(t *testing.T) {
	e := newSyncedEnv(t)
	srv := e.aiRules(t, false)
	_, err := e.s.SaveRule(bg, Rule{Enabled: true, Doc: draftReplyRule()})
	require.ErrorIs(t, err, ErrInvalidRule)

	e.setRuleAI(t, true)
	e.saveRule(t, draftReplyRule(), true)
	e.setRuleAI(t, false)
	e.put(t, "INBOX", eml("c@x", "bob@example.com", "alice@example.com", "Your card", day, "It is "+cardNumber))
	e.sync(t)
	e.waitRuns(t, 1)

	assert.Equal(t, [][]any{{"draft_reply", "failed", RuleFailAIOff}}, e.rows(t, "SELECT kind, state, result FROM rule_action_results"))
	assert.Empty(t, srv.Requests())
	assert.Zero(t, e.count(t, "SELECT count(*) FROM drafts"))
}

// 让 Wit 起草回复 writes a reply draft from what the masked mail gave the
// model, and nothing more: it is never sent.
func TestRuleDraftReplyOnlyDrafts(t *testing.T) {
	e := newSyncedEnv(t)
	srv := e.aiRules(t, true)
	srv.Script(testllm.Turn{Text: []string{"<p>Thanks, noted.</p>"}})
	e.saveRule(t, draftReplyRule(), true)
	e.put(t, "INBOX", eml("c@x", "Bob <bob@example.com>", "alice@example.com", "Your card", day, "It is "+cardNumber))
	e.sync(t)
	e.waitRuns(t, 1)

	assert.Equal(t, [][]any{{"draft_reply", "done"}}, e.rows(t, "SELECT kind, state FROM rule_action_results"))
	require.Len(t, srv.Requests(), 1)
	body := string(srv.Requests()[0].Body)
	assert.NotContains(t, body, cardNumber)
	assert.Contains(t, body, "{{BANK_CARD_")
	drafts := e.rows(t, "SELECT mode, source_message_id, to_addrs, body_html FROM drafts")
	require.Len(t, drafts, 1)
	assert.Equal(t, "reply", drafts[0][0])
	assert.Equal(t, e.idOf(t, "c@x"), drafts[0][1])
	assert.Contains(t, drafts[0][2], "bob@example.com")
	assert.Contains(t, drafts[0][3], "Thanks, noted.")
	assert.Zero(t, e.outboxLen(t))
	assert.Empty(t, e.smtp.Messages())
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, [][]any{{u.Host}}, e.rows(t, "SELECT destination FROM audit_log WHERE source = ? AND tool = 'draft_reply'", AuditRule),
		"the audit names the model service's host")
}

// 让 Wit 提取要点 writes the key points the model gave into the folder, as
// the file actions name their files.
func TestRuleKeyPointsWritesTheFolder(t *testing.T) {
	e := newSyncedEnv(t)
	srv := e.aiRules(t, true)
	srv.Script(testllm.Turn{Text: []string{"- Meet on Friday"}})
	dir := t.TempDir()
	e.saveRule(t, RuleDoc{Version: RuleVersion, Name: "Points", Trigger: TriggerReceive, Match: MatchAll,
		Actions: []RuleAction{{Kind: ActionKeyPoints, Dir: dir, Naming: "{subject}"}}}, true)
	e.put(t, "INBOX", eml("p@x", "bob@example.com", "alice@example.com", "Plans", day, "Meet on Friday at the harbour."))
	e.sync(t)
	e.waitRuns(t, 1)

	b, err := os.ReadFile(filepath.Join(dir, "Plans.md"))
	require.NoError(t, err)
	assert.Equal(t, "- Meet on Friday\n", string(b))
	assert.Equal(t, [][]any{{"key_points", "done"}}, e.rows(t, "SELECT kind, state FROM rule_action_results"))
}
