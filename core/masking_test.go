package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 脱敏 is on unless the user turned it off: for settings stored before it
// existed too.
func TestMaskingIsOnByDefault(t *testing.T) {
	e := newApprovalEnv(t)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.True(t, st.Masking)
	_, err = e.s.db.Execute(t.Context(), "UPDATE settings SET value = ? WHERE key = 'app'", `{"Theme":"dark","SettingsVersion":4}`)
	require.NoError(t, err)
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.True(t, st.Masking)

	m, err := e.s.masker()
	require.NoError(t, err)
	st.Masking = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	assert.False(t, m.On())
}

// Codes, phones and addresses are each masked unless turned off, for
// settings stored before they were too; the masker follows the switch.
func TestEachCategoryIsOnUntilTurnedOff(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.db.Execute(t.Context(), "INSERT INTO settings(key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", `{"Theme":"dark","SettingsVersion":4,"Masking":true}`)
	require.NoError(t, err)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, defaultMaskCategories(), st.MaskCategories)

	m, err := e.s.masker()
	require.NoError(t, err)
	st.MaskCategories.Phones.On = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	out, err := m.Text("Call +86 138 1234 5678 or ann@example.com")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "Call +86 138 1234 5678 or {{EMAIL_"), out)
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.False(t, st.MaskCategories.Phones.On)
	assert.True(t, st.MaskCategories.Emails.On)

	st.MaskCategories.Emails.Action = "fax"
	_, err = e.s.SetSettings(t.Context(), st)
	require.Error(t, err)
}

// Wit sends the card as a placeholder, and the user sees the card where the
// model wrote the placeholder: live, in the conversation, and after a
// restart, when the card gets the same placeholder again.
func TestWitMasksAndRestoresAcrossARestart(t *testing.T) {
	const card = "4111 1111 1111 1111"
	e := newApprovalEnv(t)
	m, err := e.s.masker()
	require.NoError(t, err)
	p, err := m.Text(card)
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"It is " + p[:9], p[9:] + "."}})
	var live strings.Builder
	for _, ev := range e.start(t, conv, "Is "+card+" my card?") {
		if ev.Kind == "text" {
			live.WriteString(ev.Text)
		}
	}
	assert.Equal(t, "It is "+card+".", live.String())
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, card)
	assert.Contains(t, body, p)
	shown := func() string {
		c, err := e.s.GetConversation(t.Context(), conv.ID)
		require.NoError(t, err)
		require.Len(t, c.Turns, 1)
		return c.Turns[0].Text
	}
	assert.Equal(t, "It is "+card+".", shown())

	e.restart(t)
	assert.Equal(t, "It is "+card+".", shown())
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "And "+card+"?")
	body = string(e.srv.Requests()[1].Body)
	assert.NotContains(t, body, card)
	assert.Equal(t, 3, strings.Count(body, p), "the question, the answer and the new question")
}

// Apple Intelligence runs on this device, and only it: what it is sent is
// not masked. A plugin's or a service's model is masked, a local one too.
func TestOnlyAppleIntelligenceRunsOnDevice(t *testing.T) {
	e := newSkillEnv(t)
	e.s.opts.AppleIntelligence = &fakeAppleIntelligence{ver: "1"}
	m, _, err := e.s.resolveRef(t.Context(), "plugin/"+AppleIntelligencePlugin+"/on-device")
	require.NoError(t, err)
	assert.True(t, m.OnDevice)
	m, _, err = e.s.resolveRef(t.Context(), "x/m1") // a service on 127.0.0.1
	require.NoError(t, err)
	assert.False(t, m.OnDevice)
}

// What the user sets for 脱敏 holds from the next request on: their words
// and rules go as their own kinds, an allowed value and a category turned
// off go as they are.
func TestMaskingSettingsHoldFromTheNextRequest(t *testing.T) {
	const card, key = "4111 1111 1111 1111", "sk-proj-abcdefghijklmnopqrstuvwx1234"
	e := newApprovalEnv(t)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "Falcon E123456, card "+card+" "+key)
	body := string(e.srv.Requests()[0].Body)
	assert.Contains(t, body, "Falcon E123456, card {{BANK_CARD_")
	assert.NotContains(t, body, card)

	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskWords = []string{"falcon"}
	st.MaskRules = []mask.Rule{{Kind: "EMPLOYEE_ID", Regex: `E\d{6}`}}
	st.MaskAllow = []string{card}
	st.MaskCategories.Secrets.On = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "Falcon E123456, card "+card+" "+key+"?")
	body = string(e.srv.Requests()[1].Body)
	assert.NotContains(t, body, "Falcon E123456, card "+card)
	assert.Contains(t, body, "{{TERM_")
	assert.Contains(t, body, "{{EMPLOYEE_ID_")
	assert.Contains(t, body, " "+card+" "+key+"?")
}

