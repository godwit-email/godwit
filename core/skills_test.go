package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func skillService(t *testing.T) (*core.Service, string) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	s, err := core.New(ctx, core.Options{DataDir: dir})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s, filepath.Join(dir, "skills")
}

func writeSkill(t *testing.T, root, name, md string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(md), 0o644))
}

func find(infos []core.SkillInfo, name string) (core.SkillInfo, bool) {
	for _, i := range infos {
		if i.Name == name {
			return i, true
		}
	}
	return core.SkillInfo{}, false
}

func TestUserSkillOverridesBuiltin(t *testing.T) {
	s, root := skillService(t)
	writeSkill(t, root, "summarize", "---\nname: summarize\ndescription: mine\n---\nmy body")
	writeSkill(t, root, "plain", "---\nname: plain\ndescription: agent only\n---\nx")
	infos, err := s.ListSkills(ctx)
	require.NoError(t, err)
	i, ok := find(infos, "summarize")
	require.True(t, ok)
	assert.Equal(t, "mine", i.Description)
	assert.False(t, i.Builtin)
	assert.False(t, i.UI)
	p, _ := find(infos, "plain")
	assert.False(t, p.UI)
	tr, _ := find(infos, "translate")
	assert.True(t, tr.UI)
	assert.True(t, tr.Builtin)
	assert.True(t, tr.Enabled)
}

func TestBrokenUserSkillListed(t *testing.T) {
	s, root := skillService(t)
	writeSkill(t, root, "broken", "no front matter")
	infos, err := s.ListSkills(ctx)
	require.NoError(t, err)
	i, ok := find(infos, "broken")
	require.True(t, ok)
	assert.NotEmpty(t, i.Error)
	assert.Len(t, infos, 9)
}

func TestSetSkillEnabled(t *testing.T) {
	s, _ := skillService(t)
	require.NoError(t, s.SetSkillEnabled(ctx, "summarize", false))
	infos, _ := s.ListSkills(ctx)
	i, _ := find(infos, "summarize")
	assert.False(t, i.Enabled)
	require.NoError(t, s.SetSkillEnabled(ctx, "summarize", false)) // idempotent
	require.ErrorIs(t, s.SetSkillEnabled(ctx, "nope", false), core.ErrSkillNotFound)
	require.NoError(t, s.SetSkillEnabled(ctx, "summarize", true))
	infos, _ = s.ListSkills(ctx)
	i, _ = find(infos, "summarize")
	assert.True(t, i.Enabled)
}
