package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateSkillRejectsMissingUIEntry(t *testing.T) {
	e := newApprovalEnv(t)
	for _, binding := range []struct {
		on     []string
		output string
	}{
		{[]string{"draft"}, "replace-draft"},
		{[]string{"selection"}, "replace-draft"},
		{[]string{"selection"}, "replace-body"},
		{[]string{"selection"}, "panel"},
		{[]string{"thread"}, "panel"},
		{[]string{"message"}, "replace-body"},
	} {
		t.Run(fmt.Sprint(binding.on, binding.output), func(t *testing.T) {
			in := createSkillArgs{Name: "new-skill", Description: "Reusable instructions", Instructions: "Do the requested analysis.", On: binding.on, Output: binding.output}
			_, err := e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
			require.Error(t, err, "创建工具只应提供已有产品入口能运行的绑定")
		})
	}
	require.NoDirExists(t, filepath.Join(e.s.dataDir, "skills"))
}

func TestWitSkillRejectsIgnoredModelConfig(t *testing.T) {
	for _, metadata := range []string{"godwit-model: local/private", "godwit-model-tier: small"} {
		t.Run(metadata, func(t *testing.T) {
			raw := []byte("---\nname: private-analysis\ndescription: Analyze privately\nmetadata:\n  godwit-action-on: message\n  godwit-action-output: wit\n  " + metadata + "\n---\nAnalyze the quoted mail.\n")
			_, err := skills.ParseContent("private-analysis", raw)
			require.Error(t, err, "不能加载一个会静默丢弃模型选择的 Wit Skill")
		})
	}
}

func TestWitSkillCommandRejectsInvalidModelBeforeEgress(t *testing.T) {
	e := newApprovalEnv(t)
	dir := filepath.Join(e.s.dataDir, "skills", "private-analysis")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	raw := "---\nname: private-analysis\ndescription: Analyze privately\nmetadata:\n  godwit-action-on: message\n  godwit-action-output: wit\n  godwit-model: local/private\n---\nAnalyze the quoted mail.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0o600))
	conv := e.newConv(t, "ask", 0)
	ch, err := e.s.AskWith(t.Context(), conv.ID, "/private-analysis", []int64{e.msg})
	if ch != nil {
		drainWit(t, ch)
	}
	require.Error(t, err, "即使菜单缓存了旧配置，也不能先把引用发给默认模型再发现 Skill 无效")
	assert.Empty(t, e.srv.Requests())
}

func TestWitSkillCommandRejectsDeletedBeforeEgress(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "removed-skill", Description: "Read the quoted mail", Instructions: "Explain the mail.", On: []string{"message"}, Output: "wit"}
	created := proposeSkill(t, e, createSkillCall(t, in))
	require.NoError(t, e.s.ResolveApproval(t.Context(), created.Approvals[0].ID, true))
	e.waitRun(t, created.RunID, "done")
	dir := filepath.Join(e.s.dataDir, "skills", in.Name)
	require.FileExists(t, filepath.Join(dir, "SKILL.md"))
	require.NoError(t, os.RemoveAll(dir))
	requests := len(e.srv.Requests())
	conv := e.newConv(t, "ask", 0)
	ch, err := e.s.AskWith(t.Context(), conv.ID, "/removed-skill", []int64{e.msg})
	if ch != nil {
		drainWit(t, ch)
	}
	assert.Len(t, e.srv.Requests(), requests, "旧菜单指向已删除 Skill 时也不能先外发引用")
	require.ErrorIs(t, err, ErrSkillNotFound)
}

func TestCreateSkillReusesDeletedDisabledName(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "reusable-name", Description: "Reusable instructions", Instructions: "The first version."}
	first := proposeSkill(t, e, createSkillCall(t, in))
	require.NoError(t, e.s.ResolveApproval(t.Context(), first.Approvals[0].ID, true))
	e.waitRun(t, first.RunID, "done")
	require.NoError(t, e.s.SetSkillEnabled(t.Context(), in.Name, false))
	dir := filepath.Join(e.s.dataDir, "skills", in.Name)
	require.FileExists(t, filepath.Join(dir, "SKILL.md"))
	require.NoError(t, os.RemoveAll(dir))
	in.Instructions = "The replacement version."
	_, err := e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
	require.NoError(t, err, "删除的目录不能被残留的禁用记录永久占名")
	second := proposeSkill(t, e, createSkillCall(t, in))
	require.NoError(t, e.s.ResolveApproval(t.Context(), second.Approvals[0].ID, true))
	e.waitRun(t, second.RunID, "done")
	sk, err := e.s.skill(t.Context(), in.Name)
	require.NoError(t, err, "重新创建的 Skill 应可使用")
	assert.Equal(t, in.Instructions, sk.Body)
}

