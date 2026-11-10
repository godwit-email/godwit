package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createSkillCall(t *testing.T, in createSkillArgs) testllm.Call {
	t.Helper()
	args, err := json.Marshal(in)
	require.NoError(t, err)
	return testllm.Call{Name: "create_skill", Args: string(args)}
}

func proposeSkill(t *testing.T, e *approvalEnv, calls ...testllm.Call) WitEvent {
	t.Helper()
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Calls: calls}, testllm.Turn{Text: []string{"Finished."}})
	ch, err := e.s.Ask(t.Context(), conv.ID, "Save these reusable instructions as a skill.")
	require.NoError(t, err)
	evs := drainWit(t, ch)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, len(calls))
	return last
}

func TestCreateSkillNeedsApproval(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "reject", true: "accept"}[accept], func(t *testing.T) {
			e := newApprovalEnv(t)
			in := createSkillArgs{Name: "mail-review", Description: "Review important mail", Instructions: "List decisions and deadlines, without inventing them."}
			path := filepath.Join(e.s.dataDir, "skills", in.Name, "SKILL.md")
			ev := proposeSkill(t, e, createSkillCall(t, in))
			a := ev.Approvals[0]
			assert.False(t, a.SessionAllow)
			assert.Empty(t, a.Destinations)
			assert.Contains(t, string(a.Args), in.Instructions)
			require.NoFileExists(t, path)
			require.ErrorIs(t, e.s.ResolveApprovalForConversation(t.Context(), a.ID), ErrNotGrantable)
			require.NoFileExists(t, path)

			ch, stop := e.s.bus.Subscribe()
			defer stop()
			require.NoError(t, e.s.ResolveApproval(t.Context(), a.ID, accept))
			e.waitRun(t, ev.RunID, "done")
			if !accept {
				require.NoFileExists(t, path)
				assert.Equal(t, "rejected", e.approval(t, a.ID).State)
				return
			}
			assert.Equal(t, "done", e.approval(t, a.ID).State)
			assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM approvals WHERE id = ? AND exec_mode = 'external'", a.ID))
			assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'create_skill' AND decision = 'executed'"))
			require.FileExists(t, path)
			changed := false
			for len(ch) > 0 {
				changed = (<-ch).Type == events.SkillsChanged || changed
			}
			assert.True(t, changed, "保存成功通知已打开的界面")
			sk, err := e.s.skill(t.Context(), in.Name)
			require.NoError(t, err)
			assert.Equal(t, in.Instructions, sk.Body)
			assert.Equal(t, in.Description, sk.Description)
			assert.False(t, sk.Builtin)
			listed, err := e.s.listSkills(t.Context(), nil)
			require.NoError(t, err)
			assert.Contains(t, listed.(map[string]any)["skills"], map[string]string{"name": in.Name, "description": in.Description})
			used, err := e.s.useSkill(t.Context(), json.RawMessage(`{"name":"mail-review"}`))
			require.NoError(t, err)
			assert.Equal(t, in.Instructions, used.(map[string]any)["instructions"])
			require.ErrorIs(t, e.s.ResolveApproval(t.Context(), a.ID, true), ErrInvalidState)
			e.restart(t)
			sk, err = e.s.skill(t.Context(), in.Name)
			require.NoError(t, err)
			assert.Equal(t, in.Instructions, sk.Body, "重启后仍然可用")
		})
	}
}

func TestCreateSkillReaderBinding(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "watch-for", Description: "注意事项: \"日期\"\n和决策", Instructions: "概括邮件。\n---\n列出需要注意的事项。", On: []string{"message"}, Output: "panel"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	require.NoError(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true))
	e.waitRun(t, ev.RunID, "done")
	sk, err := e.s.skill(t.Context(), in.Name)
	require.NoError(t, err)
	assert.Equal(t, in.Description, sk.Description)
	assert.Equal(t, in.Instructions, sk.Body)
	assert.Equal(t, in.On, sk.On)
	assert.Equal(t, in.Output, sk.Output)
	listed, err := e.s.listSkills(t.Context(), nil)
	require.NoError(t, err)
	assert.NotContains(t, listed.(map[string]any)["skills"], map[string]string{"name": in.Name, "description": in.Description}, "UI Skill 保留现有的列表边界")
	e.srv.Script(testllm.Turn{Text: []string{"注意明天的截止日期。"}})
	ch, err := e.s.RunSkill(t.Context(), in.Name, Target{Kind: "message", ID: e.msg})
	require.NoError(t, err)
	evs := drain(ch)
	assert.Equal(t, "done", evs[len(evs)-1].Kind)
	assert.Contains(t, promptOf(t, e.srv.Requests()[len(e.srv.Requests())-1]), in.Instructions)
}

