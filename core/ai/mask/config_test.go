package mask_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
)

func configured(t *testing.T, c mask.Config) *mask.Masker {
	t.Helper()
	m := mask.New(key)
	require.NoError(t, m.Configure(c))
	return m
}

func masked(t *testing.T, m *mask.Masker, s string) string {
	t.Helper()
	out, err := m.Text(s)
	require.NoError(t, err)
	assert.Equal(t, s, m.Restore(out), "restores")
	return shown(out)
}

// A category turned off is sent as it is; the others are still masked.
func TestACategoryTurnedOffIsNotMasked(t *testing.T) {
	m := configured(t, mask.Config{Off: []string{mask.IDs}})
	assert.Equal(t, "card "+card+", key <API_KEY>", masked(t, m, "card "+card+", key "+apiKey))
	require.NoError(t, m.Configure(mask.Config{}))
	assert.Equal(t, "card <BANK_CARD>, key <API_KEY>", masked(t, m, "card "+card+", key "+apiKey), "the next text goes as the new settings say")
}

// Every kind the rules find is in a category the user can turn off.
func TestEveryKindHasACategory(t *testing.T) {
	for _, k := range []string{"PRIVATE_KEY", "API_KEY", "TOKEN", "PASSWORD", "SECRET"} {
		assert.Equal(t, mask.Secrets, mask.Category(k), k)
	}
	for _, k := range []string{"ID_CARD", "BANK_CARD", "PASSPORT", "IBAN"} {
		assert.Equal(t, mask.IDs, mask.Category(k), k)
	}
	assert.Equal(t, mask.Names, mask.Category("NAME"))
	assert.Equal(t, "", mask.Category("EMPLOYEE_ID"), "the user's own")
}

// A value on the allowlist is never masked, whatever finds it.
func TestAnAllowedValueIsNotMasked(t *testing.T) {
	m := configured(t, mask.Config{Allow: []string{card, "acme"}, Words: []string{"Acme", "Zenith"}})
	assert.Equal(t, "card "+card+", ACME and <TERM>", masked(t, m, "card "+card+", ACME and Zenith"))
}

// The user's words go as TERM: in any case unless asked, whole words only if asked.
func TestMasksTheUsersWords(t *testing.T) {
	for _, c := range []struct {
		cfg      mask.Config
		in, want string
	}{
		{mask.Config{Words: []string{"Project Falcon"}}, "re: project falcon, Project Falcon!", "re: <TERM>, <TERM>!"},
		{mask.Config{Words: []string{"Falcon"}, WordsCase: true}, "falcon and Falcon", "falcon and <TERM>"},
		{mask.Config{Words: []string{"cat"}}, "concatenate the cat", "con<TERM>enate the <TERM>"},
		{mask.Config{Words: []string{"cat"}, WordsWhole: true}, "concatenate the cat_x cat.", "concatenate the cat_x <TERM>."},
		{mask.Config{Words: []string{"猎鹰"}, WordsWhole: true}, "代号猎鹰计划", "代号<TERM>计划"},
		{mask.Config{Words: []string{"Falcon", "Falcon Nine"}}, "Falcon Nine", "<TERM>"},
	} {
		assert.Equal(t, c.want, masked(t, configured(t, c.cfg), c.in), "%+v", c.cfg)
	}
}

// A rule of the user's masks what it matches (its first group, if it has
// one) as its own kind, before the built-in rules do.
func TestMasksByTheUsersRules(t *testing.T) {
	m := configured(t, mask.Config{Rules: []mask.Rule{
		{Kind: "EMPLOYEE_ID", Regex: `E\d{6}`},
		{Kind: "ORDER", Regex: `order #(\d{8})`},
		{Kind: "CARD_HOLDER", Regex: `4111 1111 1111 1111`},
	}})
	assert.Equal(t, "by <EMPLOYEE_ID>, order #<ORDER>, <CARD_HOLDER>", masked(t, m, "by E123456, order #12345678, "+card))
}

