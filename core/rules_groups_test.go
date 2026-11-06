package core

import (
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func condGroup(match string, conds ...RuleCondition) RuleCondition {
	return RuleCondition{Match: match, Group: conds}
}

// A rule's condition groups match as written, in the evaluator and in the
// preview: (A or B) and C, and (A and B) or C.
func TestRuleGroupsMatch(t *testing.T) {
	e := newPreviewEnv(t)
	billing := RuleCondition{Field: FieldFrom, Op: OpContains, Value: "billing@"}
	stripe := RuleCondition{Field: FieldFrom, Op: OpContains, Value: "stripe.com"}
	attached := RuleCondition{Field: FieldAttachments, Op: OpIs, Value: RuleYes}
	contract := RuleCondition{Field: FieldSubject, Op: OpContains, Value: "合同"}
	for _, tc := range []struct {
		name string
		d    RuleDoc
		want []string
	}{
		{"(billing or stripe) and attached", RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{
			condGroup(MatchAny, billing, stripe), condGroup(MatchAll, attached)}}, []string{"aws@x"}},
		{"(billing and attached) or contract", RuleDoc{Trigger: TriggerReceive, Match: MatchAny, Conditions: []RuleCondition{
			condGroup(MatchAll, billing, attached), condGroup(MatchAll, contract)}}, []string{"aws@x", "cn@x"}},
		{"(billing or stripe) and (attached or contract)", RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{
			condGroup(MatchAny, billing, stripe), condGroup(MatchAny, attached, contract)}}, []string{"aws@x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want []int64
			for _, mid := range tc.want {
				want = append(want, e.idOf(t, mid))
			}
			assert.ElementsMatch(t, want, e.evaluated(t, tc.d))
			assert.Equal(t, e.evaluated(t, tc.d), e.previewed(t, tc.d))
		})
	}
}

// conditions is all conditions or at least two groups of them, one level deep.
func TestRuleGroupsAreValidated(t *testing.T) {
	e := newRulesEnv(t)
	a := RuleCondition{Field: FieldSubject, Op: OpContains, Value: "a"}
	many := make([]RuleCondition, MaxRuleConditions)
	for i := range many {
		many[i] = a
	}
	for name, conds := range map[string][]RuleCondition{
		"one group":          {condGroup(MatchAny, a, a)},
		"a group beside one": {condGroup(MatchAny, a, a), a},
		"nested":             {condGroup(MatchAny, condGroup(MatchAll, a, a)), condGroup(MatchAll, a)},
		"empty group":        {{Match: MatchAny, Group: []RuleCondition{}}, condGroup(MatchAll, a)},
		"no match":           {condGroup("", a, a), condGroup(MatchAll, a)},
		"bad match":          {condGroup("some", a, a), condGroup(MatchAll, a)},
		"group with a field": {{Field: FieldSubject, Match: MatchAny, Group: []RuleCondition{a}}, condGroup(MatchAll, a)},
		"match on a row":     {{Field: FieldSubject, Op: OpContains, Value: "a", Match: MatchAny}},
		"too many in groups": {condGroup(MatchAny, many...), condGroup(MatchAll, a)},
	} {
		d := rule(RuleAction{Kind: ActionStar})
		d.Conditions = conds
		_, err := e.s.SaveRule(bg, Rule{Doc: d})
		assert.ErrorIs(t, err, ErrInvalidRule, name)
	}
	d := rule(RuleAction{Kind: ActionStar})
	d.Conditions = []RuleCondition{condGroup(MatchAny, a, a), condGroup(MatchAll, a)}
	_, err := e.s.SaveRule(bg, Rule{Doc: d})
	assert.NoError(t, err)
}

// A build without condition groups reads one as a condition on no field, which
// never matches, so the rule stops running instead of matching all mail.
func TestRuleGroupIsStoredInConditions(t *testing.T) {
	e := newRulesEnv(t)
	d := rule(RuleAction{Kind: ActionStar})
	a := RuleCondition{Field: FieldSubject, Op: OpContains, Value: "a"}
	d.Conditions = []RuleCondition{condGroup(MatchAny, a), condGroup(MatchAll, a)}
	_, err := e.s.SaveRule(bg, Rule{Doc: d})
	require.NoError(t, err)
	assert.Contains(t, store.Str(e.rows(t, "SELECT doc FROM rules")[0][0]),
		`"conditions":[{"field":"","op":"","value":"","match":"any","group":[{"field":"subject","op":"contains","value":"a"}]}`)
}

// What the model and the list say of a rule shows its groups.
func TestRuleDoesNamesGroups(t *testing.T) {
	a := RuleCondition{Field: FieldFrom, Op: OpContains, Value: "a"}
	b := RuleCondition{Field: FieldFrom, Op: OpContains, Value: "b"}
	c := RuleCondition{Field: FieldAttachments, Op: OpIs, Value: RuleYes}
	got := ruleDoes(RuleDoc{Trigger: TriggerReceive, Match: MatchAll, Conditions: []RuleCondition{condGroup(MatchAny, a, b), condGroup(MatchAll, c)},
		Actions: []RuleAction{{Kind: ActionStar}}}, nil, "")
	assert.True(t, strings.Contains(got, `if (the sender contains "a" or the sender contains "b") and it has attachments:`), got)
}