func TestCreateSkillWitBinding(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "mail-analysis", DisplayName: "邮件分析", Icon: "mail", Description: "Analyze mail in Wit", Instructions: "Explain the decisions and help with follow-up questions.", On: []string{"message"}, Output: "wit"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	var approved createSkillArgs
	require.NoError(t, json.Unmarshal(ev.Approvals[0].Args, &approved))
	assert.Equal(t, in, approved, "approval keeps the final display fields and binding")
	assert.False(t, ev.Approvals[0].SessionAllow)
	assert.Empty(t, ev.Approvals[0].Destinations)
	require.NoError(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true))
	e.waitRun(t, ev.RunID, "done")
	sk, err := e.s.skill(t.Context(), in.Name)
	require.NoError(t, err)
	assert.Equal(t, in.DisplayName, sk.DisplayName)
	assert.Equal(t, in.Icon, sk.Icon)
	assert.Equal(t, in.On, sk.On)
	assert.Equal(t, in.Output, sk.Output)
	assert.Equal(t, in.Instructions, sk.Body)
	listed, err := e.s.listSkills(t.Context(), nil)
	require.NoError(t, err)
	assert.Contains(t, listed.(map[string]any)["skills"], map[string]string{"name": in.Name, "description": in.Description})
	used, err := e.s.useSkill(t.Context(), json.RawMessage(`{"name":"mail-analysis"}`))
	require.NoError(t, err)
	assert.Equal(t, in.Instructions, used.(map[string]any)["instructions"])
	requests := len(e.srv.Requests())
	ch, err := e.s.RunSkill(t.Context(), in.Name, Target{Kind: "message", ID: e.msg})
	require.ErrorContains(t, err, "Wit; start /mail-analysis with the message as quoted context")
	assert.Nil(t, ch)
	assert.Len(t, e.srv.Requests(), requests, "RunSkill must not generate a one-shot result")
	require.NoError(t, e.s.SetSkillEnabled(t.Context(), in.Name, false))
	listed, err = e.s.listSkills(t.Context(), nil)
	require.NoError(t, err)
	assert.NotContains(t, listed.(map[string]any)["skills"], map[string]string{"name": in.Name, "description": in.Description})
	_, err = e.s.useSkill(t.Context(), json.RawMessage(`{"name":"mail-analysis"}`))
	require.ErrorIs(t, err, ErrSkillDisabled)
}

func TestCreateSkillDisplayDefaults(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "create-skill", Description: "Create a skill", Instructions: "Instructions"}
	prepared, err := e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
	require.NoError(t, err)
	var approved createSkillArgs
	require.NoError(t, json.Unmarshal(prepared.Args, &approved))
	assert.Empty(t, approved.DisplayName)
	assert.Equal(t, "sparkles", approved.Icon)
	in.DisplayName = strings.Repeat("字", 80)
	_, raw, err := skillDefinition(json.RawMessage(createSkillCall(t, in).Args))
	require.NoError(t, err)
	sk, err := skills.ParseContent(in.Name, raw)
	require.NoError(t, err)
	assert.Equal(t, in.DisplayName, sk.DisplayName, "length is measured in characters")
}