// A rule that can't be used is not saved, and the error says which and why.
func TestAnInvalidMaskingRuleIsNotSaved(t *testing.T) {
	e := newApprovalEnv(t)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskRules = []mask.Rule{{Kind: "OK", Regex: `x\d+`}, {Kind: "BAD", Regex: `(x`}}
	_, err = e.s.SetSettings(t.Context(), st)
	var re *mask.RuleError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, 1, re.Index)
	assert.Contains(t, err.Error(), "missing closing )")
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Empty(t, st.MaskRules)

	st.MaskCategories.IDs.Action = "shout"
	_, err = e.s.SetSettings(t.Context(), st)
	require.Error(t, err)
}

// A rule stored under a built-in kind before they were refused still masks
// and lets the rest of the settings save; a new one is refused.
func TestAStoredRuleKeepsItsBuiltInKind(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.db.Execute(t.Context(), "INSERT INTO settings(key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		`{"SettingsVersion":4,"Masking":true,"MaskRules":[{"Kind":"PHONE","Regex":"ext-\\d+"}]}`)
	require.NoError(t, err)
	m, err := e.s.masker()
	require.NoError(t, err)
	out, err := m.Text("call ext-42")
	require.NoError(t, err)
	assert.NotContains(t, out, "ext-42")
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.Theme = "dark"
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)

	st.MaskRules = append(st.MaskRules, mask.Rule{Kind: "email", Regex: `x\d+`})
	_, err = e.s.SetSettings(t.Context(), st)
	var re *mask.RuleError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, 1, re.Index)
	assert.ErrorIs(t, err, mask.ErrReservedKind)
}

// Names are not masked unless the user turns them on; every other
// category is.
func TestMaskingCategoriesDefaults(t *testing.T) {
	n := 0
	cats := defaultSettings().MaskCategories
	for name, c := range cats.All() {
		assert.Equal(t, name != mask.Names, c.On, name)
		assert.Equal(t, MaskMask, c.Action, name)
		n++
	}
	assert.Equal(t, 8, n)
}

// The preview masks with the settings it is given and stores nothing: not
// the settings, not the masking key.
func TestMaskingPreviewStoresNothing(t *testing.T) {
	e := newApprovalEnv(t)
	rows := func() string {
		r, err := e.s.db.Query(t.Context(), "SELECT key, value FROM settings ORDER BY key")
		require.NoError(t, err)
		return fmt.Sprint(r.Values)
	}
	before := rows()
	st := defaultSettings()
	st.MaskWords = []string{"Falcon"}
	out, err := e.s.PreviewMasking(t.Context(), st, "Project Falcon, card 4111 1111 1111 1111")
	require.NoError(t, err)
	assert.Regexp(t, `^Project \{\{TERM_[a-z2-7]{12}\}\}, card \{\{BANK_CARD_[a-z2-7]{12}\}\}$`, out)
	assert.Equal(t, before, rows())

	st.MaskRules = []mask.Rule{{Kind: "BAD", Regex: `(`}}
	_, err = e.s.PreviewMasking(t.Context(), st, "x")
	require.Error(t, err)
	st.Masking, st.MaskRules = false, nil
	out, err = e.s.PreviewMasking(t.Context(), st, "Falcon")
	require.NoError(t, err)
	assert.Equal(t, "Falcon", out, "masking off sends it as it is")
}

// setMaskAction sets what is done with category's values.
func setMaskAction(t *testing.T, s *Service, actions map[string]string) {
	t.Helper()
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	for name, c := range st.MaskCategories.All() {
		if a, ok := actions[name]; ok {
			c.Action = a
		}
	}
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
}

