package webui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReaderSkillHandoffKeepsMailAndConversation(t *testing.T) {
	const name = "email-briefing"
	const instructions = "Describe the orchard plan, keep its dates exactly as written, then answer follow-up questions."
	f := newWitPageFixture(t, func(e *coretest.Env) {
		dir := filepath.Join(e.DataDir, "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		md := "---\nname: " + name + "\ndescription: Discuss this email\nmetadata:\n  godwit-action-on: message\n  godwit-action-output: wit\n  godwit-display-name: 邮件解读\n  godwit-icon: file-text\n---\n" + instructions + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600))
	})
	listed, err := f.p.Skills(f.ctx())
	require.NoError(t, err)
	assert.Contains(t, listed, WitSkill{Name: name, DisplayName: "邮件解读", Icon: "file-text", Description: "Discuss this email"})

	m := f.deliverOne(t, coretest.Msg("lena@partner.example", f.acc.Email, "Orchard plan", "Please review the orchard plan by Friday."))
	f.w.Handoff("/"+name, m)
	hand, err := f.p.TakeAsk(f.ctx())
	require.NoError(t, err)
	require.Len(t, hand.Hits, 1)
	assert.Equal(t, m.ID, hand.Hits[0].ID)
	f.llm.Script(
		coretest.Turn{Calls: []coretest.Call{{Name: "use_skill", Args: `{"name":"email-briefing"}`}}},
		coretest.Turn{Text: []string{"请在周五前审阅计划。"}},
	)
	run, err := f.p.Ask(f.ctx(), 0, hand.Text, []int64{hand.Hits[0].ID}, "", "", "", "")
	require.NoError(t, err)
	f.idle(t, run.Conversation)
	history, answer := f.answer(t, run.Conversation)
	require.Len(t, history.Turns, 1)
	assert.Equal(t, "/email-briefing", history.Turns[0].Question)
	assert.Contains(t, answer.Text, "周五")
	require.NotEmpty(t, answer.Citations)
	assert.Equal(t, m.ID, answer.Citations[0].MessageID)
	requests := f.llm.RequestLog()
	require.Len(t, requests, 2)
	assert.Contains(t, string(requests[0].Messages), "Please review the orchard plan by Friday.")
	assert.Contains(t, string(requests[1].Messages), instructions)

	f.llm.Script(coretest.Turn{Text: []string{"可以先确认参与人员和审阅范围。"}})
	follow, err := f.p.Ask(f.ctx(), run.Conversation, "我应该先确认什么？", nil, "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, run.Conversation, follow.Conversation)
	f.idle(t, follow.Conversation)
	history, answer = f.answer(t, follow.Conversation)
	require.Len(t, history.Turns, 2)
	assert.Contains(t, answer.Text, "参与人员")
}
