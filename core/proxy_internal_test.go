package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// The proxy password lives in the keychain, never in the settings row;
// saving other settings keeps it, and clearing it deletes the entry.
func TestProxyPasswordInKeychain(t *testing.T) {
	keyring.MockInit()
	ctx := t.Context()
	s, err := New(ctx, Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	defer s.Close()
	row := func() string { return queryStr(t, s, "SELECT value FROM settings WHERE key = 'app'") }
	entry := s.installID + "/proxy"

	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.ProxyUser, st.ProxyPassword = "alice", "pr0xy-pass"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	pw, err := keyring.Get(keyringService, entry)
	require.NoError(t, err)
	assert.Equal(t, "pr0xy-pass", pw)
	assert.NotContains(t, row(), "pr0xy-pass")
	assert.Contains(t, row(), "alice")

	st, err = s.GetSettings(ctx)
	require.NoError(t, err)
	assert.Empty(t, st.ProxyPassword, "never read back")
	st.Theme = "dark"
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	pw, err = keyring.Get(keyringService, entry)
	require.NoError(t, err)
	assert.Equal(t, "pr0xy-pass", pw, "another setting's save keeps the password")

	st.ProxyPasswordSet = false
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	_, err = keyring.Get(keyringService, entry)
	assert.ErrorIs(t, err, keyring.ErrNotFound)
}