func TestSkillDisplayNameNormalization(t *testing.T) {
	for input, want := range map[string]string{
		" ":              "",
		"  邮件解读  ":       "邮件解读",
		"Mail\n\tReview": "Mail Review",
	} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			raw := []byte(fmt.Sprintf("---\nname: email-briefing\ndescription: Explain mail\nmetadata:\n  godwit-display-name: %q\n---\nExplain the mail.\n", input))
			sk, err := skills.ParseContent("email-briefing", raw)
			require.NoError(t, err)
			assert.Equal(t, want, sk.DisplayName)
			label := want
			if label == "" {
				label = "Email Briefing"
			}
			assert.Equal(t, label, skills.Label(sk.Name, input))
		})
	}
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "mail-review", DisplayName: "  邮件解读  ", Description: "Explain mail", Instructions: "Explain the mail."}
	prepared, err := e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
	require.NoError(t, err)
	var final createSkillArgs
	require.NoError(t, json.Unmarshal(prepared.Args, &final))
	assert.Equal(t, "邮件解读", final.DisplayName, "审批和持久化应使用同一个规范值")
}

func TestSkillPublicationKeepsIncompleteStateHidden(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "publish-later", Description: "Publish atomically", Instructions: "Instructions"}
	_, raw, err := skillDefinition(json.RawMessage(createSkillCall(t, in).Args))
	require.NoError(t, err)
	original := syncSkillDir
	syncSkillDir = func(root *os.Root, name string) error {
		_, err := root.Stat("skills/publish-later")
		assert.ErrorIs(t, err, os.ErrNotExist, "完成暂存目录持久化之前不能暴露最终名称")
		loaded, failed, err := e.s.loadSkills()
		assert.NoError(t, err)
		assert.NotContains(t, loaded, in.Name)
		assert.Empty(t, failed, "暂存目录不应成为损坏的 Skill")
		return errors.New("injected before publication")
	}
	t.Cleanup(func() { syncSkillDir = original })
	require.ErrorContains(t, e.s.saveNewSkill(t.Context(), in.Name, raw), "injected before publication")
	require.NoDirExists(t, filepath.Join(e.s.dataDir, "skills", in.Name))
	syncSkillDir = original
	require.NoError(t, e.s.saveNewSkill(t.Context(), in.Name, raw), "未发布的失败不能永久占用名称")
	require.FileExists(t, filepath.Join(e.s.dataDir, "skills", in.Name, "SKILL.md"))
}

func TestCreateSkillSyncFailureIsUncertain(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "published-skill", Description: "Published before sync fails", Instructions: "Instructions"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	original := syncSkillDir
	syncSkillDir = func(root *os.Root, name string) error {
		if _, err := root.Stat("skills/published-skill/SKILL.md"); err == nil {
			return errors.New("injected directory sync failure")
		}
		return original(root, name)
	}
	t.Cleanup(func() { syncSkillDir = original })
	changed, stop := e.s.bus.Subscribe()
	defer stop()
	require.ErrorContains(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true), "injected directory sync failure")
	e.waitRun(t, ev.RunID, "done")
	require.FileExists(t, filepath.Join(e.s.dataDir, "skills", in.Name, "SKILL.md"))
	approval := e.approval(t, ev.Approvals[0].ID)
	assert.Equal(t, "failed", approval.State)
	assert.Equal(t, msgUncertain, approval.Error)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'create_skill' AND decision = 'uncertain'"))
	seen := false
	for len(changed) > 0 {
		seen = (<-changed).Type == events.SkillsChanged || seen
	}
	assert.True(t, seen, "已经发布的文件即使同步失败也必须通知界面刷新")
}
