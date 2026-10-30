package core

import (
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	modelAddr = "上海市徐汇区漕溪北路 88 号 1203 室"
	modelUser = "octocat42"
)

// addressesAndAccounts is a fake 识别模型 that finds modelAddr and modelUser.
func addressesAndAccounts() *fakeModel {
	return newFakeModel(map[string]string{modelAddr: "private_address", modelUser: "account"})
}

// With 识别模型 on, Wit sends the addresses and the user names it finds as
// placeholders of their own.
func TestWitMasksTheAddressesAndAccountsTheModelFinds(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, addressesAndAccounts())
	setMaskModel(t, e.s, true)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "寄到"+modelAddr+"，GitHub 上是 "+modelUser)
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, "漕溪北路")
	assert.NotContains(t, body, modelUser)
	assert.Regexp(t, `寄到\{\{ADDRESS_[a-z2-7]{12}\}\}，GitHub 上是 \{\{ACCOUNT_[a-z2-7]{12}\}\}`, body)
}

// 仅警告 sends an address as it is and counts it; off, an account goes
// as it is, uncounted.
func TestAddressesWarnAndAccountsTurnOff(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, addressesAndAccounts())
	setMaskModel(t, e.s, true)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskCategories.Addresses.Action = MaskWarn
	st.MaskCategories.Accounts.On = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "寄到"+modelAddr+"，GitHub 上是 "+modelUser)
	body := string(e.srv.Requests()[0].Body)
	assert.Contains(t, body, "寄到"+modelAddr+"，GitHub 上是 "+modelUser)

	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Len(t, c.Turns, 1)
	assert.Equal(t, 0, c.Turns[0].Masked)
	assert.Equal(t, 1, c.Turns[0].Warned)
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	require.Len(t, log, 1)
	assert.Equal(t, []string{"ADDRESS", mask.Addresses, MaskWarn}, []string{log[0].Kind, log[0].Category, log[0].Action})
}

// 拦截 sends nothing: Wit fails naming 账号, and a background judge, whose
// mail the labeler then skips, is refused the same way.
func TestABlockedAccountIsNotSent(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, addressesAndAccounts())
	setMaskModel(t, e.s, true)
	setMaskAction(t, e.s, map[string]string{mask.Accounts: MaskBlock})
	conv := e.newConv(t, "ask", 0)
	evs := e.start(t, conv, "GitHub 上是 "+modelUser)
	last := evs[len(evs)-1]
	require.Equal(t, "error", last.Kind)
	assert.Equal(t, ai.CodeBlocked, last.Err.Code)
	assert.Equal(t, []string{mask.Accounts}, MaskBlocked(last.Err))
	assert.Empty(t, e.srv.Requests())

	s := newMailContextService(t)
	installMaskModel(t, s, addressesAndAccounts())
	setMaskModel(t, s, true)
	setMaskAction(t, s, map[string]string{mask.Accounts: MaskBlock})
	record := usePluginJudge(t, s, nil, nil)
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), modelUser+" 的账单", []ai.Question{{Name: "bill", Type: ai.Predicate, Instructions: "Is it a bill?"}})
	assert.Equal(t, []string{mask.Accounts}, MaskBlocked(err))
	assert.True(t, unanswerable(err), "the labeler skips the mail")
	assert.Empty(t, recorded(t, record, "models.judge"))
}

// Addresses and accounts are on for settings stored before they were,
// whatever the user set the other categories to.
func TestAddressesAndAccountsAreOnAfterAnUpgrade(t *testing.T) {
	e := newApprovalEnv(t)
	_, err := e.s.db.Execute(t.Context(), "INSERT INTO settings(key, value) VALUES ('app', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		`{"SettingsVersion":4,"Masking":true,"MaskCategories":{"Secrets":{"On":true,"Action":"mask"},"IDs":{"On":true,"Action":"block"},"Codes":{"On":false,"Action":"mask"},"Phones":{"On":true,"Action":"warn"},"Emails":{"On":true,"Action":"mask"},"Names":{"On":false,"Action":"mask"}}}`)
	require.NoError(t, err)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, MaskCategory{On: true, Action: MaskMask}, st.MaskCategories.Addresses)
	assert.Equal(t, MaskCategory{On: true, Action: MaskMask}, st.MaskCategories.Accounts)
	assert.Equal(t, MaskCategory{On: true, Action: MaskBlock}, st.MaskCategories.IDs, "the rest as stored")
	assert.False(t, st.MaskCategories.Codes.On)

	installMaskModel(t, e.s, addressesAndAccounts())
	setMaskModel(t, e.s, true)
	out, err := e.s.PreviewMasking(t.Context(), st, modelAddr+" "+modelUser)
	require.NoError(t, err)
	assert.Equal(t, modelAddr+" "+modelUser, out, "the settings given have the model off")
	st.MaskModel = true
	out, err = e.s.PreviewMasking(t.Context(), st, modelAddr+" "+modelUser)
	require.NoError(t, err)
	assert.Regexp(t, `^\{\{ADDRESS_[a-z2-7]{12}\}\} \{\{ACCOUNT_[a-z2-7]{12}\}\}$`, out)
}
