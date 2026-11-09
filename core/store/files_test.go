package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/store"
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

// A file another process holds open for a moment, as a virus scanner does with
// one just written, still goes.
func TestRemoveAllOutlastsABriefHold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "parts")
	require.NoError(t, os.Mkdir(dir, 0o700))
	p := filepath.Join(dir, "a.pdf")
	require.NoError(t, os.WriteFile(p, []byte("pdf"), 0o600))
	f, err := os.Open(p) // on Windows without FILE_SHARE_DELETE: deleting fails while open
	require.NoError(t, err)
	time.AfterFunc(50*time.Millisecond, func() { f.Close() })
	require.NoError(t, store.RemoveAll(dir))
	require.NoDirExists(t, dir)
}