func TestCreateSkillDraftBinding(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "clearer-draft", Description: "Make a draft clearer", Instructions: "Rewrite the draft clearly without adding facts.", On: []string{"draft", "selection"}, Output: "replace-draft"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	require.NoError(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true))
	e.waitRun(t, ev.RunID, "done")
	draftID := e.draft(t)
	before, err := e.s.GetDraft(t.Context(), draftID)
	require.NoError(t, err)
	before.HTML = "<p>The original facts.</p>"
	before, err = e.s.SaveDraft(t.Context(), before)
	require.NoError(t, err)
	e.srv.Script(testllm.Turn{Text: []string{"<p>A clearer draft.</p>"}})
	ch, err := e.s.AssistSkill(t.Context(), draftID, in.Name, SkillOptions{})
	require.NoError(t, err)
	evs := drainWit(t, ch)
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	require.Len(t, last.Approvals, 1)
	assert.Equal(t, "update_draft", last.Approvals[0].Tool)
	after, err := e.s.GetDraft(t.Context(), draftID)
	require.NoError(t, err)
	assert.Equal(t, before.HTML, after.HTML, "新 Skill 的草稿修改仍须单独审批")
}

func TestCreateSkillValidation(t *testing.T) {
	e := newApprovalEnv(t)
	base := createSkillArgs{Name: "safe-name", Description: "Description", Instructions: "Instructions"}
	cases := map[string]func(*createSkillArgs){
		"path":                             func(a *createSkillArgs) { a.Name = "../outside" },
		"absolute":                         func(a *createSkillArgs) { a.Name = "/tmp/outside" },
		"long name":                        func(a *createSkillArgs) { a.Name = strings.Repeat("a", 65) },
		"builtin":                          func(a *createSkillArgs) { a.Name = "summarize" },
		"blank display name":               func(a *createSkillArgs) { a.DisplayName = " " },
		"long display name":                func(a *createSkillArgs) { a.DisplayName = strings.Repeat("字", 81) },
		"multiline display name":           func(a *createSkillArgs) { a.DisplayName = "Mail\nReview" },
		"control display name":             func(a *createSkillArgs) { a.DisplayName = "Mail\tReview" },
		"line separator display name":      func(a *createSkillArgs) { a.DisplayName = "Mail" + string(rune(0x2028)) + "Review" },
		"paragraph separator display name": func(a *createSkillArgs) { a.DisplayName = "Mail" + string(rune(0x2029)) + "Review" },
		"unknown icon":                     func(a *createSkillArgs) { a.Icon = "unknown" },
		"svg icon":                         func(a *createSkillArgs) { a.Icon = "<svg/>" },
		"url icon":                         func(a *createSkillArgs) { a.Icon = "https://example.com/icon.svg" },
		"wit without target":               func(a *createSkillArgs) { a.Output = "wit" },
		"wit thread":                       func(a *createSkillArgs) { a.On, a.Output = []string{"thread"}, "wit" },
		"wit selection":                    func(a *createSkillArgs) { a.On, a.Output = []string{"selection"}, "wit" },
		"wit draft":                        func(a *createSkillArgs) { a.On, a.Output = []string{"draft"}, "wit" },
		"wit multiple targets":             func(a *createSkillArgs) { a.On, a.Output = []string{"message", "thread"}, "wit" },
		"description":                      func(a *createSkillArgs) { a.Description = " " },
		"instructions":                     func(a *createSkillArgs) { a.Instructions = "\n " },
		"too large":                        func(a *createSkillArgs) { a.Instructions = strings.Repeat("a", skills.MaxFileBytes) },
		"target":                           func(a *createSkillArgs) { a.On, a.Output = []string{"unknown"}, "panel" },
		"comma target":                     func(a *createSkillArgs) { a.On, a.Output = []string{"message,thread"}, "panel" },
		"repeat target":                    func(a *createSkillArgs) { a.On, a.Output = []string{"message", "message"}, "panel" },
		"missing output":                   func(a *createSkillArgs) { a.On = []string{"message"} },
		"missing target":                   func(a *createSkillArgs) { a.Output = "panel" },
		"output":                           func(a *createSkillArgs) { a.On, a.Output = []string{"message"}, "send" },
		"draft panel":                      func(a *createSkillArgs) { a.On, a.Output = []string{"draft"}, "panel" },
		"draft body":                       func(a *createSkillArgs) { a.On, a.Output = []string{"draft"}, "replace-body" },
		"message draft":                    func(a *createSkillArgs) { a.On, a.Output = []string{"message"}, "replace-draft" },
		"thread draft":                     func(a *createSkillArgs) { a.On, a.Output = []string{"thread"}, "replace-draft" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			change(&in)
			_, err := e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
			require.Error(t, err)
		})
	}
	require.NoDirExists(t, filepath.Join(e.s.dataDir, "skills"), "校验和提案不写 Skill 文件")
}

