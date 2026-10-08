package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwitmail/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	require.NoError(t, store.WriteFileAtomic(p, []byte("hello")))
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	ents, _ := os.ReadDir(dir)
	assert.Len(t, ents, 1)

	// 孤儿：残留的临时文件与 keep 返回 false 的文件都被清理
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "orphan"), []byte("x"), 0o600))
	require.NoError(t, store.CleanOrphans(dir, func(path string) bool { return filepath.Base(path) == "a.bin" }))
	ents, _ = os.ReadDir(dir)
	require.Len(t, ents, 1)
	assert.Equal(t, "a.bin", ents[0].Name())
}