// 仅警告 sends the address as it is: the turn says how many values it
// masked and that one went unmasked, and the record has both, never a value.
func TestWarnedValuesGoAsTheyAreAndAreShown(t *testing.T) {
	const card, addr = "4111 1111 1111 1111", "ann@example.com"
	e := newApprovalEnv(t)
	setMaskAction(t, e.s, map[string]string{mask.Emails: MaskWarn})
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "Is "+card+" "+addr+"'s card?")
	body := string(e.srv.Requests()[0].Body)
	assert.Contains(t, body, addr)
	assert.NotContains(t, body, card)

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, c.Turns, 1)
	assert.Equal(t, 1, c.Turns[0].Masked)
	assert.Equal(t, 1, c.Turns[0].Warned)

	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.Len(t, log, 2)
	for _, r := range log {
		assert.Equal(t, "wit", r.Feature)
		assert.WithinDuration(t, time.Now(), r.At, time.Minute)
	}
	assert.Equal(t, []MaskRecord{
		{Feature: "wit", Kind: "BANK_CARD", Category: mask.IDs, First: "4", Len: 19, Action: MaskMask, Source: MaskSourceRule, At: log[0].At},
		{Feature: "wit", Kind: "EMAIL", Category: mask.Emails, First: "", Len: 3, Action: MaskWarn, Source: MaskSourceRule, At: log[1].At}, // short: no first character
	}, []MaskRecord{withoutRequest(log[1]), withoutRequest(log[0])}, "newest first")
	assert.NotZero(t, log[0].Request)
	assert.Equal(t, log[0].Request, log[1].Request, "one request")
	rows, err := e.s.db.Query(t.Context(), "SELECT * FROM mask_log")
	require.NoError(t, err)
	assert.NotContains(t, fmt.Sprint(rows.Values), "4111")
	assert.NotContains(t, fmt.Sprint(rows.Values), "ann")
}

// 拦截 sends nothing: Wit fails naming the category, and the request is
// recorded as blocked.
func TestABlockedWitQuestionIsNotSent(t *testing.T) {
	e := newApprovalEnv(t)
	setMaskAction(t, e.s, map[string]string{mask.IDs: MaskBlock})
	conv := e.newConv(t, "ask", 0)
	evs := e.start(t, conv, "Is 4111 1111 1111 1111 my card?")
	last := evs[len(evs)-1]
	require.Equal(t, "error", last.Kind)
	assert.Equal(t, ai.CodeBlocked, last.Err.Code)
	assert.Equal(t, []string{mask.IDs}, MaskBlocked(last.Err))
	assert.Empty(t, e.srv.Requests())
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, MaskBlock, log[0].Action)
}

// The record keeps 30 days.
func TestTheMaskingLogKeepsThirtyDays(t *testing.T) {
	e := newApprovalEnv(t)
	m, err := e.s.masker()
	require.NoError(t, err)
	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	_, err = e.s.db.Execute(t.Context(), "INSERT INTO mask_log(at, feature, kind, category, first, len, action) VALUES (?, 'wit', 'PHONE', 'phones', '1', 11, 'mask')", old)
	require.NoError(t, err)
	_, err = m.Request(mask.Tag(t.Context(), "summary", nil), "card 4111 1111 1111 1111")
	require.NoError(t, err)
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, "summary", log[0].Feature)
}

func withoutRequest(r MaskRecord) MaskRecord { r.Request = 0; return r }

// Each request's values share a number of their own, and 清空记录 deletes
// them all.
func TestTheMaskingLogTellsRequestsApart(t *testing.T) {
	e := newApprovalEnv(t)
	m, err := e.s.masker()
	require.NoError(t, err)
	ctx := mask.Tag(t.Context(), "summary", nil)
	_, err = m.Request(ctx, "card 4111 1111 1111 1111, mail ann.lee@example.com")
	require.NoError(t, err)
	_, err = m.Request(ctx, "card 4111 1111 1111 1111")
	require.NoError(t, err)
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.Len(t, log, 3)
	assert.Equal(t, log[1].Request, log[2].Request, "the first request's two values")
	assert.Greater(t, log[0].Request, log[1].Request, "the second request")

	require.NoError(t, e.s.ClearMaskingLog(t.Context()))
	log, err = e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	assert.Empty(t, log)
}