// A rule that can't be used is refused, saying why; the masker keeps the
// settings it had.
func TestRefusesARuleThatCannotBeUsed(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"Zenith"}})
	many := make([]mask.Rule, mask.MaxRules+1)
	for i := range many {
		many[i] = mask.Rule{Kind: "R", Regex: fmt.Sprintf("x%dy", i)}
	}
	for _, c := range []struct {
		rules []mask.Rule
		why   string
	}{
		{[]mask.Rule{{Kind: "BAD", Regex: `(unclosed`}}, "missing closing )"},
		{[]mask.Rule{{Kind: "BAD", Regex: `a{2,1}`}}, "invalid repeat count"},
		{[]mask.Rule{{Kind: "BAD", Regex: `(?=x)y`}}, "invalid or unsupported Perl syntax"},
		{[]mask.Rule{{Kind: "EVERY", Regex: `x*`}}, "matches empty text"},
		{[]mask.Rule{{Kind: "", Regex: `x`}}, "kind"},
		{[]mask.Rule{{Kind: "员工", Regex: `x`}}, "kind"},
		{many, "at most 32"},
	} {
		err := m.Configure(mask.Config{Rules: c.rules})
		require.Error(t, err)
		assert.Contains(t, err.Error(), c.why)
		var re *mask.RuleError
		if c.why != "at most 32" {
			require.ErrorAs(t, err, &re)
			assert.Equal(t, 0, re.Index)
		}
	}
	assert.Equal(t, "<TERM>", masked(t, m, "Zenith"), "the last settings stay")
	assert.Error(t, m.Configure(mask.Config{Allow: make([]string, mask.MaxAllow+1)}))
}

// A rule's kind is written as a placeholder's: upper case, and ok in lower.
func TestARulesKindIsUpperCased(t *testing.T) {
	m := configured(t, mask.Config{Rules: []mask.Rule{{Kind: " employee_id ", Regex: `E\d{6}`}}})
	assert.Equal(t, "<EMPLOYEE_ID>", masked(t, m, "E123456"))
}

// Names follow their category, and an allowed display name, a brand's
// most often, is sent as it is.
func TestNamesFollowTheSettings(t *testing.T) {
	const mail = "From: Zhang Wei <zw@example.com>\nTo: GitHub <noreply@github.com>\n\nZhang Wei, GitHub says hi"
	m := configured(t, mask.Config{Off: []string{mask.Names, mask.Emails}})
	assert.Equal(t, mail, masked(t, m, mail))
	require.NoError(t, m.Configure(mask.Config{Off: []string{mask.Emails}, Allow: []string{"github"}}))
	assert.Equal(t, "From: <NAME> <zw@example.com>\nTo: GitHub <noreply@github.com>\n\n<NAME>, GitHub says hi", masked(t, m, mail))
}

// A word that matches part of a placeholder the model wrote back leaves it
// whole: it still restores, and a tool may take it.
func TestAWordLeavesAPlaceholderWhole(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"email"}})
	p, err := m.Text("alice@example.com")
	require.NoError(t, err)
	out, err := m.Body(`{"messages":[{"role":"assistant","content":"sent to ` + p + `"}]}`)
	require.NoError(t, err)
	assert.Contains(t, out, `"sent to `+p+`"`)
	args, err := m.RestoreArgs([]byte(`{"to":"` + p + `"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"to":"alice@example.com"}`, string(args))
}

// A bare placeholder of a word or of the user's rule that names no value is
// left over: a tool does not take it, and it is not mailed.
func TestAnUnknownTermPlaceholderIsLeft(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"acme"}, Rules: []mask.Rule{{Kind: "ORDER", Regex: `ORD-\d+`}}})
	for _, p := range []string{"TERM_aaaaaaaaaaaa", "ORDER_aaaaaaaaaaaa"} {
		assert.True(t, m.Left(p), p)
		_, err := m.RestoreArgs([]byte(`{"q":"` + p + `"}`))
		assert.Error(t, err, p)
	}
	assert.False(t, m.Left("report_finalversion"))
}

// A placeholder of a word, the user's rule or a name the stream cuts is
// held back like a built-in one, and restores whole.
func TestAStreamHoldsBackAWordsPlaceholder(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"acme"}})
	p, err := m.Text("acme")
	require.NoError(t, err)
	bare := strings.Trim(p, "{}")
	assert.Equal(t, 0, m.Cut(bare[:8]), "a start of TERM_… is held back")
	assert.Equal(t, "acme", m.Restore(bare))
}

// A value found by its label masks without it afterwards: a text cut after
// "password:" does not send what follows.
func TestALabeledSecretIsKnownWithoutItsLabel(t *testing.T) {
	m := mask.New(key)
	_, err := m.Text("password: abcd1234efgh5678")
	require.NoError(t, err)
	out, err := m.Text("abcd1234efgh5678")
	require.NoError(t, err)
	assert.NotContains(t, out, "abcd1234efgh5678")
	assert.Equal(t, "abcd1234efgh5678", m.Restore(out))
}

// The user's rule masks what it finds even under a built-in kind whose
// category is off or set to 仅警告.
func TestAUserRuleIgnoresItsKindsCategory(t *testing.T) {
	rules := []mask.Rule{{Kind: "EMAIL", Regex: `customer-[0-9]+`}}
	for _, c := range []mask.Config{{Off: []string{mask.Emails}, Rules: rules}, {Actions: map[string]string{mask.Emails: mask.Warn}, Rules: rules}} {
		assert.Equal(t, "id <EMAIL>", masked(t, configured(t, c), "id customer-1234"))
	}
}
