package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rename and every directory WriteFileAtomic creates must reach the disk:
// each parent directory whose entries changed is fsynced, after the change.
func TestWriteFileAtomicSyncsDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "f.eml")
	var synced []string
	old := syncDir
	syncDir = func(dir string) error {
		synced = append(synced, dir)
		if dir == filepath.Dir(path) {
			_, err := os.Stat(path)
			assert.NoError(t, err, "the directory is synced after the rename")
		}
		return old(dir)
	}
	t.Cleanup(func() { syncDir = old })

	require.NoError(t, WriteFileAtomic(path, []byte("x")))
	assert.Equal(t, []string{root, filepath.Join(root, "a"), filepath.Join(root, "a", "b")}, synced)

	synced = nil
	require.NoError(t, WriteFileAtomic(path, []byte("y")))
	assert.Equal(t, []string{filepath.Join(root, "a", "b")}, synced, "existing directories: only the rename's")
}