func TestCreateSkillDoesNotReplaceExisting(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "keep-me", Description: "New", Instructions: "New instructions"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	path := filepath.Join(e.s.dataDir, "skills", in.Name, "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	const old = "user content, even if not a valid skill"
	require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
	require.ErrorContains(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true), "already exists")
	e.waitRun(t, ev.RunID, "done")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, old, string(got), "审批期间出现的文件也不能覆盖")
	_, err = e.s.prepareCreateSkill(t.Context(), Conversation{}, json.RawMessage(createSkillCall(t, in).Args))
	require.ErrorContains(t, err, "already exists")
}

func TestCreateSkillConcurrentNames(t *testing.T) {
	e := newApprovalEnv(t)
	first := createSkillArgs{Name: "same-name", Description: "One", Instructions: "First"}
	second := createSkillArgs{Name: "same-name", Description: "Two", Instructions: "Second"}
	ev := proposeSkill(t, e, createSkillCall(t, first), createSkillCall(t, second))
	errs := make(chan error, 2)
	for _, a := range ev.Approvals {
		go func() { errs <- e.s.ResolveApproval(t.Context(), a.ID, true) }()
	}
	a, b := <-errs, <-errs
	assert.NotEqual(t, a == nil, b == nil, "两个同名提案只允许一个成功")
	e.waitRun(t, ev.RunID, "done")
	sk, err := e.s.skill(t.Context(), first.Name)
	require.NoError(t, err)
	assert.Contains(t, []string{"First", "Second"}, sk.Body)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM approvals WHERE tool = 'create_skill' AND state = 'done'"))
}

func TestCreateSkillRefusesSymlinks(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "skill", true: "skills directory"}[parent], func(t *testing.T) {
			e := newApprovalEnv(t)
			outside := t.TempDir()
			in := createSkillArgs{Name: "safe-name", Description: "Description", Instructions: "Instructions"}
			ev := proposeSkill(t, e, createSkillCall(t, in))
			path := filepath.Join(e.s.dataDir, "skills")
			if !parent {
				require.NoError(t, os.MkdirAll(path, 0o700))
				path = filepath.Join(path, in.Name)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			require.Error(t, e.s.ResolveApproval(t.Context(), ev.Approvals[0].ID, true))
			e.waitRun(t, ev.RunID, "done")
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestCreateSkillCrashUncertain(t *testing.T) {
	e := newApprovalEnv(t)
	in := createSkillArgs{Name: "saved-once", Description: "Description", Instructions: "Instructions"}
	ev := proposeSkill(t, e, createSkillCall(t, in))
	a := ev.Approvals[0]
	_, err := e.s.db.Execute(t.Context(), `CREATE TRIGGER fail_skill_done BEFORE UPDATE OF state ON approvals WHEN NEW.state = 'done'
		BEGIN SELECT RAISE(ABORT, 'injected commit failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, e.s.ResolveApproval(t.Context(), a.ID, true), "injected commit failure")
	e.waitRun(t, ev.RunID, "interrupted")
	assert.Equal(t, "executing", e.approval(t, a.ID).State)
	path := filepath.Join(e.s.dataDir, "skills", in.Name, "SKILL.md")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), "DROP TRIGGER fail_skill_done")
	require.NoError(t, err)
	e.restart(t)
	e.waitRun(t, ev.RunID, "done")
	got := e.approval(t, a.ID)
	assert.Equal(t, "failed", got.State)
	assert.Equal(t, msgUncertain, got.Error)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, 1, e.count(t, "SELECT COUNT(*) FROM audit_log WHERE tool = 'create_skill' AND decision = 'uncertain'"))
}
