package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/godwit-email/godwit/core/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishSkillDirDoesNotReplace(t *testing.T) {
	for _, kind := range []string{"empty directory", "nonempty directory", "file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			defer root.Close()
			require.NoError(t, root.Mkdir("staging", 0o700))
			require.NoError(t, root.WriteFile("staging/SKILL.md", []byte("new"), 0o600))
			if kind == "file" {
				require.NoError(t, root.WriteFile("target", []byte("old"), 0o600))
			} else {
				require.NoError(t, root.Mkdir("target", 0o700))
				if kind == "nonempty directory" {
					require.NoError(t, root.WriteFile("target/keep", []byte("old"), 0o600))
				}
			}
			before, err := root.Stat("target")
			require.NoError(t, err)
			require.Error(t, publishSkillDir(root, "staging", "target"))
			after, err := root.Stat("target")
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "既有空目录也不能被替换")
			require.FileExists(t, filepath.Join(dir, "staging", "SKILL.md"))
			if kind == "nonempty directory" {
				b, err := root.ReadFile("target/keep")
				require.NoError(t, err)
				assert.Equal(t, "old", string(b))
			}
		})
	}
}

func TestPublishSkillDirConcurrent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer root.Close()
	for _, name := range []string{"one", "two"} {
		require.NoError(t, root.Mkdir(name, 0o700))
		require.NoError(t, root.WriteFile(name+"/SKILL.md", []byte(name), 0o600))
	}
	done := make(chan error, 2)
	start := make(chan struct{})
	for _, name := range []string{"one", "two"} {
		go func() {
			<-start
			done <- publishSkillDir(root, name, "target")
		}()
	}
	close(start)
	first, second := <-done, <-done
	assert.NotEqual(t, first == nil, second == nil, "并发发布只允许一个成功")
	b, err := root.ReadFile("target/SKILL.md")
	require.NoError(t, err)
	assert.Contains(t, []string{"one", "two"}, string(b))
}

func TestPublishSkillDirSingleCharacter(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, root.Mkdir("staging", 0o700))
	require.NoError(t, root.WriteFile("staging/SKILL.md", []byte("new"), 0o600))
	require.NoError(t, publishSkillDir(root, "staging", "a"))
	b, err := root.ReadFile("a/SKILL.md")
	require.NoError(t, err)
	assert.Equal(t, "new", string(b))
}

func TestPublishSkillDirRejectsWindowsDeviceNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 保留设备名称")
	}
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, root.Mkdir("staging", 0o700))
	require.NoError(t, root.WriteFile("staging/SKILL.md", []byte("new"), 0o600))
	for _, name := range []string{"nul", "con", "com1", "lpt1"} {
		in := createSkillArgs{Name: name, Description: "Description", Instructions: "Instructions"}
		_, _, err := skillDefinition([]byte(createSkillCall(t, in).Args))
		require.ErrorContains(t, err, "reserved", "应在审批前拒绝设备名")
		require.Error(t, publishSkillDir(root, "staging", name), name)
	}
	entries, err := os.ReadDir(root.Name())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "staging", entries[0].Name())
}

func TestLoadSkillsIgnoresAbandonedStaging(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".tmp-interrupted"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tmp-interrupted", "SKILL.md"), []byte("partial"), 0o600))
	good, failed, err := skills.LoadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, good)
	assert.Empty(t, failed, "进程中断后残留的暂存目录不能污染可见 Skill 列表")
}
