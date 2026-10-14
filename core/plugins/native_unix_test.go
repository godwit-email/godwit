//go:build !windows

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A plugin's helper process ends with the plugin, also when it exits on its own.
func TestNativeGracefulExitEndsHelpers(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	lib, sha := fakeLib(t, map[string]any{"record": record})
	n := newFake(t, lib, sha, 0, nil)
	_, err := n.Call(context.Background(), "probe.spawn", struct{}{})
	require.NoError(t, err)
	b, err := os.ReadFile(record + ".helper")
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(b))
	require.NoError(t, err)
	n.Close() // stdin closes, the host exits by itself
	assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 50*time.Millisecond, "the helper is gone")
}
