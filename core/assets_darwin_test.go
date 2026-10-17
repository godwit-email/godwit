package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// macOS refuses to load a quarantined library: such a file fails the check,
// and the flag is left for the user to see, not cleared.
func TestModelAssetsQuarantine(t *testing.T) {
	e := newAssetEnv(t)
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	lib := filepath.Join(dirs["engine"], "libllama")
	require.NoError(t, unix.Setxattr(lib, "com.apple.quarantine", []byte("0083;6ad356ea;Safari;"), 0))
	_, _, err = e.a.Verify(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, "verification_failed", core.ModelErrorCode(err))
	_, err = unix.Getxattr(lib, "com.apple.quarantine", nil)
	assert.NoError(t, err, "the flag is still there")
	_, err = os.Stat(lib)
	assert.NoError(t, err)
}
