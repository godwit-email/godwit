package core

import (
	"strings"
	"testing"

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
	assert.Equal(t, map[string]bool{"secret": true, "code": true, "phone": true, "email": true}, st.MaskCategories)

	m, err := e.s.masker()
	require.NoError(t, err)
	st.MaskCategories["phone"] = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	out, err := m.Text("Call +86 138 1234 5678 or ann@example.com")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(out, "Call +86 138 1234 5678 or {{EMAIL_"), out)
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.False(t, st.MaskCategories["phone"])
	assert.True(t, st.MaskCategories["email"])

	st.MaskCategories["fax"] = false
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
