//go:build !windows

package plugins

import (
	"context"
	"io"
	"os"
	"os/exec"
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

// The protocol's stdout is not inherited by a helper the library starts: it
// ends when the host exits, not when the helper does.
func TestTakeStdoutNotInherited(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), takeStdoutEnv+"=1")
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	start := time.Now()
	b, err := io.ReadAll(out)
	require.NoError(t, err)
	require.NoError(t, cmd.Wait())
	assert.Equal(t, "done\n", string(b))
	assert.Less(t, time.Since(start), 2*time.Second, "EOF waited for the helper")
}

// A helper left holding the host's stderr cannot keep the process from
// counting as ended: a call returns, and nothing waits for the helper.
func TestNativeExitWithHelperHoldingStderr(t *testing.T) {
	n := NewNativeProcess(NativeOptions{Command: []string{"sh", "-c", "sleep 10 >/dev/null & exit 0"}, Timeout: 30 * time.Second})
	t.Cleanup(n.Close)
	start := time.Now()
	_, err := n.Call(context.Background(), "describe", struct{}{})
	assert.ErrorIs(t, err, ErrNativeExited)
	assert.Less(t, time.Since(start), 5*time.Second, "Wait waited for the helper")
}
