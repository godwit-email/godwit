package skills_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/godwit-email/godwit/core/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const good = "---\nname: demo\ndescription: Does a thing\nmetadata:\n  godwit-action-on: \"message, selection\"\n  godwit-action-output: panel\n  godwit-model: fast/small\n---\n\nBody text.\n"

func mapFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestParseSkill(t *testing.T) {
	s, warns, err := skills.Parse("demo", mapFS(map[string]string{"SKILL.md": good}))
	require.NoError(t, err)
	assert.Empty(t, warns)
	assert.Equal(t, "demo", s.Name)
	assert.Equal(t, "Does a thing", s.Description)
	assert.Equal(t, "Body text.", s.Body)
	assert.Equal(t, []string{"message", "selection"}, s.On)
	assert.Equal(t, "panel", s.Output)
	assert.Equal(t, "fast/small", s.Model)
	assert.Len(t, s.Version, 16)

	plain, _, err := skills.Parse("plain", mapFS(map[string]string{"SKILL.md": "---\nname: plain\ndescription: x\n---\nhi"}))
	require.NoError(t, err)
	assert.Empty(t, plain.On)
}

func TestParseRejects(t *testing.T) {
	for name, c := range map[string]struct{ dir, md string }{
		"name mismatch": {"demo", strings.Replace(good, "name: demo", "name: other", 1)},
		"upper case":    {"Demo", strings.Replace(good, "name: demo", "name: Demo", 1)},
		"no desc":       {"demo", "---\nname: demo\ndescription: \"\"\n---\n"},
		"bad on":        {"demo", strings.Replace(good, "selection", "everything", 1)},
		"bad output":    {"demo", strings.Replace(good, "panel", "popup", 1)},
		"on only":       {"demo", "---\nname: demo\ndescription: x\nmetadata:\n  godwit-action-on: draft\n---\n"},
		"no front":      {"demo", "just text"},
		"bad yaml":      {"demo", "---\nname: [\n---\n"},
	} {
		_, _, err := skills.Parse(c.dir, mapFS(map[string]string{"SKILL.md": c.md}))
		assert.Error(t, err, name)
	}
	_, _, err := skills.Parse("demo", mapFS(nil))
	assert.Error(t, err)
}

func TestScriptsWarnOnly(t *testing.T) {
	_, warns, err := skills.Parse("demo", mapFS(map[string]string{"SKILL.md": good, "scripts/run.sh": "echo"}))
	require.NoError(t, err)
	assert.Len(t, warns, 1)
}

func userSkill(t *testing.T, dir, name string) skills.Skill {
	t.Helper()
	ok, failed, err := skills.LoadDir(dir)
	require.NoError(t, err)
	require.Empty(t, failed)
	for _, s := range ok {
		if s.Name == name {
			return s
		}
	}
	t.Fatal("skill not loaded")
	return skills.Skill{}
}

func TestSkillReadFile(t *testing.T) {
	dir := t.TempDir()
	sk := filepath.Join(dir, "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(sk, "references"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(sk, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "other/references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other/SKILL.md"), []byte(strings.ReplaceAll(good, "demo", "other")), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other/references/o.md"), []byte("other"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sk, "SKILL.md"), []byte(good), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sk, "references/a.md"), []byte("ref"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sk, "references/big.md"), make([]byte, skills.MaxFileBytes+1), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sk, "scripts/run.sh"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x"), []byte("secret"), 0o644))
	links := map[string]string{
		"references/out.md":    filepath.Join(dir, "x"),
		"references/other.md":  "../../other/references/o.md",
		"references/script.sh": "../scripts/run.sh",
		"references/self.md":   "a.md", // even a link inside references/ is refused
	}
	linked := true
	for l, target := range links {
		if os.Symlink(target, filepath.Join(sk, l)) != nil {
			linked = false
		}
	}
	s := userSkill(t, dir, "demo")

	b, err := s.ReadFile("references/a.md") // still works after loading finished
	require.NoError(t, err)
	assert.Equal(t, "ref", string(b))
	for _, p := range []string{"../x", "references/../../x", "/etc/passwd", "scripts/run.sh", "SKILL.md", "references/big.md", "references"} {
		_, err := s.ReadFile(p)
		assert.Error(t, err, p)
	}
	if linked {
		for l := range links {
			_, err := s.ReadFile(l)
			assert.Error(t, err, l)
		}
	} else {
		t.Log("symlink creation not permitted; skipping link sub-tests")
	}
}

func TestLoadDirErrors(t *testing.T) {
	_, _, err := skills.LoadDir(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	f := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(f, nil, 0o644))
	_, _, err = skills.LoadDir(f) // not a directory
	require.Error(t, err)
}

func TestBuiltinSkillsValid(t *testing.T) {
	want := map[string]struct {
		on  []string
		out string
	}{
		"summarize":       {[]string{"message", "thread"}, "panel"},
		"translate":       {[]string{"message", "selection"}, "replace-body"},
		"translate-draft": {[]string{"draft"}, "replace-draft"},
		"tone-formal":     {[]string{"draft", "selection"}, "replace-draft"},
		"tone-friendly":   {[]string{"draft", "selection"}, "replace-draft"},
		"shorter":         {[]string{"draft", "selection"}, "replace-draft"},
		"longer":          {[]string{"draft", "selection"}, "replace-draft"},
		"proofread":       {[]string{"draft", "selection"}, "replace-draft"},
		"format":          {[]string{"draft", "selection"}, "replace-draft"},
	}
	got := skills.Builtins()
	require.Len(t, got, len(want))
	for _, s := range got {
		w, ok := want[s.Name]
		require.True(t, ok, s.Name)
		assert.True(t, s.Builtin)
		assert.Equal(t, w.on, s.On, s.Name)
		assert.Equal(t, w.out, s.Output, s.Name)
		assert.NotEmpty(t, s.Body, s.Name)
	}
	for _, s := range got {
		if s.Name == "translate" {
			assert.Contains(t, s.Body, "NO_TRANSLATION")
		}
	}
}

func TestSkillVersionChangesWithContent(t *testing.T) {
	a, _, _ := skills.Parse("demo", mapFS(map[string]string{"SKILL.md": good}))
	a2, _, _ := skills.Parse("demo", mapFS(map[string]string{"SKILL.md": good}))
	b, _, _ := skills.Parse("demo", mapFS(map[string]string{"SKILL.md": good + "more"}))
	assert.Equal(t, a.Version, a2.Version)
	assert.NotEqual(t, a.Version, b.Version)
}
